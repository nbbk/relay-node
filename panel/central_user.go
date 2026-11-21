package main

// ---------- 按订阅限流量 A（v1.3.25）第三阶段：集中管理 ----------
//
// 管理面板（manager_only，本机无落地）上的订阅，限流要落在远程面板
// 上：管理面板把“每人一条转发”下发到各远程面板，远程面板建转发、
// 计量、执法；管理面板汇总各面板的用量、统一裁决（80% 提醒、100%
// 停用、周期到了恢复）。远程面板是其转发记录的唯一事实来源。
//
// 协议（central/*，令牌认证，与现有集中管理同一套 requireCentralToken）：
//   POST /api/v1/central/user-forwards
//     下发一个订阅在本面板上的转发集合（全量替换）：管理面板算好
//     端口与配额发过来，本面板校验（落地存在、端口不撞落地监听
//     端口与其他订阅的转发）后落盘、重建 endpoints、重启 realm。
//     forwards 为空 = 删掉该订阅在本面板上的全部转发。
//   GET  /api/v1/central/user-traffic?key=subuser:su-xxxxxx
//     一个订阅在本面板上的用量与转发状态（管理面板汇总用）。
//   POST /api/v1/central/user-forwards/disable  {key, reason}
//   POST /api/v1/central/user-forwards/resume   {key}
//     管理面板下发的停用/恢复指令（汇总裁决后调）。
//   GET  /api/v1/central/subscription?landings=1,2&for_user=subuser:su-xxxxxx
//     for_user：按该订阅在本面板上的转发改写中转节点链接端口
//     （见 subscription.go handleCentralSubscription）。
//
// 版本门：按人转发要远程面板 1.3.25+。管理面板调之前先查缓存的远程
// 版本（remoteVersionOK），没缓存时放行、靠远程 404 兜底；版本不够
// 时该远程走共享端口、不限流，界面与警告里明确标注。
// 离线：管理面板离线时远程面板按最后同步的配额 + 本地累计继续执法
// （fail-open，转发继续跑、已停用的保持停用）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// centralUserForward 下发的一条转发（与 UserForward 同字段，
// Panel 不用发——收到的就是本面板的）。
type centralUserForward struct {
	LandingAddr string `json:"landing_addr"`
	ListenPort  int    `json:"listen_port"`
	LimitBytes  int64  `json:"limit_bytes"`
	LimitMode   string `json:"limit_mode"`
	ResetMode   string `json:"reset_mode"`
	ResetDay    int    `json:"reset_day"`
	NextResetAt string `json:"next_reset_at"`
	Disabled    bool   `json:"disabled"`
}

