package main

// ---------- 节点侧 agent 接口（v1.3.30，D10） ----------
//
// 背景：节点侧按人计量完整方案 v1（`node-side-metering-plan.md`）与
// agent 仓 DEV-PLAN §3.6、接口审计（面板 1.3.29 实测结论）：agent
// 每 60 秒向面板拉用户表/配置、推流量增量。面板 1.3.29 之前这三个
// 接口一个都没有，panel-adapter 转接做不到推送，必须面板原生提供。
//
// 三个接口（全部复用 central 的 rpt_ 令牌认证：`requireCentralToken`，
// Bearer + SHA-256 哈希比对，同 IP 5 次失败锁 5 分钟；未认证返回 401，
// 不新发明认证）：
//   GET  /api/v1/node/users?node_id=…   用户表下发，带 ETag，未变 304
//   POST /api/v1/node/push             流量增量接收，按 (user_id, period_id, seq) 去重
//   GET  /api/v1/node/config?node_id=… agent 配置下发
//
// node_id 是落地地址（relay.conf 的 LANDING，与 state.json 的落地键
// 一致，例如 203.0.113.1）。用户表是全面板的（不按落地过滤），
// node_id 只用来取该落地的 agent 状态与计量来源。
//
// 计量口径：agent 推的是 Xray 负载字节（不含 TLS/WS 帧头与 TCP/IP 头），
// A 方案采的是 L3 字节；同一落地、同一用户，面板只认一个来源的数
// （方案 §4.5）：该落地 agent 在线（5 分钟内有推送）时，以节点侧
// 的数为准，A 的中转侧采样跳过该落地的转发（见 sampleUserTraffic
// 里的 nodeAgentLive 门）；agent 离线超过 5 分钟回落到 A 的数，
// 界面标注“仅中转”。两个来源永远不相加。
//
// 限制（本版不做，Phase 2 再做）：
//   - enforce=false：本版只收数、不执法；80%/100% 停用仍只走 A 的
//     enforceUserTrafficLimits（读同一张 user_traffic.json，所以
//     agent 推上来的超量用户也会被 A 的裁决停用——但停用只撤中转
//     转发，agent 侧删用户 + 重启 Xray 的执法是 Phase 2 的事）。
//   - 切换到 agent 的当期，本期用量含切换前 A 已采的数（同一张表），
//     下一周期起纯净；总量（UpTotal/DownTotal）始终是两路累计之和，
//     切换时不重算。
//   - 每人 UUID 在 1.3.35 生成（见本文件末尾的 newNodeUUID 与
//     ensureNodeUUIDs*）：新建用户即生成、已有用户启动与集中管理轮询
//     时补生成、删除用户随记录回收；用户表下发时带上 UUID。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

// agent 拉取/推送节奏（与方案 v1 §4.3 一致：快照 10 秒，推送 60 秒）。
const (
	nodeAgentPushIntervalSeconds = 60
	// nodeAgentMinVersion agent 最低版本：低于它的 agent 面板仍接受推送，
	// 但 config 里标明，面板侧可按版本标红（Phase 1 的界面工作）。
	nodeAgentMinVersion = "0.1.0"
	// nodeAgentOfflineAfter 超过这么久没推送，agent 算离线，计量回落到 A。
	nodeAgentOfflineAfter = 5 * time.Minute
	// nodeAgentMaxPushEntries 一次推送最多条数，防超大请求拖垮面板。
	nodeAgentMaxPushEntries = 1000
	// nodeAgentGenericUserID 通用订阅的计量身份（方案 §3.4）：旧共用 UUID
	// 的流量计在这里，不归属到任何订阅者；Phase 0 节点总量也走这条。
	nodeAgentGenericUserID = "generic"
	// nodeAgentGenericEmail 通用订阅在 Xray 里的 email。
	nodeAgentGenericEmail = "generic@node"
)

