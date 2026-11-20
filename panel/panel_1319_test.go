package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 1.3.19：订阅用户改勾选不换令牌 ----------

// subEditPUT 直接调编辑接口（PUT），返回状态码与解析后的 JSON。
func subEditPUT(t *testing.T, handler func(http.ResponseWriter, *http.Request), target, id, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("PUT", target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	handler(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// subUserTokenHash 取一条订阅用户的当前令牌哈希（验编辑前后令牌没变）。
func subUserTokenHash(t *testing.T, s *Server, id string) string {
	t.Helper()
	for _, u := range s.cfg.SubUsers {
		if u.ID == id {
			return u.TokenHash
		}
	}
	t.Fatalf("没有 id=%s 的订阅用户", id)
	return ""
}

func subMergedTokenHash(t *testing.T, s *Server, id string) string {
	t.Helper()
	for _, m := range s.cfg.MergedSubs {
		if m.ID == id {
			return m.TokenHash
		}
	}
	t.Fatalf("没有 id=%s 的合并订阅", id)
	return ""
}

// subUserListItem 取订阅用户列表里的一行（验编辑后列表摘要与模式跟着变）。
func subUserListItem(t *testing.T, s *Server, id string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/subscriptions/users", nil)
	rec := httptest.NewRecorder()
	s.handleSubUsersList(rec, req)
	if rec.Code != 200 {
		t.Fatalf("订阅用户列表应 200，实际 %d", rec.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	users, _ := resp["users"].([]any)
	for _, u := range users {
		if m, ok := u.(map[string]any); ok && m["id"] == id {
			return m
		}
	}
	t.Fatalf("列表里没有 id=%s 的订阅用户：%v", id, resp)
	return nil
}

func TestSubUserEditKeepsToken(t *testing.T) {
	s := newTestSubServer(t)

	// 建每人链接：只勾落地 1，仅中转 → 旧令牌只有 1 条中转链接
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersCreate(w, r) },
		"/api/v1/subscriptions/users", `{"name":"张三","mode":"relay","selection":[{"panel":"local","landing":1}]}`)
	if code != 200 {
		t.Fatalf("建订阅用户应 200，实际 %d %v", code, resp)
	}
	uid, _ := resp["id"].(string)
	url, _ := resp["url"].(string)
	tok := strings.TrimPrefix(url, "http://example.com/sub/")
	if links := subPlainLinks(t, s, tok); len(links) != 1 || !strings.Contains(links[0], "-中转") {
		t.Fatalf("编辑前旧令牌应只有 1 条中转链接，实际 %v", links)
	}
	hashBefore := subUserTokenHash(t, s, uid)

	// 编辑：改名、勾选加到落地 1+2、模式改仅直连 —— 令牌必须原样不动
	code, resp = subEditPUT(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersUpdate(w, r) },
		"/api/v1/subscriptions/users/"+uid, uid,
		`{"name":"李四","note":"备用机","mode":"direct","selection":[{"panel":"local","landing":1},{"panel":"local","landing":2}]}`)
	if code != 200 {
		t.Fatalf("编辑订阅用户应 200，实际 %d %v", code, resp)
	}
	if resp["mode"] != "direct" {
		t.Fatalf("编辑后 mode 应是 direct，实际 %v", resp)
	}
	if resp["name"] != "李四" {
		t.Fatalf("编辑后名字应是李四，实际 %v", resp)
	}
	if subUserTokenHash(t, s, uid) != hashBefore {
		t.Fatalf("编辑后令牌哈希变了（应保持不变）")
	}

	// 旧链接继续能用，内容已按新勾选 + 新模式出：两个落地的直连链接
	if links := subPlainLinks(t, s, tok); len(links) != 2 {
		t.Fatalf("编辑后旧令牌应出 2 条直连链接，实际 %v", links)
	} else {
		for _, ln := range links {
			if !strings.Contains(ln, "-直连") {
				t.Fatalf("编辑后旧令牌只应含直连链接：%s", ln)
			}
		}
	}
	// 列表行跟着变：名字、内容仅直连、勾选摘要含两个落地
	item := subUserListItem(t, s, uid)
	if item["name"] != "李四" || item["mode"] != "direct" {
		t.Fatalf("列表行应是 李四/仅直连，实际 %v", item)
	}
	if sel, ok := item["selection"].([]any); !ok || len(sel) != 2 {
		t.Fatalf("列表行勾选应是 2 个落地，实际 %v", item["selection"])
	}
}

func TestMergedSubEditKeepsToken(t *testing.T) {
	s := newTestSubServer(t)

	// 建合并订阅：只勾落地 1，仅中转 → 旧令牌只有 1 条中转链接
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleMergedSubsCreate(w, r) },
		"/api/v1/subscriptions/merged", `{"selection":[{"panel":"local","landing":1}],"mode":"relay"}`)
	if code != 200 {
		t.Fatalf("建合并订阅应 200，实际 %d %v", code, resp)
	}
	mid, _ := resp["id"].(string)
	url, _ := resp["url"].(string)
	tok := strings.TrimPrefix(url, "http://example.com/sub/")
	if links := subPlainLinks(t, s, tok); len(links) != 1 || !strings.Contains(links[0], "-中转") {
		t.Fatalf("编辑前旧令牌应只有 1 条中转链接，实际 %v", links)
	}
	hashBefore := subMergedTokenHash(t, s, mid)

	// 编辑：勾选改成只勾落地 2、模式改仅直连、归属改名 —— 令牌必须原样不动
	code, resp = subEditPUT(t, func(w http.ResponseWriter, r *http.Request) { s.handleMergedSubsUpdate(w, r) },
		"/api/v1/subscriptions/merged/"+mid, mid,
		`{"selection":[{"panel":"local","landing":2}],"mode":"direct","name":"王五"}`)
	if code != 200 {
		t.Fatalf("编辑合并订阅应 200，实际 %d %v", code, resp)
	}
	if resp["mode"] != "direct" || resp["name"] != "王五" {
		t.Fatalf("编辑后应是 王五/direct，实际 %v", resp)
	}
	if subMergedTokenHash(t, s, mid) != hashBefore {
		t.Fatalf("编辑后合并订阅令牌哈希变了（应保持不变）")
	}
	// 旧链接继续能用，内容已按新勾选出：只有落地 2 的直连链接
	links := subPlainLinks(t, s, tok)
	if len(links) != 1 || !strings.Contains(links[0], "-直连") || !strings.Contains(links[0], "203.0.113.2") {
		t.Fatalf("编辑后旧令牌应只有落地 2 的 1 条直连链接，实际 %v", links)
	}
}

