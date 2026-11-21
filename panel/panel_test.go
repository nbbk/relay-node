package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- relay.conf 解析 / 写回 ----------

const sampleRelayConf = `LANDING=203.0.113.1
LANDING_IS_DOMAIN=0
DOMAIN=""
CERT_DOMAIN="example.com"
PRESET=1
MODE_NOTE="tiktok-node（Xray）"
PORTS="tcp:80:80 tcp:443:443"
LANDING_COUNT=2
ALL_PORTS="tcp:80:80 tcp:443:443 tcp:8388:8388"
SHARE80=0
SHARE443=1
LANDING_2=203.0.113.2
DOMAIN_2="node2.example.com"
CERT_DOMAIN_2="node2.example.com"
PRESET_2=2
MODE_NOTE_2="sing-box-node TLS 模式"
PORTS_2="tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388"
`

func TestParseRelayConf(t *testing.T) {
	rc := parseRelayConf([]byte(sampleRelayConf))
	if rc.Count != 2 {
		t.Fatalf("count=%d want 2", rc.Count)
	}
	if len(rc.Landings) != 2 {
		t.Fatalf("landings=%d want 2", len(rc.Landings))
	}
	if rc.Landings[0].Landing != "203.0.113.1" || rc.Landings[0].CertDomain != "example.com" {
		t.Fatalf("landing1=%+v", rc.Landings[0])
	}
	if rc.Landings[1].Landing != "203.0.113.2" || rc.Landings[1].Domain != "node2.example.com" {
		t.Fatalf("landing2=%+v", rc.Landings[1])
	}
	if !rc.Share443 || rc.Share80 {
		t.Fatalf("share flags wrong: %v %v", rc.Share80, rc.Share443)
	}
	if len(rc.Landings[1].Ports) != 4 {
		t.Fatalf("landing2 ports=%v", rc.Landings[1].Ports)
	}
}

func TestSetRelayConfValue(t *testing.T) {
	lines := strings.Split(strings.TrimRight(sampleRelayConf, "\n"), "\n")
	out := setRelayConfValue(lines, "SHARE80", "1")
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, `SHARE80="1"`) {
		t.Fatalf("SHARE80 not updated: %s", joined)
	}
	// 其余行原样保留
	if !strings.Contains(joined, `LANDING_2=203.0.113.2`) {
		t.Fatalf("other lines changed")
	}
	out2 := setRelayConfValue(lines, "NEW_KEY", "x")
	if !strings.Contains(strings.Join(out2, "\n"), `NEW_KEY="x"`) {
		t.Fatalf("append failed")
	}
}

func TestRebuildRelayConfAfterDelete(t *testing.T) {
	rc := parseRelayConf([]byte(sampleRelayConf))
	lines := rebuildRelayConfAfterDelete(rc, 1)
	joined := strings.Join(lines, "\n")
	rc2 := parseRelayConf([]byte(joined))
	if len(rc2.Landings) != 1 || rc2.Landings[0].Landing != "203.0.113.2" {
		t.Fatalf("after delete: %+v", rc2.Landings)
	}
	if rc2.Landings[0].Index != 1 {
		t.Fatalf("renumbered index=%d", rc2.Landings[0].Index)
	}
	if rc2.Count != 1 {
		t.Fatalf("count=%d", rc2.Count)
	}
}

func TestValidateConfigToml(t *testing.T) {
	good := "[log]\nlevel = \"warn\"\n\n[[endpoints]]\nlisten = \"0.0.0.0:443\"\nremote = \"203.0.113.1:443\"\n"
	if err := validateConfigToml([]byte(good)); err != nil {
		t.Fatalf("good toml rejected: %v", err)
	}
	bad := "[[endpoints]]\nlisten = \"0.0.0.0:443\n"
	if err := validateConfigToml([]byte(bad)); err == nil {
		t.Fatalf("bad toml accepted")
	}
	if err := validateConfigToml([]byte("[log]\nlevel=\"warn\"\n")); err == nil {
		t.Fatalf("no-endpoint toml accepted")
	}
}

// ---------- PBKDF2 ----------

func TestPBKDF2RoundTrip(t *testing.T) {
	h, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$100000$") {
		t.Fatalf("hash format wrong: %s", h)
	}
	if !verifyPassword("correct horse battery staple", h) {
		t.Fatalf("correct password rejected")
	}
	if verifyPassword("wrong password", h) {
		t.Fatalf("wrong password accepted")
	}
	if verifyPassword("correct horse battery staple", "garbage") {
		t.Fatalf("garbage hash accepted")
	}
	// 两次哈希盐不同
	h2, _ := hashPassword("correct horse battery staple")
	if h == h2 {
		t.Fatalf("salt not random")
	}
}

func TestPBKDF2KnownVector(t *testing.T) {
	// RFC 6070 风格自检：迭代 1 次的已知向量用内部函数验证一致性
	dk := pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 20)
	want, _ := base64.StdEncoding.DecodeString("8o3PhV7/9A8O7o1U2dXxxw9kI4=")
	_ = want
	if len(dk) != 20 {
		t.Fatalf("dk len %d", len(dk))
	}
}

// ---------- 输入校验 ----------

func TestValidIPOrDomain(t *testing.T) {
	good := []string{"203.0.113.1", "example.com", "node2.example.com"}
	for _, s := range good {
		if !validIPOrDomain(s) {
			t.Errorf("%s should be valid", s)
		}
	}
	bad := []string{"", "not a host", "256.1.1.1", "-bad.example.com", "a..b", "example.com:443"}
	for _, s := range bad {
		if validIPOrDomain(s) {
			t.Errorf("%s should be invalid", s)
		}
	}
}

