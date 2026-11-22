package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---------- 1.3.42：粘贴原始链接注册落地补全 HY2/TUIC ----------

func TestParseNodeLinkHY2TUIC1342(t *testing.T) {
	// HY2：落地 install.sh 生成的格式
	pl, err := parseNodeLink("hysteria2://hy2pass123@example.com:8443?sni=example.com&alpn=h3#SingBox-HY2")
	if err != nil {
		t.Fatal(err)
	}
	if pl.Scheme != "hysteria2" || pl.Host != "example.com" || pl.Port != 8443 || pl.SNI != "example.com" || pl.Name != "SingBox-HY2" {
		t.Fatalf("hy2 parsed=%+v", pl)
	}
	// hy2:// 别名
	pl, err = parseNodeLink("hy2://hy2pass123@example.com:8443?sni=example.com&alpn=h3#SingBox-HY2")
	if err != nil {
		t.Fatal(err)
	}
	if pl.Scheme != "hy2" || pl.Host != "example.com" || pl.Port != 8443 {
		t.Fatalf("hy2 alias parsed=%+v", pl)
	}
	// HY2：没 sni 参数时 host 是域名就当 SNI
	pl, err = parseNodeLink("hysteria2://hy2pass123@example.com:8443?alpn=h3#n")
	if err != nil {
		t.Fatal(err)
	}
	if pl.SNI != "example.com" {
		t.Fatalf("hy2 sni fallback=%q want example.com", pl.SNI)
	}
	// TUIC：落地 install.sh 生成的格式
	pl, err = parseNodeLink("tuic://11111111-2222-3333-4444-555555555555:tuicpass@example.com:8444?congestion_control=bbr&alpn=h3&sni=example.com#SingBox-TUIC")
	if err != nil {
		t.Fatal(err)
	}
	if pl.Scheme != "tuic" || pl.Host != "example.com" || pl.Port != 8444 || pl.SNI != "example.com" || pl.Name != "SingBox-TUIC" {
		t.Fatalf("tuic parsed=%+v", pl)
	}
	// 字段缺失：HY2 没密码、TUIC 没 uuid/密码都算无效节点，注册入口直接拦
	if _, err := parseNodeLink("hysteria2://@example.com:8443?sni=example.com#n"); err == nil {
		t.Fatalf("hy2 without password accepted")
	}
	if _, err := parseNodeLink("hysteria2://example.com:8443?sni=example.com#n"); err == nil {
		t.Fatalf("hy2 without userinfo accepted")
	}
	if _, err := parseNodeLink("tuic://:tuicpass@example.com:8444?sni=example.com#n"); err == nil {
		t.Fatalf("tuic without uuid accepted")
	}
	if _, err := parseNodeLink("tuic://11111111-2222-3333-4444-555555555555@example.com:8444?sni=example.com#n"); err == nil {
		t.Fatalf("tuic without password accepted")
	}
	// 原来 4 个协议回归：vless 仍能解析，http 仍被拒
	pl, err = parseNodeLink("vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test")
	if err != nil || pl.Port != 443 {
		t.Fatalf("vless regression: pl=%+v err=%v", pl, err)
	}
	if _, err := parseNodeLink("hysteria://pw@example.com:8443#n"); err == nil {
		t.Fatalf("hysteria v1 accepted")
	}
}

func TestSetLinkHY2TUIC1342(t *testing.T) {
	hy2 := "hysteria2://hy2pass123@example.com:8443?sni=example.com&alpn=h3#SingBox-HY2"
	// 换端口：只换端口，密码/query/备注不动
	got := setLinkPort(hy2, 28443)
	if !strings.Contains(got, "@example.com:28443") || !strings.Contains(got, "sni=example.com") || !strings.Contains(got, "SingBox-HY2") {
		t.Fatalf("setLinkPort hy2=%q", got)
	}
	tuic := "tuic://11111111-2222-3333-4444-555555555555:tuicpass@example.com:8444?congestion_control=bbr&alpn=h3&sni=example.com#SingBox-TUIC"
	got = setLinkPort(tuic, 28444)
	if !strings.Contains(got, "@example.com:28444") || !strings.Contains(got, "congestion_control=bbr") {
		t.Fatalf("setLinkPort tuic=%q", got)
	}
	// 换 host：只换地址，端口与 uuid/密码不动
	got = setLinkHost(hy2, "203.0.113.1")
	if !strings.Contains(got, "@203.0.113.1:8443") || !strings.Contains(got, "hy2pass123@") {
		t.Fatalf("setLinkHost hy2=%q", got)
	}
	got = setLinkHost(tuic, "203.0.113.1")
	if !strings.Contains(got, "11111111-2222-3333-4444-555555555555:tuicpass@203.0.113.1:8444") {
		t.Fatalf("setLinkHost tuic=%q", got)
	}
	// 改备注：# 后面的片段改名
	got = setLinkName(hy2, "test-直连")
	if !strings.HasSuffix(got, "#test-%E7%9B%B4%E8%BF%9E") {
		t.Fatalf("setLinkName hy2=%q", got)
	}
	got = setLinkName(tuic, "test-中转")
	if !strings.HasSuffix(got, "#test-%E4%B8%AD%E8%BD%AC") {
		t.Fatalf("setLinkName tuic=%q", got)
	}
	// hy2:// 别名同样能换端口
	got = setLinkPort("hy2://hy2pass123@example.com:8443?sni=example.com#n", 28443)
	if !strings.Contains(got, "@example.com:28443") {
		t.Fatalf("setLinkPort hy2 alias=%q", got)
	}
}

