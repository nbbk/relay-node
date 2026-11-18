package main

// 1.2.2 单测：原始链接存取与卡片链接、自定义端口证书探测、编辑落地、
// 改名、续期自定义天数、twin 命名、历史记录删除。

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const test122UUID = "11111111-2222-3333-4444-555555555555"

// twin122Stub：--twin/--door 按参数生成链接（直连用 $3 落地 IP、
// 任意门用 $3 中转地址 + $4 转发端口）；--nodes 直接成功；
// 其他调用模拟一次完整安装，写出单落地 relay.conf（443）+ 空 config.toml。
const twin122Stub = `#!/bin/bash
if [ "$1" = "--twin" ]; then echo "vless://` + test122UUID + `@$3:443?security=tls&sni=$4#test-直连"; exit 0; fi
if [ "$1" = "--door" ]; then echo "vless://` + test122UUID + `@$3:$4?security=tls&sni=$5#test-中转"; exit 0; fi
if [ "$1" = "--nodes" ]; then exit 0; fi
mkdir -p "$REALM_DIR"
cat > "$REALM_DIR/relay.conf" <<'EOF'
LANDING=203.0.113.1
LANDING_IS_DOMAIN=0
DOMAIN=""
CERT_DOMAIN="example.com"
PRESET=1
MODE_NOTE="tiktok-node（Xray）"
PORTS="tcp:443:443"
LANDING_COUNT=1
ALL_PORTS="tcp:443:443"
SHARE80=0
SHARE443=0
EOF
touch "$REALM_DIR/config.toml"
exit 0
`

const test122OrigLink = "vless://" + test122UUID + "@example.com:443?security=tls&sni=example.com#test"

// newTest122Server 与 newTestAddLandingServer 一样，但 panelHome 由调用方指定，
// 这样“重启面板”（新 Server 同目录）能验证 state.json 里的状态还在。
func newTest122Server(t *testing.T, kitDir, realmDir, panelHome string) *Server {
	t.Helper()
	t.Setenv("PANEL_INITIAL_PASSWORD", "test-password-123")
	s, err := newServer(Env{PanelHome: panelHome, RealmDir: realmDir, KitDir: kitDir, Port: 11111})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return s
}

// fakeSystemctl122 往 PATH 最前面放一个永远成功的 systemctl：
// restartRealm 在测试机上没有真的 realm 服务会失败，
// 用它冒充 systemctl restart/is-active 成功，覆盖“重启生效”这一步。
func fakeSystemctl122(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte("#!/bin/bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
}

func callHandler122(t *testing.T, fn func(http.ResponseWriter, *http.Request), method, path, body string, pathVals ...string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// pathVals 成对出现：key, value, key, value…（handleUpdateLanding 这类接口
	// 用 r.PathValue("index") 取编号，直接调 handler 时要手动设）
	for i := 0; i+1 < len(pathVals); i += 2 {
		req.SetPathValue(pathVals[i], pathVals[i+1])
	}
	// 没显式传时从 URL 里取：/api/v1/landings/<index>/... → index
	if req.PathValue("index") == "" {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		for i, seg := range parts {
			if seg == "landings" && i+1 < len(parts) {
				req.SetPathValue("index", parts[i+1])
			}
		}
	}
	rec := httptest.NewRecorder()
	fn(rec, req)
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s %s 响应不是 JSON：%v（%s）", method, path, err, rec.Body.String())
	}
	return rec.Code, resp
}

// statusLandings122 调 handleStatus，返回 landings 数组（check.sh 不存在时
// 走 TLS 握手回退；443 没监听时只影响证书字段，不影响链接与名称断言）。
func statusLandings122(t *testing.T, s *Server) []any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleStatus(rec, httptest.NewRequest("GET", "/api/v1/status", nil))
	if rec.Code != 200 {
		t.Fatalf("handleStatus 状态=%d：%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("status 响应不是 JSON：%v", err)
	}
	ls, _ := resp["landings"].([]any)
	return ls
}

