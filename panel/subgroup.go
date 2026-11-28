package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ---------- 分组（v1.3.26，权限组） ----------
// 一组 = 一组落地 + 一组用户，组里的人用组里的节点。建一次组、加用户就行，
// 不用每人重复勾落地；通用共享节点放一个组，美国/英国/法国等定制用户
// 各放各的组。
//
// 成员归属的唯一事实来源是 SubUser 上的 GroupID（单字段，
// 一个订阅最多属于一个组，不会有两个组的落地集合冲突）；组本身不另存
// 成员列表，groupMembers() 现算。成员自己的 Selection 保留在记录里：
// 进组后订阅按组的落地集合出内容，自己的勾选不用；出组/删组后自动恢复，
// 不用重勾。
// 改组的落地集合后，组内所有成员的订阅下次更新立即同步（订阅内容是实时
// 生成的，链接不变、不用重新导入）。删组不删用户、不删链接、不换令牌。

// findSubGroup 按 ID 找分组，没找到返回 nil。
func (s *Server) findSubGroup(id string) *SubGroup {
	for i := range s.cfg.SubGroups {
		if s.cfg.SubGroups[i].ID == id {
			return &s.cfg.SubGroups[i]
		}
	}
	return nil
}

// groupMember 分组里的一名成员（列表接口用）：Kind 是 "user"（订阅用户），
// Name 是显示名。v1.4.34（方案 A）：独立合并订阅不进分组，"indmerged"
// 不是分组成员 kind——validateGroupMembers 只认 user，传 indmerged 会 400。
// （旧合并订阅已在 v1.4.40 彻底移除，merged 分支已删。）
type groupMember struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// groupMembers 算一个分组的全部成员（现算，不另存）。
func (s *Server) groupMembers(id string) []groupMember {
	out := []groupMember{}
	for _, u := range s.cfg.SubUsers {
		if u.GroupID == id {
			out = append(out, groupMember{Kind: "user", ID: u.ID, Name: u.Name})
		}
	}
	return out
}

// effectiveSelectionForSubUser 一个订阅用户的生效落地集合：进了组就用组的，
// 没进组用自己的勾选（空 = 全部落地）。组被删了、组里没落地时回落自己的勾选。
func (s *Server) effectiveSelectionForSubUser(u *SubUser) []SubSelection {
	if u.GroupID != "" {
		if g := s.findSubGroup(u.GroupID); g != nil && len(g.Selection) > 0 {
			return g.Selection
		}
	}
	return u.Selection
}

// groupNameOf 分组显示名（列表徽章用）：没分组返回 ""。
func (s *Server) groupNameOf(id string) string {
	if id == "" {
		return ""
	}
	if g := s.findSubGroup(id); g != nil {
		return g.Name
	}
	return ""
}

// validateGroupID 校验归属分组：空 = 不分组；非空必须存在。返回规范化后的 ID。
func (s *Server) validateGroupID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	if s.findSubGroup(id) == nil {
		return "", errInvalidGroup
	}
	return id, nil
}

var errInvalidGroup = &groupError{"没有这个分组"}

type groupError struct{ msg string }

func (e *groupError) Error() string { return e.msg }

// applyGroupMembers 把成员名单落到分组上（新建/编辑分组时调）：
// 先清掉原来在这个组里的成员（不在新名单里的出组，恢复自己勾选），
// 再把新名单的成员拉进来——一个人只能属于一个组，单字段 GroupID，
// 改了就从旧组挪过来。调用方须先校验成员存在。
func (s *Server) applyGroupMembers(groupID string, members []groupMember) {
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].GroupID == groupID {
			s.cfg.SubUsers[i].GroupID = ""
		}
	}
	for _, m := range members {
		if m.Kind == "user" {
			if u := findSubUserByID(s.cfg.SubUsers, m.ID); u != nil {
				u.GroupID = groupID
			}
		}
	}
}

