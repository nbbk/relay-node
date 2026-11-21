package main

// ---------- 按订阅限流量 A（v1.3.25）：每人一条转发 ----------
//
// 核心：每个开了限流的订阅，在每台中转机上有一条独立转发（独立监听
// 端口 → 同一落地端口）。端口就是用户身份：流量按端口统计，复用现有
// 的 iptables/nftables 按端口计数 + 每分钟采样，粒度从“落地”细化
// 到“订阅 × 落地”。订阅链接里的中转节点改用该用户的端口，SNI 与域
// 名不变，链接本身不变，客户端更新即同步。落地零改动。
//
// 锁约定：本文件的 reconcile/apply/remove 函数不自己加 opMu，调用方
// 负责持锁——订阅处理器里调用前先 opMu.Lock()，后台 disableUserForwards
// 自己加锁（与 disableLanding 同一套路），落地增删处理器本来就持锁
// （tryOpLock）。不要在持锁的调用链里再调会加锁的版本，避免死锁。

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 每用户端口段默认值（v1.3.25）：30000–39999，设置页可改。
const (
	defaultUserPortStart = 30000
	defaultUserPortEnd   = 39999
)

// userPortRange 返回每用户端口段（配置里 0 或非法 = 用默认值）。
func (s *Server) userPortRange() (int, int) {
	start, end := s.cfg.UserPortStart, s.cfg.UserPortEnd
	if start <= 0 || end <= 0 || start > end || end > 65535 {
		return defaultUserPortStart, defaultUserPortEnd
	}
	return start, end
}

// userSubKey 订阅标识："subuser:<id>" 或 "merged:<id>"。
func userSubKey(kind, id string) string {
	return kind + ":" + id
}

// normalizeUserLimit 校验订阅的流量上限字段：limit<=0 = 不限流（其余
// 字段忽略）；limit>0 时模式只认 total/monthly（空 = total），重置
// 模式只认 monthly/anniversary/custom（空 = monthly），custom 必须
// 给 1–31 的 ResetDay。返回归一化后的四项。
func normalizeUserLimit(limitBytes int64, limitMode, resetMode string, resetDay int) (int64, string, string, int, error) {
	if limitBytes <= 0 {
		return 0, "", "", 0, nil
	}
	mode := strings.TrimSpace(limitMode)
	if mode == "" {
		mode = "total"
	}
	if mode != "total" && mode != "monthly" {
		return 0, "", "", 0, fmt.Errorf("流量上限模式只支持 累计/按月")
	}
	rm := strings.TrimSpace(resetMode)
	if rm == "" {
		rm = "monthly"
	}
	if rm != "monthly" && rm != "anniversary" && rm != "custom" {
		return 0, "", "", 0, fmt.Errorf("重置模式只支持 自然月/跟随生成日/自定义每月几号")
	}
	day := resetDay
	if rm == "custom" {
		if day < 1 || day > 31 {
			return 0, "", "", 0, fmt.Errorf("自定义重置日填 1–31 号")
		}
	}
	if day < 0 || day > 31 {
		day = 0
	}
	return limitBytes, mode, rm, day, nil
}

// ---------- 重置时间 ----------

// clampDay 把 day 钳制到该月的天数内（31 号在小月钳制到月末）。
func clampDay(year int, month time.Month, day int) int {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		return last
	}
	if day < 1 {
		return 1
	}
	return day
}

// nextResetOccurrence 返回 resetDay（每月几号）在 from 之后下一次
// 出现的时间（当天 00:00，服务器本地时间）。from 正好是重置时刻
// 时返回下个月的，不会原地打转。
func nextResetOccurrence(resetDay int, from time.Time) time.Time {
	loc := from.Location()
	y, m, _ := from.Date()
	d := clampDay(y, m, resetDay)
	cand := time.Date(y, m, d, 0, 0, 0, 0, loc)
	if cand.After(from) {
		return cand
	}
	nm := m + 1
	ny := y
	if nm > 12 {
		nm = 1
		ny++
	}
	d2 := clampDay(ny, nm, resetDay)
	return time.Date(ny, nm, d2, 0, 0, 0, 0, loc)
}

// computeNextReset 按重置模式算下次重置时间：monthly 是每月 1 号，
// anniversary 是建订阅那天（anchor 的日），custom 是 ResetDay。
// from 是“现在”（传进来是为了测试能定死时间）。
func computeNextReset(resetMode string, resetDay int, anchor, from time.Time) time.Time {
	switch resetMode {
	case "anniversary":
		day := anchor.Day()
		return nextResetOccurrence(day, from)
	case "custom":
		return nextResetOccurrence(resetDay, from)
	default: // "monthly" 或空
		return nextResetOccurrence(1, from)
	}
}

// normalizeUserLimitFields 归一化一组限流字段并算出 NextResetAt：
// limit<=0 = 不限流（其余字段清掉，NextResetAt 为空）；monthly 模式
// 时 NextResetAt 是下一次重置时间（RFC3339），total 模式为空。
// anchor 是订阅创建时间（anniversary 取它的日），from 是“现在”。
func normalizeUserLimitFields(limitBytes int64, limitMode, resetMode string, resetDay int, anchor, from time.Time) (int64, string, string, int, string, error) {
	limit, mode, rm, day, err := normalizeUserLimit(limitBytes, limitMode, resetMode, resetDay)
	if err != nil {
		return 0, "", "", 0, "", err
	}
	if limit <= 0 {
		return 0, "", "", 0, "", nil
	}
	next := ""
	if mode == "monthly" {
		next = computeNextReset(rm, day, anchor, from).Format(time.RFC3339)
	}
	return limit, mode, rm, day, next, nil
}

// mbToBytes 兆字节转字节（前端传 MB，存字节；<=0 = 不限流）。
func mbToBytes(mb int64) int64 {
	if mb <= 0 {
		return 0
	}
	return mb * 1024 * 1024
}