func landingByIndex122(t *testing.T, ls []any, index int) map[string]any {
	t.Helper()
	for _, v := range ls {
		m, _ := v.(map[string]any)
		if idx, _ := m["index"].(float64); int(idx) == index {
			return m
		}
	}
	t.Fatalf("status 里没有落地 %d：%v", index, ls)
	return nil
}

// ---------- 问题 1：原始链接存取往返 + 卡片链接生成 ----------

func TestOrigLinkRoundTripAndCardLinks(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	panelHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(twin122Stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REALM_DIR", realmDir)
	s := newTest122Server(t, kitDir, realmDir, panelHome)
	resp := postAddLanding(t, s, `{"landing":"203.0.113.1","preset":"1","cert_domain":"example.com","name":"香港-01","orig_link":"`+test122OrigLink+`","link_name":"香港-01"}`)
	if resp["ok"] != true {
		t.Fatalf("加落地应成功：%v", resp)
	}
	// 卡片链接：现场算出中转 + 直连，备注按节点名称区分
	ls := statusLandings122(t, s)
	l := landingByIndex122(t, ls, 1)
	if l["orig_link"] != test122OrigLink {
		t.Fatalf("status orig_link=%v want 原链接", l["orig_link"])
	}
	if l["name"] != "香港-01" {
		t.Fatalf("status name=%v want 香港-01", l["name"])
	}
	links, _ := l["links"].([]any)
	if len(links) != 2 {
		t.Fatalf("卡片 links=%v want 2 条（中转+直连）", links)
	}
	relay, _ := links[0].(map[string]any)
	direct, _ := links[1].(map[string]any)
	if relay["label"] != "中转" || !strings.Contains(relay["link"].(string), "@example.com:443") ||
		!strings.Contains(relay["link"].(string), "%E9%A6%99%E6%B8%AF-01-%E4%B8%AD%E8%BD%AC") {
		t.Fatalf("中转链接不对：%v", relay)
	}
	if direct["label"] != "直连" || !strings.Contains(direct["link"].(string), "@203.0.113.1:443") ||
		!strings.Contains(direct["link"].(string), "%E9%A6%99%E6%B8%AF-01-%E7%9B%B4%E8%BF%9E") {
		t.Fatalf("直连链接不对：%v", direct)
	}
	qr, _ := l["qr"].(map[string]any)
	if len(qr) != 2 || qr["中转"] == nil || qr["直连"] == nil {
		t.Fatalf("卡片二维码缺失：%v", qr)
	}
	// 原始链接不进审计日志：审计文件里不能出现 UUID
	audit, _ := os.ReadFile(filepath.Join(panelHome, "audit.log"))
	if strings.Contains(string(audit), test122UUID) {
		t.Fatalf("审计日志里出现了原始链接的 UUID")
	}
	// 重启面板（新 Server 同目录）：原始链接与卡片链接还在
	s2 := newTest122Server(t, kitDir, realmDir, panelHome)
	l2 := landingByIndex122(t, statusLandings122(t, s2), 1)
	if l2["orig_link"] != test122OrigLink {
		t.Fatalf("重启后 orig_link 丢了：%v", l2["orig_link"])
	}
	links2, _ := l2["links"].([]any)
	if len(links2) != 2 {
		t.Fatalf("重启后卡片 links=%v want 2 条", links2)
	}
}

// ---------- 问题 2：自定义端口证书探测 ----------

// selfSignedTLS122 起一个自签 TLS 服务（证书 89 天后到期），返回监听端口。
func selfSignedTLS122(t *testing.T, days int) (int, func()) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		DNSNames:     []string{"example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Duration(days) * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
				time.Sleep(200 * time.Millisecond)
			}(c)
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	return port, func() { _ = ln.Close() }
}

