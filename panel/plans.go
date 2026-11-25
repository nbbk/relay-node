package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------- 套餐档位 Plan（v1.4.27，方案 B） ----------
// XBoard 式：档位 = 名字＋权限组＋流量上限＋到期规则（＋内容模式）。
// 订阅用户挂 PlanID，建用户时档位值作默认值，个人值优先覆盖。
// 档位本身不存落地勾选：节点范围只走档位关联的权限组（GroupID）。
// 默认档位：Plans 为空时自动有一条“默认”（无分组、不限流、永不过期、
// both），ID 固定为 pl-default。ensureDefaultPlan() 只在写路径上建
// （计划创建/订阅用户创建入口），持久化到 config；GET 列表不写盘，
// 没有档位时列表接口返回一条合成的默认档位，行为上保证“永远至少有
// 一条可用档位”。

const defaultPlanID = "pl-default"

// defaultPlan 合成的默认档位（不落盘时用；落盘的默认档位也是这一份，
// 只是 CreatedAt 是实际创建时间）。
func defaultPlan() Plan {
	return Plan{ID: defaultPlanID, Name: "默认", Mode: "both"}
}

// findPlan 按 ID 找档位：Plans 为空且要找的正是默认档位时返回合成的
// 默认档位（列表接口的合成项与它对应，建用户时 plan_id 写 pl-default
// 也能找到）；其余情况找不到返回 nil。
func (s *Server) findPlan(id string) *Plan {
	for i := range s.cfg.Plans {
		if s.cfg.Plans[i].ID == id {
			return &s.cfg.Plans[i]
		}
	}
	if id == defaultPlanID && len(s.cfg.Plans) == 0 {
		p := defaultPlan()
		return &p
	}
	return nil
}

// planNameOf 档位显示名：没挂档位返回 ""（列表里 plan_name 为空，
// 登录回包里 plan.name 也为空）。
func (s *Server) planNameOf(id string) string {
	if id == "" {
		return ""
	}
	if p := s.findPlan(id); p != nil {
		return p.Name
	}
	return ""
}

// findSubUserByUsername 按登录账号名精确找订阅用户；没找到返回 nil。
// 用户名不得与管理员账号名相同（创建/编辑时校验，这里不另拦——
// 管理员账号不在 SubUsers 里，撞名时这里本来也找不到）。
func (s *Server) findSubUserByUsername(name string) *SubUser {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].Username == name {
			return &s.cfg.SubUsers[i]
		}
	}
	return nil
}

// ensureDefaultPlan Plans 为空时建一条默认档位并持久化（写路径专用）。
// 已有档位时什么都不做。调用方在写路径入口调（订阅用户创建/编辑、
// 档位创建不调——新建档位本身已保证至少一条）。
func (s *Server) ensureDefaultPlan() error {
	if len(s.cfg.Plans) > 0 {
		return nil
	}
	p := defaultPlan()
	p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	s.cfg.Plans = append(s.cfg.Plans, p)
	return s.saveConfig()
}

// validatePlanFields 校验档位字段（创建/编辑共用）：名字必填、≤24 字、
// 在现有档位里唯一（excludeID 除外）；分组存在；限流三件套合法；
// 内容模式合法；到期天数 ≥0。返回规范化后的值。
func (s *Server) validatePlanFields(name, groupID string, limitMB int64, limitMode, resetMode string, resetDay, expiresDays int, mode, excludeID string) (string, string, int64, string, string, int, int, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", 0, "", "", 0, 0, "", fmt.Errorf("请填档位名字（例如“V1”）")
	}
	if utf8.RuneCountInString(name) > 24 {
		return "", "", 0, "", "", 0, 0, "", fmt.Errorf("档位名字太长，最多 24 个字")
	}
	for _, p := range s.cfg.Plans {
		if p.ID != excludeID && p.Name == name {
			return "", "", 0, "", "", 0, 0, "", fmt.Errorf("已经有同名档位：%s", name)
		}
	}
	groupID, err := s.validateGroupID(groupID)
	if err != nil {
		return "", "", 0, "", "", 0, 0, "", err
	}
	now := time.Now().UTC()
	limitBytes, limitMode, resetMode, resetDay, _, err := normalizeUserLimitFields(mbToBytes(limitMB), limitMode, resetMode, resetDay, now, now)
	if err != nil {
		return "", "", 0, "", "", 0, 0, "", err
	}
	subMode, err := normalizeSubMode(mode)
	if err != nil {
		return "", "", 0, "", "", 0, 0, "", err
	}
	if expiresDays < 0 {
		return "", "", 0, "", "", 0, 0, "", fmt.Errorf("到期天数不能是负数（0 = 永久）")
	}
	return name, groupID, limitBytes, limitMode, resetMode, resetDay, expiresDays, subMode, nil
}