// resetDayForMode 返回该重置模式对应的每月几号（monthly=1，
// anniversary=建订阅那天，custom=用户填的号）。
func resetDayForMode(resetMode string, resetDay int, anchor time.Time) int {
	switch resetMode {
	case "anniversary":
		return anchor.Day()
	case "custom":
		return resetDay
	default:
		return 1
	}
}

// ---------- 每用户流量累计（PANEL_HOME/user_traffic.json） ----------

// userTrafficRec 一个订阅在本面板上的流量累计：UpTotal/DownTotal 是
// 累计（total 模式用），UpPeriod/DownPeriod 是本期（monthly 模式用），
// PeriodID 是周期号（重置时 +1，推送带上防串周期），LastCounters 是
// 采样基数（key 是 "proto:port"，只对本订阅的用户端口采样）。
type userTrafficRec struct {
	UpTotal      int64             `json:"up_total"`
	DownTotal    int64             `json:"down_total"`
	UpPeriod     int64             `json:"up_period"`
	DownPeriod   int64             `json:"down_period"`
	PeriodID     int               `json:"period_id"`
	LastCounters map[string][2]int64 `json:"last_counters"`
}

type userTrafficStore struct {
	mu   sync.Mutex
	path string
	data map[string]*userTrafficRec
}

func newUserTrafficStore(path string) *userTrafficStore {
	return &userTrafficStore{path: path, data: map[string]*userTrafficRec{}}
}

func (t *userTrafficStore) load() {
	b, err := os.ReadFile(t.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &t.data)
	if t.data == nil {
		t.data = map[string]*userTrafficRec{}
	}
}

func (t *userTrafficStore) saveLocked() {
	b, _ := json.MarshalIndent(t.data, "", "  ")
	_ = writeFileAtomic(t.path, b, 0o600)
}

func (t *userTrafficStore) get(key string) *userTrafficRec {
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
	return rec
}

// totals 只读累计（不建新记录）：返回累计上行/下行、本期上行/下行与有没有数据。
func (t *userTrafficStore) totals(key string) (up, down, upPeriod, downPeriod int64, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec, ok := t.data[key]
	if !ok || rec == nil {
		return 0, 0, 0, 0, false
	}
	return rec.UpTotal, rec.DownTotal, rec.UpPeriod, rec.DownPeriod, true
}

// addDelta 累计增量（采样用）；计数器重启清零时按新值重记基数，不算负增量。
func (t *userTrafficStore) addDelta(key string, counters map[string][2]int64) {
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
	var upDelta, downDelta int64
	for k, cur := range counters {
		prev, seen := rec.LastCounters[k]
		if !seen {
			rec.LastCounters[k] = cur
			continue
		}
		upDelta += deltaCounter(prev[0], cur[0])
		downDelta += deltaCounter(prev[1], cur[1])
		rec.LastCounters[k] = cur
	}
	if upDelta == 0 && downDelta == 0 {
		return
	}
	rec.UpTotal += upDelta
	rec.DownTotal += downDelta
	rec.UpPeriod += upDelta
	rec.DownPeriod += downDelta
	t.saveLocked()
}

// userTrafficUsed 按模式算已用字节：total=累计，monthly=本期。
func userTrafficUsed(rec *userTrafficRec, mode string) int64 {
	if rec == nil {
		return 0
	}
	if mode == "monthly" {
		return rec.UpPeriod + rec.DownPeriod
	}
	return rec.UpTotal + rec.DownTotal
}

// userOverLimit 限量判定：上限>0 且已用>=上限。
func userOverLimit(rec *userTrafficRec, limit int64, mode string) bool {
	if limit <= 0 || rec == nil {
		return false
	}
	return userTrafficUsed(rec, mode) >= limit
}

// ---------- 端口分配 ----------

// usedListenPorts 返回本机已被占用的监听端口：落地监听端口 + 已分配的
// 用户端口 + 面板自身端口。分配用户端口时避让这些。
func (s *Server) usedListenPorts() map[int]bool {
	used := map[int]bool{}
	if rc, _, err := s.readRelayConf(); err == nil && rc != nil {
		for _, l := range rc.Landings {
			for _, p := range l.Ports {
				used[p.Listen] = true
			}
		}
	}
	for _, f := range s.cfg.UserForwards {
		used[f.ListenPort] = true
	}
	if s.cfg.Port > 0 {
		used[s.cfg.Port] = true
	}
	return used
}

// allocateUserPort 在用户端口段内分配一个空闲端口（从小到大找）；
// 段内全满返回错误。used 是调用方算好的已占用集合（避免重复读配置）。
func (s *Server) allocateUserPort(used map[int]bool) (int, error) {
	start, end := s.userPortRange()
	for p := start; p <= end; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("用户端口段 %d–%d 已满，请在设置里扩大端口段或删除不用的订阅", start, end)
}

// ---------- 转发规则 ----------

// nodePortForLanding 取一个落地的节点端口（twin 中转链接里的端口）：
// 先读 nodes.txt 里该落地的中转链接解析，解析不到回落到落地第一个
// TCP 端口的监听端口。
func (s *Server) nodePortForLanding(l Landing) int {
	groups, err := s.loadLinkGroups()
	if err == nil {
		for _, g := range groups {
			if g.Landing != l.Landing {
				continue
			}
			for _, lk := range g.Links {
				if lk.Label != "中转" {
					continue
				}
				if pl, err := parseNodeLink(lk.Link); err == nil && pl.Port > 0 {
					return pl.Port
				}
			}
		}
	}
	for _, p := range l.Ports {
		if p.Proto == "tcp" {
			return p.Listen
		}
	}
	if len(l.Ports) > 0 {
		return l.Ports[0].Listen
	}
	return 0
}

