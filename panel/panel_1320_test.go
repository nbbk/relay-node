package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1.3.20：订阅到期时间 / 自定义订阅路径 / Logo / 通用文件名 ----------

// TestParseSubExpiry 到期时间解析：空 = 永久；日期 = 当天 23:59:59；
// RFC3339 原样接受；非法日期报错。
func TestParseSubExpiry(t *testing.T) {
	if got, err := parseSubExpiry(""); err != nil || got != "" {
		t.Fatalf("空应是永久（\"\"），实际 %q err=%v", got, err)
	}
	got, err := parseSubExpiry("2027-02-17")
	if err != nil {
		t.Fatalf("日期应能解析：%v", err)
	}
	if !strings.HasPrefix(got, "2027-02-17T23:59:59") {
		t.Fatalf("日期应规范化成当天 23:59:59，实际 %s", got)
	}
	got, err = parseSubExpiry("2027-02-17T10:00:00Z")
	if err != nil || got != "2027-02-17T10:00:00Z" {
		t.Fatalf("RFC3339 应原样接受，实际 %q err=%v", got, err)
	}
	if _, err := parseSubExpiry("2027-13-40"); err == nil {
		t.Fatalf("非法日期应报错")
	}
	if _, err := parseSubExpiry("下个月"); err == nil {
		t.Fatalf("非日期字符串应报错")
	}
}

// TestValidSubPath 自定义订阅路径校验：单段、字母开头、只含字母数字与 - _、
// 1–32 字符；api 与 logo 是保留段。
func TestValidSubPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		ok   bool
	}{
		{"sub", true},
		{"x9f2", true},
		{"a-b_c1", true},
		{"A1", true},
		{"api", false},
		{"logo", false},
		{"1abc", false},
		{"a/b", false},
		{"a b", false},
		{"", false},
		{"a", true},
		{strings.Repeat("a", 32), true},
		{strings.Repeat("a", 33), false},
		{"a.b", false},
	} {
		if got := validSubPath(tc.path); got != tc.ok {
			t.Fatalf("validSubPath(%q) 应是 %v，实际 %v", tc.path, tc.ok, got)
		}
	}
}

// subUserTokenFromURL 从创建返回的订阅链接里抠出令牌。
func subUserTokenFromURL(t *testing.T, url string) string {
	t.Helper()
	i := strings.Index(url, "/sub_")
	if i < 0 {
		t.Fatalf("订阅链接里没有令牌：%s", url)
	}
	tok := url[i+1:]
	if j := strings.IndexAny(tok, "?#"); j >= 0 {
		tok = tok[:j]
	}
	return tok
}

// TestSubExpiredTokenReturns410 过期令牌取订阅应 410（文案“订阅已过期”），
// 而不是 200 空内容或 404。
func TestSubExpiredTokenReturns410(t *testing.T) {
	s := newTestSubServer(t)
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersCreate(w, r) },
		"/api/v1/subscriptions/users", `{"name":"过期的人","mode":"both","expires_at":"2000-01-01"}`)
	if code != 200 {
		t.Fatalf("建订阅用户应 200，实际 %d %v", code, resp)
	}
	url, _ := resp["url"].(string)
	tok := subUserTokenFromURL(t, url)
	rec := callSub(t, s, tok, "/sub/"+tok, "")
	if rec.Code != 410 {
		t.Fatalf("过期令牌应 410，实际 %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "已过期") {
		t.Fatalf("410 文案应说明已过期：%s", rec.Body.String())
	}
}

