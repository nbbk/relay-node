package main

// ---------- 轻量节点后端（v1.4.1，切片 2）：入站模板 ----------
//
// 入站模板页的后端：入站 CRUD、一键生成默认三件套、443 端口主人校验、
// REALITY 密钥生成与轮换。入站存在节点的 Inbounds 里，随 nodes.json
// 落盘；任何改动都把节点 ConfigVersion＋1，agent 下次拉配置（60 秒内）
// 就能拿到新模板。
//
// 443 端口主人规则（§5.2.1，硬规则）：
//   - TCP 443 同一时间只能有一个启用的入站占着（REALITY 直出时是
//     reality-443；Caddy 在前时面板不管 Caddy，只校验 Xray 入站之间
//     不撞；xhttp-443 默认走 TCP 8443，不占 443）。
//   - UDP 443 同理：HY2 与 TUIC 不能同时占，面板校验时二选一。
// 校验不过时保存直接报错，写明冲突的两个入站 tag，不让用户存错配置。
//
// REALITY 密钥：X25519 密钥对，base64 RawURLEncoding（Xray 的格式）；
// 私钥只发给节点（配置拉取里带），绝不进管理接口的返回与订阅。

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// realityPlaceholders REALITY 的 SNI 与 dest 占位值（用户 2026-11-23
// 定：代码与文档里只用占位，真实大站部署时再填）。
const (
	realityDefaultSNI  = "www.example.com"
	realityDefaultDest = "www.example.com:443"
)

// defaultInboundPorts 备用协议的模板默认端口（v1.4.10，切片 6；
// 实施方案 §5.2 定值）：SS／SS2022 默认 8388，SOCKS5 默认 1080。
// 只是新建入站的模板默认值，可改；端口本身仍是入站字段，不走配置。
const (
	defaultShadowsocksPort = 8388
	defaultSocksPort       = 1080
)

// ss2022KeyBytes SS2022 方法要的密钥字节数（agent 0.1.7 实测口径）：
// Xray 要求密码是 base64 编码的定长密钥，长度不对时应用配置报 bad key
// 拒掉（fail-closed）。2022-blake3-aes-128-gcm 要 16 字节，其余
// 2022 方法要 32 字节；不在表里的 2022- 开头方法返回 false。
func ss2022KeyBytes(method string) (int, bool) {
	switch method {
	case "2022-blake3-aes-128-gcm":
		return 16, true
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		return 32, true
	default:
		return 0, false
	}
}