// userForwardEndpoints 一个用户转发要建的 [[endpoints]]：只镜像落地
// 里监听端口等于节点端口的那几条（24443 这类 tcp+udp 同号会出两条，
// 443 只出一条 tcp；80 这类非节点端口不镜像，免得一个监听端口指
// 两个远端起冲突）。返回的 PortSpec 里 Listen 是用户端口，
// Remote 是落地那边的端口。
func (s *Server) userForwardEndpoints(l Landing, userPort int) []PortSpec {
	nodePort := s.nodePortForLanding(l)
	var out []PortSpec
	seen := map[string]bool{}
	for _, p := range l.Ports {
		if p.Listen != nodePort {
			continue
		}
		k := p.Proto + ":" + strconv.Itoa(userPort)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, PortSpec{Proto: p.Proto, Listen: userPort, Remote: p.Remote})
	}
	if len(out) == 0 {
		// 兜底：节点端口对不上时镜像全部端口（按协议去重，避免冲突）。
		for _, p := range l.Ports {
			k := p.Proto + ":" + strconv.Itoa(userPort)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, PortSpec{Proto: p.Proto, Listen: userPort, Remote: p.Remote})
		}
	}
	return out
}

// userForwardsFor 取一个订阅在本面板上的全部转发记录。
func (s *Server) userForwardsFor(key string) []UserForward {
	var out []UserForward
	for _, f := range s.cfg.UserForwards {
		if f.Key == key {
			out = append(out, f)
		}
	}
	return out
}

// findUserForward 取一个订阅在某落地上的转发记录（按落地地址找）。
func (s *Server) findUserForward(key, landingAddr string) *UserForward {
	for i := range s.cfg.UserForwards {
		if s.cfg.UserForwards[i].Key == key && s.cfg.UserForwards[i].LandingAddr == landingAddr {
			return &s.cfg.UserForwards[i]
		}
	}
	return nil
}

// userPortByAddr 取一个订阅在本面板上的转发端口表（落地地址 → 用户端口），
// 订阅内容生成时改写中转节点链接用；已停用的也返回（链接照常用端口，
// 流量到 quota 就断，客户端测速失败自动切走）。只取 Panel=local 的
// （本机转发）；远程的转发存在管理面板上是另一份记录（Panel=远程 id），
// 由远程自己在 handleCentralSubscription 里改写，不走这里。
// 注意：同一落地地址同时在本机与远程各有一条转发时（极少见，同一台
// 落地机在两个面板上都登记了），本地改写会按地址匹配到两条链接，
// 远程那条也会被换成本机端口——这时以本机端口为准，远程那条失效，
// 属于登记重复，应把落地只留在一个面板上。
func (s *Server) userPortByAddr(key string) map[string]int {
	m := map[string]int{}
	for _, f := range s.cfg.UserForwards {
		if f.Key == key && f.Panel == "local" {
			m[f.LandingAddr] = f.ListenPort
		}
	}
	return m
}

// localLandingsForSelection 取一份订阅勾选里属于本面板的落地：
// SubUser 空勾选 = 单面板全量（全部本地落地）；否则只取勾选里
// Panel == "local" 的那些。返回落地列表（按 relay.conf 顺序）。
func (s *Server) localLandingsForSelection(sel []SubSelection) []Landing {
	rc, _, err := s.readRelayConf()
	if err != nil || rc == nil {
		return nil
	}
	if len(sel) == 0 {
		return rc.Landings
	}
	want := map[int]bool{}
	for _, e := range sel {
		if e.Panel == "local" {
			want[e.Landing] = true
		}
	}
	var out []Landing
	for _, l := range rc.Landings {
		if want[l.Index] {
			out = append(out, l)
		}
	}
	return out
}

// reconcileUserForwards 按订阅的限流设置核对本面板上的转发：
// limit<=0 就删掉该订阅的全部转发（回共享端口）；否则给勾选里每个
// 本地落地确保一条转发（没有就分配端口新建，有就更新配额字段），
// 不在勾选里的落地转发删掉。改完重建用户 endpoints 并重启 realm。
// 调用方须持 opMu（见文件头锁约定）。
func (s *Server) reconcileUserForwards(subKind, subID, subName string, sel []SubSelection, limitBytes int64, limitMode, resetMode string, resetDay int, nextResetAt string) error {
	key := userSubKey(subKind, subID)
	if limitBytes <= 0 {
		return s.removeUserForwards(subKind, subID)
	}
	landings := s.localLandingsForSelection(sel)
	want := map[string]bool{}
	for _, l := range landings {
		want[l.Landing] = true
	}
	// 已占用端口：落地监听 + 其他订阅的转发 + 本订阅在新勾选内的转发。
	// 本订阅在新勾选之外的转发会被删掉，它们的端口可以复用。
	used := s.usedListenPorts()
	for _, f := range s.cfg.UserForwards {
		if f.Key == key && !want[f.LandingAddr] {
			delete(used, f.ListenPort)
		}
	}
	// 预检：先算要新建几条转发，端口段不够就直接报错，避免建一半
	// 失败、留半截转发（调用方持 opMu，预检与分配之间不会被插队）。
	need := 0
	for _, l := range landings {
		if s.findUserForward(key, l.Landing) == nil {
			need++
		}
	}
	if need > 0 {
		start, end := s.userPortRange()
		free := 0
		for p := start; p <= end; p++ {
			if !used[p] {
				free++
			}
		}
		if free < need {
			return fmt.Errorf("用户端口段 %d–%d 只剩 %d 个空闲端口，需要 %d 个（该订阅勾选了 %d 个落地），请在设置里扩大端口段或删掉不用的订阅", start, end, free, need, len(landings))
		}
	}
	changed := false
	for _, l := range landings {
		f := s.findUserForward(key, l.Landing)
		if f == nil {
			port, err := s.allocateUserPort(used)
			if err != nil {
				return err
			}
			used[port] = true
			s.cfg.UserForwards = append(s.cfg.UserForwards, UserForward{
				Key: key, SubKind: subKind, SubID: subID, SubName: subName,
				Panel: "local", LandingAddr: l.Landing, ListenPort: port,
				LimitBytes: limitBytes, LimitMode: limitMode, ResetMode: resetMode,
				ResetDay: resetDay, NextResetAt: nextResetAt,
			})
			changed = true
			continue
		}
		// 已有：更新配额字段（限流改了就同步下去）；停用状态不动，
// 恢复走 resume（不在这里自动恢复，避免改个上限就把已停用的放行）。
		if f.LimitBytes != limitBytes || f.LimitMode != limitMode || f.ResetMode != resetMode || f.ResetDay != resetDay || f.NextResetAt != nextResetAt {
			f.LimitBytes = limitBytes
			f.LimitMode = limitMode
			f.ResetMode = resetMode
			f.ResetDay = resetDay
			f.NextResetAt = nextResetAt
			changed = true
		}
	}
	// 不在勾选里的转发删掉
	var kept []UserForward
	for _, f := range s.cfg.UserForwards {
		if f.Key == key && !want[f.LandingAddr] {
			changed = true
			continue
		}
		kept = append(kept, f)
	}
	s.cfg.UserForwards = kept
	if changed {
		if err := s.saveConfig(); err != nil {
			return err
		}
		return s.applyUserForwards()
	}
	return nil
}

