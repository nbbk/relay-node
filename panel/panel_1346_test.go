package main

// ---------- 1.3.46：体检优化 ----------

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------- 桩命令 ----------

// stubBin1346 建桩命令目录（systemctl：记调用日志，is-active 恒 0；
// ss：-tlnp 按 STUB_TLNP_LINES 环境变量输出），插到 PATH 最前面。
func stubBin1346(t *testing.T, logPath string) string {
	t.Helper()
	dir := t.TempDir()
	systemctl := "#!/usr/bin/env bash\n" +
		"echo \"$@\" >> " + strconv.Quote(logPath) + "\n" +
		"exit 0\n"
	ss := "#!/usr/bin/env bash\n" +
		"if [ \"$1\" = \"-tlnp\" ]; then printf '%s' \"${STUB_TLNP_LINES:-}\"; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(systemctl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ss"), []byte(ss), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return dir
}

// writeRealmFiles1346 写 relay.conf 与 config.toml 到测试面板的 RealmDir。
func writeRealmFiles1346(t *testing.T, s *Server, relayConf, toml string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "relay.conf"), []byte(relayConf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fix1346 调 POST /api/v1/fix。
func fix1346(t *testing.T, h http.Handler, cookie, code string, landing int) (int, map[string]any) {
	t.Helper()
	return postJSON144(t, h, cookie, "/api/v1/fix", fmt.Sprintf(`{"issue_code":%q,"landing":%d}`, code, landing))
}

// relayConf1346 生成单落地 relay.conf（端口由调用方拼）。
func relayConf1346(landing, ports, extra string) string {
	return "LANDING=" + landing + "\nLANDING_IS_DOMAIN=0\nDOMAIN=\"example.com\"\nCERT_DOMAIN=\"example.com\"\n" +
		"PRESET=3\nMODE_NOTE=\"自定义端口\"\nPORTS=\"" + ports + "\"\n" + extra +
		"LANDING_COUNT=1\nALL_PORTS=\"" + ports + "\"\nSHARE80=0\nSHARE443=0\n"
}

// toml1346 生成最小 config.toml（带一个 endpoint 段，满足 validateConfigToml）。
func toml1346(useUDP bool) string {
	u := "false"
	if useUDP {
		u = "true"
	}
	return "[network]\nno_tcp = false\nuse_udp = " + u + "\n\n[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"127.0.0.1:24443\"\n"
}

// freePort1346 占一个空闲端口再放掉（调用方随即使用，极小竞态可接受）。
func freePort1346(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return p
}

// acceptCloseListener1346 起一个只接不握手的 TCP 监听（模拟“端口通但 TLS 不通”）。
func acceptCloseListener1346(t *testing.T) (net.Listener, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln, ln.Addr().(*net.TCPAddr).Port
}

// tlsListener1346 起一个自签 TLS 监听（模拟直连落地能拿到证书）。
func tlsListener1346(t *testing.T) (net.Listener, int) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"example.com"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
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
	return ln, ln.Addr().(*net.TCPAddr).Port
}

// systemctlLog1346 读桩 systemctl 的调用日志。
func systemctlLog1346(t *testing.T, logPath string) string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------- NEEDS_UDP 计算与解析 ----------

// TestComputeNeedsUDP1346 按落地实际节点协议算 NEEDS_UDP：有 HY2/TUIC→1，
// 其余协议→0；链接解析不出时回落到 WANT_UDP。
func TestComputeNeedsUDP1346(t *testing.T) {
	cases := []struct {
		link    string
		wantUDP bool
		want    int
	}{
		{"vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?encryption=none&security=tls&sni=example.com#test", false, 0},
		{"trojan://password@203.0.113.1:443?sni=example.com#test", false, 0},
		{"ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@203.0.113.1:8388#test", false, 0},
		{"hysteria2://password@203.0.113.1:443?sni=example.com#test", false, 1},
		{"hy2://password@203.0.113.1:443?sni=example.com#test", false, 1},
		{"tuic://11111111-2222-3333-4444-555555555555:password@203.0.113.1:443?sni=example.com#test", false, 1},
		// 链接解析不出：回落到 WANT_UDP。
		{"不是链接", true, 1},
		{"不是链接", false, 0},
		{"", true, 1},
		{"", false, 0},
	}
	for _, c := range cases {
		if got := computeNeedsUDP(c.link, c.wantUDP); got != c.want {
			t.Fatalf("computeNeedsUDP(%q, %v) 应为 %d，实际 %d", c.link, c.wantUDP, c.want, got)
		}
	}
}

