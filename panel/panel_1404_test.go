package main

// v1.4.4：轻量节点后端切片 4——流量上报与计量＋限流＋健康＋四级指派。
//
// 覆盖：
//   - POST /api/v1/node/traffic：mTLS 认证（没证书 401、node_id 与证书
//     不一致 400、窗口不对 400）；窗口去重（同一窗口重复推只算一次，
//     去重记录落盘、重启后仍挡得住）；user_key 解析失败跳过并计数；
//     按人增量进 user_traffic.json；倍率折算（节点倍率×中转倍率归一化、
//     上限 100）；入站总量累计；健康存取与状态（未运行＋last_error、
//     失联 180 秒、在线）。
//   - 节点详情流量块：本期按人合计＋各入站总量。
//   - 后端节点限流执法：80% 提醒标志、100% 停用后节点 ConfigVersion＋1
//     且配置文档里没他了（停用 choke 点链路）。
//   - 无 relay 时 resume／disable 不报错（2026-11-23 实锤 bug）；有
//     relay 但重建失败时 resume／disable 仍报错（状态已落盘）。
//   - 四级指派：all／group／node／inbound 解析成配置文档 users[]
//     .inbounds（空＝全部入站）；非法指派 400。
//   - 版本断言 1.4.12。

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// trafficReq1404 构造一发流量上报请求：cert 为空表示不带证书。
func trafficReq1404(cert *x509.Certificate, body string) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/node/traffic", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cert != nil {
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	}
	return req
}

// postTraffic1404 走完整路由发一发流量上报，返回状态码与解析后的 map。
func postTraffic1404(t *testing.T, h http.Handler, cert *x509.Certificate, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, trafficReq1404(cert, body))
	return rec.Code, respMap1400(t, rec.Body.String())
}

// trafficBody1404 拼一发上报 body（窗口默认 1000–1060）。
func trafficBody1404(nodeID int64, users string) string {
	return `{"node_id":` + itoa1400(nodeID) + `,"window_start":1000,"window_end":1060,"users":` + users + `,"inbounds":[{"inbound_tag":"reality-443","up":700,"down":800}],"online":[],"health":{"agent_version":"0.1.4","xray_version":"26.11.9","xray_running":true,"xray_restarts":0,"config_version":3,"last_error":"","cpu_percent":3.2,"mem_mb":210}}`
}

// setupTrafficNode1404 建节点＋入网＋指派全部用户，返回证书与 handler。
func setupTrafficNode1404(t *testing.T) (*Server, http.Handler, string, *x509.Certificate) {
	t.Helper()
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	_, tok := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	certPEM, _ := joinNode1400(t, h, 1, tok)
	cert := parseCert1400(t, certPEM)
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":null}`); code != 200 {
		t.Fatalf("节点 1 指派全部应 200，实际 %d：%s", code, body)
	}
	return s, h, cookie, cert
}