// removeUserForwards 删掉一个订阅在本面板上的全部转发（订阅删除、
// 限流取消时调用）：删记录、撤 endpoints、重启 realm。调用方须持 opMu。
func (s *Server) removeUserForwards(subKind, subID string) error {
	key := userSubKey(subKind, subID)
	var kept []UserForward
	removed := false
	for _, f := range s.cfg.UserForwards {
		if f.Key == key {
			removed = true
			continue
		}
		kept = append(kept, f)
	}
	if !removed {
		return nil
	}
	s.cfg.UserForwards = kept
	if err := s.saveConfig(); err != nil {
		return err
	}
	return s.applyUserForwards()
}

// renameUserForwardsLanding 落地改地址时调用：把转发记录的落地地址
// 从 old 迁到 new（只改记录、不动 config.toml，调用方负责重建端点）。
// 返回有没有迁到记录。
func (s *Server) renameUserForwardsLanding(oldAddr, newAddr string) bool {
	if oldAddr == newAddr {
		return false
	}
	renamed := false
	for i := range s.cfg.UserForwards {
		if s.cfg.UserForwards[i].LandingAddr == oldAddr {
			s.cfg.UserForwards[i].LandingAddr = newAddr
			renamed = true
		}
	}
	if renamed {
		_ = s.saveConfig()
	}
	return renamed
}

// dropUserForwardsForLanding 删掉某落地上的全部用户转发记录（落地删除时
// 调用，按地址删，编号前移也不影响）。只清 config.json 里的记录、不动
// config.toml 与 realm——调用方（删落地处理器）已按 remote 地址把该
// 落地的端点全删了并重启了 realm。返回有没有删掉记录。
func (s *Server) dropUserForwardsForLanding(landingAddr string) bool {
	var kept []UserForward
	removed := false
	for _, f := range s.cfg.UserForwards {
		if f.LandingAddr == landingAddr {
			removed = true
			continue
		}
		kept = append(kept, f)
	}
	if !removed {
		return false
	}
	s.cfg.UserForwards = kept
	_ = s.saveConfig()
	return true
}

// reconcileAllUserForwards 给所有开了限流的订阅补转发（落地新增后调用，
// 新落地要进全量订阅与勾选含它的订阅；调用方须持 opMu）。
func (s *Server) reconcileAllUserForwards() {
	for i := range s.cfg.SubUsers {
		u := &s.cfg.SubUsers[i]
		if u.TrafficLimitBytes <= 0 {
			continue
		}
		_ = s.reconcileUserForwards("subuser", u.ID, u.Name, u.Selection, u.TrafficLimitBytes, u.TrafficLimitMode, u.ResetMode, u.ResetDay, u.NextResetAt)
	}
	for i := range s.cfg.MergedSubs {
		ms := &s.cfg.MergedSubs[i]
		if ms.TrafficLimitBytes <= 0 {
			continue
		}
		_ = s.reconcileUserForwards("merged", ms.ID, ms.Name, ms.Selection, ms.TrafficLimitBytes, ms.TrafficLimitMode, ms.ResetMode, ms.ResetDay, ms.NextResetAt)
	}
}

// ---------- config.toml 里的用户 endpoints ----------

// ensureTomlNewline 保证 toml 内容以换行结尾再追加新段：按空行分段的
// remove 类函数 join 后不带尾换行，直接追加会让最后一段与新段之间
// 没有空行，下次解析时两段合并成一段、listen 取最后一个，保留段会
// 被误删（v1.3.25 复现：建第 2 个限流用户后共享 24443 段消失；
// 更新最后一个落地时同理）。
func ensureTomlNewline(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] != '\n' {
		return append(b, '\n')
	}
	return b
}

// removeUserEndpointsFromToml 从 config.toml 删掉监听端口在 ports 里的
// [[endpoints]] 段（只按监听端口认，不碰共享落地的段）。
func removeUserEndpointsFromToml(data []byte, ports map[int]bool) []byte {
	text := string(data)
	parts := strings.Split(text, "\n\n")
	var kept []string
	for _, seg := range parts {
		if !strings.Contains(seg, "[[endpoints]]") {
			kept = append(kept, seg)
			continue
		}
		listen := 0
		for _, line := range strings.Split(seg, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "listen = ") {
				v := strings.Trim(strings.TrimPrefix(line, "listen = "), "\"")
				if _, p, err := net.SplitHostPort(v); err == nil {
					listen, _ = strconv.Atoi(p)
				}
			}
		}
		if listen > 0 && ports[listen] {
			continue
		}
		kept = append(kept, seg)
	}
	return []byte(strings.Join(kept, "\n\n"))
}

