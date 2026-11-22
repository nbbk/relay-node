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
//   GET  /api/v1/central/user-forwards
//     （v1.3.33）本机全部按人转发记录 + 已占用监听端口（只读，供管理
//     面板分配端口前问询、登记时导入用；不含任何令牌）。
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
	"sort"
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

// handleCentralUserForwardsList 被管端：返回本机全部按人转发记录与已占
// 用监听端口（v1.3.33，只读）。供管理面板分配端口前问询（远程说了算，
// 不再只凭管理面板自己的记录猜）、登记远程时导入用。UserForward 本来
// 就不含令牌，响应里也没有任何敏感字段。
func (s *Server) handleCentralUserForwardsList(w http.ResponseWriter, r *http.Request) {
	forwards := append([]UserForward{}, s.cfg.UserForwards...)
	if forwards == nil {
		forwards = []UserForward{}
	}
	used := s.usedListenPorts()
	ports := make([]int, 0, len(used))
	for p := range used {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	writeJSON(w, 200, okResp(map[string]any{"forwards": forwards, "used_ports": ports, "panel_version": Version}))
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
		// v1.3.27：本面板流量采样失败时告诉管理面板（用量不可信，别静默显示 0）。
		"traffic_error": s.trafficErr(),
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
// 聚合循环每分钟刷新；列表显示用）。TrafficError 是某台远程面板上报
// 的流量采样失败原因（v1.3.27）：有它时列表用量不可信，要在行里提示。
type centralUserUsage struct {
	UpTotal      int64
	DownTotal    int64
	UpPeriod     int64
	DownPeriod   int64
	Disabled     bool
	UpdatedAt    time.Time
	TrafficError string
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
// 上两个订阅撞端口）。v1.3.33：分配前先问远程要已占端口表（远程说了
// 算，见 fetchRemoteUserForwards）——只凭本机记录会撞上远程自己建的
// 转发、或撤销重登记后换了面板 id 的旧记录。远程不可达、超时或旧版本
// 无此接口时回退本地记录（行为与旧版一致，不报错）；远程收到后仍会
// 自己校验、冲突的跳过并上报（见 handleCentralUserForwards）。
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
	// v1.3.33：问远程要已占端口表并入避让集合。问不到就按本地记录分
	// （旧远程、远程掉线时都走这条路，不阻断保存）。
	if rem := s.remoteByID(panelID); rem != nil {
		if rf, err := s.fetchRemoteUserForwards(*rem); err == nil {
			for _, p := range rf.UsedPorts {
				used[p] = true
			}
			for _, f := range rf.Forwards {
				used[f.ListenPort] = true
			}
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

// ---------- 随时更换中转端口（v1.3.34） ----------
//
// 一次性操作，不新增任何后台 goroutine、定时器、轮询循环；只复用已有
// 的一次性调用链（1.3.33 的 fetchRemoteUserForwards、pushUserForwardsToRemote、
// applyUserForwards）。新接口只读内存配置（s.cfg.UserForwards、centralCache），
// 不新增落盘读——唯一一次 relay.conf 读与现有 allocateUserPortForPanel
// 是同一处调用（本机落地监听端口必须避让）；换口一次的开销：远程
// 1 次 GET（问占用）+ 1 次 POST（下发新端口），本机 1 次 saveConfig，
// 下发失败回滚时再加 1 次 saveConfig + 1 次 POST（把旧集合推回去恢复）。
// 常驻占用新增：零。

// userPortLocalUsed 本机侧已占端口（与 allocateUserPortForPanel 同一套
// 口径：本机落地监听端口 + 面板端口 + 该面板的本机转发记录），返回
// 端口 → 占用原因。excludeIdx >= 0 时本机记录里那条转发不算占用
// （换端口时它自己的旧端口要空出来）。
func (s *Server) userPortLocalUsed(panelID string, excludeIdx int) map[int]string {
	used := map[int]string{}
	if rc, _, err := s.readRelayConf(); err == nil && rc != nil {
		for _, l := range rc.Landings {
			for _, p := range l.Ports {
				if _, ok := used[p.Listen]; !ok {
					used[p.Listen] = "落地监听端口"
				}
			}
		}
	}
	if s.cfg.Port > 0 {
		used[s.cfg.Port] = "面板端口"
	}
	for i := range s.cfg.UserForwards {
		if i == excludeIdx {
			continue
		}
		f := &s.cfg.UserForwards[i]
		if f.Panel == panelID {
			name := f.SubName
			if name == "" {
				name = f.Key
			}
			used[f.ListenPort] = "订阅「" + name + "」已在用"
		}
	}
	return used
}

// centralForwardsFor 取本机存的某订阅在某台面板上的转发集合（下发用）。
func (s *Server) centralForwardsFor(key, panelID string) []centralUserForward {
	list := []centralUserForward{}
	for _, f := range s.cfg.UserForwards {
		if f.Key == key && f.Panel == panelID {
			list = append(list, centralUserForward{LandingAddr: f.LandingAddr, ListenPort: f.ListenPort, LimitBytes: f.LimitBytes, LimitMode: f.LimitMode, ResetMode: f.ResetMode, ResetDay: f.ResetDay, NextResetAt: f.NextResetAt, Disabled: f.Disabled})
		}
	}
	return list
}

// remoteLandingName 从集中管理快照（内存）里取一台远程面板上某落地
// 地址的显示名；快照里没有就返回空串（前端直接显示地址，不另读文件）。
func (s *Server) remoteLandingName(panelID, addr string) string {
	s.centralMu.Lock()
	snap := s.centralCache[panelID]
	s.centralMu.Unlock()
	if snap == nil {
		return ""
	}
	for _, l := range snap.Landings {
		if l.Landing == addr {
			return l.Name
		}
	}
	return ""
}

// changeUserForwardPort 换一条按人转发的端口（v1.3.34）：key/panelID/
// landingAddr 定位一条转发；newPort > 0 = 自定义端口（须在用户端口
// 段内、不撞任何已占端口），newPort == 0 = 自动分配。返回旧端口、
// 新端口、远程占用是否确认过（远程问不到时 false，调用方提示）、
// 失败原因。失败时本地记录不动：下发失败会把本地记录与远程集合都
// 回滚到旧端口（远程的全量替换会丢掉被跳过的那条，不推旧集合恢复
// 的话转发就没了）。调用方须持 opMu。
func (s *Server) changeUserForwardPort(key, panelID, landingAddr string, newPort int) (oldPort, newPortOut int, remoteChecked bool, err error) {
	idx := -1
	for i := range s.cfg.UserForwards {
		f := &s.cfg.UserForwards[i]
		if f.Key == key && f.Panel == panelID && f.LandingAddr == landingAddr {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0, 0, false, fmt.Errorf("这条订阅在这个落地没有按人转发（没开限流，或转发还没建起来，重新保存一次订阅）")
	}
	fwd := &s.cfg.UserForwards[idx]
	oldPort = fwd.ListenPort
	subName := fwd.SubName
	var rem *RemotePanel
	if panelID != "local" {
		rem = s.remoteByID(panelID)
		if rem == nil {
			return 0, 0, false, fmt.Errorf("远程面板 %s 已不在集中管理里", panelID)
		}
	}
	// 已占端口（本机一套 + 远程一套，只问一次远程；问不到就按本机
	// 记录校验，remoteChecked=false，调用方提示用户）。
	used := s.userPortLocalUsed(panelID, idx)
	remoteUsed := map[int]string{}
	if rem != nil {
		if rf, err := s.fetchRemoteUserForwards(*rem); err == nil {
			remoteChecked = true
			for _, p := range rf.UsedPorts {
				if p == oldPort {
					continue
				}
				if _, ok := remoteUsed[p]; !ok {
					remoteUsed[p] = "远程面板上已被占用"
				}
			}
			for _, f := range rf.Forwards {
				if f.ListenPort == oldPort {
					continue
				}
				name := f.SubName
				if name == "" {
					name = f.Key
				}
				remoteUsed[f.ListenPort] = "远程面板上订阅「" + name + "」在用"
			}
		}
	}
	start, end := s.userPortRange()
	if newPort == 0 {
		// 自动分配：本机与远程的已占都避让，从小到大找；旧端口不算
		// （换走即空出来，但自动换回同一个端口等于没换）。
		for p := start; p <= end; p++ {
			if p == oldPort {
				continue
			}
			if _, ok := used[p]; ok {
				continue
			}
			if _, ok := remoteUsed[p]; ok {
				continue
			}
			newPort = p
			break
		}
		if newPort == 0 {
			return 0, 0, remoteChecked, fmt.Errorf("用户端口段 %d–%d 已满，请在设置里扩大端口段或删除不用的订阅", start, end)
		}
	} else {
		if newPort < start || newPort > end {
			return 0, 0, remoteChecked, fmt.Errorf("端口须在用户端口段 %d–%d 之间", start, end)
		}
		if newPort == oldPort {
			return 0, 0, remoteChecked, fmt.Errorf("新端口与当前端口相同，不用换")
		}
		if reason, ok := used[newPort]; ok {
			return 0, 0, remoteChecked, fmt.Errorf("端口 %d 已被占用（%s），请换一个", newPort, reason)
		}
		if reason, ok := remoteUsed[newPort]; ok {
			return 0, 0, remoteChecked, fmt.Errorf("端口 %d 已被占用（%s），请换一个", newPort, reason)
		}
	}
	// 先改本机记录并落盘，再下发；下发失败回滚两边。
	oldPayload := s.centralForwardsFor(key, panelID)
	fwd.ListenPort = newPort
	if err := s.saveConfig(); err != nil {
		fwd.ListenPort = oldPort
		return 0, 0, remoteChecked, fmt.Errorf("保存失败：%v", err)
	}
	rollback := func() {
		fwd.ListenPort = oldPort
		_ = s.saveConfig()
	}
	if panelID == "local" {
		if err := s.applyUserForwards(); err != nil {
			rollback()
			return 0, 0, remoteChecked, fmt.Errorf("转发没建起来：%v", err)
		}
		return oldPort, newPort, remoteChecked, nil
	}
	applied, skipped, err := s.pushUserForwardsToRemote(*rem, key, subName, s.centralForwardsFor(key, panelID))
	if err != nil || skipped > 0 {
		// 远程的全量替换会丢掉被跳过的那条：把旧集合推回去恢复。
		rollback()
		_, _, _ = s.pushUserForwardsToRemote(*rem, key, subName, oldPayload)
		if err != nil {
			return 0, 0, remoteChecked, fmt.Errorf("远程面板没应用上：%v，已恢复原端口", err)
		}
		return 0, 0, remoteChecked, fmt.Errorf("远程面板有 %d 条转发没应用上（端口冲突），已恢复原端口 %d（已应用 %d 条）", skipped, oldPort, applied)
	}
	return oldPort, newPort, remoteChecked, nil
}

// handleSubUserForwards 管理端：一条订阅的按人转发列表（更换端口入口
// 用）。只读内存配置，不读文件。kind 是 "subuser" 或 "merged"。
func (s *Server) handleSubUserForwards(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		key := userSubKey(kind, id)
		var subName string
		var found bool
		if kind == "subuser" {
			if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
				found, subName = true, u.Name
			}
		} else if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			found, subName = true, ms.Name
		}
		if !found {
			writeJSON(w, 404, errResp("没有这条订阅"))
			return
		}
		start, end := s.userPortRange()
		out := []map[string]any{}
		for _, f := range s.userForwardsFor(key) {
			panelName := "本面板"
			landingName := ""
			if f.Panel != "local" {
				if rem := s.remoteByID(f.Panel); rem != nil {
					panelName = rem.Name
				} else {
					panelName = f.Panel
				}
				landingName = s.remoteLandingName(f.Panel, f.LandingAddr)
			}
			out = append(out, map[string]any{
				"panel_id": f.Panel, "panel_name": panelName,
				"landing_addr": f.LandingAddr, "landing_name": landingName,
				"listen_port": f.ListenPort, "disabled": f.Disabled,
			})
		}
		writeJSON(w, 200, okResp(map[string]any{"id": id, "name": subName, "forwards": out, "port_start": start, "port_end": end}))
	}
}

// handleSubChangePort 管理端：给一条订阅换一个中转端口（v1.3.34）。
// 请求体 {panel_id, landing_addr, new_port}，new_port 为 0 = 自动分配。
// 换完订阅链接里的中转端口跟着变（下次取订阅即新端口），用量记录按
// 订阅 key 走不清零，旧端口释放回池子。kind 是 "subuser" 或 "merged"。
func (s *Server) handleSubChangePort(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		key := userSubKey(kind, id)
		var req struct {
			PanelID     string `json:"panel_id"`
			LandingAddr string `json:"landing_addr"`
			NewPort     int    `json:"new_port"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, 400, errResp("请求格式不对"))
			return
		}
		var subName string
		var found bool
		if kind == "subuser" {
			if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
				found, subName = true, u.Name
			}
		} else if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			found, subName = true, ms.Name
		}
		if !found {
			writeJSON(w, 404, errResp("没有这条订阅"))
			return
		}
		req.PanelID = strings.TrimSpace(req.PanelID)
		req.LandingAddr = strings.TrimSpace(req.LandingAddr)
		if req.PanelID == "" || req.LandingAddr == "" {
			writeJSON(w, 400, errResp("没指定要换哪条转发"))
			return
		}
		if !s.tryOpLock(w) {
			return
		}
		defer s.opMu.Unlock()
		oldPort, newPort, remoteChecked, err := s.changeUserForwardPort(key, req.PanelID, req.LandingAddr, req.NewPort)
		if err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
		panelName := "本面板"
		if req.PanelID != "local" {
			if rem := s.remoteByID(req.PanelID); rem != nil {
				panelName = rem.Name
			} else {
				panelName = req.PanelID
			}
		}
		detail := fmt.Sprintf("落地 %s（%s）：%d → %d", req.LandingAddr, panelName, oldPort, newPort)
		warning := ""
		if req.PanelID != "local" && !remoteChecked {
			warning = "远程面板没问到（未确认远程占用：版本太旧或连不上），只按本机记录校验了占用；换完请看远程面板上新端口是否生效"
			detail += "；" + warning
		}
		s.audit(s.currentUser(r), "更换中转端口", subName, "成功", detail)
		resp := map[string]any{"id": id, "name": subName, "old_port": oldPort, "new_port": newPort, "remote_checked": remoteChecked}
		if warning != "" {
			resp["warning"] = warning
		}
		writeJSON(w, 200, okResp(resp))
	}
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
	TrafficError                             string // 远程面板的流量采样失败原因（v1.3.27）
}

// remoteUserForwards 一台远程面板的按人转发全量与已占端口
// （GET /api/v1/central/user-forwards 的返回）。
type remoteUserForwards struct {
	Forwards  []UserForward `json:"forwards"`
	UsedPorts []int         `json:"used_ports"`
}

// fetchRemoteUserForwards 拉一台远程面板的按人转发全量与已占端口
// （v1.3.33）。远程不可达、超时、旧版本无此接口（404）时返回错误，
// 调用方回退本地记录逻辑、不阻断保存。5 秒超时，别让一次远程掉线
// 拖垮保存/登记流程。
func (s *Server) fetchRemoteUserForwards(rem RemotePanel) (*remoteUserForwards, error) {
	_, body, err := s.remoteCall(rem, "GET", "/api/v1/central/user-forwards", nil, 5*time.Second)
	if err != nil {
		return nil, err
	}
	var resp struct {
		OK        bool          `json:"ok"`
		Forwards  []UserForward `json:"forwards"`
		UsedPorts []int         `json:"used_ports"`
		Error     string        `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("local：远程面板返回的内容解析失败：%v", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("remote：远程面板：%s", resp.Error)
	}
	if resp.Forwards == nil {
		resp.Forwards = []UserForward{}
	}
	return &remoteUserForwards{Forwards: resp.Forwards, UsedPorts: resp.UsedPorts}, nil
}

// importRemoteUserForwards 从一台远程面板拉它的按人转发全量，导入为
// 本机记录（v1.3.33，登记远程时调用）：Panel 写成该远程的面板 id，与
// 本机已有的该远程记录按 (Key, ListenPort) 去重；同订阅同落地、端口
// 已变的，以远程的为准（远程是其转发记录的唯一事实来源，连端口一起
// 刷新）。本机原有记录不删。拉不到（不可达、超时、旧版本）返回错误，
// 调用方只记警告、不阻断登记。
func (s *Server) importRemoteUserForwards(rem RemotePanel) (imported, updated int, err error) {
	rf, err := s.fetchRemoteUserForwards(rem)
	if err != nil {
		return 0, 0, err
	}
	for _, f := range rf.Forwards {
		var existing *UserForward
		for i := range s.cfg.UserForwards {
			c := &s.cfg.UserForwards[i]
			if c.Panel != rem.ID || c.Key != f.Key {
				continue
			}
			if c.LandingAddr == f.LandingAddr || c.ListenPort == f.ListenPort {
				existing = c
				break
			}
		}
		if existing == nil {
			nf := f
			nf.Panel = rem.ID
			s.cfg.UserForwards = append(s.cfg.UserForwards, nf)
			imported++
			continue
		}
		// 同订阅同落地（或同端口）：以远程的为准，全字段刷新。
		existing.SubKind = f.SubKind
		existing.SubID = f.SubID
		existing.SubName = f.SubName
		existing.LandingAddr = f.LandingAddr
		existing.ListenPort = f.ListenPort
		existing.Disabled = f.Disabled
		existing.LimitBytes = f.LimitBytes
		existing.LimitMode = f.LimitMode
		existing.ResetMode = f.ResetMode
		existing.ResetDay = f.ResetDay
		existing.NextResetAt = f.NextResetAt
		updated++
	}
	if imported+updated > 0 {
		if err := s.saveConfig(); err != nil {
			return imported, updated, fmt.Errorf("保存失败：%v", err)
		}
	}
	return imported, updated, nil
}

// fetchRemoteUserTraffic 拉一台远程面板上一个订阅的用量。
func (s *Server) fetchRemoteUserTraffic(rem RemotePanel, key string) (remoteUserTraffic, error) {
	var out remoteUserTraffic
	_, body, err := s.remoteCall(rem, "GET", "/api/v1/central/user-traffic?key="+url.QueryEscape(key), nil, 15*time.Second)
	if err != nil {
		return out, err
	}
	var resp struct {
		OK           bool   `json:"ok"`
		UpTotal      int64  `json:"up_total"`
		DownTotal    int64  `json:"down_total"`
		UpPeriod     int64  `json:"up_period"`
		DownPeriod   int64  `json:"down_period"`
		LimitBytes   int64  `json:"limit_bytes"`
		LimitMode    string `json:"limit_mode"`
		Disabled     bool   `json:"disabled"`
		HasData      bool   `json:"has_data"`
		TrafficError string `json:"traffic_error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || !resp.OK {
		return out, fmt.Errorf("远程面板返回的内容解析失败")
	}
	return remoteUserTraffic{UpTotal: resp.UpTotal, DownTotal: resp.DownTotal, UpPeriod: resp.UpPeriod, DownPeriod: resp.DownPeriod, LimitBytes: resp.LimitBytes, LimitMode: resp.LimitMode, Disabled: resp.Disabled, HasData: resp.HasData, TrafficError: resp.TrafficError}, nil
}

// centralUserTarget 聚合循环里一个带远程落地的限流订阅。
type centralUserTarget struct {
	key, kind, id, name, mode string
	limit                     int64
	sel                       []SubSelection
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
		var trafficErr string
		anyDisabled := false
		if lUp, lDown, lUpP, lDownP, ok := s.userTraffic.totals(t.key); ok {
			up, down, upPeriod, downPeriod = lUp, lDown, lUpP, lDownP
		}
		if terr := s.trafficErr(); terr != "" {
			trafficErr = terr
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
			if trafficErr == "" && ut.TrafficError != "" {
				trafficErr = "远程面板 " + rem.Name + "：" + ut.TrafficError
			}
		}
		used := up + down
		if t.mode == "monthly" {
			used = upPeriod + downPeriod
		}
		s.centralUserUsageMu.Lock()
		s.centralUserUsage[t.key] = &centralUserUsage{UpTotal: up, DownTotal: down, UpPeriod: upPeriod, DownPeriod: downPeriod, Disabled: anyDisabled, UpdatedAt: time.Now(), TrafficError: trafficErr}
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
