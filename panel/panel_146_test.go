package main

// v1.3.16：节点链接生成方式三选一（both/relay/direct）。
// 生成逻辑本身不动，只按选择决定输出哪几条；没选中的不返回、不记历史、不出二维码。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twin146StdConf = "RELAY_ADDR=\"203.0.113.9\"\nLANDING=203.0.113.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"example.com\"\nCERT_DOMAIN=\"example.com\"\nPRESET=1\nMODE_NOTE=\"tiktok-node\"\nMODE=\"standard\"\nPORTS=\"tcp:443:443\"\nLANDING_COUNT=1\nALL_PORTS=\"tcp:443:443\"\nSHARE80=0\nSHARE443=0\n"

func newTwin146Server(t *testing.T, stub, conf string) (*Server, string, string) {
	t.Helper()
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	stubLog := filepath.Join(t.TempDir(), "stub.log")
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_LOG", stubLog)
	t.Setenv("REALM_DIR", realmDir)
	return newTestAddLandingServer(t, kitDir, realmDir), kitDir, realmDir
}

func twin146QRCount(t *testing.T, resp map[string]any) int {
	t.Helper()
	qr, _ := resp["qr"].(map[string]any)
	return len(qr)
}

func TestTwinModeRelay(t *testing.T) {
	s, kitDir, _ := newTwin146Server(t, twin122Stub, twin146StdConf)
	code, resp := postTwin(t, s, `{"link":"`+test122OrigLink+`","landing_ip":"203.0.113.1","mode":"relay"}`)
	if code != 200 {
		t.Fatalf("relay 方式状态=%d resp=%v", code, resp)
	}
	rl, _ := resp["relay_link"].(string)
	if rl == "" {
		t.Fatalf("relay 方式应返回中转链接：%v", resp)
	}
	if dl, _ := resp["direct_link"].(string); dl != "" {
		t.Fatalf("relay 方式不应返回直连链接：%q", dl)
	}
	if resp["mode"] != "relay" {
		t.Fatalf("mode=%v want relay", resp["mode"])
	}
	if n := twin146QRCount(t, resp); n != 1 {
		t.Fatalf("relay 方式应只有 1 个二维码，实际 %d：%v", n, resp["qr"])
	}
	if qr, _ := resp["qr"].(map[string]any); qr["中转"] == nil {
		t.Fatalf("relay 方式二维码应是中转：%v", resp["qr"])
	}
	b, err := os.ReadFile(filepath.Join(kitDir, "nodes.txt"))
	if err != nil {
		t.Fatalf("twin 后 nodes.txt 不存在：%v", err)
	}
	// 注意：nodes.txt 顶部注释里本身就含“中转：”“直连：”字样，断言须按解析后的分组查，不能直接 grep 全文。
	if !strings.Contains(string(b), rl) {
		t.Fatalf("历史应记中转链接：\n%s", string(b))
	}
	groups := parseNodesTxt(b)
	if len(groups) != 1 || len(groups[0].Links) != 1 || groups[0].Links[0].Label != "中转" {
		t.Fatalf("历史分组应只有 1 条中转链接：%+v", groups)
	}
}

func TestTwinModeDirect(t *testing.T) {
	s, kitDir, _ := newTwin146Server(t, twin122Stub, twin146StdConf)
	code, resp := postTwin(t, s, `{"link":"`+test122OrigLink+`","landing_ip":"203.0.113.1","mode":"direct"}`)
	if code != 200 {
		t.Fatalf("direct 方式状态=%d resp=%v", code, resp)
	}
	if rl, _ := resp["relay_link"].(string); rl != "" {
		t.Fatalf("direct 方式不应返回中转链接：%q", rl)
	}
	dl, _ := resp["direct_link"].(string)
	if dl == "" || !strings.Contains(dl, "@203.0.113.1:443") {
		t.Fatalf("direct 方式应返回落地直连链接：%q", dl)
	}
	if resp["mode"] != "direct" {
		t.Fatalf("mode=%v want direct", resp["mode"])
	}
	if n := twin146QRCount(t, resp); n != 1 {
		t.Fatalf("direct 方式应只有 1 个二维码，实际 %d：%v", n, resp["qr"])
	}
	if qr, _ := resp["qr"].(map[string]any); qr["直连"] == nil {
		t.Fatalf("direct 方式二维码应是直连：%v", resp["qr"])
	}
	b, err := os.ReadFile(filepath.Join(kitDir, "nodes.txt"))
	if err != nil {
		t.Fatalf("twin 后 nodes.txt 不存在：%v", err)
	}
	// 注意：nodes.txt 顶部注释里本身就含“中转：”“直连：”字样，断言须按解析后的分组查，不能直接 grep 全文。
	if !strings.Contains(string(b), dl) {
		t.Fatalf("历史应记直连链接：\n%s", string(b))
	}
	groups := parseNodesTxt(b)
	if len(groups) != 1 || len(groups[0].Links) != 1 || groups[0].Links[0].Label != "直连" {
		t.Fatalf("历史分组应只有 1 条直连链接：%+v", groups)
	}
}

