package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1.1.9：任意门模式 ----------

const doorRelayConf = `RELAY_ADDR="203.0.113.9"
LANDING=203.0.113.1
LANDING_IS_DOMAIN=0
DOMAIN=""
CERT_DOMAIN="example.com"
PRESET=1
MODE_NOTE="tiktok-node"
MODE="door"
PORTS="tcp:20080:80 tcp:20443:443"
LANDING_COUNT=2
ALL_PORTS="tcp:20080:80 tcp:20443:443 tcp:80:80 tcp:443:443"
SHARE80=0
SHARE443=0
LANDING_2=203.0.113.2
DOMAIN_2="node2.example.com"
CERT_DOMAIN_2="node2.example.com"
PRESET_2=1
MODE_NOTE_2="tiktok-node"
MODE_2="standard"
PORTS_2="tcp:80:80 tcp:443:443"
`

func TestParseRelayConfDoor(t *testing.T) {
	rc := parseRelayConf([]byte(doorRelayConf))
	if rc.RelayAddr != "203.0.113.9" {
		t.Fatalf("RelayAddr=%q want 203.0.113.9", rc.RelayAddr)
	}
	if len(rc.Landings) != 2 {
		t.Fatalf("landings=%d want 2", len(rc.Landings))
	}
	if rc.Landings[0].Mode != "door" {
		t.Fatalf("landing1 mode=%q want door", rc.Landings[0].Mode)
	}
	if rc.Landings[1].Mode != "standard" {
		t.Fatalf("landing2 mode=%q want standard", rc.Landings[1].Mode)
	}
	// 旧 conf：没 MODE/RELAY_ADDR 时默认 standard、空地址（向后兼容）
	rc2 := parseRelayConf([]byte(sampleRelayConf))
	if rc2.RelayAddr != "" {
		t.Fatalf("old conf RelayAddr=%q want empty", rc2.RelayAddr)
	}
	for _, l := range rc2.Landings {
		if l.Mode != "standard" {
			t.Fatalf("old conf landing %d mode=%q want standard", l.Index, l.Mode)
		}
	}
	// fieldKeys 含 MODE 后缀键（删落地时一起清掉）
	keys := rc.fieldKeys(2)
	found := false
	for _, k := range keys {
		if k == "MODE_2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fieldKeys(2) 缺 MODE_2：%v", keys)
	}
}

func TestDoorMapPorts(t *testing.T) {
	ports := parsePorts("tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388 udp:8443:8443 udp:8444:8444 tcp:8080:8080")
	mapped := doorMapPorts(ports)
	want := map[int]int{80: 20080, 443: 20443, 8388: 28388, 8443: 28443, 8444: 28444, 8080: 28080}
	if len(mapped) != len(ports) {
		t.Fatalf("mapped len=%d want %d", len(mapped), len(ports))
	}
	for i, p := range mapped {
		if want[p.Remote] != p.Listen {
			t.Errorf("remote %d -> listen %d want %d", p.Remote, p.Listen, want[p.Remote])
		}
		// remote 与 proto 不变
		if p.Remote != ports[i].Remote || p.Proto != ports[i].Proto {
			t.Errorf("spec %d changed remote/proto: %+v want %+v", i, p, ports[i])
		}
	}
	// 超限回退：45536+20000=65536 超了，+10000=55536
	m := doorMapPorts([]PortSpec{{Proto: "tcp", Listen: 45536, Remote: 45536}})
	if m[0].Listen != 55536 {
		t.Fatalf("45536 -> %d want 55536 (+10000 回退)", m[0].Listen)
	}
	// 50000+20000 超了，+10000=60000
	m = doorMapPorts([]PortSpec{{Proto: "tcp", Listen: 50000, Remote: 50000}})
	if m[0].Listen != 60000 {
		t.Fatalf("50000 -> %d want 60000", m[0].Listen)
	}
	// 65000：+20000/+10000/+1000 全超，保留原值（install.sh 随后会明确报错）
	m = doorMapPorts([]PortSpec{{Proto: "tcp", Listen: 65000, Remote: 65000}})
	if m[0].Listen != 65000 {
		t.Fatalf("65000 -> %d want 65000 (keep)", m[0].Listen)
	}
	// 自定义“监听:落地”：映射按落地端口（remote）算
	m = doorMapPorts([]PortSpec{{Proto: "tcp", Listen: 10443, Remote: 443}})
	if m[0].Listen != 20443 || m[0].Remote != 443 {
		t.Fatalf("10443:443 -> %+v want listen 20443 remote 443", m[0])
	}
}

