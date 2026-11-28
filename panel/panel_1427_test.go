package main

// ---------- 1.4.27：订阅体系统一方案 B（Plan＋每人登录＋退场） ----------

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestVersion1427 版本断言与 VERSION 一致（订阅体系统一 B＋订阅级
// LinkMode，v1.4.27）。
func TestVersion1427(t *testing.T) {
	if Version != "1.4.39" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
}

// ---------- 脚手架 ----------

// createPlan1427 调档位创建接口，返回状态码与响应体。
func createPlan1427(t *testing.T, h http.Handler, cookie, body string) (int, map[string]any) {
	t.Helper()
	return postJSON144(t, h, cookie, "/api/v1/subscriptions/plans", body)
}

// createSubUser1427 调订阅用户创建接口，返回状态码与响应体。
func createSubUser1427(t *testing.T, h http.Handler, cookie, body string) (int, map[string]any) {
	t.Helper()
	return postJSON144(t, h, cookie, "/api/v1/subscriptions/users", body)
}

// subUsers1427 取订阅用户列表（已解析的 users 数组）。
func subUsers1427(t *testing.T, h http.Handler, cookie string) []map[string]any {
	t.Helper()
	code, resp := getJSON144(t, h, cookie, "/api/v1/subscriptions/users")
	if code != 200 {
		t.Fatalf("订阅用户列表应 200，实际 %d：%v", code, resp)
	}
	raw, _ := resp["users"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		out = append(out, m)
	}
	return out
}

// findSubUser1427 在列表里按 id 找一行。
func findSubUser1427(users []map[string]any, id string) map[string]any {
	for _, u := range users {
		if u["id"] == id {
			return u
		}
	}
	return nil
}

// setupGroup1427 直接往 config 里加一个分组（不走接口，免得勾选落地
// 校验依赖 relay.conf）：BackendNodeIDs 给后端节点 id。
func setupGroup1427(s *Server, id, name string, backendNodes []int64) {
	s.cfg.SubGroups = append(s.cfg.SubGroups, SubGroup{ID: id, Name: name, CreatedAt: "2026-11-25T00:00:00Z", BackendNodeIDs: backendNodes})
}

// setupNode1427 直接往 store 里加一个后端节点（UserIDs=[]，一个不发，
// 等创建/指派时再加人），入站用 1420 的契约入站。
func setupNode1427(t *testing.T, s *Server, id int64) *BackendNode {
	t.Helper()
	node := &BackendNode{ID: id, Name: "测试节点", Address: "203.0.113.1", UserIDs: []string{}, Inbounds: contractInbounds1420()}
	for i := range node.Inbounds {
		node.Inbounds[i].NodeID = id
	}
	s.nodes.mu.Lock()
	s.nodes.data.Nodes = append(s.nodes.data.Nodes, node)
	s.nodes.mu.Unlock()
	return node
}

// setupRelayConf1427 给测试面板写一份只有一个本地落地的 relay.conf。
func setupRelayConf1427(t *testing.T, s *Server) {
	t.Helper()
	conf := "LANDING_COUNT=1\nLANDING=203.0.113.1\nDOMAIN=\nPORTS=tcp:30001:443\n"
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "relay.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
}

// createLoginUser1427 建一个带登录密码的订阅用户，返回 id 与 username。
func createLoginUser1427(t *testing.T, h http.Handler, cookie, body string) (string, string) {
	t.Helper()
	code, resp := createSubUser1427(t, h, cookie, body)
	if code != 200 {
		t.Fatalf("建订阅用户应 200，实际 %d：%v", code, resp)
	}
	id, _ := resp["id"].(string)
	username, _ := resp["username"].(string)
	if id == "" || username == "" {
		t.Fatalf("建用户回包应有 id 与 username，实际 %v", resp)
	}
	return id, username
}

// nodeUserIDs1427 读某节点 UserIDs 里有没有指定用户键。
func nodeUserIDs1427(t *testing.T, s *Server, nodeID int64) []string {
	t.Helper()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	n := s.nodes.getNode(nodeID)
	if n == nil {
		t.Fatalf("没有这个节点：%d", nodeID)
	}
	return n.UserIDs
}

func containsStr1427(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ---------- Plan ----------

func TestPlansCreateValidation1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	// 名字必填。
	if code, resp := createPlan1427(t, h, cookie, `{}`); code != 400 {
		t.Fatalf("没名字应 400，实际 %d：%v", code, resp)
	}
	// 名字太长（25 字）。
	if code, resp := createPlan1427(t, h, cookie, `{"name":"`+strings.Repeat("档", 25)+`"}`); code != 400 {
		t.Fatalf("名字超 24 字应 400，实际 %d：%v", code, resp)
	}
	// 非法 mode。
	if code, resp := createPlan1427(t, h, cookie, `{"name":"V1","mode":"xxx"}`); code != 400 {
		t.Fatalf("非法 mode 应 400，实际 %d：%v", code, resp)
	}
	// 到期天数负数。
	if code, resp := createPlan1427(t, h, cookie, `{"name":"V1","expires_days":-1}`); code != 400 {
		t.Fatalf("到期天数负数应 400，实际 %d：%v", code, resp)
	}
	// 正常创建。
	code, resp := createPlan1427(t, h, cookie, `{"name":"V1","traffic_limit_mb":2048,"traffic_limit_mode":"total","mode":"both","expires_days":30}`)
	if code != 200 {
		t.Fatalf("正常创建应 200，实际 %d：%v", code, resp)
	}
	if resp["traffic_limit_mb"] != float64(2048) || resp["expires_days"] != float64(30) {
		t.Fatalf("回包限流/到期不对，实际 %v", resp)
	}
	// 重名 400。
	if code, resp := createPlan1427(t, h, cookie, `{"name":"V1"}`); code != 400 {
		t.Fatalf("重名应 400，实际 %d：%v", code, resp)
	}
	// 不存在的分组 400。
	if code, resp := createPlan1427(t, h, cookie, `{"name":"V2","group_id":"sg-nope"}`); code != 400 {
		t.Fatalf("不存在的分组应 400，实际 %d：%v", code, resp)
	}
}

