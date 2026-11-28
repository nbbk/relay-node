package main

// v1.4.2：轻量节点后端切片 3——用户同步（每节点指派＋轮换 UUID）
// 与 1.4.2 UI 清单的后端部分（一键换端口、SNI 同步修 bug）。
//
// 覆盖：
//   - 指派：nil = 全部用户（切片 2 行为保留）；PUT 全量替换——null/缺失
//     = 全部、[] = 一个不发、非空 = 只发这些；不认的用户键 400；
//     指派改后 ConfigVersion＋1；GET 返回有效用户与 all_users 标记。
//   - 轮换 UUID：只换已指派节点的 per-node UUID（每个节点地址各一个
//     新 UUID），旧 UUID 换掉、未指派节点的不动；受影响节点 bump；
//     不带 kind 的裸 id 也认；没有这个用户 404；未登录 401。
//   - 停用／恢复 bump：disableUserForwardsLocked 与
//     resumeUserForwardsLocked 改 Disabled 后 bump 已指派节点。
//   - 一键换端口：随机空闲端口（10000–60000，避开同节点其它已启用
//     入站的端口）、bump 版本、旧端口不再用、不存在的入站 404。
//   - SNI 同步（修 SNI 编辑不同步）：新建 REALITY 入站带 SNI 时 dest
//     与 serverNames 从 SNI 派生；编辑 SNI 后 dest/serverNames 跟着变
//     （实测 bug 场景：SNI 改成 www.microsoft.com，渲染仍是
//     www.example.com）；TLS 入站的 SNI 不动 dest；三件套三者一致。
//   - 版本断言。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedUsers1402 往测试面板里放两个订阅用户与一条合并订阅（直接写
// s.cfg，与 TestNodeBackendUsers1401 同款做法），返回它们的用户键。
func seedUsers1402(s *Server) (u1, u2, m1 string) {
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-test1", Name: "测试用户一"})
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-test2", Name: "测试用户二"})
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: "ms-test1", Name: "合并用户"})
	return "subuser:su-test1", "subuser:su-test2", "merged:ms-test1"
}

// nodeUsers1402 GET /api/v1/nodes/{id}/users，期望 200，返回解析后的 map。
func nodeUsers1402(t *testing.T, h http.Handler, cookie string, nodeID int64) map[string]any {
	t.Helper()
	code, body := authed1400(t, h, cookie, "GET", "/api/v1/nodes/"+itoa1400(nodeID)+"/users", "")
	if code != 200 {
		t.Fatalf("节点用户列表应 200，实际 %d：%s", code, body)
	}
	return respMap1400(t, body)
}

// nodeUserIDs1402 从 GET /nodes/{id}/users 的 users 里取 user_id 集合。
func nodeUserIDs1402(m map[string]any) map[string]bool {
	out := map[string]bool{}
	raw, _ := m["users"].([]any)
	for _, r := range raw {
		if u, ok := r.(map[string]any); ok {
			if id, _ := u["user_id"].(string); id != "" {
				out[id] = true
			}
		}
	}
	return out
}

// nodeVersion1402 读 nodes.json 里某节点的 config_version。
func nodeVersion1402(t *testing.T, s *Server, nodeID int64) int64 {
	t.Helper()
	m := nodesJSON1401(t, s)
	nodes, _ := m["nodes"].([]any)
	for _, n := range nodes {
		node := n.(map[string]any)
		if int64(node["id"].(float64)) == nodeID {
			v, _ := node["config_version"].(float64)
			return int64(v)
		}
	}
	t.Fatalf("nodes.json 里找不到节点 %d", nodeID)
	return 0
}

// nodeUserIDsDisk1402 读 nodes.json 里某节点存的 user_ids（nil = 全部）。
func nodeUserIDsDisk1402(t *testing.T, s *Server, nodeID int64) ([]string, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.env.PanelHome, "nodes.json"))
	if err != nil {
		t.Fatalf("读 nodes.json 失败：%v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("nodes.json 解析失败：%v", err)
	}
	nodes, _ := m["nodes"].([]any)
	for _, n := range nodes {
		node := n.(map[string]any)
		if int64(node["id"].(float64)) != nodeID {
			continue
		}
		v, present := node["user_ids"]
		if v == nil {
			return nil, present // null：present=true（字段在）但值为 nil
		}
		arr, _ := v.([]any)
		out := make([]string, 0, len(arr))
		for _, x := range arr {
			out = append(out, x.(string))
		}
		return out, true
	}
	t.Fatalf("nodes.json 里找不到节点 %d", nodeID)
	return nil, false
}