// validateGroupMembers 校验成员名单：kind 只能是 user，id 必须存在，
// 重复的去重（保留第一次）。
func (s *Server) validateGroupMembers(members []groupMember) ([]groupMember, error) {
	seen := map[string]bool{}
	out := []groupMember{}
	for _, m := range members {
		key := m.Kind + ":" + m.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		if m.Kind == "user" {
			if findSubUserByID(s.cfg.SubUsers, m.ID) == nil {
				return nil, &groupError{"没有这个订阅用户：" + m.ID}
			}
		} else {
			return nil, &groupError{"成员类型不对：" + m.Kind}
		}
		out = append(out, groupMember{Kind: m.Kind, ID: m.ID})
	}
	return out, nil
}

// reconcileGroupMembersForwards 分组改动后，给受影响的成员重建按人转发
// （v1.3.25）：只给开了限流的成员调（没开限流的走共享端口、不用转发）。
// affected 是受影响的成员（进组/出组/组内落地变了的），按各自当前生效
// 勾选重建。返回警告（空 = 全部建好）。
func (s *Server) reconcileGroupMembersForwards(affected []groupMember) string {
	var warns []string
	for _, m := range affected {
		if m.Kind == "user" {
			u := findSubUserByID(s.cfg.SubUsers, m.ID)
			if u == nil || u.TrafficLimitBytes <= 0 {
				continue
			}
			if w := s.syncUserForwardsWarning("subuser", u.ID, u.Name, s.effectiveSelectionForSubUser(u), u.TrafficLimitBytes, u.TrafficLimitMode, u.ResetMode, u.ResetDay, u.NextResetAt); w != "" {
				warns = append(warns, u.Name+"："+w)
			}
		}
	}
	return strings.Join(warns, "；")
}

// ---------- 分组接口（会话认证） ----------

func (s *Server) handleSubGroupsList(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Note      string         `json:"note,omitempty"`
		CreatedAt string         `json:"created_at"`
		Selection []SubSelection `json:"selection"`
		Summary   string         `json:"summary"`
		Members   []groupMember  `json:"members"`
		// BackendNodeIDs 该组用的后端节点（v1.4.4，组级指派用）。
		BackendNodeIDs []int64 `json:"backend_node_ids,omitempty"`
	}
	items := []item{}
	for _, g := range s.cfg.SubGroups {
		sel := g.Selection
		if sel == nil {
			sel = []SubSelection{}
		}
		// v1.4.32：空组在列表里标注“空组”，不要显示成空白摘要或异常。
		summary := s.selectionSummary(sel)
		if summary == "" {
			summary = "空组"
		}
		items = append(items, item{ID: g.ID, Name: g.Name, Note: g.Note, CreatedAt: g.CreatedAt, Selection: sel, Summary: summary, Members: s.groupMembers(g.ID), BackendNodeIDs: g.BackendNodeIDs})
	}
	writeJSON(w, 200, okResp(map[string]any{"groups": items}))
}

