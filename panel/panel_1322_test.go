package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1.3.22：Logo 按钮间距 + 删旧链接提示 + 域名选项卡布局根因修复 + 系统选项卡 ----------

// settingsIndexHTML1322 读内嵌的 index.html（回归测试用）。
func settingsIndexHTML1322(t *testing.T) string {
	t.Helper()
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读内嵌 index.html 失败：%v", err)
	}
	return string(b)
}

// TestSettingsDomainPanelOutsideTablist1322 域名面板布局根因回归：
// 1.3.21 里 settings-panel-domain 嵌在 .settings-tabs（flex + overflow-x:auto）
// 里面，激活时把选项卡栏撑宽、出现横向滚动、卡片偏右。本版要求它与其它面板
// 一样是 tablist 外的兄弟节点（按选项卡顺序排在订阅面板之后、提醒面板之前）。
func TestSettingsDomainPanelOutsideTablist1322(t *testing.T) {
	html := settingsIndexHTML1322(t)
	tablistOpen := strings.Index(html, `<div class="settings-tabs"`)
	if tablistOpen < 0 {
		t.Fatal("找不到 .settings-tabs")
	}
	// tablist 里只有 8 个 button 子节点：从 tablist 开头到它第一个 </div> 之间
	// 不应出现任何 <div（面板 div 嵌进去是本 bug 的根因）。
	tablistClose := strings.Index(html[tablistOpen:], "</div>")
	if tablistClose < 0 {
		t.Fatal("找不到 .settings-tabs 的关闭")
	}
	// 内侧从开标签的 > 之后算起（开标签本身含 <div，不能算进去）。
	openEnd := strings.Index(html[tablistOpen:], ">")
	tabInner := html[tablistOpen+openEnd+1 : tablistOpen+tablistClose]
	if strings.Contains(tabInner, "<div") {
		t.Fatalf("tablist 里不应再有 <div>（域名面板又嵌进去了）：%q", tabInner[:200])
	}
	if n := strings.Count(tabInner, `class="settings-tab"`); n != 8 {
		t.Fatalf("设置页应有 8 个选项卡按钮，实际 %d", n)
	}
	// 域名面板在 tablist 关闭之后，且按顺序在订阅面板之后、提醒面板之前。
	domainIdx := strings.Index(html, `id="settings-panel-domain"`)
	if domainIdx < tablistOpen+tablistClose {
		t.Fatal("域名面板仍在 tablist 里面")
	}
	subIdx := strings.Index(html, `id="settings-panel-subscription"`)
	alertsIdx := strings.Index(html, `id="settings-panel-alerts"`)
	if !(subIdx < domainIdx && domainIdx < alertsIdx) {
		t.Fatalf("域名面板顺序应为订阅 < 域名 < 提醒，实际 sub=%d domain=%d alerts=%d", subIdx, domainIdx, alertsIdx)
	}
}

// TestSettingsSystemTab1322 系统选项卡：8 个选项卡里最后一个是系统；
// 检查更新与导出/导入两张卡在系统面板里；基础面板不再有检查更新；
// 备份面板只留备份与远程备份两张卡（备份在上、远程备份在下）。
func TestSettingsSystemTab1322(t *testing.T) {
	html := settingsIndexHTML1322(t)
	if !strings.Contains(html, `id="settings-tab-system"`) || !strings.Contains(html, `id="settings-panel-system"`) {
		t.Fatal("找不到系统选项卡按钮/面板")
	}
	sysIdx := strings.Index(html, `id="settings-panel-system"`)
	updIdx := strings.Index(html, `id="update-check"`)
	expIdx := strings.Index(html, `id="export-btn"`)
	if !(sysIdx < updIdx && sysIdx < expIdx) {
		t.Fatalf("检查更新与导出/导入应在系统面板里，实际 system=%d update=%d export=%d", sysIdx, updIdx, expIdx)
	}
	basic := html[strings.Index(html, `id="settings-panel-basic"`):strings.Index(html, `id="settings-panel-security"`)]
	if strings.Contains(basic, `id="update-check"`) {
		t.Fatal("基础面板里不应再有检查更新卡片")
	}
	backup := html[strings.Index(html, `id="settings-panel-backup"`):strings.Index(html, `id="settings-panel-system"`)]
	if strings.Contains(backup, `id="export-btn"`) {
		t.Fatal("备份面板里不应再有导出/导入卡片")
	}
	// 备份面板应只有两张卡，左右并排：备份在左、远程备份在右（grid cols-2，
	// 与订阅选项卡的订阅链接/订阅用户同款两列布局）。
	// 注意备份卡里有两个 h2（“备份”与“自动定时备份”），数卡片而不是数 h2。
	if !strings.Contains(backup, `<div class="grid cols-2">`) {
		t.Fatal("备份面板的两张卡应左右并排（grid cols-2）")
	}
	cards := strings.Split(backup, `<div class="card">`)
	if len(cards) != 3 {
		t.Fatalf("备份面板应只有两张卡，实际 %d 张", len(cards)-1)
	}
	if !strings.Contains(cards[1], "<h2>备份</h2>") || !strings.Contains(cards[1], "自动定时备份") {
		t.Fatalf("备份面板第一张卡（左）应是备份（含自动定时备份），实际 %q", cards[1][:200])
	}
	if !strings.Contains(cards[2], "远程备份（异地备份）") {
		t.Fatalf("备份面板第二张卡（右）应是远程备份，实际 %q", cards[2][:200])
	}
}

