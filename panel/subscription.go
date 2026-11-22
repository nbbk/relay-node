package main

// ---------- 订阅链接（v1.2.7） ----------
// 机场式订阅：一个链接导入，客户端一次拿到全部节点。
//   - 单面板订阅：GET /sub/:token，内容是本面板全部落地的 twin 链接，实时生成，
//     面板里增删节点后订阅内容自动跟着变，不用重新导出。
//   - 合并订阅：主面板按勾选的落地集合（本面板 + 多台远程面板）生成，每份选择
//     一条令牌，不同选择绝不共用一个令牌；内容同样实时生成（远程部分当场去
//     远程面板取，取不到的那台暂时跳过，下次拉取时自动恢复）。
//   - 格式：?format=clash|base64|plain|singbox 手动指定优先；不指定时按
//     User-Agent 自动识别——含 clash/stash/mihomo（不区分大小写）返回
//     Clash YAML，含 sing-box/SFA/SFI/SFM 返回 sing-box JSON，
//     其余返回 base64 节点列表。sing-box 系里 Hiddify/Karing/NekoBox
//     的 UA 没法嗅探，靠界面上单独给的 ?format=singbox 链接。
//   - 模式（v1.2.9，只影响 Clash YAML）：?mode=split|global，缺省 split；
//     split 是分流（TikTok/社交/流媒体走节点，其他直连），global 是全局
//     （全部走代理）；非法 mode 返回 400，与非法 format 的处理一致。
//     sing-box JSON 不认 mode，始终按分流出（route final direct）。
// 安全要点：
//   - 订阅令牌存 SHA-256 哈希（鉴权查表）与 AES-GCM 密文（v1.3.7，
//     密钥在 PANEL_HOME/sub_token.key，重新拼链接用）；1.3.7 之前生成的
//     没有密文，复制时提示轮换一次。令牌泄露可一键轮换，旧链接立即失效；
//     撤销后订阅立即 404。
//   - /sub/ 限流（v1.3.7）：同一 IP 10 次失败锁 10 分钟，返回 429；
//     格式不对的令牌直接 404 不查库；对的令牌永远放行，不受锁影响。
//   - 订阅端点只返回节点链接，不泄露面板其他信息；令牌不对返回 404
//     （不返回 401，避免暴露端点存在）。
//   - /sub/ 不走 IP 白名单（订阅客户端从任意 IP 拉取，令牌本身就是鉴权），
//     见 ipWhitelistMiddleware 的豁免。

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const subTokenPrefix = "sub_"

// generateSubToken 生成订阅令牌：sub_ + 64 位随机十六进制（32 字节随机数）。
func generateSubToken() string {
	return subTokenPrefix + randomTokenHex(32)
}

// hashSubToken 订阅令牌的 SHA-256 哈希（十六进制），这是面板唯一存的东西。
func hashSubToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// verifySubToken 校验订阅令牌：同样算 SHA-256，再常数时间比较，防时序攻击。
func verifySubToken(tok, hash string) bool {
	if tok == "" || hash == "" {
		return false
	}
	sum := sha256.Sum256([]byte(tok))
	want, err := hex.DecodeString(hash)
	if err != nil || len(want) != len(sum) {
		return false
	}
	return subtle.ConstantTimeCompare(sum[:], want) == 1
}

// subLink 订阅里的一条节点：Name 是客户端里显示的名字（沿用 twin 备注），
// Link 是完整的节点链接。LandingAddr 是这条节点所属落地的地址（内部用，
// 按人转发改写端口时定位，不序列化进 JSON）。
type subLink struct {
	Name        string `json:"name"`
	Link        string `json:"link"`
	LandingAddr string `json:"-"`
}

// ---------- 节点链接来源 ----------

// loadLinkGroups 读 nodes.txt 的分组（没有就跑 install.sh --nodes 补生成一次，
// 与 handleLinks 同一个来源），并按 relay.conf 校正模式与名称。
// handleLinks 与订阅都走这里，不再各写一份。
func (s *Server) loadLinkGroups() ([]linkGroup, error) {
	if _, err := os.Stat(s.nodesFilePath()); err != nil {
		_, _ = s.runScript("install.sh", 120*time.Second, nil, "--nodes")
	}
	b, err := os.ReadFile(s.nodesFilePath())
	if err != nil {
		return nil, err
	}
	groups := parseNodesTxt(b)
	if groups == nil {
		groups = []linkGroup{}
	}
	modeByIndex := map[int]string{}
	nameByIndex := map[int]string{}
	if rc, _, err := s.readRelayConf(); err == nil {
		for _, l := range rc.Landings {
			modeByIndex[l.Index] = l.Mode
			nameByIndex[l.Index] = l.displayName()
		}
	}
	for i := range groups {
		if m, ok := modeByIndex[groups[i].LandingIndex]; ok && m != "" {
			groups[i].Mode = m
		}
		if n, ok := nameByIndex[groups[i].LandingIndex]; ok && n != "" {
			groups[i].Name = n
		} else {
			groups[i].Name = groups[i].Landing
		}
	}
	return groups, nil
}

// linkDisplayName 取一条节点链接在客户端里显示的名字：优先用链接自己的备注
// （vless/trojan 的 #fragment、vmess 的 ps），都没有就用 fallback
// （一般是“落地名-中转/直连”）。
func linkDisplayName(link, fallback string) string {
	if pl, err := parseNodeLink(link); err == nil && pl.Name != "" {
		return pl.Name
	}
	if u, err := url.Parse(strings.TrimSpace(link)); err == nil && u.Fragment != "" {
		return u.Fragment
	}
	return fallback
}

// collectSubLinks 取订阅用的节点链接：nodes.txt 里的 twin 链接（中转+直连都含）。
// landingIndices 为 nil 取全部落地；否则只取指定编号的落地。
func (s *Server) collectSubLinks(landingIndices map[int]bool) []subLink {
	groups, err := s.loadLinkGroups()
	if err != nil {
		return nil
	}
	var out []subLink
	for _, g := range groups {
		if landingIndices != nil && !landingIndices[g.LandingIndex] {
			continue
		}
		for _, l := range g.Links {
			name := linkDisplayName(l.Link, g.Name+"-"+l.Label)
			out = append(out, subLink{Name: name, Link: l.Link, LandingAddr: g.Landing})
		}
	}
	return out
}

// ---------- 订阅内容范围三选一（v1.3.17） ----------
// 模式存在令牌记录上（通用订阅是 cfg.SubTokenMode，SubUser/MergedSub 是
// 各自的 Mode 字段），/sub/ 服务时按它过滤链接再渲染；与 ?mode=split|global
// 的分流模式无关（那个只改 Clash 规则的 MATCH 走向，不改链接集合）。
// 值：both=全部（中转+直连，默认）、relay=仅中转、direct=仅直连。
// 老记录没有该字段一律按 both，已发出去的订阅链接行为不变。

// normalizeSubMode 校验创建表单传来的模式：空 = 默认 both；非法值报错。
func normalizeSubMode(m string) (string, error) {
	switch strings.TrimSpace(m) {
	case "", "both":
		return "both", nil
	case "relay", "direct":
		return strings.TrimSpace(m), nil
	default:
		return "", fmt.Errorf("生成方式只支持 全部/仅中转/仅直连")
	}
}

// subModeLabel 模式值写成人话（列表与弹窗里显示用）。
func subModeLabel(m string) string {
	switch m {
	case "relay":
		return "仅中转"
	case "direct":
		return "仅直连"
	default:
		return "全部"
	}
}

// filterSubLinksByKind 按内容范围过滤已收集的订阅链接：仅中转留名字以
// “-中转”结尾的，仅直连留以“-直连”结尾的；名字没有这两种后缀的（手工
// 改过备注的节点）两边都留着，不丢数据。both/空 = 不过滤。
func filterSubLinksByKind(links []subLink, kind string) []subLink {
	if kind == "" || kind == "both" {
		return links
	}
	out := []subLink{}
	for _, l := range links {
		switch kind {
		case "relay":
			if strings.HasSuffix(l.Name, "-中转") || (!strings.HasSuffix(l.Name, "-中转") && !strings.HasSuffix(l.Name, "-直连")) {
				out = append(out, l)
			}
		case "direct":
			if strings.HasSuffix(l.Name, "-直连") || (!strings.HasSuffix(l.Name, "-中转") && !strings.HasSuffix(l.Name, "-直连")) {
				out = append(out, l)
			}
		default:
			out = append(out, l)
		}
	}
	return out
}

// ---------- 订阅内容渲染 ----------

// dedupSubLinks 同一条链接只留一次（保持原顺序）。
func dedupSubLinks(links []subLink) []subLink {
	seen := map[string]bool{}
	out := []subLink{}
	for _, l := range links {
		if l.Link == "" || seen[l.Link] {
			continue
		}
		seen[l.Link] = true
		out = append(out, l)
	}
	return out
}

// renderSubContent 把节点链接渲染成订阅内容：plain=明文列表，
// base64=标准 base64 编码的列表，clash=Clash YAML（dns 段 + proxies +
// 策略组 + 分流规则），singbox=sing-box 完整配置 JSON。mode 只影响
// clash（split 分流 / global 全局），base64/plain/singbox 原样不动
// （sing-box JSON 始终按分流出）；format 不合法返回错误。
func renderSubContent(links []subLink, format, mode string) (string, string, error) {
	links = dedupSubLinks(links)
	switch format {
	case "plain":
		var lines []string
		for _, l := range links {
			lines = append(lines, l.Link)
		}
		return strings.Join(lines, "\n") + "\n", "text/plain; charset=utf-8", nil
	case "base64":
		var lines []string
		for _, l := range links {
			lines = append(lines, l.Link)
		}
		return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n") + "\n")), "text/plain; charset=utf-8", nil
	case "clash":
		return renderClashYAML(links, mode), "text/yaml; charset=utf-8", nil
	case "singbox":
		return renderSingboxJSON(links), "application/json; charset=utf-8", nil
	default:
		return "", "", fmt.Errorf("format 只支持 clash/base64/plain/singbox")
	}
}

// singboxOutboundFromLink 把一条节点链接转成 sing-box outbound
// （map 形式，由 renderSingboxJSON 统一 Marshal）。支持 vless /
// trojan / vmess / ss；hy2/tuic 暂不支持，跳过。
func singboxOutboundFromLink(l subLink) (map[string]any, bool) {
	u, err := url.Parse(strings.TrimSpace(l.Link))
	if err != nil || u.Scheme == "" {
		return nil, false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "ss" {
		cipher, pw, h, p, ok := parseSSLink(l.Link)
		if !ok {
			return nil, false
		}
		return map[string]any{
			"type": "shadowsocks", "tag": l.Name,
			"server": h, "server_port": p,
			"method": cipher, "password": pw,
		}, true
	}
	q := u.Query()
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if host == "" || port <= 0 {
		return nil, false
	}
	tlsOn := strings.EqualFold(q.Get("security"), "tls")
	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("host")
	}
	network := q.Get("type")
	if network == "" {
		network = "ws"
	}
	path := q.Get("path")
	wsHost := q.Get("host")
	var ob map[string]any
	switch scheme {
	case "vless":
		uuid := ""
		if u.User != nil {
			uuid = u.User.Username()
		}
		if uuid == "" {
			return nil, false
		}
		ob = map[string]any{"type": "vless", "tag": l.Name, "server": host, "server_port": port, "uuid": uuid}
	case "trojan":
		pw := ""
		if u.User != nil {
			pw = u.User.Username()
		}
		if pw == "" {
			return nil, false
		}
		ob = map[string]any{"type": "trojan", "tag": l.Name, "server": host, "server_port": port, "password": pw}
	case "vmess":
		raw, jerr := decodeVmessLink(l.Link)
		if jerr != nil {
			return nil, false
		}
		p, _ := strconv.Atoi(raw["port"])
		if raw["add"] == "" || p <= 0 || raw["id"] == "" {
			return nil, false
		}
		ob = map[string]any{"type": "vmess", "tag": l.Name, "server": raw["add"], "server_port": p, "uuid": raw["id"], "alter_id": 0, "security": "auto"}
		tlsOn = raw["tls"] == "tls"
		if raw["sni"] != "" {
			sni = raw["sni"]
		} else if raw["host"] != "" {
			sni = raw["host"]
		}
		network = raw["net"]
		if network == "" {
			network = "ws"
		}
		path = raw["path"]
		wsHost = raw["host"]
	default:
		return nil, false
	}
	if tlsOn {
		tls := map[string]any{"enabled": true}
		if sni != "" {
			tls["server_name"] = sni
		}
		ob["tls"] = tls
	}
	if network == "ws" {
		tr := map[string]any{"type": "ws"}
		if path != "" {
			tr["path"] = path
		}
		if wsHost != "" {
			tr["headers"] = map[string]any{"Host": wsHost}
		}
		ob["transport"] = tr
	}
	return ob, true
}

// renderSingboxJSON 渲染 sing-box 订阅：完整 sing-box 配置 JSON
// （dns + mixed 入站 + 节点 outbounds + selector/urltest + route 规则）。
// DNS 查询本身可以走 detour：国内域名走直连 DoH，TikTok 等国外域名走
// 代理 DoH（detour: proxy），这是 sing-box 比 mihomo 优越的地方。
// 入站只放 mixed（官方客户端自己会按需加 tun，订阅里不强加 tun，避免
// 与客户端的 TUN 设置冲突）。route 里 TikTok/社交/流媒体走 Proxy 组，
// .cn 直连，其余 final direct。mode 参数不影响 sing-box（始终分流）。
// 节点为空时只出合法的空配置（dns + mixed 入站 + direct/block，route
// 里不引用不存在的 Proxy 组），客户端能正常导入、节点数为 0。
func renderSingboxJSON(links []subLink) string {
	used := map[string]int{}
	var tags []string
	var nodeObs []map[string]any
	directTag := ""
	for _, l := range links {
		name := l.Name
		if n, ok := used[name]; ok {
			used[name] = n + 1
			name = fmt.Sprintf("%s (%d)", l.Name, n+1)
		} else {
			used[name] = 1
		}
		ob, ok := singboxOutboundFromLink(subLink{Name: name, Link: l.Link})
		if !ok {
			continue
		}
		tags = append(tags, name)
		nodeObs = append(nodeObs, ob)
		if directTag == "" && strings.HasSuffix(l.Name, "-直连") {
			directTag = name
		}
	}
	cfg := map[string]any{
		"log": map[string]any{"level": "info"},
		"dns": map[string]any{
			"servers": []any{
				map[string]any{"tag": "dns-direct", "address": "https://doh.pub/dns-query", "detour": "direct"},
				map[string]any{"tag": "dns-proxy", "address": "https://dns.google/dns-query", "detour": "proxy"},
			},
			"rules": []any{
				map[string]any{"domain_suffix": []string{".cn"}, "server": "dns-direct"},
				map[string]any{"domain_suffix": tiktokSuffixes, "server": "dns-proxy"},
				map[string]any{"domain_suffix": ipCheckSuffixes, "server": "dns-proxy"},
			},
			"final": "dns-direct",
		},
		"inbounds": []any{
			map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080},
		},
	}
	outbounds := []any{}
	routeRules := []any{}
	if len(tags) == 0 {
		outbounds = append(outbounds,
			map[string]any{"type": "direct", "tag": "direct"},
			map[string]any{"type": "block", "tag": "block"},
		)
		routeRules = append(routeRules,
			map[string]any{"domain_suffix": []string{".cn"}, "outbound": "direct"},
		)
	} else {
		for _, ob := range nodeObs {
			outbounds = append(outbounds, ob)
		}
		def := directTag
		if def == "" {
			def = tags[0]
		}
		outbounds = append(outbounds,
			map[string]any{"type": "selector", "tag": "Proxy", "outbounds": tags, "default": def},
			map[string]any{"type": "urltest", "tag": "自动选择", "outbounds": tags, "url": "http://www.gstatic.com/generate_204", "interval": "5m"},
			map[string]any{"type": "direct", "tag": "direct"},
			map[string]any{"type": "block", "tag": "block"},
		)
		routeRules = append(routeRules,
			map[string]any{"domain_suffix": tiktokSuffixes, "outbound": "Proxy"},
			map[string]any{"domain_suffix": socialSuffixes, "outbound": "Proxy"},
			map[string]any{"domain_suffix": streamSuffixes, "outbound": "Proxy"},
			map[string]any{"domain_suffix": ipCheckSuffixes, "outbound": "Proxy"},
			map[string]any{"domain_suffix": []string{".cn"}, "outbound": "direct"},
		)
	}
	cfg["outbounds"] = outbounds
	cfg["route"] = map[string]any{"rules": routeRules, "final": "direct"}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(raw) + "\n"
}

