package main

// ---------- 轻量节点后端进订阅（v1.4.20，切片 10） ----------
//
// 后端节点（BackendNode＋NodeInbound）进订阅的规则：
//   - 只进按人订阅（SubUser／MergedSub 的令牌）：凭证是每人每节点一个
//     UUID（NodeUUIDs[节点地址]），通用订阅没有用户身份、拿不到 UUID，
//     不含后端节点（与“发人用合并订阅”的口径一致）。
//   - 只认八种后端入站：VLESS＋REALITY、VLESS＋TLS、AnyTLS、
//     Hysteria2、TUIC、SS／SS2022、Trojan、SOCKS5（v1.4.23 补全；
//     之前切片 10 只做前三种，HY2／TUIC／SS／Trojan／VLESS-TLS
//     是已知缺口）；XHTTP 入站不进（§8.3，服务端 fallback）；
//     VLESS 明文（tcp＋none）不进（订阅渲染器与客户端解析器都
//     默认当 WS，TLS 才有意义）。
//   - 指派口径与 agent 下发一致：节点侧 UserIDs（nodeHasUser）＋用户侧
//     四级指派（backendAssignAllows／backendAssignInbounds）；停用的
//     订阅不进；UUID 还没生成（节点地址不在 NodeUUIDs 里）时，UUID 制
//     协议的入站先跳过、SS／SOCKS5 照常出（只读路径上不现生成 UUID、
//     不写 config，生成是建用户／节点同步时的事）。
//   - UUID 门控在协议级（v1.4.23）：只有 6 种 UUID 制协议
//     （VLESS＋REALITY、VLESS＋TLS、HY2、TUIC、AnyTLS、Trojan）
//     要求用户在该节点有 UUID；SS／SOCKS5 用入站级共享凭证，
//     不要求 UUID——没 UUID 的用户也能拿到这两条链接。
//     backendInboundLink 按 backendInboundNeedsUUID 判断，
//   - 命名：<节点名>-<入站 tag>（与切片 10 对齐契约一致）；NodeName 填
//     节点名，供渲染器做同节点多入站分组（Clash fallback／sing-box
//     urltest）。直连链接（地址就是节点地址），不带 -中转／-直连后缀，
//     按内容范围过滤时两边都留着（filterSubLinksByKind 的无后缀口径）。
//   - 私钥绝不进订阅：REALITY 只写公钥（pbk）与 short id，
//     RealityPrivateKey 在这里碰都不碰。