// TestSettingsLogoButtonsSpacing1322 Logo 卡片的上传/删除按钮与检查更新
// 按钮同款：放在带 gap 的 .btn-row 里（1.3.21 用的是无样式的 .row，
// 两个按钮直接贴在一起）。
func TestSettingsLogoButtonsSpacing1322(t *testing.T) {
	html := settingsIndexHTML1322(t)
	css, err := webFS.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("读内嵌 style.css 失败：%v", err)
	}
	if !strings.Contains(string(css), ".btn-row{display:flex; gap:8px;") {
		t.Fatal("style.css 里 .btn-row 应是 flex + gap:8px")
	}
	i := strings.Index(html, `id="logo-input"`)
	if i < 0 {
		t.Fatal("找不到 logo-input")
	}
	rowStart := strings.LastIndex(html[:i], "<div")
	rowTag := html[rowStart : strings.Index(html[rowStart:], ">")+rowStart]
	if !strings.Contains(rowTag, `class="btn-row"`) {
		t.Fatalf("Logo 按钮行应用 .btn-row，实际 %q", rowTag)
	}
	if strings.Contains(html, `class="row"><input type="file" id="logo-input"`) {
		t.Fatal("Logo 行仍在用无样式的 .row")
	}
}

// TestSubUserLegacyHintRemoved1322 订阅用户里的旧链接提示句已删
// （两处静态文案：订阅用户卡片段落、合并订阅列表底部说明；后端对旧链接的处理
// 不动：真遇到旧链接时点轮换仍走原来的确认流程）。
func TestSubUserLegacyHintRemoved1322(t *testing.T) {
	html := settingsIndexHTML1322(t)
	if strings.Contains(html, "1.3.7 之前生成的") {
		t.Fatal("订阅用户里的旧链接提示句应已删掉")
	}
	js, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读内嵌 app.js 失败：%v", err)
	}
	if strings.Contains(string(js), "旧链接（1.3.7 之前生成的）没有存密文，点轮换生成一条新的（旧链接立即失效）") {
		t.Fatal("订阅用户编辑弹窗里的旧链接提示句应已删掉")
	}
}

// ---------- 1.3.22：订阅回归 A——空订阅不出空策略组 ----------

