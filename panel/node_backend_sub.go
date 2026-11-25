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
//   - 命名：<节点名>-<入站 tag>-A（直连）／<节点名>-<入站 tag>-B（中转）
//     （v1.4.25，twin：同入站先 A 后 B；-A／-B 是成对后缀，不是
//     「中转」／「直连」字样——链接名里绝不出现这两个词。直连链接的
//     地址就是节点地址；中转链接的地址是中转机的监听地址，UUID 与
//     入站参数与直连逐字一致。NodeName 填节点名，供渲染器做同节点
//     多入站分组（Clash fallback／sing-box urltest）。按内容范围
//     过滤时两边都留着（filterSubLinksByKind 的无后缀口径：-A／-B
//     既不是 -中转也不是 -直连，两边过滤都通过）。
//   - 私钥绝不进订阅：REALITY 只写公钥（pbk）与 short id，
//     RealityPrivateKey 在这里碰都不碰。

import (
	"encoding/base64"
	"net"
	"net/url"
	"sort"
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

// backendInboundLink 把一条后端入站渲染成一条直连订阅链接（v1.4.20，
// 切片 10；v1.4.23 补全到八种协议；v1.4.25：名字带 -A 后缀，见
// backendInboundLinkHost）。单条链接接口也走这里（直连不变）。
func backendInboundLink(node BackendNode, in NodeInbound, uuid string) (subLink, bool) {
	if strings.TrimSpace(node.Address) == "" {
		return subLink{}, false
	}
	host := net.JoinHostPort(strings.TrimSpace(node.Address), strconv.Itoa(in.Port))
	return backendInboundLinkHost(node, in, uuid, host, "-A")
}

// backendInboundRelayLink 把一条后端入站渲染成一条中转订阅链接
// （v1.4.25，twin 的 B 链接）：host 是中转机的监听地址（"ip:port"），
// 名字带 -B 后缀；UUID 与入站参数与直连逐字一致（同一套 builder）。
// 地址／端口不对时返回 false，调用方跳过该路由、绝不凭空编链接。
func backendInboundRelayLink(node BackendNode, in NodeInbound, uuid, relayHost string, relayPort int) (subLink, bool) {
	if strings.TrimSpace(relayHost) == "" || relayPort < 1 || relayPort > 65535 {
		return subLink{}, false
	}
	host := net.JoinHostPort(strings.TrimSpace(relayHost), strconv.Itoa(relayPort))
	return backendInboundLinkHost(node, in, uuid, host, "-B")
}

// backendInboundLinkHost 直连／中转共用的链接 builder（v1.4.25）：
// 按传入的 host（"ip:port"）与名字后缀拼链接。uuid 是该用户在该节点
// 的 VLESS UUID（HY2／TUIC／Trojan／AnyTLS 的凭证口径见各分支注释；
// SS／SOCKS5 不用 UUID）。不在范围的入站（XHTTP／VLESS 明文）与必填
// 字段缺失的入站返回 ok=false，调用方跳过，不断链。
// v1.4.22：启用／协议／必填字段的判断收进 backendInboundLinkable，
// 这里只剩节点地址／tag 与各分支的 UUID 口径，行为与原来逐字一致。
// v1.4.23：UUID 要求按 backendInboundNeedsUUID 在协议级判断，缺了
// 返回 false；SS／SOCKS5 不要求 UUID。
// 链接模板（已核对过订阅渲染器与客户端解析器，逐字拼）：
//   - VLESS-TLS：vless://<uuid>@<host>:<port>?encryption=none&security=tls&sni=<sni>&type=tcp#<名>-<tag>-A
//     （必须带 security=tls 和 type=tcp，否则渲染器与客户端解析器都
//     默认当 WS；不要带 fp/alpn，渲染器会丢）。
//   - HY2：hysteria2://<uuid>@<host>:<port>?sni=<sni>&alpn=h3#<名>-<tag>-A
//     （密码＝UUID；alpn=h3 写死；不要带 obfs，agent 没开）。
//   - TUIC：tuic://<uuid>:<uuid>@<host>:<port>?congestion_control=bbr&alpn=h3&sni=<sni>#<名>-<tag>-A
//     （uuid 与 password 都是用户 UUID；congestion_control=bbr 与 alpn=h3 写死）。
//   - SS：ss://<base64url(method:password)>@<host>:<port>#<名>-<tag>-A
//     （SIP002 形式；method／password 是入站级共享凭证；必须整体 base64
//     RawURLEncoding（去=），不能把密码原文拼进 userinfo——SS2022 密码
//     本身就是带+/=的 base64；不要带任何 query 参数）。
//   - Trojan：trojan://<uuid>@<host>:<port>?security=tls&sni=<sni>&type=tcp#<名>-<tag>-A
//     （密码＝UUID；必须带 security=tls 和 type=tcp，理由同 VLESS-TLS）。
func backendInboundLinkHost(node BackendNode, in NodeInbound, uuid, host, suffix string) (subLink, bool) {
	if strings.TrimSpace(in.InboundTag) == "" {
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
	display := name + "-" + strings.TrimSpace(in.InboundTag) + suffix
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

// ---------- 后端订阅 twin 链接（v1.4.25） ----------
//
// 同一条后端入站可以同时出直连（-A）与中转（-B）两条链接：中转链接
// 是把直连链接的地址换成中转机的监听地址，UUID 与入站参数逐字不变
// （已验证：这样拼出来的链接能走中转）。中转路由不在管理面板上，
// 在中转面板（远程面板）上——管理面板靠已有的 2 分钟集中管理拉取链
// 从远程面板的 central/status 里收 relay_routes（见 central.go），
// 不新增轮询；本地面板自己的路由（s.nodes.data.Routes，管理面板
// 一般没有）也纳入。
//
// 匹配（四条全满足才出 B）：路由已启用（远程的发之前已过滤，本地的
// 在这里过滤）、目标地址与节点 Address 字符串相等（EqualFold，空
// 目标跳过）、目标端口＝入站端口、协议一致（路由 UDP 当且仅当
// proto=="udp" 或 UseUDP；入站 UDP 当且仅当 HY2 或 TUIC，其余全算
// TCP）。每个入站最多一条 B（多条匹配时取第一条，顺序确定：本地
// 路由在前，再按远程面板 ID、listen_addr 排序）。

// linkModeOf 节点的订阅链接模式：direct／relay／both；空值＝默认 both。
func linkModeOf(node *BackendNode) string {
	switch strings.TrimSpace(node.LinkMode) {
	case "direct", "relay", "both":
		return strings.TrimSpace(node.LinkMode)
	default:
		return "both"
	}
}

// backendInboundIsUDP 一条后端入站是不是 UDP 入站（twin 匹配用）：
// 只有 HY2 与 TUIC 是 UDP，其余全算 TCP。
func backendInboundIsUDP(in NodeInbound) bool {
	return in.Protocol == "hysteria2" || in.Protocol == "tuic"
}

// twinRelay 一条可供 twin 匹配的中转路由（已归一化：只含启用的路由，
// host 是拼 B 链接用的中转机地址（不带端口），port 是监听端口）。
type twinRelay struct {
	remoteAddr string // 路由的目标地址（节点地址）
	remotePort int    // 路由的目标端口（入站端口）
	udp        bool   // 路由是不是 UDP
	host       string // 中转机地址（RelayAddr 或远程面板地址的 hostname）
	port       int    // 中转监听端口（路由 ListenAddr 里拆出来的）
	sortKey    string // 排序键：本地路由在前，再按远程面板 ID、listen_addr
}

// twinRelayHost 中转机地址归一化：RelayAddr 里万一带了端口就只取 host
// 部分（"1.2.3.4:8080"→"1.2.3.4"）；不带端口时原样返回。
func twinRelayHost(addr string) string {
	addr = strings.TrimSpace(addr)
	if h, _, err := net.SplitHostPort(addr); err == nil && h != "" {
		return h
	}
	return addr
}

// twinListenPort 从路由 ListenAddr（"0.0.0.0:30004"）里拆监听端口；
// 拆不出或不在 1-65535 时返回 0，调用方跳过该路由。
func twinListenPort(listenAddr string) int {
	_, p, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

// twinRelayCandidates 收集全部可供 twin 匹配的中转路由（已排序，
// 本地路由在前，再按远程面板 ID、listen_addr）。只读快照与本地
// 路由表，不发任何请求、不新增轮询。快照里没有路由的远程面板、
// RelayAddr 与 BaseURL 都拿不到地址的远程面板、本地没配 RelayAddr
// 的面板，对应的路由直接跳过（绝不凭空编地址）。
func (s *Server) twinRelayCandidates() []twinRelay {
	// 远程快照：centralMu 下只拷贝指针，匹配在锁外做。
	s.centralMu.Lock()
	remotes := make([]RemotePanel, len(s.cfg.Remotes))
	copy(remotes, s.cfg.Remotes)
	snaps := make(map[string]*RemoteStatusSnapshot, len(s.centralCache))
	for k, v := range s.centralCache {
		snaps[k] = v
	}
	s.centralMu.Unlock()
	var out []twinRelay
	for _, rem := range remotes {
		snap := snaps[rem.ID]
		if snap == nil || len(snap.RelayRoutes) == 0 {
			continue
		}
		host := twinRelayHost(snap.RelayAddr)
		if host == "" {
			// RelayAddr 为空时退到远程面板登记地址的 hostname。
			if u, err := url.Parse(snap.BaseURL); err == nil && u.Hostname() != "" {
				host = u.Hostname()
			} else if u, err := url.Parse(rem.BaseURL); err == nil && u.Hostname() != "" {
				host = u.Hostname()
			}
		}
		if host == "" {
			continue
		}
		for _, r := range snap.RelayRoutes {
			// 远程面板发之前已只含启用的路由；这里不再验 Enabled。
			port := twinListenPort(r.ListenAddr)
			if port == 0 {
				continue
			}
			out = append(out, twinRelay{
				remoteAddr: strings.TrimSpace(r.RemoteAddr), remotePort: r.RemotePort,
				udp:  strings.EqualFold(strings.TrimSpace(r.Proto), "udp"),
				host: host, port: port, sortKey: rem.ID + "\x00" + r.ListenAddr,
			})
		}
	}
	// 本地路由（管理面板一般没有，有也纳入；host 取本地 relay 配置
	// 的 RelayAddr，读不到或为空就跳过）。
	s.nodes.mu.Lock()
	localRoutes := append([]BackendRelayRoute(nil), s.nodes.data.Routes...)
	s.nodes.mu.Unlock()
	localHost := ""
	if rc, _, err := s.readRelayConf(); err == nil && rc != nil {
		localHost = twinRelayHost(rc.RelayAddr)
	}
	if localHost != "" {
		for _, r := range localRoutes {
			if !r.Enabled {
				continue
			}
			port := twinListenPort(r.ListenAddr)
			if port == 0 {
				continue
			}
			out = append(out, twinRelay{
				remoteAddr: strings.TrimSpace(r.RemoteAddr), remotePort: r.RemotePort,
				udp: routeIsUDP(r), host: localHost, port: port,
				sortKey: "\x00" + r.ListenAddr,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sortKey < out[j].sortKey })
	return out
}

// twinRelayFor 在候选里找第一条匹配（节点，入站）的中转路由；没有
// 时返回 ok=false。四条全满足才算匹配：目标地址 EqualFold 相等
// （空目标跳过）、目标端口＝入站端口、协议一致（UDP 对 UDP、TCP
// 对 TCP）。候选已排好序，取第一条即顺序确定。
func twinRelayFor(cands []twinRelay, node BackendNode, in NodeInbound) (twinRelay, bool) {
	addr := strings.TrimSpace(node.Address)
	if addr == "" {
		return twinRelay{}, false
	}
	wantUDP := backendInboundIsUDP(in)
	for _, rt := range cands {
		if strings.TrimSpace(rt.remoteAddr) == "" {
			continue
		}
		if !strings.EqualFold(rt.remoteAddr, addr) {
			continue
		}
		if rt.remotePort != in.Port {
			continue
		}
		if rt.udp != wantUDP {
			continue
		}
		return rt, true
	}
	return twinRelay{}, false
}

// backendSubLinksForUser 取某份按人订阅（kind="subuser"/"merged"）在
// 全部后端节点上的订阅链接（只读：不生成 UUID、不写 config、不 bump
// 版本）。调用方是订阅内容生成（serveSubContent 与被管端订阅接口）。
// 节点顺序按节点 id（listNodes 已排好序），同节点内按入站列表顺序。
// v1.4.25 twin：每条（节点，入站）按 LinkMode 出直连（-A）与／或中转
// （-B），同入站先 A 后 B；没有路由的节点照常只出直连；relay 模式
// 但无路由时该入站不出链接。
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
	// 中转路由候选（远程快照＋本地路由，已排序；只读，不发请求）。
	relays := s.twinRelayCandidates()
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
		mode := linkModeOf(&node)
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
			// twin（v1.4.25）：同入站先 A（直连）后 B（中转）。
			if mode != "relay" {
				if l, ok := backendInboundLink(node, in, uuid); ok {
					out = append(out, l)
				}
			}
			if mode != "direct" {
				if rt, ok := twinRelayFor(relays, node, in); ok {
					if l, ok := backendInboundRelayLink(node, in, uuid, rt.host, rt.port); ok {
						out = append(out, l)
					}
				}
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
