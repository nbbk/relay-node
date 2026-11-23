package main

// v1.3.15：修面板自身 ACME 客户端的 kid 缺失 bug（真证书签发失败的根因）。
// 用户真机截图：重试签发后 ACME 下单失败 HTTP 400
// "Unable to validate JWS :: No Key ID in JWS header"。
// 根因：旧 post() 不返回响应头，newAccount 后拿不到 Location 头里的账户 URL，
// ac.kid 永远为空，后续请求全用 jwk 签名；RFC 8555 规定除注册外必须用 kid。
// 顺带修同一流程的第二个缺口：finalize 响应通常不带证书，必须轮询 newOrder
// 的 Location 头里的订单 URL 等证书（旧代码第一次迭代就 break，从不轮询）。
//
// 测试用本地 httptest 模拟 ACME 服务端，严格按 LE 的规则：
// newAccount 返回 Location；newOrder/authz/挑战/finalize/订单轮询/证书下载
// 用 jwk 签名返回 400 "No Key ID in JWS header"，用 kid 返回 200。

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- 假 ACME 服务端（按 LE 的规则行事） ----------

type mockACME145 struct {
	t   *testing.T
	srv *httptest.Server

	// 行为开关
	accountCode       int  // newAccount 返回的状态码（201 新注册 / 200 已有账户）
	noAccountLocation bool // newAccount 不返回 Location（模拟异常服务端）
	noOrderLocation   bool // newOrder 不返回 Location
	orderNeverValid   bool // 订单轮询永远 processing（测 30 次上限与报错）

	mu            sync.Mutex
	nonceN        int
	challengeDone bool
	orderPolls    int
	newOrderSeen  bool
	authzSeen     bool
	kidHits       map[string]int // kid 签名命中的端点
	jwkRejected   int            // 因 jwk 签名被 400 拒绝的次数
	certPEM       []byte
}

func newMockACME145(t *testing.T, accountCode int) *mockACME145 {
	t.Helper()
	m := &mockACME145{t: t, accountCode: accountCode, kidHits: map[string]int{}}
	// 假证书链：测试里现签一张自签证书当服务端要发的证书
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成假证书密钥失败：%v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("生成假证书失败：%v", err)
	}
	m.certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockACME145) url(p string) string { return m.srv.URL + p }

func (m *mockACME145) nonce(w http.ResponseWriter) {
	m.mu.Lock()
	m.nonceN++
	n := m.nonceN
	m.mu.Unlock()
	w.Header().Set("Replay-Nonce", fmt.Sprintf("test-nonce-%d", n))
}

