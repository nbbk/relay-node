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
	"log"
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

// ---------- 面板自身证书状态与自动续签（v1.3.14） ----------

// acmeDirectoryURL 是 ACME 目录地址，包级变量只为测试能指向本地假 ACME 服务端。
var acmeDirectoryURL = "https://acme-v02.api.letsencrypt.org/directory"

// acmeSleep 是 obtainACMECert 里的等待函数，包级变量只为测试能跳过
// DNS 传播等待与轮询间隔（默认就是 time.Sleep，流程本身不变）。
var acmeSleep = time.Sleep

// cfTXTFunc 是 Cloudflare TXT 记录的写/删函数，包级变量只为测试能替身
// （真机走 s.cloudflareTXT；测试里替换成记录调用的假函数）。
var cfTXTFunc = func(s *Server, domain, value, deleteID string) (string, error) {
	return s.cloudflareTXT(domain, value, deleteID)
}

// panelCertRenewDays 面板自身真证书剩余天数 ≤ 该值时自动续签（每天检查一次）。
const panelCertRenewDays = 30

// panelCertInfo 面板自身证书（certs/cert.pem）的状态。
type panelCertInfo struct {
	Exists     bool
	SelfSigned bool      // 自签证书（ensureSelfSigned 生成，Issuer == Subject）
	DaysLeft   int       // 剩余天数（与 fetchCert 同口径）
	Expires    time.Time // 到期时间
}

// panelCertInfo 读面板自身证书：不存在或解析失败返回 Exists=false。
func (s *Server) panelCertInfo() panelCertInfo {
	b, err := os.ReadFile(filepath.Join(s.env.PanelHome, "certs", "cert.pem"))
	if err != nil {
		return panelCertInfo{}
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return panelCertInfo{}
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return panelCertInfo{}
	}
	return panelCertInfo{
		Exists:     true,
		SelfSigned: bytes.Equal(cert.RawSubject, cert.RawIssuer),
		DaysLeft:   int(time.Until(cert.NotAfter).Hours() / 24),
		Expires:    cert.NotAfter,
	}
}