// applyUserForwards 按当前 UserForwards 重建 config.toml 里的用户
// endpoints：先删掉全部用户端口的段，再把已启用的加回去；有变化才
// 写文件并重启 realm。调用方须持 opMu。
func (s *Server) applyUserForwards() error {
	rc, _, err := s.readRelayConf()
	if err != nil {
		return err
	}
	tomlData, err := s.readConfigToml()
	if err != nil {
		return err
	}
	ports := map[int]bool{}
	for _, f := range s.cfg.UserForwards {
		ports[f.ListenPort] = true
	}
	newToml := removeUserEndpointsFromToml(tomlData, ports)
	// 保留内容末尾补一个换行再追加（ensureTomlNewline）。
	newToml = ensureTomlNewline(newToml)
	// 加回已启用的转发
	byAddr := map[string]Landing{}
	for _, l := range rc.Landings {
		byAddr[l.Landing] = l
	}
	var add strings.Builder
	for _, f := range s.cfg.UserForwards {
		if f.Disabled {
			continue
		}
		l, ok := byAddr[f.LandingAddr]
		if !ok {
			continue
		}
		for _, p := range s.userForwardEndpoints(l, f.ListenPort) {
			add.WriteString(fmt.Sprintf("\n[[endpoints]]\nlisten = \"0.0.0.0:%d\"\nremote = \"%s:%d\"\n", p.Listen, l.Landing, p.Remote))
		}
	}
	newToml = append(newToml, []byte(add.String())...)
	// realm 无 endpoint 会 panic：一个不剩就留占位（与删落地同一路数）。
	if !strings.Contains(string(newToml), "[[endpoints]]") {
		newToml = append(newToml, []byte("\n[[endpoints]]\nlisten = \"127.0.0.1:9\"\nremote = \"127.0.0.1:9\"\n")...)
	}
	if string(newToml) == string(tomlData) {
		return nil
	}
	if err := s.writeConfigTomlAtomic(newToml); err != nil {
		return err
	}
	return s.restartRealm()
}

// ---------- 采样与执法 ----------

// sampleUserTraffic 一次采样：读本机计数器，按用户端口分到各订阅
// 的累计里（一个订阅在多落地上的端口汇总到同一条记录）。管理面板
// 模式不跑（本地没有转发）。
func (s *Server) sampleUserTraffic() {
	if !s.managesLocalRelay() {
		return
	}
	if len(s.cfg.UserForwards) == 0 {
		return
	}
	counters := readTrafficCounters()
	if len(counters) == 0 {
		return
	}
	// 端口 → 订阅 key（同一订阅多落地多端口汇总到一条记录）
	portToKey := map[int]string{}
	for _, f := range s.cfg.UserForwards {
		portToKey[f.ListenPort] = f.Key
	}
	perKey := map[string]map[string][2]int64{}
	for key, cur := range counters {
		proto, portStr, ok := strings.Cut(key, ":")
		if !ok {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		subKey, ok := portToKey[port]
		if !ok {
			continue
		}
		_ = proto
		if perKey[subKey] == nil {
			perKey[subKey] = map[string][2]int64{}
		}
		perKey[subKey][key] = cur
	}
	for key, per := range perKey {
		s.userTraffic.addDelta(key, per)
	}
}

// rollUserPeriods 检查所有开了限流的订阅（monthly 模式）：到了
// NextResetAt 就翻周期（本期用量清零、周期号 +1、算下一次、清掉
// 80%/100% 提醒标志）。翻周期 = 新周期开始：之前因超量停用的也一
// 并恢复（新周期有新额度），转发在函数末尾统一加回去。
// 返回有没有翻过周期（翻过要存配置）。
// 调用方在 trafficLoop 里调，不持 opMu（只改内存里的用量与配置
// 字段，存配置时 saveConfig 本身是原子写；恢复转发要重启 realm
// 时自己加锁）。
func (s *Server) rollUserPeriods(now time.Time) bool {
	changed := false
	needApply := false
	rolled := map[string]bool{}
	roll := func(limitMode, resetMode string, resetDay int, nextResetAt *string, disabled *bool, disabledReason *string) bool {
		if limitMode != "monthly" || *nextResetAt == "" {
			return false
		}
		next, err := time.Parse(time.RFC3339, *nextResetAt)
		if err != nil {
			return false
		}
		if now.Before(next) {
			return false
		}
		*nextResetAt = nextResetOccurrence(resetDayForMode(resetMode, resetDay, next), now).Format(time.RFC3339)
		if *disabled && *disabledReason == "traffic" {
			*disabled = false
			*disabledReason = ""
			needApply = true
		}
		return true
	}
	for i := range s.cfg.SubUsers {
		u := &s.cfg.SubUsers[i]
		if u.TrafficLimitBytes <= 0 {
			continue
		}
		if roll(u.TrafficLimitMode, u.ResetMode, u.ResetDay, &u.NextResetAt, &u.Disabled, &u.DisabledReason) {
			key := userSubKey("subuser", u.ID)
			rolled[key] = true
			rec := s.userTraffic.get(key)
			s.userTraffic.mu.Lock()
			rec.UpPeriod, rec.DownPeriod = 0, 0
			rec.PeriodID++
			s.userTraffic.saveLocked()
			s.userTraffic.mu.Unlock()
			u.TrafficWarned80, u.TrafficWarned100 = false, false
			u.LastResetAt = now.Format(time.RFC3339)
			changed = true
		}
	}
	for i := range s.cfg.MergedSubs {
		ms := &s.cfg.MergedSubs[i]
		if ms.TrafficLimitBytes <= 0 {
			continue
		}
		if roll(ms.TrafficLimitMode, ms.ResetMode, ms.ResetDay, &ms.NextResetAt, &ms.Disabled, &ms.DisabledReason) {
			key := userSubKey("merged", ms.ID)
			rolled[key] = true
			rec := s.userTraffic.get(key)
			s.userTraffic.mu.Lock()
			rec.UpPeriod, rec.DownPeriod = 0, 0
			rec.PeriodID++
			s.userTraffic.saveLocked()
			s.userTraffic.mu.Unlock()
			ms.TrafficWarned80, ms.TrafficWarned100 = false, false
			ms.LastResetAt = now.Format(time.RFC3339)
			changed = true
		}
	}
	// 远程面板上的转发（本面板没有对应的订阅记录，配额是管理面板下
	// 发来的）：按转发自己的 NextResetAt 翻周期。没有这一步，远程
	// 面板上被停用的转发在新周期开始后永远恢复不了（管理面板离线
	// 时尤其如此）。NextResetAt 用转发自己的 ResetMode/ResetDay
	// 往后推，与管理面板算出来的是一致的（NextResetAt 一定落在重置
	// 日上，所以 anniversary 取它的日就是重置日）。
	remoteKeys := map[string]bool{}
	for _, f := range s.cfg.UserForwards {
		if f.LimitMode != "monthly" || f.NextResetAt == "" {
			continue
		}
		if f.SubKind == "subuser" && findSubUserByID(s.cfg.SubUsers, f.SubID) != nil {
			continue
		}
		if f.SubKind == "merged" && findMergedSubByID(s.cfg.MergedSubs, f.SubID) != nil {
			continue
		}
		remoteKeys[f.Key] = true
	}
	for key := range remoteKeys {
		var rf *UserForward
		for i := range s.cfg.UserForwards {
			if s.cfg.UserForwards[i].Key == key {
				rf = &s.cfg.UserForwards[i]
				break
			}
		}
		if rf == nil {
			continue
		}
		next, err := time.Parse(time.RFC3339, rf.NextResetAt)
		if err != nil || now.Before(next) {
			continue
		}
		newNext := nextResetOccurrence(resetDayForMode(rf.ResetMode, rf.ResetDay, next), now).Format(time.RFC3339)
		for i := range s.cfg.UserForwards {
			if s.cfg.UserForwards[i].Key == key {
				s.cfg.UserForwards[i].NextResetAt = newNext
				if s.cfg.UserForwards[i].Disabled {
					s.cfg.UserForwards[i].Disabled = false
					needApply = true
				}
			}
		}
		rec := s.userTraffic.get(key)
		s.userTraffic.mu.Lock()
		rec.UpPeriod, rec.DownPeriod = 0, 0
		rec.PeriodID++
		s.userTraffic.saveLocked()
		s.userTraffic.mu.Unlock()
		changed = true
	}
	if changed {
		// 转发记录上的 NextResetAt 跟着订阅走（远程面板靠它离线执法）；
		// 翻了周期的订阅，转发上的停用标志也清掉。
		for i := range s.cfg.UserForwards {
			f := &s.cfg.UserForwards[i]
			if u := findSubUserByID(s.cfg.SubUsers, f.SubID); u != nil && f.SubKind == "subuser" {
				f.NextResetAt = u.NextResetAt
			}
			if ms := findMergedSubByID(s.cfg.MergedSubs, f.SubID); ms != nil && f.SubKind == "merged" {
				f.NextResetAt = ms.NextResetAt
			}
			if rolled[f.Key] && f.Disabled {
				f.Disabled = false
			}
		}
		_ = s.saveConfig()
	}
	if needApply {
		s.opMu.Lock()
		_ = s.applyUserForwards()
		s.opMu.Unlock()
	}
	return changed
}

func findSubUserByID(users []SubUser, id string) *SubUser {
	for i := range users {
		if users[i].ID == id {
			return &users[i]
		}
	}
	return nil
}

func findMergedSubByID(subs []MergedSub, id string) *MergedSub {
	for i := range subs {
		if subs[i].ID == id {
			return &subs[i]
		}
	}
	return nil
}

// userLimitForKey 取一个订阅的限流设置（本地订阅记录优先；远程面板上
// 只有转发记录时用转发上的）。返回 limit、mode、resetMode、resetDay、
// nextResetAt 与显示名。
func (s *Server) userLimitForKey(key string) (limit int64, mode, resetMode string, resetDay int, nextResetAt, name string, ok bool) {
	kind, id, found := strings.Cut(key, ":")
	if !found {
		return 0, "", "", 0, "", "", false
	}
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil && u.TrafficLimitBytes > 0 {
			return u.TrafficLimitBytes, u.TrafficLimitMode, u.ResetMode, u.ResetDay, u.NextResetAt, u.Name, true
		}
	} else if kind == "merged" {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil && ms.TrafficLimitBytes > 0 {
			return ms.TrafficLimitBytes, ms.TrafficLimitMode, ms.ResetMode, ms.ResetDay, ms.NextResetAt, ms.Name, true
		}
	}
	// 远程面板：只有转发记录，用它上面的配额。
	for _, f := range s.cfg.UserForwards {
		if f.Key == key && f.LimitBytes > 0 {
			return f.LimitBytes, f.LimitMode, f.ResetMode, f.ResetDay, f.NextResetAt, f.SubName, true
		}
	}
	return 0, "", "", 0, "", "", false
}