// jwsAuth 解请求体的 JWS protected 头，返回（是否 kid 签名，是否 jwk 签名）。
func (m *mockACME145) jwsAuth(r *http.Request) (bool, bool) {
	b, _ := io.ReadAll(r.Body)
	var env struct {
		Protected string `json:"protected"`
	}
	if json.Unmarshal(b, &env) != nil || env.Protected == "" {
		return false, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(env.Protected)
	if err != nil {
		return false, false
	}
	var prot map[string]any
	if json.Unmarshal(raw, &prot) != nil {
		return false, false
	}
	_, hasKid := prot["kid"]
	_, hasJWK := prot["jwk"]
	return hasKid, hasJWK
}

// requireKid：非注册请求必须用 kid 签名；用 jwk 签名就按 LE 的原样 400 拒绝。
func (m *mockACME145) requireKid(w http.ResponseWriter, r *http.Request, endpoint string) bool {
	kid, jwk := m.jwsAuth(r)
	if kid && !jwk {
		m.mu.Lock()
		m.kidHits[endpoint]++
		m.mu.Unlock()
		return true
	}
	m.mu.Lock()
	m.jwkRejected++
	m.mu.Unlock()
	m.nonce(w)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(400)
	_, _ = w.Write([]byte(`{"type":"urn:ietf:params:acme:error:malformed","detail":"Unable to validate JWS :: No Key ID in JWS header","status":400}`))
	return false
}

func (m *mockACME145) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "GET" && r.URL.Path == "/directory":
		m.nonce(w)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"newNonce":   m.url("/new-nonce"),
			"newAccount": m.url("/new-acct"),
			"newOrder":   m.url("/new-order"),
		})
	case r.Method == "HEAD" && r.URL.Path == "/new-nonce":
		m.nonce(w)
		w.WriteHeader(200)
	case r.Method == "POST" && r.URL.Path == "/new-acct":
		// 注册请求用 jwk 签名是合法的（此时还没有 kid）。
		m.nonce(w)
		if !m.noAccountLocation {
			w.Header().Set("Location", m.url("/acct/1"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(m.accountCode)
		_, _ = w.Write([]byte(`{"status":"valid"}`))
	case r.Method == "POST" && r.URL.Path == "/new-order":
		m.mu.Lock()
		m.newOrderSeen = true
		m.mu.Unlock()
		if !m.requireKid(w, r, "new-order") {
			return
		}
		m.nonce(w)
		if !m.noOrderLocation {
			w.Header().Set("Location", m.url("/order/1"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorizations": []string{m.url("/authz/1")},
			"finalize":       m.url("/finalize"),
		})
	case r.Method == "POST" && r.URL.Path == "/authz/1":
		m.mu.Lock()
		m.authzSeen = true
		valid := m.challengeDone
		m.mu.Unlock()
		if !m.requireKid(w, r, "authz") {
			return
		}
		m.nonce(w)
		status := "pending"
		if valid {
			status = "valid"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": status,
			"challenges": []map[string]string{
				{"type": "dns-01", "url": m.url("/chall/1"), "token": "test-dns-token"},
			},
		})
	case r.Method == "POST" && r.URL.Path == "/chall/1":
		if !m.requireKid(w, r, "challenge") {
			return
		}
		m.mu.Lock()
		m.challengeDone = true
		m.mu.Unlock()
		m.nonce(w)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "dns-01", "status": "pending", "url": m.url("/chall/1"), "token": "test-dns-token"})
	case r.Method == "POST" && r.URL.Path == "/finalize":
		if !m.requireKid(w, r, "finalize") {
			return
		}
		m.nonce(w)
		w.Header().Set("Content-Type", "application/json")
		// 真实 LE 的 finalize 响应通常不带证书：只给 processing，等客户端轮询订单。
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "processing"})
	case r.Method == "POST" && r.URL.Path == "/order/1":
		if !m.requireKid(w, r, "order-poll") {
			return
		}
		m.mu.Lock()
		m.orderPolls++
		polls := m.orderPolls
		m.mu.Unlock()
		m.nonce(w)
		w.Header().Set("Content-Type", "application/json")
		if m.orderNeverValid {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "processing"})
			return
		}
		if polls < 3 {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "processing"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "valid", "certificate": m.url("/cert/1")})
	case r.Method == "POST" && r.URL.Path == "/cert/1":
		if !m.requireKid(w, r, "cert-download") {
			return
		}
		m.nonce(w)
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(m.certPEM)
	default:
		w.WriteHeader(404)
	}
}

// ---------- 测试脚手架 ----------

func newTestServer145(t *testing.T) *Server {
	t.Helper()
	t.Setenv("PANEL_INITIAL_PASSWORD", "test-password-123")
	s, err := newServer(Env{PanelHome: t.TempDir(), RealmDir: t.TempDir(), KitDir: t.TempDir(), Port: 11111})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return s
}

// driveObtain145 把 obtainACMECert 接到假 ACME 与假 Cloudflare 上，返回
// （Cloudflare TXT 的写调用，删除调用）。
func driveObtain145(t *testing.T, s *Server, m *mockACME145) (writes []string, deletes []string) {
	t.Helper()
	oldDir := acmeDirectoryURL
	acmeDirectoryURL = m.url("/directory")
	defer func() { acmeDirectoryURL = oldDir }()
	oldSleep := acmeSleep
	acmeSleep = func(time.Duration) {}
	defer func() { acmeSleep = oldSleep }()
	oldTXT := cfTXTFunc
	cfTXTFunc = func(s *Server, domain, value, deleteID string) (string, error) {
		if deleteID != "" {
			deletes = append(deletes, deleteID)
			return "", nil
		}
		writes = append(writes, value)
		return "fake-record-id", nil
	}
	defer func() { cfTXTFunc = oldTXT }()
	// v1.3.49：obtainACMECert 先查当前服务商凭证是否配齐（Cloudflare 默认），
	// 没配会先报错、不走 ACME 流程；测试里把 Token 配上。
	s.cfg.CloudflareToken = "cfut_testtoken145"
	if err := s.obtainACMECert("example.com"); err != nil {
		t.Fatalf("obtainACMECert 失败：%v", err)
	}
	return writes, deletes
}

// ---------- 1. kid 强制：假服务端按 LE 的规则拒绝 jwk ----------

