package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1.2.1：二维码纯 Go 生成 ----------

func TestQRDataURINoQrencode(t *testing.T) {
	// 本机无 qrencode（本 VM 就没有），纯 Go 也必须出图，不依赖主机工具
	uri, err := qrDataURI("vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls#test")
	if err != nil {
		t.Fatalf("qrDataURI: %v", err)
	}
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("前缀不对：%.40s", uri)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("base64 解码失败：%v", err)
	}
	if len(raw) < 8 || string(raw[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("解出来不是 PNG")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("PNG 解析失败：%v", err)
	}
	if cfg.Width != 256 || cfg.Height != 256 {
		t.Fatalf("尺寸=%dx%d want 256x256", cfg.Width, cfg.Height)
	}
	// 超长内容应报错，而不是静默出空图
	if _, err := qrDataURI(strings.Repeat("x", 10000)); err == nil {
		t.Fatalf("10000 字符应报错")
	}
}

// ---------- 1.2.1：twin 写 nodes.txt ----------

const twinPersistLink = "vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test"
const twinPersistDirect = "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls&sni=example.com#test-直连"
const twinPersistDoor = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:20443?security=tls&sni=example.com#test-%E4%B8%AD%E8%BD%AC" // 1.2.2：中转/任意门备注统一是“<名称>-中转”，中文按 setLinkHost 规则百分号编码

func TestUpsertTwinLinksNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.txt")
	if err := upsertTwinLinks(path, "203.0.113.1", "203.0.113.1", "中转", twinPersistLink, twinPersistDirect); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("文件不存在：%v", err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Fatalf("权限=%o want 0644", st.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "## 落地 1：203.0.113.1（手动 twin）") {
		t.Fatalf("缺 twin 分组头：\n%s", string(b))
	}
	groups := parseNodesTxt(b)
	if len(groups) != 1 || len(groups[0].Links) != 2 {
		t.Fatalf("groups=%+v want 1 组 2 条", groups)
	}
	if groups[0].Mode != "standard" {
		t.Fatalf("mode=%q want standard", groups[0].Mode)
	}
}

func TestUpsertTwinLinksExistingGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.txt")
	seed := "# relay-node 双节点\n\n## 落地 1：203.0.113.1（tiktok-node）\n\n[第 1 条]\n中转：vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls#old\n直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls#old-直连\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	// 追加一条新 twin
	if err := upsertTwinLinks(path, "203.0.113.1", "203.0.113.1", "中转", twinPersistLink, twinPersistDirect); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	b, _ := os.ReadFile(path)
	groups := parseNodesTxt(b)
	if len(groups) != 1 || len(groups[0].Links) != 4 {
		t.Fatalf("追加后应 1 组 4 条（2 条记录），实际 %+v", groups)
	}
	if !strings.Contains(string(b), "[第 2 条]") {
		t.Fatalf("缺 [第 2 条]：\n%s", string(b))
	}
	// 重复 twin 同一条：不重复
	if err := upsertTwinLinks(path, "203.0.113.1", "203.0.113.1", "中转", twinPersistLink, twinPersistDirect); err != nil {
		t.Fatalf("重复 upsert: %v", err)
	}
	b, _ = os.ReadFile(path)
	groups = parseNodesTxt(b)
	if len(groups) != 1 || len(groups[0].Links) != 4 {
		t.Fatalf("重复 twin 后应仍 1 组 4 条，实际 %+v", groups)
	}
	// 换一条新链接：再追加一条
	other := "vless://11111111-2222-3333-4444-555555555555@example.com:8443?security=tls#other"
	otherDirect := "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:8443?security=tls#other-直连"
	if err := upsertTwinLinks(path, "203.0.113.1", "203.0.113.1", "中转", other, otherDirect); err != nil {
		t.Fatalf("upsert 新链接: %v", err)
	}
	b, _ = os.ReadFile(path)
	groups = parseNodesTxt(b)
	if len(groups) != 1 || len(groups[0].Links) != 6 {
		t.Fatalf("第三条后应 1 组 6 条，实际 %+v", groups)
	}
}

