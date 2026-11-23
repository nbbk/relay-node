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

// handleNodeInboundsList GET /api/v1/nodes/{id}/inbounds：入站列表
// （私钥不返回）。
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
	writeJSON(w, 200, okResp(map[string]any{"node_id": id, "inbounds": inbounds}))
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
	// REALITY 入站没带 SNI／dest 时填占位默认值（真实值部署时再改）。
	if (in.Transport == "reality" || in.Transport == "xhttp") && in.SNI == "" {
		in.SNI = realityDefaultSNI
	}
	if (in.Transport == "reality" || in.Transport == "xhttp") && in.RealityDest == "" {
		in.RealityDest = realityDefaultDest
	}
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
	if in.RealityServerName == "" && (in.Transport == "reality" || in.Transport == "xhttp") {
		in.RealityServerName = in.SNI
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