// TestParseNeedsUDP1346 relay.conf 里 NEEDS_UDP[_N] 解析：1 号平铺、2 号后缀、
// 老 relay.conf 缺失标记→-1（未知，按旧行为检查）。
func TestParseNeedsUDP1346(t *testing.T) {
	conf := "LANDING=203.0.113.1\nPORTS=\"tcp:443:443\"\nNEEDS_UDP=0\n" +
		"LANDING_2=203.0.113.2\nPORTS_2=\"tcp:443:443 udp:443:443\"\nNEEDS_UDP_2=1\nLANDING_COUNT=2\n"
	rc := parseRelayConf([]byte(conf))
	if len(rc.Landings) != 2 {
		t.Fatalf("应解析出 2 个落地，实际 %d", len(rc.Landings))
	}
	if rc.Landings[0].NeedsUDP != 0 {
		t.Fatalf("落地 1 NEEDS_UDP 应为 0，实际 %d", rc.Landings[0].NeedsUDP)
	}
	if rc.Landings[1].NeedsUDP != 1 {
		t.Fatalf("落地 2 NEEDS_UDP 应为 1，实际 %d", rc.Landings[1].NeedsUDP)
	}
	old := parseRelayConf([]byte("LANDING=203.0.113.1\nPORTS=\"tcp:443:443\"\nLANDING_COUNT=1\n"))
	if old.Landings[0].NeedsUDP != -1 {
		t.Fatalf("老 relay.conf 缺失标记时 NEEDS_UDP 应为 -1，实际 %d", old.Landings[0].NeedsUDP)
	}
}

// TestRebuildDeleteKeepsNeedsUDP1346 删落地重排编号后，NEEDS_UDP 跟着落地走，
// 不会错位、不会丢。
func TestRebuildDeleteKeepsNeedsUDP1346(t *testing.T) {
	conf := "LANDING=203.0.113.1\nPORTS=\"tcp:443:443\"\nNEEDS_UDP=0\n" +
		"LANDING_2=203.0.113.2\nPORTS_2=\"tcp:443:443 udp:443:443\"\nNEEDS_UDP_2=1\nLANDING_COUNT=2\nALL_PORTS=\"tcp:443:443 udp:443:443\"\n"
	rc := parseRelayConf([]byte(conf))
	lines := rebuildRelayConfAfterDelete(rc, 1)
	rc2 := parseRelayConf([]byte(strings.Join(lines, "\n")))
	if len(rc2.Landings) != 1 {
		t.Fatalf("删后应剩 1 个落地，实际 %d", len(rc2.Landings))
	}
	if rc2.Landings[0].Landing != "203.0.113.2" || rc2.Landings[0].NeedsUDP != 1 {
		t.Fatalf("删后剩下的落地应是 203.0.113.2 且 NEEDS_UDP=1，实际 %+v", rc2.Landings[0])
	}
	if rc2.Raw["NEEDS_UDP_2"] != "" {
		t.Fatalf("删后不应残留 NEEDS_UDP_2，实际 %q", rc2.Raw["NEEDS_UDP_2"])
	}
}

// TestEditRebuildWritesNeedsUDP1346 编辑重建 + NEEDS_UDP 写回：按新原始链接
// 的协议重算（HY2→1、VLESS→0），解析回来一致。
func TestEditRebuildWritesNeedsUDP1346(t *testing.T) {
	rc := parseRelayConf([]byte(relayConf1346("203.0.113.1", "tcp:443:443", "NEEDS_UDP=0\n")))
	nl := Landing{Index: 1, Landing: "203.0.113.1", Domain: "example.com", CertDomain: "example.com", Preset: "3", Mode: "standard", Ports: parsePorts("tcp:443:443 udp:443:443")}
	lines := rebuildRelayConfForEdit(rc, 1, nl, false, false, "")
	lines = setRelayConfValue(lines, needsUDPKey(1), strconv.Itoa(computeNeedsUDP("hysteria2://password@203.0.113.1:443?sni=example.com#test", false)))
	rc2 := parseRelayConf([]byte(strings.Join(lines, "\n")))
	if rc2.Landings[0].NeedsUDP != 1 {
		t.Fatalf("HY2 落地 NEEDS_UDP 应为 1，实际 %d", rc2.Landings[0].NeedsUDP)
	}
	lines = setRelayConfValue(lines, needsUDPKey(1), strconv.Itoa(computeNeedsUDP("vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?encryption=none&security=tls&sni=example.com#test", false)))
	rc3 := parseRelayConf([]byte(strings.Join(lines, "\n")))
	if rc3.Landings[0].NeedsUDP != 0 {
		t.Fatalf("VLESS 落地 NEEDS_UDP 应为 0，实际 %d", rc3.Landings[0].NeedsUDP)
	}
}