// TestPlansDefault1427 默认档位：Plans 为空时列表返回一条合成的默认
// 档位、且 GET 不写盘；建订阅用户后默认档位持久化到 config。
func TestPlansDefault1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	code, resp := getJSON144(t, h, cookie, "/api/v1/subscriptions/plans")
	if code != 200 {
		t.Fatalf("档位列表应 200，实际 %d：%v", code, resp)
	}
	plans, _ := resp["plans"].([]any)
	if len(plans) != 1 {
		t.Fatalf("没有档位时列表应有 1 条合成默认档位，实际 %d：%v", len(plans), resp)
	}
	p0, _ := plans[0].(map[string]any)
	if p0["id"] != "pl-default" || p0["name"] != "默认" || p0["mode"] != "both" {
		t.Fatalf("合成默认档位不对，实际 %v", p0)
	}
	if len(s.cfg.Plans) != 0 {
		t.Fatal("GET 列表不应悄悄写盘，cfg.Plans 应仍为空")
	}
	// 建一个订阅用户（写路径）后默认档位落盘。
	createLoginUser1427(t, h, cookie, `{"name":"小明"}`)
	if len(s.cfg.Plans) != 1 || s.cfg.Plans[0].ID != "pl-default" {
		t.Fatalf("建用户后默认档位应持久化，实际 %+v", s.cfg.Plans)
	}
	code, resp = getJSON144(t, h, cookie, "/api/v1/subscriptions/plans")
	plans, _ = resp["plans"].([]any)
	if len(plans) != 1 {
		t.Fatalf("落盘后列表应仍为 1 条，实际 %d", len(plans))
	}
}

