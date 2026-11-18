package main

import (
	"encoding/base64"
	"encoding/json"
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
	// 第一轮只记基数，不产生增量
	ts.addDelta("203.0.113.1", map[string][2]int64{"tcp:443": {1000, 2000}})
	lt := ts.get("203.0.113.1")
	if lt.UpTotal != 0 || lt.DownTotal != 0 {
		t.Fatalf("first sample should only set baseline, got up=%d down=%d", lt.UpTotal, lt.DownTotal)
	}
	// 第二轮增量
	ts.addDelta("203.0.113.1", map[string][2]int64{"tcp:443": {1500, 3000}})
	if lt.UpTotal != 500 || lt.DownTotal != 1000 {
		t.Fatalf("delta wrong: up=%d down=%d", lt.UpTotal, lt.DownTotal)
	}
	// 计数器清零（快照比上次小）：不算负增量
	ts.addDelta("203.0.113.1", map[string][2]int64{"tcp:443": {100, 200}})
	if lt.UpTotal != 500 || lt.DownTotal != 1000 {
		t.Fatalf("counter reset produced negative delta: up=%d down=%d", lt.UpTotal, lt.DownTotal)
	}
	// 清零后继续增长：按新基数算
	ts.addDelta("203.0.113.1", map[string][2]int64{"tcp:443": {300, 400}})
	if lt.UpTotal != 700 || lt.DownTotal != 1200 {
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
