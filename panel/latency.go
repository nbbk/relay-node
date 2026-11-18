package main

// ---------- 落地延迟探测（v1.1.11） ----------
// 后台定时对每个落地做 TCP 连接测延迟：只做 TCP 握手，不发任何应用层数据，
// 不会干扰 TLS / Xray。探测目标是落地地址:落地端口（中转配置里的转发目标），
// 不是中转机本地监听端口——只有这样才能真实反映落地在线与否；
// 任意门模式的落地同样探测（目标仍是落地地址:落地端口）。
// 结果存在面板自己的 latency.json 里（与流量统计的 traffic.json 分开存、
// 分开算），重启面板后读回，不丢上次状态。
// 离线与恢复走现有 Telegram 提醒，复用 1.1.10 监控告警的去重节流：
// 同一落地离线只提醒一次，持续离线每小时最多再提醒一次，恢复后复位并报恢复。
// 离线/恢复记审计日志。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ---------- 配置（存在 config.json 的 latency 一节） ----------

type LatencyCfg struct {
	Enabled     bool `json:"enabled"`
	IntervalMin int  `json:"interval_min"`
	TimeoutSec  int  `json:"timeout_sec"` // 固定 3 秒，设置页不开放改
}

// 默认：开启，间隔 5 分钟，超时 3 秒。
func defaultLatencyCfg() LatencyCfg {
	return LatencyCfg{Enabled: true, IntervalMin: 5, TimeoutSec: 3}
}

// applyDefaults 只补“整节缺失”的情况（1.1.11 之前装的面板，config.json 里
// 没有 latency 一节时走这里，全 0 值 = 缺失）；用户填过的值不动。
// 注意：用户关掉探测后保存的配置里 IntervalMin 恒 >= 1（validate 会钳制），
// 所以不会误判成缺失。
func (c *LatencyCfg) applyDefaults() {
	if !c.Enabled && c.IntervalMin == 0 && c.TimeoutSec == 0 {
		*c = defaultLatencyCfg()
		return
	}
	if c.IntervalMin == 0 {
		c.IntervalMin = 5
	}
	if c.TimeoutSec == 0 {
		c.TimeoutSec = 3
	}
}

// validate 校验设置页提交的值：间隔钳制到 1–60 分钟，超时钳制到 1–10 秒
// （超范围就地钳制，不报错；超时设置页不开放，正常恒为 3）。
func (c *LatencyCfg) validate() {
	if c.IntervalMin < 1 {
		c.IntervalMin = 1
	}
	if c.IntervalMin > 60 {
		c.IntervalMin = 60
	}
	if c.TimeoutSec < 1 {
		c.TimeoutSec = 1
	}
	if c.TimeoutSec > 10 {
		c.TimeoutSec = 10
	}
}

// latencyEnabled 探测是否开启（配置缺失时默认开，见 applyDefaults）。
func (s *Server) latencyEnabled() bool {
	return s.cfg.Latency.Enabled
}

// latencyTimeout 探测超时（秒），非法值回落到 3 秒。
func (s *Server) latencyTimeout() time.Duration {
	sec := s.cfg.Latency.TimeoutSec
	if sec < 1 || sec > 10 {
		sec = 3
	}
	return time.Duration(sec) * time.Second
}

// ---------- 探测结果与持久化（PANEL_HOME/latency.json） ----------

type LatencyResult struct {
	Landing   string `json:"landing"`
	Target    string `json:"target"`     // 探测目标，如 203.0.113.1:443
	Online    bool   `json:"online"`     // 在线 / 离线
	LatencyMs *int   `json:"latency_ms"` // 在线时为毫秒数，离线时为 null
	ErrorKind string `json:"error_kind"` // "" / timeout / refused / dns / failed
	Error     string `json:"error"`      // 中文失败原因，在线时为空
	CheckedAt string `json:"checked_at"` // RFC3339，上次探测时间
}

type latencyStore struct {
	mu   sync.Mutex
	path string
	data map[string]*LatencyResult
}

func newLatencyStore(path string) *latencyStore {
	return &latencyStore{path: path, data: map[string]*LatencyResult{}}
}

func (t *latencyStore) load() {
	b, err := os.ReadFile(t.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &t.data)
	if t.data == nil {
		t.data = map[string]*LatencyResult{}
	}
}

func (t *latencyStore) saveLocked() {
	b, _ := json.MarshalIndent(t.data, "", "  ")
	_ = writeFileAtomic(t.path, b, 0o600)
}

