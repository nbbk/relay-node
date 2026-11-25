package main

// v1.4.12：轻量节点后端切片 7——节点证书签发／下发／续签／置灰。
//
// 覆盖：
//   - 续签决策纯函数 shouldRenewNodeCert（到期前 30 天含已过期就续，
//     ExpiresAt 为空不续）与 nodeCertStatus（valid／expiring／expired／
//     unknown，现算不落盘）；inboundNeedsCert／inboundCertDomain 口径
//     （HY2／TUIC／AnyTLS／Trojan／VLESS＋TLS，agent 0.1.8 起 Trojan 也
//     是 Xray TLS 入站）。
//   - configDocOf 绑定（下发时按域名解析）：引用的有效证书进 certs[]
//     且入站 cert_ref 对上；过期／未引用／文件缺失不进；REALITY／SS
//     的 cert_ref 留 0；ETag 覆盖 certs 内容（只换 PEM 不 bump 版本，
//     ETag 也变——续签后 agent 拉到 200，不会卡在 304 上）。
//   - HTTP 接口：假 ACME 走 POST /api/v1/node-certs 签发成功（文件
//     600、元数据 ExpiresAt 为假证书 NotAfter）；非法域名／未知服务
//     商／manual 走自动接口／未配置凭证／重复域名 400；立即续签后用到
//     该证书的节点 ConfigVersion＋1（且证书文件换成新证书）；删除被引
//     用时 400 并列出引用，删入站后可删且文件清除；未登录 401。
//   - 手动两步走：start 生成 Kind=node 的 pending（含 ref），confirm
//     成功落盘；面板 manual start／confirm／cancel 在节点单进行中时
//     400（Kind 隔离）；节点 pending 不回填到设置页 manual_pending；
//     取消清掉 pending。
//   - maybeRenewNodeCerts：到期的自动证书续签＋bump；手动模式跳过；
//     未到期的不续。
//   - 前端静态断言：节点页证书卡片、七个服务商选项、证书页 JS 函数与
//     置灰逻辑（Trojan 同口径、REALITY 行“无证书”）、.ib-no-cert 样式。
//   - 脱敏：新文件与改动的页面里不出现用户真实域名／IP。
//   - 版本断言 1.4.12。

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersion1412(t *testing.T) {
	if Version != "1.4.23" {
		t.Fatalf("VERSION 应是 1.4.23，实际 %q", Version)
	}
}

// ---------- 1. 续签决策与状态（纯函数） ----------

func TestNodeCertRenewDecision1412(t *testing.T) {
	now := time.Now()
	// shouldRenewNodeCert：到期前 30 天含已过期就续，ExpiresAt 为空不续。
	cases := []struct {
		name   string
		exp    time.Time
		renew  bool
		status string
	}{
		{"90 天后到期不续", now.Add(90 * 24 * time.Hour), false, "valid"},
		{"31 天后到期不续", now.Add(31 * 24 * time.Hour), false, "valid"},
		{"30 天边界续", now.Add(30 * 24 * time.Hour), true, "expiring"},
		{"10 天后到期续", now.Add(10 * 24 * time.Hour), true, "expiring"},
		{"已过期续", now.Add(-24 * time.Hour), true, "expired"},
		{"ExpiresAt 为空不续", time.Time{}, false, "unknown"},
	}
	for _, c := range cases {
		if got := shouldRenewNodeCert(c.exp, now); got != c.renew {
			t.Errorf("%s：shouldRenewNodeCert 应为 %v，实际 %v", c.name, c.renew, got)
		}
		if got := nodeCertStatus(c.exp, now); got != c.status {
			t.Errorf("%s：nodeCertStatus 应为 %q，实际 %q", c.name, c.status, got)
		}
	}
}

// ---------- 2. 哪些入站要证书 ----------