func TestParseNodesTxtDoor(t *testing.T) {
	data := "# relay-node 双节点\n\n## 落地 1：203.0.113.1（tiktok-node）\n\n[第 1 条]\n任意门：vless://11111111-2222-3333-4444-555555555555@example.com:20443?security=tls#test\n直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls#test-直连\n"
	groups := parseNodesTxt([]byte(data))
	if len(groups) != 1 {
		t.Fatalf("groups=%d want 1", len(groups))
	}
	if len(groups[0].Links) != 2 {
		t.Fatalf("links=%v want 2", groups[0].Links)
	}
	if groups[0].Links[0].Label != "任意门" || !strings.Contains(groups[0].Links[0].Link, ":20443") {
		t.Fatalf("door link wrong: %+v", groups[0].Links[0])
	}
	if groups[0].Links[1].Label != "直连" {
		t.Fatalf("second link label=%q want 直连", groups[0].Links[1].Label)
	}
}

func TestRenewModeForDoor(t *testing.T) {
	l := Landing{Mode: "door", Ports: parsePorts("tcp:20443:443")}
	mode, manual := renewModeFor(l, false)
	if mode != "自动（域名指落地，直接续签）" || manual {
		t.Fatalf("door renew=(%q,%v) want (自动（域名指落地，直接续签）,false)", mode, manual)
	}
	// 标准模式不受影响：没配 80、443 又改了监听端口 → 手动
	l2 := Landing{Mode: "standard", Ports: parsePorts("tcp:20443:443")}
	mode2, manual2 := renewModeFor(l2, false)
	if !manual2 || !strings.HasPrefix(mode2, "手动") {
		t.Fatalf("standard renew=(%q,%v) want manual", mode2, manual2)
	}
}

func TestSetLinkHost(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	newHost := "203.0.113.9"
	// vless：换地址，端口/SNI/名字不动
	vless := "vless://" + uuid + "@example.com:443?encryption=none&security=tls&sni=example.com&type=ws&path=%2Fexample-path#test"
	got := setLinkHost(vless, newHost)
	pl, err := parseNodeLink(got)
	if err != nil {
		t.Fatalf("vless setLinkHost 后解析失败：%v（%s）", err, got)
	}
	if pl.Host != newHost || pl.Port != 443 || pl.SNI != "example.com" {
		t.Fatalf("vless parsed=%+v want host=%s port=443 sni=example.com", pl, newHost)
	}
	if !strings.HasSuffix(got, "#test") || !strings.Contains(got, "path=%2Fexample-path") {
		t.Fatalf("vless 名字/路径丢了：%s", got)
	}
	// trojan
	trojan := "trojan://password@example.com:443?security=tls&sni=example.com#test"
	got = setLinkHost(trojan, newHost)
	pl, err = parseNodeLink(got)
	if err != nil || pl.Host != newHost || pl.Port != 443 {
		t.Fatalf("trojan parsed=%+v err=%v", pl, err)
	}
	// ss SIP002
	ss2 := "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388#test"
	got = setLinkHost(ss2, newHost)
	pl, err = parseNodeLink(got)
	if err != nil || pl.Host != newHost || pl.Port != 8388 {
		t.Fatalf("ss SIP002 parsed=%+v err=%v", pl, err)
	}
	if !strings.HasSuffix(got, "#test") {
		t.Fatalf("ss SIP002 名字丢了：%s", got)
	}
	// ss 传统 base64：直接解码验证（parseNodeLink 本身把 ss://BASE64 当 host，
	// 这是既有行为；这里只验证 setLinkHost 换对了地址）
	raw := "aes-256-gcm:password@example.com:8388"
	ssLegacy := "ss://" + base64.RawURLEncoding.EncodeToString([]byte(raw)) + "#test"
	got = setLinkHost(ssLegacy, newHost)
	if !strings.HasSuffix(got, "#test") {
		t.Fatalf("ss legacy 名字丢了：%s", got)
	}
	b64part := strings.TrimPrefix(got, "ss://")
	b64part = strings.SplitN(b64part, "#", 2)[0]
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(b64part, "="))
	if err != nil {
		t.Fatalf("ss legacy 结果解不回 base64：%v（%s）", err, got)
	}
	if string(decoded) != "aes-256-gcm:password@203.0.113.9:8388" {
		t.Fatalf("ss legacy 解出来=%q want aes-256-gcm:password@203.0.113.9:8388", string(decoded))
	}
	// vmess：改 add，端口/SNI 不动
	vj := `{"v":"2","ps":"test","add":"example.com","port":"443","id":"` + uuid + `","aid":"0","net":"ws","type":"none","host":"example.com","path":"/example-path","tls":"tls","sni":"example.com"}`
	vmess := "vmess://" + base64.RawURLEncoding.EncodeToString([]byte(vj)) + "#test"
	got = setLinkHost(vmess, newHost)
	pl, err = parseNodeLink(got)
	if err != nil || pl.Host != newHost || pl.Port != 443 || pl.SNI != "example.com" {
		t.Fatalf("vmess parsed=%+v err=%v", pl, err)
	}
	if !strings.HasSuffix(got, "#test") {
		t.Fatalf("vmess 名字丢了：%s", got)
	}
	// 换不了原样返回
	if setLinkHost("not-a-link", newHost) != "not-a-link" {
		t.Fatalf("非法链接应原样返回")
	}
	if setLinkHost(vless, "") != vless {
		t.Fatalf("空地址应原样返回")
	}
	// 域名新地址也行
	got = setLinkHost(vless, "relay.example.com")
	pl, err = parseNodeLink(got)
	if err != nil || pl.Host != "relay.example.com" {
		t.Fatalf("domain host parsed=%+v err=%v", pl, err)
	}
}