// planListItem 列表里的一项（含 user_count 与 group_name）。
func (s *Server) planListItem(p Plan) map[string]any {
	count := 0
	for _, u := range s.cfg.SubUsers {
		if u.PlanID == p.ID {
			count++
		}
	}
	return map[string]any{
		"id": p.ID, "name": p.Name, "group_id": p.GroupID, "group_name": s.groupNameOf(p.GroupID),
		"traffic_limit_mb": p.TrafficLimitBytes / 1024 / 1024, "traffic_limit_mode": p.TrafficLimitMode,
		"reset_mode": p.ResetMode, "reset_day": p.ResetDay, "expires_days": p.ExpiresDays,
		"mode": p.Mode, "note": p.Note, "created_at": p.CreatedAt, "user_count": count,
	}
}

// handlePlansList GET /api/v1/subscriptions/plans：列表。Plans 为空时
// 返回一条合成的默认档位（不写盘，见文件头注释）。
func (s *Server) handlePlansList(w http.ResponseWriter, r *http.Request) {
	items := []map[string]any{}
	if len(s.cfg.Plans) == 0 {
		items = append(items, s.planListItem(defaultPlan()))
	} else {
		for _, p := range s.cfg.Plans {
			items = append(items, s.planListItem(p))
		}
	}
	writeJSON(w, 200, okResp(map[string]any{"plans": items}))
}

