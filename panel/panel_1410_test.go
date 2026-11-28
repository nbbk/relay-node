package main

// v1.4.10：轻量节点后端切片 6（面板侧）——备用协议 SS／SS2022／SOCKS5，
// 外加节点详情「核心版本」显示修复。
//
// 覆盖：
//   - 新建入站模板默认值：SS／SS2022 端口留空默认 8388、SOCKS5 默认 1080；
//     密码留空自动生成（可改）：普通 SS 与 SOCKS5 是 16 位随机密码，
//     SS2022 是对应方法长度的 base64 密钥（128-gcm 16 字节、
//     256-gcm／chacha20 32 字节；agent 0.1.7 实测口径）；手填的端口
//     与密码原样保留；编辑时留空不改（不重新生成）。
//   - SS2022 密码格式校验：手填不是 base64 或长度不对时保存就 400
//     带提示，不等节点上 Xray 报 bad key。
//   - 核心版本：agent 健康上报里的 xray_version 进节点详情（node 行与
//     backend_health 都带）；上报里没带版本时不覆盖已存的值；没上报过
//     的节点版本为空，不编造。
//   - 订阅核对：只有落地链（nodes.txt twin 链接）进订阅，后端入站目前
//     一条都不进（切片 10 的活）；SS／SS2022 链接在 plain／base64／
//     Clash／sing-box 四种格式里都渲染出来；socks5:// 链接在 Clash 与
//     sing-box 里不出 proxy／outbound。
//   - 版本断言 1.4.12。

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// randomPasswordChars1410 与 auth.go 里 randomPassword 用的字符集一致
// （去掉了 0／1／l／O／I 这些易混淆字符）。
const randomPasswordChars1410 = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"

func isRandomPassword1410(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(randomPasswordChars1410, r) {
			return false
		}
	}
	return true
}

// setupNode1410 建一个后端节点，返回 handler 与会话 cookie。
func setupNode1410(t *testing.T) (*Server, http.Handler, string, int64) {
	t.Helper()
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id, _ := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	return s, h, cookie, id
}

func TestVersion1410(t *testing.T) {
	if Version != "1.4.40" {
		t.Fatalf("VERSION 应是 1.4.33，实际 %q", Version)
	}
}

func TestInboundBackupDefaults1410(t *testing.T) {
	_, h, cookie, id := setupNode1410(t)
	base := "/api/v1/nodes/" + itoa1400(id) + "/inbounds"

	// SS：端口留空、密码留空 → 默认 8388＋16 位随机密码。
	_, ss := createInbound1401(t, h, cookie, id,
		`{"inbound_tag":"ss-8388","protocol":"shadowsocks","transport":"tcp","security":"none","port":0,"ss_method":"aes-256-gcm"}`)
	if ss["port"] != float64(8388) {
		t.Fatalf("SS 默认端口应为 8388，实际 %v", ss["port"])
	}
	ssPw, _ := ss["ss_password"].(string)
	if !isRandomPassword1410(ssPw) {
		t.Fatalf("SS 密码应自动生成 16 位随机密码，实际 %q", ssPw)
	}
	if ss["ss_method"] != "aes-256-gcm" {
		t.Fatalf("SS 加密方法应保留，实际 %v", ss["ss_method"])
	}

	// SS2022：同样默认 8388；密码是对应方法长度的 base64 密钥，
	// 不是普通随机串（agent 0.1.7 实测口径：128-gcm 16 字节）。
	_, ss2 := createInbound1401(t, h, cookie, id,
		`{"inbound_tag":"ss2022-8388","protocol":"shadowsocks","transport":"tcp","security":"none","port":0,"ss_method":"2022-blake3-aes-128-gcm"}`)
	if ss2["port"] != float64(8388) {
		t.Fatalf("SS2022 默认端口应为 8388，实际 %v", ss2["port"])
	}
	if b64KeyBytes1410(t, ss2["ss_password"].(string)) != 16 {
		t.Fatalf("SS2022 128-gcm 自动生成的密码应是 16 字节密钥的 base64，实际 %q", ss2["ss_password"])
	}

	// SOCKS5：端口留空、密码留空 → 默认 1080＋16 位随机密码，用户名保留。
	skID, sk := createInbound1401(t, h, cookie, id,
		`{"inbound_tag":"socks-1080","protocol":"socks","transport":"tcp","security":"none","port":0,"socks_username":"user1"}`)
	if sk["port"] != float64(1080) {
		t.Fatalf("SOCKS5 默认端口应为 1080，实际 %v", sk["port"])
	}
	skPw, _ := sk["socks_password"].(string)
	if !isRandomPassword1410(skPw) {
		t.Fatalf("SOCKS5 密码应自动生成 16 位随机密码，实际 %q", skPw)
	}
	if sk["socks_username"] != "user1" {
		t.Fatalf("SOCKS5 用户名应保留，实际 %v", sk["socks_username"])
	}

	// 手填的端口与密码原样保留，不被默认值覆盖。
	_, ssExp := createInbound1401(t, h, cookie, id,
		`{"inbound_tag":"ss-9000","protocol":"shadowsocks","transport":"tcp","security":"none","port":9000,"ss_method":"aes-256-gcm","ss_password":"explicit-pw-123"}`)
	if ssExp["port"] != float64(9000) || ssExp["ss_password"] != "explicit-pw-123" {
		t.Fatalf("手填的端口与密码应原样保留，实际 port=%v pw=%v", ssExp["port"], ssExp["ss_password"])
	}

	// 编辑时留空不改：只改用户名，密码还是新建时自动生成的那一个。
	code, body := authed1400(t, h, cookie, "PUT", base+"/"+itoa1400(skID), `{"socks_username":"user2"}`)
	if code != 200 {
		t.Fatalf("编辑 SOCKS5 入站应 200，实际 %d：%s", code, body)
	}
	edited := respMap1400(t, body)["inbound"].(map[string]any)
	if edited["socks_password"] != skPw {
		t.Fatalf("编辑留空时密码不应重新生成，实际 %q，原值 %q", edited["socks_password"], skPw)
	}
	if edited["socks_username"] != "user2" {
		t.Fatalf("编辑后用户名应为 user2，实际 %v", edited["socks_username"])
	}
	if edited["port"] != float64(1080) {
		t.Fatalf("编辑后端口应仍为 1080，实际 %v", edited["port"])
	}

	// 列表视图里 SOCKS5 配置卡要的四个字段都在（地址走节点行，见 jsdom）。
	list := listInbounds1401(t, h, cookie, id)
	skRow := inboundByTag1401(list, "socks-1080")
	if skRow == nil {
		t.Fatalf("入站列表里找不到 socks-1080")
	}
	for _, f := range []string{"socks_username", "socks_password", "port"} {
		if skRow[f] == nil || skRow[f] == "" {
			t.Fatalf("入站列表里 SOCKS5 的 %s 字段不应为空：%v", f, skRow)
		}
	}
}

