package main

// v1.4.17：
//   - 设置页 DNS 服务商卡片：每家凭证表单旁加「怎么获取？」入口
//     （17 家服务商＋手动模式，共 18 个），点开宽弹窗显示该家凭证
//     获取教程；教程内容在 app.js 的 DNS_TUTORIALS 里，与
//     dns-provider-tutorials.md 一致（步骤数与文案文件为准），单独
//     成行的网址渲染成新标签页链接；Namecheap 第 6 步的 curl 命令
//     放代码块里，不渲染成链接；手动模式只显示 §18 那一句话。
//   - 顺手修 Namecheap 面板旧提示：
//     “Profile → Security → API Access”改成官方路径
//     “Profile → Tools → Business & Dev Tools → Namecheap API Access”。
//
// 覆盖：
//   - 版本断言 1.4.17。
//   - 前端静态断言：18 个入口（每家一个、在自己那家的表单内、
//     type="button"、文案「怎么获取？」）；DNS_TUTORIALS 数据结构、
//     document 级委托接线与宽弹窗调用、标题格式；教程块内 27 处
//     新标签页链接；api.ipify.org 不在任何 <a href> 里、只在
//     Namecheap 代码块里出现一次；每家 <ol> 步骤数与文案文件一致
//     （7/7/5/7/7/6/7/8/5/5/6/6/6/5/5/5/6，手动 0）；Namecheap
//     新路径在、旧路径不在。
//
// 注意：app.js 全文件的 target="_blank" rel="noopener" 是 29 处，
// 教程块外另有 2 处（仓库链接、另一处通用链接），27 这个数只能在
// DNS_TUTORIALS 块内数，全文件数会数出 29 而误判。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion1417(t *testing.T) {
	if Version != "1.4.40" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
}

// 18 家服务商 id（顺序与 index.html 两个下拉框、app.js
// DNS_PROVIDER_NAMES 一致）与文案文件里的步骤数。
var dnsTutorialProviders1417 = []struct {
	id    string
	steps int
}{
	{"cloudflare", 7}, {"aliyun", 7}, {"dnspod", 5}, {"route53", 7},
	{"godaddy", 7}, {"namecheap", 6}, {"digitalocean", 7}, {"linode", 8},
	{"vultr", 5}, {"bunny", 5}, {"hetzner", 6}, {"spaceship", 6},
	{"gandi", 6}, {"namesilo", 5}, {"desec", 5}, {"porkbun", 5},
	{"cloudns", 6},
}

// v1.4.17：18 个「怎么获取？」入口，每家一个、在自己那家的表单内。
func TestDNSTutorialEntries1417(t *testing.T) {
	htmlB, err := os.ReadFile(filepath.Join("web", "index.html"))
	if err != nil {
		t.Fatalf("读 index.html 失败：%v", err)
	}
	html := string(htmlB)
	if n := strings.Count(html, `data-dns-tutorial=`); n != 18 {
		t.Fatalf("index.html 里 data-dns-tutorial 入口应为 18 个，实际 %d 个", n)
	}
	for _, p := range dnsTutorialProviders1417 {
		attr := `data-dns-tutorial="` + p.id + `"`
		if n := strings.Count(html, attr); n != 1 {
			t.Errorf("%s 入口应恰好 1 个，实际 %d 个", p.id, n)
			continue
		}
		btn := strings.Index(html, attr)
		// 按钮本身：type="button"（在表单里不能是 submit），文案「怎么获取？」。
		tagStart := strings.LastIndex(html[:btn], "<button")
		tagEnd := strings.Index(html[btn:], ">")
		if tagStart < 0 || tagEnd < 0 {
			t.Errorf("%s 入口找不到 <button> 标签", p.id)
			continue
		}
		tag := html[tagStart : btn+tagEnd]
		if !strings.Contains(tag, `type="button"`) {
			t.Errorf("%s 入口按钮应是 type=\"button\"，实际标签 %q", p.id, tag)
		}
		after := html[btn : btn+200]
		if !strings.Contains(after, "怎么获取？</button>") {
			t.Errorf("%s 入口按钮文案应是「怎么获取？」", p.id)
		}
		// 在自己那家的 <form id="dns-form-<id>"> 内：表单开始在按钮前，
		// 该表单的 </form> 在按钮后。
		formStart := strings.LastIndex(html[:btn], `<form id="dns-form-`+p.id+`"`)
		if formStart < 0 {
			t.Errorf("%s 入口前找不到 <form id=\"dns-form-%s\">", p.id, p.id)
			continue
		}
		formEnd := strings.Index(html[formStart:], "</form>")
		if formEnd < 0 || formStart+formEnd < btn {
			t.Errorf("%s 入口不在 <form id=\"dns-form-%s\"> 内", p.id, p.id)
		}
	}
	// 手动模式：在 #dns-form-manual div 内（不是 form），同样 type="button"。
	attr := `data-dns-tutorial="manual"`
	if n := strings.Count(html, attr); n != 1 {
		t.Fatalf("manual 入口应恰好 1 个，实际 %d 个", n)
	}
	btn := strings.Index(html, attr)
	tagStart := strings.LastIndex(html[:btn], "<button")
	if tagStart < 0 || !strings.Contains(html[tagStart:btn+100], `type="button"`) {
		t.Error("manual 入口按钮应是 type=\"button\"")
	}
	if !strings.Contains(html[btn:btn+200], "怎么获取？</button>") {
		t.Error("manual 入口按钮文案应是「怎么获取？」")
	}
	divStart := strings.LastIndex(html[:btn], `<div id="dns-form-manual"`)
	if divStart < 0 {
		t.Error("manual 入口前找不到 <div id=\"dns-form-manual\">")
	} else {
		// dns-form-manual div 在 #dns-cred-status 之前闭合：按钮须在
		// 该 div 开始之后、状态行之前。
		status := strings.Index(html, `id="dns-cred-status"`)
		if status < 0 || btn > status {
			t.Error("manual 入口应在 #dns-form-manual div 内（#dns-cred-status 之前）")
		}
	}
}

