package main

// ---------- 后端节点流量上报与健康（v1.4.4，切片 4） ----------
//
// agent 每 60 秒推一次：POST /api/v1/node/traffic（mTLS 客户端证书，
// 与配置拉取同一套 verifyNodeClientCert；没证书 401，没有旧接口可
// 回落）。请求体字段与 agent 端约定死，不可改字段名：
//   {"node_id":1,"window_start":<unix>,"window_end":<unix>,
//    "users":[{"user_key":"subuser:su-xxx","up":123,"down":456,
//              "inbound_tag":"reality-443"}],
//    "inbounds":[{"inbound_tag":"reality-443","up":123,"down":456}],
//    "online":[{"user_key":"subuser:su-xxx","ips":["203.0.113.1"]}],
//    "health":{"agent_version":"0.1.4","xray_version":"26.11.9",
//              "xray_running":true,"xray_restarts":0,"config_version":12,
//              "last_error":"","cpu_percent":3.2,"mem_mb":210}}
//
// 口径：
//   - 窗口去重：按 (node_id, window_start, window_end)，同一窗口重复
//     推只算一次。去重记录落盘 PANEL_HOME/node_traffic_seen.json
//     （600，原子写），面板重启不丢；48 小时前的窗口记录定期清理。
//     重复窗口仍会更新健康与最后上报时间（那是新鲜的状态），但不
//     再记流量。
//   - 汇总进 user_traffic.json（与 A 共用一本账，落盘规则不变）：
//     按人增量先按倍率折算再累加（addBackendDelta，不翻周期——周期
//     翻转只由 rollUserPeriods 做，与 A 同款）；user_key 解析失败、
//     用户不存在、上下行为负的条目跳过并计数进响应的 ignored_entries。
//   - 倍率：后端节点流量用节点自身倍率——BackendNode.Multiplier
//     （没有该字段的老节点按 1）× 中转倍率，再归一化（≤0／非法→1，
//     >100 钳到 100），与 1.3.40／1.3.43 同口径。只乘按人条目；入站
//     总量（落地级）不乘，与现有口径一致。
//   - 入站总量：节点上累计（InboundTraffic，存 nodes.json），不乘倍率。
//   - 健康：每次上报的 health＋收到时间随节点存（BackendNode 的
//     Health* 字段＋LastTrafficAt，存 nodes.json）。节点行上的状态
//     在渲染时按 last_seen 算，不新增轮询、不新增后台任务：
//     从没上报过 = 尚未上报；超过 3 个窗口（180 秒）没收到上报 =
//     失联；Xray 未运行 = 未运行＋last_error；否则在线＋已应用配置版本。
//   - 限流执法：userinfo、80% 提醒、100% 停用对直连生效——100% 停用
//     走已有的 disableUserForwardsLocked（它 bump 已指派该用户的节
//     点，agent 60 秒内拉到新用户列表、拿掉该用户）。执法跑在已有
//     的 trafficLoop（60 秒）里，不新开后台任务；见
//     enforceBackendNodeTrafficLimits。

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// nodeTrafficWindowStaleAfter 去重记录保留多久：超过这个时间的窗口
// 不再记（agent spool 只留 24 小时，48 小时足够挡重放）。
const nodeTrafficWindowStaleAfter = 48 * time.Hour

// nodeTrafficOfflineAfter 超过这么久没收到上报，节点算失联（3 个窗口）。
const nodeTrafficOfflineAfter = 180 * time.Second

// nodeTrafficMaxEntries 一次上报里每类数组最多条数，防超大请求拖垮面板。
const nodeTrafficMaxEntries = 1000

// ---------- 上报请求体 ----------

type nodeTrafficUserEntry struct {
	UserKey    string `json:"user_key"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
	InboundTag string `json:"inbound_tag"`
}

type nodeTrafficInboundEntry struct {
	InboundTag string `json:"inbound_tag"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
}

type nodeTrafficOnlineEntry struct {
	UserKey string   `json:"user_key"`
	IPs     []string `json:"ips"`
}

type nodeTrafficHealth struct {
	AgentVersion   string  `json:"agent_version"`
	XrayVersion    string  `json:"xray_version"`
	SingBoxVersion string  `json:"singbox_version"`
	XrayRunning    bool    `json:"xray_running"`
	XrayRestarts   int64   `json:"xray_restarts"`
	ConfigVersion  int64   `json:"config_version"`
	LastError      string  `json:"last_error"`
	CPUPercent     float64 `json:"cpu_percent"`
	MemMB          float64 `json:"mem_mb"`
}

