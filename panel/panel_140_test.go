package main

// v1.3.10 两件事的测试：
// ① 备用码一键复制是前端改动，后端无测试，jsdom 覆盖（见 /tmp/jsdom140/run.js）。
// ② 管理面板模式误报 realm 故障：healthLoop 每 5 分钟查 realmRunning()，
//    background.go 与 monitor.go 里没有任何 ManagerOnly 判断；管理面板本地
//    没有 realm，realmRunning() 恒为假，于是误报；而且提醒里说的“面板服务页”
//    在管理面板导航里根本没有。修法：只在完整面板且本机装了 realm 时才监控，
//    其余依赖本地中转/本地落地的后台检查（证书、到期、延迟、流量）也一律跳过。

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureTelegram140 拦截发往 Telegram 的 HTTP 请求（sendTelegram 走
// http.DefaultClient），返回收到的消息文本；测试结束自动恢复。
func captureTelegram140(t *testing.T) *[]string {
	t.Helper()
	var got []string
	orig := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc140(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if s, _ := m["text"].(string); s != "" {
			got = append(got, s)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = orig })
	return &got
}

type roundTripFunc140 func(*http.Request) (*http.Response, error)

func (f roundTripFunc140) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// enableTelegram140 给测试面板配上 Telegram（假令牌，请求会被 captureTelegram140 拦截）。
func enableTelegram140(s *Server) {
	s.cfg.Telegram.Enabled = true
	s.cfg.Telegram.BotToken = "test-token"
	s.cfg.Telegram.ChatID = "test-chat"
}

// writeRelayConf140 在 realmDir 写一份最小 relay.conf。
func writeRelayConf140(t *testing.T, realmDir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(content), 0o644); err != nil {
		t.Fatalf("写 relay.conf：%v", err)
	}
}

// stubRealmInstalled140 把 realm 安装检测桩成指定值，测试结束自动恢复。
func stubRealmInstalled140(t *testing.T, installed bool) {
	t.Helper()
	orig := realmInstalledFunc
	realmInstalledFunc = func() bool { return installed }
	t.Cleanup(func() { realmInstalledFunc = orig })
}

// TestWatchRealmDecision140 监控决策真值表：只在完整面板且本机装了 realm 时监控。
func TestWatchRealmDecision140(t *testing.T) {
	cases := []struct {
		managerOnly, installed, want bool
	}{
		{true, true, false},   // 管理面板：本地没中转，不监控
		{true, false, false},  // 管理面板：本地没中转，不监控
		{false, false, false}, // 完整面板但没装中转：没东西可监控，不误报
		{false, true, true},   // 完整面板且装了中转：监控
	}
	for _, c := range cases {
		if got := watchRealmDecision(c.managerOnly, c.installed); got != c.want {
			t.Errorf("watchRealmDecision(%v, %v)=%v want %v", c.managerOnly, c.installed, got, c.want)
		}
	}
}

// TestHealthTickManagerOnlyNoAlert 管理面板模式：realm 挂了也不提醒。
// （用户真机上报：CP管理面板不开中转，却收到“realm 挂了”提醒。）
func TestHealthTickManagerOnlyNoAlert(t *testing.T) {
	realmDir := t.TempDir()
	s := newTestAddLandingServer(t, t.TempDir(), realmDir)
	s.cfg.ManagerOnly = true
	enableTelegram140(s)
	msgs := captureTelegram140(t)
	stubRealmInstalled140(t, true) // 管理面板的机器上就算装了 realm 也不监控
	s.lastHealthOK = true          // 模拟之前是运行态

	s.healthTick()

	if len(*msgs) != 0 {
		t.Fatalf("管理面板模式不应发任何提醒，实际发了：%v", *msgs)
	}
	if s.lastHealthOK {
		t.Fatal("healthTick 后基线应刷新为当前状态（realm 没跑 = false）")
	}
}

// TestHealthTickFullPanelNoRelayNoAlert 完整面板但没装中转：
// 面板只装（新装面板还没装中转）时 realmRunning() 恒为假，
// 以前 lastHealthOK 初始 true，第一 tick 就误报一次；现在不报。
func TestHealthTickFullPanelNoRelayNoAlert(t *testing.T) {
	if realmInstalled() {
		t.Skip("本机装了 realm，测不了“没装中转”的分支")
	}
	realmDir := t.TempDir()
	s := newTestAddLandingServer(t, t.TempDir(), realmDir)
	enableTelegram140(s)
	msgs := captureTelegram140(t)
	s.lastHealthOK = true // 新面板的初始基线

	s.healthTick()

	if len(*msgs) != 0 {
		t.Fatalf("没装中转的完整面板不应发提醒，实际发了：%v", *msgs)
	}
}

