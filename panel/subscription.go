package main

// ---------- 订阅链接（v1.2.7） ----------
// 机场式订阅：一个链接导入，客户端一次拿到全部节点。
//   - 单面板订阅：GET /sub/:token，内容是本面板全部落地的 twin 链接，实时生成，
//     面板里增删节点后订阅内容自动跟着变，不用重新导出。
//   - 合并订阅：主面板按勾选的落地集合（本面板 + 多台远程面板）生成，每份选择
//     一条令牌，不同选择绝不共用一个令牌；内容同样实时生成（远程部分当场去
//     远程面板取，取不到的那台暂时跳过，下次拉取时自动恢复）。
//   - 格式：?format=clash|base64|plain 手动指定优先；不指定时按 User-Agent
//     自动识别——含 clash/stash/mihomo（不区分大小写）返回 Clash YAML，
//     其余返回 base64 节点列表。
//   - 模式（v1.2.9，只影响 Clash YAML）：?mode=split|global，缺省 split；
//     split 是分流（TikTok/社交/流媒体走节点，其他直连），global 是全局
//     （全部走代理）；非法 mode 返回 400，与非法 format 的处理一致。
// 安全要点：
//   - 订阅令牌只存 SHA-256 哈希，绝不存明文；生成/轮换时明文只显示一次。
//     令牌泄露可一键轮换，旧链接立即失效；撤销后订阅立即 404。
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
	"fmt"
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
// Link 是完整的节点链接。
type subLink struct {
	Name string `json:"name"`
	Link string `json:"link"`
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
			out = append(out, subLink{Name: name, Link: l.Link})
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
// base64=标准 base64 编码的列表，clash=Clash YAML（proxies + 策略组 +
// 分流规则）。mode 只影响 clash（split 分流 / global 全局），
// base64/plain 原样不动；format 不合法返回错误。
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
	default:
		return "", "", fmt.Errorf("format 只支持 clash/base64/plain")
	}
}

// yamlDQ 把字符串包成 YAML 双引号标量（strconv.Quote 的转义在 YAML 双引号
// 里同样合法，中文原样保留）。
func yamlDQ(s string) string {
	return strconv.Quote(s)
}

// clashProxyFromLink 把一条节点链接转成一个 Clash proxy 条目（YAML 行块）。
// 支持 vless / trojan / vmess（都是 WS+TLS 的节点形态）；ss 暂不支持，
// 在 clash 里跳过（base64/plain 订阅里还在，TUTORIAL 里写明了）。
func clashProxyFromLink(l subLink) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(l.Link))
	if err != nil || u.Scheme == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
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

// ---------- Clash 策略组与分流规则（v1.2.9，v1.3.1 补 GLOBAL 组，v1.3.4 改顺序） ----------
// Clash YAML 订阅出 7 个策略组：自动选择（url-test，全部节点，排第一，
// 每 5 分钟测一次挑延迟最低）、GLOBAL（select，全部节点，排第二，
// 客户端切全局模式时走它）、手动选择（select，全部节点，默认入口）、
// 中转节点（select，只有中转链接）、直连节点（select，只有直连链接）、
// 社交（select，全部节点）、流媒体（select，全部节点）。中转/直连按 twin
// 名字后缀 -中转/-直连 分组；没有中转或直连链接时对应组不出现（不出空组）。
// 规则顺序：TikTok → 社交 → 流媒体 → MATCH；TikTok 规则指向手动选择，
// 社交指向社交组，流媒体指向流媒体组。分流模式 MATCH,DIRECT（只有这三
// 类走节点，其他直连），全局模式 MATCH,手动选择（全部走代理）。

