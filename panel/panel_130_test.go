package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ---------- 1.3.0：订阅防封——一人一条订阅链接＋Worker 反代代码 ----------

// callJSON130 直接调一个会话认证 handler（测试里不走 requireAuth 中间件，
// 与已有测试调 handleAddLanding 的做法一致），返回 recorder。
func callJSON130(t *testing.T, h func(w http.ResponseWriter, r *http.Request), method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Host = "203.0.113.1:11111"
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// respMap130 把 handler 返回的 JSON 解成 map（okResp 信封）。
func respMap130(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	return m
}

// tokenFromSubURL130 从 /sub/<令牌> 链接里取出令牌。
func tokenFromSubURL130(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("订阅链接解析失败：%v", err)
	}
	tok := strings.TrimPrefix(u.Path, "/sub/")
	if !strings.HasPrefix(tok, "sub_") {
		t.Fatalf("订阅链接里没有 sub_ 令牌：%s", raw)
	}
	return tok
}

func TestSubUsers130(t *testing.T) {
	s := newTestSubServer(t)

	// 旧的面板级通用令牌保留：先生成一个，后面每人令牌建好后它仍能用
	panelTok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(panelTok)

	// 新建两人
	rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"张三的手机","note":"备用机"}`)
	if rec.Code != 200 {
		t.Fatalf("新建订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	urlA := respMap130(t, rec)["url"].(string)
	tokA := tokenFromSubURL130(t, urlA)
	rec = callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"李四"}`)
	if rec.Code != 200 {
		t.Fatalf("新建第二个订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	urlB := respMap130(t, rec)["url"].(string)
	tokB := tokenFromSubURL130(t, urlB)
	if tokA == tokB {
		t.Fatalf("两人令牌不应相同")
	}
	// 只存哈希，不存明文
	if len(s.cfg.SubUsers) != 2 || s.cfg.SubUsers[0].TokenHash == tokA || s.cfg.SubUsers[0].TokenHash != hashSubToken(tokA) {
		t.Fatalf("订阅用户令牌应只存哈希：%+v", s.cfg.SubUsers)
	}

	// 两人链接都能用，错令牌 404
	if rec := callSub(t, s, tokA, "/sub/"+tokA, ""); rec.Code != 200 {
		t.Fatalf("张三的链接应 200，实际 %d", rec.Code)
	}
	if rec := callSub(t, s, tokB, "/sub/"+tokB, ""); rec.Code != 200 {
		t.Fatalf("李四的链接应 200，实际 %d", rec.Code)
	}
	if rec := callSub(t, s, "sub_0000000000000000000000000000000000000000000000000000000000000000", "/sub/x", ""); rec.Code != 404 {
		t.Fatalf("错令牌应 404，实际 %d", rec.Code)
	}
	// 旧通用令牌照常
	if rec := callSub(t, s, panelTok, "/sub/"+panelTok, ""); rec.Code != 200 {
		t.Fatalf("旧通用订阅链接应仍 200，实际 %d", rec.Code)
	}
	// 每人链接的 format/mode 参数都生效
	clash := callSub(t, s, tokA, "/sub/"+tokA+"?format=clash&mode=global", "")
	if clash.Code != 200 || !strings.Contains(clash.Body.String(), "proxy-groups:") || !strings.Contains(clash.Body.String(), "MATCH,手动选择") {
		t.Fatalf("张三的 clash+global 应出策略组与 MATCH,手动选择：%d\n%s", clash.Code, clash.Body.String())
	}
	b64 := callSub(t, s, tokA, "/sub/"+tokA+"?format=base64", "")
	if b64.Code != 200 || strings.Contains(b64.Body.String(), "proxy-groups:") {
		t.Fatalf("base64 订阅不应含策略组：%d\n%s", b64.Code, b64.Body.String())
	}
	if rec := callSub(t, s, tokA, "/sub/"+tokA+"?mode=bogus", ""); rec.Code != 400 {
		t.Fatalf("非法 mode 应 400，实际 %d", rec.Code)
	}

	// 列表含两人，不含令牌明文与哈希
	rec = callJSON130(t, s.handleSubUsersList, "GET", "/api/v1/subscriptions/users", "")
	if rec.Code != 200 {
		t.Fatalf("列订阅用户应 200，实际 %d", rec.Code)
	}
	listBody := rec.Body.String()
	for _, want := range []string{"张三的手机", "李四"} {
		if !strings.Contains(listBody, want) {
			t.Fatalf("列表缺 %q：%s", want, listBody)
		}
	}
	if strings.Contains(listBody, tokA) || strings.Contains(listBody, tokB) || strings.Contains(listBody, "token_hash") {
		t.Fatalf("列表不应泄露令牌明文与哈希：%s", listBody)
	}

	// 名字校验：空名 400，超长 400
	if rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"  "}`); rec.Code != 400 {
		t.Fatalf("空名字应 400，实际 %d", rec.Code)
	}
	if rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"`+strings.Repeat("名", 65)+`"}`); rec.Code != 400 {
		t.Fatalf("超长名字应 400，实际 %d", rec.Code)
	}

	// 轮换 A：旧 A 失效，新 A 能用，B 照常
	idA := s.cfg.SubUsers[0].ID
	req := httptest.NewRequest("POST", "/api/v1/subscriptions/users/"+idA+"/rotate", nil)
	req.SetPathValue("id", idA)
	reqRec := httptest.NewRecorder()
	s.handleSubUsersRotate(reqRec, req)
	if reqRec.Code != 200 {
		t.Fatalf("轮换应 200，实际 %d：%s", reqRec.Code, reqRec.Body.String())
	}
	tokA2 := tokenFromSubURL130(t, respMap130(t, reqRec)["url"].(string))
	if rec := callSub(t, s, tokA, "/sub/"+tokA, ""); rec.Code != 404 {
		t.Fatalf("轮换后旧 A 令牌应 404，实际 %d", rec.Code)
	}
	if rec := callSub(t, s, tokA2, "/sub/"+tokA2, ""); rec.Code != 200 {
		t.Fatalf("轮换后新 A 令牌应 200，实际 %d", rec.Code)
	}
	if rec := callSub(t, s, tokB, "/sub/"+tokB, ""); rec.Code != 200 {
		t.Fatalf("轮换 A 不应影响 B，实际 %d", rec.Code)
	}

	// 撤销 B：B 失效，A 照常
	idB := s.cfg.SubUsers[1].ID
	req = httptest.NewRequest("DELETE", "/api/v1/subscriptions/users/"+idB, nil)
	req.SetPathValue("id", idB)
	reqRec = httptest.NewRecorder()
	s.handleSubUsersDelete(reqRec, req)
	if reqRec.Code != 200 {
		t.Fatalf("撤销应 200，实际 %d：%s", reqRec.Code, reqRec.Body.String())
	}
	if rec := callSub(t, s, tokB, "/sub/"+tokB, ""); rec.Code != 404 {
		t.Fatalf("撤销后 B 令牌应 404，实际 %d", rec.Code)
	}
	if rec := callSub(t, s, tokA2, "/sub/"+tokA2, ""); rec.Code != 200 {
		t.Fatalf("撤销 B 不应影响 A，实际 %d", rec.Code)
	}
	if rec := callSub(t, s, panelTok, "/sub/"+panelTok, ""); rec.Code != 200 {
		t.Fatalf("撤销 B 不应影响旧通用链接，实际 %d", rec.Code)
	}
}

