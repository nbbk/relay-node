package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- v1.3.4 ----------

// TestWorkerOriginRejectsIP134 Worker 源站地址明确填 IP 时拒绝（400，
// 提示先建域名再填），填域名放行；没填时默认用当前面板地址（哪怕是 IP）
// 不拦，警告由 handleWorkerCode 另行返回。
func TestWorkerOriginRejectsIP134(t *testing.T) {
	s := newTestSubServer(t)
	_ = s
	for _, origin := range []string{
		"203.0.113.1",
		"203.0.113.1:11111",
		"http://203.0.113.1:11111",
		"https://203.0.113.1",
	} {
		req := httptest.NewRequest("GET", "/api/v1/subscriptions/worker?origin="+origin, nil)
		if _, err := workerOrigin(req); err == nil {
			t.Fatalf("origin=%q 应拒绝，实际放行", origin)
		} else if !strings.Contains(err.Error(), "1003") {
			t.Fatalf("origin=%q 拒绝原因应提到 1003，实际：%v", origin, err)
		}
	}
	// 域名放行
	for _, origin := range []string{"relay.example.com", "http://panel.example.com:11111", "https://panel.example.com"} {
		req := httptest.NewRequest("GET", "/api/v1/subscriptions/worker?origin="+origin, nil)
		o, err := workerOrigin(req)
		if err != nil {
			t.Fatalf("origin=%q 应放行，实际：%v", origin, err)
		}
		if !strings.HasPrefix(o, "http") || !strings.Contains(o, "example.com") {
			t.Fatalf("origin=%q 返回不对：%q", origin, o)
		}
	}
	// 没填：默认用当前面板地址，哪怕是 IP 也不拦（默认用法）
	req := httptest.NewRequest("GET", "/api/v1/subscriptions/worker", nil)
	req.Host = "203.0.113.1:11111"
	o, err := workerOrigin(req)
	if err != nil || o != "http://203.0.113.1:11111" {
		t.Fatalf("默认源站应是当前面板地址，实际 origin=%q err=%v", o, err)
	}
}