// tiktokSuffixes TikTok 分流域名：主站、App 接口/视频流/日志上报、
// 视频 CDN、老域名与字节海外基础设施。
var tiktokSuffixes = []string{
	"tiktok.com", "tiktokv.com", "tiktokcdn.com", "tiktokcdn-us.com",
	"musical.ly", "byteoversea.com", "ibytedtos.com",
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

// renderClashYAML 渲染 Clash 订阅：proxies 列表 + 7 个策略组 + 分流规则。
// 名字去重（Clash 要求 proxy 名唯一），不支持的协议跳过；mode 是
// split（分流，MATCH,DIRECT）或 global（全局，MATCH,手动选择）。
// GLOBAL 组只在客户端切全局模式时生效，规则模式下只看 rules，不走它。
func renderClashYAML(links []subLink, mode string) string {
	var b strings.Builder
	b.WriteString("proxies:\n")
	used := map[string]int{}
	var names []string
	var kinds []string // 与 names 一一对应：relay / direct / ""（无后缀）
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
		b.WriteString(proxy)
	}
	if len(names) == 0 {
		b.WriteString("  []\n")
	}
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
	// 分流规则：TikTok → 社交 → 流媒体 → MATCH（先命中先走）。
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
	writeSuffixRules(tiktokSuffixes, "手动选择")
	b.WriteString("  - DOMAIN-KEYWORD,tiktok,手动选择\n")
	writeSuffixRules(socialSuffixes, "社交")
	writeSuffixRules(streamSuffixes, "流媒体")
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

// resolveSubFormat 定订阅返回格式：?format=clash|base64|plain 手动指定优先；
// 不指定时按 UA 自动识别（Clash 系 → YAML，其余 → base64）。
func resolveSubFormat(r *http.Request) (string, error) {
	f := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch f {
	case "clash", "yaml":
		return "clash", nil
	case "base64":
		return "base64", nil
	case "plain":
		return "plain", nil
	case "":
		if wantsClashByUA(r.UserAgent()) {
			return "clash", nil
		}
		return "base64", nil
	default:
		return "", fmt.Errorf("format 只支持 clash/base64/plain")
	}
}

// ---------- subscription-userinfo ----------

// subUserinfoHeader 按 subscription-userinfo 规范拼响应头，只用现成真实数据：
// upload/download 取流量统计的累计值（本面板这些落地没数据就不含这两项），
// total 取各落地流量上限之和（都没设限就不含），expire 取各落地到期日里
// 最早的（都没设就不含）。三项都没有就不返回头，不编数据。
// localIndices 为 nil 表示本面板全部落地；合并订阅只统计其中本面板的部分
// （远程面板的流量数据主面板拿不到，不含）。
func (s *Server) subUserinfoHeader(localIndices map[int]bool) string {
	rc, _, err := s.readRelayConf()
	if err != nil {
		return ""
	}
	var up, down, total int64
	var hasTraffic, hasLimit bool
	var earliest time.Time
	s.stateMu.Lock()
	exts := map[string]*LandingExt{}
	for k, v := range s.state.Landings {
		exts[k] = v
	}
	s.stateMu.Unlock()
	for _, l := range rc.Landings {
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
func (s *Server) handleSub(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
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
	if tok != "" && s.cfg.SubTokenHash != "" && verifySubToken(tok, s.cfg.SubTokenHash) {
		links = s.collectSubLinks(nil)
	} else if u, ok := s.findSubUser(tok); ok {
		// 一人一条（v1.3.0）：没勾选 = 单面板全量订阅，有勾选 = 合并订阅
		if len(u.Selection) == 0 {
			links = s.collectSubLinks(nil)
		} else {
			links, localIndices = s.collectMergedSubLinks(&MergedSub{Selection: u.Selection})
		}
	} else if ms, ok := s.findMergedSub(tok); ok {
		links, localIndices = s.collectMergedSubLinks(ms)
	} else {
		http.NotFound(w, r)
		return
	}
	body, ctype, err := renderSubContent(links, format, mode)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if h := s.subUserinfoHeader(localIndices); h != "" {
		w.Header().Set("subscription-userinfo", h)
	}
	w.Header().Set("Content-Type", ctype)
	// 下载文件名（v1.3.4）：让客户端按文件名给导入的配置起名。
	w.Header().Set("Content-Disposition", subContentDisposition(s.panelName(), format))
	_, _ = w.Write([]byte(body))
}

// ---------- 订阅下载文件名（v1.3.4） ----------
// 订阅响应带 Content-Disposition 文件名，让客户端按文件名给导入的配置
// 起名：Clash YAML 用“<面板名称>.yaml”，base64/plain 用“<面板名称>.txt”。
// 单面板订阅用本面板名，合并订阅用主面板（本地面板）的名——合并订阅
// 本来就是主面板生成与下发的，所以两边都用本面板的 panelName()。
// 面板名称可能含中文：filename* 用 RFC 5987 的 UTF-8 百分号编码；
// filename 放一份 ASCII 兜底（名字纯 ASCII 时用原名，否则用 panel）。
func subContentDisposition(panelName, format string) string {
	ext := "txt"
	if format == "clash" {
		ext = "yaml"
	}
	name := subFileName(panelName)
	encoded := rfc5987Encode(name + "." + ext)
	if isASCIISafe(name) {
		return `attachment; filename="` + name + "." + ext + `"; filename*=UTF-8''` + encoded
	}
	return `attachment; filename="panel.` + ext + `"; filename*=UTF-8''` + encoded
}

// subFileName 文件名清洗（第二道防线：面板名称保存时已校验过）：
// 去掉换行与文件名非法字符，空了就回退 CP管理，最多 64 个字。
func subFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "CP管理"
	}
	var b []rune
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\\:*?"<>|`, r) {
			b = append(b, '-')
		} else {
			b = append(b, r)
		}
	}
	name = strings.Trim(string(b), " .")
	if utf8.RuneCountInString(name) > 64 {
		name = string([]rune(name)[:64])
	}
	if name == "" {
		return "CP管理"
	}
	return name
}

