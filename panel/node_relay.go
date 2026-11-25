package main

// ---------- 中转投影（v1.4.18，切片 8） ----------
//
// 面板改中转路由（BackendRelayRoute），中转机 realm 配置跟着变并生效。
// 渲染、下发、重启三步：
//
//  1. 渲染：renderBackendRelayRoutes 把启用的路由渲染成 realm
//     config.toml 里的 [[endpoints]] 段（每段前带 `# backend-route`
//     标记行，下次重建时只认这个标记删，不碰落地／按人转发的段）。
//  2. 下发：applyBackendRelayRoutes 复用现有的中转配置下发通道——
//     本机写 config.toml（writeConfigTomlAtomic）＋重启 realm
//     （restartRealm），与落地增删、按人转发（applyUserForwards）
//     同一条本地写路径；管理面板（ManagerOnly，本机无中转）不下发，
//     只存路由并在返回里写明原因。
//  3. 同机 agent 代收代写（方案 §10.2 第 3 步）：下发前先问本机
//     127.0.0.1:18081 的 agent（POST /relay-config，见 agent 0.1.9
//     的 relay.go）；agent 在就由它写文件＋重启 realm，agent 不在
//     （连接被拒）面板自己写；agent 明确拒绝时不回退直写（fail-closed，
//     免得绕过它的校验）。
//
// 读法说明（方案 §10.1 有两处与已落地的实现对不上，这里按已落地的
// 代码走，理由写在函数注释里）：
//   - use_udp 是 config.toml 顶层 [network] 的全局开关（v1.3.46 的
//     readUseUDP／setUseUDP／ensureUseUDPForConf），不是每段
//     [[endpoints]] 的字段；渲染出的段里不写 use_udp，任一 UDP 路由
//     存在时把全局开关翻成 true（只开不关，与 ensureUseUDP 同口径）。
//   - 同一 listen 的 TCP＋UDP 两条路由只写一段（v1.3.33 的教训：
//     写两段一模一样的，第二段绑定必定失败；不带 network 字段的段按
//     全局 use_udp 开关同时走 TCP／UDP）。方案 §10.1 那句“渲染成两段”
//     与这条已验证的教训冲突，这里按一段走。
//   - RelayID：模型里是 int64，但远程面板登记的 ID 是 "rm-…" 字符串，
//     没有 RelayID→远程面板的映射；v1 只支持本机中转（RelayID 省略
//     即 0＝本机），管理面板→远程中转机的下发不在本切片范围（见汇报
//     的拿不准问题）。路由的增删改只在本机 realm 上生效。
//
// 路由的键是（relay_id，listen_addr）：listen 不可改，改监听地址
// 请删了重建。删节点时孤儿路由的警告与“一起删”在 handleNodesDelete
// 里（切片 1 已落位）；“一起删”后这里顺手重下发一次，把段从
// config.toml 里撤掉，不然段会残留。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// backendRouteMarker 中转路由段的标记：重建时只删带这个标记的段。
const backendRouteMarker = "# backend-route"

// agentRelayConfigAddr 同机 agent 的本地控制面地址（agent 默认
// ControlAddr；agent 改了 control_addr 时面板找不到它，会回退直写）。
const agentRelayConfigAddr = "127.0.0.1:18081"

// ---------- 渲染 ----------

// routeIsUDP 这条路由是不是 UDP 路由：Proto==udp 或 UseUDP 开着都算
// （模型里两个字段都有，任一为真即 UDP；渲染时只影响全局开关）。
func routeIsUDP(r BackendRelayRoute) bool {
	return strings.EqualFold(strings.TrimSpace(r.Proto), "udp") || r.UseUDP
}