// TestHealthTickFullPanelRelayDownAlerts 完整面板＋装了中转：
// realm 由运行变不运行时仍提醒一次，之后不再重复提醒。
func TestHealthTickFullPanelRelayDownAlerts(t *testing.T) {
	realmDir := t.TempDir()
	s := newTestAddLandingServer(t, t.TempDir(), realmDir)
	enableTelegram140(s)
	msgs := captureTelegram140(t)
	stubRealmInstalled140(t, true)
	s.lastHealthOK = true // 模拟之前 realm 在跑

	s.healthTick()

	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "realm 挂了") {
		t.Fatalf("中转由运行变不运行时应提醒一次“realm 挂了”，实际：%v", *msgs)
	}
	s.healthTick()
	if len(*msgs) != 1 {
		t.Fatalf("持续停止不应重复提醒，实际发了 %d 条：%v", len(*msgs), *msgs)
	}
}

// TestManagerOnlySwitchTakesEffectImmediately 模式切换后立即生效：
// 切到管理面板就不再提醒（不用等下一个 5 分钟 tick），
// 切回完整面板也不拿旧基线误报。
func TestManagerOnlySwitchTakesEffectImmediately(t *testing.T) {
	realmDir := t.TempDir()
	s := newTestAddLandingServer(t, t.TempDir(), realmDir)
	h := s.routes()
	enableTelegram140(s)
	msgs := captureTelegram140(t)
	stubRealmInstalled140(t, true)
	s.lastHealthOK = true // 模拟之前 realm 在跑
	cookie := loginTestCookie(t, h)

	// 切到管理面板：应 200，且基线立即刷新为当前状态（本机 realm 没跑 = false）。
	code, body := postAuthed139(t, h, cookie, "POST", "/api/v1/settings/manager-only", `{"manager_only":true}`)
	if code != 200 {
		t.Fatalf("切到管理面板应 200，实际 %d：%s", code, body)
	}
	if !s.cfg.ManagerOnly {
		t.Fatal("切到管理面板后 ManagerOnly 应为 true")
	}
	if s.lastHealthOK != realmRunning() {
		t.Fatalf("切换后健康基线应立即刷新为当前 realm 状态（%v），实际 %v", realmRunning(), s.lastHealthOK)
	}
	s.healthTick()
	if len(*msgs) != 0 {
		t.Fatalf("切到管理面板后不应发提醒，实际发了：%v", *msgs)
	}

	// 切回完整面板：基线已是 false（realm 没跑），不应拿旧的 true 基线误报。
	code, body = postAuthed139(t, h, cookie, "POST", "/api/v1/settings/manager-only", `{"manager_only":false}`)
	if code != 200 {
		t.Fatalf("切回完整面板应 200，实际 %d：%s", code, body)
	}
	s.healthTick()
	if len(*msgs) != 0 {
		t.Fatalf("切回完整面板后不应拿旧基线误报，实际发了：%v", *msgs)
	}
}

// TestCheckExpiryManagerOnlySkipped 到期检查：管理面板模式不跑。
func TestCheckExpiryManagerOnlySkipped(t *testing.T) {
	conf := "LANDING=\"203.0.113.1\"\nLANDING_COUNT=1\nPORTS=\"tcp:30080:30080\"\n"
	expiry := time.Now().AddDate(0, 0, 7).Add(time.Hour).Format(time.RFC3339) // 7 天档（多 1 小时防边界）

	// 完整面板：7 天到期提醒应发。
	realmDir := t.TempDir()
	s := newTestAddLandingServer(t, t.TempDir(), realmDir)
	writeRelayConf140(t, realmDir, conf)
	s.state.ext("203.0.113.1").Expiry = expiry
	enableTelegram140(s)
	msgs := captureTelegram140(t)
	s.checkExpiry()
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "还有 7 天到期") {
		t.Fatalf("完整面板应发 7 天到期提醒，实际：%v", *msgs)
	}

	// 管理面板：同样条件不发。
	realmDir2 := t.TempDir()
	s2 := newTestAddLandingServer(t, t.TempDir(), realmDir2)
	writeRelayConf140(t, realmDir2, conf)
	s2.state.ext("203.0.113.1").Expiry = expiry
	s2.cfg.ManagerOnly = true
	enableTelegram140(s2)
	msgs2 := captureTelegram140(t)
	s2.checkExpiry()
	if len(*msgs2) != 0 {
		t.Fatalf("管理面板模式不应发到期提醒，实际发了：%v", *msgs2)
	}
}

