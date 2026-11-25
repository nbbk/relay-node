package main

// v1.4.16：
//   - 证书页服务商下拉框同会话内不刷新问题：1.4.13 的过滤逻辑只删
//     option、不补回，同会话内在设置页配好新服务商后，证书页重渲染
//     也不会出现它，须刷新页面才出现。改成每次渲染前按当前 providers
//     列表重建 option（已配置的出现、未配置的不出现、手动恒保留，
//     顺序与 providers 列表一致），重建时保住用户当前选中的值。
//   - HTTPS 卡片的 #dns-provider 查过：它从不删 option（只回填选中
//     值），没有同样的问题，不动——未配置的服务商本来就要能选（选了
//     才填得了它的凭证表单），改成只列已配置反而会断掉配凭证的入口。
//
// 覆盖：
//   - 版本断言 1.4.16。
//   - 前端静态断言：renderNodeCerts 里按 providers 重建 option（不是
//     只删）、保住当前选中值、旧的只删不补写法已不在；renderDNSProvider
//     仍只回填选中值、不删不重建 option。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion1416(t *testing.T) {
	if Version != "1.4.26" {
		t.Fatalf("Version 应为 1.4.23，实际 %q", Version)
	}
}

// v1.4.16：证书页服务商下拉框每次渲染都按 providers 列表重建 option。
func TestNodeCertProviderRebuild1416(t *testing.T) {
	jsB, err := os.ReadFile(filepath.Join("web", "app.js"))
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	js := string(jsB)
	start := strings.Index(js, "function renderNodeCerts(r)")
	end := strings.Index(js[start:], "function showNodeCertError")
	if start < 0 || end < 0 {
		t.Fatal("app.js 里找不到 renderNodeCerts / showNodeCertError")
	}
	fn := js[start : start+end]
	for _, want := range []string{
		"const prev = sel.value",
		"sel.innerHTML = ''",
		"nodeCertProvidersCache.forEach",
		"document.createElement('option')",
		"o.value = p.id",
		// 未配置的不出现、手动恒保留（与 1.4.13 的过滤规则一致）。
		"if (p.id !== 'manual' && !p.configured) return",
		// 重建后保住当前选中值，留不住才退到第一个可用项。
		"if (Array.from(sel.options).some((o) => o.value === prev)) sel.value = prev",
		"else if (sel.options.length) sel.value = sel.options[0].value",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("renderNodeCerts 应含 %q（按 providers 重建 option 并保住选中值）", want)
		}
	}
	// 旧的只删不补写法已不在：renderNodeCerts 里不应再有 o.remove()。
	if strings.Contains(fn, "o.remove()") {
		t.Error("renderNodeCerts 里不应再有 o.remove()（只删不补是 1.4.16 修掉的根因）")
	}
}

// v1.4.16：HTTPS 卡片的 #dns-provider 不删 option，不动——
// renderDNSProvider 仍只回填选中值，不重建、不删除下拉框选项。
func TestDNSProviderNoFilter1416(t *testing.T) {
	jsB, err := os.ReadFile(filepath.Join("web", "app.js"))
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	js := string(jsB)
	start := strings.Index(js, "function renderDNSProvider(st)")
	end := strings.Index(js[start:], "function renderManualPending")
	if start < 0 || end < 0 {
		t.Fatal("app.js 里找不到 renderDNSProvider / renderManualPending")
	}
	fn := js[start : start+end]
	if !strings.Contains(fn, "sel.value = provider") {
		t.Error("renderDNSProvider 应仍回填 sel.value = provider")
	}
	if strings.Contains(fn, "innerHTML") || strings.Contains(fn, ".remove()") {
		t.Error("renderDNSProvider 不应重建或删除 #dns-provider 的 option（未配置的服务商要能选来配凭证）")
	}
}
