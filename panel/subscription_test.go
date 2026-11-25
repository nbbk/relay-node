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

// subUserForToken1427 给测试面板加一个订阅用户（令牌哈希用给定的明文令牌
// 算），返回该用户。1.4.27 起订阅内容只走 SubUser 令牌（通用/合并订阅已
// 退场）：无勾选（单面板全量）、mode 空（=both）、不限流，内容与旧通用
// 订阅一致；旧测试里 `subUserForToken1427(t, s, tok)` 改成调它
// 即可继续测内容渲染。
func subUserForToken1427(t *testing.T, s *Server, tok string) *SubUser {
	t.Helper()
	u := SubUser{ID: "su-1427-" + tok[4:10], Name: "测试用户", TokenHash: hashSubToken(tok), CreatedAt: "2026-11-25T00:00:00Z"}
	s.cfg.SubUsers = append(s.cfg.SubUsers, u)
	return &s.cfg.SubUsers[len(s.cfg.SubUsers)-1]
}

func TestSubTokenAuth(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	subUserForToken1427(t, s, tok)

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
	subUserForToken1427(t, s, tok)

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
	for _, want := range []string{"proxies:", "type: vless", "servername: \"example.com\"", "path: \"/example-path\"", "proxy-groups:", "name: \"手动选择\"", "MATCH,DIRECT"} {
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
	subUserForToken1427(t, s, tok)
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
	// 1.4.27：通用订阅已退场，生成与撤销接口一律 410；旧通用令牌拉取
	// 也 410（不再是轮换/撤销语义）。轮换与撤销现在由订阅用户承担，
	// 见 TestSubUsers130。
	gen := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/settings/subscription/generate", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleSubTokenGenerate(rec, req)
		return rec
	}
	if rec := gen(`{}`); rec.Code != 410 {
		t.Fatalf("生成通用订阅应 410（已退场），实际 %d %s", rec.Code, rec.Body.String())
	}
	if rec := gen(`{}`); rec.Code != 410 {
		t.Fatalf("再次生成通用订阅也应 410，实际 %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/api/v1/settings/subscription/revoke", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleSubTokenRevoke(rec, req)
	if rec.Code != 410 {
		t.Fatalf("撤销通用订阅应 410（已退场），实际 %d %s", rec.Code, rec.Body.String())
	}
	// 旧通用令牌（直接写配置的遗留记录）拉取 → 410。
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)
	if r := callSub(t, s, tok, "/sub/"+tok, ""); r.Code != 410 {
		t.Fatalf("旧通用订阅链接应 410（已退场），实际 %d", r.Code)
	}
}

func TestClashNameDedup(t *testing.T) {
	links := []subLink{
		{Name: "智利-中转", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转"},
		{Name: "智利-中转", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:24444?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转"},
		{Name: "测试", Link: "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=none&type=ws&path=%2Fexample-path#测试"},
		{Name: "SS节点", Link: "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@203.0.113.1:8388#SS节点"},
	}
	body, ctype, err := renderSubContent(links, "clash", "split")
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
	// 1.3.23 起 SS 也进 Clash（type: ss + cipher + password + udp: true）。
	if !strings.Contains(body, "SS节点") || !strings.Contains(body, "type: ss") || !strings.Contains(body, "cipher: \"aes-256-gcm\"") || !strings.Contains(body, "password: \"password\"") {
		t.Fatalf("SS 节点 1.3.23 起应进 Clash：\n%s", body)
	}
	// 同样的链接在 base64 里都在
	raw, _, err := renderSubContent(links, "base64", "split")
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
	// 1.4.27：合并订阅已退场，创建一律 410（到不了选择校验）。
	for _, body := range []string{
		`{"selection":[]}`,
		`{"selection":[{"panel":"local","landing":99}]}`,
		`{"selection":[{"panel":"rm-nope","landing":1}]}`,
		`{"selection":[{"panel":"local","landing":1}]}`,
		`{"selection":[{"panel":"local","landing":2}]}`,
	} {
		if code, _ := call(body); code != 410 {
			t.Fatalf("合并订阅创建 %s 应 410（已退场），实际 %d", body, code)
		}
	}
	// 列表仍可查（供核对），顶层标 retired:true。
	req := httptest.NewRequest("GET", "/api/v1/subscriptions/merged", nil)
	rec2 := httptest.NewRecorder()
	s.handleMergedSubsList(rec2, req)
	if rec2.Code != 200 {
		t.Fatalf("列合并订阅应 200，实际 %d", rec2.Code)
	}
	var list map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &list)
	if list["retired"] != true {
		t.Fatalf("合并订阅列表应标 retired:true，实际 %v", list)
	}
	if subs, _ := list["subs"].([]any); len(subs) != 0 {
		t.Fatalf("退场后创建失败，列表应为空，实际 %v", list)
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

	// 1.4.27：合并订阅已退场，改走订阅用户：勾本面板落地 1 + 远程落地 1。
	rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users",
		`{"name":"小明","selection":[{"panel":"local","landing":1},{"panel":"rm-1","landing":1}]}`)
	if rec.Code != 200 {
		t.Fatalf("新建订阅用户应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	url, _ := resp["url"].(string)
	tok := tokenFromSubURL130(t, url)
	// 取订阅：应含本地 2 条 + 远程 1 条
	r2 := callSub(t, s, tok, "/sub/"+tok+"?format=plain", "")
	if r2.Code != 200 {
		t.Fatalf("订阅用户应 200，实际 %d", r2.Code)
	}
	if strings.Count(r2.Body.String(), "vless://") != 3 || !strings.Contains(r2.Body.String(), "remote.example.com") {
		t.Fatalf("订阅应含本地 2 条 + 远程 1 条：%s", r2.Body.String())
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
	subUserForToken1427(t, s, tok)
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