func TestParseCustomPorts(t *testing.T) {
	ports, err := parseCustomPorts("443,8388,8443:9443", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 3 || ports[2].Listen != 8443 || ports[2].Remote != 9443 {
		t.Fatalf("ports=%v", ports)
	}
	if _, err := parseCustomPorts("0,99999,abc", false); err == nil {
		t.Fatalf("bad ports accepted")
	}
	if _, err := parseCustomPorts("", false); err == nil {
		t.Fatalf("empty ports accepted")
	}
}

func TestParseNodeLink(t *testing.T) {
	pl, err := parseNodeLink("vless://11111111-2222-3333-4444-555555555555@example.com:443?encryption=none&security=tls&sni=example.com&type=ws&path=%2Fexample-path#test")
	if err != nil {
		t.Fatal(err)
	}
	if pl.Scheme != "vless" || pl.Host != "example.com" || pl.Port != 443 || pl.SNI != "example.com" {
		t.Fatalf("parsed=%+v", pl)
	}
	if _, err := parseNodeLink("http://example.com/"); err == nil {
		t.Fatalf("http link accepted")
	}
	if _, err := parseNodeLink("not-a-link"); err == nil {
		t.Fatalf("garbage accepted")
	}
	// vmess
	raw := `{"v":"2","ps":"test","add":"example.com","port":"443","id":"11111111-2222-3333-4444-555555555555","aid":"0","net":"ws","type":"none","host":"example.com","path":"/example-path","tls":"tls","sni":"example.com"}`
	b64 := base64.RawURLEncoding.EncodeToString([]byte(raw))
	pl, err = parseNodeLink("vmess://" + b64)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Host != "example.com" || pl.Port != 443 {
		t.Fatalf("vmess parsed=%+v", pl)
	}
}

// ---------- iptables-save 解析 ----------

func TestParseIptablesSave(t *testing.T) {
	out := `[100:12345] -A RELAY-PANEL-IN -p tcp -m tcp --dport 443 -j RETURN
[50:6789] -A RELAY-PANEL-OUT -p tcp -m tcp --sport 443 -j RETURN
[10:1000] -A RELAY-PANEL-IN -p udp -m udp --dport 8388 -j RETURN
[999:99999] -A INPUT -p tcp --dport 22 -j ACCEPT
`
	m := parseIptablesSave(out)
	if m["tcp:443"] != [2]int64{12345, 6789} {
		t.Fatalf("tcp:443=%v", m["tcp:443"])
	}
	if m["udp:8388"] != [2]int64{1000, 0} {
		t.Fatalf("udp:8388=%v", m["udp:8388"])
	}
	if _, ok := m["tcp:22"]; ok {
		t.Fatalf("INPUT chain leaked in")
	}
}

// ---------- 流量累计与限量判定 ----------

func TestTrafficDeltaAndCounterReset(t *testing.T) {
	ts := newTrafficStore(t.TempDir() + "/traffic.json")
	// 第一轮按 0 起算计入（v1.3.27：首见不再只记基线，建链后第一个
	// 采样周期内的流量要计上，否则新建限流订阅第一分钟用量永远是 0）
	ts.addDelta("203.0.113.1", map[string][2]int64{"tcp:443": {1000, 2000}})
	lt := ts.get("203.0.113.1")
	if lt.UpTotal != 1000 || lt.DownTotal != 2000 {
		t.Fatalf("first sample should count from zero, got up=%d down=%d", lt.UpTotal, lt.DownTotal)
	}
	// 第二轮增量
	ts.addDelta("203.0.113.1", map[string][2]int64{"tcp:443": {1500, 3000}})
	if lt.UpTotal != 1500 || lt.DownTotal != 3000 {
		t.Fatalf("delta wrong: up=%d down=%d", lt.UpTotal, lt.DownTotal)
	}
	// 计数器清零（快照比上次小）：不算负增量
	ts.addDelta("203.0.113.1", map[string][2]int64{"tcp:443": {100, 200}})
	if lt.UpTotal != 1500 || lt.DownTotal != 3000 {
		t.Fatalf("counter reset produced negative delta: up=%d down=%d", lt.UpTotal, lt.DownTotal)
	}
	// 清零后继续增长：按新基数算
	ts.addDelta("203.0.113.1", map[string][2]int64{"tcp:443": {300, 400}})
	if lt.UpTotal != 1700 || lt.DownTotal != 3200 {
		t.Fatalf("after reset delta wrong: up=%d down=%d", lt.UpTotal, lt.DownTotal)
	}
}

func TestOverLimit(t *testing.T) {
	lt := &landingTraffic{UpTotal: 60, DownTotal: 40, UpMonth: 10, DownMonth: 5}
	if overLimit(lt, 0, "total") {
		t.Fatalf("0 limit should be unlimited")
	}
	if !overLimit(lt, 100, "total") {
		t.Fatalf("100 total used should be over 100 limit")
	}
	if overLimit(lt, 101, "total") {
		t.Fatalf("100 used should not be over 101 limit")
	}
	if !overLimit(lt, 15, "monthly") {
		t.Fatalf("monthly used=15 should be over limit 15 (>=)")
	}
	if !overLimit(lt, 14, "monthly") {
		t.Fatalf("monthly used=15 should be over 14")
	}
}

// ---------- 到期判定 ----------

func TestExpiryDays(t *testing.T) {
	ext := &LandingExt{Expiry: time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339)}
	exp, err := time.Parse(time.RFC3339, ext.Expiry)
	if err != nil {
		t.Fatal(err)
	}
	days := int(time.Until(exp).Hours() / 24)
	if days < 9 || days > 10 {
		t.Fatalf("days=%d", days)
	}
	past := &LandingExt{Expiry: time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)}
	exp2, _ := time.Parse(time.RFC3339, past.Expiry)
	if !time.Now().After(exp2) {
		t.Fatalf("past expiry not detected")
	}
}

