package main

// ---------- 轻量节点后端（v1.4.0，切片 1） ----------
//
// 面板是唯一的事实来源；节点后端（agent）每 60 秒拉一次配置。
// 入网：面板生成一次性 join_ 令牌（只存 SHA-256 哈希，30 分钟过期，
// 用一次即作废）与面板签发的 mTLS 客户端证书（面板当 CA）；agent
// 拿令牌换证书，之后拉配置只认 mTLS 证书。
//
// 注意：GET /api/v1/node/config 这个路径之前已是 D10 旧 agent 接口
// （handleNodeConfig，rpt_ 令牌认证）。新后端按证书分流：带有效
// 节点证书的请求走新后端配置，否则走旧接口（行为不变）。
// 旧接口的认证与返回一个字都没动，只改了路由注册这一行。
//
// 存储：$PANEL_HOME/nodes.json（600，原子写），Node／NodeConfig／
// RelayRoute／Certificate 四张模型放同一文件；切片 1 入站、路由、
// 证书三张表恒为空，只有模型与存储先落位。

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// nodeBackendJoinTokenTTL 入网令牌有效期：30 分钟，过期未用须重新生成。
	nodeBackendJoinTokenTTL = 30 * time.Minute
	// nodeBackendOfflineAfter 连续这么久没拉配置，节点算离线。
	nodeBackendOfflineAfter = 3 * time.Minute
	// nodeBackendCertYears 面板签发的节点证书有效期（年）；轮换靠重新入网。
	nodeBackendCertYears = 10
)

// ---------- §4.6 Node／Relay 清单 ----------

// BackendNode 一个后端节点（§4.6 字段＋入网字段）。
type BackendNode struct {
	ID             int64         `json:"id"`
	Name           string        `json:"name"`
	Address        string        `json:"address"`
	AgentVersion   string        `json:"agent_version"`
	XrayVersion    string        `json:"xray_version"`
	SingBoxVersion string        `json:"singbox_version"`
	LastSeen       int64         `json:"last_seen"` // unix 秒，0＝从没拉过配置
	ConfigVersion  int64         `json:"config_version"`
	Inbounds       []NodeInbound `json:"inbounds,omitempty"` // §4.3，切片 1 恒为空
	// UserIDs 每节点用户指派（v1.4.2，切片 3）：存用户键
	// "subuser:su-…" / "merged:ms-…"（见 nodeBackendUsersForNode）。
	// 语义：nil = 全部未停用用户（切片 2 行为，已有节点不受影响）；
	// 非 nil = 只发这些用户；空数组 [] = 一个不发。
	// 注意：字段不能加 omitempty——nil 要落盘成 null、[] 要落盘成 []，
	// 两者在 nodes.json 里必须分得清，否则“全部”与“一个不发”会混掉。
	UserIDs []string `json:"user_ids"`
	// Multiplier 节点自身流量倍率（v1.4.4，切片 4）：后端节点上报的
	// 按人流量先乘它（再乘中转倍率、归一化、上限 100，见
	// effectiveBackendMultiplier）。0 = 没填，按 1 算；默认 1。
	Multiplier float64 `json:"multiplier,omitempty"`
	// InboundTraffic 各入站累计流量（v1.4.4）：tag → [上行, 下行]
	// （原始字节，不乘倍率；agent 每次上报累加，随节点存 nodes.json）。
	InboundTraffic map[string][2]int64 `json:"inbound_traffic,omitempty"`
	// LastTrafficAt 上次收到流量上报的时间（unix 秒，0＝从没上报过；
	// v1.4.4）。健康 staleness 按它算，不按 LastSeen（配置拉取）算。
	LastTrafficAt int64 `json:"last_traffic_at,omitempty"`
	// Health* 上次上报的健康（v1.4.4，随节点存）：HealthAt 是收到
	// 时间；XrayRunning=false 且 HealthAt!=0 = Xray 未运行。
	HealthXrayRunning   bool    `json:"health_xray_running,omitempty"`
	HealthXrayRestarts  int64   `json:"health_xray_restarts,omitempty"`
	HealthLastError     string  `json:"health_last_error,omitempty"`
	HealthCPUPercent    float64 `json:"health_cpu_percent,omitempty"`
	HealthMemMB         float64 `json:"health_mem_mb,omitempty"`
	HealthConfigVersion int64   `json:"health_config_version,omitempty"`
	HealthAt            int64   `json:"health_at,omitempty"`
	JoinTokenHash       string  `json:"join_token_hash,omitempty"`
	JoinTokenExpires    int64   `json:"join_token_expires_at"`
	Joined              bool    `json:"joined"`
	CreatedAt           int64   `json:"created_at"`
}

