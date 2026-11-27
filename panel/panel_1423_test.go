package main

// v1.4.23：后端入站链接扩展到全部协议（VLESS-TLS／HY2／TUIC／
// SS／SS2022／Trojan 进订阅与单条链接，订阅与单条共用同一套拼法）。
//
// 覆盖：
//   - 版本断言 1.4.23。
//   - backendInboundLinkable：5 种新协议齐全时可出链接；缺 SNI／
//     缺 SS 方法密码／SS2022 密码长度不对／停用时不可并给直白原因；
//     XHTTP／VLESS 明文仍不可；backendInboundNeedsUUID 只对 6 种
//     UUID 制协议要求 UUID。
//   - 链接格式逐字断言：VLESS-TLS（security=tls＋type=tcp）、HY2
//     （alpn=h3，不带 obfs）、TUIC（uuid:uuid＋bbr＋h3）、SS／
//     SS2022（整体 base64url 的 SIP002，不带 query）、Trojan
//     （security=tls＋type=tcp）。
//   - UUID 门控下移：没 UUID 的用户照常拿到 SS／SS2022／SOCKS5
//     三条链接，UUID 制协议的入站跳过；单条链接接口同样（SS 没
//     UUID 也 200，REALITY 没 UUID 仍 400）。
//   - 订阅四种格式渲染：plain／base64 里 9 条链接全在；sing-box
//     JSON 里 HY2／TUIC／SS／SS2022／Trojan／VLESS-TLS 的 outbound
//     字段全对；Clash YAML 里对应 proxy 全对；同节点 urltest／
//     fallback 组含 9 个成员。
//   - 单条链接接口：5 种新协议的链接与订阅逐字一致＋二维码 data URI。

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func TestVersion1423(t *testing.T) {
	if Version != "1.4.32" {
		t.Fatalf("Version 应为 1.4.32，实际 %q", Version)
	}
}

// ---------- 1.4.23 的契约：9 条入站（1.4.20 的 3 条＋新 6 条） ----------

const (
	// 32 字节全 0 密钥的 base64（SS2022 密码，2022-blake3-aes-256-gcm 要 32 字节）。
	contract1423SS2022Password = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	contract1423VLESS_TLS      = "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:8444?encryption=none&security=tls&sni=example.com&type=tcp#example-node-tls-8444-A"
	contract1423HY2            = "hysteria2://11111111-2222-3333-4444-555555555555@203.0.113.1:443?sni=example.com&alpn=h3#example-node-hy2-443-A"
	contract1423TUIC           = "tuic://11111111-2222-3333-4444-555555555555:11111111-2222-3333-4444-555555555555@203.0.113.1:4443?congestion_control=bbr&alpn=h3&sni=example.com#example-node-tuic-4443-A"
	contract1423SS             = "ss://YWVzLTI1Ni1nY206c2VjcmV0@203.0.113.1:8388#example-node-ss-8388-A"
	contract1423SS2022         = "ss://MjAyMi1ibGFrZTMtYWVzLTI1Ni1nY206QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQT0@203.0.113.1:8389#example-node-ss2022-8389-A"
	contract1423Trojan         = "trojan://11111111-2222-3333-4444-555555555555@203.0.113.1:8443?security=tls&sni=example.com&type=tcp#example-node-trojan-8443-A"
)