type nodeTrafficReport struct {
	NodeID      int64                     `json:"node_id"`
	WindowStart int64                     `json:"window_start"`
	WindowEnd   int64                     `json:"window_end"`
	Users       []nodeTrafficUserEntry    `json:"users"`
	Inbounds    []nodeTrafficInboundEntry `json:"inbounds"`
	Online      []nodeTrafficOnlineEntry  `json:"online"`
	Health      *nodeTrafficHealth        `json:"health"`
}

// ---------- 窗口去重表（落盘） ----------

// nodeTrafficSeenStore 流量窗口去重表（PANEL_HOME/node_traffic_seen.json）：
// key 是 "nodeID:window_start:window_end"，值是收到时间（unix 秒）。
// 只记窗口不记条目——同一窗口整体只算一次。备份里带这份文件（与
// node_push_seen.json 同款待遇，见 backup_sched.go 的备份文件表）。
type nodeTrafficSeenStore struct {
	mu   sync.Mutex
	path string
	data map[string]int64
}

func newNodeTrafficSeenStore(path string) *nodeTrafficSeenStore {
	return &nodeTrafficSeenStore{path: path, data: map[string]int64{}}
}

func (n *nodeTrafficSeenStore) load() {
	b, err := os.ReadFile(n.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &n.data)
	if n.data == nil {
		n.data = map[string]int64{}
	}
}

func (n *nodeTrafficSeenStore) saveLocked() {
	b, _ := json.MarshalIndent(n.data, "", "  ")
	_ = writeFileAtomic(n.path, b, 0o600)
}

// checkAndRecord 校验并登记一个窗口：已见过返回 true（不登记、不计），
// 否则登记并落盘，返回 false。登记时顺带清掉 48 小时前的旧窗口记录。
func (n *nodeTrafficSeenStore) checkAndRecord(nodeID, windowStart, windowEnd int64) (dup bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	key := fmt.Sprintf("%d:%d:%d", nodeID, windowStart, windowEnd)
	if _, ok := n.data[key]; ok {
		return true
	}
	if n.data == nil {
		n.data = map[string]int64{}
	}
	now := time.Now().Unix()
	cutoff := now - int64(nodeTrafficWindowStaleAfter/time.Second)
	for k, v := range n.data {
		if v < cutoff {
			delete(n.data, k)
		}
	}
	n.data[key] = now
	n.saveLocked()
	return false
}

// ---------- 按人增量记账（后端节点上报用） ----------

// addBackendDelta 后端节点上报的按人增量记账（与 A 的 addDelta、旧
// agent 的 addAgentDelta 共用同一张 user_traffic.json）：只累加到
// 总量／本期／当日，不翻周期——周期翻转只由 rollUserPeriods 做（它
// 清本期、PeriodID+1、清提醒标志），上报端不带 period_id，就不该
// 在这里碰周期号。负增量调用方先拦住，不要直接调。
func (t *userTrafficStore) addBackendDelta(key string, up, down int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec, ok := t.data[key]
	if !ok || rec == nil {
		rec = &userTrafficRec{LastCounters: map[string][2]int64{}}
		t.data[key] = rec
	}
	if rec.LastCounters == nil {
		rec.LastCounters = map[string][2]int64{}
	}
	dayKey := time.Now().Format("2006-01-02")
	if rec.DayKey != dayKey {
		rec.DayKey = dayKey
		rec.DayUp, rec.DayDown = 0, 0
	}
	rec.UpTotal += up
	rec.DownTotal += down
	rec.UpPeriod += up
	rec.DownPeriod += down
	rec.DayUp += up
	rec.DayDown += down
	t.saveLocked()
}

// effectiveBackendMultiplier 某后端节点的有效倍率（v1.4.4）：节点自身
// 倍率（BackendNode.Multiplier，老节点无该字段按 1）× 中转倍率，再
// 按同一规则归一化（≤0／非法→1，>100 钳到 100）。与 1.3.40／1.3.43
// 同口径；只乘按人条目，入站总量与落地级流量不乘。
func (s *Server) effectiveBackendMultiplier(node *BackendNode) float64 {
	mult := 1.0
	if node != nil {
		mult = node.Multiplier
	}
	return normalizeTrafficMultiplier(mult * s.relayMultiplier())
}

// ---------- 健康状态（渲染时算，不轮询） ----------