// b64KeyBytes1410 把 base64 串解出字节数（非法 base64 直接失败）。
func b64KeyBytes1410(t *testing.T, s string) int {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("应是合法 base64：%q：%v", s, err)
	}
	return len(raw)
}

// TestInboundSS2022Key1410 SS2022 密码口径（agent 0.1.7 实测）：
// 自动生成按方法长度（256-gcm／chacha20 都是 32 字节）；手填错格式
// 在保存时就 400 带提示，不等节点上 Xray 报 bad key。
func TestInboundSS2022Key1410(t *testing.T) {
	_, h, cookie, id := setupNode1410(t)
	_, k256 := createInbound1401(t, h, cookie, id,
		`{"inbound_tag":"ss2022-256","protocol":"shadowsocks","transport":"tcp","security":"none","port":0,"ss_method":"2022-blake3-aes-256-gcm"}`)
	if b64KeyBytes1410(t, k256["ss_password"].(string)) != 32 {
		t.Fatalf("SS2022 256-gcm 自动生成的密码应是 32 字节密钥的 base64，实际 %q", k256["ss_password"])
	}
	_, kch := createInbound1401(t, h, cookie, id,
		`{"inbound_tag":"ss2022-chacha","protocol":"shadowsocks","transport":"tcp","security":"none","port":0,"ss_method":"2022-blake3-chacha20-poly1305"}`)
	if b64KeyBytes1410(t, kch["ss_password"].(string)) != 32 {
		t.Fatalf("SS2022 chacha20 自动生成的密码应是 32 字节密钥的 base64，实际 %q", kch["ss_password"])
	}
	// 手填合法密钥（agent 实测用的 16 字节示例）通过。
	_, _ = createInbound1401(t, h, cookie, id,
		`{"inbound_tag":"ss2022-manual-ok","protocol":"shadowsocks","transport":"tcp","security":"none","port":9001,"ss_method":"2022-blake3-aes-128-gcm","ss_password":"AAECAwQFBgcICQoLDA0ODw=="}`)
	// 手填错格式一律 400 带提示：不是 base64、长度不对（16 位随机串
	// 解出来只有 12 字节）、32 字节密钥用在 128-gcm 上。
	base := "/api/v1/nodes/" + itoa1400(id) + "/inbounds"
	for _, tc := range []struct{ tag, body, want string }{
		{"bad-not-b64", `{"inbound_tag":"ss2022-bad1","protocol":"shadowsocks","transport":"tcp","security":"none","port":9002,"ss_method":"2022-blake3-aes-128-gcm","ss_password":"!!!not-base64!!!"}`, "base64"},
		{"bad-16char", `{"inbound_tag":"ss2022-bad2","protocol":"shadowsocks","transport":"tcp","security":"none","port":9003,"ss_method":"2022-blake3-aes-128-gcm","ss_password":"abcdefghijklmnop"}`, "长度不对"},
		{"bad-32-for-128", `{"inbound_tag":"ss2022-bad3","protocol":"shadowsocks","transport":"tcp","security":"none","port":9004,"ss_method":"2022-blake3-aes-128-gcm","ss_password":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`, "长度不对"},
	} {
		code, body := authed1400(t, h, cookie, "POST", base, tc.body)
		if code != 400 || !strings.Contains(body, tc.want) {
			t.Fatalf("SS2022 手填错密码（%s）应 400 且提示含 %q，实际 %d：%s", tc.tag, tc.want, code, body)
		}
	}
}