// TestWorkerCodeIPWarning134 默认源站是 IP 时代码照常生成，但返回里带
// warning、代码注释里也写明会 1003；源站是域名时没有 warning。
func TestWorkerCodeIPWarning134(t *testing.T) {
	s := newTestSubServer(t)
	call := func(host, origin string) map[string]any {
		t.Helper()
		target := "/api/v1/subscriptions/worker"
		if origin != "" {
			target += "?origin=" + origin
		}
		req := httptest.NewRequest("GET", target, nil)
		if host != "" {
			req.Host = host
		}
		rec := httptest.NewRecorder()
		s.handleWorkerCode(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET %s 应 200，实际 %d：%s", target, rec.Code, rec.Body.String())
		}
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp
	}
	d := call("203.0.113.1:11111", "")
	warn, _ := d["warning"].(string)
	if !strings.Contains(warn, "1003") {
		t.Fatalf("默认源站是 IP 时应带 1003 警告，实际：%v", d)
	}
	code, _ := d["code"].(string)
	if !strings.Contains(code, "1003") || !strings.Contains(code, `const ORIGIN = "http://203.0.113.1:11111"`) {
		t.Fatalf("IP 源站的代码注释里应写明 1003：\n%s", code)
	}
	d = call("panel.example.com:11111", "")
	if _, has := d["warning"]; has {
		t.Fatalf("源站是域名时不应带警告，实际：%v", d)
	}
	// 明确填 IP → 400
	req := httptest.NewRequest("GET", "/api/v1/subscriptions/worker?origin=http://203.0.113.1:11111", nil)
	rec := httptest.NewRecorder()
	s.handleWorkerCode(rec, req)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "1003") {
		t.Fatalf("明确填 IP 应 400 并提示 1003，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestPanelNameDefault134 新面板默认名是 CP管理；设置接口下发 panel_name。
func TestPanelNameDefault134(t *testing.T) {
	s := newTestSubServer(t)
	if s.panelName() != "CP管理" {
		t.Fatalf("默认面板名应是 CP管理，实际 %q", s.panelName())
	}
	req := httptest.NewRequest("GET", "/api/v1/settings", nil)
	rec := httptest.NewRecorder()
	s.handleSettings(rec, req)
	if rec.Code != 200 {
		t.Fatalf("设置接口应 200，实际 %d", rec.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["panel_name"] != "CP管理" {
		t.Fatalf("设置里的 panel_name 应是 CP管理，实际：%v", resp)
	}
}

// TestPanelNameSet134 设置页改面板名称：合法名保存成功（设置接口跟着变），
// 非法名（空、含 /、超 24 字、以 . 开头）返回 400。
func TestPanelNameSet134(t *testing.T) {
	s := newTestSubServer(t)
	set := func(name string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/settings/panel-name", strings.NewReader(`{"name":`+strconv_Quote134(name)+`}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handlePanelNameSet(rec, req)
		return rec
	}
	rec := set("我的面板")
	if rec.Code != 200 {
		t.Fatalf("改名应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if s.panelName() != "我的面板" {
		t.Fatalf("改名后 panelName() 应是 我的面板，实际 %q", s.panelName())
	}
	for _, bad := range []string{"", "a/b", "a\\b", "a:b", ".hidden", strings.Repeat("字", 25)} {
		rec := set(bad)
		if rec.Code != 400 {
			t.Fatalf("名字 %q 应 400，实际 %d：%s", bad, rec.Code, rec.Body.String())
		}
	}
	// 改名后设置接口下发新名
	req := httptest.NewRequest("GET", "/api/v1/settings", nil)
	rec2 := httptest.NewRecorder()
	s.handleSettings(rec2, req)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp["panel_name"] != "我的面板" {
		t.Fatalf("设置里的 panel_name 应跟着改成 我的面板，实际：%v", resp)
	}
}

func strconv_Quote134(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestSubContentDisposition134 订阅响应带 Content-Disposition 文件名：
// v1.3.22 起文件名用订阅文件名称设置，留空回落面板名；ASCII 面板名直接
// filename=，非 ASCII 走 RFC 5987 filename* + 通用名 filename= 兜底；
// 设了文件名后改面板名不再影响文件名；单面板与合并订阅都一样。
func TestSubContentDisposition134(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)
	// 默认：文件名留空 → 回落面板名 CP管理（非 ASCII）：filename* 带编码后的
	// 面板名，ASCII 兜底仍是通用名 subscription.yaml
	rec := callSub(t, s, tok, "/sub/"+tok+"?format=clash", "Clash")
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename*=UTF-8''CP%E7%AE%A1%E7%90%86.yaml`) || !strings.Contains(cd, `filename="subscription.yaml"`) {
		t.Fatalf("默认文件名应回落面板名（RFC 5987 + 通用名兜底），实际：%q", cd)
	}
	if strings.Contains(cd, "CP管理") {
		t.Fatalf("文件名里不应出现未编码的中文面板名，实际：%q", cd)
	}
	// base64 → 后缀 .txt
	rec = callSub(t, s, tok, "/sub/"+tok+"?format=base64", "")
	cd = rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename*=UTF-8''CP%E7%AE%A1%E7%90%86.txt`) || !strings.Contains(cd, `filename="subscription.txt"`) {
		t.Fatalf("base64 订阅的文件名后缀应是 .txt，实际：%q", cd)
	}
	// 面板名改成 ASCII 后文件名跟着面板名走（留空回落）
	s.cfg.PanelName = "MyPanel"
	rec = callSub(t, s, tok, "/sub/"+tok+"?format=clash", "Clash")
	cd = rec.Header().Get("Content-Disposition")
	if cd != `attachment; filename="MyPanel.yaml"` {
		t.Fatalf("ASCII 面板名时文件名应是面板名，实际：%q", cd)
	}
	// 设了订阅文件名称后，改面板名不再影响文件名
	s.cfg.SubFileName = "我的订阅"
	s.cfg.PanelName = "OtherPanel"
	rec = callSub(t, s, tok, "/sub/"+tok+"?format=clash", "Clash")
	cd = rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename*=UTF-8''%E6%88%91%E7%9A%84%E8%AE%A2%E9%98%85.yaml`) || strings.Contains(cd, "OtherPanel") {
		t.Fatalf("设了文件名后改面板名不应影响文件名，实际：%q", cd)
	}
	// 合并订阅同样走订阅文件名称（此时已设“我的订阅”）
	req := httptest.NewRequest("POST", "/api/v1/subscriptions/merged", strings.NewReader(`{"selection":[{"panel":"local","landing":1}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	s.handleMergedSubsCreate(rec2, req)
	if rec2.Code != 200 {
		t.Fatalf("合并订阅生成应 200，实际 %d：%s", rec2.Code, rec2.Body.String())
	}
	var merged map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &merged)
	murl, _ := merged["url"].(string)
	mtok := strings.TrimPrefix(murl, "http://example.com/sub/")
	rec = callSub(t, s, mtok, "/sub/"+mtok+"?format=clash", "Clash")
	if rec.Code != 200 {
		t.Fatalf("合并订阅应 200，实际 %d", rec.Code)
	}
	cd = rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename*=UTF-8''%E6%88%91%E7%9A%84%E8%AE%A2%E9%98%85.yaml`) {
		t.Fatalf("合并订阅也应用订阅文件名称，实际：%q", cd)
	}
}

// TestFetchRemoteStatusTraffic134 集中管理快照里带每落地的流量、到期与子节点：
// 卡片汇总与详情弹窗就靠这些字段，不用另拉接口。
func TestFetchRemoteStatusTraffic134(t *testing.T) {
	srv, _ := newFakeRemote(t)
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	rem := RemotePanel{ID: "rm-test", Name: "测试远程", BaseURL: srv.URL, Token: tok}
	snap, err := s.fetchRemoteStatus(rem)
	if err != nil {
		t.Fatalf("fetchRemoteStatus: %v", err)
	}
	l := snap.Landings[0]
	tr := l.Traffic
	if tr.TodayUp != 100 || tr.TodayDown != 200 || tr.MonthUp != 1000 || tr.TotalDown != 20000 {
		t.Fatalf("落地流量解析不对：%+v", tr)
	}
	if tr.LimitBytes != 300000 || tr.UsedBytes != 30000 || tr.Pct != 10 {
		t.Fatalf("落地流量限制解析不对：%+v", tr)
	}
	if l.DaysLeft == nil || *l.DaysLeft != 30 {
		t.Fatalf("落地到期天数应是 30，实际：%v", l.DaysLeft)
	}
	if l.Expiry != "2027-01-01T00:00:00Z" {
		t.Fatalf("落地到期日不对：%q", l.Expiry)
	}
	if len(l.SubNodes) != 1 || l.SubNodes[0].Domain != "node1.example.com" {
		t.Fatalf("子节点解析不对：%v", l.SubNodes)
	}
}

// TestClashGroupOrder134 策略组顺序（v1.3.4）：自动选择排第一，GLOBAL
// 排第二，之后是手动选择、中转、直连、社交、流媒体；规则指向：TikTok、
// 社交、流媒体三组都直接指向自动选择（1.3.23 起从手动选择/各自的组改过来），
// MATCH 仍是分流 MATCH,DIRECT。
// 单面板与合并订阅共用同一份渲染逻辑，两边都验。
func TestClashGroupOrder134(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)
	checkOrder := func(body string) {
		t.Helper()
		want := []string{"自动选择", "GLOBAL", "手动选择", "中转节点", "直连节点", "社交", "流媒体"}
		prev := -1
		for _, g := range want {
			i := strings.Index(body, "- name: \""+g+"\"")
			if i < 0 {
				t.Fatalf("缺策略组 %q：\n%s", g, body)
			}
			if i <= prev {
				t.Fatalf("策略组 %q 顺序不对（应为 自动选择/GLOBAL/手动选择/中转/直连/社交/流媒体）", g)
			}
			prev = i
		}
		if !strings.Contains(body, "MATCH,DIRECT") {
			t.Fatalf("默认仍应是分流 MATCH,DIRECT：\n%s", body)
		}
		// 规则指向：TikTok、社交、流媒体三组都直接指向自动选择
		for _, r := range []string{"DOMAIN-SUFFIX,tiktok.com,自动选择", "DOMAIN-SUFFIX,instagram.com,自动选择", "DOMAIN-SUFFIX,netflix.com,自动选择"} {
			if !strings.Contains(body, r) {
				t.Fatalf("规则应指向自动选择，缺 %q", r)
			}
		}
	}
	rec := callSub(t, s, tok, "/sub/"+tok+"?format=clash", "")
	if rec.Code != 200 {
		t.Fatalf("clash 应 200，实际 %d", rec.Code)
	}
	checkOrder(rec.Body.String())
	// 合并订阅
	req := httptest.NewRequest("POST", "/api/v1/subscriptions/merged", strings.NewReader(`{"selection":[{"panel":"local","landing":1}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	s.handleMergedSubsCreate(rec2, req)
	if rec2.Code != 200 {
		t.Fatalf("合并订阅生成应 200，实际 %d：%s", rec2.Code, rec2.Body.String())
	}
	var merged map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &merged)
	murl, _ := merged["url"].(string)
	mtok := strings.TrimPrefix(murl, "http://example.com/sub/")
	rec = callSub(t, s, mtok, "/sub/"+mtok+"?format=clash", "")
	if rec.Code != 200 {
		t.Fatalf("合并订阅 clash 应 200，实际 %d", rec.Code)
	}
	checkOrder(rec.Body.String())
}

// TestServeIndexPanelName134 首页按当前面板名称渲染：默认 CP管理；
// 改名后标题/品牌/manifest 都跟着变，占位符不残留。
func TestServeIndexPanelName134(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	get := func() string {
		t.Helper()
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("/ 应 200，实际 %d", rec.Code)
		}
		return rec.Body.String()
	}
	body := get()
	if !strings.Contains(body, "CP管理") {
		t.Fatalf("默认首页应含 CP管理")
	}
	if strings.Contains(body, "__PANEL_NAME") {
		t.Fatalf("首页不应残留面板名称占位符")
	}
	s.cfg.PanelName = "我的面板"
	if err := s.saveConfig(); err != nil {
		t.Fatalf("保存配置失败：%v", err)
	}
	body = get()
	if !strings.Contains(body, "<title>我的面板</title>") {
		t.Fatalf("改名后标题应是 我的面板")
	}
	if !strings.Contains(body, `<div class="brand">我的面板</div>`) {
		t.Fatalf("改名后登录页品牌应是 我的面板")
	}
	if strings.Contains(body, "__PANEL_NAME") {
		t.Fatalf("改名后首页不应残留面板名称占位符")
	}
	// manifest 里的名字是 URL 百分号编码（浏览器解码后读到的就是原名）
	if !strings.Contains(body, "%E6%88%91%E7%9A%84%E9%9D%A2%E6%9D%BF") {
		t.Fatalf("manifest 里应有百分号编码的面板名")
	}
}
