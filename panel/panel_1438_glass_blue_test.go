package main

import (
	"os"
	"strings"
	"testing"
)

// ---------- 1.4.38：玻璃主题改偏蓝、加通透感与质感（与客户端 0.1.19 同一套共享 token） ----------
//
// 用户 2026-11-28 两件反馈：① 10:17 颜色不对、要偏蓝的感觉；② 10:18 卡片等
// 效果不太像玻璃拟态、少了通透感和质感。只改颜色与质感，不动别的：
// 偏蓝——底色 #E9EEF3→#DCE8F3、横带 #F4F7FA→#EAF1F8（约 42% 处）、磨砂面
// 从纯白 rgba(255,255,255,0.70／0.55) 改带蓝调 rgba(230,238,248,0.60／0.50)、
// 侧边栏与顶栏 .scrolled 的字面量白底同步改带蓝调（0.60／0.65）；强调 #2B7FFF、
// 主文字 #33475B、次要文字 #6B7C8D、深藏青 #0F2A4A、金色 #EEC362 不动。
// 通透感与质感——卡片组顶部加一处高光（同一套：inset 0 1px 0 rgba(255,255,255,0.9)
// 内阴影＋顶部淡淡的白色渐变罩＋柔和外阴影）；边框保持 2px rgba(255,255,255,0.6)
// 半透明白描边不变；底色渐变加一段略深的蓝 #D5E3F2 与顶部淡淡的径向亮斑，
// 让模糊有东西可透；blur 保持 14px 不动。
// 可读性兜底（对比度验算，WCAG 相对亮度公式，主文字 #33475B）：
// 磨砂面按底色与磨砂面的 alpha 叠加估算等效底色（0.60×面＋0.40×底；
// 0.50 的 --surface-muted 同理）：底色 #DCE8F3 上等效 (226.0,235.6,246.0)
// 对比度 7.99:1；横带 #EAF1F8 上等效 (231.6,239.2,248.0) 8.27:1；最深段
// #D5E3F2 上等效 (223.2,233.6,245.6) 7.84:1；--surface-muted（0.50）三处
// 分别为 7.94:1／8.29:1／7.76:1；裸底三处分别为 7.70:1／8.41:1／7.34:1。
// 最差 7.76:1，远高于 4.5:1，不用加深文字、不用把卡片调回不透明。
// 次要文字 #6B7C8D 与选中态（亮蓝文字＋亮蓝下划线）这次不动：用户还没定
// 是否跟客户端对齐（客户端偏离成 #55677A／深蓝灰选中底）。
// 前端行为由 jsdom test-1438-glass-blue.js 覆盖。

func TestVersion1438(t *testing.T) {
	if Version != "1.4.43" {
		t.Fatalf("Version 应为 1.4.38，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.43" {
		t.Fatalf("VERSION 文件应为 1.4.38，实际 %q", strings.TrimSpace(string(vb)))
	}
}

func TestGlassBlue1438(t *testing.T) {
	cssB, err := os.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("读 web/style.css 失败：%v", err)
	}
	css := string(cssB)

	// 玻璃变量块存在，新 token 数值齐全。
	glassIdx := strings.Index(css, `html[data-theme="glass"]{`)
	if glassIdx < 0 {
		t.Fatalf("style.css 应有 html[data-theme=\"glass\"] 变量块")
	}
	glassVars := css[glassIdx:]
	for _, want := range []string{"#DCE8F3", "#EAF1F8", "rgba(230,238,248,0.60)", "rgba(230,238,248,0.50)", "#2B7FFF", "#33475B", "#6B7C8D", "#0F2A4A", "#EEC362"} {
		if !strings.Contains(glassVars, want) {
			t.Fatalf("玻璃变量块里缺 %s", want)
		}
	}
	// 底色渐变多段：横带 #EAF1F8 仍在约 42% 处，深段 #D5E3F2 与顶部径向亮斑都在。
	for _, want := range []string{"#EAF1F8 42%", "#D5E3F2", "radial-gradient(1200px 420px at 50% -80px,rgba(255,255,255,0.55),rgba(255,255,255,0) 70%)", "linear-gradient(180deg,#DCE8F3 0%,#EAF1F8 42%,#D5E3F2 78%,#DCE8F3 100%)"} {
		if !strings.Contains(css, want) {
			t.Fatalf("玻璃底色渐变里缺 %s", want)
		}
	}
	// 卡片组高光规则存在（同一套：顶部 1px 内阴影＋顶部白色渐变罩＋柔和外阴影）。
	for _, want := range []string{
		"background-image:linear-gradient(180deg,rgba(255,255,255,0.35),rgba(255,255,255,0) 42%)",
		"box-shadow:inset 0 1px 0 rgba(255,255,255,0.9),0 8px 30px rgba(51,71,91,0.10)",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("玻璃卡片组高光规则里缺 %s", want)
		}
	}
	// 侧边栏 0.60 透、顶栏 .scrolled 0.92 不透（v1.4.40：0.65 遮不住标题）。
	if !strings.Contains(css, "background:rgba(230,238,248,0.60)") {
		t.Fatalf("玻璃侧边栏底应为 rgba(230,238,248,0.60)")
	}
	if !strings.Contains(css, "background:rgba(230,238,248,0.92)") {
		t.Fatalf("玻璃顶栏 .scrolled 底应为 rgba(230,238,248,0.92)")
	}
	// 不动项：blur 仍是 14px、边框仍是 2px 半透明白描边、强调／主文字／深藏青／金色不变。
	for _, want := range []string{
		"backdrop-filter:blur(14px)",
		"border:2px solid rgba(255,255,255,0.6)",
		"#2B7FFF", "#33475B", "#6B7C8D", "#0F2A4A", "#EEC362",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("玻璃主题不动项里缺 %s", want)
		}
	}
	// 旧值在 glass 块里已消失（从第一个 glass 变量块起到文件末尾的区域内
	// 不应再有 #E9EEF3 与 rgba(255,255,255,0.70)；输入框／表头的 0.75 与
	// 粘性列的 0.92 是另一档，不在此列）。
	glassRegion := css[glassIdx:]
	for _, gone := range []string{"#E9EEF3", "rgba(255,255,255,0.70)"} {
		if strings.Contains(glassRegion, gone) {
			t.Fatalf("glass 块里旧值 %s 应已消失", gone)
		}
	}
}

func TestChangelog1438(t *testing.T) {
	clB, err := os.ReadFile("../CHANGELOG.md")
	if err != nil {
		t.Fatalf("读 CHANGELOG.md 失败：%v", err)
	}
	cl := string(clB)
	if !strings.Contains(cl, "### v1.4.38 更新") || !strings.Contains(cl, "偏蓝") || !strings.Contains(cl, "高光") || !strings.Contains(cl, "通透") {
		t.Fatalf("CHANGELOG 顶部应有 v1.4.38 一节，写清偏蓝＋通透感／高光")
	}
	if !strings.Contains(cl, "#DCE8F3") || !strings.Contains(cl, "#EAF1F8") || !strings.Contains(cl, "rgba(230,238,248,0.60)") {
		t.Fatalf("v1.4.38 一节应写清新 token 数值（#DCE8F3／#EAF1F8／带蓝调磨砂面）")
	}
	// v1.4.37 一节仍在下面（只新增一节，不重抄、不删旧节）。
	if !strings.Contains(cl, "### v1.4.37 更新") {
		t.Fatalf("CHANGELOG 里 v1.4.37 一节应仍在")
	}
}