// nodeUser 下发给 agent 的一条用户记录：订阅记录（SubUser/MergedSub）
// 的计量子集。user_id 与面板内部的订阅标识一致（"subuser:su-…" /
// "merged:ms-…"），email 是 Xray 计数钥匙（方案 §3.2：
// u-sub-<id>@node / u-merged-<id>@node），轮换 UUID 不断用户历史
// （流量永远按 user_id 记）。
type nodeUser struct {
	UserID            string            `json:"user_id"`
	Kind              string            `json:"kind"`
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Email             string            `json:"email"`
	NodeUUIDs         map[string]string `json:"node_uuids"`
	TrafficLimitBytes int64             `json:"traffic_limit_bytes"`
	TrafficLimitMode  string            `json:"traffic_limit_mode"`
	ResetMode         string            `json:"reset_mode"`
	ResetDay          int               `json:"reset_day"`
	NextResetAt       string            `json:"next_reset_at"`
	LastResetAt       string            `json:"last_reset_at"`
	ExpiresAt         string            `json:"expires_at"`
	Disabled          bool              `json:"disabled"`
	DisabledReason    string            `json:"disabled_reason"`
	GroupID           string            `json:"group_id"`
}

// nodeUserEmail Xray 计数钥匙（方案 §3.2）。
func nodeUserEmail(kind, id string) string {
	if kind == "merged" {
		return "u-merged-" + id + "@node"
	}
	return "u-sub-" + id + "@node"
}

// ---------- 每人 UUID 生成与补齐（v1.3.35，切片 1） ----------
//
// 每人 UUID 是节点侧按人计量的身份（方案 §3.2）：一个订阅在每个落地
// 各一个 UUID，agent 按落地地址从 node_uuids 里取。生成规则：
//   - 只补不剪：缺失的落地补生成，已有的永远不动；勾选去掉的落地的
//     UUID 留在表里不删（agent 只按自己落地地址取，复勾时 UUID 稳定
//     不变；删用户时整条记录删掉，UUID 随之回收）。
//   - 生效勾选：进了组用组的落地集合，没进组用自己的勾选（与转发
//     核对同一口径，见 effectiveSelectionFor*）。
//   - 地址解析：本地落地直接从 relay.conf 解；远程落地靠集中管理
//     快照（remoteLandingAddr），快照没到时该落地先不生成、等快照
//     到了由轮询补齐（pollAllRemotes 末尾扫一遍）。
//   - 调用方负责保存：ensure/backfill 只改内存、返回有没有改动，
//     改了的调用方调 saveConfig 落盘。

// newNodeUUID 生成一个 UUIDv4（crypto/rand，与节点 UUID 同款随机强度）。
func newNodeUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 读不到是致命环境问题，不要静默返回全零 UUID。
		panic("生成节点 UUID 失败：" + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // 版本 4
	b[8] = (b[8] & 0x3f) | 0x80 // 变体 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// landingAddrsForSelection 把生效勾选解析成落地地址列表（本地在前、
// 远程在后，各自保持原有顺序；去重）。远程落地快照没到时跳过不报错，
// 调用方（轮询补齐）下次再来。
func (s *Server) landingAddrsForSelection(sel []SubSelection) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range s.localLandingsForSelection(sel) {
		if l.Landing == "" || seen[l.Landing] {
			continue
		}
		seen[l.Landing] = true
		out = append(out, l.Landing)
	}
	for _, e := range sel {
		if e.Panel == "local" {
			continue
		}
		addr, ok := s.remoteLandingAddr(e.Panel, e.Landing)
		if !ok || addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, addr)
	}
	return out
}