func TestProbeCertCustomPort(t *testing.T) {
	port, stop := selfSignedTLS122(t, 89)
	defer stop()
	// 自定义端口 TLS 落地：监听 24443（不是 443），以前根本不去握手
	l := Landing{
		Landing: "203.0.113.1", CertDomain: "example.com",
		Ports: []PortSpec{{Proto: "tcp", Listen: port, Remote: port}},
	}
	ci, ok := probeCertForLanding(l)
	if !ok || ci.DaysLeft == nil {
		t.Fatalf("自定义端口证书没探到：ok=%v ci=%+v", ok, ci)
	}
	if *ci.DaysLeft < 87 || *ci.DaysLeft > 90 {
		t.Fatalf("剩余天数=%d want 89 左右", *ci.DaysLeft)
	}
	// renewModeFor：握手成功就不判明文
	mode, _ := renewModeFor(l, false, true)
	if strings.Contains(mode, "明文") {
		t.Fatalf("有证书还判明文：%s", mode)
	}
	// 握手失败且没有 443 影子：才判明文
	mode2, _ := renewModeFor(l, false, false)
	if mode2 != "无证书（明文模式）" {
		t.Fatalf("无证书应判明文，实际：%s", mode2)
	}
	// 有 443 端口但暂时握手失败：按手动算，不判明文（可能是落地暂时不通）
	l443 := Landing{Landing: "203.0.113.1", CertDomain: "example.com",
		Ports: []PortSpec{{Proto: "tcp", Listen: 443, Remote: 443}}}
	mode3, _ := renewModeFor(l443, false, false)
	if strings.Contains(mode3, "明文") {
		t.Fatalf("有 443 端口不应判明文：%s", mode3)
	}
}

// ---------- 编辑落地 ----------

const test122EditConf = `LANDING=203.0.113.1
LANDING_IS_DOMAIN=0
DOMAIN=""
CERT_DOMAIN="example.com"
PRESET=1
MODE_NOTE="tiktok-node（Xray）"
PORTS="tcp:80:80 tcp:443:443"
LANDING_COUNT=1
ALL_PORTS="tcp:80:80 tcp:443:443"
SHARE80=0
SHARE443=0
`

const test122EditToml = "[log]\nlevel = \"warn\"\n\n[[endpoints]]\nlisten = \"0.0.0.0:80\"\nremote = \"203.0.113.1:80\"\n\n[[endpoints]]\nlisten = \"0.0.0.0:443\"\nremote = \"203.0.113.1:443\"\n"