// shouldRenewPanelCert 面板自身真证书自动续签的纯决策（好测）：HTTPS 开着、
// 有域名、当前服务商的凭证配齐了、证书存在且是真证书、剩余 ≤30 天才续；
// 自签的不续（自签是签发失败的回落态，留给用户在 HTTPS 卡片点“重试签发”
// 手动处理）；手动模式不自动续签（要人加 TXT，providerConfigured 恒 false
// 之外——见 maybeRenewPanelCert，那里直接跳过 manual）。
func shouldRenewPanelCert(httpsEnabled bool, domain string, providerConfigured bool, info panelCertInfo) bool {
	if !httpsEnabled || domain == "" || !providerConfigured {
		return false
	}
	if !info.Exists || info.SelfSigned {
		return false
	}
	return info.DaysLeft <= panelCertRenewDays
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

// post 发一个带 JWS 签名的 ACME 请求，返回（状态码，响应体，响应头，错误）。
// 响应头必须返回：newAccount 的 Location 头里是账户 URL（kid），newOrder 的
// Location 头里是订单 URL；只看响应体拿不到这两样东西。
// 签名方式按 RFC 8555：kid 已拿到时用 kid 签名（除注册外的所有请求都必须用
// kid，用 jwk 会被 400 拒绝）；kid 为空时（只有注册请求）才用 jwk 签名。
func (a *acmeClient) post(url string, payload any) (int, []byte, http.Header, error) {
	nonce, err := a.getNonce()
	if err != nil {
		return 0, nil, nil, err
	}
	var payloadB64 string
	if payload == nil {
		payloadB64 = ""
	} else {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, nil, err
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
		return 0, nil, nil, err
	}
	body, _ := json.Marshal(map[string]string{"protected": b64url(protB), "payload": payloadB64, "signature": b64url(sig)})
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/jose+json")
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	if n := resp.Header.Get("Replay-Nonce"); n != "" {
		a.nonce = n
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header, nil
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
// truncateCFBody 把 Cloudflare 的错误响应体截断到 500 字符再拼进报错里：
// 响应体里才有真正原因，但整段贴会把 PanelCertLastErr 和前端 toast 撑爆。
func truncateCFBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if r := []rune(s); len(r) > 500 {
		s = string(r[:500]) + "…"
	}
	return s
}

// cleanupStaleChallengeTXT 删掉该 zone 里残留的 _acme-challenge.<域名> TXT
// 记录，再建新的（v1.3.48）：上次签发留下的同名记录不删，再建会报 400。
// 安全边界：只删名字与类型都完全对得上的记录，其它任何记录一律不动；
// 删除是 best-effort——查不到、删失败都只记日志，继续走建新记录的流程，
// 不因此报错（建记录失败时自有报错透出逻辑，会把真正原因报出来）。
func cleanupStaleChallengeTXT(token, zoneID, name string) {
	code, b, err := cfReq(token, "GET", "https://api.cloudflare.com/client/v4/zones/"+zoneID+"/dns_records?type=TXT&name="+name+"&per_page=100", nil)
	if err != nil || code >= 300 {
		log.Printf("清理残留的 ACME TXT 记录失败（跳过，继续建新记录）：%v", err)
		return
	}
	var list struct {
		Result []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, r := range list.Result {
		if r.Type != "TXT" || r.Name != name {
			continue
		}
		if code, _, err := cfReq(token, "DELETE", "https://api.cloudflare.com/client/v4/zones/"+zoneID+"/dns_records/"+r.ID, nil); err != nil || code >= 300 {
			log.Printf("删残留的 ACME TXT 记录 %s 失败（跳过，继续建新记录）：%v", r.ID, err)
		}
	}
}

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
	// 先清残留（v1.3.48）：上次签发留下的同名 TXT 记录不删，再建会报 400。
	cleanupStaleChallengeTXT(token, zoneID, name)
	code, b, err := cfReq(token, "POST", "https://api.cloudflare.com/client/v4/zones/"+zoneID+"/dns_records",
		map[string]any{"type": "TXT", "name": name, "content": value, "ttl": 60})
	if err != nil {
		return "", fmt.Errorf("Cloudflare 写 TXT 记录失败：%v", err)
	}
	if code >= 300 {
		// 响应体里才有真正原因（记录已存在、权限不足、TTL 不合法等），
		// 以前只报 HTTP 状态码，PanelCertLastErr 和前端 toast 都没法定位。
		// 再追加一句手动指引：自动清理是第一道，万一它没成功，用户按这句
		// 去 Cloudflare DNS 删残留记录再重试。
		return "", fmt.Errorf("Cloudflare 写 TXT 记录失败（HTTP %d）：%s。请到 Cloudflare DNS 删除残留的 _acme-challenge.%s TXT 记录后再重试签发", code, truncateCFBody(b), domain)
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

// cfReq 是包级变量只为测试能替身（默认就是调 Cloudflare API 的真实现）。
var cfReq = func(token, method, url string, payload any) (int, []byte, error) {
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

// ---------- ACME 订单分阶段（v1.3.49：手动模式两步走） ----------
//
// obtainACMECert 原先是一整段同步流程（下单→建 TXT→提交挑战→finalize）。
// 手动模式要分两步走：第一步下单并返回 TXT 值（用户去 DNS 后台加记录），
// 第二步用户点继续后才提交挑战、finalize、下证书。所以拆成三段：
// startACMEOrder（下单＋取各 authz 的 dns-01 挑战值）、
// submitAndPollChallenge（提交挑战＋轮询 authz）、
// finalizeACMEOrder（CSR＋finalize＋轮询订单＋下载证书）。
// API 服务商的流程是三段连着跑；手动模式第一步只跑 startACMEOrder，
// 把订单状态存进 pending 文件，第二步读回 pending 再跑后两段。

// acmeAuthzState 是一条 DNS-01 授权的状态。
type acmeAuthzState struct {
	AuthzURL     string
	ChallengeURL string
	Token        string
	TXTName      string // _acme-challenge.<domain>
	TXTValue     string // 43 字符的 keyAuthorization 哈希
}

// acmeOrderState 是一次 ACME 下单的完整状态（内存里用）。
type acmeOrderState struct {
	Domain      string
	OrderURL    string
	FinalizeURL string
	Authz       []acmeAuthzState
	CSRKey      *rsa.PrivateKey // finalize 用的 CSR 密钥（内存里）
	CSRKeyPEM   []byte          // 同上（PEM，存 pending 用）
}

// errACMEValidationFailed 是 DNS-01 验证失败的哨兵错误：验证一旦 invalid，
// 订单就作废了，手动模式的确认接口收到它要删 pending、让用户重新开始。
var errACMEValidationFailed = fmt.Errorf("ACME DNS-01 验证失败")

// loadOrCreateACMEKey 读/生成 ACME 账户密钥（certs/acme-key.pem，600 权限）。
func (s *Server) loadOrCreateACMEKey(dir string) (*rsa.PrivateKey, error) {
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
			return nil, err
		}
		_ = writeFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600)
	}
	return key, nil
}

// startACMEOrder 下单：拉目录、注册（拿 kid）、下单、取每个 authz 的
// dns-01 挑战并算出 TXT 值、生成 CSR 密钥。返回订单状态与 ACME 客户端。
func (s *Server) startACMEOrder(domain string) (*acmeOrderState, *acmeClient, error) {
	dir := filepath.Join(s.env.PanelHome, "certs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	key, err := s.loadOrCreateACMEKey(dir)
	if err != nil {
		return nil, nil, err
	}
	ac := &acmeClient{http: &http.Client{Timeout: 30 * time.Second}, key: key, dirURL: acmeDirectoryURL}
	code, b, err := ac.get(ac.dirURL)
	if err != nil {
		return nil, nil, fmt.Errorf("ACME 目录拉不到：%v", err)
	}
	if code != 200 {
		return nil, nil, fmt.Errorf("ACME 目录拉不到（HTTP %d）", code)
	}
	ac.dir = map[string]string{}
	_ = json.Unmarshal(b, &ac.dir)
	// newAccount：注册请求本身用 jwk 签名（此时还没有 kid）；成功后账户 URL
	// 在 Location 头里，之后除注册外的所有请求都必须用这个 kid 签名。
	code, b, hdr, err := ac.post(ac.dir["newAccount"], map[string]any{"termsOfServiceAgreed": true})
	if err != nil {
		return nil, nil, err
	}
	if code != 200 && code != 201 {
		return nil, nil, fmt.Errorf("ACME 注册失败（HTTP %d）：%s", code, string(b))
	}
	ac.kid = hdr.Get("Location")
	if ac.kid == "" {
		return nil, nil, fmt.Errorf("ACME 注册成功但没返回账户地址（Location 头为空），无法继续")
	}
	// newOrder：订单 URL 在 Location 头里，finalize 后要轮询它等证书。
	code, b, hdr, err = ac.post(ac.dir["newOrder"], map[string]any{"identifiers": []map[string]string{{"type": "dns", "value": domain}}})
	if err != nil || code >= 300 {
		return nil, nil, fmt.Errorf("ACME 下单失败（HTTP %d）：%s", code, string(b))
	}
	orderURL := hdr.Get("Location")
	if orderURL == "" {
		return nil, nil, fmt.Errorf("ACME 下单成功但没返回订单地址（Location 头为空），无法继续")
	}
	var order struct {
		Authorizations []string `json:"authorizations"`
		Finalize       string   `json:"finalize"`
	}
	if err := json.Unmarshal(b, &order); err != nil {
		return nil, nil, fmt.Errorf("ACME 订单解析失败")
	}
	thumb, err := ac.jwkThumbprint()
	if err != nil {
		return nil, nil, err
	}
	st := &acmeOrderState{Domain: domain, OrderURL: orderURL, FinalizeURL: order.Finalize}
	for _, authzURL := range order.Authorizations {
		code, b, _, err := ac.post(authzURL, nil)
		if err != nil || code >= 300 {
			return nil, nil, fmt.Errorf("ACME 授权查询失败")
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
			return nil, nil, fmt.Errorf("ACME 没给 dns-01 挑战")
		}
		keyAuth := dnsCh.Token + "." + thumb
		h := sha256.Sum256([]byte(keyAuth))
		st.Authz = append(st.Authz, acmeAuthzState{
			AuthzURL: authzURL, ChallengeURL: dnsCh.URL, Token: dnsCh.Token,
			TXTName: "_acme-challenge." + domain, TXTValue: b64url(h[:]),
		})
	}
	// CSR 密钥现在就生成（旧流程在挑战后才生成，效果一样）：手动模式要把
	// 它存进 pending，第二步 finalize 时用同一把密钥。
	csrKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	st.CSRKey = csrKey
	st.CSRKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(csrKey)})
	return st, ac, nil
}

// submitAndPollChallenge 提交一条 dns-01 挑战并轮询 authz（最多 30×5 秒）。
// manual=true 时报错文案按手动模式写（记录没加好/没生效＋订单作废）。
func submitAndPollChallenge(ac *acmeClient, az acmeAuthzState, manual bool) error {
	code, _, _, err := ac.post(az.ChallengeURL, map[string]any{})
	if err != nil || code >= 300 {
		return fmt.Errorf("ACME 挑战提交失败（HTTP %d）", code)
	}
	for i := 0; i < 30; i++ {
		acmeSleep(5 * time.Second)
		code, b, _, err := ac.post(az.AuthzURL, nil)
		if err != nil {
			continue
		}
		var st struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(b, &st)
		if st.Status == "valid" {
			return nil
		}
		if st.Status == "invalid" {
			if manual {
				return fmt.Errorf("%w：TXT 记录可能没加好或还没生效（请确认 %s 的 TXT 值与面板显示的一致）。本次订单已作废，请重新开始手动签发", errACMEValidationFailed, az.TXTName)
			}
			return fmt.Errorf("%w：TXT 记录可能没生效或 Token 权限不对", errACMEValidationFailed)
		}
		_ = code
	}
	// 30 次都没 valid 也不报错（旧行为）：交给 finalize 兜底。
	return nil
}

// finalizeACMEOrderTo 生成 CSR、finalize、轮询订单等证书、下载并写
// 到指定路径（证书 certPerm、私钥恒 600）。finalizeACMEOrder 是它
// 的面板版（写 certs/cert.pem／key.pem）；节点证书签发走同一段流程，
// 只换落盘路径（node-<ref>.crt／.key，证书也是 600）。
func (s *Server) finalizeACMEOrderTo(ac *acmeClient, st *acmeOrderState, certPath, keyPath string, certPerm os.FileMode) error {
	csrKey := st.CSRKey
	if csrKey == nil && len(st.CSRKeyPEM) > 0 {
		if blk, _ := pem.Decode(st.CSRKeyPEM); blk != nil {
			csrKey, _ = x509.ParsePKCS1PrivateKey(blk.Bytes)
		}
	}
	if csrKey == nil {
		return fmt.Errorf("CSR 密钥丢了，请重新签发")
	}
	csrTmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: st.Domain}, DNSNames: []string{st.Domain}}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTmpl, csrKey)
	if err != nil {
		return err
	}
	code, b, _, err := ac.post(st.FinalizeURL, map[string]any{"csr": b64url(csrDER)})
	if err != nil || code >= 300 {
		return fmt.Errorf("ACME finalize 失败（HTTP %d）", code)
	}
	// finalize 响应通常不带证书：轮询订单 URL（带 kid 的空体 POST，每 5 秒
	// 一次、最多 30 次），直到 status=valid 且 certificate 字段出现再下载。
	var certURL string
	for i := 0; i < 30; i++ {
		acmeSleep(5 * time.Second)
		code, b, _, err := ac.post(st.OrderURL, nil)
		if err != nil {
			continue
		}
		var o struct {
			Status      string `json:"status"`
			Certificate string `json:"certificate"`
		}
		_ = json.Unmarshal(b, &o)
		if o.Status == "valid" && o.Certificate != "" {
			certURL = o.Certificate
			break
		}
		if o.Status == "invalid" {
			return fmt.Errorf("ACME 订单无效：证书签发失败")
		}
		_ = code
	}
	if certURL == "" {
		return fmt.Errorf("ACME 证书还没签好（finalize 后 150 秒内订单未出证书），请稍后重试")
	}
	code, b, _, err = ac.post(certURL, nil)
	if err != nil || code >= 300 {
		return fmt.Errorf("ACME 下载证书失败（HTTP %d）", code)
	}
	if err := writeFileAtomic(certPath, b, certPerm); err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(csrKey)})
	return writeFileAtomic(keyPath, keyPEM, 0o600)
}

