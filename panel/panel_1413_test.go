package main

// v1.4.13：
//   - 节点列表改方形小卡片网格（#nodes-list 加 nodes-grid 类，auto-fill
//     280px 列、卡片 min-height 260px、按钮沉底）。
//   - 手动添加入站拆出单独弹窗（新 template nodes-ib-add-template，
//     openNodeIbAddModal／wireNodeIbAddModal；提交成功回入站模板弹窗，
//     失败弹窗保持打开、报错可见、表单保留）。
//   - 节点证书泛域名双模式：泛域名与单个子域名并存，签发时选；匹配精确
//     优先、泛域名只覆盖一级子域名；覆盖关系只警告、不拦截（warning 字段，
//     前端 toast 显示）；ACME TXT 名去掉 `*.` 再拼。
//   - 证书字段 ⓘ 提示：证书域名与 DNS 服务商两个字段复用入站表单的共享
//     提示框机制（IB_HELP 加 cert-domain／cert-provider）。
//   - DNS 服务商下拉框只列已配置的服务商＋手动模式；一个都没配时证书卡片
//     给提示（node-cert-provider-hint）。
//
// 覆盖：
//   - 版本断言 1.4.13。
//   - validNodeCertDomain／nodeCertDomainMatches／acmeChallengeName 纯函数。
//   - nodeCertForDomain（精确优先／一级子域名／裸域与多级不匹配／过期
//     泛域名跳过）、nodeCertUsedBy 与 bump 的覆盖口径、configDocOf 下发
//     绑定走泛域名。
//   - HTTP 接口：签发去重（精确重复 400、覆盖关系 warning 不拦截，双向）、
//     手动 start 的 TXT 名与 warning、泛域名证书被引用时删除 400、选未配置
//     服务商的 400 文案指到设置页。
//   - 前端静态断言：网格 CSS、添加弹窗、证书字段 ⓘ、服务商下拉框过滤与
//     提示元素、warning 的前端呈现。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersion1413(t *testing.T) {
	if Version != "1.4.29" {
		t.Fatalf("VERSION 应是 1.4.29，实际 %q", Version)
	}
}

// ---------- 1. 泛域名校验与匹配（纯函数） ----------

func TestNodeCertWildcardValidDomain1413(t *testing.T) {
	cases := []struct {
		domain string
		ok     bool
	}{
		{"example.com", true},
		{"hy2ceshi.example.com", true},
		{"*.example.com", true},
		{"*.a.b.example.com", true}, // 多级基域的泛域名也允许，只覆盖其下一级
		{"*example.com", false},     // `*.` 开头才算泛域名
		{"*.", false},
		{"*.example.com.", false}, // 尾点不合法
		{"not a domain", false},
		{"", false},
		{"example", false}, // 至少要有一级子域
	}
	for _, c := range cases {
		if got := validNodeCertDomain(c.domain); got != c.ok {
			t.Errorf("validNodeCertDomain(%q) 应为 %v，实际 %v", c.domain, c.ok, got)
		}
	}
}

func TestNodeCertWildcardMatch1413(t *testing.T) {
	cases := []struct {
		cert, domain string
		match        bool
	}{
		{"example.com", "example.com", true},
		{"Example.COM", "example.com", true}, // 大小写不敏感
		{"example.com", "other.example.com", false},
		{"*.example.com", "hy2ceshi.example.com", true},
		{"*.example.com", "example.com", false},     // 裸域不匹配
		{"*.example.com", "a.b.example.com", false}, // 多级不匹配
		{"*.example.com", "hy2ceshi.other.com", false},
		{"*.example.com", "*.example.com", true}, // 两个泛域名完全相同算相等
		{"hy2ceshi.example.com", "hy2ceshi.example.com", true},
		{"hy2ceshi.example.com", "other.example.com", false},
		{"*.a.example.com", "x.a.example.com", true},
		{"*.a.example.com", "x.y.a.example.com", false},
		{"", "example.com", false},
		{"*.example.com", "", false},
	}
	for _, c := range cases {
		if got := nodeCertDomainMatches(c.cert, c.domain); got != c.match {
			t.Errorf("nodeCertDomainMatches(%q, %q) 应为 %v，实际 %v", c.cert, c.domain, c.match, got)
		}
	}
}