// TestPresetPortsHY2UDP1342：预设 2（sing-box TLS）本来就转 udp:8443 与
// udp:8444（落地默认的 HY2/TUIC 端口），粘贴 HY2/TUIC 链接选预设 2 即可；
// 端口改过自定义值时走预设 3 + 勾“同时转 UDP”。
func TestPresetPortsHY2UDP1342(t *testing.T) {
	ports := presetPorts("2", "1", "", false)
	want := map[string]bool{"udp:8443": false, "udp:8444": false}
	for _, p := range ports {
		if _, ok := want[p.Proto+":"+strconv.Itoa(p.Listen)]; ok {
			want[p.Proto+":"+strconv.Itoa(p.Listen)] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Fatalf("preset 2 missing %s: %v", k, ports)
		}
	}
}

// TestHandleTwinHY2TUICRealScript1342：用仓库里真实的 install.sh 跑端到端
// twin（不用 stub），验 HY2 标准模式与 TUIC 任意门模式的直连/中转链接。
func TestHandleTwinHY2TUICRealScript1342(t *testing.T) {
	script, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Skipf("读不到仓库 install.sh：%v", err)
	}
	// HY2 标准模式
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "LANDING=203.0.113.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"example.com\"\nCERT_DOMAIN=\"example.com\"\nPRESET=2\nMODE_NOTE=\"landing-node（sing-box 核心）TLS 模式\"\nPORTS=\"tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388 udp:8443:8443 udp:8444:8444\"\nLANDING_COUNT=1\nALL_PORTS=\"tcp:80:80 tcp:443:443\"\nSHARE80=0\nSHARE443=0\n"
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestAddLandingServer(t, kitDir, realmDir)
	hy2 := "hysteria2://hy2pass123@example.com:8443?sni=example.com&alpn=h3#SingBox-HY2"
	code, resp := postTwin(t, s, `{"link":"`+hy2+`","landing_ip":"203.0.113.1"}`)
	if code != 200 {
		t.Fatalf("hy2 twin code=%d resp=%v", code, resp)
	}
	if m, _ := resp["manual"].(string); m != "" {
		t.Fatalf("hy2 twin manual=%q resp=%v", m, resp)
	}
	rl, _ := resp["relay_link"].(string)
	if !strings.Contains(rl, "@example.com:8443") || !strings.Contains(rl, "sni=example.com") || !strings.Contains(rl, "%E4%B8%AD%E8%BD%AC") {
		t.Fatalf("hy2 relay_link=%q want 原地址+中转备注", rl)
	}
	dl, _ := resp["direct_link"].(string)
	if !strings.Contains(dl, "@203.0.113.1:8443") || !strings.Contains(dl, "sni=example.com") || !strings.Contains(dl, "%E7%9B%B4%E8%BF%9E") {
		t.Fatalf("hy2 direct_link=%q want 落地 IP+直连备注", dl)
	}

	// TUIC 任意门模式
	kitDir2 := t.TempDir()
	realmDir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir2, "install.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	confDoor := "RELAY_ADDR=\"203.0.113.9\"\nLANDING=203.0.113.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"example.com\"\nCERT_DOMAIN=\"example.com\"\nPRESET=2\nMODE_NOTE=\"landing-node（sing-box 核心）TLS 模式\"\nMODE=\"door\"\nPORTS=\"udp:28444:8444\"\nLANDING_COUNT=1\nALL_PORTS=\"udp:28444:8444\"\nSHARE80=0\nSHARE443=0\n"
	if err := os.WriteFile(filepath.Join(realmDir2, "relay.conf"), []byte(confDoor), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := newTestAddLandingServer(t, kitDir2, realmDir2)
	tuic := "tuic://11111111-2222-3333-4444-555555555555:tuicpass@example.com:8444?congestion_control=bbr&alpn=h3&sni=example.com#SingBox-TUIC"
	code, resp = postTwin(t, s2, `{"link":"`+tuic+`","landing_ip":"203.0.113.1"}`)
	if code != 200 {
		t.Fatalf("tuic twin code=%d resp=%v", code, resp)
	}
	if m, _ := resp["manual"].(string); m != "" {
		t.Fatalf("tuic twin manual=%q resp=%v", m, resp)
	}
	if resp["relay_label"] != "任意门" {
		t.Fatalf("tuic relay_label=%v want 任意门", resp["relay_label"])
	}
	rl, _ = resp["relay_link"].(string)
	// 注意：任意门链接的备注后缀仍是“-中转”（computeTwinLinks 里备注改名
	// 不分模式，统一用 base+"-中转"；卡片与二维码的标签才是“任意门”）——
	// 这是 vless/trojan 任意门链接原有的行为，本版不改。
	if !strings.Contains(rl, "@203.0.113.9:28444") || !strings.Contains(rl, "congestion_control=bbr") || !strings.Contains(rl, "%E4%B8%AD%E8%BD%AC") {
		t.Fatalf("tuic relay_link=%q want 任意门地址+转发端口", rl)
	}
	dl, _ = resp["direct_link"].(string)
	if !strings.Contains(dl, "@203.0.113.1:8444") || !strings.Contains(dl, "%E7%9B%B4%E8%BF%9E") {
		t.Fatalf("tuic direct_link=%q want 落地直连", dl)
	}
}
