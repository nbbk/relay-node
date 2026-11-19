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
	"time"
)

// waitCentralPolls 等后台轮询跑完：代理写操作成功后会异步刷新一次远程快照；
// 测试直接返回的话，轮询可能在假服务器已关、TempDir 已开始清理时才跑，
// 失败写审计日志会撞上清理（偶发 "directory not empty"）。等跑完再返回。
func waitCentralPolls(t *testing.T, s *Server) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.centralPollWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("后台轮询 10 秒内没跑完")
	}
}

// ---------- 1.2.8：管理面板模式开关 ----------

func TestManagerOnlyToggle(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	call := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/v1/settings/manager-only", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleManagerOnly(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	if s.cfg.ManagerOnly {
		t.Fatalf("默认应是完整面板（manager_only=false）")
	}
	// 切到管理面板
	if code, resp := call(`{"manager_only":true}`); code != 200 || resp["manager_only"] != true {
		t.Fatalf("切到管理面板应成功：%d %v", code, resp)
	}
	b, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "config.json"))
	if !strings.Contains(string(b), `"manager_only": true`) && !strings.Contains(string(b), `"manager_only":true`) {
		t.Fatalf("磁盘 config.json 里应存下 manager_only=true：%s", b)
	}
	// 切回完整面板
	if code, resp := call(`{"manager_only":false}`); code != 200 || resp["manager_only"] != false {
		t.Fatalf("切回完整面板应成功：%d %v", code, resp)
	}
	// 非法请求体 → 400
	if code, _ := call(`{bad`); code != 400 {
		t.Fatalf("非法请求体应 400，实际 %d", code)
	}
	// 未知字段 → 400（DisallowUnknownFields）
	if code, _ := call(`{"manager_only":true,"zzz":1}`); code != 400 {
		t.Fatalf("未知字段应 400，实际 %d", code)
	}
}

func TestManagerOnlyInMeAndSettings(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	s.cfg.ManagerOnly = true
	// /api/v1/me 应带 manager_only
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	rec := httptest.NewRecorder()
	s.handleMe(rec, req)
	var me map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me["manager_only"] != true {
		t.Fatalf("/api/v1/me 应返回 manager_only=true：%v", me)
	}
	// /api/v1/settings 应带 manager_only（版本检查走缓存，不拉网）
	latestVersionCache.set("1.2.7")
	req2 := httptest.NewRequest("GET", "/api/v1/settings", nil)
	rec2 := httptest.NewRecorder()
	s.handleSettings(rec2, req2)
	var st map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &st)
	if st["manager_only"] != true {
		t.Fatalf("/api/v1/settings 应返回 manager_only=true：%v", st)
	}
}

// ---------- 1.2.8：新增 central 接口的鉴权 ----------