// ---------- use_udp 读写 ----------

// TestUseUDPReadWrite1346 readUseUDP / setUseUDP / ensureUseUDP：
// 只改已有的 use_udp 行（行尾注释保留），没这行不凭空加；不需要时保持原值。
func TestUseUDPReadWrite1346(t *testing.T) {
	toml := "[network]\nno_tcp = false\nuse_udp = false # 注释保留\n"
	if on, ok := readUseUDP([]byte(toml)); !ok || on {
		t.Fatalf("use_udp=false 应读出 (false, true)，实际 (%v, %v)", on, ok)
	}
	nt, ok := setUseUDP([]byte(toml), true)
	if !ok {
		t.Fatal("已有 use_udp 行时 setUseUDP 应返回 true")
	}
	if !strings.Contains(string(nt), "use_udp = true # 注释保留") {
		t.Fatalf("改完应为 use_udp = true 且注释保留，实际 %q", string(nt))
	}
	if on, ok := readUseUDP(nt); !ok || !on {
		t.Fatalf("改完应读出 (true, true)，实际 (%v, %v)", on, ok)
	}
	// 没这行：不凭空加行。
	if _, ok := setUseUDP([]byte("[network]\nno_tcp = false\n"), true); ok {
		t.Fatal("没有 use_udp 行时 setUseUDP 应返回 false")
	}
	if _, ok := readUseUDP([]byte("[network]\nno_tcp = false\n")); ok {
		t.Fatal("没有 use_udp 行时 readUseUDP 应返回 ok=false")
	}
	// 不需要时保持原值（绝不乱关）。
	if nt2, changed := ensureUseUDP([]byte(toml), false); changed || string(nt2) != toml {
		t.Fatalf("不需要 UDP 时 ensureUseUDP 不应改动，实际 changed=%v", changed)
	}
	// 需要且已开：不改。
	if _, changed := ensureUseUDP(nt, true); changed {
		t.Fatal("use_udp 已开时 ensureUseUDP 不应改动")
	}
	// ensureUseUDPForConf：PORTS 里有 udp: 条目才保证开着。
	rc := parseRelayConf([]byte(relayConf1346("203.0.113.1", "tcp:443:443 udp:443:443", "")))
	if nt3, changed := ensureUseUDPForConf(rc, []byte(toml)); !changed || !strings.Contains(string(nt3), "use_udp = true") {
		t.Fatalf("PORTS 有 udp: 条目时应把开关打开，changed=%v 内容 %q", changed, string(nt3))
	}
	rcTCP := parseRelayConf([]byte(relayConf1346("203.0.113.1", "tcp:443:443", "")))
	if _, changed := ensureUseUDPForConf(rcTCP, []byte(toml)); changed {
		t.Fatal("PORTS 全是 TCP 时 ensureUseUDPForConf 不应改动")
	}
}

// ---------- handleFix：tls_unreachable 分流 ----------

// fixServer1346 建带一个落地 + 桩 systemctl/ss 的测试面板，返回 (s, h, cookie, 日志路径)。
func fixServer1346(t *testing.T, ports string) (*Server, http.Handler, string, string) {
	t.Helper()
	s := newTestServer144(t)
	logPath := filepath.Join(t.TempDir(), "systemctl.log")
	stubBin1346(t, logPath)
	writeRealmFiles1346(t, s, relayConf1346("127.0.0.1", ports, ""), toml1346(false))
	h := s.routes()
	cookie := loginCookie144(t, h)
	return s, h, cookie, logPath
}