// yamlDQ 把字符串包成 YAML 双引号标量（strconv.Quote 的转义在 YAML 双引号
// 里同样合法，中文原样保留）。
func yamlDQ(s string) string {
	return strconv.Quote(s)
}

// clashProxyFromLink 把一条节点链接转成一个 Clash proxy 条目（YAML 行块）。
// 支持 vless / trojan / vmess / ss；hy2/tuic 暂不支持，在 clash 里跳过
// （base64/plain 订阅里还在）。所有条目都写 udp: true——mihomo 的 udp
// 默认 false，不写等于禁用 UDP 中继，TikTok 大量走的 QUIC 就不通。
func clashProxyFromLink(l subLink) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(l.Link))
	if err != nil || u.Scheme == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "ss" {
		// ss 链接有整体 base64 的写法（ss://<base64(method:password@host:port)>），
		// 按 URL 解析拿不到 host/port，走 parseSSLink 单独解析。
		cipher, pw, h, p, ok := parseSSLink(l.Link)
		if !ok {
			return "", false
		}
		var sb strings.Builder
		sb.WriteString("  - name: " + yamlDQ(l.Name) + "\n")
		sb.WriteString("    type: ss\n")
		sb.WriteString("    server: " + yamlDQ(h) + "\n")
		sb.WriteString("    port: " + strconv.Itoa(p) + "\n")
		sb.WriteString("    cipher: " + yamlDQ(cipher) + "\n")
		sb.WriteString("    password: " + yamlDQ(pw) + "\n")
		sb.WriteString("    udp: true\n")
		return sb.String(), true
	}
	q := u.Query()
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if host == "" || port <= 0 {
		return "", false
	}
	tlsOn := strings.EqualFold(q.Get("security"), "tls")
	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("host")
	}
	network := q.Get("type")
	if network == "" {
		network = "ws"
	}
	path := q.Get("path")
	wsHost := q.Get("host")

	var b strings.Builder
	b.WriteString("  - name: " + yamlDQ(l.Name) + "\n")
	switch scheme {
	case "vless":
		uuid := ""
		if u.User != nil {
			uuid = u.User.Username()
		}
		if uuid == "" {
			return "", false
		}
		b.WriteString("    type: vless\n")
		b.WriteString("    server: " + yamlDQ(host) + "\n")
		b.WriteString("    port: " + strconv.Itoa(port) + "\n")
		b.WriteString("    uuid: " + yamlDQ(uuid) + "\n")
	case "trojan":
		pw := ""
		if u.User != nil {
			pw = u.User.Username()
		}
		if pw == "" {
			return "", false
		}
		b.WriteString("    type: trojan\n")
		b.WriteString("    server: " + yamlDQ(host) + "\n")
		b.WriteString("    port: " + strconv.Itoa(port) + "\n")
		b.WriteString("    password: " + yamlDQ(pw) + "\n")
	case "vmess":
		raw, jerr := decodeVmessLink(l.Link)
		if jerr != nil {
			return "", false
		}
		b.WriteString("    type: vmess\n")
		b.WriteString("    server: " + yamlDQ(raw["add"]) + "\n")
		p, _ := strconv.Atoi(raw["port"])
		if p <= 0 {
			return "", false
		}
		b.WriteString("    port: " + strconv.Itoa(p) + "\n")
		b.WriteString("    uuid: " + yamlDQ(raw["id"]) + "\n")
		b.WriteString("    alterId: 0\n")
		b.WriteString("    cipher: auto\n")
		tlsOn = raw["tls"] == "tls"
		if raw["sni"] != "" {
			sni = raw["sni"]
		} else if raw["host"] != "" {
			sni = raw["host"]
		}
		network = raw["net"]
		if network == "" {
			network = "ws"
		}
		path = raw["path"]
		wsHost = raw["host"]
	default:
		return "", false
	}
	b.WriteString("    udp: true\n")
	if tlsOn {
		b.WriteString("    tls: true\n")
		if sni != "" {
			b.WriteString("    servername: " + yamlDQ(sni) + "\n")
		}
	} else {
		b.WriteString("    tls: false\n")
	}
	b.WriteString("    network: " + yamlDQ(network) + "\n")
	if network == "ws" {
		b.WriteString("    ws-opts:\n")
		if path != "" {
			b.WriteString("      path: " + yamlDQ(path) + "\n")
		}
		if wsHost != "" {
			b.WriteString("      headers:\n")
			b.WriteString("        Host: " + yamlDQ(wsHost) + "\n")
		}
	}
	return b.String(), true
}

// decodeVmessLink 解出 vmess://<base64 JSON> 里的字段（字符串值）。
func decodeVmessLink(link string) (map[string]string, error) {
	b64raw := strings.TrimPrefix(strings.TrimSpace(link), "vmess://")
	b64raw = strings.SplitN(b64raw, "#", 2)[0]
	b64raw = strings.SplitN(b64raw, "?", 2)[0]
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(b64raw, "="))
	if err != nil {
		return nil, fmt.Errorf("vmess 链接解码失败")
	}
	var j map[string]any
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("vmess 链接解码失败")
	}
	out := map[string]string{}
	for k, v := range j {
		switch t := v.(type) {
		case string:
			out[k] = t
		case float64:
			out[k] = strconv.Itoa(int(t))
		}
	}
	return out, nil
}

// parseSSLink 解析 ss:// 链接，支持两种常见写法：
// ① ss://<base64(method:password)>@host:port#name
// ② ss://<base64(method:password@host:port)>#name
// （以及明文的 ss://method:password@host:port）。
// 返回 cipher、password、host、port；解析失败返回 ok=false。
func parseSSLink(link string) (cipher, password, host string, port int, ok bool) {
	s := strings.TrimSpace(link)
	s = strings.TrimPrefix(s, "ss://")
	if i := strings.Index(s, "#"); i >= 0 {
		s = s[:i]
	}
	// 先试整体 base64 解码（写法 ②）：含 @ : 等字符的写法 ① 在这里解码失败，
	// 自然落到下面的 URL 解析。
	if dec, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "=")); err == nil {
		if c, p, h, pt, ok2 := parseSSPlain(string(dec)); ok2 {
			return c, p, h, pt, true
		}
	}
	u, err := url.Parse("ss://" + s)
	if err != nil || u.Host == "" || u.User == nil {
		return "", "", "", 0, false
	}
	host = u.Hostname()
	port, _ = strconv.Atoi(u.Port())
	if host == "" || port <= 0 {
		return "", "", "", 0, false
	}
	ui := u.User.Username()
	if pw, hasPw := u.User.Password(); hasPw {
		// 明文写法 ss://method:password@host:port
		return ui, pw, host, port, true
	}
	// userinfo 是 base64(method:password)
	dec, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(ui, "="))
	if err != nil {
		return "", "", "", 0, false
	}
	if i := strings.Index(string(dec), ":"); i > 0 {
		return string(dec)[:i], string(dec)[i+1:], host, port, true
	}
	return "", "", "", 0, false
}

// parseSSPlain 解析 "method:password@host:port" 明文（写法 ② 解码后的内容）。
func parseSSPlain(s string) (cipher, password, host string, port int, ok bool) {
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return "", "", "", 0, false
	}
	up, hp := s[:at], s[at+1:]
	i := strings.Index(up, ":")
	if i <= 0 {
		return "", "", "", 0, false
	}
	h, p, err := net.SplitHostPort(hp)
	if err != nil {
		return "", "", "", 0, false
	}
	port, _ = strconv.Atoi(p)
	if h == "" || port <= 0 {
		return "", "", "", 0, false
	}
	return up[:i], up[i+1:], h, port, true
}

// subLinkDomains 从一条节点链接里收集节点域名（servername/host）：
// 用于 Clash YAML 的 fake-ip-filter 与 sniffer skip-domain。
// 只收非 IP 的域名；IP 直连的节点没有域名可写，跳过。返回去重后的域名。
func subLinkDomains(link string) []string {
	var out []string
	add := func(d string) {
		d = strings.TrimSpace(strings.ToLower(d))
		if d == "" || net.ParseIP(d) != nil {
			return
		}
		if h, _, err := net.SplitHostPort(d); err == nil {
			d = h
		}
		for _, e := range out {
			if e == d {
				return
			}
		}
		out = append(out, d)
	}
	trimmed := strings.TrimSpace(link)
	scheme := strings.ToLower(strings.SplitN(trimmed, "://", 2)[0])
	switch scheme {
	case "vmess":
		raw, err := decodeVmessLink(link)
		if err != nil {
			return out
		}
		add(raw["add"])
		add(raw["sni"])
		add(raw["host"])
	case "ss":
		if _, _, h, _, ok := parseSSLink(link); ok {
			add(h)
		}
	default:
		u, err := url.Parse(trimmed)
		if err != nil {
			return out
		}
		add(u.Hostname())
		add(u.Query().Get("sni"))
		add(u.Query().Get("host"))
	}
	return out
}

// ---------- Clash 策略组与分流规则（v1.2.9，v1.3.1 补 GLOBAL 组，v1.3.4 改顺序） ----------
// Clash YAML 订阅出 7 个策略组：自动选择（url-test，全部节点，排第一，
// 每 5 分钟测一次挑延迟最低）、GLOBAL（select，全部节点，排第二，
// 客户端切全局模式时走它）、手动选择（select，全部节点，默认入口）、
// 中转节点（select，只有中转链接）、直连节点（select，只有直连链接）、
// 社交（select，全部节点）、流媒体（select，全部节点）。中转/直连按 twin
// 名字后缀 -中转/-直连 分组；没有中转或直连链接时对应组不出现（不出空组）。
// 规则顺序：TikTok → 社交 → 流媒体 → 测 IP → MATCH；TikTok/社交/流媒体/
// 测 IP 四组规则都直接指向自动选择（url-test 组，死节点自动排除——v1.3.23 从手动选择改过来：
// 手动选择组的默认成员是列表第一个节点，中转节点已死时会把分流流量全拖死，
// ?mode=global 同样落到它，也救不了）。社交/流媒体组保留（select，全部节点），
// 供用户在客户端里手动切换，但规则不再经过它们。
// 分流模式 MATCH,DIRECT（只有这三类走节点，其他直连），全局模式
// MATCH,手动选择（全部走代理）。手动选择与 GLOBAL 组保留，供手动切换。
// YAML 顶部（v1.3.23）补 dns 段（fake-ip，国内可达的 DoH）+ ipv6: false
// + mode: rule + sniffer：没有 dns 段时 mihomo 内核 DNS 是关的，TUN 抓到
// 的流量没有域名→IP 映射，域名规则匹配不上，全落到 MATCH,DIRECT，国外
// 流量根本进不了代理（2026-11-20 晚 FlClash 故障的根因）。dns 段里的
// 节点域名（fake-ip-filter 与 sniffer skip-domain）从订阅里实际的节点
// 域名动态生成，不写死任何域名。

// tiktokSuffixes TikTok 分流域名：主站、App 接口/视频流/日志上报、
// 视频 CDN、老域名与字节海外基础设施。
var tiktokSuffixes = []string{
	"tiktok.com", "tiktokv.com", "tiktokcdn.com", "tiktokcdn-us.com",
	"musical.ly", "byteoversea.com", "ibytedtos.com", "isnssdk.com",
}

// socialSuffixes 海外社交分流域名。只用 DOMAIN-SUFFIX，不用
// DOMAIN-KEYWORD——x 这类短词做 keyword 会误伤。
var socialSuffixes = []string{
	"facebook.com", "fb.com", "fbcdn.net", "instagram.com", "cdninstagram.com",
	"x.com", "twitter.com", "twimg.com", "whatsapp.com", "whatsapp.net",
	"telegram.org", "t.me", "snapchat.com", "scdn.co", "reddit.com",
	"redditmedia.com", "redditstatic.com", "pinterest.com", "pinimg.com",
	"linkedin.com", "licdn.com", "discord.com", "discordapp.com", "discord.gg",
	"threads.net", "line.me",
}

// streamSuffixes 海外流媒体分流域名。只用 DOMAIN-SUFFIX；苹果只写
// tv.apple.com，不写 apple.com，否则会把全部苹果流量都拖进来。
var streamSuffixes = []string{
	"youtube.com", "googlevideo.com", "ytimg.com", "youtu.be", "netflix.com",
	"nflxvideo.net", "nflxext.com", "nflxso.net", "disneyplus.com", "dssott.com",
	"hulu.com", "huluim.com", "max.com", "hbomax.com", "hbo.com",
	"primevideo.com", "amazonvideo.com", "aiv-cdn.net", "tv.apple.com",
	"spotify.com", "scdn.co", "spotifycdn.com", "twitch.tv", "twitchcdn.net",
	"jtvnw.net",
}

// ipCheckSuffixes 各主流客户端“测 IP / 网络检测”用的查 IP 服务域名
// （v1.3.24）：规则模式下这些请求本来走 DIRECT，在 fake-ip 下失败，
// 检测显示失败；加规则指自动选择后走代理，显示代理出口 IP。
// 域名均已从源码或官方文档核实（只写确认了的，没确认的不写）：
//   - FlClash（chen08209/FlClash，lib/common/service_probe.dart 的
//     ipInfoSources，8 个源竞速）：www.cloudflare.com、api.ip.sb、
//     ipinfo.io、ipwho.is、get.geojs.io、api.country.is、
//     api.ipquery.io、ident.me；
//   - Clash Verge Rev（官方 PRIVACY.md §4.5，IP 信息卡随机选一个）：
//     api.ip.sb、ipapi.co、api.ipapi.is、ipwho.is、ip.api.skk.moe、
//     get.geojs.io；
//   - Hiddify（hiddify-app，lib/features/proxy/data/
//     proxy_repository.dart 的 _ipInfoSources，proxyOnly: true 强制走
//     代理）：ipwho.is、api.ip.sb、ipapi.co、ipinfo.io；
//   - Clash Party（原 Mihomo Party，调研代理从源码核实）：api.internetdata.io。
//     注意：它的 IP 查询跟随系统代理（Electron 默认 session），只有开了
//     系统代理或 TUN 时订阅规则才管用，两者都关仍直连——客户端实现决定，
//     订阅规则解决不了。
//   - 补 3 个（v1.3.25）：ifconfig.co（Hiddify 当前 main 第 5 个源）、
//     api.myip.com 与 ip-api.com（FlClash 旧版 v0.8.88–0.8.93 旧表独有）。
//
// 未确认的不写进规则：Stash、Shadowrocket、Surge、Quantumult X、Loon、
// Clash 官方 iOS 客户端（ProjectClash/Clash-Client）、Karing、SFA、
// NekoBox——官方文档/源码里没找到内置查 IP 服务的域名；v2rayNG 吃
// base64 订阅、测 IP 走代理（api.ip.sb/geoip），不需要这边配合。
// 注意 www.cloudflare.com 是 FlClash 检测的首选源（cdn-cgi/trace），
// 域名规则只覆盖 www.cloudflare.com，不影响其他 Cloudflare 域名。
var ipCheckSuffixes = []string{
	"www.cloudflare.com", "api.ip.sb", "ipinfo.io", "ipwho.is",
	"get.geojs.io", "api.country.is", "api.ipquery.io", "ident.me",
	"ipapi.co", "api.ipapi.is", "ip.api.skk.moe", "api.internetdata.io",
	"ifconfig.co", "api.myip.com", "ip-api.com",
}