import (
	"encoding/base64"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// backendInboundNeedsUUID 一条后端入站出链接是否要求用户在该节点有
// UUID（v1.4.23，UUID 门控下移到协议级）：6 种 UUID 制协议要求，
// SS／SOCKS5 用入站级共享凭证、不要求。linkable 不管 UUID，只管入站
// 字段；backendInboundLink 与单条链接接口按它判断。
func backendInboundNeedsUUID(in NodeInbound) bool {
	switch {
	case in.Protocol == "vless" && in.Transport == "reality" && in.Security == "reality":
		return true
	case in.Protocol == "vless" && in.Transport == "tcp" && in.Security == "tls":
		return true
	case in.Protocol == "anytls":
		return true
	case in.Protocol == "hysteria2":
		return true
	case in.Protocol == "tuic":
		return true
	case in.Protocol == "trojan":
		return true
	default:
		return false
	}
}

// backendInboundLinkable 判断一条后端入站能不能出单条链接（v1.4.22，
// v1.4.23 补全到八种协议）。
// 只看入站本身（启用状态、协议、必填字段），不看节点地址与用户 UUID。
// ok=false 时附一句直白原因，供入站卡片置灰按钮的 title 与单条链接
// 接口的报错。协议口径与 backendInboundLink 完全一致——这里是唯一
// 事实来源，backendInboundLink 复用它，订阅能拼什么这里就认什么。
func backendInboundLinkable(in NodeInbound) (bool, string) {
	if !in.Enabled {
		return false, "入站已停用"
	}
	switch {
	case in.Protocol == "vless" && in.Transport == "reality" && in.Security == "reality":
		if strings.TrimSpace(in.RealityPublicKey) == "" || strings.TrimSpace(in.RealityShortID) == "" {
			return false, "REALITY 公钥或 short id 缺失，轮换一次密钥即可"
		}
		sni := strings.TrimSpace(in.SNI)
		if sni == "" {
			sni = strings.TrimSpace(in.RealityServerName)
		}
		if sni == "" {
			return false, "SNI 为空，请先填 SNI"
		}
		return true, ""
	case in.Protocol == "vless" && in.Transport == "tcp" && in.Security == "tls":
		// VLESS＋TLS（v1.4.23）：SNI 必填；证书不检查（保持 1.4.22 口径）。
		if strings.TrimSpace(in.SNI) == "" {
			return false, "SNI 为空，请先填 SNI"
		}
		return true, ""
	case in.Protocol == "anytls":
		if strings.TrimSpace(in.SNI) == "" {
			return false, "SNI 为空，请先填 SNI"
		}
		return true, ""
	case in.Protocol == "hysteria2":
		// HY2（v1.4.23）：SNI 必填；不带 obfs（agent 没开）。
		if strings.TrimSpace(in.SNI) == "" {
			return false, "SNI 为空，请先填 SNI"
		}
		return true, ""
	case in.Protocol == "tuic":
		// TUIC（v1.4.23）：SNI 必填。
		if strings.TrimSpace(in.SNI) == "" {
			return false, "SNI 为空，请先填 SNI"
		}
		return true, ""
	case in.Protocol == "shadowsocks":
		// SS／SS2022（v1.4.23）：方法与密码必填；SS2022 密码长度复用
		// validateSS2022Password（与保存时同一口径）。
		if strings.TrimSpace(in.SSMethod) == "" || strings.TrimSpace(in.SSPassword) == "" {
			return false, "SS 加密方法或密码为空"
		}
		if msg := validateSS2022Password(in.SSMethod, in.SSPassword); msg != "" {
			return false, msg
		}
		return true, ""
	case in.Protocol == "trojan":
		// Trojan（v1.4.23）：SNI 必填。
		if strings.TrimSpace(in.SNI) == "" {
			return false, "SNI 为空，请先填 SNI"
		}
		return true, ""
	case in.Protocol == "socks":
		if strings.TrimSpace(in.SocksUsername) == "" || in.SocksPassword == "" {
			return false, "SOCKS5 用户名或密码为空"
		}
		return true, ""
	case in.Protocol == "vless" && in.Transport == "xhttp":
		return false, "XHTTP 只作服务端 fallback，不进订阅，不支持单条链接"
	case in.Protocol == "vless":
		return false, "该 VLESS 入站不进后端订阅，不支持单条链接"
	default:
		return false, "该协议不支持单条链接"
	}
}

// backendInboundLink 把一条后端入站渲染成一条订阅链接（v1.4.20，切片 10；
// v1.4.23 补全到八种协议）。
// uuid 是该用户在该节点的 VLESS UUID（HY2／TUIC／Trojan／AnyTLS 的凭证
// 口径见各分支注释；SS／SOCKS5 不用 UUID）。不在范围的入站（XHTTP／
// VLESS 明文）与必填字段缺失的入站返回 ok=false，调用方跳过，不断链。
// v1.4.22：启用／协议／必填字段的判断收进 backendInboundLinkable，
// 这里只剩节点地址／tag 与各分支的 UUID 口径，行为与原来逐字一致。
// v1.4.23：UUID 要求按 backendInboundNeedsUUID 在协议级判断，缺了
// 返回 false；SS／SOCKS5 不要求 UUID。
// 链接模板（已核对过订阅渲染器与客户端解析器，逐字拼）：
//   - VLESS-TLS：vless://<uuid>@<host>:<port>?encryption=none&security=tls&sni=<sni>&type=tcp#<名>-<tag>
//     （必须带 security=tls 和 type=tcp，否则渲染器与客户端解析器都
//     默认当 WS；不要带 fp/alpn，渲染器会丢）。
//   - HY2：hysteria2://<uuid>@<host>:<port>?sni=<sni>&alpn=h3#<名>-<tag>
//     （密码＝UUID；alpn=h3 写死；不要带 obfs，agent 没开）。
//   - TUIC：tuic://<uuid>:<uuid>@<host>:<port>?congestion_control=bbr&alpn=h3&sni=<sni>#<名>-<tag>
//     （uuid 与 password 都是用户 UUID；congestion_control=bbr 与 alpn=h3 写死）。
//   - SS：ss://<base64url(method:password)>@<host>:<port>#<名>-<tag>
//     （SIP002 形式；method／password 是入站级共享凭证；必须整体 base64
//     RawURLEncoding（去=），不能把密码原文拼进 userinfo——SS2022 密码
//     本身就是带+/=的 base64；不要带任何 query 参数）。
//   - Trojan：trojan://<uuid>@<host>:<port>?security=tls&sni=<sni>&type=tcp#<名>-<tag>
//     （密码＝UUID；必须带 security=tls 和 type=tcp，理由同 VLESS-TLS）。
func backendInboundLink(node BackendNode, in NodeInbound, uuid string) (subLink, bool) {
	if strings.TrimSpace(node.Address) == "" || strings.TrimSpace(in.InboundTag) == "" {
		return subLink{}, false
	}
	if ok, _ := backendInboundLinkable(in); !ok {
		return subLink{}, false
	}
	if backendInboundNeedsUUID(in) && uuid == "" {
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
		// pbk／sid／sni 非空已由 backendInboundLinkable 保证，uuid 非空
		// 已由上面的 backendInboundNeedsUUID 门控保证。
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
	case in.Protocol == "vless" && in.Transport == "tcp" && in.Security == "tls":
		// VLESS＋TLS（v1.4.23）：uuid 是用户 UUID；sni 是入站域名。
		// type=tcp 与 security=tls 都必须写死在模板里（见函数头注释）。
		sni := strings.TrimSpace(in.SNI)
		// sni 非空已由 backendInboundLinkable 保证。
		u := url.URL{Scheme: "vless", User: url.User(uuid), Host: host, Fragment: display}
		u.RawQuery = "encryption=none&security=tls" +
			"&sni=" + url.QueryEscape(sni) + "&type=tcp"
		return subLink{Name: display, Link: u.String(), LandingAddr: node.Address, NodeName: name}, true
	case in.Protocol == "anytls":
		// AnyTLS（切片 10）：password＝UUID（§9.4 凭证口径），sni 是
		// 入站域名；链接里只带 sni 一个参数（与对齐契约逐字一致）。
		sni := strings.TrimSpace(in.SNI)
		// sni 非空已由 backendInboundLinkable 保证。
		u := url.URL{Scheme: "anytls", User: url.User(uuid), Host: host, Fragment: display}
		u.RawQuery = "sni=" + url.QueryEscape(sni)
		return subLink{Name: display, Link: u.String(), LandingAddr: node.Address, NodeName: name}, true
	case in.Protocol == "hysteria2":
		// HY2（v1.4.23）：密码＝UUID；sni 是入站域名；alpn=h3 写死。
		sni := strings.TrimSpace(in.SNI)
		// sni 非空已由 backendInboundLinkable 保证。
		u := url.URL{Scheme: "hysteria2", User: url.User(uuid), Host: host, Fragment: display}
		u.RawQuery = "sni=" + url.QueryEscape(sni) + "&alpn=h3"
		return subLink{Name: display, Link: u.String(), LandingAddr: node.Address, NodeName: name}, true
	case in.Protocol == "tuic":
		// TUIC（v1.4.23）：uuid 与 password 都是用户 UUID；sni 是入站
		// 域名；congestion_control=bbr 与 alpn=h3 写死。
		sni := strings.TrimSpace(in.SNI)
		// sni 非空已由 backendInboundLinkable 保证。
		u := url.URL{Scheme: "tuic", User: url.UserPassword(uuid, uuid), Host: host, Fragment: display}
		u.RawQuery = "congestion_control=bbr&alpn=h3&sni=" + url.QueryEscape(sni)
		return subLink{Name: display, Link: u.String(), LandingAddr: node.Address, NodeName: name}, true
	case in.Protocol == "shadowsocks":
		// SS／SS2022（v1.4.23）：SIP002 形式，整体 base64url 编码
		// method:password（RawURLEncoding，去=）；凭证取入站配置，
		// 全员共用，不用 UUID；不带任何 query 参数。
		// 方法／密码非空与 SS2022 长度已由 backendInboundLinkable 保证。
		method := strings.TrimSpace(in.SSMethod)
		cred := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + in.SSPassword))
		u := url.URL{Scheme: "ss", User: url.User(cred), Host: host, Fragment: display}
		return subLink{Name: display, Link: u.String(), LandingAddr: node.Address, NodeName: name}, true
	case in.Protocol == "trojan":
		// Trojan（v1.4.23）：密码＝UUID；sni 是入站域名。
		// security=tls 与 type=tcp 都必须写死在模板里（理由同 VLESS-TLS）。
		sni := strings.TrimSpace(in.SNI)
		// sni 非空已由 backendInboundLinkable 保证。
		u := url.URL{Scheme: "trojan", User: url.User(uuid), Host: host, Fragment: display}
		u.RawQuery = "security=tls&sni=" + url.QueryEscape(sni) + "&type=tcp"
		return subLink{Name: display, Link: u.String(), LandingAddr: node.Address, NodeName: name}, true
	case in.Protocol == "socks":
		// SOCKS5（切片 10）：凭证取入站配置的用户名／密码。
		// 用户名／密码非空已由 backendInboundLinkable 保证。
		user := strings.TrimSpace(in.SocksUsername)
		pw := in.SocksPassword
		u := url.URL{Scheme: "socks5", User: url.UserPassword(user, pw), Host: host, Fragment: display}
		return subLink{Name: display, Link: u.String(), LandingAddr: node.Address, NodeName: name}, true
	default:
		// XHTTP（§8.3，只作服务端 fallback）／VLESS 明文：本切片不进订阅。
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
		// UUID 门控在协议级（v1.4.23）：uuid 可能为空，传给
		// backendInboundLink 由它按协议决定（SS／SOCKS5 不要求
		// UUID，照常出链接；UUID 制协议缺 UUID 时该入站跳过）。
		uuid := uuids[node.Address]
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