func TestUpsertTwinLinksDoorAndNewIP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.txt")
	seed := "# relay-node 双节点\n\n## 落地 1：203.0.113.1（tiktok-node）\n\n[第 1 条]\n中转：vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls#old\n直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls#old-直连\n\n## 落地 2：203.0.113.2（tiktok-node）\n\n[第 1 条]\n中转：vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls#old2\n直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.2:443?security=tls#old2-直连\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	// 新 IP：新分组编号 = 最大编号 + 1（3），任意门行正确
	if err := upsertTwinLinks(path, "203.0.113.9", "203.0.113.9", "任意门", twinPersistDoor, twinPersistDirect); err != nil {
		t.Fatalf("upsert 新 IP: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "## 落地 3：203.0.113.9（手动 twin）") {
		t.Fatalf("新分组头应为 落地 3：\n%s", string(b))
	}
	groups := parseNodesTxt(b)
	if len(groups) != 3 {
		t.Fatalf("groups=%d want 3", len(groups))
	}
	if groups[2].Links[0].Label != "任意门" || !strings.Contains(groups[2].Links[0].Link, "@203.0.113.9:20443") {
		t.Fatalf("任意门行不对：%+v", groups[2].Links)
	}
	if groups[2].Mode != "door" {
		t.Fatalf("mode=%q want door", groups[2].Mode)
	}
}

func TestUpsertTwinLinksUpdateTwinGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.txt")
	seed := "# relay-node 双节点\n\n## 落地 2：203.0.113.1（手动 twin）\n# 手动 twin：面板节点链接页生成，install.sh --nodes 不会洗掉\n\n[第 1 条]\n中转：vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls#old\n直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls#old-直连\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	// 同一落地再 twin 一条新链接：twin 分组原地替换，仍只有 1 条记录
	if err := upsertTwinLinks(path, "203.0.113.1", "203.0.113.1", "中转", twinPersistLink, twinPersistDirect); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	b, _ := os.ReadFile(path)
	groups := parseNodesTxt(b)
	if len(groups) != 1 || len(groups[0].Links) != 2 {
		t.Fatalf("twin 分组原地替换后应 1 组 2 条，实际 %+v", groups)
	}
	if groups[0].Links[0].Link != twinPersistLink {
		t.Fatalf("链接没换成新的：%+v", groups[0].Links)
	}
	if !strings.Contains(string(b), "## 落地 2：203.0.113.1（手动 twin）") {
		t.Fatalf("分组头标记丢了：\n%s", string(b))
	}
}

func TestUpsertTwinLinksMatchRawLanding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.txt")
	seed := "# relay-node 双节点\n\n## 落地 1：node.example.com（tiktok-node）\n\n[第 1 条]\n中转：vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls#old\n直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls#old-直连\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	// 分组头是域名、twin 传的是解析后的 IP：按原始输入也能匹配上，不另起分组
	if err := upsertTwinLinks(path, "203.0.113.1", "node.example.com", "中转", twinPersistLink, twinPersistDirect); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	b, _ := os.ReadFile(path)
	groups := parseNodesTxt(b)
	if len(groups) != 1 {
		t.Fatalf("应仍 1 组，实际 %d：\n%s", len(groups), string(b))
	}
	if len(groups[0].Links) != 4 {
		t.Fatalf("应追加成 4 条，实际 %+v", groups[0].Links)
	}
}

// ---------- 1.2.1：handleTwin 落盘 + 历史可读 ----------