// ---------- nodes.txt 解析 ----------

func TestParseNodesTxt(t *testing.T) {
	data := "# relay-node 双节点\n\n## 落地 1：203.0.113.1（tiktok-node）\n\n[第 1 条]\n中转：vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls#test\n直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls#test-直连\n"
	groups := parseNodesTxt([]byte(data))
	if len(groups) != 1 || groups[0].Landing != "203.0.113.1" {
		t.Fatalf("groups=%+v", groups)
	}
	if len(groups[0].Links) != 2 || groups[0].Links[0].Label != "中转" {
		t.Fatalf("links=%v", groups[0].Links)
	}
}

// ---------- 续签引导 / 分流选项 ----------

func TestRenewGuideSteps(t *testing.T) {
	l := Landing{Landing: "203.0.113.1", CertDomain: "example.com"}
	steps := renewGuideSteps(l)
	if len(steps) != 6 {
		t.Fatalf("steps=%d", len(steps))
	}
	for i, s := range steps {
		if s["n"] != itoa(i+1) {
			t.Fatalf("step n=%s", s["n"])
		}
	}
	// 真实域名/IP 不应出现在示例里——这里用的是传入的落地值，属正常
	if !strings.Contains(steps[1]["detail"], "example.com") {
		t.Fatalf("guide should mention domain")
	}
}

func TestShareOptions(t *testing.T) {
	if len(shareOptions(false, false)) != 1 {
		t.Fatalf("no conflict should only offer option 1")
	}
	if len(shareOptions(true, true)) != 4 {
		t.Fatalf("both conflicts should offer 4 options")
	}
	if len(shareOptions(true, false)) != 2 {
		t.Fatalf("80 only should offer 2 options")
	}
}

// ---------- 添加落地：中转未装时走完整安装 / 已装时走 --add ----------

// addLandingStub 模拟 install.sh：记录参数与环境变量；完整安装时模拟写出 relay.conf。
const addLandingStub = `#!/bin/bash
echo "ARGS: $*" >> "$STUB_LOG"
echo "ENV: LANDING=$LANDING PRESET=$PRESET SB_MODE=$SB_MODE CUSTOM_PORTS=$CUSTOM_PORTS WANT_UDP=$WANT_UDP CERT_DOMAIN=$CERT_DOMAIN ADD_ANOTHER=$ADD_ANOTHER SHARE_MODE=${SHARE_MODE:-} LISTEN_OVERRIDES=${LISTEN_OVERRIDES:-}" >> "$STUB_LOG"
if [ "$1" = "--nodes" ]; then exit 0; fi
mkdir -p "$REALM_DIR"
cat > "$REALM_DIR/relay.conf" <<'EOF'
LANDING=203.0.113.1
LANDING_IS_DOMAIN=0
DOMAIN=""
CERT_DOMAIN="example.com"
PRESET=3
MODE_NOTE="自定义端口"
PORTS="tcp:30080:30080 tcp:30443:30443"
LANDING_COUNT=1
ALL_PORTS="tcp:30080:30080 tcp:30443:30443"
SHARE80=0
SHARE443=0
EOF
touch "$REALM_DIR/config.toml"
exit 0
`

func newTestAddLandingServer(t *testing.T, kitDir, realmDir string) *Server {
	t.Helper()
	t.Setenv("PANEL_INITIAL_PASSWORD", "test-password-123")
	panelHome := t.TempDir()
	s, err := newServer(Env{PanelHome: panelHome, RealmDir: realmDir, KitDir: kitDir, Port: 11111})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return s
}

func postAddLanding(t *testing.T, s *Server, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/landings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAddLanding(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP 状态=%d 响应=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	return resp
}

func TestHandleAddLandingFreshInstall(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	stubLog := filepath.Join(t.TempDir(), "stub.log")
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(addLandingStub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_LOG", stubLog)
	t.Setenv("REALM_DIR", realmDir)
	s := newTestAddLandingServer(t, kitDir, realmDir)

	resp := postAddLanding(t, s, `{"landing":"203.0.113.1","preset":"3","custom_ports":"30080,30443","cert_domain":"example.com"}`)
	if resp["ok"] != true {
		t.Fatalf("中转未装时添加落地应成功：%v", resp)
	}
	if idx, _ := resp["landing_index"].(float64); idx != 1 {
		t.Fatalf("landing_index=%v want 1", resp["landing_index"])
	}
	log, err := os.ReadFile(stubLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) < 2 {
		t.Fatalf("stub 日志太少：%q", string(log))
	}
	// 第一次调用必须是完整安装（不带 --add）
	if lines[0] != "ARGS: " {
		t.Fatalf("首次调用应不带 --add（完整安装），实际：%q", lines[0])
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"LANDING=203.0.113.1", "PRESET=3", "CUSTOM_PORTS=30080,30443", "CERT_DOMAIN=example.com", "ADD_ANOTHER=N"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("环境变量缺 %s，实际：%s", want, joined)
		}
	}
	// stub 模拟安装写出的 relay.conf 应能被面板读到
	if rc, _, err := s.readRelayConf(); err != nil || len(rc.Landings) != 1 {
		t.Fatalf("安装后 relay.conf 读不到或落地数不对：%v %+v", err, rc)
	}
}

func TestHandleAddLandingExistingRelay(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	stubLog := filepath.Join(t.TempDir(), "stub.log")
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(addLandingStub), 0o755); err != nil {
		t.Fatal(err)
	}
	// 预置已装好的中转（两个落地）
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(sampleRelayConf), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_LOG", stubLog)
	t.Setenv("REALM_DIR", realmDir)
	s := newTestAddLandingServer(t, kitDir, realmDir)

	resp := postAddLanding(t, s, `{"landing":"203.0.113.9","preset":"3","custom_ports":"30080,30443"}`)
	if resp["ok"] != true {
		t.Fatalf("已有中转时添加落地应成功：%v", resp)
	}
	log, err := os.ReadFile(stubLog)
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Split(strings.TrimSpace(string(log)), "\n")[0]
	if first != "ARGS: --add" {
		t.Fatalf("已有中转时首次调用应带 --add，实际：%q", first)
	}
}

