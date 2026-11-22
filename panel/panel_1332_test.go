package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------- v1.3.32：agent 总量显示 + 到期时间时区 ----------

// TestAgentDeltaDayBucket1332：addAgentDelta 的日桶累加与翻日清零，
// 以及 agentGenericTotals 无记录时 ok=false。
func TestAgentDeltaDayBucket1332(t *testing.T) {
	s := newTestSubServer(t)
	if _, _, _, _, _, _, ok := s.userTraffic.agentGenericTotals(); ok {
		t.Fatalf("没推过数时 agentGenericTotals 应 ok=false")
	}
	s.userTraffic.addAgentDelta("generic", 100, 200, 0)
	s.userTraffic.addAgentDelta("generic", 50, 60, 0)
	up, down, upPeriod, downPeriod, dayUp, dayDown, ok := s.userTraffic.agentGenericTotals()
	if !ok || up != 150 || down != 260 || upPeriod != 150 || downPeriod != 260 || dayUp != 150 || dayDown != 260 {
		t.Fatalf("日桶应累加：up=%d down=%d upPeriod=%d downPeriod=%d dayUp=%d dayDown=%d ok=%v", up, down, upPeriod, downPeriod, dayUp, dayDown, ok)
	}
	// 模拟翻日：把 DayKey 改成昨天再推一笔，当日清零重计，累计继续涨。
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	s.userTraffic.data["generic"].DayKey = yesterday
	s.userTraffic.addAgentDelta("generic", 7, 8, 0)
	up, down, _, _, dayUp, dayDown, ok = s.userTraffic.agentGenericTotals()
	if !ok || up != 157 || down != 268 || dayUp != 7 || dayDown != 8 {
		t.Fatalf("翻日后当日应清零重计：up=%d down=%d dayUp=%d dayDown=%d ok=%v", up, down, dayUp, dayDown, ok)
	}
}

// callStatus1332 直接调 handleStatus（绕过登录中间件，与 callSub 同款做法）。
func callStatus1332(t *testing.T, s *Server) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	rec := httptest.NewRecorder()
	s.handleStatus(rec, req)
	if rec.Code != 200 {
		t.Fatalf("handleStatus 应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	return resp
}

// landingTraffic1332 从总览响应里取指定落地的 traffic 块与 metering_source。
func landingTraffic1332(t *testing.T, resp map[string]any, landing string) (map[string]any, string) {
	t.Helper()
	landings, _ := resp["landings"].([]any)
	for _, li := range landings {
		lm, _ := li.(map[string]any)
		if lm == nil || lm["landing"] != landing {
			continue
		}
		tr, _ := lm["traffic"].(map[string]any)
		if tr == nil {
			t.Fatalf("落地 %s 没有 traffic 块：%v", landing, lm)
		}
		ms, _ := lm["metering_source"].(string)
		return tr, ms
	}
	t.Fatalf("总览里找不到落地 %s", landing)
	return nil, ""
}

func num1332(v any) float64 {
	f, _ := v.(float64)
	return f
}

// TestHandleStatusAgentTraffic1332：agent 在线时落地卡片流量等于 agent
// 推来的 generic 总量并标 metering_source=agent；推送超过 5 分钟
// （nodeAgentOfflineAfter）回落到 A 侧数、标注清空。
func TestHandleStatusAgentTraffic1332(t *testing.T) {
	s := newTestSubServer(t)
	// A 侧数（与 agent 数故意不同，好分辨）：上行 1000、下行 2000。
	s.traffic.addDelta("203.0.113.1", map[string][2]int64{"tcp:24443": {1000, 2000}})
	// agent 数：上行 112、下行 224（两笔增量）。
	s.userTraffic.addAgentDelta("generic", 111, 222, 0)
	s.userTraffic.addAgentDelta("generic", 1, 2, 0)

	ext := s.state.ext("203.0.113.1")
	ext.MeteringSource = "agent"
	ext.AgentLastPushAt = time.Now().UTC().Format(time.RFC3339)
	tr, ms := landingTraffic1332(t, callStatus1332(t, s), "203.0.113.1")
	if ms != "agent" {
		t.Fatalf("agent 在线时 metering_source 应为 agent，实际 %q", ms)
	}
	if num1332(tr["today_up"]) != 112 || num1332(tr["today_down"]) != 224 ||
		num1332(tr["month_up"]) != 112 || num1332(tr["month_down"]) != 224 ||
		num1332(tr["total_up"]) != 112 || num1332(tr["total_down"]) != 224 {
		t.Fatalf("agent 在线时卡片应用 agent 数：%v", tr)
	}

	// 推送停了 10 分钟：回落 A 侧数。
	ext.AgentLastPushAt = time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	tr, ms = landingTraffic1332(t, callStatus1332(t, s), "203.0.113.1")
	if ms != "" {
		t.Fatalf("agent 离线时 metering_source 应为空，实际 %q", ms)
	}
	if num1332(tr["today_up"]) != 1000 || num1332(tr["today_down"]) != 2000 ||
		num1332(tr["month_up"]) != 1000 || num1332(tr["month_down"]) != 2000 ||
		num1332(tr["total_up"]) != 1000 || num1332(tr["total_down"]) != 2000 {
		t.Fatalf("agent 离线时卡片应回落 A 侧数：%v", tr)
	}
}

// TestParseSubExpiryIn1332：纯日期按浏览器时区算当天结束；
// 不带时区或非法时区回退服务器本地时间；RFC3339 全串原样接受。
func TestParseSubExpiryIn1332(t *testing.T) {
	cases := []struct {
		in, tz, want string
		wantErr      bool
	}{
		{"", "", "", false},
		{"2027-02-17", "Asia/Shanghai", "2027-02-17T15:59:59Z", false},
		{"2027-02-17", "America/New_York", "2027-02-18T04:59:59Z", false},
		{"2027-02-17", "UTC", "2027-02-17T23:59:59Z", false},
		// 非法时区与不带时区都回退 time.Local；测试跑在 TZ=UTC 下即 UTC。
		{"2027-02-17", "No/Such_Zone", "2027-02-17T23:59:59Z", false},
		{"2027-02-17", "", "2027-02-17T23:59:59Z", false},
		{"2027-02-17T10:00:00+08:00", "Asia/Shanghai", "2027-02-17T02:00:00Z", false},
		{"not-a-date", "Asia/Shanghai", "", true},
		{"2027-13-40", "Asia/Shanghai", "", true},
	}
	for _, c := range cases {
		got, err := parseSubExpiryIn(c.in, c.tz)
		if c.wantErr {
			if err == nil {
				t.Fatalf("parseSubExpiryIn(%q, %q) 应报错，实际 %q", c.in, c.tz, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseSubExpiryIn(%q, %q) 不应报错：%v", c.in, c.tz, err)
		}
		if got != c.want {
			t.Fatalf("parseSubExpiryIn(%q, %q) = %q，应为 %q", c.in, c.tz, got, c.want)
		}
	}
	// wrapper 保持老行为（TZ=UTC 下纯日期 = 当天 23:59:59Z）。
	if got, err := parseSubExpiry("2027-02-17"); err != nil || got != "2027-02-17T23:59:59Z" {
		t.Fatalf("parseSubExpiry 老 wrapper 应回退 time.Local：%q, %v", got, err)
	}
}
