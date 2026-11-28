package main

import (
	"os"
	"strings"
	"testing"
)

// ---------- 1.4.37：玻璃拟物主题（与客户端 0.1.18 同一套共享 token） ----------
//
// 新增 html[data-theme="glass"]（浅蓝灰底 #E9EEF3、亮蓝强调 #2B7FFF、
// 深蓝灰文字 #33475B、次要文字 #6B7C8D、磨砂白 rgba(255,255,255,0.65–0.75)
// ＋blur(12–16px)、卡片圆角 20–24px、边框 rgba(255,255,255,0.6) 2–3px、
// 阴影 0 8px 30px rgba(51,71,91,0.10)、深藏青 #0F2A4A 配金色 #EEC362；
// 数值全部取自 ~/workspace/031-invest/glass-theme-tokens.md，按观察数值
// 自己实现，不复制任何第三方主题文件）。顶栏 theme-btn 三档循环
// 玻璃→浅色→深色→玻璃；设置页基础选项卡新增“界面主题”卡片，三选一
// radio 立即生效并持久化；无保存值时默认玻璃（不再跟随系统），已保存
// 浅色／深色的老用户继续尊重其选择；旧浅色／深色主题保留可切回。
// 顶栏平时与背景融为一体，滚动后变磨砂白悬浮条置顶（.scrolled 只在
// 玻璃主题下生效）；深藏青＋金色只用在登录卡品牌块与总览第一张主统计卡。
// 内容区 1200px、区块留白 60–80px 一档；页面结构不动，只换主题层。
// 顺手修正 v1.4.36 CHANGELOG“测试”一行：只有 test-1433-batch3.js 与
// test-1429-users-subtabs.js 按新行为更新了断言，test-1321～1324／
// test-142 本批没动（它们钉在旧 kit 上，旧断言对旧 kit 正确）。
// 前端行为由 jsdom test-1437-glass.js 覆盖。

