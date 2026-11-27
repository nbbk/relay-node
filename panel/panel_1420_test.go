package main

// v1.4.20：轻量节点后端切片 10（面板侧）——订阅全链路。
//
// 覆盖：
//   - 版本断言 1.4.20。
//   - 对齐契约（~/workspace/slice10-fixtures/contract.md）：同一组固定
//     输入渲染出的三条链接与契约逐字一致，base64 订阅与契约的
//     subscription-base64.txt 逐字一致（期望串直接抄进本文件，kit
//     自包含，不读 kit 外的 fixture 文件）。
//   - sing-box JSON：REALITY 的 tls 块按 §8.2 一字不差、AnyTLS／
//     SOCKS5 的 outbound 关键字段、每节点一个 urltest 组（组名就是
//     节点名，成员是该节点的三个 outbound tag）。
//   - Clash YAML：REALITY 的 reality-opts／指纹／flow／network、
//     AnyTLS／SOCKS5 的关键字段、每节点一个 fallback 组。
//   - XHTTP 不进订阅：后端 XHTTP 入站不产生链接；粘贴进来的 XHTTP
//     链接在 renderSubContent 层被滤掉，四种格式都不进。
//   - 范围（v1.4.23 更新）：停用的入站／XHTTP／VLESS 明文不进订阅；
//     HY2／TUIC／SS／SS2022／VLESS-TLS／Trojan 都进；没 UUID 的节点
//     只出 SS／SOCKS5（UUID 门控下移到协议级）；节点指派／入站级指派／
//     停用的订阅都不进。
//   - 粘贴注册与 twin 推导：parseNodeLink 认 AnyTLS；REALITY／AnyTLS
//     链接的 SNI 不被 CertDomain 覆盖（computeTwinLinks）；真 install.sh
//     的 --twin／--door 对 AnyTLS 与 REALITY 只换地址、参数全留；
//     setLinkHost／setLinkPort／setLinkName 认 AnyTLS。
//   - HTTP 端到端：按人订阅里出现后端三条链接，通用订阅里没有后端链接。

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion1420(t *testing.T) {
	if Version != "1.4.31" {
		t.Fatalf("Version 应为 1.4.31，实际 %q", Version)
	}
}

// ---------- 对齐契约的固定输入 ----------

const (
	contract1420UUID    = "11111111-2222-3333-4444-555555555555"
	contract1420Node    = "example-node"
	contract1420Addr    = "203.0.113.1"
	contract1420Reality = "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?encryption=none&security=reality&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcdef01&sni=www.microsoft.com&fp=chrome&type=tcp&flow=xtls-rprx-vision#example-node-reality-443-A"
	contract1420AnyTLS  = "anytls://11111111-2222-3333-4444-555555555555@203.0.113.1:443?sni=example.com#example-node-anytls-443-A"
	contract1420Socks   = "socks5://user1:pass1234@203.0.113.1:1080#example-node-socks-1080-A"
	// 契约 subscription-base64.txt 的内容（base64(links.txt 三行＋末尾换行)）。
	contract1420Base64 = "dmxlc3M6Ly8xMTExMTExMS0yMjIyLTMzMzMtNDQ0NC01NTU1NTU1NTU1NTVAMjAzLjAuMTEzLjE6NDQzP2VuY3J5cHRpb249bm9uZSZzZWN1cml0eT1yZWFsaXR5JnBiaz1BQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBJnNpZD1hYmNkZWYwMSZzbmk9d3d3Lm1pY3Jvc29mdC5jb20mZnA9Y2hyb21lJnR5cGU9dGNwJmZsb3c9eHRscy1ycHJ4LXZpc2lvbiNleGFtcGxlLW5vZGUtcmVhbGl0eS00NDMtQQphbnl0bHM6Ly8xMTExMTExMS0yMjIyLTMzMzMtNDQ0NC01NTU1NTU1NTU1NTVAMjAzLjAuMTEzLjE6NDQzP3NuaT1leGFtcGxlLmNvbSNleGFtcGxlLW5vZGUtYW55dGxzLTQ0My1BCnNvY2tzNTovL3VzZXIxOnBhc3MxMjM0QDIwMy4wLjExMy4xOjEwODAjZXhhbXBsZS1ub2RlLXNvY2tzLTEwODAtQQo="
)