// TestFixTLSUnreachableOccupied1346 TCP 不通且端口被非 realm 程序占用
// → need_manual（带进程名），不重启、不 400。
func TestFixTLSUnreachableOccupied1346(t *testing.T) {
	p := freePort1346(t)
	_, h, cookie, logPath := fixServer1346(t, fmt.Sprintf("tcp:%d:%d", p, p))
	t.Setenv("STUB_TLNP_LINES", fmt.Sprintf("LISTEN 0 4096 0.0.0.0:%d 0.0.0.0:* users:((\"nginx\",pid=123,fd=6))\n", p))
	code, resp := fix1346(t, h, cookie, "tls_unreachable", 1)
	if code != 200 {
		t.Fatalf("tls_unreachable 应 200，实际 %d：%v", code, resp)
	}
	if resp["ok"] != true || resp["need_manual"] != true {
		t.Fatalf("被占用时应回 need_manual，实际 %v", resp)
	}
	reason, _ := resp["reason"].(string)
	if !strings.Contains(reason, "nginx") {
		t.Fatalf("原因里应有占用进程名 nginx，实际 %q", reason)
	}
	if got := systemctlLog1346(t, logPath); strings.Contains(got, "restart realm") {
		t.Fatalf("need_manual 时不应重启 realm，日志：%q", got)
	}
}

// TestFixTLSUnreachableNoListener1346 TCP 不通且没人监听 → realm 本身的问题，
// 重启，返回成功文案。
func TestFixTLSUnreachableNoListener1346(t *testing.T) {
	p := freePort1346(t)
	_, h, cookie, logPath := fixServer1346(t, fmt.Sprintf("tcp:%d:%d", p, p))
	t.Setenv("STUB_TLNP_LINES", "")
	code, resp := fix1346(t, h, cookie, "tls_unreachable", 1)
	if code != 200 {
		t.Fatalf("tls_unreachable 应 200，实际 %d：%v", code, resp)
	}
	if resp["ok"] != true || resp["need_manual"] == true {
		t.Fatalf("没人监听时应重启成功，实际 %v", resp)
	}
	if msg, _ := resp["msg"].(string); !strings.Contains(msg, "已重启") {
		t.Fatalf("成功文案应含“已重启”，实际 %q", msg)
	}
	if got := systemctlLog1346(t, logPath); !strings.Contains(got, "restart realm") {
		t.Fatalf("应调 systemctl restart realm，日志：%q", got)
	}
}

// TestFixTLSUnreachableDirectOK1346 TCP 通、中转握手失败、直连落地能拿到证书
// → realm 转发层问题，重启。
func TestFixTLSUnreachableDirectOK1346(t *testing.T) {
	_, relayPort := acceptCloseListener1346(t)
	_, tlsPort := tlsListener1346(t)
	_, h, cookie, logPath := fixServer1346(t, fmt.Sprintf("tcp:%d:%d", relayPort, tlsPort))
	t.Setenv("STUB_TLNP_LINES", "")
	code, resp := fix1346(t, h, cookie, "tls_unreachable", 1)
	if code != 200 {
		t.Fatalf("tls_unreachable 应 200，实际 %d：%v", code, resp)
	}
	if resp["ok"] != true || resp["need_manual"] == true {
		t.Fatalf("直连正常时应重启成功，实际 %v", resp)
	}
	if msg, _ := resp["msg"].(string); !strings.Contains(msg, "直连落地正常") {
		t.Fatalf("成功文案应含“直连落地正常”，实际 %q", msg)
	}
	if got := systemctlLog1346(t, logPath); !strings.Contains(got, "restart realm") {
		t.Fatalf("应调 systemctl restart realm，日志：%q", got)
	}
}

// TestFixTLSUnreachableDirectFail1346 TCP 通、中转与直连都拿不到证书
// → 落地本身的问题，need_manual，不重启、不 400。
func TestFixTLSUnreachableDirectFail1346(t *testing.T) {
	_, relayPort := acceptCloseListener1346(t)
	deadPort := freePort1346(t)
	_, h, cookie, logPath := fixServer1346(t, fmt.Sprintf("tcp:%d:%d", relayPort, deadPort))
	t.Setenv("STUB_TLNP_LINES", "")
	code, resp := fix1346(t, h, cookie, "tls_unreachable", 1)
	if code != 200 {
		t.Fatalf("tls_unreachable 应 200，实际 %d：%v", code, resp)
	}
	if resp["ok"] != true || resp["need_manual"] != true {
		t.Fatalf("直连也失败时应回 need_manual，实际 %v", resp)
	}
	reason, _ := resp["reason"].(string)
	if !strings.Contains(reason, "落地 TLS 本身") {
		t.Fatalf("原因应指落地本身，实际 %q", reason)
	}
	if got := systemctlLog1346(t, logPath); strings.Contains(got, "restart realm") {
		t.Fatalf("need_manual 时不应重启 realm，日志：%q", got)
	}
}