func TestVersion1437(t *testing.T) {
	if Version != "1.4.37" {
		t.Fatalf("Version 应为 1.4.37，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.37" {
		t.Fatalf("VERSION 文件应为 1.4.37，实际 %q", strings.TrimSpace(string(vb)))
	}
}

func TestGlassFrontend1437(t *testing.T) {
	jsB, err := os.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读 web/app.js 失败：%v", err)
	}
	htmlB, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 web/index.html 失败：%v", err)
	}
	cssB, err := os.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("读 web/style.css 失败：%v", err)
	}
	js, html, css := string(jsB), string(htmlB), string(cssB)

	// 玻璃变量块存在，数值与共享 token 一致。
	glassIdx := strings.Index(css, `html[data-theme="glass"]{`)
	if glassIdx < 0 {
		t.Fatalf("style.css 应有 html[data-theme=\"glass\"] 变量块")
	}
	glassVars := css[glassIdx:]
	for _, want := range []string{"#E9EEF3", "#2B7FFF", "#33475B", "#6B7C8D", "rgba(255,255,255,0.70)", "--radius:22px", "0 8px 30px rgba(51,71,91,0.10)", "#0F2A4A", "#EEC362"} {
		if !strings.Contains(glassVars, want) {
			t.Fatalf("玻璃变量块里缺 %s", want)
		}
	}
	// 玻璃专属规则：磨砂卡片（半透明＋blur＋大圆角＋2px 白边框）、
	// 顶栏平时透明、滚动后磨砂悬浮条、内容区 1200px、区块留白 60–80px 一档。
	for _, want := range []string{
		"backdrop-filter:blur(14px)",
		"border:2px solid rgba(255,255,255,0.6)",
		".topbar.scrolled",
		"background:rgba(255,255,255,0.70)",
		"max-width:1200px",
		"padding:70px 28px",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("玻璃专属规则里缺 %s", want)
		}
	}
	// 深藏青＋金色只用在两处对比位置：登录卡品牌块、总览第一张主统计卡。
	if !strings.Contains(css, `.login-card .brand`) || !strings.Contains(css, `#overview-stats .card:first-child`) {
		t.Fatalf("深藏青＋金色对比位置应为登录卡品牌块与总览第一张主统计卡")
	}
	// 输入框／下拉／表格在玻璃主题下保持可读（不透明的底／粘性列）。
	if !strings.Contains(css, `background:rgba(255,255,255,0.75)`) {
		t.Fatalf("玻璃主题下输入框／表头应有不透明的磨砂白底")
	}
	// 旧浅色／深色主题仍在（变量块与覆盖块都在）。
	if !strings.Contains(css, ":root{") || !strings.Contains(css, `html[data-theme="dark"]{`) {
		t.Fatalf("旧浅色（:root）／深色主题变量块应保留")
	}

	// 主题切换三档：THEMES 玻璃／浅色／深色，theme-btn 循环，文案与 title 同步。
	if !strings.Contains(js, "const THEMES = ['glass', 'light', 'dark']") {
		t.Fatalf("应有 THEMES = ['glass', 'light', 'dark']")
	}
	if !strings.Contains(js, "(THEMES.indexOf(cur) + 1) % THEMES.length") {
		t.Fatalf("theme-btn 应三档循环")
	}
	for _, want := range []string{"'🥛 玻璃'", "'☀️ 浅色'", "'🌙 深色'"} {
		if !strings.Contains(js, want) {
			t.Fatalf("主题按钮文案里缺 %s", want)
		}
	}
	// 无保存值时默认玻璃，不再跟随系统（不再有 'auto' 与 prefers-color-scheme 监听）。
	if !strings.Contains(js, "applyTheme(saved || 'glass')") {
		t.Fatalf("initTheme 无保存值时应默认玻璃")
	}
	if strings.Contains(js, "prefers-color-scheme") {
		t.Fatalf("玻璃主题落地后不应再跟随系统（prefers-color-scheme 应已撤掉）")
	}
	// 滚动后顶栏变磨砂悬浮条，只在玻璃主题下生效。
	if !strings.Contains(js, "function updateTopbarScrolled()") || !strings.Contains(js, "classList.toggle('scrolled'") {
		t.Fatalf("应有 updateTopbarScrolled（玻璃主题下滚动加 .scrolled）")
	}
	if !strings.Contains(js, "dataset.theme === 'glass' && window.scrollY > 8") {
		t.Fatalf(".scrolled 应只在玻璃主题且滚动超过 8px 时生效")
	}
	// 设置页主题 radio 与顶栏按钮同步。
	if !strings.Contains(js, "syncThemeRadios") || !strings.Contains(js, `input[name="ui-theme"]`) {
		t.Fatalf("应有设置页主题 radio 与顶栏按钮的同步（syncThemeRadios）")
	}

	// index.html 默认玻璃，首屏直接是玻璃；meta 与 manifest 主题色同步改成亮蓝。
	if !strings.Contains(html, `<html lang="zh-CN" data-theme="glass">`) {
		t.Fatalf("index.html 默认应为 data-theme=\"glass\"")
	}
	if !strings.Contains(html, `<meta name="theme-color" content="#2B7FFF">`) {
		t.Fatalf("meta theme-color 应为 #2B7FFF")
	}
	if !strings.Contains(html, `theme_color":"%232B7FFF"`) {
		t.Fatalf("manifest theme_color 应为 #2B7FFF")
	}
	// 顶栏按钮默认显示玻璃。
	if !strings.Contains(html, `id="theme-btn"`) || !strings.Contains(html, "🥛 玻璃") {
		t.Fatalf("顶栏 theme-btn 默认应显示“🥛 玻璃”")
	}
	// 设置页基础选项卡有“界面主题”卡片，三选一 radio。
	basicIdx := strings.Index(html, `id="settings-panel-basic"`)
	if basicIdx < 0 {
		t.Fatalf("设置页基础选项卡应还在")
	}
	basic := html[basicIdx:]
	if !strings.Contains(basic, `id="card-ui-theme"`) || !strings.Contains(basic, "界面主题") {
		t.Fatalf("设置页基础选项卡里应有“界面主题”卡片")
	}
	for _, v := range []string{"glass", "light", "dark"} {
		if !strings.Contains(basic, `name="ui-theme" value="`+v+`"`) {
			t.Fatalf("界面主题卡片里缺 %s 选项", v)
		}
	}
}

func TestChangelog1437(t *testing.T) {
	clB, err := os.ReadFile("../CHANGELOG.md")
	if err != nil {
		t.Fatalf("读 CHANGELOG.md 失败：%v", err)
	}
	cl := string(clB)
	if !strings.Contains(cl, "### v1.4.37 更新") || !strings.Contains(cl, "玻璃拟物主题") || !strings.Contains(cl, "默认玻璃") {
		t.Fatalf("CHANGELOG 顶部应有 v1.4.37 一节，写清玻璃主题与默认主题")
	}
	// v1.4.36 那行已改准：只有 test-1433 与 test-1429 按新行为更新了断言，
	// test-1321～1324／test-142 本批没动；旧的不准说法不应再出现。
	if strings.Contains(cl, "test-1433-batch3.js／test-1429-users-subtabs.js／test-1321～1324／test-142 按新行为更新断言") {
		t.Fatalf("v1.4.36 那行不准的描述应已改掉")
	}
	if !strings.Contains(cl, "test-1433-batch3.js／test-1429-users-subtabs.js 按新行为更新断言") {
		t.Fatalf("v1.4.36 那行应写清只有 test-1433 与 test-1429 按新行为更新了断言")
	}
	if !strings.Contains(cl, "test-1321～1324／test-142 本批没动") {
		t.Fatalf("v1.4.36 那行应写清 test-1321～1324／test-142 本批没动")
	}
}
