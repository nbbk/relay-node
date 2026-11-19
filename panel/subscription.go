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
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
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
// base64=标准 base64 编码的列表，clash=Clash YAML（proxies + PROXY 组）。
// 返回内容与 Content-Type；format 不合法返回错误。
func renderSubContent(links []subLink, format string) (string, string, error) {
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
		return renderClashYAML(links), "text/yaml; charset=utf-8", nil
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

// renderClashYAML 渲染 Clash 订阅：proxies 列表 + PROXY select 组 + 全走代理规则。
// 名字去重（Clash 要求 proxy 名唯一），不支持的协议跳过。
func renderClashYAML(links []subLink) string {
	var b strings.Builder
	b.WriteString("proxies:\n")
	used := map[string]int{}
	var names []string
	for _, l := range links {
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
		b.WriteString(proxy)
	}
	if len(names) == 0 {
		b.WriteString("  []\n")
	}
	b.WriteString("proxy-groups:\n")
	b.WriteString("  - name: \"PROXY\"\n")
	b.WriteString("    type: select\n")
	b.WriteString("    proxies:\n")
	for _, n := range names {
		b.WriteString("      - " + yamlDQ(n) + "\n")
	}
	b.WriteString("rules:\n")
	b.WriteString("  - MATCH,PROXY\n")
	return b.String()
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
	var links []subLink
	var localIndices map[int]bool
	if tok != "" && s.cfg.SubTokenHash != "" && verifySubToken(tok, s.cfg.SubTokenHash) {
		links = s.collectSubLinks(nil)
	} else if ms, ok := s.findMergedSub(tok); ok {
		links, localIndices = s.collectMergedSubLinks(ms)
	} else {
		http.NotFound(w, r)
		return
	}
	body, ctype, err := renderSubContent(links, format)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if h := s.subUserinfoHeader(localIndices); h != "" {
		w.Header().Set("subscription-userinfo", h)
	}
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write([]byte(body))
}

// ---------- 单面板订阅令牌管理（会话认证） ----------

// subURL 按当前请求的协议与 Host 拼订阅链接（面板怎么打开，订阅就用什么地址）。
func (s *Server) subURL(r *http.Request, tok string) string {
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
		CreatedAt string         `json:"created_at"`
		Selection []SubSelection `json:"selection"`
		Summary   string         `json:"summary"`
	}
	items := []item{}
	for _, ms := range s.cfg.MergedSubs {
		if ms.Selection == nil {
			ms.Selection = []SubSelection{}
		}
		items = append(items, item{ID: ms.ID, CreatedAt: ms.CreatedAt, Selection: ms.Selection, Summary: s.selectionSummary(ms.Selection)})
	}
	writeJSON(w, 200, okResp(map[string]any{"subs": items}))
}

// handleMergedSubsCreate 生成合并订阅：同一选择重新生成 = 轮换（旧链接立即失效）；
// 新选择 = 新起一条令牌。链接只在这一次返回，之后再也看不到。
func (s *Server) handleMergedSubsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Selection []SubSelection `json:"selection"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
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
	for i := range s.cfg.MergedSubs {
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
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	action := "生成合并订阅"
	if rotated {
		action = "轮换合并订阅"
	}
	s.audit(s.currentUser(r), action, s.selectionSummary(sel), "成功", "订阅链接只显示一次，请立即复制保存")
	writeJSON(w, 200, okResp(map[string]any{"id": id, "url": s.subURL(r, tok), "rotated": rotated, "selection": sel, "summary": s.selectionSummary(sel)}))
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