func (t *latencyStore) save() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.saveLocked()
}

// get 取一份拷贝（没有结果时返回 nil）；调用方改拷贝不影响库存。
// key 是 latencyStoreKey 算出的“落地|探测目标”，同一 IP、不同端口的
// 两个落地各有各的结果，不会互相覆盖。
func (t *latencyStore) get(key string) *LatencyResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.data[key]
	if !ok || r == nil {
		return nil
	}
	cp := *r
	return &cp
}

func (t *latencyStore) set(key string, r *LatencyResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := *r
	t.data[key] = &cp
	t.saveLocked()
}

// remove 删掉指定键的结果（编辑改地址后旧探测目标的结果不留着占地方，
// 新地址下一轮探测会重新生成）。
func (t *latencyStore) remove(key string) {
	if key == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.data, key)
	t.saveLocked()
}

// prune 删掉不在 keep 里的结果（落地删了之后旧结果不留着占地方）。
func (t *latencyStore) prune(keep map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.data {
		if !keep[k] {
			delete(t.data, k)
		}
	}
	t.saveLocked()
}

// latencyStoreKey 一个落地的结果键：落地地址|探测目标。
// 同一 IP、不同端口的两个落地键不同，各有各的结果；
// 没有 TCP 端口的落地不探测，键为空串。
func latencyStoreKey(l Landing) string {
	host, port, ok := probeTarget(l)
	if !ok {
		return ""
	}
	return l.Landing + "|" + net.JoinHostPort(host, fmt.Sprintf("%d", port))
}

// ---------- 探测目标 ----------

// probeTarget 取探测目标（落地地址:落地端口）：优先 TCP 的 443 落地端口
// （节点主端口），没有 443 就取第一个 TCP 端口的落地端口；
// 只有 UDP 端口的落地（如纯 HY2/TUIC）没有 TCP 可握手，返回 ok=false，
// 这种落地不探测、不记离线（TCP 测不了 UDP，硬测只会误报）。
func probeTarget(l Landing) (host string, port int, ok bool) {
	for _, p := range l.Ports {
		if p.Proto == "tcp" && p.Remote == 443 {
			return l.Landing, p.Remote, true
		}
	}
	for _, p := range l.Ports {
		if p.Proto == "tcp" {
			return l.Landing, p.Remote, true
		}
	}
	return "", 0, false
}

// ---------- 失败原因分类 ----------

// classifyProbeError 把拨号错误分成超时 / 拒绝连接 / DNS 解析失败 / 其他连接失败
// 四类，便于排障：超时多半是落地机不通或防火墙丢包，拒绝连接多半是落地机上
// 节点没跑起来，DNS 失败多半是落地填的域名解析出了问题。
func classifyProbeError(err error, timeout time.Duration) (kind, msg string) {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout", fmt.Sprintf("连接超时（%d 秒无响应）", int(timeout.Seconds()))
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns", "DNS 解析失败：" + dnsErr.Error()
	}
	if strings.Contains(err.Error(), "connection refused") {
		return "refused", "连接被拒绝"
	}
	return "failed", "连接失败：" + err.Error()
}

// probeDial 拨号函数（包级变量，单测可替换成注入错误的假拨号；
// 真机走 net.DialTimeout）。
var probeDial = net.DialTimeout

// probeOne 对一个落地做一次 TCP 握手测延迟：只 Dial + 立刻 Close，
// 不发任何应用层数据。返回结果（含失败原因分类）。
func probeOne(l Landing, timeout time.Duration) *LatencyResult {
	host, port, ok := probeTarget(l)
	if !ok {
		return nil
	}
	target := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	start := time.Now()
	conn, err := probeDial("tcp", target, timeout)
	now := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		kind, msg := classifyProbeError(err, timeout)
		return &LatencyResult{Landing: l.Landing, Target: target, Online: false, ErrorKind: kind, Error: msg, CheckedAt: now}
	}
	// 握手成功立刻关闭，不发任何数据
	_ = conn.Close()
	ms := int(time.Since(start).Milliseconds())
	return &LatencyResult{Landing: l.Landing, Target: target, Online: true, LatencyMs: &ms, CheckedAt: now}
}

