package main

// v1.4.0：轻量节点后端切片 1——面板模型与接口＋agent 骨架与入网。
//
// 覆盖：建节点／列表／删节点／重新生成入网令牌（会话认证）；
// 入网换证（令牌一次有效、过期作废、节点不存在、令牌与节点不匹配）；
// 配置拉取分流（带有效节点证书走新后端配置、没证书走旧 D10 接口、
// 错 CA 的证书验不过、删节点后回 deleted:true 的空配置）；
// 节点 CA 文件与权限；版本断言。

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer1400(t *testing.T) *Server {
	t.Helper()
	t.Setenv("PANEL_INITIAL_PASSWORD", "test-password-123")
	s, err := newServer(Env{PanelHome: t.TempDir(), RealmDir: t.TempDir(), KitDir: t.TempDir(), Port: 11111})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return s
}

// authed1400 走完整路由（带会话 cookie），返回状态码与响应体。
func authed1400(t *testing.T, h http.Handler, cookie, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func respMap1400(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("响应不是 JSON：%v：%s", err, body)
	}
	return m
}

// createNode1400 建一个节点，返回节点 id 与入网令牌。
func createNode1400(t *testing.T, h http.Handler, cookie, name, addr string) (int64, string) {
	t.Helper()
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes", `{"name":"`+name+`","address":"`+addr+`"}`)
	if code != 200 {
		t.Fatalf("建节点应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	if !m["ok"].(bool) {
		t.Fatalf("建节点 ok 应为 true：%s", body)
	}
	id, _ := m["node_id"].(float64)
	tok, _ := m["join_token"].(string)
	cmd, _ := m["install_command"].(string)
	if id == 0 || tok == "" || !strings.HasPrefix(tok, "join_") || len(tok) != 37 {
		t.Fatalf("建节点返回不对：node_id=%v token=%q", id, tok)
	}
	if !strings.Contains(cmd, "--node-id "+itoa1400(int64(id))) || !strings.Contains(cmd, "--token "+tok) ||
		!strings.Contains(cmd, "install-node-backend.sh") {
		t.Fatalf("安装命令不对：%q", cmd)
	}
	return int64(id), tok
}

// joinNode1400 用令牌换证，返回客户端证书 PEM 与密钥 PEM。
func joinNode1400(t *testing.T, h http.Handler, nodeID int64, token string) (string, string) {
	t.Helper()
	code, body := authed1400(t, h, "", "POST", "/api/v1/node-backend/join",
		`{"node_id":`+itoa1400(nodeID)+`,"token":"`+token+`"}`)
	if code != 200 {
		t.Fatalf("入网换证应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	certPEM, _ := m["client_cert_pem"].(string)
	keyPEM, _ := m["client_key_pem"].(string)
	if !strings.HasPrefix(certPEM, "-----BEGIN CERTIFICATE-----") ||
		!strings.HasPrefix(keyPEM, "-----BEGIN EC PRIVATE KEY-----") {
		t.Fatalf("换证返回的 PEM 不对：cert=%q key=%q", certPEM[:40], keyPEM[:40])
	}
	return certPEM, keyPEM
}

func itoa1400(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func parseCert1400(t *testing.T, pemStr string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		t.Fatal("PEM 解析失败")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("证书解析失败：%v", err)
	}
	return cert
}

// configReq1400 构造一发拉配置请求：cert 为空表示不带证书。
func configReq1400(cert *x509.Certificate) *http.Request {
	req := httptest.NewRequest("GET", "/api/v1/node/config", nil)
	req.Header.Set("X-Node-Backend-Version", "0.1.0")
	if cert != nil {
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	}
	return req
}

// ---------- 1. 建节点／列表／重名重地址 ----------

func TestNodesCreateList1400(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)

	// 未登录 → 401。
	if code, _ := authed1400(t, h, "", "GET", "/api/v1/nodes", ""); code != 401 {
		t.Fatalf("未登录调节点列表应 401，实际 %d", code)
	}
	id, tok := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	if id != 1 {
		t.Fatalf("第一个节点 id 应为 1，实际 %d", id)
	}
	// 重名、重地址都拒绝。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes", `{"name":"新落地-01","address":"203.0.113.2"}`); code != 400 || !strings.Contains(body, "同名") {
		t.Fatalf("重名建节点应 400，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes", `{"name":"新落地-02","address":"203.0.113.1"}`); code != 400 || !strings.Contains(body, "同地址") {
		t.Fatalf("重地址建节点应 400，实际 %d：%s", code, body)
	}
	// 空名字拒绝。
	if code, _ := authed1400(t, h, cookie, "POST", "/api/v1/nodes", `{"name":"","address":"203.0.113.3"}`); code != 400 {
		t.Fatalf("空名字建节点应 400，实际 %d", code)
	}
	// 列表里有它，健康 never（还没入网拉过配置），入网令牌只在创建
	// 返回里出现一次——nodes.json 里只有哈希，没有明文令牌。
	code, body := authed1400(t, h, cookie, "GET", "/api/v1/nodes", "")
	if code != 200 {
		t.Fatalf("节点列表应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	nodes, _ := m["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("节点列表应 1 条，实际 %d", len(nodes))
	}
	row := nodes[0].(map[string]any)
	if row["name"] != "新落地-01" || row["health"] != "never" || row["joined"] != false {
		t.Fatalf("节点行不对：%v", row)
	}
	raw, err := os.ReadFile(filepath.Join(s.env.PanelHome, "nodes.json"))
	if err != nil {
		t.Fatalf("nodes.json 应存在：%v", err)
	}
	if strings.Contains(string(raw), tok) {
		t.Fatalf("nodes.json 里不应出现入网令牌明文")
	}
	if st, err := os.Stat(filepath.Join(s.env.PanelHome, "nodes.json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("nodes.json 权限应为 600：%v", err)
	}
	// 详情接口。
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	if code != 200 || !strings.Contains(body, "新落地-01") {
		t.Fatalf("节点详情应 200 且带名字，实际 %d：%s", code, body)
	}
	if code, _ := authed1400(t, h, cookie, "GET", "/api/v1/nodes/999", ""); code != 404 {
		t.Fatalf("不存在的节点详情应 404，实际 %d", code)
	}
}

// ---------- 2. 入网换证 ----------

func TestNodeBackendJoin1400(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	_, tok := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")

	// 令牌不对 → 400；节点不存在 → 404；node_id 缺失 → 400。
	if code, _ := authed1400(t, h, "", "POST", "/api/v1/node-backend/join", `{"node_id":1,"token":"join_00000000000000000000000000000000"}`); code != 400 {
		t.Fatalf("错令牌入网应 400，实际 %d", code)
	}
	if code, _ := authed1400(t, h, "", "POST", "/api/v1/node-backend/join", `{"node_id":999,"token":"`+tok+`"}`); code != 404 {
		t.Fatalf("不存在的节点入网应 404，实际 %d", code)
	}
	if code, _ := authed1400(t, h, "", "POST", "/api/v1/node-backend/join", `{"token":"`+tok+`"}`); code != 400 {
		t.Fatalf("缺 node_id 入网应 400，实际 %d", code)
	}
	// 令牌属于节点 1，拿去给节点 2 用 → 400。
	createNode1400(t, h, cookie, "新落地-02", "203.0.113.2")
	if code, _ := authed1400(t, h, "", "POST", "/api/v1/node-backend/join", `{"node_id":2,"token":"`+tok+`"}`); code != 400 {
		t.Fatalf("令牌与节点不匹配应 400，实际 %d", code)
	}
	// 正常换证：证书能解析，CN=node-1，用途 ClientAuth，10 年有效期；
	// 拿节点 CA 验得过。
	certPEM, _ := joinNode1400(t, h, 1, tok)
	cert := parseCert1400(t, certPEM)
	if cert.Subject.CommonName != "node-1" {
		t.Fatalf("客户端证书 CN 应为 node-1，实际 %q", cert.Subject.CommonName)
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("客户端证书用途应只有 ClientAuth，实际 %v", cert.ExtKeyUsage)
	}
	if cert.NotAfter.Sub(cert.NotBefore) < 9*365*24*time.Hour {
		t.Fatalf("客户端证书有效期应约 10 年，实际 %v", cert.NotAfter.Sub(cert.NotBefore))
	}
	caPEM, err := os.ReadFile(filepath.Join(s.env.PanelHome, "certs", "node-ca.crt"))
	if err != nil {
		t.Fatalf("节点 CA 证书应存在：%v", err)
	}
	caCert := parseCert1400(t, string(caPEM))
	if !caCert.IsCA || caCert.Subject.CommonName != "relay-panel node CA" {
		t.Fatalf("节点 CA 不对：IsCA=%v CN=%q", caCert.IsCA, caCert.Subject.CommonName)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("客户端证书拿节点 CA 验不过：%v", err)
	}
	if st, err := os.Stat(filepath.Join(s.env.PanelHome, "certs", "node-ca.key")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("节点 CA 私钥权限应为 600：%v", err)
	}
	// 令牌一次有效：用过就作废，再用 → 400。
	if code, _ := authed1400(t, h, "", "POST", "/api/v1/node-backend/join", `{"node_id":1,"token":"`+tok+`"}`); code != 400 {
		t.Fatalf("用过的令牌再入网应 400，实际 %d", code)
	}
	// 节点已标已入网。
	_, body := authed1400(t, h, cookie, "GET", "/api/v1/nodes", "")
	m := respMap1400(t, body)
	row := m["nodes"].([]any)[0].(map[string]any)
	if row["joined"] != true {
		t.Fatalf("入网后 joined 应为 true：%v", row)
	}
}

// ---------- 3. 入网令牌过期与重新生成 ----------

func TestJoinTokenExpiryRegen1400(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	_, tok := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	// 把过期时间拨到过去 → 令牌过期，换证 400。
	s.nodes.mu.Lock()
	s.nodes.getNode(1).JoinTokenExpires = time.Now().Add(-time.Minute).Unix()
	s.nodes.mu.Unlock()
	if code, _ := authed1400(t, h, "", "POST", "/api/v1/node-backend/join", `{"node_id":1,"token":"`+tok+`"}`); code != 400 {
		t.Fatalf("过期令牌入网应 400，实际 %d", code)
	}
	// 重新生成：旧令牌作废，新令牌能用。
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/join-token", `{}`)
	if code != 200 {
		t.Fatalf("重新生成入网令牌应 200，实际 %d：%s", code, body)
	}
	newTok := respMap1400(t, body)["join_token"].(string)
	if newTok == tok || !strings.HasPrefix(newTok, "join_") {
		t.Fatalf("新令牌应与旧的不同且 join_ 开头：%q", newTok)
	}
	if code, _ := authed1400(t, h, "", "POST", "/api/v1/node-backend/join", `{"node_id":1,"token":"`+tok+`"}`); code != 400 {
		t.Fatalf("旧令牌在重新生成后应作废，实际 %d", code)
	}
	joinNode1400(t, h, 1, newTok)
}

// ---------- 4. 配置拉取分流：证书走新接口，没证书走旧接口 ----------

func TestNodeBackendConfigDispatch1400(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	_, tok := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	certPEM, _ := joinNode1400(t, h, 1, tok)
	cert := parseCert1400(t, certPEM)

	// 没证书、没 rpt 令牌 → 旧接口 401。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, configReq1400(nil))
	if rec.Code != 401 {
		t.Fatalf("没证书没令牌应 401（旧接口），实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 没证书、有 rpt 令牌 → 旧接口 200，带 panel_version。
	tok2 := generateRemoteToken()
	s.cfg.RemoteTokenHash = hashRemoteToken(tok2)
	req := configReq1400(nil)
	req.Header.Set("Authorization", "Bearer "+tok2)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"panel_version"`) {
		t.Fatalf("有 rpt 令牌应走旧接口 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 带有效节点证书 → 新接口 200：空配置（切片 1 三张表全空），
	// etag 带上；拉完健康变 online。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, configReq1400(cert))
	if rec.Code != 200 {
		t.Fatalf("带证书拉配置应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	m := respMap1400(t, rec.Body.String())
	if m["node_id"] != float64(1) || m["config_version"] != float64(1) || m["deleted"] != false {
		t.Fatalf("新接口配置不对：%v", m)
	}
	etag, _ := m["etag"].(string)
	if etag == "" || rec.Header().Get("ETag") != etag {
		t.Fatalf("新接口应回 ETag 头：%q vs %q", rec.Header().Get("ETag"), etag)
	}
	for _, k := range []string{"inbounds", "users", "certs"} {
		arr, ok := m[k].([]any)
		if !ok || len(arr) != 0 {
			t.Fatalf("切片 1 的 %s 应为空数组：%v", k, m[k])
		}
	}
	// 同一 etag 再拉 → 304（无 body）；弱校验 W/ 前缀也认。
	req = configReq1400(cert)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("etag 没变应 304 空 body，实际 %d len=%d", rec.Code, rec.Body.Len())
	}
	req = configReq1400(cert)
	req.Header.Set("If-None-Match", "W/"+etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 304 {
		t.Fatalf("弱 etag 也应 304，实际 %d", rec.Code)
	}
	// 错 CA 签的证书 → 验不过，走旧接口，没令牌 → 401。
	wrongCert := selfSignedCert1400(t, "node-1")
	req = configReq1400(wrongCert)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("错 CA 的证书应验不过、走旧接口 401，实际 %d", rec.Code)
	}
	// 证书有效但节点不存在（CN=node-999，CA 是对的）→ 404。
	otherCert := issueCert1400(t, s, 999)
	req = configReq1400(otherCert)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("不存在的节点拉配置应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 拉完一次后健康 online。
	code, body := authed1400(t, h, cookie, "GET", "/api/v1/nodes", "")
	_ = code
	row := respMap1400(t, body)["nodes"].([]any)[0].(map[string]any)
	if row["health"] != "online" || row["agent_version"] != "0.1.0" {
		t.Fatalf("拉过配置后健康应 online、agent 版本 0.1.0：%v", row)
	}
}

// selfSignedCert1400 用别的 CA 自签一张 CN=node-1 的证书（错 CA 用）。
func selfSignedCert1400(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	key := testECKey1400(t)
	tmpl := &x509.Certificate{
		SerialNumber: bigInt1400(1),
		Subject:      pkixName1400(cn),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(randReader1400(), tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("自签证书失败：%v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("解析自签证书失败：%v", err)
	}
	return cert
}

// issueCert1400 用面板节点 CA 给指定 id 签一张客户端证书（不走 join）。
func issueCert1400(t *testing.T, s *Server, nodeID int64) *x509.Certificate {
	t.Helper()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	certPEM, _, err := s.nodes.issueNodeClientCert(s.env.PanelHome, nodeID)
	if err != nil {
		t.Fatalf("签发证书失败：%v", err)
	}
	return parseCert1400(t, string(certPEM))
}

// ---------- 5. 删节点：墓碑＋空配置自停信号＋孤儿路由 ----------

func TestNodesDelete1400(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	_, tok := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	certPEM, _ := joinNode1400(t, h, 1, tok)
	cert := parseCert1400(t, certPEM)

	// 路由表里挂一条指向该节点的路由（切片 1 正常建不出来，直接写 store 模拟切片 8 的状态）。
	s.nodes.mu.Lock()
	s.nodes.data.Routes = append(s.nodes.data.Routes, BackendRelayRoute{
		RelayID: 1, ListenAddr: "0.0.0.0:30004", RemoteNodeID: 1,
		RemoteAddr: "203.0.113.1", RemotePort: 443, Proto: "tcp", Transport: "tcp", Enabled: true,
	})
	s.nodes.mu.Unlock()
	// 不勾一起删 → 400 并列出孤儿路由。
	code, body := authed1400(t, h, cookie, "DELETE", "/api/v1/nodes/1", `{}`)
	if code != 400 || !strings.Contains(body, "orphan_routes") {
		t.Fatalf("有孤儿路由不勾一起删应 400 并列出路由，实际 %d：%s", code, body)
	}
	// 勾一起删 → 200，路由也没了，节点也没了。
	code, body = authed1400(t, h, cookie, "DELETE", "/api/v1/nodes/1", `{"delete_routes":true}`)
	if code != 200 {
		t.Fatalf("勾一起删应 200，实际 %d：%s", code, body)
	}
	if m := respMap1400(t, body); m["deleted_id"] != float64(1) || m["deleted_routes"] != float64(1) {
		t.Fatalf("删除返回不对：%v", m)
	}
	// 再删 → 404。
	if code, _ := authed1400(t, h, cookie, "DELETE", "/api/v1/nodes/1", `{}`); code != 404 {
		t.Fatalf("重复删应 404，实际 %d", code)
	}
	// 删完 agent 再来拉：墓碑认得出，200 回 deleted:true 的空配置。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, configReq1400(cert))
	if rec.Code != 200 {
		t.Fatalf("删节点后拉配置应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	m := respMap1400(t, rec.Body.String())
	if m["deleted"] != true {
		t.Fatalf("删节点后拉配置应 deleted:true：%v", m)
	}
	for _, k := range []string{"inbounds", "users", "certs"} {
		arr, ok := m[k].([]any)
		if !ok || len(arr) != 0 {
			t.Fatalf("删节点后 %s 应为空数组：%v", k, m[k])
		}
	}
	// nodes.json 里墓碑还在（重启后 agent 再来拉还能认出已删）。
	raw, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "nodes.json"))
	if !strings.Contains(string(raw), `"deleted_ids"`) {
		t.Fatalf("nodes.json 里应有 deleted_ids 墓碑")
	}
}

// ---------- 6. 真 TLS 握手：主监听要证书，agent 出示证书能通 ----------

func TestNodeConfigTLSHandshake1400(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	_, tok := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	certPEM, keyPEM := joinNode1400(t, h, 1, tok)
	clientCert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatalf("客户端证书对解析失败：%v", err)
	}
	// 模拟主监听：RequestClientCert（要证书但不强制），与 main.go 同款。
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	srv.StartTLS()
	defer srv.Close()
	// 带证书 → 新接口 200。
	cli := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		Certificates: []tls.Certificate{clientCert}, InsecureSkipVerify: true,
	}}}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/node/config", nil)
	req.Header.Set("X-Node-Backend-Version", "0.1.0")
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("带证书拉配置失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("带证书拉配置应 200，实际 %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil || m["deleted"] != false {
		t.Fatalf("带证书拉配置返回不对：%v %v", m, err)
	}
	// 不带证书 → 旧接口，没令牌 → 401（浏览器行为不受影响）。
	cli2 := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp2, err := cli2.Get(srv.URL + "/api/v1/node/config")
	if err != nil {
		t.Fatalf("不带证书拉配置失败：%v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 401 {
		t.Fatalf("不带证书没令牌应 401，实际 %d", resp2.StatusCode)
	}
}

// ---------- 7. 版本断言 ----------

func TestVersion1400(t *testing.T) {
	if Version != "1.4.27" {
		t.Fatalf("VERSION 应是 1.4.27，实际 %q", Version)
	}
}

// ---------- 测试小 helper ----------

func testECKey1400(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成测试 EC 密钥失败：%v", err)
	}
	return key
}

func bigInt1400(n int64) *big.Int { return big.NewInt(n) }

func pkixName1400(cn string) pkix.Name { return pkix.Name{CommonName: cn} }

func randReader1400() io.Reader { return rand.Reader }