// enforceUserTrafficLimits 每分钟跑一次（trafficLoop 里，采样之后）：
// 先翻周期，再按订阅算用量——80% 发 Telegram 提醒一次，100% 停用
// 该订阅的全部转发（撤 endpoints + 重启 realm，quota 已先一步断流）。
// 管理面板模式不跑（本地没有转发）。
func (s *Server) enforceUserTrafficLimits() {
	if !s.managesLocalRelay() {
		return
	}
	if len(s.cfg.UserForwards) == 0 {
		return
	}
	s.rollUserPeriods(time.Now())
	// 去重：一个订阅多条转发只算一次
	seen := map[string]bool{}
	for _, f := range s.cfg.UserForwards {
		key := f.Key
		if seen[key] {
			continue
		}
		seen[key] = true
		limit, mode, _, _, _, name, ok := s.userLimitForKey(key)
		if !ok || limit <= 0 {
			continue
		}
		rec := s.userTraffic.get(key)
		used := userTrafficUsed(rec, mode)
		pct := float64(used) / float64(limit) * 100
		display := name
		if display == "" {
			display = key
		}
		if pct >= 80 && !s.userWarned80(key) {
			s.sendTelegram("流量提醒：订阅 " + display + " 流量已用 " + formatPct(pct) + "%")
			s.setUserWarned80(key, true)
		}
		if userOverLimit(rec, limit, mode) {
			s.disableUserForwards(key, "traffic")
			s.sendTelegram("流量用完：订阅 " + display + " 已自动停用（流量上限），续量后恢复")
			s.setUserWarned100(key, true)
		}
	}
	// quota 由同节拍跑的落地 enforceTrafficLimits 末尾统一重建
	// （ensureTrafficChains 里已含用户端口的 quota 规则），这里不
	// 再单独重建，避免一分钟内刷两次链。
}