func setupEditServer122(t *testing.T) (*Server, string, string) {
	t.Helper()
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	panelHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(twin122Stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(test122EditConf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "config.toml"), []byte(test122EditToml), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeSystemctl122(t)
	s := newTest122Server(t, kitDir, realmDir, panelHome)
	return s, kitDir, realmDir
}

func TestEditLandingRenameOnly(t *testing.T) {
	s, kitDir, realmDir := setupEditServer122(t)
	code, resp := callHandler122(t, s.handleUpdateLanding, "PUT", "/api/v1/landings/1",
		`{"landing":"203.0.113.1","preset":"1","mode":"standard","cert_domain":"example.com","duration_days":-1,"traffic_limit_bytes":0,"speed_limit_mbps":0,"name":"香港-01"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("改名应成功：code=%d resp=%v", code, resp)
	}
	if resp["restarted"] != false {
		t.Fatalf("只改名不应重启 realm：%v", resp)
	}
	conf, _ := os.ReadFile(filepath.Join(realmDir, "relay.conf"))
	if !strings.Contains(string(conf), `NAME="香港-01"`) {
		t.Fatalf("relay.conf 里没有新名称：\n%s", string(conf))
	}
	// 改名不动转发配置与端口
	if !strings.Contains(string(conf), `PORTS="tcp:80:80 tcp:443:443"`) {
		t.Fatalf("改名改动了端口：\n%s", string(conf))
	}
	toml, _ := os.ReadFile(filepath.Join(realmDir, "config.toml"))
	if string(toml) != test122EditToml {
		t.Fatalf("只改名不应动 config.toml：\n%s", string(toml))
	}
	// 卡片标题变了
	l := landingByIndex122(t, statusLandings122(t, s), 1)
	if l["name"] != "香港-01" {
		t.Fatalf("改名后卡片标题=%v want 香港-01", l["name"])
	}
	// 重启后名称还在（新 Server 同目录读 relay.conf）
	s2 := newTest122Server(t, kitDir, realmDir, s.env.PanelHome)
	rc, _, err := s2.readRelayConf()
	if err != nil || rc.Landings[0].Name != "香港-01" {
		t.Fatalf("重启后名称丢了：%v %+v", err, rc)
	}
}

func TestEditLandingPortChange(t *testing.T) {
	s, _, realmDir := setupEditServer122(t)
	code, resp := callHandler122(t, s.handleUpdateLanding, "PUT", "/api/v1/landings/1",
		`{"landing":"203.0.113.1","preset":"3","custom_ports":"30443,30888","mode":"standard","cert_domain":"example.com","duration_days":-1,"traffic_limit_bytes":0,"speed_limit_mbps":0}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("改端口应成功：code=%d resp=%v", code, resp)
	}
	if resp["restarted"] != true {
		t.Fatalf("改端口应重启 realm：%v", resp)
	}
	conf, _ := os.ReadFile(filepath.Join(realmDir, "relay.conf"))
	if !strings.Contains(string(conf), `PORTS="tcp:30443:30443 tcp:30888:30888"`) {
		t.Fatalf("relay.conf 端口没改过来：\n%s", string(conf))
	}
	toml, _ := os.ReadFile(filepath.Join(realmDir, "config.toml"))
	if !strings.Contains(string(toml), `listen = "0.0.0.0:30443"`) || !strings.Contains(string(toml), `remote = "203.0.113.1:30888"`) {
		t.Fatalf("config.toml 转发规则没改过来：\n%s", string(toml))
	}
	if strings.Contains(string(toml), `0.0.0.0:443`) {
		t.Fatalf("config.toml 里还留着旧 443 规则：\n%s", string(toml))
	}
}

func TestEditLandingAddressMigratesState(t *testing.T) {
	s, kitDir, realmDir := setupEditServer122(t)
	// 先种扩展状态：续期 90 天、流量上限 50GB 总量、限速 10Mbps、原始链接
	if code, _ := callHandler122(t, s.handleRenew, "POST", "/api/v1/landings/1/renew", `{"days":90}`); code != 200 {
		t.Fatalf("续期应成功：code=%d", code)
	}
	if code, _ := callHandler122(t, s.handleSetTraffic, "POST", "/api/v1/landings/1/traffic", `{"limit_bytes":53687091200,"limit_mode":"total","speed_limit_mbps":10}`); code != 200 {
		t.Fatalf("设流量应成功：code=%d", code)
	}
	if code, _ := callHandler122(t, s.handleSetOrigLink, "POST", "/api/v1/landings/1/orig-link", `{"orig_link":"`+test122OrigLink+`","link_name":"香港-01"}`); code != 200 {
		t.Fatalf("补粘贴应成功：code=%d", code)
	}
	// 历史里先有一组旧地址的记录
	nodesPath := filepath.Join(kitDir, "nodes.txt")
	relay := "vless://" + test122UUID + "@example.com:443?security=tls&sni=example.com#test-%E4%B8%AD%E8%BD%AC"
	direct := "vless://" + test122UUID + "@203.0.113.1:443?security=tls&sni=example.com#test-%E7%9B%B4%E8%BF%9E"
	if err := upsertLandingLinks(nodesPath, "203.0.113.1", "203.0.113.1", 1, "中转", relay, direct); err != nil {
		t.Fatal(err)
	}
	// 改地址：203.0.113.1 → 203.0.113.9（其他字段原样带上，全量替换）
	code, resp := callHandler122(t, s.handleUpdateLanding, "PUT", "/api/v1/landings/1",
		`{"landing":"203.0.113.9","preset":"1","mode":"standard","cert_domain":"example.com","duration_days":-1,"traffic_limit_bytes":53687091200,"traffic_limit_mode":"total","speed_limit_mbps":10,"orig_link":"`+test122OrigLink+`","link_name":"香港-01"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("改地址应成功：code=%d resp=%v", code, resp)
	}
	// 扩展状态跟着新地址走，旧键没了
	s.stateMu.Lock()
	_, oldExists := s.state.Landings["203.0.113.1"]
	ext, newExists := s.state.Landings["203.0.113.9"]
	s.stateMu.Unlock()
	if oldExists {
		t.Fatalf("旧地址的状态键没删掉")
	}
	if !newExists {
		t.Fatalf("新地址没有继承状态")
	}
	if ext.Expiry == "" {
		t.Fatalf("到期时间没迁过来")
	}
	if t0, err := time.Parse(time.RFC3339, ext.Expiry); err != nil || time.Until(t0) < 89*24*time.Hour {
		t.Fatalf("到期时间不对：%v %v", ext.Expiry, err)
	}
	if ext.TrafficLimitBytes != 53687091200 || ext.SpeedLimitMbps != 10 || ext.OrigLink != test122OrigLink || ext.OrigLinkName != "香港-01" {
		t.Fatalf("流量/限速/原始链接没迁过来：%+v", ext)
	}
	// 转发配置跟新地址走
	conf, _ := os.ReadFile(filepath.Join(realmDir, "relay.conf"))
	if !strings.Contains(string(conf), `LANDING="203.0.113.9"`) {
		t.Fatalf("relay.conf 地址没改：\n%s", string(conf))
	}
	toml, _ := os.ReadFile(filepath.Join(realmDir, "config.toml"))
	if !strings.Contains(string(toml), `remote = "203.0.113.9:443"`) {
		t.Fatalf("config.toml 转发目标没改：\n%s", string(toml))
	}
	// 历史：旧地址分组删了，新地址分组建起来了（直连指向新地址）
	nodes, _ := os.ReadFile(nodesPath)
	if strings.Contains(string(nodes), "203.0.113.1") {
		t.Fatalf("历史里还留着旧地址分组：\n%s", string(nodes))
	}
	groups := parseNodesTxt(nodes)
	if len(groups) != 1 || groups[0].Landing != "203.0.113.9" {
		t.Fatalf("新地址历史分组不对：%+v", groups)
	}
	found := false
	for _, lk := range groups[0].Links {
		if lk.Label == "直连" && strings.Contains(lk.Link, "@203.0.113.9:443") {
			found = true
		}
	}
	if !found {
		t.Fatalf("新地址历史里直连链接不对：%+v", groups[0].Links)
	}
}

func TestEditLandingRejectsInvalid(t *testing.T) {
	s, _, _ := setupEditServer122(t)
	base := `{"landing":"203.0.113.1","preset":"1","mode":"standard","cert_domain":"example.com","duration_days":-1,"traffic_limit_bytes":0,"speed_limit_mbps":0}`
	cases := []struct {
		name string
		body string
	}{
		{"落地地址非法", strings.Replace(base, `"landing":"203.0.113.1"`, `"landing":"not a host!!"`, 1)},
		{"预设非法", strings.Replace(base, `"preset":"1"`, `"preset":"9"`, 1)},
		{"流量为负", strings.Replace(base, `"traffic_limit_bytes":0`, `"traffic_limit_bytes":-1`, 1)},
		{"时长 -2", strings.Replace(base, `"duration_days":-1`, `"duration_days":-2`, 1)},
		{"证书域名非法", strings.Replace(base, `"cert_domain":"example.com"`, `"cert_domain":"not a domain"`, 1)},
	}
	for _, c := range cases {
		code, _ := callHandler122(t, s.handleUpdateLanding, "PUT", "/api/v1/landings/1", c.body)
		if code != 400 {
			t.Fatalf("%s 应 400，实际 %d", c.name, code)
		}
	}
	// 地址撞上别的落地
	s2, _, _ := setupEditServer122(t)
	conf2 := strings.Replace(test122EditConf, "LANDING_COUNT=1", "LANDING_COUNT=2\nLANDING_2=203.0.113.2\nDOMAIN_2=\"\"\nCERT_DOMAIN_2=\"example.com\"\nPRESET_2=1\nMODE_NOTE_2=\"tiktok-node（Xray）\"\nPORTS_2=\"tcp:443:443\"", 1)
	realmDir2 := s2.env.RealmDir
	if err := os.WriteFile(filepath.Join(realmDir2, "relay.conf"), []byte(conf2), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _ := callHandler122(t, s2.handleUpdateLanding, "PUT", "/api/v1/landings/1",
		strings.Replace(base, `"landing":"203.0.113.1"`, `"landing":"203.0.113.2"`, 1))
	if code != 400 {
		t.Fatalf("地址撞车应 400，实际 %d", code)
	}
}

// DurationDays -1（保持不变）校验通过，-2 拒绝。
func TestValidateDurationSentinel(t *testing.T) {
	req := addLandingReq{Landing: "203.0.113.1", Preset: "1", Mode: "standard", DurationDays: -1}
	if msg := validateLandingReqFields(req); msg != "" {
		t.Fatalf("duration -1 应通过，实际：%s", msg)
	}
	req.DurationDays = -2
	if msg := validateLandingReqFields(req); msg == "" {
		t.Fatalf("duration -2 应拒绝")
	}
}

// ---------- 续期自定义天数 ----------

func TestRenewCustomDays(t *testing.T) {
	s, _, _ := setupEditServer122(t)
	code, resp := callHandler122(t, s.handleRenew, "POST", "/api/v1/landings/1/renew", `{"days":45}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("自定义 45 天续期应成功：code=%d resp=%v", code, resp)
	}
	exp1, _ := resp["expiry"].(string)
	t1, err := time.Parse(time.RFC3339, exp1)
	if err != nil || time.Until(t1) < 44*24*time.Hour || time.Until(t1) > 46*24*time.Hour {
		t.Fatalf("45 天后续期到期不对：%v %v", exp1, err)
	}
	// 再续 45 天：从上次到期往后延，总共约 90 天
	code, resp = callHandler122(t, s.handleRenew, "POST", "/api/v1/landings/1/renew", `{"days":45}`)
	if code != 200 {
		t.Fatalf("第二次续期应成功：code=%d", code)
	}
	exp2, _ := resp["expiry"].(string)
	t2, _ := time.Parse(time.RFC3339, exp2)
	if time.Until(t2) < 89*24*time.Hour || time.Until(t2) > 91*24*time.Hour {
		t.Fatalf("两次 45 天后续期应约 90 天，实际：%v", exp2)
	}
	for _, bad := range []string{`{"days":0}`, `{"days":-5}`, `{"days":3651}`, `{"days":100000}`} {
		code, _ := callHandler122(t, s.handleRenew, "POST", "/api/v1/landings/1/renew", bad)
		if code != 400 {
			t.Fatalf("续期 %s 应 400，实际 %d", bad, code)
		}
	}
}

// ---------- twin 命名 ----------

func TestTwinNaming122(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir, "install.sh"), []byte(twin122Stub), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "RELAY_ADDR=\"203.0.113.9\"\nLANDING=203.0.113.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"example.com\"\nCERT_DOMAIN=\"example.com\"\nPRESET=1\nMODE_NOTE=\"tiktok-node\"\nMODE=\"standard\"\nPORTS=\"tcp:443:443\"\nLANDING_COUNT=1\nALL_PORTS=\"tcp:443:443\"\nSHARE80=0\nSHARE443=0\n"
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestAddLandingServer(t, kitDir, realmDir)
	// 填了节点名称：直连“<名>-直连”、中转“<名>-中转”
	code, resp := postTwin(t, s, `{"link":"`+test122OrigLink+`","landing_ip":"203.0.113.1","name":"香港-01"}`)
	if code != 200 {
		t.Fatalf("twin 状态=%d resp=%v", code, resp)
	}
	rl, _ := resp["relay_link"].(string)
	dl, _ := resp["direct_link"].(string)
	if !strings.Contains(rl, "%E9%A6%99%E6%B8%AF-01-%E4%B8%AD%E8%BD%AC") {
		t.Fatalf("填名后中转备注不对：%s", rl)
	}
	if !strings.Contains(dl, "%E9%A6%99%E6%B8%AF-01-%E7%9B%B4%E8%BF%9E") {
		t.Fatalf("填名后直连备注不对：%s", dl)
	}
	// 留空：用原链接备注名加后缀
	code, resp = postTwin(t, s, `{"link":"`+test122OrigLink+`","landing_ip":"203.0.113.1"}`)
	if code != 200 {
		t.Fatalf("留空 twin 状态=%d", code)
	}
	rl, _ = resp["relay_link"].(string)
	dl, _ = resp["direct_link"].(string)
	if !strings.Contains(rl, "#test-%E4%B8%AD%E8%BD%AC") || !strings.Contains(dl, "#test-%E7%9B%B4%E8%BF%9E") {
		t.Fatalf("留空应按原备注名加后缀：relay=%s direct=%s", rl, dl)
	}
	// 二维码与 nodes.txt 历史里的链接一致
	qr, _ := resp["qr"].(map[string]any)
	if len(qr) != 2 {
		t.Fatalf("twin 二维码应有 2 个：%v", qr)
	}
	b, _ := os.ReadFile(filepath.Join(kitDir, "nodes.txt"))
	if !strings.Contains(string(b), rl) || !strings.Contains(string(b), dl) {
		t.Fatalf("nodes.txt 里的链接与返回的不一致：\n%s", string(b))
	}
}

