package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------- 采样解析 ----------

func TestParseMeminfo(t *testing.T) {
	total, avail := parseMeminfo("MemTotal:       4024548 kB\nMemFree:         123456 kB\nMemAvailable:   2012274 kB\nBuffers:          12345 kB\n")
	if total != 4024548*1024 {
		t.Fatalf("total=%d", total)
	}
	if avail != 2012274*1024 {
		t.Fatalf("avail=%d", avail)
	}
	// 缺 MemAvailable 时返回 0（不崩）
	if _, avail2 := parseMeminfo("MemTotal:       1024 kB\n"); avail2 != 0 {
		t.Fatalf("avail=%d want 0", avail2)
	}
}

func TestParseLoadavg(t *testing.T) {
	if v := parseLoadavg("0.42 0.35 0.28 1/234 5678\n"); v != 0.42 {
		t.Fatalf("load1=%v want 0.42", v)
	}
	if v := parseLoadavg(""); v != 0 {
		t.Fatalf("empty load1=%v want 0", v)
	}
}

func TestSampleSystemRealMachine(t *testing.T) {
	st := sampleSystem()
	if st.DiskTotalBytes <= 0 {
		t.Fatalf("磁盘总量读不到：%+v", st)
	}
	if st.DiskPct < 0 || st.DiskPct > 100 {
		t.Fatalf("磁盘使用率越界：%v", st.DiskPct)
	}
	if st.MemTotalBytes <= 0 {
		t.Fatalf("内存总量读不到：%+v", st)
	}
	if st.CPUCores <= 0 {
		t.Fatalf("CPU 核心数读不到：%+v", st)
	}
	if st.SampledAt == "" {
		t.Fatalf("采样时间为空")
	}
}

// ---------- 阈值判定 ----------

func TestAlertLevel(t *testing.T) {
	if alertLevel(50, 85, 95) != "ok" {
		t.Fatalf("50 应为 ok")
	}
	if alertLevel(85, 85, 95) != "warn" {
		t.Fatalf("85（等于警告阈值）应为 warn")
	}
	if alertLevel(94.9, 85, 95) != "warn" {
		t.Fatalf("94.9 应为 warn")
	}
	if alertLevel(95, 85, 95) != "critical" {
		t.Fatalf("95（等于严重阈值）应为 critical")
	}
	if alertLevel(100, 85, 95) != "critical" {
		t.Fatalf("100 应为 critical")
	}
}

func TestMonitorCfgValidate(t *testing.T) {
	good := defaultMonitorCfg()
	if err := good.validate(); err != nil {
		t.Fatalf("默认配置应合法：%v", err)
	}
	bad := defaultMonitorCfg()
	bad.DiskWarn = 95
	bad.DiskCrit = 85
	if err := bad.validate(); err == nil {
		t.Fatalf("警告 >= 严重应被拒绝")
	}
	bad2 := defaultMonitorCfg()
	bad2.MemWarn = 0
	if err := bad2.validate(); err == nil {
		t.Fatalf("阈值 0 应被拒绝")
	}
	bad3 := defaultMonitorCfg()
	bad3.IntervalMin = 120
	if err := bad3.validate(); err != nil {
		t.Fatalf("间隔超范围应钳制而不是报错：%v", err)
	}
	if bad3.IntervalMin != 60 {
		t.Fatalf("间隔应钳制到 60，实际 %d", bad3.IntervalMin)
	}
	bad4 := defaultMonitorCfg()
	bad4.IntervalMin = 0
	_ = bad4.validate()
	if bad4.IntervalMin != 1 {
		t.Fatalf("间隔应钳制到 1，实际 %d", bad4.IntervalMin)
	}
}

// ---------- 告警去重 / 节流 / 回落 ----------

func countAudit(t *testing.T, s *Server) int {
	t.Helper()
	_, total := s.readAuditPage(1, 1000)
	return total
}