// ---------- handleFix：udp_disabled ----------

// TestFixUDPDisabled1346 use_udp 关着 → 打开开关 + 重启；已开着时不再重启。
func TestFixUDPDisabled1346(t *testing.T) {
	s := newTestServer144(t)
	logPath := filepath.Join(t.TempDir(), "systemctl.log")
	stubBin1346(t, logPath)
	writeRealmFiles1346(t, s, relayConf1346("127.0.0.1", "tcp:24443:24443 udp:24443:24443", "NEEDS_UDP=1\n"), toml1346(false))
	h := s.routes()
	cookie := loginCookie144(t, h)
	code, resp := fix1346(t, h, cookie, "udp_disabled", 1)
	if code != 200 {
		t.Fatalf("udp_disabled 应 200，实际 %d：%v", code, resp)
	}
	if resp["ok"] != true {
		t.Fatalf("udp_disabled 应成功，实际 %v", resp)
	}
	if msg, _ := resp["msg"].(string); !strings.Contains(msg, "UDP 转发") {
		t.Fatalf("成功文案应含“UDP 转发”，实际 %q", msg)
	}
	tomlData, err := s.readConfigToml()
	if err != nil {
		t.Fatal(err)
	}
	if on, ok := readUseUDP(tomlData); !ok || !on {
		t.Fatalf("修完 config.toml 里 use_udp 应为 true，实际 %q", string(tomlData))
	}
	if got := systemctlLog1346(t, logPath); !strings.Contains(got, "restart realm") {
		t.Fatalf("应调 systemctl restart realm，日志：%q", got)
	}
	// 再点一次：已经开着，不再重启。
	before := systemctlLog1346(t, logPath)
	code, resp = fix1346(t, h, cookie, "udp_disabled", 1)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("已开着时应 200 成功，实际 %d：%v", code, resp)
	}
	if msg, _ := resp["msg"].(string); !strings.Contains(msg, "已经开着") {
		t.Fatalf("已开着时文案应说明无需修复，实际 %q", msg)
	}
	if after := systemctlLog1346(t, logPath); after != before {
		t.Fatalf("已开着时不应再重启，日志多了：%q", strings.TrimPrefix(after, before))
	}
}

// ---------- 补原始链接时重算 NEEDS_UDP ----------

// TestSetOrigLinkRecomputesNeedsUDP1346 补粘贴原始链接换协议时，NEEDS_UDP
// 跟着重算（HY2→1、VLESS→0）。
func TestSetOrigLinkRecomputesNeedsUDP1346(t *testing.T) {
	s := newTestServer144(t)
	writeRealmFiles1346(t, s, relayConf1346("127.0.0.1", "tcp:24443:24443 udp:24443:24443", "NEEDS_UDP=0\n"), toml1346(true))
	h := s.routes()
	cookie := loginCookie144(t, h)
	hy2 := `{"orig_link":"hysteria2://password@127.0.0.1:443?sni=example.com#test","link_name":""}`
	code, resp := postJSON144(t, h, cookie, "/api/v1/landings/1/orig-link", hy2)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("补 HY2 链接应 200 成功，实际 %d：%v", code, resp)
	}
	rc, _, err := s.readRelayConf()
	if err != nil {
		t.Fatal(err)
	}
	if rc.Landings[0].NeedsUDP != 1 {
		t.Fatalf("补 HY2 链接后 NEEDS_UDP 应为 1，实际 %d", rc.Landings[0].NeedsUDP)
	}
	vless := `{"orig_link":"vless://11111111-2222-3333-4444-555555555555@127.0.0.1:443?encryption=none&security=tls&sni=example.com#test","link_name":""}`
	code, resp = postJSON144(t, h, cookie, "/api/v1/landings/1/orig-link", vless)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("补 VLESS 链接应 200 成功，实际 %d：%v", code, resp)
	}
	rc, _, err = s.readRelayConf()
	if err != nil {
		t.Fatal(err)
	}
	if rc.Landings[0].NeedsUDP != 0 {
		t.Fatalf("补 VLESS 链接后 NEEDS_UDP 应为 0，实际 %d", rc.Landings[0].NeedsUDP)
	}
}

