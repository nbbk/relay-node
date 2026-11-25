package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 1.3.2：订阅对外域名＋合并订阅弹窗轮换/撤销 ----------

func TestValidPublicDomain132(t *testing.T) {
	valid := []string{
		"relay.example.com",
		"sub.example.com:8443",
		"example.com",
		"203.0.113.1",
		"203.0.113.1:11111",
		"a-b.example-domain.com",
	}
	for _, d := range valid {
		if !validPublicDomain(d) {
			t.Fatalf("合法域名 %q 应通过", d)
		}
	}
	invalid := []string{
		"https://example.com",
		"http://example.com",
		"example.com/path",
		"example.com/",
		"example.com?x=1",
		"example.com#frag",
		"exa mple.com",
		"exam_ple.com",
		"-bad.example.com",
		"bad-.example.com",
		"example..com",
		".example.com",
		"example.com.",
		"example.com:",
		"example.com:abc",
		"example.com:0",
		"example.com:65536",
		"example.com:99999",
		`example.com";alert(1);//`,
		`example.com\`,
	}
	for _, d := range invalid {
		if validPublicDomain(d) {
			t.Fatalf("非法域名 %q 应拒绝", d)
		}
	}
}

// TestSubPublicDomainSet132 订阅对外域名：设置后新生成的订阅链接改用
// https://域名/sub/…；非法值 400 且不改动已保存的值；清空后恢复成面板地址。
// TestSubPublicDomainSet132 订阅对外域名：设置后新生成的订阅链接改用
// https://域名/sub/…；非法值 400 且不改动已保存的值；清空后恢复成面板地址。
// （1.4.27：通用订阅已退场，链接改走订阅用户创建回包。）
func TestSubPublicDomainSet132(t *testing.T) {
	s := newTestSubServer(t)
	mkUserURL := func() string {
		t.Helper()
		rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"张三的手机"}`)
		if rec.Code != 200 {
			t.Fatalf("新建订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
		}
		return respMap130(t, rec)["url"].(string)
	}

	// 没填时用面板自己的地址
	u := mkUserURL()
	if !strings.HasPrefix(u, "http://203.0.113.1:11111/sub/sub_") {
		t.Fatalf("没填对外域名时应用面板地址：%s", u)
	}

	// 设置合法域名
	rec := callJSON130(t, s.handleSubPublicDomainSet, "POST", "/api/v1/settings/subscription-domain", `{"domain":"relay.example.com"}`)
	if rec.Code != 200 || respMap130(t, rec)["domain"] != "relay.example.com" {
		t.Fatalf("设置对外域名应 200 且回显：%d %s", rec.Code, rec.Body.String())
	}
	if s.cfg.SubPublicDomain != "relay.example.com" {
		t.Fatalf("配置应已保存对外域名")
	}
	// settings 接口带 public_domain
	rec = callJSON130(t, s.handleSettings, "GET", "/api/v1/settings", "")
	sub, _ := respMap130(t, rec)["subscription"].(map[string]any)
	if sub == nil || sub["public_domain"] != "relay.example.com" {
		t.Fatalf("settings 订阅信息应带 public_domain：%s", rec.Body.String())
	}
	// 之后生成的订阅用户链接改用域名
	u = mkUserURL()
	if !strings.HasPrefix(u, "https://relay.example.com/sub/sub_") {
		t.Fatalf("填了对外域名后链接应用域名开头：%s", u)
	}

	// 非法值 400，旧值不动
	for _, bad := range []string{"https://relay.example.com", "relay.example.com/path", "relay.example.com:abc", "relay.example.com:0"} {
		rec = callJSON130(t, s.handleSubPublicDomainSet, "POST", "/api/v1/settings/subscription-domain", `{"domain":"`+bad+`"}`)
		if rec.Code != 400 {
			t.Fatalf("非法域名 %q 应 400，实际 %d：%s", bad, rec.Code, rec.Body.String())
		}
	}
	if s.cfg.SubPublicDomain != "relay.example.com" {
		t.Fatalf("非法设置不应改动已保存的值")
	}

	// 清空恢复面板地址
	rec = callJSON130(t, s.handleSubPublicDomainSet, "POST", "/api/v1/settings/subscription-domain", `{"domain":""}`)
	if rec.Code != 200 || respMap130(t, rec)["domain"] != "" {
		t.Fatalf("清空对外域名应 200 且回显空：%d %s", rec.Code, rec.Body.String())
	}
	u = mkUserURL()
	if !strings.HasPrefix(u, "http://203.0.113.1:11111/sub/sub_") {
		t.Fatalf("清空后链接应恢复成面板地址：%s", u)
	}
}