// probeAllLandings 并发探测全部落地：每个落地一个 goroutine，各自带超时，
// 互不阻塞；总耗时上限 ≈ 单次超时（默认 3 秒）+ 一点调度开销，
// 单个落地探测失败绝不拖垮整轮探测。没有 TCP 端口的落地跳过。
func probeAllLandings(landings []Landing, timeout time.Duration) []*LatencyResult {
	var wg sync.WaitGroup
	out := make([]*LatencyResult, len(landings))
	for i, l := range landings {
		wg.Add(1)
		go func(i int, l Landing) {
			defer wg.Done()
			out[i] = probeOne(l, timeout)
		}(i, l)
	}
	wg.Wait()
	var res []*LatencyResult
	for _, r := range out {
		if r != nil {
			res = append(res, r)
		}
	}
	return res
}

// ---------- 一轮探测 + 告警去重节流 ----------

type latencyAlertState struct {
	Online   bool // 上次已知状态（默认在线：首次探测就离线也会提醒一次）
	LastSent time.Time
}

// evaluateLatency 按一轮探测结果更新各落地告警状态：
// 首次离线（在线→离线）记审计 + 发 Telegram；持续离线满 1 小时只发 Telegram
// 提醒一次（不记审计，避免刷屏）；恢复（离线→在线）记审计 + 发 Telegram 报恢复；
// 下次再离线重新提醒。没配 Telegram 时 sendTelegram 本身就是空操作。
// 锁内只收集要发的动作，锁外再发 Telegram / 记审计，避免发消息时阻塞状态读取。
func (s *Server) evaluateLatency(results []*LatencyResult, now time.Time) {
	type action struct {
		landing string
		result  string // 离线 / 恢复
		detail  string
		tg      string // 为空 = 只记审计不发 Telegram（持续离线的每小时提醒只发 Telegram，不记审计）
		audit   bool
	}
	var acts []action
	s.latencyMu.Lock()
	for _, r := range results {
		// 告警状态按“落地|探测目标”记（与结果键一致），同一 IP、不同端口的
		// 两个落地各记各的离线状态
		key := r.Landing + "|" + r.Target
		prev, ok := s.latencyAlerts[key]
		if !ok || prev == nil {
			prev = &latencyAlertState{Online: true}
			s.latencyAlerts[key] = prev
		}
		if !r.Online {
			if prev.Online {
				acts = append(acts, action{
					landing: r.Landing, result: "离线", audit: true,
					detail: fmt.Sprintf("探测目标 %s 失败：%s", r.Target, r.Error),
					tg:     fmt.Sprintf("落地离线：%s 探测失败（目标 %s，%s），请检查落地机节点是否在跑", r.Landing, r.Target, r.Error),
				})
				prev.Online = false
				prev.LastSent = now
			} else if now.Sub(prev.LastSent) >= time.Hour {
				// 同一落地持续离线：满 1 小时再提醒一次（只发 Telegram，不记审计）
				acts = append(acts, action{
					landing: r.Landing, audit: false,
					tg: fmt.Sprintf("落地仍离线：%s 已持续离线 1 小时（目标 %s，%s）", r.Landing, r.Target, r.Error),
				})
				prev.LastSent = now
			}
			continue
		}
		if !prev.Online {
			ms := 0
			if r.LatencyMs != nil {
				ms = *r.LatencyMs
			}
			acts = append(acts, action{
				landing: r.Landing, result: "恢复", audit: true,
				detail: fmt.Sprintf("探测目标 %s 已通，延迟 %d ms", r.Target, ms),
				tg:     fmt.Sprintf("落地恢复：%s 已恢复在线（目标 %s，延迟 %d ms）", r.Landing, r.Target, ms),
			})
			prev.Online = true
			prev.LastSent = now
		}
	}
	s.latencyMu.Unlock()
	for _, a := range acts {
		if a.audit {
			s.audit("system", "延迟探测", a.landing, a.result, a.detail)
		}
		if a.tg != "" {
			s.sendTelegram(a.tg)
		}
	}
}

// probeLatencyRoundLocked 真正跑一轮探测（调用方须持有 latencyRoundMu）：
// 读 relay.conf → 并发探测 → 存 latency.json → 评估告警。
// 探测关了或 relay.conf 读不到时直接返回（不报错、不拖垮面板）。
func (s *Server) probeLatencyRoundLocked() {
	if !s.latencyEnabled() {
		return
	}
	rc, _, err := s.readRelayConf()
	if err != nil || rc == nil {
		return
	}
	results := probeAllLandings(rc.Landings, s.latencyTimeout())
	keep := map[string]bool{}
	for _, r := range results {
		key := r.Landing + "|" + r.Target
		keep[key] = true
		s.latency.set(key, r)
	}
	// 落地删了之后旧结果不留着占地方
	s.latency.prune(keep)
	s.evaluateLatency(results, time.Now())
}

