package main

// 中转路由 C 方案（v1.4.27，未发版）：放宽校验＋代理。
//
// ① 只填远端 IP:端口就能建路由（remote_node_id 为 0 时不再要求节点
//    记录存在），同时保留地址格式校验；② 管理面板的路由增删改／
//    列表／重下发代理到选中的中转面板（远程面板）的令牌接口
//    /api/v1/central/relay-routes*；③ 管理面板不再本地存路由。
// 旧的管理面板行为（只存不下发）见 TestRelayRoutesCreateManagerOnly1418
//（panel_1418_test.go，已按 C 改断言）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ---------- 假中转面板 ----------

// fakeRelayRemote1428 假中转面板：只认 /api/v1/central/relay-routes*
// 与 /api/v1/central/status，记下每次路由调用（方法＋路径＋令牌＋
// 原请求体）。status 不记（代理成功后后台会刷一次缓存，不记免得
// 干扰断言）。
type fakeRelayRemote1428 struct {
	srv    *httptest.Server
	mu     sync.Mutex
	seen   []string
	routes []map[string]any
}

func newFakeRelayRemote1428(t *testing.T) *fakeRelayRemote1428 {
	t.Helper()
	f := &fakeRelayRemote1428{routes: []map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "panel_version": Version, "realm": map[string]any{"running": true}, "landings": []any{}})
	})
	mux.HandleFunc("/api/v1/central/relay-routes", func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll1428(r)
		f.mu.Lock()
		f.seen = append(f.seen, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization")+" body="+string(body))
		f.mu.Unlock()
		switch r.Method {
		case "GET":
			writeJSON(w, 200, map[string]any{"ok": true, "routes": f.routes, "local_relay": true})
		case "POST", "PUT":
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			route := map[string]any{
				"relay_id": req["relay_id"], "listen_addr": req["listen_addr"],
				"remote_node_id": req["remote_node_id"], "remote_addr": req["remote_addr"],
				"remote_port": req["remote_port"], "proto": req["proto"],
				"transport": "tcp", "enabled": true, "use_udp": false,
			}
			writeJSON(w, 200, map[string]any{"ok": true, "route": route, "applied": true, "apply_error": ""})
		case "DELETE":
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			writeJSON(w, 200, map[string]any{"ok": true, "deleted": req["listen_addr"], "applied": true, "apply_error": ""})
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/api/v1/central/relay-routes/apply", func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll1428(r)
		f.mu.Lock()
		f.seen = append(f.seen, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization")+" body="+string(body))
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true, "applied": true, "apply_error": ""})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func readAll1428(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}

func (f *fakeRelayRemote1428) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.seen...)
}

// newManagerServer1428 管理面板测试服：登录后设为 ManagerOnly，
// 并登记给定的远程面板。返回 (server, 路由, 会话 cookie)。
func newManagerServer1428(t *testing.T, remotes []RemotePanel) (*Server, http.Handler, string) {
	t.Helper()
	s := newTestServer1400(t)
	h := s.routes()
	cookie := login1418(t, h)
	s.cfg.ManagerOnly = true
	s.cfg.Remotes = remotes
	return s, h, cookie
}

// bearer1428 带令牌调接口（central/* 用），返回 (状态码, 响应体)。
func bearer1428(t *testing.T, h http.Handler, token, method, path, body string) (int, string) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// ---------- ① 只填地址建路由 ----------