func TestNodeCertInboundNeedsCert1412(t *testing.T) {
	need := []NodeInbound{
		{Protocol: "hysteria2", SNI: "example.com"},
		{Protocol: "tuic", SNI: "example.com"},
		{Protocol: "anytls", SNI: "example.com"},
		{Protocol: "trojan", SNI: "example.com"}, // agent 0.1.8 起 Trojan 也是 TLS 入站
		{Protocol: "vless", Security: "tls", SNI: "example.com"},
	}
	for _, in := range need {
		if !inboundNeedsCert(in) {
			t.Errorf("协议 %s（TLS）应要证书", in.Protocol)
		}
	}
	noNeed := []NodeInbound{
		{Protocol: "vless", Security: "reality", SNI: "www.example.com"},
		{Protocol: "vless", Security: "none"},
		{Protocol: "shadowsocks"},
		{Protocol: "socks"},
	}
	for _, in := range noNeed {
		if inboundNeedsCert(in) {
			t.Errorf("协议 %s（%s）不应要证书", in.Protocol, in.Security)
		}
	}
	// 域名绑定：Domain 优先，没有就用 SNI；不要证书的入站 ok=false。
	d, ok := inboundCertDomain(NodeInbound{Protocol: "hysteria2", Domain: "Example.COM", SNI: "other.example.com"})
	if !ok || d != "Example.COM" {
		t.Fatalf("有 Domain 时应取 Domain，实际 %q ok=%v", d, ok)
	}
	d, ok = inboundCertDomain(NodeInbound{Protocol: "hysteria2", SNI: "example.com"})
	if !ok || d != "example.com" {
		t.Fatalf("无 Domain 时应取 SNI，实际 %q ok=%v", d, ok)
	}
	if _, ok := inboundCertDomain(NodeInbound{Protocol: "vless", Security: "reality", SNI: "www.example.com"}); ok {
		t.Fatal("REALITY 不应有证书域名")
	}
}

// ---------- 3. configDocOf 绑定与 ETag ----------

// seedNodeCert1412 直接往 nodes.json 与证书文件里塞一张证书（不走
// ACME），返回它；文件内容就是给定的假 PEM，configDocOf 只读字节、
// 不解析 PEM。
func seedNodeCert1412(t *testing.T, s *Server, ref int64, domain, provider string, expiresAt time.Time) {
	t.Helper()
	dir := filepath.Join(s.env.PanelHome, "certs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建 certs 目录失败：%v", err)
	}
	crt := "fake-crt-pem-" + itoa1400(ref)
	key := "fake-key-pem-" + itoa1400(ref)
	if err := os.WriteFile(filepath.Join(dir, "node-"+itoa1400(ref)+".crt"), []byte(crt), 0o600); err != nil {
		t.Fatalf("写假证书失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node-"+itoa1400(ref)+".key"), []byte(key), 0o600); err != nil {
		t.Fatalf("写假私钥失败：%v", err)
	}
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	s.nodes.data.NodeCerts = append(s.nodes.data.NodeCerts, NodeCert{
		Ref: ref, Domain: domain, Provider: provider,
		IssuedAt: time.Now().Unix(), ExpiresAt: expiresAt.Unix(),
	})
	if ref >= s.nodes.data.NextCertRef {
		s.nodes.data.NextCertRef = ref + 1
	}
	if err := s.nodes.saveLocked(); err != nil {
		t.Fatalf("落盘 nodes.json 失败：%v", err)
	}
}

// nodeForDoc1412 造一个带四条入站的节点（REALITY／HY2／Trojan／SS），
// 直接塞进 store（不走 HTTP 校验）。
func nodeForDoc1412(t *testing.T, s *Server) *BackendNode {
	t.Helper()
	node := &BackendNode{
		ID: 1, Name: "新落地-01", Address: "203.0.113.1", ConfigVersion: 7,
		Inbounds: []NodeInbound{
			{NodeID: 1, InboundID: 1, InboundTag: "reality-443", Protocol: "vless", Transport: "reality", Security: "reality", Port: 443, SNI: "www.example.com", Enabled: true},
			{NodeID: 1, InboundID: 2, InboundTag: "hy2-443", Protocol: "hysteria2", Transport: "quic", Security: "tls", Port: 443, SNI: "example.com", Domain: "example.com", Enabled: true},
			{NodeID: 1, InboundID: 3, InboundTag: "trojan-8443", Protocol: "trojan", Transport: "tcp", Security: "tls", Port: 8443, SNI: "example.com", Domain: "example.com", Enabled: true},
			{NodeID: 1, InboundID: 4, InboundTag: "ss-8388", Protocol: "shadowsocks", Transport: "tcp", Security: "none", Port: 8388, Enabled: true},
		},
	}
	s.nodes.mu.Lock()
	s.nodes.data.Nodes = append(s.nodes.data.Nodes, node)
	s.nodes.mu.Unlock()
	return node
}

func docInbound1412(t *testing.T, doc nodeBackendConfigDoc, tag string) NodeInbound {
	t.Helper()
	for _, in := range doc.Inbounds {
		if in.InboundTag == tag {
			return in
		}
	}
	t.Fatalf("配置文档里没有入站 %s", tag)
	return NodeInbound{}
}