// finalizeACMEOrder 生成 CSR、finalize、轮询订单等证书、下载并写 certs/。
func (s *Server) finalizeACMEOrder(ac *acmeClient, st *acmeOrderState) error {
	dir := filepath.Join(s.env.PanelHome, "certs")
	return s.finalizeACMEOrderTo(ac, st, filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), 0o644)
}

// obtainACMECert 为 domain 申请 Let's Encrypt 证书（DNS-01），存到 certs/。
// 走配置选中的 DNS 服务商；手动模式不能自动签发（handleHTTPS 会提前拦，
// 这里是第二道防线）；凭证没配齐时先报错，不浪费一次 ACME 下单。
func (s *Server) obtainACMECert(domain string) error {
	provider := s.dnsProvider()
	if provider.ID() == "manual" {
		return fmt.Errorf("当前 DNS 服务商是手动模式，不能自动签发：请在设置页 HTTPS 卡片点“开始手动签发”，按两步走")
	}
	if !s.dnsProviderConfigured(provider.ID()) {
		return fmt.Errorf("请先在设置页 DNS 服务商卡片填写 %s 的凭证", dnsProviderName(provider.ID()))
	}
	st, ac, err := s.startACMEOrder(domain)
	if err != nil {
		return err
	}
	type cleanup struct {
		id, value string
	}
	var cleanups []cleanup
	defer func() {
		for _, c := range cleanups {
			_ = provider.Delete(s, domain, c.id, c.value)
		}
	}()
	for _, az := range st.Authz {
		recID, err := provider.Create(s, domain, az.TXTValue)
		if err != nil {
			return err
		}
		cleanups = append(cleanups, cleanup{recID, az.TXTValue})
		// 等 DNS 传播
		acmeSleep(10 * time.Second)
		if err := submitAndPollChallenge(ac, az, false); err != nil {
			return err
		}
	}
	return s.finalizeACMEOrder(ac, st)
}

