package main

import (
	"encoding/json"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 1.3.43：中转机倍率（落地倍率 × 中转倍率） ----------

// TestRelayMultiplier1343 中转倍率归一化：0/非法→1，>100 钳到 100，
// 与落地倍率同一规则（normalizeTrafficMultiplier）。
func TestRelayMultiplier1343(t *testing.T) {
	s, _ := newMultiplierServer1340(t)
	cases := []struct{ in, want float64 }{
		{0, 1}, {1, 1}, {1.5, 1.5}, {2, 2}, {100, 100},
		{200, 100}, {-1, 1}, {math.NaN(), 1}, {math.Inf(1), 1},
	}
	for _, c := range cases {
		s.cfg.RelayMultiplier = c.in
		if got := s.relayMultiplier(); got != c.want {
			t.Fatalf("中转倍率 %v 应归一化为 %v，实际 %v", c.in, c.want, got)
		}
	}
}

// TestEffectiveMultiplier1343 有效倍率 = 落地倍率 × 中转倍率，再按同一
// 规则归一化；只设一处就只算那一处，两处都 1 就是 1。
func TestEffectiveMultiplier1343(t *testing.T) {
	s, _ := newMultiplierServer1340(t)
	s.cfg.RelayMultiplier = 1.5
	// 落地 1 倍率 2 × 中转 1.5 = 3；落地 2 倍率 2.5 × 1.5 = 3.75。
	if got := s.effectiveMultiplier("203.0.113.1"); got != 3 {
		t.Fatalf("落地 1 有效倍率应为 3，实际 %v", got)
	}
	if got := s.effectiveMultiplier("203.0.113.2"); got != 3.75 {
		t.Fatalf("落地 2 有效倍率应为 3.75，实际 %v", got)
	}
	// 不存在的落地：落地倍率按 1，只剩中转倍率。
	if got := s.effectiveMultiplier("203.0.113.9"); got != 1.5 {
		t.Fatalf("不存在的落地有效倍率应为 1.5，实际 %v", got)
	}
	// 只设中转倍率（落地倍率 1）：有效 = 中转倍率。
	if got := s.effectiveLandingMultiplier(Landing{TrafficMultiplier: 0}); got != 1.5 {
		t.Fatalf("落地倍率 1 × 中转 1.5 应为 1.5，实际 %v", got)
	}
	// 只设落地倍率（中转倍率 1）：有效 = 落地倍率。
	s.cfg.RelayMultiplier = 0
	if got := s.effectiveMultiplier("203.0.113.1"); got != 2 {
		t.Fatalf("中转倍率 1 时落地 1 有效倍率应为 2，实际 %v", got)
	}
	// 两处都 1：有效 1。
	if got := s.effectiveMultiplier("203.0.113.9"); got != 1 {
		t.Fatalf("两处都 1 时有效倍率应为 1，实际 %v", got)
	}
	// 乘积超上限钳到 100：落地 100 × 中转 100。
	s.cfg.RelayMultiplier = 100
	if got := s.effectiveLandingMultiplier(Landing{TrafficMultiplier: 100}); got != 100 {
		t.Fatalf("100×100 应钳到 100，实际 %v", got)
	}
}

// TestUserTrafficPortMultipliersRelay1343 采样侧逐端口有效倍率：落地倍率
// × 中转倍率；不存在的落地对应的端口仍无倍率。
func TestUserTrafficPortMultipliersRelay1343(t *testing.T) {
	s, _ := newMultiplierServer1340(t)
	s.cfg.RelayMultiplier = 1.5
	s.cfg.UserForwards = []UserForward{
		{Key: "subuser:su-1", SubKind: "subuser", SubID: "su-1", Panel: "local", LandingAddr: "203.0.113.1", ListenPort: 30000},
		{Key: "subuser:su-1", SubKind: "subuser", SubID: "su-1", Panel: "local", LandingAddr: "203.0.113.2", ListenPort: 30001},
		{Key: "subuser:su-1", SubKind: "subuser", SubID: "su-1", Panel: "local", LandingAddr: "203.0.113.9", ListenPort: 30002},
	}
	m := s.userTrafficPortMultipliers()
	if m[30000] != 3 {
		t.Fatalf("落地 1 端口 30000 有效倍率应为 3（2×1.5），实际 %v", m[30000])
	}
	if m[30001] != 3.75 {
		t.Fatalf("落地 2 端口 30001 有效倍率应为 3.75（2.5×1.5），实际 %v", m[30001])
	}
	if _, ok := m[30002]; ok {
		t.Fatalf("不存在的落地对应的端口不应有倍率，实际 %v", m)
	}
	// 中转倍率改回 1：端口倍率回到落地倍率本身。
	s.cfg.RelayMultiplier = 0
	m = s.userTrafficPortMultipliers()
	if m[30000] != 2 || m[30001] != 2.5 {
		t.Fatalf("中转倍率 1 时端口倍率应回到 2/2.5，实际 %v", m)
	}
}

// TestNodePushEffectiveMultiplier1343 agent 推送按有效倍率（落地 × 中转）
// 记账；generic 节点总量不乘；重推去重、不重复乘。
func TestNodePushEffectiveMultiplier1343(t *testing.T) {
	s, tok := newMultiplierServer1340(t)
	s.cfg.RelayMultiplier = 1.5
	push := func(body string) map[string]any {
		rec := callNode1330(t, s, "POST", "/api/v1/node/push", body, tok)
		if rec.Code != 200 {
			t.Fatalf("推送应 200，实际 %d：%s", rec.Code, rec.Body.String())
		}
		return decodeNode1330(t, rec)
	}
	// 落地 1 有效倍率 3：1000/2000 → 3000/6000。
	resp := push(`{"node_id":"203.0.113.1","entries":[{"user_id":"subuser:su-1","period_id":0,"seq":1,"up_bytes":1000,"down_bytes":2000}]}`)
	if resp["accepted"] != float64(1) {
		t.Fatalf("推送应 accepted=1，实际 %v", resp)
	}
	up, down, _, _, _ := s.userTraffic.totals("subuser:su-1")
	if up != 3000 || down != 6000 {
		t.Fatalf("有效倍率 3 的推送 1000/2000 应记 3000/6000，实际 up=%d down=%d", up, down)
	}
	// generic 节点总量不乘：7000/8000 原样记。
	resp = push(`{"node_id":"203.0.113.1","entries":[{"user_id":"generic","period_id":0,"seq":1,"up_bytes":7000,"down_bytes":8000}]}`)
	if resp["accepted"] != float64(1) {
		t.Fatalf("generic 推送应 accepted=1，实际 %v", resp)
	}
	up, down, _, _, ok := s.userTraffic.totals("generic")
	if !ok || up != 7000 || down != 8000 {
		t.Fatalf("generic 用量应原样 7000/8000，实际 up=%d down=%d ok=%v", up, down, ok)
	}
	// 重推第一条（同 seq）：去重，不重复乘。
	resp = push(`{"node_id":"203.0.113.1","entries":[{"user_id":"subuser:su-1","period_id":0,"seq":1,"up_bytes":1000,"down_bytes":2000}]}`)
	if resp["duplicates"] != float64(1) {
		t.Fatalf("重推应 duplicates=1，实际 %v", resp)
	}
	up, down, _, _, _ = s.userTraffic.totals("subuser:su-1")
	if up != 3000 || down != 6000 {
		t.Fatalf("重推后用量不应变，实际 up=%d down=%d", up, down)
	}
}

// TestUserQuotaRemainingEffective1343 quota 换算除以有效倍率（落地 × 中转）。
func TestUserQuotaRemainingEffective1343(t *testing.T) {
	s, _ := newMultiplierServer1340(t)
	s.cfg.RelayMultiplier = 1.5
	// 落地 1 有效倍率 3：上限 10000、已用 4000，剩余 6000/3 = 2000 原始字节。
	if got := userQuotaRemaining(10000, 4000, s.effectiveMultiplier("203.0.113.1")); got != 2000 {
		t.Fatalf("有效倍率 3 时剩余量应为 2000，实际 %d", got)
	}
	// 落地记录口径：落地 2.5 × 中转 1.5 = 3.75，剩余 6000/3.75 = 1600。
	if got := userQuotaRemaining(10000, 4000, s.effectiveLandingMultiplier(Landing{TrafficMultiplier: 2.5})); got != 1600 {
		t.Fatalf("有效倍率 3.75 时剩余量应为 1600，实际 %d", got)
	}
}

// TestRelayMultiplierSet1343 设置接口改中转倍率：合法保存、设置接口返回
// 生效值；0.1–100 之外拒绝；0 = 没填（按 1 算）。
func TestRelayMultiplierSet1343(t *testing.T) {
	s, _ := newMultiplierServer1340(t)
	set := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/settings/relay-multiplier", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleRelayMultiplierSet(rec, req)
		return rec
	}
	rec := set(`{"multiplier":1.5}`)
	if rec.Code != 200 {
		t.Fatalf("中转倍率 1.5 应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if s.relayMultiplier() != 1.5 {
		t.Fatalf("保存后中转倍率应为 1.5，实际 %v", s.relayMultiplier())
	}
	// 设置接口返回生效值 1.5。
	req := httptest.NewRequest("GET", "/api/v1/settings", nil)
	rec2 := httptest.NewRecorder()
	s.handleSettings(rec2, req)
	if rec2.Code != 200 {
		t.Fatalf("设置接口应 200，实际 %d", rec2.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("设置响应不是 JSON：%v", err)
	}
	if resp["relay_multiplier"] != 1.5 {
		t.Fatalf("设置接口 relay_multiplier 应为 1.5，实际 %v", resp["relay_multiplier"])
	}
	// 非法值拒绝：101、-1、0.05（0.1–100 之外）。
	for _, bad := range []string{`{"multiplier":101}`, `{"multiplier":-1}`, `{"multiplier":0.05}`} {
		rec := set(bad)
		if rec.Code != 400 {
			t.Fatalf("倍率 %s 应 400，实际 %d：%s", bad, rec.Code, rec.Body.String())
		}
	}
	// 0 = 没填，按 1 算。
	rec = set(`{"multiplier":0}`)
	if rec.Code != 200 {
		t.Fatalf("倍率 0 应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if s.relayMultiplier() != 1 {
		t.Fatalf("倍率 0 保存后应按 1 算，实际 %v", s.relayMultiplier())
	}
}
