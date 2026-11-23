package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- v1.3.30：节点侧 agent 三个接口 ----------
// 目标：GET /api/v1/node/users（ETag/304）、POST /api/v1/node/push
//（(user_id, period_id, seq) 去重）、GET /api/v1/node/config，
// 全部复用 rpt_ 令牌认证（未认证 401）；新字段 node_uuids、
// metering_source、agent 状态（版本/最后推送时间）齐全。

const testNodeUUID = "11111111-2222-3333-4444-555555555555"

// newNodeAgentServer1330 造一台带订阅用户与合并订阅的面板：
// su-1（限流 100MB，带一个落地 UUID）与 ms-1（限流 200MB，monthly），
// rpt_ 令牌已设好，明文返回给调用方。
func newNodeAgentServer1330(t *testing.T) (*Server, string) {
	t.Helper()
	s := newTestSubServer(t)
	tok := generateRemoteToken()
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)
	s.cfg.SubUsers = []SubUser{{
		ID: "su-1", Name: "小明", TokenHash: "hash-su-1",
		CreatedAt: "2026-11-20T00:00:00Z", Selection: []SubSelection{{Panel: "local", Landing: 1}},
		Mode: "both", ExpiresAt: "2027-05-01T23:59:59Z",
		TrafficLimitBytes: 100 * 1024 * 1024, TrafficLimitMode: "total",
		ResetMode: "monthly", ResetDay: 1,
		NodeUUIDs: map[string]string{"203.0.113.1": testNodeUUID},
	}}
	s.cfg.MergedSubs = []MergedSub{{
		ID: "ms-1", Name: "合并一", TokenHash: "hash-ms-1",
		CreatedAt: "2026-11-20T00:00:00Z", Selection: []SubSelection{{Panel: "local", Landing: 1}},
		Mode: "both", ExpiresAt: "2027-06-01T23:59:59Z",
		TrafficLimitBytes: 200 * 1024 * 1024, TrafficLimitMode: "monthly",
		ResetMode: "monthly", ResetDay: 1, NextResetAt: "2026-12-01T00:00:00Z",
	}}
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
	return s, tok
}

// callNode1330 走完整路由（含 requireCentralToken 中间件）调 node 接口。
func callNode1330(t *testing.T, s *Server, method, target, body, tok string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	return rec
}

func decodeNode1330(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	return resp
}