// ensureNodeUUIDs 给一张 node_uuids 表补齐指定落地地址的 UUID（只补
// 不剪）。uuids 为 nil 时新建表。返回新表与有没有改动。
func ensureNodeUUIDs(uuids map[string]string, addrs []string) (map[string]string, bool) {
	if uuids == nil {
		uuids = map[string]string{}
	}
	changed := false
	for _, addr := range addrs {
		if addr == "" {
			continue
		}
		if _, ok := uuids[addr]; ok {
			continue
		}
		uuid := newNodeUUID()
		// 同一张表里不许撞（概率极低，撞了就重抽一个）。
		for {
			dup := false
			for _, v := range uuids {
				if v == uuid {
					dup = true
					break
				}
			}
			if !dup {
				break
			}
			uuid = newNodeUUID()
		}
		uuids[addr] = uuid
		changed = true
	}
	return uuids, changed
}

// ensureNodeUUIDsForSubUser 按生效勾选给一个订阅用户补齐每人 UUID。
func (s *Server) ensureNodeUUIDsForSubUser(u *SubUser) bool {
	addrs := s.landingAddrsForSelection(s.effectiveSelectionForSubUser(u))
	uuids, changed := ensureNodeUUIDs(u.NodeUUIDs, addrs)
	u.NodeUUIDs = uuids
	return changed
}

// ensureNodeUUIDsForMerged 按生效勾选给一条合并订阅补齐每人 UUID。
func (s *Server) ensureNodeUUIDsForMerged(m *MergedSub) bool {
	addrs := s.landingAddrsForSelection(s.effectiveSelectionForMerged(m))
	uuids, changed := ensureNodeUUIDs(m.NodeUUIDs, addrs)
	m.NodeUUIDs = uuids
	return changed
}

// backfillNodeUUIDs 给全部订阅用户与合并订阅补齐每人 UUID（启动与
// 集中管理轮询时调用）。只改内存、返回有没有改动，改了的调用方负责
// 调 saveConfig 落盘。
func (s *Server) backfillNodeUUIDs() bool {
	changed := false
	for i := range s.cfg.SubUsers {
		if s.ensureNodeUUIDsForSubUser(&s.cfg.SubUsers[i]) {
			changed = true
		}
	}
	for i := range s.cfg.MergedSubs {
		if s.ensureNodeUUIDsForMerged(&s.cfg.MergedSubs[i]) {
			changed = true
		}
	}
	return changed
}

// nodeUserForSubUser 一个订阅用户的下发记录（v1.3.36 从
// nodeUsersPayload 里抽出：集中用户同步复用同一形状）。NodeUUIDs
// 复制一份（调用方合并集中用户时会改这个 map，不能写回配置）。
func nodeUserForSubUser(u *SubUser) nodeUser {
	uuids := map[string]string{}
	for a, v := range u.NodeUUIDs {
		uuids[a] = v
	}
	return nodeUser{
		UserID: userSubKey("subuser", u.ID), Kind: "subuser", ID: u.ID, Name: u.Name,
		Email: nodeUserEmail("subuser", u.ID), NodeUUIDs: uuids,
		TrafficLimitBytes: u.TrafficLimitBytes, TrafficLimitMode: u.TrafficLimitMode,
		ResetMode: u.ResetMode, ResetDay: u.ResetDay, NextResetAt: u.NextResetAt, LastResetAt: u.LastResetAt,
		ExpiresAt: u.ExpiresAt, Disabled: u.Disabled, DisabledReason: u.DisabledReason, GroupID: u.GroupID,
	}
}

// nodeUserForMerged 一条合并订阅的下发记录（同上）。
func nodeUserForMerged(m *MergedSub) nodeUser {
	uuids := map[string]string{}
	for a, v := range m.NodeUUIDs {
		uuids[a] = v
	}
	return nodeUser{
		UserID: userSubKey("merged", m.ID), Kind: "merged", ID: m.ID, Name: m.Name,
		Email: nodeUserEmail("merged", m.ID), NodeUUIDs: uuids,
		TrafficLimitBytes: m.TrafficLimitBytes, TrafficLimitMode: m.TrafficLimitMode,
		ResetMode: m.ResetMode, ResetDay: m.ResetDay, NextResetAt: m.NextResetAt, LastResetAt: m.LastResetAt,
		ExpiresAt: m.ExpiresAt, Disabled: m.Disabled, DisabledReason: m.DisabledReason, GroupID: m.GroupID,
	}
}

