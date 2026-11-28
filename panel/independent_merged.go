package main

// ---------- 独立合并订阅（v1.4.34，第 4 批，设计文档 §5 全套） ----------
//
// 与 SubUser 用户订阅链接完全不关联、不混合的另一套订阅体系：不登录、
// 不绑用户、不挂档位、不进分组（方案 A），只是一份“勾选了哪些落地”的
// 订阅内容。入口在集中管理页第三个大选项卡“独立合并订阅”。
//
// 三点区分（缺一不可）：令牌前缀 mrg_（vs sub_）、链接路径 /m/<token>
// （vs /sub/<token>）、记录 ID 前缀 ims-（vs su- / 旧 ms-）。
// 存储是 Config 里新切片 independent_merged_subs；旧 MergedSubs 切片已在
// v1.4.40 随旧合并订阅彻底移除，不复用、不共存。转发/用量 key 用
// indmerged:<id>。
// 审计单独命名“独立合并订阅”，不要复用“未命名合并订阅”。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// indMergedSubKey 独立合并订阅的转发/用量 key：indmerged:<id>。
func indMergedSubKey(id string) string {
	return userSubKey("indmerged", id)
}

// findIndependentMergedSubByID 按 ID 找一条独立合并订阅。
func findIndependentMergedSubByID(subs []IndependentMergedSub, id string) *IndependentMergedSub {
	for i := range subs {
		if subs[i].ID == id {
			return &subs[i]
		}
	}
	return nil
}

// findIndependentMergedSub 按令牌找一条独立合并订阅（常数时间比较）。
func (s *Server) findIndependentMergedSub(tok string) (*IndependentMergedSub, bool) {
	for i := range s.cfg.IndependentMergedSubs {
		if verifySubToken(tok, s.cfg.IndependentMergedSubs[i].TokenHash) {
			return &s.cfg.IndependentMergedSubs[i], true
		}
	}
	return nil, false
}

// indMergedAuditName 审计用户列用的显示名：Name 优先，空时用勾选摘要，
// 再空时用“未勾选落地的独立合并订阅”（不要复用旧合并订阅的
// “未命名合并订阅”）。
func (s *Server) indMergedAuditName(ims *IndependentMergedSub) string {
	if ims == nil {
		return "独立合并订阅"
	}
	if strings.TrimSpace(ims.Name) != "" {
		return ims.Name
	}
	if sum := s.selectionSummary(ims.Selection); sum != "" {
		return sum
	}
	return "未勾选落地的独立合并订阅"
}

