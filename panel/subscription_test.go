package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1.2.7：订阅链接 ----------

const subTestRelayConf = `LANDING=203.0.113.1
LANDING_IS_DOMAIN=0
DOMAIN=""
CERT_DOMAIN="example.com"
PRESET=1
MODE_NOTE="tiktok-node（Xray）"
PORTS="tcp:80:80 tcp:443:443"
NAME="智利"
LANDING_COUNT=2
ALL_PORTS="tcp:80:80 tcp:443:443"
SHARE80=0
SHARE443=1
LANDING_2=203.0.113.2
DOMAIN_2="node2.example.com"
CERT_DOMAIN_2="node2.example.com"
PRESET_2=2
MODE_NOTE_2="sing-box-node TLS 模式"
PORTS_2="tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388"
`

const subTestNodesTxt = `## 落地 1：203.0.113.1
中转：vless://11111111-2222-3333-4444-555555555555@example.com:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转
直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-直连
## 落地 2：203.0.113.2
中转：vless://11111111-2222-3333-4444-555555555555@node2.example.com:443?encryption=none&security=tls&type=ws&host=node2.example.com&path=%2Fexample-path#test-中转
直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.2:443?encryption=none&security=tls&type=ws&host=node2.example.com&path=%2Fexample-path#test-直连
`

