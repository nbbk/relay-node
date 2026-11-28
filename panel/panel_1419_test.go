package main

// v1.4.19：管理面板代理视图（切片 9）与 ensureUseUDP 缺口修补。
//
// 覆盖：
//   - 版本断言 1.4.19。
//   - ensureUseUDP 缺行场景（缺行补上、已有 false 行翻 true、已有 true
//     行不动、needUDP=false 缺行不动、[network] 节无行插节内、注释行
//     里的 [network] 不算节）与 udp_disabled 一键修复缺行时自动补行。
//   - 节点摘要进 central/status（播种节点＋用户流量，断言 health／流量）。
//   - fetchRemoteStatus 解析 nodes 与 NodesSupported（1.4.19 远端→true；
//     1.4.18 远端→false；nodes 为空数组）。
//   - 汇总聚合接口 GET /api/v1/central/nodes（online／offline_cached／
//     never 三种 state、remote_id 过滤、未登记 404、返回里无令牌明文、
//     local 本机节点）。
//   - 详情代理 GET /api/v1/central/nodes/{remote_id}/{node_id}（fake
//     远端透传、未登记 404、远端 404→版本门提示、缓存版本太旧→版本门拒绝）。
//   - 被管端详情 GET /api/v1/central/nodes/{id}（经 s.routes()＋Bearer
//     令牌调通、错令牌 401、节点不存在 404）。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestVersion1419(t *testing.T) {
	if Version != "1.4.42" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
}

// ---------- ensureUseUDP 缺口（B 部分） ----------

func TestEnsureUseUDPMissingLine1419(t *testing.T) {
	// 缺行＋needUDP：已有 [network] 节无 use_udp 行 → 插进该节，不重复建节。
	in := "[network]\nno_tcp = false\n\n[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"127.0.0.1:24443\"\n"
	out, changed := ensureUseUDP([]byte(in), true)
	if !changed || !strings.Contains(string(out), "[network]\nuse_udp = true") {
		t.Fatalf("缺行时应补 use_udp = true，changed=%v，输出：%s", changed, out)
	}
	if strings.Count(string(out), "[network]") != 1 {
		t.Fatalf("不应重复建 [network] 节：%s", out)
	}
	// 缺行＋连 [network] 节都没有 → 文件末尾追加 [network] 小节。
	in2 := "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"127.0.0.1:24443\"\n"
	out2, changed2 := ensureUseUDP([]byte(in2), true)
	if !changed2 || !strings.Contains(string(out2), "[network]\nuse_udp = true") {
		t.Fatalf("无节时应末尾追加 [network] 小节，changed=%v，输出：%s", changed2, out2)
	}
	// 已有 false 行 → 翻成 true。
	out3, changed3 := ensureUseUDP([]byte(toml1346(false)), true)
	if !changed3 || !strings.Contains(string(out3), "use_udp = true") {
		t.Fatalf("false 行应翻成 true，changed=%v，输出：%s", changed3, out3)
	}
	// 已有 true 行 → 不动。
	in4 := toml1346(true)
	out4, changed4 := ensureUseUDP([]byte(in4), true)
	if changed4 || string(out4) != in4 {
		t.Fatalf("true 行应不动，changed=%v，输出：%s", changed4, out4)
	}
	// 缺行＋needUDP=false → 不动（“不需要时绝不乱关”口径不变）。
	out5, changed5 := ensureUseUDP([]byte(in2), false)
	if changed5 || string(out5) != in2 {
		t.Fatalf("needUDP=false 缺行时应不动，changed=%v，输出：%s", changed5, out5)
	}
	// 注释行里的 [network] 不算节 → 末尾追加真节。
	in6 := "# [network] 是注释\n[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"127.0.0.1:24443\"\n"
	out6, changed6 := ensureUseUDP([]byte(in6), true)
	if !changed6 || !strings.Contains(string(out6), "[network]\nuse_udp = true") {
		t.Fatalf("注释里的 [network] 不应算节，changed=%v，输出：%s", changed6, out6)
	}
}

// tomlNoUseUDP1419 最小 config.toml：有 [network] 节、有一个 endpoint，
// 但根本没有 use_udp 行（不是 false，是缺行——中转机真机上的情况）。
func tomlNoUseUDP1419() string {
	return "[network]\nno_tcp = false\n\n[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"127.0.0.1:24443\"\n"
}