// nodeUsersPayload 组装用户表（SubUser + MergedSub，按 ID 排序，稳定
// 的 ETag 前提）与它的 ETag（用户表 JSON 的 SHA-256，强校验）。
func (s *Server) nodeUsersPayload() ([]nodeUser, string) {
	users := make([]nodeUser, 0, len(s.cfg.SubUsers)+len(s.cfg.MergedSubs))
	byID := map[string]int{}
	for i := range s.cfg.SubUsers {
		row := nodeUserForSubUser(&s.cfg.SubUsers[i])
		byID[row.UserID] = len(users)
		users = append(users, row)
	}
	for i := range s.cfg.MergedSubs {
		row := nodeUserForMerged(&s.cfg.MergedSubs[i])
		byID[row.UserID] = len(users)
		users = append(users, row)
	}
	// 集中用户（v1.3.36）：管理面板同步来的用户行（按落地分表），与本地
	// 用户合并：同一 user_id 只出现一次，NodeUUIDs 按落地合并；本地记录
	// 优先（本面板自己的用户为准），集中用户里同一 user_id 在多个落地
	// 出现时只合并 UUID、不重复出 row。落地键排序，保证 ETag 稳定
	// （map 迭代顺序随机，不能直接拼）。
	landings := make([]string, 0, len(s.cfg.CentralUsers))
	for addr := range s.cfg.CentralUsers {
		landings = append(landings, addr)
	}
	sort.Strings(landings)
	for _, addr := range landings {
		for _, cu := range s.cfg.CentralUsers[addr] {
			if idx, ok := byID[cu.UserID]; ok {
				for a, uuid := range cu.NodeUUIDs {
					if _, exists := users[idx].NodeUUIDs[a]; !exists {
						users[idx].NodeUUIDs[a] = uuid
					}
				}
				continue
			}
			byID[cu.UserID] = len(users)
			row := cu
			row.NodeUUIDs = map[string]string{}
			for a, uuid := range cu.NodeUUIDs {
				row.NodeUUIDs[a] = uuid
			}
			users = append(users, row)
		}
	}
	b, _ := json.Marshal(users)
	sum := sha256.Sum256(b)
	return users, `"` + hex.EncodeToString(sum[:]) + `"`
}

// nodeAgentStatusPayload 该落地的 agent 状态（版本 / 最后推送时间 /
// 计量来源 / 是否在线）。node_id 没给或落地不存在时返回全空状态。
func (s *Server) nodeAgentStatusPayload(nodeID string) map[string]any {
	st := map[string]any{"version": "", "last_push_at": "", "metering_source": "", "online": false}
	if strings.TrimSpace(nodeID) == "" {
		return st
	}
	ext := s.state.ext(nodeID)
	st["version"] = ext.AgentVersion
	st["last_push_at"] = ext.AgentLastPushAt
	st["metering_source"] = ext.MeteringSource
	st["online"] = s.nodeAgentLive(nodeID)
	return st
}

// nodeAgentLive 该落地 agent 是否在线：计量来源是 agent，且 5 分钟内
// 有过推送。在线时 A 的中转侧采样跳过该落地的转发（§4.5 不叠加）。
func (s *Server) nodeAgentLive(landingAddr string) bool {
	ext := s.state.ext(landingAddr)
	if ext.MeteringSource != "agent" {
		return false
	}
	last, err := time.Parse(time.RFC3339, ext.AgentLastPushAt)
	if err != nil {
		return false
	}
	return time.Since(last) <= nodeAgentOfflineAfter
}

