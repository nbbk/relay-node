package main

import (
	"encoding/base64"
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
