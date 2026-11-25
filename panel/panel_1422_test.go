package main

// v1.4.22：后端入站一键复制链接＋二维码。
// v1.4.23 更新：HY2／TUIC／SS／SS2022／Trojan／VLESS-TLS 也可出单条
// 链接（linkable 与链接格式的覆盖见 panel_1423_test.go）；本文件只
// 保留 1.4.22 的三条旧协议与 XHTTP／VLESS 明文的断言，版本号升到 1.4.23。
//
// 覆盖：
//   - 版本断言 1.4.23。
//   - backendInboundLinkable：VLESS+REALITY／AnyTLS／SOCKS5 齐全时
//     可出单条链接；缺字段／停用时不可并给直白原因；XHTTP／VLESS 明文
//     不进订阅、不可出单条链接并给原因。
//   - 入站列表（GET /api/v1/nodes/{id}/inbounds）每条附 linkable／
//     link_reason；私钥不返回。
//   - 单条链接接口（GET /api/v1/nodes/{id}/inbounds/{inbound_id}/link）：
//     链接与订阅里同一条入站的链接逐字一致（对照 1.4.20 的契约常量）；
//     二维码是 data URI；校验（节点／入站／用户不存在、没选用户、
//     用户不能访问节点、入站级未指派、用户在该节点没 UUID、
//     不支持的协议）都返回明确错误；未登录 401；只读（不生成 UUID、
//     不写 config）。
//     注意：没 UUID 的 400 只针对 UUID 制协议（REALITY 等），SS／
//     SOCKS5 没 UUID 也能出链接，见 panel_1423_test.go。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestVersion1422(t *testing.T) {
	if Version != "1.4.23" {
		t.Fatalf("Version 应为 1.4.23，实际 %q", Version)
	}
}

// ---------- backendInboundLinkable ----------

func TestBackendInboundLinkable1422(t *testing.T) {
	reality := NodeInbound{InboundID: 1, InboundTag: "reality-443", Protocol: "vless", Transport: "reality", Security: "reality",
		Port: 443, SNI: "www.microsoft.com", RealityPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		RealityShortID: "abcdef01", Enabled: true}
	if ok, reason := backendInboundLinkable(reality); !ok {
		t.Fatalf("REALITY 齐全时应可出单条链接，实际不可：%q", reason)
	}
	// 缺公钥。
	noPbk := reality
	noPbk.RealityPublicKey = ""
	if ok, reason := backendInboundLinkable(noPbk); ok || !strings.Contains(reason, "公钥") {
		t.Fatalf("REALITY 缺公钥时应不可并说明公钥，实际 ok=%v reason=%q", ok, reason)
	}
	// 缺 SNI。
	noSNI := reality
	noSNI.SNI = ""
	noSNI.RealityServerName = ""
	if ok, reason := backendInboundLinkable(noSNI); ok || !strings.Contains(reason, "SNI") {
		t.Fatalf("REALITY 缺 SNI 时应不可并说明 SNI，实际 ok=%v reason=%q", ok, reason)
	}
	// 停用。
	disabled := reality
	disabled.Enabled = false
	if ok, reason := backendInboundLinkable(disabled); ok || !strings.Contains(reason, "停用") {
		t.Fatalf("停用的入站应不可并说明停用，实际 ok=%v reason=%q", ok, reason)
	}
	// AnyTLS。
	anytls := NodeInbound{InboundID: 2, InboundTag: "anytls-443", Protocol: "anytls", Transport: "tcp", Security: "tls",
		Port: 443, SNI: "example.com", Enabled: true}
	if ok, reason := backendInboundLinkable(anytls); !ok {
		t.Fatalf("AnyTLS 齐全时应可出单条链接，实际不可：%q", reason)
	}
	anytlsNoSNI := anytls
	anytlsNoSNI.SNI = ""
	if ok, _ := backendInboundLinkable(anytlsNoSNI); ok {
		t.Fatalf("AnyTLS 缺 SNI 时应不可")
	}
	// SOCKS5。
	socks := NodeInbound{InboundID: 3, InboundTag: "socks-1080", Protocol: "socks", Transport: "tcp", Security: "none",
		Port: 1080, SocksUsername: "user1", SocksPassword: "pass1234", Enabled: true}
	if ok, reason := backendInboundLinkable(socks); !ok {
		t.Fatalf("SOCKS5 齐全时应可出单条链接，实际不可：%q", reason)
	}
	socksNoPw := socks
	socksNoPw.SocksPassword = ""
	if ok, _ := backendInboundLinkable(socksNoPw); ok {
		t.Fatalf("SOCKS5 缺密码时应不可")
	}
	// 不支持的协议：原因里要点出协议／口径（v1.4.23 起 HY2／TUIC／SS／
	// Trojan／VLESS-TLS 都已支持，这里只剩 XHTTP 与 VLESS 明文）。
	unsupported := []struct {
		in   NodeInbound
		want string
	}{
		{NodeInbound{InboundTag: "xhttp-443", Protocol: "vless", Transport: "xhttp", Security: "reality", Port: 8443, SNI: "www.microsoft.com", Enabled: true}, "XHTTP"},
		{NodeInbound{InboundTag: "plain-8080", Protocol: "vless", Transport: "tcp", Security: "none", Port: 8080, Enabled: true}, "VLESS"},
	}
	for _, c := range unsupported {
		ok, reason := backendInboundLinkable(c.in)
		if ok {
			t.Fatalf("%s 应不可出单条链接，实际可", c.in.InboundTag)
		}
		if !strings.Contains(reason, c.want) {
			t.Fatalf("%s 的原因应提到 %q，实际 %q", c.in.InboundTag, c.want, reason)
		}
	}
}

