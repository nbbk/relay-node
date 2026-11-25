package main

// v1.4.25：后端订阅 twin 链接（直连＋中转双链接）。
//
// 背景：有些落地网络差、需要中转；一个订阅链接里要同时含直连和中转
// 的节点。中转路由在中转面板（远程面板）上，不在管理面板上；管理
// 面板靠已有的 2 分钟集中管理拉取链从远程面板的 central/status 里
// 收 relay_routes（只含启用的路由），不新增轮询。
//
// 修法：
//   - 远程面板 handleCentralStatus 回包加 relay_routes（只含启用的
//     路由，每条 {listen_addr, remote_addr, remote_port, proto}，
//     proto 已归一化：UDP 路由发 "udp"，其余发 "tcp"）；管理面板
//     fetchRemoteStatus 解析进快照（RelayRoutes＋RelayRoutesSupported，
//     远程版本 >= 1.4.25 才为 true，空时为空数组）。
//   - BackendNode 加 LinkMode（direct／relay／both，空值＝默认 both）；
//     PUT /api/v1/nodes/{id}/link-mode 改它，非法值 400，只落盘
//     nodes.json、不 bump 节点 ConfigVersion。
//   - backendInboundLink 抽成 builder：直连走它（名字 -A），新增
//     backendInboundRelayLink（中转监听地址，名字 -B，UUID 与入站
//     参数与直连逐字一致）。backendSubLinksForUser 里每条（节点，入站）
//     按 LinkMode 出 A 与／或 B，同入站先 A 后 B；匹配四条全满足才出
//     B（路由已启用、目标地址 EqualFold 相等、目标端口＝入站端口、
//     协议一致：路由 UDP 当且仅当 proto=="udp" 或 UseUDP，入站 UDP
//     当且仅当 HY2 或 TUIC）；每个入站最多一条 B（多条匹配取第一条，
//     本地路由在前，再按远程面板 ID、listen_addr 排序）。
//   - 旧系统 collectSubLinks（twin）一字不动；单条链接接口继续走
//     backendInboundLink（直连 -A）。
//
// 覆盖：
//   - 版本断言 1.4.25。
//   - twin：有匹配路由时同入站出 A＋B，host 分别是节点地址与中转监听
//     地址、UUID 与参数一致、名字带 -A/-B；三种模式；无路由时 both
//     也只出 A；relay 模式无路由时该入站不出链接；协议／端口／目标
//     地址不匹配不出 B；多条匹配只出一条 B 且顺序确定；本地路由也出
//     B；名字里无「中转」「直连」。
//   - central/status 回包含 relay_routes、fetchRemoteStatus 能解析进
//     快照（含版本门）。
//   - LinkMode 接口非法值 400、合法值持久化（列表／详情带出 link_mode，
//     ConfigVersion 不变，nodes.json 落盘）。

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion1425(t *testing.T) {
	if Version != "1.4.27" {
		t.Fatalf("Version 应为 1.4.27，实际 %q", Version)
	}
}

// seedRemoteRelay1425 登记一台远程面板并往集中管理快照里塞中转路由
// （绕过 HTTP 拉取：快照本来就是 fetchRemoteStatus 填的，这里直接
// 填快照、只测 twin 匹配；解析本身由 TestFetchRemoteStatusRelayRoutes1425
// 单独覆盖）。
func seedRemoteRelay1425(t *testing.T, s *Server, remoteID, relayAddr string, routes []centralRelayRoute) {
	t.Helper()
	s.cfg.Remotes = append(s.cfg.Remotes, RemotePanel{ID: remoteID, Name: remoteID, BaseURL: "http://203.0.113.200:11111", Token: "rpt_test"})
	s.centralMu.Lock()
	s.centralCache[remoteID] = &RemoteStatusSnapshot{
		ID: remoteID, Name: remoteID, BaseURL: "http://203.0.113.200:11111",
		Online: true, Version: "1.4.25", RelayAddr: relayAddr,
		Landings: []remoteLandingSummary{}, Nodes: []centralNodeSummary{},
		RelayRoutes: routes, RelayRoutesSupported: true,
	}
	s.centralMu.Unlock()
}