// handleNodeUsers GET /api/v1/node/users：用户表下发。
// If-None-Match 与当前 ETag 一致时返回 304（无 body），agent 每 60 秒
// 拉一次、没变化不传表。
func (s *Server) handleNodeUsers(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	users, etag := s.nodeUsersPayload()
	w.Header().Set("ETag", etag)
	if inm := strings.TrimSpace(r.Header.Get("If-None-Match")); inm != "" {
		// 弱校验（W/ 前缀）也认；多值时逐个比对。
		for _, v := range strings.Split(inm, ",") {
			v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "W/"))
			if v == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	writeJSON(w, 200, okResp(map[string]any{
		"node_id": nodeID, "panel_version": Version, "etag": etag,
		"users": users, "generic_email": nodeAgentGenericEmail,
		"agent_status": s.nodeAgentStatusPayload(nodeID),
	}))
}

// handleNodeConfig GET /api/v1/node/config：agent 配置下发。
// enforce=false：本版只收数、不执法（Phase 2 再开）。
func (s *Server) handleNodeConfig(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	writeJSON(w, 200, okResp(map[string]any{
		"node_id": nodeID, "panel_version": Version,
		"push_interval_seconds": nodeAgentPushIntervalSeconds,
		"min_agent_version":     nodeAgentMinVersion,
		"enforce":               false,
		"generic_email":         nodeAgentGenericEmail,
		"agent_status":          s.nodeAgentStatusPayload(nodeID),
	}))
}

