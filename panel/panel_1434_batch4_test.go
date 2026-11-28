package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ---------- 1.4.34：面板 UI/UX 重设计第 4 批（合并订阅独立体系） ----------
//
// 新实体 IndependentMergedSub（mrg_ 令牌、/m/ 链接、ims- ID、新切片
// independent_merged_subs、转发 key indmerged:<id>）；旧合并订阅已在
// v1.4.40 彻底移除（记录、接口、退场 410 全删，旧令牌走 404）。前端行为由 jsdom
// test-1434-batch4.js 覆盖。

func TestVersion1434(t *testing.T) {
	if Version != "1.4.41" {
		t.Fatalf("Version 应为 1.4.34，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.41" {
		t.Fatalf("VERSION 文件应为 1.4.34，实际 %q", strings.TrimSpace(string(vb)))
	}
}

// createIndMerged1434 经 API 建一条独立合并订阅，返回 id 与链接。
func createIndMerged1434(t *testing.T, h http.Handler, cookie, body string) (string, string) {
	t.Helper()
	code, resp := postJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged", body)
	if code != 200 {
		t.Fatalf("建独立合并订阅状态=%d want 200：%v", code, resp)
	}
	id, _ := resp["id"].(string)
	url, _ := resp["url"].(string)
	if id == "" || !strings.HasPrefix(id, "ims-") {
		t.Fatalf("新建 id 应为 ims- 前缀，实际 %q", id)
	}
	if !strings.Contains(url, "/m/mrg_") {
		t.Fatalf("新建链接应含 /m/mrg_，实际 %q", url)
	}
	return id, url
}

// fetchIndMerged1434 经路由 GET /m/<token> 拉订阅。
func fetchIndMerged1434(t *testing.T, h http.Handler, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIndMergedCreateEmptyAndFetch1434(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)

	// 空订阅能建（名字选填、Selection 允许空）。
	id, url := createIndMerged1434(t, h, cookie, `{"name":"空订阅"}`)
	// 列表里一行一个订阅：空订阅标注“尚未勾选落地或后端节点”。
	code, resp := getJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged")
	if code != 200 {
		t.Fatalf("列表状态=%d want 200：%v", code, resp)
	}
	list, _ := resp["subs"].([]any)
	if len(list) != 1 {
		t.Fatalf("列表应有 1 条，实际 %d：%v", len(list), resp)
	}
	row, _ := list[0].(map[string]any)
	if row["id"] != id || row["empty"] != true || row["summary"] != "尚未勾选落地或后端节点" {
		t.Fatalf("空订阅行不对：%v", row)
	}
	if _, ok := row["token"]; ok {
		t.Fatalf("列表不应返回令牌：%v", row)
	}
	// 空订阅拉取返回空内容，不 410、不报错。默认 UA 拿 base64 列表：
	// 解码后应是空行（没有节点链接）。
	rec := fetchIndMerged1434(t, h, url)
	if rec.Code != 200 {
		t.Fatalf("空订阅拉取状态=%d want 200：%s", rec.Code, rec.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rec.Body.String()))
	if err != nil {
		t.Fatalf("空订阅内容应是 base64 列表，解码失败：%v（实际 %q）", err, rec.Body.String())
	}
	if strings.TrimSpace(string(decoded)) != "" {
		t.Fatalf("空订阅解码后应为空，实际 %q", string(decoded))
	}
}

func TestOldMergedLinkNow4041434(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()

	// 1.4.40：旧合并订阅彻底移除——旧合并订阅记录（sub_ 令牌）已不存在，
	// 旧令牌拉取 /sub/ 与 /m/ 都与随机错令牌一样走 404，不再 410。
	oldTok := "sub_" + strings.Repeat("a1", 32)
	rec := callSub(t, s, oldTok, "/sub/"+oldTok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("旧链接状态=%d want 404：%s", rec.Code, rec.Body.String())
	}
	// 旧令牌在新端点 /m/ 上同样 404（handleIndMergedSub 只认 mrg_ 令牌）。
	rec = fetchIndMerged1434(t, h, "/m/"+oldTok)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("旧令牌走 /m/ 应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

func TestIndMergedIsolation1434(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)

	id, url := createIndMerged1434(t, h, cookie, `{"name":"隔离测试"}`)
	tok := strings.TrimPrefix(url[strings.LastIndex(url, "/m/")+3:], "")
	_ = id

	// mrg_ 令牌走 /sub/ 也能解析：handleSub 与 /m/ 共用 serveSubContent
	// 的同一套分发（任务指定的顺序：退场→新实体→SubUser→404），路径段
	// 不做二次隔离；规范链接仍是 /m/，旧 sub_ 令牌两条路都 404（记录已删）。
	rec := callSub(t, s, tok, "/sub/"+tok, "")
	if rec.Code != 200 {
		t.Fatalf("mrg_ 令牌走 /sub/ 应 200（共享分发），实际 %d：%s", rec.Code, rec.Body.String())
	}
	// mrg_ 令牌走 /m/ 认（新实体）。
	rec = fetchIndMerged1434(t, h, url)
	if rec.Code != 200 {
		t.Fatalf("mrg_ 令牌走 /m/ 应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// mrg_ 令牌不进旧命名空间：旧 "merged:<id>" key 下没有任何记录
	// （旧合并订阅已在 v1.4.40 彻底移除，userLimitForKey 查不到）。
	if _, _, _, _, _, _, ok := s.userLimitForKey("merged:" + id); ok {
		t.Fatalf("旧 merged:<id> 命名空间下不应有记录")
	}
	if findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, id) == nil {
		t.Fatalf("新记录应在 IndependentMergedSubs 切片里")
	}
	if !validIndependentMergedTokenFormat(tok) || !strings.HasPrefix(tok, "mrg_") {
		t.Fatalf("新令牌格式不对：%q", tok)
	}
	if len(tok) != 68 {
		t.Fatalf("新令牌总长应为 68，实际 %d", len(tok))
	}
	// 令牌格式：sub_ 仍合法、mrg_ 也合法，其他前缀不合法。
	if !validSubTokenFormat("sub_"+strings.Repeat("b2", 32)) || !validAnySubTokenFormat(tok) {
		t.Fatalf("两种前缀都应被接受")
	}
	if validAnySubTokenFormat("xxx_" + strings.Repeat("c3", 32)) {
		t.Fatalf("其他前缀不应被接受")
	}
	// SubUser 登录不受影响：建一个带账号密码的订阅用户，客户端登录能过；
	// mrg_ 令牌当用户名登录不过。注意客户端登录走独立的 clientLoginMux，
	// 不在 routes() 上（routes() 的兜底会回 SPA 页面 200）。
	loginH := s.clientLoginMux()
	pwHash, err := hashPassword("pw1434")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-1434test", Name: "登录测试", Username: "u-test1434", PasswordHash: pwHash})
	code, resp := postJSON144(t, loginH, "", "/api/v1/client/login", `{"username":"u-test1434","password":"pw1434"}`)
	if code != 200 {
		t.Fatalf("SubUser 客户端登录状态=%d want 200：%v", code, resp)
	}
	code, resp = postJSON144(t, loginH, "", "/api/v1/client/login", `{"username":"`+tok+`","password":"pw1434"}`)
	if code != 401 {
		t.Fatalf("mrg_ 令牌当用户名登录应 401，实际 %d：%v", code, resp)
	}
}

func TestIndMergedRotateDisableDelete1434(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)

	id, url := createIndMerged1434(t, h, cookie, `{"name":"轮换测试"}`)
	// 轮换令牌：旧链接立即失效，新链接能拉。
	code, resp := postJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged/"+id+"/rotate", `{}`)
	if code != 200 {
		t.Fatalf("轮换状态=%d want 200：%v", code, resp)
	}
	newURL, _ := resp["url"].(string)
	if newURL == "" || newURL == url {
		t.Fatalf("轮换后应返回一条新链接，实际 %q", newURL)
	}
	rec := fetchIndMerged1434(t, h, url)
	if rec.Code != 404 {
		t.Fatalf("旧链接轮换后应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
	rec = fetchIndMerged1434(t, h, newURL)
	if rec.Code != 200 {
		t.Fatalf("新链接应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 停用：转发 key 命名空间是 indmerged:<id>。
	code, _ = postJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged/"+id+"/disable", `{}`)
	if code != 200 {
		t.Fatalf("停用状态=%d want 200", code)
	}
	if ims := findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, id); ims == nil || !ims.Disabled {
		t.Fatalf("停用后 indmerged:<id> 应为停用态")
	}
	// 恢复。
	code, _ = postJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged/"+id+"/resume", `{}`)
	if code != 200 {
		t.Fatalf("恢复状态=%d want 200", code)
	}
	if ims := findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, id); ims == nil || ims.Disabled {
		t.Fatalf("恢复后 indmerged:<id> 应解除停用")
	}
	// 删除：记录消失、链接失效。
	req := httptest.NewRequest("DELETE", "/api/v1/subscriptions/independent-merged/"+id, nil)
	req.Header.Set("Cookie", cookie)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != 200 {
		t.Fatalf("删除状态=%d want 200：%s", rec2.Code, rec2.Body.String())
	}
	if findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, id) != nil {
		t.Fatalf("删除后记录应消失")
	}
	rec = fetchIndMerged1434(t, h, newURL)
	if rec.Code != 404 {
		t.Fatalf("删除后链接应 404，实际 %d", rec.Code)
	}
}

func TestIndMergedForwardKeyNamespace1434(t *testing.T) {
	if got := indMergedSubKey("ims-abc123"); got != "indmerged:ims-abc123" {
		t.Fatalf("转发 key 应为 indmerged:ims-abc123，实际 %q", got)
	}
	// 限流字段走新命名空间，不走旧 merged 命名空间。
	s := newTestServer144(t)
	s.cfg.IndependentMergedSubs = append(s.cfg.IndependentMergedSubs, IndependentMergedSub{
		ID: "ims-abc123", Name: "限流测试", TrafficLimitBytes: 10 * 1024 * 1024, TrafficLimitMode: "hard",
	})
	limit, mode, _, _, _, name, ok := s.userLimitForKey(indMergedSubKey("ims-abc123"))
	if !ok || limit != 10*1024*1024 || mode != "hard" || name != "限流测试" {
		t.Fatalf("userLimitForKey(indmerged:…) 不对：ok=%v limit=%d mode=%q name=%q", ok, limit, mode, name)
	}
	if _, _, _, _, _, _, ok := s.userLimitForKey(userSubKey("merged", "ims-abc123")); ok {
		t.Fatalf("旧 merged:<id> 不应命中新记录的限流")
	}
	// 停用/恢复也走新命名空间。
	s.applyDisableUserForwardsLocked(indMergedSubKey("ims-abc123"), "traffic")
	if s.userDisabledReason(indMergedSubKey("ims-abc123")) != "traffic" {
		t.Fatalf("停用原因应走新命名空间")
	}
	if s.userDisabledReason(userSubKey("merged", "ims-abc123")) == "traffic" {
		t.Fatalf("旧命名空间不应有停用原因")
	}
	if !s.cfg.IndependentMergedSubs[0].Disabled {
		t.Fatalf("停用后新记录的 Disabled 应为 true")
	}
	s.clearUserDisabled(indMergedSubKey("ims-abc123"))
	if s.cfg.IndependentMergedSubs[0].Disabled {
		t.Fatalf("clearUserDisabled 后新命名空间应解除停用")
	}
}

func TestIndMergedGroupReject1434(t *testing.T) {
	s := newTestServer144(t)
	// 方案 A：独立合并订阅不进分组，kind=indmerged 加成员返回错误。
	if _, err := s.validateGroupMembers([]groupMember{{Kind: "indmerged", ID: "ims-abc123"}}); err == nil {
		t.Fatalf("kind=indmerged 加分组应返回错误")
	}
	// 旧 kind 同样被拒（1.4.40：旧合并订阅已移除，validateGroupMembers 只认 user）。
	if _, err := s.validateGroupMembers([]groupMember{{Kind: "merged", ID: "ms-ok1434"}}); err == nil {
		t.Fatalf("kind=merged 加分组应返回错误（旧合并订阅已移除）")
	}
}

func TestIndMergedBackup1434(t *testing.T) {
	s := newTestServer144(t)
	tok := generateIndMergedToken()
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatalf("encryptSubTokenNow: %v", err)
	}
	s.cfg.IndependentMergedSubs = append(s.cfg.IndependentMergedSubs, IndependentMergedSub{
		ID: "ims-bak1434", Name: "备份测试", TokenHash: hashSubToken(tok), TokenEnc: enc,
	})
	// 带令牌备份保留哈希与密文；不带令牌备份去掉。
	with := sanitizeIndMergedSubs(s.cfg.IndependentMergedSubs, true)
	if len(with) != 1 || with[0].TokenHash == "" || with[0].TokenEnc == "" {
		t.Fatalf("带令牌备份应保留哈希与密文：%+v", with)
	}
	without := sanitizeIndMergedSubs(s.cfg.IndependentMergedSubs, false)
	if len(without) != 1 || without[0].TokenHash != "" || without[0].TokenEnc != "" {
		t.Fatalf("不带令牌备份应去掉哈希与密文：%+v", without)
	}
	// 备份结构里没有 GroupID（独立订阅不进分组）。
	var b map[string]any
	raw, _ := json.Marshal(with[0])
	_ = json.Unmarshal(raw, &b)
	if _, ok := b["group_id"]; ok {
		t.Fatalf("独立合并订阅备份不应含 group_id")
	}
	// 令牌成对自检认新实体。
	if bad := s.checkSubTokenPairs(); len(bad) != 0 {
		t.Fatalf("成对自检不应报坏：%v", bad)
	}
	s.cfg.IndependentMergedSubs[0].TokenHash = hashSubToken(generateIndMergedToken())
	if bad := s.checkSubTokenPairs(); len(bad) != 1 || bad[0] != "ims-bak1434" {
		t.Fatalf("改坏哈希后自检应报出 ims-bak1434，实际 %v", bad)
	}
}

func TestIndMergedExpired1434(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)

	// 到期后拉订阅回 410“订阅已过期”（与退场 410 不同文案）。
	id, url := createIndMerged1434(t, h, cookie, `{"name":"过期测试","expires_at":"2020-01-01"}`)
	_ = id
	rec := fetchIndMerged1434(t, h, url)
	if rec.Code != http.StatusGone {
		t.Fatalf("过期订阅拉取状态=%d want 410：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "订阅已过期") {
		t.Fatalf("过期文案应为“订阅已过期”，实际：%s", rec.Body.String())
	}
}

func TestBatch4Frontend1434(t *testing.T) {
	jsB, err := os.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("读 web/app.js 失败：%v", err)
	}
	htmlB, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 web/index.html 失败：%v", err)
	}
	js, html := string(jsB), string(htmlB)
	// 集中管理页第三个大选项卡。
	if !strings.Contains(html, `id="central-tab-indmerged"`) {
		t.Fatalf("index.html 里没有 central-tab-indmerged")
	}
	if !strings.Contains(html, `id="central-panel-indmerged"`) {
		t.Fatalf("index.html 里没有 central-panel-indmerged")
	}
	if !strings.Contains(html, `id="indmerged-list"`) {
		t.Fatalf("index.html 里没有 indmerged-list")
	}
	// 旧只读列表已撤（v1.4.40）：index.html 里不应再有 central-sub-list。
	if strings.Contains(html, `id="central-sub-list"`) {
		t.Fatalf("index.html 里不应再有 central-sub-list（旧记录只读区已撤）")
	}
	// 选项卡切换调新列表。
	if !strings.Contains(js, "loadIndMergedSubsList") {
		t.Fatalf("app.js 里没有 loadIndMergedSubsList")
	}
	if !strings.Contains(js, "'indmerged'") {
		t.Fatalf("CENTRAL_TABS 里没有 indmerged")
	}
	// 退场提示已删（v1.4.40）：旧函数与旧文案都不应再有；单落地“订阅”
	// 入口改开新建弹窗（带预选）。
	if strings.Contains(js, "showMergedRetiredNotice(") || strings.Contains(js, "旧链接已失效，不能恢复") {
		t.Fatalf("退场提示函数与旧文案应已删掉")
	}
	if !strings.Contains(js, "openIndMergedCreateModal") {
		t.Fatalf("app.js 里没有 openIndMergedCreateModal（新建入口）")
	}
	// 一行一个订阅：序号列＋ID 复制按钮；名字列只放名字＋短 ID，不放链接。
	if !strings.Contains(js, "data-indmerged-copy-id") {
		t.Fatalf("合并订阅表 ID 列没有复制按钮 data-indmerged-copy-id")
	}
	if !strings.Contains(js, "openIndMergedOpsModal") {
		t.Fatalf("app.js 里没有点行弹窗 openIndMergedOpsModal")
	}
}
