package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ---------- 1.3.35：Phase 1 切片 1——每人 UUID 生成与下发 ----------
//
// 只发 UUID、不改订阅链接、不执法、不动 agent。脱敏：只用
// example.com / 203.0.113.x / 假 UUID。

var nodeUUIDRe1335 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestNodeUUIDFormat1335 UUIDv4 格式与唯一性（1000 个不撞）。
func TestNodeUUIDFormat1335(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		u := newNodeUUID()
		if !nodeUUIDRe1335.MatchString(u) {
			t.Fatalf("UUID 格式不对：%q", u)
		}
		if seen[u] {
			t.Fatalf("UUID 重复：%q", u)
		}
		seen[u] = true
	}
}

// TestNodeUUIDCreateSubUser1335 新建订阅用户即按生效勾选生成 UUID；
// 空勾选 = 全部本地落地；两人同一落地的 UUID 不同。
func TestNodeUUIDCreateSubUser1335(t *testing.T) {
	s := newTestSubServer(t)
	rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"小明","selection":[{"panel":"local","landing":1}]}`)
	if rec.Code != 200 {
		t.Fatalf("新建订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	u := s.cfg.SubUsers[0]
	if len(u.NodeUUIDs) != 1 || u.NodeUUIDs["203.0.113.1"] == "" {
		t.Fatalf("应只给落地 203.0.113.1 生成 UUID，实际 %v", u.NodeUUIDs)
	}
	if !nodeUUIDRe1335.MatchString(u.NodeUUIDs["203.0.113.1"]) {
		t.Fatalf("生成的 UUID 格式不对：%q", u.NodeUUIDs["203.0.113.1"])
	}
	rec = callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"小红"}`)
	if rec.Code != 200 {
		t.Fatalf("空勾选新建应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	u2 := s.cfg.SubUsers[1]
	if len(u2.NodeUUIDs) != 2 || u2.NodeUUIDs["203.0.113.1"] == "" || u2.NodeUUIDs["203.0.113.2"] == "" {
		t.Fatalf("空勾选应给全部本地落地生成 UUID，实际 %v", u2.NodeUUIDs)
	}
	if u.NodeUUIDs["203.0.113.1"] == u2.NodeUUIDs["203.0.113.1"] {
		t.Fatalf("两人同一落地的 UUID 不应相同")
	}
}

// TestNodeUUIDCreateIndMerged1335 1.4.40：旧合并订阅已彻底移除，新建独立
// 合并订阅时按勾选生成 UUID 的行为与订阅用户一致，见
// TestNodeUUIDCreateUser1335。
func TestNodeUUIDCreateIndMerged1335(t *testing.T) {
	s := newTestSubServer(t)
	rec := callJSON130(t, s.handleIndMergedSubsCreate, "POST", "/api/v1/subscriptions/independent-merged", `{"name":"合并一","selection":[{"panel":"local","landing":1},{"panel":"local","landing":2}]}`)
	if rec.Code != 200 {
		t.Fatalf("新建独立合并订阅应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if len(s.cfg.IndependentMergedSubs) != 1 {
		t.Fatalf("新建后应有 1 条独立合并订阅记录，实际 %+v", s.cfg.IndependentMergedSubs)
	}
	uuids := s.cfg.IndependentMergedSubs[0].NodeUUIDs
	if len(uuids) != 2 || uuids["203.0.113.1"] == "" || uuids["203.0.113.2"] == "" {
		t.Fatalf("新建时应按勾选给两个落地生成 UUID，实际 %v", uuids)
	}
}

// TestNodeUUIDBackfillExisting1335 已有用户（1.3.34 升上来的旧数据、
// 无 UUID）补生成；补一次后稳定不变；saveConfig 落盘后从磁盘读回还在。
func TestNodeUUIDBackfillExisting1335(t *testing.T) {
	s := newTestSubServer(t)
	s.cfg.SubUsers = []SubUser{{
		ID: "su-old", Name: "老用户", TokenHash: "hash-old",
		CreatedAt: "2026-11-20T00:00:00Z", Selection: []SubSelection{{Panel: "local", Landing: 1}},
	}}
	s.cfg.IndependentMergedSubs = []IndependentMergedSub{{
		ID: "ims-old", Name: "老独立合并", TokenHash: "hash-old",
		CreatedAt: "2026-11-20T00:00:00Z", Selection: []SubSelection{{Panel: "local", Landing: 2}},
	}}
	if !s.backfillNodeUUIDs() {
		t.Fatalf("旧用户应补生成 UUID")
	}
	u1 := s.cfg.SubUsers[0].NodeUUIDs["203.0.113.1"]
	m1 := s.cfg.IndependentMergedSubs[0].NodeUUIDs["203.0.113.2"]
	if u1 == "" || m1 == "" {
		t.Fatalf("补生成后 UUID 不应为空：用户 %v，独立合并 %v", s.cfg.SubUsers[0].NodeUUIDs, s.cfg.IndependentMergedSubs[0].NodeUUIDs)
	}
	if s.backfillNodeUUIDs() {
		t.Fatalf("第二次补生成不应再有改动")
	}
	if s.cfg.SubUsers[0].NodeUUIDs["203.0.113.1"] != u1 || s.cfg.IndependentMergedSubs[0].NodeUUIDs["203.0.113.2"] != m1 {
		t.Fatalf("补生成的 UUID 应稳定不变")
	}
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var back PanelConfig
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("config.json 解析失败：%v", err)
	}
	if back.SubUsers[0].NodeUUIDs["203.0.113.1"] != u1 || back.IndependentMergedSubs[0].NodeUUIDs["203.0.113.2"] != m1 {
		t.Fatalf("落盘后读回的 UUID 应一致，实际 %v / %v", back.SubUsers[0].NodeUUIDs, back.IndependentMergedSubs[0].NodeUUIDs)
	}
}

// TestNodeUUIDUpdateKeepsAndAdds1335 编辑勾选：新落地补 UUID、已有
// UUID 不变；去掉勾选的落地 UUID 保留（只补不剪）。
func TestNodeUUIDUpdateKeepsAndAdds1335(t *testing.T) {
	s := newTestSubServer(t)
	rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"小明","selection":[{"panel":"local","landing":1}]}`)
	if rec.Code != 200 {
		t.Fatalf("新建应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	id := s.cfg.SubUsers[0].ID
	uuid1 := s.cfg.SubUsers[0].NodeUUIDs["203.0.113.1"]

	put := func(body string) {
		t.Helper()
		req := httptest.NewRequest("PUT", "/api/v1/subscriptions/users/"+id, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		s.handleSubUsersUpdate(rec, req)
		if rec.Code != 200 {
			t.Fatalf("编辑应 200，实际 %d：%s", rec.Code, rec.Body.String())
		}
	}
	// 加落地 2：旧 UUID 不变、新落地补一个。
	put(`{"name":"小明","selection":[{"panel":"local","landing":1},{"panel":"local","landing":2}]}`)
	u := s.cfg.SubUsers[0]
	if u.NodeUUIDs["203.0.113.1"] != uuid1 {
		t.Fatalf("编辑不应换掉已有 UUID：%q 变 %q", uuid1, u.NodeUUIDs["203.0.113.1"])
	}
	if u.NodeUUIDs["203.0.113.2"] == "" {
		t.Fatalf("新勾选的落地应补 UUID，实际 %v", u.NodeUUIDs)
	}
	// 去掉落地 1：它的 UUID 应保留在表里（只补不剪，复勾时稳定）。
	put(`{"name":"小明","selection":[{"panel":"local","landing":2}]}`)
	u = s.cfg.SubUsers[0]
	if u.NodeUUIDs["203.0.113.1"] != uuid1 {
		t.Fatalf("去掉勾选的落地 UUID 应保留，实际 %v", u.NodeUUIDs)
	}
}

// TestNodeUUIDDeleteRecovers1335 删除用户时 UUID 随整条记录回收。
func TestNodeUUIDDeleteRecovers1335(t *testing.T) {
	s := newTestSubServer(t)
	rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"小明","selection":[{"panel":"local","landing":1}]}`)
	if rec.Code != 200 {
		t.Fatalf("新建应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	id := s.cfg.SubUsers[0].ID
	if s.cfg.SubUsers[0].NodeUUIDs["203.0.113.1"] == "" {
		t.Fatalf("新建时应已生成 UUID")
	}
	req := httptest.NewRequest("DELETE", "/api/v1/subscriptions/users/"+id, nil)
	req.SetPathValue("id", id)
	del := httptest.NewRecorder()
	s.handleSubUsersDelete(del, req)
	if del.Code != 200 {
		t.Fatalf("删除应 200，实际 %d：%s", del.Code, del.Body.String())
	}
	if len(s.cfg.SubUsers) != 0 {
		t.Fatalf("删除后用户应没了，实际 %d 条", len(s.cfg.SubUsers))
	}
}

// TestNodeUUIDBackupRestore1335 定时备份带走 node_uuids，跨机恢复后
// UUID 原样回来（用户不用重新导入）。
func TestNodeUUIDBackupRestore1335(t *testing.T) {
	src := newTestSubServer(t)
	// 真机上 config.toml 一直有 [[endpoints]]，测试夹具默认是空文件，
	// 先补一份最小配置（否则恢复写回 config.toml 时校验失败）。
	if err := os.WriteFile(filepath.Join(src.env.RealmDir, "config.toml"), []byte("[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := callJSON130(t, src.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"小明","selection":[{"panel":"local","landing":1},{"panel":"local","landing":2}]}`)
	if rec.Code != 200 {
		t.Fatalf("新建应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	want := map[string]string{}
	for addr, uuid := range src.cfg.SubUsers[0].NodeUUIDs {
		want[addr] = uuid
	}
	p := backupPayload1329(t, src)
	if len(p.SubUsers) != 1 || p.SubUsers[0].NodeUUIDs["203.0.113.1"] == "" || p.SubUsers[0].NodeUUIDs["203.0.113.2"] == "" {
		t.Fatalf("备份文件里应带 node_uuids，实际 %v", p.SubUsers)
	}
	dst := newTestSubServer(t)
	var notes []string
	_, fail := dst.applyImportPayload(p, importApplyOpts{
		preBackupLabel: "测试恢复前备份", auditAction: "测试恢复", auditDetail: "1335 备份恢复",
		restartWarnPrefix: "导入成功，但重启失败：", verifyWarnPrefix: "", user: "test", notes: &notes,
	})
	if fail != nil {
		t.Fatalf("跨机恢复应成功，实际失败：%v", fail)
	}
	if len(dst.cfg.SubUsers) != 1 {
		t.Fatalf("恢复后应有 1 个用户，实际 %d", len(dst.cfg.SubUsers))
	}
	for addr, uuid := range want {
		if dst.cfg.SubUsers[0].NodeUUIDs[addr] != uuid {
			t.Fatalf("恢复后 UUID 应原样回来：%s 期望 %q，实际 %q", addr, uuid, dst.cfg.SubUsers[0].NodeUUIDs[addr])
		}
	}
}

// TestNodeUUIDUsersEndpoint1335 用户表下发带 node_uuids；UUID 变了
// ETag 跟着变，没变时 If-None-Match 返回 304。
func TestNodeUUIDUsersEndpoint1335(t *testing.T) {
	s, tok := newNodeAgentServer1330(t)
	rec := callNode1330(t, s, "GET", "/api/v1/node/users?node_id=203.0.113.1", "", tok)
	if rec.Code != 200 {
		t.Fatalf("用户表应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	etag1 := rec.Header().Get("ETag")
	if etag1 == "" {
		t.Fatalf("下发应带 ETag 头")
	}
	resp := decodeNode1330(t, rec)
	users, _ := resp["users"].([]any)
	byID := map[string]map[string]any{}
	for _, u := range users {
		m, _ := u.(map[string]any)
		byID[m["id"].(string)] = m
	}
	su1 := byID["su-1"]
	nu, _ := su1["node_uuids"].(map[string]any)
	if nu["203.0.113.1"] != testNodeUUID {
		t.Fatalf("su-1 下发的 node_uuids 应含 203.0.113.1=%s，实际 %v", testNodeUUID, nu)
	}
	// ETag 没变 → 304。
	req := httptest.NewRequest("GET", "/api/v1/node/users?node_id=203.0.113.1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("If-None-Match", etag1)
	rec304 := httptest.NewRecorder()
	s.routes().ServeHTTP(rec304, req)
	if rec304.Code != http.StatusNotModified {
		t.Fatalf("ETag 没变应 304，实际 %d", rec304.Code)
	}
	// 加一个 UUID → ETag 应变，且下发里多出新落地。
	s.cfg.SubUsers[0].NodeUUIDs["203.0.113.2"] = newNodeUUID()
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
	rec2 := callNode1330(t, s, "GET", "/api/v1/node/users?node_id=203.0.113.1", "", tok)
	if rec2.Code != 200 {
		t.Fatalf("用户表应 200，实际 %d", rec2.Code)
	}
	if etag2 := rec2.Header().Get("ETag"); etag2 == etag1 {
		t.Fatalf("UUID 变了 ETag 应跟着变，实际没变：%q", etag2)
	}
	resp2 := decodeNode1330(t, rec2)
	users2, _ := resp2["users"].([]any)
	for _, u := range users2 {
		m, _ := u.(map[string]any)
		if m["id"] == "su-1" {
			nu2, _ := m["node_uuids"].(map[string]any)
			if len(nu2) != 2 {
				t.Fatalf("su-1 下发的 node_uuids 应有 2 个落地，实际 %v", nu2)
			}
		}
	}
}

// TestNodeUUIDGroupEffective1335 进了组的用户按组的落地生成 UUID，
// 不是按自己的勾选。
func TestNodeUUIDGroupEffective1335(t *testing.T) {
	s := newTestSubServer(t)
	s.cfg.SubGroups = []SubGroup{{
		ID: "g-1", Name: "二组",
		Selection: []SubSelection{{Panel: "local", Landing: 2}},
		CreatedAt: "2026-11-20T00:00:00Z",
	}}
	rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"组员","selection":[{"panel":"local","landing":1}],"group_id":"g-1"}`)
	if rec.Code != 200 {
		t.Fatalf("新建进组用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	u := s.cfg.SubUsers[0]
	if len(u.NodeUUIDs) != 1 || u.NodeUUIDs["203.0.113.2"] == "" {
		t.Fatalf("进了组应按组的落地（203.0.113.2）生成 UUID，实际 %v", u.NodeUUIDs)
	}
}

// TestNodeUUIDRemotePending1335 远程落地快照没到时先不生成它的 UUID；
// 快照到了，补齐时补上（轮询补齐的逻辑）。
func TestNodeUUIDRemotePending1335(t *testing.T) {
	s := newTestSubServer(t)
	// 直接写 cfg（走 HTTP 创建会因远程面板未登记被挡住；这里测的是
	// 生成逻辑本身：快照没到先跳过，快照到了再补）。
	s.cfg.SubUsers = []SubUser{{
		ID: "su-rm", Name: "远程用户", TokenHash: "hash-rm",
		CreatedAt: "2026-11-20T00:00:00Z",
		Selection: []SubSelection{{Panel: "local", Landing: 1}, {Panel: "rm-test", Landing: 1}},
	}}
	if !s.backfillNodeUUIDs() {
		t.Fatalf("本地落地应补生成 UUID")
	}
	u := s.cfg.SubUsers[0]
	if len(u.NodeUUIDs) != 1 || u.NodeUUIDs["203.0.113.1"] == "" {
		t.Fatalf("快照没到时远程落地不应有 UUID，实际 %v", u.NodeUUIDs)
	}
	// 快照到了（集中管理轮询刚刷完），再补一次。
	s.centralMu.Lock()
	s.centralCache["rm-test"] = &RemoteStatusSnapshot{
		ID: "rm-test", Online: true,
		Landings: []remoteLandingSummary{{Index: 1, Landing: "203.0.113.9"}},
	}
	s.centralMu.Unlock()
	if !s.backfillNodeUUIDs() {
		t.Fatalf("快照到后应补上远程落地 UUID")
	}
	if s.cfg.SubUsers[0].NodeUUIDs["203.0.113.9"] == "" {
		t.Fatalf("远程落地 UUID 应补上，实际 %v", s.cfg.SubUsers[0].NodeUUIDs)
	}
	if s.cfg.SubUsers[0].NodeUUIDs["203.0.113.1"] == "" {
		t.Fatalf("本地落地 UUID 不应丢")
	}
}
