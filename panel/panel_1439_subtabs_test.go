package main

import (
	"os"
	"strings"
	"testing"
)

// ---------- 1.4.39：全站子选项卡化 ----------
//
// 用户 2026-11-28 12:51 指出排版偏差：订阅页应为“一个页面、多个子选项卡”，
// 现在是一个大选项卡、卡片往下排，其他页面也一样。逐页对照样图（
// ~/workspace/025-invest/mockups/panel-mockups-single.html）后：
//   订阅 → 4 子选项卡（套餐档位／分组／订阅用户／订阅设置）
//   节点 → 3 子选项卡（后端节点／中转路由／节点证书）
//   落地证书 → 2 子选项卡（证书列表／续签引导）
//   落地管理 → 2 子选项卡（落地列表／回收站）
//   流量 → 时间范围分段按钮改成选项卡样式（按天／按月／累计，行为不变）
//   总览／设置／添加落地／节点链接／服务与日志／端口管理／集中管理：不动
// （总览是仪表盘、设置／添加落地／节点链接／服务与日志／端口管理／集中管理
// 已是选项卡；设置页 8-vs-7 选项卡差距是 1.4.36 有意的决定，这次不动）。
// 如实记录：样图订阅页本身也是卡片往下排、没有子选项卡——实现与样图是一致的，
// 用户“样图里是子选项卡”的前提与样图原文不符；但用户已明确拍板要子选项卡，
// 按用户最新拍板改。只改排版，不动功能与玻璃主题；选项卡复用节点链接页模式
// （原生 button＋aria-selected＋方向键＋localStorage 记住上次）。
// 命名注意：旧设置页订阅二级的 id（sub-tab-plans 等）与函数名（showSubTab／
// initSubTabs）被 Go 测试断言已删，新订阅选项卡改用 subws-* 前缀，不撞旧名。
// 前端行为由 jsdom test-1439-subtabs.js 覆盖。

