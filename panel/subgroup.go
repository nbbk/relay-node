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
// 成员归属的唯一事实来源是 SubUser/MergedSub 上的 GroupID（单字段，
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

// groupMember 分组里的一名成员（列表接口用）：Kind 是 "user"（订阅用户）
// 或 "merged"（合并订阅），Name 是显示名（合并订阅没填名时用勾选摘要）。
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
	for _, m := range s.cfg.MergedSubs {
		if m.GroupID == id {
			name := m.Name
			if name == "" {
				name = s.selectionSummary(m.Selection)
			}
			out = append(out, groupMember{Kind: "merged", ID: m.ID, Name: name})
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

// effectiveSelectionForMerged 一条合并订阅的生效落地集合（与订阅用户同理）。
func (s *Server) effectiveSelectionForMerged(m *MergedSub) []SubSelection {
	if m.GroupID != "" {
		if g := s.findSubGroup(m.GroupID); g != nil && len(g.Selection) > 0 {
			return g.Selection
		}
	}
	return m.Selection
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
	for i := range s.cfg.MergedSubs {
		if s.cfg.MergedSubs[i].GroupID == groupID {
			s.cfg.MergedSubs[i].GroupID = ""
		}
	}
	for _, m := range members {
		if m.Kind == "user" {
			if u := findSubUserByID(s.cfg.SubUsers, m.ID); u != nil {
				u.GroupID = groupID
			}
		} else if m.Kind == "merged" {
			if ms := findMergedSubByID(s.cfg.MergedSubs, m.ID); ms != nil {
				ms.GroupID = groupID
			}
		}
	}
}

// validateGroupMembers 校验成员名单：kind 只能是 user/merged，id 必须存在，
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
		} else if m.Kind == "merged" {
			if findMergedSubByID(s.cfg.MergedSubs, m.ID) == nil {
				return nil, &groupError{"没有这条合并订阅：" + m.ID}
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
		} else if m.Kind == "merged" {
			ms := findMergedSubByID(s.cfg.MergedSubs, m.ID)
			if ms == nil || ms.TrafficLimitBytes <= 0 {
				continue
			}
			name := ms.Name
			if name == "" {
				name = s.selectionSummary(ms.Selection)
			}
			if w := s.syncUserForwardsWarning("merged", ms.ID, name, s.effectiveSelectionForMerged(ms), ms.TrafficLimitBytes, ms.TrafficLimitMode, ms.ResetMode, ms.ResetDay, ms.NextResetAt); w != "" {
				warns = append(warns, name+"："+w)
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
		items = append(items, item{ID: g.ID, Name: g.Name, Note: g.Note, CreatedAt: g.CreatedAt, Selection: sel, Summary: s.selectionSummary(sel), Members: s.groupMembers(g.ID), BackendNodeIDs: g.BackendNodeIDs})
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

// handleSubGroupsCreate 新建分组：名字必填，落地至少勾一个（空组没有意义），
// 成员选填（不选就先建个空组，之后在成员的编辑里或分组编辑里加）。
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
	sel, _, err := s.validateMergedSelection(req.Selection)
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
	sel, _, err := s.validateMergedSelection(req.Selection)
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
	for i := range s.cfg.MergedSubs {
		if s.cfg.MergedSubs[i].GroupID == id {
			s.cfg.MergedSubs[i].GroupID = ""
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