var vlessTwin1322 = subLink{Name: "智利-中转", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转"}

var ssOnly1322 = subLink{Name: "SS节点", Link: "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388#SS节点"}

// TestRenderClashYAMLEmpty1322 节点为空时不出空策略组：以前会出 5 个
// proxies: 为空的组，Clash 导入直接报
// “proxy group[0]: 'use' or 'proxies' missing”。现在只出合法的空配置
// （proxies: []、proxy-groups: []、规则只留 MATCH,DIRECT），不引用
// 不存在的组。
func TestRenderClashYAMLEmpty1322(t *testing.T) {
	out := renderClashYAML(nil, "split")
	if !strings.Contains(out, "proxies: []") || !strings.Contains(out, "proxy-groups: []") {
		t.Fatalf("空订阅应出 proxies: [] 与 proxy-groups: []，实际：\n%s", out)
	}
	if strings.Contains(out, "  - name:") || strings.Contains(out, "手动选择") || strings.Contains(out, "社交") || strings.Contains(out, "流媒体") {
		t.Fatalf("空订阅不应出任何策略组或引用组名的规则，实际：\n%s", out)
	}
	if !strings.Contains(out, "- MATCH,DIRECT") {
		t.Fatalf("空订阅规则应只留 MATCH,DIRECT，实际：\n%s", out)
	}
	out = renderClashYAML(nil, "global")
	if strings.Contains(out, "手动选择") {
		t.Fatalf("空订阅 global 模式也不应引用手动选择组，实际：\n%s", out)
	}
}

// TestRenderClashYAMLSSOnly1322 全是 SS 时：1.3.22 以前 SS 不进 Clash，
// 出空配置；1.3.23 起 clashProxyFromLink 有了 ss 分支，SS 正常进
// YAML（type: ss + cipher + password + udp: true），不再出空配置。
func TestRenderClashYAMLSSOnly1322(t *testing.T) {
	out := renderClashYAML([]subLink{ssOnly1322}, "split")
	if strings.Contains(out, "proxy-groups: []") {
		t.Fatalf("SS-only 订阅 1.3.23 起应有 SS 节点，不应出空配置，实际：\n%s", out)
	}
	for _, want := range []string{"name: \"SS节点\"", "type: ss", "cipher: \"aes-256-gcm\"", "password: \"password\"", "udp: true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("SS-only 订阅缺 %q，实际：\n%s", want, out)
		}
	}
}

// TestRenderClashYAMLHY2Only1322 全是 Clash 不支持的类型（HY2/TUIC
// 仍会被 clashProxyFromLink 跳过）时不出空组。
func TestRenderClashYAMLHY2Only1322(t *testing.T) {
	hy2 := subLink{Name: "HY2节点", Link: "hysteria2://pass@203.0.113.1:8443#HY2节点"}
	out := renderClashYAML([]subLink{hy2}, "split")
	if !strings.Contains(out, "proxy-groups: []") {
		t.Fatalf("HY2-only 订阅应出空配置（HY2 不进 Clash），实际：\n%s", out)
	}
}

// TestRenderClashYAMLMixed1322 VLESS + SS 混合时两者都进 YAML，
// 每个策略组都有非空 proxies。
func TestRenderClashYAMLMixed1322(t *testing.T) {
	out := renderClashYAML([]subLink{vlessTwin1322, ssOnly1322}, "split")
	for _, g := range []string{"自动选择", "GLOBAL", "手动选择", "社交", "流媒体", "中转节点"} {
		if !strings.Contains(out, "name: \""+g+"\"") {
			t.Fatalf("混合订阅应有 %s 组，实际：\n%s", g, out)
		}
	}
	if strings.Contains(out, "直连节点") {
		t.Fatalf("混合订阅只有中转链接，不应出直连节点组，实际：\n%s", out)
	}
	if !strings.Contains(out, "name: \"SS节点\"") || !strings.Contains(out, "type: ss") {
		t.Fatalf("混合订阅里 SS 节点应进 proxies，实际：\n%s", out)
	}
	// 每个组名后面都应跟着非空的 proxies 列表（组里至少有一条 - "名字"）。
	// 只看 proxy-groups 一节：proxies 一节里的节点条目也以 "  - name: " 开头。
	gi := strings.Index(out, "proxy-groups:\n")
	if gi < 0 {
		t.Fatalf("应有 proxy-groups 一节，实际：\n%s", out)
	}
	groups := strings.Split(out[gi:], "  - name: ")
	for _, seg := range groups[1:] {
		name := seg[:strings.Index(seg, "\n")]
		proxiesPart := seg[strings.Index(seg, "proxies:"):]
		if !strings.Contains(proxiesPart, "      - ") {
			t.Fatalf("组 %s 的 proxies 为空，实际：\n%s", name, out)
		}
	}
}

// ---------- 1.3.22：订阅回归 B——订阅文件名称设置项 ----------