func TestACMEPostRequiresKid145(t *testing.T) {
	m := newMockACME145(t, 201)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成测试密钥失败：%v", err)
	}
	ac := &acmeClient{http: m.srv.Client(), key: key, dirURL: m.url("/directory")}
	code, b, err := ac.get(ac.dirURL)
	if err != nil || code != 200 {
		t.Fatalf("拉目录失败：code=%d err=%v", code, err)
	}
	ac.dir = map[string]string{}
	if err := json.Unmarshal(b, &ac.dir); err != nil {
		t.Fatalf("目录解析失败：%v", err)
	}
	// 注册（jwk 签名）成功，且 post 必须把 Location 头带回来。
	code, _, hdr, err := ac.post(ac.dir["newAccount"], map[string]any{"termsOfServiceAgreed": true})
	if err != nil || code != 201 {
		t.Fatalf("注册失败：code=%d err=%v", code, err)
	}
	kid := hdr.Get("Location")
	if kid == "" {
		t.Fatal("post 没返回 Location 头：kid 拿不到")
	}
	if kid != m.url("/acct/1") {
		t.Fatalf("Location 不是账户地址：%s", kid)
	}
	// 拿到 kid 后下单成功。
	ac.kid = kid
	code, _, _, err = ac.post(ac.dir["newOrder"], map[string]any{"identifiers": []map[string]string{{"type": "dns", "value": "example.com"}}})
	if err != nil || code != 201 {
		t.Fatalf("kid 签名下单失败：code=%d err=%v", code, err)
	}
	// 回归断言：旧写法（kid 为空时 jwk 调 newOrder）必被 400 拒绝，
	// 错误就是用户真机上看到的那条。
	ac2 := &acmeClient{http: m.srv.Client(), key: key, dirURL: m.url("/directory"), dir: ac.dir}
	code, b, _, err = ac2.post(ac.dir["newOrder"], map[string]any{"identifiers": []map[string]string{{"type": "dns", "value": "example.com"}}})
	if err != nil {
		t.Fatalf("jwk 下单请求本身失败：%v", err)
	}
	if code != 400 {
		t.Fatalf("旧写法应被 400 拒绝，实际 HTTP %d：%s", code, string(b))
	}
	if !strings.Contains(string(b), "No Key ID in JWS header") {
		t.Fatalf("400 原因不是 No Key ID：%s", string(b))
	}
}

// ---------- 2. 全流程：注册→下单→授权→挑战→finalize→订单轮询→下载 ----------

func assertFullFlow145(t *testing.T, accountCode int) {
	t.Helper()
	m := newMockACME145(t, accountCode)
	s := newTestServer145(t)
	writes, deletes := driveObtain145(t, s, m)

	// 证书与私钥落盘，且证书就是假服务端发的，私钥能解析。
	certB, err := os.ReadFile(filepath.Join(s.env.PanelHome, "certs", "cert.pem"))
	if err != nil {
		t.Fatalf("cert.pem 没写下来：%v", err)
	}
	if string(certB) != string(m.certPEM) {
		t.Fatal("cert.pem 不是服务端发的证书")
	}
	keyB, err := os.ReadFile(filepath.Join(s.env.PanelHome, "certs", "key.pem"))
	if err != nil {
		t.Fatalf("key.pem 没写下来：%v", err)
	}
	blk, _ := pem.Decode(keyB)
	if blk == nil {
		t.Fatal("key.pem 不是 PEM")
	}
	if _, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err != nil {
		t.Fatalf("key.pem 解析失败：%v", err)
	}
	// 注册之后所有请求都必须走 kid：newOrder、authz、挑战、finalize、
	// 订单轮询、证书下载一个都不能少，且一次 jwk 拒绝都没有。
	for _, ep := range []string{"new-order", "authz", "challenge", "finalize", "order-poll", "cert-download"} {
		if m.kidHits[ep] == 0 {
			t.Fatalf("端点 %s 没收到 kid 签名请求", ep)
		}
	}
	if m.jwkRejected != 0 {
		t.Fatalf("不应有 jwk 签名被拒绝，实际 %d 次", m.jwkRejected)
	}
	// finalize 响应不带证书：必须靠轮询订单拿到证书，且轮询了 3 次
	//（前两次 processing，第三次 valid），不是只试一次。
	if m.orderPolls != 3 {
		t.Fatalf("订单轮询应为 3 次，实际 %d 次", m.orderPolls)
	}
	// DNS-01 的 TXT 写了一次（43 字符的 base64url SHA-256），事后删了一次。
	if len(writes) != 1 {
		t.Fatalf("TXT 应写 1 次，实际 %d 次", len(writes))
	}
	if len(writes[0]) != 43 {
		t.Fatalf("TXT 值应为 43 字符的 keyAuthorization 哈希，实际 %q", writes[0])
	}
	if len(deletes) != 1 || deletes[0] != "fake-record-id" {
		t.Fatalf("TXT 事后删除不对：%v", deletes)
	}
}

