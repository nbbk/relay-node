package main

// ---------- 机器状态监控告警（v1.1.10） ----------
// 每 5 分钟（可改）采样一次根分区磁盘、内存、CPU（1 分钟平均负载 ÷ 核心数），
// 超阈值时面板标红/标黄，并走现有 Telegram 提醒（没配 Telegram 就只标色）。
// 同一指标越过阈值只提醒一次，持续超标每小时最多再提醒一次，
// 回落到阈值以下后状态复位，下次再越过重新提醒。事件记审计日志。

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ---------- 阈值配置（存在 config.json 的 monitor 一节） ----------

type MonitorCfg struct {
	DiskWarn    int `json:"disk_warn"`
	DiskCrit    int `json:"disk_crit"`
	MemWarn     int `json:"mem_warn"`
	MemCrit     int `json:"mem_crit"`
	CPUWarn     int `json:"cpu_warn"`
	CPUCrit     int `json:"cpu_crit"`
	IntervalMin int `json:"interval_min"`
}

// 默认阈值：磁盘 85%/95%（任务定）；内存 80%/90%（90% 再往上就快 OOM 了，
// 提前告警才有时间清日志/腾空间）；CPU 80%/95%（1 分钟平均负载 ÷ 核心数换算成百分比）。
// 采样间隔默认 5 分钟，可填 1–60 分钟。
func defaultMonitorCfg() MonitorCfg {
	return MonitorCfg{DiskWarn: 85, DiskCrit: 95, MemWarn: 80, MemCrit: 90, CPUWarn: 80, CPUCrit: 95, IntervalMin: 5}
}

// applyDefaults 只补 0 值（旧 config.json 里没有 monitor 一节时走这里）；
// 用户填过的值不动。
func (c *MonitorCfg) applyDefaults() {
	d := defaultMonitorCfg()
	if c.DiskWarn == 0 {
		c.DiskWarn = d.DiskWarn
	}
	if c.DiskCrit == 0 {
		c.DiskCrit = d.DiskCrit
	}
	if c.MemWarn == 0 {
		c.MemWarn = d.MemWarn
	}
	if c.MemCrit == 0 {
		c.MemCrit = d.MemCrit
	}
	if c.CPUWarn == 0 {
		c.CPUWarn = d.CPUWarn
	}
	if c.CPUCrit == 0 {
		c.CPUCrit = d.CPUCrit
	}
	if c.IntervalMin == 0 {
		c.IntervalMin = d.IntervalMin
	}
}

// validate 校验设置页提交的值：阈值须在 1–99 之间且警告 < 严重；
// 采样间隔钳制到 1–60 分钟（超范围就地钳制，不报错）。
func (c *MonitorCfg) validate() error {
	for _, f := range []struct {
		name string
		v    int
	}{
		{"磁盘警告阈值", c.DiskWarn}, {"磁盘严重阈值", c.DiskCrit},
		{"内存警告阈值", c.MemWarn}, {"内存严重阈值", c.MemCrit},
		{"CPU 警告阈值", c.CPUWarn}, {"CPU 严重阈值", c.CPUCrit},
	} {
		if f.v < 1 || f.v > 99 {
			return fmt.Errorf("%s请填 1–99", f.name)
		}
	}
	if c.DiskWarn >= c.DiskCrit {
		return fmt.Errorf("磁盘警告阈值必须小于严重阈值")
	}
	if c.MemWarn >= c.MemCrit {
		return fmt.Errorf("内存警告阈值必须小于严重阈值")
	}
	if c.CPUWarn >= c.CPUCrit {
		return fmt.Errorf("CPU 警告阈值必须小于严重阈值")
	}
	if c.IntervalMin < 1 {
		c.IntervalMin = 1
	}
	if c.IntervalMin > 60 {
		c.IntervalMin = 60
	}
	return nil
}

// ---------- 采样 ----------

type SystemStats struct {
	DiskPct        float64 `json:"disk_pct"`
	DiskTotalBytes int64   `json:"disk_total_bytes"`
	DiskUsedBytes  int64   `json:"disk_used_bytes"`
	DiskFreeBytes  int64   `json:"disk_free_bytes"`
	MemPct         float64 `json:"mem_pct"`
	MemTotalBytes  int64   `json:"mem_total_bytes"`
	MemAvailBytes  int64   `json:"mem_avail_bytes"`
	CPUPct         float64 `json:"cpu_pct"`
	CPULoad1       float64 `json:"cpu_load1"`
	CPUCores       int     `json:"cpu_cores"`
	SampledAt      string  `json:"sampled_at"`
}