// configUsers1404 带证书拉一次配置文档，返回 users 数组。
func configUsers1404(t *testing.T, h http.Handler, cert *x509.Certificate) []any {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, configReq1400(cert))
	if rec.Code != 200 {
		t.Fatalf("拉配置应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	m := respMap1400(t, rec.Body.String())
	users, _ := m["users"].([]any)
	return users
}

// findConfigUser1404 在配置文档 users 里按 user_id 找一条。
func findConfigUser1404(t *testing.T, users []any, key string) map[string]any {
	t.Helper()
	for _, raw := range users {
		if u, ok := raw.(map[string]any); ok && u["user_id"] == key {
			return u
		}
	}
	t.Fatalf("配置文档里找不到用户 %s：%v", key, users)
	return nil
}

// ---------- 1. 流量上报：认证与窗口校验 ----------

func TestNodeTrafficAuth1404(t *testing.T) {
	s, h, _, cert := setupTrafficNode1404(t)
	_ = s
	body := trafficBody1404(1, `[{"user_key":"subuser:su-test1","up":100,"down":200,"inbound_tag":"reality-443"}]`)
	// 没证书 → 401。
	if code, _ := postTraffic1404(t, h, nil, body); code != 401 {
		t.Fatalf("没证书上报应 401，实际 %d", code)
	}
	// node_id 与证书不一致 → 400。
	if code, _ := postTraffic1404(t, h, cert, trafficBody1404(2, `[]`)); code != 400 {
		t.Fatalf("node_id 与证书不一致应 400，实际 %d", code)
	}
	// 窗口不对（end<=start）→ 400。
	bad := `{"node_id":1,"window_start":1060,"window_end":1000,"users":[]}`
	if code, _ := postTraffic1404(t, h, cert, bad); code != 400 {
		t.Fatalf("窗口不对应 400，实际 %d", code)
	}
	// 节点不存在的证书 → 404。
	otherCert := issueCert1400(t, s, 999)
	if code, _ := postTraffic1404(t, h, otherCert, trafficBody1404(999, `[]`)); code != 404 {
		t.Fatalf("不存在的节点上报应 404，实际 %d", code)
	}
}

// ---------- 2. 窗口去重：重复推只算一次，落盘重启不丢 ----------

func TestNodeTrafficWindowDedup1404(t *testing.T) {
	s, h, _, cert := setupTrafficNode1404(t)
	u1, _, _ := seedUsers1402(s)
	body := trafficBody1404(1, `[{"user_key":"`+u1+`","up":100,"down":200,"inbound_tag":"reality-443"}]`)
	code, m := postTraffic1404(t, h, cert, body)
	if code != 200 {
		t.Fatalf("第一次上报应 200，实际 %d：%v", code, m)
	}
	if m["accepted_windows"] != float64(1) || m["duplicate"] != false || m["accepted_users"] != float64(1) {
		t.Fatalf("第一次上报返回不对：%v", m)
	}
	if m["ignored_entries"] != float64(0) {
		t.Fatalf("第一次上报 ignored 应为 0：%v", m)
	}
	// 同一窗口再推一次 → duplicate，流量不重复算。
	code, m = postTraffic1404(t, h, cert, body)
	if code != 200 || m["duplicate"] != true || m["accepted_windows"] != float64(0) || m["accepted_users"] != float64(0) {
		t.Fatalf("重复窗口应 duplicate 且不计流量，实际 %d：%v", code, m)
	}
	// user_traffic.json 里只算了一次：300 字节。
	up, down, _, _, ok := s.userTraffic.totals(u1)
	if !ok || up+down != 300 {
		t.Fatalf("去重后用量应为 300，实际 up=%d down=%d ok=%v", up, down, ok)
	}
	// 入站总量也只算了一次。
	s.nodes.mu.Lock()
	ib := s.nodes.getNode(1).InboundTraffic["reality-443"]
	s.nodes.mu.Unlock()
	if ib[0] != 700 || ib[1] != 800 {
		t.Fatalf("入站总量应为 700/800，实际 %v", ib)
	}
	// 去重记录落盘：新起一个 store 读盘，同一窗口仍是重复。
	ns2 := newNodeTrafficSeenStore(filepath.Join(s.env.PanelHome, "node_traffic_seen.json"))
	ns2.load()
	if !ns2.checkAndRecord(1, 1000, 1060) {
		t.Fatalf("去重记录应落盘，重启后同一窗口仍应判重复")
	}
	// 不同窗口正常算。
	body2 := strings.Replace(body, `"window_start":1000,"window_end":1060`, `"window_start":1060,"window_end":1120`, 1)
	code, m = postTraffic1404(t, h, cert, body2)
	if code != 200 || m["duplicate"] != false || m["accepted_windows"] != float64(1) {
		t.Fatalf("新窗口应正常接受，实际 %d：%v", code, m)
	}
	up, down, _, _, _ = s.userTraffic.totals(u1)
	if up+down != 600 {
		t.Fatalf("两个窗口后用量应为 600，实际 %d", up+down)
	}
}

// ---------- 3. user_key 解析失败跳过并计数 ----------

func TestNodeTrafficIgnoredEntries1404(t *testing.T) {
	s, h, _, cert := setupTrafficNode1404(t)
	u1, _, _ := seedUsers1402(s)
	body := trafficBody1404(1, `[{"user_key":"subuser:su-test1","up":100,"down":200,"inbound_tag":"reality-443"},{"user_key":"bogus","up":1,"down":1},{"user_key":"subuser:su-nope","up":1,"down":1},{"user_key":"subuser:su-test1","up":-5,"down":0}]`)
	code, m := postTraffic1404(t, h, cert, body)
	if code != 200 {
		t.Fatalf("上报应 200，实际 %d：%v", code, m)
	}
	if m["accepted_users"] != float64(1) || m["ignored_entries"] != float64(3) {
		t.Fatalf("应接受 1 条、忽略 3 条，实际 %v", m)
	}
	up, down, _, _, _ := s.userTraffic.totals(u1)
	if up+down != 300 {
		t.Fatalf("只应记下合法那条 300，实际 %d", up+down)
	}
}

// ---------- 4. 倍率折算：节点倍率×中转倍率归一化、上限 100 ----------

func TestNodeTrafficMultiplier1404(t *testing.T) {
	s, h, _, cert := setupTrafficNode1404(t)
	u1, _, _ := seedUsers1402(s)
	// 节点倍率 2、中转倍率 1 → 2 倍。
	s.nodes.mu.Lock()
	s.nodes.getNode(1).Multiplier = 2
	_ = s.nodes.saveLocked()
	s.nodes.mu.Unlock()
	body := trafficBody1404(1, `[{"user_key":"`+u1+`","up":100,"down":200,"inbound_tag":"reality-443"}]`)
	if code, _ := postTraffic1404(t, h, cert, body); code != 200 {
		t.Fatalf("上报应 200，实际 %d", code)
	}
	up, down, _, _, _ := s.userTraffic.totals(u1)
	if up+down != 600 {
		t.Fatalf("节点倍率 2 时 300 应记成 600，实际 %d", up+down)
	}
	// 入站总量不乘倍率。
	s.nodes.mu.Lock()
	ib := s.nodes.getNode(1).InboundTraffic["reality-443"]
	s.nodes.mu.Unlock()
	if ib[0]+ib[1] != 1500 {
		t.Fatalf("入站总量不应乘倍率，应为 1500，实际 %v", ib)
	}
	// 节点倍率 60 × 中转倍率 2 = 120 → 钳到 100。
	s.cfg.RelayMultiplier = 2
	s.nodes.mu.Lock()
	s.nodes.getNode(1).Multiplier = 60
	_ = s.nodes.saveLocked()
	s.nodes.mu.Unlock()
	body2 := strings.Replace(body, `"window_start":1000,"window_end":1060`, `"window_start":2000,"window_end":2060`, 1)
	if code, _ := postTraffic1404(t, h, cert, body2); code != 200 {
		t.Fatalf("上报应 200，实际 %d", code)
	}
	up, down, _, _, _ = s.userTraffic.totals(u1)
	if up+down != 600+300*100 {
		t.Fatalf("倍率乘积 120 应钳到 100，总量应为 %d，实际 %d", 600+300*100, up+down)
	}
	// 节点无倍率字段（0）按 1 算。
	s.cfg.RelayMultiplier = 0
	s.nodes.mu.Lock()
	s.nodes.getNode(1).Multiplier = 0
	_ = s.nodes.saveLocked()
	s.nodes.mu.Unlock()
	body3 := strings.Replace(body, `"window_start":1000,"window_end":1060`, `"window_start":3000,"window_end":3060`, 1)
	if code, _ := postTraffic1404(t, h, cert, body3); code != 200 {
		t.Fatalf("上报应 200，实际 %d", code)
	}
	up, down, _, _, _ = s.userTraffic.totals(u1)
	if up+down != 600+300*100+300 {
		t.Fatalf("倍率 0 应按 1 算，总量应为 %d，实际 %d", 600+300*100+300, up+down)
	}
}

// ---------- 5. 健康存取与状态 ----------

func TestNodeTrafficHealth1404(t *testing.T) {
	s, h, cookie, cert := setupTrafficNode1404(t)
	// 没上报过：详情里 backend_health 是 never。
	code, body := authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	if code != 200 {
		t.Fatalf("节点详情应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	bh := m["backend_health"].(map[string]any)
	if bh["status"] != "never" || bh["detail"] != "尚未上报" {
		t.Fatalf("没上报过应为 never／尚未上报：%v", bh)
	}
	// 上报 Xray 未运行＋last_error → offline，行上带最后错误。
	bad := `{"node_id":1,"window_start":1000,"window_end":1060,"users":[],"inbounds":[],"online":[],"health":{"agent_version":"0.1.4","xray_version":"26.11.9","xray_running":false,"xray_restarts":2,"config_version":3,"last_error":"xray exited with code 1","cpu_percent":1.1,"mem_mb":100}}`
	if code, _ := postTraffic1404(t, h, cert, bad); code != 200 {
		t.Fatalf("上报应 200，实际 %d", code)
	}
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/nodes", "")
	row := respMap1400(t, body)["nodes"].([]any)[0].(map[string]any)
	bh = row["backend_health"].(map[string]any)
	if bh["status"] != "offline" || !strings.Contains(bh["detail"].(string), "Xray 未运行") || !strings.Contains(bh["detail"].(string), "xray exited with code 1") {
		t.Fatalf("Xray 未运行时行上应显示未运行＋最后错误：%v", bh)
	}
	if bh["xray_restarts"] != float64(2) || bh["config_version"] != float64(3) {
		t.Fatalf("健康字段不对：%v", bh)
	}
	// 上报 Xray 正常 → online＋配置版本。
	good := strings.Replace(bad, `"xray_running":false`, `"xray_running":true`, 1)
	good = strings.Replace(good, `"window_start":1000,"window_end":1060`, `"window_start":1060,"window_end":1120`, 1)
	good = strings.Replace(good, `"last_error":"xray exited with code 1"`, `"last_error":""`, 1)
	if code, _ := postTraffic1404(t, h, cert, good); code != 200 {
		t.Fatalf("上报应 200，实际 %d", code)
	}
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	bh = respMap1400(t, body)["backend_health"].(map[string]any)
	if bh["status"] != "online" || !strings.Contains(bh["detail"].(string), "配置版本 v3") {
		t.Fatalf("Xray 正常时应在线＋配置版本：%v", bh)
	}
	// 把最后上报时间拨到 200 秒前 → 失联（3 个窗口没收到上报）。
	s.nodes.mu.Lock()
	s.nodes.getNode(1).LastTrafficAt = time.Now().Add(-200 * time.Second).Unix()
	_ = s.nodes.saveLocked()
	s.nodes.mu.Unlock()
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	bh = respMap1400(t, body)["backend_health"].(map[string]any)
	if bh["status"] != "offline" || !strings.Contains(bh["detail"].(string), "失联") {
		t.Fatalf("超过 180 秒没上报应显示失联：%v", bh)
	}
}

// ---------- 6. 节点详情流量块 ----------

func TestNodeDetailTrafficBlock1404(t *testing.T) {
	s, h, cookie, cert := setupTrafficNode1404(t)
	u1, u2, _ := seedUsers1402(s)
	body := trafficBody1404(1, `[{"user_key":"`+u1+`","up":100,"down":200,"inbound_tag":"reality-443"},{"user_key":"`+u2+`","up":50,"down":50,"inbound_tag":"reality-443"}]`)
	if code, _ := postTraffic1404(t, h, cert, body); code != 200 {
		t.Fatalf("上报应 200，实际 %d", code)
	}
	code, resp := authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	if code != 200 {
		t.Fatalf("节点详情应 200，实际 %d：%s", code, resp)
	}
	m := respMap1400(t, resp)
	tr := m["traffic"].(map[string]any)
	if tr["up_period"] != float64(150) || tr["down_period"] != float64(250) {
		t.Fatalf("本期按人合计应为 150/250，实际 %v", tr)
	}
	users, _ := tr["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("流量块应有 2 个用户，实际 %v", users)
	}
	inbounds, _ := tr["inbounds"].([]any)
	if len(inbounds) != 1 || inbounds[0].(map[string]any)["inbound_tag"] != "reality-443" {
		t.Fatalf("流量块应有 reality-443 入站总量，实际 %v", inbounds)
	}
	if inbounds[0].(map[string]any)["up"] != float64(700) {
		t.Fatalf("入站总量上行应为 700，实际 %v", inbounds[0])
	}
}

// ---------- 7. 后端节点限流执法：80% 提醒、100% 停用拿掉用户 ----------

func TestBackendNodeEnforce1404(t *testing.T) {
	s, h, _, cert := setupTrafficNode1404(t)
	// 限流用户：上限 1000，total 模式；无 UserForwards（后端节点专属）。
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-lim1", Name: "限流用户", TrafficLimitBytes: 1000, TrafficLimitMode: "total"})
	key := "subuser:su-lim1"
	vBefore := nodeVersion1402(t, s, 1)
	// 推 800（80%）：提醒标志置上，不停用。
	body := trafficBody1404(1, `[{"user_key":"`+key+`","up":500,"down":300,"inbound_tag":"reality-443"}]`)
	if code, _ := postTraffic1404(t, h, cert, body); code != 200 {
		t.Fatalf("上报应 200，实际 %d", code)
	}
	s.enforceBackendNodeTrafficLimits()
	if u := findSubUserByID(s.cfg.SubUsers, "su-lim1"); u == nil || !u.TrafficWarned80 {
		t.Fatalf("用量到 80%% 应置提醒标志")
	}
	if u := findSubUserByID(s.cfg.SubUsers, "su-lim1"); u.Disabled {
		t.Fatalf("80%% 不应停用")
	}
	// 再推 300（总量 1100 ≥ 1000）：100% 停用。
	body2 := strings.Replace(body, `"window_start":1000,"window_end":1060`, `"window_start":1060,"window_end":1120`, 1)
	body2 = strings.Replace(body2, `"up":500,"down":300`, `"up":200,"down":100`, 1)
	if code, _ := postTraffic1404(t, h, cert, body2); code != 200 {
		t.Fatalf("上报应 200，实际 %d", code)
	}
	s.enforceBackendNodeTrafficLimits()
	u := findSubUserByID(s.cfg.SubUsers, "su-lim1")
	if u == nil || !u.Disabled || u.DisabledReason != "traffic" {
		t.Fatalf("用量到 100%% 应停用（原因 traffic）：%+v", u)
	}
	if got := nodeVersion1402(t, s, 1); got != vBefore+1 {
		t.Fatalf("100%% 停用后节点 ConfigVersion 应＋1，实际 %d→%d", vBefore, got)
	}
	// 配置文档里没他了（agent 下次拉取就拿掉他）。
	users := configUsers1404(t, h, cert)
	for _, raw := range users {
		if row, ok := raw.(map[string]any); ok && row["user_id"] == key {
			t.Fatalf("停用后配置文档里不应再有该用户：%v", users)
		}
	}
}

// ---------- 8. 无 relay 时 resume／disable 不报错 ----------

func TestNoRelayResumeDisable1404(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	// 故意不写 relay.conf 与 config.toml 桩：管理面板场景。
	// applyUserForwards 应直接返回 nil。
	if err := s.applyUserForwards(); err != nil {
		t.Fatalf("无 relay 时 applyUserForwards 应返回 nil，实际 %v", err)
	}
	createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	seedUsers1402(s)
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":null}`); code != 200 {
		t.Fatalf("节点 1 指派全部应 200，实际 %d：%s", code, body)
	}
	// 停用（以前吞错所以是 200，这次不断言新行为，只不断言恢复）。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/subscriptions/users/su-test1/disable", "{}"); code != 200 {
		t.Fatalf("无 relay 时停用应 200，实际 %d：%s", code, body)
	}
	// 恢复：2026-11-23 实锤 bug——以前这里 500（读 relay.conf 失败），
	// 现在应 200。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/subscriptions/users/su-test1/resume", "{}"); code != 200 {
		t.Fatalf("无 relay 时恢复应 200，实际 %d：%s", code, body)
	}
	// 恢复后节点用户列表里他回来了。
	m := nodeUsers1402(t, h, cookie, 1)
	if !nodeUserIDs1402(m)["subuser:su-test1"] {
		t.Fatalf("恢复后节点 1 应重新下发该用户：%v", m)
	}
}