// validateBackendNodeIDs 校验一组后端节点 id（v1.4.4）：去重、去无效
// 值、必须存在。返回规范化后的列表（nil 输入返回 nil）。
func (s *Server) validateBackendNodeIDs(ids []int64) ([]int64, error) {
	if ids == nil {
		return nil, nil
	}
	seen := map[int64]bool{}
	out := []int64{}
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		if s.nodes.getNode(id) == nil {
			return nil, fmt.Errorf("没有这个后端节点：%d", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// validateGroupSelection 分组专用的落地集合校验（v1.4.32，先建后配）：
// 允许空集合（先建空组、稍后在组详情里再勾落地）；非空时走
// validateMergedSelection 的同一套校验（面板存在、落地编号存在，
// 远程面板当场验版本）。注意不要直接放宽 validateMergedSelection：
// 它还被订阅用户的创建共用，那两处仍要求非空时才校验、
// 空集合走各自的默认值逻辑；分组这两处单独放宽。
func (s *Server) validateGroupSelection(sel []SubSelection) ([]SubSelection, string, error) {
	if len(sel) == 0 {
		return []SubSelection{}, "", nil
	}
	return s.validateMergedSelection(sel)
}

// handleSubGroupsCreate 新建分组：名字必填，落地可以先不勾（空组，
// 建好后在组详情里再勾落地、加成员、勾后端节点）；成员选填。
func (s *Server) handleSubGroupsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name           string         `json:"name"`
		Note           string         `json:"note"`
		Selection      []SubSelection `json:"selection"`
		Members        []groupMember  `json:"members"`
		BackendNodeIDs []int64        `json:"backend_node_ids"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, 400, errResp("请填分组名字（例如“美国组”）"))
		return
	}
	if len(name) > 64 {
		writeJSON(w, 400, errResp("分组名字太长，最多 64 个字"))
		return
	}
	note := strings.TrimSpace(req.Note)
	if len(note) > 200 {
		writeJSON(w, 400, errResp("备注太长，最多 200 个字"))
		return
	}
	sel, _, err := s.validateGroupSelection(req.Selection)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	members, err := s.validateGroupMembers(req.Members)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	backendNodes, err := s.validateBackendNodeIDs(req.BackendNodeIDs)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	id := "sg-" + randomTokenHex(6)
	now := time.Now().UTC().Format(time.RFC3339)
	s.cfg.SubGroups = append(s.cfg.SubGroups, SubGroup{ID: id, Name: name, Note: note, Selection: sel, CreatedAt: now, BackendNodeIDs: backendNodes})
	s.applyGroupMembers(id, members)
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	summary := s.selectionSummary(sel)
	resp := map[string]any{"id": id, "name": name, "summary": summary, "selection": sel, "members": members, "backend_node_ids": backendNodes}
	if warn := s.reconcileGroupMembersForwards(members); warn != "" {
		resp["warning"] = warn
		s.audit(s.currentUser(r), "新建分组", name+"（"+summary+"）", "部分成功", warn)
	} else {
		s.audit(s.currentUser(r), "新建分组", name+"（"+summary+"）", "成功", "成员订阅下次更新即按组内落地同步")
	}
	writeJSON(w, 200, okResp(resp))
}

// handleSubGroupsUpdate 编辑分组：改名字/备注/落地集合/成员，全量替换。
// 落地集合变了，组内所有成员的订阅下次更新立即同步（实时生成）；成员出组
// 后恢复自己原来的勾选。开了限流的成员按新生效勾选重建转发。
// v1.4.32：落地集合允许清空（空组），成员进组后仍按自己的勾选出订阅，
// 不会断网（effectiveSelectionForSubUser 空组时回落自己的勾选）。
func (s *Server) handleSubGroupsUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name           string         `json:"name"`
		Note           string         `json:"note"`
		Selection      []SubSelection `json:"selection"`
		Members        []groupMember  `json:"members"`
		BackendNodeIDs []int64        `json:"backend_node_ids"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, 400, errResp("请填分组名字（例如“美国组”）"))
		return
	}
	if len(name) > 64 {
		writeJSON(w, 400, errResp("分组名字太长，最多 64 个字"))
		return
	}
	note := strings.TrimSpace(req.Note)
	if len(note) > 200 {
		writeJSON(w, 400, errResp("备注太长，最多 200 个字"))
		return
	}
	sel, _, err := s.validateGroupSelection(req.Selection)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	members, err := s.validateGroupMembers(req.Members)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	backendNodes, err := s.validateBackendNodeIDs(req.BackendNodeIDs)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	g := s.findSubGroup(id)
	if g == nil {
		writeJSON(w, 404, errResp("没有这个分组"))
		return
	}
	// 受影响的成员 = 原来在组里的 + 现在要进组里的（出组的按自己勾选重建，
	// 进组的按组的新勾选重建），去重后统一调一次。
	affected := map[string]groupMember{}
	for _, m := range s.groupMembers(id) {
		affected[m.Kind+":"+m.ID] = m
	}
	for _, m := range members {
		affected[m.Kind+":"+m.ID] = m
	}
	oldBackendNodes := g.BackendNodeIDs
	g.Name = name
	g.Note = note
	g.Selection = sel
	g.BackendNodeIDs = backendNodes
	s.applyGroupMembers(id, members)
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	// v1.4.4：分组的后端节点变了，组级指派的成员在这些节点上的归属
	// 可能变了，改前改后并集里的节点都 bump，agent 60 秒内拉到新列表。
	s.nodes.mu.Lock()
	bump := map[int64]bool{}
	for _, nid := range oldBackendNodes {
		bump[nid] = true
	}
	for _, nid := range backendNodes {
		bump[nid] = true
	}
	changed := false
	for _, node := range s.nodes.data.Nodes {
		if bump[node.ID] {
			node.ConfigVersion++
			changed = true
		}
	}
	if changed {
		_ = s.nodes.saveLocked()
	}
	s.nodes.mu.Unlock()
	summary := s.selectionSummary(sel)
	affectedList := []groupMember{}
	for _, m := range affected {
		affectedList = append(affectedList, m)
	}
	resp := map[string]any{"id": id, "name": name, "summary": summary, "selection": sel, "members": members, "backend_node_ids": backendNodes}
	if warn := s.reconcileGroupMembersForwards(affectedList); warn != "" {
		resp["warning"] = warn
		s.audit(s.currentUser(r), "编辑分组", name+"（"+summary+"）", "部分成功", warn)
	} else {
		s.audit(s.currentUser(r), "编辑分组", name+"（"+summary+"）", "成功", "成员订阅下次更新即按新勾选同步")
	}
	writeJSON(w, 200, okResp(resp))
}