func TestRebuildRelayConfAfterDeletePreservesMode(t *testing.T) {
	rc := parseRelayConf([]byte(doorRelayConf))
	lines := rebuildRelayConfAfterDelete(rc, 1)
	rc2 := parseRelayConf([]byte(strings.Join(lines, "\n")))
	if len(rc2.Landings) != 1 || rc2.Landings[0].Landing != "203.0.113.2" {
		t.Fatalf("after delete: %+v", rc2.Landings)
	}
	if rc2.Landings[0].Mode != "standard" {
		t.Fatalf("剩余落地 mode=%q want standard", rc2.Landings[0].Mode)
	}
	// 删标准落地，door 落地前移后模式不能丢
	rc3 := parseRelayConf([]byte(doorRelayConf))
	lines3 := rebuildRelayConfAfterDelete(rc3, 2)
	rc4 := parseRelayConf([]byte(strings.Join(lines3, "\n")))
	if len(rc4.Landings) != 1 || rc4.Landings[0].Mode != "door" {
		t.Fatalf("删 2 号后剩余落地 mode=%q want door：%+v", rc4.Landings[0].Mode, rc4.Landings)
	}
	// 全局 RELAY_ADDR 不应被删落地带走
	if rc4.RelayAddr != "203.0.113.9" {
		t.Fatalf("RelayAddr=%q want 203.0.113.9", rc4.RelayAddr)
	}
}

// ---------- 任意门地址设置 ----------

const doorNodesTxt = `# relay-node 双节点

## 落地 1：203.0.113.1（tiktok-node）

[第 1 条]
任意门：vless://11111111-2222-3333-4444-555555555555@example.com:20443?security=tls&sni=example.com#test-任意门
直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls&sni=example.com#test-直连
`