func TestNodeUsersFieldsAndAuth1330(t *testing.T) {
	s, tok := newNodeAgentServer1330(t)
	if rec := callNode1330(t, s, "GET", "/api/v1/node/users?node_id=203.0.113.1", "", ""); rec.Code != 401 {
		t.Fatalf("无令牌应 401，实际 %d", rec.Code)
	}
	if rec := callNode1330(t, s, "GET", "/api/v1/node/users", "", "rpt_0000000000000000000000000000000000000000000000000000000000000000"); rec.Code != 401 {
		t.Fatalf("错令牌应 401，实际 %d", rec.Code)
	}
	rec := callNode1330(t, s, "GET", "/api/v1/node/users?node_id=203.0.113.1", "", tok)
	if rec.Code != 200 {
		t.Fatalf("正确令牌应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatalf("下发应带 ETag 头")
	}
	resp := decodeNode1330(t, rec)
	if resp["panel_version"] != "1.3.52" {
		t.Fatalf("panel_version 应为 1.3.52，实际 %v", resp["panel_version"])
	}
	if resp["generic_email"] != "generic@node" {
		t.Fatalf("generic_email 应为 generic@node，实际 %v", resp["generic_email"])
	}
	users, _ := resp["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("用户表应有 2 条（订阅用户 + 合并订阅），实际 %d：%s", len(users), rec.Body.String())
	}
	byID := map[string]map[string]any{}
	for _, u := range users {
		m, _ := u.(map[string]any)
		byID[m["id"].(string)] = m
	}
	su := byID["su-1"]
	if su["user_id"] != "subuser:su-1" || su["email"] != "u-sub-su-1@node" {
		t.Fatalf("su-1 的 user_id/email 不对：%v", su)
	}
	uuids, _ := su["node_uuids"].(map[string]any)
	if uuids["203.0.113.1"] != testNodeUUID {
		t.Fatalf("su-1 的 node_uuids 应含 203.0.113.1 → %s，实际 %v", testNodeUUID, uuids)
	}
	if su["traffic_limit_bytes"] != float64(100*1024*1024) || su["traffic_limit_mode"] != "total" || su["expires_at"] != "2027-05-01T23:59:59Z" {
		t.Fatalf("su-1 的限流/到期字段不对：%v", su)
	}
	ms := byID["ms-1"]
	if ms["user_id"] != "merged:ms-1" || ms["email"] != "u-merged-ms-1@node" {
		t.Fatalf("ms-1 的 user_id/email 不对：%v", ms)
	}
	if ms["traffic_limit_mode"] != "monthly" || ms["next_reset_at"] != "2026-12-01T00:00:00Z" {
		t.Fatalf("ms-1 的限流/重置字段不对：%v", ms)
	}
	st, _ := resp["agent_status"].(map[string]any)
	if st == nil || st["online"] != false || st["metering_source"] != "" {
		t.Fatalf("没推送过时 agent_status 应为离线/来源空，实际 %v", st)
	}
}

// 下发 ETag：没变 304，变了 200 且 ETag 跟着变。
func TestNodeUsersETag3041330(t *testing.T) {
	s, tok := newNodeAgentServer1330(t)
	rec1 := callNode1330(t, s, "GET", "/api/v1/node/users", "", tok)
	if rec1.Code != 200 {
		t.Fatalf("首次下发应 200，实际 %d", rec1.Code)
	}
	etag := rec1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("首次下发应带 ETag")
	}
	req := httptest.NewRequest("GET", "/api/v1/node/users", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	s.routes().ServeHTTP(rec2, req)
	if rec2.Code != 304 {
		t.Fatalf("ETag 没变应 304，实际 %d", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Fatalf("304 不应带 body，实际 %d 字节", rec2.Body.Len())
	}
	// 加一个用户，ETag 必须变、再拉是 200。
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-2", Name: "小红", TokenHash: "hash-su-2"})
	req3 := httptest.NewRequest("GET", "/api/v1/node/users", nil)
	req3.Header.Set("Authorization", "Bearer "+tok)
	req3.Header.Set("If-None-Match", etag)
	rec3 := httptest.NewRecorder()
	s.routes().ServeHTTP(rec3, req3)
	if rec3.Code != 200 {
		t.Fatalf("用户表变了应 200，实际 %d", rec3.Code)
	}
	if rec3.Header().Get("ETag") == etag {
		t.Fatalf("用户表变了 ETag 必须变")
	}
}

// 配置下发：字段齐全，未认证 401。
func TestNodeConfigFields1330(t *testing.T) {
	s, tok := newNodeAgentServer1330(t)
	if rec := callNode1330(t, s, "GET", "/api/v1/node/config?node_id=203.0.113.1", "", ""); rec.Code != 401 {
		t.Fatalf("无令牌应 401，实际 %d", rec.Code)
	}
	rec := callNode1330(t, s, "GET", "/api/v1/node/config?node_id=203.0.113.1", "", tok)
	if rec.Code != 200 {
		t.Fatalf("正确令牌应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	resp := decodeNode1330(t, rec)
	if resp["push_interval_seconds"] != float64(60) {
		t.Fatalf("push_interval_seconds 应为 60，实际 %v", resp["push_interval_seconds"])
	}
	if resp["min_agent_version"] != "0.1.0" {
		t.Fatalf("min_agent_version 应为 0.1.0，实际 %v", resp["min_agent_version"])
	}
	if resp["enforce"] != false {
		t.Fatalf("本版 enforce 应为 false，实际 %v", resp["enforce"])
	}
	if resp["panel_version"] != "1.3.52" {
		t.Fatalf("panel_version 应为 1.3.52，实际 %v", resp["panel_version"])
	}
	st, _ := resp["agent_status"].(map[string]any)
	if st == nil {
		t.Fatalf("config 应带 agent_status")
	}
	if _, ok := st["version"]; !ok {
		t.Fatalf("agent_status 应含 version 字段：%v", st)
	}
	if _, ok := st["last_push_at"]; !ok {
		t.Fatalf("agent_status 应含 last_push_at 字段：%v", st)
	}
}

// 推送去重：同一条重推不重复计；新 seq 累加；未知用户与负数被拒。
func TestNodePushDedup1330(t *testing.T) {
	s, tok := newNodeAgentServer1330(t)
	push := func(body string) map[string]any {
		rec := callNode1330(t, s, "POST", "/api/v1/node/push", body, tok)
		if rec.Code != 200 {
			t.Fatalf("推送应 200，实际 %d：%s", rec.Code, rec.Body.String())
		}
		return decodeNode1330(t, rec)
	}
	body1 := `{"node_id":"203.0.113.1","agent_version":"0.1.0","entries":[{"user_id":"subuser:su-1","period_id":0,"seq":1,"up_bytes":1000,"down_bytes":2000}]}`
	resp := push(body1)
	if resp["accepted"] != float64(1) || resp["duplicates"] != float64(0) {
		t.Fatalf("首次推送应 accepted=1、duplicates=0，实际 %v", resp)
	}
	up, down, upP, downP, ok := s.userTraffic.totals("subuser:su-1")
	if !ok || up != 1000 || down != 2000 || upP != 1000 || downP != 2000 {
		t.Fatalf("推送后用量应为 1000/2000，实际 up=%d down=%d upP=%d downP=%d ok=%v", up, down, upP, downP, ok)
	}
	// 同一条原样重推：去重，不重复计。
	resp = push(body1)
	if resp["accepted"] != float64(0) || resp["duplicates"] != float64(1) {
		t.Fatalf("重推应 accepted=0、duplicates=1，实际 %v", resp)
	}
	up, down, _, _, _ = s.userTraffic.totals("subuser:su-1")
	if up != 1000 || down != 2000 {
		t.Fatalf("重推后用量不应变，实际 up=%d down=%d", up, down)
	}
	// 旧 seq（比已见最大 seq 小）也是重推。
	resp = push(`{"node_id":"203.0.113.1","entries":[{"user_id":"subuser:su-1","period_id":0,"seq":1,"up_bytes":999,"down_bytes":999}]}`)
	if resp["duplicates"] != float64(1) {
		t.Fatalf("旧 seq 应算重推，实际 %v", resp)
	}
	// 新 seq 累加。
	resp = push(`{"node_id":"203.0.113.1","entries":[{"user_id":"subuser:su-1","period_id":0,"seq":2,"up_bytes":500,"down_bytes":500}]}`)
	if resp["accepted"] != float64(1) {
		t.Fatalf("新 seq 应 accepted=1，实际 %v", resp)
	}
	up, down, _, _, _ = s.userTraffic.totals("subuser:su-1")
	if up != 1500 || down != 2500 {
		t.Fatalf("新 seq 推送后用量应为 1500/2500，实际 up=%d down=%d", up, down)
	}
	// 未知用户与负数：整单不失败，只记 rejected。
	resp = push(`{"node_id":"203.0.113.1","entries":[
		{"user_id":"subuser:su-404","period_id":0,"seq":1,"up_bytes":10,"down_bytes":10},
		{"user_id":"merged:ms-1","period_id":0,"seq":1,"up_bytes":-5,"down_bytes":10},
		{"user_id":"badformat","period_id":0,"seq":1,"up_bytes":10,"down_bytes":10}]}`)
	if resp["accepted"] != float64(0) {
		t.Fatalf("三条坏条目应 accepted=0，实际 %v", resp)
	}
	if rej, _ := resp["rejected"].([]any); len(rej) != 3 {
		t.Fatalf("三条坏条目应 rejected=3，实际 %v", resp)
	}
	// 通用订阅总量（Phase 0 节点总量走这条）。
	resp = push(`{"node_id":"203.0.113.1","entries":[{"user_id":"generic","period_id":0,"seq":1,"up_bytes":7000,"down_bytes":8000}]}`)
	if resp["accepted"] != float64(1) {
		t.Fatalf("generic 推送应 accepted=1，实际 %v", resp)
	}
	up, down, _, _, ok = s.userTraffic.totals("generic")
	if !ok || up != 7000 || down != 8000 {
		t.Fatalf("generic 用量应为 7000/8000，实际 up=%d down=%d ok=%v", up, down, ok)
	}
	// 未认证推送 401。
	if rec := callNode1330(t, s, "POST", "/api/v1/node/push", body1, ""); rec.Code != 401 {
		t.Fatalf("无令牌推送应 401，实际 %d", rec.Code)
	}
}

// 推送更新 agent 状态：metering_source=agent、版本与最后推送时间落盘，
// 在线判定 5 分钟；空 entries 当心跳也更新状态。
func TestNodePushAgentStatus1330(t *testing.T) {
	s, tok := newNodeAgentServer1330(t)
	rec := callNode1330(t, s, "POST", "/api/v1/node/push",
		`{"node_id":"203.0.113.1","agent_version":"0.1.0","entries":[]}`, tok)
	if rec.Code != 200 {
		t.Fatalf("空推送（心跳）应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	ext := s.state.ext("203.0.113.1")
	if ext.MeteringSource != "agent" || ext.AgentVersion != "0.1.0" || ext.AgentLastPushAt == "" {
		t.Fatalf("推送后落地状态不对：metering_source=%q version=%q last_push=%q",
			ext.MeteringSource, ext.AgentVersion, ext.AgentLastPushAt)
	}
	if !s.nodeAgentLive("203.0.113.1") {
		t.Fatalf("刚推送过应算在线")
	}
	st := s.nodeAgentStatusPayload("203.0.113.1")
	if st["online"] != true || st["version"] != "0.1.0" || st["metering_source"] != "agent" {
		t.Fatalf("agent_status 不对：%v", st)
	}
	// 6 分钟前推送过：离线。
	ext.AgentLastPushAt = time.Now().Add(-6 * time.Minute).UTC().Format(time.RFC3339)
	if s.nodeAgentLive("203.0.113.1") {
		t.Fatalf("6 分钟没推送应算离线")
	}
	// 没标 agent 来源：不算在线。
	ext.AgentLastPushAt = time.Now().UTC().Format(time.RFC3339)
	ext.MeteringSource = ""
	if s.nodeAgentLive("203.0.113.1") {
		t.Fatalf("metering_source 不是 agent 时不应算在线")
	}
}

// 翻周期：period_id 变了，本期清零、总量累加、周期号跟上。
func TestNodePushPeriodRollover1330(t *testing.T) {
	s, tok := newNodeAgentServer1330(t)
	push := func(body string) {
		rec := callNode1330(t, s, "POST", "/api/v1/node/push", body, tok)
		if rec.Code != 200 {
			t.Fatalf("推送应 200，实际 %d：%s", rec.Code, rec.Body.String())
		}
	}
	push(`{"node_id":"203.0.113.1","entries":[{"user_id":"merged:ms-1","period_id":0,"seq":1,"up_bytes":100,"down_bytes":100}]}`)
	push(`{"node_id":"203.0.113.1","entries":[{"user_id":"merged:ms-1","period_id":1,"seq":1,"up_bytes":50,"down_bytes":60}]}`)
	up, down, upP, downP, ok := s.userTraffic.totals("merged:ms-1")
	if !ok || up != 150 || down != 160 || upP != 50 || downP != 60 {
		t.Fatalf("翻周期后总量应 150/160、本期 50/60，实际 up=%d down=%d upP=%d downP=%d ok=%v", up, down, upP, downP, ok)
	}
}

// 去重表重启后仍有效：同 PanelHome 起第二台服务器，重推同一条仍去重。
func TestNodePushSeenSurvivesRestart1330(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	panelHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir, "nodes.txt"), []byte(subTestNodesTxt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(subTestRelayConf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PANEL_INITIAL_PASSWORD", "test-password-123")
	mkServer := func() *Server {
		s, err := newServer(Env{PanelHome: panelHome, RealmDir: realmDir, KitDir: kitDir, Port: 11111})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s1 := mkServer()
	tok := generateRemoteToken()
	s1.cfg.RemoteTokenHash = hashRemoteToken(tok)
	s1.cfg.SubUsers = []SubUser{{ID: "su-1", Name: "小明", TokenHash: "hash-su-1"}}
	if err := s1.saveConfig(); err != nil {
		t.Fatal(err)
	}
	body := `{"node_id":"203.0.113.1","entries":[{"user_id":"subuser:su-1","period_id":0,"seq":1,"up_bytes":100,"down_bytes":100}]}`
	rec := callNode1330(t, s1, "POST", "/api/v1/node/push", body, tok)
	if rec.Code != 200 || decodeNode1330(t, rec)["accepted"] != float64(1) {
		t.Fatalf("首次推送应 accepted=1，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 重启（同目录新 Server）：去重表从磁盘重载。
	s2 := mkServer()
	s2.cfg.RemoteTokenHash = hashRemoteToken(tok)
	rec = callNode1330(t, s2, "POST", "/api/v1/node/push", body, tok)
	if rec.Code != 200 {
		t.Fatalf("重启后推送应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	resp := decodeNode1330(t, rec)
	if resp["duplicates"] != float64(1) || resp["accepted"] != float64(0) {
		t.Fatalf("重启后重推应去重，实际 %v", resp)
	}
	up, down, _, _, _ := s2.userTraffic.totals("subuser:su-1")
	if up != 100 || down != 100 {
		t.Fatalf("重启后重推不应重复计，实际 up=%d down=%d", up, down)
	}
}

// 备份口径：node_uuids 随备份走（sanitize → 备份结构 → 恢复结构），
// 丢了它 Phase 1 的用户就得重新导入。
func TestNodeUUIDsBackupRoundTrip1330(t *testing.T) {
	users := []SubUser{{ID: "su-1", Name: "小明",
		NodeUUIDs: map[string]string{"203.0.113.1": testNodeUUID}}}
	backed := sanitizeSubUsers(users, true)
	if len(backed) != 1 || backed[0].NodeUUIDs["203.0.113.1"] != testNodeUUID {
		t.Fatalf("备份里 node_uuids 丢了：%+v", backed)
	}
	b, _ := json.Marshal(backed)
	if !strings.Contains(string(b), `"node_uuids"`) {
		t.Fatalf("备份 JSON 里应有 node_uuids：%s", b)
	}
	subs := []MergedSub{{ID: "ms-1", Name: "合并一",
		NodeUUIDs: map[string]string{"203.0.113.1": testNodeUUID}}}
	backedM := sanitizeMergedSubs(subs, true)
	if len(backedM) != 1 || backedM[0].NodeUUIDs["203.0.113.1"] != testNodeUUID {
		t.Fatalf("合并订阅备份里 node_uuids 丢了：%+v", backedM)
	}
}