// contractInbounds1423 契约的 9 条入站（顺序即订阅里的顺序）。
func contractInbounds1423() []NodeInbound {
	out := contractInbounds1420()
	out = append(out,
		NodeInbound{InboundID: 4, InboundTag: "tls-8444", Protocol: "vless", Transport: "tcp", Security: "tls",
			Port: 8444, Listen: "0.0.0.0", SNI: "example.com", Domain: "example.com",
			Multiplier: 1, Enabled: true},
		NodeInbound{InboundID: 5, InboundTag: "hy2-443", Protocol: "hysteria2", Transport: "quic", Security: "tls",
			Port: 443, Listen: "0.0.0.0", SNI: "example.com", Domain: "example.com",
			Multiplier: 1, Enabled: true},
		NodeInbound{InboundID: 6, InboundTag: "tuic-4443", Protocol: "tuic", Transport: "quic", Security: "tls",
			Port: 4443, Listen: "0.0.0.0", SNI: "example.com", Domain: "example.com",
			Multiplier: 1, Enabled: true},
		NodeInbound{InboundID: 7, InboundTag: "ss-8388", Protocol: "shadowsocks", Transport: "tcp", Security: "none",
			Port: 8388, Listen: "0.0.0.0", SSMethod: "aes-256-gcm", SSPassword: "secret",
			Multiplier: 1, Enabled: true},
		NodeInbound{InboundID: 8, InboundTag: "ss2022-8389", Protocol: "shadowsocks", Transport: "tcp", Security: "none",
			Port: 8389, Listen: "0.0.0.0", SSMethod: "2022-blake3-aes-256-gcm", SSPassword: contract1423SS2022Password,
			Multiplier: 1, Enabled: true},
		NodeInbound{InboundID: 9, InboundTag: "trojan-8443", Protocol: "trojan", Transport: "tcp", Security: "tls",
			Port: 8443, Listen: "0.0.0.0", SNI: "example.com", Domain: "example.com",
			Multiplier: 1, Enabled: true},
	)
	return out
}

// seedContractNode1423 建契约节点＋契约用户（9 条入站，su-1420 有 UUID）。
func seedContractNode1423(t *testing.T, s *Server, h http.Handler, cookie string) int64 {
	t.Helper()
	id := seedContractNode1420(t, s, h, cookie)
	s.nodes.mu.Lock()
	n := s.nodes.getNode(id)
	n.Inbounds = contractInbounds1423()
	for i := range n.Inbounds {
		n.Inbounds[i].NodeID = id
	}
	s.nodes.mu.Unlock()
	return id
}

// contractLinks1423 9 条链接的期望顺序（与入站顺序一致）。
func contractLinks1423() []string {
	return []string{
		contract1420Reality, contract1420AnyTLS, contract1420Socks,
		contract1423VLESS_TLS, contract1423HY2, contract1423TUIC,
		contract1423SS, contract1423SS2022, contract1423Trojan,
	}
}

// ---------- backendInboundLinkable：5 种新协议 ----------

