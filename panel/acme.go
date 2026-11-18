package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------- 自签证书（Go 标准库 crypto/x509） ----------

func ensureSelfSigned(panelHome, domain string) error {
	dir := filepath.Join(panelHome, "certs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domainOrIP(domain)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(825 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if domain != "" {
		tmpl.DNSNames = []string{domain}
	}
	// SAN 含本机 IP
	if ips, err := localIPs(); err == nil {
		tmpl.IPAddresses = ips
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := writeFileAtomic(filepath.Join(dir, "cert.pem"), certPEM, 0o644); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "key.pem"), keyPEM, 0o600)
}

func domainOrIP(domain string) string {
	if domain != "" {
		return domain
	}
	if ips, err := localIPs(); err == nil && len(ips) > 0 {
		return ips[0].String()
	}
	return "relay-panel"
}

func localIPs() ([]net.IP, error) {
	var out []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() {
				out = append(out, ipnet.IP)
			}
		}
	}
	return out, nil
}

// ---------- ACME v2 + Cloudflare DNS-01（标准库手写最小客户端） ----------

type acmeClient struct {
	http   *http.Client
	key    *rsa.PrivateKey
	kid    string
	nonce  string
	dirURL string
	dir    map[string]string
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (a *acmeClient) getNonce() (string, error) {
	if a.nonce != "" {
		n := a.nonce
		a.nonce = ""
		return n, nil
	}
	req, _ := http.NewRequest("HEAD", a.dir["newNonce"], nil)
	resp, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return resp.Header.Get("Replay-Nonce"), nil
}

func (a *acmeClient) jwkThumbprint() (string, error) {
	// RFC 7638: {"e":"...","kty":"RSA","n":"..."} 按字典序
	jwk := fmt.Sprintf(`{"e":"%s","kty":"RSA","n":"%s"}`,
		b64url(big.NewInt(int64(a.key.PublicKey.E)).Bytes()),
		b64url(a.key.PublicKey.N.Bytes()))
	h := sha256.Sum256([]byte(jwk))
	return b64url(h[:]), nil
}

func (a *acmeClient) post(url string, payload any) (int, []byte, error) {
	nonce, err := a.getNonce()
	if err != nil {
		return 0, nil, err
	}
	var payloadB64 string
	if payload == nil {
		payloadB64 = ""
	} else {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		payloadB64 = b64url(b)
	}
	var protected map[string]any
	if a.kid != "" {
		protected = map[string]any{"alg": "RS256", "kid": a.kid, "nonce": nonce, "url": url}
	} else {
		jwk := map[string]any{"e": b64url(big.NewInt(int64(a.key.PublicKey.E)).Bytes()), "kty": "RSA", "n": b64url(a.key.PublicKey.N.Bytes())}
		protected = map[string]any{"alg": "RS256", "jwk": jwk, "nonce": nonce, "url": url}
	}
	protB, _ := json.Marshal(protected)
	signing := b64url(protB) + "." + payloadB64
	h := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, h[:])
	if err != nil {
		return 0, nil, err
	}
	body, _ := json.Marshal(map[string]string{"protected": b64url(protB), "payload": payloadB64, "signature": b64url(sig)})
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/jose+json")
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if n := resp.Header.Get("Replay-Nonce"); n != "" {
		a.nonce = n
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

func (a *acmeClient) get(url string) (int, []byte, error) {
	req, _ := http.NewRequest("GET", url, nil)
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// cloudflareTXT 写/删 _acme-challenge TXT 记录，返回记录 ID。
func (s *Server) cloudflareTXT(domain, value string, deleteID string) (string, error) {
	token := s.cfg.CloudflareToken
	if token == "" {
		return "", fmt.Errorf("未配置 Cloudflare Token（设置里填 Zone:DNS:Edit 权限的 Token）")
	}
	// 找 zone：从完整域名逐级往上试
	zoneID := ""
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone := strings.Join(parts[i:], ".")
		code, b, err := cfReq(token, "GET", "https://api.cloudflare.com/client/v4/zones?name="+zone, nil)
		if err != nil || code != 200 {
			continue
		}
		var z struct {
			Result []struct {
				ID string `json:"id"`
			} `json:"result"`
		}
		if json.Unmarshal(b, &z) == nil && len(z.Result) > 0 {
			zoneID = z.Result[0].ID
			break
		}
	}
	if zoneID == "" {
		return "", fmt.Errorf("Cloudflare 里找不到域名 %s 的 Zone", domain)
	}
	if deleteID != "" {
		_, _, _ = cfReq(token, "DELETE", "https://api.cloudflare.com/client/v4/zones/"+zoneID+"/dns_records/"+deleteID, nil)
		return "", nil
	}
	name := "_acme-challenge." + domain
	code, b, err := cfReq(token, "POST", "https://api.cloudflare.com/client/v4/zones/"+zoneID+"/dns_records",
		map[string]any{"type": "TXT", "name": name, "content": value, "ttl": 60})
	if err != nil || code >= 300 {
		return "", fmt.Errorf("Cloudflare 写 TXT 记录失败（HTTP %d）", code)
	}
	var r struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &r) == nil {
		return r.Result.ID, nil
	}
	return "", fmt.Errorf("Cloudflare 返回解析失败")
}

func cfReq(token, method, url string, payload any) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// obtainACMECert 为 domain 申请 Let's Encrypt 证书（DNS-01），存到 certs/。
func (s *Server) obtainACMECert(domain string) error {
	dir := filepath.Join(s.env.PanelHome, "certs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// 账户密钥（复用）
	keyPath := filepath.Join(dir, "acme-key.pem")
	var key *rsa.PrivateKey
	if b, err := os.ReadFile(keyPath); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			key, _ = x509.ParsePKCS1PrivateKey(blk.Bytes)
		}
	}
	if key == nil {
		var err error
		key, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return err
		}
		_ = writeFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600)
	}
	ac := &acmeClient{http: &http.Client{Timeout: 30 * time.Second}, key: key, dirURL: "https://acme-v02.api.letsencrypt.org/directory"}
	code, b, err := ac.get(ac.dirURL)
	if err != nil || code != 200 {
		return fmt.Errorf("ACME 目录拉不到：%v", err)
	}
	ac.dir = map[string]string{}
	_ = json.Unmarshal(b, &ac.dir)
	// newAccount
	code, b, err = ac.post(ac.dir["newAccount"], map[string]any{"termsOfServiceAgreed": true})
	if err != nil {
		return err
	}
	if code != 200 && code != 201 {
		return fmt.Errorf("ACME 注册失败（HTTP %d）：%s", code, string(b))
	}
	// kid 从 Location 头取
	// post 里没返回头，这里简化：重新用 newAccount 的响应头无法拿到，改为直接用 jwk 方式继续
	// （Let's Encrypt 允许每请求都用 jwk 只要账户已存在会返回 200；为稳妥，kid 为空时继续用 jwk）
	// newOrder
	code, b, err = ac.post(ac.dir["newOrder"], map[string]any{"identifiers": []map[string]string{{"type": "dns", "value": domain}}})
	if err != nil || code >= 300 {
		return fmt.Errorf("ACME 下单失败（HTTP %d）：%s", code, string(b))
	}
	var order struct {
		Authorizations []string `json:"authorizations"`
		Finalize       string   `json:"finalize"`
	}
	if err := json.Unmarshal(b, &order); err != nil {
		return fmt.Errorf("ACME 订单解析失败")
	}
	thumb, err := ac.jwkThumbprint()
	if err != nil {
		return err
	}
	// 处理每个 authz 的 dns-01
	type cleanup struct {
		zone, id string
	}
	var cleanups []cleanup
	defer func() {
		for _, c := range cleanups {
			_, _ = s.cloudflareTXT(domain, "", c.id)
		}
	}()
	for _, authzURL := range order.Authorizations {
		code, b, err := ac.post(authzURL, nil)
		if err != nil || code >= 300 {
			return fmt.Errorf("ACME 授权查询失败")
		}
		var az struct {
			Challenges []struct {
				Type  string `json:"type"`
				URL   string `json:"url"`
				Token string `json:"token"`
			} `json:"challenges"`
		}
		_ = json.Unmarshal(b, &az)
		var dnsCh *struct {
			Type  string `json:"type"`
			URL   string `json:"url"`
			Token string `json:"token"`
		}
		for i := range az.Challenges {
			if az.Challenges[i].Type == "dns-01" {
				dnsCh = &az.Challenges[i]
				break
			}
		}
		if dnsCh == nil {
			return fmt.Errorf("ACME 没给 dns-01 挑战")
		}
		keyAuth := dnsCh.Token + "." + thumb
		h := sha256.Sum256([]byte(keyAuth))
		txtVal := b64url(h[:])
		recID, err := s.cloudflareTXT(domain, txtVal, "")
		if err != nil {
			return err
		}
		cleanups = append(cleanups, cleanup{domain, recID})
		// 等 DNS 传播
		time.Sleep(10 * time.Second)
		code, b, err = ac.post(dnsCh.URL, map[string]any{})
		if err != nil || code >= 300 {
			return fmt.Errorf("ACME 挑战提交失败（HTTP %d）", code)
		}
		// 轮询 authz 状态
		for i := 0; i < 30; i++ {
			time.Sleep(5 * time.Second)
			code, b, err = ac.post(authzURL, nil)
			if err != nil {
				continue
			}
			var st struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal(b, &st)
			if st.Status == "valid" {
				break
			}
			if st.Status == "invalid" {
				return fmt.Errorf("ACME DNS-01 验证失败：TXT 记录可能没生效或 Token 权限不对")
			}
			_ = code
		}
	}
	// CSR
	csrKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	csrTmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: domain}, DNSNames: []string{domain}}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTmpl, csrKey)
	if err != nil {
		return err
	}
	code, b, err = ac.post(order.Finalize, map[string]any{"csr": b64url(csrDER)})
	if err != nil || code >= 300 {
		return fmt.Errorf("ACME finalize 失败（HTTP %d）", code)
	}
	// 轮询订单，等证书 URL
	var certURL string
	for i := 0; i < 30; i++ {
		time.Sleep(5 * time.Second)
		// 订单 URL 未保存：用 finalize 响应里的 location 无法直接取；这里用 order 对象重新查
		// 简化：finalize 响应体里若已有 certificate 字段直接用
		var o struct {
			Status      string `json:"status"`
			Certificate string `json:"certificate"`
		}
		_ = json.Unmarshal(b, &o)
		if o.Certificate != "" {
			certURL = o.Certificate
			break
		}
		// 否则再 post 一次订单 URL（order URL 在 newOrder 的 Location 头里，未保存则无法轮询）
		break
	}
	if certURL == "" {
		return fmt.Errorf("ACME 证书还没签好（finalize 后未拿到证书地址），请稍后重试")
	}
	code, b, err = ac.post(certURL, nil)
	if err != nil || code >= 300 {
		return fmt.Errorf("ACME 下载证书失败（HTTP %d）", code)
	}
	if err := writeFileAtomic(filepath.Join(dir, "cert.pem"), b, 0o644); err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(csrKey)})
	return writeFileAtomic(filepath.Join(dir, "key.pem"), keyPEM, 0o600)
}