// nodeHealth 健康：online／offline／never（§4.6 口径）。
func nodeHealth(n *BackendNode) string {
	if n.LastSeen == 0 {
		return "never"
	}
	if time.Since(time.Unix(n.LastSeen, 0)) <= nodeBackendOfflineAfter {
		return "online"
	}
	return "offline"
}

// ---------- §4.3 NodeConfig／入站模板 ----------

// NodeInbound 一条入站（§4.3 字段全表；切片 1 只有模型与存储，
// 入站列表恒为空，渲染 Xray／sing-box 配置是切片 2 的事）。
type NodeInbound struct {
	NodeID     int64  `json:"node_id"`
	InboundID  int64  `json:"inbound_id"`
	InboundTag string `json:"inbound_tag"`
	Protocol   string `json:"protocol"`
	Transport  string `json:"transport"`
	Security   string `json:"security"`
	Port       int    `json:"port"`
	// Listen 监听地址（变量原则 2026-11-23：随部署变化的值走配置，不写
	// 死；空或 0.0.0.0 = 监听全部网卡，多 IP 机器可填指定 IP）。
	Listen             string  `json:"listen,omitempty"`
	SNI                string  `json:"sni"`
	Domain             string  `json:"domain"`
	RealityPrivateKey  string  `json:"reality_private_key,omitempty"`
	RealityPublicKey   string  `json:"reality_public_key,omitempty"`
	RealityShortID     string  `json:"reality_short_id,omitempty"`
	RealityDest        string  `json:"reality_dest,omitempty"`
	RealityServerName  string  `json:"reality_server_names,omitempty"`
	RealityFingerprint string  `json:"reality_fingerprint,omitempty"`
	CertRef            int64   `json:"cert_ref"`
	Flow               string  `json:"flow,omitempty"`
	SSMethod           string  `json:"ss_method,omitempty"`
	SSPassword         string  `json:"ss_password,omitempty"`
	SocksUsername      string  `json:"socks_username,omitempty"`
	SocksPassword      string  `json:"socks_password,omitempty"`
	Routes             string  `json:"routes,omitempty"`
	Limits             string  `json:"limits,omitempty"`
	Multiplier         float64 `json:"multiplier"`
	Enabled            bool    `json:"enabled"`
}

// ---------- §4.4 RelayRoute（中转路由） ----------

// BackendRelayRoute 一条中转路由（§4.4 字段全表；切片 1 只有模型
// 与存储，路由表恒为空，下发 realm 配置是切片 8 的事）。
type BackendRelayRoute struct {
	RelayID      int64  `json:"relay_id"`
	ListenAddr   string `json:"listen_addr"`
	RemoteNodeID int64  `json:"remote_node_id"`
	RemoteAddr   string `json:"remote_addr"`
	RemotePort   int    `json:"remote_port"`
	Proto        string `json:"proto"`
	UseUDP       bool   `json:"use_udp"`
	Transport    string `json:"transport"`
	Enabled      bool   `json:"enabled"`
}

// ---------- §4.5 Certificate（证书） ----------

// BackendCertificate 一张证书（§4.5 字段全表；切片 1 只有模型与
// 存储，证书表恒为空，签发与下发是切片 7 的事）。
type BackendCertificate struct {
	ID         int64  `json:"id"`
	Domain     string `json:"domain"`
	Mode       string `json:"mode"`
	Provider   string `json:"provider"`
	Expiry     int64  `json:"expiry"`
	CertPEMRef string `json:"cert_pem_ref"`
	KeyPEMRef  string `json:"key_pem_ref"`
	NodeID     int64  `json:"node_id"`
}

// ---------- 存储：$PANEL_HOME/nodes.json（600） ----------