// contractInbounds1420 契约的三条入站（REALITY／AnyTLS／SOCKS5）。
func contractInbounds1420() []NodeInbound {
	return []NodeInbound{
		{InboundID: 1, InboundTag: "reality-443", Protocol: "vless", Transport: "reality", Security: "reality",
			Port: 443, Listen: "0.0.0.0", SNI: "www.microsoft.com",
			RealityPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			RealityShortID:   "abcdef01", RealityDest: "www.microsoft.com:443",
			RealityServerName: "www.microsoft.com", RealityFingerprint: "chrome",
			Flow: "xtls-rprx-vision", Multiplier: 1, Enabled: true},
		{InboundID: 2, InboundTag: "anytls-443", Protocol: "anytls", Transport: "tcp", Security: "tls",
			Port: 443, Listen: "0.0.0.0", SNI: "example.com", Domain: "example.com",
			Multiplier: 1, Enabled: true},
		{InboundID: 3, InboundTag: "socks-1080", Protocol: "socks", Transport: "tcp", Security: "none",
			Port: 1080, Listen: "0.0.0.0", SocksUsername: "user1", SocksPassword: "pass1234",
			Multiplier: 1, Enabled: true},
	}
}

// seedContractNode1420 建契约节点＋契约用户（SubUser，NodeUUIDs 里有该
// 节点的 UUID），返回节点 id。用户默认指派全部节点（UserIDs=nil）。
func seedContractNode1420(t *testing.T, s *Server, h http.Handler, cookie string) int64 {
	t.Helper()
	id, _ := createNode1400(t, h, cookie, contract1420Node, contract1420Addr)
	s.nodes.mu.Lock()
	n := s.nodes.getNode(id)
	n.Inbounds = contractInbounds1420()
	for i := range n.Inbounds {
		n.Inbounds[i].NodeID = id
	}
	// 新建节点默认 UserIDs 是空数组（一个不发）；契约用户要能上这个
	// 节点，须显式改成 nil（全部用户，见 nodeHasUser 的语义）。
	n.UserIDs = nil
	s.nodes.mu.Unlock()
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{
		ID: "su-1420", Name: "契约用户",
		NodeUUIDs: map[string]string{contract1420Addr: contract1420UUID},
	})
	return id
}

// ---------- 契约链接与 base64 ----------

func TestBackendSubLinksContract1420(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	seedContractNode1420(t, s, h, cookie)

	links := s.backendSubLinksForUser("subuser", "su-1420")
	if len(links) != 3 {
		t.Fatalf("契约用户应有 3 条后端链接，实际 %d：%v", len(links), links)
	}
	want := []string{contract1420Reality, contract1420AnyTLS, contract1420Socks}
	for i, w := range want {
		if links[i].Link != w {
			t.Fatalf("第 %d 条链接与契约逐字不一致：\n实际 %q\n期望 %q", i+1, links[i].Link, w)
		}
		if links[i].Name != contract1420Node+"-"+[]string{"reality-443", "anytls-443", "socks-1080"}[i]+"-A" {
			t.Fatalf("第 %d 条备注名不对：%q", i+1, links[i].Name)
		}
		if links[i].NodeName != contract1420Node {
			t.Fatalf("第 %d 条 NodeName 应为 %q，实际 %q", i+1, contract1420Node, links[i].NodeName)
		}
	}
	// base64 订阅与契约逐字一致。
	body, ctype, err := renderSubContent(links, "base64", "split")
	if err != nil || body != contract1420Base64 {
		t.Fatalf("base64 订阅与契约不一致：err=%v ctype=%s\n实际 %q\n期望 %q", err, ctype, body, contract1420Base64)
	}
	// plain 订阅就是契约的 links.txt（三行＋末尾换行）。
	body, _, err = renderSubContent(links, "plain", "split")
	if err != nil {
		t.Fatalf("plain 渲染：%v", err)
	}
	if body != contract1420Reality+"\n"+contract1420AnyTLS+"\n"+contract1420Socks+"\n" {
		t.Fatalf("plain 订阅与契约不一致：\n%s", body)
	}
}