// rfc5987Encode 按 RFC 5987 把字符串编成 filename* 用的 UTF-8 百分号编码：
// attr-char（字母数字与 ! # $ & + - . ^ _ ` | ~）原样保留，其余字节 %XX 大写。
func rfc5987Encode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// isASCIISafe 名字是否只含 ASCII 安全字符（可直接放进 filename="…"）。
func isASCIISafe(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x80 || c == '"' || c == '\\' || c < 0x20 {
			return false
		}
	}
	return s != ""
}

// ---------- 单面板订阅令牌管理（会话认证） ----------

// subURL 拼订阅链接：填了订阅对外域名（v1.3.2，Worker 域名或自定义域名）
// 就用 https://域名/sub/…，不再用面板自己的地址；没填时按当前请求的协议
// 与 Host 拼（面板怎么打开，订阅就用什么地址）。
func (s *Server) subURL(r *http.Request, tok string) string {
	if d := strings.TrimSpace(s.cfg.SubPublicDomain); d != "" {
		return "https://" + d + "/sub/" + tok
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/sub/" + tok
}

func (s *Server) handleSubTokenGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, okResp(map[string]any{
		"has_token":  s.cfg.SubTokenHash != "",
		"created_at": s.cfg.SubTokenCreatedAt,
	}))
}

// handleSubTokenGenerate 生成（或轮换）单面板订阅令牌：订阅链接只在这一次返回，
// 之后再也看不到；已有令牌时旧链接立即失效。记审计日志（不记令牌本身）。
func (s *Server) handleSubTokenGenerate(w http.ResponseWriter, r *http.Request) {
	tok := generateSubToken()
	rotated := s.cfg.SubTokenHash != ""
	s.cfg.SubTokenHash = hashSubToken(tok)
	s.cfg.SubTokenCreatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	action := "生成订阅链接"
	if rotated {
		action = "轮换订阅链接"
	}
	s.audit(s.currentUser(r), action, "", "成功", "旧订阅链接已失效")
	writeJSON(w, 200, okResp(map[string]any{"url": s.subURL(r, tok), "rotated": rotated}))
}

func (s *Server) handleSubTokenRevoke(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SubTokenHash == "" {
		writeJSON(w, 400, errResp("本来就没有订阅链接，不用撤销"))
		return
	}
	s.cfg.SubTokenHash = ""
	s.cfg.SubTokenCreatedAt = ""
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "撤销订阅链接", "", "成功", "订阅链接已失效")
	writeJSON(w, 200, okResp(nil))
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
		detail = "订阅链接改用 https://" + domain + "/sub/…"
	}
	s.audit(s.currentUser(r), "设置订阅对外域名", domain, "成功", detail)
	writeJSON(w, 200, okResp(map[string]any{"domain": domain}))
}

// ---------- 订阅用户：一人一条订阅链接（v1.3.0，会话认证） ----------
// 一个面板一个通用令牌时，所有人拉同一条链接，封一条就全军覆没；
// 每人一个独立令牌，封一条只影响一个人。令牌只存哈希，明文只在
// 生成/轮换时显示一次；旧的面板级通用令牌保留不动，已发出去的链接继续能用。

// handleSubUsersList 列出订阅用户（不含令牌与链接，链接只在生成/轮换时显示一次）。
func (s *Server) handleSubUsersList(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Note      string `json:"note"`
		CreatedAt string `json:"created_at"`
		Kind      string `json:"kind"` // single=单面板全量，merged=合并订阅
		Summary   string `json:"summary"`
	}
	items := []item{}
	for _, u := range s.cfg.SubUsers {
		kind := "single"
		summary := "单面板全量订阅（全部落地）"
		if len(u.Selection) > 0 {
			kind = "merged"
			summary = s.selectionSummary(u.Selection)
		}
		items = append(items, item{ID: u.ID, Name: u.Name, Note: u.Note, CreatedAt: u.CreatedAt, Kind: kind, Summary: summary})
	}
	writeJSON(w, 200, okResp(map[string]any{"users": items}))
}