// ---------- 1. 指派：nil = 全部，PUT 语义 ----------

func TestNodeUsersAssign1402(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	createNode1400(t, h, cookie, "新落地-02", "203.0.113.2")
	u1, u2, m1 := seedUsers1402(s)

	// 未登录 → 401。
	if code, _ := authed1400(t, h, "", "GET", "/api/v1/nodes/1/users", ""); code != 401 {
		t.Fatalf("未登录调节点用户列表应 401，实际 %d", code)
	}
	if code, _ := authed1400(t, h, "", "PUT", "/api/v1/nodes/1/users", `{"user_ids":[]}`); code != 401 {
		t.Fatalf("未登录改指派应 401，实际 %d", code)
	}

	// 新建节点默认一个用户不发（§5.3：老用户不自动上新节点，须手动勾）。
	m := nodeUsers1402(t, h, cookie, 1)
	if m["all_users"] != false || len(nodeUserIDs1402(m)) != 0 {
		t.Fatalf("新建节点应一个用户不发：%v", m)
	}
	if disk, _ := nodeUserIDsDisk1402(t, s, 1); disk == nil {
		t.Fatalf("新建节点落盘应是 []，实际 null")
	}

	// PUT null = 全部用户：3 个都在。
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":null}`); code != 200 {
		t.Fatalf("PUT null 应 200，实际 %d：%s", code, body)
	}
	m = nodeUsers1402(t, h, cookie, 1)
	if m["all_users"] != true {
		t.Fatalf("PUT null 后应 all_users=true：%v", m)
	}
	ids := nodeUserIDs1402(m)
	if len(ids) != 3 || !ids[u1] || !ids[u2] || !ids[m1] {
		t.Fatalf("PUT null 后应下发 3 个用户，实际 %v", ids)
	}

	// PUT [] = 一个不发。
	vBefore := nodeVersion1402(t, s, 1)
	code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":[]}`)
	if code != 200 {
		t.Fatalf("PUT [] 应 200，实际 %d：%s", code, body)
	}
	m = nodeUsers1402(t, h, cookie, 1)
	if m["all_users"] != false || len(nodeUserIDs1402(m)) != 0 {
		t.Fatalf("PUT [] 后应一个不发：%v", m)
	}
	if got := nodeVersion1402(t, s, 1); got != vBefore+1 {
		t.Fatalf("指派改后 ConfigVersion 应＋1，实际 %d→%d", vBefore, got)
	}
	// [] 落盘是 [] 不是 null（nil 与 [] 在 nodes.json 里必须分得清）。
	if disk, _ := nodeUserIDsDisk1402(t, s, 1); disk == nil {
		t.Fatalf("PUT [] 落盘应是 []，实际 null")
	} else if len(disk) != 0 {
		t.Fatalf("PUT [] 落盘应是空数组，实际 %v", disk)
	}

	// PUT 非空 = 只发这些；不认的用户键 400。
	code, body = authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":["`+u1+`","`+m1+`"]}`)
	if code != 200 {
		t.Fatalf("PUT 指派应 200，实际 %d：%s", code, body)
	}
	m = nodeUsers1402(t, h, cookie, 1)
	ids = nodeUserIDs1402(m)
	if len(ids) != 2 || !ids[u1] || !ids[m1] || ids[u2] {
		t.Fatalf("指派后应只有 %s 与 %s，实际 %v", u1, m1, ids)
	}
	code, body = authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":["subuser:su-nope"]}`)
	if code != 400 || !strings.Contains(body, "没有这个用户") {
		t.Fatalf("不认的用户键应 400，实际 %d：%s", code, body)
	}

	// PUT null = 回到全部用户。
	code, body = authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":null}`)
	if code != 200 {
		t.Fatalf("PUT null 应 200，实际 %d：%s", code, body)
	}
	m = nodeUsers1402(t, h, cookie, 1)
	if m["all_users"] != true || len(nodeUserIDs1402(m)) != 3 {
		t.Fatalf("PUT null 后应回到全部用户：%v", m)
	}
	if disk, _ := nodeUserIDsDisk1402(t, s, 1); disk != nil {
		t.Fatalf("PUT null 落盘应是 null，实际 %v", disk)
	}

	// 节点 2 不受节点 1 指派影响：仍是新建默认（一个用户不发）。
	m = nodeUsers1402(t, h, cookie, 2)
	if m["all_users"] != false || len(nodeUserIDs1402(m)) != 0 {
		t.Fatalf("节点 2 应仍是一个用户不发：%v", m)
	}

	// 停用的用户不下发（nodeBackendUsersForNode 的过滤与指派叠加）：
	// 先把节点 2 指派成全部用户，再停用 u2。
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/2/users", `{"user_ids":null}`); code != 200 {
		t.Fatalf("节点 2 指派全部应 200，实际 %d：%s", code, body)
	}
	s.cfg.SubUsers[1].Disabled = true
	m = nodeUsers1402(t, h, cookie, 2)
	ids = nodeUserIDs1402(m)
	if len(ids) != 2 || ids[u2] {
		t.Fatalf("停用后节点 2 应只剩 2 个用户，实际 %v", ids)
	}

	// 不存在的节点 404。
	if code, _ := authed1400(t, h, cookie, "GET", "/api/v1/nodes/99/users", ""); code != 404 {
		t.Fatalf("不存在的节点应 404，实际 %d", code)
	}
	if code, _ := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/99/users", `{"user_ids":[]}`); code != 404 {
		t.Fatalf("不存在的节点 PUT 应 404，实际 %d", code)
	}
}

