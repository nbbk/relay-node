package main

// v1.4.3：订阅用户手动停用。
//
// 覆盖：
//   - 单用户停用／恢复走接口：停用后节点 ConfigVersion＋1、节点用户列表
//     里没他了、列表接口 traffic_disabled=true 且 disabled_reason="手动停用"；
//     重复停用幂等（版本不动）；恢复后版本再＋1、用户回来、标记清除。
//     种子用户没开限流——恢复走接口能过，正是 resumeSub 限流门修掉的
//     回归点（以前会报“没开限流，不用恢复”）。
//   - 合并订阅同样走一遍。
//   - 停用只 bump 已指派节点（节点 2 不发该用户，不 bump）。
//   - 404／401。
//   - 版本断言 1.4.3。

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// writeRealmStubs1403 补 applyUserForwards 要读的两个文件桩（与
// TestUserDisableResumeBump1402 同款）：relay.conf 为空、config.toml
// 只有一个占位 endpoint，apply 无变化直接返回，不会走到 systemctl。
func writeRealmStubs1403(t *testing.T, s *Server) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "relay.conf"), []byte(""), 0o644); err != nil {
		t.Fatalf("写测试桩 relay.conf 失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte("\n[[endpoints]]\nlisten = \"127.0.0.1:9\"\nremote = \"127.0.0.1:9\"\n"), 0o644); err != nil {
		t.Fatalf("写测试桩 config.toml 失败：%v", err)
	}
}

