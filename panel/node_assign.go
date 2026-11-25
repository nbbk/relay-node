package main

// ---------- 用户指派四级模型（v1.4.4，切片 4） ----------
//
// 四级（由粗到细）：全部／组／节点／入站。默认全部。
//
//
// - 级别与目标存在用户记录上：SubUser／MergedSub 的 BackendScope
// 与 BackendTargets（config.json，随用户走，备份／恢复全量跟走）。
// Scope 是 "" 或 "all" = 全部；"group" = 跟所在分组走；"node" =
// 只上指定节点；"inbound" = 只上指定入站。Targets 的含义随 scope
// 变：group 时是分组 id（一般就一个，即用户自己的 GroupID）；
// node 时是节点 id（十进制字符串，如 "3"）；inbound 时是
// "节点id:入站tag"（如 "3:reality-443"）。空 scope 与空 targets
// 都按全部算（nil／空＝全部，保持现有行为；老记录没有这两个字
// 段，读出来就是空，正好是全部）。
// - 组级指派的节点清单存在分组上：SubGroup.BackendNodeIDs（该组用
// 的后端节点 id 列表，分组编辑页里勾；空 = 该组没配后端节点，组
// 级指派下成员上不了任何后端节点）。
// - 节点侧的成员门（BackendNode.UserIDs）不动：nil = 全部用户，
// 显式列表 = 只发这些，[] = 一个不发（1.4.2 语义，PUT
// /api/v1/nodes/{id}/users 照旧能改）。四级模型是“用户侧的再
// 一道门”，不是替代它。
//
//
// - 节点 N 上的有效用户 = 同时过两道门的人：nodeHasUser(N, key)
// （节点侧门）AND s.backendAssignAllows(scope, targets, groupID, N)
// （用户侧四级门）。nodeBackendUsersForNode 里两道门都算。
// - users[].inbounds 的来源：backendAssignInbounds(...)。scope
// 是 all／group／node 时返回空（= 全部入站）；scope 是 inbound
// 时返回该节点上 targets 里指定的 tag 列表（去重、按节点入站顺
// 序排）。空 = 全部入站，老 agent 不认这个字段时直接忽略、即全
// 入站，保持兼容；新 agent（0.1.4 起）按它渲染用户列表。
// - “发到节点”弹窗保存时两边一起写：先按旧逻辑调节点侧门的 PUT
// （勾上的节点把人加进 UserIDs，不勾的拿掉；nil 节点不用动），
// 再 PUT /api/v1/users/{key}/backend-assignment 写 scope 与
// targets。只改一边会两边对不上，弹窗里保存失败时以报错为准、
// 不静默。
// - 任何指派变化都 bump 受影响节点的 ConfigVersion（改前与改后
// 的用户集合并集里的节点都 bump），agent 60 秒内拉到新用户列表。
//
//
// - 用户 scope=group 时：targets 里第一个分组 id 必须等于用户自己
// 的 GroupID（不等于 = 该用户不属于这个组，解析为不上任何节点，
// 保存时直接 400）；节点取该分组的 BackendNodeIDs。组里没配节
// 点 = 上不了节点，弹窗里会提示“分组还没配后端节点”。

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// backendAssignScopeValid scope 合法吗（"" 与 "all" 都是全部）。
func backendAssignScopeValid(scope string) bool {
	switch strings.TrimSpace(scope) {
	case "", "all", "group", "node", "inbound":
		return true
	default:
		return false
	}
}

