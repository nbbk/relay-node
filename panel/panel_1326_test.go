package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ---------- 1.3.26：分组（权限组） ----------

// callJSON1326 直接调一个分组/订阅接口，返回状态码与解析后的 JSON。
// id 非空时写进路径参数（分组编辑/删除接口用）。
func callJSON1326(t *testing.T, h func(http.ResponseWriter, *http.Request), method, target, body, id string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if id != "" {
		req.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// createSubUser1326 建一个订阅用户，返回 id 与订阅令牌（从返回的 url 里取）。
func createSubUser1326(t *testing.T, s *Server, body string) (string, string) {
	t.Helper()
	code, resp := callJSON1326(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", body, "")
	if code != 200 {
		t.Fatalf("建订阅用户应 200，实际 %d：%v", code, resp)
	}
	id, _ := resp["id"].(string)
	urlStr, _ := resp["url"].(string)
	u, err := url.Parse(urlStr)
	if err != nil || u.Path == "" {
		t.Fatalf("订阅 url 解析失败：%q %v", urlStr, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	tok := parts[len(parts)-1]
	if id == "" || tok == "" || !strings.HasPrefix(tok, "sub_") {
		t.Fatalf("建订阅用户返回不对：id=%q url=%q", id, urlStr)
	}
	return id, tok
}

// plainSubLinks1326 取一条订阅的明文链接列表（?format=plain，一行一条）。
func plainSubLinks1326(t *testing.T, s *Server, tok string) []string {
	t.Helper()
	rec := callSub(t, s, tok, "/sub/"+tok+"?format=plain", "")
	if rec.Code != 200 {
		t.Fatalf("取订阅应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	body := strings.TrimSpace(rec.Body.String())
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

// assertLinkFragments1326 断言链接列表里有 want 里的每个片段、没有
// notWant 里的任何片段。片段取节点链接的 # 后缀（智利-中转 / test-直连
// 这种），不拿域名判——落地 2 的域名 node2.example.com 包含 example.com，
// 直接判域名会误判。
func assertLinkFragments1326(t *testing.T, links []string, want, notWant []string) {
	t.Helper()
	joined := strings.Join(links, "\n")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Fatalf("订阅应包含 %q，实际 %d 条：%v", w, len(links), links)
		}
	}
	for _, nw := range notWant {
		if strings.Contains(joined, nw) {
			t.Fatalf("订阅不应包含 %q，实际 %d 条：%v", nw, len(links), links)
		}
	}
}

// setupGroupUser1326 建一个自己勾落地 2 的订阅用户，再建一个落地 1 的分组
// 把他拉进去，返回用户 id、令牌、分组 id。
func setupGroupUser1326(t *testing.T, s *Server) (string, string, string) {
	t.Helper()
	uid, tok := createSubUser1326(t, s, `{"name":"小明","selection":[{"panel":"local","landing":2}]}`)
	code, resp := callJSON1326(t, s.handleSubGroupsCreate, "POST", "/api/v1/subscriptions/groups", `{"name":"智利组","selection":[{"panel":"local","landing":1}],"members":[{"kind":"user","id":"`+uid+`"}]}`, "")
	if code != 200 {
		t.Fatalf("建组应 200，实际 %d：%v", code, resp)
	}
	gid, _ := resp["id"].(string)
	if !strings.HasPrefix(gid, "sg-") {
		t.Fatalf("分组 id 应以 sg- 开头，实际 %q", gid)
	}
	return uid, tok, gid
}

func TestSubGroupCreate1326(t *testing.T) {
	s := newTestSubServer(t)
	// 空名字 → 400
	if code, _ := callJSON1326(t, s.handleSubGroupsCreate, "POST", "/api/v1/subscriptions/groups", `{"name":"","selection":[{"panel":"local","landing":1}]}`, ""); code != 400 {
		t.Fatalf("空名字应 400，实际 %d", code)
	}
	// 不勾落地 → 400
	if code, _ := callJSON1326(t, s.handleSubGroupsCreate, "POST", "/api/v1/subscriptions/groups", `{"name":"美国组","selection":[]}`, ""); code != 400 {
		t.Fatalf("不勾落地应 400，实际 %d", code)
	}
	// 不存在的成员 → 400
	if code, _ := callJSON1326(t, s.handleSubGroupsCreate, "POST", "/api/v1/subscriptions/groups", `{"name":"美国组","selection":[{"panel":"local","landing":1}],"members":[{"kind":"user","id":"su-nope"}]}`, ""); code != 400 {
		t.Fatalf("不存在的成员应 400，实际 %d", code)
	}
	// 不存在的分组 id（建用户时）→ 400
	if code, _ := callJSON1326(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"小红","group_id":"sg-nope"}`, ""); code != 400 {
		t.Fatalf("不存在的分组 id 应 400，实际 %d", code)
	}
	// 正常建组：落地 1，带一个成员
	uid, _ := createSubUser1326(t, s, `{"name":"小明","selection":[{"panel":"local","landing":2}]}`)
	code, resp := callJSON1326(t, s.handleSubGroupsCreate, "POST", "/api/v1/subscriptions/groups", `{"name":"智利组","note":"测试","selection":[{"panel":"local","landing":1}],"members":[{"kind":"user","id":"`+uid+`"}]}`, "")
	if code != 200 {
		t.Fatalf("建组应 200，实际 %d：%v", code, resp)
	}
	gid, _ := resp["id"].(string)
	// 成员归属已写上
	for _, u := range s.cfg.SubUsers {
		if u.ID == uid && u.GroupID != gid {
			t.Fatalf("成员 GroupID 应是 %s，实际 %q", gid, u.GroupID)
		}
	}
	// 列表里能看到，成员数 1，摘要是落地 1
	code, resp = callJSON1326(t, s.handleSubGroupsList, "GET", "/api/v1/subscriptions/groups", "", "")
	if code != 200 {
		t.Fatalf("分组列表应 200，实际 %d", code)
	}
	groups, _ := resp["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("应有 1 个分组，实际 %d", len(groups))
	}
	g := groups[0].(map[string]any)
	if g["name"] != "智利组" {
		t.Fatalf("分组列表行不对：%v", g)
	}
	members, _ := g["members"].([]any)
	if len(members) != 1 {
		t.Fatalf("分组应有 1 名成员，实际 %v", members)
	}
}

// 组落地集合生效：进了组的用户，订阅只含组内落地，不再看自己勾的那份；
// 自己的勾选保留在记录里，列表摘要带分组名。
func TestSubGroupEffectiveSelection1326(t *testing.T) {
	s := newTestSubServer(t)
	uid, tok, gid := setupGroupUser1326(t, s)
	// 进组后：订阅只有落地 1（智利-中转/直连），落地 2 的没了
	links := plainSubLinks1326(t, s, tok)
	if len(links) != 2 {
		t.Fatalf("进组后订阅应只有 2 条链接（落地 1 的中转+直连），实际 %d：%v", len(links), links)
	}
	assertLinkFragments1326(t, links, []string{"智利-中转", "智利-直连"}, []string{"test-中转", "test-直连"})
	// 用户自己的勾选还在记录里（落地 2），没被组覆盖
	for _, u := range s.cfg.SubUsers {
		if u.ID == uid {
			if len(u.Selection) != 1 || u.Selection[0].Landing != 2 {
				t.Fatalf("自己的勾选应保留落地 2，实际 %v", u.Selection)
			}
		}
	}
	// 订阅用户列表行带分组徽章字段，摘要是组的落地
	item := subUserListItem(t, s, uid)
	if item["group_id"] != gid || item["group_name"] != "智利组" {
		t.Fatalf("列表行应带 group_id/group_name，实际 %v", item)
	}
	if sum, _ := item["summary"].(string); !strings.Contains(sum, "分组「智利组」") {
		t.Fatalf("列表摘要应带分组名，实际 %q", sum)
	}
}

// 改组落地后成员订阅立即同步，链接（令牌）不变。
func TestSubGroupUpdateSync1326(t *testing.T) {
	s := newTestSubServer(t)
	uid, tok, gid := setupGroupUser1326(t, s)
	code, resp := callJSON1326(t, s.handleSubGroupsUpdate, "PUT", "/api/v1/subscriptions/groups/"+gid, `{"name":"智利组","selection":[{"panel":"local","landing":2}],"members":[{"kind":"user","id":"`+uid+`"}]}`, gid)
	if code != 200 {
		t.Fatalf("改组应 200，实际 %d：%v", code, resp)
	}
	// 同一个令牌再取：只有落地 2，落地 1 的没了
	links := plainSubLinks1326(t, s, tok)
	if len(links) != 2 {
		t.Fatalf("改组后订阅应只有 2 条链接（落地 2 的中转+直连），实际 %d：%v", len(links), links)
	}
	assertLinkFragments1326(t, links, []string{"test-中转", "test-直连"}, []string{"智利-中转", "智利-直连"})
	// 令牌没变：旧令牌还能用（上面 plainSubLinks 已用同一个 tok 取到 200）
}

// 删组：成员自动回到自己原来勾的落地集合，不删用户、不换令牌。
func TestSubGroupDeleteRestore1326(t *testing.T) {
	s := newTestSubServer(t)
	uid, tok, gid := setupGroupUser1326(t, s)
	code, resp := callJSON1326(t, s.handleSubGroupsDelete, "DELETE", "/api/v1/subscriptions/groups/"+gid, "", gid)
	if code != 200 {
		t.Fatalf("删组应 200，实际 %d：%v", code, resp)
	}
	// 用户还在，GroupID 空了
	found := false
	for _, u := range s.cfg.SubUsers {
		if u.ID == uid {
			found = true
			if u.GroupID != "" {
				t.Fatalf("删组后 GroupID 应清空，实际 %q", u.GroupID)
			}
		}
	}
	if !found {
		t.Fatalf("删组不应删用户，用户 %s 没了", uid)
	}
	// 订阅恢复自己原来勾的落地 2，同一个令牌
	links := plainSubLinks1326(t, s, tok)
	if len(links) != 2 {
		t.Fatalf("删组后订阅应恢复 2 条链接（落地 2），实际 %d：%v", len(links), links)
	}
	assertLinkFragments1326(t, links, []string{"test-中转", "test-直连"}, []string{"智利-中转", "智利-直连"})
	// 分组列表空了
	if code, resp := callJSON1326(t, s.handleSubGroupsList, "GET", "/api/v1/subscriptions/groups", "", ""); code != 200 {
		t.Fatalf("分组列表应 200，实际 %d", code)
	} else if groups, _ := resp["groups"].([]any); len(groups) != 0 {
		t.Fatalf("删组后分组列表应为空，实际 %d", len(groups))
	}
	// 删不存在的分组 → 404
	if code, _ := callJSON1326(t, s.handleSubGroupsDelete, "DELETE", "/api/v1/subscriptions/groups/sg-nope", "", "sg-nope"); code != 404 {
		t.Fatalf("删不存在的分组应 404，实际 %d", code)
	}
}

// 一个用户只能属于一个组：拉进 B 组就自动从 A 组出来；
// 编辑订阅用户换组也一样（直接改归属）。
func TestSubGroupOneGroupOnly1326(t *testing.T) {
	s := newTestSubServer(t)
	uid, tok, gidA := setupGroupUser1326(t, s)
	// 再建 B 组（落地 2），成员还是小明
	code, resp := callJSON1326(t, s.handleSubGroupsCreate, "POST", "/api/v1/subscriptions/groups", `{"name":"二组","selection":[{"panel":"local","landing":2}],"members":[{"kind":"user","id":"`+uid+`"}]}`, "")
	if code != 200 {
		t.Fatalf("建 B 组应 200，实际 %d：%v", code, resp)
	}
	gidB, _ := resp["id"].(string)
	// 用户归属变成 B，A 组里没人了
	for _, u := range s.cfg.SubUsers {
		if u.ID == uid && u.GroupID != gidB {
			t.Fatalf("用户应已换到 B 组 %s，实际 %q", gidB, u.GroupID)
		}
	}
	groups := groupList1326(t, s)
	for _, g := range groups {
		if g["id"] == gidA {
			if members, _ := g["members"].([]any); len(members) != 0 {
				t.Fatalf("A 组应已没人，实际 %v", members)
			}
		}
	}
	// 订阅按 B 组出（落地 2）
	assertLinkFragments1326(t, plainSubLinks1326(t, s, tok), []string{"test-中转", "test-直连"}, []string{"智利-中转", "智利-直连"})
	// 编辑订阅用户换回 A 组（group_id），自己的勾选不动
	code, resp = subEditPUT(t, s.handleSubUsersUpdate, "/api/v1/subscriptions/users/"+uid, uid, `{"name":"小明","selection":[{"panel":"local","landing":2}],"group_id":"`+gidA+`"}`)
	if code != 200 {
		t.Fatalf("编辑换组应 200，实际 %d：%v", code, resp)
	}
	if resp["group_id"] != gidA {
		t.Fatalf("编辑后 group_id 应是 %s，实际 %v", gidA, resp["group_id"])
	}
	for _, u := range s.cfg.SubUsers {
		if u.ID == uid {
			if u.GroupID != gidA {
				t.Fatalf("编辑后归属应是 A 组，实际 %q", u.GroupID)
			}
			if len(u.Selection) != 1 || u.Selection[0].Landing != 2 {
				t.Fatalf("换组时自己的勾选应保留落地 2，实际 %v", u.Selection)
			}
		}
	}
	assertLinkFragments1326(t, plainSubLinks1326(t, s, tok), []string{"智利-中转", "智利-直连"}, []string{"test-中转", "test-直连"})
	// 编辑出组（group_id 空串）：恢复自己勾选（落地 2）
	code, resp = subEditPUT(t, s.handleSubUsersUpdate, "/api/v1/subscriptions/users/"+uid, uid, `{"name":"小明","selection":[{"panel":"local","landing":2}],"group_id":""}`)
	if code != 200 {
		t.Fatalf("编辑出组应 200，实际 %d：%v", code, resp)
	}
	assertLinkFragments1326(t, plainSubLinks1326(t, s, tok), []string{"test-中转", "test-直连"}, []string{"智利-中转", "智利-直连"})
}

func groupList1326(t *testing.T, s *Server) []map[string]any {
	t.Helper()
	code, resp := callJSON1326(t, s.handleSubGroupsList, "GET", "/api/v1/subscriptions/groups", "", "")
	if code != 200 {
		t.Fatalf("分组列表应 200，实际 %d", code)
	}
	raw, _ := resp["groups"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, g := range raw {
		out = append(out, g.(map[string]any))
	}
	return out
}

// 不进分组的订阅用户（全量）不受分组影响：建组、改组、删组都不影响它。
func TestSubGroupGeneralUnaffected1326(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	subUserForToken1427(t, s, tok)
	uid, _, gid := setupGroupUser1326(t, s)
	// 建组后不进组的订阅仍是全部 4 条
	links := plainSubLinks1326(t, s, tok)
	if len(links) != 4 {
		t.Fatalf("建组后不进组的订阅应仍有 4 条链接，实际 %d：%v", len(links), links)
	}
	// 改组、删组后也一样
	if code, _ := callJSON1326(t, s.handleSubGroupsUpdate, "PUT", "/api/v1/subscriptions/groups/"+gid, `{"name":"智利组","selection":[{"panel":"local","landing":2}],"members":[{"kind":"user","id":"`+uid+`"}]}`, gid); code != 200 {
		t.Fatalf("改组应 200，实际 %d", code)
	}
	if len(plainSubLinks1326(t, s, tok)) != 4 {
		t.Fatalf("改组后不进组的订阅应仍有 4 条链接")
	}
	if code, _ := callJSON1326(t, s.handleSubGroupsDelete, "DELETE", "/api/v1/subscriptions/groups/"+gid, "", gid); code != 200 {
		t.Fatalf("删组应 200，实际 %d", code)
	}
	if len(plainSubLinks1326(t, s, tok)) != 4 {
		t.Fatalf("删组后不进组的订阅应仍有 4 条链接")
	}
}

// 组含远程落地：远程面板低于 1.3.26 也能用（只提供落地，走现有 central
// 订阅接口取链接）；组内用户订阅只有远程那条链接。
func TestSubGroupRemoteLanding1326(t *testing.T) {
	s := newTestSubServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/central/status":
			writeJSON(w, 200, map[string]any{"ok": true, "panel_version": "1.3.25", "realm": map[string]any{"running": true}, "landings": []any{map[string]any{"index": 1, "landing": "203.0.113.9", "domain": "remote.example.com", "mode": "standard", "online": true}}})
		case "/api/v1/central/subscription":
			writeJSON(w, 200, map[string]any{"ok": true, "links": []any{map[string]any{"name": "远程-中转", "link": "vless://11111111-2222-3333-4444-555555555555@remote.example.com:443?encryption=none&security=tls&type=ws&host=remote.example.com&path=%2Fexample-path#远程-中转"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s.cfg.Remotes = []RemotePanel{{ID: "rm-1", Name: "香港", BaseURL: srv.URL, Token: "rpt_test"}}
	uid, tok := createSubUser1326(t, s, `{"name":"小明","selection":[{"panel":"local","landing":2}]}`)
	code, resp := callJSON1326(t, s.handleSubGroupsCreate, "POST", "/api/v1/subscriptions/groups", `{"name":"远程组","selection":[{"panel":"rm-1","landing":1}],"members":[{"kind":"user","id":"`+uid+`"}]}`, "")
	if code != 200 {
		t.Fatalf("含远程落地的分组应能建，实际 %d：%v", code, resp)
	}
	links := plainSubLinks1326(t, s, tok)
	if len(links) != 1 || !strings.Contains(links[0], "remote.example.com") {
		t.Fatalf("组内用户订阅应只有远程那 1 条链接，实际 %v", links)
	}
}

// 合并订阅进组：订阅按组的落地出；出组后恢复自己勾选。
// 订阅用户进组：订阅按组的落地出；出组后恢复自己勾选。
// （1.4.27：合并订阅已退场，进组的成员改走订阅用户；合并订阅进组的
// 旧用例不再适用，组行为本身由本用例覆盖。）
func TestSubGroupMergedSub1326(t *testing.T) {
	s := newTestSubServer(t)
	// 订阅用户自己勾落地 2，建落地 1 的分组把他拉进去。
	uid, tok, gid := setupGroupUser1326(t, s)
	// 进组后：落地 1
	assertLinkFragments1326(t, plainSubLinks1326(t, s, tok), []string{"智利-中转", "智利-直连"}, []string{"test-中转", "test-直连"})
	// 订阅用户列表行带分组徽章
	item := subUserListItem(t, s, uid)
	if item["group_id"] != gid || item["group_name"] != "智利组" {
		t.Fatalf("订阅用户列表行应带 group_id/group_name，实际 %v", item)
	}
	// 编辑订阅用户出组：恢复自己勾选（落地 2）
	code, resp := subEditPUT(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersUpdate(w, r) },
		"/api/v1/subscriptions/users/"+uid, uid, `{"name":"小明","selection":[{"panel":"local","landing":2}],"group_id":""}`)
	if code != 200 {
		t.Fatalf("编辑订阅用户出组应 200，实际 %d：%v", code, resp)
	}
	assertLinkFragments1326(t, plainSubLinks1326(t, s, tok), []string{"test-中转", "test-直连"}, []string{"智利-中转", "智利-直连"})
}

// 备份/导入：分组随整机配置一起走；成员的分组指向在备份里没有时，
// 清掉该成员的分组（恢复自己勾选），并在导入结果里写明。
func TestSubGroupBackupRestore1326(t *testing.T) {
	s := newTestSubServer(t)
	uid, _, gid := setupGroupUser1326(t, s)
	// 定时备份数据里应有分组与成员的分组归属
	data := s.buildScheduledBackupData("测试备份")
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("备份数据序列化失败：%v", err)
	}
	var p importPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("备份数据解析失败：%v", err)
	}
	if len(p.SubGroups) != 1 || p.SubGroups[0].ID != gid {
		t.Fatalf("备份里应有 1 个分组 %s，实际 %v", gid, p.SubGroups)
	}
	found := false
	for _, bu := range p.SubUsers {
		if bu.ID == uid && bu.GroupID == gid {
			found = true
		}
	}
	if !found {
		t.Fatalf("备份里的订阅用户应带 group_id %s，实际 %v", gid, p.SubUsers)
	}
	// 导入一份“分组丢了”的备份：分组列表为空，但用户还指着旧分组。
	// 注意：测试夹具的 config.toml 是空的，写回时会被校验挡住（真机上
	// config.toml 一直是带 [[endpoints]] 的真配置），这里直接给一份
	// 带 [[endpoints]] 的最小配置，测的就是分组恢复那段逻辑。
	p2 := importPayload{
		RelayConf:  subTestRelayConf,
		ConfigToml: "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n",
		SubGroups:  []SubGroup{},
		SubUsers:   []subUserBackup{{ID: uid, Name: "小明", Selection: []SubSelection{{Panel: "local", Landing: 2}}, GroupID: "sg-missing"}},
	}
	warn, fail := s.applyImportPayload(p2, importApplyOpts{preBackupLabel: "测试恢复前备份", auditAction: "测试导入", auditDetail: "分组导入测试", restartWarnPrefix: "导入成功，但重启失败：", user: "test"})
	if fail != nil {
		t.Fatalf("导入应成功，实际失败：%v", fail)
	}
	if !strings.Contains(warn, "分组在备份里没有") {
		t.Fatalf("导入结果应写明成员的分组在备份里没有，实际 warn=%q", warn)
	}
	// 成员的分组已清掉（恢复自己勾选），分组列表为空
	for _, u := range s.cfg.SubUsers {
		if u.ID == uid && u.GroupID != "" {
			t.Fatalf("导入后成员 GroupID 应清空，实际 %q", u.GroupID)
		}
	}
	if len(s.cfg.SubGroups) != 0 {
		t.Fatalf("导入后分组列表应为空，实际 %d", len(s.cfg.SubGroups))
	}
}