// ---------- check.sh --json（真实脚本 + 桩命令） ----------

const (
	// stubSS1346 桩 ss：-tln/-uln 按 STUB_TCP_LISTEN/STUB_UDP_LISTEN 输出，
	// -tlnp 按 STUB_TLNP_LINES 输出。
	stubSS1346 = "#!/usr/bin/env bash\n" +
		"case \"$1\" in\n" +
		"  -tln) for p in ${STUB_TCP_LISTEN:-}; do echo \"LISTEN 0 4096 0.0.0.0:$p 0.0.0.0:*\"; done ;;\n" +
		"  -uln) for p in ${STUB_UDP_LISTEN:-}; do echo \"UNCONN 0 0 0.0.0.0:$p 0.0.0.0:*\"; done ;;\n" +
		"  -tlnp) printf '%s' \"${STUB_TLNP_LINES:-}\" ;;\n" +
		"esac\nexit 0\n"
	// stubOpenssl1346 桩 openssl：s_client 按 STUB_RELAY_CERT /
	// STUB_DIRECT_CERT / STUB_DIRECT_NOSNI_CERT 决定给不给证书；
	// x509 从 stdin 的 CERTID 行输出指纹/到期日。
	stubOpenssl1346 = "#!/usr/bin/env bash\n" +
		"if [ \"$1\" = \"s_client\" ]; then\n" +
		"  shift; addr=\"\"; sni=0\n" +
		"  while [ $# -gt 0 ]; do case \"$1\" in -connect) addr=\"$2\"; shift 2;; -servername) sni=1; shift 2;; *) shift;; esac; done\n" +
		"  cert=\"\"\n" +
		"  case \"$addr\" in\n" +
		"    127.0.0.1:*) cert=\"${STUB_RELAY_CERT:-}\" ;;\n" +
		"    *) if [ \"$sni\" = \"1\" ]; then cert=\"${STUB_DIRECT_CERT:-}\"; else cert=\"${STUB_DIRECT_NOSNI_CERT:-}\"; fi ;;\n" +
		"  esac\n" +
		"  [ -n \"$cert\" ] && printf -- '-----BEGIN CERTIFICATE-----\\n%s\\n-----END CERTIFICATE-----\\n' \"$cert\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"x509\" ]; then\n" +
		"  shift; body=\"$(cat)\"; id=\"$(printf '%s' \"$body\" | grep -v -- '-----' | head -1)\"\n" +
		"  [ -z \"$id\" ] && exit 0\n" +
		"  for a in \"$@\"; do case \"$a\" in -fingerprint) echo \"SHA256 Fingerprint=${id}-FP\";; -enddate) echo \"notAfter=${STUB_ENDDATE:-Feb 16 12:00:00 2027 GMT}\";; esac; done\n" +
		"  exit 0\n" +
		"fi\nexit 0\n"
	stubSystemctl1346 = "#!/usr/bin/env bash\n" +
		"if [ \"$1\" = \"is-active\" ]; then exit 0; fi\nexit 0\n"
	stubSysctl1346 = "#!/usr/bin/env bash\necho bbr\n"
	stubPgrep1346  = "#!/usr/bin/env bash\nexit 1\n"
)

// checkJSON1346 check.sh --json 的输出结构（只取断言要用的字段）。
type checkJSON1346 struct {
	Issues []struct {
		Code    string `json:"code"`
		Landing int    `json:"landing"`
		Message string `json:"message"`
		Fix     string `json:"fix"`
	} `json:"issues"`
	Sections []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Items  []struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		} `json:"items"`
	} `json:"sections"`
	Certs []struct {
		DaysLeft *int `json:"days_left"`
	} `json:"certs"`
}