// generateSS2022Key 生成 SS2022 的 base64 密钥：n 字节随机数，标准
// base64 编码（带 = 补齐，与 Xray 要求的格式一致）。
// rand.Read 失败是极小概率事件，失败时返回空串、调用方校验会 400
// 挡住，不会把非法密钥存进去。
func generateSS2022Key(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// validateSS2022Password SS2022 密码格式校验（agent 0.1.7 实测口径）：
// 密码必须是 base64 编码的定长密钥；面板在保存时就拦住，不等节点上
// 应用配置失败才发现。其它 2022- 开头的方法只要求 base64 能解出
// 16 或 32 字节；非 2022 方法不校验。
func validateSS2022Password(method, password string) string {
	if !strings.HasPrefix(method, "2022-") {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(password))
	if err != nil {
		return "SS2022 密码须是 base64 编码的密钥（不是普通密码）"
	}
	if want, known := ss2022KeyBytes(method); known {
		if len(raw) != want {
			return fmt.Sprintf("SS2022 密码长度不对：%s 要 %d 字节密钥的 base64，实际解出 %d 字节", method, want, len(raw))
		}
		return ""
	}
	if len(raw) != 16 && len(raw) != 32 {
		return "SS2022 密码须是 16 或 32 字节密钥的 base64"
	}
	return ""
}

// generateRealityKeypair 生成 REALITY X25519 密钥对，返回
// (privateKey, publicKey)，都是 base64 RawURLEncoding。
func generateRealityKeypair() (string, string, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	privB64 := base64.RawURLEncoding.EncodeToString(priv.Bytes())
	pubB64 := base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	return privB64, pubB64, nil
}

// newRealityShortID 生成 REALITY short id：8 位十六进制。
func newRealityShortID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("生成 REALITY short id 失败：" + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// syncRealitySNIFields REALITY/XHTTP 入站的 SNI 同步（v1.4.2，修 SNI
// 编辑不同步）：SNI 是唯一事实来源，RealityDest 与 RealityServerName
// 都从 SNI 派生（dest = SNI:443，serverNames = SNI 去端口后的主机名）。
// 只对 transport 为 reality/xhttp 的入站做；TLS 入站的 SNI 有独立语义，
// 不动。新建与编辑都调：旧数据（SNI 改了、dest 还停在旧值）下次编辑即
// 自愈。agent 渲染 dest/serverNames 用的是这两个字段，不用 SNI，
// 不同步就会出现“列表显示新 SNI、节点实际跑的还是旧 dest”的断服。
func syncRealitySNIFields(in *NodeInbound) {
	if in.Transport != "reality" && in.Transport != "xhttp" {
		return
	}
	sni := strings.TrimSpace(in.SNI)
	if sni == "" {
		return
	}
	host := sni
	if h, _, err := net.SplitHostPort(sni); err == nil && h != "" {
		host = h
	}
	in.RealityServerName = host
	if strings.Contains(sni, ":") {
		in.RealityDest = sni
	} else {
		in.RealityDest = sni + ":443"
	}
}

// ---------- 入站校验 ----------

// inboundTransportIsTCP 该入站是不是走 TCP（占 TCP 端口）。
func inboundTransportIsTCP(in NodeInbound) bool {
	switch in.Transport {
	case "tcp", "reality", "xhttp", "tls":
		return true
	default:
		return false
	}
}

// validateInbound 校验一条入站的字段（不含端口主人冲突，那个要整
// 个列表一起看）。返回第一条不对的原因，空表示通过。
// realityFingerprintPresets REALITY 指纹预设（v1.4.4，切片 4）：
// 用户亲口定的 UI 预设（chrome、firefox、safari、ios、android、edge、
// qq、random），前端下拉框列全部预设＋“自定义”出文本框；后端只拦
// 非法字符，不拦预设外的值（手填自定义指纹要能存）。
var realityFingerprintPresets = []string{"chrome", "firefox", "safari", "ios", "android", "edge", "qq", "random"}

// sniPresets SNI 预设（v1.4.2 起的五个常用大站，用户亲口定的 UI 预设）。
var sniPresets = []string{"www.microsoft.com", "www.apple.com", "www.amazon.com", "www.cloudflare.com", "www.bing.com"}

// validateRealityFingerprint 指纹只允许字母数字、下划线、连字符
// （uTLS 指纹名就这几种字符；空格与其它符号直接 400）。
func validateRealityFingerprint(fp string) string {
	for _, r := range fp {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return "REALITY 指纹只能用字母、数字、下划线、连字符"
	}
	if len(fp) > 64 {
		return "REALITY 指纹太长，最多 64 个字"
	}
	return ""
}

func validateInbound(in NodeInbound) string {
	switch in.Protocol {
	case "vless":
		switch {
		case in.Transport == "reality" && in.Security == "reality":
		case in.Transport == "xhttp" && in.Security == "reality":
		case in.Transport == "tcp" && in.Security == "tls":
		case in.Transport == "tcp" && in.Security == "none":
		default:
			return "VLESS 的传输只支持 REALITY、XHTTP、TLS、TCP 明文四种"
		}
		if (in.Transport == "reality" || in.Transport == "xhttp") && strings.TrimSpace(in.SNI) == "" {
			return "REALITY 入站要填 SNI 目标"
		}
		if in.Transport == "tcp" && in.Security == "tls" && strings.TrimSpace(in.SNI) == "" {
			return "TLS 入站要填 SNI 目标（真实域名）"
		}
	case "trojan":
		if in.Transport != "tcp" || in.Security != "tls" {
			return "Trojan 只支持 TCP＋TLS"
		}
		if strings.TrimSpace(in.SNI) == "" {
			return "Trojan 需要真实域名"
		}
	case "shadowsocks":
		if in.Transport != "tcp" || in.Security != "none" {
			return "Shadowsocks 只支持 TCP 明文"
		}
		if strings.TrimSpace(in.SSMethod) == "" || strings.TrimSpace(in.SSPassword) == "" {
			return "Shadowsocks 要填加密方法与密码"
		}
		if msg := validateSS2022Password(in.SSMethod, in.SSPassword); msg != "" {
			return msg
		}
	case "hysteria2", "tuic":
		if in.Transport != "quic" {
			return in.Protocol + " 只支持 QUIC 传输"
		}
		if strings.TrimSpace(in.SNI) == "" {
			return in.Protocol + " 需要真实域名"
		}
	case "anytls":
		if in.Transport != "tcp" || in.Security != "tls" {
			return "AnyTLS 只支持 TCP＋TLS"
		}
		if strings.TrimSpace(in.SNI) == "" {
			return "AnyTLS 需要真实域名"
		}
	case "socks":
		if in.Transport != "tcp" || in.Security != "none" {
			return "SOCKS5 只支持 TCP 明文"
		}
	default:
		return "不支持的协议：" + in.Protocol
	}
	if in.Port < 1 || in.Port > 65535 {
		return "端口要在 1–65535 之间"
	}
	if l := strings.TrimSpace(in.Listen); l != "" && net.ParseIP(l) == nil {
		return "监听地址要是合法 IP（如 0.0.0.0）"
	}
	if strings.TrimSpace(in.InboundTag) == "" {
		return "入站 tag 不能为空"
	}
	if fp := strings.TrimSpace(in.RealityFingerprint); fp != "" {
		if msg := validateRealityFingerprint(fp); msg != "" {
			return msg
		}
	}
	return ""
}

// validateInboundPortOwners 校验端口主人：TCP 443 与 UDP 443 各只能
// 有一个启用的入站占着。返回冲突描述，空表示通过。只看启用的入站，
// 停用的不占端口。
func validateInboundPortOwners(inbounds []NodeInbound) string {
	var tcp443, udp443 *NodeInbound
	for i := range inbounds {
		in := &inbounds[i]
		if !in.Enabled || in.Port != 443 {
			continue
		}
		if inboundTransportIsTCP(*in) {
			if tcp443 != nil {
				return fmt.Sprintf("TCP 443 端口冲突：%s 与 %s 都要占 TCP 443，同一时间只能有一个入站占着", tcp443.InboundTag, in.InboundTag)
			}
			tcp443 = in
			continue
		}
		if in.Transport == "quic" {
			if udp443 != nil {
				return fmt.Sprintf("UDP 443 端口冲突：%s 与 %s 都要占 UDP 443，HY2 与 TUIC 同一时间只能开一个", udp443.InboundTag, in.InboundTag)
			}
			udp443 = in
		}
	}
	return ""
}

// validateInboundList 整表校验：逐条字段校验＋tag 不重复＋端口主人。
func validateInboundList(inbounds []NodeInbound) string {
	seen := map[string]bool{}
	for _, in := range inbounds {
		if seen[in.InboundTag] {
			return "入站 tag 重复：" + in.InboundTag
		}
		seen[in.InboundTag] = true
		if msg := validateInbound(in); msg != "" {
			return in.InboundTag + "：" + msg
		}
	}
	return validateInboundPortOwners(inbounds)
}

// ---------- 视图：私钥绝不进管理接口 ----------

// sanitizedInbound 入站的管理接口视图：私钥去掉，其余字段原样。
func sanitizedInbound(in NodeInbound) NodeInbound {
	in.RealityPrivateKey = ""
	return in
}

func sanitizedInbounds(inbounds []NodeInbound) []NodeInbound {
	out := make([]NodeInbound, 0, len(inbounds))
	for _, in := range inbounds {
		out = append(out, sanitizedInbound(in))
	}
	return out
}

// bumpNodeConfigVersionLocked 节点配置版本＋1（调用方须持有 n.mu；
// 改完调用方负责 saveLocked 落盘）。
func (n *nodeBackendStore) bumpNodeConfigVersionLocked(node *BackendNode) {
	node.ConfigVersion++
}

// nextInboundIDLocked 取下一个入站 id（调用方须持有 n.mu）。
func (n *nodeBackendStore) nextInboundIDLocked() int64 {
	if n.data.NextInboundID < 1 {
		n.data.NextInboundID = 1
	}
	id := n.data.NextInboundID
	n.data.NextInboundID++
	return id
}

// ---------- 管理接口（会话认证） ----------

// nodeIDFromPath 从 /api/v1/nodes/{id}/... 取节点 id。
func nodeIDFromPath(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// nodeInboundListItem 入站列表里的一条：入站本身＋单条链接可行性
// （v1.4.22：linkable／link_reason 由 backendInboundLinkable 算，
// 前端据此置灰「复制链接」「二维码」按钮并写明原因；不进 config，
// 只在列表响应里现算）。
type nodeInboundListItem struct {
	NodeInbound
	Linkable   bool   `json:"linkable"`
	LinkReason string `json:"link_reason,omitempty"`
}

// handleNodeInboundsList GET /api/v1/nodes/{id}/inbounds：入站列表
// （私钥不返回；每条附 linkable／link_reason，见 nodeInboundListItem）。
func (s *Server) handleNodeInboundsList(w http.ResponseWriter, r *http.Request) {
	id, ok := nodeIDFromPath(r)
	if !ok {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	s.nodes.mu.Lock()
	node := s.nodes.getNode(id)
	if node == nil {
		s.nodes.mu.Unlock()
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	inbounds := sanitizedInbounds(node.Inbounds)
	s.nodes.mu.Unlock()
	items := make([]nodeInboundListItem, 0, len(inbounds))
	for _, in := range inbounds {
		ok, reason := backendInboundLinkable(in)
		items = append(items, nodeInboundListItem{NodeInbound: in, Linkable: ok, LinkReason: reason})
	}
	writeJSON(w, 200, okResp(map[string]any{"node_id": id, "inbounds": items}))
}

// handleNodeInboundLink GET /api/v1/nodes/{id}/inbounds/{inbound_id}/link?user_id=...
// （v1.4.22）：单条入站链接＋二维码。user_id 是用户键（subuser:su-…／
// merged:ms-…，节点用户列表返回的 user_id 原样传）。
// 校验：节点存在、入站存在、用户存在且未停用、用户能访问该节点
// （节点侧 UserIDs＋用户侧四级指派，含入站级）、UUID 制协议要求该
// 用户在该节点有 UUID（v1.4.23：SS／SOCKS5 不要求 UUID，没 UUID
// 也能出链接）、入站能出单条链接（backendInboundLink，与订阅同一
// 套拼法）。
// 只读：不生成 UUID、不写 config、不 bump 版本（UUID 缺失时报错，
// 让调用方先拉一次节点用户列表——那条接口会补生成 UUID）。
// 链接与订阅里同一条入站的链接逐字一致；二维码用 qrDataURI（与订阅
// 同一套 go-qrcode，Medium 纠错、256px）；二维码生成失败时仍返回链接，
// qr 为空，前端显示“二维码生成失败，仅显示链接”。
func (s *Server) handleNodeInboundLink(w http.ResponseWriter, r *http.Request) {
	id, ok := nodeIDFromPath(r)
	if !ok {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	inboundID, err := strconv.ParseInt(r.PathValue("inbound_id"), 10, 64)
	if err != nil || inboundID <= 0 {
		writeJSON(w, 400, errResp("入站 id 不对"))
		return
	}
	userKey := strings.TrimSpace(r.URL.Query().Get("user_id"))
	kind, userID, found := strings.Cut(userKey, ":")
	if !found || (kind != "subuser" && kind != "merged" && kind != "indmerged") || strings.TrimSpace(userID) == "" {
		writeJSON(w, 400, errResp("请先选一个用户"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	node := s.nodes.getNode(id)
	if node == nil {
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	var in *NodeInbound
	for i := range node.Inbounds {
		if node.Inbounds[i].InboundID == inboundID {
			in = &node.Inbounds[i]
			break
		}
	}
	if in == nil {
		writeJSON(w, 404, errResp("入站不存在"))
		return
	}
	// 用户：存在、未停用、能访问该节点（含入站级指派）。
	var uuids map[string]string
	var scope string
	var targets []string
	var groupID string
	var userName string
	var disabled bool
	switch kind {
	case "subuser":
		u := findSubUserByID(s.cfg.SubUsers, userID)
		if u == nil {
			writeJSON(w, 404, errResp("用户不存在"))
			return
		}
		uuids, scope, targets, groupID, userName, disabled = u.NodeUUIDs, u.BackendScope, u.BackendTargets, u.GroupID, u.Name, u.Disabled
	case "merged":
		m := findMergedSubByID(s.cfg.MergedSubs, userID)
		if m == nil {
			writeJSON(w, 404, errResp("用户不存在"))
			return
		}
		uuids, scope, targets, groupID, userName, disabled = m.NodeUUIDs, m.BackendScope, m.BackendTargets, m.GroupID, m.Name, m.Disabled
	case "indmerged":
		ims := findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, userID)
		if ims == nil {
			writeJSON(w, 404, errResp("用户不存在"))
			return
		}
		uuids, scope, targets, userName, disabled = ims.NodeUUIDs, ims.BackendScope, ims.BackendTargets, ims.Name, ims.Disabled
	}
	if disabled {
		writeJSON(w, 400, errResp("该用户已停用"))
		return
	}
	if !nodeHasUser(node, userKey) || !s.backendAssignAllows(scope, targets, groupID, node) {
		writeJSON(w, 403, errResp("该用户不能访问这个节点"))
		return
	}
	if allowed := backendAssignInbounds(scope, targets, node); allowed != nil {
		found := false
		for _, tag := range allowed {
			if tag == in.InboundTag {
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, 403, errResp("该用户没有被指派这个入站"))
			return
		}
	}
	uuid := ""
	if uuids != nil {
		uuid = uuids[node.Address]
	}
	// UUID 门控在协议级（v1.4.23）：只有 UUID 制协议要求 UUID，
	// SS／SOCKS5 不要求，没 UUID 也能出链接。
	if backendInboundNeedsUUID(*in) && uuid == "" {
		writeJSON(w, 400, errResp("该用户在该节点还没有 UUID：先打开节点用户列表同步一次，再回来复制链接"))
		return
	}
	l, ok := backendInboundLink(*node, *in, uuid)
	if !ok {
		_, reason := backendInboundLinkable(*in)
		if reason == "" {
			reason = "该入站现在出不了单条链接"
		}
		writeJSON(w, 400, errResp(reason))
		return
	}
	qr, _ := qrDataURI(l.Link)
	writeJSON(w, 200, okResp(map[string]any{
		"node_id": id, "inbound_id": inboundID, "inbound_tag": in.InboundTag,
		"user_id": userKey, "username": userName,
		"label": l.Name, "link": l.Link, "qr": qr,
	}))
}

// inboundRequest 入站的新增／修改请求体（§4.3 字段；私钥不接受，
// 轮换走单独的接口）。
type inboundRequest struct {
	InboundTag         string  `json:"inbound_tag"`
	Protocol           string  `json:"protocol"`
	Transport          string  `json:"transport"`
	Security           string  `json:"security"`
	Port               int     `json:"port"`
	Listen             string  `json:"listen"`
	SNI                string  `json:"sni"`
	Domain             string  `json:"domain"`
	RealityDest        string  `json:"reality_dest"`
	RealityServerName  string  `json:"reality_server_names"`
	RealityFingerprint string  `json:"reality_fingerprint"`
	CertRef            int64   `json:"cert_ref"`
	Flow               string  `json:"flow"`
	SSMethod           string  `json:"ss_method"`
	SSPassword         string  `json:"ss_password"`
	SocksUsername      string  `json:"socks_username"`
	SocksPassword      string  `json:"socks_password"`
	Multiplier         float64 `json:"multiplier"`
	Enabled            *bool   `json:"enabled"`
}

// applyInboundRequest 把请求体写进一条入站（新建时补默认值）。
func applyInboundRequest(in *NodeInbound, req inboundRequest, isNew bool) {
	if req.InboundTag != "" {
		in.InboundTag = strings.TrimSpace(req.InboundTag)
	}
	if req.Protocol != "" {
		in.Protocol = strings.TrimSpace(req.Protocol)
	}
	if req.Transport != "" {
		in.Transport = strings.TrimSpace(req.Transport)
	}
	if req.Security != "" {
		in.Security = strings.TrimSpace(req.Security)
	}
	if req.Port != 0 {
		in.Port = req.Port
	}
	if req.Listen != "" {
		in.Listen = strings.TrimSpace(req.Listen)
	}
	if req.SNI != "" {
		in.SNI = strings.TrimSpace(req.SNI)
	}
	if req.Domain != "" {
		in.Domain = strings.TrimSpace(req.Domain)
	}
	if req.RealityDest != "" {
		in.RealityDest = strings.TrimSpace(req.RealityDest)
	}
	if req.RealityServerName != "" {
		in.RealityServerName = strings.TrimSpace(req.RealityServerName)
	}
	if req.RealityFingerprint != "" {
		in.RealityFingerprint = strings.TrimSpace(req.RealityFingerprint)
	}
	if req.CertRef != 0 {
		in.CertRef = req.CertRef
	}
	if req.Flow != "" {
		in.Flow = strings.TrimSpace(req.Flow)
	}
	if req.SSMethod != "" {
		in.SSMethod = strings.TrimSpace(req.SSMethod)
	}
	if req.SSPassword != "" {
		in.SSPassword = req.SSPassword
	}
	if req.SocksUsername != "" {
		in.SocksUsername = strings.TrimSpace(req.SocksUsername)
	}
	if req.SocksPassword != "" {
		in.SocksPassword = req.SocksPassword
	}
	if req.Multiplier != 0 {
		in.Multiplier = req.Multiplier
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
	}
	if isNew {
		if in.Multiplier == 0 {
			in.Multiplier = 1
		}
		if strings.TrimSpace(in.Listen) == "" {
			in.Listen = "0.0.0.0"
		}
		if in.RealityFingerprint == "" && (in.Transport == "reality" || in.Transport == "xhttp") {
			in.RealityFingerprint = "chrome"
		}
		// 切片 6：备用协议的新建默认值——端口没填时按协议补模板
		// 默认值（SS／SS2022 8388，SOCKS5 1080）；密码没填时自动
		// 生成（可改；只在新建时生成，编辑留空不改）。SS2022 的密码
		// 不是普通随机串，而是对应方法长度的 base64 密钥（agent 0.1.7
		// 实测口径：128-gcm 16 字节、256-gcm／chacha20 32 字节），
		// 否则 Xray 应用配置时报 bad key 拒掉；普通 SS 与 SOCKS5
		// 仍生成 16 位随机密码。
		if in.Port == 0 {
			switch in.Protocol {
			case "shadowsocks":
				in.Port = defaultShadowsocksPort
			case "socks":
				in.Port = defaultSocksPort
			}
		}
		if in.Protocol == "shadowsocks" && strings.TrimSpace(in.SSPassword) == "" {
			if n, ok := ss2022KeyBytes(in.SSMethod); ok {
				in.SSPassword = generateSS2022Key(n)
			} else if strings.HasPrefix(in.SSMethod, "2022-") {
				// 不在表里的 2022 方法：默认按 32 字节生成（校验对未知
				// 方法只要求 16 或 32 字节，不会把自动生成的挡住）。
				in.SSPassword = generateSS2022Key(32)
			} else {
				in.SSPassword = randomPassword(16)
			}
		}
		if in.Protocol == "socks" && strings.TrimSpace(in.SocksPassword) == "" {
			in.SocksPassword = randomPassword(16)
		}
	}
}

// handleNodeInboundsCreate POST /api/v1/nodes/{id}/inbounds：添加入站。
func (s *Server) handleNodeInboundsCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := nodeIDFromPath(r)
	if !ok {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	var req inboundRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	node := s.nodes.getNode(id)
	if node == nil {
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	in := NodeInbound{NodeID: id, Enabled: true}
	applyInboundRequest(&in, req, true)
	// REALITY 入站没带 SNI／dest 时填占位默认值（真实值部署时再改），
	// 再从 SNI 同步 dest 与 serverNames（SNI 是唯一事实来源，v1.4.2）。
	if (in.Transport == "reality" || in.Transport == "xhttp") && in.SNI == "" {
		in.SNI = realityDefaultSNI
	}
	if (in.Transport == "reality" || in.Transport == "xhttp") && in.RealityDest == "" {
		in.RealityDest = realityDefaultDest
	}
	syncRealitySNIFields(&in)
	// REALITY 入站新建时生成密钥对。
	if in.Transport == "reality" || in.Transport == "xhttp" {
		priv, pub, err := generateRealityKeypair()
		if err != nil {
			writeJSON(w, 500, errResp("生成 REALITY 密钥失败："+err.Error()))
			return
		}
		in.RealityPrivateKey = priv
		in.RealityPublicKey = pub
		in.RealityShortID = newRealityShortID()
	}
	in.InboundID = s.nodes.nextInboundIDLocked()
	candidate := append(append([]NodeInbound{}, node.Inbounds...), in)
	if msg := validateInboundList(candidate); msg != "" {
		writeJSON(w, 400, errResp(msg))
		return
	}
	node.Inbounds = candidate
	s.nodes.bumpNodeConfigVersionLocked(node)
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "添加入站", fmt.Sprintf("节点 %d 入站 %s", node.ID, in.InboundTag), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"inbound_id": in.InboundID, "inbound": sanitizedInbound(in)}))
}

// handleNodeInboundsUpdate PUT /api/v1/nodes/{id}/inbounds/{inbound_id}：
// 改入站（私钥保留，不可改；改端口／协议后节点 60 秒内应用）。
func (s *Server) handleNodeInboundsUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := nodeIDFromPath(r)
	if !ok {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	inboundID, err := strconv.ParseInt(r.PathValue("inbound_id"), 10, 64)
	if err != nil || inboundID <= 0 {
		writeJSON(w, 400, errResp("入站 id 不对"))
		return
	}
	var req inboundRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	node := s.nodes.getNode(id)
	if node == nil {
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	idx := -1
	for i := range node.Inbounds {
		if node.Inbounds[i].InboundID == inboundID {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeJSON(w, 404, errResp("入站不存在"))
		return
	}
	updated := node.Inbounds[idx]
	applyInboundRequest(&updated, req, false)
	// SNI 是唯一事实来源：REALITY/XHTTP 入站改 SNI 时 dest 与
	// serverNames 跟着同步（v1.4.2 修 SNI 编辑不同步；只改端口等
	// 其它字段时也同步一次，旧数据即自愈）。
	syncRealitySNIFields(&updated)
	candidate := append([]NodeInbound{}, node.Inbounds...)
	candidate[idx] = updated
	if msg := validateInboundList(candidate); msg != "" {
		writeJSON(w, 400, errResp(msg))
		return
	}
	node.Inbounds = candidate
	s.nodes.bumpNodeConfigVersionLocked(node)
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "修改入站", fmt.Sprintf("节点 %d 入站 %s", node.ID, updated.InboundTag), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"inbound": sanitizedInbound(updated)}))
}

// handleNodeInboundsDelete DELETE /api/v1/nodes/{id}/inbounds/{inbound_id}：
// 删除入站，节点 60 秒内下线该入站。
func (s *Server) handleNodeInboundsDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := nodeIDFromPath(r)
	if !ok {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	inboundID, err := strconv.ParseInt(r.PathValue("inbound_id"), 10, 64)
	if err != nil || inboundID <= 0 {
		writeJSON(w, 400, errResp("入站 id 不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	node := s.nodes.getNode(id)
	if node == nil {
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	idx := -1
	for i := range node.Inbounds {
		if node.Inbounds[i].InboundID == inboundID {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeJSON(w, 404, errResp("入站不存在"))
		return
	}
	tag := node.Inbounds[idx].InboundTag
	node.Inbounds = append(node.Inbounds[:idx], node.Inbounds[idx+1:]...)
	s.nodes.bumpNodeConfigVersionLocked(node)
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "删除入站", fmt.Sprintf("节点 %d 入站 %s", node.ID, tag), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"deleted_id": inboundID}))
}

// handleNodeInboundsDefaultTrio POST /api/v1/nodes/{id}/inbounds/default-trio：
// 一键生成默认三件套。请求 {domain}：HY2 要用的域名；没有域名时只
// 生成前两条并提示。已存在的 tag 跳过不重复建；三条都已存在时报错。
func (s *Server) handleNodeInboundsDefaultTrio(w http.ResponseWriter, r *http.Request) {
	id, ok := nodeIDFromPath(r)
	if !ok {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	var req struct {
		Domain string `json:"domain"`
	}
	if r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, 400, errResp("请求格式不对"))
			return
		}
	}
	domain := strings.TrimSpace(req.Domain)
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	node := s.nodes.getNode(id)
	if node == nil {
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	existing := map[string]bool{}
	for _, in := range node.Inbounds {
		existing[in.InboundTag] = true
	}
	var created []NodeInbound
	var note string
	// reality-443：VLESS＋REALITY＋Vision，TCP 443。
	if !existing["reality-443"] {
		priv, pub, err := generateRealityKeypair()
		if err != nil {
			writeJSON(w, 500, errResp("生成 REALITY 密钥失败："+err.Error()))
			return
		}
		created = append(created, NodeInbound{
			NodeID: node.ID, InboundID: s.nodes.nextInboundIDLocked(),
			InboundTag: "reality-443", Protocol: "vless", Transport: "reality",
			Security: "reality", Port: 443, Listen: "0.0.0.0", SNI: realityDefaultSNI,
			RealityPrivateKey: priv, RealityPublicKey: pub,
			RealityShortID: newRealityShortID(), RealityDest: realityDefaultDest,
			RealityServerName: realityDefaultSNI, RealityFingerprint: "chrome",
			Flow: "xtls-rprx-vision", Multiplier: 1, Enabled: true,
		})
	}
	// xhttp-443：VLESS＋REALITY＋XHTTP，TCP 8443（443 由 reality-443 独占）。
	if !existing["xhttp-443"] {
		priv, pub, err := generateRealityKeypair()
		if err != nil {
			writeJSON(w, 500, errResp("生成 REALITY 密钥失败："+err.Error()))
			return
		}
		created = append(created, NodeInbound{
			NodeID: node.ID, InboundID: s.nodes.nextInboundIDLocked(),
			InboundTag: "xhttp-443", Protocol: "vless", Transport: "xhttp",
			Security: "reality", Port: 8443, Listen: "0.0.0.0", SNI: realityDefaultSNI,
			RealityPrivateKey: priv, RealityPublicKey: pub,
			RealityShortID: newRealityShortID(), RealityDest: realityDefaultDest,
			RealityServerName: realityDefaultSNI, RealityFingerprint: "chrome",
			Multiplier: 1, Enabled: true,
		})
	}
	// hy2-443：Hysteria2，UDP 443；要真实域名，没有就不生成。
	if domain != "" && !existing["hy2-443"] {
		created = append(created, NodeInbound{
			NodeID: node.ID, InboundID: s.nodes.nextInboundIDLocked(),
			InboundTag: "hy2-443", Protocol: "hysteria2", Transport: "quic",
			Security: "tls", Port: 443, Listen: "0.0.0.0", SNI: domain, Domain: domain,
			Multiplier: 1, Enabled: true,
		})
	}
	if len(created) == 0 {
		writeJSON(w, 400, errResp("默认三件套已存在，不用重复生成"))
		return
	}
	candidate := append(append([]NodeInbound{}, node.Inbounds...), created...)
	if msg := validateInboundList(candidate); msg != "" {
		writeJSON(w, 400, errResp(msg))
		return
	}
	node.Inbounds = candidate
	s.nodes.bumpNodeConfigVersionLocked(node)
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	if domain == "" {
		note = "没有填域名，只生成了 REALITY＋XHTTP 两条；HY2 需要真实域名，填了域名再点一次就能补上"
	} else {
		note = "三条都生成了；HY2 需要面板签发的证书（切片 7）与 sing-box 监管（切片 5），现在还跑不起来，先跑 REALITY 两条"
	}
	s.audit(s.currentUser(r), "生成默认三件套", fmt.Sprintf("节点 %d 生成 %d 条入站", node.ID, len(created)), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"created": sanitizedInbounds(created), "note": note,
		"inbounds": sanitizedInbounds(node.Inbounds),
	}))
}

// handleNodeInboundRotateRealityKey POST /api/v1/nodes/{id}/inbounds/{inbound_id}/rotate-reality-key：
// 轮换 REALITY 密钥（只对 REALITY／XHTTP 入站有效）。轮换后旧订阅
// 链接失效，用户须重新导入。
func (s *Server) handleNodeInboundRotateRealityKey(w http.ResponseWriter, r *http.Request) {
	id, ok := nodeIDFromPath(r)
	if !ok {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	inboundID, err := strconv.ParseInt(r.PathValue("inbound_id"), 10, 64)
	if err != nil || inboundID <= 0 {
		writeJSON(w, 400, errResp("入站 id 不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	node := s.nodes.getNode(id)
	if node == nil {
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	idx := -1
	for i := range node.Inbounds {
		if node.Inbounds[i].InboundID == inboundID {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeJSON(w, 404, errResp("入站不存在"))
		return
	}
	in := &node.Inbounds[idx]
	if in.Transport != "reality" && in.Transport != "xhttp" {
		writeJSON(w, 400, errResp("只有 REALITY 入站才能轮换密钥"))
		return
	}
	priv, pub, err := generateRealityKeypair()
	if err != nil {
		writeJSON(w, 500, errResp("生成 REALITY 密钥失败："+err.Error()))
		return
	}
	in.RealityPrivateKey = priv
	in.RealityPublicKey = pub
	in.RealityShortID = newRealityShortID()
	s.nodes.bumpNodeConfigVersionLocked(node)
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "轮换 REALITY 密钥", fmt.Sprintf("节点 %d 入站 %s", node.ID, in.InboundTag), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"inbound": sanitizedInbound(*in),
		"note":    "密钥已换，旧订阅链接失效，用户须重新导入",
	}))
}

// ---------- 一键换端口（v1.4.2，切片 3） ----------

// randomFreeInboundPort 在 10000–60000 里随机抽一个空闲端口：避开
// 同节点其它已启用入站占的端口（TCP 与 UDP 端口是两个名字空间，这里
// 从严：两个都避开，换出来的端口一定不撞）；抽 100 次抽不到就报错，
// 调用方直接 500。特权端口（<1024）不抽，443 不在抽取范围里。
func randomFreeInboundPort(inbounds []NodeInbound, excludeID int64) (int, error) {
	used := map[int]bool{}
	for _, in := range inbounds {
		if in.InboundID == excludeID || !in.Enabled {
			continue
		}
		used[in.Port] = true
	}
	for i := 0; i < 100; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(50001))
		if err != nil {
			return 0, err
		}
		port := 10000 + int(n.Int64())
		if !used[port] {
			return port, nil
		}
	}
	return 0, fmt.Errorf("抽 100 次没抽到空闲端口")
}

// handleNodeInboundChangePort POST /api/v1/nodes/{id}/inbounds/{inbound_id}/change-port：
// 一键换端口。给该入站换一个随机空闲端口（避开已占用端口，换完走
// validateInboundList 全表校验——443 冲突等一样拦得住），bump 配置
// 版本号，节点 60 秒内应用；订阅按配置渲染，自动跟着更新，不用另做。
func (s *Server) handleNodeInboundChangePort(w http.ResponseWriter, r *http.Request) {
	id, ok := nodeIDFromPath(r)
	if !ok {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	inboundID, err := strconv.ParseInt(r.PathValue("inbound_id"), 10, 64)
	if err != nil || inboundID <= 0 {
		writeJSON(w, 400, errResp("入站 id 不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	node := s.nodes.getNode(id)
	if node == nil {
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	idx := -1
	for i := range node.Inbounds {
		if node.Inbounds[i].InboundID == inboundID {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeJSON(w, 404, errResp("入站不存在"))
		return
	}
	oldPort := node.Inbounds[idx].Port
	newPort, err := randomFreeInboundPort(node.Inbounds, inboundID)
	if err != nil {
		writeJSON(w, 500, errResp("换端口失败："+err.Error()))
		return
	}
	updated := node.Inbounds[idx]
	updated.Port = newPort
	candidate := append([]NodeInbound{}, node.Inbounds...)
	candidate[idx] = updated
	if msg := validateInboundList(candidate); msg != "" {
		writeJSON(w, 400, errResp(msg))
		return
	}
	node.Inbounds = candidate
	s.nodes.bumpNodeConfigVersionLocked(node)
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "入站换端口", fmt.Sprintf("节点 %d 入站 %s 端口 %d→%d", node.ID, updated.InboundTag, oldPort, newPort), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"inbound":  sanitizedInbound(updated),
		"old_port": oldPort, "new_port": newPort,
		"note": "端口已换，节点 60 秒内应用，订阅自动更新",
	}))
}