// renderBackendRelayRoutes 把启用的路由渲染成 config.toml 片段＋
// needUDP（任一启用的 UDP 路由存在即 true）。
// 只写启用的；按 listen 去重（先按 listen 排序，同 listen 只留第一条，
// 排序保证结果稳定）；段里不写 use_udp（见文件头说明）。
func renderBackendRelayRoutes(routes []BackendRelayRoute) (string, bool) {
	enabled := make([]BackendRelayRoute, 0, len(routes))
	for _, r := range routes {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	sort.Slice(enabled, func(i, j int) bool {
		if enabled[i].ListenAddr != enabled[j].ListenAddr {
			return enabled[i].ListenAddr < enabled[j].ListenAddr
		}
		return enabled[i].RemoteNodeID < enabled[j].RemoteNodeID
	})
	var add strings.Builder
	seen := map[string]bool{}
	// needUDP 先对全部启用路由算（去重掉的那条也算：同 listen 有 TCP
	// 又有 UDP 时，留下的那一段照样要走 UDP）。
	needUDP := false
	for _, r := range enabled {
		if routeIsUDP(r) {
			needUDP = true
			break
		}
	}
	for _, r := range enabled {
		if seen[r.ListenAddr] {
			continue
		}
		seen[r.ListenAddr] = true
		add.WriteString(fmt.Sprintf("\n%s relay=%d listen=%s\n[[endpoints]]\nlisten = %q\nremote = %q\n",
			backendRouteMarker, r.RelayID, r.ListenAddr, r.ListenAddr, r.RemoteAddr+":"+strconv.Itoa(r.RemotePort)))
	}
	return add.String(), needUDP
}

// removeBackendRouteSegments 从 config.toml 里删掉中转路由的段（只认
// # backend-route 标记；落地与按人转发的段不动）。分段口径与
// removeEndpointsFromToml 一致（按空行分段）。
func removeBackendRouteSegments(data []byte) []byte {
	parts := strings.Split(string(data), "\n\n")
	kept := make([]string, 0, len(parts))
	for _, seg := range parts {
		if strings.Contains(seg, backendRouteMarker) && strings.Contains(seg, "[[endpoints]]") {
			continue
		}
		kept = append(kept, seg)
	}
	return []byte(strings.Join(kept, "\n\n"))
}

// ---------- 下发 ----------

// relayApplySkipReason 本机下发不了的原因（空＝可以下发）。
func (s *Server) relayApplySkipReason() string {
	if !s.managesLocalRelay() {
		return "本机是管理面板，没有中转，路由只保存、没有下发到中转机"
	}
	if _, err := s.readConfigToml(); err != nil {
		return "本机没有 realm 配置（config.toml），路由只保存、没有下发"
	}
	return ""
}

// tryAgentRelayApply 同机 agent 代收代写：把渲染好的完整 config.toml
// 发给本机 agent（POST /relay-config），由它写文件＋重启 realm。
// 返回 (handled, err)：agent 不在（连接被拒）返回 (false, nil)，面板
// 自己写；agent 接了返回 (true, nil)；agent 明确拒绝返回 (true, err)，
// 不回退直写。
func tryAgentRelayApply(toml []byte) (bool, error) {
	body, _ := json.Marshal(map[string]any{"config_toml": string(toml)})
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post("http://"+agentRelayConfigAddr+"/relay-config", "application/json", bytes.NewReader(body))
	if err != nil {
		if strings.Contains(err.Error(), "connection refused") {
			return false, nil
		}
		return false, fmt.Errorf("联系本机 agent 下发中转配置失败：%v", err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	if resp.StatusCode != http.StatusOK {
		msg, _ := m["error"].(string)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return true, fmt.Errorf("agent 下发中转配置失败：%s", msg)
	}
	if ok, _ := m["ok"].(bool); !ok {
		msg, _ := m["error"].(string)
		if msg == "" {
			msg = "agent 未确认"
		}
		return true, fmt.Errorf("agent 下发中转配置失败：%s", msg)
	}
	return true, nil
}

// applyBackendRelayRoutesLocked 按当前路由表重建本机 config.toml 里
// 的中转路由段并重启 realm 生效。调用方须持有 s.nodes.mu（读路由表）
// 与 opMu。返回 (applied, err)：没变化／本机不下发时 applied=false，
// err=nil；写失败／重启失败时 err 非 nil（路由已保存，调用方把错误
// 透给用户，留“重新下发”重试）。
func (s *Server) applyBackendRelayRoutesLocked() (bool, error) {
	if !s.managesLocalRelay() {
		return false, nil
	}
	tomlData, err := s.readConfigToml()
	if err != nil {
		// 没装 realm（没 config.toml）时跳过，与 applyUserForwards 同款。
		return false, nil
	}
	rendered, needUDP := renderBackendRelayRoutes(s.nodes.data.Routes)
	newToml := removeBackendRouteSegments(tomlData)
	newToml = ensureTomlNewline(newToml)
	newToml = append(newToml, []byte(rendered)...)
	if nt, changed := ensureUseUDP(newToml, needUDP); changed {
		newToml = nt
	}
	// realm 无 endpoint 会 panic：一个不剩就留占位（与删落地／
	// applyUserForwards 同一路数）。
	if !strings.Contains(string(newToml), "[[endpoints]]") {
		newToml = append(newToml, []byte("\n[[endpoints]]\nlisten = \"127.0.0.1:9\"\nremote = \"127.0.0.1:9\"\n")...)
	}
	if string(newToml) == string(tomlData) {
		return false, nil
	}
	if _, err := s.backupNow("中转路由下发前自动备份", "realm"); err == nil {
		// 备份成功才继续（与 disableLanding 同款写法：失败也不拦）
	}
	// 同机 agent 先收：agent 在就由它写＋重启，面板不再自己写。
	if handled, err := tryAgentRelayApply(newToml); err != nil {
		return false, err
	} else if handled {
		return true, nil
	}
	if err := s.writeConfigTomlAtomic(newToml); err != nil {
		return false, err
	}
	if err := s.restartRealm(); err != nil {
		return false, err
	}
	return true, nil
}

// applyBackendRelayRoutes 对外入口：自己拿锁、快照路由表、调 Locked 版。
func (s *Server) applyBackendRelayRoutes() (bool, error) {
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	return s.applyBackendRelayRoutesLocked()
}

// ---------- 校验 ----------

// normalizeListenAddr 监听地址归一化：只填端口（如 30004）补成
// 0.0.0.0:30004；host 为空补 0.0.0.0；host 须是合法 IP；端口 1–65535。
func normalizeListenAddr(in string) (string, error) {
	v := strings.TrimSpace(in)
	if v == "" {
		return "", fmt.Errorf("监听地址不能为空")
	}
	if !strings.Contains(v, ":") {
		v = "0.0.0.0:" + v
	}
	host, portStr, err := net.SplitHostPort(v)
	if err != nil {
		return "", fmt.Errorf("监听地址不对，应是 IP:端口，如 0.0.0.0:30004")
	}
	if host == "" {
		host = "0.0.0.0"
	}
	if ip := net.ParseIP(host); ip == nil {
		return "", fmt.Errorf("监听地址里的 IP 不对：%s", host)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("监听端口须是 1–65535")
	}
	return host + ":" + strconv.Itoa(port), nil
}

// listenPortOf 取监听地址里的端口（归一化过的地址一定能取到）。
func listenPortOf(listenAddr string) int {
	_, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(portStr)
	return p
}

// normalizeRemoteAddr 落地地址归一化：空着调用方用节点地址补；
// 非空时不许含空白与冒号（端口另填），须是合法 IP 或域名。
func normalizeRemoteAddr(in string) (string, error) {
	v := strings.TrimSpace(in)
	if v == "" {
		return "", nil
	}
	if strings.ContainsAny(v, " \t\r\n/:") {
		return "", fmt.Errorf("落地地址不对：填 IP 或域名，不要带端口（端口另填）")
	}
	if ip := net.ParseIP(v); ip != nil {
		return v, nil
	}
	if !validDomainName(v) {
		return "", fmt.Errorf("落地地址不对：填合法的 IP 或域名，如 203.0.113.1 或 example.com")
	}
	return v, nil
}

// validDomainName 域名格式校验：点分标签，每段由字母数字与连字符组成、
// 不以连字符开头或结尾；单标签主机名（如内网名）也放行。IP 走 net.ParseIP，
// 不走这里。
func validDomainName(v string) bool {
	if len(v) == 0 || len(v) > 253 {
		return false
	}
	for _, label := range strings.Split(v, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for i, c := range label {
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-'
			if !ok {
				return false
			}
			if c == '-' && (i == 0 || i == len(label)-1) {
				return false
			}
		}
	}
	return true
}

// ---------- 管理接口（会话认证） ----------

// findRelayRoute 按键找路由（调用方须持有 nodes.mu；返回下标，-1＝没有）。
func (n *nodeBackendStore) findRelayRoute(relayID int64, listenAddr string) int {
	for i, r := range n.data.Routes {
		if r.RelayID == relayID && r.ListenAddr == listenAddr {
			return i
		}
	}
	return -1
}

// handleRelayRoutesList GET /api/v1/relay-routes：中转路由列表。
// local_relay 告诉前端本机有没有中转（管理面板上没有，下发按钮要
// 写明“只保存、未下发”）。管理面板上不列本地的死路由：列表代理到
// 选中的中转面板（query 里带 relay_panel_id，没带时只有一台就用它），
// 回包里补上 proxied／relay_panel_id／relay_panel_name；没登记中转
// 面板或远程连不上时回空列表＋relay_hint，前端显示提示、不弹错。
// 管理面板本地以前存过的死路由（C 之前建的、从未下发过）不再列出，
// 它们从未生效过，留在 nodes.json 里不动。
func (s *Server) handleRelayRoutesList(w http.ResponseWriter, r *http.Request) {
	if !s.managesLocalRelay() {
		s.proxyRelayRoutesList(w, r)
		return
	}
	s.nodes.mu.Lock()
	routes := append([]BackendRelayRoute{}, s.nodes.data.Routes...)
	s.nodes.mu.Unlock()
	if routes == nil {
		routes = []BackendRelayRoute{}
	}
	writeJSON(w, 200, okResp(map[string]any{
		"routes":      routes,
		"local_relay": s.managesLocalRelay(),
	}))
}

// relayRouteReq 路由增改请求的公共字段（enabled／use_udp 用指针：
// 没填＝创建时默认 true／改时保持原值）。
// RelayPanelID 只在管理面板上用：代理到哪台中转面板（远程面板 id），
// 远程收到后忽略它（路由存在中转面板本机，relay_id 仍是 0）。
type relayRouteReq struct {
	RelayID      int64  `json:"relay_id"`
	RelayPanelID string `json:"relay_panel_id"`
	ListenAddr   string `json:"listen_addr"`
	RemoteNodeID int64  `json:"remote_node_id"`
	RemoteAddr   string `json:"remote_addr"`
	RemotePort   int    `json:"remote_port"`
	Proto        string `json:"proto"`
	Transport    string `json:"transport"`
	Enabled      *bool  `json:"enabled"`
	UseUDP       *bool  `json:"use_udp"`
}

// relayRouteFormat 校验后归一化的路由格式字段（不含节点存在与撞端口
// 检查；管理面板代理前也用它先拦格式错，见 validateRelayRouteFormat）。
type relayRouteFormat struct {
	ListenAddr string
	RemoteAddr string // 可能为空：调用方再按节点补
	RemotePort int
	Proto      string
	Transport  string
	Enabled    bool
	UseUDP     bool
	RelayID    int64
}

// errRelayNoTarget 没选目标节点、落地地址又空着时的报错（C 方案：
// 只填远端 IP:端口建路由时，落地地址必须直接填）。
const errRelayNoTarget = "落地地址不能为空：没选目标节点时请直接填落地 IP 或域名"

// validateRelayRouteFormat 只校验路由的格式（监听地址、落地地址、
// 落地端口、协议、传输、启用），不查节点存在、不查撞端口；返回归一化
// 后的字段。管理面板代理前调它先拦格式错，远程收到后再走完整校验。
func validateRelayRouteFormat(req relayRouteReq) (relayRouteFormat, error) {
	listen, err := normalizeListenAddr(req.ListenAddr)
	if err != nil {
		return relayRouteFormat{}, err
	}
	remoteAddr, err := normalizeRemoteAddr(req.RemoteAddr)
	if err != nil {
		return relayRouteFormat{}, err
	}
	if req.RemotePort < 1 || req.RemotePort > 65535 {
		return relayRouteFormat{}, fmt.Errorf("落地端口须是 1–65535")
	}
	proto := strings.ToLower(strings.TrimSpace(req.Proto))
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		return relayRouteFormat{}, fmt.Errorf("协议只能是 tcp 或 udp")
	}
	transport := strings.ToLower(strings.TrimSpace(req.Transport))
	if transport == "" {
		transport = "tcp"
	}
	if transport != "tcp" && transport != "tls" && transport != "ws" {
		return relayRouteFormat{}, fmt.Errorf("传输只能是 tcp、tls 或 ws")
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	useUDP := proto == "udp"
	if req.UseUDP != nil {
		useUDP = *req.UseUDP
	}
	return relayRouteFormat{
		ListenAddr: listen, RemoteAddr: remoteAddr, RemotePort: req.RemotePort,
		Proto: proto, UseUDP: useUDP, Transport: transport, Enabled: enabled,
		RelayID: req.RelayID, // v1 只支持本机中转：前端不填即 0
	}, nil
}

// validateRelayRoute 校验并归一化一条路由（新建／修改共用；exclude
// 是修改时要排除的自己下标，-1＝新建）。node 与 routes 由调用方在
// 持锁时快照后传进来（纯函数，自己不加锁）；used 是已占用的监听端口
// 集合（落地监听、按人转发、面板端口）。
// 校验：监听地址合法、目标（节点记录或直接填的落地地址）、落地端口
// 1–65535、协议 tcp／udp、传输 tcp／tls／ws、同中转机监听端口不撞其
// 他路由与已占用的监听端口（落地监听、按人转发、面板端口）。
// 目标（C 方案）：remote_node_id 指到的节点存在就用它（落地地址空着
// 时补节点地址）；没选节点（remote_node_id 为 0）时落地地址必须直接
// 填，不再要求节点记录存在——登记那一步就此省掉。
func validateRelayRoute(req relayRouteReq, exclude int, node *BackendNode, routes []BackendRelayRoute, used map[int]bool) (BackendRelayRoute, error) {
	f, err := validateRelayRouteFormat(req)
	if err != nil {
		return BackendRelayRoute{}, err
	}
	remoteAddr := f.RemoteAddr
	if node == nil {
		if req.RemoteNodeID != 0 {
			return BackendRelayRoute{}, fmt.Errorf("目标节点不存在")
		}
		if remoteAddr == "" {
			return BackendRelayRoute{}, fmt.Errorf(errRelayNoTarget)
		}
	} else if remoteAddr == "" {
		remoteAddr = strings.TrimSpace(node.Address)
	}
	relayID := f.RelayID
	// 同中转机监听端口不撞其他路由（只比启用的；停用的不占端口）。
	for i, rt := range routes {
		if i == exclude || rt.RelayID != relayID || !rt.Enabled {
			continue
		}
		if rt.ListenAddr == f.ListenAddr {
			return BackendRelayRoute{}, fmt.Errorf("监听地址 %s 已有中转路由", f.ListenAddr)
		}
	}
	// 不撞已占用的监听端口（落地监听、按人转发、面板端口）。
	if f.Enabled {
		if used[listenPortOf(f.ListenAddr)] {
			return BackendRelayRoute{}, fmt.Errorf("监听端口 %d 已被占用（落地监听／按人转发／面板端口），换一个", listenPortOf(f.ListenAddr))
		}
	}
	return BackendRelayRoute{
		RelayID: relayID, ListenAddr: f.ListenAddr, RemoteNodeID: req.RemoteNodeID,
		RemoteAddr: remoteAddr, RemotePort: f.RemotePort,
		Proto: f.Proto, UseUDP: f.UseUDP, Transport: f.Transport, Enabled: f.Enabled,
	}, nil
}

// applyAfterRouteChange 路由改完后的下发：返回 (applied, apply_error)。
// 下发失败不回滚路由（路由已保存），调用方把错误透给用户、留
// “重新下发”重试。
func (s *Server) applyAfterRouteChange() (bool, string) {
	applied, err := s.applyBackendRelayRoutes()
	if err != nil {
		return false, err.Error()
	}
	if !applied {
		if reason := s.relayApplySkipReason(); reason != "" {
			return false, reason
		}
	}
	return applied, ""
}

// ---------- 管理面板代理（C 方案：放宽校验＋代理） ----------
//
// 管理面板（ManagerOnly，本机无中转）不再本地存路由：增删改／重下发
// 一律代理到选中的中转面板（远程面板）的令牌接口
// /api/v1/central/relay-routes*，中转面板在自己本机上走同一份
// handler 存路由、下发 realm。列表也代理，并在回包里补上代理信息
// （proxied／relay_panel_id／relay_panel_name），前端一次拿到、
// 不用再猜。路由的目标只填远端 IP:端口、不再要求节点记录存在
// （validateRelayRoute 的放宽口径），登记那一步就此省掉。
//
// 选哪台中转机：请求里带 relay_panel_id（写操作放 body、列表放
// query）；没带时已登记的远程面板只有一台就用它，多台须指明，
// 一台没有就回提示。代理走集中式代理同一套 remoteCall（令牌认证），
// 版本门是本版（远程低于本版时没有这个令牌接口，直接提示升级）。

// decodeRelayRouteBody 读路由请求体并严格解码（未知字段直接 400，
// 与 decodeJSON 同口径）；返回原请求体（代理时原样转发）与解出的请求。
func decodeRelayRouteBody(r *http.Request) ([]byte, relayRouteReq, error) {
	var req relayRouteReq
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, req, fmt.Errorf("请求读失败")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, req, fmt.Errorf("请求格式不对")
	}
	return body, req, nil
}

// resolveRelayProxyRemote 解析中转路由代理的目标远程面板。
// id 为空时，已登记的远程面板只有一台就用它；多台须指明；一台没有
// 就回提示。返回 (nil, hint) 时调用方按读写分别处理：写操作直接
// 400，读操作回空列表＋提示（不弹错）。
func (s *Server) resolveRelayProxyRemote(id string) (*RemotePanel, string) {
	id = strings.TrimSpace(id)
	if id != "" {
		if rem := s.remoteByID(id); rem != nil {
			return rem, ""
		}
		return nil, "没有登记过这台远程面板"
	}
	switch len(s.cfg.Remotes) {
	case 0:
		return nil, "还没登记中转面板：先在集中管理页登记一台远程面板，再回来建路由。"
	case 1:
		return &s.cfg.Remotes[0], ""
	default:
		return nil, "登记了多台远程面板，请先在上面选一台中转机。"
	}
}

// relayProxyPanelID 取本次代理的中转面板 id：写操作优先 body 里的
// relay_panel_id，没有就看 query 参数（DELETE 空 body 时用得上）。
func relayProxyPanelID(r *http.Request, req *relayRouteReq) string {
	if req != nil && strings.TrimSpace(req.RelayPanelID) != "" {
		return strings.TrimSpace(req.RelayPanelID)
	}
	return strings.TrimSpace(r.URL.Query().Get("relay_panel_id"))
}

// proxyRelayRouteWrite 把中转路由的写操作代理到一台远程面板：
// method/path 是远程的令牌接口；auditAction 记在管理面板审计日志里
// 的动作（沿用集中式代理先校验、再转发、再记审计的路数）。
func (s *Server) proxyRelayRouteWrite(w http.ResponseWriter, r *http.Request, rem *RemotePanel, method, path string, body []byte, auditAction, auditDetail string) {
	s.proxyRemoteWriteMinVer(w, r, *rem, method, path, body, 60*time.Second, Version)
	s.audit(s.currentUser(r), auditAction, auditDetail+"（经 "+rem.Name+"）", "已转发到远程面板", "")
}

// proxyRelayRoutesList 管理面板代理拉一台中转面板的路由列表，并在
// 回包里补上代理信息（proxied／relay_panel_id／relay_panel_name）。
// 远程版本低／连不上时回空列表＋提示（读操作不弹错，前端显示提示）。
func (s *Server) proxyRelayRoutesList(w http.ResponseWriter, r *http.Request) {
	rem, hint := s.resolveRelayProxyRemote(r.URL.Query().Get("relay_panel_id"))
	empty := func(hint string) {
		m := map[string]any{
			"routes": []BackendRelayRoute{}, "local_relay": false,
			"relay_hint": hint,
		}
		if rem != nil {
			m["relay_panel_id"] = rem.ID
			m["relay_panel_name"] = rem.Name
		}
		writeJSON(w, 200, okResp(m))
	}
	if rem == nil {
		empty(hint)
		return
	}
	if ok, msg := s.remoteVersionOK(rem.ID, Version); !ok {
		empty(msg)
		return
	}
	status, respBody, err := s.remoteCall(*rem, "GET", "/api/v1/central/relay-routes", nil, 10*time.Second)
	if err != nil {
		_, msg := remoteSideOf(err)
		if status == 404 {
			msg = fmt.Sprintf("远程面板版本太旧（需要 %s+），请先升级远程面板", Version)
		}
		empty(msg)
		return
	}
	var remote struct {
		Routes     []BackendRelayRoute `json:"routes"`
		LocalRelay bool                `json:"local_relay"`
	}
	if err := json.Unmarshal(respBody, &remote); err != nil || remote.Routes == nil {
		remote.Routes = []BackendRelayRoute{}
	}
	writeJSON(w, 200, okResp(map[string]any{
		"routes": remote.Routes, "local_relay": true,
		"proxied": true, "relay_panel_id": rem.ID, "relay_panel_name": rem.Name,
	}))
}

// relayRouteTargetLabel 审计日志里的路由目标写法：有节点记录写
// “节点 <id> <地址>:<端口>”，只填地址的路由写“<地址>:<端口>”。
func relayRouteTargetLabel(route BackendRelayRoute) string {
	if route.RemoteNodeID != 0 {
		return fmt.Sprintf("节点 %d %s:%d", route.RemoteNodeID, route.RemoteAddr, route.RemotePort)
	}
	return fmt.Sprintf("%s:%d", route.RemoteAddr, route.RemotePort)
}

// handleRelayRoutesCreate POST /api/v1/relay-routes：新增中转路由。
// 先存路由、再下发（改完重启 realm 已建立的中转连接会断，须重连——
// 前端确认框里写明）。管理面板上不本地存，直接代理到选中的中转面板。
func (s *Server) handleRelayRoutesCreate(w http.ResponseWriter, r *http.Request) {
	body, req, err := decodeRelayRouteBody(r)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	if !s.managesLocalRelay() {
		// 代理前先在本地拦格式错（监听／落地地址／端口／协议）；
		// 没选节点时落地地址必须直接填；撞端口等要查远程路由表，
		// 留给远程的完整校验。
		if _, err := validateRelayRouteFormat(req); err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
		if req.RemoteNodeID == 0 && strings.TrimSpace(req.RemoteAddr) == "" {
			writeJSON(w, 400, errResp(errRelayNoTarget))
			return
		}
		rem, hint := s.resolveRelayProxyRemote(relayProxyPanelID(r, &req))
		if rem == nil {
			writeJSON(w, 400, errResp(hint))
			return
		}
		s.proxyRelayRouteWrite(w, r, rem, "POST", "/api/v1/central/relay-routes", body,
			"代理新增中转路由", fmt.Sprintf("监听 %s → %s", req.ListenAddr, relayRouteTargetLabel(BackendRelayRoute{RemoteNodeID: req.RemoteNodeID, RemoteAddr: strings.TrimSpace(req.RemoteAddr), RemotePort: req.RemotePort})))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	route, err := validateRelayRoute(req, -1, s.nodes.getNode(req.RemoteNodeID),
		append([]BackendRelayRoute{}, s.nodes.data.Routes...), s.usedListenPorts())
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	// validate 只拦启用的撞监听；停用的同监听路由也要拦（键不能重）。
	if s.nodes.findRelayRoute(route.RelayID, route.ListenAddr) >= 0 {
		writeJSON(w, 400, errResp(fmt.Sprintf("监听地址 %s 已有中转路由", route.ListenAddr)))
		return
	}
	s.nodes.data.Routes = append(s.nodes.data.Routes, route)
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	applied, applyErr := s.applyAfterRouteChangeLocked()
	s.audit(s.currentUser(r), "新增中转路由", fmt.Sprintf("监听 %s → %s（%s）", route.ListenAddr, relayRouteTargetLabel(route), route.Proto), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"route": route, "applied": applied, "apply_error": applyErr,
	}))
}

// applyAfterRouteChangeLocked 与 applyAfterRouteChange 同义，调用方已
// 持有 nodes.mu 与 opMu（路由增改删 handler 内用）。
func (s *Server) applyAfterRouteChangeLocked() (bool, string) {
	applied, err := s.applyBackendRelayRoutesLocked()
	if err != nil {
		return false, err.Error()
	}
	if !applied {
		if reason := s.relayApplySkipReason(); reason != "" {
			return false, reason
		}
	}
	return applied, ""
}

// handleRelayRoutesUpdate PUT /api/v1/relay-routes：改一条路由
// （键是 relay_id＋listen_addr，监听地址本身不可改，改监听请删了重建；
// 没填的字段保持原值）。管理面板上不本地改，直接代理到选中的中转面板
// （部分字段可缺省，远程收到后按自己的路由表补齐再校验，管理面板
// 本地不做格式预校验）。
func (s *Server) handleRelayRoutesUpdate(w http.ResponseWriter, r *http.Request) {
	body, req, err := decodeRelayRouteBody(r)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	if !s.managesLocalRelay() {
		rem, hint := s.resolveRelayProxyRemote(relayProxyPanelID(r, &req))
		if rem == nil {
			writeJSON(w, 400, errResp(hint))
			return
		}
		s.proxyRelayRouteWrite(w, r, rem, "PUT", "/api/v1/central/relay-routes", body,
			"代理修改中转路由", fmt.Sprintf("监听 %s", strings.TrimSpace(req.ListenAddr)))
		return
	}
	if strings.TrimSpace(req.ListenAddr) == "" {
		writeJSON(w, 400, errResp("监听地址不能为空"))
		return
	}
	listen, err := normalizeListenAddr(req.ListenAddr)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	idx := s.nodes.findRelayRoute(req.RelayID, listen)
	if idx < 0 {
		writeJSON(w, 404, errResp("中转路由不存在"))
		return
	}
	// 没填的字段用原值补齐，再走同一份校验（自己排除在外）。
	// RemoteNodeID 为 0 且落地地址也空着＝没提供目标，用原值补；
	// 只填了落地地址、没选节点＝改成只填地址的路由（C 方案），不补节点。
	cur := s.nodes.data.Routes[idx]
	if req.RemoteNodeID == 0 && strings.TrimSpace(req.RemoteAddr) == "" {
		req.RemoteNodeID = cur.RemoteNodeID
	}
	if strings.TrimSpace(req.RemoteAddr) == "" {
		req.RemoteAddr = cur.RemoteAddr
	}
	if req.RemotePort == 0 {
		req.RemotePort = cur.RemotePort
	}
	if strings.TrimSpace(req.Proto) == "" {
		req.Proto = cur.Proto
	}
	if strings.TrimSpace(req.Transport) == "" {
		req.Transport = cur.Transport
	}
	if req.Enabled == nil {
		v := cur.Enabled
		req.Enabled = &v
	}
	if req.UseUDP == nil {
		v := cur.UseUDP
		req.UseUDP = &v
	}
	req.ListenAddr = listen
	route, err := validateRelayRoute(req, idx, s.nodes.getNode(req.RemoteNodeID),
		append([]BackendRelayRoute{}, s.nodes.data.Routes...), s.usedListenPorts())
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	s.nodes.data.Routes[idx] = route
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	applied, applyErr := s.applyAfterRouteChangeLocked()
	s.audit(s.currentUser(r), "修改中转路由", fmt.Sprintf("监听 %s → %s（%s）", route.ListenAddr, relayRouteTargetLabel(route), route.Proto), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"route": route, "applied": applied, "apply_error": applyErr,
	}))
}