// 取 app.js 里 DNS_TUTORIALS 块（const DNS_TUTORIALS = { … };）。
func dnsTutorialBlock1417(t *testing.T, js string) string {
	t.Helper()
	start := strings.Index(js, "const DNS_TUTORIALS = {")
	if start < 0 {
		t.Fatal("app.js 里找不到 const DNS_TUTORIALS = {")
	}
	rest := js[start:]
	end := strings.Index(rest, "\n};")
	if end < 0 {
		t.Fatal("app.js 里找不到 DNS_TUTORIALS 块的结尾 };")
	}
	return rest[:end]
}

// v1.4.17：教程数据结构、委托接线、宽弹窗、链接数、步骤数、
// Namecheap 代码块与新路径。
func TestDNSTutorialContent1417(t *testing.T) {
	jsB, err := os.ReadFile(filepath.Join("web", "app.js"))
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	js := string(jsB)

	// 接线：initNodes 里 ibHelpWire() 下一行调 dnsTutorialWire()；
	// document 级委托、closest 找入口、showDNSTutorial 用 openModal 宽弹窗。
	for _, want := range []string{
		"function showDNSTutorial(provider)",
		"function dnsTutorialWire()",
		"document.addEventListener('click'",
		"closest('[data-dns-tutorial]')",
		"showDNSTutorial(btn.getAttribute('data-dns-tutorial'))",
		"provider === 'manual' ? '手动模式' : dnsProviderDisplay(provider) + ' 凭证获取教程'",
		"openModal('<h2>' + esc(title) + '</h2><div class=\"dns-tutorial\">' + html + '</div>', true)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js 应含 %q", want)
		}
	}
	ib := strings.Index(js, "ibHelpWire();")
	wire := strings.Index(js, "dnsTutorialWire();")
	if ib < 0 || wire < 0 {
		t.Fatal("app.js 里找不到 ibHelpWire() / dnsTutorialWire() 调用")
	}
	if wire < ib {
		t.Error("dnsTutorialWire() 应在 ibHelpWire() 之后调用（initNodes 里 ibHelpWire 下一行）")
	}

	block := dnsTutorialBlock1417(t, js)
	// 17 家服务商的 key 都在块里。
	for _, p := range dnsTutorialProviders1417 {
		if !strings.Contains(block, "\n  "+p.id+":") {
			t.Errorf("DNS_TUTORIALS 里缺 %s", p.id)
		}
	}
	if !strings.Contains(block, "\n  manual:") {
		t.Error("DNS_TUTORIALS 里缺 manual")
	}
	// 教程块内 27 处新标签页链接（全文件是 29 处，块外另有 2 处，
	// 见本文件头注释——只能在块内数）。
	if n := strings.Count(block, `target="_blank" rel="noopener"`); n != 27 {
		t.Errorf("DNS_TUTORIALS 块内 target=\"_blank\" rel=\"noopener\" 应为 27 处，实际 %d 处", n)
	}
	// api.ipify.org 不在任何 <a href> 里，只在 Namecheap 代码块里出现一次。
	if strings.Contains(js, `<a href="https://api.ipify.org"`) || strings.Contains(js, `<a href=\"https://api.ipify.org\"`) {
		t.Error("api.ipify.org 不应出现在任何 <a href> 里（Namecheap 的 curl 命令只放代码块）")
	}
	if n := strings.Count(js, "api.ipify.org"); n != 1 {
		t.Errorf("app.js 里 api.ipify.org 应只出现 1 次（Namecheap 代码块），实际 %d 次", n)
	}
	if !strings.Contains(block, "curl https://api.ipify.org</code></pre>") {
		t.Error("Namecheap 第 6 步的 curl https://api.ipify.org 应在 <pre><code> 代码块里")
	}
	// 每家 <ol> 步骤数与文案文件一致；手动模式无 <ol>、只有 §18 那一句话。
	for _, p := range dnsTutorialProviders1417 {
		seg := dnsTutorialSegment1417(t, block, p.id)
		olStart := strings.Index(seg, "<ol>")
		olEnd := strings.Index(seg, "</ol>")
		if olStart < 0 || olEnd < 0 || olEnd < olStart {
			t.Errorf("%s 教程里找不到 <ol>…</ol> 步骤列表", p.id)
			continue
		}
		if n := strings.Count(seg[olStart:olEnd], "<li>"); n != p.steps {
			t.Errorf("%s 教程步骤数应为 %d（文案文件），实际 %d", p.id, p.steps, n)
		}
	}
	manual := dnsTutorialSegment1417(t, block, "manual")
	if strings.Contains(manual, "<ol>") {
		t.Error("manual 教程不应有步骤列表（§18 只有一句话）")
	}
	if strings.Contains(manual, "<a href=") {
		t.Error("manual 教程不应有链接（§18 只有一句话）")
	}
	if !strings.Contains(manual, "不用填凭证：点「开启 HTTPS」后，面板先下单并在下方给出 TXT 记录名与值，你去任意 DNS 后台加上，再点「已添加，继续验证」；订单 1 小时内有效，面板重启后仍可继续；不自动续签，到期前手动重新签发。") {
		t.Error("manual 教程内容应是文案文件 §18 那一句话")
	}
	// Namecheap 新路径在、旧路径不在（app.js 里 & 写成 &amp;）。
	if !strings.Contains(js, "Profile → Tools → Business &amp; Dev Tools → Namecheap API Access") {
		t.Error("app.js 里 Namecheap 教程应含新路径 Profile → Tools → Business & Dev Tools → Namecheap API Access")
	}
	if strings.Contains(js, "Profile → Security") {
		t.Error("app.js 里不应再有旧路径 Profile → Security（Namecheap 旧提示已修）")
	}
}