// subUsers1403 GET /api/v1/subscriptions/users，期望 200，返回 users 数组。
func subUsers1403(t *testing.T, h http.Handler, cookie string) []any {
	t.Helper()
	code, body := authed1400(t, h, cookie, "GET", "/api/v1/subscriptions/users", "")
	if code != 200 {
		t.Fatalf("订阅用户列表应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	users, _ := m["users"].([]any)
	return users
}

// mergedSubs1403 GET /api/v1/subscriptions/merged，期望 200，返回 subs 数组。
func mergedSubs1403(t *testing.T, h http.Handler, cookie string) []any {
	t.Helper()
	code, body := authed1400(t, h, cookie, "GET", "/api/v1/subscriptions/merged", "")
	if code != 200 {
		t.Fatalf("合并订阅列表应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	subs, _ := m["subs"].([]any)
	return subs
}

// findItem1403 在列表数组里按 id 找一条，找不到就 Fatal。
func findItem1403(t *testing.T, items []any, id string) map[string]any {
	t.Helper()
	for _, raw := range items {
		if u, ok := raw.(map[string]any); ok && u["id"] == id {
			return u
		}
	}
	t.Fatalf("列表里找不到 id=%s：%v", id, items)
	return nil
}

// ---------- 1. 单用户停用／恢复走接口 ----------

func TestManualDisableSubUser1403(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	writeRealmStubs1403(t, s)
	createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	createNode1400(t, h, cookie, "新落地-02", "203.0.113.2")
	u1, u2, _ := seedUsers1402(s)
	// 节点 1 发全部用户（含 u1），节点 2 不发 u1：停用／恢复只应 bump 节点 1。
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":null}`); code != 200 {
		t.Fatalf("节点 1 指派全部应 200，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/2/users", `{"user_ids":[]}`); code != 200 {
		t.Fatalf("节点 2 清空指派应 200，实际 %d：%s", code, body)
	}
	v1Before, v2Before := nodeVersion1402(t, s, 1), nodeVersion1402(t, s, 2)

	// 停用（走接口，不直接调内部函数）。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/subscriptions/users/su-test1/disable", "{}"); code != 200 {
		t.Fatalf("停用应 200，实际 %d：%s", code, body)
	}
	if got := nodeVersion1402(t, s, 1); got != v1Before+1 {
		t.Fatalf("停用后节点 1 ConfigVersion 应＋1，实际 %d→%d", v1Before, got)
	}
	if got := nodeVersion1402(t, s, 2); got != v2Before {
		t.Fatalf("停用后节点 2 不应 bump，实际 %d→%d", v2Before, got)
	}
	m := nodeUsers1402(t, h, cookie, 1)
	ids := nodeUserIDs1402(m)
	if ids[u1] {
		t.Fatalf("停用后节点 1 不应再下发该用户：%v", m)
	}
	if !ids[u2] {
		t.Fatalf("停用后节点 1 应继续下发其他用户：%v", m)
	}
	item := findItem1403(t, subUsers1403(t, h, cookie), "su-test1")
	if item["traffic_disabled"] != true {
		t.Fatalf("停用后列表 traffic_disabled 应为 true：%v", item)
	}
	if item["disabled_reason"] != "手动停用" {
		t.Fatalf("停用后列表 disabled_reason 应为“手动停用”：%v", item)
	}

	// 重复停用幂等：200，版本不动。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/subscriptions/users/su-test1/disable", "{}"); code != 200 {
		t.Fatalf("重复停用应 200，实际 %d：%s", code, body)
	}
	if got := nodeVersion1402(t, s, 1); got != v1Before+1 {
		t.Fatalf("重复停用不应再 bump，实际 %d→%d", v1Before+1, got)
	}

	// 恢复（走接口；种子用户没开限流，以前这里会报“没开限流，不用恢复”）。
	v1Before = nodeVersion1402(t, s, 1)
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/subscriptions/users/su-test1/resume", "{}"); code != 200 {
		t.Fatalf("恢复应 200，实际 %d：%s", code, body)
	}
	if got := nodeVersion1402(t, s, 1); got != v1Before+1 {
		t.Fatalf("恢复后节点 1 ConfigVersion 应＋1，实际 %d→%d", v1Before, got)
	}
	m = nodeUsers1402(t, h, cookie, 1)
	if !nodeUserIDs1402(m)[u1] {
		t.Fatalf("恢复后节点 1 应重新下发该用户：%v", m)
	}
	item = findItem1403(t, subUsers1403(t, h, cookie), "su-test1")
	if item["traffic_disabled"] == true {
		t.Fatalf("恢复后列表 traffic_disabled 应清除：%v", item)
	}
	if r, _ := item["disabled_reason"].(string); r != "" {
		t.Fatalf("恢复后列表 disabled_reason 应清除：%v", item)
	}
}

// ---------- 2. 合并订阅停用／恢复已退场（410） ----------

func TestManualDisableMerged1403(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	writeRealmStubs1403(t, s)
	createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	_, _, _ = seedUsers1402(s)
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":null}`); code != 200 {
		t.Fatalf("节点 1 指派全部应 200，实际 %d：%s", code, body)
	}
	// 1.4.27：合并订阅已退场，停用/恢复接口一律 410。
	// 停用/恢复语义现在由订阅用户承担，见 TestManualDisableSubUser1403。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/subscriptions/merged/ms-test1/disable", "{}"); code != 410 {
		t.Fatalf("停用合并订阅应 410（已退场），实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/subscriptions/merged/ms-test1/resume", "{}"); code != 410 {
		t.Fatalf("恢复合并订阅应 410（已退场），实际 %d：%s", code, body)
	}
	// 合并订阅列表仍可查（供核对）。
	_ = mergedSubs1403(t, h, cookie)
}

// ---------- 3. 404／401 ----------

func TestManualDisableNotFound1403(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/subscriptions/users/su-nope/disable", "{}"); code != 404 {
		t.Fatalf("停用不存在的用户应 404，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/subscriptions/merged/ms-nope/disable", "{}"); code != 410 {
		t.Fatalf("停用合并订阅应 410（已退场，到不了查 id），实际 %d：%s", code, body)
	}
	if code, _ := authed1400(t, h, "", "POST", "/api/v1/subscriptions/users/su-test1/disable", "{}"); code != 401 {
		t.Fatalf("未登录停用应 401，实际 %d", code)
	}
}

// ---------- 4. 版本断言 ----------

func TestVersion1403(t *testing.T) {
	if Version != "1.4.34" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
}