func newTestSubServer(t *testing.T) *Server {
	t.Helper()
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir, "nodes.txt"), []byte(subTestNodesTxt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(subTestRelayConf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	return newTestAddLandingServer(t, kitDir, realmDir)
}

func callSub(t *testing.T, s *Server, tok, target string, ua string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	req.SetPathValue("token", tok)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	rec := httptest.NewRecorder()
	s.handleSub(rec, req)
	return rec
}

func TestSubTokenAuth(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)

	// 无令牌 → 404（不是 401，避免暴露端点存在）
	if rec := callSub(t, s, "sub_0000000000000000000000000000000000000000000000000000000000000000", "/sub/x", ""); rec.Code != 404 {
		t.Fatalf("错令牌应 404，实际 %d", rec.Code)
	}
	// 对令牌 → 200，base64 解码后是 4 条链接
	rec := callSub(t, s, tok, "/sub/"+tok, "")
	if rec.Code != 200 {
		t.Fatalf("对令牌应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rec.Body.String()))
	if err != nil {
		t.Fatalf("订阅内容应是标准 base64：%v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("应有 4 条链接，实际 %d：%v", len(lines), lines)
	}
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "vless://") {
			t.Fatalf("链接应以 vless:// 开头：%s", ln)
		}
	}
}

func TestSubFormats(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)

	// plain：明文列表
	rec := callSub(t, s, tok, "/sub/"+tok+"?format=plain", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "vless://") {
		t.Fatalf("plain 应返回明文链接：%d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("plain Content-Type 应是 text/plain，实际 %s", ct)
	}
	// clash：YAML
	rec = callSub(t, s, tok, "/sub/"+tok+"?format=clash", "")
	if rec.Code != 200 {
		t.Fatalf("clash 应 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"proxies:", "type: vless", "servername: \"example.com\"", "path: \"/example-path\"", "proxy-groups:", "name: \"PROXY\"", "MATCH,PROXY"} {
		if !strings.Contains(body, want) {
			t.Fatalf("clash YAML 缺 %q：\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/yaml") {
		t.Fatalf("clash Content-Type 应是 text/yaml，实际 %s", ct)
	}
	// 非法 format → 400
	rec = callSub(t, s, tok, "/sub/"+tok+"?format=bogus", "")
	if rec.Code != 400 {
		t.Fatalf("非法 format 应 400，实际 %d", rec.Code)
	}
}

func TestSubUserAgent(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)
	url := "/sub/" + tok

	cases := []struct {
		ua   string
		yaml bool
	}{
		{"Clash/1.0", true},
		{"ClashMeta/1.2.0", true},
		{"clash-verge/2.0", true},
		{"Stash/2.6.0", true},
		{"FlClash/0.8.0", true},
		{"mihomo/1.18", true},
		{"v2rayNG/1.9.0", false},
		{"Shadowrocket/2.2", false},
		{"", false},
	}
	for _, c := range cases {
		rec := callSub(t, s, tok, url, c.ua)
		if rec.Code != 200 {
			t.Fatalf("UA %q 应 200，实际 %d", c.ua, rec.Code)
		}
		isYAML := strings.Contains(rec.Body.String(), "proxies:")
		if isYAML != c.yaml {
			t.Fatalf("UA %q 应返回 yaml=%v，实际内容：%.80s", c.ua, c.yaml, rec.Body.String())
		}
	}
	// 手动 format 优先于 UA：Clash UA + ?format=plain → 明文
	rec := callSub(t, s, tok, url+"?format=plain", "Clash/1.0")
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "vless://") {
		t.Fatalf("手动 format 应覆盖 UA：%d %s", rec.Code, rec.Body.String())
	}
	// 手动 format=base64 + Clash UA → base64
	rec = callSub(t, s, tok, url+"?format=base64", "Clash/1.0")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "proxies:") {
		t.Fatalf("手动 format=base64 应覆盖 UA：%d %.80s", rec.Code, rec.Body.String())
	}
}

func TestSubTokenRotateRevoke(t *testing.T) {
	s := newTestSubServer(t)
	// 生成
	req := httptest.NewRequest("POST", "/api/v1/settings/subscription/generate", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleSubTokenGenerate(rec, req)
	if rec.Code != 200 {
		t.Fatalf("生成订阅应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	url1, _ := resp["url"].(string)
	if !strings.HasPrefix(url1, "http://") || !strings.Contains(url1, "/sub/sub_") {
		t.Fatalf("生成应返回订阅 URL：%v", resp)
	}
	tok1 := strings.TrimPrefix(url1, "http://example.com/sub/")
	// 旧链接能用
	if r := callSub(t, s, tok1, "/sub/"+tok1, ""); r.Code != 200 {
		t.Fatalf("生成的订阅应能访问：%d", r.Code)
	}
	// 轮换：旧链接失效，新链接能用
	req = httptest.NewRequest("POST", "/api/v1/settings/subscription/generate", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	s.handleSubTokenGenerate(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	url2, _ := resp["url"].(string)
	if url2 == url1 {
		t.Fatalf("轮换应换新链接")
	}
	if resp["rotated"] != true {
		t.Fatalf("轮换应返回 rotated=true：%v", resp)
	}
	tok2 := strings.TrimPrefix(url2, "http://example.com/sub/")
	if r := callSub(t, s, tok1, "/sub/"+tok1, ""); r.Code != 404 {
		t.Fatalf("轮换后旧链接应 404，实际 %d", r.Code)
	}
	if r := callSub(t, s, tok2, "/sub/"+tok2, ""); r.Code != 200 {
		t.Fatalf("轮换后新链接应 200，实际 %d", r.Code)
	}
	// 撤销：订阅 404
	req = httptest.NewRequest("POST", "/api/v1/settings/subscription/revoke", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	s.handleSubTokenRevoke(rec, req)
	if rec.Code != 200 {
		t.Fatalf("撤销应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if r := callSub(t, s, tok2, "/sub/"+tok2, ""); r.Code != 404 {
		t.Fatalf("撤销后订阅应 404，实际 %d", r.Code)
	}
}

func TestClashNameDedup(t *testing.T) {
	links := []subLink{
		{Name: "智利-中转", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转"},
		{Name: "智利-中转", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:24444?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转"},
		{Name: "测试", Link: "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=none&type=ws&path=%2Fexample-path#测试"},
		{Name: "SS节点", Link: "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@203.0.113.1:8388#SS节点"},
	}
	body, ctype, err := renderSubContent(links, "clash")
	if err != nil {
		t.Fatalf("renderSubContent clash 失败：%v", err)
	}
	if !strings.HasPrefix(ctype, "text/yaml") {
		t.Fatalf("ctype 应是 text/yaml，实际 %s", ctype)
	}
	if !strings.Contains(body, "name: \"智利-中转\"") || !strings.Contains(body, "name: \"智利-中转 (2)\"") {
		t.Fatalf("同名 proxy 应去重加后缀：\n%s", body)
	}
	if !strings.Contains(body, "tls: false") {
		t.Fatalf("明文节点应 tls: false：\n%s", body)
	}
	if strings.Contains(body, "SS节点") {
		t.Fatalf("ss 节点在 clash 里应跳过：\n%s", body)
	}
	// 同样的链接在 base64 里都在
	raw, _, err := renderSubContent(links, "base64")
	if err != nil {
		t.Fatalf("base64 失败：%v", err)
	}
	dec, _ := base64.StdEncoding.DecodeString(raw)
	if strings.Count(string(dec), "vless://") != 3 || !strings.Contains(string(dec), "ss://") {
		t.Fatalf("base64 应含全部 4 条链接：%s", dec)
	}
}

func TestMergedSubCreate(t *testing.T) {
	s := newTestSubServer(t)
	call := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/v1/subscriptions/merged", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleMergedSubsCreate(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	// 空选择 → 400
	if code, _ := call(`{"selection":[]}`); code != 400 {
		t.Fatalf("空选择应 400，实际 %d", code)
	}
	// 不存在的落地 → 400
	if code, _ := call(`{"selection":[{"panel":"local","landing":99}]}`); code != 400 {
		t.Fatalf("不存在的落地应 400，实际 %d", code)
	}
	// 不存在的面板 → 400
	if code, _ := call(`{"selection":[{"panel":"rm-nope","landing":1}]}`); code != 400 {
		t.Fatalf("不存在的面板应 400，实际 %d", code)
	}
	// 只选落地 1 → 200，URL 能用且只含落地 1 的两条链接
	code, resp := call(`{"selection":[{"panel":"local","landing":1}]}`)
	if code != 200 {
		t.Fatalf("有效选择应 200，实际 %d %v", code, resp)
	}
	url1, _ := resp["url"].(string)
	tok1 := strings.TrimPrefix(url1, "http://example.com/sub/")
	rec := callSub(t, s, tok1, "/sub/"+tok1+"?format=plain", "")
	if rec.Code != 200 {
		t.Fatalf("合并订阅应 200，实际 %d", rec.Code)
	}
	if strings.Count(rec.Body.String(), "vless://") != 2 || !strings.Contains(rec.Body.String(), "example.com:24443") {
		t.Fatalf("只选落地 1 应只有它的 2 条链接：%s", rec.Body.String())
	}
	// 同一选择再生成 = 轮换：旧链接失效
	_, resp2 := call(`{"selection":[{"panel":"local","landing":1}]}`)
	if resp2["rotated"] != true {
		t.Fatalf("同一选择再生成应 rotated=true：%v", resp2)
	}
	if r := callSub(t, s, tok1, "/sub/"+tok1, ""); r.Code != 404 {
		t.Fatalf("轮换后旧合并链接应 404，实际 %d", r.Code)
	}
	// 不同选择 = 新令牌，旧选择的新链接照常用
	code, resp3 := call(`{"selection":[{"panel":"local","landing":2}]}`)
	if code != 200 {
		t.Fatalf("新选择应 200，实际 %d %v", code, resp3)
	}
	url3, _ := resp3["url"].(string)
	tok3 := strings.TrimPrefix(url3, "http://example.com/sub/")
	url2, _ := resp2["url"].(string)
	tok2 := strings.TrimPrefix(url2, "http://example.com/sub/")
	if r := callSub(t, s, tok2, "/sub/"+tok2, ""); r.Code != 200 {
		t.Fatalf("不同选择的旧链接应继续有效：%d", r.Code)
	}
	if r := callSub(t, s, tok3, "/sub/"+tok3+"?format=plain", ""); !strings.Contains(r.Body.String(), "node2.example.com") {
		t.Fatalf("落地 2 的订阅应含 node2.example.com：%s", r.Body.String())
	}
	// 列表里有两条合并订阅（落地 1 与落地 2），不含链接
	req := httptest.NewRequest("GET", "/api/v1/subscriptions/merged", nil)
	rec2 := httptest.NewRecorder()
	s.handleMergedSubsList(rec2, req)
	var list map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &list)
	subs, _ := list["subs"].([]any)
	if len(subs) != 2 {
		t.Fatalf("应有 2 条合并订阅，实际 %v", list)
	}
	if strings.Contains(rec2.Body.String(), "sub_") {
		t.Fatalf("列表不应泄露令牌：%s", rec2.Body.String())
	}
}

func TestCentralSubscription(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateRemoteToken()
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)
	call := func(tokHeader, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		if tokHeader != "" {
			req.Header.Set("Authorization", "Bearer "+tokHeader)
		}
		rec := httptest.NewRecorder()
		s.requireCentralToken(s.handleCentralSubscription)(rec, req)
		return rec
	}
	// 无令牌 → 401
	if rec := call("", "/api/v1/central/subscription"); rec.Code != 401 {
		t.Fatalf("无令牌应 401，实际 %d", rec.Code)
	}
	// 错令牌 → 401
	if rec := call("rpt_0000000000000000000000000000000000000000000000000000000000000000", "/api/v1/central/subscription"); rec.Code != 401 {
		t.Fatalf("错令牌应 401，实际 %d", rec.Code)
	}
	// 对令牌：全部落地 4 条链接
	rec := call(tok, "/api/v1/central/subscription")
	if rec.Code != 200 {
		t.Fatalf("对令牌应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK    bool      `json:"ok"`
		Links []subLink `json:"links"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.OK || len(resp.Links) != 4 {
		t.Fatalf("应返回 4 条链接，实际 %+v", resp)
	}
	// 只取落地 1：2 条
	rec = call(tok, "/api/v1/central/subscription?landings=1")
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Links) != 2 {
		t.Fatalf("landings=1 应返回 2 条链接，实际 %+v", resp)
	}
	// 非法参数 → 400
	if rec := call(tok, "/api/v1/central/subscription?landings=abc"); rec.Code != 400 {
		t.Fatalf("非法 landings 应 400，实际 %d", rec.Code)
	}
}

func TestMergedSubFetchRemote(t *testing.T) {
	s := newTestSubServer(t)
	// 假远程面板：status 与 subscription 都返回固定内容
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/central/status":
			_, _ = w.Write([]byte(`{"ok":true,"panel_version":"1.2.7","realm":{"running":true},"disk_pct":10,"landings":[{"index":1,"landing":"203.0.113.9","domain":"","mode":"standard","ports":[],"online":true,"latency_ms":100}]}`))
		case "/api/v1/central/subscription":
			_, _ = w.Write([]byte(`{"ok":true,"links":[{"name":"远程-中转","link":"vless://11111111-2222-3333-4444-555555555555@remote.example.com:443?security=tls&sni=remote.example.com#远程-中转"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	s.cfg.Remotes = append(s.cfg.Remotes, RemotePanel{ID: "rm-1", Name: "远程-01", BaseURL: remote.URL, Token: "rpt_test"})

	// 生成含本面板落地 1 + 远程落地 1 的合并订阅
	req := httptest.NewRequest("POST", "/api/v1/subscriptions/merged", strings.NewReader(`{"selection":[{"panel":"local","landing":1},{"panel":"rm-1","landing":1}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleMergedSubsCreate(rec, req)
	if rec.Code != 200 {
		t.Fatalf("生成应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	url, _ := resp["url"].(string)
	tok := strings.TrimPrefix(url, "http://example.com/sub/")
	// 取订阅：应含本地 2 条 + 远程 1 条
	r2 := callSub(t, s, tok, "/sub/"+tok+"?format=plain", "")
	if r2.Code != 200 {
		t.Fatalf("合并订阅应 200，实际 %d", r2.Code)
	}
	if strings.Count(r2.Body.String(), "vless://") != 3 || !strings.Contains(r2.Body.String(), "remote.example.com") {
		t.Fatalf("合并订阅应含本地 2 条 + 远程 1 条：%s", r2.Body.String())
	}
	// 远程挂了：只剩本地 2 条，订阅照常返回 200
	remote.Close()
	r3 := callSub(t, s, tok, "/sub/"+tok+"?format=plain", "")
	if r3.Code != 200 || strings.Count(r3.Body.String(), "vless://") != 2 {
		t.Fatalf("远程离线时订阅应只剩本地 2 条且 200：%d %s", r3.Code, r3.Body.String())
	}
}

func TestSubUserinfo(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)
	// 没数据时不加头
	rec := callSub(t, s, tok, "/sub/"+tok, "")
	if h := rec.Header().Get("subscription-userinfo"); h != "" {
		t.Fatalf("没数据时不应有 subscription-userinfo 头，实际 %q", h)
	}
	// 补真实数据：落地 1 有流量累计与上限、到期日
	s.stateMu.Lock()
	s.state.Landings["203.0.113.1"] = &LandingExt{TrafficLimitBytes: 100 << 30, Expiry: "2027-02-16T00:00:00Z"}
	s.stateMu.Unlock()
	lt := s.traffic.get("203.0.113.1")
	lt.UpTotal = 123456
	lt.DownTotal = 789012
	rec = callSub(t, s, tok, "/sub/"+tok, "")
	h := rec.Header().Get("subscription-userinfo")
	for _, want := range []string{"upload=123456", "download=789012", "total=107374182400", "expire="} {
		if !strings.Contains(h, want) {
			t.Fatalf("userinfo 头缺 %q，实际 %q", want, h)
		}
	}
}