// TestSubPermanentTokenUnaffected 永久令牌（不填到期时间）取订阅不受影响，仍 200。
func TestSubPermanentTokenUnaffected(t *testing.T) {
	s := newTestSubServer(t)
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersCreate(w, r) },
		"/api/v1/subscriptions/users", `{"name":"永久的人","mode":"both"}`)
	if code != 200 {
		t.Fatalf("建订阅用户应 200，实际 %d %v", code, resp)
	}
	url, _ := resp["url"].(string)
	tok := subUserTokenFromURL(t, url)
	rec := callSub(t, s, tok, "/sub/"+tok, "")
	if rec.Code != 200 {
		t.Fatalf("永久令牌应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// TestSubGenericExpirySetRotateKeepsExpiry 通用订阅：设到期时间后轮换，
// 到期时间不变（轮换只换令牌）；重新复制的链接带 expires_at。
func TestSubGenericExpirySetRotateKeepsExpiry(t *testing.T) {
	s := newTestSubServer(t)
	// 先生成通用订阅
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubTokenGenerate(w, r) },
		"/api/v1/settings/subscription/generate", `{"mode":"both"}`)
	if code != 200 {
		t.Fatalf("生成通用订阅应 200，实际 %d %v", code, resp)
	}
	// 设到期时间 2027-02-17
	code, resp = subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubTokenSetExpiry(w, r) },
		"/api/v1/settings/subscription/expiry", `{"expires_at":"2027-02-17"}`)
	if code != 200 {
		t.Fatalf("设到期时间应 200，实际 %d %v", code, resp)
	}
	exp, _ := resp["expires_at"].(string)
	if !strings.HasPrefix(exp, "2027-02-17T23:59:59") {
		t.Fatalf("到期时间应是 2027-02-17 23:59:59，实际 %s", exp)
	}
	// 轮换：到期时间必须原样不动
	code, _ = subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubTokenGenerate(w, r) },
		"/api/v1/settings/subscription/generate", `{"mode":"both"}`)
	if code != 200 {
		t.Fatalf("轮换应 200，实际 %d", code)
	}
	if !strings.HasPrefix(s.cfg.SubTokenExpiresAt, "2027-02-17T23:59:59") {
		t.Fatalf("轮换后到期时间应不变，实际 %s", s.cfg.SubTokenExpiresAt)
	}
	// 重新复制：链接响应里带 expires_at
	req := httptest.NewRequest("GET", "/api/v1/subscriptions/link?kind=generic", nil)
	rec := httptest.NewRecorder()
	s.handleSubLink(rec, req)
	if rec.Code != 200 {
		t.Fatalf("重新复制应 200，实际 %d", rec.Code)
	}
	var linkResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &linkResp)
	if exp2, _ := linkResp["expires_at"].(string); !strings.HasPrefix(exp2, "2027-02-17T23:59:59") {
		t.Fatalf("重新复制的链接应带到期时间，实际 %v", linkResp)
	}
}

// TestSubUserEditExpiry 编辑订阅用户：不带 expires_at 保持原到期时间；
// 带了就改；带空串就改回永久。令牌始终不动（1.3.19 回归）。
func TestSubUserEditExpiry(t *testing.T) {
	s := newTestSubServer(t)
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersCreate(w, r) },
		"/api/v1/subscriptions/users", `{"name":"改到期的人","mode":"both","expires_at":"2027-02-17"}`)
	if code != 200 {
		t.Fatalf("建订阅用户应 200，实际 %d %v", code, resp)
	}
	uid, _ := resp["id"].(string)
	before := subUserTokenHash(t, s, uid)
	// 不带 expires_at 编辑：到期时间不变，令牌不变
	code, resp = subEditPUT(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersUpdate(w, r) },
		"/api/v1/subscriptions/users/"+uid, uid, `{"name":"改到期的人","mode":"relay","selection":[]}`)
	if code != 200 {
		t.Fatalf("编辑应 200，实际 %d %v", code, resp)
	}
	if exp, _ := resp["expires_at"].(string); !strings.HasPrefix(exp, "2027-02-17T23:59:59") {
		t.Fatalf("不带 expires_at 编辑，到期时间应保持 2027-02-17，实际 %v", resp)
	}
	if subUserTokenHash(t, s, uid) != before {
		t.Fatalf("编辑后令牌应不变")
	}
	// 带 expires_at 改成 2028-01-01
	code, resp = subEditPUT(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersUpdate(w, r) },
		"/api/v1/subscriptions/users/"+uid, uid, `{"name":"改到期的人","mode":"relay","selection":[],"expires_at":"2028-01-01"}`)
	if code != 200 {
		t.Fatalf("改到期时间应 200，实际 %d %v", code, resp)
	}
	if exp, _ := resp["expires_at"].(string); !strings.HasPrefix(exp, "2028-01-01T23:59:59") {
		t.Fatalf("到期时间应改成 2028-01-01，实际 %v", resp)
	}
	// 带空串改回永久
	code, resp = subEditPUT(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersUpdate(w, r) },
		"/api/v1/subscriptions/users/"+uid, uid, `{"name":"改到期的人","mode":"relay","selection":[],"expires_at":""}`)
	if code != 200 {
		t.Fatalf("改回永久应 200，实际 %d %v", code, resp)
	}
	if exp, _ := resp["expires_at"].(string); exp != "" {
		t.Fatalf("改回永久后 expires_at 应为空，实际 %q", exp)
	}
}