// runCheckSh1346 用临时 relay.conf/config.toml + 桩命令跑真实 check.sh --json。
func runCheckSh1346(t *testing.T, relayConf, toml string, env map[string]string) checkJSON1346 {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "relay.conf"), []byte(relayConf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	stubs := t.TempDir()
	for name, body := range map[string]string{
		"ss": stubSS1346, "openssl": stubOpenssl1346, "systemctl": stubSystemctl1346,
		"sysctl": stubSysctl1346, "pgrep": stubPgrep1346,
	} {
		if err := os.WriteFile(filepath.Join(stubs, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	checkSh := filepath.Join(kitRoot1344(t), "check.sh")
	cmd := exec.Command("bash", checkSh, "--json")
	cmd.Env = append(os.Environ(),
		"RELAY_CONF="+filepath.Join(dir, "relay.conf"),
		"REALM_TOML="+filepath.Join(dir, "config.toml"),
		"PATH="+stubs+":"+os.Getenv("PATH"),
		"STUB_REALM_RUNNING=1",
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("check.sh --json 应跑通：%v\n输出：%s", err, out)
	}
	var res checkJSON1346
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("check.sh 输出应为合法 JSON：%v\n输出：%s", err, out)
	}
	return res
}

// sectionItems1346 取某 section 的全部 item 文案。
func sectionItems1346(t *testing.T, res checkJSON1346, name string) []string {
	t.Helper()
	for _, s := range res.Sections {
		if s.Name == name {
			var out []string
			for _, it := range s.Items {
				out = append(out, it.Level+":"+it.Message)
			}
			return out
		}
	}
	t.Fatalf("找不到 section %q", name)
	return nil
}

// TestCheckShSkipUDP1346 check.sh：NEEDS_UDP=0 的 TCP 协议落地跳过 UDP 检查，
// use_udp 关着也不报错。
func TestCheckShSkipUDP1346(t *testing.T) {
	conf := relayConf1346("203.0.113.1", "tcp:80:80 tcp:443:443 tcp:24443:24443 udp:24443:24443", "NEEDS_UDP=0\nCERT_MODE=dns01\n")
	res := runCheckSh1346(t, conf, toml1346(false), map[string]string{
		"STUB_TCP_LISTEN": "80 443 24443", "STUB_UDP_LISTEN": "",
		"STUB_RELAY_CERT": "CERT-A", "STUB_DIRECT_CERT": "CERT-A", "STUB_DIRECT_NOSNI_CERT": "CERT-A",
	})
	if len(res.Issues) != 0 {
		t.Fatalf("跳过 UDP 时应无问题，实际 %+v", res.Issues)
	}
	items := sectionItems1346(t, res, "监听端口")
	found := false
	for _, it := range items {
		if strings.Contains(it, "24443/udp 跳过") {
			found = true
		}
	}
	if !found {
		t.Fatalf("监听端口段应有 UDP 跳过项，实际 %v", items)
	}
}

// TestCheckShUDPDisabled1346 check.sh：NEEDS_UDP=1 且 use_udp 关着时报真因
// udp_disabled，不再报“端口没监听”。
func TestCheckShUDPDisabled1346(t *testing.T) {
	conf := relayConf1346("203.0.113.1", "tcp:80:80 tcp:443:443 tcp:24443:24443 udp:24443:24443", "NEEDS_UDP=1\nCERT_MODE=dns01\n")
	res := runCheckSh1346(t, conf, toml1346(false), map[string]string{
		"STUB_TCP_LISTEN": "80 443 24443", "STUB_UDP_LISTEN": "",
		"STUB_RELAY_CERT": "CERT-A", "STUB_DIRECT_CERT": "CERT-A", "STUB_DIRECT_NOSNI_CERT": "CERT-A",
	})
	if len(res.Issues) != 1 || res.Issues[0].Code != "udp_disabled" || res.Issues[0].Fix != "enable_udp" {
		t.Fatalf("应只有一个 udp_disabled 问题（fix=enable_udp），实际 %+v", res.Issues)
	}
	if !strings.Contains(res.Issues[0].Message, "use_udp 关着") {
		t.Fatalf("原因应指 use_udp 关着，实际 %q", res.Issues[0].Message)
	}
}

// TestCheckShTLSDirectOK1346 check.sh：TCP 通、中转握手失败、直连成功
// → tls_unreachable“直连落地正常，中转转发有问题”；证书不再单独 warn。
func TestCheckShTLSDirectOK1346(t *testing.T) {
	_, relayPort := acceptCloseListener1346(t)
	remotePort := freePort1346(t)
	ports := fmt.Sprintf("tcp:%d:%d udp:%d:%d", relayPort, remotePort, relayPort, remotePort)
	conf := relayConf1346("203.0.113.1", ports, "NEEDS_UDP=0\nCERT_MODE=dns01\n")
	res := runCheckSh1346(t, conf, toml1346(true), map[string]string{
		"STUB_TCP_LISTEN": strconv.Itoa(relayPort), "STUB_UDP_LISTEN": "",
		"STUB_RELAY_CERT": "", "STUB_DIRECT_CERT": "CERT-A", "STUB_DIRECT_NOSNI_CERT": "CERT-A",
	})
	if len(res.Issues) != 1 || res.Issues[0].Code != "tls_unreachable" {
		t.Fatalf("应只有一个 tls_unreachable 问题，实际 %+v", res.Issues)
	}
	if !strings.Contains(res.Issues[0].Message, "直连落地正常，中转转发有问题") {
		t.Fatalf("原因应指直连正常、中转有问题，实际 %q", res.Issues[0].Message)
	}
	if res.Issues[0].Fix != "restart_realm" {
		t.Fatalf("直连正常时 fix 应为 restart_realm，实际 %q", res.Issues[0].Fix)
	}
	// 证书不再单独 warn：没有 cert_* 问题，证书段是中性文案，days_left 为 null。
	for _, is := range res.Issues {
		if strings.HasPrefix(is.Code, "cert_") {
			t.Fatalf("握手失败时不应再有证书问题，实际 %+v", res.Issues)
		}
	}
	items := sectionItems1346(t, res, "证书到期天数")
	if len(items) != 1 || !strings.Contains(items[0], "证书到期随 TLS 直通验证失败，未单独计算") {
		t.Fatalf("证书段应为中性文案，实际 %v", items)
	}
	if len(res.Certs) != 1 || res.Certs[0].DaysLeft != nil {
		t.Fatalf("握手失败时 certs days_left 应为 null，实际 %+v", res.Certs)
	}
}

// TestCheckShTLSOccupied1346 check.sh：TCP 不通且 ss 称端口归 nginx
// → tls_unreachable“被非 realm 程序占用”，fix=manual。
func TestCheckShTLSOccupied1346(t *testing.T) {
	p := freePort1346(t)
	ports := fmt.Sprintf("tcp:%d:%d udp:%d:%d", p, p, p, p)
	conf := relayConf1346("203.0.113.1", ports, "NEEDS_UDP=0\nCERT_MODE=dns01\n")
	res := runCheckSh1346(t, conf, toml1346(true), map[string]string{
		"STUB_TCP_LISTEN": strconv.Itoa(p), "STUB_UDP_LISTEN": "",
		"STUB_TLNP_LINES": fmt.Sprintf("LISTEN 0 4096 0.0.0.0:%d 0.0.0.0:* users:((\"nginx\",pid=123,fd=6))\n", p),
		"STUB_RELAY_CERT": "", "STUB_DIRECT_CERT": "", "STUB_DIRECT_NOSNI_CERT": "",
	})
	if len(res.Issues) != 1 || res.Issues[0].Code != "tls_unreachable" {
		t.Fatalf("应只有一个 tls_unreachable 问题，实际 %+v", res.Issues)
	}
	if !strings.Contains(res.Issues[0].Message, "被非 realm 程序占用（进程：nginx）") {
		t.Fatalf("原因应指 nginx 占用，实际 %q", res.Issues[0].Message)
	}
	if res.Issues[0].Fix != "manual" {
		t.Fatalf("被占用时 fix 应为 manual，实际 %q", res.Issues[0].Fix)
	}
}

// TestFixReturns200Not4001346 回归：tls_unreachable 点修复不再 400
// （上面四个分流测试已覆盖各档，这里再对未知落地号确认不 400、
// 只回“没有编号为 N 的落地”的错误文案）。
func TestFixReturns200Not4001346(t *testing.T) {
	s := newTestServer144(t)
	writeRealmFiles1346(t, s, relayConf1346("127.0.0.1", "tcp:24443:24443", ""), toml1346(true))
	h := s.routes()
	cookie := loginCookie144(t, h)
	req := httptest.NewRequest("POST", "/api/v1/fix", strings.NewReader(`{"issue_code":"tls_unreachable","landing":99}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == 400 {
		t.Fatalf("tls_unreachable 不应再 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}