func TestTwinModeDefaultBoth(t *testing.T) {
	s, kitDir, _ := newTwin146Server(t, twin122Stub, twin146StdConf)
	// 不传 mode：默认 both，与旧行为一致
	code, resp := postTwin(t, s, `{"link":"`+test122OrigLink+`","landing_ip":"203.0.113.1"}`)
	if code != 200 {
		t.Fatalf("默认方式状态=%d resp=%v", code, resp)
	}
	rl, _ := resp["relay_link"].(string)
	dl, _ := resp["direct_link"].(string)
	if rl == "" || dl == "" {
		t.Fatalf("默认应两条都返回：relay=%q direct=%q", rl, dl)
	}
	if resp["mode"] != "both" {
		t.Fatalf("mode=%v want both", resp["mode"])
	}
	if n := twin146QRCount(t, resp); n != 2 {
		t.Fatalf("默认应有 2 个二维码，实际 %d", n)
	}
	b, _ := os.ReadFile(filepath.Join(kitDir, "nodes.txt"))
	if !strings.Contains(string(b), rl) || !strings.Contains(string(b), dl) {
		t.Fatalf("默认历史应两条链接都有：\n%s", string(b))
	}
	// 显式传 both 也一样
	code, resp = postTwin(t, s, `{"link":"`+test122OrigLink+`","landing_ip":"203.0.113.1","mode":"both"}`)
	if code != 200 {
		t.Fatalf("显式 both 状态=%d", code)
	}
	if rl, _ := resp["relay_link"].(string); rl == "" {
		t.Fatalf("显式 both 应返回中转链接")
	}
	if dl, _ := resp["direct_link"].(string); dl == "" {
		t.Fatalf("显式 both 应返回直连链接")
	}
}

func TestTwinModeInvalid(t *testing.T) {
	s, _, _ := newTwin146Server(t, twin122Stub, twin146StdConf)
	for _, bad := range []string{"all", "BOTH", "relay ", "0", "direct,relay"} {
		code, _ := postTwin(t, s, `{"link":"`+test122OrigLink+`","landing_ip":"203.0.113.1","mode":"`+bad+`"}`)
		if code != 400 {
			t.Fatalf("mode=%q 应 400，实际 %d", bad, code)
		}
	}
}

func TestTwinModeRelayDoor(t *testing.T) {
	// 任意门模式 + 仅中转：应走 --door 出任意门链接，直连不返回
	doorConf := "RELAY_ADDR=\"203.0.113.9\"\nLANDING=203.0.113.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"example.com\"\nCERT_DOMAIN=\"example.com\"\nPRESET=1\nMODE_NOTE=\"tiktok-node\"\nMODE=\"door\"\nPORTS=\"tcp:20443:443\"\nLANDING_COUNT=1\nALL_PORTS=\"tcp:20443:443\"\nSHARE80=0\nSHARE443=0\n"
	s, kitDir, _ := newTwin146Server(t, twinDoorStub, doorConf)
	link := "vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test"
	code, resp := postTwin(t, s, `{"link":"`+link+`","landing_ip":"203.0.113.1","mode":"relay"}`)
	if code != 200 {
		t.Fatalf("door relay 方式状态=%d resp=%v", code, resp)
	}
	if resp["relay_label"] != "任意门" {
		t.Fatalf("relay_label=%v want 任意门", resp["relay_label"])
	}
	rl, _ := resp["relay_link"].(string)
	if !strings.Contains(rl, "@203.0.113.9:20443") {
		t.Fatalf("door relay 方式中转链接不对：%q", rl)
	}
	if dl, _ := resp["direct_link"].(string); dl != "" {
		t.Fatalf("door relay 方式不应返回直连链接：%q", dl)
	}
	if n := twin146QRCount(t, resp); n != 1 {
		t.Fatalf("door relay 方式应只有 1 个二维码，实际 %d", n)
	}
	b, _ := os.ReadFile(filepath.Join(kitDir, "nodes.txt"))
	// 注意：nodes.txt 顶部注释里本身就含“直连：”字样，断言须按解析后的分组查，不能直接 grep 全文。
	if !strings.Contains(string(b), rl) {
		t.Fatalf("door relay 方式历史应记任意门链接：\n%s", string(b))
	}
	groups := parseNodesTxt(b)
	if len(groups) != 1 || len(groups[0].Links) != 1 || groups[0].Links[0].Label != "任意门" {
		t.Fatalf("door relay 方式历史分组应只有 1 条任意门链接：%+v", groups)
	}
}
