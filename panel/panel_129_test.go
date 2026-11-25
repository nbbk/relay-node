package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 1.2.9：订阅 Clash YAML 策略组＋分流规则 ----------

// groupBlock129 取 YAML 里某个策略组的整段（从 - name: "X" 到下一个组或
// rules: 为止），找不到返回 ""。
func groupBlock129(t *testing.T, body, name string) string {
	t.Helper()
	start := strings.Index(body, "- name: \""+name+"\"")
	if start < 0 {
		return ""
	}
	rest := body[start:]
	end := len(rest)
	if i := strings.Index(rest[1:], "\n  - name: "); i >= 0 && i+1 < end {
		end = i + 1
	}
	if i := strings.Index(rest[1:], "\nrules:\n"); i >= 0 && i+1 < end {
		end = i + 1
	}
	return rest[:end]
}

func TestClashGroups129(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	subUserForToken1427(t, s, tok)
	rec := callSub(t, s, tok, "/sub/"+tok+"?format=clash", "")
	if rec.Code != 200 {
		t.Fatalf("clash 应 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	// 7 个组，按顺序出现（v1.3.4 起自动选择排第一，GLOBAL 排第二）
	groups := []string{"自动选择", "GLOBAL", "手动选择", "中转节点", "直连节点", "社交", "流媒体"}
	prev := -1
	for _, g := range groups {
		i := strings.Index(body, "- name: \""+g+"\"")
		if i < 0 {
			t.Fatalf("缺策略组 %q：\n%s", g, body)
		}
		if i <= prev {
			t.Fatalf("策略组 %q 顺序不对", g)
		}
		prev = i
	}
	// 旧的 PROXY 组不应再出现
	if strings.Contains(body, "- name: \"PROXY\"") || strings.Contains(body, "MATCH,PROXY") {
		t.Fatalf("旧 PROXY 组应已去掉：\n%s", body)
	}
	// 自动选择是 url-test，带测速地址与间隔
	auto := groupBlock129(t, body, "自动选择")
	for _, want := range []string{"type: url-test", "url: \"http://www.gstatic.com/generate_204\"", "interval: 300"} {
		if !strings.Contains(auto, want) {
			t.Fatalf("自动选择组缺 %q：\n%s", want, auto)
		}
	}
	// 中转组只有两条中转链接，直连组只有两条直连链接
	relay := groupBlock129(t, body, "中转节点")
	for _, want := range []string{"\"智利-中转\"", "\"test-中转\""} {
		if !strings.Contains(relay, want) {
			t.Fatalf("中转节点组缺 %s：\n%s", want, relay)
		}
	}
	for _, no := range []string{"\"智利-直连\"", "\"test-直连\""} {
		if strings.Contains(relay, no) {
			t.Fatalf("中转节点组不应含 %s：\n%s", no, relay)
		}
	}
	direct := groupBlock129(t, body, "直连节点")
	for _, want := range []string{"\"智利-直连\"", "\"test-直连\""} {
		if !strings.Contains(direct, want) {
			t.Fatalf("直连节点组缺 %s：\n%s", want, direct)
		}
	}
	for _, no := range []string{"\"智利-中转\"", "\"test-中转\""} {
		if strings.Contains(direct, no) {
			t.Fatalf("直连节点组不应含 %s：\n%s", no, direct)
		}
	}
	// 手动选择、社交、流媒体三组都含全部 4 条
	for _, g := range []string{"手动选择", "社交", "流媒体"} {
		blk := groupBlock129(t, body, g)
		for _, want := range []string{"\"智利-中转\"", "\"智利-直连\"", "\"test-中转\"", "\"test-直连\""} {
			if !strings.Contains(blk, want) {
				t.Fatalf("%s 组缺 %s：\n%s", g, want, blk)
			}
		}
	}
}

func TestClashRules129(t *testing.T) {
	links := []subLink{
		{Name: "智利-中转", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转"},
		{Name: "智利-直连", Link: "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-直连"},
	}
	split, _, err := renderSubContent(links, "clash", "split")
	if err != nil {
		t.Fatalf("分流渲染失败：%v", err)
	}
	// TikTok：8 个后缀 + keyword 补网，指向自动选择（1.3.23 起从手动选择改过来，
	// 死节点自动排除；清单里多了 isnssdk.com）
	for _, d := range []string{"tiktok.com", "tiktokv.com", "tiktokcdn.com", "tiktokcdn-us.com", "musical.ly", "byteoversea.com", "ibytedtos.com", "isnssdk.com"} {
		if !strings.Contains(split, "DOMAIN-SUFFIX,"+d+",自动选择") {
			t.Fatalf("缺 TikTok 规则 %s：\n%s", d, split)
		}
	}
	if !strings.Contains(split, "DOMAIN-KEYWORD,tiktok,自动选择") {
		t.Fatalf("缺 TikTok keyword 补网规则：\n%s", split)
	}
	// 社交：抽查关键域名，全部指向自动选择（1.3.23 起从社交组改过来）
	for _, d := range []string{"facebook.com", "fb.com", "instagram.com", "x.com", "twitter.com", "whatsapp.com", "telegram.org", "t.me", "snapchat.com", "reddit.com", "discord.com", "threads.net", "line.me"} {
		if !strings.Contains(split, "DOMAIN-SUFFIX,"+d+",自动选择") {
			t.Fatalf("缺社交规则 %s：\n%s", d, split)
		}
	}
	// 流媒体：抽查关键域名，全部指向自动选择（1.3.23 起从流媒体组改过来）
	for _, d := range []string{"youtube.com", "googlevideo.com", "youtu.be", "netflix.com", "nflxvideo.net", "disneyplus.com", "hulu.com", "max.com", "primevideo.com", "tv.apple.com", "spotify.com", "twitch.tv", "jtvnw.net"} {
		if !strings.Contains(split, "DOMAIN-SUFFIX,"+d+",自动选择") {
			t.Fatalf("缺流媒体规则 %s：\n%s", d, split)
		}
	}
	// 苹果只写 tv.apple.com，不写 apple.com
	if strings.Contains(split, "DOMAIN-SUFFIX,apple.com,") {
		t.Fatalf("不应出现 DOMAIN-SUFFIX,apple.com：\n%s", split)
	}
	// scdn.co 在社交与流媒体清单里都有，只出一次（先命中社交段）
	if strings.Count(split, "DOMAIN-SUFFIX,scdn.co,") != 1 || !strings.Contains(split, "DOMAIN-SUFFIX,scdn.co,自动选择") {
		t.Fatalf("scdn.co 应只出现一次且指向自动选择：\n%s", split)
	}
	// 规则顺序：TikTok → 社交 → 流媒体 → MATCH
	ti := strings.Index(split, "DOMAIN-SUFFIX,tiktok.com,")
	so := strings.Index(split, "DOMAIN-SUFFIX,facebook.com,")
	st := strings.Index(split, "DOMAIN-SUFFIX,youtube.com,")
	ma := strings.Index(split, "MATCH,")
	if ti < 0 || so < 0 || st < 0 || ma < 0 || !(ti < so && so < st && st < ma) {
		t.Fatalf("规则顺序应为 TikTok → 社交 → 流媒体 → MATCH：\n%s", split)
	}
	// 分流模式 MATCH,DIRECT，全局模式 MATCH,手动选择
	if !strings.Contains(split, "MATCH,DIRECT") {
		t.Fatalf("分流模式 MATCH 应是 DIRECT：\n%s", split)
	}
	global, _, err := renderSubContent(links, "clash", "global")
	if err != nil {
		t.Fatalf("全局渲染失败：%v", err)
	}
	if !strings.Contains(global, "MATCH,手动选择") || strings.Contains(global, "MATCH,DIRECT") {
		t.Fatalf("全局模式 MATCH 应是手动选择：\n%s", global)
	}
	// 三类规则在两种模式下相同（只有 MATCH 不同）
	if strings.Replace(split, "MATCH,DIRECT", "MATCH,手动选择", 1) != global {
		t.Fatalf("分流与全局除 MATCH 外应完全相同")
	}
}

func TestSubModeParam(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	subUserForToken1427(t, s, tok)
	url := "/sub/" + tok

	// 缺省 = 分流
	rec := callSub(t, s, tok, url+"?format=clash", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "MATCH,DIRECT") {
		t.Fatalf("缺省 mode 应是分流 MATCH,DIRECT：%d %s", rec.Code, rec.Body.String())
	}
	// 显式 split = 分流
	rec = callSub(t, s, tok, url+"?format=clash&mode=split", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "MATCH,DIRECT") {
		t.Fatalf("mode=split 应是分流：%d %s", rec.Code, rec.Body.String())
	}
	// global = 全局（大小写不敏感）
	rec = callSub(t, s, tok, url+"?format=clash&mode=GLOBAL", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "MATCH,手动选择") {
		t.Fatalf("mode=global 应是全局 MATCH,手动选择：%d %s", rec.Code, rec.Body.String())
	}
	// 非法 mode → 400
	rec = callSub(t, s, tok, url+"?format=clash&mode=bogus", "")
	if rec.Code != 400 {
		t.Fatalf("非法 mode 应 400，实际 %d", rec.Code)
	}
	// mode 不影响 base64：不含 groups/rules
	rec = callSub(t, s, tok, url+"?format=base64&mode=global", "")
	if rec.Code != 200 {
		t.Fatalf("base64+mode 应 200，实际 %d", rec.Code)
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rec.Body.String()))
	if err != nil {
		t.Fatalf("base64 解码失败：%v", err)
	}
	if strings.Contains(string(dec), "proxy-groups") || strings.Contains(string(dec), "rules:") {
		t.Fatalf("base64 里不应含 groups/rules：%s", dec)
	}
	// 带勾选的订阅用户同样走 mode：一份只含落地 1 的订阅，global 下 MATCH,手动选择
	// （1.4.27：合并订阅已退场，勾选改挂订阅用户身上）。
	rec2 := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users",
		`{"name":"只选落地1","selection":[{"panel":"local","landing":1}]}`)
	if rec2.Code != 200 {
		t.Fatalf("带勾选新建订阅用户应 200，实际 %d %s", rec2.Code, rec2.Body.String())
	}
	// 生成响应里就有带明文令牌的 URL，从里面取令牌再拉订阅验证 mode
	var merged map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &merged)
	murl, _ := merged["url"].(string)
	mtok := tokenFromSubURL130(t, murl)
	rec = callSub(t, s, mtok, "/sub/"+mtok+"?format=clash&mode=global", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "MATCH,手动选择") {
		t.Fatalf("只选落地 1 的订阅 mode=global 应是全局：%d %s", rec.Code, rec.Body.String())
	}
	if strings.Count(rec.Body.String(), "type: vless") != 2 || strings.Contains(rec.Body.String(), "node2.example.com") {
		t.Fatalf("只选落地 1 的订阅应只有它的 2 条链接：%s", rec.Body.String())
	}
}

