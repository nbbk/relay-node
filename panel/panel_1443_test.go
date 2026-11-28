package main

// ---------- 1.4.43：导航图标换 B 玻璃双色＋导航收起功能＋选中态改浅蓝胶囊 ----------
//
// 用户 2026-11-28 19:57 反馈导航菜单左边的图标太俗太丑、要设计高级一点；
// 2026-11-28 20:05 挑了 B 玻璃双色，并加“导航收起功能也做了”。
// 根因：图标是彩色 emoji（PAGES 数组里直接拼进 .ico span），各平台渲染不一、
// 粗细颜色全无章法，与玻璃主题蓝灰配色不搭。修法：12 项全换成 B 方向内联 SVG
// （1.5px 细线条＋一处浅蓝填充，平时 rgba(43,127,255,0.14)、选中 0.30，
// 由 .nav-item／.nav-item.active 上的 --ic-fill 控制）；选中态由整行实色蓝底
// 改浅蓝胶囊；侧边栏底部加收起开关，收起后 64px 只留图标，状态记 localStorage。

import (
	"os"
	"strings"
	"testing"
)

func TestVersion1443(t *testing.T) {
	if Version != "1.4.43" {
		t.Fatalf("Version 应为 1.4.43，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.43" {
		t.Fatalf("VERSION 文件应为 1.4.43，实际 %q", strings.TrimSpace(string(vb)))
	}
}

// TestNavIcons1443：PAGES 12 项 icon 全是 B 玻璃双色内联 SVG，不再是 emoji；
// buildNav 给标题 span 加 lbl 类、给按钮加 title 属性（收起态悬停提示）。
func TestNavIcons1443(t *testing.T) {
	aj, err := os.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	src := string(aj)
	if strings.Count(src, "icon: '<svg") != 12 {
		t.Fatalf("PAGES 里内联 SVG 图标应为 12 个，实际 %d", strings.Count(src, "icon: '<svg"))
	}
	if strings.Count(src, `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"`) != 12 {
		t.Fatalf("12 个 SVG 图标应全是 1.5 描边的 24 框")
	}
	for _, e := range []string{"🏠", "➕", "🗂️", "🖧", "🔗", "🔒", "⚙️", "🔧", "👤", "🧱", "📊", "🖥️"} {
		if strings.Contains(src, e) {
			t.Fatalf("app.js 里还有旧导航 emoji：%s", e)
		}
	}
	for _, want := range []string{
		`class="lbl"`,
		`b1.title = p.title`,
		`relay-panel-nav-mini`,
		`function toggleNavMini()`,
		`function applyNavMini()`,
		`function initNavMini()`,
		`safeInit(initNavMini)`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("app.js 缺了收起／lbl 相关实现：%s", want)
		}
	}
}

// TestNavMini1443：收起开关在侧边栏底部 .foot 里（退出登录上方）；mini 样式
// 只在 @media (min-width:641px) 内生效，≤640px 汉堡抽屉不动。
func TestNavMini1443(t *testing.T) {
	ih, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 index.html 失败：%v", err)
	}
	html := string(ih)
	if !strings.Contains(html, `id="nav-mini-btn"`) {
		t.Fatalf("index.html 缺收起开关按钮 #nav-mini-btn")
	}
	if strings.Index(html, `nav-mini-btn`) > strings.Index(html, `id="logout-btn"`) {
		t.Fatalf("收起开关应在退出登录按钮上方")
	}
	if !strings.Contains(html, `class="nav-mini-wrap"`) {
		t.Fatalf("index.html 缺收起开关的 nav-mini-wrap 包裹")
	}
	cs, err := os.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("读 style.css 失败：%v", err)
	}
	css := string(cs)
	mq := strings.Index(css, "@media (min-width:641px)")
	if mq < 0 {
		t.Fatalf("style.css 缺 min-width:641px 的 mini 媒体查询")
	}
	mini := strings.Index(css, ".sidebar.mini{width:64px")
	if mini < 0 || mini < mq {
		t.Fatalf("mini 规则应在 min-width:641px 媒体查询内")
	}
	for _, want := range []string{
		`.sidebar.mini .nav-item .lbl{display:none}`,
		`.sidebar.mini .foot > *:not(.nav-mini-wrap){display:none}`,
		`.sidebar.mini #brand-name{display:none}`,
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("style.css 缺 mini 规则：%s", want)
		}
	}
}

// TestNavActiveCapsule1443：选中态是浅蓝胶囊（不是整行实色蓝底）；--ic-fill
// 两档；.ico svg 定 20×20。
func TestNavActiveCapsule1443(t *testing.T) {
	cs, err := os.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("读 style.css 失败：%v", err)
	}
	css := string(cs)
	if !strings.Contains(css, ".nav-item.active{background:rgba(43,127,255,0.12)") {
		t.Fatalf("选中态应是浅蓝胶囊 rgba(43,127,255,0.12)")
	}
	if strings.Contains(css, ".nav-item.active{background:var(--accent)") {
		t.Fatalf("选中态还留着整行实色蓝底 var(--accent)")
	}
	for _, want := range []string{
		`--ic-fill:rgba(43,127,255,0.14)`,
		`--ic-fill:rgba(43,127,255,0.30)`,
		`.nav-item .ico svg{width:20px; height:20px; display:block}`,
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("style.css 缺：%s", want)
		}
	}
}