// handleSubGroupsDelete 删分组：成员自动回到自己原来勾的落地集合（自己的
// Selection 一直保留在记录里，出组即恢复）；不删用户、不删链接、不换令牌。
// 开了限流的成员按自己勾选重建转发。
func (s *Server) handleSubGroupsDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g := s.findSubGroup(id)
	if g == nil {
		writeJSON(w, 404, errResp("没有这个分组"))
		return
	}
	name := g.Name
	summary := s.selectionSummary(g.Selection)
	members := s.groupMembers(id)
	for i := range s.cfg.SubGroups {
		if s.cfg.SubGroups[i].ID == id {
			s.cfg.SubGroups = append(s.cfg.SubGroups[:i], s.cfg.SubGroups[i+1:]...)
			break
		}
	}
	// 成员出组：GroupID 清掉，自己的 Selection 恢复生效。
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].GroupID == id {
			s.cfg.SubUsers[i].GroupID = ""
		}
	}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	resp := map[string]any{}
	if warn := s.reconcileGroupMembersForwards(members); warn != "" {
		resp["warning"] = warn
		s.audit(s.currentUser(r), "删除分组", name+"（"+summary+"）", "部分成功", "成员已恢复自己勾选；"+warn)
	} else {
		s.audit(s.currentUser(r), "删除分组", name+"（"+summary+"）", "成功", "成员已恢复自己原来的勾选，链接不变")
	}
	writeJSON(w, 200, okResp(resp))
}

// ---------- 分组成员增量接口（v1.4.32，先建后配） ----------
// 编辑分组原来是全量替换（先清掉该组全部成员的 GroupID 再写回新名单），
// 漏勾一个人就等于把他踢出组。增量接口一次只动一个人：加人只改那个人
// 的 GroupID，减人只清那个人的 GroupID，组里其他人一律不动。
// 加已在别组的人时后端直接换组（前端负责二次确认，后端只做换组本身）。

// groupMemberDisplayName 成员显示名（审计日志用）：订阅用户用名字。
func (s *Server) groupMemberDisplayName(kind, id string) string {
	if kind == "user" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			return u.Name
		}
	}
	return id
}

// setGroupMemberGroupID 只改一个成员的 GroupID（换组就是直接改这个字段）。
// 成员不存在时返回 false。
func (s *Server) setGroupMemberGroupID(kind, id, groupID string) bool {
	if kind == "user" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			u.GroupID = groupID
			return true
		}
		return false
	}
	return false
}