func TestNodeCertWildcardTXTName1413(t *testing.T) {
	cases := []struct {
		domain, want string
	}{
		{"*.example.com", "_acme-challenge.example.com"},
		{"example.com", "_acme-challenge.example.com"},
		{" *.example.com ", "_acme-challenge.example.com"},
		{"hy2ceshi.example.com", "_acme-challenge.hy2ceshi.example.com"},
	}
	for _, c := range cases {
		if got := acmeChallengeName(c.domain); got != c.want {
			t.Errorf("acmeChallengeName(%q) 应为 %q，实际 %q", c.domain, c.want, got)
		}
	}
}

// ---------- 2. store 级匹配：精确优先／过期跳过／引用与 bump ----------

func TestNodeCertWildcardForDomain1413(t *testing.T) {
	s := newTestServer1400(t)
	now := time.Now()
	seedNodeCert1412(t, s, 1, "*.example.com", "cloudflare", now.Add(90*24*time.Hour))
	seedNodeCert1412(t, s, 2, "hy2ceshi.example.com", "cloudflare", now.Add(90*24*time.Hour))
	seedNodeCert1412(t, s, 3, "*.other.com", "cloudflare", now.Add(-24*time.Hour)) // 过期泛域名
	seedNodeCert1412(t, s, 4, "api.example.com", "cloudflare", now.Add(30*24*time.Hour))
	seedNodeCert1412(t, s, 5, "*.example.com", "cloudflare", now.Add(10*24*time.Hour)) // 同口径第二张，到期更早
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	cases := []struct {
		domain string
		want   int64 // 0 = 不应匹配到证书
	}{
		{"hy2ceshi.example.com", 2}, // 精确优先于泛域名
		{"www.example.com", 1},      // 泛域名匹配，同口径多张取到期最晚
		{"API.EXAMPLE.COM", 4},      // 大小写不敏感的精确匹配
		{"example.com", 0},          // 裸域不匹配泛域名
		{"a.b.example.com", 0},      // 多级不匹配泛域名
		{"x.other.com", 0},          // 过期的泛域名不算
		{"nope.other.com", 0},
	}
	for _, c := range cases {
		got := s.nodes.nodeCertForDomain(c.domain, now)
		var ref int64
		if got != nil {
			ref = got.Ref
		}
		if ref != c.want {
			t.Errorf("nodeCertForDomain(%q) 应为 ref %d，实际 %d", c.domain, c.want, ref)
		}
	}
}

// 泛域名证书的引用检查与续签 bump、configDocOf 下发绑定都走覆盖口径。
func TestNodeCertWildcardUsedByAndBump1413(t *testing.T) {
	s := newTestServer1400(t)
	now := time.Now()
	seedNodeCert1412(t, s, 1, "*.example.com", "cloudflare", now.Add(90*24*time.Hour))
	node := &BackendNode{
		ID: 1, Name: "新落地-01", Address: "203.0.113.1", ConfigVersion: 7,
		Inbounds: []NodeInbound{
			{NodeID: 1, InboundID: 1, InboundTag: "hy2-443", Protocol: "hysteria2", Transport: "quic", Security: "tls", Port: 443, SNI: "hy2ceshi.example.com", Domain: "hy2ceshi.example.com", Enabled: true},
			{NodeID: 1, InboundID: 2, InboundTag: "hy2-deep", Protocol: "hysteria2", Transport: "quic", Security: "tls", Port: 8443, SNI: "a.b.example.com", Domain: "a.b.example.com", Enabled: true},
		},
	}
	s.nodes.mu.Lock()
	s.nodes.data.Nodes = append(s.nodes.data.Nodes, node)
	// 引用：只有一级子域名那条入站算引用，多级那条不算。
	used := s.nodes.nodeCertUsedBy(1)
	if len(used) != 1 || used[0] != "新落地-01 / hy2-443" {
		s.nodes.mu.Unlock()
		t.Fatalf("泛域名证书的 used_by 应只有 [新落地-01 / hy2-443]，实际 %v", used)
	}
	// 下发绑定：hy2-443 拿到泛域名证书（cert_ref 1），多级那条留 0。
	doc := s.nodes.configDocOf(node, nil)
	if len(doc.Certs) != 1 || doc.Certs[0].Ref != 1 {
		s.nodes.mu.Unlock()
		t.Fatalf("configDocOf 应只下发泛域名证书 ref 1，实际 %+v", doc.Certs)
	}
	if got := docInbound1412(t, doc, "hy2-443").CertRef; got != 1 {
		s.nodes.mu.Unlock()
		t.Fatalf("hy2-443 的 cert_ref 应为 1，实际 %d", got)
	}
	if got := docInbound1412(t, doc, "hy2-deep").CertRef; got != 0 {
		s.nodes.mu.Unlock()
		t.Fatalf("多级子域名入站的 cert_ref 应留 0，实际 %d", got)
	}
	// 续签 bump：泛域名证书续签时，被它覆盖的入站所在节点 bump；不覆盖的不 bump。
	s.nodes.bumpNodesUsingCertDomainLocked("*.example.com")
	if v := s.nodes.data.Nodes[0].ConfigVersion; v != 8 {
		s.nodes.mu.Unlock()
		t.Fatalf("泛域名续签 bump 后配置版本应为 8，实际 %d", v)
	}
	s.nodes.bumpNodesUsingCertDomainLocked("other.example.com")
	if v := s.nodes.data.Nodes[0].ConfigVersion; v != 8 {
		s.nodes.mu.Unlock()
		t.Fatalf("不覆盖的域名 bump 不应涨版本，实际 %d", v)
	}
	s.nodes.mu.Unlock()
}

