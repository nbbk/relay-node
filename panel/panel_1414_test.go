package main

// v1.4.15：
//   - 证书页改成跟节点一样的小卡片：#node-certs-list 加 nodes-grid 类，
//     证书卡片复用节点页同款 .mini-card（方形小卡片，min-height 260px、
//     按钮沉底）；信息一个不少（域名＋ref、服务商、状态／到期、引用它
//     的入站、上次续签失败、复制／续签／删除）；签发表单区域不动。
//   - 节点列表与证书列表都可以切列表视图：各加一个切换按钮（卡片视图
//     ↔ 列表视图），放在列表上方工具条里；列表视图是紧凑行（.list-view
//     ＋ .list-row），每行一条，关键信息横向排，操作按钮都在；选择记
//     localStorage（relay-panel-nodes-view／relay-panel-certs-view），
//     默认卡片视图。
//
// 覆盖：
//   - 版本断言 1.4.15。
//   - 前端静态断言：两个切换按钮、证书列表的 nodes-grid 类、卡片／
//     列表行渲染函数、localStorage 键与默认卡片视图、列表视图样式。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion1414(t *testing.T) {
	if Version != "1.4.17" {
		t.Fatalf("VERSION 应是 1.4.17，实际 %q", Version)
	}
}

// v1.4.15：设置页 HTTPS 卡片排布——DNS 服务商下拉框和 API Token
// 输入框是一组，开启／关闭 HTTPS 两个按钮不能夹在中间；按钮移到
// 整个服务商＋凭证组下面（保存凭证按钮与状态文案之后）。只挪位置，
// 按钮 id 与功能不动。
func TestHttpsCardLayout1414(t *testing.T) {
	htmlB, err := os.ReadFile(filepath.Join("web", "index.html"))
	if err != nil {
		t.Fatalf("读 index.html 失败：%v", err)
	}
	html := string(htmlB)
	idx := func(s string) int {
		i := strings.Index(html, s)
		if i < 0 {
			t.Fatalf("index.html 里找不到 %q", s)
		}
		return i
	}
	// 顺序：服务商下拉框 → Token 输入框 → 保存凭证按钮 → 状态文案 → 两个 HTTPS 按钮。
	provider := idx(`id="dns-provider"`)
	token := idx(`id="dns-cf-token"`)
	save := idx("保存 Cloudflare 凭证")
	status := idx(`id="dns-cred-status"`)
	enable := idx(`id="https-enable"`)
	disable := idx(`id="https-disable"`)
	if !(provider < token && token < save && save < status && status < enable && enable < disable) {
		t.Errorf("HTTPS 卡片顺序不对：服务商(%d) → Token(%d) → 保存凭证(%d) → 状态(%d) → 开启(%d) → 关闭(%d)，应严格递增",
			provider, token, save, status, enable, disable)
	}
	// 两个 HTTPS 按钮不在服务商下拉框与 Token 输入框之间。
	if enable < token || disable < token {
		t.Error("开启／关闭 HTTPS 按钮不应在 DNS 服务商下拉框与 API Token 输入框之间")
	}
	// 按钮已不在 https-form 里（表单只剩域名框＋服务商下拉框）。
	formStart := strings.Index(html, `id="https-form"`)
	formEnd := strings.Index(html[formStart:], "</form>")
	if formStart < 0 || formEnd < 0 {
		t.Fatal("index.html 里找不到 https-form")
	}
	if strings.Contains(html[formStart:formStart+formEnd], "https-enable") {
		t.Error("开启 HTTPS 按钮不应再嵌在 https-form 里，应在凭证组下面")
	}
}

func TestListViewFrontend1414(t *testing.T) {
	htmlB, err := os.ReadFile(filepath.Join("web", "index.html"))
	if err != nil {
		t.Fatalf("读 index.html 失败：%v", err)
	}
	html := string(htmlB)
	for _, want := range []string{
		`id="nodes-view-toggle"`,
		`id="node-certs-view-toggle"`,
		`id="node-certs-list" class="nodes-grid"`,
		`列表视图`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html 应含 %q", want)
		}
	}
	// 切换按钮在列表上方工具条里，不在签发表单里。
	formStart := strings.Index(html, `id="node-cert-issue-form"`)
	formEnd := strings.Index(html[formStart:], "</form>")
	if formStart < 0 || formEnd < 0 {
		t.Fatal("index.html 里找不到 node-cert-issue-form")
	}
	if strings.Contains(html[formStart:formStart+formEnd], "node-certs-view-toggle") {
		t.Error("证书视图切换按钮不应在签发表单里，应在列表上方工具条里")
	}

	jsB, err := os.ReadFile(filepath.Join("web", "app.js"))
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	js := string(jsB)
	for _, want := range []string{
		"relay-panel-nodes-view",
		"relay-panel-certs-view",
		"function loadListView(",
		"function saveListView(",
		"function syncViewToggle(",
		"function applyListViewClass(",
		"function nodeListRow(",
		"function certMiniCard(",
		"function certListRow(",
		"function certStatusOf(",
		"function certActionButtons(",
		"function nodeActionButtons(",
		// 列表行复用卡片的操作按钮绑定（data-* 属性一致才点得动）。
		"data-nodedetail",
		"data-cert-copy",
		"data-cert-renew",
		"data-cert-del",
		// 默认卡片视图：读不到／值不对都回 'card'。
		"return v === 'list' || v === 'card' ? v : 'card'",
		// 证书小卡片复用 .mini-card，旧 .card＋.kv 那版不再渲染证书。
		"class=\"mini-card static\" data-cert-card=",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js 应含 %q", want)
		}
	}
	// 证书渲染不再用旧的 .card＋.kv 拼证书行（改走 certMiniCard／certListRow）。
	if strings.Contains(js, `'<div class="card" style="margin-bottom:12px"><div class="kv"><span>证书</span>`) {
		t.Error("app.js 里旧的证书 .card＋.kv 渲染应已改掉，走 certMiniCard／certListRow")
	}

	cssB, err := os.ReadFile(filepath.Join("web", "style.css"))
	if err != nil {
		t.Fatalf("读 style.css 失败：%v", err)
	}
	css := string(cssB)
	for _, want := range []string{
		".list-view{display:flex; flex-direction:column; gap:8px}",
		".list-row{display:flex; align-items:center;",
		".list-row .btn-row{margin-left:auto;",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css 应含 %q", want)
		}
	}
}