func TestHandleRelayAddr(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(doorRelayConf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kitDir, "nodes.txt"), []byte(doorNodesTxt), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestAddLandingServer(t, kitDir, realmDir)
	post := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/v1/settings/relay-addr", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleRelayAddr(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	// 非法地址拒绝
	if code, _ := post(`{"relay_addr":"not a host"}`); code != 400 {
		t.Fatalf("非法地址应 400，实际 %d", code)
	}
	// 设置地址：relay.conf 写进 RELAY_ADDR，nodes.txt 任意门链接地址跟着改（端口不动），直连不动
	code, resp := post(`{"relay_addr":"203.0.113.9"}`)
	if code != 200 || resp["ok"] != true || resp["relay_addr"] != "203.0.113.9" {
		t.Fatalf("设置地址：code=%d resp=%v", code, resp)
	}
	conf, _ := os.ReadFile(filepath.Join(realmDir, "relay.conf"))
	if !strings.Contains(string(conf), `RELAY_ADDR="203.0.113.9"`) {
		t.Fatalf("relay.conf 里没有新 RELAY_ADDR：\n%s", string(conf))
	}
	nodes, _ := os.ReadFile(filepath.Join(kitDir, "nodes.txt"))
	if !strings.Contains(string(nodes), "任意门：vless://11111111-2222-3333-4444-555555555555@203.0.113.9:20443") {
		t.Fatalf("nodes.txt 任意门链接地址没改：\n%s", string(nodes))
	}
	if !strings.Contains(string(nodes), "直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443") {
		t.Fatalf("nodes.txt 直连链接不应被改：\n%s", string(nodes))
	}
	// GET settings 应带 relay_addr
	req := httptest.NewRequest("GET", "/api/v1/settings", nil)
	rec := httptest.NewRecorder()
	s.handleSettings(rec, req)
	var st map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st["relay_addr"] != "203.0.113.9" {
		t.Fatalf("settings relay_addr=%v want 203.0.113.9", st["relay_addr"])
	}
	// 清除：relay.conf 里删掉 RELAY_ADDR 键；nodes.txt 任意门链接不再重写（保持上次地址）
	code, resp = post(`{"relay_addr":""}`)
	if code != 200 || resp["relay_addr"] != "" {
		t.Fatalf("清除地址：code=%d resp=%v", code, resp)
	}
	conf, _ = os.ReadFile(filepath.Join(realmDir, "relay.conf"))
	if strings.Contains(string(conf), "RELAY_ADDR") {
		t.Fatalf("清除后 relay.conf 里不应再有 RELAY_ADDR：\n%s", string(conf))
	}
	nodes, _ = os.ReadFile(filepath.Join(kitDir, "nodes.txt"))
	if !strings.Contains(string(nodes), "@203.0.113.9:20443") {
		t.Fatalf("清除时 nodes.txt 不应再改：\n%s", string(nodes))
	}
	// 审计应记一条
	audit, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if !strings.Contains(string(audit), "设置任意门地址") {
		t.Fatalf("审计里没有“设置任意门地址”：\n%s", string(audit))
	}
}

// ---------- 添加落地：任意门模式 ----------

const addLandingDoorStub = `#!/bin/bash
echo "ARGS: $*" >> "$STUB_LOG"
echo "ENV: RELAY_MODE=${RELAY_MODE:-} RELAY_ADDR=${RELAY_ADDR:-} DOOR_PORT_OVERRIDES=${DOOR_PORT_OVERRIDES:-}" >> "$STUB_LOG"
if [ "$1" = "--nodes" ]; then exit 0; fi
mkdir -p "$REALM_DIR"
cat > "$REALM_DIR/relay.conf" <<'DOOR_STUB_EOF'
LANDING=203.0.113.1
LANDING_IS_DOMAIN=0
DOMAIN=""
CERT_DOMAIN="example.com"
PRESET=1
MODE_NOTE="tiktok-node"
MODE="door"
PORTS="tcp:20080:80 tcp:20443:443"
LANDING_COUNT=1
ALL_PORTS="tcp:20080:80 tcp:20443:443"
SHARE80=0
SHARE443=0
DOOR_STUB_EOF
touch "$REALM_DIR/config.toml"
exit 0
`

