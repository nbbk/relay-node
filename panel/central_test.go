package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- 令牌生成 / 校验 / 轮换 / 撤销 ----------

func TestRemoteTokenGenerateVerify(t *testing.T) {
	tok := generateRemoteToken()
	if !strings.HasPrefix(tok, "rpt_") || len(tok) != 4+64 {
		t.Fatalf("令牌格式不对：%s", tok)
	}
	h := hashRemoteToken(tok)
	if h == tok || strings.Contains(h, tok) {
		t.Fatalf("哈希里不应出现明文")
	}
	if !verifyRemoteToken(tok, h) {
		t.Fatalf("正确令牌校验失败")
	}
	if verifyRemoteToken(tok+"x", h) {
		t.Fatalf("错误令牌通过了校验")
	}
	if verifyRemoteToken("", h) || verifyRemoteToken(tok, "") {
		t.Fatalf("空令牌/空哈希应拒绝")
	}
	// 两次生成不一样，哈希也不一样
	if tok2 := generateRemoteToken(); tok2 == tok || hashRemoteToken(tok2) == h {
		t.Fatalf("令牌应每次随机")
	}
}

func TestCentralTokenGenerateRotateRevoke(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	call := func(path string) (int, map[string]any) {
		req := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		switch path {
		case "/api/v1/settings/central-token/generate":
			s.handleCentralTokenGenerate(rec, req)
		case "/api/v1/settings/central-token/revoke":
			s.handleCentralTokenRevoke(rec, req)
		}
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	// 生成
	_, resp := call("/api/v1/settings/central-token/generate")
	tok1, _ := resp["token"].(string)
	if tok1 == "" {
		t.Fatalf("生成应返回明文令牌：%v", resp)
	}
	if s.cfg.RemoteTokenHash == "" || !verifyRemoteToken(tok1, s.cfg.RemoteTokenHash) {
		t.Fatalf("生成的令牌校验不上")
	}
	// 磁盘上的 config.json 里绝不能有明文令牌
	b, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "config.json"))
	if strings.Contains(string(b), tok1) {
		t.Fatalf("config.json 里出现了明文令牌")
	}
	if !strings.Contains(string(b), s.cfg.RemoteTokenHash) {
		t.Fatalf("config.json 里应存哈希")
	}
	// 轮换：旧令牌立即失效
	_, resp2 := call("/api/v1/settings/central-token/generate")
	tok2, _ := resp2["token"].(string)
	if tok2 == tok1 {
		t.Fatalf("轮换应产生新令牌")
	}
	if verifyRemoteToken(tok1, s.cfg.RemoteTokenHash) {
		t.Fatalf("轮换后旧令牌还有效")
	}
	if !verifyRemoteToken(tok2, s.cfg.RemoteTokenHash) {
		t.Fatalf("轮换后新令牌校验不上")
	}
	// 撤销
	if code, _ := call("/api/v1/settings/central-token/revoke"); code != 200 {
		t.Fatalf("撤销应 200，实际 %d", code)
	}
	if s.cfg.RemoteTokenHash != "" || verifyRemoteToken(tok2, s.cfg.RemoteTokenHash) {
		t.Fatalf("撤销后令牌应失效")
	}
	// 再撤销一次应 400（本来就没有）
	if code, _ := call("/api/v1/settings/central-token/revoke"); code != 400 {
		t.Fatalf("重复撤销应 400，实际 %d", code)
	}
}

// ---------- 令牌认证中间件 ----------

func TestRequireCentralToken(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)
	next := s.requireCentralToken(func(w http.ResponseWriter, r *http.Request) {
		if s.currentUser(r) != "central" {
			t.Errorf("令牌进来的请求 currentUser 应为 central，实际 %s", s.currentUser(r))
		}
		writeJSON(w, 200, okResp(nil))
	})
	do := func(auth string) int {
		req := httptest.NewRequest("GET", "/api/v1/central/status", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		next(rec, req)
		return rec.Code
	}
	if code := do(""); code != 401 {
		t.Fatalf("无令牌应 401，实际 %d", code)
	}
	if code := do("Bearer wrong-token"); code != 401 {
		t.Fatalf("错令牌应 401，实际 %d", code)
	}
	if code := do("Bearer " + tok); code != 200 {
		t.Fatalf("正确令牌应 200，实际 %d", code)
	}
	// 没设置令牌时任何令牌都拒绝
	s.cfg.RemoteTokenHash = ""
	if code := do("Bearer " + tok); code != 401 {
		t.Fatalf("未设置令牌时应 401，实际 %d", code)
	}
}