func TestObtainACMECertFullFlow145(t *testing.T) {
	assertFullFlow145(t, 201) // 新注册
}

func TestObtainACMECertReusedKeyReturns200(t *testing.T) {
	assertFullFlow145(t, 200) // 同一把持久化密钥重试注册时 LE 返回 200
}

// ---------- 3. Location 头缺失时直接报错，不许静默用 jwk 继续 ----------

func TestObtainACMECertMissingAccountLocation145(t *testing.T) {
	m := newMockACME145(t, 201)
	m.noAccountLocation = true
	s := newTestServer145(t)
	oldDir := acmeDirectoryURL
	acmeDirectoryURL = m.url("/directory")
	defer func() { acmeDirectoryURL = oldDir }()
	oldSleep := acmeSleep
	acmeSleep = func(time.Duration) {}
	defer func() { acmeSleep = oldSleep }()
	oldTXT := cfTXTFunc
	cfTXTFunc = func(s *Server, domain, value, deleteID string) (string, error) { return "fake-record-id", nil }
	defer func() { cfTXTFunc = oldTXT }()
	s.cfg.CloudflareToken = "cfut_testtoken145" // v1.3.49：obtainACMECert 先查凭证是否配齐

	err := s.obtainACMECert("example.com")
	if err == nil {
		t.Fatal("注册没返回 Location，应报错")
	}
	if !strings.Contains(err.Error(), "Location") {
		t.Fatalf("错误应明确说 Location 头缺失，实际：%v", err)
	}
	if m.newOrderSeen {
		t.Fatal("注册没拿到 kid 就不该继续下单")
	}
}

func TestObtainACMECertMissingOrderLocation145(t *testing.T) {
	m := newMockACME145(t, 201)
	m.noOrderLocation = true
	s := newTestServer145(t)
	oldDir := acmeDirectoryURL
	acmeDirectoryURL = m.url("/directory")
	defer func() { acmeDirectoryURL = oldDir }()
	oldSleep := acmeSleep
	acmeSleep = func(time.Duration) {}
	defer func() { acmeSleep = oldSleep }()
	oldTXT := cfTXTFunc
	cfTXTFunc = func(s *Server, domain, value, deleteID string) (string, error) { return "fake-record-id", nil }
	defer func() { cfTXTFunc = oldTXT }()
	s.cfg.CloudflareToken = "cfut_testtoken145" // v1.3.49：obtainACMECert 先查凭证是否配齐

	err := s.obtainACMECert("example.com")
	if err == nil {
		t.Fatal("下单没返回 Location，应报错")
	}
	if !strings.Contains(err.Error(), "Location") {
		t.Fatalf("错误应明确说 Location 头缺失，实际：%v", err)
	}
	if m.authzSeen {
		t.Fatal("下单没拿到订单地址就不该继续查授权")
	}
}

// ---------- 4. 订单 30 次都 processing：报“还没签好”，不是只试一次 ----------

func TestObtainACMECertOrderNeverValid145(t *testing.T) {
	m := newMockACME145(t, 201)
	m.orderNeverValid = true
	s := newTestServer145(t)
	oldDir := acmeDirectoryURL
	acmeDirectoryURL = m.url("/directory")
	defer func() { acmeDirectoryURL = oldDir }()
	oldSleep := acmeSleep
	acmeSleep = func(time.Duration) {}
	defer func() { acmeSleep = oldSleep }()
	oldTXT := cfTXTFunc
	cfTXTFunc = func(s *Server, domain, value, deleteID string) (string, error) { return "fake-record-id", nil }
	defer func() { cfTXTFunc = oldTXT }()
	s.cfg.CloudflareToken = "cfut_testtoken145" // v1.3.49：obtainACMECert 先查凭证是否配齐

	err := s.obtainACMECert("example.com")
	if err == nil {
		t.Fatal("订单一直 processing，应报错")
	}
	if !strings.Contains(err.Error(), "还没签好") {
		t.Fatalf("错误应说证书还没签好，实际：%v", err)
	}
	if m.orderPolls != 30 {
		t.Fatalf("订单轮询应跑满 30 次，实际 %d 次", m.orderPolls)
	}
}