// ---------- 2. 轮换 UUID ----------

func TestUserRotateNodeUUID1402(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	createNode1400(t, h, cookie, "新落地-02", "203.0.113.2")
	u1, _, _ := seedUsers1402(s)

	// 未登录 → 401。
	if code, _ := authed1400(t, h, "", "POST", "/api/v1/users/"+u1+"/rotate-uuid", "{}"); code != 401 {
		t.Fatalf("未登录轮换应 401，实际 %d", code)
	}
	// 没有这个用户 → 404。
	if code, _ := authed1400(t, h, cookie, "POST", "/api/v1/users/subuser:su-nope/rotate-uuid", "{}"); code != 404 {
		t.Fatalf("没有这个用户应 404，实际 %d", code)
	}

	// 先让节点 1 只发 u1、节点 2 发全部：两个节点都指派了 u1。
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":["`+u1+`"]}`); code != 200 {
		t.Fatalf("节点 1 指派应 200，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/2/users", `{"user_ids":null}`); code != 200 {
		t.Fatalf("节点 2 指派全部应 200，实际 %d：%s", code, body)
	}
	// 取一次用户列表，让 UUID 生成并写回。
	before := nodeUsers1402(t, h, cookie, 1)
	oldUUID := ""
	for _, r := range before["users"].([]any) {
		if u, ok := r.(map[string]any); ok && u["user_id"] == u1 {
			oldUUID, _ = u["vless_uuid"].(string)
		}
	}
	if oldUUID == "" {
		t.Fatalf("轮换前应已有 UUID：%v", before)
	}
	v1Before, v2Before := nodeVersion1402(t, s, 1), nodeVersion1402(t, s, 2)

	// 轮换：两个已指派节点各换一个新 UUID。
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/users/"+u1+"/rotate-uuid", "{}")
	if code != 200 {
		t.Fatalf("轮换应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	uuids, _ := m["uuids"].(map[string]any)
	if len(uuids) != 2 {
		t.Fatalf("轮换应换 2 个节点的 UUID，实际 %v", m)
	}
	if uuids["1"] == oldUUID {
		t.Fatalf("节点 1 的 UUID 应变，旧 %s 新 %v", oldUUID, uuids["1"])
	}
	if uuids["1"] == uuids["2"] {
		t.Fatalf("两节点 UUID 不应相同：%v", uuids)
	}
	if m["vless_uuid"] != uuids["1"] {
		t.Fatalf("vless_uuid 应是第一个节点的 UUID：%v", m)
	}
	if got := nodeVersion1402(t, s, 1); got != v1Before+1 {
		t.Fatalf("轮换后节点 1 ConfigVersion 应＋1，实际 %d→%d", v1Before, got)
	}
	if got := nodeVersion1402(t, s, 2); got != v2Before+1 {
		t.Fatalf("轮换后节点 2 ConfigVersion 应＋1，实际 %d→%d", v2Before, got)
	}
	// 新 UUID 已写回 NodeUUIDs（按节点地址存），且列表里看到的就是新的。
	if got := s.cfg.SubUsers[0].NodeUUIDs["203.0.113.1"]; got != uuids["1"] {
		t.Fatalf("节点 1 地址的 UUID 应写回，实际 %q", got)
	}
	after := nodeUsers1402(t, h, cookie, 1)
	for _, r := range after["users"].([]any) {
		if u, ok := r.(map[string]any); ok && u["user_id"] == u1 && u["vless_uuid"] != uuids["1"] {
			t.Fatalf("列表里的 UUID 应是新的，实际 %v", u)
		}
	}

	// 不带 kind 的裸 id 也认。
	code, body = authed1400(t, h, cookie, "POST", "/api/v1/users/su-test1/rotate-uuid", "{}")
	if code != 200 {
		t.Fatalf("裸 id 轮换应 200，实际 %d：%s", code, body)
	}

	// 该用户一个节点都没指派时：200，但 nodes 为空、不 bump。
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":[]}`); code != 200 {
		t.Fatalf("节点 1 清空指派应 200，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/2/users", `{"user_ids":[]}`); code != 200 {
		t.Fatalf("节点 2 清空指派应 200，实际 %d：%s", code, body)
	}
	v1, v2 := nodeVersion1402(t, s, 1), nodeVersion1402(t, s, 2)
	code, body = authed1400(t, h, cookie, "POST", "/api/v1/users/"+u1+"/rotate-uuid", "{}")
	if code != 200 {
		t.Fatalf("无指派节点时轮换应 200，实际 %d：%s", code, body)
	}
	m = respMap1400(t, body)
	if nodes, _ := m["nodes"].([]any); len(nodes) != 0 {
		t.Fatalf("无指派节点时 nodes 应为空，实际 %v", m)
	}
	if got := nodeVersion1402(t, s, 1); got != v1 {
		t.Fatalf("无指派节点时不应 bump 节点 1，实际 %d→%d", v1, got)
	}
	if got := nodeVersion1402(t, s, 2); got != v2 {
		t.Fatalf("无指派节点时不应 bump 节点 2，实际 %d→%d", v2, got)
	}
}

// ---------- 3. 停用／恢复 bump ----------

func TestUserDisableResumeBump1402(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	createNode1400(t, h, cookie, "新落地-02", "203.0.113.2")
	u1, _, _ := seedUsers1402(s)
	// 节点 1 发全部用户（含 u1），节点 2 不发 u1：停用／恢复只应 bump 节点 1。
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":null}`); code != 200 {
		t.Fatalf("节点 1 指派全部应 200，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/2/users", `{"user_ids":[]}`); code != 200 {
		t.Fatalf("节点 2 清空指派应 200，实际 %d：%s", code, body)
	}
	v1Before, v2Before := nodeVersion1402(t, s, 1), nodeVersion1402(t, s, 2)

	// 停用（走 disableUserForwardsLocked 的 choke 点）。
	s.disableUserForwardsLocked(u1, "traffic")
	if got := nodeVersion1402(t, s, 1); got != v1Before+1 {
		t.Fatalf("停用后节点 1 ConfigVersion 应＋1，实际 %d→%d", v1Before, got)
	}
	if got := nodeVersion1402(t, s, 2); got != v2Before {
		t.Fatalf("停用后节点 2 不应 bump，实际 %d→%d", v2Before, got)
	}
	// 停用后节点 1 的有效用户里没他了。
	m := nodeUsers1402(t, h, cookie, 1)
	if nodeUserIDs1402(m)[u1] {
		t.Fatalf("停用后节点 1 不应再下发该用户：%v", m)
	}

	// 恢复（走 resumeUserForwardsLocked 的 choke 点；它最后调
	// applyUserForwards，要读 relay.conf 与 config.toml，先补文件桩：
	// config.toml 写成占位段本身，无转发可加时 apply 无变化直接返回，
	// 不会走到 systemctl restart realm（沙箱里没有 realm 服务）。
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "relay.conf"), []byte(""), 0o644); err != nil {
		t.Fatalf("写测试桩 relay.conf 失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte("\n[[endpoints]]\nlisten = \"127.0.0.1:9\"\nremote = \"127.0.0.1:9\"\n"), 0o644); err != nil {
		t.Fatalf("写测试桩 config.toml 失败：%v", err)
	}
	v1Before = nodeVersion1402(t, s, 1)
	if err := s.resumeUserForwardsLocked(u1); err != nil {
		t.Fatalf("恢复应成功：%v", err)
	}
	if got := nodeVersion1402(t, s, 1); got != v1Before+1 {
		t.Fatalf("恢复后节点 1 ConfigVersion 应＋1，实际 %d→%d", v1Before, got)
	}
	m = nodeUsers1402(t, h, cookie, 1)
	if !nodeUserIDs1402(m)[u1] {
		t.Fatalf("恢复后节点 1 应重新下发该用户：%v", m)
	}
}