// ---------- 3. HTTP 接口：去重警告（双向）与 400 文案 ----------

func TestNodeCertWildcardIssueWarning1413(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	m := newMockACME145(t, 201)
	stubNodeCertACME1412(t, s, m)
	// 先签泛域名：200，无 warning。
	code, body := issueNodeCert1412(t, h, cookie, "*.example.com", "cloudflare")
	if code != 200 {
		t.Fatalf("签发泛域名应 200，实际 %d：%s", code, body)
	}
	mm := respMap1400(t, body)
	if mm["warning"] != nil {
		t.Fatalf("第一次签泛域名不应有 warning：%s", body)
	}
	if cert, _ := mm["cert"].(map[string]any); cert == nil || cert["domain"] != "*.example.com" {
		t.Fatalf("签发的证书域名应为 *.example.com：%s", body)
	}
	// 已有泛域名再签其中一个子域名：200＋warning，不拦截。
	code, body = issueNodeCert1412(t, h, cookie, "hy2ceshi.example.com", "cloudflare")
	if code != 200 {
		t.Fatalf("已有泛域名时签子域名应 200（只警告不拦截），实际 %d：%s", code, body)
	}
	warn, _ := respMap1400(t, body)["warning"].(string)
	if !strings.Contains(warn, "已有泛域名证书 *.example.com") || !strings.Contains(warn, "单个优先") {
		t.Fatalf("子域名签发的 warning 应提泛域名与单个优先，实际 %q", warn)
	}
	// 完全相同的域名重复签发仍 400（大小写不同也算相同）。
	if code, body := issueNodeCert1412(t, h, cookie, "*.EXAMPLE.COM", "cloudflare"); code != 400 {
		t.Fatalf("泛域名重复签发应 400，实际 %d：%s", code, body)
	}
	if code, body := issueNodeCert1412(t, h, cookie, "hy2ceshi.example.com", "cloudflare"); code != 400 {
		t.Fatalf("子域名重复签发应 400，实际 %d：%s", code, body)
	}
	// 两张证书并存。
	if code, body := listNodeCerts1412(t, h, cookie); code != 200 {
		t.Fatalf("证书列表应 200，实际 %d：%s", code, body)
	} else if certs, _ := respMap1400(t, body)["certs"].([]any); len(certs) != 2 {
		t.Fatalf("泛域名＋子域名签发后列表应有 2 张，实际 %d 张", len(certs))
	}
	// 反方向（新服务器）：已有单域名再签覆盖它的泛域名，同样 200＋warning。
	s2 := newTestServer1400(t)
	h2 := s2.routes()
	cookie2 := loginTestCookie(t, h2)
	m2 := newMockACME145(t, 201)
	stubNodeCertACME1412(t, s2, m2)
	if code, body := issueNodeCert1412(t, h2, cookie2, "api.example.com", "cloudflare"); code != 200 {
		t.Fatalf("先签单域名应 200，实际 %d：%s", code, body)
	}
	code, body = issueNodeCert1412(t, h2, cookie2, "*.example.com", "cloudflare")
	if code != 200 {
		t.Fatalf("已有单域名时签泛域名应 200（只警告不拦截），实际 %d：%s", code, body)
	}
	warn, _ = respMap1400(t, body)["warning"].(string)
	if !strings.Contains(warn, "已有单域名证书 api.example.com") || !strings.Contains(warn, "单个优先") {
		t.Fatalf("泛域名签发的 warning 应提被覆盖的单域名与单个优先，实际 %q", warn)
	}
	// 选未配置的服务商：400 文案要指到设置页 DNS 服务商卡片。
	if code, body := issueNodeCert1412(t, h2, cookie2, "www.example.com", "aliyun"); code != 400 || !strings.Contains(body, "设置页") || !strings.Contains(body, "DNS 服务商卡片") {
		t.Fatalf("选未配置服务商应 400 并指到设置页 DNS 服务商卡片，实际 %d：%s", code, body)
	}
}