// backendAssignAllows 用户侧四级门：该用户在节点 node 上吗。
// groupID 是用户自己的 GroupID（SubUser／MergedSub.GroupID）。
// 读 s.cfg.SubGroups（组级要分组的 BackendNodeIDs），调用方须持有
// opMu（与 nodeBackendUsersForNode 同一调用链）。
func (s *Server) backendAssignAllows(scope string, targets []string, groupID string, node *BackendNode) bool {
	switch strings.TrimSpace(scope) {
	case "", "all":
		return true
	case "group":
		if groupID == "" || len(targets) == 0 || strings.TrimSpace(targets[0]) != groupID {
			return false
		}
		g := s.findSubGroup(groupID)
		if g == nil {
			return false
		}
		for _, id := range g.BackendNodeIDs {
			if id == node.ID {
				return true
			}
		}
		return false
	case "node":
		for _, t := range targets {
			if id, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil && id == node.ID {
				return true
			}
		}
		return false
	case "inbound":
		for _, t := range targets {
			if nid, _, ok := parseAssignInboundTarget(t); ok && nid == node.ID {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// parseAssignInboundTarget 解析一条 inbound 级 target "节点id:入站tag"。
func parseAssignInboundTarget(t string) (nodeID int64, tag string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(t), ":", 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	id, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || id <= 0 || strings.TrimSpace(parts[1]) == "" {
		return 0, "", false
	}
	return id, strings.TrimSpace(parts[1]), true
}

// backendAssignInbounds 该用户在节点 node 上的入站 tag 列表（写进
// 配置文档 users[].inbounds）。非 inbound 级返回 nil（= 全部入站）。
// 只返回节点上真实存在的入站 tag（已删的 tag 不下发）。
func backendAssignInbounds(scope string, targets []string, node *BackendNode) []string {
	if strings.TrimSpace(scope) != "inbound" {
		return nil
	}
	exist := map[string]bool{}
	order := []string{}
	for _, inb := range node.Inbounds {
		if inb.InboundTag == "" || exist[inb.InboundTag] {
			continue
		}
		exist[inb.InboundTag] = true
		order = append(order, inb.InboundTag)
	}
	want := map[string]bool{}
	for _, t := range targets {
		if nid, tag, ok := parseAssignInboundTarget(t); ok && nid == node.ID {
			want[tag] = true
		}
	}
	var out []string
	for _, tag := range order {
		if want[tag] {
			out = append(out, tag)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// backendAssignNodesForUser 该用户四级指派解析出的节点 id 集合（改前／
// 改后比对、bump 用）。all 级 = 全部节点（节点侧门不过滤，这里只算
// 用户侧）；group 级 = 分组的 BackendNodeIDs；node／inbound 级 =
// targets 里的节点 id。调用方须持有 opMu（读分组）。
func (s *Server) backendAssignNodesForUser(scope string, targets []string, groupID string) map[int64]bool {
	out := map[int64]bool{}
	switch strings.TrimSpace(scope) {
	case "", "all":
		s.nodes.mu.Lock()
		for _, n := range s.nodes.data.Nodes {
			out[n.ID] = true
		}
		s.nodes.mu.Unlock()
	case "group":
		if groupID != "" && len(targets) > 0 && strings.TrimSpace(targets[0]) == groupID {
			if g := s.findSubGroup(groupID); g != nil {
				for _, id := range g.BackendNodeIDs {
					out[id] = true
				}
			}
		}
	case "node":
		for _, t := range targets {
			if id, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil && id > 0 {
				out[id] = true
			}
		}
	case "inbound":
		for _, t := range targets {
			if nid, _, ok := parseAssignInboundTarget(t); ok {
				out[nid] = true
			}
		}
	}
	return out
}

// validateBackendAssignment 校验一套 scope＋targets（不写盘）：
// group 级 targets 只能是用户自己的 GroupID 且分组存在；node 级
// targets 必须是已存在的节点 id；inbound 级必须是已存在的节点上已
// 存在的入站 tag。返回规范化后的 scope（"" 归一成 "all"）与 targets。
// 空 targets 的 node/inbound 级（v1.4.27，前端新建/编辑弹窗的约定）：
// 表示该用户先不上任何后端节点——新建/编辑弹窗里不勾具体节点与入站，
// 具体指派仍在「发到节点」里勾，勾了之后走 PUT 指派接口写 targets；
// 空 targets 时 backendAssignNodesForUser 返回空集、backendAssignAllows
// 处处返回 false，与“不上任何节点”一致，不是“全部”。
func (s *Server) validateBackendAssignment(scope string, targets []string, groupID string) (string, []string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "all"
	}
	if !backendAssignScopeValid(scope) {
		return "", nil, fmt.Errorf("指派级别只支持 全部/组/节点/入站")
	}
	// 去重＋去空串，保留原顺序。
	seen := map[string]bool{}
	norm := []string{}
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		norm = append(norm, t)
	}
	switch scope {
	case "all":
		return scope, nil, nil
	case "group":
		if groupID == "" {
			return "", nil, fmt.Errorf("组级指派要先给这个用户分组")
		}
		// targets 为空 = 跟自己的分组走（新建/编辑弹窗只发 scope
		// 不发 targets 时用，见 v1.4.27 创建语义），自动补上。
		if len(norm) == 0 {
			norm = []string{groupID}
		}
		if len(norm) != 1 || norm[0] != groupID {
			return "", nil, fmt.Errorf("组级指派只能选用户自己的分组")
		}
		if s.findSubGroup(groupID) == nil {
			return "", nil, fmt.Errorf("没有这个分组")
		}
		return scope, norm, nil
	case "node":
		// 空 targets = 先不上任何后端节点（见函数头注释），不报错；
		// 给了 targets 才逐个校验节点存在。
		for _, t := range norm {
			id, err := strconv.ParseInt(t, 10, 64)
			if err != nil || id <= 0 {
				return "", nil, fmt.Errorf("节点 id 不对：%s", t)
			}
			s.nodes.mu.Lock()
			exists := s.nodes.getNode(id) != nil
			s.nodes.mu.Unlock()
			if !exists {
				return "", nil, fmt.Errorf("没有这个节点：%s", t)
			}
		}
		return scope, norm, nil
	case "inbound":
		// 空 targets = 先不上任何后端节点（见函数头注释），不报错；
		// 给了 targets 才逐个校验节点与入站存在。
		for _, t := range norm {
			nid, tag, ok := parseAssignInboundTarget(t)
			if !ok {
				return "", nil, fmt.Errorf("入站指派格式不对：%s（应为 节点id:入站tag）", t)
			}
			s.nodes.mu.Lock()
			node := s.nodes.getNode(nid)
			s.nodes.mu.Unlock()
			if node == nil {
				return "", nil, fmt.Errorf("没有这个节点：%d", nid)
			}
			found := false
			for _, inb := range node.Inbounds {
				if inb.InboundTag == tag {
					found = true
					break
				}
			}
			if !found {
				return "", nil, fmt.Errorf("节点 %d 上没有入站 %s", nid, tag)
			}
		}
		return scope, norm, nil
	}
	return scope, norm, nil
}

// handleBackendAssignmentGet GET /api/v1/users/{user_id}/backend-assignment：
// 该用户的四级指派（scope＋targets＋所在分组的后端节点）。
func (s *Server) handleBackendAssignmentGet(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("user_id")
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	key, ok := s.parseRotateUserKey(raw)
	if !ok {
		writeJSON(w, 404, errResp("没有这个用户"))
		return
	}
	kind, id, _, _, _, _ := s.findBackendUser(key)
	var scope string
	var targets []string
	var groupID string
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			scope, targets, groupID = u.BackendScope, u.BackendTargets, u.GroupID
		}
	} else {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			scope, targets, groupID = ms.BackendScope, ms.BackendTargets, ms.GroupID
		}
	}
	if strings.TrimSpace(scope) == "" {
		scope = "all"
	}
	var groupNodes []int64
	var groupName string
	if groupID != "" {
		if g := s.findSubGroup(groupID); g != nil {
			groupNodes = g.BackendNodeIDs
			groupName = g.Name
		}
	}
	if groupNodes == nil {
		groupNodes = []int64{}
	}
	if targets == nil {
		targets = []string{}
	}
	writeJSON(w, 200, okResp(map[string]any{
		"user_id": key, "scope": scope, "targets": targets,
		"group_id": groupID, "group_name": groupName, "group_nodes": groupNodes,
	}))
}

// applyBackendAssignment 一套四级指派的写逻辑（v1.4.27，创建用户与
// PUT 指派接口共用，不复制粘贴两份）：校验 scope＋targets → 写用户
// 记录上的 BackendScope/BackendTargets → 同步节点侧 UserIDs（指派
// 到的节点：UserIDs 非 nil 时把用户键加进去；没指派到的节点：
// UserIDs 非 nil 时把用户键拿掉；UserIDs 为 nil 的节点不动——
// nil = 全部用户，用户本来就在上面）→ 改前改后并集里的节点与
// UserIDs 有变动的节点都 bump ConfigVersion，agent 60 秒内拉到
// 新列表。调用方负责 saveConfig 落盘（用户记录的改动）；节点侧
// 这里自己 saveLocked。返回指派到的节点 id（排序后）。
// kind 是 "subuser"/"merged"，groupID 是用户自己的 GroupID。
func (s *Server) applyBackendAssignment(kind, id, scope string, targets []string, groupID string) ([]int64, error) {
	scope, targets, err := s.validateBackendAssignment(scope, targets, groupID)
	if err != nil {
		return nil, err
	}
	var oldScope string
	var oldTargets []string
	var recOK bool
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			oldScope, oldTargets = u.BackendScope, u.BackendTargets
			u.BackendScope, u.BackendTargets = scope, targets
			recOK = true
		}
	} else if kind == "merged" {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			oldScope, oldTargets = ms.BackendScope, ms.BackendTargets
			ms.BackendScope, ms.BackendTargets = scope, targets
			recOK = true
		}
	}
	if !recOK {
		return nil, fmt.Errorf("没有这个用户")
	}
	before := s.backendAssignNodesForUser(oldScope, oldTargets, groupID)
	after := s.backendAssignNodesForUser(scope, targets, groupID)
	key := userSubKey(kind, id)
	s.nodes.mu.Lock()
	changed := false
	for _, node := range s.nodes.data.Nodes {
		inAfter := after[node.ID]
		// 节点侧 UserIDs 同步（nil = 全部用户，不动）。
		if node.UserIDs != nil {
			has := false
			for _, k := range node.UserIDs {
				if k == key {
					has = true
					break
				}
			}
			if inAfter && !has {
				node.UserIDs = append(node.UserIDs, key)
				changed = true
			} else if !inAfter && has {
				kept := node.UserIDs[:0]
				for _, k := range node.UserIDs {
					if k != key {
						kept = append(kept, k)
					}
				}
				node.UserIDs = kept
				changed = true
			}
		}
		if before[node.ID] || after[node.ID] {
			node.ConfigVersion++
			changed = true
		}
	}
	if changed {
		_ = s.nodes.saveLocked()
	}
	s.nodes.mu.Unlock()
	ids := make([]int64, 0, len(after))
	for nid := range after {
		ids = append(ids, nid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// handleBackendAssignmentUpdate PUT /api/v1/users/{user_id}/backend-assignment：
// 全量替换该用户的四级指派。写逻辑走 applyBackendAssignment（与创建
// 用户时同一份：用户记录＋节点 UserIDs 两边写，改前／改后节点集合并集
// 里的节点都 bump ConfigVersion，agent 60 秒内拉到新用户列表）。
func (s *Server) handleBackendAssignmentUpdate(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("user_id")
	var req struct {
		Scope   string   `json:"scope"`
		Targets []string `json:"targets"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	key, ok := s.parseRotateUserKey(raw)
	if !ok {
		writeJSON(w, 404, errResp("没有这个用户"))
		return
	}
	kind, id, name, _, _, _ := s.findBackendUser(key)
	var groupID string
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			groupID = u.GroupID
		}
	} else {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			groupID = ms.GroupID
		}
	}
	ids, err := s.applyBackendAssignment(kind, id, req.Scope, req.Targets, groupID)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	// 回包用规范化后的 scope/targets（"" 已归一成 "all"，见
	// validateBackendAssignment）。
	respScope, respTargets := req.Scope, req.Targets
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			respScope, respTargets = u.BackendScope, u.BackendTargets
		}
	} else {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			respScope, respTargets = ms.BackendScope, ms.BackendTargets
		}
	}
	s.audit(s.currentUser(r), "改用户后端指派", fmt.Sprintf("%s %s 指派 %s", key, name, respScope), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"user_id": key, "scope": respScope, "targets": respTargets, "nodes": ids,
	}))
}