// userWarned80 / userWarned100 提醒去重标志（存在订阅记录上）。
func (s *Server) userWarned80(key string) bool {
	kind, id, found := strings.Cut(key, ":")
	if !found {
		return false
	}
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			return u.TrafficWarned80
		}
	} else if kind == "merged" {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			return ms.TrafficWarned80
		}
	}
	return false
}

func (s *Server) userWarned100(key string) bool {
	kind, id, found := strings.Cut(key, ":")
	if !found {
		return false
	}
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			return u.TrafficWarned100
		}
	} else if kind == "merged" {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			return ms.TrafficWarned100
		}
	}
	return false
}

func (s *Server) setUserWarned80(key string, v bool) {
	kind, id, found := strings.Cut(key, ":")
	if !found {
		return
	}
	changed := false
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			u.TrafficWarned80 = v
			changed = true
		}
	} else if kind == "merged" {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			ms.TrafficWarned80 = v
			changed = true
		}
	}
	if changed {
		_ = s.saveConfig()
	}
}

func (s *Server) setUserWarned100(key string, v bool) {
	kind, id, found := strings.Cut(key, ":")
	if !found {
		return
	}
	changed := false
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			u.TrafficWarned100 = v
			changed = true
		}
	} else if kind == "merged" {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			ms.TrafficWarned100 = v
			changed = true
		}
	}
	if changed {
		_ = s.saveConfig()
	}
}

// disableUserForwards 停用一个订阅的全部转发（超量/到期）：把 endpoints
// 从 config.toml 撤掉并重启 realm，同时把订阅与转发记成已停用。
// 自己加 opMu（与 disableLanding 同一套路），不要在已持锁的调用链里调。
func (s *Server) disableUserForwards(key, reason string) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.disableUserForwardsLocked(key, reason)
}

// disableUserForwardsLocked 同上，不加锁（调用方已持 opMu）。
func (s *Server) disableUserForwardsLocked(key, reason string) {
	kind, id, found := strings.Cut(key, ":")
	if !found {
		return
	}
	changed := false
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil && !u.Disabled {
			u.Disabled = true
			u.DisabledReason = reason
			changed = true
		}
	} else if kind == "merged" {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil && !ms.Disabled {
			ms.Disabled = true
			ms.DisabledReason = reason
			changed = true
		}
	}
	for i := range s.cfg.UserForwards {
		if s.cfg.UserForwards[i].Key == key && !s.cfg.UserForwards[i].Disabled {
			s.cfg.UserForwards[i].Disabled = true
			changed = true
		}
	}
	if !changed {
		return
	}
	_ = s.saveConfig()
	_ = s.applyUserForwards()
	s.audit("system", "自动停用订阅", key, "成功", "原因："+reason)
}

// userUsageSummary 给列表行用的用量摘要：used 是按模式算的已用字节
// （monthly 用本期，total 用累计），limit 是上限字节；ok 表示本面板
// 上有该订阅的用量记录（没有时列表不显示用量，只显示上限）。
// 管理面板模式：本地没有用量时用聚合循环汇总的各面板用量（缓存，
// 每分钟刷新）；缓存也没有时只显示上限。
func (s *Server) userUsageSummary(key string, limitBytes int64, limitMode string) (used, limit int64, ok bool) {
	if limitBytes <= 0 {
		return 0, 0, false
	}
	up, down, upPeriod, downPeriod, has := s.userTraffic.totals(key)
	if has {
		if limitMode == "monthly" {
			return upPeriod + downPeriod, limitBytes, true
		}
		return up + down, limitBytes, true
	}
	s.centralUserUsageMu.Lock()
	cu := s.centralUserUsage[key]
	s.centralUserUsageMu.Unlock()
	if cu == nil {
		return 0, limitBytes, false
	}
	if limitMode == "monthly" {
		return cu.UpPeriod + cu.DownPeriod, limitBytes, true
	}
	return cu.UpTotal + cu.DownTotal, limitBytes, true
}

// syncUserForwardsWarning 创建/编辑订阅后调：先核对本机转发，再同步
// 到勾选里的各远程面板，返回警告文案（空 = 全成功）。本机转发失败
// 时远程照常同步（两边端口段独立），警告里分开写。
// 注意：本机核对持 opMu（要动 config.toml 与 realm），远程同步不持
// （只改 config.json 里的远程转发记录 + 调网络，持锁会把一次 15 秒
// 的远程调用变成全面板写锁）；与本地核对的并发写 config.json 竞争
// 是订阅处理器本来就有的（它们不拿 opMu），丢一条转发记录时下次
// 保存会重新对上，是自愈的。
func (s *Server) syncUserForwardsWarning(kind, id, name string, sel []SubSelection, limitBytes int64, limitMode, resetMode string, resetDay int, nextResetAt string) string {
	if limitBytes <= 0 {
		return ""
	}
	var warnings []string
	s.opMu.Lock()
	recErr := s.reconcileUserForwards(kind, id, name, sel, limitBytes, limitMode, resetMode, resetDay, nextResetAt)
	s.opMu.Unlock()
	if recErr != nil {
		warnings = append(warnings, "本机转发没建起来："+recErr.Error())
	}
	warnings = append(warnings, s.syncUserForwardsToRemotes(userSubKey(kind, id), name, sel, limitBytes, limitMode, resetMode, resetDay, nextResetAt)...)
	if len(warnings) == 0 {
		return ""
	}
	return "订阅已保存，但" + strings.Join(warnings, "；") + "。没建起来的那部分暂时走共享端口、不限流，请处理后重新保存一次。"
}