func TestHandleAddLandingDoor(t *testing.T) {
	// 全新安装 + door：env 透传 RELAY_MODE/RELAY_ADDR，无冲突时 door_note 为空
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	stubLog := filepath.Join(t.TempDir(), "stub.log")
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(addLandingDoorStub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_LOG", stubLog)
	t.Setenv("REALM_DIR", realmDir)
	s := newTestAddLandingServer(t, kitDir, realmDir)
	resp := postAddLanding(t, s, `{"landing":"203.0.113.1","preset":"1","mode":"door","relay_addr":"203.0.113.9"}`)
	if resp["ok"] != true {
		t.Fatalf("door 添加落地应成功：%v", resp)
	}
	if resp["door_note"] != "" {
		t.Fatalf("无冲突时 door_note 应为空，实际 %v", resp["door_note"])
	}
	log, _ := os.ReadFile(stubLog)
	if !strings.Contains(string(log), "RELAY_MODE=door") || !strings.Contains(string(log), "RELAY_ADDR=203.0.113.9") {
		t.Fatalf("install.sh 没收到 RELAY_MODE/RELAY_ADDR：\n%s", string(log))
	}
	if lines := strings.Split(strings.TrimSpace(string(log)), "\n"); lines[0] != "ARGS: " {
		t.Fatalf("全新安装应不带 --add，实际：%q", lines[0])
	}
}

func TestHandleAddLandingDoorBadMode(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(addLandingDoorStub), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestAddLandingServer(t, kitDir, realmDir)
	req := httptest.NewRequest("POST", "/api/v1/landings", strings.NewReader(`{"landing":"203.0.113.1","preset":"1","mode":"weird"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAddLanding(rec, req)
	if rec.Code != 400 {
		t.Fatalf("非法 mode 应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAddLandingDoorConflictBump(t *testing.T) {
	// 已有落地占了 20443：door 映射后冲突，没给覆盖就自动顺延 +10000，写进 DOOR_PORT_OVERRIDES
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	stubLog := filepath.Join(t.TempDir(), "stub.log")
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(addLandingDoorStub), 0o755); err != nil {
		t.Fatal(err)
	}
	preConf := "LANDING=203.0.113.2\nLANDING_IS_DOMAIN=0\nDOMAIN=\"\"\nCERT_DOMAIN=\"node2.example.com\"\nPRESET=1\nMODE_NOTE=\"tiktok-node\"\nMODE=\"standard\"\nPORTS=\"tcp:20443:443\"\nLANDING_COUNT=1\nALL_PORTS=\"tcp:20443:443\"\nSHARE80=0\nSHARE443=0\n"
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(preConf), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_LOG", stubLog)
	t.Setenv("REALM_DIR", realmDir)
	s := newTestAddLandingServer(t, kitDir, realmDir)
	resp := postAddLanding(t, s, `{"landing":"203.0.113.1","preset":"1","mode":"door","relay_addr":"203.0.113.9"}`)
	if resp["ok"] != true {
		t.Fatalf("冲突自动顺延后应成功：%v", resp)
	}
	if resp["door_note"] != "转发端口 20443 已被占用，已改用 30443" {
		t.Fatalf("door_note=%v want 转发端口 20443 已被占用，已改用 30443", resp["door_note"])
	}
	log, _ := os.ReadFile(stubLog)
	if !strings.Contains(string(log), "DOOR_PORT_OVERRIDES=443:30443") {
		t.Fatalf("install.sh 没收到顺延后的 DOOR_PORT_OVERRIDES=443:30443：\n%s", string(log))
	}
	if !strings.Contains(string(log), "RELAY_MODE=door") {
		t.Fatalf("install.sh 没收到 RELAY_MODE=door：\n%s", string(log))
	}
}

// ---------- twin：任意门模式 ----------

const twinDoorStub = `#!/bin/bash
echo "ARGS: $*" >> "$STUB_LOG"
if [ "$1" = "--twin" ]; then echo "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls&sni=example.com#test-直连"; exit 0; fi
if [ "$1" = "--door" ]; then echo "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:20443?security=tls&sni=example.com#test-任意门"; exit 0; fi
exit 0
`

func postTwin(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/links/twin", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleTwin(rec, req)
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("twin 响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	return rec.Code, resp
}

func TestHandleTwinDoor(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	stubLog := filepath.Join(t.TempDir(), "stub.log")
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(twinDoorStub), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "RELAY_ADDR=\"203.0.113.9\"\nLANDING=203.0.113.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"example.com\"\nCERT_DOMAIN=\"example.com\"\nPRESET=1\nMODE_NOTE=\"tiktok-node\"\nMODE=\"door\"\nPORTS=\"tcp:20443:443\"\nLANDING_COUNT=1\nALL_PORTS=\"tcp:20443:443\"\nSHARE80=0\nSHARE443=0\n"
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_LOG", stubLog)
	t.Setenv("REALM_DIR", realmDir)
	s := newTestAddLandingServer(t, kitDir, realmDir)
	link := "vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test"
	code, resp := postTwin(t, s, `{"link":"`+link+`","landing_ip":"203.0.113.1"}`)
	if code != 200 {
		t.Fatalf("twin 状态=%d resp=%v", code, resp)
	}
	if resp["relay_label"] != "任意门" {
		t.Fatalf("relay_label=%v want 任意门", resp["relay_label"])
	}
	rl, _ := resp["relay_link"].(string)
	if !strings.Contains(rl, "@203.0.113.9:20443") {
		t.Fatalf("relay_link=%q want 任意门地址+转发端口", rl)
	}
	dl, _ := resp["direct_link"].(string)
	if !strings.Contains(dl, "@203.0.113.1:443") {
		t.Fatalf("direct_link=%q want 落地直连", dl)
	}
	log, _ := os.ReadFile(stubLog)
	if !strings.Contains(string(log), "--door "+link+" 203.0.113.9 20443 example.com") {
		t.Fatalf("--door 参数不对：\n%s", string(log))
	}
	// 标准模式对照：relay_label=中转，不调 --door，relay_link 就是原链接
	confStd := strings.Replace(conf, "MODE=\"door\"\n", "", 1)
	confStd = strings.Replace(confStd, "tcp:20443:443", "tcp:443:443", 1)
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(confStd), 0o644); err != nil {
		t.Fatal(err)
	}
	code, resp = postTwin(t, s, `{"link":"`+link+`","landing_ip":"203.0.113.1"}`)
	if code != 200 || resp["relay_label"] != "中转" {
		t.Fatalf("标准 twin：code=%d resp=%v", code, resp)
	}
	if resp["relay_link"] != link {
		t.Fatalf("标准 relay_link=%v want 原链接", resp["relay_link"])
	}
	log, _ = os.ReadFile(stubLog)
	if strings.Count(string(log), "--door") != 1 {
		t.Fatalf("标准模式不应再调 --door，stub 日志：\n%s", string(log))
	}
}

func TestHandleTwinDoorFailure(t *testing.T) {
	// --door 失败：relay_link 留空，给 manual 原因，直连 twin 还在
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	stub := "#!/bin/bash\n" +
		"if [ \"$1\" = \"--twin\" ]; then echo \"vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls&sni=example.com#test-直连\"; exit 0; fi\n" +
		"if [ \"$1\" = \"--door\" ]; then echo \"__MANUAL__:测试原因\"; exit 1; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "RELAY_ADDR=\"203.0.113.9\"\nLANDING=203.0.113.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"example.com\"\nCERT_DOMAIN=\"example.com\"\nPRESET=1\nMODE_NOTE=\"tiktok-node\"\nMODE=\"door\"\nPORTS=\"tcp:20443:443\"\nLANDING_COUNT=1\nALL_PORTS=\"tcp:20443:443\"\nSHARE80=0\nSHARE443=0\n"
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestAddLandingServer(t, kitDir, realmDir)
	link := "vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test"
	code, resp := postTwin(t, s, `{"link":"`+link+`","landing_ip":"203.0.113.1"}`)
	if code != 200 {
		t.Fatalf("twin 状态=%d resp=%v", code, resp)
	}
	if resp["relay_link"] != "" {
		t.Fatalf("--door 失败时 relay_link 应留空，实际 %v", resp["relay_link"])
	}
	if resp["manual"] != "测试原因" {
		t.Fatalf("manual=%v want 测试原因", resp["manual"])
	}
	if resp["relay_label"] != "任意门" {
		t.Fatalf("relay_label=%v want 任意门", resp["relay_label"])
	}
	dl, _ := resp["direct_link"].(string)
	if !strings.Contains(dl, "@203.0.113.1:443") {
		t.Fatalf("direct_link=%q 应保留直连 twin", dl)
	}
}

// ---------- 1.1.9：前端含有任意门入口 ----------

func TestDoorFrontendWiring(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	get := func(path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Body.String()
	}
	html := get("/")
	for _, want := range []string{
		`id="add-mode"`, `id="add-relay-addr"`, `留空 = 自动用中转机公网 IP`,
		`id="door-form"`, `id="door-addr"`, `id="door-status"`,
		`任意门链接用这个地址生成`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("首页缺 %q（任意门表单/设置卡片）", want)
		}
	}
	js := get("/app.js")
	for _, want := range []string{
		"relay_label", "door_note", "modeText", "任意门模式",
		"/api/v1/settings/relay-addr", "#door-form", "#door-addr", "#door-status",
		"add-relay-addr-wrap", "badge blue",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js 缺 %q（任意门前端逻辑）", want)
		}
	}
}