// renderClashHeader 渲染 Clash YAML 顶部的 dns / ipv6 / mode / sniffer 段
// （v1.3.23）。domains 是订阅里实际的节点域名（已去重、已排序），只用来
// 生成 fake-ip-filter 与 sniffer skip-domain 的条目，不写死任何域名。
func renderClashHeader(domains []string) string {
	var b strings.Builder
	b.WriteString("dns:\n")
	b.WriteString("  enable: true\n")
	b.WriteString("  listen: 0.0.0.0:1053\n")
	b.WriteString("  ipv6: false\n")
	b.WriteString("  enhanced-mode: fake-ip\n")
	b.WriteString("  fake-ip-range: 198.18.0.1/16\n")
	b.WriteString("  use-hosts: true\n")
	b.WriteString("  default-nameserver:\n")
	b.WriteString("    - 223.5.5.5\n")
	b.WriteString("    - 114.114.114.114\n")
	b.WriteString("  nameserver:\n")
	b.WriteString("    - https://doh.pub/dns-query\n")
	b.WriteString("    - https://dns.alidns.com/dns-query\n")
	b.WriteString("  proxy-server-nameserver:\n")
	b.WriteString("    - https://doh.pub/dns-query\n")
	b.WriteString("  fallback:\n")
	b.WriteString("    - https://dns.google/dns-query\n")
	b.WriteString("    - https://1.1.1.1/dns-query\n")
	b.WriteString("  fallback-filter:\n")
	b.WriteString("    geoip: true\n")
	b.WriteString("    geoip-code: CN\n")
	b.WriteString("    ipcidr:\n")
	b.WriteString("      - 240.0.0.0/4\n")
	b.WriteString("      - 0.0.0.0/32\n")
	b.WriteString("  fake-ip-filter:\n")
	for _, e := range []string{"*.lan", "*.local", "*.localhost", "time.windows.com", "time.apple.com", "time.asia.apple.com", "ntp.*.com"} {
		b.WriteString("    - " + yamlDQ(e) + "\n")
	}
	for _, d := range domains {
		b.WriteString("    - " + yamlDQ("*."+d) + "\n")
		b.WriteString("    - " + yamlDQ(d) + "\n")
	}
	b.WriteString("ipv6: false\n")
	b.WriteString("mode: rule\n")
	b.WriteString("sniffer:\n")
	b.WriteString("  enable: true\n")
	b.WriteString("  sniff:\n")
	b.WriteString("    TLS:\n")
	b.WriteString("      ports: [443, 8443]\n")
	b.WriteString("    HTTP:\n")
	b.WriteString("      ports: [80, 8080]\n")
	if len(domains) > 0 {
		b.WriteString("  skip-domain:\n")
		for _, d := range domains {
			b.WriteString("    - " + yamlDQ("*."+d) + "\n")
		}
	}
	return b.String()
}

// renderClashYAML 渲染 Clash 订阅：dns 段 + proxies 列表 + 7 个策略组 +
// 分流规则。名字去重（Clash 要求 proxy 名唯一），不支持的协议跳过；mode 是
// split（分流，MATCH,DIRECT）或 global（全局，MATCH,手动选择）。
// GLOBAL 组只在客户端切全局模式时生效，规则模式下只看 rules，不走它。
// 节点为空时（v1.3.22 修）：订阅里没有任何能进 Clash 的节点（节点为空，
// 或全是 Clash 不支持的类型如 Hysteria2/TUIC）时，不出任何策略组——
// Clash 要求每个组必须有非空 proxies 或 use，空组会让导入直接报错
// “proxy group[0]: 'use' or 'proxies' missing”。此时只出一个合法的
// 空配置（proxies: []、proxy-groups: []、规则只留 MATCH,DIRECT，不引用
// 不存在的组），客户端能正常导入、节点数为 0，而不是报一串看不懂的错。
// 空配置保持 1.3.22 原样（不写 dns 段），不要动它。
func renderClashYAML(links []subLink, mode string) string {
	var proxies strings.Builder
	used := map[string]int{}
	var names []string
	var kinds []string // 与 names 一一对应：relay / direct / ""（无后缀）
	domainSet := map[string]bool{}
	for _, l := range links {
		kind := ""
		switch {
		case strings.HasSuffix(l.Name, "-中转"):
			kind = "relay"
		case strings.HasSuffix(l.Name, "-直连"):
			kind = "direct"
		}
		name := l.Name
		if n, ok := used[name]; ok {
			used[name] = n + 1
			name = fmt.Sprintf("%s (%d)", l.Name, n+1)
		} else {
			used[name] = 1
		}
		proxy, ok := clashProxyFromLink(subLink{Name: name, Link: l.Link})
		if !ok {
			continue
		}
		names = append(names, name)
		kinds = append(kinds, kind)
		proxies.WriteString(proxy)
		for _, d := range subLinkDomains(l.Link) {
			domainSet[d] = true
		}
	}
	if len(names) == 0 {
		return "proxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n"
	}
	domains := make([]string, 0, len(domainSet))
	for d := range domainSet {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	var b strings.Builder
	b.WriteString(renderClashHeader(domains))
	b.WriteString("proxies:\n")
	b.WriteString(proxies.String())
	var relayNames, directNames []string
	for i, n := range names {
		switch kinds[i] {
		case "relay":
			relayNames = append(relayNames, n)
		case "direct":
			directNames = append(directNames, n)
		}
	}
	writeGroup := func(name, typ string, members []string) {
		b.WriteString("  - name: " + yamlDQ(name) + "\n")
		b.WriteString("    type: " + typ + "\n")
		if typ == "url-test" {
			b.WriteString("    url: \"http://www.gstatic.com/generate_204\"\n")
			b.WriteString("    interval: 300\n")
		}
		b.WriteString("    proxies:\n")
		for _, n := range members {
			b.WriteString("      - " + yamlDQ(n) + "\n")
		}
	}
	b.WriteString("proxy-groups:\n")
	// 自动选择排第一：客户端默认进这个组挑延迟最低；GLOBAL 排第二：
	// 客户端切全局模式时 Clash 按名字找 GLOBAL 组走全部流量（顺序不影响
	// 它，只影响默认显示）；成员与手动选择一致（只有节点名，不含自动选择组）。
	writeGroup("自动选择", "url-test", names)
	writeGroup("GLOBAL", "select", names)
	writeGroup("手动选择", "select", names)
	if len(relayNames) > 0 {
		writeGroup("中转节点", "select", relayNames)
	}
	if len(directNames) > 0 {
		writeGroup("直连节点", "select", directNames)
	}
	writeGroup("社交", "select", names)
	writeGroup("流媒体", "select", names)
	// 分流规则：TikTok → 社交 → 流媒体 → 测 IP → MATCH（先命中先走）。
	b.WriteString("rules:\n")
	seenSuffix := map[string]bool{}
	writeSuffixRules := func(suffixes []string, group string) {
		for _, d := range suffixes {
			if seenSuffix[d] {
				continue // scdn.co 在社交与流媒体清单里都有，只出一次（先命中社交组）
			}
			seenSuffix[d] = true
			b.WriteString("  - DOMAIN-SUFFIX," + d + "," + group + "\n")
		}
	}
	writeSuffixRules(tiktokSuffixes, "自动选择")
	b.WriteString("  - DOMAIN-KEYWORD,tiktok,自动选择\n")
	writeSuffixRules(socialSuffixes, "自动选择")
	writeSuffixRules(streamSuffixes, "自动选择")
	// 测 IP 域名走代理（v1.3.24）：客户端自带的测 IP/网络检测在规则模式
	// 下本来走 DIRECT，在 fake-ip 下失败、显示检测失败；指自动选择后
	// 走代理，显示代理出口 IP。空配置（无节点）时不写这组规则（没有
	// 自动选择组可指，写了会让 mihomo -t 报错）。
	writeSuffixRules(ipCheckSuffixes, "自动选择")
	if mode == "global" {
		b.WriteString("  - MATCH,手动选择\n")
	} else {
		b.WriteString("  - MATCH,DIRECT\n")
	}
	return b.String()
}

// resolveSubMode 定订阅的分流模式：?mode=split|global，缺省 split；
// 非法 mode 返回错误（调用方返回 400，与 ?format 的处理一致）。
func resolveSubMode(r *http.Request) (string, error) {
	m := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	switch m {
	case "", "split":
		return "split", nil
	case "global":
		return "global", nil
	default:
		return "", fmt.Errorf("mode 只支持 split/global")
	}
}

// ---------- 格式识别 ----------

// wantsClashByUA 按 User-Agent 判断客户端是不是 Clash 系：含 clash / stash /
// mihomo（不区分大小写）即认为是 Clash 系——覆盖 Clash、Clash Meta、
// Clash Verge、Stash、FlClash 等。
func wantsClashByUA(ua string) bool {
	l := strings.ToLower(ua)
	return strings.Contains(l, "clash") || strings.Contains(l, "stash") || strings.Contains(l, "mihomo")
}

// wantsSingboxByUA 按 User-Agent 判断客户端是不是 sing-box 官方系：
// 含 sing-box / SFA / SFI / SFM（不区分大小写）即认为是 sing-box 系。
// Hiddify/Karing/NekoBox/Streisand 的 UA 没法嗅探，不靠这个，靠界面上
// 单独给的 ?format=singbox 链接。
func wantsSingboxByUA(ua string) bool {
	l := strings.ToLower(ua)
	return strings.Contains(l, "sing-box") || strings.Contains(l, "sfa") || strings.Contains(l, "sfi") || strings.Contains(l, "sfm")
}

// resolveSubFormat 定订阅返回格式：?format=clash|base64|plain|singbox
// 手动指定优先（sing-box 接受 sing-box 别名）；不指定时按 UA 自动识别
// （sing-box 系 → JSON，Clash 系 → YAML，其余 → base64）。
func resolveSubFormat(r *http.Request) (string, error) {
	f := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch f {
	case "clash", "yaml":
		return "clash", nil
	case "singbox", "sing-box":
		return "singbox", nil
	case "base64":
		return "base64", nil
	case "plain":
		return "plain", nil
	case "":
		if wantsSingboxByUA(r.UserAgent()) {
			return "singbox", nil
		}
		if wantsClashByUA(r.UserAgent()) {
			return "clash", nil
		}
		return "base64", nil
	default:
		return "", fmt.Errorf("format 只支持 clash/base64/plain/singbox")
	}
}

// ---------- subscription-userinfo ----------

// subUserinfoHeader 按 subscription-userinfo 规范拼响应头，只用现成真实数据：
// upload/download 取流量统计的累计值（本面板这些落地没数据就不含这两项），
// total 取各落地流量上限之和（都没设限就不含），expire 取「订阅本身的到期
// 时间（tokenExpiry，RFC3339，空 = 永久）」与「各落地到期日里最早的」两
// 者中更早的（都没设就不含）。三项都没有就不返回头，不编数据。
// localIndices 为 nil 表示本面板全部落地；合并订阅只统计其中本面板的部分
// （远程面板的流量数据主面板拿不到，不含）。订阅本身的到期必须算进来：
// 管理面板模式下本机没有落地，落地数据全空时只靠令牌到期才能让客户端
// （如小火箭）显示到期时间，否则客户端只能显示更新/生成时间。
// 调用方保证令牌未过期（已过期的令牌在 handleSub 里直接 410），这里
// 不用再判过期。
// v1.3.27：relay.conf 读不到（管理面板模式本机没有中转、没有 relay.conf）
// 时不要整个返回空头——落地集合按空算，仍按 tokenExpiry 与已有数据拼头
// （只有令牌到期时头里就只有 expire 一项）。三项全都没有时才返回空
// （不编数据口径不变）。1.3.24 的 tokenExpiry 修法本意就是让管理面板
// 只靠令牌到期也能显示，但这个 early return 把它全堵死了。
func (s *Server) subUserinfoHeader(localIndices map[int]bool, tokenExpiry string) string {
	rc, _, err := s.readRelayConf()
	var landings []Landing
	if err == nil && rc != nil {
		landings = rc.Landings
	}
	var up, down, total int64
	var hasTraffic, hasLimit bool
	var earliest time.Time
	if tokenExpiry != "" {
		if t, err := time.Parse(time.RFC3339, tokenExpiry); err == nil {
			earliest = t
		}
	}
	s.stateMu.Lock()
	exts := map[string]*LandingExt{}
	for k, v := range s.state.Landings {
		exts[k] = v
	}
	s.stateMu.Unlock()
	for _, l := range landings {
		if localIndices != nil && !localIndices[l.Index] {
			continue
		}
		if up0, down0, ok := s.traffic.totals(l.Landing); ok {
			up += up0
			down += down0
			hasTraffic = true
		}
		if ext := exts[l.Landing]; ext != nil {
			if ext.TrafficLimitBytes > 0 {
				total += ext.TrafficLimitBytes
				hasLimit = true
			}
			if ext.Expiry != "" {
				if t, err := time.Parse(time.RFC3339, ext.Expiry); err == nil {
					if earliest.IsZero() || t.Before(earliest) {
						earliest = t
					}
				}
			}
		}
	}
	var parts []string
	if hasTraffic {
		parts = append(parts, "upload="+strconv.FormatInt(up, 10), "download="+strconv.FormatInt(down, 10))
	}
	if hasLimit {
		parts = append(parts, "total="+strconv.FormatInt(total, 10))
	}
	if !earliest.IsZero() {
		parts = append(parts, "expire="+strconv.FormatInt(earliest.Unix(), 10))
	}
	return strings.Join(parts, "; ")
}

// ---------- 公开订阅端点 ----------

// findSubUser 按令牌找一个订阅用户（常数时间比较）。
func (s *Server) findSubUser(tok string) (*SubUser, bool) {
	for i := range s.cfg.SubUsers {
		if verifySubToken(tok, s.cfg.SubUsers[i].TokenHash) {
			return &s.cfg.SubUsers[i], true
		}
	}
	return nil, false
}

// findMergedSub 按令牌找一份合并订阅（常数时间比较）。
func (s *Server) findMergedSub(tok string) (*MergedSub, bool) {
	for i := range s.cfg.MergedSubs {
		if verifySubToken(tok, s.cfg.MergedSubs[i].TokenHash) {
			return &s.cfg.MergedSubs[i], true
		}
	}
	return nil, false
}

// handleSub 公开订阅端点：GET /sub/{token}。令牌不对返回 404
// （不返回 401，避免暴露端点存在）；内容实时生成。
// 限流（v1.3.7）：先验令牌，对的永远放行（正常客户端不受影响）；
// 错的才看锁——锁了返回 429，没锁记一次失败（只记 IP 与时间，绝不记令牌）
// 再返回 404。
func (s *Server) handleSub(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	ip := clientIP(r)
	if s.subTokenValid(tok) {
		// 到期检查（v1.3.20）：令牌本身对，但到期时间已过 → 410（不是 404，
		// 让客户端分得清“令牌错了”还是“订阅到期了”），审计记“已过期”。
		if exp, ok := s.subExpiryForToken(tok); ok && subExpired(exp, time.Now()) {
			s.subFailed(ip)
			s.audit("订阅客户端", "拉取订阅", ip, "失败", "订阅已过期")
			http.Error(w, "订阅已过期", http.StatusGone)
			return
		}
		s.subOK(ip)
		// 成功拉取记一条轻量记录：IP + 时间 + 令牌哈希前 8 位（认得出是哪条，
		// 又不把日志打爆）；明文令牌绝不进审计日志。
		s.audit("订阅客户端", "拉取订阅", ip, "成功", "令牌哈希前 8 位 "+hashSubToken(tok)[:8])
		s.serveSubContent(w, r, tok)
		return
	}
	if s.subLocked(ip) {
		w.Header().Set("Retry-After", "600")
		http.Error(w, "请求太频繁，稍后再试", 429)
		return
	}
	s.subFailed(ip)
	s.audit("订阅客户端", "拉取订阅", ip, "失败", "令牌不对")
	http.NotFound(w, r)
}

// subExpiryForToken 取一条令牌的到期时间（RFC3339，空 = 永久）：
// 通用订阅、每人链接、合并订阅三处都查；令牌不存在返回 false。
func (s *Server) subExpiryForToken(tok string) (string, bool) {
	if tok != "" && s.cfg.SubTokenHash != "" && verifySubToken(tok, s.cfg.SubTokenHash) {
		return s.cfg.SubTokenExpiresAt, true
	}
	if u, ok := s.findSubUser(tok); ok {
		return u.ExpiresAt, true
	}
	if ms, ok := s.findMergedSub(tok); ok {
		return ms.ExpiresAt, true
	}
	return "", false
}

// serveSubContent /sub/ 的内容生成（handleSub 验过令牌/限流后调这里）。
func (s *Server) serveSubContent(w http.ResponseWriter, r *http.Request, tok string) {
	format, err := resolveSubFormat(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	mode, err := resolveSubMode(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var links []subLink
	var localIndices map[int]bool
	// contentMode 是令牌上存的订阅内容范围（both|relay|direct，老记录空 = both），
	// 与上面 resolveSubMode 的分流模式（split|global）是两回事。
	contentMode := "both"
	// 按人限流（v1.3.25）：命中的订阅开了限流时，订阅里的中转节点改用
	// 该用户的端口（本面板上的转发），SNI 与域名不变。已停用的订阅
	// （超量）链接照常返回、端口已死（endpoints 已撤），与落地级停用
	// 后链接仍在订阅里但连不上的做法一致；userinfo 头里能看到已用
	// 满的量，知道为什么连不上。
	var userKey string
	var userLimit int64
	var userLimitMode string
	if tok != "" && s.cfg.SubTokenHash != "" && verifySubToken(tok, s.cfg.SubTokenHash) {
		links = s.collectSubLinks(nil)
		contentMode = s.cfg.SubTokenMode
	} else if u, ok := s.findSubUser(tok); ok {
		// 一人一条（v1.3.0）：没勾选 = 单面板全量订阅，有勾选 = 合并订阅
		// 分组（v1.3.26）：进了组的用组的落地集合，自己的勾选保留在记录里
		// （出组/删组后恢复）；没进组按自己的勾选。
		contentMode = u.Mode
		if u.TrafficLimitBytes > 0 {
			userKey, userLimit, userLimitMode = userSubKey("subuser", u.ID), u.TrafficLimitBytes, u.TrafficLimitMode
		}
		if sel := s.effectiveSelectionForSubUser(u); len(sel) == 0 {
			links = s.collectSubLinks(nil)
		} else {
			links, localIndices = s.collectMergedSubLinks(&MergedSub{Selection: sel}, userKey)
		}
	} else if ms, ok := s.findMergedSub(tok); ok {
		contentMode = ms.Mode
		if ms.TrafficLimitBytes > 0 {
			userKey, userLimit, userLimitMode = userSubKey("merged", ms.ID), ms.TrafficLimitBytes, ms.TrafficLimitMode
		}
		// 分组（v1.3.26）：进了组的用组的落地集合。
		links, localIndices = s.collectMergedSubLinks(&MergedSub{Selection: s.effectiveSelectionForMerged(ms)}, userKey)
	} else {
		http.NotFound(w, r)
		return
	}
	links = filterSubLinksByKind(links, contentMode)
	if userKey != "" {
		links = rewriteSubLinksForUser(links, s.userPortByAddr(userKey))
	}
	body, ctype, err := renderSubContent(links, format, mode)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// 令牌到期与落地到期取更早的（v1.3.24：订阅本身的到期也要进 userinfo，
	// 否则管理面板模式下客户端看不到到期时间）。
	exp, _ := s.subExpiryForToken(tok)
	// 开了限流的订阅优先用本机每用户用量拼 userinfo（total 是该订阅
	// 的上限）；本面板上没有该订阅的用量（例如管理面板模式）时回落
	// 到原来的落地汇总头。
	h := ""
	if userKey != "" {
		h = s.subUserinfoHeaderForUser(userKey, userLimit, userLimitMode, exp, localIndices)
	}
	if h == "" {
		h = s.subUserinfoHeader(localIndices, exp)
	}
	if h != "" {
		w.Header().Set("subscription-userinfo", h)
	}
	w.Header().Set("Content-Type", ctype)
	// 下载文件名（v1.3.22）：基本名 = 订阅文件名称设置，留空回落面板名；
	// 后缀按格式加（Clash 是 .yaml，sing-box 是 .json，base64/plain 是 .txt）。客户端按
	// 文件名给导入的配置起名。注意：文件名会露出面板名或自定义名，
	// 1.3.20 的通用文件名 subscription.yaml 的反特征作用就没了，这是
	// 用户主动要求改回的（见 CHANGELOG 1.3.22）。
	w.Header().Set("Content-Disposition", s.subContentDisposition(format))
	_, _ = w.Write([]byte(body))
}

// ---------- 订阅下载文件名（v1.3.22，设置项） ----------
// 文件名 = 基本名 + 后缀（Clash 是 .yaml，base64/plain 是 .txt）。
// 基本名：订阅文件名称设置（SubFileName）填了就用它，留空回落面板名；
// 改面板名不影响已设的文件名。1.3.20 起曾固定用通用名 subscription.yaml，
// 1.3.22 起改成可设置（用户主动要求：导入的配置名要能看出是自家面板）。
// 非 ASCII 文件名按 RFC 5987 编码（filename*=UTF-8”...），同时保留
// ASCII 的 filename= 兜底（老客户端不认 filename* 时用通用名）。
func (s *Server) subFileBaseName() string {
	if n := strings.TrimSpace(s.cfg.SubFileName); n != "" {
		return n
	}
	return s.panelName()
}

func (s *Server) subContentDisposition(format string) string {
	ext := "txt"
	if format == "clash" {
		ext = "yaml"
	} else if format == "singbox" {
		ext = "json"
	}
	name := s.subFileBaseName() + "." + ext
	if isASCIIString(name) {
		return `attachment; filename="` + name + `"`
	}
	return `attachment; filename="subscription.` + ext + `"; filename*=UTF-8''` + rfc3986Escape(name)
}

// isASCIIString 字符串是否全是 ASCII（写 Content-Disposition 前用）。
func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}

// validSubFileName 校验订阅文件名称：去首尾空格后 1–64 字，禁
// / \ : * ? " < > | 与控制字符（这些在 Windows 文件名里也不合法）。
func validSubFileName(name string) bool {
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 64 {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return false
		}
	}
	return true
}

// handleSubFileNameSet 改订阅文件名称（v1.3.22，会话认证）：
// 空 = 清除（回落面板名）；非空须通过 validSubFileName 校验。
func (s *Server) handleSubFileNameSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name != "" && !validSubFileName(name) {
		writeJSON(w, 400, errResp("订阅文件名称不对：1–64 字，不能含 / \\ : * ? \" < > | 与控制字符"))
		return
	}
	old := s.cfg.SubFileName
	s.cfg.SubFileName = name
	if err := s.saveConfig(); err != nil {
		s.cfg.SubFileName = old
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	detail := "已清除，改回用面板名"
	if name != "" {
		detail = "订阅文件名改成“" + name + ".yaml/.txt”"
	}
	s.audit(s.currentUser(r), "设置订阅文件名称", name, "成功", detail)
	writeJSON(w, 200, okResp(map[string]any{"file_name": name}))
}

// ---------- 单面板订阅令牌管理（会话认证） ----------

// subURL 拼订阅链接：填了订阅对外域名（v1.3.2，Worker 域名或自定义域名）
// 就用 https://域名/<订阅路径>/…，不再用面板自己的地址；没填时按当前请求的协议
// 与 Host 拼（面板怎么打开，订阅就用什么地址）。路径段走 s.subPath()
// （v1.3.20 可自定义，默认 sub）。
func (s *Server) subURL(r *http.Request, tok string) string {
	if d := strings.TrimSpace(s.cfg.SubPublicDomain); d != "" {
		return "https://" + d + "/" + s.subPath() + "/" + tok
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/" + s.subPath() + "/" + tok
}

func (s *Server) handleSubTokenGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, okResp(map[string]any{
		"has_token":  s.cfg.SubTokenHash != "",
		"has_link":   s.cfg.SubTokenEnc != "",
		"created_at": s.cfg.SubTokenCreatedAt,
		"mode":       s.cfg.SubTokenMode,
		"expires_at": s.cfg.SubTokenExpiresAt,
	}))
}