// clearUserDisabled 解除一个订阅的停用（编辑调高上限、周期翻转后调用）：
// 订阅记录与转发记录上的停用标志都清掉，下次 apply 时 endpoints 加
// 回去。只改内存里的配置字段，保存由调用方负责。
func (s *Server) clearUserDisabled(key string) {
	kind, id, found := strings.Cut(key, ":")
	if !found {
		return
	}
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			u.Disabled = false
			u.DisabledReason = ""
			u.TrafficWarned100 = false
		}
	} else if kind == "merged" {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			ms.Disabled = false
			ms.DisabledReason = ""
			ms.TrafficWarned100 = false
		}
	}
	for i := range s.cfg.UserForwards {
		if s.cfg.UserForwards[i].Key == key {
			s.cfg.UserForwards[i].Disabled = false
		}
	}
}

// maybeResumeUser 编辑时调高上限后调用：如果该订阅之前因超量停用，
// 而新上限大于当前已用，就解除停用（订阅记录与转发记录都清），
// 让接下来的 reconcile 把 endpoints 加回去；仍超量就继续停用。
// 只改内存，保存与 reconcile 由调用方负责。
func (s *Server) maybeResumeUser(key string, limitBytes int64, limitMode string) bool {
	kind, id, found := strings.Cut(key, ":")
	if !found || limitBytes <= 0 {
		return false
	}
	disabled := false
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			disabled = u.Disabled && u.DisabledReason == "traffic"
		}
	} else if kind == "merged" {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
			disabled = ms.Disabled && ms.DisabledReason == "traffic"
		}
	}
	if !disabled {
		return false
	}
	rec := s.userTraffic.get(key)
	if userTrafficUsed(rec, limitMode) >= limitBytes {
		return false
	}
	s.clearUserDisabled(key)
	return true
}

// resumeUserForwards 恢复一个订阅的转发（续量/手动恢复）：清掉停用标志，
// 把 endpoints 加回去并重启 realm。自己加 opMu。
func (s *Server) resumeUserForwards(key string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.resumeUserForwardsLocked(key)
}

// resumeUserForwardsLocked 同上，不加锁（调用方已持 opMu）。
func (s *Server) resumeUserForwardsLocked(key string) error {
	kind, id, found := strings.Cut(key, ":")
	if !found {
		return fmt.Errorf("订阅标识不对")
	}
	changed := false
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil && u.Disabled {
			u.Disabled = false
			u.DisabledReason = ""
			u.TrafficWarned100 = false
			changed = true
		}
	} else if kind == "merged" {
		if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil && ms.Disabled {
			ms.Disabled = false
			ms.DisabledReason = ""
			ms.TrafficWarned100 = false
			changed = true
		}
	}
	for i := range s.cfg.UserForwards {
		if s.cfg.UserForwards[i].Key == key && s.cfg.UserForwards[i].Disabled {
			s.cfg.UserForwards[i].Disabled = false
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := s.saveConfig(); err != nil {
		return err
	}
	return s.applyUserForwards()
}

// ---------- 订阅内容改写与 userinfo ----------

// rewriteSubLinksForUser 把订阅里的中转节点链接改成该用户的端口：
// 名字以“-中转”结尾的才改（直连不动）；portByAddr 是落地地址 →
// 用户端口。链接的 host（中转地址）不动，只换端口。
func rewriteSubLinksForUser(links []subLink, portByAddr map[string]int) []subLink {
	if len(portByAddr) == 0 {
		return links
	}
	out := make([]subLink, len(links))
	copy(out, links)
	for i, l := range out {
		if !strings.HasSuffix(l.Name, "-中转") {
			continue
		}
		port, ok := portByAddr[l.LandingAddr]
		if !ok || port <= 0 {
			continue
		}
		out[i].Link = setLinkPort(l.Link, port)
	}
	return out
}

// subUserinfoHeaderForUser 给开了限流的订阅拼 subscription-userinfo：
// upload/download 是该订阅本面板上的用量（monthly 用本期，total 用
// 累计），total 是该订阅的流量上限，expire 取令牌到期与落地到期中
// 更早的。本面板上没有该订阅的用量记录（例如管理面板模式）时返回
// ""，调用方回落到原来的落地汇总头。
func (s *Server) subUserinfoHeaderForUser(key string, limitBytes int64, limitMode, tokenExpiry string, localIndices map[int]bool) string {
	up, down, upPeriod, downPeriod, ok := s.userTraffic.totals(key)
	if !ok {
		return ""
	}
	var usedUp, usedDown int64
	if limitMode == "monthly" {
		usedUp, usedDown = upPeriod, downPeriod
	} else {
		usedUp, usedDown = up, down
	}
	earliest := s.earliestExpiry(localIndices, tokenExpiry)
	var parts []string
	parts = append(parts, "upload="+strconv.FormatInt(usedUp, 10), "download="+strconv.FormatInt(usedDown, 10))
	if limitBytes > 0 {
		parts = append(parts, "total="+strconv.FormatInt(limitBytes, 10))
	}
	if !earliest.IsZero() {
		parts = append(parts, "expire="+strconv.FormatInt(earliest.Unix(), 10))
	}
	return strings.Join(parts, "; ")
}

// earliestExpiry 取令牌到期与指定落地到期里最早的那个（都没有返回零值）。
func (s *Server) earliestExpiry(localIndices map[int]bool, tokenExpiry string) time.Time {
	var earliest time.Time
	if tokenExpiry != "" {
		if t, err := time.Parse(time.RFC3339, tokenExpiry); err == nil {
			earliest = t
		}
	}
	rc, _, err := s.readRelayConf()
	if err != nil {
		return earliest
	}
	s.stateMu.Lock()
	exts := map[string]*LandingExt{}
	for k, v := range s.state.Landings {
		exts[k] = v
	}
	s.stateMu.Unlock()
	for _, l := range rc.Landings {
		if localIndices != nil && !localIndices[l.Index] {
			continue
		}
		if ext := exts[l.Landing]; ext != nil && ext.Expiry != "" {
			if t, err := time.Parse(time.RFC3339, ext.Expiry); err == nil {
				if earliest.IsZero() || t.Before(earliest) {
					earliest = t
				}
			}
		}
	}
	return earliest
}