// ---------- 4. 手动 start：泛域名 TXT 名与 warning ----------

func TestNodeCertWildcardManualStart1413(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/settings/dns-provider", `{"provider":"manual"}`); code != 200 {
		t.Fatalf("切手动服务商应 200，实际 %d：%s", code, body)
	}
	m := newMockACME145(t, 201)
	oldDir := acmeDirectoryURL
	acmeDirectoryURL = m.url("/directory")
	t.Cleanup(func() { acmeDirectoryURL = oldDir })
	stubACMESleep1349(t)
	// 已有一张单域名证书。
	seedNodeCert1412(t, s, 1, "api.example.com", "cloudflare", time.Now().Add(90*24*time.Hour))
	// 手动 start 泛域名：200，TXT 名去掉 `*.` 再拼，warning 提被覆盖的单域名。
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/node-certs/manual/start", `{"domain":"*.example.com"}`)
	if code != 200 {
		t.Fatalf("手动 start 泛域名应 200，实际 %d：%s", code, body)
	}
	mm := respMap1400(t, body)
	if mm["txt_name"] != "_acme-challenge.example.com" {
		t.Fatalf("泛域名订单的 TXT 名应为 _acme-challenge.example.com，实际 %q", mm["txt_name"])
	}
	warn, _ := mm["warning"].(string)
	if !strings.Contains(warn, "已有单域名证书 api.example.com") || !strings.Contains(warn, "单个优先") {
		t.Fatalf("手动 start 的 warning 应提被覆盖的单域名与单个优先，实际 %q", warn)
	}
	// 取消后，手动 start 已有单域名：精确重复仍 400。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/node-certs/manual/cancel", `{}`); code != 200 {
		t.Fatalf("手动 cancel 应 200，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/node-certs/manual/start", `{"domain":"api.example.com"}`); code != 400 {
		t.Fatalf("手动 start 精确重复域名应 400，实际 %d：%s", code, body)
	}
}

// ---------- 5. 泛域名证书被引用时删除拦住 ----------