// TestInboundCoreVersion1410 核心版本以上报的 xray_version 为准。
func TestInboundCoreVersion1410(t *testing.T) {
	_, h, cookie, _ := setupNode1410(t)
	id2, tok := createNode1400(t, h, cookie, "新落地-02", "203.0.113.2")
	// 上报要走 mTLS：第二个节点入网换证。
	certPEM, _ := joinNode1400(t, h, id2, tok)
	cert := parseCert1400(t, certPEM)
	nodePath := "/api/v1/nodes/" + itoa1400(id2)

	// 没上报过：版本为空，不编造。
	code, body := authed1400(t, h, cookie, "GET", nodePath, "")
	if code != 200 {
		t.Fatalf("节点详情应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	node := m["node"].(map[string]any)
	if node["xray_version"] != "" {
		t.Fatalf("没上报过时 xray_version 应为空，实际 %v", node["xray_version"])
	}
	bh := m["backend_health"].(map[string]any)
	if bh["xray_version"] != "" {
		t.Fatalf("没上报过时 backend_health.xray_version 应为空，实际 %v", bh["xray_version"])
	}

	// 上报一窗，health 带 xray_version 26.11.9。
	report := `{"node_id":` + itoa1400(id2) + `,"window_start":1000,"window_end":1060,"users":[],"inbounds":[],"online":[],"health":{"agent_version":"0.1.5","xray_version":"26.11.9","singbox_version":"1.14.4","xray_running":true,"xray_restarts":0,"singbox_running":true,"singbox_restarts":0,"config_version":5,"last_error":"","cpu_percent":3.2,"mem_mb":210}}`
	if code, body := postTraffic1404(t, h, cert, report); code != 200 {
		t.Fatalf("流量上报应 200，实际 %d：%s", code, body)
	}
	code, body = authed1400(t, h, cookie, "GET", nodePath, "")
	if code != 200 {
		t.Fatalf("节点详情应 200，实际 %d：%s", code, body)
	}
	m = respMap1400(t, body)
	node = m["node"].(map[string]any)
	if node["xray_version"] != "26.11.9" {
		t.Fatalf("节点行 xray_version 应为上报的 26.11.9，实际 %v", node["xray_version"])
	}
	bh = m["backend_health"].(map[string]any)
	if bh["xray_version"] != "26.11.9" {
		t.Fatalf("backend_health.xray_version 应为上报的 26.11.9，实际 %v", bh["xray_version"])
	}

	// 再报一窗，health 里不带 xray_version：已存的值不被覆盖。
	report2 := `{"node_id":` + itoa1400(id2) + `,"window_start":2000,"window_end":2060,"users":[],"inbounds":[],"online":[],"health":{"agent_version":"0.1.5","xray_running":true,"xray_restarts":0,"config_version":5,"last_error":"","cpu_percent":3.2,"mem_mb":210}}`
	if code, body := postTraffic1404(t, h, cert, report2); code != 200 {
		t.Fatalf("第二窗上报应 200，实际 %d：%s", code, body)
	}
	code, body = authed1400(t, h, cookie, "GET", nodePath, "")
	m = respMap1400(t, body)
	node = m["node"].(map[string]any)
	if node["xray_version"] != "26.11.9" {
		t.Fatalf("上报里没带版本时已存的 26.11.9 不应被覆盖，实际 %v", node["xray_version"])
	}
}

// TestSubscriptionSSAndSocks1420 订阅核对：后端入站一条都不进订阅
// （切片 10 的活）；落地链的 SS／SS2022 链接四种格式都渲染；SOCKS5
// 在 Clash 与 sing-box 里也渲染（1.4.20 切片 10 改了 1.4.10 的旧口径：
// 旧名 TestSubscriptionSSAndNoSocks1410，旧断言是“不出节点”，已作废）。
func TestSubscriptionSSAndSocks1420(t *testing.T) {
	s := newTestServer1400(t)

	// 只有后端节点的面板：订阅链接集合为空，后端入站不进订阅。
	links := s.collectSubLinks(nil)
	if len(links) != 0 {
		t.Fatalf("只有后端节点时订阅链接应为空，实际 %d 条", len(links))
	}

	// 落地链的 SS／SS2022 链接（明文 SIP002 写法）＋一条 SOCKS5 链接。
	sub := []subLink{
		{Name: "测试-SS", Link: "ss://aes-256-gcm:test-password-1@203.0.113.1:8388#测试-SS"},
		{Name: "测试-SS2022", Link: "ss://2022-blake3-aes-128-gcm:test-password-2@203.0.113.1:8388#测试-SS2022"},
		{Name: "测试-S5", Link: "socks5://socks-user:socks-pw@203.0.113.1:1080#测试-S5"},
	}
	plain, _, err := renderSubContent(sub, "plain", "split")
	if err != nil {
		t.Fatalf("plain 渲染不应报错：%v", err)
	}
	if !strings.Contains(plain, "ss://aes-256-gcm:test-password-1@203.0.113.1:8388") ||
		!strings.Contains(plain, "ss://2022-blake3-aes-128-gcm:test-password-2@203.0.113.1:8388") {
		t.Fatalf("plain 订阅里应有两条 SS 链接：%s", plain)
	}
	b64, _, err := renderSubContent(sub, "base64", "split")
	if err != nil {
		t.Fatalf("base64 渲染不应报错：%v", err)
	}
	decoded, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if derr != nil || !strings.Contains(string(decoded), "ss://aes-256-gcm:test-password-1@203.0.113.1:8388") {
		t.Fatalf("base64 解开后应有 SS 链接：%v / %s", derr, string(decoded))
	}
	clash, _, err := renderSubContent(sub, "clash", "split")
	if err != nil {
		t.Fatalf("clash 渲染不应报错：%v", err)
	}
	if !strings.Contains(clash, "type: ss") || !strings.Contains(clash, `cipher: "aes-256-gcm"`) ||
		!strings.Contains(clash, `cipher: "2022-blake3-aes-128-gcm"`) || !strings.Contains(clash, "port: 8388") {
		t.Fatalf("Clash 里应有两条 SS proxy：\n%s", clash)
	}
	if strings.Contains(clash, "测试-S5") && strings.Contains(clash, "type: socks5") &&
		strings.Contains(clash, `username: "socks-user"`) && strings.Contains(clash, "port: 1080") {
		// SOCKS5 在 Clash 里渲染为 socks5 proxy（1.4.20 切片 10 新口径）。
	} else {
		t.Fatalf("Clash 里应有 SOCKS5 proxy（type: socks5，测试-S5，1080）：\n%s", clash)
	}
	sb, _, err := renderSubContent(sub, "singbox", "split")
	if err != nil {
		t.Fatalf("singbox 渲染不应报错：%v", err)
	}
	var doc map[string]any
	if jerr := json.Unmarshal([]byte(sb), &doc); jerr != nil {
		t.Fatalf("sing-box JSON 解析失败：%v：%s", jerr, sb)
	}
	outs, _ := doc["outbounds"].([]any)
	ssCount, socksCount := 0, 0
	for _, o := range outs {
		om, _ := o.(map[string]any)
		if om["type"] == "shadowsocks" {
			ssCount++
			if om["method"] != "aes-256-gcm" && om["method"] != "2022-blake3-aes-128-gcm" {
				t.Fatalf("sing-box SS 出站方法不对：%v", om)
			}
			if om["server"] != "203.0.113.1" || om["server_port"] != float64(8388) {
				t.Fatalf("sing-box SS 出站地址端口不对：%v", om)
			}
		}
		if om["type"] == "socks" {
			socksCount++
			if om["server"] != "203.0.113.1" || om["server_port"] != float64(1080) ||
				om["version"] != "5" || om["username"] != "socks-user" || om["password"] != "socks-pw" {
				t.Fatalf("sing-box SOCKS5 出站字段不对：%v", om)
			}
		}
	}
	if ssCount != 2 {
		t.Fatalf("sing-box 里应有 2 条 SS 出站，实际 %d", ssCount)
	}
	if socksCount != 1 {
		t.Fatalf("sing-box 里应有 1 条 SOCKS5 出站，实际 %d", socksCount)
	}
}