func TestSubQR129(t *testing.T) {
	s := newTestSubServer(t)
	call := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/v1/subscription/qr", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleSubQR(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	// 带 format+mode 参数的完整订阅链接（约 150 字）应出合法 PNG
	longURL := "http://203.0.113.1:11111/sub/sub_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef?format=clash&mode=global"
	code, resp := call(`{"url":"` + longURL + `"}`)
	if code != 200 {
		t.Fatalf("有效订阅链接应 200，实际 %d %v", code, resp)
	}
	qr, _ := resp["qr"].(string)
	if !strings.HasPrefix(qr, "data:image/png;base64,") {
		t.Fatalf("应返回 PNG data URI：%.60s", qr)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(qr, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("二维码 base64 解码失败：%v", err)
	}
	if len(raw) < 8 || raw[0] != 0x89 || raw[1] != 0x50 || raw[2] != 0x4E || raw[3] != 0x47 {
		t.Fatalf("二维码应是合法 PNG（魔数 89 50 4E 47），实际前 8 字节 % x", raw[:8])
	}
	// 换 mode 后二维码内容跟着变（同一链接不同参数 = 不同图）
	_, resp2 := call(`{"url":"http://203.0.113.1:11111/sub/sub_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef?format=clash&mode=split"}`)
	qr2, _ := resp2["qr"].(string)
	if qr2 == "" || qr2 == qr {
		t.Fatalf("换 mode 后二维码应不同")
	}
	// 非订阅链接 → 400
	for _, bad := range []string{
		`{"url":""}`,
		`{"url":"https://example.com/foo"}`,
		`{"url":"http://203.0.113.1:11111/sub/other"}`,
		`{"url":"http://203.0.113.1:11111/sub/sub_` + strings.Repeat("a", 2100) + `"}`,
		`not json`,
		`{"url":"http://x/sub/sub_abc","extra":1}`,
	} {
		if code, _ := call(bad); code != 400 {
			t.Fatalf("非法请求 %q 应 400，实际 %d", bad[:40], code)
		}
	}
}

func TestClashRelayDirectGroupOmission(t *testing.T) {
	// 只有直连：不出现中转节点组；只有中转：不出现直连节点组
	directOnly := []subLink{
		{Name: "智利-直连", Link: "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-直连"},
	}
	body, _, err := renderSubContent(directOnly, "clash", "split")
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(body, "name: \"中转节点\"") {
		t.Fatalf("只有直连时不应出现中转节点组：\n%s", body)
	}
	if !strings.Contains(body, "name: \"直连节点\"") {
		t.Fatalf("只有直连时应出现直连节点组：\n%s", body)
	}
	relayOnly := []subLink{
		{Name: "智利-中转", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转"},
	}
	body, _, err = renderSubContent(relayOnly, "clash", "split")
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(body, "name: \"直连节点\"") {
		t.Fatalf("只有中转时不应出现直连节点组：\n%s", body)
	}
	if !strings.Contains(body, "name: \"中转节点\"") {
		t.Fatalf("只有中转时应出现中转节点组：\n%s", body)
	}
	// 没有 -中转/-直连 后缀的节点：只进全量组，不进中转/直连组
	mixed := []subLink{
		{Name: "测试", Link: "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=none&type=ws&path=%2Fexample-path#测试"},
		{Name: "智利-直连", Link: "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-直连"},
	}
	body, _, err = renderSubContent(mixed, "clash", "split")
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(body, "name: \"中转节点\"") {
		t.Fatalf("没有中转链接时不应出现中转节点组：\n%s", body)
	}
	if strings.Contains(groupBlock129(t, body, "直连节点"), "\"测试\"") {
		t.Fatalf("无后缀节点不应进直连节点组：\n%s", body)
	}
	if !strings.Contains(groupBlock129(t, body, "手动选择"), "\"测试\"") {
		t.Fatalf("无后缀节点应在手动选择组里：\n%s", body)
	}
}