// TestCheckCertsManagerOnlySkipped 证书检查：管理面板模式不跑。
// 本地起一个自签 TLS 服务（证书 3 天后过期），落地指向它。
func TestCheckCertsManagerOnlySkipped(t *testing.T) {
	cert := selfSignedCert140(t, time.Now().AddDate(0, 0, 3).Add(time.Hour))
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("起 TLS 监听：%v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				if tc, ok := conn.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
			}(c)
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	conf := "LANDING=\"127.0.0.1\"\nLANDING_COUNT=1\nCERT_DOMAIN=\"example.com\"\nPORTS=\"tcp:" + itoa(port) + ":443\"\n"

	// 完整面板：证书只剩 3 天应发提醒。
	realmDir := t.TempDir()
	s := newTestAddLandingServer(t, t.TempDir(), realmDir)
	writeRelayConf140(t, realmDir, conf)
	enableTelegram140(s)
	msgs := captureTelegram140(t)
	s.checkCerts()
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "只剩 3 天") {
		t.Fatalf("完整面板应发证书 3 天提醒，实际：%v", *msgs)
	}

	// 管理面板：同样条件不发。
	realmDir2 := t.TempDir()
	s2 := newTestAddLandingServer(t, t.TempDir(), realmDir2)
	writeRelayConf140(t, realmDir2, conf)
	s2.cfg.ManagerOnly = true
	enableTelegram140(s2)
	msgs2 := captureTelegram140(t)
	s2.checkCerts()
	if len(*msgs2) != 0 {
		t.Fatalf("管理面板模式不应发证书提醒，实际发了：%v", *msgs2)
	}
}

// selfSignedCert140 生成一张自签证书（NotAfter 指定），供 checkCerts 测试用。
func selfSignedCert140(t *testing.T, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 RSA 密钥：%v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		DNSNames:     []string{"example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("生成自签证书：%v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("拼 TLS 证书：%v", err)
	}
	return cert
}

// TestLatencyManagerOnlySkipped 延迟探测：管理面板模式不跑。
// 落地指向本机已关闭的端口（127.0.0.1:1），完整面板应报“落地离线”。
func TestLatencyManagerOnlySkipped(t *testing.T) {
	conf := "LANDING=\"127.0.0.1\"\nLANDING_COUNT=1\nPORTS=\"tcp:1:1\"\n"

	realmDir := t.TempDir()
	s := newTestAddLandingServer(t, t.TempDir(), realmDir)
	writeRelayConf140(t, realmDir, conf)
	s.cfg.Latency.Enabled = true
	enableTelegram140(s)
	msgs := captureTelegram140(t)
	s.probeLatencyRoundLocked()
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "落地离线") {
		t.Fatalf("完整面板应发落地离线提醒，实际：%v", *msgs)
	}

	realmDir2 := t.TempDir()
	s2 := newTestAddLandingServer(t, t.TempDir(), realmDir2)
	writeRelayConf140(t, realmDir2, conf)
	s2.cfg.Latency.Enabled = true
	s2.cfg.ManagerOnly = true
	enableTelegram140(s2)
	msgs2 := captureTelegram140(t)
	s2.probeLatencyRoundLocked()
	if len(*msgs2) != 0 {
		t.Fatalf("管理面板模式不应发落地离线提醒，实际发了：%v", *msgs2)
	}
}

// TestTrafficManagerOnlySkipped 流量限量检查：管理面板模式不跑。
// 限量 1000 字节、已用 850（85%），完整面板应发 80% 提醒。
func TestTrafficManagerOnlySkipped(t *testing.T) {
	conf := "LANDING=\"203.0.113.1\"\nLANDING_COUNT=1\nPORTS=\"tcp:30080:30080\"\n"

	realmDir := t.TempDir()
	s := newTestAddLandingServer(t, t.TempDir(), realmDir)
	writeRelayConf140(t, realmDir, conf)
	s.state.ext("203.0.113.1").TrafficLimitBytes = 1000
	// v1.3.27：首采按 0 起算，一次 addDelta {425,425} 即计入 850（85%）。
	s.traffic.addDelta("203.0.113.1", map[string][2]int64{"tcp:30080": {425, 425}})
	enableTelegram140(s)
	msgs := captureTelegram140(t)
	s.enforceTrafficLimits()
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "流量已用 85%") {
		t.Fatalf("完整面板应发流量 85%% 提醒，实际：%v", *msgs)
	}

	realmDir2 := t.TempDir()
	s2 := newTestAddLandingServer(t, t.TempDir(), realmDir2)
	writeRelayConf140(t, realmDir2, conf)
	s2.state.ext("203.0.113.1").TrafficLimitBytes = 1000
	// v1.3.27：首采按 0 起算，一次 addDelta {425,425} 即计入 850（85%）。
	s2.traffic.addDelta("203.0.113.1", map[string][2]int64{"tcp:30080": {425, 425}})
	s2.cfg.ManagerOnly = true
	enableTelegram140(s2)
	msgs2 := captureTelegram140(t)
	s2.enforceTrafficLimits()
	if len(*msgs2) != 0 {
		t.Fatalf("管理面板模式不应发流量提醒，实际发了：%v", *msgs2)
	}
}
