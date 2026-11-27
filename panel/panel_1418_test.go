package main

// v1.4.18：轻量节点后端切片 8——中转投影。
//
// 覆盖：
//   - 版本断言 1.4.18。
//   - 渲染（纯函数）：TCP 路由一段、无 use_udp 字段、needUDP=false；
//     UDP 路由 needUDP=true；同 listen 的 TCP＋UDP 只写一段（v1.3.33
//     的教训：写两段第二段绑定必定失败）；停用的跳过；空表。
//   - 删段：只删带 # backend-route 标记的段，落地段不动。
//   - 下发（桩掉 restartRealmImpl）：加路由 → config.toml 多一段
//     endpoint、全局 use_udp 翻 true、realm 重启一次；无变化再下发
//     不重启；删路由 → 段撤掉；只剩路由段时删光留占位；管理面板
//     （ManagerOnly）只存不下发。
//   - 路由增删改查接口：校验（监听地址、目标节点、协议、端口、
//     撞监听、撞已占端口）、裸端口归一化、落地地址空着用节点地址、
//     开关切换、重新下发。
//   - 删节点“一起删路由”后重下发：段从 config.toml 里撤掉。
//   - 同机 agent 不在时 tryAgentRelayApply 回 (false, nil)，面板走直写。
//
// 注意：restartRealmImpl 是包级变量桩（与 realmInstalledFunc 同款），
// 用完恢复；测试不跑真 realm、不碰 154.37.217.135。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion1418(t *testing.T) {
	if Version != "1.4.31" {
		t.Fatalf("Version 应为 1.4.31，实际 %q", Version)
	}
}

// seedToml1418 往测试服务器的 RealmDir 写一份初始 config.toml。
func seedToml1418(t *testing.T, s *Server, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("写 config.toml 失败：%v", err)
	}
}

func readToml1418(t *testing.T, s *Server) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.env.RealmDir, "config.toml"))
	if err != nil {
		t.Fatalf("读 config.toml 失败：%v", err)
	}
	return string(b)
}

// stubRestart1418 把 restartRealmImpl 换成计数桩，返回计数器与恢复函数。
func stubRestart1418(t *testing.T) (*int, func()) {
	t.Helper()
	old := restartRealmImpl
	calls := 0
	restartRealmImpl = func(s *Server) error { calls++; return nil }
	return &calls, func() { restartRealmImpl = old }
}

// login1418 建会话，返回 cookie（与 1400 的建节点助手共用一套路由）。
func login1418(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("登录应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	if cookie == "" {
		t.Fatalf("登录没回 Set-Cookie")
	}
	return strings.Split(cookie, ";")[0]
}

// ---------- 渲染 ----------