func TestVersion1439(t *testing.T) {
	if Version != "1.4.41" {
		t.Fatalf("Version 应为 1.4.39，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.41" {
		t.Fatalf("VERSION 文件应为 1.4.39，实际 %q", strings.TrimSpace(string(vb)))
	}
}

func TestSubtabs1439(t *testing.T) {
	htmlB, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 web/index.html 失败：%v", err)
	}
	html := string(htmlB)
	cssB, err := os.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("读 web/style.css 失败：%v", err)
	}
	css := string(cssB)
	jsB, err := os.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读 web/app.js 失败：%v", err)
	}
	js := string(jsB)

	// 订阅页：四子选项卡＋四面板＋壳都在；旧卡片 id 已撤（1.4.33 断言已同步改）。
	for _, id := range []string{"subws-tab-plans", "subws-tab-groups", "subws-tab-users", "subws-tab-settings",
		"subws-panel-plans", "subws-panel-groups", "subws-panel-users", "subws-panel-settings"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Fatalf("订阅页里没有 %s", id)
		}
	}
	if !strings.Contains(html, `class="card sub-shell"`) {
		t.Fatalf("订阅页里没有 sub-shell")
	}
	for _, id := range []string{"card-sub-plans", "card-sub-groups", "card-sub-users", "card-sub-settings"} {
		if strings.Contains(html, `id="`+id+`"`) {
			t.Fatalf("订阅页里还留着旧卡片 id %s", id)
		}
	}
	// 互跳按钮的值是选项卡 id（不再是卡片 id）。
	for _, jump := range []string{`data-sub-jump="groups"`, `data-sub-jump="users"`, `data-sub-jump="plans"`} {
		if !strings.Contains(html, jump) {
			t.Fatalf("订阅页里没有互跳链接 %s", jump)
		}
	}
	// 节点页：三子选项卡＋三面板＋壳都在；三个模板没被挪坏（仍在壳内、id 齐全）。
	for _, id := range []string{"nodes-tab-backend", "nodes-tab-relay", "nodes-tab-certs",
		"nodes-panel-backend", "nodes-panel-relay", "nodes-panel-certs",
		"nodes-ib-add-template", "nodes-relay-route-template", "nodes-inbounds-template"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Fatalf("节点页里没有 %s", id)
		}
	}
	if !strings.Contains(html, `class="card nodes-shell"`) {
		t.Fatalf("节点页里没有 nodes-shell")
	}
	// 落地证书页：两子选项卡＋两面板＋壳都在。
	for _, id := range []string{"certs-tab-list", "certs-tab-guide", "certs-panel-list", "certs-panel-guide"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Fatalf("落地证书页里没有 %s", id)
		}
	}
	if !strings.Contains(html, `class="card certs-shell"`) {
		t.Fatalf("落地证书页里没有 certs-shell")
	}
	// 落地管理页：两子选项卡＋两面板＋壳都在。
	for _, id := range []string{"manage-tab-list", "manage-tab-recycle", "manage-panel-list", "manage-panel-recycle"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Fatalf("落地管理页里没有 %s", id)
		}
	}
	if !strings.Contains(html, `class="card manage-shell"`) {
		t.Fatalf("落地管理页里没有 manage-shell")
	}
	// 流量页：时间范围三个按钮是选项卡样式（带 data-range，aria-selected 控选中态）。
	for _, id := range []string{"traffic-tab-day", "traffic-tab-month", "traffic-tab-total"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Fatalf("流量页里没有 %s", id)
		}
	}
	if !strings.Contains(html, `class="traffic-tabs"`) {
		t.Fatalf("流量页里没有 traffic-tabs")
	}
	// CSS：新选项卡类都进了分组规则（含玻璃下选中态）。
	for _, cls := range []string{".sub-shell", ".nodes-shell", ".certs-shell", ".manage-shell",
		".nodes-tabs", ".certs-tabs", ".manage-tabs", ".traffic-tabs",
		".nodes-tab", ".certs-tab", ".manage-tab", ".traffic-tab",
		".nodes-panel", ".certs-panel", ".manage-panel"} {
		if !strings.Contains(css, cls) {
			t.Fatalf("style.css 里没有 %s", cls)
		}
	}
	// JS：五套选项卡函数与 localStorage 键都在；启动处都 safeInit 了。
	for _, fn := range []string{"SUBWS_TABS", "showSubwsTab", "initSubwsTabs", "relay-panel-subscription-tab",
		"NODES_TABS", "showNodesTab", "initNodesTabs", "relay-panel-nodes-tab",
		"CERTS_TABS", "showCertsTab", "initCertsTabs", "relay-panel-certs-tab",
		"MANAGE_TABS", "showManageTab", "initManageTabs", "relay-panel-manage-tab",
		"TRAFFIC_TABS", "showTrafficTab", "initTrafficTabs", "relay-panel-traffic-tab"} {
		if !strings.Contains(js, fn) {
			t.Fatalf("app.js 里没有 %s", fn)
		}
	}
	for _, init := range []string{"safeInit(initSubwsTabs)", "safeInit(initNodesTabs)", "safeInit(initCertsTabs)", "safeInit(initManageTabs)", "safeInit(initTrafficTabs)"} {
		if !strings.Contains(js, init) {
			t.Fatalf("app.js 启动处没有 %s", init)
		}
	}
	// 流量页旧按钮绑定已撤（initTraffic 里不再单独绑 data-range click，避免同一按钮两处改状态）。
	if strings.Contains(js, `$$('[data-range]').forEach((b) => b.addEventListener`) {
		t.Fatalf("app.js 里还留着 initTraffic 的 data-range click 绑定")
	}
	// 新建订阅用户成功后切到“订阅用户”选项卡（成功区立刻可见）。
	if !strings.Contains(js, `showSubwsTab('users')`) {
		t.Fatalf("app.js 里新建订阅用户成功后没有切到 users 选项卡")
	}
	// 旧设置页订阅二级的函数名仍不存在（新名 subws-* 不撞旧名）。
	for _, gone := range []string{"SUB_TABS", "showSubTab", "initSubTabs"} {
		if strings.Contains(js, gone) {
			t.Fatalf("app.js 里还留着旧订阅二级选项卡 %s", gone)
		}
	}
}