// handleSubTokenGenerate 生成（或轮换）单面板订阅令牌：哈希与密文一起存，
// 链接随时能重新复制；已有令牌时旧链接立即失效。记审计日志（不记令牌本身）。
// v1.3.17：请求可带 mode（both|relay|direct，不带默认 both），存进
// SubTokenMode，轮换时同样按表单当前选择存（生成表单的显式选择）。
// v1.3.20：请求可带 expires_at（日期或 RFC3339，空串 = 永久）；不带
// expires_at 字段时保持原到期时间（轮换不改到期时间）。
func (s *Server) handleSubTokenGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode      string  `json:"mode"`
		ExpiresAt *string `json:"expires_at"`
		Timezone  string  `json:"timezone,omitempty"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	subMode, err := normalizeSubMode(req.Mode)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	expiresAt := s.cfg.SubTokenExpiresAt
	if req.ExpiresAt != nil {
		expiresAt, err = parseSubExpiryIn(*req.ExpiresAt, req.Timezone)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	tok := generateSubToken()
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		writeJSON(w, 500, errResp("令牌加密失败："+err.Error()))
		return
	}
	rotated := s.cfg.SubTokenHash != ""
	s.cfg.SubTokenHash = hashSubToken(tok)
	s.cfg.SubTokenEnc = enc
	s.cfg.SubTokenCreatedAt = time.Now().UTC().Format(time.RFC3339)
	s.cfg.SubTokenMode = subMode
	s.cfg.SubTokenExpiresAt = expiresAt
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	action := "生成订阅链接"
	if rotated {
		action = "轮换订阅链接"
	}
	s.audit(s.currentUser(r), action, "", "成功", "旧订阅链接已失效")
	writeJSON(w, 200, okResp(map[string]any{"url": s.subURL(r, tok), "rotated": rotated, "mode": subMode, "expires_at": expiresAt}))
}

// handleSubTokenSetExpiry 只改通用订阅的到期时间，不换令牌、不换链接
// （v1.3.20，设置页用）：expires_at 空 = 永久；填日期（如 2026-12-31）
// 或 RFC3339。没有通用订阅令牌时 400。
func (s *Server) handleSubTokenSetExpiry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpiresAt string `json:"expires_at"`
		Timezone  string `json:"timezone,omitempty"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if s.cfg.SubTokenHash == "" {
		writeJSON(w, 400, errResp("还没有通用订阅链接，先点“生成订阅链接”"))
		return
	}
	expiresAt, err := parseSubExpiryIn(req.ExpiresAt, req.Timezone)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	old := s.cfg.SubTokenExpiresAt
	s.cfg.SubTokenExpiresAt = expiresAt
	if err := s.saveConfig(); err != nil {
		s.cfg.SubTokenExpiresAt = old
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	detail := "改成永久"
	if expiresAt != "" {
		detail = "改成 " + expiresAt[:10]
	}
	s.audit(s.currentUser(r), "设置通用订阅到期时间", "", "成功", detail)
	writeJSON(w, 200, okResp(map[string]any{"expires_at": expiresAt}))
}

func (s *Server) handleSubTokenRevoke(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SubTokenHash == "" {
		writeJSON(w, 400, errResp("本来就没有订阅链接，不用撤销"))
		return
	}
	s.cfg.SubTokenHash = ""
	s.cfg.SubTokenEnc = ""
	s.cfg.SubTokenCreatedAt = ""
	s.cfg.SubTokenMode = ""
	s.cfg.SubTokenExpiresAt = ""
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "撤销订阅链接", "", "成功", "订阅链接已失效")
	writeJSON(w, 200, okResp(nil))
}

// ---------- 重新复制订阅链接（v1.3.7，会话认证） ----------
// GET /api/v1/subscriptions/link?kind=generic|user|merged&id=...
// 有密文就解密拼链接返回（前端直接复制；二维码也用这条链接生成）。
// 1.3.7 之前生成的旧链接没有密文，返回 legacy:true 与提示，前端弹轮换确认
// （轮换一次后就有密文了）；密钥丢了/密文损坏也按旧链接处理，不编链接。
// 绝不走公开的 /sub/ 路径；复制是只读操作，不记审计日志（明文令牌
// 绝不进审计日志）。

const legacySubLinkMsg = "这条是旧链接（1.3.7 之前生成的），面板里没有存它的密文，无法恢复。点轮换生成一条新的，旧链接立即失效。"

func (s *Server) handleSubLink(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	id := r.URL.Query().Get("id")
	var enc string
	expiresAt := ""
	switch kind {
	case "generic":
		if s.cfg.SubTokenHash == "" {
			writeJSON(w, 404, errResp("还没有订阅链接"))
			return
		}
		enc = s.cfg.SubTokenEnc
		expiresAt = s.cfg.SubTokenExpiresAt
	case "user":
		found := false
		for i := range s.cfg.SubUsers {
			if s.cfg.SubUsers[i].ID == id {
				enc = s.cfg.SubUsers[i].TokenEnc
				expiresAt = s.cfg.SubUsers[i].ExpiresAt
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, 404, errResp("没有这个订阅用户"))
			return
		}
	case "merged":
		found := false
		for i := range s.cfg.MergedSubs {
			if s.cfg.MergedSubs[i].ID == id {
				enc = s.cfg.MergedSubs[i].TokenEnc
				expiresAt = s.cfg.MergedSubs[i].ExpiresAt
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, 404, errResp("没有这条合并订阅"))
			return
		}
	default:
		writeJSON(w, 400, errResp("kind 只支持 generic/user/merged"))
		return
	}
	if enc == "" {
		writeJSON(w, 200, okResp(map[string]any{"legacy": true, "message": legacySubLinkMsg}))
		return
	}
	tok, err := s.decryptSubTokenNow(enc)
	if err != nil {
		writeJSON(w, 200, okResp(map[string]any{"legacy": true, "message": legacySubLinkMsg}))
		return
	}
	writeJSON(w, 200, okResp(map[string]any{"url": s.subURL(r, tok), "legacy": false, "expires_at": expiresAt}))
}

// ---------- 订阅对外域名（v1.3.2，会话认证） ----------
// Worker 反代部署后，面板里显示的订阅链接还是面板自己的地址（Worker 只负责
// 反代，不会改写面板显示的链接），用户就以为“部署了也没用”。填了对外域名
// 后，面板里所有订阅链接（通用链接、每人链接、合并订阅、二维码）都改成
// https://域名/sub/…；Worker 代码里的源站地址（ORIGIN）不受影响，
// 仍是面板真实地址。