// handleCentralUserForwards 被管端：接收管理面板下发的一个订阅在本
// 面板上的转发集合（全量替换）。校验后落盘、重建 endpoints、重启
// realm。调用方须持 opMu（tryOpLock 在这里拿，占用返回 409）。
func (s *Server) handleCentralUserForwards(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key      string               `json:"key"`
		SubName  string               `json:"sub_name"`
		Forwards []centralUserForward `json:"forwards"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	kind, id, found := strings.Cut(req.Key, ":")
	if !found || (kind != "subuser" && kind != "merged") || id == "" {
		writeJSON(w, 400, errResp("订阅标识不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	// 本面板的落地地址表（只接受已存在的落地；删了的落地跳过）。
	rc, _, err := s.readRelayConf()
	if err != nil {
		writeJSON(w, 500, errResp("读落地配置失败："+err.Error()))
		return
	}
	known := map[string]bool{}
	for _, l := range rc.Landings {
		known[l.Landing] = true
	}
	// 已占用端口：落地监听 + 其他订阅的转发（本订阅自己的旧端口
	// 可以复用，替换时先清掉再算）。
	used := s.usedListenPorts()
	for _, f := range s.cfg.UserForwards {
		if f.Key == req.Key {
			delete(used, f.ListenPort)
		}
	}
	var fresh []UserForward
	skipped := 0
	for _, cf := range req.Forwards {
		if !known[cf.LandingAddr] {
			skipped++
			continue
		}
		if cf.ListenPort < 1 || cf.ListenPort > 65535 || used[cf.ListenPort] {
			skipped++
			continue
		}
		limit, mode, rm, day, err := normalizeUserLimit(cf.LimitBytes, cf.LimitMode, cf.ResetMode, cf.ResetDay)
		if err != nil {
			skipped++
			continue
		}
		used[cf.ListenPort] = true
		fresh = append(fresh, UserForward{
			Key: req.Key, SubKind: kind, SubID: id, SubName: req.SubName,
			Panel: "local", LandingAddr: cf.LandingAddr, ListenPort: cf.ListenPort,
			Disabled: cf.Disabled, LimitBytes: limit, LimitMode: mode,
			ResetMode: rm, ResetDay: day, NextResetAt: cf.NextResetAt,
		})
	}
	var kept []UserForward
	for _, f := range s.cfg.UserForwards {
		if f.Key != req.Key {
			kept = append(kept, f)
		}
	}
	s.cfg.UserForwards = append(kept, fresh...)
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	if err := s.applyUserForwards(); err != nil {
		writeJSON(w, 500, errResp("转发没建起来："+err.Error()))
		return
	}
	s.audit("central", "下发按人转发", req.Key, "成功", fmt.Sprintf("应用 %d 条，跳过 %d 条", len(fresh), skipped))
	writeJSON(w, 200, okResp(map[string]any{"applied": len(fresh), "skipped": skipped, "panel_version": Version}))
}

// handleCentralUserTraffic 被管端：一个订阅在本面板上的用量与转发状态
// （管理面板汇总用）。用量是本面板的 user_traffic.json（只含本面板
// 的转发端口），total 是该订阅的上限（取订阅记录，远程上没有订阅
// 记录时取转发上的配额）。
func (s *Server) handleCentralUserTraffic(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	kind, id, found := strings.Cut(key, ":")
	if !found || (kind != "subuser" && kind != "merged") || id == "" {
		writeJSON(w, 400, errResp("订阅标识不对"))
		return
	}
	up, down, upPeriod, downPeriod, has := s.userTraffic.totals(key)
	limit, mode, _, _, _, name, ok := s.userLimitForKey(key)
	var forwards []map[string]any
	disabled := false
	for _, f := range s.userForwardsFor(key) {
		forwards = append(forwards, map[string]any{"landing_addr": f.LandingAddr, "listen_port": f.ListenPort, "disabled": f.Disabled, "limit_bytes": f.LimitBytes, "limit_mode": f.LimitMode})
		if f.Disabled {
			disabled = true
		}
	}
	if forwards == nil {
		forwards = []map[string]any{}
	}
	// 订阅记录上的停用也算停用（管理面板下发的停用会同时写两边）。
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil && u.Disabled {
			disabled = true
		}
	} else if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil && ms.Disabled {
		disabled = true
	}
	writeJSON(w, 200, okResp(map[string]any{
		"key": key, "has_data": has, "up_total": up, "down_total": down,
		"up_period": upPeriod, "down_period": downPeriod,
		"limit_bytes": limit, "limit_mode": mode, "limit_ok": ok,
		"disabled": disabled, "name": name, "forwards": forwards,
		"panel_version": Version,
	}))
}

// handleCentralUserForwardDisable 被管端：管理面板下发的停用指令
// （汇总裁决后调）。只停本面板上的转发，订阅记录只在远程上有
// 才一起写（远程上没有订阅记录时只停转发）。
func (s *Server) handleCentralUserForwardDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key    string `json:"key"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	kind, id, found := strings.Cut(req.Key, ":")
	if !found || (kind != "subuser" && kind != "merged") || id == "" {
		writeJSON(w, 400, errResp("订阅标识不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "traffic"
	}
	s.disableUserForwardsLocked(req.Key, reason)
	s.audit("central", "下发停用订阅", req.Key, "成功", "原因："+reason)
	writeJSON(w, 200, okResp(map[string]any{"panel_version": Version}))
}

// handleCentralUserForwardResume 被管端：管理面板下发的恢复指令。
func (s *Server) handleCentralUserForwardResume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	kind, id, found := strings.Cut(req.Key, ":")
	if !found || (kind != "subuser" && kind != "merged") || id == "" {
		writeJSON(w, 400, errResp("订阅标识不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	if err := s.resumeUserForwardsLocked(req.Key); err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	s.audit("central", "下发恢复订阅", req.Key, "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"panel_version": Version}))
}

// ---------- 管理面板侧：下发与汇总 ----------

// centralUserUsage 管理面板汇总的一条订阅在各面板上的用量（内存缓存，
// 聚合循环每分钟刷新；列表显示用）。
type centralUserUsage struct {
	UpTotal    int64
	DownTotal  int64
	UpPeriod   int64
	DownPeriod int64
	Disabled   bool
	UpdatedAt  time.Time
}

// remoteLandingAddr 把远程落地的编号解析成地址（快照缓存里找）。
// 快照还没同步到时返回 false，调用方警告、跳过该落地。
func (s *Server) remoteLandingAddr(panelID string, index int) (string, bool) {
	s.centralMu.Lock()
	snap := s.centralCache[panelID]
	s.centralMu.Unlock()
	if snap == nil {
		return "", false
	}
	for _, l := range snap.Landings {
		if l.Index == index && l.Landing != "" {
			return l.Landing, true
		}
	}
	return "", false
}

// findRemoteUserForward 取管理面板上存的一条远程转发
// （key + 远程面板 id + 落地地址）。
func (s *Server) findRemoteUserForward(key, panelID, landingAddr string) *UserForward {
	for i := range s.cfg.UserForwards {
		f := &s.cfg.UserForwards[i]
		if f.Key == key && f.Panel == panelID && f.LandingAddr == landingAddr {
			return f
		}
	}
	return nil
}

// allocateUserPortForPanel 给某台远程面板上的转发分配端口：避让本机
// 落地监听端口、面板端口与该远程面板上已分配的用户端口（防同一远程
// 上两个订阅撞端口）。远程的落地端口本面板看不见，远程收到后自己
// 校验、冲突的跳过并上报（见 handleCentralUserForwards）。
func (s *Server) allocateUserPortForPanel(panelID string) (int, error) {
	used := map[int]bool{}
	if rc, _, err := s.readRelayConf(); err == nil && rc != nil {
		for _, l := range rc.Landings {
			for _, p := range l.Ports {
				used[p.Listen] = true
			}
		}
	}
	if s.cfg.Port > 0 {
		used[s.cfg.Port] = true
	}
	for _, f := range s.cfg.UserForwards {
		if f.Panel == panelID {
			used[f.ListenPort] = true
		}
	}
	start, end := s.userPortRange()
	for p := start; p <= end; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("用户端口段 %d–%d 已满（远程面板 %s 上已无空闲端口），请在设置里扩大端口段或删除不用的订阅", start, end, panelID)
}

// reconcileRemoteUserForwards 管理面板侧：算一个订阅在各远程面板上
// 应该有的转发，更新本面板存的记录（Panel=远程面板 id），返回每台
// 远程要下发的转发集合（按远程面板 id 分组）与警告。limit<=0 时清
// 掉该订阅全部远程转发记录，返回各远程的空集（下发空集 = 删远程转发）。
// 只改记录、不调网络，下发由 syncUserForwardsToRemotes 做。
func (s *Server) reconcileRemoteUserForwards(key, subName string, sel []SubSelection, limitBytes int64, limitMode, resetMode string, resetDay int, nextResetAt string) (map[string][]centralUserForward, []string) {
	kind, id, _ := strings.Cut(key, ":")
	payloads := map[string][]centralUserForward{}
	var warns []string
	// 该订阅勾选里的远程落地（按远程面板分组，地址靠快照解析）。
	remoteAddrs := map[string][]string{}
	var remotePanels []string
	seenPanel := map[string]bool{}
	for _, e := range sel {
		if e.Panel == "local" {
			continue
		}
		addr, ok := s.remoteLandingAddr(e.Panel, e.Landing)
		if !ok {
			warns = append(warns, fmt.Sprintf("远程面板 %s 的落地 #%d 还没同步到地址，先在集中管理里点一下刷新", e.Panel, e.Landing))
			continue
		}
		if !seenPanel[e.Panel] {
			seenPanel[e.Panel] = true
			remotePanels = append(remotePanels, e.Panel)
		}
		remoteAddrs[e.Panel] = append(remoteAddrs[e.Panel], addr)
	}
	// 之前存过的远程面板（这次勾选里没了的也要下发空集清掉）。
	for _, f := range s.cfg.UserForwards {
		if f.Key == key && f.Panel != "local" && !seenPanel[f.Panel] {
			seenPanel[f.Panel] = true
			remotePanels = append(remotePanels, f.Panel)
			remoteAddrs[f.Panel] = []string{}
		}
	}
	if limitBytes <= 0 {
		var kept []UserForward
		for _, f := range s.cfg.UserForwards {
			if f.Key == key && f.Panel != "local" {
				continue
			}
			kept = append(kept, f)
		}
		s.cfg.UserForwards = kept
		_ = s.saveConfig()
		for _, pid := range remotePanels {
			payloads[pid] = []centralUserForward{}
		}
		return payloads, warns
	}
	changed := false
	for _, pid := range remotePanels {
		addrs := remoteAddrs[pid]
		want := map[string]bool{}
		for _, a := range addrs {
			want[a] = true
		}
		list := []centralUserForward{}
		for _, addr := range addrs {
			f := s.findRemoteUserForward(key, pid, addr)
			if f == nil {
				port, err := s.allocateUserPortForPanel(pid)
				if err != nil {
					warns = append(warns, err.Error())
					continue
				}
				s.cfg.UserForwards = append(s.cfg.UserForwards, UserForward{
					Key: key, SubKind: kind, SubID: id, SubName: subName,
					Panel: pid, LandingAddr: addr, ListenPort: port,
					LimitBytes: limitBytes, LimitMode: limitMode, ResetMode: resetMode,
					ResetDay: resetDay, NextResetAt: nextResetAt,
				})
				changed = true
				list = append(list, centralUserForward{LandingAddr: addr, ListenPort: port, LimitBytes: limitBytes, LimitMode: limitMode, ResetMode: resetMode, ResetDay: resetDay, NextResetAt: nextResetAt})
				continue
			}
			if f.LimitBytes != limitBytes || f.LimitMode != limitMode || f.ResetMode != resetMode || f.ResetDay != resetDay || f.NextResetAt != nextResetAt {
				f.LimitBytes = limitBytes
				f.LimitMode = limitMode
				f.ResetMode = resetMode
				f.ResetDay = resetDay
				f.NextResetAt = nextResetAt
				changed = true
			}
			list = append(list, centralUserForward{LandingAddr: addr, ListenPort: f.ListenPort, LimitBytes: f.LimitBytes, LimitMode: f.LimitMode, ResetMode: f.ResetMode, ResetDay: f.ResetDay, NextResetAt: f.NextResetAt, Disabled: f.Disabled})
		}
		// 不在勾选里的删掉
		var kept []UserForward
		for _, f := range s.cfg.UserForwards {
			if f.Key == key && f.Panel == pid && !want[f.LandingAddr] {
				changed = true
				continue
			}
			kept = append(kept, f)
		}
		s.cfg.UserForwards = kept
		payloads[pid] = list
	}
	if changed {
		_ = s.saveConfig()
	}
	return payloads, warns
}

// pushUserForwardsToRemote 把一个订阅的转发集合下发到一台远程面板。
// 版本门：缓存的远程版本低于 1.3.25 直接报错；没缓存时放行，远程
// 404 时同样按版本太旧报错（remoteCall 已把 404 翻译成版本文案，
// 这里再补一句）。
func (s *Server) pushUserForwardsToRemote(rem RemotePanel, key, subName string, forwards []centralUserForward) (applied, skipped int, err error) {
	if ok, msg := s.remoteVersionOK(rem.ID, "1.3.25"); !ok {
		return 0, 0, fmt.Errorf("%s", msg)
	}
	if forwards == nil {
		forwards = []centralUserForward{}
	}
	body, _ := json.Marshal(map[string]any{"key": key, "sub_name": subName, "forwards": forwards})
	status, respBody, err := s.remoteCall(rem, "POST", "/api/v1/central/user-forwards", body, 15*time.Second)
	if err != nil {
		if status == 404 {
			return 0, 0, fmt.Errorf("远程面板 %s 版本太旧（需要 1.3.25+），请先升级远程面板", rem.Name)
		}
		return 0, 0, err
	}
	var resp struct {
		OK      bool `json:"ok"`
		Applied int  `json:"applied"`
		Skipped int  `json:"skipped"`
	}
	_ = json.Unmarshal(respBody, &resp)
	return resp.Applied, resp.Skipped, nil
}

// syncUserForwardsToRemotes 管理面板侧：把一个订阅的限流转发同步到
// 勾选里的各远程面板（先算记录、再逐台下发）。返回警告列表
// （空 = 全成功）。下发失败的那台走共享端口、不限流，警告里写明。
func (s *Server) syncUserForwardsToRemotes(key, subName string, sel []SubSelection, limitBytes int64, limitMode, resetMode string, resetDay int, nextResetAt string) []string {
	payloads, warns := s.reconcileRemoteUserForwards(key, subName, sel, limitBytes, limitMode, resetMode, resetDay, nextResetAt)
	// 按勾选顺序下发（map 随机顺序会让警告忽前忽后）。
	var order []string
	seen := map[string]bool{}
	for _, e := range sel {
		if e.Panel == "local" || seen[e.Panel] {
			continue
		}
		seen[e.Panel] = true
		order = append(order, e.Panel)
	}
	for pid := range payloads {
		if !seen[pid] {
			seen[pid] = true
			order = append(order, pid)
		}
	}
	for _, pid := range order {
		rem := s.remoteByID(pid)
		if rem == nil {
			warns = append(warns, "远程面板 "+pid+" 已不在集中管理里，转发没下发")
			continue
		}
		applied, skipped, err := s.pushUserForwardsToRemote(*rem, key, subName, payloads[pid])
		if err != nil {
			warns = append(warns, "远程面板 "+rem.Name+" 下发失败："+err.Error())
			continue
		}
		if skipped > 0 {
			warns = append(warns, fmt.Sprintf("远程面板 %s 有 %d 条转发没应用上（端口冲突或落地已删），已应用 %d 条", rem.Name, skipped, applied))
		}
	}
	return warns
}

// removeUserForwardsFromRemotes 管理面板侧：订阅删除时，把该订阅的
// 转发从各远程面板上撤掉（下发空集）并清掉本面板存的远程转发记录。
// sel 是删除前该订阅的勾选（用来知道往哪几台远程下发空集）。
func (s *Server) removeUserForwardsFromRemotes(key string, sel []SubSelection) []string {
	var warns []string
	seen := map[string]bool{}
	var panels []string
	for _, e := range sel {
		if e.Panel == "local" || seen[e.Panel] {
			continue
		}
		seen[e.Panel] = true
		panels = append(panels, e.Panel)
	}
	for _, f := range s.cfg.UserForwards {
		if f.Key == key && f.Panel != "local" && !seen[f.Panel] {
			seen[f.Panel] = true
			panels = append(panels, f.Panel)
		}
	}
	for _, pid := range panels {
		rem := s.remoteByID(pid)
		if rem == nil {
			continue
		}
		_, _, err := s.pushUserForwardsToRemote(*rem, key, "", []centralUserForward{})
		if err != nil {
			warns = append(warns, "远程面板 "+rem.Name+" 上的转发没撤掉："+err.Error())
		}
	}
	var kept []UserForward
	for _, f := range s.cfg.UserForwards {
		if f.Key == key && f.Panel != "local" {
			continue
		}
		kept = append(kept, f)
	}
	s.cfg.UserForwards = kept
	_ = s.saveConfig()
	return warns
}

// remoteUserTraffic 一台远程面板上一个订阅的用量快照。
type remoteUserTraffic struct {
	UpTotal, DownTotal, UpPeriod, DownPeriod int64
	LimitBytes                               int64
	LimitMode                                string
	Disabled                                 bool
	HasData                                  bool
}

// fetchRemoteUserTraffic 拉一台远程面板上一个订阅的用量。
func (s *Server) fetchRemoteUserTraffic(rem RemotePanel, key string) (remoteUserTraffic, error) {
	var out remoteUserTraffic
	_, body, err := s.remoteCall(rem, "GET", "/api/v1/central/user-traffic?key="+url.QueryEscape(key), nil, 15*time.Second)
	if err != nil {
		return out, err
	}
	var resp struct {
		OK         bool   `json:"ok"`
		UpTotal    int64  `json:"up_total"`
		DownTotal  int64  `json:"down_total"`
		UpPeriod   int64  `json:"up_period"`
		DownPeriod int64  `json:"down_period"`
		LimitBytes int64  `json:"limit_bytes"`
		LimitMode  string `json:"limit_mode"`
		Disabled   bool   `json:"disabled"`
		HasData    bool   `json:"has_data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || !resp.OK {
		return out, fmt.Errorf("远程面板返回的内容解析失败")
	}
	return remoteUserTraffic{UpTotal: resp.UpTotal, DownTotal: resp.DownTotal, UpPeriod: resp.UpPeriod, DownPeriod: resp.DownPeriod, LimitBytes: resp.LimitBytes, LimitMode: resp.LimitMode, Disabled: resp.Disabled, HasData: resp.HasData}, nil
}

// centralUserTarget 聚合循环里一个带远程落地的限流订阅。
type centralUserTarget struct {
	key, kind, id, name, mode string
	limit                      int64
	sel                        []SubSelection
}

// centralUserTargets 收集带远程落地的限流订阅（纯本机订阅走
// enforceUserTrafficLimits，不管这里）。
func (s *Server) centralUserTargets() []centralUserTarget {
	hasRemote := func(sel []SubSelection) bool {
		for _, e := range sel {
			if e.Panel != "local" {
				return true
			}
		}
		return false
	}
	var out []centralUserTarget
	for i := range s.cfg.SubUsers {
		u := &s.cfg.SubUsers[i]
		if u.TrafficLimitBytes > 0 && hasRemote(u.Selection) {
			out = append(out, centralUserTarget{key: userSubKey("subuser", u.ID), kind: "subuser", id: u.ID, name: u.Name, mode: u.TrafficLimitMode, limit: u.TrafficLimitBytes, sel: u.Selection})
		}
	}
	for i := range s.cfg.MergedSubs {
		ms := &s.cfg.MergedSubs[i]
		if ms.TrafficLimitBytes > 0 && hasRemote(ms.Selection) {
			out = append(out, centralUserTarget{key: userSubKey("merged", ms.ID), kind: "merged", id: ms.ID, name: ms.Name, mode: ms.TrafficLimitMode, limit: ms.TrafficLimitBytes, sel: ms.Selection})
		}
	}
	return out
}

// remotePanelsOf 一个订阅勾选里的远程面板（去重，保留勾选顺序）。
func (s *Server) remotePanelsOf(sel []SubSelection) []RemotePanel {
	var out []RemotePanel
	seen := map[string]bool{}
	for _, e := range sel {
		if e.Panel == "local" || seen[e.Panel] {
			continue
		}
		seen[e.Panel] = true
		if rem := s.remoteByID(e.Panel); rem != nil {
			out = append(out, *rem)
		}
	}
	return out
}

// disableCentralUser 停用一个带远程落地的订阅：管理面板记录 + 本地
// 转发 + 各远程（失联的跳过，下轮重试；远程自己每分钟也会按本地
// 用量执法）。管理面板记录的停用由 disableUserForwardsLocked 写。
func (s *Server) disableCentralUser(t centralUserTarget, reason string) {
	s.disableUserForwards(t.key, reason)
	for _, rem := range s.remotePanelsOf(t.sel) {
		if ok, _ := s.remoteVersionOK(rem.ID, "1.3.25"); !ok {
			continue
		}
		body, _ := json.Marshal(map[string]any{"key": t.key, "reason": reason})
		_, _, _ = s.remoteCall(rem, "POST", "/api/v1/central/user-forwards/disable", body, 15*time.Second)
	}
}

// resumeCentralUser 恢复一个带远程落地的订阅：管理面板记录 + 本地
// 转发 + 各远程（失联的跳过，下轮重试；远程自己翻周期时也会恢复）。
func (s *Server) resumeCentralUser(t centralUserTarget) {
	_ = s.resumeUserForwards(t.key)
	for _, rem := range s.remotePanelsOf(t.sel) {
		if ok, _ := s.remoteVersionOK(rem.ID, "1.3.25"); !ok {
			continue
		}
		body, _ := json.Marshal(map[string]any{"key": t.key})
		_, _, _ = s.remoteCall(rem, "POST", "/api/v1/central/user-forwards/resume", body, 15*time.Second)
	}
}

// enforceCentralUserTraffic 聚合循环（每分钟）：汇总带远程落地的限流
// 订阅在各面板上的用量，统一裁决——80% 发 Telegram 提醒一次，
// 100% 停用（本地 + 各远程），已在上限以下但还停着的恢复。
// 远程失联时按已有的数据裁决（fail-open，不断转发）；失联的远程
// 本轮不下发指令，等它回来下轮再说。
func (s *Server) enforceCentralUserTraffic() {
	// 先翻周期（管理面板侧的记录也要翻；本地 apply 在管理面板上是空操作）。
	s.rollUserPeriods(time.Now())
	targets := s.centralUserTargets()
	if len(targets) == 0 {
		return
	}
	for _, t := range targets {
		var up, down, upPeriod, downPeriod int64
		anyDisabled := false
		if lUp, lDown, lUpP, lDownP, ok := s.userTraffic.totals(t.key); ok {
			up, down, upPeriod, downPeriod = lUp, lDown, lUpP, lDownP
		}
		for _, f := range s.userForwardsFor(t.key) {
			if f.Disabled {
				anyDisabled = true
			}
		}
		if t.kind == "subuser" {
			if u := findSubUserByID(s.cfg.SubUsers, t.id); u != nil && u.Disabled {
				anyDisabled = true
			}
		} else if ms := findMergedSubByID(s.cfg.MergedSubs, t.id); ms != nil && ms.Disabled {
			anyDisabled = true
		}
		for _, rem := range s.remotePanelsOf(t.sel) {
			ut, err := s.fetchRemoteUserTraffic(rem, t.key)
			if err != nil {
				continue
			}
			up += ut.UpTotal
			down += ut.DownTotal
			upPeriod += ut.UpPeriod
			downPeriod += ut.DownPeriod
			if ut.Disabled {
				anyDisabled = true
			}
		}
		used := up + down
		if t.mode == "monthly" {
			used = upPeriod + downPeriod
		}
		s.centralUserUsageMu.Lock()
		s.centralUserUsage[t.key] = &centralUserUsage{UpTotal: up, DownTotal: down, UpPeriod: upPeriod, DownPeriod: downPeriod, Disabled: anyDisabled, UpdatedAt: time.Now()}
		s.centralUserUsageMu.Unlock()
		pct := float64(used) / float64(t.limit) * 100
		display := t.name
		if display == "" {
			display = t.key
		}
		if pct >= 80 && !s.userWarned80(t.key) {
			s.sendTelegram("流量提醒：订阅 " + display + " 流量已用 " + formatPct(pct) + "%（各面板合计）")
			s.setUserWarned80(t.key, true)
		}
		if used >= t.limit {
			s.disableCentralUser(t, "traffic")
			if !s.userWarned100(t.key) {
				s.sendTelegram("流量用完：订阅 " + display + " 已自动停用（流量上限，各面板合计），续量后恢复")
				s.setUserWarned100(t.key, true)
			}
			continue
		}
		if anyDisabled {
			s.resumeCentralUser(t)
		}
	}
}

// centralUserTrafficLoop 聚合循环：每 60 秒汇总一次（与本地流量采样
// 同一节拍）。没带远程落地的限流订阅时直接返回，不做事。
func (s *Server) centralUserTrafficLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.enforceCentralUserTraffic()
	}
}