// nodeBackendHealthStatus 节点上报健康（v1.4.4）：按 LastTrafficAt
// 算 staleness。返回 status（online／offline／never）与一行详情文案。
func nodeBackendHealthStatus(n *BackendNode, now time.Time) (status, detail string) {
	if n == nil || n.LastTrafficAt == 0 {
		return "never", "尚未上报"
	}
	if now.Unix()-n.LastTrafficAt > int64(nodeTrafficOfflineAfter/time.Second) {
		return "offline", "失联，超过 180 秒没收到上报"
	}
	if n.HealthAt != 0 && !n.HealthXrayRunning {
		d := "Xray 未运行"
		if strings.TrimSpace(n.HealthLastError) != "" {
			d += "：" + strings.TrimSpace(n.HealthLastError)
		}
		return "offline", d
	}
	d := "在线"
	if n.HealthConfigVersion > 0 {
		d += "，配置版本 v" + strconv.FormatInt(n.HealthConfigVersion, 10)
	}
	return "online", d
}

// nodeBackendHealthMap 节点上报健康的可序列化视图（列表行与详情共用）。
func nodeBackendHealthMap(n *BackendNode, now time.Time) map[string]any {
	status, detail := nodeBackendHealthStatus(n, now)
	lastTraffic := ""
	if n.LastTrafficAt != 0 {
		lastTraffic = time.Unix(n.LastTrafficAt, 0).UTC().Format(time.RFC3339)
	}
	cfgVer := n.HealthConfigVersion
	if cfgVer == 0 {
		cfgVer = n.ConfigVersion
	}
	return map[string]any{
		"status": status, "detail": detail,
		"last_error": n.HealthLastError, "xray_running": n.HealthXrayRunning,
		"xray_restarts": n.HealthXrayRestarts, "last_traffic_at": lastTraffic,
		"config_version": cfgVer,
		"cpu_percent":    n.HealthCPUPercent, "mem_mb": n.HealthMemMB,
	}
}

// ---------- 流量上报接口 ----------