func TestBackendInboundLinkable1423(t *testing.T) {
	// 齐全时都可出链接。
	linkables := []NodeInbound{
		{InboundTag: "tls-8444", Protocol: "vless", Transport: "tcp", Security: "tls", Port: 8444, SNI: "example.com", Enabled: true},
		{InboundTag: "hy2-443", Protocol: "hysteria2", Transport: "quic", Security: "tls", Port: 443, SNI: "example.com", Enabled: true},
		{InboundTag: "tuic-4443", Protocol: "tuic", Transport: "quic", Security: "tls", Port: 4443, SNI: "example.com", Enabled: true},
		{InboundTag: "ss-8388", Protocol: "shadowsocks", Transport: "tcp", Security: "none", Port: 8388, SSMethod: "aes-256-gcm", SSPassword: "secret", Enabled: true},
		{InboundTag: "ss2022-8389", Protocol: "shadowsocks", Transport: "tcp", Security: "none", Port: 8389, SSMethod: "2022-blake3-aes-256-gcm", SSPassword: contract1423SS2022Password, Enabled: true},
		{InboundTag: "trojan-8443", Protocol: "trojan", Transport: "tcp", Security: "tls", Port: 8443, SNI: "example.com", Enabled: true},
	}
	for _, in := range linkables {
		if ok, reason := backendInboundLinkable(in); !ok {
			t.Fatalf("%s 齐全时应可出单条链接，实际不可：%q", in.InboundTag, reason)
		}
	}
	// 缺 SNI：VLESS-TLS／HY2／TUIC／Trojan 都不可并说明 SNI。
	for _, in := range []NodeInbound{
		{InboundTag: "tls-8444", Protocol: "vless", Transport: "tcp", Security: "tls", Port: 8444, Enabled: true},
		{InboundTag: "hy2-443", Protocol: "hysteria2", Transport: "quic", Security: "tls", Port: 443, Enabled: true},
		{InboundTag: "tuic-4443", Protocol: "tuic", Transport: "quic", Security: "tls", Port: 4443, Enabled: true},
		{InboundTag: "trojan-8443", Protocol: "trojan", Transport: "tcp", Security: "tls", Port: 8443, Enabled: true},
	} {
		if ok, reason := backendInboundLinkable(in); ok || !strings.Contains(reason, "SNI") {
			t.Fatalf("%s 缺 SNI 时应不可并说明 SNI，实际 ok=%v reason=%q", in.InboundTag, ok, reason)
		}
	}
	// SS 缺方法／缺密码。
	ssNoMethod := NodeInbound{InboundTag: "ss-8388", Protocol: "shadowsocks", Transport: "tcp", Security: "none", Port: 8388, SSPassword: "secret", Enabled: true}
	if ok, reason := backendInboundLinkable(ssNoMethod); ok || !strings.Contains(reason, "SS") {
		t.Fatalf("SS 缺方法时应不可并说明，实际 ok=%v reason=%q", ok, reason)
	}
	ssNoPw := NodeInbound{InboundTag: "ss-8388", Protocol: "shadowsocks", Transport: "tcp", Security: "none", Port: 8388, SSMethod: "aes-256-gcm", Enabled: true}
	if ok, _ := backendInboundLinkable(ssNoPw); ok {
		t.Fatalf("SS 缺密码时应不可")
	}
	// SS2022 密码长度不对（16 字节密钥当 256-gcm 用）。
	ss2022Bad := NodeInbound{InboundTag: "ss2022-8389", Protocol: "shadowsocks", Transport: "tcp", Security: "none", Port: 8389,
		SSMethod: "2022-blake3-aes-256-gcm", SSPassword: base64.StdEncoding.EncodeToString(make([]byte, 16)), Enabled: true}
	if ok, reason := backendInboundLinkable(ss2022Bad); ok || !strings.Contains(reason, "长度") {
		t.Fatalf("SS2022 密码长度不对时应不可并说明长度，实际 ok=%v reason=%q", ok, reason)
	}
	// 停用的 HY2 不可。
	off := linkables[1]
	off.Enabled = false
	if ok, reason := backendInboundLinkable(off); ok || !strings.Contains(reason, "停用") {
		t.Fatalf("停用的 HY2 应不可并说明停用，实际 ok=%v reason=%q", ok, reason)
	}
	// 仍不可：XHTTP 与 VLESS 明文。
	for _, in := range []NodeInbound{
		{InboundTag: "xhttp-443", Protocol: "vless", Transport: "xhttp", Security: "reality", Port: 8443, SNI: "www.microsoft.com", Enabled: true},
		{InboundTag: "plain-8080", Protocol: "vless", Transport: "tcp", Security: "none", Port: 8080, Enabled: true},
	} {
		if ok, _ := backendInboundLinkable(in); ok {
			t.Fatalf("%s 应仍不可出单条链接，实际可", in.InboundTag)
		}
	}
	// backendInboundNeedsUUID：6 种 UUID 制协议要，SS／SOCKS5 不要。
	for _, in := range []NodeInbound{
		{Protocol: "vless", Transport: "reality", Security: "reality"},
		{Protocol: "vless", Transport: "tcp", Security: "tls"},
		{Protocol: "anytls"},
		{Protocol: "hysteria2"},
		{Protocol: "tuic"},
		{Protocol: "trojan"},
	} {
		if !backendInboundNeedsUUID(in) {
			t.Fatalf("%s 应要求 UUID，实际不要", in.Protocol+"/"+in.Transport+"/"+in.Security)
		}
	}
	for _, in := range []NodeInbound{
		{Protocol: "shadowsocks"},
		{Protocol: "socks"},
		{Protocol: "vless", Transport: "xhttp", Security: "reality"},
	} {
		if backendInboundNeedsUUID(in) {
			t.Fatalf("%s 不应要求 UUID，实际要求", in.Protocol)
		}
	}
}

// ---------- 链接格式逐字断言（9 条） ----------