func TestSubEditValidation(t *testing.T) {
	s := newTestSubServer(t)
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersCreate(w, r) },
		"/api/v1/subscriptions/users", `{"name":"张三","mode":"both"}`)
	if code != 200 {
		t.Fatalf("建订阅用户应 200，实际 %d %v", code, resp)
	}
	uid, _ := resp["id"].(string)
	userH := func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersUpdate(w, r) }

	cases := []struct {
		name string
		body string
	}{
		{"不存在的本地落地", `{"name":"张三","selection":[{"panel":"local","landing":99}]}`},
		{"没登记的远程面板", `{"name":"张三","selection":[{"panel":"no-such-panel","landing":1}]}`},
		{"非法模式", `{"name":"张三","mode":"bogus"}`},
		{"空名字", `{"name":""}`},
	}
	for _, c := range cases {
		if code, resp := subEditPUT(t, userH, "/api/v1/subscriptions/users/"+uid, uid, c.body); code != 400 {
			t.Fatalf("每人链接编辑（%s）应 400，实际 %d %v", c.name, code, resp)
		}
	}
	if code, resp := subEditPUT(t, userH, "/api/v1/subscriptions/users/no-such-id", "no-such-id", `{"name":"张三"}`); code != 404 {
		t.Fatalf("编辑不存在的用户应 404，实际 %d %v", code, resp)
	}

	// 合并订阅：空勾选必须 400（至少要一个落地）；非法模式 400；不存在的 id 404
	code, resp = subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleMergedSubsCreate(w, r) },
		"/api/v1/subscriptions/merged", `{"selection":[{"panel":"local","landing":1}],"mode":"both"}`)
	if code != 200 {
		t.Fatalf("建合并订阅应 200，实际 %d %v", code, resp)
	}
	mid, _ := resp["id"].(string)
	mergedH := func(w http.ResponseWriter, r *http.Request) { s.handleMergedSubsUpdate(w, r) }
	if code, resp := subEditPUT(t, mergedH, "/api/v1/subscriptions/merged/"+mid, mid, `{"selection":[]}`); code != 400 {
		t.Fatalf("合并订阅空勾选应 400，实际 %d %v", code, resp)
	}
	if code, resp := subEditPUT(t, mergedH, "/api/v1/subscriptions/merged/"+mid, mid,
		`{"selection":[{"panel":"local","landing":1}],"mode":"bogus"}`); code != 400 {
		t.Fatalf("合并订阅非法模式应 400，实际 %d %v", code, resp)
	}
	if code, resp := subEditPUT(t, mergedH, "/api/v1/subscriptions/merged/"+mid, mid,
		`{"selection":[{"panel":"local","landing":99}]}`); code != 400 {
		t.Fatalf("合并订阅不存在的落地应 400，实际 %d %v", code, resp)
	}
	if code, resp := subEditPUT(t, mergedH, "/api/v1/subscriptions/merged/no-such-id", "no-such-id",
		`{"selection":[{"panel":"local","landing":1}]}`); code != 404 {
		t.Fatalf("编辑不存在的合并订阅应 404，实际 %d %v", code, resp)
	}
}

