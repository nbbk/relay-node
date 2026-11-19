package main

// v1.3.14 两件事的测试：
// ① 管理面板模式下 Cloudflare Token 卡片可见（后端无改动，jsdom 覆盖，见 test-144.js；
//    机器监控卡片同理可见——monitorLoop 在管理面板下照常跑，藏配置是同类问题，一起修）。
// ② 面板自身真证书自动续签：panelCertInfo（自签判定）、shouldRenewPanelCert
//    决策矩阵、maybeRenewPanelCert 失败只提醒一次、成功收尾（清标志、发 Telegram、
//    重启）、handleHTTPS 失败原因持久化、填令牌清掉旧错、settings 返回 panel_cert。

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- 测试脚手架 ----------

func newTestServer144(t *testing.T) *Server {
	t.Helper()
	t.Setenv("PANEL_INITIAL_PASSWORD", "test-password-123")
	s, err := newServer(Env{PanelHome: t.TempDir(), RealmDir: t.TempDir(), KitDir: t.TempDir(), Port: 11111})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return s
}

func loginCookie144(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("登录失败：%d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	if cookie == "" {
		t.Fatal("登录后没发会话 cookie")
	}
	return strings.SplitN(cookie, ";", 2)[0]
}

func postJSON144(t *testing.T, h http.Handler, cookie, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func getJSON144(t *testing.T, h http.Handler, cookie, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// stubACMEFail144 把 ACME 目录指向本地一律 500 的假服务端（obtainACMECert
// 第一步就失败，不碰真实 Let's Encrypt），测试结束恢复。
func stubACMEFail144(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(srv.Close)
	orig := acmeDirectoryURL
	acmeDirectoryURL = srv.URL
	t.Cleanup(func() { acmeDirectoryURL = orig })
}

// stubRestart144 把面板重启换成计数 stub（真重启会 os.Exit，测试里不能真跑）。
// 计数用 atomic：重启 goroutine 里的 Add 与测试里的 Load 之间形成
// happens-before，否则 race detector 会把“goroutine 读 restartPanelFunc 全局”
// 与“测试结束清理写回原值”报成数据竞争。
func stubRestart144(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	orig := restartPanelFunc
	restartPanelFunc = func(env Env) { n.Add(1) }
	t.Cleanup(func() { restartPanelFunc = orig })
	return &n
}

func waitRestart144(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n.Load() >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("重启次数=%d want >= %d", n.Load(), want)
}

// writeCAleaf144 写一张 CA 签发的叶子证书（Issuer != Subject，即真证书的样子）
// 到面板 certs/，剩余天数约 days。
func writeCAleaf144(t *testing.T, s *Server, days int) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA 144"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "example.com"}, DNSNames: []string{"example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Duration(days)*24*time.Hour + 12*time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.env.PanelHome, "certs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ---------- panelCertInfo ----------

func TestPanelCertInfo144(t *testing.T) {
	s := newTestServer144(t)

	// 没有证书文件
	if info := s.panelCertInfo(); info.Exists {
		t.Fatal("没有 cert.pem 时 Exists 应为 false")
	}

	// 自签证书：Issuer == Subject
	if err := ensureSelfSigned(s.env.PanelHome, "example.com"); err != nil {
		t.Fatalf("ensureSelfSigned: %v", err)
	}
	info := s.panelCertInfo()
	if !info.Exists || !info.SelfSigned {
		t.Fatalf("自签证书应 Exists=true SelfSigned=true，实际 %+v", info)
	}
	if info.DaysLeft < 800 || info.DaysLeft > 825 {
		t.Fatalf("自签证书剩余天数=%d want 约 825", info.DaysLeft)
	}

	// CA 签发的叶子：不是自签，剩余天数约 10 天
	writeCAleaf144(t, s, 10)
	info = s.panelCertInfo()
	if !info.Exists || info.SelfSigned {
		t.Fatalf("CA 叶子应 Exists=true SelfSigned=false，实际 %+v", info)
	}
	if info.DaysLeft != 10 {
		t.Fatalf("CA 叶子剩余天数=%d want 10", info.DaysLeft)
	}

	// 损坏的证书文件
	if err := os.WriteFile(filepath.Join(s.env.PanelHome, "certs", "cert.pem"), []byte("not a pem"), 0o644); err != nil {
		t.Fatal(err)
	}
	if info := s.panelCertInfo(); info.Exists {
		t.Fatal("损坏的 cert.pem 时 Exists 应为 false")
	}
}

// ---------- shouldRenewPanelCert 决策矩阵 ----------

func TestShouldRenewPanelCert144(t *testing.T) {
	real10 := panelCertInfo{Exists: true, SelfSigned: false, DaysLeft: 10}
	real30 := panelCertInfo{Exists: true, SelfSigned: false, DaysLeft: 30}
	real31 := panelCertInfo{Exists: true, SelfSigned: false, DaysLeft: 31}
	realExpired := panelCertInfo{Exists: true, SelfSigned: false, DaysLeft: -2}
	selfSigned := panelCertInfo{Exists: true, SelfSigned: true, DaysLeft: 10}
	missing := panelCertInfo{}
	cases := []struct {
		name    string
		enabled bool
		domain  string
		token   string
		info    panelCertInfo
		want    bool
	}{
		{"HTTPS 没开不续", false, "example.com", "cfut_x", real10, false},
		{"没域名不续", true, "", "cfut_x", real10, false},
		{"没令牌不续", true, "example.com", "", real10, false},
		{"没证书不续", true, "example.com", "cfut_x", missing, false},
		{"自签的不续", true, "example.com", "cfut_x", selfSigned, false},
		{"真证书剩 31 天不续", true, "example.com", "cfut_x", real31, false},
		{"真证书剩 30 天续", true, "example.com", "cfut_x", real30, true},
		{"真证书剩 10 天续", true, "example.com", "cfut_x", real10, true},
		{"真证书已过期续", true, "example.com", "cfut_x", realExpired, true},
	}
	for _, c := range cases {
		if got := shouldRenewPanelCert(c.enabled, c.domain, c.token, c.info); got != c.want {
			t.Errorf("%s：shouldRenew=%v want %v", c.name, got, c.want)
		}
	}
}

// ---------- maybeRenewPanelCert：跳过条件 ----------

func TestMaybeRenewPanelCertSkips144(t *testing.T) {
	s := newTestServer144(t)
	got := captureTelegram140(t)
	enableTelegram140(s)
	restarts := stubRestart144(t)
	writeCAleaf144(t, s, 10) // 真证书剩 10 天，本应续
	s.cfg.HTTPSEnabled = true
	s.cfg.HTTPSDomain = "example.com"

	// 没令牌：不跑
	s.cfg.CloudflareToken = ""
	s.maybeRenewPanelCert()
	if len(*got) != 0 || s.cfg.PanelCertLastErr != "" || restarts.Load() != 0 {
		t.Fatalf("没令牌时应什么都不做：telegram=%v err=%q restarts=%d", *got, s.cfg.PanelCertLastErr, restarts.Load())
	}

	// 自签证书：不续（哪怕有令牌）
	s.cfg.CloudflareToken = "cfut_testtoken144"
	if err := ensureSelfSigned(s.env.PanelHome, "example.com"); err != nil {
		t.Fatal(err)
	}
	s.maybeRenewPanelCert()
	if len(*got) != 0 || s.cfg.PanelCertLastErr != "" || restarts.Load() != 0 {
		t.Fatalf("自签证书时应不续：telegram=%v err=%q restarts=%d", *got, s.cfg.PanelCertLastErr, restarts.Load())
	}

	// HTTPS 没开：不跑
	writeCAleaf144(t, s, 10)
	s.cfg.HTTPSEnabled = false
	s.maybeRenewPanelCert()
	if len(*got) != 0 || restarts.Load() != 0 {
		t.Fatalf("HTTPS 没开时应不跑：telegram=%v restarts=%d", *got, restarts.Load())
	}
}

// ---------- maybeRenewPanelCert：失败只提醒一次，成功收尾 ----------

func TestMaybeRenewPanelCertFailureOnce144(t *testing.T) {
	s := newTestServer144(t)
	got := captureTelegram140(t)
	enableTelegram140(s)
	restarts := stubRestart144(t)
	stubACMEFail144(t)
	writeCAleaf144(t, s, 10)
	s.cfg.HTTPSEnabled = true
	s.cfg.HTTPSDomain = "example.com"
	s.cfg.CloudflareToken = "cfut_testtoken144"

	s.maybeRenewPanelCert()
	if s.cfg.PanelCertLastErr == "" {
		t.Fatal("续签失败后 PanelCertLastErr 应记下原因")
	}
	if !strings.Contains(s.cfg.PanelCertLastErr, "ACME") {
		t.Fatalf("失败原因应提到 ACME，实际：%q", s.cfg.PanelCertLastErr)
	}
	if len(*got) != 1 || !strings.Contains((*got)[0], "续签失败") {
		t.Fatalf("续签失败应恰发 1 条 Telegram，实际：%v", *got)
	}
	if !s.cfg.PanelCertRenewWarned {
		t.Fatal("失败后去重标志应置上")
	}
	if restarts.Load() != 0 {
		t.Fatalf("续签失败不应重启，restarts=%d", restarts.Load())
	}

	// 再跑一次：不再重复发 Telegram
	s.maybeRenewPanelCert()
	if len(*got) != 1 {
		t.Fatalf("第二次失败不应重复提醒，telegram 条数=%d", len(*got))
	}

	// 成功收尾：清标志、发成功提醒、重启一次
	s.onPanelCertRenewed("example.com")
	if s.cfg.PanelCertLastErr != "" || s.cfg.PanelCertRenewWarned {
		t.Fatalf("成功后失败记录与去重标志应清掉：err=%q warned=%v", s.cfg.PanelCertLastErr, s.cfg.PanelCertRenewWarned)
	}
	if len(*got) != 2 || !strings.Contains((*got)[1], "已自动续签") {
		t.Fatalf("成功后应发续签成功提醒，实际：%v", *got)
	}
	waitRestart144(t, restarts, 1)
}

// ---------- handleHTTPS：失败原因持久化，成功清掉 ----------

func TestHandleHTTPSPersistsError144(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	stubACMEFail144(t)
	restarts := stubRestart144(t)
	s.cfg.CloudflareToken = "cfut_testtoken144"

	// 域名 + ACME 失败：200 + warning，失败原因写进 config
	code, resp := postJSON144(t, h, cookie, "/api/v1/settings/https", `{"enable":true,"domain":"example.com"}`)
	if code != 200 {
		t.Fatalf("开启 HTTPS 状态=%d want 200：%v", code, resp)
	}
	if _, ok := resp["warning"]; !ok {
		t.Fatalf("ACME 失败应返回 warning 字段，实际：%v", resp)
	}
	if s.cfg.PanelCertLastErr == "" {
		t.Fatal("ACME 失败后 PanelCertLastErr 应持久化")
	}
	if !s.cfg.HTTPSEnabled || s.cfg.HTTPSDomain != "example.com" {
		t.Fatalf("失败后 HTTPS 仍应开启（自签回落）：enabled=%v domain=%q", s.cfg.HTTPSEnabled, s.cfg.HTTPSDomain)
	}
	waitRestart144(t, restarts, 1)

	// settings 接口应带出 panel_cert.last_error
	code, settings := getJSON144(t, h, cookie, "/api/v1/settings")
	if code != 200 {
		t.Fatalf("settings 状态=%d want 200", code)
	}
	pc, _ := settings["panel_cert"].(map[string]any)
	if pc == nil {
		t.Fatal("settings 应返回 panel_cert")
	}
	if pc["last_error"] == "" {
		t.Fatalf("panel_cert.last_error 应非空，实际：%v", pc)
	}
	if pc["self_signed"] != true {
		t.Fatalf("回落自签后 self_signed 应为 true，实际：%v", pc)
	}

	// 无域名开启（自签路径，不调 ACME）：成功，清掉旧错。注意这次也起了一个
	// 300ms 后触发的重启 goroutine，必须等它跑完再返回，否则它会在测试恢复
	// restartPanelFunc 之后调到真的 restartSelf，把整个测试进程杀掉。
	code, _ = postJSON144(t, h, cookie, "/api/v1/settings/https", `{"enable":true,"domain":""}`)
	if code != 200 {
		t.Fatalf("无域名开启 HTTPS 状态=%d want 200", code)
	}
	waitRestart144(t, restarts, 2)
	if s.cfg.PanelCertLastErr != "" {
		t.Fatalf("自签路径成功后旧错应清掉，实际：%q", s.cfg.PanelCertLastErr)
	}
}

// ---------- handleCloudflare：填令牌清掉旧错 ----------

func TestHandleCloudflareClearsError144(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	s.cfg.PanelCertLastErr = "ACME 目录拉不到：旧错"
	s.cfg.PanelCertRenewWarned = true
	_ = s.saveConfig()

	code, _ := postJSON144(t, h, cookie, "/api/v1/settings/cloudflare", `{"token":"cfut_newtoken14400000000000000000000000"}`)
	if code != 200 {
		t.Fatalf("保存令牌状态=%d want 200", code)
	}
	if s.cfg.PanelCertLastErr != "" {
		t.Fatalf("换令牌后旧的签发失败原因应清掉，实际：%q", s.cfg.PanelCertLastErr)
	}
}

// ---------- handleSettings：panel_cert 字段 ----------

func TestHandleSettingsPanelCert144(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	if err := ensureSelfSigned(s.env.PanelHome, "example.com"); err != nil {
		t.Fatal(err)
	}
	s.cfg.HTTPSEnabled = true
	s.cfg.HTTPSDomain = "example.com"
	s.cfg.PanelCertLastErr = "ACME 目录拉不到：测试错"
	_ = s.saveConfig()

	_, settings := getJSON144(t, h, cookie, "/api/v1/settings")
	pc, _ := settings["panel_cert"].(map[string]any)
	if pc == nil {
		t.Fatal("settings 应返回 panel_cert")
	}
	if pc["exists"] != true || pc["self_signed"] != true {
		t.Fatalf("自签证书的 panel_cert 应 exists=true self_signed=true，实际：%v", pc)
	}
	if pc["last_error"] != "ACME 目录拉不到：测试错" {
		t.Fatalf("panel_cert.last_error 不对：%v", pc["last_error"])
	}
	if pc["expires"] == "" {
		t.Fatal("panel_cert.expires 应有值")
	}
}

// ---------- serveWithRetry：重启抢绑端口时重试，不直接死掉 ----------

func TestServeWithRetryBindConflict144(t *testing.T) {
	// 先占一个端口：模拟还没退出的旧进程。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占端口失败：%v", err)
	}
	addr := ln.Addr().String()

	srv := &http.Server{Addr: addr, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok144"))
	})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveWithRetry(srv, "", "")
	}()
	// 1.2 秒后放掉端口（模拟旧进程退出），新进程应在重试中绑上。
	time.Sleep(1200 * time.Millisecond)
	ln.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := client.Get("http://" + addr + "/")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && string(body) == "ok144" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("端口放掉后服务没起来：last err=%v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = srv.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("srv.Close() 后 serveWithRetry 没返回")
	}
}