func TestBackendSubLinksContract1423(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	seedContractNode1423(t, s, h, cookie)

	links := s.backendSubLinksForUser("subuser", "su-1420")
	if len(links) != 9 {
		t.Fatalf("契约用户应有 9 条后端链接，实际 %d：%v", len(links), links)
	}
	for i, w := range contractLinks1423() {
		if links[i].Link != w {
			t.Fatalf("第 %d 条链接与契约逐字不一致：\n实际 %q\n期望 %q", i+1, links[i].Link, w)
		}
	}
	// SS 链接：userinfo 整体是 base64url（不含 +/=，不含明文密码），
	// 且 parseSSLink 能解回方法与密码；不带任何 query 参数。
	for _, l := range links {
		if strings.HasPrefix(l.Link, "ss://") {
			if strings.Contains(l.Link, "?") {
				t.Fatalf("SS 链接不应带 query 参数：%s", l.Link)
			}
			if strings.Contains(l.Link, "secret") || strings.Contains(l.Link, contract1423SS2022Password) {
				t.Fatalf("SS 链接里不应出现明文密码：%s", l.Link)
			}
			cipher, pw, host, port, ok := parseSSLink(l.Link)
			if !ok {
				t.Fatalf("SS 链接应能被 parseSSLink 解开：%s", l.Link)
			}
			if host != "203.0.113.1" || port <= 0 {
				t.Fatalf("SS 链接地址不对：%s → %s:%d", l.Link, host, port)
			}
			if cipher == "aes-256-gcm" && pw != "secret" {
				t.Fatalf("SS 密码解回不对：%q", pw)
			}
			if cipher == "2022-blake3-aes-256-gcm" && pw != contract1423SS2022Password {
				t.Fatalf("SS2022 密码解回不对：%q", pw)
			}
		}
	}
	// VLESS-TLS／Trojan 必须带 security=tls 与 type=tcp（否则渲染器与
	// 客户端解析器都默认当 WS）；HY2 必须带 alpn=h3 且不带 obfs；
	// TUIC 必须带 congestion_control=bbr 与 alpn=h3，uuid 与密码相同。
	for _, l := range links {
		switch {
		case strings.HasPrefix(l.Link, "vless://") && strings.Contains(l.Link, "security=tls"):
			if !strings.Contains(l.Link, "type=tcp") {
				t.Fatalf("VLESS-TLS 链接缺 type=tcp：%s", l.Link)
			}
			if strings.Contains(l.Link, "fp=") || strings.Contains(l.Link, "alpn=") {
				t.Fatalf("VLESS-TLS 链接不应带 fp/alpn：%s", l.Link)
			}
		case strings.HasPrefix(l.Link, "hysteria2://"):
			if !strings.Contains(l.Link, "alpn=h3") {
				t.Fatalf("HY2 链接缺 alpn=h3：%s", l.Link)
			}
			if strings.Contains(l.Link, "obfs") {
				t.Fatalf("HY2 链接不应带 obfs：%s", l.Link)
			}
		case strings.HasPrefix(l.Link, "tuic://"):
			if !strings.Contains(l.Link, "congestion_control=bbr") || !strings.Contains(l.Link, "alpn=h3") {
				t.Fatalf("TUIC 链接缺 congestion_control=bbr 或 alpn=h3：%s", l.Link)
			}
		case strings.HasPrefix(l.Link, "trojan://"):
			if !strings.Contains(l.Link, "security=tls") || !strings.Contains(l.Link, "type=tcp") {
				t.Fatalf("Trojan 链接缺 security=tls 或 type=tcp：%s", l.Link)
			}
		}
	}
}

// ---------- UUID 门控下移 ----------