// setNodeLinkMode1425 直接改节点的 LinkMode（接口本身由
// TestNodeLinkModeAPI1425 单独覆盖）。
func setNodeLinkMode1425(t *testing.T, s *Server, nodeID int64, mode string) {
	t.Helper()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	n := s.nodes.getNode(nodeID)
	if n == nil {
		t.Fatalf("节点 %d 找不到了", nodeID)
	}
	n.LinkMode = mode
}

// stripLinkHostName1425 去掉一条链接的 host 与备注，剩下的部分
// （scheme／凭证／query）应逐字一致——A 与 B 只差地址与名字后缀。
func stripLinkHostName1425(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("链接解析失败：%v：%s", err, link)
	}
	u.Host = ""
	u.Fragment = ""
	return u.String()
}

func TestTwinDirectAndRelay1425(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	seedContractNode1420(t, s, h, cookie)
	// 中转机 203.0.113.200:30004 转节点 203.0.113.1:443（TCP）。
	seedRemoteRelay1425(t, s, "rm-1", "203.0.113.200", []centralRelayRoute{
		{ListenAddr: "0.0.0.0:30004", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp"},
	})

	links := s.backendSubLinksForUser("subuser", "su-1420")
	// REALITY 与 AnyTLS 各出 A＋B，SOCKS5（1080 没路由）只出 A，共 5 条。
	if len(links) != 5 {
		t.Fatalf("应有 5 条链接（REALITY／AnyTLS 各 A＋B、SOCKS5 只 A），实际 %d：%v", len(links), links)
	}
	byName := map[string]subLink{}
	for _, l := range links {
		byName[l.Name] = l
		if strings.Contains(l.Name, "中转") || strings.Contains(l.Name, "直连") {
			t.Fatalf("链接名里不应出现「中转」「直连」：%q", l.Name)
		}
	}
	for _, tag := range []string{"reality-443", "anytls-443"} {
		a, okA := byName["example-node-"+tag+"-A"]
		b, okB := byName["example-node-"+tag+"-B"]
		if !okA || !okB {
			t.Fatalf("%s 应同时有 -A 与 -B，实际 %v", tag, links)
		}
		au, err := url.Parse(a.Link)
		if err != nil {
			t.Fatal(err)
		}
		bu, err := url.Parse(b.Link)
		if err != nil {
			t.Fatal(err)
		}
		if au.Host != "203.0.113.1:443" {
			t.Fatalf("直连 host 应是节点地址 203.0.113.1:443，实际 %q", au.Host)
		}
		if bu.Host != "203.0.113.200:30004" {
			t.Fatalf("中转 host 应是中转监听地址 203.0.113.200:30004，实际 %q", bu.Host)
		}
		// UUID 与入站参数逐字一致：去掉 host 与备注后其余相同。
		if stripLinkHostName1425(t, a.Link) != stripLinkHostName1425(t, b.Link) {
			t.Fatalf("%s 的 A 与 B 除地址／名字外应逐字一致：\nA %s\nB %s", tag, a.Link, b.Link)
		}
		if !strings.Contains(a.Link, contract1420UUID) || !strings.Contains(b.Link, contract1420UUID) {
			t.Fatalf("%s 的 A 与 B 都应带用户 UUID：\nA %s\nB %s", tag, a.Link, b.Link)
		}
	}
	if _, ok := byName["example-node-socks-1080-A"]; !ok {
		t.Fatalf("SOCKS5（1080 没路由）应只出 -A，实际 %v", links)
	}
	if _, ok := byName["example-node-socks-1080-B"]; ok {
		t.Fatalf("SOCKS5（1080 没路由）不应出 -B，实际 %v", links)
	}
	// 同入站先 A 后 B（顺序：REALITY-A、REALITY-B、AnyTLS-A、AnyTLS-B、SOCKS5-A）。
	wantOrder := []string{"example-node-reality-443-A", "example-node-reality-443-B",
		"example-node-anytls-443-A", "example-node-anytls-443-B", "example-node-socks-1080-A"}
	for i, w := range wantOrder {
		if links[i].Name != w {
			t.Fatalf("第 %d 条应是 %s，实际 %q（全部 %v）", i+1, w, links[i].Name, links)
		}
	}
}

func TestTwinLinkModes1425(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id := seedContractNode1420(t, s, h, cookie)
	seedRemoteRelay1425(t, s, "rm-1", "203.0.113.200", []centralRelayRoute{
		{ListenAddr: "0.0.0.0:30004", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp"},
	})
	count := func() (a, b int) {
		links := s.backendSubLinksForUser("subuser", "su-1420")
		for _, l := range links {
			if strings.HasSuffix(l.Name, "-A") {
				a++
			} else if strings.HasSuffix(l.Name, "-B") {
				b++
			} else {
				t.Fatalf("链接名应带 -A 或 -B 后缀，实际 %q", l.Name)
			}
		}
		return a, b
	}

	// 空值＝默认 both。
	if a, b := count(); a != 3 || b != 2 {
		t.Fatalf("空 LinkMode 应默认 both（3 条 A、2 条 B），实际 A=%d B=%d", a, b)
	}
	// direct：只出 A。
	setNodeLinkMode1425(t, s, id, "direct")
	if a, b := count(); a != 3 || b != 0 {
		t.Fatalf("direct 应只出 3 条 A，实际 A=%d B=%d", a, b)
	}
	// relay：只出 B（SOCKS5 没路由，该入站不出链接）。
	setNodeLinkMode1425(t, s, id, "relay")
	if a, b := count(); a != 0 || b != 2 {
		t.Fatalf("relay 应只出 2 条 B，实际 A=%d B=%d", a, b)
	}
	// both：两者都出。
	setNodeLinkMode1425(t, s, id, "both")
	if a, b := count(); a != 3 || b != 2 {
		t.Fatalf("both 应出 3 条 A、2 条 B，实际 A=%d B=%d", a, b)
	}
	// 非法值按默认 both 算（接口层会 400，这里是读路径的兜底）。
	setNodeLinkMode1425(t, s, id, "bogus")
	if a, b := count(); a != 3 || b != 2 {
		t.Fatalf("非法 LinkMode 读路径应按默认 both 算，实际 A=%d B=%d", a, b)
	}
}

func TestTwinNoRoute1425(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id := seedContractNode1420(t, s, h, cookie)

	// 没有任何路由：both 也只出 A。
	links := s.backendSubLinksForUser("subuser", "su-1420")
	if len(links) != 3 {
		t.Fatalf("无路由时 both 应只出 3 条 A，实际 %d：%v", len(links), links)
	}
	for _, l := range links {
		if !strings.HasSuffix(l.Name, "-A") {
			t.Fatalf("无路由时不应出 B，实际 %v", links)
		}
	}
	// relay 模式无路由：该入站一条不出（绝不凭空编）。
	setNodeLinkMode1425(t, s, id, "relay")
	links = s.backendSubLinksForUser("subuser", "su-1420")
	if len(links) != 0 {
		t.Fatalf("relay 模式无路由时应 0 条链接，实际 %d：%v", len(links), links)
	}
}

func TestTwinMismatch1425(t *testing.T) {
	newSeed := func(t *testing.T) *Server {
		t.Helper()
		s := newTestServer1400(t)
		h := s.routes()
		cookie := loginTestCookie(t, h)
		seedContractNode1423(t, s, h, cookie)
		return s
	}
	countB := func(s *Server) int {
		n := 0
		for _, l := range s.backendSubLinksForUser("subuser", "su-1420") {
			if strings.HasSuffix(l.Name, "-B") {
				n++
			}
		}
		return n
	}
	namesB := func(s *Server) []string {
		var out []string
		for _, l := range s.backendSubLinksForUser("subuser", "su-1420") {
			if strings.HasSuffix(l.Name, "-B") {
				out = append(out, l.Name)
			}
		}
		return out
	}

	// TCP 路由＋443：REALITY 与 AnyTLS（TCP，443）出 B；HY2（UDP，443）不出。
	s := newSeed(t)
	seedRemoteRelay1425(t, s, "rm-1", "203.0.113.200", []centralRelayRoute{
		{ListenAddr: "0.0.0.0:30004", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp"},
	})
	if got := namesB(s); len(got) != 2 {
		t.Fatalf("TCP 路由 443 应只给 REALITY／AnyTLS 出 B，实际 B=%v", got)
	}
	// UDP 路由＋443：HY2（UDP，443）出 B；REALITY（TCP，443）不出。
	s = newSeed(t)
	seedRemoteRelay1425(t, s, "rm-1", "203.0.113.200", []centralRelayRoute{
		{ListenAddr: "0.0.0.0:30004", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "udp"},
	})
	if got := namesB(s); len(got) != 1 || got[0] != "example-node-hy2-443-B" {
		t.Fatalf("UDP 路由 443 应只给 HY2 出 B，实际 B=%v", got)
	}
	// 端口不匹配：TCP 路由目标 4443，443 的入站不出 B（TUIC 是 UDP，协议也不对）。
	s = newSeed(t)
	seedRemoteRelay1425(t, s, "rm-1", "203.0.113.200", []centralRelayRoute{
		{ListenAddr: "0.0.0.0:30004", RemoteAddr: "203.0.113.1", RemotePort: 4443, Proto: "tcp"},
	})
	if n := countB(s); n != 0 {
		t.Fatalf("目标端口 4443 时不应出 B，实际 %d 条 B：%v", n, namesB(s))
	}
	// 目标地址不匹配：路由指 203.0.113.9，节点是 203.0.113.1，不出 B。
	s = newSeed(t)
	seedRemoteRelay1425(t, s, "rm-1", "203.0.113.200", []centralRelayRoute{
		{ListenAddr: "0.0.0.0:30004", RemoteAddr: "203.0.113.9", RemotePort: 443, Proto: "tcp"},
	})
	if n := countB(s); n != 0 {
		t.Fatalf("目标地址不对时不应出 B，实际 %d 条 B：%v", n, namesB(s))
	}
	// 多条匹配只出一条 B，顺序确定：同远程面板按 listen_addr 排序。
	s = newSeed(t)
	seedRemoteRelay1425(t, s, "rm-1", "203.0.113.200", []centralRelayRoute{
		{ListenAddr: "0.0.0.0:30005", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp"},
		{ListenAddr: "0.0.0.0:30004", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp"},
	})
	links := s.backendSubLinksForUser("subuser", "su-1420")
	bHosts := map[string]bool{}
	for _, l := range links {
		if strings.HasSuffix(l.Name, "-B") {
			u, _ := url.Parse(l.Link)
			bHosts[u.Host] = true
		}
	}
	if len(bHosts) != 1 || !bHosts["203.0.113.200:30004"] {
		t.Fatalf("多条匹配时每入站只应出一条 B，且取 listen 最小的 30004，实际 B host=%v", bHosts)
	}
	// 多台远程面板：按远程面板 ID 排序，rm-a 的路由先于 rm-b。
	s = newSeed(t)
	seedRemoteRelay1425(t, s, "rm-b", "203.0.113.200", []centralRelayRoute{
		{ListenAddr: "0.0.0.0:30004", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp"},
	})
	seedRemoteRelay1425(t, s, "rm-a", "203.0.113.201", []centralRelayRoute{
		{ListenAddr: "0.0.0.0:30009", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp"},
	})
	bHosts = map[string]bool{}
	for _, l := range s.backendSubLinksForUser("subuser", "su-1420") {
		if strings.HasSuffix(l.Name, "-B") {
			u, _ := url.Parse(l.Link)
			bHosts[u.Host] = true
		}
	}
	if len(bHosts) != 1 || !bHosts["203.0.113.201:30009"] {
		t.Fatalf("多台远程面板时应取 ID 最小的 rm-a 的路由（30009），实际 B host=%v", bHosts)
	}
}

func TestTwinLocalRoute1425(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	seedContractNode1420(t, s, h, cookie)
	// 本地 relay.conf 配好 RelayAddr，本地路由启用。
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "relay.conf"), []byte("RELAY_ADDR=203.0.113.201\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.nodes.mu.Lock()
	s.nodes.data.Routes = append(s.nodes.data.Routes, BackendRelayRoute{
		RelayID: 0, ListenAddr: "0.0.0.0:30009", RemoteAddr: "203.0.113.1", RemotePort: 443,
		Proto: "tcp", Enabled: true,
	})
	s.nodes.mu.Unlock()
	links := s.backendSubLinksForUser("subuser", "su-1420")
	bHosts := map[string]bool{}
	for _, l := range links {
		if strings.HasSuffix(l.Name, "-B") {
			u, _ := url.Parse(l.Link)
			bHosts[u.Host] = true
		}
	}
	if len(bHosts) != 1 || !bHosts["203.0.113.201:30009"] {
		t.Fatalf("本地路由也应出 B（host 203.0.113.201:30009），实际 B host=%v，全部链接 %v", bHosts, links)
	}
	// 本地路由停用后不再出 B。
	s.nodes.mu.Lock()
	s.nodes.data.Routes[0].Enabled = false
	s.nodes.mu.Unlock()
	for _, l := range s.backendSubLinksForUser("subuser", "su-1420") {
		if strings.HasSuffix(l.Name, "-B") {
			t.Fatalf("本地路由停用后不应出 B，实际 %v", l)
		}
	}
}

func TestCentralRelayRoutes1425(t *testing.T) {
	s := newTestServer1400(t)
	s.nodes.mu.Lock()
	s.nodes.data.Routes = []BackendRelayRoute{
		{RelayID: 0, ListenAddr: "0.0.0.0:30005", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp", Enabled: true},
		{RelayID: 0, ListenAddr: "0.0.0.0:30004", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "", UseUDP: true, Enabled: true},
		{RelayID: 0, ListenAddr: "0.0.0.0:30006", RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp", Enabled: false},
	}
	s.nodes.mu.Unlock()

	// helper：只含启用的，按 listen_addr 排序；UseUDP 的那条 proto 归一化成 udp。
	got := s.centralRelayRoutesSummary()
	if len(got) != 2 {
		t.Fatalf("只应有 2 条启用的路由摘要，实际 %d：%v", len(got), got)
	}
	if got[0].ListenAddr != "0.0.0.0:30004" || got[0].Proto != "udp" {
		t.Fatalf("第一条应是 30004 的 UDP 路由（UseUDP 归一化），实际 %+v", got[0])
	}
	if got[1].ListenAddr != "0.0.0.0:30005" || got[1].Proto != "tcp" {
		t.Fatalf("第二条应是 30005 的 TCP 路由，实际 %+v", got[1])
	}

	// handleCentralStatus 回包里含 relay_routes，停用的不在里面。
	rec := httptest.NewRecorder()
	s.handleCentralStatus(rec, httptest.NewRequest("GET", "/api/v1/central/status", nil))
	if rec.Code != 200 {
		t.Fatalf("central/status 应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK          bool                `json:"ok"`
		RelayRoutes []centralRelayRoute `json:"relay_routes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("central/status 解析失败：%v", err)
	}
	if !resp.OK || len(resp.RelayRoutes) != 2 {
		t.Fatalf("central/status 里 relay_routes 应有 2 条启用的，实际 %+v", resp)
	}
}

func TestFetchRemoteStatusRelayRoutes1425(t *testing.T) {
	fake := func(panelVersion string, routes string) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"panel_version":"` + panelVersion + `","realm":{"running":true},"disk_pct":10,` +
				`"relay_addr":"203.0.113.200","landings":[],"nodes":[],` + routes + `}`))
		})
		srv := httptest.NewServer(mux)
		return srv
	}

	// 1.4.25 的远程面板：relay_routes 解析进快照，Supported 为 true。
	srv := fake("1.4.25", `"relay_routes":[{"listen_addr":"0.0.0.0:30004","remote_addr":"203.0.113.1","remote_port":443,"proto":"tcp"}]`)
	t.Cleanup(srv.Close)
	s := newTestServer1400(t)
	snap, err := s.fetchRemoteStatus(RemotePanel{ID: "rm-1", Name: "r1", BaseURL: srv.URL, Token: "rpt_x"})
	if err != nil {
		t.Fatalf("fetchRemoteStatus: %v", err)
	}
	if len(snap.RelayRoutes) != 1 || snap.RelayRoutes[0].ListenAddr != "0.0.0.0:30004" ||
		snap.RelayRoutes[0].RemoteAddr != "203.0.113.1" || snap.RelayRoutes[0].RemotePort != 443 ||
		snap.RelayRoutes[0].Proto != "tcp" {
		t.Fatalf("快照里 relay_routes 解析不对：%+v", snap.RelayRoutes)
	}
	if !snap.RelayRoutesSupported {
		t.Fatalf("远程 1.4.25 时 RelayRoutesSupported 应为 true")
	}

	// 1.4.24 的远程面板：没下发 relay_routes，Supported 为 false，
	// RelayRoutes 是空数组（不是 null）。
	srv2 := fake("1.4.24", `"relay_routes_missing":true`)
	t.Cleanup(srv2.Close)
	snap2, err := s.fetchRemoteStatus(RemotePanel{ID: "rm-2", Name: "r2", BaseURL: srv2.URL, Token: "rpt_x"})
	if err != nil {
		t.Fatalf("fetchRemoteStatus（旧版）: %v", err)
	}
	if snap2.RelayRoutesSupported {
		t.Fatalf("远程 1.4.24 时 RelayRoutesSupported 应为 false")
	}
	if snap2.RelayRoutes == nil || len(snap2.RelayRoutes) != 0 {
		t.Fatalf("旧版快照的 RelayRoutes 应是空数组，实际 %+v", snap2.RelayRoutes)
	}
}

func TestNodeLinkModeAPI1425(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id := seedContractNode1420(t, s, h, cookie)
	put := func(body string) (int, map[string]any) {
		t.Helper()
		code, resp := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/"+itoa1400(id)+"/link-mode", body)
		return code, respMap1400(t, resp)
	}
	getDetail := func() map[string]any {
		t.Helper()
		code, resp := authed1400(t, h, cookie, "GET", "/api/v1/nodes/"+itoa1400(id), "")
		if code != 200 {
			t.Fatalf("节点详情应 200，实际 %d：%s", code, resp)
		}
		m := respMap1400(t, resp)
		node, _ := m["node"].(map[string]any)
		if node == nil {
			t.Fatalf("节点详情里没有 node：%s", resp)
		}
		return node
	}

	// 非法值 400，错误直白说明可选值。
	if code, m := put(`{"link_mode":"bogus"}`); code != 400 {
		t.Fatalf("非法 link_mode 应 400，实际 %d：%v", code, m)
	} else if err, _ := m["error"].(string); !strings.Contains(err, "direct") || !strings.Contains(err, "relay") || !strings.Contains(err, "both") {
		t.Fatalf("非法值的报错应直白列出 direct／relay／both，实际 %q", err)
	}
	// 不存在的节点 404。
	if code, _ := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/999/link-mode", `{"link_mode":"relay"}`); code != 404 {
		t.Fatalf("不存在的节点应 404，实际 %d", code)
	}
	// 未登录 401。
	if code, _ := authed1400(t, h, "", "PUT", "/api/v1/nodes/"+itoa1400(id)+"/link-mode", `{"link_mode":"relay"}`); code != 401 {
		t.Fatalf("未登录应 401，实际 %d", code)
	}

	before := getDetail()
	cvBefore, _ := before["config_version"].(float64)

	// 合法值 relay：200，详情里带出 link_mode，ConfigVersion 不变。
	if code, m := put(`{"link_mode":"relay"}`); code != 200 || m["link_mode"] != "relay" {
		t.Fatalf("relay 应 200 且回 link_mode=relay，实际 %d：%v", code, m)
	}
	after := getDetail()
	if after["link_mode"] != "relay" {
		t.Fatalf("节点详情应带出 link_mode=relay，实际 %v", after)
	}
	if cvAfter, _ := after["config_version"].(float64); cvAfter != cvBefore {
		t.Fatalf("改 LinkMode 不应 bump ConfigVersion（改前 %v，改后 %v）", cvBefore, cvAfter)
	}
	// 落盘：nodes.json 里有 link_mode。
	raw, err := os.ReadFile(s.nodes.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"link_mode": "relay"`) {
		t.Fatalf("nodes.json 里应落盘 link_mode=relay，实际：%s", raw)
	}
	// 空串＝重置为默认 both。
	if code, m := put(`{"link_mode":""}`); code != 200 || m["link_mode"] != "both" {
		t.Fatalf("空串应重置为 both，实际 %d：%v", code, m)
	}
	if getDetail()["link_mode"] != "both" {
		t.Fatalf("空串重置后详情应是 both")
	}
	// 列表接口也带出 link_mode。
	code, resp := authed1400(t, h, cookie, "GET", "/api/v1/nodes", "")
	if code != 200 {
		t.Fatalf("节点列表应 200，实际 %d", code)
	}
	list := respMap1400(t, resp)
	nodes, _ := list["nodes"].([]any)
	found := false
	for _, n := range nodes {
		if row, _ := n.(map[string]any); row != nil {
			if row["id"] == float64(id) {
				found = true
				if row["link_mode"] != "both" {
					t.Fatalf("节点列表应带出 link_mode=both，实际 %v", row)
				}
			}
		}
	}
	if !found {
		t.Fatalf("节点列表里没有该节点：%s", resp)
	}
}

// TestStatusMachineIP1425 v1.4.25：顶栏本机 IP——status 回包两条路
// （relay.conf 缺失／正常）都带 machine_ip，且与 machineIP() 一致；
// 非空时应是合法 IPv4（优先公网、否则首块网卡）。
func TestStatusMachineIP1425(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	call := func() map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/status", nil)
		rec := httptest.NewRecorder()
		s.handleStatus(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status 应 200，实际 %d：%s", rec.Code, rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("status 响应不是 JSON：%v", err)
		}
		return m
	}
	check := func(m map[string]any, path string) {
		t.Helper()
		ip, _ := m["machine_ip"].(string)
		if ip != machineIP() {
			t.Fatalf("%s：machine_ip 应与 machineIP() 一致，实际 %q、期望 %q", path, ip, machineIP())
		}
		if ip != "" && net.ParseIP(ip) == nil {
			t.Fatalf("%s：machine_ip 非空时应是合法 IP，实际 %q", path, ip)
		}
	}
	// relay.conf 缺失的早返回路。
	check(call(), "relay.conf 缺失")
	// 正常路：写一份 relay.conf 再调。
	conf := "LANDING=127.0.0.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"\"\nCERT_DOMAIN=\"example.com\"\nPRESET=1\nMODE_NOTE=\"tiktok-node（Xray）\"\nPORTS=\"tcp:80:80 tcp:443:443\"\nLANDING_COUNT=1\n"
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "relay.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	check(call(), "relay.conf 正常")
}