type nodeBackendStoreData struct {
	NextID        int64                `json:"next_node_id"`
	NextInboundID int64                `json:"next_inbound_id"`
	Nodes         []*BackendNode       `json:"nodes"`
	DeletedIDs    []int64              `json:"deleted_ids"` // 删节点的墓碑：agent 再来拉时认得出“已删”，回空配置让它自停
	Routes        []BackendRelayRoute  `json:"routes"`
	Certs         []BackendCertificate `json:"certs"`
}

type nodeBackendStore struct {
	mu        sync.Mutex
	path      string
	panelHome string
	data      nodeBackendStoreData
	// 节点 CA（懒加载，第一次用时生成或读盘；只存内存，不落别的盘）。
	caCert *x509.Certificate
	caKey  crypto.Signer
	caPool *x509.CertPool
}

func newNodeBackendStore(panelHome string) *nodeBackendStore {
	return &nodeBackendStore{panelHome: panelHome, path: filepath.Join(panelHome, "nodes.json"), data: nodeBackendStoreData{NextID: 1}}
}

func (n *nodeBackendStore) load() {
	b, err := os.ReadFile(n.path)
	if err != nil {
		return
	}
	var d nodeBackendStoreData
	if err := json.Unmarshal(b, &d); err != nil {
		return
	}
	if d.NextID < 1 {
		d.NextID = 1
	}
	if d.NextInboundID < 1 {
		d.NextInboundID = 1
	}
	if d.Nodes == nil {
		d.Nodes = []*BackendNode{}
	}
	if d.DeletedIDs == nil {
		d.DeletedIDs = []int64{}
	}
	if d.Routes == nil {
		d.Routes = []BackendRelayRoute{}
	}
	if d.Certs == nil {
		d.Certs = []BackendCertificate{}
	}
	n.data = d
}