// TestPlansDeleteInUseAndLast1427 删除：有订阅用户在用时 400；
// 最后一条档位拒绝删除（选的是“拒绝删除”，不是“删后重建”）。
func TestPlansDeleteInUseAndLast1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	_, resp := createPlan1427(t, h, cookie, `{"name":"V1"}`)
	planID, _ := resp["id"].(string)
	// 只有一条档位时删它 → 400（最后一条）。
	req := httptest.NewRequest("DELETE", "/api/v1/subscriptions/plans/"+planID, nil)
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("删最后一条档位应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 再建一条，删第一条就放行。
	_, resp2 := createPlan1427(t, h, cookie, `{"name":"V2"}`)
	_ = resp2
	req = httptest.NewRequest("DELETE", "/api/v1/subscriptions/plans/"+planID, nil)
	req.Header.Set("Cookie", cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("有两条时删一条应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 剩下的最后一条再删 → 又 400。
	restID := s.cfg.Plans[0].ID
	req = httptest.NewRequest("DELETE", "/api/v1/subscriptions/plans/"+restID, nil)
	req.Header.Set("Cookie", cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("删最后一条档位应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 有订阅用户在用时 400。
	s2 := newTestServer144(t)
	h2 := s2.routes()
	cookie2 := loginCookie144(t, h2)
	_, resp3 := createPlan1427(t, h2, cookie2, `{"name":"V1"}`)
	planID2, _ := resp3["id"].(string)
	createPlan1427(t, h2, cookie2, `{"name":"V2"}`)
	createSubUser1427(t, h2, cookie2, `{"name":"小明","plan_id":"`+planID2+`"}`)
	req = httptest.NewRequest("DELETE", "/api/v1/subscriptions/plans/"+planID2, nil)
	req.Header.Set("Cookie", cookie2)
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("档位有订阅用户在用时删应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// ---------- 订阅用户创建：档位默认值与登录账号 ----------

func TestSubUserCreateWithPlan1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	setupGroup1427(s, "sg-1427", "内容组", nil)
	_, resp := createPlan1427(t, h, cookie, `{"name":"V1","group_id":"sg-1427","traffic_limit_mb":2048,"traffic_limit_mode":"monthly","reset_mode":"monthly","mode":"direct","expires_days":30}`)
	planID, _ := resp["id"].(string)
	// 只给档位：分组/限流/模式/到期都用档位的值。
	code, resp := createSubUser1427(t, h, cookie, `{"name":"小明","plan_id":"`+planID+`"}`)
	if code != 200 {
		t.Fatalf("建用户应 200，实际 %d：%v", code, resp)
	}
	id, _ := resp["id"].(string)
	row := findSubUser1427(subUsers1427(t, h, cookie), id)
	if row == nil {
		t.Fatal("列表里找不到新建的用户")
	}
	if row["group_id"] != "sg-1427" || row["traffic_limit_mb"] != float64(2048) ||
		row["traffic_limit_mode"] != "monthly" || row["mode"] != "direct" {
		t.Fatalf("档位默认值没生效，实际 %v", row)
	}
	exp, _ := row["expires_at"].(string)
	if exp == "" {
		t.Fatal("档位 ExpiresDays=30 时到期应按 now+30 天算，不应为空")
	}
	if et, err := time.Parse(time.RFC3339, exp); err != nil || et.Before(time.Now().Add(29*24*time.Hour)) || et.After(time.Now().Add(31*24*time.Hour)) {
		t.Fatalf("到期应在约 30 天后，实际 %q", exp)
	}
	if row["plan_id"] != planID || row["plan_name"] != "V1" {
		t.Fatalf("列表 plan_id/plan_name 不对，实际 %v", row)
	}
	// 个人覆盖优先：给了限流与模式就用个人的，分组仍走档位。
	code, resp = createSubUser1427(t, h, cookie, `{"name":"小红","plan_id":"`+planID+`","traffic_limit_mb":512,"mode":"relay"}`)
	if code != 200 {
		t.Fatalf("建用户应 200，实际 %d：%v", code, resp)
	}
	id2, _ := resp["id"].(string)
	row2 := findSubUser1427(subUsers1427(t, h, cookie), id2)
	if row2["traffic_limit_mb"] != float64(512) || row2["mode"] != "relay" || row2["group_id"] != "sg-1427" {
		t.Fatalf("个人覆盖没优先，实际 %v", row2)
	}
	// 不存在的档位 400。
	if code, resp := createSubUser1427(t, h, cookie, `{"name":"小刚","plan_id":"pl-nope"}`); code != 400 {
		t.Fatalf("不存在的档位应 400，实际 %d：%v", code, resp)
	}
}

func TestSubUserLoginAccount1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	// 不给 username/password：账号自动生成 u-＋randomTokenHex(6)（12 位十六进制，
	// 与 pl-/su-/sg- 各 ID 同款生成方式），没设密码。
	code, resp := createSubUser1427(t, h, cookie, `{"name":"小明"}`)
	if code != 200 {
		t.Fatalf("建用户应 200，实际 %d：%v", code, resp)
	}
	username, _ := resp["username"].(string)
	if !strings.HasPrefix(username, "u-") || len(username) != 14 {
		t.Fatalf("username 应自动生成 u-＋12 位十六进制，实际 %q", username)
	}
	if _, err := hex.DecodeString(username[2:]); err != nil {
		t.Fatalf("username 后缀应为十六进制，实际 %q：%v", username, err)
	}
	if resp["has_login"] != false {
		t.Fatalf("没设密码时 has_login 应为 false，实际 %v", resp)
	}
	// 重名 400。
	if code, resp := createSubUser1427(t, h, cookie, `{"name":"小红","username":"`+username+`"}`); code != 400 {
		t.Fatalf("username 重名应 400，实际 %d：%v", code, resp)
	}
	// 与管理员账号重名 400。
	if code, resp := createSubUser1427(t, h, cookie, `{"name":"小刚","username":"admin"}`); code != 400 {
		t.Fatalf("与管理员账号重名应 400，实际 %d：%v", code, resp)
	}
	// 密码少于 6 位 400。
	if code, resp := createSubUser1427(t, h, cookie, `{"name":"小李","password":"12345"}`); code != 400 {
		t.Fatalf("密码少于 6 位应 400，实际 %d：%v", code, resp)
	}
	// 给了密码：has_login true，列表里不返回密码哈希。
	code, resp = createSubUser1427(t, h, cookie, `{"name":"小李","username":"lisi","password":"123456"}`)
	if code != 200 || resp["has_login"] != true || resp["username"] != "lisi" {
		t.Fatalf("带密码建用户应 200 且 has_login true，实际 %d：%v", code, resp)
	}
	id, _ := resp["id"].(string)
	row := findSubUser1427(subUsers1427(t, h, cookie), id)
	if row["has_login"] != true || row["username"] != "lisi" {
		t.Fatalf("列表 has_login/username 不对，实际 %v", row)
	}
	if _, ok := row["password_hash"]; ok {
		t.Fatal("列表绝不能返回 password_hash")
	}
}

// ---------- 订阅用户创建：后端指派 ----------

func TestSubUserCreateBackendAssignment1427(t *testing.T) {
	// 指定节点：用户记录与节点 UserIDs 两边都写了。
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	setupNode1427(t, s, 1)
	code, resp := createSubUser1427(t, h, cookie, `{"name":"小明","backend_scope":"node","backend_targets":["1"]}`)
	if code != 200 {
		t.Fatalf("建用户应 200，实际 %d：%v", code, resp)
	}
	id, _ := resp["id"].(string)
	if !containsStr1427(nodeUserIDs1427(t, s, 1), "subuser:"+id) {
		t.Fatalf("节点 UserIDs 里应有该用户，实际 %v", nodeUserIDs1427(t, s, 1))
	}
	row := findSubUser1427(subUsers1427(t, h, cookie), id)
	if row["backend_scope"] != "node" {
		t.Fatalf("列表 backend_scope 应回显 node，实际 %v", row)
	}
	targets, _ := row["backend_targets"].([]any)
	if len(targets) != 1 || targets[0] != "1" {
		t.Fatalf("列表 backend_targets 应回显 [1]，实际 %v", row["backend_targets"])
	}
	// 默认跟档位分组走：档位有关联分组 → scope group。
	s2 := newTestServer144(t)
	h2 := s2.routes()
	cookie2 := loginCookie144(t, h2)
	setupNode1427(t, s2, 1)
	setupGroup1427(s2, "sg-1427", "内容组", []int64{1})
	_, resp2 := createPlan1427(t, h2, cookie2, `{"name":"V1","group_id":"sg-1427"}`)
	planID, _ := resp2["id"].(string)
	code, resp = createSubUser1427(t, h2, cookie2, `{"name":"小明","plan_id":"`+planID+`"}`)
	if code != 200 {
		t.Fatalf("建用户应 200，实际 %d：%v", code, resp)
	}
	id2, _ := resp["id"].(string)
	if resp["backend_scope"] != "group" {
		t.Fatalf("默认指派应为 group，实际 %v", resp)
	}
	if !containsStr1427(nodeUserIDs1427(t, s2, 1), "subuser:"+id2) {
		t.Fatalf("组级指派下节点 UserIDs 里应有该用户，实际 %v", nodeUserIDs1427(t, s2, 1))
	}
	// 档位没关联分组 → group 回落到全部（all）。
	s3 := newTestServer144(t)
	h3 := s3.routes()
	cookie3 := loginCookie144(t, h3)
	setupNode1427(t, s3, 1)
	_, resp3 := createPlan1427(t, h3, cookie3, `{"name":"V1"}`)
	planID3, _ := resp3["id"].(string)
	code, resp = createSubUser1427(t, h3, cookie3, `{"name":"小明","plan_id":"`+planID3+`"}`)
	if code != 200 {
		t.Fatalf("建用户应 200，实际 %d：%v", code, resp)
	}
	if resp["backend_scope"] != "all" {
		t.Fatalf("档位没分组时应回落到 all，实际 %v", resp)
	}
	id3, _ := resp["id"].(string)
	if !containsStr1427(nodeUserIDs1427(t, s3, 1), "subuser:"+id3) {
		t.Fatalf("回落 all 后节点 UserIDs 里应有该用户，实际 %v", nodeUserIDs1427(t, s3, 1))
	}
	// 显式发 node 但 targets 为空 = 先不上任何后端节点。
	s4 := newTestServer144(t)
	h4 := s4.routes()
	cookie4 := loginCookie144(t, h4)
	setupNode1427(t, s4, 1)
	code, resp = createSubUser1427(t, h4, cookie4, `{"name":"小明","backend_scope":"node"}`)
	if code != 200 {
		t.Fatalf("空 targets 的 node 指派应 200，实际 %d：%v", code, resp)
	}
	id4, _ := resp["id"].(string)
	if containsStr1427(nodeUserIDs1427(t, s4, 1), "subuser:"+id4) {
		t.Fatalf("空 targets 的 node 指派下节点 UserIDs 里不应有该用户，实际 %v", nodeUserIDs1427(t, s4, 1))
	}
	// 显式发 group 但用户没分组 → 回落 all。
	code, resp = createSubUser1427(t, h4, cookie4, `{"name":"小红","backend_scope":"group"}`)
	if code != 200 {
		t.Fatalf("没分组时显式 group 应回落 all、200，实际 %d：%v", code, resp)
	}
	if resp["backend_scope"] != "all" {
		t.Fatalf("没分组时显式 group 应回落 all，实际 %v", resp)
	}
}

// ---------- 登录 ----------

func TestClientLoginSubUser1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	setupRelayConf1427(t, s)
	setupNode1427(t, s, 1)
	setupGroup1427(s, "sg-1427", "内容组", []int64{1})
	_, resp := createPlan1427(t, h, cookie, `{"name":"V1","group_id":"sg-1427","traffic_limit_mb":2048,"traffic_limit_mode":"total","expires_days":30}`)
	planID, _ := resp["id"].(string)
	_, resp = createSubUser1427(t, h, cookie, `{"name":"小明","plan_id":"`+planID+`","username":"xiaoming","password":"123456"}`)
	userID, _ := resp["id"].(string)
	userURL, _ := resp["url"].(string)
	// 给该用户补一个后端节点 UUID（订阅/节点链接要它）。
	if u := findSubUserByID(s.cfg.SubUsers, userID); u != nil {
		if u.NodeUUIDs == nil {
			u.NodeUUIDs = map[string]string{}
		}
		u.NodeUUIDs["203.0.113.1"] = newNodeUUID()
	}
	code, lresp := clientLogin1345(t, s, `{"username":"xiaoming","password":"123456"}`)
	if code != 200 {
		t.Fatalf("SubUser 正确密码应 200，实际 %d：%v", code, lresp)
	}
	if lresp["account_type"] != "user" {
		t.Fatalf("account_type 应为 user，实际 %v", lresp["account_type"])
	}
	if lresp["plan_name"] != "V1" {
		t.Fatalf("plan_name 应为 V1，实际 %v", lresp["plan_name"])
	}
	// subscription_url 是他自己的订阅（不是别人的、不是通用/合并）：
	// 按令牌比对（创建链接用请求 Host、登录链接用主端口，Host 不同）。
	userTok := userURL[strings.LastIndex(userURL, "/")+1:]
	u, _ := lresp["subscription_url"].(string)
	if u == "" || !strings.Contains(u, "/"+s.subPath()+"/"+userTok) {
		t.Fatalf("订阅链接应是该用户自己的令牌，期望含 %q，实际 %q", userTok, u)
	}
	// plan 对象与该用户一致。
	plan, _ := lresp["plan"].(map[string]any)
	if plan == nil {
		t.Fatal("登录回包应有 plan 对象")
	}
	if plan["id"] != planID || plan["name"] != "V1" || plan["group_id"] != "sg-1427" ||
		plan["group_name"] != "内容组" || plan["traffic_limit_mb"] != float64(2048) ||
		plan["traffic_limit_mode"] != "total" {
		t.Fatalf("plan 对象与用户不一致，实际 %v", plan)
	}
	if plan["expires_at"] == "" {
		t.Fatal("plan.expires_at 应取用户的 ExpiresAt，不应为空")
	}
	// nodes：后端节点与中转落地都有。
	nodes, _ := lresp["nodes"].([]any)
	hasBackend, hasLanding := false, false
	for _, n := range nodes {
		m, _ := n.(map[string]any)
		if m["kind"] == "backend" && m["id"] == float64(1) {
			hasBackend = true
		}
		if m["kind"] == "landing" && m["id"] == "local:1" {
			hasLanding = true
		}
	}
	if !hasBackend || !hasLanding {
		t.Fatalf("nodes 应含后端节点 1 与中转落地 local:1，实际 %v", nodes)
	}
}

func TestClientLoginSubUserWrongPassword1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	createLoginUser1427(t, h, cookie, `{"name":"小明","username":"xiaoming","password":"123456"}`)
	if code, resp := clientLogin1345(t, s, `{"username":"xiaoming","password":"wrong"}`); code != 401 || resp["error"] != "账号或密码不对" {
		t.Fatalf("密码错应 401，实际 %d：%v", code, resp)
	}
	if code, resp := clientLogin1345(t, s, `{"username":"nobody","password":"123456"}`); code != 401 {
		t.Fatalf("账号不存在应 401，实际 %d：%v", code, resp)
	}
}

func TestClientLoginSubUserDisabledExpired1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	id, username := createLoginUser1427(t, h, cookie, `{"name":"小明","username":"xiaoming","password":"123456"}`)
	u := findSubUserByID(s.cfg.SubUsers, id)
	// 停用 → 401。
	u.Disabled = true
	if code, _ := clientLogin1345(t, s, `{"username":"xiaoming","password":"123456"}`); code != 401 {
		t.Fatalf("被停用应 401，实际 %d", code)
	}
	// 恢复停用、改成已过期 → 401。
	u.Disabled = false
	u.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
	if code, _ := clientLogin1345(t, s, `{"username":"xiaoming","password":"123456"}`); code != 401 {
		t.Fatalf("已过期应 401，实际 %d", code)
	}
	_ = username
}

// TestClientLoginTOTPSubUser1427 二次验证开着时管理员登录 401，
// 但 SubUser 登录仍 200（SubUser 跳过 TOTP）。
func TestClientLoginTOTPSubUser1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	createLoginUser1427(t, h, cookie, `{"name":"小明","username":"xiaoming","password":"123456"}`)
	s.cfg.TOTPEnabled = true
	if code, _ := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`); code != 401 {
		t.Fatalf("TOTP 开着时管理员登录应 401，实际 %d", code)
	}
	if code, resp := clientLogin1345(t, s, `{"username":"xiaoming","password":"123456"}`); code != 200 {
		t.Fatalf("TOTP 开着时 SubUser 登录仍应 200，实际 %d：%v", code, resp)
	}
}

func TestClientLoginAdminFallback1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	setupNode1427(t, s, 1)
	// 没有 SubUser：不给 subscription_url，nodes 为空数组，plan 为 null。
	code, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if code != 200 {
		t.Fatalf("管理员登录应 200，实际 %d：%v", code, resp)
	}
	if _, ok := resp["subscription_url"]; ok {
		t.Fatalf("没有 SubUser 时不应给 subscription_url，实际 %v", resp["subscription_url"])
	}
	if resp["plan"] != nil {
		t.Fatalf("管理员登录 plan 应为 null，实际 %v", resp["plan"])
	}
	nodes, _ := resp["nodes"].([]any)
	if len(nodes) != 0 {
		t.Fatalf("没有 SubUser 时 nodes 应为空数组，实际 %v", nodes)
	}
	// 有两份 SubUser：取第一份的订阅与节点（按令牌比对，见上）。
	_, resp1 := createSubUser1427(t, h, cookie, `{"name":"小明","username":"xiaoming","password":"123456"}`)
	_, resp2 := createSubUser1427(t, h, cookie, `{"name":"小红","username":"xiaohong","password":"123456"}`)
	url1, _ := resp1["url"].(string)
	url2, _ := resp2["url"].(string)
	tok1 := url1[strings.LastIndex(url1, "/")+1:]
	tok2 := url2[strings.LastIndex(url2, "/")+1:]
	code, resp = clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if code != 200 {
		t.Fatalf("管理员登录应 200，实际 %d：%v", code, resp)
	}
	adminURL, _ := resp["subscription_url"].(string)
	if !strings.Contains(adminURL, "/"+s.subPath()+"/"+tok1) {
		t.Fatalf("管理员登录应取第一份 SubUser 的订阅，期望含 %q，实际 %q", tok1, adminURL)
	}
	if strings.Contains(adminURL, "/"+s.subPath()+"/"+tok2) {
		t.Fatalf("管理员登录不应取第二份 SubUser 的订阅，实际 %q", adminURL)
	}
	// 管理员的 nodes 与第一份 SubUser 登录的 nodes 一致。
	_, userResp := clientLogin1345(t, s, `{"username":"xiaoming","password":"123456"}`)
	adminNodes, _ := json.Marshal(resp["nodes"])
	userNodes, _ := json.Marshal(userResp["nodes"])
	if string(adminNodes) != string(userNodes) {
		t.Fatalf("管理员 nodes 应与第一份 SubUser 一致，管理员 %s，用户 %s", adminNodes, userNodes)
	}
}

// TestClientLoginNoPlan1427 没挂档位的用户登录：plan 的 id/name 为空，
// 其余字段取用户自己的值。
func TestClientLoginNoPlan1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	createLoginUser1427(t, h, cookie, `{"name":"小明","username":"xiaoming","password":"123456"}`)
	code, resp := clientLogin1345(t, s, `{"username":"xiaoming","password":"123456"}`)
	if code != 200 {
		t.Fatalf("登录应 200，实际 %d：%v", code, resp)
	}
	plan, _ := resp["plan"].(map[string]any)
	if plan == nil {
		t.Fatal("登录回包应有 plan 对象")
	}
	if plan["id"] != "" || plan["name"] != "" {
		t.Fatalf("没挂档位时 plan 的 id/name 应为空，实际 %v", plan)
	}
	if resp["plan_name"] != "" {
		t.Fatalf("没挂档位时 plan_name 应为空，实际 %v", resp["plan_name"])
	}
}

// TestLoginPlanTrafficUsed1427 登录回包 plan.traffic_used 与订阅用户
// 列表里的 traffic_used 一致（同款 userUsageSummary 算法）。
func TestLoginPlanTrafficUsed1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	id, _ := createLoginUser1427(t, h, cookie, `{"name":"小明","username":"xiaoming","password":"123456","traffic_limit_mb":2048,"traffic_limit_mode":"total"}`)
	key := userSubKey("subuser", id)
	s.userTraffic.addDelta(key, map[string][2]int64{"a": {0, 0}})
	s.userTraffic.addDelta(key, map[string][2]int64{"a": {3000, 4000}})
	// 列表里的值。
	row := findSubUser1427(subUsers1427(t, h, cookie), id)
	listUsed, _ := row["traffic_used"].(float64)
	if listUsed <= 0 {
		t.Fatalf("种子用量后列表 traffic_used 应大于 0，实际 %v", row)
	}
	// 登录回包里的值。
	code, resp := clientLogin1345(t, s, `{"username":"xiaoming","password":"123456"}`)
	if code != 200 {
		t.Fatalf("登录应 200，实际 %d：%v", code, resp)
	}
	plan, _ := resp["plan"].(map[string]any)
	loginUsed, _ := plan["traffic_used"].(float64)
	if loginUsed != listUsed {
		t.Fatalf("plan.traffic_used 应与列表一致，登录 %v，列表 %v", loginUsed, listUsed)
	}
}

// ---------- 游客模式：订阅链接照常提供节点 ----------

func TestGuestSubLink1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	setupNode1427(t, s, 1)
	_, resp := createSubUser1427(t, h, cookie, `{"name":"小明","backend_scope":"node","backend_targets":["1"]}`)
	userID, _ := resp["id"].(string)
	userURL, _ := resp["url"].(string)
	if u := findSubUserByID(s.cfg.SubUsers, userID); u != nil {
		if u.NodeUUIDs == nil {
			u.NodeUUIDs = map[string]string{}
		}
		u.NodeUUIDs["203.0.113.1"] = newNodeUUID()
	}
	// 不用登录，直接拿订阅链接拉内容（plain 格式看明文链接名）。
	tok := userURL[strings.LastIndex(userURL, "/")+1:]
	req := httptest.NewRequest("GET", "/"+s.subPath()+"/"+tok+"?format=plain", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("游客模式拉 SubUser 订阅应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 内容里应有后端节点链接（链接名在 fragment 里是 URL 编码的，先解码再查）。
	body := rec.Body.String()
	decoded, _ := url.QueryUnescape(body)
	if !strings.Contains(decoded, "测试节点") || !strings.Contains(body, "vless://") {
		t.Fatalf("游客订阅内容里应有后端节点链接，实际 %q", body[:min(500, len(body))])
	}
}

// ---------- 退场 ----------

func TestRetiredGenericSub1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	genTok := setupGenericSub1345(t, s)
	mergedTok := setupMergedSub1426(t, s, "ms-1427-a", false, true)
	// 旧通用订阅链接 410。
	req := httptest.NewRequest("GET", "/"+s.subPath()+"/"+genTok, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 410 || !strings.Contains(rec.Body.String(), "已退场") {
		t.Fatalf("旧通用订阅应 410 退场，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 旧合并订阅链接 410。
	req = httptest.NewRequest("GET", "/"+s.subPath()+"/"+mergedTok, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 410 || !strings.Contains(rec.Body.String(), "已退场") {
		t.Fatalf("旧合并订阅应 410 退场，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// SubUser 链接仍 200（游客模式不受退场影响）。
	_, resp := createSubUser1427(t, h, cookie, `{"name":"小明"}`)
	userURL, _ := resp["url"].(string)
	tok := userURL[strings.LastIndex(userURL, "/")+1:]
	req = httptest.NewRequest("GET", "/"+s.subPath()+"/"+tok, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("SubUser 订阅应仍 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 无效令牌仍 404。
	req = httptest.NewRequest("GET", "/"+s.subPath()+"/sub_0000000000000000000000000000000000000000000000000000000000000000", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("无效令牌应 404，实际 %d", rec.Code)
	}
}

func TestRetiredMergedCreate1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	// 合并订阅创建 410。
	code, resp := postJSON144(t, h, cookie, "/api/v1/subscriptions/merged", `{"selection":[{"panel":"local","landing":1}]}`)
	if code != 410 {
		t.Fatalf("合并订阅创建应 410，实际 %d：%v", code, resp)
	}
	// rotate/resume/disable/PUT 同样 410（先直接放一条记录，不走创建接口）。
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: "ms-1427-x", CreatedAt: "2026-11-25T00:00:00Z"})
	for _, p := range []string{
		"/api/v1/subscriptions/merged/ms-1427-x/rotate",
		"/api/v1/subscriptions/merged/ms-1427-x/resume",
		"/api/v1/subscriptions/merged/ms-1427-x/disable",
	} {
		code, resp := postJSON144(t, h, cookie, p, `{}`)
		if code != 410 {
			t.Fatalf("%s 应 410，实际 %d：%v", p, code, resp)
		}
	}
	req := httptest.NewRequest("PUT", "/api/v1/subscriptions/merged/ms-1427-x", strings.NewReader(`{"selection":[{"panel":"local","landing":1}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 410 {
		t.Fatalf("合并订阅编辑应 410，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 列表照常返回，顶层 retired:true；删除保留。
	code, resp = getJSON144(t, h, cookie, "/api/v1/subscriptions/merged")
	if code != 200 || resp["retired"] != true {
		t.Fatalf("合并订阅列表应 200 且 retired:true，实际 %d：%v", code, resp)
	}
	req = httptest.NewRequest("DELETE", "/api/v1/subscriptions/merged/ms-1427-x", nil)
	req.Header.Set("Cookie", cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("合并订阅删除应保留 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

func TestRetiredGenericAPIs1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	// 通用订阅接口一律 410。
	code, _ := getJSON144(t, h, cookie, "/api/v1/settings/subscription")
	if code != 410 {
		t.Fatalf("GET 通用订阅应 410，实际 %d", code)
	}
	for _, p := range []string{
		"/api/v1/settings/subscription/generate",
		"/api/v1/settings/subscription/expiry",
		"/api/v1/settings/subscription/revoke",
	} {
		code, _ := postJSON144(t, h, cookie, p, `{}`)
		if code != 410 {
			t.Fatalf("%s 应 410，实际 %d", p, code)
		}
	}
	code, _ = getJSON144(t, h, cookie, "/api/v1/subscriptions/link?kind=generic")
	if code != 410 {
		t.Fatalf("GET 取链接应 410，实际 %d", code)
	}
	// 订阅文件名称接口不 410（共用设置，订阅用户下载还在用）。
	code, resp := postJSON144(t, h, cookie, "/api/v1/settings/subscription-filename", `{"name":"my-sub"}`)
	if code != 200 {
		t.Fatalf("订阅文件名称设置应仍 200，实际 %d：%v", code, resp)
	}
	if resp["file_name"] != "my-sub" {
		t.Fatalf("文件名应存成 my-sub，实际 %v", resp)
	}
}

// ---------- 重设密码与改档位 ----------

func TestSubUserResetPassword1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	id, username := createLoginUser1427(t, h, cookie, `{"name":"小明","username":"xiaoming","password":"123456"}`)
	// 登录一次，拿到会话；重设密码后旧会话应失效。
	code, resp := clientLogin1345(t, s, `{"username":"xiaoming","password":"123456"}`)
	if code != 200 {
		t.Fatalf("重设前登录应 200，实际 %d：%v", code, resp)
	}
	tok, _ := resp["token"].(string)
	if _, ok := s.clientSessions.validate(tok); !ok {
		t.Fatal("重设前会话应有效")
	}
	// 密码太短 400。
	code, resp = postJSON144(t, h, cookie, "/api/v1/subscriptions/users/"+id+"/reset-password", `{"password":"123"}`)
	if code != 400 {
		t.Fatalf("重设密码太短应 400，实际 %d：%v", code, resp)
	}
	code, resp = postJSON144(t, h, cookie, "/api/v1/subscriptions/users/"+id+"/reset-password", `{"password":"654321"}`)
	if code != 200 {
		t.Fatalf("重设密码应 200，实际 %d：%v", code, resp)
	}
	if _, ok := s.clientSessions.validate(tok); ok {
		t.Fatal("重设密码后旧会话应被吊销")
	}
	if code, _ := clientLogin1345(t, s, `{"username":"xiaoming","password":"123456"}`); code != 401 {
		t.Fatalf("旧密码登录应 401，实际 %d", code)
	}
	if code, _ := clientLogin1345(t, s, `{"username":"xiaoming","password":"654321"}`); code != 200 {
		t.Fatalf("新密码登录应 200，实际 %d", code)
	}
	_ = username
}

func TestSubUserUpdatePlan1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	setupGroup1427(s, "sg-1427", "内容组", nil)
	_, resp := createPlan1427(t, h, cookie, `{"name":"V1","group_id":"sg-1427","traffic_limit_mb":1024,"traffic_limit_mode":"total","mode":"relay","expires_days":10}`)
	planA, _ := resp["id"].(string)
	_, resp = createPlan1427(t, h, cookie, `{"name":"V2","traffic_limit_mb":2048,"traffic_limit_mode":"monthly","reset_mode":"monthly","mode":"direct","expires_days":30}`)
	planB, _ := resp["id"].(string)
	_, resp = createSubUser1427(t, h, cookie, `{"name":"小明","plan_id":"`+planA+`","username":"xiaoming","password":"123456"}`)
	id, _ := resp["id"].(string)
	// 改档位到 V2：分组/限流/mode/到期按新档位重算，响应里有 plan_note。
	// 编辑走 PUT。
	req := httptest.NewRequest("PUT", "/api/v1/subscriptions/users/"+id, strings.NewReader(`{"name":"小明","plan_id":"`+planB+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("改档位应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var uresp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &uresp)
	if uresp["plan_note"] == nil || !strings.Contains(fmt.Sprint(uresp["plan_note"]), "V2") {
		t.Fatalf("改档位响应应有 plan_note 说明，实际 %v", uresp)
	}
	row := findSubUser1427(subUsers1427(t, h, cookie), id)
	if row["plan_id"] != planB || row["traffic_limit_mb"] != float64(2048) || row["mode"] != "direct" {
		t.Fatalf("改档位后应按新档位重算，实际 %v", row)
	}
	// V2 没关联分组，改档位后分组应清空（列表项不带 group_id，直接查 config）。
	for _, u := range s.cfg.SubUsers {
		if u.ID == id && u.GroupID != "" {
			t.Fatalf("改到无分组档位后 GroupID 应清空，实际 %q", u.GroupID)
		}
	}
	exp, _ := row["expires_at"].(string)
	if et, err := time.Parse(time.RFC3339, exp); err != nil || et.Before(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("改档位后到期应在约 30 天后，实际 %q", exp)
	}
	// 改账号名：唯一校验。
	req = httptest.NewRequest("PUT", "/api/v1/subscriptions/users/"+id, strings.NewReader(`{"name":"小明","username":"xiaoming2"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("改账号名应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	row = findSubUser1427(subUsers1427(t, h, cookie), id)
	if row["username"] != "xiaoming2" {
		t.Fatalf("账号名应改成 xiaoming2，实际 %v", row)
	}
}

// TestPlansBackup1427 备份里 Plans 全量跟走。
func TestPlansBackup1427(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	createPlan1427(t, h, cookie, `{"name":"V1","traffic_limit_mb":1024,"expires_days":30}`)
	data := s.buildScheduledBackupData("test")
	plans, _ := data["plans"].([]planBackup)
	if len(plans) != 1 || plans[0].Name != "V1" || plans[0].TrafficLimitBytes != 1024*1024*1024 {
		t.Fatalf("备份里 plans 应有 V1，实际 %+v", plans)
	}
}

// ---------- 订阅级链接模式（Unreleased） ----------

// putSubUser1427 调订阅用户编辑接口（PUT），返回状态码与响应体。
func putSubUser1427(t *testing.T, h http.Handler, cookie, id, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("PUT", "/api/v1/subscriptions/users/"+id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// countAB1427 数链接里 -A（直连）与 -B（中转）各多少条。
func countAB1427(links []subLink) (a, b int) {
	for _, l := range links {
		if strings.HasSuffix(l.Name, "-A") {
			a++
		} else if strings.HasSuffix(l.Name, "-B") {
			b++
		}
	}
	return a, b
}

// nodeVersion1427 读某节点的 ConfigVersion（只改 LinkMode 不应 bump 它）。
func nodeVersion1427(t *testing.T, s *Server, nodeID int64) int64 {
	t.Helper()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	n := s.nodes.getNode(nodeID)
	if n == nil {
		t.Fatalf("没有这个节点：%d", nodeID)
	}
	return n.ConfigVersion
}

// TestSubUserLinkMode1427 订阅级链接模式四档：订阅 direct 只出 A 不出
// B、relay 只出 B 不出 A、both 两者都出、空值跟节点模式走（节点 direct
// 则只出 A）；改 LinkMode 后 ConfigVersion 不变；非法值 400。
func TestSubUserLinkMode1427(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	nodeID := seedContractNode1420(t, s, h, cookie)
	// 本地落地 203.0.113.1（与节点地址相同）：新建用户创建时按生效勾选
	// 生成每人 UUID，键就是这个地址，拼后端链接时才能取到 UUID。
	setupRelayConf1427(t, s)
	// 中转机 203.0.113.200:30004 转节点 203.0.113.1:443（TCP），与
	// 1425 的 twin 脚手架同款：REALITY 与 AnyTLS 能出 B，SOCKS5（1080）没路由。
	seedRemoteRelay1425(t, s, "rm-1", "203.0.113.200", []centralRelayRoute{
		{ListenAddr: "0.0.0.0:30004", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp"},
	})
	linksOf := func(id string) []subLink { return s.backendSubLinksForUser("subuser", id) }
	// 基线：订阅跟随节点（空值）、节点默认 both → 3A+2B。
	if a, b := countAB1427(linksOf("su-1420")); a != 3 || b != 2 {
		t.Fatalf("基线（跟随节点 both）应 3A+2B，实际 %dA+%dB：%v", a, b, linksOf("su-1420"))
	}
	// 订阅 direct：只出 A 不出 B；改完 ConfigVersion 不变。
	vBefore := nodeVersion1427(t, s, nodeID)
	code, resp := putSubUser1427(t, h, cookie, "su-1420", `{"name":"契约用户","link_mode":"direct"}`)
	if code != 200 || resp["link_mode"] != "direct" {
		t.Fatalf("改 link_mode=direct 应 200 且回包带 direct，实际 %d：%v", code, resp)
	}
	if vAfter := nodeVersion1427(t, s, nodeID); vAfter != vBefore {
		t.Fatalf("只改 LinkMode 时 ConfigVersion 应不变，实际 %d → %d", vBefore, vAfter)
	}
	if row := findSubUser1427(subUsers1427(t, h, cookie), "su-1420"); row["link_mode"] != "direct" {
		t.Fatalf("列表项 link_mode 应为 direct，实际 %v", row)
	}
	if a, b := countAB1427(linksOf("su-1420")); a != 3 || b != 0 {
		t.Fatalf("订阅 direct 应 3A+0B，实际 %dA+%dB：%v", a, b, linksOf("su-1420"))
	}
	// 订阅 relay：只出 B 不出 A（SOCKS5 没路由，不出）。
	code, resp = putSubUser1427(t, h, cookie, "su-1420", `{"name":"契约用户","link_mode":"relay"}`)
	if code != 200 || resp["link_mode"] != "relay" {
		t.Fatalf("改 link_mode=relay 应 200 且回包带 relay，实际 %d：%v", code, resp)
	}
	if a, b := countAB1427(linksOf("su-1420")); a != 0 || b != 2 {
		t.Fatalf("订阅 relay 应 0A+2B，实际 %dA+%dB：%v", a, b, linksOf("su-1420"))
	}
	// 订阅 both：两者都出。
	code, _ = putSubUser1427(t, h, cookie, "su-1420", `{"name":"契约用户","link_mode":"both"}`)
	if code != 200 {
		t.Fatalf("改 link_mode=both 应 200，实际 %d", code)
	}
	if a, b := countAB1427(linksOf("su-1420")); a != 3 || b != 2 {
		t.Fatalf("订阅 both 应 3A+2B，实际 %dA+%dB：%v", a, b, linksOf("su-1420"))
	}
	// 订阅改回空值（跟随节点），节点改 direct → 只出 A。
	code, resp = putSubUser1427(t, h, cookie, "su-1420", `{"name":"契约用户","link_mode":""}`)
	if code != 200 {
		t.Fatalf("改 link_mode 回空值应 200，实际 %d：%v", code, resp)
	}
	if row := findSubUser1427(subUsers1427(t, h, cookie), "su-1420"); row["link_mode"] != nil && row["link_mode"] != "" {
		t.Fatalf("改回空值后列表项 link_mode 应空，实际 %v", row)
	}
	setNodeLinkMode1425(t, s, nodeID, "direct")
	if a, b := countAB1427(linksOf("su-1420")); a != 3 || b != 0 {
		t.Fatalf("订阅空值＋节点 direct 应 3A+0B，实际 %dA+%dB：%v", a, b, linksOf("su-1420"))
	}
	// 订阅显式设置优先于节点级：节点仍是 direct，订阅 relay → 只出 B。
	code, _ = putSubUser1427(t, h, cookie, "su-1420", `{"name":"契约用户","link_mode":"relay"}`)
	if code != 200 {
		t.Fatalf("改 link_mode=relay 应 200，实际 %d", code)
	}
	if a, b := countAB1427(linksOf("su-1420")); a != 0 || b != 2 {
		t.Fatalf("订阅 relay 优先于节点 direct，应 0A+2B，实际 %dA+%dB：%v", a, b, linksOf("su-1420"))
	}
	// 非法值 400（编辑与创建都校验）。
	code, _ = putSubUser1427(t, h, cookie, "su-1420", `{"name":"契约用户","link_mode":"bogus"}`)
	if code != 400 {
		t.Fatalf("link_mode 非法值编辑应 400，实际 %d", code)
	}
	code, _ = createSubUser1427(t, h, cookie, `{"name":"非法模式","link_mode":"bogus"}`)
	if code != 400 {
		t.Fatalf("link_mode 非法值创建应 400，实际 %d", code)
	}
	// 创建时带 link_mode：列表带出该值，订阅链接按它出（relay → 只出 B）。
	code, resp = createSubUser1427(t, h, cookie, `{"name":"链接模式新建","link_mode":"relay"}`)
	if code != 200 || resp["link_mode"] != "relay" {
		t.Fatalf("创建带 link_mode=relay 应 200 且回包带 relay，实际 %d：%v", code, resp)
	}
	newID, _ := resp["id"].(string)
	if row := findSubUser1427(subUsers1427(t, h, cookie), newID); row["link_mode"] != "relay" {
		t.Fatalf("新建用户列表项 link_mode 应为 relay，实际 %v", row)
	}
	if a, b := countAB1427(linksOf(newID)); a != 0 || b != 2 {
		t.Fatalf("新建 relay 用户应 0A+2B，实际 %dA+%dB：%v", a, b, linksOf(newID))
	}
}