func TestCentral128EndpointsTokenAuth(t *testing.T) {
	s, tok := newTestAddNodeServer(t)
	mux := s.routes()
	do := func(method, path, auth, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	// 新增的被管端 central 接口：无令牌 401，错令牌 401，对令牌不过 401
	//（走到业务校验/落地不存在，不是鉴权失败）
	paths := []struct{ method, path, body string }{
		{"PUT", "/api/v1/central/landings/1", `{}`},
		{"POST", "/api/v1/central/landings/1/orig-link", `{}`},
		{"DELETE", "/api/v1/central/landings/1/subnodes/node2.example.com", ""},
		{"POST", "/api/v1/central/landings/1/renew", `{}`},
		{"POST", "/api/v1/central/landings/1/add-traffic", `{}`},
		{"POST", "/api/v1/central/landings/1/traffic", `{}`},
		{"GET", "/api/v1/central/certs", ""},
		{"GET", "/api/v1/central/traffic", ""},
		{"POST", "/api/v1/central/check", `{}`},
		{"POST", "/api/v1/central/fix", `{}`},
	}
	for _, p := range paths {
		if code := do(p.method, p.path, "", p.body); code != 401 {
			t.Errorf("%s %s 无令牌应 401，实际 %d", p.method, p.path, code)
		}
		if code := do(p.method, p.path, "Bearer rpt_0000000000000000000000000000000000000000000000000000000000000000", p.body); code != 401 {
			t.Errorf("%s %s 错令牌应 401，实际 %d", p.method, p.path, code)
		}
		if code := do(p.method, p.path, "Bearer "+tok, p.body); code == 401 {
			t.Errorf("%s %s 对令牌不应 401，实际 %d", p.method, p.path, code)
		}
	}
	// 令牌调设置类接口（模式切换）仍应 401：令牌权限没有扩大到设置
	if code := do("POST", "/api/v1/settings/manager-only", "Bearer "+tok, `{"manager_only":true}`); code != 401 {
		t.Fatalf("令牌调模式切换应 401，实际 %d", code)
	}
}

// ---------- 1.2.8：新代理接口的请求体校验 ----------

func TestValidateRemote128Bodies(t *testing.T) {
	// 补原始链接
	for _, c := range []struct {
		name string
		body string
		want bool
	}{
		{"空链接", `{"orig_link":""}`, false},
		{"链接格式不对", `{"orig_link":"not-a-link"}`, false},
		{"合法", `{"orig_link":"vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test"}`, true},
		{"合法+名称", `{"orig_link":"vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test","link_name":"香港-01"}`, true},
		{"名称超长", `{"orig_link":"vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test","link_name":"` + strings.Repeat("名", 65) + `"}`, false},
		{"未知字段", `{"orig_link":"vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test","zzz":1}`, false},
	} {
		err := validateRemoteOrigLinkBody([]byte(c.body))
		if c.want && err != nil {
			t.Errorf("补链接 %s：应通过，实际 %v", c.name, err)
		}
		if !c.want && err == nil {
			t.Errorf("补链接 %s：应拒绝，却通过了", c.name)
		}
	}
	// 续期
	for _, c := range []struct {
		name string
		body string
		want bool
	}{
		{"0 天", `{"days":0}`, false},
		{"3651 天", `{"days":3651}`, false},
		{"90 天", `{"days":90}`, true},
		{"3650 天", `{"days":3650}`, true},
	} {
		err := validateRemoteRenewBody([]byte(c.body))
		if c.want && err != nil {
			t.Errorf("续期 %s：应通过，实际 %v", c.name, err)
		}
		if !c.want && err == nil {
			t.Errorf("续期 %s：应拒绝，却通过了", c.name)
		}
	}
	// 续量
	if err := validateRemoteAddTrafficBody([]byte(`{"bytes":0}`)); err == nil {
		t.Errorf("续量 0 字节应拒绝")
	}
	if err := validateRemoteAddTrafficBody([]byte(`{"bytes":1073741824}`)); err != nil {
		t.Errorf("续量 1GB 应通过，实际 %v", err)
	}
	// 流量限制
	for _, c := range []struct {
		name string
		body string
		want bool
	}{
		{"负数", `{"limit_bytes":-1,"limit_mode":"total","speed_limit_mbps":0}`, false},
		{"模式非法", `{"limit_bytes":0,"limit_mode":"week","speed_limit_mbps":0}`, false},
		{"合法", `{"limit_bytes":107374182400,"limit_mode":"monthly","speed_limit_mbps":50}`, true},
		{"不限", `{"limit_bytes":0,"limit_mode":"total","speed_limit_mbps":0}`, true},
	} {
		err := validateRemoteSetTrafficBody([]byte(c.body))
		if c.want && err != nil {
			t.Errorf("流量限制 %s：应通过，实际 %v", c.name, err)
		}
		if !c.want && err == nil {
			t.Errorf("流量限制 %s：应拒绝，却通过了", c.name)
		}
	}
	// 一键修复
	if err := validateRemoteFixBody([]byte(`{"issue_code":"","landing":1}`)); err == nil {
		t.Errorf("修复空问题码应拒绝")
	}
	if err := validateRemoteFixBody([]byte(`{"issue_code":"restart_realm","landing":1}`)); err != nil {
		t.Errorf("修复合法请求应通过，实际 %v", err)
	}
}

// ---------- 1.2.8：新代理接口的版本门 ----------

func TestCentralRemote128VersionGate(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	rem := RemotePanel{ID: "rm-128", Name: "测试远程", BaseURL: "http://127.0.0.1:1", Token: tok}
	s.cfg.Remotes = []RemotePanel{rem}
	callRenew := func() map[string]any {
		req := httptest.NewRequest("POST", "/api/v1/central/remotes/rm-128/landings/1/renew", strings.NewReader(`{"days":90}`))
		req.SetPathValue("id", "rm-128")
		req.SetPathValue("index", "1")
		rec := httptest.NewRecorder()
		s.handleCentralRemoteRenew(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp
	}
	// 缓存版本 1.2.7：直接拒掉，不发请求，side=remote，文案写清要 1.2.8+
	s.centralMu.Lock()
	s.centralCache["rm-128"] = &RemoteStatusSnapshot{ID: "rm-128", Version: "1.2.7", Landings: []remoteLandingSummary{}}
	s.centralMu.Unlock()
	if r := callRenew(); r["ok"] == true || r["side"] != "remote" || !strings.Contains(r["error"].(string), "1.2.8") {
		t.Fatalf("远程 1.2.7 续期应拒掉并提示升 1.2.8：%v", r)
	}
	// 缓存版本 1.2.8：放行，走到连不上远程（side=local），说明版本门通过了
	s.centralMu.Lock()
	s.centralCache["rm-128"].Version = "1.2.8"
	s.centralMu.Unlock()
	if r := callRenew(); r["ok"] == true || r["side"] != "local" {
		t.Fatalf("远程 1.2.8 续期应放行到连远程（side=local）：%v", r)
	}
	// 远程返回 404（老面板没这个接口）：文案应写清要 1.2.8+，side=remote
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/landings/1/renew", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"ok":false,"error":"not found"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s.cfg.Remotes[0].BaseURL = srv.URL
	s.centralMu.Lock()
	delete(s.centralCache, "rm-128")
	s.centralMu.Unlock()
	if r := callRenew(); r["ok"] == true || r["side"] != "remote" || !strings.Contains(r["error"].(string), "1.2.8") {
		t.Fatalf("远程 404 续期应提示版本太旧要 1.2.8+：%v", r)
	}
}

// ---------- 1.2.8：远程落地摘要带全字段（子节点/流量/证书/到期） ----------

const fakeRemoteStatus128 = `{"ok":true,"panel_version":"1.2.8","realm":{"running":true},"disk_pct":42.5,"relay_addr":"203.0.113.9","landings":[{"index":1,"landing":"203.0.113.1","name":"智利","domain":"example.com","cert_domain":"example.com","cert_mode":"dns01","preset":"1","sb_mode":"","custom_ports":"","want_udp":false,"mode":"standard","ports":[{"proto":"tcp","listen":443,"remote":443}],"online":true,"latency_ms":12,"cert_days_left":89,"cert_expiry":"2027-02-16","renew_mode":"自动（DNS 验证）","expiry":"2027-02-16T00:00:00Z","days_left":90,"orig_link":"vless://11111111-2222-3333-4444-555555555555@example.com:443?security=tls&sni=example.com#test","orig_link_name":"智利","speed_limit_mbps":100,"traffic":{"today_up":100,"today_down":200,"month_up":3000,"month_down":4000,"total_up":50000,"total_down":60000,"limit_bytes":107374182400,"limit_mode":"monthly","used_bytes":7000,"pct":0.006},"disabled":false,"disabled_reason":"","subnodes":[{"domain":"node2.example.com","created_at":"2026-11-19T00:00:00Z","note":""}]}]}`

func TestFetchRemoteStatusExtendedFields(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeRemoteStatus128))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	rem := RemotePanel{ID: "rm-128", Name: "测试远程", BaseURL: srv.URL, Token: generateRemoteToken()}
	snap, err := s.fetchRemoteStatus(rem)
	if err != nil {
		t.Fatalf("拉远程状态应成功：%v", err)
	}
	if snap.RelayAddr != "203.0.113.9" {
		t.Fatalf("快照应带 relay_addr，实际 %q", snap.RelayAddr)
	}
	if len(snap.Landings) != 1 {
		t.Fatalf("应有 1 个落地，实际 %d", len(snap.Landings))
	}
	l := snap.Landings[0]
	if l.Name != "智利" || l.CertMode != "dns01" || l.Preset != "1" {
		t.Fatalf("摘要字段不对：%+v", l)
	}
	if l.CertDaysLeft == nil || *l.CertDaysLeft != 89 {
		t.Fatalf("证书剩余天数应 89，实际 %v", l.CertDaysLeft)
	}
	if l.RenewMode != "自动（DNS 验证）" || l.DaysLeft == nil || *l.DaysLeft != 90 {
		t.Fatalf("续签方式/到期天数不对：%q %v", l.RenewMode, l.DaysLeft)
	}
	if l.OrigLink == "" || l.OrigLinkName != "智利" || l.SpeedLimitMbps != 100 {
		t.Fatalf("原始链接/节点名称/限速不对：%+v", l)
	}
	if l.Traffic.LimitBytes != 107374182400 || l.Traffic.LimitMode != "monthly" || l.Traffic.UsedBytes != 7000 {
		t.Fatalf("流量快照不对：%+v", l.Traffic)
	}
	if len(l.SubNodes) != 1 || l.SubNodes[0].Domain != "node2.example.com" {
		t.Fatalf("子节点列表不对：%+v", l.SubNodes)
	}
	// 名称与地址相同时回退成空（前端据此只显示地址）
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"panel_version":"1.2.8","realm":{"running":true},"disk_pct":10,"landings":[{"index":1,"landing":"203.0.113.1","name":"203.0.113.1","mode":"standard","ports":[]}]}`))
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	rem.BaseURL = srv2.URL
	snap2, err := s.fetchRemoteStatus(rem)
	if err != nil {
		t.Fatalf("拉远程状态应成功：%v", err)
	}
	if snap2.Landings[0].Name != "" {
		t.Fatalf("名称与地址相同时 Name 应清掉，实际 %q", snap2.Landings[0].Name)
	}
}

// ---------- 1.2.8：管理面板（无本地 relay.conf）也能建纯远程合并订阅 ----------

func TestValidateMergedSelectionNoLocalRelayConf(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeRemoteStatus128))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// realmDir 里不放 relay.conf：模拟管理面板模式（本机没装中转）
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	s.cfg.Remotes = []RemotePanel{{ID: "rm-128", Name: "测试远程", BaseURL: srv.URL, Token: generateRemoteToken()}}
	// 只勾远程落地：应通过（以前会报“relay.conf 不存在或读不到”）
	if _, _, err := s.validateMergedSelection([]SubSelection{{Panel: "rm-128", Landing: 1}}); err != nil {
		t.Fatalf("纯远程勾选应通过，实际 %v", err)
	}
	// 勾本面板落地：本机没有落地，应按“本面板没有编号 1 的落地”拒绝
	if _, _, err := s.validateMergedSelection([]SubSelection{{Panel: "local", Landing: 1}}); err == nil || !strings.Contains(err.Error(), "本面板没有编号 1 的落地") {
		t.Fatalf("勾本面板落地应报没有该编号，实际 %v", err)
	}
	// 勾不存在的远程落地：应拒绝
	if _, _, err := s.validateMergedSelection([]SubSelection{{Panel: "rm-128", Landing: 99}}); err == nil || !strings.Contains(err.Error(), "没有编号 99 的落地") {
		t.Fatalf("勾不存在的远程落地应拒绝，实际 %v", err)
	}
	// 空勾选：应拒绝
	if _, _, err := s.validateMergedSelection(nil); err == nil {
		t.Fatalf("空勾选应拒绝")
	}
}

// ---------- 1.2.8：代理续期原样透传到被管面板 ----------

func TestCentralRemoteRenewPassthrough(t *testing.T) {
	var gotBody string
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeRemoteStatus128))
	})
	mux.HandleFunc("/api/v1/central/landings/1/renew", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"expiry":"2027-05-17T00:00:00Z"}`))
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
	req2 := httptest.NewRequest("POST", "/api/v1/central/remotes/"+id+"/landings/1/renew", strings.NewReader(`{"days":90}`))
	req2.SetPathValue("id", id)
	req2.SetPathValue("index", "1")
	rec2 := httptest.NewRecorder()
	s.handleCentralRemoteRenew(rec2, req2)
	var resp2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if resp2["ok"] != true {
		t.Fatalf("代理续期应成功：%v", resp2)
	}
	if gotPath != "/api/v1/central/landings/1/renew" {
		t.Fatalf("应调被管面板的新 central 续期接口，实际 %s", gotPath)
	}
	if gotBody != `{"days":90}` {
		t.Fatalf("续期请求体应原样透传，实际 %s", gotBody)
	}
	// 等后台轮询跑完再返回，否则轮询可能撞上假服务器关闭与 TempDir 清理
	waitCentralPolls(t, s)
}