// handlePlansCreate POST /api/v1/subscriptions/plans：创建，name 必填。
func (s *Server) handlePlansCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string `json:"name"`
		GroupID          string `json:"group_id"`
		TrafficLimitMB   int64  `json:"traffic_limit_mb"`
		TrafficLimitMode string `json:"traffic_limit_mode"`
		ResetMode        string `json:"reset_mode"`
		ResetDay         int    `json:"reset_day"`
		ExpiresDays      int    `json:"expires_days"`
		Mode             string `json:"mode"`
		Note             string `json:"note"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	note := strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(note) > 200 {
		writeJSON(w, 400, errResp("备注太长，最多 200 个字"))
		return
	}
	name, groupID, limitBytes, limitMode, resetMode, resetDay, expiresDays, subMode, err := s.validatePlanFields(req.Name, req.GroupID, req.TrafficLimitMB, req.TrafficLimitMode, req.ResetMode, req.ResetDay, req.ExpiresDays, req.Mode, "")
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	id := "pl-" + randomTokenHex(6)
	now := time.Now().UTC().Format(time.RFC3339)
	s.cfg.Plans = append(s.cfg.Plans, Plan{ID: id, Name: name, GroupID: groupID, TrafficLimitBytes: limitBytes, TrafficLimitMode: limitMode, ResetMode: resetMode, ResetDay: resetDay, ExpiresDays: expiresDays, Mode: subMode, Note: note, CreatedAt: now})
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "新建套餐档位", name, "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"id": id, "name": name, "group_id": groupID, "traffic_limit_mb": limitBytes / 1024 / 1024, "traffic_limit_mode": limitMode, "reset_mode": resetMode, "reset_day": resetDay, "expires_days": expiresDays, "mode": subMode}))
}

// handlePlansUpdate PUT /api/v1/subscriptions/plans/{id}：全量替换
// （与分组编辑同款口径，名字必填）。
func (s *Server) handlePlansUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name             string `json:"name"`
		GroupID          string `json:"group_id"`
		TrafficLimitMB   int64  `json:"traffic_limit_mb"`
		TrafficLimitMode string `json:"traffic_limit_mode"`
		ResetMode        string `json:"reset_mode"`
		ResetDay         int    `json:"reset_day"`
		ExpiresDays      int    `json:"expires_days"`
		Mode             string `json:"mode"`
		Note             string `json:"note"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	note := strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(note) > 200 {
		writeJSON(w, 400, errResp("备注太长，最多 200 个字"))
		return
	}
	name, groupID, limitBytes, limitMode, resetMode, resetDay, expiresDays, subMode, err := s.validatePlanFields(req.Name, req.GroupID, req.TrafficLimitMB, req.TrafficLimitMode, req.ResetMode, req.ResetDay, req.ExpiresDays, req.Mode, id)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	// 合成的默认档位（Plans 为空时列表里的那条）不在 config 里，改它
	// 等于没改：先确认这条档位真实存在于 config 里。
	var p *Plan
	for i := range s.cfg.Plans {
		if s.cfg.Plans[i].ID == id {
			p = &s.cfg.Plans[i]
			break
		}
	}
	if p == nil {
		writeJSON(w, 404, errResp("没有这个档位"))
		return
	}
	p.Name = name
	p.GroupID = groupID
	p.TrafficLimitBytes = limitBytes
	p.TrafficLimitMode = limitMode
	p.ResetMode = resetMode
	p.ResetDay = resetDay
	p.ExpiresDays = expiresDays
	p.Mode = subMode
	p.Note = note
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "编辑套餐档位", name, "成功", "已有用户不受影响（档位只作新建/改档位时的默认值）")
	writeJSON(w, 200, okResp(map[string]any{"id": id, "name": name, "group_id": groupID, "traffic_limit_mb": limitBytes / 1024 / 1024, "traffic_limit_mode": limitMode, "reset_mode": resetMode, "reset_day": resetDay, "expires_days": expiresDays, "mode": subMode}))
}

// handlePlansDelete DELETE /api/v1/subscriptions/plans/{id}：有订阅用户
// 在用时 400 拒绝（先把人挪走或改档位再删）。
// “永远至少有一条可用档位”由后端保证（v1.4.27，前端契约）：选的是
// “拒绝删除”而不是“删后重建”——最后一条档位（无论是不是名叫“默认”）
// 删了就没有可用档位了，直接 400，让管理员先新建一条再删。
func (s *Server) handlePlansDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p *Plan
	for i := range s.cfg.Plans {
		if s.cfg.Plans[i].ID == id {
			p = &s.cfg.Plans[i]
			break
		}
	}
	if p == nil {
		writeJSON(w, 404, errResp("没有这个档位"))
		return
	}
	if len(s.cfg.Plans) <= 1 {
		writeJSON(w, 400, errResp("这是最后一条档位，删了就没有可用档位了，先新建一条档位再删"))
		return
	}
	count := 0
	for _, u := range s.cfg.SubUsers {
		if u.PlanID == id {
			count++
		}
	}
	if count > 0 {
		writeJSON(w, 400, errResp(fmt.Sprintf("这个档位还有 %d 个订阅用户在用，先把他们改到别的档位再删", count)))
		return
	}
	name := p.Name
	for i := range s.cfg.Plans {
		if s.cfg.Plans[i].ID == id {
			s.cfg.Plans = append(s.cfg.Plans[:i], s.cfg.Plans[i+1:]...)
			break
		}
	}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "删除套餐档位", name, "成功", "")
	writeJSON(w, 200, okResp(nil))
}