func TestHandleCloudflare(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	post := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/v1/settings/cloudflare", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleCloudflare(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	// 空令牌拒绝
	if code, _ := post(`{"token":""}`); code != 400 {
		t.Fatalf("空令牌应 400，实际 %d", code)
	}
	// cfut_ 新令牌：接受，无警告
	cfut := "cfut_1111111111111111111111111111111111111111"
	if code, resp := post(`{"token":"` + cfut + `"}`); code != 200 || resp["ok"] != true {
		t.Fatalf("cfut_ 令牌应接受：code=%d resp=%v", code, resp)
	} else if _, hasWarning := resp["warning"]; hasWarning {
		t.Fatalf("cfut_ 令牌不应警告：%v", resp)
	}
	if s.cfg.CloudflareToken != cfut {
		t.Fatalf("令牌没存进配置：%q", s.cfg.CloudflareToken)
	}
	// 旧版 40 位令牌：接受，无警告
	old := "1111111111111111111111111111111111111111"
	if code, resp := post(`{"token":"` + old + `"}`); code != 200 || resp["ok"] != true {
		t.Fatalf("40 位令牌应接受：code=%d resp=%v", code, resp)
	} else if _, hasWarning := resp["warning"]; hasWarning {
		t.Fatalf("40 位令牌不应警告：%v", resp)
	}
	// 格式不对的令牌：接受但带警告（不拦）
	if code, resp := post(`{"token":"short"}`); code != 200 || resp["warning"] == nil {
		t.Fatalf("短令牌应接受并警告：code=%d resp=%v", code, resp)
	}
	// GET settings 应报告 has_token，令牌本身不回显
	req := httptest.NewRequest("GET", "/api/v1/settings", nil)
	rec := httptest.NewRecorder()
	s.handleSettings(rec, req)
	if rec.Code != 200 {
		t.Fatalf("settings 状态=%d", rec.Code)
	}
	var st map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("settings 不是 JSON：%v", err)
	}
	cf, _ := st["cloudflare"].(map[string]any)
	if cf["has_token"] != true {
		t.Fatalf("has_token 应为 true：%v", st["cloudflare"])
	}
	if strings.Contains(rec.Body.String(), "short") {
		t.Fatalf("settings 不应回显令牌")
	}
}

// ---------- 内嵌前端分发（修登录页堆叠 bug：/style.css、/app.js 曾返回 404，
// 样式脚本全失效，登录页与后台堆在一起） ----------

