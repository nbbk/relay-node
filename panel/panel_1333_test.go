package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1.3.33：分配端口前问远程、登记时导入远程转发 ----------
// 根因：管理面板 allocateUserPortForPanel 只避让自己记录里该远程的
// 端口，看不见远程自己建的转发、也看不见撤销重登记后换了面板 id 的
// 旧记录；分了已占的端口，远程收到就跳过（applied 0），订阅只能走
// 被堵的共享口 24443。修法：分配前问远程要已占端口表（远程说了算），
// 登记远程时把远程已有的转发记录导进来。

// ---------- 远程只读接口 ----------

func TestCentralUserForwardsList1333(t *testing.T) {
	s := newTestSubServer(t)
	s.cfg.UserForwards = []UserForward{
		{Key: "merged:ms-a", SubKind: "merged", SubID: "ms-a", Panel: "local", LandingAddr: "203.0.113.1", ListenPort: 30000, LimitBytes: 1024, LimitMode: "total", ResetMode: "monthly", ResetDay: 1},
		{Key: "subuser:su-b", SubKind: "subuser", SubID: "su-b", Panel: "local", LandingAddr: "203.0.113.2", ListenPort: 30001, LimitBytes: 2048, LimitMode: "total", ResetMode: "monthly", ResetDay: 1},
	}
	req := httptest.NewRequest("GET", "/api/v1/central/user-forwards", nil)
	rec := httptest.NewRecorder()
	s.handleCentralUserForwardsList(rec, req)
	if rec.Code != 200 {
		t.Fatalf("状态应为 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK        bool          `json:"ok"`
		Forwards  []UserForward `json:"forwards"`
		UsedPorts []int         `json:"used_ports"`
		PanelVer  string        `json:"panel_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if !resp.OK || len(resp.Forwards) != 2 {
		t.Fatalf("应返回 2 条转发，实际 %+v", resp)
	}
	if resp.PanelVer != "1.4.16" {
		t.Fatalf("panel_version 应为 1.4.16，实际 %q", resp.PanelVer)
	}
	used := map[int]bool{}
	for _, p := range resp.UsedPorts {
		used[p] = true
	}
	// 转发端口 + 落地监听端口（80/443/8388）+ 面板端口 11111 都应在表里。
	for _, p := range []int{30000, 30001, 80, 443, 8388, 11111} {
		if !used[p] {
			t.Fatalf("used_ports 里应有 %d，实际 %v", p, resp.UsedPorts)
		}
	}
	if strings.Contains(rec.Body.String(), "rpt_") {
		t.Fatalf("响应里不应出现令牌")
	}
}

// ---------- 分配前问远程 ----------

func newFakeRemoteUserForwards1333(t *testing.T, withEndpoint bool) (*httptest.Server, *seenCalls) {
	t.Helper()
	seen := &seenCalls{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		seen.add("status:" + r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeRemoteStatus))
	})
	if withEndpoint {
		mux.HandleFunc("/api/v1/central/user-forwards", func(w http.ResponseWriter, r *http.Request) {
			seen.add("user-forwards:" + r.Method)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"panel_version":"1.3.33","forwards":[
				{"key":"merged:ms-1219e664f595","sub_kind":"merged","sub_id":"ms-1219e664f595","sub_name":"合并一","panel":"local","landing_addr":"203.0.113.1","listen_port":30000,"limit_bytes":1073741824,"limit_mode":"total","reset_mode":"monthly","reset_day":1},
				{"key":"merged:ms-b49cf37de2ae","sub_kind":"merged","sub_id":"ms-b49cf37de2ae","sub_name":"合并二","panel":"local","landing_addr":"203.0.113.1","listen_port":30001,"limit_bytes":1073741824,"limit_mode":"total","reset_mode":"monthly","reset_day":1},
				{"key":"merged:ms-7b3454c6d40b","sub_kind":"merged","sub_id":"ms-7b3454c6d40b","sub_name":"合并三","panel":"local","landing_addr":"203.0.113.1","listen_port":30002,"limit_bytes":1073741824,"limit_mode":"total","reset_mode":"monthly","reset_day":1}
			],"used_ports":[80,443,30000,30001,30002,11111]}`))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestAllocateUserPortForPanelRemote1333(t *testing.T) {
	srv, seen := newFakeRemoteUserForwards1333(t, true)
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	rem := RemotePanel{ID: "rm-1333", Name: "测试远程", BaseURL: srv.URL, Token: tok}
	s.cfg.Remotes = append(s.cfg.Remotes, rem)
	// 远程已占 30000/30001/30002 → 应分到 30003。
	port, err := s.allocateUserPortForPanel("rm-1333")
	if err != nil {
		t.Fatalf("分配应成功：%v", err)
	}
	if port != 30003 {
		t.Fatalf("远程已占 30000–30002，应分到 30003，实际 %d", port)
	}
	// 本机记录里再占一个 30003 → 应分到 30004（两边都避让）。
	s.cfg.UserForwards = append(s.cfg.UserForwards, UserForward{Key: "merged:ms-x", SubKind: "merged", SubID: "ms-x", Panel: "rm-1333", LandingAddr: "203.0.113.1", ListenPort: 30003})
	port, err = s.allocateUserPortForPanel("rm-1333")
	if err != nil || port != 30004 {
		t.Fatalf("本地再占 30003 后应分到 30004，实际 port=%d err=%v", port, err)
	}
	// 确实问过远程。
	found := false
	for _, c := range seen.list() {
		if strings.HasPrefix(c, "user-forwards:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("分配前应调远程的 user-forwards 接口，实际调用：%v", seen.list())
	}
}

func TestAllocateUserPortForPanelFallback1333(t *testing.T) {
	// 旧远程（无此接口，404）与连不上的远程都回退本地记录，不报错。
	srv, _ := newFakeRemoteUserForwards1333(t, false)
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	rem := RemotePanel{ID: "rm-old", Name: "旧远程", BaseURL: srv.URL, Token: tok}
	s.cfg.Remotes = append(s.cfg.Remotes, rem)
	s.cfg.UserForwards = append(s.cfg.UserForwards, UserForward{Key: "merged:ms-x", SubKind: "merged", SubID: "ms-x", Panel: "rm-old", LandingAddr: "203.0.113.1", ListenPort: 30000})
	port, err := s.allocateUserPortForPanel("rm-old")
	if err != nil || port != 30001 {
		t.Fatalf("旧远程 404 时应回退本地记录分到 30001，实际 port=%d err=%v", port, err)
	}
	// 连不上的远程同样回退。
	dead := httptest.NewServer(http.NewServeMux())
	deadURL := dead.URL
	dead.Close()
	s.cfg.Remotes = append(s.cfg.Remotes, RemotePanel{ID: "rm-dead", Name: "掉线远程", BaseURL: deadURL, Token: tok})
	s.cfg.UserForwards = append(s.cfg.UserForwards, UserForward{Key: "merged:ms-y", SubKind: "merged", SubID: "ms-y", Panel: "rm-dead", LandingAddr: "203.0.113.1", ListenPort: 30000})
	port, err = s.allocateUserPortForPanel("rm-dead")
	if err != nil || port != 30001 {
		t.Fatalf("远程连不上时应回退本地记录分到 30001，实际 port=%d err=%v", port, err)
	}
}

// ---------- 登记时导入 ----------

func TestImportRemoteUserForwards1333(t *testing.T) {
	srv, _ := newFakeRemoteUserForwards1333(t, true)
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	rem := RemotePanel{ID: "rm-1333", Name: "测试远程", BaseURL: srv.URL, Token: tok}
	// 第一次导入：3 条全进来，Panel 写成该远程的 id。
	imported, updated, err := s.importRemoteUserForwards(rem)
	if err != nil {
		t.Fatalf("导入应成功：%v", err)
	}
	if imported != 3 || updated != 0 {
		t.Fatalf("第一次应导入 3 条、更新 0 条，实际 imported=%d updated=%d", imported, updated)
	}
	for _, f := range s.cfg.UserForwards {
		if f.Panel != "rm-1333" {
			t.Fatalf("导入的记录 Panel 应是 rm-1333，实际 %+v", f)
		}
	}
	// 再导一次：按 (Key, ListenPort) 去重，0 条新增、3 条更新。
	imported, updated, err = s.importRemoteUserForwards(rem)
	if err != nil || imported != 0 || updated != 3 {
		t.Fatalf("重复导入应 0 新增 3 更新，实际 imported=%d updated=%d err=%v", imported, updated, err)
	}
	if len(s.cfg.UserForwards) != 3 {
		t.Fatalf("重复导入不应产生重复记录，实际 %d 条", len(s.cfg.UserForwards))
	}
	// 本机记录端口变旧了（同订阅同落地、端口已变）：以远程的为准，连端口一起刷新。
	for i := range s.cfg.UserForwards {
		if s.cfg.UserForwards[i].Key == "merged:ms-1219e664f595" {
			s.cfg.UserForwards[i].ListenPort = 30005
		}
	}
	imported, updated, err = s.importRemoteUserForwards(rem)
	if err != nil || imported != 0 || updated != 3 {
		t.Fatalf("端口变旧后导入应刷新，实际 imported=%d updated=%d err=%v", imported, updated, err)
	}
	found := false
	for _, f := range s.cfg.UserForwards {
		if f.Key == "merged:ms-1219e664f595" && f.ListenPort == 30000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("ms-1219e664f595 的端口应被远程的 30000 刷新回来，实际 %+v", s.cfg.UserForwards)
	}
	// 旧远程（无此接口）：导入失败返回错误。
	srvOld, _ := newFakeRemoteUserForwards1333(t, false)
	remOld := RemotePanel{ID: "rm-old", Name: "旧远程", BaseURL: srvOld.URL, Token: tok}
	if _, _, err := s.importRemoteUserForwards(remOld); err == nil {
		t.Fatalf("旧远程无接口时导入应返回错误")
	}
}

func TestCentralRemotesAddImports1333(t *testing.T) {
	srv, _ := newFakeRemoteUserForwards1333(t, true)
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
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
	if imported, _ := resp["imported"].(float64); imported != 3 {
		t.Fatalf("登记应导入 3 条转发，实际 resp=%v", resp)
	}
	if len(s.cfg.UserForwards) != 3 {
		t.Fatalf("登记后本机应有 3 条导入的转发记录，实际 %d 条", len(s.cfg.UserForwards))
	}
	for _, f := range s.cfg.UserForwards {
		if f.Panel != id {
			t.Fatalf("导入记录的 Panel 应是新登记的 id %s，实际 %+v", id, f)
		}
	}
	waitCentralPolls(t, s)

	// 旧远程：导不进来也不阻断登记，响应里带 import_warning。
	srvOld, _ := newFakeRemoteUserForwards1333(t, false)
	s2 := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	body2 := `{"name":"旧远程","base_url":"` + srvOld.URL + `","token":"` + tok + `"}`
	req2 := httptest.NewRequest("POST", "/api/v1/central/remotes", strings.NewReader(body2))
	rec2 := httptest.NewRecorder()
	s2.handleCentralRemotesAdd(rec2, req2)
	var resp2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if resp2["ok"] != true {
		t.Fatalf("旧远程登记也应成功（导入失败不阻断）：%v", resp2)
	}
	if w, _ := resp2["import_warning"].(string); w == "" {
		t.Fatalf("旧远程登记响应里应带 import_warning，实际 %v", resp2)
	}
	if len(s2.cfg.UserForwards) != 0 {
		t.Fatalf("旧远程导不进来时不应有导入记录，实际 %d 条", len(s2.cfg.UserForwards))
	}
	waitCentralPolls(t, s2)
}

// ---------- config.toml 写两遍（tcp+udp 同端口只写一段） ----------

func TestAppendEndpointsForLandingDedup1333(t *testing.T) {
	l := Landing{Landing: "203.0.113.1", Ports: []PortSpec{
		{Proto: "tcp", Listen: 24443, Remote: 24443},
		{Proto: "udp", Listen: 24443, Remote: 24443},
		{Proto: "tcp", Listen: 443, Remote: 443},
		{Proto: "udp", Listen: 443, Remote: 443},
	}}
	out := appendEndpointsForLanding(nil, l, false, false)
	if n := strings.Count(string(out), "0.0.0.0:24443"); n != 1 {
		t.Fatalf("tcp+udp 同端口应只写一段 24443，实际 %d 段：\n%s", n, out)
	}
	if n := strings.Count(string(out), "0.0.0.0:443"); n != 1 {
		t.Fatalf("tcp+udp 同端口应只写一段 443，实际 %d 段：\n%s", n, out)
	}
	if n := strings.Count(string(out), "[[endpoints]]"); n != 2 {
		t.Fatalf("应共 2 段，实际 %d 段：\n%s", n, out)
	}
}

func TestApplyUserForwardsDedup1333(t *testing.T) {
	fakeSystemctl122(t)
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir, "nodes.txt"), []byte(subTestNodesTxt), 0o644); err != nil {
		t.Fatal(err)
	}
	relayConf := "LANDING=203.0.113.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"\"\nCERT_DOMAIN=\"example.com\"\nPRESET=3\nMODE_NOTE=\"自定义端口\"\nPORTS=\"tcp:24443:24443 udp:24443:24443\"\nLANDING_COUNT=1\nALL_PORTS=\"tcp:24443:24443 udp:24443:24443\"\nSHARE80=0\nSHARE443=0\n"
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(relayConf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestAddLandingServer(t, kitDir, realmDir)
	s.cfg.UserForwards = []UserForward{
		{Key: "subuser:su-x", SubKind: "subuser", SubID: "su-x", SubName: "测试用户", Panel: "local", LandingAddr: "203.0.113.1", ListenPort: 30000, LimitBytes: 1073741824, LimitMode: "total", ResetMode: "monthly", ResetDay: 1, NextResetAt: "2026-12-01T00:00:00Z"},
	}
	if err := s.applyUserForwards(); err != nil {
		t.Fatalf("applyUserForwards 应成功：%v", err)
	}
	tomlB, err := os.ReadFile(filepath.Join(realmDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(tomlB), "0.0.0.0:30000"); n != 1 {
		t.Fatalf("tcp+udp 同节点端口的用户转发应只写一段 30000，实际 %d 段：\n%s", n, tomlB)
	}
	if n := strings.Count(string(tomlB), "[[endpoints]]"); n != 1 {
		t.Fatalf("应共 1 段，实际 %d 段：\n%s", n, tomlB)
	}
}