// parseMeminfo 解析 /proc/meminfo，返回总量与可用字节数（MemTotal / MemAvailable，单位 kB）。
func parseMeminfo(content string) (total, avail int64) {
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	return total, avail
}

// parseLoadavg 解析 /proc/loadavg，返回 1 分钟平均负载。
func parseLoadavg(content string) float64 {
	f := strings.Fields(content)
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// sampleSystem 一次采样：磁盘走 syscall.Statfs("/")（根分区），
// 内存走 /proc/meminfo，CPU 走 /proc/loadavg 的 1 分钟负载 ÷ 核心数。
// 哪项读不到就留 0，不让一次采样失败拖垮整个监控。
func sampleSystem() SystemStats {
	st := SystemStats{SampledAt: time.Now().UTC().Format(time.RFC3339)}
	var fs syscall.Statfs_t
	if err := syscall.Statfs("/", &fs); err == nil && fs.Blocks > 0 {
		st.DiskTotalBytes = int64(fs.Blocks) * int64(fs.Bsize)
		st.DiskFreeBytes = int64(fs.Bavail) * int64(fs.Bsize)
		st.DiskUsedBytes = st.DiskTotalBytes - st.DiskFreeBytes
		if st.DiskTotalBytes > 0 {
			st.DiskPct = float64(st.DiskUsedBytes) / float64(st.DiskTotalBytes) * 100
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		total, avail := parseMeminfo(string(b))
		st.MemTotalBytes = total
		st.MemAvailBytes = avail
		if total > 0 {
			st.MemPct = float64(total-avail) / float64(total) * 100
		}
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		st.CPULoad1 = parseLoadavg(string(b))
		st.CPUCores = runtime.NumCPU()
		if st.CPUCores > 0 {
			st.CPUPct = st.CPULoad1 / float64(st.CPUCores) * 100
		}
	}
	return st
}

// formatBytes 字节数转人类可读（GB 保留 2 位小数，其余整数）。
func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n)
	i := -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}

// ---------- 阈值判定与告警去重 ----------

// alertLevel 按阈值定级别：>= 严重阈值 = critical，>= 警告阈值 = warn，其余 ok。
func alertLevel(v float64, warn, crit int) string {
	if v >= float64(crit) {
		return "critical"
	}
	if v >= float64(warn) {
		return "warn"
	}
	return "ok"
}

type monitorAlertState struct {
	Level    string
	LastSent time.Time
}

type monitorMetric struct {
	Key    string // disk / mem / cpu
	Label  string // 磁盘 / 内存 / CPU
	Value  float64
	Warn   int
	Crit   int
	Detail string // 告警文案里带的补充信息（如根分区可用空间）
}

func (s *Server) monitorMetrics(st SystemStats) []monitorMetric {
	c := s.cfg.Monitor
	return []monitorMetric{
		{Key: "disk", Label: "磁盘", Value: st.DiskPct, Warn: c.DiskWarn, Crit: c.DiskCrit,
			Detail: fmt.Sprintf("根分区共 %s，可用 %s", formatBytes(st.DiskTotalBytes), formatBytes(st.DiskFreeBytes))},
		{Key: "mem", Label: "内存", Value: st.MemPct, Warn: c.MemWarn, Crit: c.MemCrit,
			Detail: fmt.Sprintf("共 %s，可用 %s", formatBytes(st.MemTotalBytes), formatBytes(st.MemAvailBytes))},
		{Key: "cpu", Label: "CPU", Value: st.CPUPct, Warn: c.CPUWarn, Crit: c.CPUCrit,
			Detail: fmt.Sprintf("1 分钟平均负载 %.2f / %d 核", st.CPULoad1, st.CPUCores)},
	}
}

func levelText(level string) string {
	switch level {
	case "critical":
		return "严重"
	case "warn":
		return "警告"
	default:
		return "正常"
	}
}

func formatPct1(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64) + "%"
}

