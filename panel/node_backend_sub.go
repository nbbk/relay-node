package main

// ---------- 轻量节点后端进订阅（v1.4.20，切片 10） ----------
//
// 后端节点（BackendNode＋NodeInbound）进订阅的规则：
//   - 只进按人订阅（SubUser／MergedSub 的令牌）：凭证是每人每节点一个
//     UUID（NodeUUIDs[节点地址]），通用订阅没有用户身份、拿不到 UUID，
//     不含后端节点（与“发人用合并订阅”的口径一致）。
//   - 只认切片 10 的三样：VLESS＋REALITY、AnyTLS、SOCKS5；XHTTP 入站
//     不进（§8.3，服务端 fallback）；HY2／TUIC 不进（本切片不做）；
//     后端 SS／SS2022 不进（它们在旧六协议落地链里，不在本切片范围）；
//     VLESS＋TLS／VLESS 明文／Trojan 的后端入站也不进（同理，不在本
//     切片范围——节点只有这些入站时订阅里仍是 0 节点，是已知缺口，
//     不是故障；见 panel_1420_test.go 的 TestBackendSubLinksScope1420）。
//   - 指派口径与 agent 下发一致：节点侧 UserIDs（nodeHasUser）＋用户侧
//     四级指派（backendAssignAllows／backendAssignInbounds）；停用的
//     订阅不进；UUID 还没生成（节点地址不在 NodeUUIDs 里）时该节点
//     先跳过（只读路径上不现生成 UUID、不写 config，生成是建用户／
//     节点同步时的事）。
//   - 命名：<节点名>-<入站 tag>（与切片 10 对齐契约一致）；NodeName 填
//     节点名，供渲染器做同节点多入站分组（Clash fallback／sing-box
//     urltest）。直连链接（地址就是节点地址），不带 -中转／-直连后缀，
//     按内容范围过滤时两边都留着（filterSubLinksByKind 的无后缀口径）。
//   - 私钥绝不进订阅：REALITY 只写公钥（pbk）与 short id，
//     RealityPrivateKey 在这里碰都不碰。

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// backendInboundLink 把一条后端入站渲染成一条订阅链接（v1.4.20，切片 10）。
// uuid 是该用户在该节点的 VLESS UUID（AnyTLS／SOCKS5 的凭证口径见各
// 分支注释）。不在切片范围的入站（XHTTP／HY2／TUIC／SS／VLESS-TLS 等）
// 与必填字段缺失的入站返回 ok=false，调用方跳过，不断链。
func backendInboundLink(node BackendNode, in NodeInbound, uuid string) (subLink, bool) {
	if !in.Enabled || strings.TrimSpace(node.Address) == "" || strings.TrimSpace(in.InboundTag) == "" {
		return subLink{}, false
	}
	name := strings.TrimSpace(node.Name)
	if name == "" {
		name = strings.TrimSpace(node.Address)
	}
	display := name + "-" + strings.TrimSpace(in.InboundTag)
	host := net.JoinHostPort(strings.TrimSpace(node.Address), strconv.Itoa(in.Port))
	switch {
	case in.Protocol == "vless" && in.Transport == "reality" && in.Security == "reality":
		// VLESS＋REALITY（§8.2）：参数顺序与对齐契约逐字一致
		// （encryption=none&security=reality&pbk=…&sid=…&sni=…&
		// fp=…&type=tcp&flow=…）；fp／flow 缺省时补 §8.2 默认值。
		pbk := strings.TrimSpace(in.RealityPublicKey)
		sid := strings.TrimSpace(in.RealityShortID)
		sni := strings.TrimSpace(in.SNI)
		if sni == "" {
			sni = strings.TrimSpace(in.RealityServerName)
		}
		if uuid == "" || pbk == "" || sid == "" || sni == "" {
			return subLink{}, false
		}
		fp := strings.TrimSpace(in.RealityFingerprint)
		if fp == "" {
			fp = "chrome"
		}
		flow := strings.TrimSpace(in.Flow)
		if flow == "" {
			flow = "xtls-rprx-vision"
		}
		u := url.URL{Scheme: "vless", User: url.User(uuid), Host: host, Fragment: display}
		u.RawQuery = "encryption=none&security=reality" +
			"&pbk=" + url.QueryEscape(pbk) +
			"&sid=" + url.QueryEscape(sid) +
			"&sni=" + url.QueryEscape(sni) +
			"&fp=" + url.QueryEscape(fp) +
			"&type=tcp&flow=" + url.QueryEscape(flow)
		return subLink{Name: display, Link: u.String(), LandingAddr: node.Address, NodeName: name}, true
	case in.Protocol == "anytls":
		// AnyTLS（切片 10）：password＝UUID（§9.4 凭证口径），sni 是
		// 入站域名；链接里只带 sni 一个参数（与对齐契约逐字一致）。
		sni := strings.TrimSpace(in.SNI)
		if uuid == "" || sni == "" {
			return subLink{}, false
		}
		u := url.URL{Scheme: "anytls", User: url.User(uuid), Host: host, Fragment: display}
		u.RawQuery = "sni=" + url.QueryEscape(sni)
		return subLink{Name: display, Link: u.String(), LandingAddr: node.Address, NodeName: name}, true
	case in.Protocol == "socks":
		// SOCKS5（切片 10）：凭证取入站配置的用户名／密码。
		user := strings.TrimSpace(in.SocksUsername)
		pw := in.SocksPassword
		if user == "" || pw == "" {
			return subLink{}, false
		}
		u := url.URL{Scheme: "socks5", User: url.UserPassword(user, pw), Host: host, Fragment: display}
		return subLink{Name: display, Link: u.String(), LandingAddr: node.Address, NodeName: name}, true
	default:
		// XHTTP（§8.3，只作服务端 fallback）／HY2／TUIC／SS／
		// SS2022／VLESS-TLS／VLESS 明文／Trojan：本切片不进订阅。
		return subLink{}, false
	}
}