// nodePushEntry 一次推送里的一条增量（方案 §4.4）：seq 每用户每周期
// 递增，面板按 (user_id, period_id, seq) 去重。
type nodePushEntry struct {
	UserID    string `json:"user_id"`
	PeriodID  int    `json:"period_id"`
	Seq       int64  `json:"seq"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
}

type nodePushRequest struct {
	NodeID       string          `json:"node_id"`
	AgentVersion string          `json:"agent_version"`
	Entries      []nodePushEntry `json:"entries"`
}

// nodePushSeenStore 推送去重表（PANEL_HOME/node_push_seen.json）：
// key 是 "user_id\x00period_id"，值是已见的最大 seq；seq 小于等于已见
// 最大值就是重推（重试/重放），丢弃。只记最大值就够了——seq 每用户
// 每周期单调递增，不用记每一条。重启与整机迁移恢复后文件还在，
// 重放仍会被挡住（备份里带这份文件，见 backup_sched.go）。
type nodePushSeenStore struct {
	mu   sync.Mutex
	path string
	data map[string]int64
}

func newNodePushSeenStore(path string) *nodePushSeenStore {
	return &nodePushSeenStore{path: path, data: map[string]int64{}}
}

func (n *nodePushSeenStore) load() {
	b, err := os.ReadFile(n.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &n.data)
	if n.data == nil {
		n.data = map[string]int64{}
	}
}

func (n *nodePushSeenStore) saveLocked() {
	b, _ := json.MarshalIndent(n.data, "", "  ")
	_ = writeFileAtomic(n.path, b, 0o600)
}

// checkAndRecord 校验并登记一条推送：是重推返回 true（不登记、不计），
// 否则登记最大 seq 并落盘，返回 false。
func (n *nodePushSeenStore) checkAndRecord(userID string, periodID int, seq int64) (dup bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	key := userID + "\x00" + strconv.Itoa(periodID)
	if max, ok := n.data[key]; ok && seq <= max {
		return true
	}
	if n.data == nil {
		n.data = map[string]int64{}
	}
	n.data[key] = seq
	n.saveLocked()
	return false
}

// addAgentDelta agent 推送的增量记账（与 A 的 addDelta 共用同一张
// user_traffic.json）：period_id 变了就翻周期（本期清零、周期号跟上），
// 否则累加到本期与总量。负增量不接受（调用方先校验）。
func (t *userTrafficStore) addAgentDelta(key string, up, down int64, periodID int) {
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
	if rec.PeriodID != periodID {
		rec.UpPeriod, rec.DownPeriod = 0, 0
		rec.PeriodID = periodID
	}
	// v1.3.32：日桶（落地卡片“今日”用；与 A 侧 Daily 桶同一服务器本地时钟）。
	// 日期变了先清零当日再累加。
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

// handleNodePush POST /api/v1/node/push：流量增量接收。
// 空 entries 也接受（当心跳用，只更新 agent 状态）。逐条校验与去重，
// 坏条目只记 rejected，不让整单失败；agent_version 与 node_id 有就
// 更新该落地的 agent 状态（metering_source=agent、最后推送时间）。
func (s *Server) handleNodePush(w http.ResponseWriter, r *http.Request) {
	var req nodePushRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if len(req.Entries) > nodeAgentMaxPushEntries {
		writeJSON(w, 400, errResp(fmt.Sprintf("一次最多推 %d 条", nodeAgentMaxPushEntries)))
		return
	}
	nodeID := strings.TrimSpace(req.NodeID)
	agentVersion := strings.TrimSpace(req.AgentVersion)

	accepted, duplicates := 0, 0
	var rejected []map[string]any
	for _, e := range req.Entries {
		userID := strings.TrimSpace(e.UserID)
		key, why := "", ""
		switch {
		case userID == "":
			why = "user_id 为空"
		case userID == nodeAgentGenericUserID:
			key = nodeAgentGenericUserID
		default:
			kind, id, found := strings.Cut(userID, ":")
			if !found || (kind != "subuser" && kind != "merged") || id == "" {
				why = "user_id 格式不对"
			} else if kind == "subuser" && findSubUserByID(s.cfg.SubUsers, id) == nil && !s.hasCentralUser("subuser", id) {
				why = "订阅用户不存在"
			} else if kind == "merged" && findMergedSubByID(s.cfg.MergedSubs, id) == nil && !s.hasCentralUser("merged", id) {
				why = "合并订阅不存在"
			} else {
				key = userID
			}
		}
		if why == "" && (e.PeriodID < 0 || e.Seq < 0 || e.UpBytes < 0 || e.DownBytes < 0) {
			why = "period_id/seq/up_bytes/down_bytes 不能为负"
		}
		if why != "" {
			rejected = append(rejected, map[string]any{"user_id": userID, "reason": why})
			continue
		}
		if s.nodePushSeen.checkAndRecord(userID, e.PeriodID, e.Seq) {
			duplicates++
			continue
		}
		// 流量倍率（v1.3.40）：按人条目按推送节点（node_id=落地地址）
		// 的倍率折算后记账；generic 是节点总量（落地卡片用），不乘。
		// 去重在乘之前，重推不会重复乘。
		up, down := e.UpBytes, e.DownBytes
		if key != nodeAgentGenericUserID {
			if m := s.landingMultiplier(nodeID); m != 1 {
				up = int64(math.Round(float64(up) * m))
				down = int64(math.Round(float64(down) * m))
			}
		}
		s.userTraffic.addAgentDelta(key, up, down, e.PeriodID)
		accepted++
	}

	// 落地 agent 状态：有 node_id 就更新（空 entries 的心跳也更新）。
	if nodeID != "" {
		ext := s.state.ext(nodeID)
		ext.MeteringSource = "agent"
		if agentVersion != "" {
			ext.AgentVersion = agentVersion
		}
		ext.AgentLastPushAt = time.Now().UTC().Format(time.RFC3339)
		_ = s.saveState()
	}
	if rejected == nil {
		rejected = []map[string]any{}
	}
	writeJSON(w, 200, okResp(map[string]any{
		"node_id": nodeID, "panel_version": Version,
		"accepted": accepted, "duplicates": duplicates, "rejected": rejected,
		"metering_source": "agent",
		"agent_status":    s.nodeAgentStatusPayload(nodeID),
	}))
}
