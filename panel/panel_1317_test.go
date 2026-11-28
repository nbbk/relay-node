package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 1.3.17：订阅内容三选一（全部/仅中转/仅直连） ----------

// subPlainLinks 取一条订阅的明文链接列表（?format=plain）。
func subPlainLinks(t *testing.T, s *Server, tok string) []string {
	t.Helper()
	rec := callSub(t, s, tok, "/sub/"+tok+"?format=plain", "")
	if rec.Code != 200 {
		t.Fatalf("订阅应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	body := strings.TrimSpace(rec.Body.String())
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

// subModeCreatePOST 直接调创建接口，返回状态码与解析后的 JSON。
func subModeCreatePOST(t *testing.T, handler func(http.ResponseWriter, *http.Request), target, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func TestSubModeFilter(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	// 1.4.27：内容过滤的模式改挂订阅用户身上（通用订阅已退场）。
	u := subUserForToken1427(t, s, tok)

	// 全部（默认）：4 条，中转直连各 2
	u.Mode = "both"
	links := subPlainLinks(t, s, tok)
	if len(links) != 4 {
		t.Fatalf("both 应 4 条，实际 %d：%v", len(links), links)
	}
	// 仅中转：2 条，全带 -中转后缀，不含 -直连
	u.Mode = "relay"
	links = subPlainLinks(t, s, tok)
	if len(links) != 2 {
		t.Fatalf("relay 应 2 条，实际 %d：%v", len(links), links)
	}
	for _, ln := range links {
		if !strings.Contains(ln, "-中转") || strings.Contains(ln, "-直连") {
			t.Fatalf("relay 只应含中转链接：%s", ln)
		}
	}
	// 仅直连：2 条，全带 -直连后缀，不含 -中转
	u.Mode = "direct"
	links = subPlainLinks(t, s, tok)
	if len(links) != 2 {
		t.Fatalf("direct 应 2 条，实际 %d：%v", len(links), links)
	}
	for _, ln := range links {
		if !strings.Contains(ln, "-直连") || strings.Contains(ln, "-中转") {
			t.Fatalf("direct 只应含直连链接：%s", ln)
		}
	}

	// Clash：仅中转时有“中转节点”组、没有“直连节点”组；仅直连时反过来。
	u.Mode = "relay"
	rec := callSub(t, s, tok, "/sub/"+tok+"?format=clash", "")
	if rec.Code != 200 {
		t.Fatalf("clash 应 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name: "中转节点"`) {
		t.Fatalf("relay 的 Clash 应有“中转节点”组：\n%s", body)
	}
	if strings.Contains(body, `name: "直连节点"`) {
		t.Fatalf("relay 的 Clash 不应有“直连节点”组（空组要省略）：\n%s", body)
	}
	u.Mode = "direct"
	rec = callSub(t, s, tok, "/sub/"+tok+"?format=clash", "")
	body = rec.Body.String()
	if !strings.Contains(body, `name: "直连节点"`) {
		t.Fatalf("direct 的 Clash 应有“直连节点”组：\n%s", body)
	}
	if strings.Contains(body, `name: "中转节点"`) {
		t.Fatalf("direct 的 Clash 不应有“中转节点”组（空组要省略）：\n%s", body)
	}
	// ?mode=global（分流模式）与内容过滤互不干扰：仅中转 + global 照样 MATCH,手动选择
	u.Mode = "relay"
	rec = callSub(t, s, tok, "/sub/"+tok+"?format=clash&mode=global", "")
	body = rec.Body.String()
	if !strings.Contains(body, "MATCH,手动选择") || strings.Contains(body, `name: "直连节点"`) {
		t.Fatalf("relay + ?mode=global 应 MATCH,手动选择 且无直连组：\n%s", body)
	}
	// 全部 + split：两个组都在，MATCH,DIRECT
	u.Mode = "both"
	rec = callSub(t, s, tok, "/sub/"+tok+"?format=clash", "")
	body = rec.Body.String()
	if !strings.Contains(body, `name: "中转节点"`) || !strings.Contains(body, `name: "直连节点"`) || !strings.Contains(body, "MATCH,DIRECT") {
		t.Fatalf("both 的 Clash 应两个组都在且 MATCH,DIRECT：\n%s", body)
	}
}

func TestSubModeLegacyDefaultBoth(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	u := subUserForToken1427(t, s, tok)

	// 老记录：订阅用户没存模式（空）→ 按全部处理，已发出去的链接行为不变
	u.Mode = ""
	if links := subPlainLinks(t, s, tok); len(links) != 4 {
		t.Fatalf("没存模式应按全部（4 条），实际 %d：%v", len(links), links)
	}
	// 订阅用户创建时不传模式 → 默认全部，响应里 mode=both
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersCreate(w, r) },
		"/api/v1/subscriptions/users", `{"name":"张三","selection":[{"panel":"local","landing":1},{"panel":"local","landing":2}]}`)
	if code != 200 {
		t.Fatalf("创建应 200，实际 %d %v", code, resp)
	}
	if resp["mode"] != "both" {
		t.Fatalf("不传模式应默认 both，实际 %v", resp)
	}
	url, _ := resp["url"].(string)
	mtok := strings.TrimPrefix(url, "http://example.com/sub/")
	if links := subPlainLinks(t, s, mtok); len(links) != 4 {
		t.Fatalf("默认 both 的订阅用户应 4 条，实际 %d：%v", len(links), links)
	}
	// 老记录：直接写进配置的订阅用户没存模式（空）→ 同样按全部处理
	s2 := newTestSubServer(t)
	tok2 := generateSubToken()
	subUserForToken1427(t, s2, tok2)
	if links := subPlainLinks(t, s2, tok2); len(links) != 4 {
		t.Fatalf("老订阅用户记录（无模式）应按全部（4 条），实际 %d：%v", len(links), links)
	}
	// 1.4.40：旧合并订阅彻底移除——创建接口与记录都不存在，旧令牌拉取
	// /sub/ 时与随机错令牌一样走 404，不再 410。
	tok3 := generateSubToken()
	if rec := callSub(t, s, tok3, "/sub/"+tok3, ""); rec.Code != 404 {
		t.Fatalf("旧合并订阅令牌拉取应 404（记录已删），实际 %d", rec.Code)
	}
}

func TestSubModeCreateAndRotateKeepsMode(t *testing.T) {
	s := newTestSubServer(t)

	// 每人链接：建 relay → 轮换后模式仍是 relay，新令牌只出中转链接
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersCreate(w, r) },
		"/api/v1/subscriptions/users", `{"name":"张三","mode":"relay"}`)
	if code != 200 {
		t.Fatalf("建订阅用户应 200，实际 %d %v", code, resp)
	}
	if resp["mode"] != "relay" {
		t.Fatalf("建用户时 mode 应存 relay，实际 %v", resp)
	}
	uid, _ := resp["id"].(string)
	req := httptest.NewRequest("POST", "/api/v1/subscriptions/users/"+uid+"/rotate", nil)
	req.SetPathValue("id", uid)
	rec := httptest.NewRecorder()
	s.handleSubUsersRotate(rec, req)
	if rec.Code != 200 {
		t.Fatalf("轮换用户应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var rot map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &rot)
	if rot["mode"] != "relay" {
		t.Fatalf("轮换后 mode 应保持 relay，实际 %v", rot)
	}
	url, _ := rot["url"].(string)
	utok := strings.TrimPrefix(url, "http://example.com/sub/")
	links := subPlainLinks(t, s, utok)
	if len(links) != 2 {
		t.Fatalf("轮换后 relay 用户应 2 条中转链接，实际 %d：%v", len(links), links)
	}
	for _, ln := range links {
		if !strings.Contains(ln, "-中转") {
			t.Fatalf("轮换后 relay 用户只应含中转链接：%s", ln)
		}
	}

	// 1.4.40：旧合并订阅彻底移除——创建与轮换接口都不存在（独立合并订阅
	// 的轮换见 TestIndMergedRotateDisableDelete1434）。

	// 1.4.27：通用订阅已退场，生成一律 410。
	code, resp = subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubTokenGenerate(w, r) },
		"/api/v1/settings/subscription/generate", `{"mode":"relay"}`)
	if code != 410 {
		t.Fatalf("生成通用订阅应 410（已退场），实际 %d %v", code, resp)
	}
	code, resp = subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubTokenGenerate(w, r) },
		"/api/v1/settings/subscription/generate", `{"mode":"direct"}`)
	if code != 410 {
		t.Fatalf("再生成通用订阅应 410（已退场），实际 %d %v", code, resp)
	}
}

func TestSubModeInvalidRejected(t *testing.T) {
	s := newTestSubServer(t)
	// 1.4.27：通用订阅已退场，非法模式到不了校验，直接 410；
	// 订阅用户仍在，非法模式 400。
	// 1.4.40：旧合并订阅彻底移除，不再有“合并订阅”这一行。
	cases := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		target  string
		body    string
		want    int
	}{
		{"通用订阅", func(w http.ResponseWriter, r *http.Request) { s.handleSubTokenGenerate(w, r) },
			"/api/v1/settings/subscription/generate", `{"mode":"bogus"}`, 410},
		{"每人链接", func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersCreate(w, r) },
			"/api/v1/subscriptions/users", `{"name":"张三","mode":"bogus"}`, 400},
	}
	for _, c := range cases {
		code, resp := subModeCreatePOST(t, c.handler, c.target, c.body)
		if code != c.want {
			t.Fatalf("%s：应 %d，实际 %d %v", c.name, c.want, code, resp)
		}
	}
}