func (n *nodeBackendStore) saveLocked() error {
	if n.data.Nodes == nil {
		n.data.Nodes = []*BackendNode{}
	}
	if n.data.DeletedIDs == nil {
		n.data.DeletedIDs = []int64{}
	}
	if n.data.Routes == nil {
		n.data.Routes = []BackendRelayRoute{}
	}
	if n.data.Certs == nil {
		n.data.Certs = []BackendCertificate{}
	}
	b, err := json.MarshalIndent(n.data, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(n.path, b, 0o600)
}

// getNode 按 id 取节点（调用方须持有 n.mu；返回指针直接指内存，
// 调用方改完要调 saveLocked 落盘）。
func (n *nodeBackendStore) getNode(id int64) *BackendNode {
	for _, node := range n.data.Nodes {
		if node.ID == id {
			return node
		}
	}
	return nil
}

// isDeleted 该 id 是不是已删节点的墓碑。
func (n *nodeBackendStore) isDeleted(id int64) bool {
	for _, d := range n.data.DeletedIDs {
		if d == id {
			return true
		}
	}
	return false
}

// listNodes 节点列表（按 id 排序；返回拷贝，调用方随便改）。
func (n *nodeBackendStore) listNodes() []BackendNode {
	out := make([]BackendNode, 0, len(n.data.Nodes))
	for _, node := range n.data.Nodes {
		c := *node
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---------- 入网令牌 ----------

// newJoinToken 生成一次性入网令牌（join_＋32 位十六进制）。
func newJoinToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("生成入网令牌失败：" + err.Error())
	}
	return "join_" + hex.EncodeToString(b[:])
}

func hashJoinToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func verifyJoinToken(tok, hash string) bool {
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

// ---------- 节点 CA（面板当 CA） ----------

// loadNodeCA 只读盘加载节点 CA（不生成）：给配置拉取的证书校验用——
// 校验路径绝不能因为 CA 不存在就地生成一个新 CA（那样签发与校验
// 用的就不是同一份 CA，验签永远失败，还会在奇怪的目录落文件）。
// 调用方须持有 n.mu。
func (n *nodeBackendStore) loadNodeCA() error {
	if n.caCert != nil && n.caKey != nil && n.caPool != nil {
		return nil
	}
	certPath, keyPath := filepath.Join(n.panelHome, "certs", "node-ca.crt"), filepath.Join(n.panelHome, "certs", "node-ca.key")
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("节点 CA 证书解析失败")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("节点 CA 证书解析失败：%w", err)
	}
	kblock, _ := pem.Decode(keyPEM)
	if kblock == nil {
		return fmt.Errorf("节点 CA 私钥解析失败")
	}
	key, err := x509.ParseECPrivateKey(kblock.Bytes)
	if err != nil {
		return fmt.Errorf("节点 CA 私钥解析失败：%w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	n.caCert, n.caKey, n.caPool = cert, key, pool
	return nil
}

// ensureNodeCA 读盘或生成节点 CA（ECDSA P-256，10 年，CN 固定）。
// 只在第一次用时做，之后缓存在内存里。调用方须持有 n.mu。
func (n *nodeBackendStore) ensureNodeCA(panelHome string) error {
	if n.caCert != nil && n.caKey != nil && n.caPool != nil {
		return nil
	}
	if err := n.loadNodeCA(); err == nil {
		return nil
	}
	// 生成新 CA（私钥只写盘一次，之后只读）。
	dir := filepath.Join(panelHome, "certs")
	certPath, keyPath := filepath.Join(dir, "node-ca.crt"), filepath.Join(dir, "node-ca.key")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "relay-panel node CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Duration(nodeBackendCertYears) * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := writeFileAtomic(certPath, certPEM, 0o644); err != nil {
		return err
	}
	if err := writeFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	n.caCert, n.caKey, n.caPool = cert, key, pool
	return nil
}

// issueNodeClientCert 给节点签发 mTLS 客户端证书（ECDSA P-256，
// CN=node-<id>，ClientAuth 用途，10 年）。私钥只返回给 agent 一次，
// 面板不存它；轮换靠重新入网。
func (n *nodeBackendStore) issueNodeClientCert(panelHome string, nodeID int64) (certPEM, keyPEM []byte, err error) {
	if err := n.ensureNodeCA(panelHome); err != nil {
		return nil, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: fmt.Sprintf("node-%d", nodeID)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Duration(nodeBackendCertYears) * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, n.caCert, &key.PublicKey, n.caKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// verifyNodeClientCert 验请求带的节点证书：
// 请求须走 TLS 且带证书；叶子证书须由节点 CA 签发、用途 ClientAuth、
// CN=node-<id>。验不过返回 ok=false（调用方按旧接口继续走）。
// 注意：主监听只设 RequestClientCert（要证书但不强制），校验在这里
// 手工做——没证书的浏览器与旧 agent 不受影响。CA 不存在时验不过
// （fail closed），绝不在校验路径生成新 CA。
func (n *nodeBackendStore) verifyNodeClientCert(r *http.Request) (nodeID int64, ok bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return 0, false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.loadNodeCA(); err != nil {
		return 0, false
	}
	leaf := r.TLS.PeerCertificates[0]
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:       n.caPool,
		CurrentTime: time.Now(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return 0, false
	}
	name := strings.TrimSpace(leaf.Subject.CommonName)
	if !strings.HasPrefix(name, "node-") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(name, "node-"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// ---------- 配置文档与 ETag ----------

// NodeConfigUser 下发给 agent 的一条用户记录（§4.1 字段子集）：
// 订阅记录的计量子集。UserID 与面板内部的订阅标识一致
// （"subuser:su-…" / "merged:ms-…"）；VLESSUUID 是该用户在这个
// 节点上的 UUID（按节点地址从 NodeUUIDs 里取，缺失时现生成并写回
// config，只补不剪）；Email 是 Xray user>>> 统计钥匙。
type NodeConfigUser struct {
	UserID    string `json:"user_id"`
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	VLESSUUID string `json:"vless_uuid"`
	Disabled  bool   `json:"disabled"`
	// Inbounds 该用户被指派的入站 tag 列表（v1.4.4，切片 4，四级指派
	// 的入站级）：空 = 全部入站；老 agent 不认这个字段时直接忽略、即
	// 全入站，保持兼容；新 agent（0.1.4 起）按它渲染用户列表。
	Inbounds []string `json:"inbounds,omitempty"`
}

// nodeBackendConfigDoc 下发给 agent 的配置文档（§6.4 形状；
// 切片 2：入站与用户都下发，证书表恒为空（签发与下发是切片 7 的事）。
type nodeBackendConfigDoc struct {
	ConfigVersion int64            `json:"config_version"`
	Inbounds      []NodeInbound    `json:"inbounds"`
	Users         []NodeConfigUser `json:"users"`
	Certs         []any            `json:"certs"`
}

// configETag 配置文档的强 ETag：版本＋内容哈希。
func configETag(doc nodeBackendConfigDoc) string {
	b, _ := json.Marshal(doc)
	sum := sha256.Sum256(b)
	return `"` + fmt.Sprintf("v%d-%s", doc.ConfigVersion, hex.EncodeToString(sum[:])) + `"`
}

// etagMatches If-None-Match 里有没有这个 ETag（弱校验 W/ 前缀也认，
// 多值逐个比对；与 handleNodeUsers 同款写法）。
func etagMatches(inm, etag string) bool {
	for _, v := range strings.Split(inm, ",") {
		v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "W/"))
		if v == etag {
			return true
		}
	}
	return false
}

// configDocOf 该节点的配置文档（切片 2：入站与用户都下发）。
// users 由调用方从订阅用户表组装（见 nodeBackendUsersForNode）。
func (n *nodeBackendStore) configDocOf(node *BackendNode, users []NodeConfigUser) nodeBackendConfigDoc {
	inbounds := node.Inbounds
	if inbounds == nil {
		inbounds = []NodeInbound{}
	}
	if users == nil {
		users = []NodeConfigUser{}
	}
	return nodeBackendConfigDoc{
		ConfigVersion: node.ConfigVersion,
		Inbounds:      inbounds,
		Users:         users,
		Certs:         []any{},
	}
}

// nodeHasUser 该节点发不发这个用户（v1.4.2，切片 3）：
// UserIDs 为 nil = 全部用户；非 nil = 只看列表里有没有这个键。
func nodeHasUser(node *BackendNode, key string) bool {
	if node.UserIDs == nil {
		return true
	}
	for _, k := range node.UserIDs {
		if k == key {
			return true
		}
	}
	return false
}

// nodeBackendUsersForNode 该节点的用户列表（v1.4.2，切片 3：按
// BackendNode.UserIDs 指派过滤；nil = 全部未停用用户，保留切片 2
// 行为）。v1.4.4 再过一道用户侧四级指派门（backendAssignAllows，
// 默认全部），入站级指派写进每条记录的 Inbounds（空 = 全部入站）。
// UUID 按节点地址取 NodeUUIDs，缺失时现生成并写回 config
// （只补不剪，与 ensureNodeUUIDs 同款语义）。
// 调用方须持有 opMu（写 config 用）；返回 changed 表示 config 被改过，
// 调用方负责 saveConfig 落盘。
func (s *Server) nodeBackendUsersForNode(node *BackendNode) ([]NodeConfigUser, bool) {
	changed := false
	users := make([]NodeConfigUser, 0, len(s.cfg.SubUsers)+len(s.cfg.MergedSubs))
	for i := range s.cfg.SubUsers {
		u := &s.cfg.SubUsers[i]
		if u.Disabled {
			continue
		}
		if !nodeHasUser(node, userSubKey("subuser", u.ID)) {
			continue
		}
		// v1.4.4，四级指派：用户侧的第二道门（默认全部，不过滤）。
		if !s.backendAssignAllows(u.BackendScope, u.BackendTargets, u.GroupID, node) {
			continue
		}
		uuids, c := ensureNodeUUIDs(u.NodeUUIDs, []string{node.Address})
		if c {
			u.NodeUUIDs = uuids
			changed = true
		}
		users = append(users, NodeConfigUser{
			UserID: userSubKey("subuser", u.ID), Kind: "subuser", ID: u.ID,
			Name: u.Name, Email: nodeUserEmail("subuser", u.ID),
			VLESSUUID: uuids[node.Address],
			Inbounds:  backendAssignInbounds(u.BackendScope, u.BackendTargets, node),
		})
	}
	for i := range s.cfg.MergedSubs {
		m := &s.cfg.MergedSubs[i]
		if m.Disabled {
			continue
		}
		if !nodeHasUser(node, userSubKey("merged", m.ID)) {
			continue
		}
		// v1.4.4，四级指派：用户侧的第二道门（默认全部，不过滤）。
		if !s.backendAssignAllows(m.BackendScope, m.BackendTargets, m.GroupID, node) {
			continue
		}
		uuids, c := ensureNodeUUIDs(m.NodeUUIDs, []string{node.Address})
		if c {
			m.NodeUUIDs = uuids
			changed = true
		}
		users = append(users, NodeConfigUser{
			UserID: userSubKey("merged", m.ID), Kind: "merged", ID: m.ID,
			Name: m.Name, Email: nodeUserEmail("merged", m.ID),
			VLESSUUID: uuids[node.Address],
			Inbounds:  backendAssignInbounds(m.BackendScope, m.BackendTargets, node),
		})
	}
	return users, changed
}

// ---------- 管理接口（会话认证） ----------

// handleNodesCreate POST /api/v1/nodes：建节点（发入网令牌＋安装命令）。
// 入网令牌只在返回里出现一次，面板只存哈希；审计里不记令牌。
func (s *Server) handleNodesCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	name := strings.TrimSpace(req.Name)
	addr := strings.TrimSpace(req.Address)
	if name == "" || addr == "" {
		writeJSON(w, 400, errResp("名字与地址都要填"))
		return
	}
	if len(name) > 64 {
		writeJSON(w, 400, errResp("名字最多 64 个字"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	// 同名或同地址的节点不许重复建（地址是节点的身份，撞了分不清）。
	for _, node := range s.nodes.data.Nodes {
		if node.Name == name {
			writeJSON(w, 400, errResp("已有同名节点"))
			return
		}
		if node.Address == addr {
			writeJSON(w, 400, errResp("已有同地址节点"))
			return
		}
	}
	token := newJoinToken()
	node := &BackendNode{
		ID:            s.nodes.data.NextID,
		Name:          name,
		Address:       addr,
		ConfigVersion: 1,
		Inbounds:      []NodeInbound{},
		// UserIDs 显式空（§5.3：老用户不自动上新节点，须在“发到节点”
		// 里手动勾；nil = 全部用户只留给 1.4.2 之前建的老节点）。
		UserIDs:          []string{},
		JoinTokenHash:    hashJoinToken(token),
		JoinTokenExpires: time.Now().Add(nodeBackendJoinTokenTTL).Unix(),
		CreatedAt:        time.Now().Unix(),
	}
	s.nodes.data.NextID++
	s.nodes.data.Nodes = append(s.nodes.data.Nodes, node)
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "新建节点", fmt.Sprintf("节点 %d %s", node.ID, node.Name), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"node_id": node.ID, "join_token": token,
		"install_command": s.nodeInstallCommand(r, node.ID, token),
		"expires_at":      time.Unix(node.JoinTokenExpires, 0).UTC().Format(time.RFC3339),
	}))
}

// nodeInstallCommand 一键安装命令（面板地址取本次请求的 Host——
// 管理员正用这个地址看面板，agent 用同一地址连回来才通）。
// 带 --node-id：join 请求体要求 node_id，agent 侧是必填参数。
func (s *Server) nodeInstallCommand(r *http.Request, nodeID int64, token string) string {
	scheme := "http"
	if s.cfg.HTTPSEnabled {
		scheme = "https"
	}
	panelURL := scheme + "://" + r.Host
	return fmt.Sprintf("curl -fsSL https://raw.githubusercontent.com/nbbk/zhexuexi-node-backend/main/install-node-backend.sh | bash -s -- --panel %s --node-id %d --token %s", panelURL, nodeID, token)
}

// nodeRowForList 节点列表里的一行（§4.6 字段＋健康）。
// backend_health 是上报健康（v1.4.4）：按最后上报时间算 staleness，
// 与 health（配置拉取心跳）分开算、互不干扰。
func nodeRowForList(n BackendNode) map[string]any {
	lastSeen := ""
	if n.LastSeen != 0 {
		lastSeen = time.Unix(n.LastSeen, 0).UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"id": n.ID, "name": n.Name, "address": n.Address,
		"agent_version": n.AgentVersion, "xray_version": n.XrayVersion,
		"singbox_version": n.SingBoxVersion, "health": nodeHealth(&n),
		"backend_health": nodeBackendHealthMap(&n, time.Now()),
		"last_seen":      lastSeen, "config_version": n.ConfigVersion,
		"joined":     n.Joined,
		"created_at": time.Unix(n.CreatedAt, 0).UTC().Format(time.RFC3339),
		"user_count": 0,
	}
}

// handleNodesList GET /api/v1/nodes：节点列表。
func (s *Server) handleNodesList(w http.ResponseWriter, r *http.Request) {
	s.nodes.mu.Lock()
	rows := make([]map[string]any, 0, len(s.nodes.data.Nodes))
	for _, node := range s.nodes.listNodes() {
		rows = append(rows, nodeRowForList(node))
	}
	s.nodes.mu.Unlock()
	writeJSON(w, 200, okResp(map[string]any{"nodes": rows}))
}

// handleNodesGet GET /api/v1/nodes/{id}：节点详情。
func (s *Server) handleNodesGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
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
	row := nodeRowForList(*node)
	// 入站列表走管理接口视图：私钥不返回（切片 2）。
	inbounds := sanitizedInbounds(node.Inbounds)
	// v1.4.4：详情加流量块（本期按人合计＋各入站总量）与上报健康块。
	traffic := s.nodeBackendTrafficBlock(node)
	health := nodeBackendHealthMap(node, time.Now())
	s.nodes.mu.Unlock()
	writeJSON(w, 200, okResp(map[string]any{
		"node": row, "inbounds": inbounds, "users": []any{},
		"traffic": traffic, "backend_health": health,
	}))
}

// handleNodesDelete DELETE /api/v1/nodes/{id}：删除节点。
// 先查有没有 RelayRoute 指向它：有且没勾“一起删路由”时拒绝并列出
// 路由（切片 1 路由表恒为空，这段是给切片 8 的校验先落位）。
// 删节点只删节点记录与入网状态：流量历史（user_traffic.json）保留，
// 墓碑留下——agent 再来拉时认得出“已删”，回空配置让它自停。
func (s *Server) handleNodesDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	var req struct {
		DeleteRoutes bool `json:"delete_routes"`
	}
	if r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, 400, errResp("请求格式不对"))
			return
		}
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
	var orphans []BackendRelayRoute
	for _, rt := range s.nodes.data.Routes {
		if rt.RemoteNodeID == id {
			orphans = append(orphans, rt)
		}
	}
	if len(orphans) > 0 && !req.DeleteRoutes {
		writeJSON(w, 400, map[string]any{
			"ok": false, "error": "有中转路由指向该节点", "orphan_routes": orphans,
		})
		return
	}
	kept := s.nodes.data.Nodes[:0]
	for _, nd := range s.nodes.data.Nodes {
		if nd.ID != id {
			kept = append(kept, nd)
		}
	}
	s.nodes.data.Nodes = kept
	s.nodes.data.DeletedIDs = append(s.nodes.data.DeletedIDs, id)
	if req.DeleteRoutes {
		routes := s.nodes.data.Routes[:0]
		for _, rt := range s.nodes.data.Routes {
			if rt.RemoteNodeID != id {
				routes = append(routes, rt)
			}
		}
		s.nodes.data.Routes = routes
	}
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "删除节点", fmt.Sprintf("节点 %d %s", node.ID, node.Name), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"deleted_id": id, "deleted_routes": len(orphans)}))
}