// ---------- sing-box JSON ----------

// singboxOutbounds1420 解析 sing-box 订阅 JSON，返回 outbounds（按 tag 索引）。
func singboxOutbounds1420(t *testing.T, links []subLink) map[string]map[string]any {
	t.Helper()
	body, _, err := renderSubContent(links, "singbox", "split")
	if err != nil {
		t.Fatalf("singbox 渲染：%v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("singbox JSON 解析失败：%v：%s", err, body)
	}
	obs, _ := cfg["outbounds"].([]any)
	out := map[string]map[string]any{}
	for _, o := range obs {
		m, _ := o.(map[string]any)
		if tag, _ := m["tag"].(string); tag != "" {
			out[tag] = m
		}
	}
	return out
}

func TestSingboxRender1420(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	seedContractNode1420(t, s, h, cookie)
	links := s.backendSubLinksForUser("subuser", "su-1420")
	obs := singboxOutbounds1420(t, links)

	// REALITY：tls 块按 §8.2 一字不差。
	r, ok := obs["example-node-reality-443-A"]
	if !ok {
		t.Fatalf("sing-box 里没有 example-node-reality-443-A：%v", obs)
	}
	if r["type"] != "vless" || r["server"] != "203.0.113.1" || r["server_port"] != float64(443) ||
		r["uuid"] != contract1420UUID || r["flow"] != "xtls-rprx-vision" {
		t.Fatalf("REALITY outbound 基本字段不对：%v", r)
	}
	tls, _ := r["tls"].(map[string]any)
	if tls == nil || tls["enabled"] != true || tls["server_name"] != "www.microsoft.com" {
		t.Fatalf("REALITY tls 块不对：%v", tls)
	}
	utls, _ := tls["utls"].(map[string]any)
	if utls == nil || utls["enabled"] != true || utls["fingerprint"] != "chrome" {
		t.Fatalf("REALITY utls 块不对：%v", utls)
	}
	real, _ := tls["reality"].(map[string]any)
	if real == nil || real["enabled"] != true ||
		real["public_key"] != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" || real["short_id"] != "abcdef01" {
		t.Fatalf("REALITY reality 块不对：%v", real)
	}
	if _, hasTransport := r["transport"]; hasTransport {
		t.Fatalf("REALITY 是 TCP 承载，不应有 transport：%v", r)
	}
	// AnyTLS。
	a, ok := obs["example-node-anytls-443-A"]
	if !ok {
		t.Fatalf("sing-box 里没有 example-node-anytls-443-A")
	}
	if a["type"] != "anytls" || a["password"] != contract1420UUID {
		t.Fatalf("AnyTLS outbound 不对：%v", a)
	}
	atls, _ := a["tls"].(map[string]any)
	if atls == nil || atls["enabled"] != true || atls["server_name"] != "example.com" {
		t.Fatalf("AnyTLS tls 块不对：%v", atls)
	}
	// SOCKS5。
	sk, ok := obs["example-node-socks-1080-A"]
	if !ok {
		t.Fatalf("sing-box 里没有 example-node-socks-1080-A")
	}
	if sk["type"] != "socks" || sk["version"] != "5" || sk["server_port"] != float64(1080) ||
		sk["username"] != "user1" || sk["password"] != "pass1234" {
		t.Fatalf("SOCKS5 outbound 不对：%v", sk)
	}
	// 每节点一个 urltest 组：组名就是节点名，成员是三个 outbound tag。
	g, ok := obs["example-node"]
	if !ok {
		t.Fatalf("sing-box 里没有 example-node urltest 组")
	}
	if g["type"] != "urltest" {
		t.Fatalf("example-node 组应是 urltest，实际 %v", g["type"])
	}
	members, _ := g["outbounds"].([]any)
	want := []string{"example-node-reality-443-A", "example-node-anytls-443-A", "example-node-socks-1080-A"}
	if len(members) != 3 {
		t.Fatalf("example-node 组成员应为 3 个，实际 %v", members)
	}
	for i, w := range want {
		if members[i] != w {
			t.Fatalf("example-node 组成员 %d 应为 %q，实际 %v", i, w, members[i])
		}
	}
	// 全局组仍在：Proxy selector 与自动选择 urltest 含全部三个节点。
	proxy := obs["Proxy"]
	if proxy == nil || proxy["type"] != "selector" {
		t.Fatalf("全局 Proxy selector 不在：%v", obs)
	}
	auto := obs["自动选择"]
	if auto == nil || auto["type"] != "urltest" {
		t.Fatalf("全局自动选择 urltest 不在")
	}
}