func TestRenderBackendRelayRoutes1418(t *testing.T) {
	// TCP 一条：一段、无 use_udp 字段、needUDP=false。
	out, needUDP := renderBackendRelayRoutes([]BackendRelayRoute{
		{RelayID: 0, ListenAddr: "0.0.0.0:30004", RemoteNodeID: 1, RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp", Enabled: true},
	})
	if needUDP {
		t.Fatal("纯 TCP 路由 needUDP 应为 false")
	}
	if !strings.Contains(out, "[[endpoints]]") || !strings.Contains(out, `listen = "0.0.0.0:30004"`) || !strings.Contains(out, `remote = "203.0.113.1:443"`) {
		t.Fatalf("TCP 路由渲染不对：%q", out)
	}
	if strings.Contains(out, "use_udp") {
		t.Fatalf("段里不应写 use_udp（全局开关在 [network] 里）：%q", out)
	}
	if !strings.Contains(out, "# backend-route") {
		t.Fatalf("段前应有 # backend-route 标记：%q", out)
	}

	// UDP 一条：needUDP=true。
	_, needUDP = renderBackendRelayRoutes([]BackendRelayRoute{
		{RelayID: 0, ListenAddr: "0.0.0.0:30005", RemoteNodeID: 1, RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "udp", Enabled: true},
	})
	if !needUDP {
		t.Fatal("UDP 路由 needUDP 应为 true")
	}

	// 同 listen 的 TCP＋UDP 两条：只写一段（v1.3.33：两段会抢绑定）。
	out, needUDP = renderBackendRelayRoutes([]BackendRelayRoute{
		{RelayID: 0, ListenAddr: "0.0.0.0:443", RemoteNodeID: 1, RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp", Enabled: true},
		{RelayID: 0, ListenAddr: "0.0.0.0:443", RemoteNodeID: 1, RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "udp", Enabled: true},
	})
	if !needUDP {
		t.Fatal("同 listen 有 UDP 路由时 needUDP 应为 true")
	}
	if n := strings.Count(out, "[[endpoints]]"); n != 1 {
		t.Fatalf("同 listen 的 TCP＋UDP 应只写一段，实际 %d 段：%q", n, out)
	}

	// 停用的跳过；空表渲染为空、needUDP=false。
	out, needUDP = renderBackendRelayRoutes([]BackendRelayRoute{
		{RelayID: 0, ListenAddr: "0.0.0.0:30006", RemoteNodeID: 1, RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "udp", Enabled: false},
	})
	if out != "" || needUDP {
		t.Fatalf("停用的路由应不渲染：%q needUDP=%v", out, needUDP)
	}
	out, needUDP = renderBackendRelayRoutes(nil)
	if out != "" || needUDP {
		t.Fatalf("空表应渲染为空：%q needUDP=%v", out, needUDP)
	}
}

func TestRemoveBackendRouteSegments1418(t *testing.T) {
	toml := "[network]\nno_tcp = false\nuse_udp = false\n\n[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:443\"\n\n# backend-route relay=0 listen=0.0.0.0:30004\n[[endpoints]]\nlisten = \"0.0.0.0:30004\"\nremote = \"203.0.113.1:443\"\n"
	got := string(removeBackendRouteSegments([]byte(toml)))
	if strings.Contains(got, "30004") || !strings.Contains(got, "24443") {
		t.Fatalf("只应删掉带标记的段、落地段保留，实际：%q", got)
	}
}

// ---------- 下发 ----------

func TestApplyBackendRelayRoutes1418(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := login1418(t, h)
	_, _ = createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	calls, restore := stubRestart1418(t)
	defer restore()
	seedToml1418(t, s, "[network]\nno_tcp = false\nuse_udp = false\n\n[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:443\"\n")
	s.nodes.mu.Lock()
	s.nodes.data.Routes = append(s.nodes.data.Routes, BackendRelayRoute{
		RelayID: 0, ListenAddr: "0.0.0.0:30004", RemoteNodeID: 1,
		RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp", Enabled: true,
	})
	s.nodes.mu.Unlock()

	applied, err := s.applyBackendRelayRoutes()
	if err != nil || !applied {
		t.Fatalf("下发应 applied=true，实际 applied=%v err=%v", applied, err)
	}
	if *calls != 1 {
		t.Fatalf("下发应重启 realm 1 次，实际 %d 次", *calls)
	}
	got := readToml1418(t, s)
	if !strings.Contains(got, "0.0.0.0:30004") || !strings.Contains(got, "24443") {
		t.Fatalf("下发后 config.toml 应有路由段且落地段保留，实际：%q", got)
	}
	// TCP 路由不翻 use_udp。
	if on, ok := readUseUDP([]byte(got)); !ok || on {
		t.Fatalf("纯 TCP 路由不应翻 use_udp，实际 on=%v ok=%v：%q", on, ok, got)
	}

	// 无变化再下发：不重启。
	applied, err = s.applyBackendRelayRoutes()
	if err != nil || applied {
		t.Fatalf("无变化下发应 applied=false，实际 applied=%v err=%v", applied, err)
	}
	if *calls != 1 {
		t.Fatalf("无变化下发不应重启，实际共 %d 次", *calls)
	}

	// 加一条 UDP 路由：use_udp 翻 true、再重启一次。
	s.nodes.mu.Lock()
	s.nodes.data.Routes = append(s.nodes.data.Routes, BackendRelayRoute{
		RelayID: 0, ListenAddr: "0.0.0.0:30005", RemoteNodeID: 1,
		RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "udp", Enabled: true,
	})
	s.nodes.mu.Unlock()
	applied, err = s.applyBackendRelayRoutes()
	if err != nil || !applied {
		t.Fatalf("加 UDP 路由下发应 applied=true，实际 applied=%v err=%v", applied, err)
	}
	if *calls != 2 {
		t.Fatalf("加 UDP 路由应再重启 1 次，实际共 %d 次", *calls)
	}
	got = readToml1418(t, s)
	if on, ok := readUseUDP([]byte(got)); !ok || !on {
		t.Fatalf("有 UDP 路由时 use_udp 应为 true，实际 on=%v ok=%v：%q", on, ok, got)
	}

	// 路由全删：段撤掉，落地段还在；use_udp 只开不关（ensureUseUDP 口径）。
	s.nodes.mu.Lock()
	s.nodes.data.Routes = nil
	s.nodes.mu.Unlock()
	applied, err = s.applyBackendRelayRoutes()
	if err != nil || !applied {
		t.Fatalf("删光路由下发应 applied=true，实际 applied=%v err=%v", applied, err)
	}
	got = readToml1418(t, s)
	if strings.Contains(got, "30004") || strings.Contains(got, "30005") || !strings.Contains(got, "24443") {
		t.Fatalf("删光路由后段应撤掉、落地段保留，实际：%q", got)
	}
	if on, _ := readUseUDP([]byte(got)); !on {
		t.Fatalf("UDP 路由删光后 use_udp 保持 true（只开不关），实际 false：%q", got)
	}
}

func TestApplyBackendRelayRoutesPlaceholder1418(t *testing.T) {
	s := newTestServer1400(t)
	calls, restore := stubRestart1418(t)
	defer restore()
	// 只有路由段、没有落地段：删光后留占位（realm 无 endpoint 会 panic）。
	seedToml1418(t, s, "[network]\nno_tcp = false\nuse_udp = false\n\n# backend-route relay=0 listen=0.0.0.0:30004\n[[endpoints]]\nlisten = \"0.0.0.0:30004\"\nremote = \"203.0.113.1:443\"\n")
	s.nodes.mu.Lock()
	s.nodes.data.Routes = nil
	s.nodes.mu.Unlock()
	applied, err := s.applyBackendRelayRoutes()
	if err != nil || !applied {
		t.Fatalf("删光唯一路由段应 applied=true，实际 applied=%v err=%v", applied, err)
	}
	if *calls != 1 {
		t.Fatalf("应重启 1 次，实际 %d 次", *calls)
	}
	got := readToml1418(t, s)
	if !strings.Contains(got, "127.0.0.1:9") || strings.Contains(got, "30004") {
		t.Fatalf("删光后应留 127.0.0.1:9 占位、路由段撤掉，实际：%q", got)
	}
	if err := validateConfigToml([]byte(got)); err != nil {
		t.Fatalf("占位后的 config.toml 应通过校验：%v：%q", err, got)
	}
}

func TestApplyBackendRelayRoutesManagerOnly1418(t *testing.T) {
	s := newTestServer1400(t)
	s.cfg.ManagerOnly = true
	applied, err := s.applyBackendRelayRoutes()
	if err != nil || applied {
		t.Fatalf("管理面板下发应 applied=false 且无错，实际 applied=%v err=%v", applied, err)
	}
	if reason := s.relayApplySkipReason(); !strings.Contains(reason, "管理面板") {
		t.Fatalf("管理面板的跳过原因应写明管理面板，实际 %q", reason)
	}
}

func TestTryAgentRelayApplyNoAgent1418(t *testing.T) {
	// 本机 18081 没 agent：回 (false, nil)，面板走直写。
	handled, err := tryAgentRelayApply([]byte("[network]\nuse_udp = false\n\n[[endpoints]]\nlisten = \"127.0.0.1:9\"\nremote = \"127.0.0.1:9\"\n"))
	if handled || err != nil {
		t.Fatalf("没 agent 时应 (false, nil)，实际 (%v, %v)", handled, err)
	}
}

// ---------- 路由接口 ----------

func TestRelayRoutesAPI1418(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := login1418(t, h)
	_, _ = createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	calls, restore := stubRestart1418(t)
	defer restore()
	seedToml1418(t, s, "[network]\nno_tcp = false\nuse_udp = false\n\n[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:443\"\n")

	// 校验：监听地址不对、目标节点不存在、协议不对、落地端口越界。
	for _, tc := range []struct {
		name, body, want string
	}{
		{"监听地址不对", `{"listen_addr":"abc","remote_node_id":1,"remote_port":443}`, "监听端口"},
		{"目标节点不存在", `{"listen_addr":"0.0.0.0:30004","remote_node_id":99,"remote_port":443}`, "目标节点不存在"},
		{"协议不对", `{"listen_addr":"0.0.0.0:30004","remote_node_id":1,"remote_port":443,"proto":"sctp"}`, "协议只能是"},
		{"落地端口越界", `{"listen_addr":"0.0.0.0:30004","remote_node_id":1,"remote_port":70000}`, "落地端口"},
		{"监听端口撞面板端口", `{"listen_addr":"0.0.0.0:11111","remote_node_id":1,"remote_port":443}`, "已被占用"},
	} {
		code, body := authed1400(t, h, cookie, "POST", "/api/v1/relay-routes", tc.body)
		if code != 400 || !strings.Contains(body, tc.want) {
			t.Fatalf("%s 应 400 且含 %q，实际 %d：%s", tc.name, tc.want, code, body)
		}
	}

	// 新增：裸端口归一化、落地地址空着用节点地址、applied=true。
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/relay-routes",
		`{"listen_addr":"30004","remote_node_id":1,"remote_port":443,"proto":"tcp"}`)
	if code != 200 {
		t.Fatalf("新增路由应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	if m["applied"] != true {
		t.Fatalf("新增路由应 applied=true：%s", body)
	}
	route, _ := m["route"].(map[string]any)
	if route["listen_addr"] != "0.0.0.0:30004" || route["remote_addr"] != "203.0.113.1" {
		t.Fatalf("归一化不对：%v", route)
	}
	if *calls != 1 {
		t.Fatalf("新增路由应重启 1 次，实际 %d 次", *calls)
	}
	if got := readToml1418(t, s); !strings.Contains(got, "0.0.0.0:30004") {
		t.Fatalf("新增后 config.toml 应有该段，实际：%q", got)
	}

	// 重复监听 → 400。
	code, body = authed1400(t, h, cookie, "POST", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","remote_node_id":1,"remote_port":443}`)
	if code != 400 || !strings.Contains(body, "已有中转路由") {
		t.Fatalf("重复监听应 400 且含“已有中转路由”，实际 %d：%s", code, body)
	}

	// 列表：1 条、local_relay=true。
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/relay-routes", "")
	if code != 200 {
		t.Fatalf("列表应 200，实际 %d：%s", code, body)
	}
	m = respMap1400(t, body)
	routes, _ := m["routes"].([]any)
	if len(routes) != 1 || m["local_relay"] != true {
		t.Fatalf("列表应 1 条且 local_relay=true：%s", body)
	}

	// 改：停用 → 段撤掉、再重启一次。
	code, body = authed1400(t, h, cookie, "PUT", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","enabled":false}`)
	if code != 200 {
		t.Fatalf("停用路由应 200，实际 %d：%s", code, body)
	}
	if *calls != 2 {
		t.Fatalf("停用应再重启 1 次，实际共 %d 次", *calls)
	}
	if got := readToml1418(t, s); strings.Contains(got, "30004") {
		t.Fatalf("停用后段应撤掉，实际：%q", got)
	}

	// 改不存在的 → 404；删不存在的 → 404。
	code, _ = authed1400(t, h, cookie, "PUT", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:39999","enabled":true}`)
	if code != 404 {
		t.Fatalf("改不存在的路由应 404，实际 %d", code)
	}
	code, _ = authed1400(t, h, cookie, "DELETE", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:39999"}`)
	if code != 404 {
		t.Fatalf("删不存在的路由应 404，实际 %d", code)
	}

	// 删：200、段已撤（本来就撤了）、无变化不重启。
	code, body = authed1400(t, h, cookie, "DELETE", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004"}`)
	if code != 200 {
		t.Fatalf("删除路由应 200，实际 %d：%s", code, body)
	}
	if *calls != 2 {
		t.Fatalf("删已停用的路由无变化、不应重启，实际共 %d 次", *calls)
	}

	// 重新下发：无变化 → applied=false、不重启。
	code, body = authed1400(t, h, cookie, "POST", "/api/v1/relay-routes/apply", `{}`)
	if code != 200 {
		t.Fatalf("重新下发应 200，实际 %d：%s", code, body)
	}
	if m := respMap1400(t, body); m["applied"] != false {
		t.Fatalf("无变化重新下发应 applied=false：%s", body)
	}
	if *calls != 2 {
		t.Fatalf("无变化重新下发不应重启，实际共 %d 次", *calls)
	}
}

// 删节点“一起删路由”后重下发：段从 config.toml 里撤掉。
func TestNodesDeleteRoutesReapply1418(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := login1418(t, h)
	_, _ = createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	calls, restore := stubRestart1418(t)
	defer restore()
	seedToml1418(t, s, "[network]\nno_tcp = false\nuse_udp = false\n\n[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:443\"\n")
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","remote_node_id":1,"remote_port":443,"proto":"tcp"}`)
	if code != 200 {
		t.Fatalf("新增路由应 200，实际 %d：%s", code, body)
	}
	if *calls != 1 {
		t.Fatalf("新增路由应重启 1 次，实际 %d 次", *calls)
	}
	// 不勾一起删 → 400 列出孤儿路由。
	code, body = authed1400(t, h, cookie, "DELETE", "/api/v1/nodes/1", `{}`)
	if code != 400 || !strings.Contains(body, "orphan_routes") {
		t.Fatalf("有孤儿路由不勾一起删应 400 并列出路由，实际 %d：%s", code, body)
	}
	// 勾一起删 → 200，段撤掉、再重启一次。
	code, body = authed1400(t, h, cookie, "DELETE", "/api/v1/nodes/1", `{"delete_routes":true}`)
	if code != 200 {
		t.Fatalf("勾一起删应 200，实际 %d：%s", code, body)
	}
	if m := respMap1400(t, body); m["deleted_routes"] != float64(1) || m["applied"] != true {
		t.Fatalf("一起删应 deleted_routes=1 且 applied=true：%s", body)
	}
	if *calls != 2 {
		t.Fatalf("一起删后重下发应再重启 1 次，实际共 %d 次", *calls)
	}
	if got := readToml1418(t, s); strings.Contains(got, "30004") {
		t.Fatalf("一起删后段应撤掉，实际：%q", got)
	}
}

// 管理面板上建路由（C 方案）：不再本地存路由，没登记中转面板时
// 直接 400，提示先去集中管理页登记；登记了就代理到中转面板
// （代理用例见 panel_1428_relay_c_test.go）。
func TestRelayRoutesCreateManagerOnly1418(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := login1418(t, h)
	_, _ = createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	s.cfg.ManagerOnly = true
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/relay-routes",
		`{"listen_addr":"0.0.0.0:30004","remote_node_id":1,"remote_port":443,"proto":"tcp"}`)
	if code != 400 || !strings.Contains(body, "还没登记中转面板") {
		t.Fatalf("管理面板没登记中转面板时建路由应 400 且提示登记，实际 %d：%s", code, body)
	}
}