func TestEvaluateMonitorDedupThrottleRecover(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	base := time.Now()
	// 磁盘 90%：警告阈值 85、严重 95 → 触发警告，记 1 条审计
	over := SystemStats{DiskPct: 90, MemPct: 10, CPUPct: 10}
	s.evaluateMonitorAt(over, base)
	if n := countAudit(t, s); n != 1 {
		t.Fatalf("触发警告后审计=%d want 1", n)
	}
	if s.monitorAlertLevels()["disk"] != "warn" {
		t.Fatalf("磁盘级别应为 warn")
	}
	// 30 分钟后仍超标：同一级别、未满 1 小时，不重复记审计
	s.evaluateMonitorAt(over, base.Add(30*time.Minute))
	if n := countAudit(t, s); n != 1 {
		t.Fatalf("30 分钟内重复采样审计=%d want 1（去重）", n)
	}
	// 61 分钟后仍超标：只发 Telegram 提醒（没配 Telegram 是空操作），仍不记审计
	s.evaluateMonitorAt(over, base.Add(61*time.Minute))
	if n := countAudit(t, s); n != 1 {
		t.Fatalf("1 小时提醒审计=%d want 1（提醒不记审计）", n)
	}
	// 升到 96%：升级严重，再记 1 条
	s.evaluateMonitorAt(SystemStats{DiskPct: 96, MemPct: 10, CPUPct: 10}, base.Add(62*time.Minute))
	if n := countAudit(t, s); n != 2 {
		t.Fatalf("升级严重后审计=%d want 2", n)
	}
	if s.monitorAlertLevels()["disk"] != "critical" {
		t.Fatalf("磁盘级别应为 critical")
	}
	// 回落到 50%：恢复，再记 1 条
	s.evaluateMonitorAt(SystemStats{DiskPct: 50, MemPct: 10, CPUPct: 10}, base.Add(63*time.Minute))
	if n := countAudit(t, s); n != 3 {
		t.Fatalf("回落后审计=%d want 3", n)
	}
	if s.monitorAlertLevels()["disk"] != "ok" {
		t.Fatalf("回落后磁盘级别应为 ok")
	}
	// 回落后再次越过：重新触发，再记 1 条
	s.evaluateMonitorAt(over, base.Add(64*time.Minute))
	if n := countAudit(t, s); n != 4 {
		t.Fatalf("再次越过后审计=%d want 4", n)
	}
	// 审计内容应包含触发/升级/恢复三种事件
	entries, _ := s.readAuditPage(1, 1000)
	var sawWarn, sawCrit, sawRecover bool
	for _, e := range entries {
		if e.Action != "监控告警" || e.Target != "磁盘" {
			continue
		}
		switch e.Result {
		case "警告":
			sawWarn = true
		case "严重":
			sawCrit = true
		case "恢复":
			sawRecover = true
		}
	}
	if !sawWarn || !sawCrit || !sawRecover {
		t.Fatalf("审计缺事件：警告=%v 严重=%v 恢复=%v（%+v）", sawWarn, sawCrit, sawRecover, entries)
	}
}

// ---------- 接口 ----------

func TestHandleMonitorSettings(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	post := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/v1/settings/monitor", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleMonitorSettings(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	// 警告 >= 严重：拒绝
	code, resp := post(`{"disk_warn":95,"disk_crit":90,"mem_warn":80,"mem_crit":90,"cpu_warn":80,"cpu_crit":95,"interval_min":5}`)
	if code != 400 || resp["ok"] != false {
		t.Fatalf("非法阈值应 400：code=%d resp=%v", code, resp)
	}
	// 合法：保存并立即生效
	code, resp = post(`{"disk_warn":80,"disk_crit":90,"mem_warn":70,"mem_crit":85,"cpu_warn":75,"cpu_crit":90,"interval_min":10}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("合法设置应 200：code=%d resp=%v", code, resp)
	}
	if s.cfg.Monitor.DiskWarn != 80 || s.cfg.Monitor.IntervalMin != 10 {
		t.Fatalf("配置没存上：%+v", s.cfg.Monitor)
	}
	// 间隔 120：钳制到 60
	code, _ = post(`{"disk_warn":80,"disk_crit":90,"mem_warn":70,"mem_crit":85,"cpu_warn":75,"cpu_crit":90,"interval_min":120}`)
	if code != 200 || s.cfg.Monitor.IntervalMin != 60 {
		t.Fatalf("间隔应钳制到 60：code=%d cfg=%+v", code, s.cfg.Monitor)
	}
}

func TestHandleMonitor(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	req := httptest.NewRequest("GET", "/api/v1/monitor", nil)
	rec := httptest.NewRecorder()
	s.handleMonitor(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP 状态=%d 响应=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK         bool `json:"ok"`
		Stats      SystemStats
		Thresholds MonitorCfg `json:"thresholds"`
		Alerts     map[string]string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if !resp.OK || resp.Stats.DiskTotalBytes <= 0 {
		t.Fatalf("监控接口应返回磁盘总量：%+v", resp)
	}
	if resp.Thresholds.DiskWarn != 85 || resp.Thresholds.DiskCrit != 95 {
		t.Fatalf("默认阈值不对：%+v", resp.Thresholds)
	}
	if resp.Alerts["disk"] == "" || resp.Alerts["mem"] == "" || resp.Alerts["cpu"] == "" {
		t.Fatalf("级别缺失：%v", resp.Alerts)
	}
}

func TestMonitorDefaultsForOldConfig(t *testing.T) {
	// 模拟 1.1.10 之前装的面板：config.json 里没有 monitor 一节，加载后应补默认值
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	if s.cfg.Monitor != defaultMonitorCfg() {
		t.Fatalf("旧配置加载后阈值应为默认值：%+v", s.cfg.Monitor)
	}
}