// TestMergedSubsRotate132 合并订阅轮换：同一勾选集合与同一归属人换新令牌，
// 旧链接 404、新链接 200，选择与名称不变；不存在的 id 返回 404。
// TestMergedSubsRotate132 1.4.27：合并订阅已退场，创建与轮换一律 410；
// 列表仍可查（供核对）。轮换语义现在由订阅用户承担，见 TestSubUsers130。
func TestMergedSubsRotate132(t *testing.T) {
	s := newTestSubServer(t)
	sel := `[{"panel":"local","landing":1}]`
	rec := callJSON130(t, s.handleMergedSubsCreate, "POST", "/api/v1/subscriptions/merged",
		`{"selection":`+sel+`,"name":"张三"}`)
	if rec.Code != 410 {
		t.Fatalf("生成合并订阅应 410（已退场），实际 %d：%s", rec.Code, rec.Body.String())
	}
	rotate := func(id string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/subscriptions/merged/"+id+"/rotate", nil)
		req.SetPathValue("id", id)
		req.Header.Set("Content-Type", "application/json")
		req.Host = "203.0.113.1:11111"
		rec := httptest.NewRecorder()
		s.handleMergedSubsRotate(rec, req)
		return rec
	}
	if rec := rotate("ms-000000000000"); rec.Code != 410 {
		t.Fatalf("轮换合并订阅应 410（已退场），实际 %d：%s", rec.Code, rec.Body.String())
	}
	rec = callJSON130(t, s.handleMergedSubsList, "GET", "/api/v1/subscriptions/merged", "")
	if rec.Code != 200 {
		t.Fatalf("列合并订阅应 200，实际 %d", rec.Code)
	}
}

// TestMergedSubsRotateRevokes132 轮换后撤销：撤销掉轮换后的令牌，新链接也 404。
// TestMergedSubsRotateRevokes132 1.4.27：合并订阅已退场，创建与轮换一律
// 410；删除接口保留（清理用），删后旧令牌 404。
func TestMergedSubsRotateRevokes132(t *testing.T) {
	s := newTestSubServer(t)
	rec := callJSON130(t, s.handleMergedSubsCreate, "POST", "/api/v1/subscriptions/merged",
		`{"selection":[{"panel":"local","landing":1}]}`)
	if rec.Code != 410 {
		t.Fatalf("生成合并订阅应 410（已退场），实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 直接写一条合并订阅记录（供核对与清理），删掉它。
	tok := generateSubToken()
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: "ms-132", Name: "待删", TokenHash: hashSubToken(tok), TokenEnc: enc, Selection: []SubSelection{{Panel: "local", Landing: 1}}})
	req := httptest.NewRequest("POST", "/api/v1/subscriptions/merged/ms-132/rotate", nil)
	req.SetPathValue("id", "ms-132")
	req.Header.Set("Content-Type", "application/json")
	req.Host = "203.0.113.1:11111"
	rec2 := httptest.NewRecorder()
	s.handleMergedSubsRotate(rec2, req)
	if rec2.Code != 410 {
		t.Fatalf("轮换合并订阅应 410（已退场），实际 %d", rec2.Code)
	}
	del := httptest.NewRequest("DELETE", "/api/v1/subscriptions/merged/ms-132", nil)
	del.SetPathValue("id", "ms-132")
	rec3 := httptest.NewRecorder()
	s.handleMergedSubsDelete(rec3, del)
	if rec3.Code != 200 {
		t.Fatalf("删除合并订阅应 200（清理用），实际 %d", rec3.Code)
	}
	if rec := callSub(t, s, tok, "/sub/"+tok, ""); rec.Code != 404 {
		t.Fatalf("删除后旧链接应 404，实际 %d", rec.Code)
	}
}

// TestWorkerCodePublicDomain132 Worker 代码注释里的示例订阅链接：填了对外
// 域名就用它，没填时保留 <Worker 域名> 占位；ORIGIN 仍是面板真实地址，不受影响。
func TestWorkerCodePublicDomain132(t *testing.T) {
	get := func(s *Server, target string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", target, nil)
		req.Host = "203.0.113.1:11111"
		rec := httptest.NewRecorder()
		s.handleWorkerCode(rec, req)
		return rec
	}
	// 没填对外域名：示例链接是占位，ORIGIN 是面板地址
	s := newTestSubServer(t)
	rec := get(s, "/api/v1/subscriptions/worker")
	code, _ := respMap130(t, rec)["code"].(string)
	if !strings.Contains(code, "https://<Worker 域名>/sub/<令牌>") {
		t.Fatalf("没填对外域名时示例链接应是 <Worker 域名> 占位：\n%s", code)
	}
	if !strings.Contains(code, `const ORIGIN = "http://203.0.113.1:11111";`) {
		t.Fatalf("ORIGIN 应是面板地址：\n%s", code)
	}
	// 填了对外域名：示例链接用域名，ORIGIN 仍是面板地址
	s.cfg.SubPublicDomain = "relay.example.com"
	rec = get(s, "/api/v1/subscriptions/worker")
	code, _ = respMap130(t, rec)["code"].(string)
	if !strings.Contains(code, "https://relay.example.com/sub/<令牌>") {
		t.Fatalf("填了对外域名后示例链接应用域名：\n%s", code)
	}
	if !strings.Contains(code, `const ORIGIN = "http://203.0.113.1:11111";`) {
		t.Fatalf("ORIGIN 不受对外域名影响，仍是面板地址：\n%s", code)
	}
	// 直接调 renderWorkerCode 也一样
	if c := renderWorkerCode("http://203.0.113.1:11111", "relay.example.com", "sub"); !strings.Contains(c, "https://relay.example.com/sub/<令牌>") {
		t.Fatalf("renderWorkerCode 有域名时示例链接应用域名：\n%s", c)
	}
	if c := renderWorkerCode("http://203.0.113.1:11111", "", "sub"); !strings.Contains(c, "https://<Worker 域名>/sub/<令牌>") {
		t.Fatalf("renderWorkerCode 无域名时示例链接应是占位：\n%s", c)
	}
}