// ---------- 入站列表的 linkable ----------

// seedLinkNode1422 用 1.4.20 的契约节点＋用户（REALITY／AnyTLS／SOCKS5
// 三条入站＋用户 su-1420），再追加一条 XHTTP 与一条 HY2 入站，返回节点 id。
func seedLinkNode1422(t *testing.T, s *Server, h http.Handler, cookie string) int64 {
	t.Helper()
	id := seedContractNode1420(t, s, h, cookie)
	s.nodes.mu.Lock()
	n := s.nodes.getNode(id)
	n.Inbounds = append(n.Inbounds,
		NodeInbound{InboundID: 4, InboundTag: "xhttp-443", Protocol: "vless", Transport: "xhttp", Security: "reality",
			Port: 8443, SNI: "www.microsoft.com",
			RealityPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", RealityShortID: "abcdef01",
			Enabled: true},
		NodeInbound{InboundID: 5, InboundTag: "hy2-443", Protocol: "hysteria2", Transport: "quic", Security: "tls",
			Port: 443, SNI: "example.com", Domain: "example.com", Enabled: true},
	)
	for i := range n.Inbounds {
		n.Inbounds[i].NodeID = id
	}
	s.nodes.mu.Unlock()
	return id
}

func TestInboundListLinkable1422(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id := seedLinkNode1422(t, s, h, cookie)

	list := listInbounds1401(t, h, cookie, id)
	if len(list) != 5 {
		t.Fatalf("入站列表应有 5 条，实际 %d", len(list))
	}
	byTag := func(tag string) map[string]any {
		for _, in := range list {
			if in["inbound_tag"] == tag {
				return in
			}
		}
		return nil
	}
	for _, tag := range []string{"reality-443", "anytls-443", "socks-1080"} {
		in := byTag(tag)
		if in == nil {
			t.Fatalf("列表里没有 %s", tag)
		}
		if in["linkable"] != true {
			t.Fatalf("%s 的 linkable 应为 true，实际 %v（reason=%v）", tag, in["linkable"], in["link_reason"])
		}
	}
	xhttp := byTag("xhttp-443")
	if xhttp == nil || xhttp["linkable"] != false {
		t.Fatalf("xhttp-443 的 linkable 应为 false，实际 %v", xhttp)
	}
	if reason, _ := xhttp["link_reason"].(string); !strings.Contains(reason, "XHTTP") {
		t.Fatalf("xhttp-443 的 link_reason 应提到 XHTTP，实际 %q", reason)
	}
	// HY2 在 v1.4.23 起可出链接。
	hy2 := byTag("hy2-443")
	if hy2 == nil || hy2["linkable"] != true {
		t.Fatalf("hy2-443 的 linkable 应为 true，实际 %v", hy2)
	}
	// 私钥不返回。
	for _, in := range list {
		if v, _ := in["reality_private_key"].(string); v != "" {
			t.Fatalf("入站列表不应返回 REALITY 私钥：%s", in["inbound_tag"])
		}
	}
}