func TestBackendSubLinksNoUUID1423(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	seedContractNode1423(t, s, h, cookie)
	// 没 UUID 的用户：只拿到 SS／SS2022／SOCKS5 三条（入站级共享凭证）。
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{
		ID: "su-no-uuid", Name: "没 UUID 的用户",
		NodeUUIDs: map[string]string{},
	})
	links := s.backendSubLinksForUser("subuser", "su-no-uuid")
	if len(links) != 3 {
		t.Fatalf("没 UUID 的用户应只拿到 SS／SS2022／SOCKS5 三条，实际 %d：%v", len(links), links)
	}
	for _, l := range links {
		if !strings.HasPrefix(l.Link, "ss://") && !strings.HasPrefix(l.Link, "socks5://") {
			t.Fatalf("没 UUID 的用户不应拿到 UUID 制协议的链接：%v", l)
		}
	}
	if links[0].Link != contract1420Socks || links[1].Link != contract1423SS || links[2].Link != contract1423SS2022 {
		t.Fatalf("没 UUID 用户的三条链接与契约不一致：%v", links)
	}
	// 单条链接接口：SS 没 UUID 也 200，链接与订阅逐字一致；
	// REALITY 没 UUID 仍 400 并提到 UUID。
	id := int64(0)
	s.nodes.mu.Lock()
	for _, n := range s.nodes.listNodes() {
		if n.Address == contract1420Addr {
			id = n.ID
		}
	}
	s.nodes.mu.Unlock()
	if id == 0 {
		t.Fatalf("契约节点找不到了")
	}
	code, m := getInboundLink1422(t, h, cookie, id, 7, "subuser:su-no-uuid")
	if code != 200 {
		t.Fatalf("SS 单条链接没 UUID 也应 200，实际 %d：%v", code, m)
	}
	if m["link"] != contract1423SS {
		t.Fatalf("SS 单条链接与订阅不一致：实际 %q 期望 %q", m["link"], contract1423SS)
	}
	code, m = getInboundLink1422(t, h, cookie, id, 1, "subuser:su-no-uuid")
	if code != 400 {
		t.Fatalf("REALITY 单条链接没 UUID 应 400，实际 %d：%v", code, m)
	}
	err, _ := m["error"].(string)
	if !strings.Contains(err, "UUID") {
		t.Fatalf("REALITY 没 UUID 的报错应提到 UUID，实际 %q", err)
	}
}

// ---------- 订阅四种格式渲染（9 条） ----------