// TestSubUsersListReturnsExpiresAt 订阅用户列表行里带 expires_at（前端徽章用）。
func TestSubUsersListReturnsExpiresAt(t *testing.T) {
	s := newTestSubServer(t)
	code, resp := subModeCreatePOST(t, func(w http.ResponseWriter, r *http.Request) { s.handleSubUsersCreate(w, r) },
		"/api/v1/subscriptions/users", `{"name":"列表的人","mode":"both","expires_at":"2027-02-17"}`)
	if code != 200 {
		t.Fatalf("建订阅用户应 200，实际 %d %v", code, resp)
	}
	uid, _ := resp["id"].(string)
	item := subUserListItem(t, s, uid)
	if exp, _ := item["expires_at"].(string); !strings.HasPrefix(exp, "2027-02-17T23:59:59") {
		t.Fatalf("列表行应带 expires_at，实际 %v", item)
	}
}

// TestSubCustomPathRouting 自定义订阅路径：默认 /sub/ 能用；改成 /x9f2/
// 后旧路径 404（不是 SPA 回退的 index.html）、新路径 200。
func TestSubCustomPathRouting(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)
	serve := func(h http.Handler, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// 默认路径：/sub/ 能用
	if rec := serve(s.routes(), "/sub/"+tok); rec.Code != 200 {
		t.Fatalf("默认 /sub/ 应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	// 改路径（直接改配置，模拟 handleSubPathSet 保存成功后的状态；
	// handleSubPathSet 本身调 restartSelf 会重启进程，单元测试里不直接调它）
	s.cfg.SubPath = "x9f2"
	h := s.routes()
	// 旧路径：404，且不是 SPA 回退的 index.html
	rec := serve(h, "/sub/"+tok)
	if rec.Code != 404 {
		t.Fatalf("改路径后旧 /sub/ 应 404，实际 %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `<div id="app"`) {
		t.Fatalf("改路径后旧 /sub/ 不应回退成 index.html（客户端会把 HTML 当订阅）")
	}
	// 新路径：200
	if rec := serve(h, "/x9f2/"+tok); rec.Code != 200 {
		t.Fatalf("新 /x9f2/ 应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// TestSubContentDispositionFileName1322 下载文件名（v1.3.22 起）：订阅文件
// 名称留空时回落面板名；面板名非 ASCII 时走 RFC 5987 filename*，
// ASCII 的 filename= 兜底仍是通用名 subscription.yaml/.txt。
func TestSubContentDispositionFileName1322(t *testing.T) {
	s := newTestSubServer(t)
	s.cfg.PanelName = "CP管理"
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)
	// Clash UA → YAML 文件名：filename* 里是编码后的面板名
	rec := callSub(t, s, tok, "/sub/"+tok+"?format=clash", "Clash")
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename*=UTF-8''CP%E7%AE%A1%E7%90%86.yaml`) || !strings.Contains(cd, `filename="subscription.yaml"`) {
		t.Fatalf("Clash 下载文件名应回落面板名（RFC 5987 + 通用名兜底），实际 %s", cd)
	}
	if strings.Contains(cd, "CP管理") {
		t.Fatalf("下载文件名里不应出现未编码的中文面板名：%s", cd)
	}
	// 其他格式 → txt 文件名
	rec = callSub(t, s, tok, "/sub/"+tok+"?format=plain", "")
	cd = rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename*=UTF-8''CP%E7%AE%A1%E7%90%86.txt`) || !strings.Contains(cd, `filename="subscription.txt"`) {
		t.Fatalf("明文下载文件名应回落面板名、后缀 .txt，实际 %s", cd)
	}
	if strings.Contains(cd, "CP管理") {
		t.Fatalf("下载文件名里不应出现未编码的中文面板名：%s", cd)
	}
}

// testLogoPNG 1x1 透明 PNG（最小合法图片，供上传测试用）。
func testLogoPNG(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8Xw8AAwMCAO+ip1sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testLogoUpload(t *testing.T, s *Server, filename string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("logo", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/settings/logo", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.handleLogoUpload(rec, req)
	return rec
}

// TestLogoUploadDeletePublic Logo 上传/删除/公开读取：上传 PNG 后未登录
// 也能 GET /logo（200、image/png、内容一致）；删掉后 GET 404；
// 非图片上传应 400。
func TestLogoUploadDeletePublic(t *testing.T) {
	s := newTestSubServer(t)
	png := testLogoPNG(t)
	// 未上传前：GET /logo 应 404（前端显示默认样式）
	h := s.routes()
	req := httptest.NewRequest("GET", "/logo", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("没传 Logo 时 GET /logo 应 404，实际 %d", rec.Code)
	}
	// 上传 PNG
	rec = testLogoUpload(t, s, "logo.png", png)
	if rec.Code != 200 {
		t.Fatalf("上传 PNG 应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if !s.logoExists() {
		t.Fatalf("上传后 logoExists 应为真")
	}
	if _, err := os.Stat(filepath.Join(s.env.PanelHome, "logo")); err != nil {
		t.Fatalf("Logo 文件应存在 PANEL_HOME：%v", err)
	}
	// 公开读取：未登录 GET /logo 应 200、image/png、内容一致
	h = s.routes()
	req = httptest.NewRequest("GET", "/logo", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("上传后 GET /logo 应 200，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Logo Content-Type 应是 image/png，实际 %s", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("GET /logo 内容应与上传的一致")
	}
	// 非图片应 400
	rec = testLogoUpload(t, s, "note.txt", []byte("hello, not an image"))
	if rec.Code != 400 {
		t.Fatalf("上传非图片应 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	// 删除
	req = httptest.NewRequest("DELETE", "/api/v1/settings/logo", nil)
	rec = httptest.NewRecorder()
	s.handleLogoDelete(rec, req)
	if rec.Code != 200 {
		t.Fatalf("删除 Logo 应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if s.logoExists() {
		t.Fatalf("删除后 logoExists 应为假")
	}
	h = s.routes()
	req = httptest.NewRequest("GET", "/logo", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("删除后 GET /logo 应 404，实际 %d", rec.Code)
	}
}

// TestSubQRChangesWithURL 二维码随链接变：同一令牌，链接带 ?mode=global
// 与不带的二维码不同；订阅路径改了之后，旧路径链接应被拒绝、新路径链接可用。
func TestSubQRChangesWithURL(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)
	qr := func(url string) (int, string) {
		body, _ := json.Marshal(map[string]string{"url": url})
		req := httptest.NewRequest("POST", "/api/v1/subscription/qr", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleSubQR(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		qrStr, _ := resp["qr"].(string)
		return rec.Code, qrStr
	}
	base := "http://203.0.113.1:11111/sub/" + tok
	code, qr1 := qr(base)
	if code != 200 || qr1 == "" {
		t.Fatalf("默认路径二维码应 200 且有图，实际 %d", code)
	}
	code, qr2 := qr(base + "?mode=global")
	if code != 200 || qr2 == "" {
		t.Fatalf("带 mode 参数的二维码应 200 且有图，实际 %d", code)
	}
	if qr1 == qr2 {
		t.Fatalf("链接变了（带 ?mode=global），二维码应跟着变")
	}
	// 路径改成 x9f2：旧路径链接应被拒绝，新路径链接可用
	s.cfg.SubPath = "x9f2"
	if code, _ := qr(base); code != 400 {
		t.Fatalf("改路径后旧路径链接的二维码应 400，实际 %d", code)
	}
	if code, qr3 := qr("http://203.0.113.1:11111/x9f2/" + tok); code != 200 || qr3 == "" {
		t.Fatalf("新路径链接的二维码应 200 且有图，实际 %d", code)
	}
}