// ---------- 单条链接接口 ----------

// getInboundLink1422 调单条链接接口，返回状态码与解析后的响应。
func getInboundLink1422(t *testing.T, h http.Handler, cookie string, nodeID, inboundID int64, userID string) (int, map[string]any) {
	t.Helper()
	path := "/api/v1/nodes/" + itoa1400(nodeID) + "/inbounds/" + itoa1400(inboundID) + "/link"
	if userID != "" {
		path += "?user_id=" + userID
	}
	code, body := authed1400(t, h, cookie, "GET", path, "")
	return code, respMap1400(t, body)
}

func TestInboundLinkEndpoint1422(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id := seedLinkNode1422(t, s, h, cookie)
	userKey := "subuser:su-1420"

	// 四条可出链接的入站：与订阅里同一条入站的链接逐字一致（契约常量；
	// HY2 的期望串见 panel_1423_test.go 的 contract1423HY2），
	// 二维码是 data URI，label 是 <节点名>-<入站 tag>。
	want := map[int64]struct {
		link  string
		label string
	}{
		1: {contract1420Reality, "example-node-reality-443"},
		2: {contract1420AnyTLS, "example-node-anytls-443"},
		3: {contract1420Socks, "example-node-socks-1080"},
		5: {contract1423HY2, "example-node-hy2-443"},
	}
	for inboundID, w := range want {
		code, m := getInboundLink1422(t, h, cookie, id, inboundID, userKey)
		if code != 200 {
			t.Fatalf("入站 %d 单条链接应 200，实际 %d：%v", inboundID, code, m)
		}
		if m["link"] != w.link {
			t.Fatalf("入站 %d 的链接与订阅不一致：\n实际 %q\n期望 %q", inboundID, m["link"], w.link)
		}
		if m["label"] != w.label {
			t.Fatalf("入站 %d 的 label 应为 %q，实际 %q", inboundID, w.label, m["label"])
		}
		qr, _ := m["qr"].(string)
		if !strings.HasPrefix(qr, "data:image/png;base64,") {
			t.Fatalf("入站 %d 的二维码应是 data URI，实际 %q", inboundID, qr)
		}
		if m["username"] != "契约用户" {
			t.Fatalf("入站 %d 的 username 应为契约用户，实际 %q", inboundID, m["username"])
		}
	}
	// XHTTP：不支持，400 且原因点出协议（HY2 在 v1.4.23 起已支持，
	// 见上面 want 里的第 5 条）。
	for _, inboundID := range []int64{4} {
		code, m := getInboundLink1422(t, h, cookie, id, inboundID, userKey)
		if code != 400 {
			t.Fatalf("入站 %d 不支持单条链接，应 400，实际 %d：%v", inboundID, code, m)
		}
		err, _ := m["error"].(string)
		if err == "" {
			t.Fatalf("入站 %d 的 400 应带明确错误，实际 %v", inboundID, m)
		}
	}
	// 没选用户。
	if code, m := getInboundLink1422(t, h, cookie, id, 1, ""); code != 400 {
		t.Fatalf("没选用户应 400，实际 %d：%v", code, m)
	}
	// 用户不存在。
	if code, m := getInboundLink1422(t, h, cookie, id, 1, "subuser:su-nope"); code != 404 {
		t.Fatalf("用户不存在应 404，实际 %d：%v", code, m)
	}
	// 入站不存在。
	if code, m := getInboundLink1422(t, h, cookie, id, 999, userKey); code != 404 {
		t.Fatalf("入站不存在应 404，实际 %d：%v", code, m)
	}
	// 节点不存在。
	if code, m := getInboundLink1422(t, h, cookie, 999, 1, userKey); code != 404 {
		t.Fatalf("节点不存在应 404，实际 %d：%v", code, m)
	}
	// 未登录。
	if code, _ := getInboundLink1422(t, h, "", id, 1, userKey); code != 401 {
		t.Fatalf("未登录应 401，实际 %d", code)
	}
}