func TestSubUserEditEmptySelectionIsAll(t *testing.T) {
	s := newTestSubServer(t)
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersCreate(w, r) },
		"/api/v1/subscriptions/users", `{"name":"张三","mode":"relay","selection":[{"panel":"local","landing":1}]}`)
	if code != 200 {
		t.Fatalf("建订阅用户应 200，实际 %d %v", code, resp)
	}
	uid, _ := resp["id"].(string)
	url, _ := resp["url"].(string)
	tok := strings.TrimPrefix(url, "http://example.com/sub/")
	if links := subPlainLinks(t, s, tok); len(links) != 1 {
		t.Fatalf("编辑前应只有 1 条链接，实际 %v", links)
	}
	// 编辑：勾选清空、模式不传（默认全部）→ 回到全量订阅，旧链接出全部 4 条
	code, resp = subEditPUT(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersUpdate(w, r) },
		"/api/v1/subscriptions/users/"+uid, uid, `{"name":"张三","selection":[]}`)
	if code != 200 {
		t.Fatalf("清空勾选编辑应 200，实际 %d %v", code, resp)
	}
	if links := subPlainLinks(t, s, tok); len(links) != 4 {
		t.Fatalf("清空勾选后旧令牌应出全部 4 条链接，实际 %v", links)
	}
	item := subUserListItem(t, s, uid)
	if item["kind"] != "single" {
		t.Fatalf("清空勾选后列表应是单面板全量，实际 %v", item)
	}
}

func TestSubEditRotateStillRotates(t *testing.T) {
	s := newTestSubServer(t)
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersCreate(w, r) },
		"/api/v1/subscriptions/users", `{"name":"张三","mode":"relay","selection":[{"panel":"local","landing":1}]}`)
	if code != 200 {
		t.Fatalf("建订阅用户应 200，实际 %d %v", code, resp)
	}
	uid, _ := resp["id"].(string)
	url, _ := resp["url"].(string)
	tok := strings.TrimPrefix(url, "http://example.com/sub/")

	// 先编辑：勾选改成落地 2、模式改仅直连
	code, resp = subEditPUT(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersUpdate(w, r) },
		"/api/v1/subscriptions/users/"+uid, uid,
		`{"name":"张三","mode":"direct","selection":[{"panel":"local","landing":2}]}`)
	if code != 200 {
		t.Fatalf("编辑应 200，实际 %d %v", code, resp)
	}
	// 再轮换：旧令牌立即失效，新令牌按编辑后的勾选 + 模式出内容
	req := httptest.NewRequest("POST", "/api/v1/subscriptions/users/"+uid+"/rotate", nil)
	req.SetPathValue("id", uid)
	rec := httptest.NewRecorder()
	s.handleSubUsersRotate(rec, req)
	if rec.Code != 200 {
		t.Fatalf("轮换应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var rot map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &rot)
	if rot["mode"] != "direct" {
		t.Fatalf("轮换后 mode 应保持编辑后的 direct，实际 %v", rot)
	}
	newURL, _ := rot["url"].(string)
	newTok := strings.TrimPrefix(newURL, "http://example.com/sub/")
	if rec := callSub(t, s, tok, "/sub/"+tok+"?format=plain", ""); rec.Code != 404 {
		t.Fatalf("轮换后旧令牌应 404，实际 %d", rec.Code)
	}
	links := subPlainLinks(t, s, newTok)
	if len(links) != 1 || !strings.Contains(links[0], "-直连") || !strings.Contains(links[0], "203.0.113.2") {
		t.Fatalf("轮换后新令牌应只有落地 2 的 1 条直连链接，实际 %v", links)
	}
}