// ---------- 手动模式 pending 订单（v1.3.49） ----------
//
// 手动模式分两步走：第一步下单后把订单状态落盘（$PANEL_HOME/certs/
// acme-pending.json，600 权限），面板重启后 pending 还在，不放内存；
// 第二步用户点继续后读回 pending、提交挑战、finalize、下证书。
// 订单 1 小时过期（与 ACME 订单本身的有效期同量级），过期作废。

// acmePendingTTL 是手动 pending 订单的有效期。
const acmePendingTTL = time.Hour

// acmePendingAuthz 是 pending 里一条授权的状态。
type acmePendingAuthz struct {
	AuthzURL     string `json:"authz_url"`
	ChallengeURL string `json:"challenge_url"`
	Token        string `json:"token"`
	TXTName      string `json:"txt_name"`
	TXTValue     string `json:"txt_value"`
}

// acmePendingOrder 是落盘的手动签发订单。Kind 区分归属（v1.4.12：
// "panel" 面板证书／"node" 节点证书；老文件没有 Kind 的按面板算），
// Ref 是节点证书的 ref（面板单没有）。pending 文件全局只有一个，
// 两边手动签发串行化（见 handleManualStart 与节点证书的手动接口）。
type acmePendingOrder struct {
	Version     int                `json:"version"`
	Kind        string             `json:"kind,omitempty"`
	Ref         int64              `json:"ref,omitempty"`
	Domain      string             `json:"domain"`
	OrderURL    string             `json:"order_url"`
	FinalizeURL string             `json:"finalize_url"`
	Authz       []acmePendingAuthz `json:"authz"`
	CSRKeyPEM   string             `json:"csr_key_pem"`
	CreatedAt   time.Time          `json:"created_at"`
	ExpiresAt   time.Time          `json:"expires_at"`
}