func TestInboundLinkAccess1422(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id := seedLinkNode1422(t, s, h, cookie)

	// 用户不能访问该节点：把节点 UserIDs 收成只含别人。
	s.nodes.mu.Lock()
	n := s.nodes.getNode(id)
	n.UserIDs = []string{"subuser:su-other"}
	s.nodes.mu.Unlock()
	if code, m := getInboundLink1422(t, h, cookie, id, 1, "subuser:su-1420"); code != 403 {
		t.Fatalf("用户不能访问节点时应 403，实际 %d：%v", code, m)
	}

	// 用户在该节点没 UUID：新建一个用户（指派到该节点），但 NodeUUIDs 里
	// 没有该节点地址。接口只读、不现生成 UUID，应 400 并明说。
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{
		ID: "su-no-uuid", Name: "没 UUID 的用户",
		NodeUUIDs: map[string]string{},
	})
	s.nodes.mu.Lock()
	n.UserIDs = nil
	s.nodes.mu.Unlock()
	code, m := getInboundLink1422(t, h, cookie, id, 1, "subuser:su-no-uuid")
	if code != 400 {
		t.Fatalf("用户在该节点没 UUID 时应 400，实际 %d：%v", code, m)
	}
	err, _ := m["error"].(string)
	if !strings.Contains(err, "UUID") {
		t.Fatalf("没 UUID 的报错应提到 UUID，实际 %q", err)
	}
	// 只读：接口没把 UUID 写进 config（没 UUID 的用户仍没有）。
	found := false
	for _, u := range s.cfg.SubUsers {
		if u.ID == "su-no-uuid" {
			found = true
			if len(u.NodeUUIDs) != 0 {
				t.Fatalf("单条链接接口不应现生成 UUID，实际 NodeUUIDs=%v", u.NodeUUIDs)
			}
		}
	}
	if !found {
		t.Fatalf("su-no-uuid 不见了")
	}

	// 停用的用户。
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{
		ID: "su-disabled", Name: "停用的用户", Disabled: true,
		NodeUUIDs: map[string]string{contract1420Addr: contract1420UUID},
	})
	if code, m := getInboundLink1422(t, h, cookie, id, 1, "subuser:su-disabled"); code != 400 {
		t.Fatalf("停用的用户应 400，实际 %d：%v", code, m)
	}

	// 入站级未指派：用户指派 scope=inbound，只含 anytls-443。
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{
		ID: "su-inbound-only", Name: "只指派 AnyTLS 的用户",
		NodeUUIDs:     map[string]string{contract1420Addr: contract1420UUID},
		BackendScope:  "inbound",
		BackendTargets: []string{itoa1400(id) + ":anytls-443"},
	})
	if code, m := getInboundLink1422(t, h, cookie, id, 1, "subuser:su-inbound-only"); code != 403 {
		t.Fatalf("入站级未指派时应 403，实际 %d：%v", code, m)
	}
	code, m = getInboundLink1422(t, h, cookie, id, 2, "subuser:su-inbound-only")
	if code != 200 {
		t.Fatalf("入站级已指派的 AnyTLS 应 200，实际 %d：%v", code, m)
	}
	if m["link"] != contract1420AnyTLS {
		t.Fatalf("入站级已指派的 AnyTLS 链接不对：%q", m["link"])
	}
}

// TestInboundLinkMatchesSubscription1422 单条链接与订阅里同一条入站的
// 链接逐字一致（端到端对照：同一用户、同一节点，订阅出的 REALITY 链接
// 与单条链接接口出的 REALITY 链接相同）。
func TestInboundLinkMatchesSubscription1422(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id := seedLinkNode1422(t, s, h, cookie)

	subLinks := s.backendSubLinksForUser("subuser", "su-1420")
	var subReality string
	for _, l := range subLinks {
		if l.Name == "example-node-reality-443" {
			subReality = l.Link
		}
	}
	if subReality == "" {
		t.Fatalf("订阅里没有 example-node-reality-443")
	}
	code, m := getInboundLink1422(t, h, cookie, id, 1, "subuser:su-1420")
	if code != 200 {
		t.Fatalf("单条链接应 200，实际 %d：%v", code, m)
	}
	if m["link"] != subReality {
		t.Fatalf("单条链接与订阅里的不一致：\n单条 %q\n订阅 %q", m["link"], subReality)
	}
	// 响应体是合法 JSON，且不含私钥字段。
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "reality_private_key") {
		t.Fatalf("单条链接响应不应含私钥字段：%s", raw)
	}
	if strings.Contains(string(raw), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") {
		// 公钥可以出现（链接里本来就有 pbk），这里不断言。
	}
}
