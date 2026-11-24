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
// 非空时不许含空白与冒号（端口另填），IP 或域名都行。
func normalizeRemoteAddr(in string) (string, error) {
	v := strings.TrimSpace(in)
	if v == "" {
		return "", nil
	}
	if strings.ContainsAny(v, " \t\r\n/:") {
		return "", fmt.Errorf("落地地址不对：填 IP 或域名，不要带端口（端口另填）")
	}
	return v, nil
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
// 写明“只保存、未下发”）。
func (s *Server) handleRelayRoutesList(w http.ResponseWriter, r *http.Request) {
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
type relayRouteReq struct {
	RelayID      int64  `json:"relay_id"`
	ListenAddr   string `json:"listen_addr"`
	RemoteNodeID int64  `json:"remote_node_id"`
	RemoteAddr   string `json:"remote_addr"`
	RemotePort   int    `json:"remote_port"`
	Proto        string `json:"proto"`
	Transport    string `json:"transport"`
	Enabled      *bool  `json:"enabled"`
	UseUDP       *bool  `json:"use_udp"`
}

// validateRelayRoute 校验并归一化一条路由（新建／修改共用；exclude
// 是修改时要排除的自己下标，-1＝新建）。node 与 routes 由调用方在
// 持锁时快照后传进来（纯函数，自己不加锁）；used 是已占用的监听端口
// 集合（落地监听、按人转发、面板端口）。
// 校验：监听地址合法、目标节点存在、落地地址（空＝节点地址）、落地
// 端口 1–65535、协议 tcp／udp、传输 tcp／tls／ws、同中转机监听端口不
// 撞其他路由与已占用的监听端口（落地监听、按人转发、面板端口）。
func validateRelayRoute(req relayRouteReq, exclude int, node *BackendNode, routes []BackendRelayRoute, used map[int]bool) (BackendRelayRoute, error) {
	listen, err := normalizeListenAddr(req.ListenAddr)
	if err != nil {
		return BackendRelayRoute{}, err
	}
	if node == nil {
		return BackendRelayRoute{}, fmt.Errorf("目标节点不存在")
	}
	remoteAddr, err := normalizeRemoteAddr(req.RemoteAddr)
	if err != nil {
		return BackendRelayRoute{}, err
	}
	if remoteAddr == "" {
		remoteAddr = strings.TrimSpace(node.Address)
	}
	if req.RemotePort < 1 || req.RemotePort > 65535 {
		return BackendRelayRoute{}, fmt.Errorf("落地端口须是 1–65535")
	}
	proto := strings.ToLower(strings.TrimSpace(req.Proto))
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		return BackendRelayRoute{}, fmt.Errorf("协议只能是 tcp 或 udp")
	}
	transport := strings.ToLower(strings.TrimSpace(req.Transport))
	if transport == "" {
		transport = "tcp"
	}
	if transport != "tcp" && transport != "tls" && transport != "ws" {
		return BackendRelayRoute{}, fmt.Errorf("传输只能是 tcp、tls 或 ws")
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	useUDP := proto == "udp"
	if req.UseUDP != nil {
		useUDP = *req.UseUDP
	}
	relayID := req.RelayID // v1 只支持本机中转：前端不填即 0
	// 同中转机监听端口不撞其他路由（只比启用的；停用的不占端口）。
	for i, rt := range routes {
		if i == exclude || rt.RelayID != relayID || !rt.Enabled {
			continue
		}
		if rt.ListenAddr == listen {
			return BackendRelayRoute{}, fmt.Errorf("监听地址 %s 已有中转路由", listen)
		}
	}
	// 不撞已占用的监听端口（落地监听、按人转发、面板端口）。
	if enabled {
		if used[listenPortOf(listen)] {
			return BackendRelayRoute{}, fmt.Errorf("监听端口 %d 已被占用（落地监听／按人转发／面板端口），换一个", listenPortOf(listen))
		}
	}
	return BackendRelayRoute{
		RelayID: relayID, ListenAddr: listen, RemoteNodeID: req.RemoteNodeID,
		RemoteAddr: remoteAddr, RemotePort: req.RemotePort,
		Proto: proto, UseUDP: useUDP, Transport: transport, Enabled: enabled,
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

// handleRelayRoutesCreate POST /api/v1/relay-routes：新增中转路由。
// 先存路由、再下发（改完重启 realm 已建立的中转连接会断，须重连——
// 前端确认框里写明）。
func (s *Server) handleRelayRoutesCreate(w http.ResponseWriter, r *http.Request) {
	var req relayRouteReq
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
	s.audit(s.currentUser(r), "新增中转路由", fmt.Sprintf("监听 %s → 节点 %d %s:%d（%s）", route.ListenAddr, route.RemoteNodeID, route.RemoteAddr, route.RemotePort, route.Proto), "成功", "")
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
// 没填的字段保持原值）。
func (s *Server) handleRelayRoutesUpdate(w http.ResponseWriter, r *http.Request) {
	var req relayRouteReq
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
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
	cur := s.nodes.data.Routes[idx]
	if req.RemoteNodeID == 0 {
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
	s.audit(s.currentUser(r), "修改中转路由", fmt.Sprintf("监听 %s → 节点 %d %s:%d（%s）", route.ListenAddr, route.RemoteNodeID, route.RemoteAddr, route.RemotePort, route.Proto), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"route": route, "applied": applied, "apply_error": applyErr,
	}))
}

// handleRelayRoutesDelete DELETE /api/v1/relay-routes：删一条路由
// （body 里带 relay_id＋listen_addr）。
func (s *Server) handleRelayRoutesDelete(w http.ResponseWriter, r *http.Request) {
	var req relayRouteReq
	if r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, 400, errResp("请求格式不对"))
			return
		}
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
	s.audit(s.currentUser(r), "删除中转路由", fmt.Sprintf("监听 %s → 节点 %d", route.ListenAddr, route.RemoteNodeID), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"deleted": listen, "applied": applied, "apply_error": applyErr,
	}))
}

// handleRelayRoutesApply POST /api/v1/relay-routes/apply：按当前路由表
// 重新下发一次（下发失败后重试用；无变化时不重启）。
func (s *Server) handleRelayRoutesApply(w http.ResponseWriter, r *http.Request) {
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