// pendingOrderPath 返回 pending 文件路径。
func (s *Server) pendingOrderPath() string {
	return filepath.Join(s.env.PanelHome, "certs", "acme-pending.json")
}

// savePendingOrder 把订单状态写盘（原子写，600 权限）。
func (s *Server) savePendingOrder(p *acmePendingOrder) error {
	dir := filepath.Join(s.env.PanelHome, "certs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.pendingOrderPath(), data, 0o600)
}

// loadPendingOrder 读回 pending（没有、解析失败、过期都返回错误）。
func (s *Server) loadPendingOrder() (*acmePendingOrder, error) {
	b, err := os.ReadFile(s.pendingOrderPath())
	if err != nil {
		return nil, fmt.Errorf("没有进行中的手动签发订单（请先点“开始手动签发”）")
	}
	var p acmePendingOrder
	if json.Unmarshal(b, &p) != nil {
		return nil, fmt.Errorf("手动签发订单损坏，请重新开始")
	}
	if p.Version != 1 || p.Domain == "" || p.OrderURL == "" || p.FinalizeURL == "" || len(p.Authz) == 0 {
		return nil, fmt.Errorf("手动签发订单不完整，请重新开始")
	}
	if time.Now().After(p.ExpiresAt) {
		return nil, fmt.Errorf("手动签发订单已过期（超过 1 小时），请重新开始")
	}
	return &p, nil
}

// clearPendingOrder 删 pending 文件（签发成功、订单作废、用户取消时调；
// 文件不存在也不报错）。
func (s *Server) clearPendingOrder() {
	_ = os.Remove(s.pendingOrderPath())
}

// pendingToState 把落盘的 pending 转成内存里的订单状态（finalize 用）。
func pendingToState(p *acmePendingOrder) *acmeOrderState {
	st := &acmeOrderState{
		Domain: p.Domain, OrderURL: p.OrderURL, FinalizeURL: p.FinalizeURL,
		CSRKeyPEM: []byte(p.CSRKeyPEM),
	}
	for _, a := range p.Authz {
		st.Authz = append(st.Authz, acmeAuthzState{
			AuthzURL: a.AuthzURL, ChallengeURL: a.ChallengeURL, Token: a.Token,
			TXTName: a.TXTName, TXTValue: a.TXTValue,
		})
	}
	return st
}

// resumeACMEClient 恢复 ACME 客户端（手动模式第二步用）：账户密钥存在
// acme-key.pem 里，同一把密钥重新注册会拿到同一个账户地址（kid）。
func (s *Server) resumeACMEClient() (*acmeClient, error) {
	dir := filepath.Join(s.env.PanelHome, "certs")
	key, err := s.loadOrCreateACMEKey(dir)
	if err != nil {
		return nil, err
	}
	ac := &acmeClient{http: &http.Client{Timeout: 30 * time.Second}, key: key, dirURL: acmeDirectoryURL}
	code, b, err := ac.get(ac.dirURL)
	if err != nil {
		return nil, fmt.Errorf("ACME 目录拉不到：%v", err)
	}
	if code != 200 {
		return nil, fmt.Errorf("ACME 目录拉不到（HTTP %d）", code)
	}
	ac.dir = map[string]string{}
	_ = json.Unmarshal(b, &ac.dir)
	code, b, hdr, err := ac.post(ac.dir["newAccount"], map[string]any{"termsOfServiceAgreed": true})
	if err != nil {
		return nil, err
	}
	if code != 200 && code != 201 {
		return nil, fmt.Errorf("ACME 注册失败（HTTP %d）：%s", code, string(b))
	}
	ac.kid = hdr.Get("Location")
	if ac.kid == "" {
		return nil, fmt.Errorf("ACME 注册成功但没返回账户地址（Location 头为空），无法继续")
	}
	return ac, nil
}

// acmeLookupTXT 是 net.LookupTXT 的包级替身（测试用）。
var acmeLookupTXT = func(name string) ([]string, error) {
	return net.LookupTXT(name)
}

// waitForTXTVisible 等 TXT 记录生效（手动模式第二步提交挑战之前）：
// 先查面板本机的 DNS 能不能看到这条 TXT（最多等 2 分钟），看不到就报错
// 但不提交挑战——pending 还能用，用户等几分钟再点继续验证。
// 注意：这只是启发式检查（面板的解析器与 Let's Encrypt 的不一定相同），
// 能看到大概率能过，看不到则一定别急着提交（提交失败订单就作废了）。
func waitForTXTVisible(name, value string) error {
	for i := 0; i < 24; i++ {
		vals, err := acmeLookupTXT(name)
		if err == nil {
			for _, v := range vals {
				if v == value {
					return nil
				}
			}
		}
		acmeSleep(5 * time.Second)
	}
	return fmt.Errorf("TXT 记录还没生效：面板的 DNS 还查不到 %s（已等 2 分钟），请确认记录加对了、等几分钟再点“继续验证”", name)
}