// ---------- 9. 有 relay 但重建失败时仍报错 ----------

func TestRelayRebuildFailureStillErrors1404(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	// 有 relay.conf，但 config.toml 是个目录 → 读失败，重建失败。
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "relay.conf"), []byte(""), 0o644); err != nil {
		t.Fatalf("写 relay.conf 桩失败：%v", err)
	}
	if err := os.Mkdir(filepath.Join(s.env.RealmDir, "config.toml"), 0o755); err != nil {
		t.Fatalf("建 config.toml 目录桩失败：%v", err)
	}
	if err := s.applyUserForwards(); err == nil {
		t.Fatalf("有 relay 但 config.toml 读失败时 applyUserForwards 应报错")
	}
	createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	seedUsers1402(s)
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":null}`); code != 200 {
		t.Fatalf("节点 1 指派全部应 200，实际 %d：%s", code, body)
	}
	// 停用：状态已落盘（Disabled=true），但接口应报 500（重建失败）。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/subscriptions/users/su-test1/disable", "{}"); code != 500 {
		t.Fatalf("重建失败时停用应 500，实际 %d：%s", code, body)
	}
	if u := findSubUserByID(s.cfg.SubUsers, "su-test1"); u == nil || !u.Disabled {
		t.Fatalf("重建失败时停用状态仍应落盘")
	}
	// 恢复：同样报错（恢复接口按旧约定返回 400），状态已清（Disabled=false）。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/subscriptions/users/su-test1/resume", "{}"); code != 400 {
		t.Fatalf("重建失败时恢复应 400，实际 %d：%s", code, body)
	}
	if u := findSubUserByID(s.cfg.SubUsers, "su-test1"); u == nil || u.Disabled {
		t.Fatalf("重建失败时恢复状态仍应落盘（已恢复）")
	}
}

