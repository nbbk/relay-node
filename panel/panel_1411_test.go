package main

// v1.4.11：添加入站表单字段帮助（ⓘ 按钮＋提示框）。
//
// 覆盖：
//   - 版本断言 1.4.12。
//   - index.html 里添加入站表单的 10 个字段 label 旁都有 data-ib-help
//     按钮（tag、protocol、port、listen、sni、reality-fingerprint、
//     ss-method、ss-password、socks-username、socks-password），是
//     type=button 的 button（点开只弹提示，不提交表单）。
//   - app.js 里 IB_HELP 有 10 个字段的帮助内容（端口／SNI／指纹／SS 方法／
//     SS 密码按当前协议联动），提示框逻辑（ibHelpShow／ibHelpHide／
//     ibHelpRefresh／ibHelpWire）齐全；协议切换时开着的提示框刷新内容。
//   - style.css 里有 .ib-help-btn 与 .ib-help-pop 样式。
// 行为级（弹出／关闭／一次只开一个／协议联动）见 jsdom test-1411.js。

import (
	"os"
	"strings"
	"testing"
)

func TestVersion1411(t *testing.T) {
	if Version != "1.4.42" {
		t.Fatalf("VERSION 应是 1.4.33，实际 %q", Version)
	}
}

func TestInboundHelpButtons1411(t *testing.T) {
	html, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 web/index.html 失败：%v", err)
	}
	keys := []string{"tag", "protocol", "port", "listen", "sni", "reality-fingerprint", "ss-method", "ss-password", "socks-username", "socks-password"}
	for _, key := range keys {
		needle := `data-ib-help="` + key + `"`
		if !strings.Contains(string(html), needle) {
			t.Fatalf("添加入站表单缺 %s 字段的帮助按钮（%s）", key, needle)
		}
	}
	// 按钮是 type=button（点开只弹提示，不提交表单），带 ib-help-btn 样式类。
	if !strings.Contains(string(html), `type="button" class="ib-help-btn" data-ib-help="tag"`) {
		t.Fatalf("帮助按钮应是 type=button 且带 ib-help-btn 样式类")
	}
	// 按钮在 label 里（紧跟 hint 或字段名），不是表单外的悬浮元素。
	if !strings.Contains(string(html), `<span class="hint">必填，节点内唯一，如 ss-8388</span><button type="button" class="ib-help-btn" data-ib-help="tag"`) {
		t.Fatalf("tag 字段的帮助按钮应在 label 内、hint 后面")
	}
}

func TestInboundHelpContent1411(t *testing.T) {
	js, err := os.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读 web/app.js 失败：%v", err)
	}
	// IB_HELP 10 个字段都在。
	entries := []string{"tag: {", "protocol: {", "port: {", "listen: {", "sni: {", "'reality-fingerprint': {", "'ss-method': {", "'ss-password': {", "'socks-username': {", "'socks-password': {"}
	for _, e := range entries {
		if !strings.Contains(string(js), e) {
			t.Fatalf("app.js 的 IB_HELP 缺条目 %s", e)
		}
	}
	// 提示框逻辑齐全。
	for _, fn := range []string{"function ibHelpShow(", "function ibHelpHide(", "function ibHelpRefresh(", "function ibHelpWire("} {
		if !strings.Contains(string(js), fn) {
			t.Fatalf("app.js 缺提示框逻辑 %s", fn)
		}
	}
	// 协议切换时开着的提示框刷新内容。
	if !strings.Contains(string(js), "ibHelpRefresh();") {
		t.Fatalf("协议切换处理里没有刷新提示框的 ibHelpRefresh() 调用")
	}
	// 关键口径都在帮助内容里（以实现为准，不是编的）：
	// 端口留空走默认、REALITY 占位值、指纹只对 REALITY 有效、SS2022
	// base64 定长密钥、SOCKS5 用户名空着节点上应用失败。
	for _, want := range []string{"留空走默认端口", "占位值 www.example.com", "只对 REALITY 入站有效", "16 字节密钥", "agent 要求用户名非空"} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("app.js 的帮助内容缺口径 %q", want)
		}
	}
	// 帮助里不许出现用户真实域名／IP：示例只用 example.com 与 203.0.113.1。
	if strings.Contains(string(js), "930327.xyz") || strings.Contains(string(js), "jiankong.cf") {
		t.Fatalf("帮助内容里不许出现用户真实域名")
	}
}

func TestInboundHelpCSS1411(t *testing.T) {
	css, err := os.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("读 web/style.css 失败：%v", err)
	}
	for _, want := range []string{".ib-help-btn", ".ib-help-pop", ".ib-help-x"} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("style.css 缺 %s 样式", want)
		}
	}
}