// TestFixUDPDisabledMissingLine1419 缺行时一键修复应自动补行、返回成功，
// 不再报 need_manual（v1.4.18 的缺口：setUseUDP 只改已有行）。
func TestFixUDPDisabledMissingLine1419(t *testing.T) {
	s := newTestServer144(t)
	logPath := filepath.Join(t.TempDir(), "systemctl.log")
	stubBin1346(t, logPath)
	writeRealmFiles1346(t, s, relayConf1346("127.0.0.1", "tcp:24443:24443 udp:24443:24443", "NEEDS_UDP=1\n"), tomlNoUseUDP1419())
	h := s.routes()
	cookie := loginTestCookie(t, h)
	code, resp := fix1346(t, h, cookie, "udp_disabled", 1)
	if code != 200 {
		t.Fatalf("udp_disabled 缺行时应 200，实际 %d：%v", code, resp)
	}
	if resp["ok"] != true {
		t.Fatalf("udp_disabled 缺行时应成功（自动补行），实际 %v", resp)
	}
	b, err := os.ReadFile(filepath.Join(s.env.RealmDir, "config.toml"))
	if err != nil {
		t.Fatalf("读 config.toml：%v", err)
	}
	if !strings.Contains(string(b), "use_udp = true") {
		t.Fatalf("修复后 config.toml 应含 use_udp = true：%s", b)
	}
}

// ---------- 节点摘要进 central/status ----------

