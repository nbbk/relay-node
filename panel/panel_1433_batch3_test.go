package main

import (
	"os"
	"strings"
	"testing"
)

// ---------- 1.4.33：面板 UI/UX 重设计第 3 批（订阅工作区合并＋命名与单位统一） ----------
//
// 只改前端（panel/web/app.js、index.html）与一处后端注释
// （handlers2.go:272），后端行为不动。本文件只做静态断言：版本号、
// 订阅工作区结构、三套“模式”改名、GB／日期口径、GB=0 置灰、搜索字段、
// SubUser 表序号与 ID 复制按钮。交互行为由 jsdom test-1433-batch3.js 覆盖。

func TestVersion1433(t *testing.T) {
	if Version != "1.4.37" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.37" {
		t.Fatalf("VERSION 文件应为 1.4.33，实际 %q", strings.TrimSpace(string(vb)))
	}
}

// readWeb1433 读 panel/web 下的文件（测试工作目录就是 panel/）。
func readWeb1433(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("web/" + name)
	if err != nil {
		t.Fatalf("读 web/%s 失败：%v", name, err)
	}
	return string(b)
}

func TestBatch3Workspace1433(t *testing.T) {
	js := readWeb1433(t, "app.js")
	html := readWeb1433(t, "index.html")
	// 顶级页“订阅”替代“用户管理”。
	if !strings.Contains(js, "{ id: 'subscription', title: '订阅'") {
		t.Fatalf("PAGES 里没有订阅页")
	}
	if strings.Contains(js, "{ id: 'users', title: '用户管理'") {
		t.Fatalf("PAGES 里还留着用户管理顶级页")
	}
	if !strings.Contains(js, "MANAGER_PAGES = ['central', 'subscription', 'nodes', 'settings']") {
		t.Fatalf("MANAGER_PAGES 没改成 subscription")
	}
	if !strings.Contains(html, `id="page-subscription"`) {
		t.Fatalf("index.html 里没有 page-subscription")
	}
	if strings.Contains(html, `id="page-users"`) {
		t.Fatalf("index.html 里还留着 page-users")
	}
	// 三栏＋订阅设置同页，三个列表容器都在订阅页里。
	for _, id := range []string{"card-sub-plans", "card-sub-groups", "card-sub-users", "card-sub-settings",
		"sub-plan-list", "sub-group-list", "sub-user-list"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Fatalf("订阅页里没有 %s", id)
		}
	}
	// 三栏互跳链接都在。
	for _, jump := range []string{`data-sub-jump="card-sub-groups"`, `data-sub-jump="card-sub-users"`, `data-sub-jump="card-sub-plans"`} {
		if !strings.Contains(html, jump) {
			t.Fatalf("订阅页里没有互跳链接 %s", jump)
		}
	}
	// 订阅对外配置三项在订阅页里只有一份（设置页对应位置是指针卡）。
	if strings.Count(html, `id="sub-public-domain"`) != 1 || strings.Count(html, `id="sub-path"`) != 1 || strings.Count(html, `id="gateway-domain-now"`) != 1 {
		t.Fatalf("订阅对外配置三项在 HTML 里不是恰好一份")
	}
	// 设置页订阅二级选项卡已撤（第 6 批）：“端口与倍率”改名“限流端口与倍率”
	// 并移到系统面板里；档位／分组指针卡随订阅面板一起撤掉。
	for _, id := range []string{"sub-tab-plans", "sub-tab-groups", "sub-tab-other", "sub-panel-other", "settings-panel-subscription", "settings-tab-subscription"} {
		if strings.Contains(html, `id="`+id+`"`) {
			t.Fatalf("设置页订阅二级选项卡应已撤掉：还留着 %s", id)
		}
	}
	sysPanel := html[strings.Index(html, `id="settings-panel-system"`):]
	if !strings.Contains(sysPanel, `id="card-rate-limit-ports"`) || !strings.Contains(sysPanel, "限流端口与倍率") {
		t.Fatalf("系统面板里应有改名后的“限流端口与倍率”卡")
	}
	for _, id := range []string{"user-port-start", "user-port-end", "user-port-save", "relay-multiplier", "relay-multiplier-save"} {
		if !strings.Contains(sysPanel, `id="`+id+`"`) {
			t.Fatalf("限流端口与倍率卡里缺 %s", id)
		}
	}
	// 模式说明文案同步改成订阅。
	if !strings.Contains(html, "只留集中管理、订阅、节点与设置") {
		t.Fatalf("index.html 管理面板模式文案没改成订阅")
	}
	if strings.Contains(html, "只留集中管理、用户管理、节点与设置") {
		t.Fatalf("index.html 里还留着用户管理的模式文案")
	}
	h2, err := os.ReadFile("handlers2.go")
	if err != nil {
		t.Fatalf("读 handlers2.go 失败：%v", err)
	}
	if !strings.Contains(string(h2), "只显示集中管理、订阅、节点与设置") {
		t.Fatalf("handlers2.go 注释没改成订阅")
	}
}