// ---------- 历史记录删除 ----------

const test122NodesTxt = `# relay-node 双节点（2026-11-18T00:00:00Z）
## 落地 1：203.0.113.1（手动 twin）
[第 1 条]
中转：vless://` + test122UUID + `@example.com:443?security=tls&sni=example.com#one-%E4%B8%AD%E8%BD%AC
直连：vless://` + test122UUID + `@203.0.113.1:443?security=tls&sni=example.com#one-%E7%9B%B4%E8%BF%9E
## 落地 2：203.0.113.2（手动 twin）
[第 1 条]
中转：vless://` + test122UUID + `@example.com:443?security=tls&sni=example.com#two-%E4%B8%AD%E8%BD%AC
直连：vless://` + test122UUID + `@203.0.113.2:443?security=tls&sni=example.com#two-%E7%9B%B4%E8%BF%9E
## 落地 3：203.0.113.9（手动 twin）
[第 1 条]
中转：vless://` + test122UUID + `@example.com:443?security=tls&sni=example.com#three-%E4%B8%AD%E8%BD%AC
直连：vless://` + test122UUID + `@203.0.113.9:443?security=tls&sni=example.com#three-%E7%9B%B4%E8%BF%9E
`

func TestDeleteLinksGroups(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	nodesPath := filepath.Join(kitDir, "nodes.txt")
	if err := os.WriteFile(nodesPath, []byte(test122NodesTxt), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestAddLandingServer(t, kitDir, realmDir)
	// 单删一组：中间那组没了，头尾两组还在
	code, resp := callHandler122(t, s.handleDeleteLinksGroups, "POST", "/api/v1/links/delete", `{"indices":[1]}`)
	if code != 200 || resp["removed"] != float64(1) {
		t.Fatalf("单删应成功：code=%d resp=%v", code, resp)
	}
	b, _ := os.ReadFile(nodesPath)
	if strings.Contains(string(b), "203.0.113.2") {
		t.Fatalf("删掉的组还在文件里：\n%s", string(b))
	}
	if !strings.Contains(string(b), "203.0.113.1") || !strings.Contains(string(b), "203.0.113.9") {
		t.Fatalf("其他组被误删了：\n%s", string(b))
	}
	// 批量删剩下两组
	code, resp = callHandler122(t, s.handleDeleteLinksGroups, "POST", "/api/v1/links/delete", `{"indices":[0,1]}`)
	if code != 200 || resp["removed"] != float64(2) {
		t.Fatalf("批量删应成功：code=%d resp=%v", code, resp)
	}
	groups := parseNodesTxt(mustRead122(t, nodesPath))
	if len(groups) != 0 {
		t.Fatalf("批量删后还剩 %d 组", len(groups))
	}
	// 刷新历史（读接口）仍是删后的样子
	rec := httptest.NewRecorder()
	s.handleLinks(rec, httptest.NewRequest("GET", "/api/v1/links", nil))
	var lr struct {
		Groups []linkGroup `json:"groups"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	if len(lr.Groups) != 0 {
		t.Fatalf("删完刷新历史还剩 %d 组", len(lr.Groups))
	}
	// 非法输入拒绝：空数组、越界序号
	for _, bad := range []string{`{"indices":[]}`, `{"indices":[5]}`, `{"indices":[-1]}`} {
		code, _ := callHandler122(t, s.handleDeleteLinksGroups, "POST", "/api/v1/links/delete", bad)
		if code != 400 {
			t.Fatalf("删历史 %s 应 400，实际 %d", bad, code)
		}
	}
}

func mustRead122(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---------- 历史分组名称 ----------

func TestLinksGroupName(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	conf := "LANDING=203.0.113.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"\"\nCERT_DOMAIN=\"example.com\"\nPRESET=1\nMODE_NOTE=\"tiktok-node\"\nMODE=\"standard\"\nPORTS=\"tcp:443:443\"\nLANDING_COUNT=1\nALL_PORTS=\"tcp:443:443\"\nSHARE80=0\nSHARE443=0\nNAME=\"香港-01\"\n"
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kitDir, "nodes.txt"), []byte(test122NodesTxt), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestAddLandingServer(t, kitDir, realmDir)
	rec := httptest.NewRecorder()
	s.handleLinks(rec, httptest.NewRequest("GET", "/api/v1/links", nil))
	var lr struct {
		Groups []linkGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatalf("历史响应不是 JSON：%v", err)
	}
	if len(lr.Groups) != 3 {
		t.Fatalf("groups=%d want 3", len(lr.Groups))
	}
	if lr.Groups[0].Name != "香港-01" {
		t.Fatalf("登记过的分组应用显示名，实际：%q", lr.Groups[0].Name)
	}
	if lr.Groups[1].Name != "203.0.113.2" {
		t.Fatalf("没登记的分组默认用落地地址，实际：%q", lr.Groups[1].Name)
	}
}

// ---------- 改名不影响转发与历史 ----------

func TestRenameKeepsForwardingAndHistory(t *testing.T) {
	s, kitDir, realmDir := setupEditServer122(t)
	nodesPath := filepath.Join(kitDir, "nodes.txt")
	relay := "vless://" + test122UUID + "@example.com:443?security=tls&sni=example.com#test-%E4%B8%AD%E8%BD%AC"
	direct := "vless://" + test122UUID + "@203.0.113.1:443?security=tls&sni=example.com#test-%E7%9B%B4%E8%BF%9E"
	if err := upsertLandingLinks(nodesPath, "203.0.113.1", "203.0.113.1", 1, "中转", relay, direct); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(nodesPath)
	code, resp := callHandler122(t, s.handleUpdateLanding, "PUT", "/api/v1/landings/1",
		`{"landing":"203.0.113.1","preset":"1","mode":"standard","cert_domain":"example.com","duration_days":-1,"traffic_limit_bytes":0,"speed_limit_mbps":0,"name":"香港-01"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("改名应成功：code=%d resp=%v", code, resp)
	}
	after, _ := os.ReadFile(nodesPath)
	if string(before) != string(after) {
		t.Fatalf("改名改动了历史记录")
	}
	toml, _ := os.ReadFile(filepath.Join(realmDir, "config.toml"))
	if string(toml) != test122EditToml {
		t.Fatalf("改名改动了转发规则")
	}
	conf, _ := os.ReadFile(filepath.Join(realmDir, "relay.conf"))
	if !strings.Contains(string(conf), `PORTS="tcp:80:80 tcp:443:443"`) || !strings.Contains(string(conf), `LANDING="203.0.113.1"`) {
		t.Fatalf("改名改动了落地地址或端口：\n%s", string(conf))
	}
}