// ---------- 4. 一键换端口 ----------

func TestInboundChangePort1402(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id, _ := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	// 先建两条入站：reality-443（TCP 443）与一条 TCP 8443。
	_, reality := createInbound1401(t, h, cookie, id, `{"inbound_tag":"reality-443","protocol":"vless","transport":"reality","security":"reality","port":443,"sni":"example.com"}`)
	realityID, _ := reality["inbound_id"].(float64)
	_, other := createInbound1401(t, h, cookie, id, `{"inbound_tag":"xhttp-443","protocol":"vless","transport":"xhttp","security":"reality","port":8443,"sni":"example.com"}`)
	otherID, _ := other["inbound_id"].(float64)

	// 未登录 → 401。
	if code, _ := authed1400(t, h, "", "POST", "/api/v1/nodes/1/inbounds/1/change-port", "{}"); code != 401 {
		t.Fatalf("未登录换端口应 401，实际 %d", code)
	}
	// 不存在的入站 → 404。
	if code, _ := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/99/change-port", "{}"); code != 404 {
		t.Fatalf("不存在的入站换端口应 404，实际 %d", code)
	}

	vBefore := nodeVersion1402(t, s, id)
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/"+itoa1400(int64(otherID))+"/change-port", "{}")
	if code != 200 {
		t.Fatalf("换端口应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	newPort, _ := m["new_port"].(float64)
	oldPort, _ := m["old_port"].(float64)
	if oldPort != 8443 {
		t.Fatalf("old_port 应是 8443，实际 %v", m)
	}
	if newPort < 10000 || newPort > 60000 || int64(newPort) == 8443 || int64(newPort) == 443 {
		t.Fatalf("新端口应在 10000–60000 且不是旧端口／443，实际 %v", m)
	}
	if got := nodeVersion1402(t, s, id); got != vBefore+1 {
		t.Fatalf("换端口后 ConfigVersion 应＋1，实际 %d→%d", vBefore, got)
	}
	// 列表里端口变了，reality-443 的 443 没动。
	list := listInbounds1401(t, h, cookie, id)
	if got := inboundByTag1401(list, "xhttp-443"); got == nil || got["port"] != newPort {
		t.Fatalf("换端口后 xhttp-443 端口应是 %v，实际 %v", newPort, got)
	}
	if got := inboundByTag1401(list, "reality-443"); got == nil || got["port"] != float64(443) {
		t.Fatalf("reality-443 端口不应动，实际 %v", got)
	}
	_ = realityID

	// 连换 20 次：每次端口都不一样，且不撞其它入站的端口。
	seen := map[float64]bool{newPort: true}
	for i := 0; i < 20; i++ {
		code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/"+itoa1400(int64(otherID))+"/change-port", "{}")
		if code != 200 {
			t.Fatalf("第 %d 次换端口应 200，实际 %d：%s", i+1, code, body)
		}
		m := respMap1400(t, body)
		p, _ := m["new_port"].(float64)
		if seen[p] || p == 443 {
			t.Fatalf("换出的端口撞了：%v", m)
		}
		seen[p] = true
	}
}

// ---------- 5. SNI 同步（修 SNI 编辑不同步） ----------

func TestInboundSNISync1402(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id, _ := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")

	// 新建时带 SNI：dest 与 serverNames 从 SNI 派生，不再是占位值。
	_, inb := createInbound1401(t, h, cookie, id, `{"inbound_tag":"reality-443","protocol":"vless","transport":"reality","security":"reality","port":443,"sni":"www.microsoft.com"}`)
	if inb["reality_dest"] != "www.microsoft.com:443" || inb["reality_server_names"] != "www.microsoft.com" {
		t.Fatalf("新建时 dest/serverNames 应从 SNI 派生，实际 dest=%v serverNames=%v", inb["reality_dest"], inb["reality_server_names"])
	}
	realityID, _ := inb["inbound_id"].(float64)

	// 实测 bug 场景：SNI 改成 www.microsoft.com 并保存——之前渲染仍是
	// www.example.com。这里先故意把磁盘值改回旧的（模拟旧数据漂移），
	// 再只改 SNI，看编辑是否把 dest/serverNames 一起同步回来。
	s.nodes.mu.Lock()
	node := s.nodes.getNode(id)
	for i := range node.Inbounds {
		if node.Inbounds[i].InboundID == int64(realityID) {
			node.Inbounds[i].SNI = "www.example.com"
			node.Inbounds[i].RealityDest = "www.example.com:443"
			node.Inbounds[i].RealityServerName = "www.example.com"
		}
	}
	_ = s.nodes.saveLocked()
	s.nodes.mu.Unlock()
	code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/inbounds/"+itoa1400(int64(realityID)), `{"sni":"www.microsoft.com"}`)
	if code != 200 {
		t.Fatalf("改 SNI 应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	updated, _ := m["inbound"].(map[string]any)
	if updated["sni"] != "www.microsoft.com" || updated["reality_dest"] != "www.microsoft.com:443" || updated["reality_server_names"] != "www.microsoft.com" {
		t.Fatalf("改 SNI 后 dest/serverNames 应跟着变，实际 %v", updated)
	}
	// agent 拉到的配置文档（configDocOf）里也是同步后的值。
	s.nodes.mu.Lock()
	node = s.nodes.getNode(id)
	doc := s.nodes.configDocOf(node, nil)
	s.nodes.mu.Unlock()
	if len(doc.Inbounds) != 1 || doc.Inbounds[0].RealityDest != "www.microsoft.com:443" || doc.Inbounds[0].RealityServerName != "www.microsoft.com" {
		t.Fatalf("配置文档里的 dest/serverNames 应已同步，实际 %+v", doc.Inbounds)
	}

	// 只改端口不改 SNI：同步也跑一次，旧数据自愈（dest 不会掉回占位值）。
	code, body = authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/inbounds/"+itoa1400(int64(realityID)), `{"port":4443}`)
	if code != 200 {
		t.Fatalf("改端口应 200，实际 %d：%s", code, body)
	}
	m = respMap1400(t, body)
	updated, _ = m["inbound"].(map[string]any)
	if updated["reality_dest"] != "www.microsoft.com:443" {
		t.Fatalf("只改端口时 dest 应保持同步后的值，实际 %v", updated)
	}

	// 边界：TLS 入站的 SNI 不动 dest（TLS 没有 RealityDest 语义）。
	_, tlsInb := createInbound1401(t, h, cookie, id, `{"inbound_tag":"tls-8443","protocol":"vless","transport":"tcp","security":"tls","port":8443,"sni":"example.com"}`)
	tlsID, _ := tlsInb["inbound_id"].(float64)
	code, body = authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/inbounds/"+itoa1400(int64(tlsID)), `{"sni":"www.microsoft.com"}`)
	if code != 200 {
		t.Fatalf("TLS 改 SNI 应 200，实际 %d：%s", code, body)
	}
	m = respMap1400(t, body)
	updated, _ = m["inbound"].(map[string]any)
	if updated["sni"] != "www.microsoft.com" {
		t.Fatalf("TLS 的 SNI 应改掉，实际 %v", updated)
	}
	if dest, _ := updated["reality_dest"].(string); dest != "" {
		t.Fatalf("TLS 入站不应有 RealityDest，实际 %q", dest)
	}

	// 三件套：三者仍保持一致（reality/xhttp 用占位，hy2 用填的域名）。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/default-trio", `{"domain":"example.com"}`); code != 200 {
		// reality-443 与 xhttp-443 已存在，三件套只补 hy2-443。
		if !strings.Contains(body, "已存在") {
			t.Fatalf("三件套补 HY2 应 200 或提示已存在，实际 %d：%s", code, body)
		}
	}
	disk := diskInbound1401(t, s, id, "reality-443")
	if disk["reality_dest"] != "www.microsoft.com:443" || disk["reality_server_names"] != "www.microsoft.com" {
		t.Fatalf("三件套后磁盘 reality-443 的 dest/serverNames 应仍与 SNI 一致，实际 %v", disk)
	}
}

// ---------- 6. 版本断言 ----------

func TestVersion1402(t *testing.T) {
	if Version != "1.4.37" {
		t.Fatalf("VERSION 应是 1.4.33，实际 %q", Version)
	}
}