// ---------- 地址校验 ----------

func TestNormalizeRemoteBaseURL(t *testing.T) {
	good := map[string]string{
		"http://203.0.113.1:11111":        "http://203.0.113.1:11111",
		"https://relay.example.com:11111": "https://relay.example.com:11111",
		"http://203.0.113.1:11111/":       "http://203.0.113.1:11111",
	}
	for in, want := range good {
		got, err := normalizeRemoteBaseURL(in)
		if err != nil || got != want {
			t.Fatalf("normalize(%q)=%q,%v want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "203.0.113.1:11111", "ftp://203.0.113.1:11111",
		"http://203.0.113.1:11111/api/v1/status", "http://203.0.113.1:11111/?x=1",
		"http://user:pass@203.0.113.1:11111", "http://999.1.1.1:11111",
		"http://203.0.113.1:99999", "http://203.0.113.1:abc",
	} {
		if _, err := normalizeRemoteBaseURL(bad); err == nil {
			t.Fatalf("normalize(%q) 应拒绝", bad)
		}
	}
}

// ---------- 代理加落地二次校验 ----------

func TestValidateRemoteAddLandingBody(t *testing.T) {
	good := `{"landing":"203.0.113.1","preset":"3","custom_ports":"30080,30443","cert_domain":"example.com"}`
	if err := validateRemoteAddLandingBody([]byte(good)); err != nil {
		t.Fatalf("合法请求被拒：%v", err)
	}
	bad := []string{
		`{"landing":"not an address!!","preset":"1"}`,
		`{"landing":"203.0.113.1","preset":"9"}`,
		`{"landing":"203.0.113.1","preset":"3","custom_ports":""}`,
		`{"landing":"203.0.113.1","preset":"3","custom_ports":"99999"}`,
		`{"landing":"203.0.113.1","preset":"1","cert_domain":"bad_domain"}`,
		`{"landing":"203.0.113.1","preset":"1","traffic_limit_bytes":-1}`,
		`{"landing":"203.0.113.1","preset":"1","mode":"weird"}`,
		`{"landing":"203.0.113.1","preset":"1","share_mode":"9"}`,
		`{"landing":"203.0.113.1","preset":"1","unknown_field":1}`,
		`not json`,
	}
	for _, b := range bad {
		if err := validateRemoteAddLandingBody([]byte(b)); err == nil {
			t.Fatalf("非法请求应被拒：%s", b)
		}
	}
}

// ---------- 远程状态聚合解析 + 登记 + 代理转发 ----------

const fakeRemoteStatus = `{"ok":true,"panel_version":"1.2.0","realm":{"running":true},"disk_pct":42.5,"disk_total_bytes":100,"disk_used_bytes":42,"disk_free_bytes":58,"landings":[{"index":1,"landing":"203.0.113.1","domain":"example.com","mode":"standard","ports":[{"proto":"tcp","listen":443,"remote":443}],"online":true,"latency_ms":12,"expiry":"2027-01-01T00:00:00Z","days_left":30,"traffic":{"today_up":100,"today_down":200,"month_up":1000,"month_down":2000,"total_up":10000,"total_down":20000,"limit_bytes":300000,"limit_mode":"total","used_bytes":30000,"pct":10},"subnodes":[{"domain":"node1.example.com","created_at":"","note":""}]},{"index":2,"landing":"203.0.113.2","domain":"node2.example.com","mode":"door","ports":[{"proto":"tcp","listen":20443,"remote":443}],"online":false,"latency_ms":null}]}`

// seenCalls 记录 fake 远程收到的调用（带锁：HTTP 处理函数与测试分属不同
// goroutine，直接共用一个 []string 会在 -race 下报数据竞争；
// 另外代理写操作会异步触发一次轮询，测试读之前要先等轮询跑完）。
type seenCalls struct {
	mu sync.Mutex
	v  []string
}

func (s *seenCalls) add(x string) {
	s.mu.Lock()
	s.v = append(s.v, x)
	s.mu.Unlock()
}

func (s *seenCalls) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.v))
	copy(out, s.v)
	return out
}