func TestNodeCertWildcardDeleteBlocked1413(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	m := newMockACME145(t, 201)
	stubNodeCertACME1412(t, s, m)
	if code, body := issueNodeCert1412(t, h, cookie, "*.example.com", "cloudflare"); code != 200 {
		t.Fatalf("签发泛域名应 200，实际 %d：%s", code, body)
	}
	nodeID, _ := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	inID, _ := createInbound1401(t, h, cookie, nodeID, `{"inbound_tag":"hy2-443","protocol":"hysteria2","transport":"quic","security":"tls","port":443,"sni":"hy2ceshi.example.com","domain":"hy2ceshi.example.com"}`)
	// 被泛域名覆盖的入站引用着它：删除 400，并列出引用它的入站。
	code, body := authed1400(t, h, cookie, "DELETE", "/api/v1/node-certs/1", "")
	if code != 400 || !strings.Contains(body, "hy2-443") {
		t.Fatalf("被泛域名覆盖的入站引用时删除应 400 并列出 hy2-443，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "DELETE", "/api/v1/nodes/"+itoa1400(nodeID)+"/inbounds/"+itoa1400(inID), ""); code != 200 {
		t.Fatalf("删入站应 200，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "DELETE", "/api/v1/node-certs/1", ""); code != 200 {
		t.Fatalf("无引用后删除应 200，实际 %d：%s", code, body)
	}
	if _, err := os.Stat(filepath.Join(s.env.PanelHome, "certs", "node-1.crt")); !os.IsNotExist(err) {
		t.Fatal("删除后 node-1.crt 应清除")
	}
}

// ---------- 6. 前端静态断言 ----------

func TestNodeCertWildcardFrontend1413(t *testing.T) {
	htmlB, err := os.ReadFile(filepath.Join("web", "index.html"))
	if err != nil {
		t.Fatalf("读 index.html 失败：%v", err)
	}
	html := string(htmlB)
	// 方形小卡片网格。
	for _, want := range []string{
		`id="nodes-list" class="nodes-list nodes-grid"`,
		`id="nodes-ib-add-template"`,
		`id="nodes-ib-add-open"`,
		`id="nodes-inbound-add-form"`,
		`id="nodes-inbounds-template"`,
		`data-ib-help="cert-domain"`,
		`data-ib-help="cert-provider"`,
		`id="node-cert-provider-hint"`,
		"还没有配置任何 DNS 服务商凭证",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html 应含 %q", want)
		}
	}
	// 添加表单在新 template 里，不在入站模板 template 里。
	tplStart := strings.Index(html, `id="nodes-inbounds-template"`)
	tplEnd := strings.Index(html[tplStart:], "</template>")
	if tplStart < 0 || tplEnd < 0 {
		t.Fatal("index.html 里找不到 nodes-inbounds-template")
	}
	if strings.Contains(html[tplStart:tplStart+tplEnd], "nodes-inbound-add-form") {
		t.Error("添加表单不应再嵌在入站模板 template 里，应在 nodes-ib-add-template 里")
	}
	addStart := strings.LastIndex(html, `<template id="nodes-ib-add-template">`)
	addEnd := strings.Index(html[addStart:], "</template>")
	if addStart < 0 || addEnd < 0 {
		t.Fatal("index.html 里找不到 nodes-ib-add-template")
	}
	if !strings.Contains(html[addStart:addStart+addEnd], "nodes-inbound-add-form") {
		t.Error("nodes-ib-add-template 里应有添加表单 nodes-inbound-add-form")
	}
	jsB, err := os.ReadFile(filepath.Join("web", "app.js"))
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	js := string(jsB)
	for _, want := range []string{
		"function openNodeIbAddModal(",
		"function wireNodeIbAddModal(",
		"nodes-ib-add-template",
		"loadNodeInbounds(currentInboundsNodeId, currentInboundsNodeName)",
		"function nodeCertDomainMatches(",
		"return exact || wild",
		"'cert-domain': {",
		"'cert-provider': {",
		"凭证在设置页 DNS 服务商卡片里配，配好后这里才会出现；手动模式不用配凭证",
		"configuredCount",
		// v1.4.16：只删不补改成每次渲染前按 providers 重建 option
		//（同会话内新配好的服务商不刷新也能出现），旧的 o.remove()
		// 写法已不在，改断言成重建的两个标记。
		"sel.innerHTML = ''",
		"document.createElement('option')",
		"node-cert-provider-hint",
		"if (r.warning) toast(r.warning, 'ok')",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js 应含 %q", want)
		}
	}
	// warning toast 两处签发成功分支都有（自动签发＋手动 start）。
	if n := strings.Count(js, "if (r.warning) toast(r.warning, 'ok')"); n != 2 {
		t.Errorf("app.js 里 warning toast 应有 2 处（自动签发＋手动 start），实际 %d 处", n)
	}
	cssB, err := os.ReadFile(filepath.Join("web", "style.css"))
	if err != nil {
		t.Fatalf("读 style.css 失败：%v", err)
	}
	css := string(cssB)
	for _, want := range []string{
		".nodes-grid{display:grid; gap:14px; grid-template-columns:repeat(auto-fill,minmax(280px,1fr))}",
		".nodes-grid .mini-card{display:flex; flex-direction:column; min-height:260px}",
		".nodes-grid .mini-card .btn-row{margin-top:auto}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css 应含 %q", want)
		}
	}
	if strings.Contains(css, ".nodes-list{display:grid; gap:12px}") {
		t.Error("style.css 里旧的 .nodes-list 单列规则应已改掉")
	}
}