// handleNodesRegenJoinToken POST /api/v1/nodes/{id}/join-token：
// 重新生成入网令牌（节点重装时用；旧令牌立即作废）。
func (s *Server) handleNodesRegenJoinToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, errResp("节点 id 不对"))
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
	token := newJoinToken()
	node.JoinTokenHash = hashJoinToken(token)
	node.JoinTokenExpires = time.Now().Add(nodeBackendJoinTokenTTL).Unix()
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "重新生成入网令牌", fmt.Sprintf("节点 %d %s", node.ID, node.Name), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"node_id": node.ID, "join_token": token,
		"install_command": s.nodeInstallCommand(r, node.ID, token),
		"expires_at":      time.Unix(node.JoinTokenExpires, 0).UTC().Format(time.RFC3339),
	}))
}

// ---------- agent 接口：入网换证 ----------

// handleNodeBackendJoin POST /api/v1/node-backend/join：
// agent 拿一次性入网令牌换长期身份（mTLS 客户端证书）。
// 不走会话认证——令牌本身就是鉴权；令牌验过即作废，只能用一次。
func (s *Server) handleNodeBackendJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID int64  `json:"node_id"`
		Token  string `json:"token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if req.NodeID <= 0 || strings.TrimSpace(req.Token) == "" {
		writeJSON(w, 400, errResp("node_id 与 token 都要填"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	node := s.nodes.getNode(req.NodeID)
	if node == nil {
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	if !verifyJoinToken(req.Token, node.JoinTokenHash) || time.Now().Unix() > node.JoinTokenExpires {
		writeJSON(w, 400, errResp("入网令牌不对或已过期，请在面板上重新生成"))
		return
	}
	certPEM, keyPEM, err := s.nodes.issueNodeClientCert(s.env.PanelHome, req.NodeID)
	if err != nil {
		writeJSON(w, 500, errResp("签发证书失败："+err.Error()))
		return
	}
	// 令牌一次有效：换完即作废；节点标为已入网。
	node.JoinTokenHash = ""
	node.JoinTokenExpires = 0
	node.Joined = true
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit("node-backend", "节点入网", fmt.Sprintf("节点 %d %s", node.ID, node.Name), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"node_id":         req.NodeID,
		"client_cert_pem": string(certPEM),
		"client_key_pem":  string(keyPEM),
	}))
}

// ---------- agent 接口：配置拉取（mTLS） ----------

// handleNodeConfigDispatch GET /api/v1/node/config：新后端按证书分流。
// 带有效节点证书 → 新后端配置；否则 → 旧 D10 接口（行为不变）。
func (s *Server) handleNodeConfigDispatch(w http.ResponseWriter, r *http.Request) {
	if id, ok := s.nodes.verifyNodeClientCert(r); ok {
		s.handleNodeBackendConfig(w, r, id)
		return
	}
	s.requireCentralToken(s.handleNodeConfig)(w, r)
}

// handleNodeBackendConfig 新后端配置下发（mTLS 已验过，nodeID 可信）。
// 200 回配置文档；If-None-Match 对得上回 304；已删节点回 deleted:true
// 的空配置，agent 收到后自停；证书有效但节点不存在回 404。
func (s *Server) handleNodeBackendConfig(w http.ResponseWriter, r *http.Request, nodeID int64) {
	// 先看节点在不在（只短暂持锁）。
	s.nodes.mu.Lock()
	node := s.nodes.getNode(nodeID)
	deleted := false
	if node == nil {
		deleted = s.nodes.isDeleted(nodeID)
	}
	s.nodes.mu.Unlock()
	if deleted {
		writeJSON(w, 200, okResp(map[string]any{
			"node_id": nodeID, "config_version": 0, "etag": `"deleted"`,
			"deleted": true, "inbounds": []any{}, "users": []any{}, "certs": []any{},
		}))
		return
	}
	if node == nil {
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	// 用户表：按节点地址取每人 UUID，缺失时现生成并写回 config。
	// 拿 opMu 写 config；占用时回 409，agent 下个周期再拉。
	if !s.tryOpLock(w) {
		return
	}
	users, changed := s.nodeBackendUsersForNode(node)
	if changed {
		_ = s.saveConfig()
	}
	s.opMu.Unlock()
	// 组装配置文档并更新心跳。
	s.nodes.mu.Lock()
	node = s.nodes.getNode(nodeID)
	if node == nil {
		// 两步之间节点被删了：按已删处理。
		s.nodes.mu.Unlock()
		writeJSON(w, 200, okResp(map[string]any{
			"node_id": nodeID, "config_version": 0, "etag": `"deleted"`,
			"deleted": true, "inbounds": []any{}, "users": []any{}, "certs": []any{},
		}))
		return
	}
	doc := s.nodes.configDocOf(node, users)
	etag := configETag(doc)
	// 拉配置即心跳：更新最后在线时间与 agent 版本（请求头带的）。
	node.LastSeen = time.Now().Unix()
	if v := strings.TrimSpace(r.Header.Get("X-Node-Backend-Version")); v != "" {
		node.AgentVersion = v
	}
	_ = s.nodes.saveLocked()
	s.nodes.mu.Unlock()
	w.Header().Set("ETag", etag)
	if inm := strings.TrimSpace(r.Header.Get("If-None-Match")); inm != "" && etagMatches(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, 200, okResp(map[string]any{
		"node_id": nodeID, "config_version": doc.ConfigVersion, "etag": etag,
		"deleted": false, "inbounds": doc.Inbounds, "users": doc.Users, "certs": doc.Certs,
	}))
}