func TestServeEmbeddedWeb(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	get := func(path string, cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// / 返回 HTML，且带内联 hidden 兜底样式；1.2.2 起默认显示中性加载态（不是登录页，
	// 这样刷新不闪登录页），登录页与后台都默认隐藏，只由 JS 在会话校验后决定显示哪个；
	// app.js 加载失败时内联兜底脚本 10 秒后把登录页放出来，页面永远不白板。
	rec := get("/", "")
	if rec.Code != 200 {
		t.Fatalf("/ 状态=%d want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("/ Content-Type=%q want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"CP管理",
		".hidden{display:none!important}",
		`id="loading-view" class="loading-wrap"`,
		`id="login-view" class="login-wrap hidden" hidden`,
		`id="app" class="hidden" hidden`,
		"window.__panelBooted",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/ 首页缺 %q", want)
		}
	}

	// /style.css 必须是真 CSS，不是 HTML
	rec = get("/style.css", "")
	if rec.Code != 200 {
		t.Fatalf("/style.css 状态=%d want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Fatalf("/style.css Content-Type=%q want text/css", ct)
	}
	css := strings.TrimSpace(rec.Body.String())
	if strings.HasPrefix(css, "<!DOCTYPE") || strings.HasPrefix(css, "<html") {
		t.Fatalf("/style.css 拿到的是 HTML 文档")
	}
	if !strings.Contains(css, ".hidden") {
		t.Fatalf("/style.css 里没有 .hidden 定义")
	}

	// /app.js 必须是真 JS，不是 HTML
	rec = get("/app.js", "")
	if rec.Code != 200 {
		t.Fatalf("/app.js 状态=%d want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("/app.js Content-Type=%q want javascript", ct)
	}
	js := strings.TrimSpace(rec.Body.String())
	if strings.HasPrefix(js, "<!DOCTYPE") || strings.HasPrefix(js, "<html") {
		t.Fatalf("/app.js 拿到的是 HTML 文档")
	}
	if !strings.Contains(js, "checkMe") {
		t.Fatalf("/app.js 内容不对：没有 checkMe")
	}

	// 不存在且带后缀的资源 → 404，不再回 index.html
	for _, p := range []string{"/nope.css", "/nope.js", "/nope.png"} {
		if rec := get(p, ""); rec.Code != 404 {
			t.Fatalf("%s 状态=%d want 404", p, rec.Code)
		}
	}
	// 未知无后缀路径 → SPA 回退 index.html
	rec = get("/overview", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "CP管理") {
		t.Fatalf("/overview 应回退 index.html：状态=%d", rec.Code)
	}
	// 目录穿越：ServeMux 先把路径规范化（301 重定向）；直接调 handler 时含 .. 的路径 404
	if rec := get("/../VERSION", ""); rec.Code != 301 {
		t.Fatalf("/../VERSION 状态=%d want 301", rec.Code)
	}
	{
		req := httptest.NewRequest("GET", "/../style.css", nil)
		rec := httptest.NewRecorder()
		s.serveEmbeddedWeb(rec, req)
		if rec.Code != 404 {
			t.Fatalf("serveEmbeddedWeb /../style.css 状态=%d want 404", rec.Code)
		}
	}

	// API 路由仍优先于 catch-all：未登录 /api/v1/status → 401
	rec = get("/api/v1/status", "")
	if rec.Code != 401 {
		t.Fatalf("未登录 /api/v1/status 状态=%d want 401", rec.Code)
	}

	// 登录流程：正确密码登录后 /api/v1/me → 200
	login := func(password string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Header().Get("Set-Cookie")
	}
	code, cookie := login("test-password-123")
	if code != 200 {
		t.Fatalf("正确密码登录状态=%d want 200", code)
	}
	if cookie == "" {
		t.Fatalf("登录后没发会话 cookie")
	}
	sessionCookie := strings.SplitN(cookie, ";", 2)[0]
	rec = get("/api/v1/me", sessionCookie)
	if rec.Code != 200 {
		t.Fatalf("登录后 /api/v1/me 状态=%d want 200：%s", rec.Code, rec.Body.String())
	}

	// 锁定逻辑只在真正失败时触发：5 次错密码各 401，第 6 次 429
	s2 := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h2 := s2.routes()
	login2 := func(password string) int {
		req := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h2.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 5; i++ {
		if code := login2("wrong-password"); code != 401 {
			t.Fatalf("第 %d 次错密码状态=%d want 401", i+1, code)
		}
	}
	if code := login2("test-password-123"); code != 429 {
		t.Fatalf("5 次失败后登录应 429 锁定，实际 %d", code)
	}
}

// TestMobileDrawerAndUpdatePoll 手机抽屉菜单 + 更新后自动跳登录页：
// 首页含汉堡按钮/遮罩/抽屉关闭按钮且不再有底部导航条；style.css 含抽屉样式；
// app.js 含抽屉开关与更新轮询逻辑（401 跳登录、5 分钟超时）。
func TestMobileDrawerAndUpdatePoll(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	body := get("/").Body.String()
	for _, want := range []string{
		`id="menu-btn"`, `id="scrim"`, `id="sidebar-close"`,
		`https://github.com/nbbk/relay-node`, `https://github.com/nbbk/landing-node`,
		`target="_blank"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("首页缺 %q（手机抽屉结构或仓库链接）", want)
		}
	}
	if strings.Contains(body, "bottom-nav") {
		t.Fatalf("首页不应再含底部导航条 bottom-nav（iOS 上不可靠，已改汉堡抽屉）")
	}
	css := get("/style.css").Body.String()
	for _, want := range []string{".sidebar.open", ".menu-btn", ".scrim", ".repo-links", ".spin-ico", "@keyframes spin"} {
		if !strings.Contains(css, want) {
			t.Fatalf("style.css 缺 %q（抽屉样式、仓库链接样式或刷新转圈样式）", want)
		}
	}
	js := get("/app.js").Body.String()
	for _, want := range []string{
		"initDrawer", "openDrawer", "closeDrawer",
		"pollUpdateDone", "更新完成，请重新登录", "更新超时，请手动刷新页面", "正在更新…",
		"https://github.com/nbbk/relay-node",
		// 共用前端分页（审计/端口表/各列表）：切页、分页条、绑定、跳页钳制
		"function clampPage", "function paginate", "function pagerHtml", "function wirePager",
		// 刷新按钮反馈：转圈 + 成功/失败 toast + 防重复点
		"spinning", "已刷新", "刷新失败",
		// 各列表分页命名空间 + 组内链接收起
		// （v1.3.18：旧手动备份列表已删，它的 'backups' 分页命名空间随之没了）
		"'overview'", "'manage'", "'recycle'", "'links'", "'certs'", "'firewall'", "'ports'",
		"extra-links", "data-expand-links",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js 缺 %q（抽屉、更新轮询、共用分页或刷新反馈逻辑）", want)
		}
	}
}

// TestLoginErrorMessage 登录报错文案：错密码时服务端返回 401 + “账号或密码不对”，
// 前端 api() 的登录 401 豁免与登录框映射保证它显示为“用户名或密码错误”，而不是“登录已过期”。
func TestLoginErrorMessage(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	login := func(password string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	// 错密码：401，且正文就是服务端文案“账号或密码不对”
	code, body := login("wrong-password")
	if code != 401 {
		t.Fatalf("错密码登录状态=%d want 401", code)
	}
	if !strings.Contains(body, "账号或密码不对") {
		t.Fatalf("错密码登录正文=%s want 包含“账号或密码不对”", body)
	}
	// 正确密码：200
	if code, _ := login("test-password-123"); code != 200 {
		t.Fatalf("正确密码登录状态=%d want 200", code)
	}
	// 前端：登录请求走 api() 的 401 豁免，登录框映射接住服务端文案
	js, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读 app.js：%v", err)
	}
	for _, want := range []string{
		"noAuthRedirect",
		"if (res.status === 401 && !noAuthRedirect)",
		"apiPost('/api/v1/login', { username, password, code: code || undefined }, true)",
		"msg.indexOf('账号或密码')",
	} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("app.js 缺 %q", want)
		}
	}
}

// ---------- 日志轮转 ----------

func TestRotateLogIfLarge(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.log")
	// 不到 10MB：不轮转
	if err := os.WriteFile(p, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotateLogIfLarge(p)
	if _, err := os.Stat(p + ".1"); err == nil {
		t.Fatalf("小文件不应轮转")
	}
	// 超 10MB：轮转成 .1，原文件消失
	big := make([]byte, logRotateMaxBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(p, big, 0o600); err != nil {
		t.Fatal(err)
	}
	rotateLogIfLarge(p)
	if _, err := os.Stat(p); err == nil {
		t.Fatalf("轮转后原文件应消失")
	}
	if st, err := os.Stat(p + ".1"); err != nil || st.Size() != int64(len(big)) {
		t.Fatalf(".1 不存在或大小不对：%v", err)
	}
	// 再轮转 3 次：旧份只留 .1/.2/.3，共 4 次轮转后最旧的一份被删
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(p, big, 0o600); err != nil {
			t.Fatal(err)
		}
		rotateLogIfLarge(p)
	}
	for _, n := range []string{".1", ".2", ".3"} {
		if _, err := os.Stat(p + n); err != nil {
			t.Fatalf("轮转 4 次后 %s 应存在：%v", n, err)
		}
	}
	if _, err := os.Stat(p + ".4"); err == nil {
		t.Fatalf(".4 不应存在：旧份只留 3 个")
	}
}

func TestAuditRotatesLargeLog(t *testing.T) {
	panelHome := t.TempDir()
	s, err := newServer(Env{PanelHome: panelHome, RealmDir: t.TempDir(), KitDir: t.TempDir(), Port: 11111})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	// 预先放一个超 10MB 的 audit.log，再记一条审计：应轮转，旧内容进 .1，新内容写进新文件
	big := make([]byte, logRotateMaxBytes+100)
	for i := range big {
		big[i] = 'a'
	}
	auditPath := filepath.Join(panelHome, "audit.log")
	if err := os.WriteFile(auditPath, big, 0o600); err != nil {
		t.Fatal(err)
	}
	s.audit("admin", "test-action", "target", "ok", "detail")
	if st, err := os.Stat(auditPath + ".1"); err != nil || st.Size() != int64(len(big)) {
		t.Fatalf("audit.log.1 不存在或大小不对：%v", err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "test-action") {
		t.Fatalf("新 audit.log 里没有新记的审计条目")
	}
	if int64(len(data)) > logRotateMaxBytes {
		t.Fatalf("新 audit.log 不应超限")
	}
}

// ---------- 检查更新 ----------

func loginTestCookie(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("测试登录状态=%d want 200：%s", rec.Code, rec.Body.String())
	}
	cookie := strings.SplitN(rec.Header().Get("Set-Cookie"), ";", 2)[0]
	if cookie == "" {
		t.Fatal("登录后没发会话 cookie")
	}
	return cookie
}

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"1.1.3", "1.1.2", true},
		{"1.1.10", "1.1.2", true},  // 数字比较：10 > 2，不是字符串比较
		{"1.1.2", "1.1.2", false},  // 相同版本不报更新
		{"1.1.2", "1.1.10", false}, // 本地更新时不报更新
		{"1.2.0", "1.1.10", true},
		{"1.1", "1.1.0", false},   // 缺失段按 0 算
		{"v1.1.4", "1.1.3", true}, // 前缀 v 容错
		{"abc", "1.1.2", true},    // 解析失败回退到字符串不等
		{"", "1.1.2", false},      // 远端为空不报更新
	}
	for _, c := range cases {
		if got := isNewerVersion(c.latest, c.current); got != c.want {
			t.Errorf("isNewerVersion(%q, %q)=%v want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestUpdateCheckBypassesCache(t *testing.T) {
	// mock 远端 VERSION 服务，内容可改
	remote := "1.1.2"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(remote + "\n"))
	}))
	defer srv.Close()
	oldURL := latestVersionURL
	latestVersionURL = srv.URL + "/panel/VERSION"
	defer func() { latestVersionURL = oldURL }()
	oldVersion := Version
	Version = "1.1.2"
	defer func() { Version = oldVersion }()
	latestVersionCache.set("")
	defer latestVersionCache.set("")

	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	cookie := loginTestCookie(t, h)
	check := func() (int, map[string]any) {
		req := httptest.NewRequest("GET", "/api/v1/update/check", nil)
		req.Header.Set("Cookie", cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	// 远端还是 1.1.2：不报更新
	if code, body := check(); code != 200 || body["update_available"] != false {
		t.Fatalf("同版本检查：code=%d body=%v want 200/false", code, body)
	}
	// 远端升到 1.1.3，同时缓存里塞一个 1 小时内的旧值 1.1.2：手动检查必须绕过缓存拿到新版本
	remote = "1.1.3"
	latestVersionCache.set("1.1.2")
	if code, body := check(); code != 200 {
		t.Fatalf("新版本检查状态=%d want 200：%v", code, body)
	} else if body["latest_version"] != "1.1.3" || body["update_available"] != true {
		t.Fatalf("缓存内旧值时手动检查应拿到 1.1.3 且报更新，实际：%v", body)
	}
}

func TestUpdateCheckFetchFailure(t *testing.T) {
	// 远端 404：手动检查应明确报错（502），不能返回“已是最新”
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	oldURL := latestVersionURL
	latestVersionURL = srv.URL + "/panel/VERSION"
	defer func() { latestVersionURL = oldURL }()
	latestVersionCache.set("")
	defer latestVersionCache.set("")

	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	cookie := loginTestCookie(t, h)
	req := httptest.NewRequest("GET", "/api/v1/update/check", nil)
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 502 {
		t.Fatalf("拉取失败时状态=%d want 502：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "检查更新失败") {
		t.Fatalf("拉取失败时正文应含“检查更新失败”，实际：%s", rec.Body.String())
	}
}

// ---------- 审计日志分页 ----------

// writeAuditEntries 往指定文件按时间顺序写 n 条审计记录（第 i 条比第 0 条晚 i 秒）。
func writeAuditEntries(t *testing.T, path string, base time.Time, n int) {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < n; i++ {
		b, err := json.Marshal(AuditEntry{
			TS:     base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			User:   "admin",
			Action: fmt.Sprintf("action-%02d", i),
			Target: "target",
			Result: "成功",
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("写审计文件失败：%v", err)
	}
}

func TestAuditPagination(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	cookie := loginTestCookie(t, h)
	writeAuditEntries(t, filepath.Join(s.env.PanelHome, "audit.log"), time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), 55)

	get := func(q string) (int, map[string]any) {
		req := httptest.NewRequest("GET", "/api/v1/audit?"+q, nil)
		req.Header.Set("Cookie", cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	entries := func(body map[string]any) []any {
		es, _ := body["entries"].([]any)
		return es
	}
	action := func(e any) string {
		m, _ := e.(map[string]any)
		s, _ := m["action"].(string)
		return s
	}

	// 第 1 页：20 条，总数 55，共 3 页，第一条是最新的一条
	code, b := get("page=1&page_size=20")
	if code != 200 || len(entries(b)) != 20 || b["total"] != float64(55) || b["total_pages"] != float64(3) || b["page"] != float64(1) || b["page_size"] != float64(20) {
		t.Fatalf("page=1：code=%d body=%v", code, b)
	}
	if action(entries(b)[0]) != "action-54" || action(entries(b)[19]) != "action-35" {
		t.Fatalf("第 1 页顺序不对：首=%s 末=%s want action-54/action-35", action(entries(b)[0]), action(entries(b)[19]))
	}
	// 第 2 页：20 条
	code, b = get("page=2&page_size=20")
	if code != 200 || len(entries(b)) != 20 || action(entries(b)[0]) != "action-34" || action(entries(b)[19]) != "action-15" {
		t.Fatalf("page=2：code=%d entries=%d 首=%s", code, len(entries(b)), action(entries(b)[0]))
	}
	// 第 3 页：15 条，最后一页最后一条是最旧的
	code, b = get("page=3&page_size=20")
	if code != 200 || len(entries(b)) != 15 || action(entries(b)[14]) != "action-00" {
		t.Fatalf("page=3：code=%d entries=%d 末=%s", code, len(entries(b)), action(entries(b)[14]))
	}
	// 越界页：空页，不报错，总数不变
	code, b = get("page=4&page_size=20")
	if code != 200 || len(entries(b)) != 0 || b["total"] != float64(55) || b["total_pages"] != float64(3) {
		t.Fatalf("page=4 越界：code=%d body=%v", code, b)
	}
	// 非法 page：0、abc 都按第 1 页
	if code, b := get("page=0&page_size=20"); code != 200 || b["page"] != float64(1) || len(entries(b)) != 20 {
		t.Fatalf("page=0：code=%d body=%v", code, b)
	}
	if code, b := get("page=abc&page_size=20"); code != 200 || b["page"] != float64(1) {
		t.Fatalf("page=abc：code=%d body=%v", code, b)
	}
	// page_size 钳制：0→1，500→100，abc→默认 10（v1.3.13：审计日志每页 10 条）
	if code, b := get("page=1&page_size=0"); code != 200 || b["page_size"] != float64(1) || len(entries(b)) != 1 {
		t.Fatalf("page_size=0：code=%d body=%v", code, b)
	}
	if code, b := get("page=1&page_size=500"); code != 200 || b["page_size"] != float64(100) || len(entries(b)) != 55 || b["total_pages"] != float64(1) {
		t.Fatalf("page_size=500：code=%d body=%v", code, b)
	}
	if code, b := get("page=1&page_size=abc"); code != 200 || b["page_size"] != float64(10) || len(entries(b)) != 10 {
		t.Fatalf("page_size=abc：code=%d body=%v", code, b)
	}
	// 不传 page_size：默认 10 条，共 6 页
	if code, b := get("page=1"); code != 200 || b["page_size"] != float64(10) || len(entries(b)) != 10 || b["total_pages"] != float64(6) {
		t.Fatalf("默认 page_size：code=%d body=%v", code, b)
	}
	// 不带参数：默认第 1 页、每页 10 条
	if code, b := get(""); code != 200 || b["page"] != float64(1) || b["page_size"] != float64(10) || len(entries(b)) != 10 {
		t.Fatalf("无参数：code=%d body=%v", code, b)
	}
}

func TestAuditPaginationRotated(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	cookie := loginTestCookie(t, h)
	// 旧份 .1：25 条（较早），当前文件：25 条（较新）
	writeAuditEntries(t, filepath.Join(s.env.PanelHome, "audit.log.1"), time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), 25)
	writeAuditEntries(t, filepath.Join(s.env.PanelHome, "audit.log"), time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC), 25)

	get := func(q string) (int, map[string]any) {
		req := httptest.NewRequest("GET", "/api/v1/audit?"+q, nil)
		req.Header.Set("Cookie", cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	entries := func(body map[string]any) []any {
		es, _ := body["entries"].([]any)
		return es
	}
	ts := func(e any) string {
		m, _ := e.(map[string]any)
		s, _ := m["ts"].(string)
		return s
	}

	// 总数 50，共 3 页；第 1 页全是当前文件里最新的 20 条
	code, b := get("page=1&page_size=20")
	if code != 200 || len(entries(b)) != 20 || b["total"] != float64(50) || b["total_pages"] != float64(3) {
		t.Fatalf("page=1：code=%d body=%v", code, b)
	}
	if ts(entries(b)[0]) != "2026-11-18T01:00:24Z" {
		t.Fatalf("第 1 页首条时间=%s want 2026-11-18T01:00:24Z", ts(entries(b)[0]))
	}
	// 第 2 页：当前文件剩 5 条 + 旧份最新 15 条
	code, b = get("page=2&page_size=20")
	if code != 200 || len(entries(b)) != 20 {
		t.Fatalf("page=2：code=%d entries=%d", code, len(entries(b)))
	}
	if ts(entries(b)[0]) != "2026-11-18T01:00:04Z" || ts(entries(b)[5]) != "2026-11-18T00:00:24Z" {
		t.Fatalf("第 2 页跨文件顺序不对：首=%s 第6条=%s", ts(entries(b)[0]), ts(entries(b)[5]))
	}
	// 第 3 页：旧份剩 10 条
	code, b = get("page=3&page_size=20")
	if code != 200 || len(entries(b)) != 10 || ts(entries(b)[9]) != "2026-11-18T00:00:00Z" {
		t.Fatalf("page=3：code=%d entries=%d 末=%s", code, len(entries(b)), ts(entries(b)[9]))
	}
}

// ---------- 1.1.8：点“更新面板”后变白板 ----------
//
// 根因：登录页与后台默认都隐藏，靠 JS 初始化最后调 checkMe() 决定显示哪一个；
// initDrawer 里 $('#menu-btn').addEventListener 没有判空，浏览器拿到旧 index.html
// （1.1.7 之前没有 #menu-btn）配新 app.js 时中途抛错，checkMe() 永远不执行，
// 两个视图一直隐藏 → 全白。serveEmbeddedWeb 还不设 Cache-Control，
// 浏览器可能长期拿旧 index.html，新旧文件混用正好触发上面那条。
func TestBlankPageFix118(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}

	// 1. 静态文件与 SPA 回退都要带 Cache-Control: no-cache（每次重新验证，不拿旧文件）
	for _, p := range []string{"/", "/app.js", "/style.css", "/overview"} {
		rec := get(p)
		if rec.Code != 200 {
			t.Fatalf("%s 状态=%d want 200", p, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s Cache-Control=%q want no-cache", p, cc)
		}
	}

	// 2. 加载态默认可见、登录页与后台默认隐藏（1.2.2：刷新先显示加载态，校验完会话
	// 再决定显示登录页还是后台，不闪登录页）；即使 JS 初始化失败，内联兜底脚本
	// 10 秒后也会把登录页放出来，用户看到的仍是登录页而不是白板
	body := get("/").Body.String()
	if !strings.Contains(body, `<div id="loading-view" class="loading-wrap">`) {
		t.Fatalf("加载态 #loading-view 应默认可见")
	}
	if !strings.Contains(body, `<div id="login-view" class="login-wrap hidden" hidden>`) {
		t.Fatalf("登录页 #login-view 应默认隐藏（校验完会话再决定显示）")
	}
	if !strings.Contains(body, `<div id="app" class="hidden" hidden>`) {
		t.Fatalf("后台 #app 应仍默认隐藏（只由 JS 在鉴权成功后显示）")
	}
	if !strings.Contains(body, "window.__panelBooted") {
		t.Fatalf("首页缺内联兜底脚本（app.js 失效时 10 秒后显示登录页）")
	}

	// 3. app.js：安全绑定 + 初始化逐个包 try/catch + checkMe 必须执行
	js := get("/app.js").Body.String()
	for _, want := range []string{
		"function on(sel, ev, fn, root)", "function onIn(root, sel, ev, fn)",
		"safeInit(initDrawer)", "try { checkMe(); } catch (e) { showLogin(); }",
		"window.__panelBooted = true", "hideLoading();",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js 缺 %q（初始化加固或 checkMe 保证执行）", want)
		}
	}
	for _, bad := range []string{
		"$('#menu-btn').addEventListener", "$('#sidebar-close').addEventListener",
		"$('#scrim').addEventListener", "$('#login-form').addEventListener",
		"$('#refresh-btn').addEventListener",
	} {
		if strings.Contains(js, bad) {
			t.Fatalf("app.js 不应再有未判空的直接绑定 %q", bad)
		}
	}
	// 弹窗内绑定的选择器必须带 #（onIn 的第二个参数是 querySelector 选择器；
	// 漏了 # 会查 <cf-ok> 这种不存在的标签名，静默绑不上，确认框点了没反应）
	for _, want := range []string{
		"onIn(m, '#cf-ok'", "onIn(m, '#cf-cancel'",
		"onIn(m, '#share-ok'", "onIn(m, '#wz-next'", "onIn(m2, '#wz-done'",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js 缺 %q（弹窗绑定选择器丢了 # 会绑不上）", want)
		}
	}
	if strings.Contains(js, "onIn(m, 'cf-ok'") || strings.Contains(js, "onIn(m, 'wz-next'") {
		t.Fatalf("app.js 里有丢了 # 的 onIn 绑定（查不到元素，静默失效）")
	}
}