// handleRelayRoutesDelete DELETE /api/v1/relay-routes：删一条路由
// （body 里带 relay_id＋listen_addr）。管理面板上不本地删，直接代理
// 到选中的中转面板。
func (s *Server) handleRelayRoutesDelete(w http.ResponseWriter, r *http.Request) {
	var req relayRouteReq
	var body []byte
	if r.ContentLength != 0 {
		b, decoded, err := decodeRelayRouteBody(r)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
		body, req = b, decoded
	}
	if !s.managesLocalRelay() {
		rem, hint := s.resolveRelayProxyRemote(relayProxyPanelID(r, &req))
		if rem == nil {
			writeJSON(w, 400, errResp(hint))
			return
		}
		s.proxyRelayRouteWrite(w, r, rem, "DELETE", "/api/v1/central/relay-routes", body,
			"代理删除中转路由", fmt.Sprintf("监听 %s", strings.TrimSpace(req.ListenAddr)))
		return
	}
	listen, err := normalizeListenAddr(req.ListenAddr)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	idx := s.nodes.findRelayRoute(req.RelayID, listen)
	if idx < 0 {
		writeJSON(w, 404, errResp("中转路由不存在"))
		return
	}
	route := s.nodes.data.Routes[idx]
	s.nodes.data.Routes = append(s.nodes.data.Routes[:idx], s.nodes.data.Routes[idx+1:]...)
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	applied, applyErr := s.applyAfterRouteChangeLocked()
	s.audit(s.currentUser(r), "删除中转路由", fmt.Sprintf("监听 %s → %s", route.ListenAddr, relayRouteTargetLabel(route)), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"deleted": listen, "applied": applied, "apply_error": applyErr,
	}))
}

// handleRelayRoutesApply POST /api/v1/relay-routes/apply：按当前路由表
// 重新下发一次（下发失败后重试用；无变化时不重启）。管理面板上不本地
// 下发，直接代理到选中的中转面板（body 里可带 relay_panel_id）。
func (s *Server) handleRelayRoutesApply(w http.ResponseWriter, r *http.Request) {
	var req relayRouteReq
	var body []byte
	if r.ContentLength != 0 {
		b, decoded, err := decodeRelayRouteBody(r)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
		body, req = b, decoded
	}
	if !s.managesLocalRelay() {
		rem, hint := s.resolveRelayProxyRemote(relayProxyPanelID(r, &req))
		if rem == nil {
			writeJSON(w, 400, errResp(hint))
			return
		}
		s.proxyRelayRouteWrite(w, r, rem, "POST", "/api/v1/central/relay-routes/apply", body,
			"代理重新下发中转路由", "")
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	applied, applyErr := s.applyAfterRouteChange()
	s.audit(s.currentUser(r), "重新下发中转路由", "", "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"applied": applied, "apply_error": applyErr,
	}))
}