func TestBackendSubLinksFourFormats1423(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	seedContractNode1423(t, s, h, cookie)
	links := s.backendSubLinksForUser("subuser", "su-1420")
	if len(links) != 9 {
		t.Fatalf("应有 9 条链接，实际 %d", len(links))
	}
	// plain：9 行，每条契约链接都在。
	body, _, err := renderSubContent(links, "plain", "split")
	if err != nil {
		t.Fatalf("plain 渲染：%v", err)
	}
	for _, w := range contractLinks1423() {
		if !strings.Contains(body, w) {
			t.Fatalf("plain 订阅里缺 %q", w)
		}
	}
	// base64：解开后是 9 行。
	body, _, err = renderSubContent(links, "base64", "split")
	if err != nil {
		t.Fatalf("base64 渲染：%v", err)
	}
	raw, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(body))
	if derr != nil {
		t.Fatalf("base64 订阅解不开：%v", derr)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 9 {
		t.Fatalf("base64 订阅解开后应 9 行，实际 %d：%q", len(lines), string(raw))
	}
	for i, w := range contractLinks1423() {
		if lines[i] != w {
			t.Fatalf("base64 第 %d 行与契约不一致：实际 %q 期望 %q", i+1, lines[i], w)
		}
	}
	// sing-box JSON：6 种新出站的字段。
	obs := singboxOutbounds1420(t, links)
	// VLESS-TLS：tls 恒开、server_name 是 SNI、TCP 承载无 transport。
	vt, ok := obs["example-node-tls-8444-A"]
	if !ok {
		t.Fatalf("sing-box 里没有 example-node-tls-8444-A")
	}
	if vt["type"] != "vless" || vt["uuid"] != contract1420UUID || vt["server_port"] != float64(8444) {
		t.Fatalf("VLESS-TLS outbound 基本字段不对：%v", vt)
	}
	tls, _ := vt["tls"].(map[string]any)
	if tls == nil || tls["enabled"] != true || tls["server_name"] != "example.com" {
		t.Fatalf("VLESS-TLS tls 块不对：%v", tls)
	}
	if _, hasTransport := vt["transport"]; hasTransport {
		t.Fatalf("VLESS-TLS 是 TCP 承载，不应有 transport：%v", vt)
	}
	// HY2：tls 恒开、server_name 是 SNI、alpn 是 [h3]。
	hy, ok := obs["example-node-hy2-443-A"]
	if !ok {
		t.Fatalf("sing-box 里没有 example-node-hy2-443-A")
	}
	if hy["type"] != "hysteria2" || hy["password"] != contract1420UUID {
		t.Fatalf("HY2 outbound 基本字段不对：%v", hy)
	}
	htls, _ := hy["tls"].(map[string]any)
	if htls == nil || htls["enabled"] != true || htls["server_name"] != "example.com" {
		t.Fatalf("HY2 tls 块不对：%v", htls)
	}
	if alpn, _ := htls["alpn"].([]any); len(alpn) != 1 || alpn[0] != "h3" {
		t.Fatalf("HY2 tls.alpn 应是 [h3]，实际 %v", htls["alpn"])
	}
	if _, hasObfs := hy["obfs"]; hasObfs {
		t.Fatalf("HY2 不应有 obfs：%v", hy)
	}
	// TUIC：uuid 与 password 都是 UUID、congestion_control 是 bbr、alpn 是 [h3]。
	tu, ok := obs["example-node-tuic-4443-A"]
	if !ok {
		t.Fatalf("sing-box 里没有 example-node-tuic-4443-A")
	}
	if tu["type"] != "tuic" || tu["uuid"] != contract1420UUID || tu["password"] != contract1420UUID ||
		tu["congestion_control"] != "bbr" {
		t.Fatalf("TUIC outbound 基本字段不对：%v", tu)
	}
	tutls, _ := tu["tls"].(map[string]any)
	if tutls == nil || tutls["enabled"] != true || tutls["server_name"] != "example.com" {
		t.Fatalf("TUIC tls 块不对：%v", tutls)
	}
	if alpn, _ := tutls["alpn"].([]any); len(alpn) != 1 || alpn[0] != "h3" {
		t.Fatalf("TUIC tls.alpn 应是 [h3]，实际 %v", tutls["alpn"])
	}
	// SS：方法与密码解回正确。
	ss, ok := obs["example-node-ss-8388-A"]
	if !ok {
		t.Fatalf("sing-box 里没有 example-node-ss-8388-A")
	}
	if ss["type"] != "shadowsocks" || ss["method"] != "aes-256-gcm" || ss["password"] != "secret" {
		t.Fatalf("SS outbound 不对：%v", ss)
	}
	ss2, ok := obs["example-node-ss2022-8389-A"]
	if !ok {
		t.Fatalf("sing-box 里没有 example-node-ss2022-8389-A")
	}
	if ss2["type"] != "shadowsocks" || ss2["method"] != "2022-blake3-aes-256-gcm" ||
		ss2["password"] != contract1423SS2022Password {
		t.Fatalf("SS2022 outbound 不对：%v", ss2)
	}
	// Trojan：密码是 UUID、tls 恒开、server_name 是 SNI。
	tj, ok := obs["example-node-trojan-8443-A"]
	if !ok {
		t.Fatalf("sing-box 里没有 example-node-trojan-8443-A")
	}
	if tj["type"] != "trojan" || tj["password"] != contract1420UUID {
		t.Fatalf("Trojan outbound 基本字段不对：%v", tj)
	}
	tjtls, _ := tj["tls"].(map[string]any)
	if tjtls == nil || tjtls["enabled"] != true || tjtls["server_name"] != "example.com" {
		t.Fatalf("Trojan tls 块不对：%v", tjtls)
	}
	// 同节点 urltest 组含 9 个成员。
	g, ok := obs["example-node"]
	if !ok || g["type"] != "urltest" {
		t.Fatalf("sing-box 里没有 example-node urltest 组：%v", obs)
	}
	members, _ := g["outbounds"].([]any)
	if len(members) != 9 {
		t.Fatalf("example-node 组成员应为 9 个，实际 %v", members)
	}
	// Clash YAML：5 种新协议的 proxy 都在，关键字段对。
	cbody, _, err := renderSubContent(links, "clash", "split")
	if err != nil {
		t.Fatalf("clash 渲染：%v", err)
	}
	for _, want := range []string{
		`name: "example-node-tls-8444-A"`, "type: vless", `servername: "example.com"`, `network: "tcp"`,
		`name: "example-node-hy2-443-A"`, "type: hysteria2", `password: "11111111-2222-3333-4444-555555555555"`, `sni: "example.com"`,
		`name: "example-node-tuic-4443-A"`, "type: tuic", `uuid: "11111111-2222-3333-4444-555555555555"`, "congestion-controller: bbr",
		`name: "example-node-ss-8388-A"`, "type: ss", `cipher: "aes-256-gcm"`, `password: "secret"`,
		`name: "example-node-ss2022-8389-A"`, `cipher: "2022-blake3-aes-256-gcm"`,
		`name: "example-node-trojan-8443-A"`, "type: trojan",
	} {
		if !strings.Contains(cbody, want) {
			t.Fatalf("Clash YAML 缺 %q：\n%s", want, cbody)
		}
	}
	// Clash fallback 组含 9 个成员。
	fb := strings.Index(cbody, `name: "example-node"`)
	if fb < 0 {
		t.Fatalf("Clash 里没有 example-node fallback 组：\n%s", cbody)
	}
	seg := cbody[fb:]
	for _, m := range []string{`"example-node-tls-8444-A"`, `"example-node-hy2-443-A"`, `"example-node-tuic-4443-A"`,
		`"example-node-ss-8388-A"`, `"example-node-ss2022-8389-A"`, `"example-node-trojan-8443-A"`} {
		if !strings.Contains(seg, "- "+m) {
			t.Fatalf("example-node fallback 组成员缺 %s：\n%s", m, seg)
		}
	}
}

