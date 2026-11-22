package main

// ---------- 1.3.45：客户端账号密码登录 ----------

import (
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupGenericSub1345 给测试面板生成一条通用订阅，返回令牌明文。
func setupGenericSub1345(t *testing.T, s *Server) string {
	t.Helper()
	tok := generateSubToken()
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.SubTokenHash = hashSubToken(tok)
	s.cfg.SubTokenEnc = enc
	return tok
}

// clientLogin1345 直接调登录接口，返回状态码与响应体。
func clientLogin1345(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/client/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.clientLoginMux().ServeHTTP(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// TestClientLoginSuccess1345 正确密码登录成功：200、sess_ 令牌（64 位十六进制）、
// 用户名回显、通用订阅链接、expires_at 约 30 天后；令牌只存哈希不存明文。
func TestClientLoginSuccess1345(t *testing.T) {
	s := newTestServer144(t)
	genTok := setupGenericSub1345(t, s)
	code, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if code != 200 {
		t.Fatalf("正确密码应 200，实际 %d：%v", code, resp)
	}
	tok, _ := resp["token"].(string)
	if !strings.HasPrefix(tok, "sess_") || len(tok) != 5+64 {
		t.Fatalf("令牌应为 sess_+64 位十六进制，实际 %q", tok)
	}
	if _, err := hex.DecodeString(tok[5:]); err != nil {
		t.Fatalf("令牌后 64 位应为十六进制：%v", err)
	}
	if resp["username"] != "admin" {
		t.Fatalf("用户名应回显 admin，实际 %v", resp["username"])
	}
	u, _ := resp["subscription_url"].(string)
	if !strings.Contains(u, "/"+s.subPath()+"/"+genTok) {
		t.Fatalf("订阅链接应含通用订阅令牌，实际 %q", u)
	}
	exp, _ := resp["expires_at"].(float64)
	want := time.Now().Add(clientSessionTTL).Unix()
	if exp < float64(want-60) || exp > float64(want+60) {
		t.Fatalf("expires_at 应约 30 天后，实际 %v", exp)
	}
	// 只存哈希：库里有哈希、没有明文。
	s.clientSessions.mu.Lock()
	defer s.clientSessions.mu.Unlock()
	if _, ok := s.clientSessions.sessions[tok]; ok {
		t.Fatal("会话表里存了令牌明文，必须只存哈希")
	}
	if _, ok := s.clientSessions.sessions[hashClientSessionToken(tok)]; !ok {
		t.Fatal("会话表里没有令牌哈希")
	}
}

// TestClientLoginWrongPassword1345 密码错与账号不存在都返回同一个 401，
// 不给枚举账号的口子。
func TestClientLoginWrongPassword1345(t *testing.T) {
	s := newTestServer144(t)
	code, resp := clientLogin1345(t, s, `{"username":"admin","password":"wrong"}`)
	if code != 401 || resp["error"] != "账号或密码不对" {
		t.Fatalf("密码错应 401 账号或密码不对，实际 %d：%v", code, resp)
	}
	code, resp = clientLogin1345(t, s, `{"username":"nobody","password":"test-password-123"}`)
	if code != 401 || resp["error"] != "账号或密码不对" {
		t.Fatalf("账号不存在应同样 401，实际 %d：%v", code, resp)
	}
}

// TestClientLoginEmptyFields1345 缺字段 400；多余字段也 400（decodeJSON 严格校验）。
func TestClientLoginEmptyFields1345(t *testing.T) {
	s := newTestServer144(t)
	for _, body := range []string{
		`{}`,
		`{"username":"admin"}`,
		`{"username":"admin","password":""}`,
		`{"username":"admin","password":"test-password-123","code":"123456"}`,
	} {
		code, _ := clientLogin1345(t, s, body)
		if code != 400 {
			t.Fatalf("请求 %s 应 400，实际 %d", body, code)
		}
	}
}

// TestClientLoginNoSubscription1345 没生成过通用订阅：登录成功但不给 subscription_url。
func TestClientLoginNoSubscription1345(t *testing.T) {
	s := newTestServer144(t)
	code, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if code != 200 {
		t.Fatalf("应 200，实际 %d：%v", code, resp)
	}
	if _, ok := resp["subscription_url"]; ok {
		t.Fatalf("没有通用订阅时不应给 subscription_url，实际 %v", resp["subscription_url"])
	}
}

// TestClientLoginLegacySub1345 只有旧链接（有哈希无密文，1.3.7 之前）：
// 登录成功但不给 subscription_url（解密不了，不编链接）。
func TestClientLoginLegacySub1345(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.SubTokenHash = "deadbeef"
	s.cfg.SubTokenEnc = ""
	code, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if code != 200 {
		t.Fatalf("应 200，实际 %d：%v", code, resp)
	}
	if _, ok := resp["subscription_url"]; ok {
		t.Fatalf("旧链接无法解密时不应给 subscription_url，实际 %v", resp["subscription_url"])
	}
}

// TestClientLoginLockout1345 防刷复用管理登录同一套逻辑：同一 IP 5 次失败
// 锁 5 分钟，第 6 次起 429 + Retry-After；锁定期内正确密码也 429；
// 管理登录同样被锁（同一张计数表）。
func TestClientLoginLockout1345(t *testing.T) {
	s := newTestServer144(t)
	for i := 0; i < 5; i++ {
		code, _ := clientLogin1345(t, s, `{"username":"admin","password":"wrong"}`)
		if code != 401 {
			t.Fatalf("第 %d 次失败应 401，实际 %d", i+1, code)
		}
	}
	code, resp := clientLogin1345(t, s, `{"username":"admin","password":"wrong"}`)
	if code != 429 {
		t.Fatalf("第 6 次应 429，实际 %d：%v", code, resp)
	}
	// Retry-After 头（httptest 的 Recorder 能读到响应头）。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/client/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	s.clientLoginMux().ServeHTTP(rec, req)
	ra := rec.Header().Get("Retry-After")
	if ra == "" {
		t.Fatal("429 应带 Retry-After 头")
	}
	var secs int
	if _, err := fmt.Sscanf(ra, "%d", &secs); err != nil || secs <= 0 || secs > 300 {
		t.Fatalf("Retry-After 应为 1–300 秒，实际 %q", ra)
	}
	// 锁定期内正确密码也 429。
	code, _ = clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if code != 429 {
		t.Fatalf("锁定期内正确密码也应 429，实际 %d", code)
	}
	// 管理登录同样被锁（同一张计数表）。
	req = httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
	rec = httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != 429 {
		t.Fatalf("客户端登录锁住后管理登录也应 429，实际 %d", rec.Code)
	}
}

// TestClientSessionValidate1345 会话校验：正常通过、错令牌不通过、
// 过期惰性失效、吊销单个、按账号吊销全部。
func TestClientSessionValidate1345(t *testing.T) {
	s := newTestServer144(t)
	tok, _ := s.clientSessions.create("admin")
	if u, ok := s.clientSessions.validate(tok); !ok || u != "admin" {
		t.Fatalf("刚建的会话应校验通过，实际 %q %v", u, ok)
	}
	if _, ok := s.clientSessions.validate("sess_0000000000000000000000000000000000000000000000000000000000000000"); ok {
		t.Fatal("没发过的令牌不应校验通过")
	}
	// 过期：手动把过期时间拨到过去，校验应失败且条目被清掉。
	s.clientSessions.mu.Lock()
	s.clientSessions.sessions[hashClientSessionToken(tok)].expiresAt = time.Now().Add(-time.Second)
	s.clientSessions.mu.Unlock()
	if _, ok := s.clientSessions.validate(tok); ok {
		t.Fatal("过期会话不应校验通过")
	}
	s.clientSessions.mu.Lock()
	_, stillThere := s.clientSessions.sessions[hashClientSessionToken(tok)]
	s.clientSessions.mu.Unlock()
	if stillThere {
		t.Fatal("过期会话校验时应惰性清掉")
	}
	// 吊销单个。
	tok2, _ := s.clientSessions.create("admin")
	s.clientSessions.revoke(tok2)
	if _, ok := s.clientSessions.validate(tok2); ok {
		t.Fatal("吊销的会话不应校验通过")
	}
	// 按账号吊销全部：另一个账号的会话不受影响。
	tokA, _ := s.clientSessions.create("admin")
	tokB, _ := s.clientSessions.create("other")
	s.clientSessions.revokeAllForUser("admin")
	if _, ok := s.clientSessions.validate(tokA); ok {
		t.Fatal("按账号吊销后该账号会话不应通过")
	}
	if _, ok := s.clientSessions.validate(tokB); !ok {
		t.Fatal("按账号吊销不应影响其它账号的会话")
	}
}

// TestClientLoginMux1345 登录监听的 mux 只挂 /api/v1/client/*：
// 管理接口在上面一律 404；主 mux 上没有客户端登录接口；
// sess_ 令牌在管理接口上无效（仍只认管理员登录会话）。
func TestClientLoginMux1345(t *testing.T) {
	s := newTestServer144(t)
	mux := s.clientLoginMux()
	for _, p := range []string{"/api/v1/subscriptions/link", "/api/v1/login", "/api/v1/status", "/"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 404 {
			t.Fatalf("登录监听上 %s 应 404，实际 %d", p, rec.Code)
		}
	}
	// GET 方法调登录接口也 404（只挂了 POST）。
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/client/login", nil))
	if rec.Code != 404 {
		t.Fatalf("GET 调登录接口应 404，实际 %d", rec.Code)
	}
	// 主 mux 上没有客户端登录接口：POST 落到前端 catch-all（handleRoot），
	// 返回的是 HTML 页面、不是登录 JSON（无 token）。
	rec = httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/client/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`)))
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") || strings.Contains(rec.Body.String(), "sess_") {
		t.Fatalf("主 mux 上 POST /api/v1/client/login 应返回前端页面而不是登录响应，实际 Content-Type=%q body 前 200 字=%q",
			rec.Header().Get("Content-Type"), rec.Body.String()[:min(200, len(rec.Body.String()))])
	}
	// sess_ 令牌在管理接口上无效：带 Bearer 头、不带管理员 cookie。
	tok, _ := s.clientSessions.create("admin")
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/subscriptions/link", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.routes().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("sess_ 令牌调管理接口应 401，实际 %d", rec.Code)
	}
}

// TestClientSubURL1345 订阅链接口径：对外域名优先；没填时用主端口
// （不是登录监听的 9876 端口），scheme 跟主端口的 HTTPS 开关。
func TestClientSubURL1345(t *testing.T) {
	s := newTestServer144(t)
	genTok := setupGenericSub1345(t, s)
	req := httptest.NewRequest("POST", "/api/v1/client/login", nil)
	req.Host = "panel.example.com:9876"
	// 对外域名：https://域名/路径/令牌。
	s.cfg.SubPublicDomain = "cc.example.com"
	if got := s.clientSubscriptionURL(req); got != "https://cc.example.com/"+s.subPath()+"/"+genTok {
		t.Fatalf("对外域名时订阅链接应为 https://cc.example.com/路径/令牌，实际 %q", got)
	}
	// 没对外域名、主端口开 HTTPS：https://主端口。
	s.cfg.SubPublicDomain = ""
	s.cfg.HTTPSEnabled = true
	if got := s.clientSubscriptionURL(req); got != "https://panel.example.com:11111/"+s.subPath()+"/"+genTok {
		t.Fatalf("主端口 HTTPS 时订阅链接应走主端口，实际 %q", got)
	}
	// 没对外域名、主端口 HTTP：http://主端口。
	s.cfg.HTTPSEnabled = false
	if got := s.clientSubscriptionURL(req); got != "http://panel.example.com:11111/"+s.subPath()+"/"+genTok {
		t.Fatalf("主端口 HTTP 时订阅链接应为 http，实际 %q", got)
	}
}

// TestClientLoginTOTP1345 二次验证开着时客户端登录直接 401（客户端发不了
// 验证码，不给密码单因素绕过二次验证的口子）。
func TestClientLoginTOTP1345(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.TOTPEnabled = true
	code, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if code != 401 || resp["error"] != "账号或密码不对" {
		t.Fatalf("二次验证开着时应 401 且不透露原因，实际 %d：%v", code, resp)
	}
}

// TestRequireClientAuth1345 客户端鉴权中间件：没令牌、错令牌 401；
// 有效令牌放行且账号名进 context。
func TestRequireClientAuth1345(t *testing.T) {
	s := newTestServer144(t)
	probe := s.requireClientAuth(func(w http.ResponseWriter, r *http.Request) {
		u, _ := r.Context().Value(clientAuthUsernameKey{}).(string)
		writeJSON(w, 200, map[string]any{"ok": true, "username": u})
	})
	// 没令牌。
	rec := httptest.NewRecorder()
	probe.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/client/x", nil))
	if rec.Code != 401 {
		t.Fatalf("没令牌应 401，实际 %d", rec.Code)
	}
	// 错令牌。
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/client/x", nil)
	req.Header.Set("Authorization", "Bearer sess_0000000000000000000000000000000000000000000000000000000000000000")
	probe.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("错令牌应 401，实际 %d", rec.Code)
	}
	// 有效令牌。
	tok, _ := s.clientSessions.create("admin")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/client/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	probe.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("有效令牌应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["username"] != "admin" {
		t.Fatalf("context 里账号名应为 admin，实际 %v", resp["username"])
	}
}

// TestClientLoginListeners1345 真起监听：自签证书、9870-9880 监听、
// 9876 上 TLS 登录成功、管理接口 404。监听器测试结束即关。
func TestClientLoginListeners1345(t *testing.T) {
	s := newTestServer144(t)
	home := t.TempDir()
	if err := ensureSelfSigned(home, "localhost"); err != nil {
		t.Fatalf("生成自签证书失败：%v", err)
	}
	cert := filepath.Join(home, "certs", "cert.pem")
	key := filepath.Join(home, "certs", "key.pem")
	lns := s.startClientLoginListeners(cert, key)
	defer func() {
		for _, ln := range lns {
			_ = ln.Close()
		}
	}()
	ports := map[int]bool{}
	for _, ln := range lns {
		if ta, ok := ln.Addr().(*net.TCPAddr); ok {
			ports[ta.Port] = true
		}
	}
	if !ports[9876] {
		t.Fatalf("9876 应在监听中，实际起来的端口：%v", ports)
	}
	// 等监听 goroutine 跑起来（ServeTLS 是异步的）。
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // 测试用自签证书
	var lastErr error
	var code int
	for i := 0; i < 20; i++ {
		code, lastErr = func() (int, error) {
			resp, err := client.Post("https://127.0.0.1:9876/api/v1/client/login", "application/json",
				strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
			if err != nil {
				return 0, err
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			return resp.StatusCode, nil
		}()
		if lastErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("9876 上登录请求连不上：%v", lastErr)
	}
	if code != 200 {
		t.Fatalf("9876 上正确密码应 200，实际 %d", code)
	}
	// 管理接口在登录监听上 404。
	resp, err := client.Get("https://127.0.0.1:9876/api/v1/subscriptions/link")
	if err != nil {
		t.Fatalf("9876 上管理接口请求失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("9876 上管理接口应 404，实际 %d", resp.StatusCode)
	}
}