func newFakeRemote(t *testing.T) (*httptest.Server, *seenCalls) {
	t.Helper()
	seen := &seenCalls{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		seen.add("status:" + r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeRemoteStatus))
	})
	mux.HandleFunc("/api/v1/central/landings", func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(map[string]any{"ok": true, "landing_index": 3, "links": []any{}, "qr": map[string]any{}, "door_note": ""})
		seen.add("add:" + r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/api/v1/central/service/restart", func(w http.ResponseWriter, r *http.Request) {
		seen.add("service:" + r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"running":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestFetchRemoteStatusParse(t *testing.T) {
	srv, seen := newFakeRemote(t)
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	rem := RemotePanel{ID: "rm-test", Name: "测试远程", BaseURL: srv.URL, Token: tok}
	snap, err := s.fetchRemoteStatus(rem)
	if err != nil {
		t.Fatalf("fetchRemoteStatus: %v", err)
	}
	if !snap.Online || snap.Version != "1.2.0" || snap.LandingCount != 2 || snap.DiskPct != 42.5 || !snap.RealmRunning {
		t.Fatalf("快照解析不对：%+v", snap)
	}
	if len(snap.Landings) != 2 || snap.Landings[0].LatencyMs == nil || *snap.Landings[0].LatencyMs != 12 {
		t.Fatalf("落地解析不对：%+v", snap.Landings)
	}
	if snap.Landings[1].Online == nil || *snap.Landings[1].Online {
		t.Fatalf("第 2 个落地应为离线：%+v", snap.Landings[1])
	}
	if got := seen.list(); len(got) != 1 || got[0] != "status:Bearer "+tok {
		t.Fatalf("应带 Bearer 令牌调远程，实际：%v", seen.list())
	}
}

func TestCentralRemotesAddAndProxy(t *testing.T) {
	srv, seen := newFakeRemote(t)
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	// 登记
	body := `{"name":"测试远程","base_url":"` + srv.URL + `","token":"` + tok + `"}`
	req := httptest.NewRequest("POST", "/api/v1/central/remotes", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCentralRemotesAdd(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["ok"] != true {
		t.Fatalf("登记应成功：%v", resp)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("登记应返回 id")
	}
	// 主面板 config.json 里存的是令牌明文（必须拿原文去调远程），但接口绝不返回它
	req2 := httptest.NewRequest("GET", "/api/v1/central/remotes", nil)
	rec2 := httptest.NewRecorder()
	s.handleCentralRemotesList(rec2, req2)
	if strings.Contains(rec2.Body.String(), tok) {
		t.Fatalf("聚合接口返回里出现了令牌明文")
	}
	var list map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &list)
	remotes, _ := list["remotes"].([]any)
	if len(remotes) != 1 {
		t.Fatalf("列表应有 1 台远程：%v", list)
	}
	// 代理加落地：应带着令牌调到远程
	req3 := httptest.NewRequest("POST", "/api/v1/central/remotes/"+id+"/landings", strings.NewReader(`{"landing":"203.0.113.9","preset":"3","custom_ports":"30080,30443","cert_domain":"example.com"}`))
	req3.SetPathValue("id", id)
	rec3 := httptest.NewRecorder()
	s.handleCentralRemoteAddLanding(rec3, req3)
	var resp3 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &resp3)
	if resp3["ok"] != true || resp3["landing_index"] == nil {
		t.Fatalf("代理加落地应成功并透传远程返回：%v", resp3)
	}
	// 代理重启 realm
	req4 := httptest.NewRequest("POST", "/api/v1/central/remotes/"+id+"/service/restart", strings.NewReader(`{}`))
	req4.SetPathValue("id", id)
	req4.SetPathValue("action", "restart")
	rec4 := httptest.NewRecorder()
	s.handleCentralRemoteService(rec4, req4)
	var resp4 map[string]any
	_ = json.Unmarshal(rec4.Body.Bytes(), &resp4)
	if resp4["ok"] != true {
		t.Fatalf("代理重启应成功：%v", resp4)
	}
	// 等两次代理成功后触发的后台轮询跑完再断言，否则轮询可能撞上假服务器关闭
	// 与 TempDir 清理，也可能在断言读 seen 时还在追加（-race 报数据竞争）。
	waitCentralPolls(t, s)
	found := map[string]bool{}
	for _, s2 := range seen.list() {
		found[strings.SplitN(s2, ":", 2)[0]] = true
	}
	for _, want := range []string{"status", "add", "service"} {
		if !found[want] {
			t.Fatalf("远程 %s 没被调到，实际：%v", want, seen.list())
		}
	}
	// 取消登记
	req5 := httptest.NewRequest("DELETE", "/api/v1/central/remotes/"+id, nil)
	req5.SetPathValue("id", id)
	rec5 := httptest.NewRecorder()
	s.handleCentralRemotesDelete(rec5, req5)
	var resp5 map[string]any
	_ = json.Unmarshal(rec5.Body.Bytes(), &resp5)
	if resp5["ok"] != true || len(s.cfg.Remotes) != 0 {
		t.Fatalf("取消登记应成功：%v", resp5)
	}
}

func TestCentralRemotesAddRejects(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	post := func(body string) map[string]any {
		req := httptest.NewRequest("POST", "/api/v1/central/remotes", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleCentralRemotesAdd(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp
	}
	// 地址不对
	if r := post(`{"name":"x","base_url":"203.0.113.1:11111","token":"rpt_abc"}`); r["ok"] == true {
		t.Fatalf("没写 http:// 的地址应拒绝：%v", r)
	}
	// 令牌格式不对
	if r := post(`{"name":"x","base_url":"http://203.0.113.1:11111","token":"abc"}`); r["ok"] == true {
		t.Fatalf("非 rpt_ 开头的令牌应拒绝：%v", r)
	}
	// 连不上的远程应拒绝，并写清是主面板连不上（side=local）
	if r := post(`{"name":"x","base_url":"http://127.0.0.1:1","token":"rpt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`); r["ok"] == true || r["side"] != "local" {
		t.Fatalf("连不上的远程应拒绝且 side=local：%v", r)
	}
}

// ---------- 离线超时处理 ----------

func TestPollOneRemoteOffline(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	rem := RemotePanel{ID: "rm-off", Name: "离线远程", BaseURL: "http://127.0.0.1:1", Token: generateRemoteToken()}
	s.cfg.Remotes = []RemotePanel{rem}
	start := time.Now()
	snap := s.pollOneRemote(rem)
	if time.Since(start) > 30*time.Second {
		t.Fatalf("离线拉取耗时太久：%v", time.Since(start))
	}
	if snap.Online {
		t.Fatalf("连不上的远程应判离线")
	}
	if snap.Error == "" {
		t.Fatalf("离线快照应带失败原因")
	}
	s.centralMu.Lock()
	cached := s.centralCache[rem.ID]
	s.centralMu.Unlock()
	if cached == nil || cached.Online {
		t.Fatalf("离线状态应进缓存")
	}
	// 远程状态聚合接口在远程离线时也应返回离线快照（不阻塞本面板）
	req := httptest.NewRequest("GET", "/api/v1/central/remotes", nil)
	rec := httptest.NewRecorder()
	s.handleCentralRemotesList(rec, req)
	var list map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	remotes, _ := list["remotes"].([]any)
	if len(remotes) != 1 {
		t.Fatalf("列表应有 1 台：%v", list)
	}
	if remotes[0].(map[string]any)["online"] != false {
		t.Fatalf("列表里应标离线：%v", remotes[0])
	}
}

// 远程面板返回 401（令牌不对）时，登记应拒绝且 side=remote。
func TestFetchRemoteStatusUnauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"ok":false,"error":"远程管理令牌不对或未设置"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	rem := RemotePanel{ID: "rm-401", Name: "x", BaseURL: srv.URL, Token: generateRemoteToken()}
	_, err := s.fetchRemoteStatus(rem)
	if err == nil {
		t.Fatalf("401 应返回错误")
	}
	side, msg := remoteSideOf(err)
	if side != "remote" || !strings.Contains(msg, "令牌") {
		t.Fatalf("401 应为 side=remote 且文案提令牌，实际 side=%s msg=%s", side, msg)
	}
}

// 远程面板版本太旧（1.1.11）时应拒绝。
func TestFetchRemoteStatusOldVersion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"panel_version":"1.1.11","realm":{"running":true},"disk_pct":10,"landings":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	rem := RemotePanel{ID: "rm-old", Name: "x", BaseURL: srv.URL, Token: generateRemoteToken()}
	if _, err := s.fetchRemoteStatus(rem); err == nil {
		t.Fatalf("旧版本应拒绝")
	}
}