// 只填远端 IP:端口就能建路由：IP 与域名都行，路由按原样存，
// 下发也走 RemoteAddr（不依赖节点记录）。
func TestRelayRouteAddrOnlyCreate1428(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := login1418(t, h)
	calls, restore := stubRestart1418(t)
	defer restore()
	seedToml1418(t, s, "[network]\nno_tcp = false\nuse_udp = false\n")

	code, body := authed1400(t, h, cookie, "POST", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","remote_addr":"203.0.113.9","remote_port":443,"proto":"tcp"}`)
	if code != 200 {
		t.Fatalf("只填 IP 建路由应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	if m["applied"] != true {
		t.Fatalf("只填 IP 建路由应 applied=true：%s", body)
	}
	route, _ := m["route"].(map[string]any)
	if route["remote_node_id"] != float64(0) || route["remote_addr"] != "203.0.113.9" {
		t.Fatalf("只填 IP 的路由应 remote_node_id=0、remote_addr=203.0.113.9，实际 %v", route)
	}
	if *calls != 1 {
		t.Fatalf("建路由应重启 1 次，实际 %d 次", *calls)
	}

	code, body = authed1400(t, h, cookie, "POST", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30005","remote_addr":"landing.example.com","remote_port":8443,"proto":"udp"}`)
	if code != 200 {
		t.Fatalf("只填域名建路由应 200，实际 %d：%s", code, body)
	}
	if got := readToml1418(t, s); !strings.Contains(got, "203.0.113.9:443") || !strings.Contains(got, "landing.example.com:8443") {
		t.Fatalf("config.toml 应有两条只填地址的路由段，实际：%q", got)
	}
}

// 只填地址的校验：没选节点又没填地址、地址格式不对、
// 指到的节点 id 不存在（仍报错），三者都直接 400。
func TestRelayRouteAddrOnlyValidation1428(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := login1418(t, h)
	_, _ = createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	for _, tc := range []struct {
		name, body, want string
	}{
		{"没选节点又没填地址", `{"listen_addr":"0.0.0.0:30004","remote_port":443}`, "落地地址不能为空"},
		{"地址带端口", `{"listen_addr":"0.0.0.0:30004","remote_addr":"203.0.113.9:443","remote_port":443}`, "落地地址不对"},
		{"地址带空格", `{"listen_addr":"0.0.0.0:30004","remote_addr":"not a host","remote_port":443}`, "落地地址不对"},
		{"地址含下划线", `{"listen_addr":"0.0.0.0:30004","remote_addr":"exa_mple.com","remote_port":443}`, "落地地址不对"},
		{"地址标签以连字符开头", `{"listen_addr":"0.0.0.0:30004","remote_addr":"-bad-.example.com","remote_port":443}`, "落地地址不对"},
		{"指到的节点不存在", `{"listen_addr":"0.0.0.0:30004","remote_node_id":99,"remote_addr":"203.0.113.9","remote_port":443}`, "目标节点不存在"},
	} {
		code, body := authed1400(t, h, cookie, "POST", "/api/v1/relay-routes", tc.body)
		if code != 400 || !strings.Contains(body, tc.want) {
			t.Fatalf("%s 应 400 且含 %q，实际 %d：%s", tc.name, tc.want, code, body)
		}
	}
}

// 域名格式校验本身：合法的放行，非法的一律拦。
func TestValidDomainName1428(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"example.com", true},
		{"landing.example.com", true},
		{"xn--fsq.example.com", true},
		{"localhost", true},
		{"203.0.113.9", true}, // 数字标签也是合法域名形式（IP 另走 ParseIP）
		{"", false},
		{"exa_mple.com", false},
		{"-bad.example.com", false},
		{"bad-.example.com", false},
		{"a..b", false},
		{"a b.com", false},
		{"a/b.com", false},
		{"a:b.com", false},
	} {
		if got := validDomainName(tc.in); got != tc.want {
			t.Fatalf("validDomainName(%q) 应 %v，实际 %v", tc.in, tc.want, got)
		}
	}
}

// 改路由：从“选节点”改成“只填地址”——remote_node_id 显式发 0
// 且落地地址已填时，不再用原值把节点补回来（补齐逻辑的 C 口径）。
func TestRelayRouteUpdateToAddrOnly1428(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := login1418(t, h)
	_, _ = createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	calls, restore := stubRestart1418(t)
	defer restore()
	seedToml1418(t, s, "[network]\nno_tcp = false\nuse_udp = false\n")

	code, body := authed1400(t, h, cookie, "POST", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","remote_node_id":1,"remote_port":443,"proto":"tcp"}`)
	if code != 200 {
		t.Fatalf("先建节点路由应 200，实际 %d：%s", code, body)
	}
	code, body = authed1400(t, h, cookie, "PUT", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","remote_node_id":0,"remote_addr":"203.0.113.9","remote_port":8443}`)
	if code != 200 {
		t.Fatalf("改成只填地址应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	route, _ := m["route"].(map[string]any)
	if route["remote_node_id"] != float64(0) || route["remote_addr"] != "203.0.113.9" || route["remote_port"] != float64(8443) {
		t.Fatalf("改完应是只填地址的路由，实际 %v", route)
	}
	if *calls != 2 {
		t.Fatalf("建＋改应各重启 1 次，共 2 次，实际 %d 次", *calls)
	}
}

// ---------- ② 管理面板代理 ----------

// 管理面板＋一台中转面板：建路由代理到中转面板的令牌接口，
// 原请求体（含 relay_panel_id）原样转发，带 Bearer 令牌；
// 列表也代理，回包补 proxied／relay_panel_id／relay_panel_name。
func TestRelayRoutesProxyCreateList1428(t *testing.T) {
	fake := newFakeRelayRemote1428(t)
	fake.routes = []map[string]any{
		{"relay_id": 0, "listen_addr": "0.0.0.0:30004", "remote_node_id": 0, "remote_addr": "203.0.113.9", "remote_port": 443, "proto": "tcp", "transport": "tcp", "enabled": true, "use_udp": false},
	}
	_, h, cookie := newManagerServer1428(t, []RemotePanel{
		{ID: "rm-hk", Name: "香港plus", BaseURL: fake.srv.URL, Token: "rpt_test1428"},
	})

	code, body := authed1400(t, h, cookie, "POST", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","remote_node_id":0,"remote_addr":"203.0.113.9","remote_port":443,"proto":"tcp","relay_panel_id":"rm-hk"}`)
	if code != 200 {
		t.Fatalf("代理建路由应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	if m["applied"] != true {
		t.Fatalf("代理建路由应透传远程的 applied=true：%s", body)
	}
	route, _ := m["route"].(map[string]any)
	if route["remote_addr"] != "203.0.113.9" || route["remote_node_id"] != float64(0) {
		t.Fatalf("代理建路由应透传远程回的路由，实际 %v", route)
	}
	calls := fake.calls()
	if len(calls) != 1 || !strings.Contains(calls[0], "POST /api/v1/central/relay-routes") ||
		!strings.Contains(calls[0], "auth=Bearer rpt_test1428") ||
		!strings.Contains(calls[0], `"relay_panel_id":"rm-hk"`) ||
		!strings.Contains(calls[0], `"remote_node_id":0`) {
		t.Fatalf("中转面板应收到 1 次带令牌的 POST /api/v1/central/relay-routes，实际 %v", calls)
	}

	code, body = authed1400(t, h, cookie, "GET", "/api/v1/relay-routes", "")
	if code != 200 {
		t.Fatalf("代理列表应 200，实际 %d：%s", code, body)
	}
	m = respMap1400(t, body)
	if m["proxied"] != true || m["relay_panel_id"] != "rm-hk" || m["relay_panel_name"] != "香港plus" {
		t.Fatalf("代理列表应带 proxied／relay_panel_id／relay_panel_name，实际 %s", body)
	}
	routes, _ := m["routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("代理列表应有远程那 1 条路由，实际 %s", body)
	}
	calls = fake.calls()
	if len(calls) != 2 || !strings.Contains(calls[1], "GET /api/v1/central/relay-routes") {
		t.Fatalf("列表应代理 GET /api/v1/central/relay-routes，实际 %v", calls)
	}
}

// 管理面板列表、没登记中转面板：回 200 空列表＋提示，不弹错。
func TestRelayRoutesProxyListNoRemote1428(t *testing.T) {
	_, h, cookie := newManagerServer1428(t, nil)
	code, body := authed1400(t, h, cookie, "GET", "/api/v1/relay-routes", "")
	if code != 200 {
		t.Fatalf("没登记中转面板时列表应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	routes, _ := m["routes"].([]any)
	if len(routes) != 0 || m["local_relay"] != false {
		t.Fatalf("没登记中转面板时列表应空且 local_relay=false：%s", body)
	}
	hint, _ := m["relay_hint"].(string)
	if !strings.Contains(hint, "还没登记中转面板") {
		t.Fatalf("没登记中转面板时列表应提示登记，实际 %q", hint)
	}
}

// 管理面板＋两台中转面板：不指明中转机时 400；指明后只代理到那台。
func TestRelayRoutesProxyMultiRemote1428(t *testing.T) {
	fake1 := newFakeRelayRemote1428(t)
	fake2 := newFakeRelayRemote1428(t)
	_, h, cookie := newManagerServer1428(t, []RemotePanel{
		{ID: "rm-1", Name: "香港plus", BaseURL: fake1.srv.URL, Token: "rpt_test1428"},
		{ID: "rm-2", Name: "香港pro", BaseURL: fake2.srv.URL, Token: "rpt_test1428"},
	})

	code, body := authed1400(t, h, cookie, "POST", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","remote_addr":"203.0.113.9","remote_port":443,"proto":"tcp"}`)
	if code != 400 || !strings.Contains(body, "选一台中转机") {
		t.Fatalf("两台中转面板、不指明时应 400 且提示选一台，实际 %d：%s", code, body)
	}

	code, body = authed1400(t, h, cookie, "POST", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","remote_addr":"203.0.113.9","remote_port":443,"proto":"tcp","relay_panel_id":"rm-2"}`)
	if code != 200 {
		t.Fatalf("指明 rm-2 后代理建路由应 200，实际 %d：%s", code, body)
	}
	if len(fake1.calls()) != 0 {
		t.Fatalf("指明 rm-2 后不应调 rm-1，实际 %v", fake1.calls())
	}
	if len(fake2.calls()) != 1 || !strings.Contains(fake2.calls()[0], "POST /api/v1/central/relay-routes") {
		t.Fatalf("指明 rm-2 后应只调 rm-2 的令牌接口，实际 %v", fake2.calls())
	}
}

// 管理面板代理改／删／重下发：各走对应的令牌接口，relay_panel_id
// 跟过去；重下发的 relay_panel_id 也可以放 query 参数里。
func TestRelayRoutesProxyUpdateDeleteApply1428(t *testing.T) {
	fake := newFakeRelayRemote1428(t)
	_, h, cookie := newManagerServer1428(t, []RemotePanel{
		{ID: "rm-hk", Name: "香港plus", BaseURL: fake.srv.URL, Token: "rpt_test1428"},
	})

	code, body := authed1400(t, h, cookie, "PUT", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","remote_addr":"203.0.113.10","remote_port":8443,"relay_panel_id":"rm-hk"}`)
	if code != 200 {
		t.Fatalf("代理改路由应 200，实际 %d：%s", code, body)
	}
	code, body = authed1400(t, h, cookie, "DELETE", "/api/v1/relay-routes",
		`{"relay_id":0,"listen_addr":"0.0.0.0:30004","relay_panel_id":"rm-hk"}`)
	if code != 200 {
		t.Fatalf("代理删路由应 200，实际 %d：%s", code, body)
	}
	code, body = authed1400(t, h, cookie, "POST", "/api/v1/relay-routes/apply?relay_panel_id=rm-hk", `{}`)
	if code != 200 {
		t.Fatalf("代理重下发应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	if m["applied"] != true {
		t.Fatalf("代理重下发应透传 applied=true：%s", body)
	}
	calls := fake.calls()
	want := []string{
		"PUT /api/v1/central/relay-routes",
		"DELETE /api/v1/central/relay-routes",
		"POST /api/v1/central/relay-routes/apply",
	}
	if len(calls) != len(want) {
		t.Fatalf("中转面板应收到 %d 次调用，实际 %v", len(want), calls)
	}
	for i, w := range want {
		if !strings.Contains(calls[i], w) || !strings.Contains(calls[i], "auth=Bearer rpt_test1428") {
			t.Fatalf("第 %d 次调用应是带令牌的 %s，实际 %q", i+1, w, calls[i])
		}
	}
}

// ---------- ③ 远程的令牌接口 ----------

// 本机 /api/v1/central/relay-routes*：无令牌／错令牌 401；
// 正确令牌能调，行为与本机 /api/v1/relay-routes 一致（走本地分支）。
func TestRelayCentralRelayRoutesAuth1428(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	calls, restore := stubRestart1418(t)
	defer restore()
	seedToml1418(t, s, "[network]\nno_tcp = false\nuse_udp = false\n")
	tok := generateRemoteToken()
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)

	if code, _ := bearer1428(t, h, "", "GET", "/api/v1/central/relay-routes", ""); code != 401 {
		t.Fatalf("令牌接口无令牌应 401，实际 %d", code)
	}
	if code, _ := bearer1428(t, h, "rpt_wrong", "GET", "/api/v1/central/relay-routes", ""); code != 401 {
		t.Fatalf("令牌接口错令牌应 401，实际 %d", code)
	}
	code, body := bearer1428(t, h, tok, "GET", "/api/v1/central/relay-routes", "")
	if code != 200 {
		t.Fatalf("令牌接口正确令牌列表应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	if m["local_relay"] != true {
		t.Fatalf("本机有中转时令牌列表应 local_relay=true：%s", body)
	}
	code, body = bearer1428(t, h, tok, "POST", "/api/v1/central/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","remote_addr":"203.0.113.9","remote_port":443,"proto":"tcp"}`)
	if code != 200 {
		t.Fatalf("令牌接口正确令牌建路由应 200，实际 %d：%s", code, body)
	}
	m = respMap1400(t, body)
	if m["applied"] != true {
		t.Fatalf("令牌接口建路由应 applied=true：%s", body)
	}
	if *calls != 1 {
		t.Fatalf("令牌接口建路由应重启 1 次，实际 %d 次", *calls)
	}
}