func TestBatch3Renames1433(t *testing.T) {
	js := readWeb1433(t, "app.js")
	// 三套“模式”改名全站一致。
	for _, want := range []string{"订阅出哪些节点", "链接走直连还是中转", "分流方式"} {
		if !strings.Contains(js, want) {
			t.Fatalf("改名缺失：%s", want)
		}
	}
	// 旧叫法不该再出现在订阅级文案里。
	for _, gone := range []string{"订阅内容：<select", `id="plan-traffic-mb"`, "流量上限（MB）", "<th>链接模式</th>", "<th>内容模式</th>"} {
		if strings.Contains(js, gone) {
			t.Fatalf("旧叫法还在：%q", gone)
		}
	}
	// 订阅级链接模式下方写清优先级（订阅级覆盖节点级）。
	if !strings.Contains(js, "订阅级设置优先于节点级") {
		t.Fatalf("新建／编辑用户弹窗里没有订阅级优先于节点级的说明")
	}
	// 节点级“节点链接模式”是另一个概念，保留原名（不与订阅级混淆）。
	if !strings.Contains(js, "节点链接模式") {
		t.Fatalf("节点详情里的节点链接模式被误删了")
	}
}

func TestBatch3UnitsAndGrey1433(t *testing.T) {
	js := readWeb1433(t, "app.js")
	// 档位弹窗流量上限改 GB（历史 MB 值按 /1024 显示），到期天数下方即时算示例日期。
	if !strings.Contains(js, "mbToGbDisplay") || !strings.Contains(js, "planExpiryExample") {
		t.Fatalf("档位弹窗缺 GB 换算或到期示例日期")
	}
	if !strings.Contains(js, `id="plan-traffic-gb"`) {
		t.Fatalf("档位弹窗里没有 #plan-traffic-gb")
	}
	// 限流表单 GB=0 置灰：共用表单、档位弹窗、流量页三处。
	if !strings.Contains(js, "data-tl-note") || !strings.Contains(js, "不限流时不用填") {
		t.Fatalf("限流表单缺 GB=0 置灰提示")
	}
	if !strings.Contains(js, "syncTrafficLimitForm") {
		t.Fatalf("流量页缺 syncTrafficLimitForm")
	}
	if strings.Contains(js, "gb > 0 ? $('#limit-mode').value : 'total'") {
		t.Fatalf("流量页还在 GB=0 时悄悄把算法改成总量")
	}
}

func TestBatch3SearchAndTable1433(t *testing.T) {
	js := readWeb1433(t, "app.js")
	html := readWeb1433(t, "index.html")
	// 搜索字段补齐。
	if !strings.Contains(js, "u.group_name") {
		t.Fatalf("用户搜索没加 group_name")
	}
	if !strings.Contains(js, "(g.members || []).map((mm) => mm.name)") {
		t.Fatalf("分组搜索没加成员名")
	}
	if !strings.Contains(html, "搜索订阅用户：名字、分组、备注、账号") {
		t.Fatalf("用户搜索框 placeholder 没写清可搜字段")
	}
	if !strings.Contains(html, "搜索分组：名字、备注、成员名") {
		t.Fatalf("分组搜索框 placeholder 没写清可搜字段")
	}
	// SubUser 表序号列（跟着分页走）与 ID 复制按钮。
	if !strings.Contains(js, "<th>序号</th>") || !strings.Contains(js, "(pg.cur - 1) * pg.per + i + 1") {
		t.Fatalf("SubUser 表缺序号列或序号没跟着分页走")
	}
	if !strings.Contains(js, "data-sub-user-copy-id") {
		t.Fatalf("SubUser 表 ID 列旁没有一键复制按钮")
	}
}