// subFileNamePOST 直接调文件名保存接口，返回状态码与解析后的 JSON。
func subFileNamePOST(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/settings/subscription-filename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleSubFileNameSet(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// TestSubFileNameValidation1322 文件名校验：1–64 字，禁
// / \ : * ? " < > | 与控制字符。
func TestSubFileNameValidation1322(t *testing.T) {
	for _, good := range []string{"CP管理面板", "my-sub", "a", "订阅 2026"} {
		if !validSubFileName(good) {
			t.Fatalf("应合法：%q", good)
		}
	}
	for _, bad := range []string{"", "a/b", `a\b`, "a:b", "a*b", "a?b", `a"b`, "a<b", "a>b", "a|b", "a\nb", strings.Repeat("x", 65)} {
		if validSubFileName(bad) {
			t.Fatalf("应非法：%q", bad)
		}
	}
}

// TestSubFileNameDisposition1322 文件名生成：填了用填的，留空回落面板名；
// 后缀按格式；非 ASCII 走 RFC 5987 filename*，同时保留 ASCII 兜底。
func TestSubFileNameDisposition1322(t *testing.T) {
	s := newTestSubServer(t)
	s.cfg.PanelName = "CP管理面板"
	if d := s.subContentDisposition("clash"); !strings.Contains(d, "filename*=UTF-8''CP%E7%AE%A1%E7%90%86%E9%9D%A2%E6%9D%BF.yaml") || !strings.Contains(d, `filename="subscription.yaml"`) {
		t.Fatalf("留空应回落面板名（RFC 5987 + 通用名兜底），实际：%s", d)
	}
	if d := s.subContentDisposition("plain"); !strings.Contains(d, "filename*=UTF-8''CP%E7%AE%A1%E7%90%86%E9%9D%A2%E6%9D%BF.txt") {
		t.Fatalf("plain 格式后缀应是 .txt，实际：%s", d)
	}
	s.cfg.SubFileName = "my-sub"
	if d := s.subContentDisposition("clash"); d != `attachment; filename="my-sub.yaml"` {
		t.Fatalf("ASCII 自定义名应直接用 filename=，实际：%s", d)
	}
	if d := s.subContentDisposition("base64"); d != `attachment; filename="my-sub.txt"` {
		t.Fatalf("base64 格式后缀应是 .txt，实际：%s", d)
	}
	s.cfg.SubFileName = "我的订阅"
	if d := s.subContentDisposition("clash"); !strings.Contains(d, "filename*=UTF-8''%E6%88%91%E7%9A%84%E8%AE%A2%E9%98%85.yaml") {
		t.Fatalf("非 ASCII 自定义名应走 filename*，实际：%s", d)
	}
}

// TestSubFileNameSet1322 文件名保存接口：非法 400；合法保存后设置接口
// 下发同一份、落盘重读一致；清空后回落面板名。
func TestSubFileNameSet1322(t *testing.T) {
	s := newTestSubServer(t)
	if code, _ := subFileNamePOST(t, s, `{"name":"a/b"}`); code != 400 {
		t.Fatalf("含 / 的文件名应 400，实际 %d", code)
	}
	if code, resp := subFileNamePOST(t, s, `{"name":"  我的订阅  "}`); code != 200 || resp["file_name"] != "我的订阅" {
		t.Fatalf("保存应 200 并回显去空格后的名字，实际 %d %v", code, resp)
	}
	if s.cfg.SubFileName != "我的订阅" {
		t.Fatalf("cfg 里应是“我的订阅”，实际 %q", s.cfg.SubFileName)
	}
	req := httptest.NewRequest("GET", "/api/v1/settings", nil)
	rec := httptest.NewRecorder()
	s.handleSettings(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	sub, _ := resp["subscription"].(map[string]any)
	if sub["file_name"] != "我的订阅" {
		t.Fatalf("设置接口下发的 file_name 应是“我的订阅”，实际：%v", sub["file_name"])
	}
	cfgBytes, err := os.ReadFile(filepath.Join(s.env.PanelHome, "config.json"))
	if err != nil {
		t.Fatalf("重读 config.json 应成功：%v", err)
	}
	var cfg PanelConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		t.Fatalf("解析 config.json 应成功：%v", err)
	}
	if cfg.SubFileName != "我的订阅" {
		t.Fatalf("落盘的 sub_file_name 应是“我的订阅”，实际 %q", cfg.SubFileName)
	}
	if code, resp := subFileNamePOST(t, s, `{"name":""}`); code != 200 || resp["file_name"] != "" {
		t.Fatalf("清空应 200 并回显空，实际 %d %v", code, resp)
	}
	if s.cfg.SubFileName != "" {
		t.Fatalf("清空后 cfg 应为空，实际 %q", s.cfg.SubFileName)
	}
}