// TestSubUserMerged130 订阅用户带勾选集合 = 合并订阅：只导选中的落地，
// 勾选不存在的落地应 400。
func TestSubUserMerged130(t *testing.T) {
	s := newTestSubServer(t)
	rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users",
		`{"name":"只导智利","selection":[{"panel":"local","landing":1}]}`)
	if rec.Code != 200 {
		t.Fatalf("带勾选新建应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	tok := tokenFromSubURL130(t, respMap130(t, rec)["url"].(string))
	body := callSub(t, s, tok, "/sub/"+tok+"?format=plain", "").Body.String()
	for _, want := range []string{"智利-中转", "智利-直连"} {
		if !strings.Contains(body, want) {
			t.Fatalf("合并订阅用户应含 %s：\n%s", want, body)
		}
	}
	for _, no := range []string{"test-中转", "test-直连"} {
		if strings.Contains(body, no) {
			t.Fatalf("合并订阅用户不应含落地 2 的 %s：\n%s", no, body)
		}
	}
	rec = callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users",
		`{"name":"不存在的落地","selection":[{"panel":"local","landing":99}]}`)
	if rec.Code != 400 {
		t.Fatalf("勾选不存在的落地应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestMergedSubName130 合并订阅填名称：同一勾选集合可有多个不同名称的令牌，
// 各归各人；不填名称走老逻辑（同一选择重新生成会轮换），且轮换不能误伤已命名的那条。
func TestMergedSubName130(t *testing.T) {
	s := newTestSubServer(t)
	sel := `[{"panel":"local","landing":1}]`
	mk := func(name string) string {
		t.Helper()
		rec := callJSON130(t, s.handleMergedSubsCreate, "POST", "/api/v1/subscriptions/merged",
			`{"selection":`+sel+`,"name":"`+name+`"}`)
		if rec.Code != 200 {
			t.Fatalf("生成合并订阅（name=%q）应 200，实际 %d：%s", name, rec.Code, rec.Body.String())
		}
		return tokenFromSubURL130(t, respMap130(t, rec)["url"].(string))
	}
	tokZhang := mk("张三")
	tokLi := mk("李四")
	if tokZhang == tokLi {
		t.Fatalf("不同名称应是不同令牌")
	}
	if rec := callSub(t, s, tokZhang, "/sub/"+tokZhang, ""); rec.Code != 200 {
		t.Fatalf("张三的合并订阅应 200，实际 %d", rec.Code)
	}
	if rec := callSub(t, s, tokLi, "/sub/"+tokLi, ""); rec.Code != 200 {
		t.Fatalf("李四的合并订阅应 200，实际 %d", rec.Code)
	}
	// 不填名称生成同一选择：新起一条（不轮换已命名的），两条命名的照常
	rec := callJSON130(t, s.handleMergedSubsCreate, "POST", "/api/v1/subscriptions/merged", `{"selection":`+sel+`}`)
	if rec.Code != 200 {
		t.Fatalf("不填名称生成应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if respMap130(t, rec)["rotated"] == true {
		t.Fatalf("第一次不填名称生成不应是轮换")
	}
	tokPlain := tokenFromSubURL130(t, respMap130(t, rec)["url"].(string))
	if rec := callSub(t, s, tokZhang, "/sub/"+tokZhang, ""); rec.Code != 200 {
		t.Fatalf("不填名称生成不应影响张三的链接，实际 %d", rec.Code)
	}
	// 再不填名称生成同一选择：轮换的是没名称的那条，命名的两条照常
	rec = callJSON130(t, s.handleMergedSubsCreate, "POST", "/api/v1/subscriptions/merged", `{"selection":`+sel+`}`)
	if respMap130(t, rec)["rotated"] != true {
		t.Fatalf("第二次不填名称生成应是轮换")
	}
	if rec := callSub(t, s, tokPlain, "/sub/"+tokPlain, ""); rec.Code != 404 {
		t.Fatalf("轮换后旧的无名链接应 404，实际 %d", rec.Code)
	}
	if rec := callSub(t, s, tokZhang, "/sub/"+tokZhang, ""); rec.Code != 200 {
		t.Fatalf("轮换无名链接不应影响张三，实际 %d", rec.Code)
	}
	if rec := callSub(t, s, tokLi, "/sub/"+tokLi, ""); rec.Code != 200 {
		t.Fatalf("轮换无名链接不应影响李四，实际 %d", rec.Code)
	}
	// 列表含名称
	rec = callJSON130(t, s.handleMergedSubsList, "GET", "/api/v1/subscriptions/merged", "")
	if !strings.Contains(rec.Body.String(), "张三") || !strings.Contains(rec.Body.String(), "李四") {
		t.Fatalf("合并订阅列表应含名称：%s", rec.Body.String())
	}
}

// TestWorkerCode130 Worker 反代代码生成：含源站地址与转发逻辑；
// origin 参数校验（带路径、注入字符应 400）。
func TestWorkerCode130(t *testing.T) {
	s := newTestSubServer(t)
	get := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", target, nil)
		req.Host = "203.0.113.1:11111"
		rec := httptest.NewRecorder()
		s.handleWorkerCode(rec, req)
		return rec
	}
	// 缺省：用当前打开面板的地址
	rec := get("/api/v1/subscriptions/worker")
	if rec.Code != 200 {
		t.Fatalf("生成 Worker 代码应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	m := respMap130(t, rec)
	code, _ := m["code"].(string)
	for _, want := range []string{
		`const ORIGIN = "http://203.0.113.1:11111";`,
		`url.pathname.startsWith("/sub/")`,
		`ORIGIN + url.pathname + url.search`,
		`"User-Agent"`,
		`return new Response(res.body, { status: res.status, headers: res.headers });`,
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("Worker 代码缺 %q：\n%s", want, code)
		}
	}
	if strings.Contains(code, "/api/v1/") || strings.Contains(code, "password") {
		t.Fatalf("Worker 代码不应含面板其他接口与凭据：\n%s", code)
	}
	// origin 参数：完整 https 写法与裸域名写法
	rec = get("/api/v1/subscriptions/worker?origin=" + url.QueryEscape("https://panel.example.com"))
	if rec.Code != 200 || !strings.Contains(respMap130(t, rec)["code"].(string), `const ORIGIN = "https://panel.example.com";`) {
		t.Fatalf("origin=https://panel.example.com 应出对应源站：%d %s", rec.Code, rec.Body.String())
	}
	rec = get("/api/v1/subscriptions/worker?origin=" + url.QueryEscape("panel.example.com:8443"))
	if rec.Code != 200 || !strings.Contains(respMap130(t, rec)["code"].(string), `const ORIGIN = "http://panel.example.com:8443";`) {
		t.Fatalf("origin 裸域名应补 http：%d %s", rec.Code, rec.Body.String())
	}
	// 非法 origin：带路径、带注入字符、带空格
	for _, bad := range []string{
		"https://panel.example.com/sub/x",
		"https://panel.example.com\";alert(1);//",
		"https://exam`ple.com",
		"panel.example.com/foo",
	} {
		rec = get("/api/v1/subscriptions/worker?origin=" + url.QueryEscape(bad))
		if rec.Code != 400 {
			t.Fatalf("非法 origin %q 应 400，实际 %d：%s", bad, rec.Code, rec.Body.String())
		}
	}
}