// handleSubUsersCreate 新建订阅用户：名字必填（用来认人）；selection 为空
// 就是单面板全量订阅，填了就是合并订阅（走同一套勾选校验，远程版本不够
// 时同样提示先升级）。链接只在这一次返回，之后再也看不到。
func (s *Server) handleSubUsersCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string         `json:"name"`
		Note      string         `json:"note"`
		Selection []SubSelection `json:"selection"`
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
	tok := generateSubToken()
	now := time.Now().UTC().Format(time.RFC3339)
	id := "su-" + randomTokenHex(6)
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: id, Name: name, Note: note, TokenHash: hashSubToken(tok), CreatedAt: now, Selection: sel})
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "新建订阅用户", name, "成功", "订阅链接只显示一次，请立即复制保存")
	writeJSON(w, 200, okResp(map[string]any{"id": id, "url": s.subURL(r, tok), "name": name}))
}

// handleSubUsersRotate 轮换一个订阅用户的链接：旧链接立即失效，只影响这个人。
func (s *Server) handleSubUsersRotate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].ID != id {
			continue
		}
		tok := generateSubToken()
		s.cfg.SubUsers[i].TokenHash = hashSubToken(tok)
		s.cfg.SubUsers[i].CreatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.saveConfig(); err != nil {
			writeJSON(w, 500, errResp("保存失败："+err.Error()))
			return
		}
		s.audit(s.currentUser(r), "轮换订阅用户链接", s.cfg.SubUsers[i].Name, "成功", "旧链接已失效")
		writeJSON(w, 200, okResp(map[string]any{"id": id, "url": s.subURL(r, tok), "name": s.cfg.SubUsers[i].Name}))
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
			s.cfg.SubUsers = append(s.cfg.SubUsers[:i], s.cfg.SubUsers[i+1:]...)
			if err := s.saveConfig(); err != nil {
				writeJSON(w, 500, errResp("保存失败："+err.Error()))
				return
			}
			s.audit(s.currentUser(r), "撤销订阅用户", name, "成功", "该用户的订阅链接已失效，其他人照常")
			writeJSON(w, 200, okResp(nil))
			return
		}
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
func renderWorkerCode(origin, publicDomain string) string {
	example := "https://<Worker 域名>/sub/<令牌>?format=clash&mode=split"
	if publicDomain != "" {
		example = "https://" + publicDomain + "/sub/<令牌>?format=clash&mode=split"
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
    if (!url.pathname.startsWith("/sub/")) {
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
func (s *Server) handleWorkerCode(w http.ResponseWriter, r *http.Request) {
	origin, err := workerOrigin(r)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	resp := map[string]any{"origin": origin, "code": renderWorkerCode(origin, s.cfg.SubPublicDomain)}
	if workerOriginIsIP(origin) {
		resp["warning"] = "源站地址是 IP，Worker 回源会被 Cloudflare 挡（error 1003）：先在 Cloudflare 里给面板建个域名（灰云 A 记录指到面板 IP），再填 http://域名:11111 重新生成代码。"
	}
	writeJSON(w, 200, okResp(resp))
}

// handleSubQR 订阅链接二维码（v1.2.9，会话认证）：
// POST /api/v1/subscription/qr，请求 {"url": "<订阅链接>"}，
// 返回 {"qr": "data:image/png;base64,..."}。
// 复用节点链接同一套 qrDataURI（Go 原生 qrcode，Medium 纠错，256px，
// 不新引库）。只接受本面板的订阅链接（须含 /sub/sub_），不接受任意文本；
// 订阅链接约 150–160 字，长度上限放宽到 2048 字符。
// 注意：订阅令牌明文只在生成时显示一次，服务端存的只是哈希、拼不出
// 订阅链接，所以二维码由前端把当前显示的链接传进来生成；换 mode/format
// 后前端重新调一次，二维码跟着变。
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
	if u == "" || len(u) > 2048 || !strings.Contains(u, "/sub/sub_") {
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

// handleMergedSubsList 列出已生成的合并订阅（不含令牌与链接，链接只在生成时显示一次）。
func (s *Server) handleMergedSubsList(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		CreatedAt string         `json:"created_at"`
		Selection []SubSelection `json:"selection"`
		Summary   string         `json:"summary"`
	}
	items := []item{}
	for _, ms := range s.cfg.MergedSubs {
		if ms.Selection == nil {
			ms.Selection = []SubSelection{}
		}
		items = append(items, item{ID: ms.ID, Name: ms.Name, CreatedAt: ms.CreatedAt, Selection: ms.Selection, Summary: s.selectionSummary(ms.Selection)})
	}
	writeJSON(w, 200, okResp(map[string]any{"subs": items}))
}

// handleMergedSubsCreate 生成合并订阅：不填名称时同一选择重新生成 = 轮换
// （旧链接立即失效）；填了名称（归属到人）就新起一条——同一勾选集合可有
// 多个不同名称的令牌，各归各人，撤销只影响那一个。链接只在这一次返回，之后再也看不到。
func (s *Server) handleMergedSubsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Selection []SubSelection `json:"selection"`
		Name      string         `json:"name"`
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
	tok := generateSubToken()
	now := time.Now().UTC().Format(time.RFC3339)
	rotated := false
	id := ""
	if name != "" {
		id = "ms-" + randomTokenHex(6)
		s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: id, Name: name, TokenHash: hashSubToken(tok), CreatedAt: now, Selection: sel})
	} else {
		for i := range s.cfg.MergedSubs {
			// 只轮换同样没填名称的记录：已归属到人的那条不能被误轮换
			if s.cfg.MergedSubs[i].Name != "" {
				continue
			}
			if _, k := canonicalSelection(s.cfg.MergedSubs[i].Selection); k == key {
				s.cfg.MergedSubs[i].TokenHash = hashSubToken(tok)
				s.cfg.MergedSubs[i].CreatedAt = now
				id = s.cfg.MergedSubs[i].ID
				rotated = true
				break
			}
		}
		if id == "" {
			id = "ms-" + randomTokenHex(6)
			s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: id, TokenHash: hashSubToken(tok), CreatedAt: now, Selection: sel})
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
	target := s.selectionSummary(sel)
	if name != "" {
		target = name + "（" + target + "）"
	}
	s.audit(s.currentUser(r), action, target, "成功", "订阅链接只显示一次，请立即复制保存")
	writeJSON(w, 200, okResp(map[string]any{"id": id, "url": s.subURL(r, tok), "rotated": rotated, "selection": sel, "summary": s.selectionSummary(sel)}))
}