// indMergedURL 拼独立合并订阅链接：对外域名复用订阅对外域名配置，
// 只是路径段固定是 /m/（不走可自定义的 subPath，/m/ 与 /sub/ 是两条
// 路径，改订阅路径只影响 /sub/，不影响 /m/）。
func (s *Server) indMergedURL(r *http.Request, tok string) string {
	if d := strings.TrimSpace(s.cfg.SubPublicDomain); d != "" {
		return "https://" + d + "/m/" + tok
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/m/" + tok
}

// effectiveSelectionForIndMerged 独立合并订阅的生效勾选：就是自己的
// Selection（不进分组，没有分组覆盖）；空集合 = 还没勾落地。
func (s *Server) effectiveSelectionForIndMerged(ims *IndependentMergedSub) []SubSelection {
	if ims == nil {
		return nil
	}
	return ims.Selection
}

// ensureNodeUUIDsForIndMerged 按生效勾选给一条独立合并订阅补齐每人 UUID。
func (s *Server) ensureNodeUUIDsForIndMerged(ims *IndependentMergedSub) bool {
	sel := s.effectiveSelectionForIndMerged(ims)
	addrs := s.landingAddrsForSelection(sel)
	// v1.4.41：勾选里的后端节点也要补 UUID（本机节点＋远程节点的本地
	// 同胎；无同胎的远程节点不补——远程面板不认这个订阅的用户）。
	addrs = append(addrs, s.backendNodeAddrsForSelection(sel)...)
	uuids, changed := ensureNodeUUIDs(ims.NodeUUIDs, addrs)
	ims.NodeUUIDs = uuids
	return changed
}

// equalInt64Slice 两份已排序的节点 id 列表是否相同（指派同步比对用）。
func equalInt64Slice(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// validateIndMergedNodeEntries 校验独立合并订阅勾选里的后端节点条目：
// 本机节点要存在；远程节点要快照里有，且远程面板版本 ≥1.4.19（节点
// 详情接口是 1.4.19 切片 9 加的，保存与取订阅时要靠它拿地址与入站）。
func (s *Server) validateIndMergedNodeEntries(nodes []SubSelection) error {
	for _, e := range nodes {
		if e.Panel == "local" {
			s.nodes.mu.Lock()
			n := s.nodes.getNode(e.Node)
			s.nodes.mu.Unlock()
			if n == nil {
				return fmt.Errorf("本面板没有编号 %d 的后端节点", e.Node)
			}
			continue
		}
		rem := s.remoteByID(e.Panel)
		if rem == nil {
			return fmt.Errorf("没有登记过这台远程面板")
		}
		snap, err := s.fetchRemoteStatus(*rem)
		if err != nil {
			_, msg := remoteSideOf(err)
			return fmt.Errorf("远程面板 %s 连不上：%s", rem.Name, msg)
		}
		if snap.Version != "" && isNewerVersion("1.4.19", snap.Version) {
			return fmt.Errorf("远程面板 %s 版本太旧（%s，需要 1.4.19+），请先升级远程面板", rem.Name, snap.Version)
		}
		found := false
		for _, n := range snap.Nodes {
			if n.NodeID == e.Node {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("远程面板 %s 没有编号 %d 的后端节点", rem.Name, e.Node)
		}
	}
	return nil
}

// validateIndMergedSelection 校验独立合并订阅的勾选集合：复用
// validateMergedSelection 的逐项校验逻辑（面板存在、落地编号存在、
// 远程面板版本），但允许空集合——先建空订阅、稍后勾落地。v1.4.41：
// 落地条目与后端节点条目分开校验（节点条目走 validateIndMergedNodeEntries）。
func (s *Server) validateIndMergedSelection(sel []SubSelection) ([]SubSelection, string, error) {
	sel, key := canonicalSelection(sel)
	if len(sel) == 0 {
		return []SubSelection{}, "", nil
	}
	var landings, nodes []SubSelection
	for _, e := range sel {
		if e.Node > 0 {
			nodes = append(nodes, e)
		} else {
			landings = append(landings, e)
		}
	}
	if len(landings) > 0 {
		if _, _, err := s.validateMergedSelectionEntries(landings, ""); err != nil {
			return nil, "", err
		}
	}
	if len(nodes) > 0 {
		if err := s.validateIndMergedNodeEntries(nodes); err != nil {
			return nil, "", err
		}
	}
	return sel, key, nil
}

// handleIndMergedSub 公开订阅端点：GET /m/{token}。分发顺序与 handleSub
// 一致：退场检查（旧切片）→ 新实体查找 → 404。mrg_ 令牌永远命中不了
// 旧表（旧令牌全是 sub_ 前缀），退场检查只是兜底；SubUser 令牌不会
// 走到这里（路径段不同）。到期后 410“订阅已过期”（与退场 410 不同文案）。
// 空订阅返回空内容，不 410、不报错。
func (s *Server) handleIndMergedSub(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	ip := clientIP(r)
	if s.isRetiredSubToken(tok) {
		s.subFailed(ip)
		s.audit(s.retiredSubAuditName(tok), "拉取订阅", ip, "失败", "订阅已退场")
		http.Error(w, retiredSubMsg, http.StatusGone)
		return
	}
	ims, ok := s.findIndependentMergedSub(tok)
	if !ok {
		if s.subLocked(ip) {
			s.audit("订阅客户端", "拉取订阅", ip, "失败", "请求太频繁")
			w.Header().Set("Retry-After", "600")
			http.Error(w, "请求太频繁，稍后再试", 429)
			return
		}
		s.subFailed(ip)
		s.audit("订阅客户端", "拉取订阅", ip, "失败", "令牌不对（令牌哈希前 8 位 "+hashSubToken(tok)[:8]+"）")
		http.NotFound(w, r)
		return
	}
	if exp := ims.ExpiresAt; exp != "" && subExpired(exp, time.Now()) {
		s.subFailed(ip)
		s.audit(s.indMergedAuditName(ims), "拉取订阅", ip, "失败", "订阅已过期")
		http.Error(w, "订阅已过期", http.StatusGone)
		return
	}
	s.subOK(ip)
	s.audit(s.indMergedAuditName(ims), "拉取订阅", ip, "成功", "令牌哈希前 8 位 "+hashSubToken(tok)[:8])
	s.serveSubContent(w, r, tok)
}

// ---------- 管理接口（会话认证） ----------

// handleIndMergedSubLink GET /api/v1/subscriptions/independent-merged/link?id=…
// 取回一条独立合并订阅的链接：复用 handleSubLink 的 kind=indmerged 分支
// （解密 TokenEnc 重新拼 /m/ 链接）。
func (s *Server) handleIndMergedSubLink(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	q.Set("kind", "indmerged")
	r.URL.RawQuery = q.Encode()
	s.handleSubLink(w, r)
}

// handleIndMergedSubsList 列出独立合并订阅（不含令牌与链接；has_link
// 表示有没有密文）。一行一个订阅：名字、勾选摘要、内容模式、到期、
// 用量、停用态、转发数；空订阅摘要写“尚未勾选落地或后端节点”。
func (s *Server) handleIndMergedSubsList(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID             string         `json:"id"`
		Name           string         `json:"name"`
		CreatedAt      string         `json:"created_at"`
		Selection      []SubSelection `json:"selection"`
		Summary        string         `json:"summary"`
		Empty          bool           `json:"empty"`
		HasLink        bool           `json:"has_link"`
		Mode           string         `json:"mode"`
		ExpiresAt      string         `json:"expires_at,omitempty"`
		LimitMB        int64          `json:"traffic_limit_mb"`
		LimitMode      string         `json:"traffic_limit_mode,omitempty"`
		ResetMode      string         `json:"reset_mode,omitempty"`
		ResetDay       int            `json:"reset_day,omitempty"`
		NextResetAt    string         `json:"next_reset_at,omitempty"`
		Used           int64          `json:"traffic_used"`
		HasUsed        bool           `json:"has_used"`
		Disabled       bool           `json:"traffic_disabled"`
		DisabledReason string         `json:"disabled_reason,omitempty"`
		ForwardCount   int            `json:"forward_count"`
		TrafficWarn    string         `json:"traffic_warn,omitempty"`
		LimitWarn      string         `json:"limit_warn,omitempty"`
		BackendScope   string         `json:"backend_scope,omitempty"`
		BackendTargets []string       `json:"backend_targets,omitempty"`
	}
	items := []item{}
	for _, ims := range s.cfg.IndependentMergedSubs {
		if ims.Selection == nil {
			ims.Selection = []SubSelection{}
		}
		summary := s.selectionSummary(ims.Selection)
		if summary == "" {
			summary = "尚未勾选落地或后端节点"
		}
		key := indMergedSubKey(ims.ID)
		used, _, hasUsed := s.userUsageSummary(key, ims.TrafficLimitBytes, ims.TrafficLimitMode)
		limitWarn := ""
		if ims.TrafficLimitBytes > 0 {
			limitWarn = s.userLimitVersionWarn(ims.Selection)
		}
		items = append(items, item{ID: ims.ID, Name: ims.Name, CreatedAt: ims.CreatedAt, Selection: ims.Selection, Summary: summary, Empty: len(ims.Selection) == 0, HasLink: ims.TokenEnc != "", Mode: ims.Mode, ExpiresAt: ims.ExpiresAt, LimitMB: ims.TrafficLimitBytes / 1024 / 1024, LimitMode: ims.TrafficLimitMode, ResetMode: ims.ResetMode, ResetDay: ims.ResetDay, NextResetAt: ims.NextResetAt, Used: used, HasUsed: hasUsed, Disabled: ims.Disabled, DisabledReason: ims.DisabledReason, ForwardCount: len(s.userForwardsFor(key)), TrafficWarn: s.userTrafficWarnFor(key, ims.Selection), LimitWarn: limitWarn, BackendScope: ims.BackendScope, BackendTargets: ims.BackendTargets})
	}
	writeJSON(w, 200, okResp(map[string]any{"subs": items}))
}

// handleIndMergedSubsCreate 新建独立合并订阅：名字选填，Selection 允许
// 空（先建空订阅、稍后再勾落地）；内容模式、到期、限流与 SubUser 创建
// 同款口径。链接在这一次返回，之后随时能点复制重新拿。
func (s *Server) handleIndMergedSubsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string         `json:"name"`
		Selection []SubSelection `json:"selection"`
		Mode      string         `json:"mode"`
		ExpiresAt *string        `json:"expires_at"`
		Timezone  string         `json:"timezone,omitempty"`
		// 流量上限（v1.3.25）：limit_mb 0/不给 = 不限流（走共享端口）。
		TrafficLimitMB   int64  `json:"traffic_limit_mb"`
		TrafficLimitMode string `json:"traffic_limit_mode"`
		ResetMode        string `json:"reset_mode"`
		ResetDay         int    `json:"reset_day"`
		// 后端指派：不给 backend_scope = 全部；显式发 node/inbound 但
		// targets 为空 = 先不上任何后端节点（等在“发到节点”里勾）；
		// 组级指派不可用（独立订阅没有分组）。
		BackendScope   string   `json:"backend_scope"`
		BackendTargets []string `json:"backend_targets"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 64 {
		writeJSON(w, 400, errResp("名字太长，最多 64 个字"))
		return
	}
	sel, _, err := s.validateIndMergedSelection(req.Selection)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	subMode, err := normalizeSubMode(req.Mode)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	nowTime := time.Now().UTC()
	expiresAt := ""
	if req.ExpiresAt != nil {
		expiresAt, err = parseSubExpiryIn(*req.ExpiresAt, req.Timezone)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	limitBytes, limitMode, resetMode, resetDay, nextResetAt, err := normalizeUserLimitFields(mbToBytes(req.TrafficLimitMB), req.TrafficLimitMode, req.ResetMode, req.ResetDay, nowTime, nowTime)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	assignScope, assignTargets := strings.TrimSpace(req.BackendScope), req.BackendTargets
	if assignScope == "" {
		assignScope = "all"
	}
	if _, _, err := s.validateBackendAssignment(assignScope, assignTargets, ""); err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	tok := generateIndMergedToken()
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		writeJSON(w, 500, errResp("令牌加密失败："+err.Error()))
		return
	}
	now := nowTime.Format(time.RFC3339)
	id := "ims-" + randomTokenHex(6)
	s.cfg.IndependentMergedSubs = append(s.cfg.IndependentMergedSubs, IndependentMergedSub{ID: id, Name: name, TokenHash: hashSubToken(tok), TokenEnc: enc, CreatedAt: now, Selection: sel, Mode: subMode, ExpiresAt: expiresAt, TrafficLimitBytes: limitBytes, TrafficLimitMode: limitMode, ResetMode: resetMode, ResetDay: resetDay, NextResetAt: nextResetAt})
	s.ensureNodeUUIDsForIndMerged(&s.cfg.IndependentMergedSubs[len(s.cfg.IndependentMergedSubs)-1])
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	assignNodes, assignErr := s.applyBackendAssignment("indmerged", id, assignScope, assignTargets, "")
	if assignErr != nil {
		writeJSON(w, 500, errResp("后端指派失败："+assignErr.Error()))
		return
	}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	storedScope, storedTargets := assignScope, assignTargets
	if ims := findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, id); ims != nil {
		storedScope, storedTargets = ims.BackendScope, ims.BackendTargets
	}
	resp := map[string]any{"id": id, "url": s.indMergedURL(r, tok), "name": name, "mode": subMode, "expires_at": expiresAt, "traffic_limit_mb": limitBytes / 1024 / 1024, "traffic_limit_mode": limitMode, "reset_mode": resetMode, "reset_day": resetDay, "next_reset_at": nextResetAt, "backend_scope": storedScope, "backend_targets": storedTargets, "backend_nodes": assignNodes}
	warn := s.syncUserForwardsWarning("indmerged", id, name, sel, limitBytes, limitMode, resetMode, resetDay, nextResetAt)
	if w := s.syncCentralUsersWarning(); w != "" {
		if warn != "" {
			warn += "；" + w
		} else {
			warn = w
		}
	}
	if warn != "" {
		resp["warning"] = warn
		s.audit(s.currentUser(r), "新建独立合并订阅", s.indMergedAuditName(findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, id)), "部分成功", warn)
	} else {
		s.audit(s.currentUser(r), "新建独立合并订阅", s.indMergedAuditName(findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, id)), "成功", "链接已加密存好，随时能复制")
	}
	writeJSON(w, 200, okResp(resp))
}

// handleIndMergedSubsRotate 轮换一条独立合并订阅：换一个新令牌，旧链接
// 立即失效；新链接在这一次返回。模式与勾选保持不变。
func (s *Server) handleIndMergedSubsRotate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for i := range s.cfg.IndependentMergedSubs {
		if s.cfg.IndependentMergedSubs[i].ID != id {
			continue
		}
		tok := generateIndMergedToken()
		enc, err := s.encryptSubTokenNow(tok)
		if err != nil {
			writeJSON(w, 500, errResp("令牌加密失败："+err.Error()))
			return
		}
		s.cfg.IndependentMergedSubs[i].TokenHash = hashSubToken(tok)
		s.cfg.IndependentMergedSubs[i].TokenEnc = enc
		if err := s.saveConfig(); err != nil {
			writeJSON(w, 500, errResp("保存失败："+err.Error()))
			return
		}
		s.audit(s.currentUser(r), "轮换独立合并订阅链接", s.indMergedAuditName(&s.cfg.IndependentMergedSubs[i]), "成功", "旧链接已失效")
		writeJSON(w, 200, okResp(map[string]any{"id": id, "url": s.indMergedURL(r, tok), "name": s.cfg.IndependentMergedSubs[i].Name, "mode": s.cfg.IndependentMergedSubs[i].Mode}))
		return
	}
	writeJSON(w, 404, errResp("没有这条独立合并订阅"))
}

// handleIndMergedSubsDelete 删除一条独立合并订阅（链接立即失效）：
// 回收转发（本机＋各远程面板）、清集中用户表，参照按人订阅的回收逻辑。
func (s *Server) handleIndMergedSubsDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for i := range s.cfg.IndependentMergedSubs {
		if s.cfg.IndependentMergedSubs[i].ID == id {
			summary := s.selectionSummary(s.cfg.IndependentMergedSubs[i].Selection)
			sel := s.cfg.IndependentMergedSubs[i].Selection
			name := s.indMergedAuditName(&s.cfg.IndependentMergedSubs[i])
			s.cfg.IndependentMergedSubs = append(s.cfg.IndependentMergedSubs[:i], s.cfg.IndependentMergedSubs[i+1:]...)
			if err := s.saveConfig(); err != nil {
				writeJSON(w, 500, errResp("保存失败："+err.Error()))
				return
			}
			s.opMu.Lock()
			recErr := s.removeUserForwards("indmerged", id)
			s.bumpBackendNodesForUserLocked(indMergedSubKey(id))
			s.opMu.Unlock()
			var warns []string
			if recErr != nil {
				warns = append(warns, "本机转发没撤干净："+recErr.Error())
			}
			warns = append(warns, s.removeUserForwardsFromRemotes(indMergedSubKey(id), sel)...)
			for _, w := range s.syncCentralUsersToRemotes() {
				warns = append(warns, w.String())
			}
			resp := map[string]any{}
			if len(warns) > 0 {
				resp["warning"] = "订阅已删，但" + strings.Join(warns, "；") + "，请检查中转机上的转发。"
				s.audit(s.currentUser(r), "删除独立合并订阅", name+"（"+summary+"）", "部分成功", strings.Join(warns, "；"))
			} else {
				s.audit(s.currentUser(r), "删除独立合并订阅", name+"（"+summary+"）", "成功", "该订阅链接已失效")
			}
			writeJSON(w, 200, okResp(resp))
			return
		}
	}
	writeJSON(w, 404, errResp("没有这条独立合并订阅"))
}

// handleIndMergedSubsUpdate 编辑一条独立合并订阅：改勾选集合/内容模式/
// 名字/到期/限流，令牌不动，链接不变，下次取订阅即按新勾选与新模式出
// 内容。勾选允许空（清空 = 变回空订阅）；没给的字段保持原值。
func (s *Server) handleIndMergedSubsUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name             *string         `json:"name"`
		Selection        *[]SubSelection `json:"selection"`
		Mode             *string         `json:"mode"`
		ExpiresAt        *string         `json:"expires_at"`
		Timezone         string          `json:"timezone,omitempty"`
		TrafficLimitMB   *int64          `json:"traffic_limit_mb"`
		TrafficLimitMode *string         `json:"traffic_limit_mode"`
		ResetMode        *string         `json:"reset_mode"`
		ResetDay         *int            `json:"reset_day"`
		BackendScope     *string         `json:"backend_scope"`
		BackendTargets   []string        `json:"backend_targets"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	nameGiven := false
	name := ""
	if req.Name != nil {
		nameGiven = true
		name = strings.TrimSpace(*req.Name)
		if len(name) > 64 {
			writeJSON(w, 400, errResp("名字太长，最多 64 个字"))
			return
		}
	}
	var sel []SubSelection
	selGiven := false
	if req.Selection != nil {
		selGiven = true
		var err error
		sel, _, err = s.validateIndMergedSelection(*req.Selection)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	subModeGiven := false
	subMode := ""
	if req.Mode != nil {
		subModeGiven = true
		var err error
		subMode, err = normalizeSubMode(*req.Mode)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	expiresGiven := false
	expiresAt := ""
	if req.ExpiresAt != nil {
		expiresGiven = true
		var err error
		expiresAt, err = parseSubExpiryIn(*req.ExpiresAt, req.Timezone)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	for i := range s.cfg.IndependentMergedSubs {
		if s.cfg.IndependentMergedSubs[i].ID != id {
			continue
		}
		ims := &s.cfg.IndependentMergedSubs[i]
		newLimit := ims.TrafficLimitBytes
		newMode, newResetMode, newResetDay := ims.TrafficLimitMode, ims.ResetMode, ims.ResetDay
		if req.TrafficLimitMB != nil {
			newLimit = mbToBytes(*req.TrafficLimitMB)
		}
		if req.TrafficLimitMode != nil {
			newMode = *req.TrafficLimitMode
		}
		if req.ResetMode != nil {
			newResetMode = *req.ResetMode
		}
		if req.ResetDay != nil {
			newResetDay = *req.ResetDay
		}
		anchor, _ := time.Parse(time.RFC3339, ims.CreatedAt)
		limitBytes, limitMode, resetMode, resetDay, nextResetAt, err := normalizeUserLimitFields(newLimit, newMode, newResetMode, newResetDay, anchor, time.Now().UTC())
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
		if req.BackendScope != nil {
			if _, _, verr := s.validateBackendAssignment(*req.BackendScope, req.BackendTargets, ""); verr != nil {
				writeJSON(w, 400, errResp(verr.Error()))
				return
			}
		}
		s.maybeResumeUser(indMergedSubKey(id), limitBytes, limitMode)
		if nameGiven {
			ims.Name = name
		}
		// v1.4.41：先记下改之前的勾选（下面指派同步要比对节点集合变没变）。
		oldSel := ims.Selection
		if selGiven {
			ims.Selection = sel
		}
		if subModeGiven {
			ims.Mode = subMode
		}
		if expiresGiven {
			ims.ExpiresAt = expiresAt
		}
		ims.TrafficLimitBytes = limitBytes
		ims.TrafficLimitMode = limitMode
		ims.ResetMode = resetMode
		ims.ResetDay = resetDay
		ims.NextResetAt = nextResetAt
		// v1.4.41：勾选里的后端节点集合变了时同步指派（本地节点＋远程
		// 节点的本地同胎），让节点 agent 拿到 UUID、/m/ 能出链接；只
		// 动节点勾选时才调，不动落地时原有指派原样保留。
		if selGiven {
			oldIDs := s.indMergedLocalNodeIDs(oldSel)
			newIDs := s.indMergedLocalNodeIDs(sel)
			if !equalInt64Slice(oldIDs, newIDs) {
				targets := make([]string, 0, len(newIDs))
				for _, nid := range newIDs {
					targets = append(targets, strconv.FormatInt(nid, 10))
				}
				if _, aerr := s.applyBackendAssignment("indmerged", id, "node", targets, ""); aerr != nil {
					writeJSON(w, 400, errResp(aerr.Error()))
					return
				}
			}
		}
		s.ensureNodeUUIDsForIndMerged(ims)
		if err := s.saveConfig(); err != nil {
			writeJSON(w, 500, errResp("保存失败："+err.Error()))
			return
		}
		if req.BackendScope != nil {
			if _, aerr := s.applyBackendAssignment("indmerged", id, *req.BackendScope, req.BackendTargets, ""); aerr != nil {
				writeJSON(w, 400, errResp(aerr.Error()))
				return
			}
			if err := s.saveConfig(); err != nil {
				writeJSON(w, 500, errResp("保存失败："+err.Error()))
				return
			}
		}
		effSel := ims.Selection
		summary := s.selectionSummary(effSel)
		if summary == "" {
			summary = "尚未勾选落地或后端节点"
		}
		selOut := ims.Selection
		if selOut == nil {
			selOut = []SubSelection{}
		}
		resp := map[string]any{"id": id, "name": ims.Name, "summary": summary, "mode": ims.Mode, "selection": selOut, "expires_at": ims.ExpiresAt, "traffic_limit_mb": limitBytes / 1024 / 1024, "traffic_limit_mode": limitMode, "reset_mode": resetMode, "reset_day": resetDay, "next_reset_at": nextResetAt, "backend_scope": ims.BackendScope, "backend_targets": ims.BackendTargets}
		warn := s.syncUserForwardsWarning("indmerged", id, ims.Name, effSel, limitBytes, limitMode, resetMode, resetDay, nextResetAt)
		if w := s.syncCentralUsersWarning(); w != "" {
			if warn != "" {
				warn += "；" + w
			} else {
				warn = w
			}
		}
		if warn != "" {
			resp["warning"] = warn
			s.audit(s.currentUser(r), "编辑独立合并订阅", ims.Name+"（"+summary+"）", "部分成功", warn)
		} else {
			s.audit(s.currentUser(r), "编辑独立合并订阅", ims.Name+"（"+summary+"）", "成功", "链接不变，下次更新订阅即按新勾选同步")
		}
		writeJSON(w, 200, okResp(resp))
		return
	}
	writeJSON(w, 404, errResp("没有这条独立合并订阅"))
}

// handleIndMergedSubsResume 手动恢复一条独立合并订阅（超量停用后）。
func (s *Server) handleIndMergedSubsResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	found := false
	name := ""
	for i := range s.cfg.IndependentMergedSubs {
		if s.cfg.IndependentMergedSubs[i].ID == id {
			found = true
			name = s.cfg.IndependentMergedSubs[i].Name
			break
		}
	}
	if !found {
		writeJSON(w, 404, errResp("没有这条独立合并订阅"))
		return
	}
	manual := s.userDisabledReason(indMergedSubKey(id)) == "手动停用"
	if err := s.resumeSub("indmerged", id); err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	detail := "流量已回到上限以下，转发已恢复"
	if manual {
		detail = "手动停用已解除，转发已恢复"
	}
	s.audit(s.currentUser(r), "手动恢复独立合并订阅", name, "成功", detail)
	writeJSON(w, 200, okResp(map[string]any{"id": id, "name": name}))
}

// handleIndMergedSubsDisable 手动停用一条独立合并订阅（v1.4.3）：复用
// disableUserForwards，不另发明停用标志；已停用时幂等返回成功。
func (s *Server) handleIndMergedSubsDisable(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	found := false
	name := ""
	for i := range s.cfg.IndependentMergedSubs {
		if s.cfg.IndependentMergedSubs[i].ID == id {
			found = true
			name = s.cfg.IndependentMergedSubs[i].Name
			break
		}
	}
	if !found {
		writeJSON(w, 404, errResp("没有这条独立合并订阅"))
		return
	}
	if _, err := s.disableUserForwardsManual(indMergedSubKey(id), s.currentUser(r)); err != nil {
		writeJSON(w, 500, errResp("停用已记下，但中转转发重建失败："+err.Error()))
		return
	}
	writeJSON(w, 200, okResp(map[string]any{"id": id, "name": name}))
}