// validPublicDomain 校验订阅对外域名：只允许域名（字母数字、点、横杠，
// 标签不以横杠开头或结尾），可选 :端口；不许带 scheme、路径、斜杠、
// 查询串与注入字符。空字符串由调用方先处理（= 清除）。
func validPublicDomain(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		port := h[i+1:]
		if port == "" {
			return false
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return false
		}
		h = h[:i]
	}
	if h == "" || strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") {
		return false
	}
	for _, lb := range strings.Split(h, ".") {
		if lb == "" || len(lb) > 63 || strings.HasPrefix(lb, "-") || strings.HasSuffix(lb, "-") {
			return false
		}
		for _, c := range lb {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// handleSubPublicDomainSet 设置/清除订阅对外域名：空字符串 = 清除（恢复成
// 面板自己的地址）；非空须通过 validPublicDomain 校验，非法返回 400。
func (s *Server) handleSubPublicDomainSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if domain != "" && !validPublicDomain(domain) {
		writeJSON(w, 400, errResp("对外域名格式不对：只填域名（可带端口），不用写 https://，不用写路径"))
		return
	}
	old := s.cfg.SubPublicDomain
	s.cfg.SubPublicDomain = domain
	if err := s.saveConfig(); err != nil {
		s.cfg.SubPublicDomain = old
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	detail := "已恢复成面板自己的地址"
	if domain != "" {
		detail = "订阅链接改用 https://" + domain + "/" + s.subPath() + "/…"
	}
	s.audit(s.currentUser(r), "设置订阅对外域名", domain, "成功", detail)
	writeJSON(w, 200, okResp(map[string]any{"domain": domain}))
}

// ---------- 订阅路径（v1.3.20，反特征，会话认证） ----------
// handleSubPathSet 改订阅路径段：空 = 恢复默认 sub；非空须通过 validSubPath
// 校验。路径是路由的一部分（启动时注册），改了要重启面板才生效，重启后
// 旧订阅链接全部失效（旧路径不再有路由）、会话失效须重新登录、客户端
// 须重新导入新链接；前端在保存前已弹过确认框，这里直接执行。
// 改成和当前一样的值时不重启（只返回当前值）。
func (s *Server) handleSubPathSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		path = "sub"
	}
	if path != "sub" && !validSubPath(path) {
		writeJSON(w, 400, errResp("订阅路径不对：单段、字母开头、只含字母数字与 - _、1–32 个字符；api 与 logo 是保留段，不能用"))
		return
	}
	old := s.subPath()
	if path == old {
		writeJSON(w, 200, okResp(map[string]any{"sub_path": path, "restart": false}))
		return
	}
	oldRaw := s.cfg.SubPath
	if path == "sub" {
		s.cfg.SubPath = ""
	} else {
		s.cfg.SubPath = path
	}
	if err := s.saveConfig(); err != nil {
		s.cfg.SubPath = oldRaw
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "改订阅路径", "/"+old+"/ -> /"+path+"/", "成功", "面板正在重启，旧订阅链接全部失效，客户端须重新导入")
	writeJSON(w, 200, okResp(map[string]any{"sub_path": path, "restart": true, "msg": "订阅路径已改成 /" + path + "/，面板正在重启，旧订阅链接全部失效，重新登录后在订阅卡片里复制新链接"}))
	// 异步重启自身（先回包再重启，和改端口同一套做法）
	go func() {
		time.Sleep(300 * time.Millisecond)
		restartSelf(s.env)
	}()
}

// ---------- 订阅用户：一人一条订阅链接（v1.3.0，会话认证） ----------
// 一个面板一个通用令牌时，所有人拉同一条链接，封一条就全军覆没；
// 每人一个独立令牌，封一条只影响一个人。令牌存哈希（鉴权）与密文
// （v1.3.7，重新复制用）；旧的面板级通用令牌保留不动，已发出去的链接继续能用。

// handleSubUsersList 列出订阅用户（不含令牌与链接；has_link 表示有没有密文，
// 有就能点复制重新拿链接，没有就是 1.3.7 之前的旧链接，复制时提示轮换）。
func (s *Server) handleSubUsersList(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Note      string         `json:"note"`
		CreatedAt string         `json:"created_at"`
		Kind      string         `json:"kind"` // single=单面板全量，merged=合并订阅
		Summary   string         `json:"summary"`
		HasLink   bool           `json:"has_link"`
		Mode      string         `json:"mode"`                 // both|relay|direct，老记录空 = 全部
		Selection []SubSelection `json:"selection,omitempty"`  // 编辑弹窗预选用；空 = 全部落地
		ExpiresAt string         `json:"expires_at,omitempty"` // v1.3.20：RFC3339，空 = 永久
		// 流量上限（v1.3.25）：limit_mb 0 = 不限流；used 是本面板上的
		// 已用字节（monthly 用本期，total 用累计），has_used=false 时
		// 本面板上还没该订阅的用量（例如管理面板模式）；disabled 是
		// 超量停用；forward_count 是本面板上的转发条数（有限流但
		// 0 条 = 转发没建起来，重新保存一次）。
		LimitMB      int64  `json:"traffic_limit_mb"`
		LimitMode    string `json:"traffic_limit_mode,omitempty"`
		ResetMode    string `json:"reset_mode,omitempty"`
		ResetDay     int    `json:"reset_day,omitempty"`
		NextResetAt  string `json:"next_reset_at,omitempty"`
		Used         int64  `json:"traffic_used"`
		HasUsed      bool   `json:"has_used"`
		Disabled     bool   `json:"traffic_disabled"`
		ForwardCount int    `json:"forward_count"`
		// v1.3.27：流量采样失败提示（空 = 正常；有值时列表用量不可信）。
		TrafficWarn string `json:"traffic_warn,omitempty"`
		// v1.3.27：限流的远程版本警告（远程面板低于 1.3.25 时限流不生效）。
		LimitWarn string `json:"limit_warn,omitempty"`
		// 分组（v1.3.26）：GroupID 空 = 不分组；GroupName 是分组显示名
		// （列表徽章用）；进了组时 Summary 是组的落地摘要。
		GroupID   string `json:"group_id,omitempty"`
		GroupName string `json:"group_name,omitempty"`
	}
	items := []item{}
	for _, u := range s.cfg.SubUsers {
		kind := "single"
		summary := "单面板全量订阅（全部落地）"
		if len(u.Selection) > 0 {
			kind = "merged"
			summary = s.selectionSummary(u.Selection)
		}
		if u.GroupID != "" {
			kind = "merged"
			summary = "分组「" + s.groupNameOf(u.GroupID) + "」：" + s.selectionSummary(s.effectiveSelectionForSubUser(&u))
		}
		used, _, hasUsed := s.userUsageSummary(userSubKey("subuser", u.ID), u.TrafficLimitBytes, u.TrafficLimitMode)
		limitWarn := ""
		if u.TrafficLimitBytes > 0 {
			limitWarn = s.userLimitVersionWarn(s.effectiveSelectionForSubUser(&u))
		}
		items = append(items, item{ID: u.ID, Name: u.Name, Note: u.Note, CreatedAt: u.CreatedAt, Kind: kind, Summary: summary, HasLink: u.TokenEnc != "", Mode: u.Mode, Selection: u.Selection, ExpiresAt: u.ExpiresAt, LimitMB: u.TrafficLimitBytes / 1024 / 1024, LimitMode: u.TrafficLimitMode, ResetMode: u.ResetMode, ResetDay: u.ResetDay, NextResetAt: u.NextResetAt, Used: used, HasUsed: hasUsed, Disabled: u.Disabled, ForwardCount: len(s.userForwardsFor(userSubKey("subuser", u.ID))), TrafficWarn: s.userTrafficWarnFor(userSubKey("subuser", u.ID), s.effectiveSelectionForSubUser(&u)), LimitWarn: limitWarn, GroupID: u.GroupID, GroupName: s.groupNameOf(u.GroupID)})
	}
	writeJSON(w, 200, okResp(map[string]any{"users": items}))
}

// handleSubUsersCreate 新建订阅用户：名字必填（用来认人）；selection 为空
// 就是单面板全量订阅，填了就是合并订阅（走同一套勾选校验，远程版本不够
// 时同样提示先升级）。链接在这一次返回，之后随时能点复制重新拿。
// v1.3.17：请求可带 mode（both|relay|direct，不带默认 both），存进记录。
// v1.3.20：请求可带 expires_at（日期或 RFC3339，空串 = 永久；不带 = 永久）。
func (s *Server) handleSubUsersCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string         `json:"name"`
		Note      string         `json:"note"`
		Selection []SubSelection `json:"selection"`
		Mode      string         `json:"mode"`
		ExpiresAt *string        `json:"expires_at"`
		Timezone  string         `json:"timezone,omitempty"`
		// 分组（v1.3.26）：group_id 空 = 不分组；进了组，订阅按组的落地
		// 集合出内容，自己的 Selection 保留在记录里（出组后恢复）。
		GroupID string `json:"group_id"`
		// 流量上限（v1.3.25）：limit_mb 0/不给 = 不限流（走共享端口）；
		// limit_mode total=累计 monthly=按月；reset_mode 只在 monthly
		// 下有效（monthly=每月 1 号，anniversary=跟随生成日，
		// custom=自定义每月几号，reset_day 1–31）。
		TrafficLimitMB   int64  `json:"traffic_limit_mb"`
		TrafficLimitMode string `json:"traffic_limit_mode"`
		ResetMode        string `json:"reset_mode"`
		ResetDay         int    `json:"reset_day"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, 400, errResp("请填名字（用来认人，例如“张三的手机”）"))
		return
	}
	if len(name) > 64 {
		writeJSON(w, 400, errResp("名字太长，最多 64 个字"))
		return
	}
	note := strings.TrimSpace(req.Note)
	if len(note) > 200 {
		writeJSON(w, 400, errResp("备注太长，最多 200 个字"))
		return
	}
	var sel []SubSelection
	if len(req.Selection) > 0 {
		var err error
		sel, _, err = s.validateMergedSelection(req.Selection)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	groupID, err := s.validateGroupID(req.GroupID)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	subMode, err := normalizeSubMode(req.Mode)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	expiresAt := ""
	if req.ExpiresAt != nil {
		expiresAt, err = parseSubExpiryIn(*req.ExpiresAt, req.Timezone)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	nowTime := time.Now().UTC()
	limitBytes, limitMode, resetMode, resetDay, nextResetAt, err := normalizeUserLimitFields(mbToBytes(req.TrafficLimitMB), req.TrafficLimitMode, req.ResetMode, req.ResetDay, nowTime, nowTime)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	tok := generateSubToken()
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		writeJSON(w, 500, errResp("令牌加密失败："+err.Error()))
		return
	}
	now := nowTime.Format(time.RFC3339)
	id := "su-" + randomTokenHex(6)
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: id, Name: name, Note: note, TokenHash: hashSubToken(tok), TokenEnc: enc, CreatedAt: now, Selection: sel, Mode: subMode, ExpiresAt: expiresAt, GroupID: groupID, TrafficLimitBytes: limitBytes, TrafficLimitMode: limitMode, ResetMode: resetMode, ResetDay: resetDay, NextResetAt: nextResetAt})
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	// 按人限流（v1.3.25）：订阅已保存，再核对本机转发并同步到各远程
	// 面板。建不起来也不撤订阅——没建起来的那部分暂时走共享端口、
	// 不限流，warning 让前端提示用户重新保存一次。
	// 分组（v1.3.26）：进了组的按组的落地集合建转发。
	effSel := sel
	if groupID != "" {
		if g := s.findSubGroup(groupID); g != nil {
			effSel = g.Selection
		}
	}
	resp := map[string]any{"id": id, "url": s.subURL(r, tok), "name": name, "mode": subMode, "expires_at": expiresAt, "group_id": groupID, "traffic_limit_mb": limitBytes / 1024 / 1024, "traffic_limit_mode": limitMode, "reset_mode": resetMode, "reset_day": resetDay, "next_reset_at": nextResetAt}
	if warn := s.syncUserForwardsWarning("subuser", id, name, effSel, limitBytes, limitMode, resetMode, resetDay, nextResetAt); warn != "" {
		resp["warning"] = warn
		s.audit(s.currentUser(r), "新建订阅用户", name, "部分成功", warn)
	} else {
		s.audit(s.currentUser(r), "新建订阅用户", name, "成功", "链接已加密存好，随时能复制")
	}
	writeJSON(w, 200, okResp(resp))
}

// handleSubUsersRotate 轮换一个订阅用户的链接：旧链接立即失效，只影响这个人。
func (s *Server) handleSubUsersRotate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].ID != id {
			continue
		}
		tok := generateSubToken()
		enc, err := s.encryptSubTokenNow(tok)
		if err != nil {
			writeJSON(w, 500, errResp("令牌加密失败："+err.Error()))
			return
		}
		s.cfg.SubUsers[i].TokenHash = hashSubToken(tok)
		s.cfg.SubUsers[i].TokenEnc = enc
		s.cfg.SubUsers[i].CreatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.saveConfig(); err != nil {
			writeJSON(w, 500, errResp("保存失败："+err.Error()))
			return
		}
		s.audit(s.currentUser(r), "轮换订阅用户链接", s.cfg.SubUsers[i].Name, "成功", "旧链接已失效")
		mode := s.cfg.SubUsers[i].Mode
		if mode == "" {
			mode = "both"
		}
		writeJSON(w, 200, okResp(map[string]any{"id": id, "url": s.subURL(r, tok), "name": s.cfg.SubUsers[i].Name, "mode": mode}))
		return
	}
	writeJSON(w, 404, errResp("没有这个订阅用户"))
}

// handleSubUsersDelete 撤销一个订阅用户：他的订阅链接立即失效，其他人照常。
func (s *Server) handleSubUsersDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].ID == id {
			name := s.cfg.SubUsers[i].Name
			// 分组（v1.3.26）：进了组的按组的落地集合回收远程转发。
			sel := s.effectiveSelectionForSubUser(&s.cfg.SubUsers[i])
			s.cfg.SubUsers = append(s.cfg.SubUsers[:i], s.cfg.SubUsers[i+1:]...)
			if err := s.saveConfig(); err != nil {
				writeJSON(w, 500, errResp("保存失败："+err.Error()))
				return
			}
			// 按人限流（v1.3.25）：订阅删了就回收它的转发与端口
			// （本机 + 各远程面板）。
			s.opMu.Lock()
			recErr := s.removeUserForwards("subuser", id)
			s.opMu.Unlock()
			var warns []string
			if recErr != nil {
				warns = append(warns, "本机转发没撤干净："+recErr.Error())
			}
			warns = append(warns, s.removeUserForwardsFromRemotes(userSubKey("subuser", id), sel)...)
			resp := map[string]any{}
			if len(warns) > 0 {
				resp["warning"] = "订阅已删，但" + strings.Join(warns, "；") + "，请检查中转机上的转发。"
				s.audit(s.currentUser(r), "撤销订阅用户", name, "部分成功", strings.Join(warns, "；"))
			} else {
				s.audit(s.currentUser(r), "撤销订阅用户", name, "成功", "该用户的订阅链接已失效，其他人照常")
			}
			writeJSON(w, 200, okResp(resp))
			return
		}
	}
	writeJSON(w, 404, errResp("没有这个订阅用户"))
}

// handleSubUsersUpdate 编辑一个订阅用户：改名字/备注/勾选集合/内容模式，
// 令牌不动（TokenHash/TokenEnc 原样保留），链接不变，下次取订阅即按新
// 勾选与新模式出内容。空勾选 = 全部落地（与创建时语义一致）。
// v1.3.19：与轮换互不干扰——轮换换令牌、编辑不换令牌。
// v1.3.20：请求可带 expires_at（日期或 RFC3339，空串 = 永久）；不带
// expires_at 字段时保持原到期时间。
func (s *Server) handleSubUsersUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name      string         `json:"name"`
		Note      string         `json:"note"`
		Selection []SubSelection `json:"selection"`
		Mode      string         `json:"mode"`
		ExpiresAt *string        `json:"expires_at"`
		Timezone  string         `json:"timezone,omitempty"`
		// 分组（v1.3.26）：指针 = 没给保持原分组；给了 "" = 出组（恢复自己
		// 勾选），给了 "sg-…" = 进组（订阅按组的落地出，自己的勾选保留在
		// 记录里不动）。进组时请求里的 selection 不动自己的勾选。
		GroupID *string `json:"group_id"`
		// 流量上限（v1.3.25）：指针 = 没给保持原值，给了按新值
		// （limit_mb 给 0 = 取消限流，回共享端口）。
		TrafficLimitMB   *int64  `json:"traffic_limit_mb"`
		TrafficLimitMode *string `json:"traffic_limit_mode"`
		ResetMode        *string `json:"reset_mode"`
		ResetDay         *int    `json:"reset_day"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, 400, errResp("请填名字（用来认人，例如“张三的手机”）"))
		return
	}
	if len(name) > 64 {
		writeJSON(w, 400, errResp("名字太长，最多 64 个字"))
		return
	}
	note := strings.TrimSpace(req.Note)
	if len(note) > 200 {
		writeJSON(w, 400, errResp("备注太长，最多 200 个字"))
		return
	}
	var sel []SubSelection
	if len(req.Selection) > 0 {
		var err error
		sel, _, err = s.validateMergedSelection(req.Selection)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	// 分组（v1.3.26）：没给 group_id = 保持原分组；给了 "" = 出组；
	// 给了 "sg-…" = 进组（校验存在）。
	groupGiven := false
	groupID := ""
	if req.GroupID != nil {
		groupGiven = true
		var err error
		groupID, err = s.validateGroupID(*req.GroupID)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	subMode, err := normalizeSubMode(req.Mode)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	expiresAt := ""
	expiresGiven := false
	if req.ExpiresAt != nil {
		expiresGiven = true
		expiresAt, err = parseSubExpiryIn(*req.ExpiresAt, req.Timezone)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].ID != id {
			continue
		}
		u := &s.cfg.SubUsers[i]
		// 限流字段：没给的保持原值，给了的按新值算（anchor 是订阅创建
		// 时间，anniversary 取它的日）。没给限流时直接用原字节数，
		// 不经过 MB 换算（避免丢精度）。
		newLimit := u.TrafficLimitBytes
		newMode, newResetMode, newResetDay := u.TrafficLimitMode, u.ResetMode, u.ResetDay
		if req.TrafficLimitMB != nil {
			newLimit = mbToBytes(*req.TrafficLimitMB)
		}
		if req.TrafficLimitMode != nil {
			newMode = *req.TrafficLimitMode
		}
		if req.ResetMode != nil {
			newResetMode = *req.ResetMode
		}
		if req.ResetDay != nil {
			newResetDay = *req.ResetDay
		}
		anchor, _ := time.Parse(time.RFC3339, u.CreatedAt)
		limitBytes, limitMode, resetMode, resetDay, nextResetAt, err := normalizeUserLimitFields(newLimit, newMode, newResetMode, newResetDay, anchor, time.Now().UTC())
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
		// 调高上限后不再超量就自动恢复（清停用标志，让 reconcile 把
		// endpoints 加回去）；仍超量就继续停用。
		s.maybeResumeUser(userSubKey("subuser", id), limitBytes, limitMode)
		// 分组（v1.3.26）：给了 group_id 就改归属；进了组，自己的
		// Selection 不动（保留在记录里，出组后恢复），订阅按组的落地出；
		// 出组后按请求里的新勾选出。
		if groupGiven {
			u.GroupID = groupID
		}
		if u.GroupID == "" {
			u.Selection = sel
		}
		effSel := s.effectiveSelectionForSubUser(u)
		u.Name = name
		u.Note = note
		u.Mode = subMode
		u.TrafficLimitBytes = limitBytes
		u.TrafficLimitMode = limitMode
		u.ResetMode = resetMode
		u.ResetDay = resetDay
		u.NextResetAt = nextResetAt
		if expiresGiven {
			u.ExpiresAt = expiresAt
		}
		if err := s.saveConfig(); err != nil {
			writeJSON(w, 500, errResp("保存失败："+err.Error()))
			return
		}
		kind := "single"
		summary := "单面板全量订阅（全部落地）"
		if len(effSel) > 0 {
			kind = "merged"
			summary = s.selectionSummary(effSel)
			if u.GroupID != "" {
				summary = "分组「" + s.groupNameOf(u.GroupID) + "」：" + summary
			}
		}
		selOut := u.Selection
		if selOut == nil {
			selOut = []SubSelection{}
		}
		resp := map[string]any{"id": id, "name": name, "kind": kind, "summary": summary, "mode": subMode, "selection": selOut, "expires_at": u.ExpiresAt, "group_id": u.GroupID, "traffic_limit_mb": limitBytes / 1024 / 1024, "traffic_limit_mode": limitMode, "reset_mode": resetMode, "reset_day": resetDay, "next_reset_at": nextResetAt}
		if warn := s.syncUserForwardsWarning("subuser", id, name, effSel, limitBytes, limitMode, resetMode, resetDay, nextResetAt); warn != "" {
			resp["warning"] = warn
			s.audit(s.currentUser(r), "编辑订阅用户", name+"（"+summary+"）", "部分成功", warn)
		} else {
			s.audit(s.currentUser(r), "编辑订阅用户", name+"（"+summary+"）", "成功", "链接不变，下次更新订阅即按新勾选同步")
		}
		writeJSON(w, 200, okResp(resp))
		return
	}
	writeJSON(w, 404, errResp("没有这个订阅用户"))
}