// memberGroupID 读一个成员当前的 GroupID（不存在返回 ""，ok=false）。
func (s *Server) memberGroupID(kind, id string) (string, bool) {
	if kind == "user" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			return u.GroupID, true
		}
		return "", false
	}
	return "", false
}

// handleSubGroupMembersAdd 往分组里加一个人：POST
// /api/v1/subscriptions/groups/{id}/members，请求体 {kind, id}。
// 已在别组的人直接换组；已在本组的人幂等返回 200。
func (s *Server) handleSubGroupMembersAdd(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g := s.findSubGroup(id)
	if g == nil {
		writeJSON(w, 404, errResp("没有这个分组"))
		return
	}
	var req struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	members, err := s.validateGroupMembers([]groupMember{{Kind: req.Kind, ID: req.ID}})
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	m := members[0]
	oldGroup, _ := s.memberGroupID(m.Kind, m.ID)
	if oldGroup == id {
		writeJSON(w, 200, okResp(map[string]any{"group_id": id, "kind": m.Kind, "id": m.ID, "name": s.groupMemberDisplayName(m.Kind, m.ID), "already_member": true}))
		return
	}
	if !s.setGroupMemberGroupID(m.Kind, m.ID, id) {
		writeJSON(w, 404, errResp("没有这个成员"))
		return
	}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	name := s.groupMemberDisplayName(m.Kind, m.ID)
	resp := map[string]any{"group_id": id, "kind": m.Kind, "id": m.ID, "name": name}
	if warn := s.reconcileGroupMembersForwards([]groupMember{m}); warn != "" {
		resp["warning"] = warn
		s.audit(s.currentUser(r), "分组加成员", g.Name+"："+name, "部分成功", warn)
	} else {
		detail := "成员已加入分组"
		if oldGroup != "" {
			detail = "成员已从分组「" + s.groupNameOf(oldGroup) + "」换到分组「" + g.Name + "」"
		}
		s.audit(s.currentUser(r), "分组加成员", g.Name+"："+name, "成功", detail)
	}
	writeJSON(w, 200, okResp(resp))
}

// handleSubGroupMembersRemove 把一个人移出分组：DELETE
// /api/v1/subscriptions/groups/{id}/members/{kind}/{id}。
// 只清那一个人的 GroupID，组里其他人不动；人不存在、或人不在这个组里
// 都返回 404，不动任何数据。
func (s *Server) handleSubGroupMembersRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	kind := r.PathValue("kind")
	memberID := r.PathValue("memberID")
	g := s.findSubGroup(id)
	if g == nil {
		writeJSON(w, 404, errResp("没有这个分组"))
		return
	}
	if kind != "user" {
		writeJSON(w, 400, errResp("成员类型不对："+kind))
		return
	}
	cur, ok := s.memberGroupID(kind, memberID)
	if !ok {
		writeJSON(w, 404, errResp("没有这个订阅用户："+memberID))
		return
	}
	if cur != id {
		writeJSON(w, 404, errResp("这个人不在这个分组里"))
		return
	}
	name := s.groupMemberDisplayName(kind, memberID)
	if !s.setGroupMemberGroupID(kind, memberID, "") {
		writeJSON(w, 404, errResp("没有这个成员"))
		return
	}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	resp := map[string]any{"group_id": id, "kind": kind, "id": memberID, "name": name}
	if warn := s.reconcileGroupMembersForwards([]groupMember{{Kind: kind, ID: memberID}}); warn != "" {
		resp["warning"] = warn
		s.audit(s.currentUser(r), "分组减成员", g.Name+"："+name, "部分成功", warn)
	} else {
		s.audit(s.currentUser(r), "分组减成员", g.Name+"："+name, "成功", "成员已恢复自己原来的勾选，链接不变")
	}
	writeJSON(w, 200, okResp(resp))
}