// ---------- 10. 四级指派解析成 users[].inbounds ----------

func TestBackendAssignmentInbounds1404(t *testing.T) {
	s, h, cookie, cert := setupTrafficNode1404(t)
	u1, u2, m1 := seedUsers1402(s)
	// 节点上建两个入站。
	for _, b := range []string{
		`{"inbound_tag":"reality-443","protocol":"vless","transport":"reality","security":"reality","port":443,"sni":"www.example.com"}`,
		`{"inbound_tag":"xhttp-443","protocol":"vless","transport":"xhttp","security":"reality","port":8443,"sni":"www.example.com"}`,
	} {
		if code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds", b); code != 200 {
			t.Fatalf("建入站应 200，实际 %d：%s", code, body)
		}
	}
	// 默认全部：inbounds 字段空（= 全部入站）。
	users := configUsers1404(t, h, cert)
	row := findConfigUser1404(t, users, u1)
	if _, has := row["inbounds"]; has {
		t.Fatalf("默认全部时 users[].inbounds 应为空（字段省略）：%v", row)
	}
	// 入站级：只指 reality-443。
	vBefore := nodeVersion1402(t, s, 1)
	code, body := authed1400(t, h, cookie, "PUT", "/api/v1/users/"+u1+"/backend-assignment", `{"scope":"inbound","targets":["1:reality-443"]}`)
	if code != 200 {
		t.Fatalf("入站级指派应 200，实际 %d：%s", code, body)
	}
	if got := nodeVersion1402(t, s, 1); got != vBefore+1 {
		t.Fatalf("指派改后节点 ConfigVersion 应＋1，实际 %d→%d", vBefore, got)
	}
	users = configUsers1404(t, h, cert)
	row = findConfigUser1404(t, users, u1)
	inb, _ := row["inbounds"].([]any)
	if len(inb) != 1 || inb[0] != "reality-443" {
		t.Fatalf("入站级指派后 users[].inbounds 应为 [reality-443]，实际 %v", row["inbounds"])
	}
	// 其他用户不受影响。
	row2 := findConfigUser1404(t, users, u2)
	if _, has := row2["inbounds"]; has {
		t.Fatalf("其他用户 inbounds 应仍为空：%v", row2)
	}
	// 节点级：只上节点 1，inbounds 为空（= 全部入站）。
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/users/"+u1+"/backend-assignment", `{"scope":"node","targets":["1"]}`); code != 200 {
		t.Fatalf("节点级指派应 200，实际 %d：%s", code, body)
	}
	users = configUsers1404(t, h, cert)
	row = findConfigUser1404(t, users, u1)
	if _, has := row["inbounds"]; has {
		t.Fatalf("节点级指派后 inbounds 应为空：%v", row)
	}
	// 组级：用户进组、组配节点 1 → 在；组没配节点 → 不在。
	s.cfg.SubGroups = append(s.cfg.SubGroups, SubGroup{ID: "sg-g1", Name: "一组", Selection: []SubSelection{}, CreatedAt: time.Now().UTC().Format(time.RFC3339), BackendNodeIDs: []int64{1}})
	findSubUserByID(s.cfg.SubUsers, "su-test1").GroupID = "sg-g1"
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/users/"+u1+"/backend-assignment", `{"scope":"group","targets":["sg-g1"]}`); code != 200 {
		t.Fatalf("组级指派应 200，实际 %d：%s", code, body)
	}
	users = configUsers1404(t, h, cert)
	findConfigUser1404(t, users, u1) // 在节点 1 上
	// 组改成没配节点 → 用户从配置文档里消失。
	s.cfg.SubGroups[0].BackendNodeIDs = nil
	users = configUsers1404(t, h, cert)
	for _, raw := range users {
		if row, ok := raw.(map[string]any); ok && row["user_id"] == u1 {
			t.Fatalf("组没配节点时组级用户不应在配置文档里：%v", users)
		}
	}
	// 合并订阅也能入站级指派。
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/users/"+m1+"/backend-assignment", `{"scope":"inbound","targets":["1:xhttp-443"]}`); code != 200 {
		t.Fatalf("合并订阅入站级指派应 200，实际 %d：%s", code, body)
	}
	users = configUsers1404(t, h, cert)
	row = findConfigUser1404(t, users, m1)
	inb, _ = row["inbounds"].([]any)
	if len(inb) != 1 || inb[0] != "xhttp-443" {
		t.Fatalf("合并订阅 inbounds 应为 [xhttp-443]，实际 %v", row["inbounds"])
	}
	// 非法指派 400：不存在的节点、不存在的入站、组不是自己的。
	for _, b := range []string{
		`{"scope":"node","targets":["999"]}`,
		`{"scope":"inbound","targets":["1:nope-443"]}`,
		`{"scope":"inbound","targets":["bad"]}`,
		`{"scope":"group","targets":["sg-nope"]}`,
		`{"scope":"whatever","targets":[]}`,
	} {
		if code, _ := authed1400(t, h, cookie, "PUT", "/api/v1/users/"+u1+"/backend-assignment", b); code != 400 {
			t.Fatalf("非法指派 %s 应 400，实际 %d", b, code)
		}
	}
	// 未登录 401、不存在的用户 404。
	if code, _ := authed1400(t, h, "", "PUT", "/api/v1/users/"+u1+"/backend-assignment", `{"scope":"all"}`); code != 401 {
		t.Fatalf("未登录改指派应 401，实际 %d", code)
	}
	if code, _ := authed1400(t, h, cookie, "PUT", "/api/v1/users/subuser:su-nope/backend-assignment", `{"scope":"all"}`); code != 404 {
		t.Fatalf("不存在的用户改指派应 404，实际 %d", code)
	}
	// GET 指派：scope 与 targets 回得来。
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/users/"+u1+"/backend-assignment", "")
	if code != 200 {
		t.Fatalf("GET 指派应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	if m["scope"] != "group" {
		t.Fatalf("GET 指派 scope 应为 group，实际 %v", m)
	}
}

// ---------- 11. 版本断言 ----------

func TestVersion1404(t *testing.T) {
	if Version != "1.4.37" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
}