// handleNodeBackendTraffic POST /api/v1/node/traffic：后端节点流量＋
// 健康上报。mTLS 认证（verifyNodeClientCert），body 里的 node_id 须
// 与证书里的节点 id 一致；没证书 401，没有旧接口可回落。
func (s *Server) handleNodeBackendTraffic(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := s.nodes.verifyNodeClientCert(r)
	if !ok {
		writeJSON(w, 401, errResp("节点证书不对"))
		return
	}
	var req nodeTrafficReport
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if req.NodeID != nodeID {
		writeJSON(w, 400, errResp("node_id 与证书里的节点不一致"))
		return
	}
	if req.WindowStart <= 0 || req.WindowEnd <= req.WindowStart || req.WindowEnd-req.WindowStart > 3600 {
		writeJSON(w, 400, errResp("上报窗口不对"))
		return
	}
	if len(req.Users) > nodeTrafficMaxEntries || len(req.Inbounds) > nodeTrafficMaxEntries || len(req.Online) > nodeTrafficMaxEntries {
		writeJSON(w, 400, errResp(fmt.Sprintf("一次上报每类最多 %d 条", nodeTrafficMaxEntries)))
		return
	}
	// 节点在不在（只短暂持锁）。
	s.nodes.mu.Lock()
	node := s.nodes.getNode(nodeID)
	deleted := false
	if node == nil {
		deleted = s.nodes.isDeleted(nodeID)
	}
	s.nodes.mu.Unlock()
	if node == nil {
		if deleted {
			writeJSON(w, 200, okResp(map[string]any{
				"node_id": nodeID, "deleted": true, "accepted_windows": 0,
				"duplicate": false, "accepted_users": 0, "ignored_entries": 0,
			}))
			return
		}
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	// 窗口去重（落盘）：重复窗口只算一次流量，但健康与最后上报时间照常更新。
	dup := s.nodeTrafficSeen.checkAndRecord(nodeID, req.WindowStart, req.WindowEnd)
	acceptedUsers := 0
	ignored := []map[string]any{}
	if !dup {
		mult := s.effectiveBackendMultiplier(node)
		for _, e := range req.Users {
			key, why := s.parseBackendTrafficUserKey(e.UserKey)
			if why == "" && (e.Up < 0 || e.Down < 0) {
				why = "up/down 不能为负"
			}
			if why != "" {
				ignored = append(ignored, map[string]any{"user_key": e.UserKey, "reason": why})
				continue
			}
			up, down := e.Up, e.Down
			if mult != 1 {
				up = int64(math.Round(float64(up) * mult))
				down = int64(math.Round(float64(down) * mult))
			}
			s.userTraffic.addBackendDelta(key, up, down)
			acceptedUsers++
		}
	}
	// 入站总量与健康随节点存（重复窗口也更新：这是新鲜的状态，不是流量）。
	s.nodes.mu.Lock()
	node = s.nodes.getNode(nodeID)
	if node != nil {
		now := time.Now().Unix()
		node.LastTrafficAt = now
		if !dup {
			if node.InboundTraffic == nil {
				node.InboundTraffic = map[string][2]int64{}
			}
			for _, e := range req.Inbounds {
				tag := strings.TrimSpace(e.InboundTag)
				if tag == "" || e.Up < 0 || e.Down < 0 {
					continue
				}
				cur := node.InboundTraffic[tag]
				node.InboundTraffic[tag] = [2]int64{cur[0] + e.Up, cur[1] + e.Down}
			}
		}
		if req.Health != nil {
			h := req.Health
			node.HealthAt = now
			node.HealthXrayRunning = h.XrayRunning
			node.HealthXrayRestarts = h.XrayRestarts
			node.HealthLastError = strings.TrimSpace(h.LastError)
			node.HealthCPUPercent = h.CPUPercent
			node.HealthMemMB = h.MemMB
			if h.ConfigVersion > 0 {
				node.HealthConfigVersion = h.ConfigVersion
			}
			if strings.TrimSpace(h.AgentVersion) != "" {
				node.AgentVersion = strings.TrimSpace(h.AgentVersion)
			}
			if strings.TrimSpace(h.XrayVersion) != "" {
				node.XrayVersion = strings.TrimSpace(h.XrayVersion)
			}
			if strings.TrimSpace(h.SingBoxVersion) != "" {
				node.SingBoxVersion = strings.TrimSpace(h.SingBoxVersion)
			}
		}
		_ = s.nodes.saveLocked()
	}
	s.nodes.mu.Unlock()
	acceptedWindows := 0
	if !dup {
		acceptedWindows = 1
	}
	if ignored == nil {
		ignored = []map[string]any{}
	}
	status, _ := nodeBackendHealthStatus(node, time.Now())
	writeJSON(w, 200, okResp(map[string]any{
		"node_id": nodeID, "accepted_windows": acceptedWindows,
		"duplicate": dup, "accepted_users": acceptedUsers,
		"ignored_entries": len(ignored), "ignored": ignored,
		"backend_health": status,
	}))
}

// parseBackendTrafficUserKey 解析一条上报里的 user_key
// （"subuser:su-…" / "merged:ms-…"）：用户不存在也算解析失败（跳过
// 并计数，不让整单失败）。返回面板内部的订阅标识与失败原因。
func (s *Server) parseBackendTrafficUserKey(userKey string) (key, why string) {
	userKey = strings.TrimSpace(userKey)
	kind, id, found := strings.Cut(userKey, ":")
	if !found || (kind != "subuser" && kind != "merged") || id == "" {
		return "", "user_key 格式不对"
	}
	if kind == "subuser" {
		if findSubUserByID(s.cfg.SubUsers, id) == nil && !s.hasCentralUser("subuser", id) {
			return "", "订阅用户不存在"
		}
	} else {
		if findMergedSubByID(s.cfg.MergedSubs, id) == nil && !s.hasCentralUser("merged", id) {
			return "", "合并订阅不存在"
		}
	}
	return userKey, ""
}

// ---------- 节点详情流量块 ----------

// nodeBackendTrafficBlock 节点详情里的流量块：本期按人合计（读
// user_traffic.json 的本期数，与 userinfo、80%／100% 执法同一本账）
// 与各入站总量（节点上累计的原始字节，不乘倍率）。
func (s *Server) nodeBackendTrafficBlock(node *BackendNode) map[string]any {
	rows := []map[string]any{}
	var upPeriod, downPeriod, upTotal, downTotal int64
	// 只算该节点上的有效用户（未停用、过两道门）；UUID 生成不走这里，
	// 只读配置，不写盘。
	for i := range s.cfg.SubUsers {
		u := &s.cfg.SubUsers[i]
		if u.Disabled {
			continue
		}
		key := userSubKey("subuser", u.ID)
		if !nodeHasUser(node, key) {
			continue
		}
		if !s.backendAssignAllows(u.BackendScope, u.BackendTargets, u.GroupID, node) {
			continue
		}
		up, down, upP, downP, ok := s.userTraffic.totals(key)
		if !ok {
			continue
		}
		upPeriod += upP
		downPeriod += downP
		upTotal += up
		downTotal += down
		rows = append(rows, map[string]any{
			"user_id": key, "username": u.Name,
			"up_period": upP, "down_period": downP,
			"up_total": up, "down_total": down,
		})
	}
	for i := range s.cfg.MergedSubs {
		m := &s.cfg.MergedSubs[i]
		if m.Disabled {
			continue
		}
		key := userSubKey("merged", m.ID)
		if !nodeHasUser(node, key) {
			continue
		}
		if !s.backendAssignAllows(m.BackendScope, m.BackendTargets, m.GroupID, node) {
			continue
		}
		up, down, upP, downP, ok := s.userTraffic.totals(key)
		if !ok {
			continue
		}
		upPeriod += upP
		downPeriod += downP
		upTotal += up
		downTotal += down
		rows = append(rows, map[string]any{
			"user_id": key, "username": m.Name,
			"up_period": upP, "down_period": downP,
			"up_total": up, "down_total": down,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i]["user_id"].(string) < rows[j]["user_id"].(string)
	})
	inbounds := []map[string]any{}
	tags := make([]string, 0, len(node.InboundTraffic))
	for tag := range node.InboundTraffic {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		v := node.InboundTraffic[tag]
		inbounds = append(inbounds, map[string]any{
			"inbound_tag": tag, "up": v[0], "down": v[1],
		})
	}
	return map[string]any{
		"users":     rows,
		"up_period": upPeriod, "down_period": downPeriod,
		"up_total": upTotal, "down_total": downTotal,
		"inbounds": inbounds,
	}
}

// ---------- 后端节点限流执法（直连） ----------

// enforceBackendNodeTrafficLimits 后端节点用户的 80%／100% 执法
// （v1.4.4）：直连流量进了 user_traffic.json 后，80% 提醒、100% 停
// 用对直连也要生效。100% 停用走 disableUserForwardsLocked——它 bump
// 已指派该用户的节点，agent 60 秒内拉到新用户列表、拿掉该用户（1.4.2
// 的停用 choke 点，链路已通；本函数只负责“在没有本地 relay 的面板
// 上也裁决”，不另发明停用路径）。
// 只裁决“后端节点专属”的用户：开了限流、在至少一个后端节点上有指
// 派、且没有任何 UserForwards 记录——有转发的用户走 A 的
// enforceUserTrafficLimits（同一分钟里不重复裁决、不重复发提醒）。
// 跑在已有的 trafficLoop（60 秒）里，不新开后台任务与轮询。
func (s *Server) enforceBackendNodeTrafficLimits() {
	s.rollUserPeriods(time.Now())
	s.nodes.mu.Lock()
	nodes := make([]BackendNode, 0, len(s.nodes.data.Nodes))
	for _, n := range s.nodes.data.Nodes {
		if n != nil {
			nodes = append(nodes, *n)
		}
	}
	s.nodes.mu.Unlock()
	if len(nodes) == 0 {
		return
	}
	hasForward := map[string]bool{}
	for _, f := range s.cfg.UserForwards {
		hasForward[f.Key] = true
	}
	check := func(key, name string, limit int64, mode string) {
		if limit <= 0 || hasForward[key] {
			return
		}
		assigned := false
		for i := range nodes {
			if nodeHasUser(&nodes[i], key) {
				assigned = true
				break
			}
		}
		if !assigned {
			return
		}
		rec := s.userTraffic.get(key)
		used := userTrafficUsed(rec, mode)
		pct := float64(used) / float64(limit) * 100
		display := name
		if display == "" {
			display = key
		}
		if pct >= 80 && !s.userWarned80(key) {
			s.sendTelegram("流量提醒：订阅 " + display + " 流量已用 " + formatPct(pct) + "%（含直连）")
			s.setUserWarned80(key, true)
		}
		if userOverLimit(rec, limit, mode) {
			if !s.userWarned100(key) {
				s.sendTelegram("流量用完：订阅 " + display + " 已自动停用（流量上限，含直连），续量后恢复")
				s.setUserWarned100(key, true)
			}
			s.disableUserForwards(key, "traffic")
		}
	}
	for i := range s.cfg.SubUsers {
		u := &s.cfg.SubUsers[i]
		check(userSubKey("subuser", u.ID), u.Name, u.TrafficLimitBytes, u.TrafficLimitMode)
	}
	for i := range s.cfg.MergedSubs {
		m := &s.cfg.MergedSubs[i]
		check(userSubKey("merged", m.ID), m.Name, m.TrafficLimitBytes, m.TrafficLimitMode)
	}
}