// handleMergedSubsRotate 轮换一条合并订阅：同一勾选集合与同一归属人换一个
// 新令牌，旧链接立即失效；新链接只在这一次返回（弹窗里直接显示）。
func (s *Server) handleMergedSubsRotate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for i := range s.cfg.MergedSubs {
		if s.cfg.MergedSubs[i].ID != id {
			continue
		}
		tok := generateSubToken()
		s.cfg.MergedSubs[i].TokenHash = hashSubToken(tok)
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
		writeJSON(w, 200, okResp(map[string]any{"id": id, "url": s.subURL(r, tok), "rotated": true, "selection": sel, "summary": summary}))
		return
	}
	writeJSON(w, 404, errResp("没有这条合并订阅"))
}

// handleMergedSubsDelete 撤销一条合并订阅（该订阅链接立即失效）。
func (s *Server) handleMergedSubsDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for i := range s.cfg.MergedSubs {
		if s.cfg.MergedSubs[i].ID == id {
			summary := s.selectionSummary(s.cfg.MergedSubs[i].Selection)
			s.cfg.MergedSubs = append(s.cfg.MergedSubs[:i], s.cfg.MergedSubs[i+1:]...)
			if err := s.saveConfig(); err != nil {
				writeJSON(w, 500, errResp("保存失败："+err.Error()))
				return
			}
			s.audit(s.currentUser(r), "撤销合并订阅", summary, "成功", "该订阅链接已失效")
			writeJSON(w, 200, okResp(nil))
			return
		}
	}
	writeJSON(w, 404, errResp("没有这条合并订阅"))
}

// ---------- 合并订阅的内容来源 ----------

// fetchRemoteSubLinks 从一台远程面板取指定落地的节点链接（走 central 令牌通道）：
// 远程面板连不上或版本太旧时返回空（合并订阅里这台暂时跳过，下次拉取自动恢复）。
func (s *Server) fetchRemoteSubLinks(rem RemotePanel, indices []int) []subLink {
	var parts []string
	for _, n := range indices {
		parts = append(parts, strconv.Itoa(n))
	}
	_, body, err := s.remoteCall(rem, "GET", "/api/v1/central/subscription?landings="+strings.Join(parts, ","), nil, 15*time.Second)
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
// 返回链接与其中本面板落地的编号集合（给 subscription-userinfo 用）。
func (s *Server) collectMergedSubLinks(ms *MergedSub) ([]subLink, map[int]bool) {
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
		out = append(out, s.fetchRemoteSubLinks(*rem, indices)...)
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
	writeJSON(w, 200, okResp(map[string]any{"links": links}))
}