// probeLatencyRound 跑一轮探测（后台循环与手动触发共用）。
func (s *Server) probeLatencyRound() {
	s.latencyRoundMu.Lock()
	defer s.latencyRoundMu.Unlock()
	s.probeLatencyRoundLocked()
}

// ensureLatencyProbed 兜底：总览打开时若有落地还没有探测结果（刚加的落地、
// 刚开探测），立刻补跑一轮（有 3 秒超时上限），保证总览能看到数；
// 已有全部结果时什么都不做；已有另一轮在跑时也不重复跑。
func (s *Server) ensureLatencyProbed() {
	if !s.latencyEnabled() {
		return
	}
	rc, _, err := s.readRelayConf()
	if err != nil || rc == nil {
		return
	}
	need := false
	for _, l := range rc.Landings {
		if _, _, ok := probeTarget(l); !ok {
			continue
		}
		if s.latency.get(latencyStoreKey(l)) == nil {
			need = true
			break
		}
	}
	if !need {
		return
	}
	if !s.latencyRoundMu.TryLock() {
		return
	}
	defer s.latencyRoundMu.Unlock()
	s.probeLatencyRoundLocked()
}

// ---------- 后台探测循环 ----------

func (s *Server) latencyLoop() {
	s.probeLatencyRound()
	for {
		interval := s.cfg.Latency.IntervalMin
		if interval < 1 || interval > 60 {
			interval = 5
		}
		time.Sleep(time.Duration(interval) * time.Minute)
		s.probeLatencyRound()
	}
}

// ---------- 接口 ----------

// GET /api/v1/latency：当前探测配置 + 每个落地的上次探测结果。
// 还没探测过的落地（刚加的、刚开探测的）当场补跑一轮，保证打开就能看到数。
func (s *Server) handleLatency(w http.ResponseWriter, r *http.Request) {
	s.ensureLatencyProbed()
	rc, _, _ := s.readRelayConf()
	var results []any
	if rc != nil {
		for _, l := range rc.Landings {
			if r := s.latency.get(latencyStoreKey(l)); r != nil {
				results = append(results, r)
				continue
			}
			// 没结果：没 TCP 端口的落地标“不探测”，其余标“尚未探测”
			if _, _, ok := probeTarget(l); !ok {
				results = append(results, map[string]any{
					"landing": l.Landing, "target": "", "online": nil, "latency_ms": nil,
					"error_kind": "not_probed", "error": "该落地没有 TCP 端口，不探测", "checked_at": "",
				})
			} else {
				results = append(results, map[string]any{
					"landing": l.Landing, "target": "", "online": nil, "latency_ms": nil,
					"error_kind": "not_probed", "error": "尚未探测", "checked_at": "",
				})
			}
		}
	}
	if results == nil {
		results = []any{}
	}
	writeJSON(w, 200, okResp(map[string]any{"latency": s.cfg.Latency, "results": results}))
}

// POST /api/v1/settings/latency：改探测开关与间隔，保存后立即生效
// （下次探测按新间隔走；开启时当场补跑一轮，新结果立刻能在总览看到）。
func (s *Server) handleLatencySettings(w http.ResponseWriter, r *http.Request) {
	var req LatencyCfg
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	// 超时固定 3 秒：设置页不开放，前端不传时保持原值，不被 0 覆盖
	if req.TimeoutSec == 0 {
		req.TimeoutSec = s.cfg.Latency.TimeoutSec
		if req.TimeoutSec == 0 {
			req.TimeoutSec = 3
		}
	}
	req.validate()
	s.cfg.Latency = req
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	s.audit(s.currentUser(r), "设置延迟探测", "", "成功",
		fmt.Sprintf("探测%s，间隔 %d 分钟，超时 %d 秒",
			map[bool]string{true: "开启", false: "关闭"}[req.Enabled], req.IntervalMin, req.TimeoutSec))
	if req.Enabled {
		// 同步补跑一轮：单次探测最多 3 秒，并发做，总耗时有上限；
		// 保证保存后下一次打开总览立刻看到新结果。
		s.probeLatencyRound()
	}
	writeJSON(w, 200, okResp(map[string]any{"latency": req}))
}
