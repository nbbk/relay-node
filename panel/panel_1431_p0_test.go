package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// ---------- 1.4.31：面板 UI/UX 重设计第 1 批（P0 修复止血） ----------
//
// 只改前端（panel/web/app.js、index.html）与一处后端注释
// （handlers2.go:272），后端行为不动。本文件只做静态断言：版本号、
// 退场入口已清、九弹窗＋四弹窗的 error-box 都在、ⓘ 查不到不打开、
// 服务页中文 toast、成功区账号行。交互行为由 jsdom
// test-1432-p0-batch1.js 覆盖。

func TestVersion1431(t *testing.T) {
	if Version != "1.4.34" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.34" {
		t.Fatalf("VERSION 文件应为 1.4.33，实际 %q", strings.TrimSpace(string(vb)))
	}
}

// readWeb1431 读 panel/web 下的文件（测试工作目录就是 panel/）。
func readWeb1431(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("web/" + name)
	if err != nil {
		t.Fatalf("读 web/%s 失败：%v", name, err)
	}
	return string(b)
}

func TestP0RetiredEntriesRemoved1431(t *testing.T) {
	js := readWeb1431(t, "app.js")
	html := readWeb1431(t, "index.html")
	// P0-4：落地详情“订阅”按钮与勾选树“复制订阅”按钮已删，分组弹窗
	// 不再列合并订阅成员。
	for _, gone := range []string{
		"data-central-sub-landing",
		"data-sub-copy-landing",
		"data-group-member-merged",
		"只留集中管理、合并订阅与设置",
		"通用链接、每人链接、合并订阅",
		"三个大选项卡",
	} {
		if strings.Contains(js, gone) || strings.Contains(html, gone) {
			t.Fatalf("退场残留还在：%q", gone)
		}
	}
	// P0-4：落地详情按钮改成“新建订阅用户”，点后跳订阅页。
	if !strings.Contains(js, "data-central-new-user") {
		t.Fatalf("落地详情里没有 data-central-new-user（新建订阅用户）按钮")
	}
	// P2-10：管理面板模式文案与 MANAGER_PAGES 一致。
	// v1.4.33（第 3 批）：文案里的“用户管理”改成“订阅”。
	if !strings.Contains(html, "只留集中管理、订阅、节点与设置") {
		t.Fatalf("index.html 管理面板模式文案没改成订阅／节点")
	}
	h2, err := os.ReadFile("handlers2.go")
	if err != nil {
		t.Fatalf("读 handlers2.go 失败：%v", err)
	}
	if !strings.Contains(string(h2), "只显示集中管理、订阅、节点与设置") {
		t.Fatalf("handlers2.go 管理面板模式注释没改")
	}
	if strings.Contains(string(h2), "只显示集中管理、合并订阅与设置") {
		t.Fatalf("handlers2.go 里还写着合并订阅")
	}
}

func TestP0ErrorBoxesPresent1431(t *testing.T) {
	js := readWeb1431(t, "app.js")
	// P0-1：编辑入站弹窗内错误区；旧的“先 showInboundsError 再关弹窗”已删。
	if !strings.Contains(js, `id="nodes-ib-edit-error"`) {
		t.Fatalf("编辑入站弹窗里没有 #nodes-ib-edit-error")
	}
	if strings.Contains(js, "showInboundsError(err2.message); closeModal()") {
		t.Fatalf("编辑入站提交 catch 里还在先报错再关弹窗")
	}
	// P0-2：九个先关后请求的弹窗都有弹窗内 error-box。
	for _, id := range []string{
		`id="renew-err"`, `id="at-err"`, `id="ts-err"`, `id="paste-err"`,
		`id="central-add-err"`, `id="central-renew-err"`, `id="central-ts-err"`,
		`id="central-at-err"`, `id="central-paste-err"`,
	} {
		if !strings.Contains(js, id) {
			t.Fatalf("缺 error-box：%s", id)
		}
	}
	// P0-10：四个用户／档位／分组弹窗的 error-box。
	for _, attr := range []string{
		"data-user-error", "data-plan-error", "data-group-error", "data-edit-error",
	} {
		if !strings.Contains(js, attr) {
			t.Fatalf("缺 error-box：%s", attr)
		}
	}
}

func TestP0ServiceAndSuccessText1431(t *testing.T) {
	js := readWeb1431(t, "app.js")
	html := readWeb1431(t, "index.html")
	// P0-8：重启确认与中文 toast，英文动作名 toast 已删。
	if !strings.Contains(js, "重启后正在用的节点会瞬断几秒，转发配置本身不受影响") {
		t.Fatalf("服务页重启确认没写清瞬断与配置不受影响")
	}
	if !strings.Contains(js, "已重启 realm") || strings.Contains(js, "toast('已执行：' + act") {
		t.Fatalf("服务页 toast 没改中文或还留着“已执行：”")
	}
	if !strings.Contains(html, "realm 没在跑时点这里启动中转") {
		t.Fatalf("服务页启动按钮旁没有用途说明")
	}
	// P0-7：添加成功清空表单，twin 成功清空链接输入框。
	if !strings.Contains(js, "addForm.reset()") {
		t.Fatalf("submitAdd 成功后没有清空添加表单")
	}
	if !strings.Contains(js, "twinLink.value = ''") {
		t.Fatalf("initTwin 成功后没有清空链接输入框")
	}
	// P0-9：成功区账号行与格式说明。
	if !strings.Contains(html, `id="sub-user-username-value"`) || !strings.Contains(html, `id="sub-user-account"`) {
		t.Fatalf("成功区没有登录账号行（#sub-user-account / #sub-user-username-value）")
	}
	if !strings.Contains(js, "u-＋12 位随机字符") {
		t.Fatalf("登录账号 hint 没写自动生成的格式")
	}
}

// TestP0IBHelpComplete1431 全站 data-ib-help 指向的键都必须在 IB_HELP
// 里有条目（P0-3；设计文档称缺 26 个是旧版情况，1.4.18／1.4.27／
// 1.4.29 已补齐，本版断言全覆盖、防止以后新增按钮又漏条目）。
func TestP0IBHelpComplete1431(t *testing.T) {
	js := readWeb1431(t, "app.js")
	html := readWeb1431(t, "index.html")
	// ibHelpShow 查不到条目时直接不打开弹窗。
	if !strings.Contains(js, "if (!IB_HELP[key])") {
		t.Fatalf("ibHelpShow 里没有查不到条目时不打开的防护")
	}
	start := strings.Index(js, "const IB_HELP = {")
	if start < 0 {
		t.Fatalf("找不到 IB_HELP 定义")
	}
	rest := js[start:]
	end := strings.Index(rest, "\n};")
	if end < 0 {
		t.Fatalf("IB_HELP 定义没有结束")
	}
	block := rest[:end]
	defined := map[string]bool{}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		m := regexp.MustCompile(`^'?([a-z0-9-]+)'?: \{$`).FindStringSubmatch(line)
		if m != nil {
			defined[m[1]] = true
		}
	}
	if len(defined) == 0 {
		t.Fatalf("IB_HELP 里一个条目都没解析出来")
	}
	usedRe := regexp.MustCompile(`data-ib-help="([^"]+)"`)
	used := map[string]bool{}
	for _, m := range usedRe.FindAllStringSubmatch(js+html, -1) {
		used[m[1]] = true
	}
	var missing []string
	for k := range used {
		if !defined[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("data-ib-help 指向不存在的 IB_HELP 条目：%v（共 %d 个已用键、%d 个已定义条目）", missing, len(used), len(defined))
	}
	t.Logf("IB_HELP：已用 %d 个键，全部有条目", len(used))
}