// ---------- 单条链接接口：5 种新协议与订阅逐字一致 ----------

func TestInboundLinkNewProtocols1423(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id := seedContractNode1423(t, s, h, cookie)
	userKey := "subuser:su-1420"

	want := map[int64]string{
		4: contract1423VLESS_TLS,
		5: contract1423HY2,
		6: contract1423TUIC,
		7: contract1423SS,
		8: contract1423SS2022,
		9: contract1423Trojan,
	}
	subLinks := s.backendSubLinksForUser("subuser", "su-1420")
	byName := map[string]string{}
	for _, l := range subLinks {
		byName[l.Name] = l.Link
	}
	for inboundID, w := range want {
		code, m := getInboundLink1422(t, h, cookie, id, inboundID, userKey)
		if code != 200 {
			t.Fatalf("入站 %d 单条链接应 200，实际 %d：%v", inboundID, code, m)
		}
		if m["link"] != w {
			t.Fatalf("入站 %d 的链接与契约不一致：\n实际 %q\n期望 %q", inboundID, m["link"], w)
		}
		// 与订阅里同一条入站的链接逐字一致。
		name, _ := m["label"].(string)
		if byName[name] != w {
			t.Fatalf("入站 %d 单条链接与订阅不一致：单条 %q 订阅 %q", inboundID, m["link"], byName[name])
		}
		qr, _ := m["qr"].(string)
		if !strings.HasPrefix(qr, "data:image/png;base64,") {
			t.Fatalf("入站 %d 的二维码应是 data URI，实际 %q", inboundID, qr)
		}
	}
}

// ---------- 入站列表：9 条全可出链接，XHTTP 仍置灰 ----------

func TestInboundListLinkable1423(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id := seedContractNode1423(t, s, h, cookie)
	s.nodes.mu.Lock()
	n := s.nodes.getNode(id)
	n.Inbounds = append(n.Inbounds, NodeInbound{
		InboundID: 10, InboundTag: "xhttp-443", Protocol: "vless", Transport: "xhttp", Security: "reality",
		Port: 8443, SNI: "www.microsoft.com",
		RealityPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", RealityShortID: "abcdef01",
		Enabled: true,
	})
	s.nodes.mu.Unlock()

	list := listInbounds1401(t, h, cookie, id)
	if len(list) != 10 {
		t.Fatalf("入站列表应有 10 条，实际 %d", len(list))
	}
	for _, in := range list {
		tag, _ := in["inbound_tag"].(string)
		if tag == "xhttp-443" {
			if in["linkable"] != false {
				t.Fatalf("xhttp-443 的 linkable 应为 false，实际 %v", in["linkable"])
			}
			continue
		}
		if in["linkable"] != true {
			t.Fatalf("%s 的 linkable 应为 true，实际 %v（reason=%v）", tag, in["linkable"], in["link_reason"])
		}
	}
}