func TestNodeCertConfigDoc1412(t *testing.T) {
	s := newTestServer1400(t)
	now := time.Now()
	seedNodeCert1412(t, s, 1, "example.com", "cloudflare", now.Add(90*24*time.Hour))
	seedNodeCert1412(t, s, 2, "expired.example.com", "cloudflare", now.Add(-24*time.Hour)) // 过期
	seedNodeCert1412(t, s, 3, "other.example.com", "cloudflare", now.Add(90*24*time.Hour))  // 没入站引用
	node := nodeForDoc1412(t, s)

	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	doc := s.nodes.configDocOf(node, nil)
	if len(doc.Certs) != 1 {
		t.Fatalf("certs 应只含 example.com 一张，实际 %d 张", len(doc.Certs))
	}
	if doc.Certs[0].Ref != 1 || doc.Certs[0].Crt != "fake-crt-pem-1" || doc.Certs[0].Key != "fake-key-pem-1" {
		t.Fatalf("certs[0] 不对：%+v", doc.Certs[0])
	}
	if got := docInbound1412(t, doc, "hy2-443").CertRef; got != 1 {
		t.Fatalf("HY2 入站 cert_ref 应为 1，实际 %d", got)
	}
	if got := docInbound1412(t, doc, "trojan-8443").CertRef; got != 1 {
		t.Fatalf("Trojan 入站 cert_ref 应为 1，实际 %d", got)
	}
	if got := docInbound1412(t, doc, "reality-443").CertRef; got != 0 {
		t.Fatalf("REALITY 入站 cert_ref 应留 0，实际 %d", got)
	}
	if got := docInbound1412(t, doc, "ss-8388").CertRef; got != 0 {
		t.Fatalf("SS 入站 cert_ref 应留 0，实际 %d", got)
	}
}

// 证书文件被删：configDocOf 不发该证书，入站 cert_ref 留 0（agent
// fail-closed 跳过，不会拿到半截配置）。
func TestNodeCertConfigDocMissingFile1412(t *testing.T) {
	s := newTestServer1400(t)
	seedNodeCert1412(t, s, 1, "example.com", "cloudflare", time.Now().Add(90*24*time.Hour))
	node := nodeForDoc1412(t, s)
	dir := filepath.Join(s.env.PanelHome, "certs")
	if err := os.Remove(filepath.Join(dir, "node-1.crt")); err != nil {
		t.Fatalf("删假证书失败：%v", err)
	}
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	doc := s.nodes.configDocOf(node, nil)
	if len(doc.Certs) != 0 {
		t.Fatalf("证书文件缺失时 certs 应为空，实际 %d 张", len(doc.Certs))
	}
	if got := docInbound1412(t, doc, "hy2-443").CertRef; got != 0 {
		t.Fatalf("证书文件缺失时 HY2 cert_ref 应留 0，实际 %d", got)
	}
}

// ETag 覆盖 certs 内容：只换 PEM（版本号不动），ETag 也变——续签后
// agent 拉到 200、同步新证书，不会因为 ETag 不变而一直拿 304。
func TestNodeCertConfigDocETagCoversCerts1412(t *testing.T) {
	s := newTestServer1400(t)
	seedNodeCert1412(t, s, 1, "example.com", "cloudflare", time.Now().Add(90*24*time.Hour))
	node := nodeForDoc1412(t, s)
	s.nodes.mu.Lock()
	doc1 := s.nodes.configDocOf(node, nil)
	s.nodes.mu.Unlock()
	dir := filepath.Join(s.env.PanelHome, "certs")
	if err := os.WriteFile(filepath.Join(dir, "node-1.crt"), []byte("fake-crt-pem-1-renewed"), 0o600); err != nil {
		t.Fatalf("改写假证书失败：%v", err)
	}
	s.nodes.mu.Lock()
	doc2 := s.nodes.configDocOf(node, nil)
	s.nodes.mu.Unlock()
	if doc1.ConfigVersion != doc2.ConfigVersion {
		t.Fatalf("两次文档版本号应相同（只换了 PEM），实际 %d vs %d", doc1.ConfigVersion, doc2.ConfigVersion)
	}
	if doc2.Certs[0].Crt != "fake-crt-pem-1-renewed" {
		t.Fatalf("新文档证书内容应为续签后的，实际 %q", doc2.Certs[0].Crt)
	}
	if configETag(doc1) == configETag(doc2) {
		t.Fatal("只换证书 PEM 时 ETag 必须变，否则 agent 拿 304 永远拉不到新证书")
	}
}

// ---------- 4. HTTP 接口：签发 ----------