// ---------- Worker 反代代码生成（v1.3.0，会话认证） ----------
// 订阅链接直接由面板出时，客户端看到的是面板真实地址；套一层 Cloudflare
// Worker 反代后，客户端只看到 Worker 自己的域名（或绑定的自定义域名），
// 面板真实地址只出现在 Worker 代码里（Cloudflare 账号内可见，不经过客户端）。
// 面板不替用户部署 Worker，只生成代码与部署步骤。

// workerOrigin 定 Worker 回源的面板地址：优先用 origin 参数（用户在面板里填
// 的对外地址，可带 https:// 完整写法），否则用当前打开面板的地址
// （怎么打开就用什么）。只接受域名（可带端口），不接受带路径、
// 查询串的写法；host 只允许字母数字、点、横杠、下划线与端口冒号，
// 防止拼进 Worker 代码时注入。
// 注意：源站地址不能是 IP 字面量——Cloudflare Worker 的 fetch() 不能
// 直接用 IP 回源，会在边缘直接被回 error 1003，订阅拿到的就是 403。
// 用户在输入框里明确填了 IP 时直接拒绝（400），告诉他先建个域名指过去；
// 没填（默认用当前面板地址）时不拦——不填域名、直接用面板地址本来就是
// 默认用法，拦了就没法直接用了——这时由 handleWorkerCode 在返回里带
// warning 提示，而不是悄悄生成一个注定跑不通的代码。
func workerOrigin(r *http.Request) (string, error) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	origin := strings.TrimSpace(r.URL.Query().Get("origin"))
	if origin == "" {
		return scheme + "://" + r.Host, nil
	}
	host := origin
	if strings.Contains(origin, "://") {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("源站地址填 https://域名 或 http://域名:端口，不用写路径")
		}
		scheme = u.Scheme
		host = u.Host
	} else if strings.ContainsAny(origin, "/?#") {
		return "", fmt.Errorf("源站地址填域名（可带端口），不用写路径")
	}
	if !validWorkerHost(host) {
		return "", fmt.Errorf("源站地址不对：填域名（可带端口）")
	}
	if workerOriginIsIP(host) {
		return "", fmt.Errorf("源站地址不能填 IP 地址：Cloudflare Worker 的 fetch() 不能直接用 IP 回源，会报 error 1003；先在 Cloudflare 里给面板建个域名（灰云 A 记录指到面板 IP），再填 http://域名:11111 重新生成")
	}
	return scheme + "://" + host, nil
}

// workerOriginIsIP 判断一个源站地址（可带 http(s):// 前缀与端口）的
// host 是不是 IP 字面量（IPv4 或 IPv6 括号形式都算）。
func workerOriginIsIP(addr string) bool {
	h := addr
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.Index(h, "/"); i >= 0 {
		h = h[:i]
	}
	if strings.HasPrefix(h, "[") {
		if i := strings.Index(h, "]"); i >= 0 {
			h = h[1:i]
		}
	} else if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return net.ParseIP(h) != nil
}

// validWorkerHost 校验 Worker 回源地址的 host 部分：字母数字、点、横杠、
// 下划线，可带 :端口；不含其他字符（代码拼进 JS 字符串时不会注入）。
func validWorkerHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i+1:], "]") {
		port := h[i+1:]
		if port == "" {
			return false
		}
		for _, c := range port {
			if c < '0' || c > '9' {
				return false
			}
		}
		h = h[:i]
	}
	if h == "" {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// renderWorkerCode 生成 Cloudflare Worker 反代代码：只代理 /sub/ 开头的
// 订阅请求，原样转发查询参数（?format、?mode）与 User-Agent（格式自适
// 应靠它），状态码、响应头与响应体原样返回；其他路径一律 404，
// 不把面板其他接口暴露出去。publicDomain 是订阅对外域名（v1.3.2）：
// 填了之后代码注释里的示例订阅链接用这个域名，没填时保留 <Worker 域名> 占位。
func renderWorkerCode(origin, publicDomain, subPath string) string {
	example := "https://<Worker 域名>/" + subPath + "/<令牌>?format=clash&mode=split"
	if publicDomain != "" {
		example = "https://" + publicDomain + "/" + subPath + "/<令牌>?format=clash&mode=split"
	}
	// 源站是 IP 时代码照常生成（默认用法不能拦），但在代码里写明会 1003，
	// 免得用户部署完才发现订阅 403。
	warn := ""
	if workerOriginIsIP(origin) {
		warn = "// 注意：下面 ORIGIN 是 IP 地址，Worker 的 fetch() 不能直接用 IP 回源，\n// 部署后订阅会报 Cloudflare error 1003：先在 Cloudflare 里给面板建个\n// 域名（灰云 A 记录指到面板 IP），再填 http://域名:11111 重新生成代码。\n"
	}
	return `// relay-panel 订阅反代（Cloudflare Worker）：
// 部署：Cloudflare 后台 → Workers → 新建 Worker → 粘贴本代码 → 部署；
// 可选：在 Workers → 触发器（Triggers）里绑一个自定义域名。
// 部署后订阅链接 = ` + example + `
// （令牌与参数和原来完全一样，只是把面板地址换成 Worker 地址）。
// 源站地址只出现在这里（你的 Cloudflare 账号内可见），客户端看不到面板真实地址。
` + warn + `const ORIGIN = "` + origin + `";
export default {
  async fetch(request) {
    const url = new URL(request.url);
    if (!url.pathname.startsWith("/` + subPath + `/")) {
      return new Response("Not Found", { status: 404 });
    }
    const target = new URL(ORIGIN + url.pathname + url.search);
    const res = await fetch(target.toString(), {
      method: "GET",
      headers: { "User-Agent": request.headers.get("User-Agent") || "" },
      redirect: "manual",
    });
    return new Response(res.body, { status: res.status, headers: res.headers });
  },
};
`
}

// handleWorkerCode 生成 Worker 反代代码（会话认证）：按当前请求的 origin
// 参数或打开面板的地址定源站，返回代码与源站地址。源站是 IP 时不拦
// （默认用法），但返回里带 warning 提示会 1003，前端显示出来。
// v1.3.5：记住用户填的源站地址——点“生成 Worker 代码”时输入框非空就把
// 填的值存下来，下次打开设置页自动回填；留空生成时把之前存过的清空
// （留空就是默认用法）。保存失败不影响本次生成，只在 warning 里提一句。
func (s *Server) handleWorkerCode(w http.ResponseWriter, r *http.Request) {
	origin, err := workerOrigin(r)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	saveWarn := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("origin")); raw != s.cfg.WorkerOrigin {
		old := s.cfg.WorkerOrigin
		s.cfg.WorkerOrigin = raw
		if err := s.saveConfig(); err != nil {
			s.cfg.WorkerOrigin = old
			saveWarn = "源站地址没记住（保存失败，下次打开还要重填）。"
		}
	}
	resp := map[string]any{"origin": origin, "code": renderWorkerCode(origin, s.cfg.SubPublicDomain, s.subPath())}
	if workerOriginIsIP(origin) {
		resp["warning"] = "源站地址是 IP，Worker 回源会被 Cloudflare 挡（error 1003）：先在 Cloudflare 里给面板建个域名（灰云 A 记录指到面板 IP），再填 http://域名:11111 重新生成代码。"
	}
	if saveWarn != "" {
		if prev, ok := resp["warning"].(string); ok && prev != "" {
			resp["warning"] = prev + " " + saveWarn
		} else {
			resp["warning"] = saveWarn
		}
	}
	writeJSON(w, 200, okResp(resp))
}

// handleSubQR 订阅链接二维码（v1.2.9，会话认证）：
// POST /api/v1/subscription/qr，请求 {"url": "<订阅链接>"}，
// 返回 {"qr": "data:image/png;base64,..."}。
// 复用节点链接同一套 qrDataURI（Go 原生 qrcode，Medium 纠错，256px，
// 不新引库）。只接受本面板的订阅链接（须含 /sub/sub_），不接受任意文本；
// 订阅链接约 150–160 字，长度上限放宽到 2048 字符。
// 注意：订阅链接二维码由前端把当前显示的链接传进来生成（v1.3.7 起也可以
// 先调取回链接接口再传）；换 mode/format 后前端重新调一次，二维码跟着变。
func (s *Server) handleSubQR(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	u := strings.TrimSpace(req.URL)
	if u == "" || len(u) > 2048 || !strings.Contains(u, "/"+s.subPath()+"/sub_") {
		writeJSON(w, 400, errResp("只支持本面板的订阅链接"))
		return
	}
	q, err := qrDataURI(u)
	if err != nil {
		writeJSON(w, 500, errResp("二维码生成失败："+err.Error()))
		return
	}
	writeJSON(w, 200, okResp(map[string]any{"qr": q}))
}

// ---------- 合并订阅（会话认证） ----------

// canonicalSelection 勾选集合规范化：去重、按面板+落地排序，拼成唯一键。
// 同一键就是同一份选择（顺序不同也算同一份）。
func canonicalSelection(sel []SubSelection) ([]SubSelection, string) {
	seen := map[SubSelection]bool{}
	out := []SubSelection{}
	for _, e := range sel {
		if e.Panel == "" || e.Landing < 1 || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Panel != out[j].Panel {
			return out[i].Panel < out[j].Panel
		}
		return out[i].Landing < out[j].Landing
	})
	var parts []string
	for _, e := range out {
		parts = append(parts, e.Panel+":"+strconv.Itoa(e.Landing))
	}
	return out, strings.Join(parts, ",")
}