// 取 DNS_TUTORIALS 块内某家的一段（从 "  <id>:" 到下一家的 key 或块尾）。
func dnsTutorialSegment1417(t *testing.T, block, id string) string {
	t.Helper()
	start := strings.Index(block, "\n  "+id+":")
	if start < 0 {
		t.Fatalf("DNS_TUTORIALS 块里找不到 %s", id)
	}
	rest := block[start+1:]
	// 下一家 key 的形式是 "\n  <小写字母数字>: '"。
	end := -1
	for i := 1; i < len(rest); i++ {
		if rest[i] == '\n' && i+3 < len(rest) && rest[i+1] == ' ' && rest[i+2] == ' ' {
			j := i + 3
			for j < len(rest) && ((rest[j] >= 'a' && rest[j] <= 'z') || (rest[j] >= '0' && rest[j] <= '9')) {
				j++
			}
			if j < len(rest) && rest[j] == ':' && j+1 < len(rest) && rest[j+1] == ' ' {
				end = i
				break
			}
		}
	}
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// v1.4.17：Namecheap 面板旧提示已修（index.html 第 584 行附近）。
func TestNamecheapHintPath1417(t *testing.T) {
	htmlB, err := os.ReadFile(filepath.Join("web", "index.html"))
	if err != nil {
		t.Fatalf("读 index.html 失败：%v", err)
	}
	html := string(htmlB)
	if !strings.Contains(html, "Profile → Tools → Business & Dev Tools → Namecheap API Access") {
		t.Error("index.html 里 Namecheap 提示应含新路径 Profile → Tools → Business & Dev Tools → Namecheap API Access")
	}
	if strings.Contains(html, "Profile → Security") {
		t.Error("index.html 里不应再有旧路径 Profile → Security（Namecheap 旧提示已修）")
	}
}