// stubNodeCertACME1412 把节点证书签发接到假 ACME 与假 Cloudflare 上
// （照 driveObtain145 的桩法），并把 Cloudflare 凭证配上。
func stubNodeCertACME1412(t *testing.T, s *Server, m *mockACME145) {
	t.Helper()
	oldDir := acmeDirectoryURL
	acmeDirectoryURL = m.url("/directory")
	t.Cleanup(func() { acmeDirectoryURL = oldDir })
	stubACMESleep1349(t)
	oldTXT := cfTXTFunc
	cfTXTFunc = func(s *Server, domain, value, deleteID string) (string, error) {
		if deleteID != "" {
			return "", nil
		}
		return "fake-record-id", nil
	}
	t.Cleanup(func() { cfTXTFunc = oldTXT })
	s.cfg.CloudflareToken = "cfut_testtoken1412"
}

func issueNodeCert1412(t *testing.T, h http.Handler, cookie, domain, provider string) (int, string) {
	t.Helper()
	return authed1400(t, h, cookie, "POST", "/api/v1/node-certs", `{"domain":"`+domain+`","provider":"`+provider+`"}`)
}

func listNodeCerts1412(t *testing.T, h http.Handler, cookie string) (int, string) {
	t.Helper()
	return authed1400(t, h, cookie, "GET", "/api/v1/node-certs", "")
}

