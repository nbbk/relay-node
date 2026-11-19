package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1.2.5：集中管理远程新增节点 ----------

// addNodeTwinStub 模拟 install.sh：--twin 直接回一条直连链接，--nodes 空跑。
const addNodeTwinStub = `#!/bin/bash
if [ "$1" = "--twin" ]; then echo "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls&sni=node2.example.com#test-直连"; exit 0; fi
exit 0
`

// addNodeRelayConf 落地 203.0.113.1（编号 1，预设 3 自定义端口）。
const addNodeRelayConf = `LANDING=203.0.113.1
LANDING_IS_DOMAIN=0
DOMAIN=""
CERT_DOMAIN="example.com"
PRESET=3
MODE_NOTE="自定义端口"
PORTS="tcp:30080:30080 tcp:30443:30443"
LANDING_COUNT=1
ALL_PORTS="tcp:30080:30080 tcp:30443:30443"
SHARE80=0
SHARE443=0
`

func newTestAddNodeServer(t *testing.T) (*Server, string) {
	t.Helper()
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(addNodeTwinStub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(addNodeRelayConf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tok := generateRemoteToken()
	s := newTestAddLandingServer(t, kitDir, realmDir)
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)
	return s, tok
}

func callCentralAddNode(t *testing.T, s *Server, tok, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/central/landings/1/add-node", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	req.SetPathValue("index", "1")
	rec := httptest.NewRecorder()
	s.requireCentralToken(s.handleAddNode)(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func TestCentralAddNodeTokenAuth(t *testing.T) {
	s, tok := newTestAddNodeServer(t)
	// 无令牌 → 401
	if code, _ := callCentralAddNode(t, s, "", `{"subdomain":"node2.example.com"}`); code != 401 {
		t.Fatalf("无令牌应 401，实际 %d", code)
	}
	// 错令牌 → 401
	if code, _ := callCentralAddNode(t, s, "rpt_0000000000000000000000000000000000000000000000000000000000000000", `{"subdomain":"node2.example.com"}`); code != 401 {
		t.Fatalf("错令牌应 401，实际 %d", code)
	}
	// 对令牌 + 空体：应过鉴权、走到子域名校验（400），而不是 401
	if code, resp := callCentralAddNode(t, s, tok, `{}`); code != 400 || resp["error"] == nil {
		t.Fatalf("对令牌+空体应 400 子域名校验，实际 %d %v", code, resp)
	}
}

func TestValidateRemoteAddNodeBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool // true=应通过
	}{
		{"空体", `{}`, false},
		{"子域名不对", `{"subdomain":"bad"}`, false},
		{"第 1 步只填子域名", `{"subdomain":"node2.example.com"}`, true},
		{"第 2 步链接域名一致", `{"subdomain":"node2.example.com","new_link":"vless://11111111-2222-3333-4444-555555555555@node2.example.com:443?security=tls&sni=node2.example.com#test"}`, true},
		{"链接域名不一致", `{"subdomain":"node2.example.com","new_link":"vless://11111111-2222-3333-4444-555555555555@other.example.com:443?security=tls&sni=other.example.com#test"}`, false},
		{"链接解析不出", `{"subdomain":"node2.example.com","new_link":"not-a-link"}`, false},
		{"未知字段", `{"subdomain":"node2.example.com","zzz":1}`, false},
	}
	for _, c := range cases {
		err := validateRemoteAddNodeBody([]byte(c.body))
		if c.want && err != nil {
			t.Errorf("%s：应通过，实际 %v", c.name, err)
		}
		if !c.want && err == nil {
			t.Errorf("%s：应拒绝，却通过了", c.name)
		}
	}
}

func TestCentralAddNodeSuccess(t *testing.T) {
	s, tok := newTestAddNodeServer(t)
	// 第 1 步：只填子域名 → 返回落地机命令
	code, resp := callCentralAddNode(t, s, tok, `{"subdomain":"node2.example.com"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("第 1 步应成功：%d %v", code, resp)
	}
	wz, _ := resp["wizard"].(map[string]any)
	cmd, _ := wz["landing_cmd"].(string)
	if !strings.Contains(cmd, "--add-node node2.example.com") {
		t.Fatalf("第 1 步应返回落地机命令，实际 %v", resp)
	}
	// 第 2 步：粘贴新链接 → 返回 twin（中转+直连）与二维码，子节点已登记
	newLink := "vless://11111111-2222-3333-4444-555555555555@node2.example.com:443?security=tls&sni=node2.example.com#test"
	code, resp = callCentralAddNode(t, s, tok, `{"subdomain":"node2.example.com","new_link":"`+newLink+`"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("第 2 步应成功：%d %v", code, resp)
	}
	links, _ := resp["links"].([]any)
	if len(links) != 2 {
		t.Fatalf("应返回中转+直连两条链接，实际 %v", resp["links"])
	}
	l0, _ := links[0].(map[string]any)
	l1, _ := links[1].(map[string]any)
	if l0["label"] != "中转" || l0["link"] != newLink {
		t.Fatalf("第 1 条应是中转原链接，实际 %v", l0)
	}
	if l1["label"] != "直连" || !strings.Contains(l1["link"].(string), "@203.0.113.1:443") {
		t.Fatalf("第 2 条应是直连链接，实际 %v", l1)
	}
	qr, _ := resp["qr"].(map[string]any)
	if qr["中转"] == nil || qr["直连"] == nil {
		t.Fatalf("中转/直连二维码都应有，实际 %v", qr)
	}
	s.stateMu.Lock()
	ext := s.state.ext("203.0.113.1")
	found := false
	for _, sn := range ext.SubNodes {
		if strings.EqualFold(sn.Domain, "node2.example.com") {
			found = true
		}
	}
	s.stateMu.Unlock()
	if !found {
		t.Fatalf("子节点应登记进 state，实际 %+v", ext.SubNodes)
	}
}

// ---------- 1.2.5：主面板代理加节点（版本门） ----------

func TestCentralRemoteAddNodeVersionGate(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	rem := RemotePanel{ID: "rm-125", Name: "测试远程", BaseURL: "http://127.0.0.1:1", Token: tok}
	s.cfg.Remotes = []RemotePanel{rem}
	call := func() map[string]any {
		req := httptest.NewRequest("POST", "/api/v1/central/remotes/rm-125/landings/1/add-node", strings.NewReader(`{"subdomain":"node2.example.com"}`))
		req.SetPathValue("id", "rm-125")
		req.SetPathValue("index", "1")
		rec := httptest.NewRecorder()
		s.handleCentralRemoteAddNode(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp
	}
	// 缓存版本 1.2.4：直接拒掉，不发请求，side=remote，文案写清要 1.2.5+
	s.centralMu.Lock()
	s.centralCache["rm-125"] = &RemoteStatusSnapshot{ID: "rm-125", Version: "1.2.4", Landings: []remoteLandingSummary{}}
	s.centralMu.Unlock()
	if r := call(); r["ok"] == true || r["side"] != "remote" || !strings.Contains(r["error"].(string), "1.2.5") {
		t.Fatalf("远程 1.2.4 应拒掉并提示升 1.2.5：%v", r)
	}
	// 缓存版本 1.2.5：放行，走到连不上远程（side=local），说明版本门通过了
	s.centralMu.Lock()
	s.centralCache["rm-125"].Version = "1.2.5"
	s.centralMu.Unlock()
	if r := call(); r["ok"] == true || r["side"] != "local" {
		t.Fatalf("远程 1.2.5 应放行到连远程（side=local）：%v", r)
	}
	// 远程返回 404（老面板没这个接口）：文案应写清要 1.2.5+，side=remote
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/landings/1/add-node", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"ok":false,"error":"not found"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s.cfg.Remotes[0].BaseURL = srv.URL
	s.centralMu.Lock()
	delete(s.centralCache, "rm-125")
	s.centralMu.Unlock()
	if r := call(); r["ok"] == true || r["side"] != "remote" || !strings.Contains(r["error"].(string), "1.2.5") {
		t.Fatalf("远程 404 应提示版本太旧要 1.2.5+：%v", r)
	}
}

// ---------- 1.2.5：令牌权限范围没有扩大 ----------

func TestCentralTokenScopeNotExpanded(t *testing.T) {
	s, tok := newTestAddNodeServer(t)
	mux := s.routes()
	do := func(method, path, auth, body string) int {
		var rd *strings.Reader
		if body != "" {
			rd = strings.NewReader(body)
		} else {
			rd = strings.NewReader("")
		}
		req := httptest.NewRequest(method, path, rd)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	// 令牌调会话接口（设置）应 401：令牌只放行 central/*，改设置调不通
	if code := do("GET", "/api/v1/settings", "Bearer "+tok, ""); code != 401 {
		t.Fatalf("令牌调 /api/v1/settings 应 401，实际 %d", code)
	}
	// 令牌调非 central 的落地接口也应 401
	if code := do("POST", "/api/v1/landings/1/add-node", "Bearer "+tok, `{"subdomain":"node2.example.com"}`); code != 401 {
		t.Fatalf("令牌调本地加节点接口应 401，实际 %d", code)
	}
	// 令牌调新 central 接口应过鉴权（400 是子域名校验，不是 401）
	if code := do("POST", "/api/v1/central/landings/1/add-node", "Bearer "+tok, `{}`); code == 401 {
		t.Fatalf("令牌调新 central 加节点接口不应 401，实际 %d", code)
	}
	// 令牌调 central/status 应过鉴权（不是 401；没装中转时可能 500，但鉴权过了）
	if code := do("GET", "/api/v1/central/status", "Bearer "+tok, ""); code == 401 {
		t.Fatalf("令牌调 central/status 不应 401，实际 %d", code)
	}
	// 无令牌调 central 接口仍 401
	if code := do("GET", "/api/v1/central/status", "", ""); code != 401 {
		t.Fatalf("无令牌调 central/status 应 401，实际 %d", code)
	}
}

// ---------- 1.2.5：远程加落地校验与本地一致 ----------

func TestValidateRemoteAddLandingBodyParity125(t *testing.T) {
	full := `{"landing":"203.0.113.1","preset":"2","sb_mode":"2","custom_ports":"","want_udp":true,"cert_domain":"example.com","cert_mode":"dns01","duration_days":3650,"traffic_limit_bytes":107374182400,"traffic_limit_mode":"monthly","speed_limit_mbps":100,"share_mode":"1","listen_overrides":{"443":10443},"mode":"door","relay_addr":"203.0.113.9","door_port_overrides":{"443":20443},"orig_link":"vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test","name":"香港-01","link_name":"香港-01"}`
	if err := validateRemoteAddLandingBody([]byte(full)); err != nil {
		t.Fatalf("本地全字段载荷应通过，实际 %v", err)
	}
	cases := []struct {
		name string
		body string
	}{
		{"未知字段", `{"landing":"203.0.113.1","preset":"1","zzz":1}`},
		{"sing-box 模式非法", `{"landing":"203.0.113.1","preset":"2","sb_mode":"3"}`},
		{"签发方式非法", `{"landing":"203.0.113.1","preset":"1","cert_mode":"acme"}`},
		{"时长 -2", `{"landing":"203.0.113.1","preset":"1","duration_days":-2}`},
		{"名称超长", `{"landing":"203.0.113.1","preset":"1","name":"` + strings.Repeat("名", 65) + `"}`},
		{"原始链接不对", `{"landing":"203.0.113.1","preset":"1","orig_link":"not-a-link"}`},
		{"转发端口覆盖非法", `{"landing":"203.0.113.1","preset":"1","mode":"door","door_port_overrides":{"443":0}}`},
		{"监听端口覆盖非法", `{"landing":"203.0.113.1","preset":"1","listen_overrides":{"443":70000}}`},
		{"任意门地址不对", `{"landing":"203.0.113.1","preset":"1","mode":"door","relay_addr":"bad host!"}`},
	}
	for _, c := range cases {
		if err := validateRemoteAddLandingBody([]byte(c.body)); err == nil {
			t.Errorf("%s：应拒绝，却通过了", c.name)
		}
	}
	// 本地允许 -1（与 validateLandingReqFields 一致：DurationDays < -1 才拒）
	if err := validateRemoteAddLandingBody([]byte(`{"landing":"203.0.113.1","preset":"1","duration_days":-1}`)); err != nil {
		t.Errorf("时长 -1 应与本地一致放行，实际 %v", err)
	}
}

// 远程加落地全字段应原样透传到被管面板（主面板不丢字段）。
func TestCentralRemoteAddLandingFullFieldsPassthrough(t *testing.T) {
	var gotBody string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeRemoteStatus))
	})
	mux.HandleFunc("/api/v1/central/landings", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"landing_index":3,"links":[],"qr":{},"door_note":""}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	body := `{"name":"测试远程","base_url":"` + srv.URL + `","token":"` + tok + `"}`
	req := httptest.NewRequest("POST", "/api/v1/central/remotes", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCentralRemotesAdd(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("登记应成功：%v", resp)
	}
	full := `{"landing":"203.0.113.9","preset":"2","sb_mode":"2","want_udp":true,"cert_domain":"example.com","cert_mode":"dns01","duration_days":90,"traffic_limit_bytes":107374182400,"traffic_limit_mode":"monthly","speed_limit_mbps":50,"mode":"door","relay_addr":"203.0.113.8","orig_link":"vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test","name":"香港-01","link_name":"香港-01"}`
	req2 := httptest.NewRequest("POST", "/api/v1/central/remotes/"+id+"/landings", strings.NewReader(full))
	req2.SetPathValue("id", id)
	rec2 := httptest.NewRecorder()
	s.handleCentralRemoteAddLanding(rec2, req2)
	var resp2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if resp2["ok"] != true {
		t.Fatalf("代理加落地应成功：%v", resp2)
	}
	for _, want := range []string{`"cert_mode":"dns01"`, `"sb_mode":"2"`, `"mode":"door"`, `"relay_addr":"203.0.113.8"`, `"name":"香港-01"`, `"link_name":"香港-01"`, `"orig_link":"vless://`, `"duration_days":90`, `"traffic_limit_mode":"monthly"`, `"speed_limit_mbps":50`, `"want_udp":true`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("透传到远程的请求体缺字段 %s，实际 %s", want, gotBody)
		}
	}
	// 等后台轮询跑完再返回，否则轮询可能撞上假服务器关闭与 TempDir 清理
	waitCentralPolls(t, s)
}