// ---------- Clash YAML ----------

func TestClashRender1420(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	seedContractNode1420(t, s, h, cookie)
	links := s.backendSubLinksForUser("subuser", "su-1420")
	body, _, err := renderSubContent(links, "clash", "split")
	if err != nil {
		t.Fatalf("clash 渲染：%v", err)
	}
	for _, want := range []string{
		`name: "example-node-reality-443-A"`, "type: vless", "tls: true",
		`servername: "www.microsoft.com"`, `fingerprint: "chrome"`, `network: "tcp"`,
		`flow: "xtls-rprx-vision"`, "reality-opts:",
		`public-key: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"`, `short-id: "abcdef01"`,
		`name: "example-node-anytls-443-A"`, "type: anytls",
		`password: "11111111-2222-3333-4444-555555555555"`, `sni: "example.com"`,
		`name: "example-node-socks-1080-A"`, "type: socks5",
		`username: "user1"`, `password: "pass1234"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Clash YAML 缺 %q：\n%s", want, body)
		}
	}
	// 每节点一个 fallback 组：组名就是节点名，成员是三个 proxy 名。
	fb := strings.Index(body, `name: "example-node"`)
	if fb < 0 {
		t.Fatalf("Clash 里没有 example-node fallback 组：\n%s", body)
	}
	seg := body[fb:]
	if !strings.Contains(seg, "type: fallback") {
		t.Fatalf("example-node 组应是 fallback：\n%s", seg)
	}
	for _, m := range []string{`"example-node-reality-443-A"`, `"example-node-anytls-443-A"`, `"example-node-socks-1080-A"`} {
		if !strings.Contains(seg, "- "+m) {
			t.Fatalf("example-node fallback 组成员缺 %s：\n%s", m, seg)
		}
	}
}

// ---------- XHTTP 不进订阅 ----------

func TestXHTTPFiltered1420(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id := seedContractNode1420(t, s, h, cookie)
	// 再加一条 XHTTP 入站（与 reality-443 同一个 REALITY 密钥对，端口 8443）。
	s.nodes.mu.Lock()
	n := s.nodes.getNode(id)
	n.Inbounds = append(n.Inbounds, NodeInbound{
		InboundID: 4, InboundTag: "xhttp-443", Protocol: "vless", Transport: "xhttp", Security: "reality",
		Port: 8443, Listen: "0.0.0.0", SNI: "www.microsoft.com",
		RealityPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		RealityShortID:   "abcdef01", RealityFingerprint: "chrome",
		Flow: "xtls-rprx-vision", Multiplier: 1, Enabled: true,
	})
	s.nodes.mu.Unlock()

	links := s.backendSubLinksForUser("subuser", "su-1420")
	if len(links) != 3 {
		t.Fatalf("XHTTP 入站不应产生链接，应仍是 3 条，实际 %d：%v", len(links), links)
	}
	for _, l := range links {
		if strings.Contains(l.Link, "xhttp") || strings.Contains(l.Name, "xhttp") {
			t.Fatalf("后端链接里出现了 XHTTP：%v", l)
		}
	}
	// 第二道防线：粘贴进来的 XHTTP 链接在 renderSubContent 层被滤掉，
	// 四种格式里都不出现。
	xhttpLink := "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:8443?encryption=none&security=reality&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcdef01&sni=www.microsoft.com&fp=chrome&type=xhttp&flow=xtls-rprx-vision#xhttp-node"
	mixed := []subLink{
		{Name: "xhttp-node", Link: xhttpLink},
		{Name: "example-node-reality-443-A", Link: contract1420Reality},
	}
	for _, format := range []string{"plain", "base64", "singbox", "clash"} {
		body, _, err := renderSubContent(mixed, format, "split")
		if err != nil {
			t.Fatalf("%s 渲染：%v", format, err)
		}
		if format == "base64" {
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(body))
			body = string(raw)
		}
		if strings.Contains(body, "xhttp") {
			t.Fatalf("%s 订阅里出现了 XHTTP：\n%s", format, body)
		}
		if !strings.Contains(body, "reality-443") {
			t.Fatalf("%s 订阅里 REALITY 链接被误滤了：\n%s", format, body)
		}
	}
}

// ---------- 范围：哪些后端入站不进订阅 ----------

func TestBackendSubLinksScope1420(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id := seedContractNode1420(t, s, h, cookie)
	s.nodes.mu.Lock()
	n := s.nodes.getNode(id)
	// 停用的 REALITY 一条仍不进；HY2、TUIC、SS、VLESS-TLS 各一条：
	// v1.4.23 起都进订阅（共 3＋4＝7 条）。
	n.Inbounds = append(n.Inbounds,
		NodeInbound{InboundID: 10, InboundTag: "reality-off", Protocol: "vless", Transport: "reality", Security: "reality",
			Port: 4443, SNI: "www.microsoft.com", RealityPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			RealityShortID: "abcdef01", RealityFingerprint: "chrome", Flow: "xtls-rprx-vision", Enabled: false},
		NodeInbound{InboundID: 11, InboundTag: "hy2-443", Protocol: "hysteria2", Transport: "quic", Security: "tls",
			Port: 443, SNI: "example.com", Domain: "example.com", Enabled: true},
		NodeInbound{InboundID: 12, InboundTag: "tuic-443", Protocol: "tuic", Transport: "quic", Security: "tls",
			Port: 443, SNI: "example.com", Domain: "example.com", Enabled: true},
		NodeInbound{InboundID: 13, InboundTag: "ss-8388", Protocol: "shadowsocks", Transport: "tcp", Security: "none",
			Port: 8388, SSMethod: "aes-256-gcm", SSPassword: "secret", Enabled: true},
		NodeInbound{InboundID: 14, InboundTag: "tls-443", Protocol: "vless", Transport: "tcp", Security: "tls",
			Port: 8443, SNI: "example.com", Domain: "example.com", Enabled: true},
	)
	s.nodes.mu.Unlock()
	links := s.backendSubLinksForUser("subuser", "su-1420")
	if len(links) != 7 {
		t.Fatalf("停用的入站不应进订阅，其余 4 条新协议应进，应是 7 条，实际 %d：%v", len(links), links)
	}
	for _, name := range []string{"example-node-hy2-443-A", "example-node-tuic-443-A", "example-node-ss-8388-A", "example-node-tls-443-A"} {
		found := false
		for _, l := range links {
			if l.Name == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("订阅里应有 %s，实际 %v", name, links)
		}
	}
	// 没 UUID 的节点：UUID 制协议跳过，只出 SOCKS5 一条（契约入站里
	// 唯一的共享凭证协议；v1.4.23 的协议级门控）。
	s.nodes.mu.Lock()
	n2 := &BackendNode{ID: 99, Name: "no-uuid-node", Address: "203.0.113.99", Inbounds: contractInbounds1420()}
	s.nodes.data.Nodes = append(s.nodes.data.Nodes, n2)
	s.nodes.mu.Unlock()
	links = s.backendSubLinksForUser("subuser", "su-1420")
	if len(links) != 8 {
		t.Fatalf("没 UUID 的节点应只出 SOCKS5 一条，应是 7＋1＝8 条，实际 %d：%v", len(links), links)
	}
	// 撤掉无 UUID 的节点，后面的指派检查只看主节点。
	s.nodes.mu.Lock()
	kept := s.nodes.data.Nodes[:0]
	for _, nd := range s.nodes.data.Nodes {
		if nd.ID != 99 {
			kept = append(kept, nd)
		}
	}
	s.nodes.data.Nodes = kept
	s.nodes.mu.Unlock()
	// 节点指派：只指派给别人时，该用户一条都拿不到。
	s.nodes.mu.Lock()
	n.UserIDs = []string{"subuser:su-other"}
	s.nodes.mu.Unlock()
	if links := s.backendSubLinksForUser("subuser", "su-1420"); len(links) != 0 {
		t.Fatalf("节点没指派给该用户时应 0 条，实际 %d：%v", len(links), links)
	}
	// 入站级指派：只指派 reality-443 时只出一条。
	s.nodes.mu.Lock()
	n.UserIDs = nil
	s.nodes.mu.Unlock()
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].ID == "su-1420" {
			s.cfg.SubUsers[i].BackendScope = "inbound"
			s.cfg.SubUsers[i].BackendTargets = []string{itoa1400(id) + ":reality-443"}
		}
	}
	links = s.backendSubLinksForUser("subuser", "su-1420")
	if len(links) != 1 || links[0].Link != contract1420Reality {
		t.Fatalf("入站级指派应只出 REALITY 一条，实际 %v", links)
	}
	// 停用的订阅不进。
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].ID == "su-1420" {
			s.cfg.SubUsers[i].BackendScope = ""
			s.cfg.SubUsers[i].BackendTargets = nil
			s.cfg.SubUsers[i].Disabled = true
		}
	}
	if links := s.backendSubLinksForUser("subuser", "su-1420"); len(links) != 0 {
		t.Fatalf("停用的订阅应 0 条，实际 %d", len(links))
	}
}

// ---------- 粘贴注册：parseNodeLink 认 AnyTLS ----------

func TestParseNodeLinkAnyTLS1420(t *testing.T) {
	pl, err := parseNodeLink(contract1420AnyTLS)
	if err != nil {
		t.Fatalf("AnyTLS 链接应解析通过：%v", err)
	}
	if pl.Scheme != "anytls" || pl.Host != "203.0.113.1" || pl.Port != 443 ||
		pl.SNI != "example.com" || pl.Name != "example-node-anytls-443-A" {
		t.Fatalf("AnyTLS 解析结果不对：%+v", pl)
	}
	// 没写 sni 时 SNI 回退到域名 host。
	pl2, err := parseNodeLink("anytls://11111111-2222-3333-4444-555555555555@example.com:443#x")
	if err != nil || pl2.SNI != "example.com" {
		t.Fatalf("AnyTLS 无 sni 时应回退到 host 域名：%+v err=%v", pl2, err)
	}
	// REALITY 链接照常解析（vless 本来就认），参数齐全。
	pl3, err := parseNodeLink(contract1420Reality)
	if err != nil || pl3.SNI != "www.microsoft.com" {
		t.Fatalf("REALITY 链接解析不对：%+v err=%v", pl3, err)
	}
	// SOCKS5 不进粘贴／twin（本切片范围：粘贴注册只补 REALITY／AnyTLS）。
	if _, err := parseNodeLink(contract1420Socks); err == nil {
		t.Fatalf("SOCKS5 链接粘贴注册应拒绝（不在本切片粘贴范围）")
	}
}

// ---------- twin：REALITY／AnyTLS 的 SNI 不被 CertDomain 覆盖 ----------

func TestTwinOwnSNI1420(t *testing.T) {
	if got := twinOwnSNI(contract1420Reality); got != "www.microsoft.com" {
		t.Fatalf("REALITY 链接自带 SNI 应为 www.microsoft.com，实际 %q", got)
	}
	if got := twinOwnSNI(contract1420AnyTLS); got != "example.com" {
		t.Fatalf("AnyTLS 链接自带 SNI 应为 example.com，实际 %q", got)
	}
	if got := twinOwnSNI("vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#x"); got != "" {
		t.Fatalf("VLESS-TLS 链接不应有自带 SNI 保护，实际 %q", got)
	}
	if got := twinOwnSNI("trojan://pw@example.com:443?sni=example.com#x"); got != "" {
		t.Fatalf("Trojan 链接不应有自带 SNI 保护，实际 %q", got)
	}
}

// realInstallSh1420 把 kit 真实的 install.sh 拷进临时 KitDir（twin 测试
// 不用 stub，直接跑 --twin／--door 的真逻辑）。
func realInstallSh1420(t *testing.T) string {
	t.Helper()
	kitDir := t.TempDir()
	raw, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Fatalf("读 kit 的 install.sh（../install.sh）：%v", err)
	}
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), raw, 0o755); err != nil {
		t.Fatal(err)
	}
	return kitDir
}

func TestComputeTwinRealitySNI1420(t *testing.T) {
	kitDir := realInstallSh1420(t)
	s := newTestAddLandingServer(t, kitDir, t.TempDir())
	// 模拟加落地／补粘贴传的 CertDomain（cert.example.com）：REALITY
	// 的直连 twin 里 sni 必须仍是链接自己的 www.microsoft.com。
	relay, direct, _, manual := s.computeTwinLinks(contract1420Reality, contract1420Addr, "cert.example.com", nil, nil, "")
	if manual != "" {
		t.Fatalf("REALITY twin 不应报 manual：%s", manual)
	}
	for _, ln := range []string{relay, direct} {
		u, err := url.Parse(ln)
		if err != nil {
			t.Fatalf("twin 链接解析失败：%v：%s", err, ln)
		}
		q := u.Query()
		if q.Get("sni") != "www.microsoft.com" || q.Get("pbk") != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" ||
			q.Get("sid") != "abcdef01" || q.Get("fp") != "chrome" ||
			q.Get("flow") != "xtls-rprx-vision" || q.Get("security") != "reality" {
			t.Fatalf("twin 链接的 REALITY 参数被改了：%s", ln)
		}
	}
	du, _ := url.Parse(direct)
	if du.Hostname() != contract1420Addr || du.Fragment != "example-node-reality-443-A-直连" {
		t.Fatalf("直连 twin 应换成落地地址＋-直连备注：%s", direct)
	}
	ru, _ := url.Parse(relay)
	if ru.Hostname() != "203.0.113.1" && ru.Hostname() != "example.com" {
		t.Fatalf("中转 twin 地址不对：%s", relay)
	}
	if ru.Fragment != "example-node-reality-443-A-中转" {
		t.Fatalf("中转 twin 备注应为 -中转：%s", relay)
	}
	// 中转与直连只差地址：去掉 host 与备注后其余参数相同。
	strip := func(ln string) string {
		u, _ := url.Parse(ln)
		u.Host = ""
		u.Fragment = ""
		return u.String()
	}
	if strip(relay) != strip(direct) {
		t.Fatalf("REALITY twin 中转与直连只应差地址：\n中转 %s\n直连 %s", relay, direct)
	}
}

func TestHandleTwinAnyTLS1420(t *testing.T) {
	kitDir := realInstallSh1420(t)
	s := newTestAddLandingServer(t, kitDir, t.TempDir())
	code, resp := postTwin(t, s, `{"link":"`+contract1420AnyTLS+`","landing_ip":"203.0.113.1"}`)
	if code != 200 {
		t.Fatalf("AnyTLS twin 应 200，实际 %d：%v", code, resp)
	}
	if resp["manual"] != nil && resp["manual"] != "" {
		t.Fatalf("AnyTLS twin 不应报 manual：%v", resp)
	}
	dl, _ := resp["direct_link"].(string)
	du, err := url.Parse(dl)
	if err != nil {
		t.Fatalf("直连 twin 解析失败：%v：%s", err, dl)
	}
	if du.Hostname() != "203.0.113.1" || du.Query().Get("sni") != "example.com" ||
		du.Fragment != "example-node-anytls-443-A-直连" {
		t.Fatalf("AnyTLS 直连 twin 不对：%s", dl)
	}
	rl, _ := resp["relay_link"].(string)
	ru, _ := url.Parse(rl)
	if ru.Fragment != "example-node-anytls-443-A-中转" || ru.Query().Get("sni") != "example.com" {
		t.Fatalf("AnyTLS 中转 twin 不对：%s", rl)
	}
}

// ---------- setLink* 认 AnyTLS ----------

func TestSetLinkAnyTLS1420(t *testing.T) {
	if got := setLinkHost(contract1420AnyTLS, "203.0.113.9"); !strings.Contains(got, "@203.0.113.9:443") || !strings.Contains(got, "sni=example.com") {
		t.Fatalf("setLinkHost(AnyTLS) 不对：%s", got)
	}
	if got := setLinkPort(contract1420AnyTLS, 20443); !strings.Contains(got, "@203.0.113.1:20443") {
		t.Fatalf("setLinkPort(AnyTLS) 不对：%s", got)
	}
	if got := setLinkName(contract1420AnyTLS, "x-中转"); !strings.HasSuffix(got, "#x-%E4%B8%AD%E8%BD%AC") {
		t.Fatalf("setLinkName(AnyTLS) 不对：%s", got)
	}
}

// ---------- HTTP 端到端：按人订阅含后端链接，通用订阅不含 ----------

func TestServeSubBackend1420(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	seedContractNode1420(t, s, h, cookie)
	// 按人订阅令牌。
	tok := generateSubToken()
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].ID == "su-1420" {
			s.cfg.SubUsers[i].TokenHash = hashSubToken(tok)
		}
	}
	rec := callSub(t, s, tok, "/sub/"+tok+"?format=plain", "")
	if rec.Code != 200 {
		t.Fatalf("按人订阅应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{contract1420Reality, contract1420AnyTLS, contract1420Socks} {
		if !strings.Contains(body, want) {
			t.Fatalf("按人订阅 plain 里缺后端链接 %q：\n%s", want, body)
		}
	}
	// 1.4.27：通用订阅已退场，旧通用令牌直接 410（后端链接只进按人
	// 订阅，通用订阅本来就没有用户身份、一条都不进）。
	gtok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(gtok)
	rec = callSub(t, s, gtok, "/sub/"+gtok+"?format=plain", "")
	if rec.Code != 410 {
		t.Fatalf("通用订阅应 410（已退场），实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 按人订阅的 singbox 与 clash 里同样有后端链接与分组。
	rec = callSub(t, s, tok, "/sub/"+tok+"?format=singbox", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tag": "example-node"`) {
		t.Fatalf("按人订阅 singbox 里应有 example-node urltest 组：%d %s", rec.Code, rec.Body.String())
	}
	rec = callSub(t, s, tok, "/sub/"+tok+"?format=clash", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "type: fallback") {
		t.Fatalf("按人订阅 clash 里应有 fallback 组：%d %s", rec.Code, rec.Body.String())
	}
}