func TestNodeCertsIssueAuto1412(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	// 未登录 401。
	if code, _ := authed1400(t, h, "", "GET", "/api/v1/node-certs", ""); code != 401 {
		t.Fatalf("未登录取证书列表应 401，实际 %d", code)
	}
	m := newMockACME145(t, 201)
	stubNodeCertACME1412(t, s, m)
	code, body := issueNodeCert1412(t, h, cookie, "example.com", "cloudflare")
	if code != 200 {
		t.Fatalf("签发应 200，实际 %d：%s", code, body)
	}
	cert, _ := respMap1400(t, body)["cert"].(map[string]any)
	if cert == nil {
		t.Fatalf("签发返回应带 cert：%s", body)
	}
	if cert["ref"] != float64(1) || cert["domain"] != "example.com" || cert["provider"] != "cloudflare" {
		t.Fatalf("签发返回的证书不对：%s", body)
	}
	if cert["status"] != "valid" {
		t.Fatalf("新证书状态应为 valid，实际 %v", cert["status"])
	}
	days, _ := cert["days_left"].(float64)
	if days < 89 || days > 90 {
		t.Fatalf("新证书剩余天数应在 89–90 天，实际 %v", days)
	}
	// 证书与私钥落盘，内容就是假服务端发的，且都是 600。
	dir := filepath.Join(s.env.PanelHome, "certs")
	crtB, err := os.ReadFile(filepath.Join(dir, "node-1.crt"))
	if err != nil {
		t.Fatalf("node-1.crt 没写下来：%v", err)
	}
	if string(crtB) != string(m.certPEM) {
		t.Fatal("node-1.crt 不是服务端发的证书")
	}
	st, err := os.Stat(filepath.Join(dir, "node-1.crt"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("node-1.crt 权限应为 600，实际 %v", st.Mode())
	}
	keyB, err := os.ReadFile(filepath.Join(dir, "node-1.key"))
	if err != nil {
		t.Fatalf("node-1.key 没写下来：%v", err)
	}
	blk, _ := pem.Decode(keyB)
	if blk == nil {
		t.Fatal("node-1.key 不是 PEM")
	}
	if _, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err != nil {
		t.Fatalf("node-1.key 解析失败：%v", err)
	}
	st, err = os.Stat(filepath.Join(dir, "node-1.key"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("node-1.key 权限应为 600，实际 %v", st.Mode())
	}
	// 列表里能看到，服务商已配置。
	code, body = listNodeCerts1412(t, h, cookie)
	if code != 200 {
		t.Fatalf("证书列表应 200，实际 %d：%s", code, body)
	}
	lm := respMap1400(t, body)
	certs, _ := lm["certs"].([]any)
	if len(certs) != 1 {
		t.Fatalf("列表应有 1 张证书，实际 %d 张：%s", len(certs), body)
	}
	provs, _ := lm["providers"].([]any)
	found := false
	for _, p := range provs {
		pm, _ := p.(map[string]any)
		if pm["id"] == "cloudflare" && pm["configured"] == true {
			found = true
		}
	}
	if !found {
		t.Fatalf("providers 里 cloudflare 应标注已配置：%s", body)
	}
}

func TestNodeCertsIssueValidation1412(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	m := newMockACME145(t, 201)
	stubNodeCertACME1412(t, s, m)
	// 非法域名 400。
	if code, body := issueNodeCert1412(t, h, cookie, "not a domain", "cloudflare"); code != 400 {
		t.Fatalf("非法域名应 400，实际 %d：%s", code, body)
	}
	// 未知服务商 400。
	if code, body := issueNodeCert1412(t, h, cookie, "example.com", "no-such-provider"); code != 400 {
		t.Fatalf("未知服务商应 400，实际 %d：%s", code, body)
	}
	// manual 走自动接口 400（手动要走两步签发）。
	if code, body := issueNodeCert1412(t, h, cookie, "example.com", "manual"); code != 400 || !strings.Contains(body, "两步") {
		t.Fatalf("manual 走自动接口应 400 提示两步签发，实际 %d：%s", code, body)
	}
	// 凭证没配的服务商 400。
	if code, body := issueNodeCert1412(t, h, cookie, "example.com", "aliyun"); code != 400 {
		t.Fatalf("未配置凭证的服务商应 400，实际 %d：%s", code, body)
	}
	// 同域名重复签发 400。
	if code, body := issueNodeCert1412(t, h, cookie, "example.com", "cloudflare"); code != 200 {
		t.Fatalf("第一次签发应 200，实际 %d：%s", code, body)
	}
	if code, body := issueNodeCert1412(t, h, cookie, "Example.COM", "cloudflare"); code != 400 {
		t.Fatalf("同域名（大小写不同）重复签发应 400，实际 %d：%s", code, body)
	}
	// 不存在的证书续签／删除 404。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/node-certs/99/renew", "{}"); code != 404 {
		t.Fatalf("不存在的证书续签应 404，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "DELETE", "/api/v1/node-certs/99", ""); code != 404 {
		t.Fatalf("不存在的证书删除应 404，实际 %d：%s", code, body)
	}
}

// ---------- 5. HTTP 接口：续签 bump 与删除拦住 ----------

func TestNodeCertsRenewBumpsConfigVersion1412(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	m1 := newMockACME145(t, 201)
	stubNodeCertACME1412(t, s, m1)
	if code, body := issueNodeCert1412(t, h, cookie, "example.com", "cloudflare"); code != 200 {
		t.Fatalf("签发应 200，实际 %d：%s", code, body)
	}
	nodeID, _ := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	createInbound1401(t, h, cookie, nodeID, `{"inbound_tag":"hy2-443","protocol":"hysteria2","transport":"quic","security":"tls","port":443,"sni":"example.com","domain":"example.com"}`)
	s.nodes.mu.Lock()
	vBefore := s.nodes.data.Nodes[0].ConfigVersion
	s.nodes.mu.Unlock()
	// 换一台假 ACME（新证书内容不同），续签后文件应换成新的。
	m2 := newMockACME145(t, 201)
	stubNodeCertACME1412(t, s, m2)
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/node-certs/1/renew", "{}")
	if code != 200 {
		t.Fatalf("续签应 200，实际 %d：%s", code, body)
	}
	crtB, err := os.ReadFile(filepath.Join(s.env.PanelHome, "certs", "node-1.crt"))
	if err != nil || string(crtB) != string(m2.certPEM) {
		t.Fatalf("续签后证书文件应换成新证书，err=%v", err)
	}
	s.nodes.mu.Lock()
	vAfter := s.nodes.data.Nodes[0].ConfigVersion
	s.nodes.mu.Unlock()
	if vAfter != vBefore+1 {
		t.Fatalf("续签后节点配置版本应从 %d 涨到 %d，实际 %d（agent 靠它 60 秒内拉到新证书）", vBefore, vBefore+1, vAfter)
	}
}

func TestNodeCertsDeleteBlocked1412(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	m := newMockACME145(t, 201)
	stubNodeCertACME1412(t, s, m)
	if code, body := issueNodeCert1412(t, h, cookie, "example.com", "cloudflare"); code != 200 {
		t.Fatalf("签发应 200，实际 %d：%s", code, body)
	}
	nodeID, _ := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	inID, _ := createInbound1401(t, h, cookie, nodeID, `{"inbound_tag":"hy2-443","protocol":"hysteria2","transport":"quic","security":"tls","port":443,"sni":"example.com","domain":"example.com"}`)
	// 被引用：删除 400，并列出引用它的入站。
	code, body := authed1400(t, h, cookie, "DELETE", "/api/v1/node-certs/1", "")
	if code != 400 || !strings.Contains(body, "hy2-443") {
		t.Fatalf("被引用的证书删除应 400 并列出 hy2-443，实际 %d：%s", code, body)
	}
	// 删掉入站后可以删，文件也清除。
	if code, body := authed1400(t, h, cookie, "DELETE", "/api/v1/nodes/"+itoa1400(nodeID)+"/inbounds/"+itoa1400(inID), ""); code != 200 {
		t.Fatalf("删入站应 200，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "DELETE", "/api/v1/node-certs/1", ""); code != 200 {
		t.Fatalf("无引用后删除应 200，实际 %d：%s", code, body)
	}
	if _, err := os.Stat(filepath.Join(s.env.PanelHome, "certs", "node-1.crt")); !os.IsNotExist(err) {
		t.Fatal("删除后 node-1.crt 应清除")
	}
	if _, err := os.Stat(filepath.Join(s.env.PanelHome, "certs", "node-1.key")); !os.IsNotExist(err) {
		t.Fatal("删除后 node-1.key 应清除")
	}
	if code, body := listNodeCerts1412(t, h, cookie); code != 200 {
		t.Fatalf("删除后列表应 200，实际 %d：%s", code, body)
	} else if certs, _ := respMap1400(t, body)["certs"].([]any); len(certs) != 0 {
		t.Fatalf("删除后列表应为空，实际 %d 张", len(certs))
	}
}

// ---------- 6. 手动两步走与面板隔离 ----------

func TestNodeCertsManualTwoStep1412(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	// 切到手动服务商。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/settings/dns-provider", `{"provider":"manual"}`); code != 200 {
		t.Fatalf("切手动服务商应 200，实际 %d：%s", code, body)
	}
	m := newMockACME145(t, 201)
	oldDir := acmeDirectoryURL
	acmeDirectoryURL = m.url("/directory")
	t.Cleanup(func() { acmeDirectoryURL = oldDir })
	stubACMESleep1349(t)
	// start：生成 Kind=node 的 pending（含 ref），回填 TXT 名与值。
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/node-certs/manual/start", `{"domain":"example.com"}`)
	if code != 200 {
		t.Fatalf("手动 start 应 200，实际 %d：%s", code, body)
	}
	mm := respMap1400(t, body)
	if mm["ref"] != float64(1) || mm["txt_name"] != "_acme-challenge.example.com" {
		t.Fatalf("手动 start 返回不对：%s", body)
	}
	txtValue, _ := mm["txt_value"].(string)
	if len(txtValue) != 43 {
		t.Fatalf("TXT 值应为 43 字符，实际 %d：%q", len(txtValue), txtValue)
	}
	pend, err := s.loadPendingOrder()
	if err != nil || pend == nil || pend.Kind != "node" || pend.Ref != 1 || pend.Domain != "example.com" {
		t.Fatalf("pending 应为 Kind=node、Ref=1、域名 example.com，实际 %+v err=%v", pend, err)
	}
	// 面板 manual start／confirm／cancel 在节点单进行中时 400（Kind 隔离）。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/settings/https/manual/start", `{"domain":"example.com"}`); code != 400 || !strings.Contains(body, "节点证书") {
		t.Fatalf("节点单进行中时面板 manual start 应 400 提示节点证书，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/settings/https/manual/confirm", `{"domain":"example.com"}`); code != 400 || !strings.Contains(body, "节点证书") {
		t.Fatalf("节点单进行中时面板 manual confirm 应 400，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/settings/https/manual/cancel", `{}`); code != 400 || !strings.Contains(body, "节点证书") {
		t.Fatalf("节点单进行中时面板 manual cancel 应 400，实际 %d：%s", code, body)
	}
	// 节点 pending 不回填到设置页 manual_pending。
	if code, sm := getJSON144(t, h, cookie, "/api/v1/settings"); code != 200 {
		t.Fatalf("取设置应 200，实际 %d", code)
	} else if sm["manual_pending"] != nil {
		t.Fatalf("节点 pending 不应出现在设置页 manual_pending 里：%v", sm["manual_pending"])
	}
	// confirm：TXT 查到值就签发成功，证书落盘，pending 清掉。
	oldLookup := acmeLookupTXT
	acmeLookupTXT = func(string) ([]string, error) { return []string{txtValue}, nil }
	t.Cleanup(func() { acmeLookupTXT = oldLookup })
	code, body = authed1400(t, h, cookie, "POST", "/api/v1/node-certs/manual/confirm", `{"ref":1,"domain":"example.com"}`)
	if code != 200 {
		t.Fatalf("手动 confirm 应 200，实际 %d：%s", code, body)
	}
	crtB, err := os.ReadFile(filepath.Join(s.env.PanelHome, "certs", "node-1.crt"))
	if err != nil || string(crtB) != string(m.certPEM) {
		t.Fatalf("confirm 后证书应落盘且等于假服务端发的，err=%v", err)
	}
	if pend, _ := s.loadPendingOrder(); pend != nil {
		t.Fatalf("confirm 成功后 pending 应清掉，实际 %+v", pend)
	}
	// 列表里是 manual 证书；手动证书立即续签 400。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/node-certs/1/renew", "{}"); code != 400 || !strings.Contains(body, "手动") {
		t.Fatalf("手动证书立即续签应 400 提示手动，实际 %d：%s", code, body)
	}
	// 再开一单手动，取消后 pending 清掉。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/node-certs/manual/start", `{"domain":"other.example.com"}`); code != 200 {
		t.Fatalf("第二单 manual start 应 200，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/node-certs/manual/cancel", `{}`); code != 200 {
		t.Fatalf("手动 cancel 应 200，实际 %d：%s", code, body)
	}
	if pend, _ := s.loadPendingOrder(); pend != nil {
		t.Fatalf("cancel 后 pending 应清掉，实际 %+v", pend)
	}
}

// ---------- 7. 自动续签循环 ----------

func TestMaybeRenewNodeCerts1412(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	now := time.Now()
	// 到期的自动证书（cloudflare）＋用到它的节点；手动的到期证书；未到期的证书。
	seedNodeCert1412(t, s, 1, "example.com", "cloudflare", now.Add(10*24*time.Hour))
	seedNodeCert1412(t, s, 2, "manual.example.com", "manual", now.Add(10*24*time.Hour))
	seedNodeCert1412(t, s, 3, "fresh.example.com", "cloudflare", now.Add(90*24*time.Hour))
	nodeID, _ := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	createInbound1401(t, h, cookie, nodeID, `{"inbound_tag":"hy2-443","protocol":"hysteria2","transport":"quic","security":"tls","port":443,"sni":"example.com","domain":"example.com"}`)
	s.nodes.mu.Lock()
	vBefore := s.nodes.data.Nodes[0].ConfigVersion
	s.nodes.mu.Unlock()
	m := newMockACME145(t, 201)
	stubNodeCertACME1412(t, s, m)
	s.maybeRenewNodeCerts()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	byRef := map[int64]NodeCert{}
	for _, c := range s.nodes.data.NodeCerts {
		byRef[c.Ref] = c
	}
	// 到期的自动证书续成新证书（假 ACME 的 90 天），且没记失败。
	if byRef[1].ExpiresAt < now.Add(80*24*time.Hour).Unix() {
		t.Fatalf("到期的自动证书应续成约 90 天，实际到期 %v", byRef[1].ExpiresAt)
	}
	if byRef[1].LastError != "" {
		t.Fatalf("续签成功后 LastError 应为空，实际 %q", byRef[1].LastError)
	}
	// 节点配置版本 bump，agent 60 秒内拉到新证书。
	if s.nodes.data.Nodes[0].ConfigVersion != vBefore+1 {
		t.Fatalf("续签后节点配置版本应从 %d 涨到 %d，实际 %d", vBefore, vBefore+1, s.nodes.data.Nodes[0].ConfigVersion)
	}
	// 手动模式的证书跳过自动续签；未到期的不续。
	if byRef[2].ExpiresAt != now.Add(10*24*time.Hour).Unix() {
		t.Fatalf("手动证书不应被自动续签，实际到期 %v", byRef[2].ExpiresAt)
	}
	if byRef[3].ExpiresAt < now.Add(80*24*time.Hour).Unix() {
		t.Fatalf("未到期的证书不应续签，实际到期 %v", byRef[3].ExpiresAt)
	}
}

// ---------- 8. 前端静态断言 ----------

func TestNodeCertsFrontend1412(t *testing.T) {
	htmlB, err := os.ReadFile(filepath.Join("web", "index.html"))
	if err != nil {
		t.Fatalf("读 index.html 失败：%v", err)
	}
	html := string(htmlB)
	for _, want := range []string{
		`id="node-certs-card"`, `id="node-cert-issue-form"`, `id="node-cert-domain"`,
		`id="node-cert-provider"`, `id="node-cert-manual-box"`, `id="node-certs-list"`,
		`id="node-cert-error"`, `id="nodes-ib-cert-hint"`,
		`value="cloudflare"`, `value="aliyun"`, `value="dnspod"`, `value="route53"`,
		`value="godaddy"`, `value="namecheap"`, `value="manual"`,
		`id="node-cert-manual-txt-name"`, `id="node-cert-manual-txt-value"`,
		`id="node-cert-manual-confirm"`, `id="node-cert-manual-cancel"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html 应含 %q", want)
		}
	}
	jsB, err := os.ReadFile(filepath.Join("web", "app.js"))
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	js := string(jsB)
	for _, want := range []string{
		"function loadNodeCerts(", "function nodeCertForDomain(", "function certGateNeedsCert(",
		"function syncCertGate(", "function certGateBlocks(", "function initNodeCerts(",
		"safeInit(initNodeCerts)", "return loadNodeCerts()",
		"/api/v1/node-certs", "ib-no-cert", "无证书",
		"proto === 'trojan'", // Trojan 与 HY2／TUIC／AnyTLS／TLS 同口径置灰
		"没有可用证书，先在下方节点证书里签发",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js 应含 %q", want)
		}
	}
	cssB, err := os.ReadFile(filepath.Join("web", "style.css"))
	if err != nil {
		t.Fatalf("读 style.css 失败：%v", err)
	}
	if !strings.Contains(string(cssB), ".ib-no-cert") {
		t.Error("style.css 应含 .ib-no-cert 置灰样式")
	}
}

// ---------- 9. 脱敏 ----------

func TestNodeCertsNoRealDomains1412(t *testing.T) {
	for _, p := range []string{"node_certs.go", "panel_1412_test.go",
		filepath.Join("web", "index.html"), filepath.Join("web", "app.js")} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", p, err)
		}
		for _, bad := range []string{"930327" + ".xyz", "jiankong" + ".cf", "154.37.217" + ".135", "129.146.27" + ".163", "23.129.180" + ".189"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s 里不应出现真实域名／IP %q", p, bad)
			}
		}
	}
}

// ---------- 10. 一键安装命令 wget 兜底 ----------

// TestNodeInstallCommand1412 安装命令：有 curl 用 curl，没有用 wget
// 兜底，两个都没有明说请先装 curl 或 wget、exit 1（不要静默成功、
// 不要空跑 bash）。一行可复制粘贴；两个分支的脚本 URL 与参数原样相同。
func TestNodeInstallCommand1412(t *testing.T) {
	s := newTestServer1400(t)
	req := httptest.NewRequest("GET", "http://203.0.113.1:11111/", nil)
	const tok = "join_0123456789abcdef0123456789abcdef"
	cmd := s.nodeInstallCommand(req, 7, tok)
	if strings.Contains(cmd, "\n") {
		t.Fatalf("安装命令应是一行可复制粘贴的命令，实际含换行：%q", cmd)
	}
	script := "https://raw.githubusercontent.com/nbbk/zhexuexi-node-backend/main/install-node-backend.sh"
	args := "--panel http://203.0.113.1:11111 --node-id 7 --token " + tok
	curlAt := strings.Index(cmd, "curl -fsSL "+script+" | bash -s -- "+args)
	wgetAt := strings.Index(cmd, "wget -qO- "+script+" | bash -s -- "+args)
	if curlAt == -1 {
		t.Fatalf("curl 分支不对：应含 %q，实际 %q", "curl -fsSL "+script+" | bash -s -- "+args, cmd)
	}
	if wgetAt == -1 {
		t.Fatalf("wget 分支不对：应含 %q，实际 %q", "wget -qO- "+script+" | bash -s -- "+args, cmd)
	}
	if curlAt > wgetAt {
		t.Fatalf("curl 优先：curl 分支应在 wget 分支之前，实际 %q", cmd)
	}
	// 两个分支各跑一次 bash，不多不少（无两者时不跑 bash）。
	if n := strings.Count(cmd, "| bash -s -- "); n != 2 {
		t.Fatalf("bash 应只在 curl／wget 两个分支里各跑一次，实际 %d 次：%q", n, cmd)
	}
	// 无两者：明说请先装 curl 或 wget（stderr），exit 1。
	if !strings.Contains(cmd, "echo '请先安装 curl 或 wget' >&2") {
		t.Fatalf("无 curl／wget 时应明说请先安装 curl 或 wget（stderr），实际 %q", cmd)
	}
	if !strings.Contains(cmd, "exit 1") {
		t.Fatalf("无 curl／wget 时应 exit 1，实际 %q", cmd)
	}
	// 检测写法：先查 curl、再查 wget。
	if !strings.Contains(cmd, "command -v curl") || !strings.Contains(cmd, "command -v wget") {
		t.Fatalf("应先 command -v 查 curl、再查 wget，实际 %q", cmd)
	}
	if strings.Index(cmd, "command -v curl") > strings.Index(cmd, "command -v wget") {
		t.Fatalf("应先查 curl、再查 wget，实际 %q", cmd)
	}
}