// validateMergedSelection 校验勾选集合：非空、面板存在、落地编号存在。
// 远程面板当场拉一次状态验版本（低于 1.2.7 没有 central/subscription 接口）与落地。
func (s *Server) validateMergedSelection(sel []SubSelection) ([]SubSelection, string, error) {
	sel, key := canonicalSelection(sel)
	if len(sel) == 0 {
		return nil, "", fmt.Errorf("请先勾选至少一个落地")
	}
	// 本地 relay.conf 读不到时本地落地集合就是空（管理面板模式本机没有
	// 中转，本来就没有本地落地；只勾远程落地时不应被挡住）。勾了“本面板”
	// 的落地时，下面按“本面板没有编号 N 的落地”拒绝，文案仍然准确。
	rc, _, _ := s.readRelayConf()
	localIdx := map[int]bool{}
	if rc != nil {
		for _, l := range rc.Landings {
			localIdx[l.Index] = true
		}
	}
	for _, e := range sel {
		if e.Panel == "local" {
			if !localIdx[e.Landing] {
				return nil, "", fmt.Errorf("本面板没有编号 %d 的落地", e.Landing)
			}
			continue
		}
		rem := s.remoteByID(e.Panel)
		if rem == nil {
			return nil, "", fmt.Errorf("没有登记过这台远程面板")
		}
		snap, err := s.fetchRemoteStatus(*rem)
		if err != nil {
			_, msg := remoteSideOf(err)
			return nil, "", fmt.Errorf("远程面板 %s 连不上：%s", rem.Name, msg)
		}
		if snap.Version != "" && isNewerVersion("1.2.7", snap.Version) {
			return nil, "", fmt.Errorf("远程面板 %s 版本太旧（%s，需要 1.2.7+），请先升级远程面板", rem.Name, snap.Version)
		}
		found := false
		for _, l := range snap.Landings {
			if l.Index == e.Landing {
				found = true
				break
			}
		}
		if !found {
			return nil, "", fmt.Errorf("远程面板 %s 没有编号 %d 的落地", rem.Name, e.Landing)
		}
	}
	return sel, key, nil
}

// selectionSummary 把勾选集合写成人话（合并订阅列表与审计日志用）：
// “本面板：智利、香港plus：智利”。名字取不到时用落地地址。
func (s *Server) selectionSummary(sel []SubSelection) string {
	rc, _, _ := s.readRelayConf()
	localName := map[int]string{}
	if rc != nil {
		for _, l := range rc.Landings {
			localName[l.Index] = l.displayName()
		}
	}
	byPanel := map[string][]string{}
	var order []string
	for _, e := range sel {
		panelName := e.Panel
		landingName := ""
		if e.Panel == "local" {
			panelName = "本面板"
			landingName = localName[e.Landing]
		} else {
			if rem := s.remoteByID(e.Panel); rem != nil {
				panelName = rem.Name
			}
			s.centralMu.Lock()
			snap := s.centralCache[e.Panel]
			s.centralMu.Unlock()
			if snap != nil {
				for _, l := range snap.Landings {
					if l.Index == e.Landing {
						landingName = remoteLandingDisplayName(l)
						break
					}
				}
			}
		}
		if landingName == "" {
			landingName = "#" + strconv.Itoa(e.Landing)
		}
		if _, ok := byPanel[panelName]; !ok {
			order = append(order, panelName)
		}
		byPanel[panelName] = append(byPanel[panelName], landingName)
	}
	var parts []string
	for _, p := range order {
		parts = append(parts, p+"："+strings.Join(byPanel[p], "、"))
	}
	return strings.Join(parts, "；")
}

// remoteLandingDisplayName 远程落地显示名（快照里有备注名就用，没有用地址）。
func remoteLandingDisplayName(l remoteLandingSummary) string {
	if strings.TrimSpace(l.Name) != "" {
		return strings.TrimSpace(l.Name)
	}
	if l.Landing != "" {
		return l.Landing
	}
	return l.Domain
}

// handleMergedSubsList 列出已生成的合并订阅（不含令牌与链接；has_link 表示
// 有没有密文，有就能点复制重新拿链接，没有就是 1.3.7 之前的旧链接）。
func (s *Server) handleMergedSubsList(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		CreatedAt string         `json:"created_at"`
		Selection []SubSelection `json:"selection"`
		Summary   string         `json:"summary"`
		HasLink   bool           `json:"has_link"`
		Mode      string         `json:"mode"`                 // both|relay|direct，老记录空 = 全部
		ExpiresAt string         `json:"expires_at,omitempty"` // v1.3.20：RFC3339，空 = 永久
		// 流量上限（v1.3.25）：与订阅用户列表同一套字段含义。
		LimitMB      int64  `json:"traffic_limit_mb"`
		LimitMode    string `json:"traffic_limit_mode,omitempty"`
		ResetMode    string `json:"reset_mode,omitempty"`
		ResetDay     int    `json:"reset_day,omitempty"`
		NextResetAt  string `json:"next_reset_at,omitempty"`
		Used         int64  `json:"traffic_used"`
		HasUsed      bool   `json:"has_used"`
		Disabled     bool   `json:"traffic_disabled"`
		ForwardCount int    `json:"forward_count"`
		// v1.3.27：流量采样失败提示（空 = 正常；有值时列表用量不可信）。
		TrafficWarn string `json:"traffic_warn,omitempty"`
		// v1.3.27：限流的远程版本警告（远程面板低于 1.3.25 时限流不生效）。
		LimitWarn string `json:"limit_warn,omitempty"`
		// 分组（v1.3.26）：GroupID 空 = 不分组；GroupName 是分组显示名
		// （列表徽章用）；进了组时 Summary 是组的落地摘要。
		GroupID   string `json:"group_id,omitempty"`
		GroupName string `json:"group_name,omitempty"`
	}
	items := []item{}
	for _, ms := range s.cfg.MergedSubs {
		if ms.Selection == nil {
			ms.Selection = []SubSelection{}
		}
		summary := s.selectionSummary(ms.Selection)
		if ms.GroupID != "" {
			summary = "分组「" + s.groupNameOf(ms.GroupID) + "」：" + s.selectionSummary(s.effectiveSelectionForMerged(&ms))
		}
		used, _, hasUsed := s.userUsageSummary(userSubKey("merged", ms.ID), ms.TrafficLimitBytes, ms.TrafficLimitMode)
		limitWarn := ""
		if ms.TrafficLimitBytes > 0 {
			limitWarn = s.userLimitVersionWarn(s.effectiveSelectionForMerged(&ms))
		}
		items = append(items, item{ID: ms.ID, Name: ms.Name, CreatedAt: ms.CreatedAt, Selection: ms.Selection, Summary: summary, HasLink: ms.TokenEnc != "", Mode: ms.Mode, ExpiresAt: ms.ExpiresAt, LimitMB: ms.TrafficLimitBytes / 1024 / 1024, LimitMode: ms.TrafficLimitMode, ResetMode: ms.ResetMode, ResetDay: ms.ResetDay, NextResetAt: ms.NextResetAt, Used: used, HasUsed: hasUsed, Disabled: ms.Disabled, ForwardCount: len(s.userForwardsFor(userSubKey("merged", ms.ID))), TrafficWarn: s.userTrafficWarnFor(userSubKey("merged", ms.ID), s.effectiveSelectionForMerged(&ms)), LimitWarn: limitWarn, GroupID: ms.GroupID, GroupName: s.groupNameOf(ms.GroupID)})
	}
	writeJSON(w, 200, okResp(map[string]any{"subs": items}))
}