// TestCentralStatusNodes1419 播种节点＋用户流量，调 handleCentralStatus，
// 断言 nodes 里该节点的 health 与本期流量（与节点详情同一块流量）。
func TestCentralStatusNodes1419(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	u1, _, _ := seedUsers1402(s)
	id, _ := createNode1400(t, h, cookie, "美国01", "23.129.180.189")
	s.nodes.mu.Lock()
	n := s.nodes.getNode(id)
	n.LastSeen = time.Now().Unix()
	// 新建节点默认 UserIDs 为空（老用户不自动上新节点）：显式把用户指派上去，
	// 流量块才会算他（与节点页“发到节点”同一口径）。
	n.UserIDs = []string{u1}
	s.nodes.mu.Unlock()
	s.userTraffic.data[u1] = &userTrafficRec{UpPeriod: 1000, DownPeriod: 2000, UpTotal: 10000, DownTotal: 20000}
	// /api/v1/central/status 是令牌认证（被管端接口），不是会话认证。
	tok := generateRemoteToken()
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)
	req := httptest.NewRequest("GET", "/api/v1/central/status", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("central/status 应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	m := respMap1400(t, rec.Body.String())
	nodes, _ := m["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("应有 1 个节点摘要：%v", m)
	}
	row, _ := nodes[0].(map[string]any)
	if row["name"] != "美国01" || row["health"] != "online" {
		t.Fatalf("节点摘要名／健康不对：%v", row)
	}
	tr, _ := row["traffic"].(map[string]any)
	if tr == nil || tr["up_period"] != float64(1000) || tr["down_period"] != float64(2000) || tr["up_total"] != float64(10000) {
		t.Fatalf("节点摘要本期／累计流量不对：%v", tr)
	}
}

// ---------- fetchRemoteStatus 解析 nodes ----------

// fakeRemoteStatus1419 返回固定 central/status 体的 fake 远程面板。
func fakeRemoteStatus1419(t *testing.T, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchRemoteStatusNodes1419(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	// 带 nodes 的 1.4.19 远端 → 解析出节点、NodesSupported=true。
	srv := fakeRemoteStatus1419(t, `{"ok":true,"panel_version":"1.4.19","realm":{"running":true},"disk_pct":10,"landings":[],"nodes":[{"node_id":7,"name":"美国01","health":"online","traffic":{"up_period":100,"down_period":200,"up_total":1000,"down_total":2000}}]}`)
	snap, err := s.fetchRemoteStatus(RemotePanel{ID: "rm-1419", Name: "测试远程", BaseURL: srv.URL, Token: generateRemoteToken()})
	if err != nil {
		t.Fatalf("fetchRemoteStatus：%v", err)
	}
	if !snap.NodesSupported {
		t.Fatalf("1.4.19 远端应 NodesSupported=true：%+v", snap)
	}
	if len(snap.Nodes) != 1 || snap.Nodes[0].NodeID != 7 || snap.Nodes[0].Name != "美国01" || snap.Nodes[0].Health != "online" {
		t.Fatalf("节点摘要解析不对：%+v", snap.Nodes)
	}
	if snap.Nodes[0].Traffic.UpPeriod != 100 || snap.Nodes[0].Traffic.DownPeriod != 200 || snap.Nodes[0].Traffic.UpTotal != 1000 {
		t.Fatalf("节点流量解析不对：%+v", snap.Nodes[0].Traffic)
	}
	// 1.4.18 远端（没有 nodes 字段）→ NodesSupported=false、nodes 为空数组。
	srv2 := fakeRemoteStatus1419(t, `{"ok":true,"panel_version":"1.4.18","realm":{"running":true},"disk_pct":10,"landings":[]}`)
	snap2, err := s.fetchRemoteStatus(RemotePanel{ID: "rm-1418", Name: "旧远程", BaseURL: srv2.URL, Token: generateRemoteToken()})
	if err != nil {
		t.Fatalf("fetchRemoteStatus（旧版）：%v", err)
	}
	if snap2.NodesSupported {
		t.Fatalf("1.4.18 远端应 NodesSupported=false：%+v", snap2)
	}
	if len(snap2.Nodes) != 0 {
		t.Fatalf("旧版远端 nodes 应为空数组：%+v", snap2.Nodes)
	}
}

// ---------- 汇总聚合接口 ----------

// seedCentralCache1419 往 centralCache 里放一台远程的快照。
func seedCentralCache1419(s *Server, id string, snap *RemoteStatusSnapshot) {
	s.centralMu.Lock()
	defer s.centralMu.Unlock()
	s.centralCache[id] = snap
}

func TestCentralNodesAggregate1419(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	tok := generateRemoteToken()
	s.cfg.Remotes = append(s.cfg.Remotes,
		RemotePanel{ID: "rm-online", Name: "在线远程", BaseURL: "http://127.0.0.1:9", Token: tok},
		RemotePanel{ID: "rm-offline", Name: "离线远程", BaseURL: "http://127.0.0.1:9", Token: tok},
		RemotePanel{ID: "rm-never", Name: "没拉过", BaseURL: "http://127.0.0.1:9", Token: tok},
	)
	now := time.Now().UTC().Format(time.RFC3339)
	seedCentralCache1419(s, "rm-online", &RemoteStatusSnapshot{ID: "rm-online", Online: true, Version: "1.4.19", LastCheckedAt: now, LastOKAt: now, NodesSupported: true,
		Nodes: []centralNodeSummary{{NodeID: 1, Name: "美国01", Health: "online", Traffic: centralNodeTraffic{UpPeriod: 10, DownPeriod: 20, UpTotal: 100, DownTotal: 200}}}})
	seedCentralCache1419(s, "rm-offline", &RemoteStatusSnapshot{ID: "rm-offline", Online: false, Version: "1.4.19", LastCheckedAt: now, LastOKAt: now, NodesSupported: true,
		Nodes: []centralNodeSummary{{NodeID: 2, Name: "智利", Health: "offline", Traffic: centralNodeTraffic{UpPeriod: 30, DownPeriod: 40}}}})
	// rm-never 不放快照：从没拉过。

	// 全量：三台、三种 state、local 本机节点、返回里无令牌明文。
	code, body := authed1400(t, h, cookie, "GET", "/api/v1/central/nodes", "")
	if code != 200 {
		t.Fatalf("汇总接口应 200，实际 %d：%s", code, body)
	}
	if strings.Contains(body, tok) {
		t.Fatalf("汇总接口返回里出现了令牌明文")
	}
	m := respMap1400(t, body)
	remotes, _ := m["remotes"].([]any)
	if len(remotes) != 3 {
		t.Fatalf("应有 3 台远程：%v", m)
	}
	states := map[string]string{}
	for _, r := range remotes {
		rm, _ := r.(map[string]any)
		states[rm["remote_id"].(string)] = rm["state"].(string)
	}
	if states["rm-online"] != "online" || states["rm-offline"] != "offline_cached" || states["rm-never"] != "never" {
		t.Fatalf("三种 state 不对：%v", states)
	}
	local, _ := m["local"].(map[string]any)
	if local == nil {
		t.Fatalf("全量返回应带 local 本机节点：%v", m)
	}
	if ln, _ := local["nodes"].([]any); ln == nil {
		t.Fatalf("local.nodes 应为数组（没有节点时为空数组）：%v", local)
	}
	// remote_id 过滤：只回那一台。
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/central/nodes?remote_id=rm-offline", "")
	if code != 200 {
		t.Fatalf("remote_id 过滤应 200，实际 %d：%s", code, body)
	}
	m = respMap1400(t, body)
	if m["remote_id"] != "rm-offline" || m["state"] != "offline_cached" {
		t.Fatalf("过滤返回不对：%v", m)
	}
	nodes, _ := m["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("离线远程应保留上次拉到的 1 个节点：%v", m)
	}
	// 未登记 → 404。
	code, _ = authed1400(t, h, cookie, "GET", "/api/v1/central/nodes?remote_id=rm-nope", "")
	if code != 404 {
		t.Fatalf("未登记的 remote_id 应 404，实际 %d", code)
	}
	// 未登录 → 401。
	code, _ = authed1400(t, h, "", "GET", "/api/v1/central/nodes", "")
	if code != 401 {
		t.Fatalf("未登录汇总接口应 401，实际 %d", code)
	}
}

// ---------- 详情代理 ----------

// fakeRemoteNodeDetail1419 fake 远程面板：/api/v1/central/nodes/7 返回
// 固定详情，其余节点 id 一律 404（模拟老面板没有该节点／接口）。
func fakeRemoteNodeDetail1419(t *testing.T, tok string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/nodes/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+tok {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"ok":false,"error":"令牌不对"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"node":{"id":7,"name":"美国01","health":"online"},"inbounds":[],"traffic":{"up_period":100,"down_period":200,"up_total":1000,"down_total":2000},"backend_health":{"status":"online","detail":"在线"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCentralNodeDetailProxy1419(t *testing.T) {
	tok := generateRemoteToken()
	srv := fakeRemoteNodeDetail1419(t, tok)
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	s.cfg.Remotes = append(s.cfg.Remotes, RemotePanel{ID: "rm-1419", Name: "测试远程", BaseURL: srv.URL, Token: tok})
	now := time.Now().UTC().Format(time.RFC3339)
	seedCentralCache1419(s, "rm-1419", &RemoteStatusSnapshot{ID: "rm-1419", Online: true, Version: "1.4.19", LastCheckedAt: now, LastOKAt: now, NodesSupported: true})

	// 透传：fake 远端详情原样回来。
	code, body := authed1400(t, h, cookie, "GET", "/api/v1/central/nodes/rm-1419/7", "")
	if code != 200 {
		t.Fatalf("详情代理应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	if m["ok"] != true {
		t.Fatalf("详情代理应透传成功：%v", m)
	}
	node, _ := m["node"].(map[string]any)
	if node == nil || node["name"] != "美国01" {
		t.Fatalf("透传的节点详情不对：%v", m)
	}
	tr, _ := m["traffic"].(map[string]any)
	if tr == nil || tr["up_period"] != float64(100) || tr["down_period"] != float64(200) {
		t.Fatalf("透传的流量不对：%v", m)
	}
	// 远端 404（节点不存在）→ 版本门提示（需要 1.4.19+）。
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/central/nodes/rm-1419/99", "")
	if code != 200 {
		t.Fatalf("远端 404 时代理应 200 包一层错误，实际 %d：%s", code, body)
	}
	m = respMap1400(t, body)
	if m["ok"] != false || !strings.Contains(fmt.Sprint(m["error"]), "需要 1.4.19+") {
		t.Fatalf("远端 404 应按版本门提示升级：%v", m)
	}
	// 未登记 → 404。
	code, _ = authed1400(t, h, cookie, "GET", "/api/v1/central/nodes/rm-nope/7", "")
	if code != 404 {
		t.Fatalf("未登记的远程详情代理应 404，实际 %d", code)
	}
	// 缓存版本太旧 → 版本门直接拒绝（不调远程）。
	seedCentralCache1419(s, "rm-1419", &RemoteStatusSnapshot{ID: "rm-1419", Online: true, Version: "1.4.18", LastCheckedAt: now, LastOKAt: now, NodesSupported: false})
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/central/nodes/rm-1419/7", "")
	m = respMap1400(t, body)
	if code != 200 || m["ok"] != false || !strings.Contains(fmt.Sprint(m["error"]), "需要 1.4.19+") {
		t.Fatalf("缓存版本太旧应按版本门拒绝：%d %v", code, m)
	}
}

// ---------- 被管端详情（令牌认证） ----------

func TestCentralNodeDetailToken1419(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id, _ := createNode1400(t, h, cookie, "美国01", "23.129.180.189")
	tok := generateRemoteToken()
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)

	do := func(auth, path string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	// 正确令牌 → 200，形状与本地节点详情一致。
	code, body := do("Bearer "+tok, "/api/v1/central/nodes/"+strconv.FormatInt(id, 10))
	if code != 200 {
		t.Fatalf("被管端详情正确令牌应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	node, _ := m["node"].(map[string]any)
	if node == nil || node["name"] != "美国01" {
		t.Fatalf("被管端详情节点不对：%v", m)
	}
	if m["traffic"] == nil || m["backend_health"] == nil {
		t.Fatalf("被管端详情应带 traffic 与 backend_health：%v", m)
	}
	if strings.Contains(body, "join_token") || strings.Contains(body, "private_key") {
		t.Fatalf("被管端详情不应泄露令牌／私钥：%s", body)
	}
	// 错令牌 → 401。
	if code, _ := do("Bearer wrong-token", "/api/v1/central/nodes/"+strconv.FormatInt(id, 10)); code != 401 {
		t.Fatalf("错令牌应 401，实际 %d", code)
	}
	// 无令牌 → 401。
	if code, _ := do("", "/api/v1/central/nodes/"+strconv.FormatInt(id, 10)); code != 401 {
		t.Fatalf("无令牌应 401，实际 %d", code)
	}
	// 节点不存在 → 404。
	if code, _ := do("Bearer "+tok, "/api/v1/central/nodes/9999"); code != 404 {
		t.Fatalf("不存在的节点应 404，实际 %d", code)
	}
}