func TestHandleTwinPersistsToHistory(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	stubLog := filepath.Join(t.TempDir(), "stub.log")
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(twinDoorStub), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "RELAY_ADDR=\"203.0.113.9\"\nLANDING=203.0.113.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"example.com\"\nCERT_DOMAIN=\"example.com\"\nPRESET=1\nMODE_NOTE=\"tiktok-node\"\nMODE=\"standard\"\nPORTS=\"tcp:443:443\"\nLANDING_COUNT=1\nALL_PORTS=\"tcp:443:443\"\nSHARE80=0\nSHARE443=0\n"
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_LOG", stubLog)
	s := newTestAddLandingServer(t, kitDir, realmDir)
	body := `{"link":"` + twinPersistLink + `","landing_ip":"203.0.113.1"}`
	code, resp := postTwin(t, s, body)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("twin：code=%d resp=%v", code, resp)
	}
	qr, _ := resp["qr"].(map[string]any)
	if len(qr) != 2 {
		t.Fatalf("twin 响应应带 2 个二维码，实际 %v", resp["qr"])
	}
	for k, v := range qr {
		vs, _ := v.(string)
		if !strings.HasPrefix(vs, "data:image/png;base64,") {
			t.Fatalf("二维码 %s 不是 data URI：%.40s", k, vs)
		}
	}
	if _, hasPersistErr := resp["persist_error"]; hasPersistErr {
		t.Fatalf("不应有 persist_error：%v", resp["persist_error"])
	}
	// nodes.txt 应已写入 twin 分组
	nodesPath := filepath.Join(kitDir, "nodes.txt")
	b, err := os.ReadFile(nodesPath)
	if err != nil {
		t.Fatalf("twin 后 nodes.txt 不存在：%v", err)
	}
	if !strings.Contains(string(b), "## 落地 1：203.0.113.1（手动 twin）") {
		t.Fatalf("缺 twin 分组头：\n%s", string(b))
	}
	// 重复 twin 同一条：不重复
	if code, _ := postTwin(t, s, body); code != 200 {
		t.Fatalf("重复 twin 状态=%d", code)
	}
	b, _ = os.ReadFile(nodesPath)
	if strings.Count(string(b), "[第 1 条]") != 1 || strings.Contains(string(b), "[第 2 条]") {
		t.Fatalf("重复 twin 不应追加条目：\n%s", string(b))
	}
	// 历史接口能读到
	var lr struct {
		Groups []linkGroup `json:"groups"`
	}
	rec := httptest.NewRecorder()
	s.handleLinks(rec, httptest.NewRequest("GET", "/api/v1/links", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatalf("历史响应不是 JSON：%v", err)
	}
	if len(lr.Groups) != 1 || len(lr.Groups[0].Links) != 2 {
		t.Fatalf("历史 groups=%+v want 1 组 2 条", lr.Groups)
	}
	// 重启面板（新 Server 同目录）历史还在
	s2 := newTestAddLandingServer(t, kitDir, realmDir)
	rec2 := httptest.NewRecorder()
	s2.handleLinks(rec2, httptest.NewRequest("GET", "/api/v1/links", nil))
	var lr2 struct {
		Groups []linkGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &lr2); err != nil {
		t.Fatalf("重启后历史响应不是 JSON：%v", err)
	}
	if len(lr2.Groups) != 1 || len(lr2.Groups[0].Links) != 2 {
		t.Fatalf("重启后历史丢了：%+v", lr2.Groups)
	}
}

func TestHandleTwinPersistsDoor(t *testing.T) {
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
	s := newTestAddLandingServer(t, kitDir, realmDir)
	code, resp := postTwin(t, s, `{"link":"`+twinPersistLink+`","landing_ip":"203.0.113.1"}`)
	if code != 200 || resp["relay_label"] != "任意门" {
		t.Fatalf("door twin：code=%d resp=%v", code, resp)
	}
	b, err := os.ReadFile(filepath.Join(kitDir, "nodes.txt"))
	if err != nil {
		t.Fatalf("twin 后 nodes.txt 不存在：%v", err)
	}
	if !strings.Contains(string(b), "任意门："+twinPersistDoor) {
		t.Fatalf("nodes.txt 里没有任意门行：\n%s", string(b))
	}
	groups := parseNodesTxt(b)
	if len(groups) != 1 || groups[0].Mode != "door" {
		t.Fatalf("door 分组 mode 应为 door：%+v", groups)
	}
}
