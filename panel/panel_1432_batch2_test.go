package main

// ---------- 1.4.32：面板 UI/UX 重设计第 2 批 ----------
// 分组“先建后配”＋用户新建放宽：
// ① 分组创建/编辑允许空集合（只放宽分组两处，validateMergedSelection
//    本身不动，别的实体仍按原规则）；空组列表摘要显示“空组”；
// ② 新增成员增量加/减接口，加已在别组的人直接换组，减人只动那一个人；
// ③ 新建用户后端早已允许只填名字（档位/密码选填），这里补回归断言。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestVersion1432 版本断言与 VERSION 一致（第 2 批，v1.4.32）。
func TestVersion1432(t *testing.T) {
	if Version != "1.4.34" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
}

// doJSON1432 走完整路由（带会话 cookie），返回状态码与解析后的 JSON。
func doJSON1432(t *testing.T, h http.Handler, cookie, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// createGroup1432 调分组创建接口，返回状态码与响应体。
func createGroup1432(t *testing.T, h http.Handler, cookie, body string) (int, map[string]any) {
	t.Helper()
	return doJSON1432(t, h, cookie, "POST", "/api/v1/subscriptions/groups", body)
}

// groups1432 取分组列表（已解析的 groups 数组）。
func groups1432(t *testing.T, h http.Handler, cookie string) []map[string]any {
	t.Helper()
	code, resp := doJSON1432(t, h, cookie, "GET", "/api/v1/subscriptions/groups", "")
	if code != 200 {
		t.Fatalf("分组列表应 200，实际 %d：%v", code, resp)
	}
	raw, _ := resp["groups"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		out = append(out, m)
	}
	return out
}

// findGroup1432 在列表里按 id 找一行。
func findGroup1432(groups []map[string]any, id string) map[string]any {
	for _, g := range groups {
		if g["id"] == id {
			return g
		}
	}
	return nil
}

// ---------- 空组：创建 / 编辑清空 / 列表摘要 ----------

func TestSubGroupCreateEmpty1432(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	// 只填名字就能建空组（以前 400“请先勾选至少一个落地”）。
	code, resp := createGroup1432(t, h, cookie, `{"name":"空组"}`)
	if code != 200 {
		t.Fatalf("空组创建应 200，实际 %d：%v", code, resp)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("建组回包应有 id，实际 %v", resp)
	}
	// 列表里空组摘要是“空组”，不是空白也不是异常。
	g := findGroup1432(groups1432(t, h, cookie), id)
	if g == nil {
		t.Fatal("列表里找不到刚建的空组")
	}
	if g["summary"] != "空组" {
		t.Fatalf("空组列表摘要应为“空组”，实际 %v", g["summary"])
	}
	if sel, _ := g["selection"].([]any); len(sel) != 0 {
		t.Fatalf("空组 selection 应为空，实际 %v", g["selection"])
	}
	// 空集合校验只放宽了“空”：勾了不存在的落地仍 400。
	if code, resp := createGroup1432(t, h, cookie, `{"name":"坏组","selection":[{"panel":"local","landing":99}]}`); code != 400 {
		t.Fatalf("勾不存在的落地应 400，实际 %d：%v", code, resp)
	}
	// 共享校验器本身没动：空集合直接调它仍报错。
	if _, _, err := s.validateMergedSelection(nil); err == nil {
		t.Fatal("validateMergedSelection 空集合应仍报错（只放宽了分组两处）")
	}
}

func TestSubGroupUpdateClearSelection1432(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	setupRelayConf1427(t, s)
	// 先建一个带落地的组。
	code, resp := createGroup1432(t, h, cookie, `{"name":"有落地组","selection":[{"panel":"local","landing":1}]}`)
	if code != 200 {
		t.Fatalf("带落地建组应 200，实际 %d：%v", code, resp)
	}
	id, _ := resp["id"].(string)
	// 组里先加一个人（自己的勾选是本地落地 1）。
	uid, _ := createLoginUser1427(t, h, cookie, `{"name":"小明","selection":[{"panel":"local","landing":1}]}`)
	if code, resp := doJSON1432(t, h, cookie, "POST", "/api/v1/subscriptions/groups/"+id+"/members", `{"kind":"user","id":"`+uid+`"}`); code != 200 {
		t.Fatalf("加成员应 200，实际 %d：%v", code, resp)
	}
	// 编辑时清空落地集合：允许清空，不 400。
	code, resp = doJSON1432(t, h, cookie, "PUT", "/api/v1/subscriptions/groups/"+id, `{"name":"有落地组","selection":[],"members":[{"kind":"user","id":"`+uid+`"}]}`)
	if code != 200 {
		t.Fatalf("清空落地编辑应 200，实际 %d：%v", code, resp)
	}
	g := findGroup1432(groups1432(t, h, cookie), id)
	if g == nil {
		t.Fatal("列表里找不到该组")
	}
	if g["summary"] != "空组" {
		t.Fatalf("清空后列表摘要应为“空组”，实际 %v", g["summary"])
	}
	// 空组＋成员进组时，成员仍按自己的勾选出订阅，不会断网。
	u := findSubUserByID(s.cfg.SubUsers, uid)
	if u == nil || u.GroupID != id {
		t.Fatalf("清空落地不应把成员踢出组，实际 GroupID=%v", u)
	}
	eff := s.effectiveSelectionForSubUser(u)
	if len(eff) != 1 || eff[0].Landing != 1 {
		t.Fatalf("空组成员应回落自己的勾选（本地落地 1），实际 %+v", eff)
	}
}

// ---------- 成员增量加 / 减 ----------

func TestSubGroupMembersAddRemove1432(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	_, resp := createGroup1432(t, h, cookie, `{"name":"空组A"}`)
	gidA, _ := resp["id"].(string)
	_, resp = createGroup1432(t, h, cookie, `{"name":"空组B"}`)
	gidB, _ := resp["id"].(string)
	uidA, _ := createLoginUser1427(t, h, cookie, `{"name":"小明"}`)
	uidB, _ := createLoginUser1427(t, h, cookie, `{"name":"小红"}`)
	uidC, _ := createLoginUser1427(t, h, cookie, `{"name":"小刚"}`)
	addURL := "/api/v1/subscriptions/groups/" + gidA + "/members"
	delURL := func(gid, kind, mid string) string {
		return "/api/v1/subscriptions/groups/" + gid + "/members/" + kind + "/" + mid
	}
	// 空组加人：200。
	if code, resp := doJSON1432(t, h, cookie, "POST", addURL, `{"kind":"user","id":"`+uidA+`"}`); code != 200 {
		t.Fatalf("空组加人应 200，实际 %d：%v", code, resp)
	}
	if u := findSubUserByID(s.cfg.SubUsers, uidA); u == nil || u.GroupID != gidA {
		t.Fatalf("加人后小明 GroupID 应为 %s，实际 %+v", gidA, u)
	}
	// 再加一次同一人：幂等 200，不报错。
	if code, resp := doJSON1432(t, h, cookie, "POST", addURL, `{"kind":"user","id":"`+uidA+`"}`); code != 200 {
		t.Fatalf("重复加同一人应 200，实际 %d：%v", code, resp)
	}
	// 加不存在的人：400。
	if code, _ := doJSON1432(t, h, cookie, "POST", addURL, `{"kind":"user","id":"su-nope"}`); code != 400 {
		t.Fatalf("加不存在的人应 400，实际 %d", code)
	}
	// 加不存在的分组：404。
	if code, _ := doJSON1432(t, h, cookie, "POST", "/api/v1/subscriptions/groups/sg-nope/members", `{"kind":"user","id":"`+uidA+`"}`); code != 404 {
		t.Fatalf("往不存在的分组加人应 404，实际 %d", code)
	}
	// 成员类型不对：400。
	if code, _ := doJSON1432(t, h, cookie, "POST", addURL, `{"kind":"alien","id":"`+uidA+`"}`); code != 400 {
		t.Fatalf("成员类型不对时应 400，实际 %d", code)
	}
	// 小红先进 B 组，再加进 A 组：直接换组。
	if code, _ := doJSON1432(t, h, cookie, "POST", "/api/v1/subscriptions/groups/"+gidB+"/members", `{"kind":"user","id":"`+uidB+`"}`); code != 200 {
		t.Fatalf("小红进 B 组应 200，实际 %d", code)
	}
	if code, _ := doJSON1432(t, h, cookie, "POST", addURL, `{"kind":"user","id":"`+uidB+`"}`); code != 200 {
		t.Fatalf("已在别组的人加进 A 组应 200（换组），实际 %d", code)
	}
	if u := findSubUserByID(s.cfg.SubUsers, uidB); u == nil || u.GroupID != gidA {
		t.Fatalf("换组后小红 GroupID 应为 %s，实际 %+v", gidA, u)
	}
	// 小刚没进过组，从 A 组减他：404，且不动任何数据。
	if code, _ := doJSON1432(t, h, cookie, "DELETE", delURL(gidA, "user", uidC), ""); code != 404 {
		t.Fatalf("减不在组里的人应 404，实际 %d", code)
	}
	// 减不存在的人：404。
	if code, _ := doJSON1432(t, h, cookie, "DELETE", delURL(gidA, "user", "su-nope"), ""); code != 404 {
		t.Fatalf("减不存在的人应 404，实际 %d", code)
	}
	// 减人只清那一个人：小明出组，小红还在 A 组。
	if code, _ := doJSON1432(t, h, cookie, "DELETE", delURL(gidA, "user", uidA), ""); code != 200 {
		t.Fatalf("减人应 200，实际 %d", code)
	}
	if u := findSubUserByID(s.cfg.SubUsers, uidA); u == nil || u.GroupID != "" {
		t.Fatalf("减人后小明 GroupID 应为空，实际 %+v", u)
	}
	if u := findSubUserByID(s.cfg.SubUsers, uidB); u == nil || u.GroupID != gidA {
		t.Fatalf("减小明不应动小红，小红 GroupID 应仍为 %s，实际 %+v", gidA, u)
	}
	// 再减一次小明：404。
	if code, _ := doJSON1432(t, h, cookie, "DELETE", delURL(gidA, "user", uidA), ""); code != 404 {
		t.Fatalf("重复减同一人应 404，实际 %d", code)
	}
	// 从不存在的分组减人：404。
	if code, _ := doJSON1432(t, h, cookie, "DELETE", delURL("sg-nope", "user", uidB), ""); code != 404 {
		t.Fatalf("从不存在的分组减人应 404，实际 %d", code)
	}
}

// ---------- 新建用户只填名字（后端回归断言，前端放宽的依据） ----------

func TestSubUserCreateNameOnly1432(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	// 只填名字：档位/密码都不填，后端应 200（前端以前硬拦两道是多余的）。
	code, resp := createSubUser1427(t, h, cookie, `{"name":"只填名字"}`)
	if code != 200 {
		t.Fatalf("只填名字建用户应 200，实际 %d：%v", code, resp)
	}
	id, _ := resp["id"].(string)
	username, _ := resp["username"].(string)
	if id == "" || username == "" {
		t.Fatalf("回包应有 id 与自动生成的 username，实际 %v", resp)
	}
	u := findSubUserByID(s.cfg.SubUsers, id)
	if u == nil {
		t.Fatal("config 里找不到刚建的用户")
	}
	// 没填密码 = 不能登录：PasswordHash 为空。
	if u.PasswordHash != "" {
		t.Fatal("没填密码时 PasswordHash 应为空（不能登录）")
	}
	// 没选档位 = 不挂档位，但默认档位已被 ensureDefaultPlan 兜底建好
	// （写路径上建，之后随时能挂；列表里 plan_name 为空，不会凭空
	// 显示一个没挂的档位）。
	if len(s.cfg.Plans) != 1 || s.cfg.Plans[0].ID != "pl-default" {
		t.Fatalf("建用户后默认档位应已兜底落盘，实际 %+v", s.cfg.Plans)
	}
	users := subUsers1427(t, h, cookie)
	row := findSubUser1427(users, id)
	if row == nil {
		t.Fatal("用户列表里找不到刚建的用户")
	}
	if pn, _ := row["plan_name"].(string); pn != "" {
		t.Fatalf("没选档位时列表 plan_name 应为空（不挂档位），实际 %q", pn)
	}
	// 填了密码仍须 ≥6 位：后端 400。
	if code, resp := createSubUser1427(t, h, cookie, `{"name":"短密码","password":"123"}`); code != 400 {
		t.Fatalf("密码短于 6 位应 400，实际 %d：%v", code, resp)
	}
}