// handleMergedSubsCreate 生成合并订阅：不填名称时同一选择重新生成 = 轮换
// （旧链接立即失效）；填了名称（归属到人）就新起一条——同一勾选集合可有
// 多个不同名称的令牌，各归各人，撤销只影响那一个。链接在这一次返回，
// 之后随时能点复制重新拿。
// v1.3.17：请求可带 mode（both|relay|direct，不带默认 both），存进记录；
// 同一选择重新生成时按表单当前选择存（生成表单的显式选择）；专用轮换接口
// （handleMergedSubsRotate）不带 mode，轮换时保持原模式不变。
// v1.3.20：请求可带 expires_at（日期或 RFC3339，空串 = 永久；不带 = 永久；
// 同一选择重新生成时不带 expires_at 字段 = 保持原到期时间）。
func (s *Server) handleMergedSubsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Selection []SubSelection `json:"selection"`
		Name      string         `json:"name"`
		Mode      string         `json:"mode"`
		ExpiresAt *string        `json:"expires_at"`
		Timezone  string         `json:"timezone,omitempty"`
		// 分组（v1.3.26）：指针 = 没给保持原分组（轮换那条的）；给了 "" =
		// 不分组，给了 "sg-…" = 进组（订阅按组的落地出，自己的勾选保留）。
		GroupID *string `json:"group_id"`
		// 流量上限（v1.3.25）：指针 = 没给保持原值（轮换时）或不限流
		// （新建时），给了按新值（给 0 = 取消限流）。
		TrafficLimitMB   *int64  `json:"traffic_limit_mb"`
		TrafficLimitMode *string `json:"traffic_limit_mode"`
		ResetMode        *string `json:"reset_mode"`
		ResetDay         *int    `json:"reset_day"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 64 {
		writeJSON(w, 400, errResp("名字太长，最多 64 个字"))
		return
	}
	sel, key, err := s.validateMergedSelection(req.Selection)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	// 分组（v1.3.26）：没给 group_id = 保持原分组（轮换那条的）；给了就校验。
	groupGiven := false
	groupID := ""
	if req.GroupID != nil {
		groupGiven = true
		var err error
		groupID, err = s.validateGroupID(*req.GroupID)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	subMode, err := normalizeSubMode(req.Mode)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	expiresAt := ""
	if req.ExpiresAt != nil {
		expiresAt, err = parseSubExpiryIn(*req.ExpiresAt, req.Timezone)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	tok := generateSubToken()
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		writeJSON(w, 500, errResp("令牌加密失败："+err.Error()))
		return
	}
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339)
	rotated := false
	id := ""
	// 限流字段：新建时没给 = 不限流；轮换时没给 = 保持原值，给了按新值。
	// anchor 是订阅创建时间（新建是现在，轮换时 CreatedAt 会刷新到
	// 现在，anniversary 锚点跟着走）。
	limitBytes, limitMode, resetMode, resetDay, nextResetAt := int64(0), "", "", 0, ""
	limitOf := func(curLimit int64, curMode, curResetMode string, curDay int) (int64, string, string, int, string, error) {
		limit := curLimit
		mode, rm, day := curMode, curResetMode, curDay
		if req.TrafficLimitMB != nil {
			limit = mbToBytes(*req.TrafficLimitMB)
		}
		if req.TrafficLimitMode != nil {
			mode = *req.TrafficLimitMode
		}
		if req.ResetMode != nil {
			rm = *req.ResetMode
		}
		if req.ResetDay != nil {
			day = *req.ResetDay
		}
		return normalizeUserLimitFields(limit, mode, rm, day, nowTime, nowTime)
	}
	if name != "" {
		var err error
		limitBytes, limitMode, resetMode, resetDay, nextResetAt, err = limitOf(0, "", "", 0)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
		id = "ms-" + randomTokenHex(6)
		s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: id, Name: name, TokenHash: hashSubToken(tok), TokenEnc: enc, CreatedAt: now, Selection: sel, Mode: subMode, ExpiresAt: expiresAt, TrafficLimitBytes: limitBytes, TrafficLimitMode: limitMode, ResetMode: resetMode, ResetDay: resetDay, NextResetAt: nextResetAt})
	} else {
		for i := range s.cfg.MergedSubs {
			// 只轮换同样没填名称的记录：已归属到人的那条不能被误轮换
			if s.cfg.MergedSubs[i].Name != "" {
				continue
			}
			if _, k := canonicalSelection(s.cfg.MergedSubs[i].Selection); k == key {
				var err error
				limitBytes, limitMode, resetMode, resetDay, nextResetAt, err = limitOf(s.cfg.MergedSubs[i].TrafficLimitBytes, s.cfg.MergedSubs[i].TrafficLimitMode, s.cfg.MergedSubs[i].ResetMode, s.cfg.MergedSubs[i].ResetDay)
				if err != nil {
					writeJSON(w, 400, errResp(err.Error()))
					return
				}
				s.cfg.MergedSubs[i].TokenHash = hashSubToken(tok)
				s.cfg.MergedSubs[i].TokenEnc = enc
				s.cfg.MergedSubs[i].CreatedAt = now
				// 同一选择重新生成：按表单当前选择存；表单没传模式时保持原模式
				if strings.TrimSpace(req.Mode) != "" {
					s.cfg.MergedSubs[i].Mode = subMode
				} else {
					subMode = s.cfg.MergedSubs[i].Mode
					if subMode == "" {
						subMode = "both"
					}
				}
				// 同一选择重新生成：表单传了 expires_at 才改到期时间，没传保持原到期时间
				if req.ExpiresAt != nil {
					s.cfg.MergedSubs[i].ExpiresAt = expiresAt
				} else {
					expiresAt = s.cfg.MergedSubs[i].ExpiresAt
				}
				s.cfg.MergedSubs[i].TrafficLimitBytes = limitBytes
				s.cfg.MergedSubs[i].TrafficLimitMode = limitMode
				s.cfg.MergedSubs[i].ResetMode = resetMode
				s.cfg.MergedSubs[i].ResetDay = resetDay
				s.cfg.MergedSubs[i].NextResetAt = nextResetAt
				id = s.cfg.MergedSubs[i].ID
				rotated = true
				break
			}
		}
		if id == "" {
			var err error
			limitBytes, limitMode, resetMode, resetDay, nextResetAt, err = limitOf(0, "", "", 0)
			if err != nil {
				writeJSON(w, 400, errResp(err.Error()))
				return
			}
			id = "ms-" + randomTokenHex(6)
			s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: id, TokenHash: hashSubToken(tok), TokenEnc: enc, CreatedAt: now, Selection: sel, Mode: subMode, ExpiresAt: expiresAt, TrafficLimitBytes: limitBytes, TrafficLimitMode: limitMode, ResetMode: resetMode, ResetDay: resetDay, NextResetAt: nextResetAt})
		}
	}
	// 分组（v1.3.26）：新建与轮换都按请求里的 group_id 归属；没给就保持
	// 轮换那条的原分组。生效勾选 = 组的落地集合（进了组）或自己的勾选。
	finalGroupID := ""
	if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
		if groupGiven {
			ms.GroupID = groupID
		}
		finalGroupID = ms.GroupID
	}
	effSel := sel
	if finalGroupID != "" {
		if g := s.findSubGroup(finalGroupID); g != nil {
			effSel = g.Selection
		}
	}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	action := "生成合并订阅"
	if rotated {
		action = "轮换合并订阅"
	}
	target := s.selectionSummary(effSel)
	if finalGroupID != "" {
		target = "分组「" + s.groupNameOf(finalGroupID) + "」：" + target
	}
	if name != "" {
		target = name + "（" + target + "）"
	}
	resp := map[string]any{"id": id, "url": s.subURL(r, tok), "rotated": rotated, "selection": sel, "summary": s.selectionSummary(effSel), "mode": subMode, "expires_at": expiresAt, "group_id": finalGroupID, "traffic_limit_mb": limitBytes / 1024 / 1024, "traffic_limit_mode": limitMode, "reset_mode": resetMode, "reset_day": resetDay, "next_reset_at": nextResetAt}
	if warn := s.syncUserForwardsWarning("merged", id, name, effSel, limitBytes, limitMode, resetMode, resetDay, nextResetAt); warn != "" {
		resp["warning"] = warn
		s.audit(s.currentUser(r), action, target, "部分成功", warn)
	} else {
		s.audit(s.currentUser(r), action, target, "成功", "链接已加密存好，随时能复制")
	}
	writeJSON(w, 200, okResp(resp))
}

// handleMergedSubsRotate 轮换一条合并订阅：同一勾选集合与同一归属人换一个
// 新令牌，旧链接立即失效；新链接在这一次返回（弹窗里直接显示），之后
// 随时能点复制重新拿。模式保持不变（不带 mode 参数）。
func (s *Server) handleMergedSubsRotate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for i := range s.cfg.MergedSubs {
		if s.cfg.MergedSubs[i].ID != id {
			continue
		}
		tok := generateSubToken()
		enc, err := s.encryptSubTokenNow(tok)
		if err != nil {
			writeJSON(w, 500, errResp("令牌加密失败："+err.Error()))
			return
		}
		s.cfg.MergedSubs[i].TokenHash = hashSubToken(tok)
		s.cfg.MergedSubs[i].TokenEnc = enc
		s.cfg.MergedSubs[i].CreatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.saveConfig(); err != nil {
			writeJSON(w, 500, errResp("保存失败："+err.Error()))
			return
		}
		sel := s.cfg.MergedSubs[i].Selection
		summary := s.selectionSummary(sel)
		target := summary
		if s.cfg.MergedSubs[i].Name != "" {
			target = s.cfg.MergedSubs[i].Name + "（" + summary + "）"
		}
		s.audit(s.currentUser(r), "轮换合并订阅", target, "成功", "旧订阅链接已失效")
		mode := s.cfg.MergedSubs[i].Mode
		if mode == "" {
			mode = "both"
		}
		writeJSON(w, 200, okResp(map[string]any{"id": id, "url": s.subURL(r, tok), "rotated": true, "selection": sel, "summary": summary, "mode": mode}))
		return
	}
	writeJSON(w, 404, errResp("没有这条合并订阅"))
}

// handleMergedSubsDelete 撤销一条合并订阅（该订阅链接立即失效）。
func (s *Server) handleMergedSubsDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for i := range s.cfg.MergedSubs {
		if s.cfg.MergedSubs[i].ID == id {
			summary := s.selectionSummary(s.effectiveSelectionForMerged(&s.cfg.MergedSubs[i]))
			// 分组（v1.3.26）：进了组的按组的落地集合回收远程转发。
			sel := s.effectiveSelectionForMerged(&s.cfg.MergedSubs[i])
			s.cfg.MergedSubs = append(s.cfg.MergedSubs[:i], s.cfg.MergedSubs[i+1:]...)
			if err := s.saveConfig(); err != nil {
				writeJSON(w, 500, errResp("保存失败："+err.Error()))
				return
			}
			// 按人限流（v1.3.25）：订阅删了就回收它的转发与端口
			// （本机 + 各远程面板）。
			s.opMu.Lock()
			recErr := s.removeUserForwards("merged", id)
			s.opMu.Unlock()
			var warns []string
			if recErr != nil {
				warns = append(warns, "本机转发没撤干净："+recErr.Error())
			}
			warns = append(warns, s.removeUserForwardsFromRemotes(userSubKey("merged", id), sel)...)
			resp := map[string]any{}
			if len(warns) > 0 {
				resp["warning"] = "订阅已删，但" + strings.Join(warns, "；") + "，请检查中转机上的转发。"
				s.audit(s.currentUser(r), "撤销合并订阅", summary, "部分成功", strings.Join(warns, "；"))
			} else {
				s.audit(s.currentUser(r), "撤销合并订阅", summary, "成功", "该订阅链接已失效")
			}
			writeJSON(w, 200, okResp(resp))
			return
		}
	}
	writeJSON(w, 404, errResp("没有这条合并订阅"))
}

// handleMergedSubsUpdate 编辑一条合并订阅：改勾选集合/内容模式/归属人名，
// 令牌不动（TokenHash/TokenEnc 原样保留），链接不变，下次取订阅即按新
// 勾选与新模式出内容。勾选不能为空（合并订阅至少要一个落地）；与轮换
// 互不干扰——轮换换令牌、编辑不换令牌。
// v1.3.20：请求可带 expires_at（日期或 RFC3339，空串 = 永久）；不带
// expires_at 字段时保持原到期时间。
func (s *Server) handleMergedSubsUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Selection []SubSelection `json:"selection"`
		Name      string         `json:"name"`
		Mode      string         `json:"mode"`
		ExpiresAt *string        `json:"expires_at"`
		Timezone  string         `json:"timezone,omitempty"`
		// 分组（v1.3.26）：指针 = 没给保持原分组；给了 "" = 出组（恢复自己
		// 勾选），给了 "sg-…" = 进组（订阅按组的落地出，自己的勾选保留在
		// 记录里不动）。进组时请求里的 selection 不动自己的勾选。
		GroupID *string `json:"group_id"`
		// 流量上限（v1.3.25）：指针 = 没给保持原值，给了按新值
		// （limit_mb 给 0 = 取消限流，回共享端口）。
		TrafficLimitMB   *int64  `json:"traffic_limit_mb"`
		TrafficLimitMode *string `json:"traffic_limit_mode"`
		ResetMode        *string `json:"reset_mode"`
		ResetDay         *int    `json:"reset_day"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 64 {
		writeJSON(w, 400, errResp("名字太长，最多 64 个字"))
		return
	}
	sel, _, err := s.validateMergedSelection(req.Selection)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	// 分组（v1.3.26）：没给 group_id = 保持原分组；给了 "" = 出组；
	// 给了 "sg-…" = 进组（校验存在）。
	groupGiven := false
	groupID := ""
	if req.GroupID != nil {
		groupGiven = true
		var err error
		groupID, err = s.validateGroupID(*req.GroupID)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	subMode, err := normalizeSubMode(req.Mode)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	expiresAt := ""
	expiresGiven := false
	if req.ExpiresAt != nil {
		expiresGiven = true
		expiresAt, err = parseSubExpiryIn(*req.ExpiresAt, req.Timezone)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	for i := range s.cfg.MergedSubs {
		if s.cfg.MergedSubs[i].ID != id {
			continue
		}
		ms := &s.cfg.MergedSubs[i]
		// 限流字段：没给的保持原值，给了的按新值算（anchor 是订阅创建
		// 时间，anniversary 取它的日；没给限流时直接用原字节数，
		// 不经过 MB 换算，避免丢精度）。
		newLimit := ms.TrafficLimitBytes
		newMode, newResetMode, newResetDay := ms.TrafficLimitMode, ms.ResetMode, ms.ResetDay
		if req.TrafficLimitMB != nil {
			newLimit = mbToBytes(*req.TrafficLimitMB)
		}
		if req.TrafficLimitMode != nil {
			newMode = *req.TrafficLimitMode
		}
		if req.ResetMode != nil {
			newResetMode = *req.ResetMode
		}
		if req.ResetDay != nil {
			newResetDay = *req.ResetDay
		}
		anchor, _ := time.Parse(time.RFC3339, ms.CreatedAt)
		limitBytes, limitMode, resetMode, resetDay, nextResetAt, err := normalizeUserLimitFields(newLimit, newMode, newResetMode, newResetDay, anchor, time.Now().UTC())
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
		// 调高上限后不再超量就自动恢复（清停用标志，让 reconcile 把
		// endpoints 加回去）；仍超量就继续停用。
		s.maybeResumeUser(userSubKey("merged", id), limitBytes, limitMode)
		// 分组（v1.3.26）：给了 group_id 就改归属；进了组，自己的
		// Selection 不动（保留在记录里，出组后恢复），订阅按组的落地出；
		// 出组后按请求里的新勾选出。
		if groupGiven {
			ms.GroupID = groupID
		}
		if ms.GroupID == "" {
			ms.Selection = sel
		}
		effSel := s.effectiveSelectionForMerged(ms)
		ms.Mode = subMode
		ms.Name = name
		ms.TrafficLimitBytes = limitBytes
		ms.TrafficLimitMode = limitMode
		ms.ResetMode = resetMode
		ms.ResetDay = resetDay
		ms.NextResetAt = nextResetAt
		if expiresGiven {
			ms.ExpiresAt = expiresAt
		}
		if err := s.saveConfig(); err != nil {
			writeJSON(w, 500, errResp("保存失败："+err.Error()))
			return
		}
		summary := s.selectionSummary(effSel)
		if ms.GroupID != "" {
			summary = "分组「" + s.groupNameOf(ms.GroupID) + "」：" + summary
		}
		target := summary
		if name != "" {
			target = name + "（" + summary + "）"
		}
		resp := map[string]any{"id": id, "name": name, "summary": summary, "mode": subMode, "selection": ms.Selection, "expires_at": ms.ExpiresAt, "group_id": ms.GroupID, "traffic_limit_mb": limitBytes / 1024 / 1024, "traffic_limit_mode": limitMode, "reset_mode": resetMode, "reset_day": resetDay, "next_reset_at": nextResetAt}
		if warn := s.syncUserForwardsWarning("merged", id, name, effSel, limitBytes, limitMode, resetMode, resetDay, nextResetAt); warn != "" {
			resp["warning"] = warn
			s.audit(s.currentUser(r), "编辑合并订阅", target, "部分成功", warn)
		} else {
			s.audit(s.currentUser(r), "编辑合并订阅", target, "成功", "链接不变，下次更新订阅即按新勾选同步")
		}
		writeJSON(w, 200, okResp(resp))
		return
	}
	writeJSON(w, 404, errResp("没有这条合并订阅"))
}

// ---------- 按订阅限流：手动恢复（v1.3.25） ----------
// resumeSub 手动恢复一条订阅（超量停用后）：先看当前用量——仍超上限
// 就不恢复（恢复了下一分钟也会再停用，提示先调高上限或等下个周期）；
// 已在上限以下就清停用、把 endpoints 加回去。没开限流的也不用恢复。
func (s *Server) resumeSub(kind, id string) error {
	key := userSubKey(kind, id)
	limit, mode, _, _, _, _, ok := s.userLimitForKey(key)
	if !ok || limit <= 0 {
		return fmt.Errorf("这条订阅没开限流，不用恢复")
	}
	rec := s.userTraffic.get(key)
	used := userTrafficUsed(rec, mode)
	if used >= limit {
		return fmt.Errorf("流量还没回到上限以下（已用 %s / 上限 %s），恢复了下一分钟也会再停用；请先调高上限或等下个周期", formatBytes(used), formatBytes(limit))
	}
	return s.resumeUserForwards(key)
}

// handleSubUsersResume 手动恢复一个订阅用户（超量停用后）。
func (s *Server) handleSubUsersResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	found := false
	name := ""
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].ID == id {
			found = true
			name = s.cfg.SubUsers[i].Name
			break
		}
	}
	if !found {
		writeJSON(w, 404, errResp("没有这个订阅用户"))
		return
	}
	if err := s.resumeSub("subuser", id); err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	s.audit(s.currentUser(r), "手动恢复订阅用户", name, "成功", "流量已回到上限以下，转发已恢复")
	writeJSON(w, 200, okResp(map[string]any{"id": id, "name": name}))
}

// handleMergedSubsResume 手动恢复一条合并订阅（超量停用后）。
func (s *Server) handleMergedSubsResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	found := false
	name := ""
	var sel []SubSelection
	for i := range s.cfg.MergedSubs {
		if s.cfg.MergedSubs[i].ID == id {
			found = true
			name = s.cfg.MergedSubs[i].Name
			sel = s.cfg.MergedSubs[i].Selection
			break
		}
	}
	if !found {
		writeJSON(w, 404, errResp("没有这条合并订阅"))
		return
	}
	if err := s.resumeSub("merged", id); err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	target := name
	if target == "" {
		target = s.selectionSummary(sel)
	}
	s.audit(s.currentUser(r), "手动恢复合并订阅", target, "成功", "流量已回到上限以下，转发已恢复")
	writeJSON(w, 200, okResp(map[string]any{"id": id, "name": name}))
}

// ---------- 合并订阅的内容来源 ----------

// fetchRemoteSubLinks 从一台远程面板取指定落地的节点链接（走 central 令牌通道）：
// 远程面板连不上或版本太旧时返回空（合并订阅里这台暂时跳过，下次拉取自动恢复）。
// forUser 不为空时带上 for_user 参数（v1.3.25）：远程按该订阅在它
// 那边的转发改写中转节点链接端口；远程版本太旧（没这个参数概念）
// 时忽略参数、返回共享端口的链接（走共享端口、不限流）。
func (s *Server) fetchRemoteSubLinks(rem RemotePanel, indices []int, forUser string) []subLink {
	var parts []string
	for _, n := range indices {
		parts = append(parts, strconv.Itoa(n))
	}
	path := "/api/v1/central/subscription?landings=" + strings.Join(parts, ",")
	if forUser != "" {
		path += "&for_user=" + url.QueryEscape(forUser)
	}
	_, body, err := s.remoteCall(rem, "GET", path, nil, 15*time.Second)
	if err != nil {
		return nil
	}
	var resp struct {
		OK    bool      `json:"ok"`
		Links []subLink `json:"links"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || !resp.OK {
		return nil
	}
	if resp.Links == nil {
		return nil
	}
	return resp.Links
}

// collectMergedSubLinks 按一份合并订阅的勾选集合取全部节点链接（实时生成）：
// 本面板的部分读 nodes.txt，远程的部分当场去各远程面板取。
// forUser 不为空时远程链接带 for_user 参数（v1.3.25，按人转发改写端口）。
// 返回链接与其中本面板落地的编号集合（给 subscription-userinfo 用）。
func (s *Server) collectMergedSubLinks(ms *MergedSub, forUser string) ([]subLink, map[int]bool) {
	localIndices := map[int]bool{}
	remoteIndices := map[string][]int{}
	for _, e := range ms.Selection {
		if e.Panel == "local" {
			localIndices[e.Landing] = true
		} else {
			remoteIndices[e.Panel] = append(remoteIndices[e.Panel], e.Landing)
		}
	}
	var out []subLink
	if len(localIndices) > 0 {
		out = append(out, s.collectSubLinks(localIndices)...)
	}
	for id, indices := range remoteIndices {
		rem := s.remoteByID(id)
		if rem == nil {
			continue
		}
		out = append(out, s.fetchRemoteSubLinks(*rem, indices, forUser)...)
	}
	return out, localIndices
}

// ---------- 被管端：订阅内容（令牌认证） ----------

// handleCentralSubscription 被管端订阅内容（令牌认证，第 7 个 central/* 接口）：
// GET /api/v1/central/subscription?landings=1,2，返回指定落地的节点链接
// （供主面板合并订阅用；只含节点链接，不含令牌与面板其他信息）。
// landings 为空 = 全部落地。
func (s *Server) handleCentralSubscription(w http.ResponseWriter, r *http.Request) {
	var indices map[int]bool
	if v := strings.TrimSpace(r.URL.Query().Get("landings")); v != "" {
		indices = map[int]bool{}
		for _, part := range strings.Split(v, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || n < 1 {
				writeJSON(w, 400, errResp("landings 参数不对：填落地编号，逗号分隔"))
				return
			}
			indices[n] = true
		}
	}
	links := s.collectSubLinks(indices)
	if links == nil {
		links = []subLink{}
	}
	// for_user（v1.3.25）：按该订阅在本面板上的转发改写中转节点链接
	// 端口（远程面板是其转发记录的唯一事实来源，改写在这里做）。
	// 没这订阅的转发时原样返回（走共享端口），for_user_applied=false。
	forUser := strings.TrimSpace(r.URL.Query().Get("for_user"))
	applied := false
	if forUser != "" {
		if ports := s.userPortByAddr(forUser); len(ports) > 0 {
			links = rewriteSubLinksForUser(links, ports)
			applied = true
		}
	}
	writeJSON(w, 200, okResp(map[string]any{"links": links, "for_user_applied": applied, "panel_version": Version}))
}