// backendSubLinksForUser 取某份按人订阅（kind="subuser"/"merged"）在
// 全部后端节点上的订阅链接（只读：不生成 UUID、不写 config、不 bump
// 版本）。调用方是订阅内容生成（serveSubContent 与被管端订阅接口）。
// 节点顺序按节点 id（listNodes 已排好序），同节点内按入站列表顺序。
func (s *Server) backendSubLinksForUser(kind, id string) []subLink {
	var uuids map[string]string
	var scope string
	var targets []string
	var groupID string
	switch kind {
	case "subuser":
		found := false
		for i := range s.cfg.SubUsers {
			if s.cfg.SubUsers[i].ID == id {
				u := &s.cfg.SubUsers[i]
				if u.Disabled {
					return nil
				}
				uuids, scope, targets, groupID = u.NodeUUIDs, u.BackendScope, u.BackendTargets, u.GroupID
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	case "merged":
		found := false
		for i := range s.cfg.MergedSubs {
			if s.cfg.MergedSubs[i].ID == id {
				m := &s.cfg.MergedSubs[i]
				if m.Disabled {
					return nil
				}
				uuids, scope, targets, groupID = m.NodeUUIDs, m.BackendScope, m.BackendTargets, m.GroupID
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	default:
		return nil
	}
	// 节点列表是拷贝（centralNodesSummary 同款做法：持 nodes.mu 取）。
	s.nodes.mu.Lock()
	nodes := s.nodes.listNodes()
	s.nodes.mu.Unlock()
	key := userSubKey(kind, id)
	var out []subLink
	for _, node := range nodes {
		if !nodeHasUser(&node, key) {
			continue
		}
		if !s.backendAssignAllows(scope, targets, groupID, &node) {
			continue
		}
		uuid := uuids[node.Address]
		if uuid == "" {
			continue
		}
		allowed := backendAssignInbounds(scope, targets, &node)
		for _, in := range node.Inbounds {
			if allowed != nil {
				ok := false
				for _, tag := range allowed {
					if tag == in.InboundTag {
						ok = true
						break
					}
				}
				if !ok {
					continue
				}
			}
			if l, ok := backendInboundLink(node, in, uuid); ok {
				out = append(out, l)
			}
		}
	}
	return out
}

// backendSubLinksForUserKey 按 userSubKey（"subuser:su-…"/"merged:ms-…"）
// 取后端订阅链接（被管端订阅接口的 for_user 参数就是这个格式）。
func (s *Server) backendSubLinksForUserKey(key string) []subLink {
	kind, id, ok := strings.Cut(key, ":")
	if !ok || (kind != "subuser" && kind != "merged") || id == "" {
		return nil
	}
	return s.backendSubLinksForUser(kind, id)
}
