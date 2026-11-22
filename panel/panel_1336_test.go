package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------- 1.3.36：Phase 1 切片 2——central 用户表同步 ----------
//
// 方案 a：管理面板向远程同步用户记录，agent 继续向入网的远程面板拉表、
// 推数。只同步用户表、不改订阅链接、不执法、不动 agent。
// 脱敏：只用 example.com / 203.0.113.x / 假 UUID。

func callCentralUsers1336(t *testing.T, s *Server, tok, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	s.requireCentralToken(s.handleCentralUsers)(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// TestCentralUsersReceive1336 接收端：认证、校验、全量替换、空表清空、
// 没变不落盘、记审计。
func TestCentralUsersReceive1336(t *testing.T) {
	fakeSystemctl122(t)
	s := newTestSubServer(t)
	tok := generateRemoteToken()
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)
	// 没令牌 → 401。
	if code, _ := callCentralUsers1336(t, s, "", "POST", "/api/v1/central/users", `{}`); code != 401 {
		t.Fatalf("没令牌应 401，实际 %d", code)
	}
	// 下发一条（带该落地的 UUID）。
	body := `{"landing_addr":"203.0.113.1","users":[{"user_id":"subuser:su-a","kind":"subuser","id":"su-a","name":"远程用户","email":"u-sub-su-a@node","node_uuids":{"203.0.113.1":"11111111-2222-3333-4444-555555555555"}}]}`
	code, resp := callCentralUsers1336(t, s, tok, "POST", "/api/v1/central/users", body)
	if code != 200 || resp["applied"] != float64(1) || resp["skipped"] != float64(0) {
		t.Fatalf("下发应 applied=1 skipped=0，实际 code=%d resp=%v", code, resp)
	}
	if resp["panel_version"] != Version {
		t.Fatalf("下发响应应带 panel_version，实际 %v", resp)
	}
	rows := s.cfg.CentralUsers["203.0.113.1"]
	if len(rows) != 1 || rows[0].UserID != "subuser:su-a" || rows[0].NodeUUIDs["203.0.113.1"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("接收后集中用户表应有 1 条，实际 %+v", rows)
	}
	// 审计里应记一笔。
	audit, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if !strings.Contains(string(audit), "接收集中用户") {
		t.Fatalf("审计里应记一笔接收集中用户：%s", audit)
	}
	// 坏行跳过：格式不对、缺该落地 UUID、kind/id 对不上、重复行。
	bad := `{"landing_addr":"203.0.113.1","users":[
		{"user_id":"nonsense","kind":"subuser","id":"x","node_uuids":{"203.0.113.1":"11111111-2222-3333-4444-555555555555"}},
		{"user_id":"subuser:su-b","kind":"subuser","id":"su-b","node_uuids":{}},
		{"user_id":"subuser:su-c","kind":"merged","id":"su-c","node_uuids":{"203.0.113.1":"11111111-2222-3333-4444-555555555555"}},
		{"user_id":"subuser:su-a","kind":"subuser","id":"su-a","node_uuids":{"203.0.113.1":"11111111-2222-3333-4444-555555555555"}},
		{"user_id":"subuser:su-a","kind":"subuser","id":"su-a","node_uuids":{"203.0.113.1":"11111111-2222-3333-4444-555555555555"}}
	]}`
	code, resp = callCentralUsers1336(t, s, tok, "POST", "/api/v1/central/users", bad)
	if code != 200 || resp["applied"] != float64(1) || resp["skipped"] != float64(4) {
		t.Fatalf("坏行应跳过 4 条、应用 1 条，实际 code=%d resp=%v", code, resp)
	}
	// 不存在的落地 → 400。
	code, _ = callCentralUsers1336(t, s, tok, "POST", "/api/v1/central/users", `{"landing_addr":"198.51.100.99","users":[]}`)
	if code != 400 {
		t.Fatalf("不存在的落地应 400，实际 %d", code)
	}
	// 同样内容再推一次 → config.json 不应重写。
	before, _ := os.ReadFile(s.cfgPath)
	code, resp = callCentralUsers1336(t, s, tok, "POST", "/api/v1/central/users", bad)
	if code != 200 {
		t.Fatalf("重复推送应 200，实际 %d", code)
	}
	after, _ := os.ReadFile(s.cfgPath)
	if string(before) != string(after) {
		t.Fatalf("内容没变的推送不应重写 config.json")
	}
	// 空表清空该落地的集中用户。
	code, resp = callCentralUsers1336(t, s, tok, "POST", "/api/v1/central/users", `{"landing_addr":"203.0.113.1","users":[]}`)
	if code != 200 || resp["applied"] != float64(0) {
		t.Fatalf("空表应 applied=0，实际 code=%d resp=%v", code, resp)
	}
	if len(s.cfg.CentralUsers["203.0.113.1"]) != 0 {
		t.Fatalf("空表应清空该落地的集中用户，实际 %+v", s.cfg.CentralUsers["203.0.113.1"])
	}
}

// TestCentralUsersMerge1336 下发合并：集中用户与本地用户同 user_id
// 只出一行、NodeUUIDs 按落地合并；本地记录优先；ETag 随集中用户变。
func TestCentralUsersMerge1336(t *testing.T) {
	s, tok := newNodeAgentServer1330(t)
	// 本地已有的 su-1（203.0.113.1 → testNodeUUID）与 ms-1 不动；
	// 再加一个本地用户 su-dup，与集中用户同名同落地、UUID 不同。
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{
		ID: "su-dup", Name: "本地同名", TokenHash: "hash-dup",
		CreatedAt: "2026-11-20T00:00:00Z", Selection: []SubSelection{{Panel: "local", Landing: 1}},
		NodeUUIDs: map[string]string{"203.0.113.1": "55555555-5555-4555-8555-555555555555"},
	})
	s.cfg.CentralUsers = map[string][]nodeUser{
		"203.0.113.1": {
			{UserID: "subuser:su-remote", Kind: "subuser", ID: "su-remote", Name: "远程用户", Email: "u-sub-su-remote@node", NodeUUIDs: map[string]string{"203.0.113.1": "22222222-2222-4333-8444-555555555555"}},
			{UserID: "merged:ms-remote", Kind: "merged", ID: "ms-remote", Name: "远程合并", Email: "u-merged-ms-remote@node", NodeUUIDs: map[string]string{"203.0.113.1": "33333333-3333-4333-8444-555555555555"}},
			{UserID: "subuser:su-dup", Kind: "subuser", ID: "su-dup", Name: "集中同名", Email: "u-sub-su-dup@node", NodeUUIDs: map[string]string{"203.0.113.1": "66666666-6666-4666-8666-666666666666"}},
		},
		"203.0.113.2": {
			{UserID: "subuser:su-remote", Kind: "subuser", ID: "su-remote", Name: "远程用户", Email: "u-sub-su-remote@node", NodeUUIDs: map[string]string{"203.0.113.2": "44444444-4444-4444-8444-555555555555"}},
		},
	}
	rec := callNode1330(t, s, "GET", "/api/v1/node/users?node_id=203.0.113.1", "", tok)
	if rec.Code != 200 {
		t.Fatalf("拉用户表应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	resp := decodeNode1330(t, rec)
	users, _ := resp["users"].([]any)
	if len(users) != 5 {
		t.Fatalf("用户表应有 5 行（本地 su-1/ms-1/su-dup + 集中 su-remote/ms-remote），实际 %d：%s", len(users), rec.Body.String())
	}
	byID := map[string]map[string]any{}
	for _, u := range users {
		m, _ := u.(map[string]any)
		byID[m["user_id"].(string)] = m
	}
	remote := byID["subuser:su-remote"]
	uuids, _ := remote["node_uuids"].(map[string]any)
	if uuids["203.0.113.1"] != "22222222-2222-4333-8444-555555555555" || uuids["203.0.113.2"] != "44444444-4444-4444-8444-555555555555" {
		t.Fatalf("同一用户在两个落地的 UUID 应合并到一行，实际 %v", uuids)
	}
	dup := byID["subuser:su-dup"]
	uuids, _ = dup["node_uuids"].(map[string]any)
	if uuids["203.0.113.1"] != "55555555-5555-4555-8555-555555555555" {
		t.Fatalf("本地与集中同名同落地时应以本地 UUID 为准，实际 %v", uuids)
	}
	etag1 := rec.Header().Get("ETag")
	if etag1 == "" {
		t.Fatalf("下发应带 ETag 头")
	}
	// 没变 → 304。
	if rec := callNode1330(t, s, "GET", "/api/v1/node/users?node_id=203.0.113.1", "", tok); rec.Header().Get("ETag") != etag1 {
		t.Fatalf("没变时 ETag 应稳定，实际 %q vs %q", rec.Header().Get("ETag"), etag1)
	}
	req := httptest.NewRequest("GET", "/api/v1/node/users?node_id=203.0.113.1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("If-None-Match", etag1)
	rec2 := httptest.NewRecorder()
	s.routes().ServeHTTP(rec2, req)
	if rec2.Code != 304 {
		t.Fatalf("ETag 没变应 304，实际 %d", rec2.Code)
	}
	// 集中用户变了 → ETag 跟着变，旧 ETag 不再 304。
	s.cfg.CentralUsers["203.0.113.2"] = append(s.cfg.CentralUsers["203.0.113.2"], nodeUser{UserID: "subuser:su-new", Kind: "subuser", ID: "su-new", Name: "新集中用户", Email: "u-sub-su-new@node", NodeUUIDs: map[string]string{"203.0.113.2": "77777777-7777-4777-8777-777777777777"}})
	_, etag2 := s.nodeUsersPayload()
	if etag2 == etag1 {
		t.Fatalf("集中用户变了 ETag 应变，实际没变")
	}
	req = httptest.NewRequest("GET", "/api/v1/node/users?node_id=203.0.113.1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("If-None-Match", etag1)
	rec3 := httptest.NewRecorder()
	s.routes().ServeHTTP(rec3, req)
	if rec3.Code != 200 {
		t.Fatalf("ETag 变了旧值应 200，实际 %d", rec3.Code)
	}
}

// centralUsersCapture1336 假远程面板：记录收到的集中用户推送。
type centralUsersCapture1336 struct {
	mu     sync.Mutex
	bodies []map[string]any
	fail   bool
}

func newFakeRemoteCentralUsers1336(t *testing.T, cap *centralUsersCapture1336) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"panel_version":"1.3.36","realm":{"running":true},"disk_pct":42.5,"landings":[{"index":1,"landing":"203.0.113.1","domain":"example.com","mode":"standard","ports":[{"proto":"tcp","listen":443,"remote":443}],"online":true},{"index":2,"landing":"203.0.113.2","domain":"node2.example.com","mode":"door","ports":[{"proto":"tcp","listen":20443,"remote":443}],"online":true}]}`))
	})
	mux.HandleFunc("/api/v1/central/users", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		cap.mu.Lock()
		cap.bodies = append(cap.bodies, body)
		fail := cap.fail
		cap.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"ok":false,"error":"远程炸了"}`))
			return
		}
		users, _ := body["users"].([]any)
		_, _ = w.Write([]byte(`{"ok":true,"applied":` + itoa1336(len(users)) + `,"skipped":0,"panel_version":"1.3.36"}`))
	})
	mux.HandleFunc("/api/v1/central/user-forwards", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"applied":0,"skipped":0,"panel_version":"1.3.36"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func itoa1336(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func seedRemoteSnapshot1336(s *Server, id string, version string, landings []remoteLandingSummary) {
	s.centralMu.Lock()
	defer s.centralMu.Unlock()
	s.centralCache[id] = &RemoteStatusSnapshot{ID: id, Name: "测试远程", Online: true, Version: version, Landings: landings}
}

// TestCentralUsersPush1336 下推：请求体形状（每行只带该落地的 UUID）、
// 旧远程 404、连不上。
func TestCentralUsersPush1336(t *testing.T) {
	cap := &centralUsersCapture1336{}
	srv := newFakeRemoteCentralUsers1336(t, cap)
	s := newTestSubServer(t)
	rem := RemotePanel{ID: "rm-1", Name: "测试远程", BaseURL: srv.URL, Token: generateRemoteToken()}
	users := []nodeUser{{
		UserID: "subuser:su-a", Kind: "subuser", ID: "su-a", Name: "用户甲", Email: "u-sub-su-a@node",
		NodeUUIDs: map[string]string{"203.0.113.1": "11111111-2222-3333-4444-555555555555"},
	}}
	applied, skipped, err := s.pushCentralUsersToRemote(rem, "203.0.113.1", users)
	if err != nil || applied != 1 || skipped != 0 {
		t.Fatalf("下推应 applied=1 skipped=0，实际 applied=%d skipped=%d err=%v", applied, skipped, err)
	}
	cap.mu.Lock()
	if len(cap.bodies) != 1 {
		cap.mu.Unlock()
		t.Fatalf("假远程应收到 1 次推送，实际 %d", len(cap.bodies))
	}
	body := cap.bodies[0]
	cap.mu.Unlock()
	if body["landing_addr"] != "203.0.113.1" {
		t.Fatalf("推送应带 landing_addr=203.0.113.1，实际 %v", body)
	}
	got, _ := body["users"].([]any)
	if len(got) != 1 {
		t.Fatalf("推送应带 1 个用户，实际 %v", body["users"])
	}
	row, _ := got[0].(map[string]any)
	uuids, _ := row["node_uuids"].(map[string]any)
	if len(uuids) != 1 || uuids["203.0.113.1"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("每行应只带该落地的 UUID，实际 %v", uuids)
	}
	// 旧远程（缓存版本 1.3.35）→ 报错提 1.3.36。
	s.cfg.Remotes = append(s.cfg.Remotes, rem)
	seedRemoteSnapshot1336(s, "rm-1", "1.3.35", nil)
	if _, _, err := s.pushCentralUsersToRemote(rem, "203.0.113.1", users); err == nil || !strings.Contains(err.Error(), "1.3.36") {
		t.Fatalf("旧远程应报版本太旧（提 1.3.36），实际 err=%v", err)
	}
	// 无此接口的远程（404）→ 报错提 1.3.36。
	srvOld := httptest.NewServer(http.NewServeMux())
	t.Cleanup(srvOld.Close)
	remOld := RemotePanel{ID: "rm-old", Name: "旧远程", BaseURL: srvOld.URL, Token: generateRemoteToken()}
	if _, _, err := s.pushCentralUsersToRemote(remOld, "203.0.113.1", users); err == nil || !strings.Contains(err.Error(), "1.3.36") {
		t.Fatalf("无接口的远程应报版本太旧（提 1.3.36），实际 err=%v", err)
	}
	// 连不上的远程 → 报错。
	dead := httptest.NewServer(http.NewServeMux())
	deadURL := dead.URL
	dead.Close()
	remDead := RemotePanel{ID: "rm-dead", Name: "掉线远程", BaseURL: deadURL, Token: generateRemoteToken()}
	if _, _, err := s.pushCentralUsersToRemote(remDead, "203.0.113.1", users); err == nil {
		t.Fatalf("连不上的远程应返回错误")
	}
}

// TestCentralUsersSyncCreateDelete1336 新建订阅即同步、删除订阅即清掉：
// 走新建/删除处理器，假远程收到对应的全量推送。
func TestCentralUsersSyncCreateDelete1336(t *testing.T) {
	fakeSystemctl122(t)
	cap := &centralUsersCapture1336{}
	srv := newFakeRemoteCentralUsers1336(t, cap)
	s := newTestSubServer(t)
	rem := RemotePanel{ID: "rm-1", Name: "测试远程", BaseURL: srv.URL, Token: generateRemoteToken()}
	s.cfg.Remotes = append(s.cfg.Remotes, rem)
	seedRemoteSnapshot1336(s, "rm-1", "1.3.36", []remoteLandingSummary{{Index: 1, Landing: "203.0.113.1"}})
	// 新建一个勾选远程落地的订阅用户（不限流：转发同步直接跳过，只验用户表同步）。
	rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"远程用户","selection":[{"panel":"rm-1","landing":1}]}`)
	if rec.Code != 200 {
		t.Fatalf("新建订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	u := s.cfg.SubUsers[0]
	uuid := u.NodeUUIDs["203.0.113.1"]
	if uuid == "" {
		t.Fatalf("新建时应按远程落地生成 UUID，实际 %v", u.NodeUUIDs)
	}
	cap.mu.Lock()
	if len(cap.bodies) != 1 {
		cap.mu.Unlock()
		t.Fatalf("新建后假远程应收到 1 次用户表推送，实际 %d 次", len(cap.bodies))
	}
	body := cap.bodies[0]
	cap.mu.Unlock()
	got, _ := body["users"].([]any)
	if len(got) != 1 {
		t.Fatalf("新建推送应带 1 个用户，实际 %v", body["users"])
	}
	row, _ := got[0].(map[string]any)
	if row["user_id"] != "subuser:"+u.ID {
		t.Fatalf("新建推送的用户应是刚建的订阅，实际 %v", row)
	}
	uuids, _ := row["node_uuids"].(map[string]any)
	if uuids["203.0.113.1"] != uuid {
		t.Fatalf("新建推送的 UUID 应与本机记录一致，实际 %v vs %s", uuids, uuid)
	}
	// 删除该订阅 → 假远程应收到空表推送。
	delReq := httptest.NewRequest("DELETE", "/api/v1/subscriptions/users/"+u.ID, nil)
	delReq.SetPathValue("id", u.ID)
	rec = httptest.NewRecorder()
	s.handleSubUsersDelete(rec, delReq)
	if rec.Code != 200 {
		t.Fatalf("删除订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	cap.mu.Lock()
	if len(cap.bodies) != 2 {
		cap.mu.Unlock()
		t.Fatalf("删除后假远程应再收到 1 次推送（共 2 次），实际 %d 次", len(cap.bodies))
	}
	body = cap.bodies[1]
	cap.mu.Unlock()
	got, _ = body["users"].([]any)
	if len(got) != 0 {
		t.Fatalf("删除推送应是空表，实际 %v", body["users"])
	}
}

// TestCentralUsersPushAccept1336 推数校验：集中用户认、陌生用户拒。
func TestCentralUsersPushAccept1336(t *testing.T) {
	s, tok := newNodeAgentServer1330(t)
	s.cfg.CentralUsers = map[string][]nodeUser{
		"203.0.113.1": {{UserID: "subuser:su-central", Kind: "subuser", ID: "su-central", Name: "集中用户", Email: "u-sub-su-central@node", NodeUUIDs: map[string]string{"203.0.113.1": "88888888-8888-4888-8888-888888888888"}}},
	}
	body := `{"node_id":"203.0.113.1","agent_version":"0.2.0","entries":[
		{"user_id":"subuser:su-central","period_id":0,"seq":1,"up_bytes":100,"down_bytes":200},
		{"user_id":"subuser:su-nope","period_id":0,"seq":1,"up_bytes":100,"down_bytes":200}
	]}`
	rec := callNode1330(t, s, "POST", "/api/v1/node/push", body, tok)
	if rec.Code != 200 {
		t.Fatalf("推数应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	resp := decodeNode1330(t, rec)
	if resp["accepted"] != float64(1) {
		t.Fatalf("集中用户那条应 accepted=1，实际 resp=%v", resp)
	}
	rejected, _ := resp["rejected"].([]any)
	if len(rejected) != 1 {
		t.Fatalf("陌生用户那条应 rejected=1，实际 resp=%v", resp)
	}
	rj, _ := rejected[0].(map[string]any)
	if rj["user_id"] != "subuser:su-nope" || rj["reason"] != "订阅用户不存在" {
		t.Fatalf("陌生用户应以“订阅用户不存在”拒掉，实际 %v", rj)
	}
}

// TestCentralUsersHeal1336 兜底同步：失败记审计、每小时每远程最多一次。
func TestCentralUsersHeal1336(t *testing.T) {
	cap := &centralUsersCapture1336{fail: true}
	srv := newFakeRemoteCentralUsers1336(t, cap)
	s := newTestSubServer(t)
	rem := RemotePanel{ID: "rm-1", Name: "测试远程", BaseURL: srv.URL, Token: generateRemoteToken()}
	s.cfg.Remotes = append(s.cfg.Remotes, rem)
	seedRemoteSnapshot1336(s, "rm-1", "1.3.36", []remoteLandingSummary{{Index: 1, Landing: "203.0.113.1"}})
	s.healCentralUsers()
	audit, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if n := strings.Count(string(audit), "集中用户表兜底同步"); n != 1 {
		t.Fatalf("第一次兜底失败应记 1 笔审计，实际 %d 笔：%s", n, audit)
	}
	// 一小时内再失败也不再记。
	s.healCentralUsers()
	audit, _ = os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if n := strings.Count(string(audit), "集中用户表兜底同步"); n != 1 {
		t.Fatalf("一小时内重复失败不应再记审计，实际 %d 笔", n)
	}
	// 远程好了 → 兜底成功，不记审计，假远程收到推送。
	cap.mu.Lock()
	cap.fail = false
	cap.mu.Unlock()
	s.healCentralUsers()
	cap.mu.Lock()
	n := len(cap.bodies)
	cap.mu.Unlock()
	if n != 3 {
		t.Fatalf("三次兜底应共推 3 次（2 次失败 + 1 次成功），实际 %d 次", n)
	}
}