// evaluateMonitorAt 按一次采样结果更新各指标告警状态：
// 触发（ok→warn/critical）、升级（warn→critical，也含 critical→warn 的降级）、
// 回落恢复（→ok）都记审计日志并发 Telegram；同一级别持续超标时，
// 距上次提醒满 1 小时才再发一条 Telegram（不记审计，避免刷屏）。
// 没配 Telegram 时 sendTelegram 本身就是空操作，只标色、不报错。
func (s *Server) evaluateMonitorAt(st SystemStats, now time.Time) {
	s.monitorMu.Lock()
	defer s.monitorMu.Unlock()
	for _, m := range s.monitorMetrics(st) {
		level := alertLevel(m.Value, m.Warn, m.Crit)
		prev, ok := s.monitorAlerts[m.Key]
		if !ok || prev == nil {
			prev = &monitorAlertState{Level: "ok"}
			s.monitorAlerts[m.Key] = prev
		}
		if level == "ok" {
			if prev.Level != "ok" {
				s.audit("system", "监控告警", m.Label, "恢复",
					fmt.Sprintf("%s使用率回落到 %s（警告阈值 %d%%），已恢复正常", m.Label, formatPct1(m.Value), m.Warn))
				s.sendTelegram(fmt.Sprintf("监控恢复：%s使用率已回落到 %s（警告阈值 %d%%），恢复正常", m.Label, formatPct1(m.Value), m.Warn))
				prev.Level = "ok"
				prev.LastSent = now
			}
			continue
		}
		if prev.Level != level {
			th := m.Warn
			if level == "critical" {
				th = m.Crit
			}
			s.audit("system", "监控告警", m.Label, levelText(level),
				fmt.Sprintf("%s使用率 %s 超过%s阈值 %d%%（%s）", m.Label, formatPct1(m.Value), levelText(level), th, m.Detail))
			s.sendTelegram(fmt.Sprintf("监控告警：%s使用率 %s 已超过%s阈值 %d%%（%s）", m.Label, formatPct1(m.Value), levelText(level), th, m.Detail))
			prev.Level = level
			prev.LastSent = now
			continue
		}
		// 同一级别持续超标：满 1 小时再提醒一次（只发 Telegram，不记审计）
		if now.Sub(prev.LastSent) >= time.Hour {
			s.sendTelegram(fmt.Sprintf("监控提醒：%s使用率仍为 %s（%s，已持续超标 1 小时；%s）", m.Label, formatPct1(m.Value), levelText(level), m.Detail))
			prev.LastSent = now
		}
	}
}

// monitorAlertLevels 当前各指标级别（供 /api/v1/monitor 返回，前端按它标色）。
func (s *Server) monitorAlertLevels() map[string]string {
	s.monitorMu.Lock()
	defer s.monitorMu.Unlock()
	out := map[string]string{"disk": "ok", "mem": "ok", "cpu": "ok"}
	for k, v := range s.monitorAlerts {
		if v != nil {
			out[k] = v.Level
		}
	}
	return out
}

// ---------- 后台采样循环 ----------

func (s *Server) sampleAndEvaluateMonitor() {
	st := sampleSystem()
	s.monitorMu.Lock()
	s.latestMonitor = &st
	s.monitorMu.Unlock()
	s.evaluateMonitorAt(st, time.Now())
}

func (s *Server) monitorLoop() {
	s.sampleAndEvaluateMonitor()
	for {
		interval := s.cfg.Monitor.IntervalMin
		if interval < 1 || interval > 60 {
			interval = 5
		}
		time.Sleep(time.Duration(interval) * time.Minute)
		s.sampleAndEvaluateMonitor()
	}
}

// ---------- 接口 ----------

// GET /api/v1/monitor：当前采样值 + 阈值 + 各指标级别。
// 还没采过样就当场采一次，保证打开总览就能看到数。
func (s *Server) handleMonitor(w http.ResponseWriter, r *http.Request) {
	s.monitorMu.Lock()
	st := s.latestMonitor
	s.monitorMu.Unlock()
	if st == nil {
		fresh := sampleSystem()
		st = &fresh
	}
	writeJSON(w, 200, okResp(map[string]any{
		"stats": st, "thresholds": s.cfg.Monitor, "alerts": s.monitorAlertLevels(),
	}))
}

// POST /api/v1/settings/monitor：改阈值与采样间隔，保存后立即生效
// （下次采样按新间隔走；保存后当场重采一次，新阈值立刻参与判定）。
func (s *Server) handleMonitorSettings(w http.ResponseWriter, r *http.Request) {
	var req MonitorCfg
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if err := req.validate(); err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	s.cfg.Monitor = req
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	s.audit(s.currentUser(r), "设置监控告警", "", "成功",
		fmt.Sprintf("磁盘 %d%%/%d%%，内存 %d%%/%d%%，CPU %d%%/%d%%，采样间隔 %d 分钟",
			req.DiskWarn, req.DiskCrit, req.MemWarn, req.MemCrit, req.CPUWarn, req.CPUCrit, req.IntervalMin))
	// 同步重采一次：采样只是读 /proc 与 statfs（毫秒级），同步做能保证
	// 下一次 GET /api/v1/monitor 立刻按新阈值判定；Telegram 只在真的触发
	// 告警时才发（没配就是空操作）。
	s.sampleAndEvaluateMonitor()
	writeJSON(w, 200, okResp(map[string]any{"monitor": req}))
}
