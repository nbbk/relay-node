package main

import (
	"os"
	"strings"
	"testing"
)

// ---------- 1.4.36：面板 UI/UX 重设计第 6 批（设置页整理＋死代码清理） ----------
//
// 设置页订阅二级撤掉（订阅选项卡与 SUB_TABS 全撤），“端口与倍率”改名
// “限流端口与倍率”移到系统页；管理面板模式下设置页藏掉“域名与反代”
// 选项卡；保存后只刷新受影响的部分（reloadSettingsForm，不重拉 7 个列表）；
// 死代码清理（P2-10）：openMergedSubModal 改名 openSubLinkModal、
// rotateMergedSub、openUserOpsMenu／openSubEditModal 的 merged 分支、
// 通用订阅回填、subTokenGenerate／loadGenericSubLink／saveSubExpiry、
// loadCentralSubTree 生成树一家。旧记录只读列表、退场提示、旧接口与
// 退场 410 判定全部保留。前端行为由 jsdom test-1436-batch6.js 覆盖。

func TestVersion1436(t *testing.T) {
	if Version != "1.4.43" {
		t.Fatalf("Version 应为 1.4.36，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.43" {
		t.Fatalf("VERSION 文件应为 1.4.36，实际 %q", strings.TrimSpace(string(vb)))
	}
}

func TestBatch6Frontend1436(t *testing.T) {
	jsB, err := os.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读 web/app.js 失败：%v", err)
	}
	htmlB, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 web/index.html 失败：%v", err)
	}
	js, html := string(jsB), string(htmlB)

	// 设置页订阅二级已撤（§4.5）：一级选项卡 7 个，没有订阅选项卡与二级。
	if strings.Contains(html, `id="settings-tab-subscription"`) || strings.Contains(html, `id="settings-panel-subscription"`) {
		t.Fatalf("设置页订阅选项卡应已撤掉")
	}
	for _, id := range []string{"sub-tab-plans", "sub-tab-groups", "sub-tab-other", "sub-panel-other"} {
		if strings.Contains(html, `id="`+id+`"`) {
			t.Fatalf("设置页订阅二级选项卡应已撤掉：还留着 %s", id)
		}
	}
	if !strings.Contains(js, "SETTINGS_TABS = ['basic', 'security', 'audit', 'domain', 'alerts', 'backup', 'system']") {
		t.Fatalf("SETTINGS_TABS 应为 7 个且不含 subscription")
	}
	for _, gone := range []string{"SUB_TABS", "showSubTab", "initSubTabs", "subTokenGenerate", "loadGenericSubLink", "saveSubExpiry", "openMergedSubModal", "rotateMergedSub", "loadCentralSubTree", "renderCentralSubTree", "refreshCentralSubTreeStates", "centralSubTree", "centralSubChecked", "subTreeCheckedCount", "isMerged"} {
		if strings.Contains(js, gone) {
			t.Fatalf("死代码应已删掉：%s", gone)
		}
	}
	// “端口与倍率”改名“限流端口与倍率”并移到系统页（元素 id 不变）。
	sysPanel := html[strings.Index(html, `id="settings-panel-system"`):]
	if !strings.Contains(sysPanel, `id="card-rate-limit-ports"`) || !strings.Contains(sysPanel, "限流端口与倍率") {
		t.Fatalf("系统面板里应有“限流端口与倍率”卡")
	}
	for _, id := range []string{"user-port-start", "user-port-end", "user-port-save", "relay-multiplier", "relay-multiplier-save"} {
		if !strings.Contains(sysPanel, `id="`+id+`"`) {
			t.Fatalf("限流端口与倍率卡里缺 %s", id)
		}
	}
	// 管理面板模式：藏“域名与反代”选项卡，其余 6 个保留（§3.8）。
	if !strings.Contains(js, "MANAGER_SETTINGS_TABS = ['basic', 'security', 'audit', 'alerts', 'backup', 'system']") {
		t.Fatalf("管理面板模式下设置页应只留 6 个选项卡（无 domain）")
	}
	if !strings.Contains(js, "applyManagerSettingsTabs") || !strings.Contains(js, "settingsTabsVisible") {
		t.Fatalf("应有管理面板模式选项卡可见性守卫")
	}
	// 保存后只刷新受影响的部分（P2-7）：不再有裸 loadSettingsPage() 全量重拉。
	if strings.Contains(js, "await loadSettingsPage();") {
		t.Fatalf("保存分支不应再调裸 loadSettingsPage() 全量重拉")
	}
	if !strings.Contains(js, "async function reloadSettingsForm() { return loadSettingsPage({ lists: false }); }") {
		t.Fatalf("应有 reloadSettingsForm（只重填表单、不重拉 7 个列表）")
	}
	// 死代码清理后仍活着的：独立合并订阅的链接弹窗（已改名）、勾选树共用函数。
	// v1.4.40：旧记录只读列表与退场提示整个移除（函数与文案都不应再有）。
	if !strings.Contains(js, "function openSubLinkModal(url, opts)") {
		t.Fatalf("独立合并订阅的链接弹窗 openSubLinkModal 应还在")
	}
	for _, live := range []string{"subTreeKey", "fetchSubTreePanels", "renderSubEditTree"} {
		if !strings.Contains(js, live) {
			t.Fatalf("不应误删仍在用的：%s", live)
		}
	}
	for _, gone := range []string{"loadMergedSubsList", "showMergedRetiredNotice(", "合并订阅已退场；要用合并订阅请去集中管理页 → 独立合并订阅"} {
		if strings.Contains(js, gone) {
			t.Fatalf("旧记录列表／退场提示应已删掉：%s", gone)
		}
	}
	// openUserOpsMenu 与 openSubEditModal 不再有 merged 分支。
	for _, gone := range []string{"data-sub-edit-merged", "data-sub-copy-merged", "data-sub-qr-merged", "data-sub-rotate-merged", "data-sub-nodes-merged", "data-sub-revoke-merged", "openSubEditModal({ kind: 'merged'", "openUserOpsMenu('merged'"} {
		if strings.Contains(js, gone) {
			t.Fatalf("merged 操作分支应已删掉：%s", gone)
		}
	}
	// 备份卡片内三级选项卡保留。
	if !strings.Contains(html, `id="rb-tab-passphrase"`) || !strings.Contains(html, `id="rb-tab-r2"`) || !strings.Contains(html, `id="rb-tab-qiniu"`) {
		t.Fatalf("备份卡片内三级选项卡（口令/R2/七牛云）应保留")
	}
	if !strings.Contains(js, "RB_TABS = ['passphrase', 'r2', 'qiniu']") {
		t.Fatalf("RB_TABS 应保留")
	}
}

func TestOldMergedRoutesRemoved1436(t *testing.T) {
	// v1.4.40：旧合并订阅接口整个移除（GET 列表与 DELETE 路由都不应再有）；
	// 退场判定 isRetiredSubToken 与 410（StatusGone）不动——那是通用订阅
	// 的退场路径，与旧合并订阅无关。
	hB, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("读 handlers.go 失败：%v", err)
	}
	handlers := string(hB)
	for _, route := range []string{
		`GET /api/v1/subscriptions/merged`,
		`DELETE /api/v1/subscriptions/merged/{id}`,
	} {
		if strings.Contains(handlers, route) {
			t.Fatalf("旧合并订阅接口应已移除：%s", route)
		}
	}
	subB, err := os.ReadFile("subscription.go")
	if err != nil {
		t.Fatalf("读 subscription.go 失败：%v", err)
	}
	sub := string(subB)
	if !strings.Contains(sub, "func (s *Server) isRetiredSubToken(tok string) bool") {
		t.Fatalf("isRetiredSubToken 退场判定应保留")
	}
	if !strings.Contains(sub, "http.StatusGone") || !strings.Contains(sub, "retiredSubMsg") {
		t.Fatalf("旧链接 410（StatusGone + retiredSubMsg）应保留")
	}
}
