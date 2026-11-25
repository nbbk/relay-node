package main

// v1.3.39 订阅补 Hysteria2 与 TUIC：落地能跑的 HY2/TUIC 以前只在
// base64/plain 订阅里有，sing-box JSON 与 Clash YAML 里被跳过
// （singboxOutboundFromLink 与 clashProxyFromLink 遇到不支持的协议
// 直接丢弃）。现在两种格式都出：sing-box 里是 hysteria2 / tuic 出站
// （TLS 恒开、server_name 取 sni、alpn 按链接参数、TUIC 的
// congestion_control 只认 cubic/new_reno/bbr；也认 obfs=salamander +
// obfs-password 与 insecure=1）；Clash 里是 hysteria2 / tuic proxy
// （字段顺序与落地 install.sh 生成的 Clash 参考一致：TUIC 的拥塞控制
// 在 Clash 里叫 congestion-controller，sing-box 里叫 congestion_control）。
// 链接格式就是落地 install.sh 生成的那两种：
// hysteria2://<密码>@<地址>:<端口>?sni=<域名>&alpn=h3
// tuic://<uuid>:<密码>@<地址>:<端口>?congestion_control=bbr&alpn=h3&sni=<域名>
// 节点模型不用动：链接从 nodes.txt 原样进 collectSubLinks，数据全在
// URI 里；parseNodeLink（粘贴原始链接的校验）与 twin 推导仍只认
// vless/trojan/vmess/ss，手动粘贴 hy2/tuic 链接当原始链接不在本版范围。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hy2TuicPair1339 落地 install.sh 生成格式的 HY2（中转）+ TUIC（直连）。
var hy2TuicPair1339 = []subLink{
	{Name: "智利-HY2-中转", Link: "hysteria2://test-hy2-pass@example.com:24444?sni=example.com&alpn=h3#智利-HY2-中转"},
	{Name: "智利-TUIC-直连", Link: "tuic://11111111-2222-3333-4444-555555555555:test-tuic-pass@203.0.113.1:24445?congestion_control=bbr&alpn=h3&sni=example.com#智利-TUIC-直连"},
}

// outboundByTag1339 从 sing-box JSON 里按 tag 取出站。
func outboundByTag1339(t *testing.T, doc map[string]any, tag string) map[string]any {
	t.Helper()
	raw, _ := doc["outbounds"].([]any)
	for _, o := range raw {
		m, _ := o.(map[string]any)
		if m != nil && m["tag"] == tag {
			return m
		}
	}
	t.Fatalf("出站里找不到 tag=%q：%v", tag, doc["outbounds"])
	return nil
}

// selectorMembers1339 取 tag=Proxy 的 selector 的成员。
func selectorMembers1339(t *testing.T, doc map[string]any) []string {
	t.Helper()
	proxy := outboundByTag1339(t, doc, "Proxy")
	raw, _ := proxy["outbounds"].([]any)
	var members []string
	for _, m := range raw {
		if s, _ := m.(string); s != "" {
			members = append(members, s)
		}
	}
	return members
}

// tlsOf1339 取出站的 tls 段。
func tlsOf1339(t *testing.T, ob map[string]any) map[string]any {
	t.Helper()
	tls, _ := ob["tls"].(map[string]any)
	if tls == nil {
		t.Fatalf("出站 %v 缺 tls 段", ob["tag"])
	}
	return tls
}

// TestSingboxHy2Tuic1339 HY2 与 TUIC 在 sing-box JSON 里出站：字段
// 与落地配置对得上（HY2 密码、TUIC uuid+密码+bbr、两边 tls 的
// server_name 与 alpn h3），两个节点都在 Proxy selector 与自动选择
// 里；HY2/TUIC 没有 WS transport。
func TestSingboxHy2Tuic1339(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(renderSingboxJSON(hy2TuicPair1339)), &doc); err != nil {
		t.Fatalf("sing-box JSON 解析失败：%v", err)
	}
	hy2 := outboundByTag1339(t, doc, "智利-HY2-中转")
	if hy2["type"] != "hysteria2" || hy2["server"] != "example.com" {
		t.Fatalf("HY2 出站的 type/server 不对：%v", hy2)
	}
	if port, _ := hy2["server_port"].(float64); port != 24444 {
		t.Fatalf("HY2 出站的 server_port 应是 24444，实际 %v", hy2["server_port"])
	}
	if hy2["password"] != "test-hy2-pass" {
		t.Fatalf("HY2 出站的 password 不对：%v", hy2["password"])
	}
	tls := tlsOf1339(t, hy2)
	if tls["enabled"] != true || tls["server_name"] != "example.com" {
		t.Fatalf("HY2 的 tls 应是 enabled + server_name=example.com：%v", tls)
	}
	if alpn, _ := tls["alpn"].([]any); len(alpn) != 1 || alpn[0] != "h3" {
		t.Fatalf("HY2 的 tls.alpn 应是 [h3]：%v", tls["alpn"])
	}
	if _, has := hy2["transport"]; has {
		t.Fatalf("HY2 出站不应有 transport（没有 WS）：%v", hy2)
	}

	tuic := outboundByTag1339(t, doc, "智利-TUIC-直连")
	if tuic["type"] != "tuic" || tuic["server"] != "203.0.113.1" {
		t.Fatalf("TUIC 出站的 type/server 不对：%v", tuic)
	}
	if port, _ := tuic["server_port"].(float64); port != 24445 {
		t.Fatalf("TUIC 出站的 server_port 应是 24445，实际 %v", tuic["server_port"])
	}
	if tuic["uuid"] != "11111111-2222-3333-4444-555555555555" || tuic["password"] != "test-tuic-pass" {
		t.Fatalf("TUIC 出站的 uuid/password 不对：%v", tuic)
	}
	if tuic["congestion_control"] != "bbr" {
		t.Fatalf("TUIC 出站的 congestion_control 应是 bbr：%v", tuic["congestion_control"])
	}
	tls = tlsOf1339(t, tuic)
	if tls["enabled"] != true || tls["server_name"] != "example.com" {
		t.Fatalf("TUIC 的 tls 应是 enabled + server_name=example.com：%v", tls)
	}
	if alpn, _ := tls["alpn"].([]any); len(alpn) != 1 || alpn[0] != "h3" {
		t.Fatalf("TUIC 的 tls.alpn 应是 [h3]：%v", tls["alpn"])
	}
	if _, has := tuic["transport"]; has {
		t.Fatalf("TUIC 出站不应有 transport（没有 WS）：%v", tuic)
	}

	members := selectorMembers1339(t, doc)
	if len(members) != 2 || members[0] != "智利-HY2-中转" || members[1] != "智利-TUIC-直连" {
		t.Fatalf("Proxy selector 成员应是两个新节点：%v", members)
	}
	auto := outboundByTag1339(t, doc, "自动选择")
	raw, _ := auto["outbounds"].([]any)
	if len(raw) != 2 {
		t.Fatalf("自动选择成员应是 2 个：%v", raw)
	}
}

// TestSingboxHy2TuicOptions1339 链接里的可选参数：HY2 的 obfs 与
// TUIC 没写 congestion_control 时不写对应字段（不写默认值猜测）。
func TestSingboxHy2TuicOptions1339(t *testing.T) {
	links := []subLink{
		{Name: "HY2-obfs", Link: "hysteria2://pw@example.com:24444?sni=example.com&alpn=h3&obfs=salamander&obfs-password=obfs-pw#HY2-obfs"},
		{Name: "TUIC-nocc", Link: "tuic://11111111-2222-3333-4444-555555555555:pw@203.0.113.1:24445?sni=example.com#TUIC-nocc"},
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(renderSingboxJSON(links)), &doc); err != nil {
		t.Fatalf("sing-box JSON 解析失败：%v", err)
	}
	hy2 := outboundByTag1339(t, doc, "HY2-obfs")
	obfs, _ := hy2["obfs"].(map[string]any)
	if obfs == nil || obfs["type"] != "salamander" || obfs["password"] != "obfs-pw" {
		t.Fatalf("HY2 的 obfs 应是 salamander + obfs-pw：%v", hy2["obfs"])
	}
	tuic := outboundByTag1339(t, doc, "TUIC-nocc")
	if _, has := tuic["congestion_control"]; has {
		t.Fatalf("TUIC 没写 congestion_control 时不应补默认值：%v", tuic["congestion_control"])
	}
	if _, has := tlsOf1339(t, tuic)["alpn"]; has {
		t.Fatalf("TUIC 没写 alpn 时 tls 里不应有 alpn：%v", tuic["tls"])
	}
}

// TestSingboxHy2TuicMissingFields1339 字段缺失的 HY2/TUIC 链接被跳过、
// 不断链：好的节点还在；全是坏链接时出合法空配置（没有 Proxy 组）。
func TestSingboxHy2TuicMissingFields1339(t *testing.T) {
	broken := []subLink{
		{Name: "HY2-没密码", Link: "hysteria2://@example.com:24444?sni=example.com#HY2-没密码"},
		{Name: "TUIC-没密码", Link: "tuic://11111111-2222-3333-4444-555555555555@203.0.113.1:24445?sni=example.com#TUIC-没密码"},
		{Name: "TUIC-没uuid", Link: "tuic://:test-tuic-pass@203.0.113.1:24445?sni=example.com#TUIC-没uuid"},
		{Name: "HY2-端口0", Link: "hysteria2://pw@example.com:0?sni=example.com#HY2-端口0"},
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(renderSingboxJSON(broken)), &doc); err != nil {
		t.Fatalf("全坏链接的 sing-box JSON 解析失败：%v", err)
	}
	for _, tag := range outboundTags1338(t, doc) {
		if tag == "Proxy" {
			t.Fatalf("全坏链接时不应有 Proxy 组，实际 %v", outboundTags1338(t, doc))
		}
	}
	// 坏链接混着好链接：好链接还在，坏的被跳过。
	mixed := append([]subLink{broken[0], broken[1]}, vlessPair1323[0])
	mixed = append(mixed, broken[2])
	if err := json.Unmarshal([]byte(renderSingboxJSON(mixed)), &doc); err != nil {
		t.Fatalf("混合链接的 sing-box JSON 解析失败：%v", err)
	}
	members := selectorMembers1339(t, doc)
	if len(members) != 1 || members[0] != "智利-中转" {
		t.Fatalf("混合链接时 Proxy 里应只剩好的 vless 节点：%v", members)
	}
}

// TestClashHy2Tuic1339 HY2 与 TUIC 在 Clash YAML 里出 proxy：字段
// 与落地 install.sh 生成的 Clash 参考一致（TUIC 的拥塞控制叫
// congestion-controller）；两个节点都在策略组里；HY2/TUIC 没有
// network / ws-opts（那是 vless/trojan/vmess 的 WS 写法）。
func TestClashHy2Tuic1339(t *testing.T) {
	out := renderClashYAML(hy2TuicPair1339, "split")
	for _, want := range []string{
		"    type: hysteria2\n",
		"    password: \"test-hy2-pass\"\n",
		"    type: tuic\n",
		"    uuid: \"11111111-2222-3333-4444-555555555555\"\n",
		"    password: \"test-tuic-pass\"\n",
		"    congestion-controller: bbr\n",
		"    sni: \"example.com\"\n",
		"      - \"h3\"\n",
		"    udp: true\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Clash YAML 缺 %q：\n%s", want, out)
		}
	}
	for _, bad := range []string{"network:", "ws-opts:", "detour", "congestion_control:"} {
		if strings.Contains(out, bad) {
			t.Fatalf("只有 HY2/TUIC 时 Clash YAML 不应含 %q：\n%s", bad, out)
		}
	}
	// 两个节点都在策略组里（组内成员按名字；自动选择/GLOBAL/手动选择/
	// 社交/流媒体组里都有，中转/直连组里各有一个）。
	for _, name := range []string{"\"智利-HY2-中转\"", "\"智利-TUIC-直连\""} {
		if strings.Count(out, "- "+name) < 2 {
			t.Fatalf("Clash YAML 里 %s 应至少出现两次（多个策略组里都有）：\n%s", name, out)
		}
	}
}

// newTestHy2TuicServer1339 nodes.txt 里只有 HY2/TUIC 链接的测试面板
// （复用订阅测试的 relay.conf 骨架）。
func newTestHy2TuicServer1339(t *testing.T) *Server {
	t.Helper()
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	nodes := "## 落地 1：203.0.113.1\n" +
		"中转：hysteria2://test-hy2-pass@example.com:24444?sni=example.com&alpn=h3#智利-HY2-中转\n" +
		"直连：tuic://11111111-2222-3333-4444-555555555555:test-tuic-pass@203.0.113.1:24445?congestion_control=bbr&alpn=h3&sni=example.com#智利-TUIC-直连\n"
	if err := os.WriteFile(filepath.Join(kitDir, "nodes.txt"), []byte(nodes), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(subTestRelayConf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	return newTestAddLandingServer(t, kitDir, realmDir)
}

// TestSingboxHy2TuicViaHTTP1339 ?format=singbox 与 ?format=clash
// 下发的订阅里都有 HY2/TUIC（走真实 handler，不止渲染函数）。
func TestSingboxHy2TuicViaHTTP1339(t *testing.T) {
	s := newTestHy2TuicServer1339(t)
	tok := generateSubToken()
	subUserForToken1427(t, s, tok)

	rec := callSub(t, s, tok, "/sub/"+tok+"?format=singbox", "")
	if rec.Code != 200 {
		t.Fatalf("singbox 应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("下发的 sing-box JSON 解析失败：%v", err)
	}
	members := selectorMembers1339(t, doc)
	if len(members) != 2 {
		t.Fatalf("下发的 sing-box JSON 里 Proxy 成员应是 2 个：%v", members)
	}
	if outboundByTag1339(t, doc, "智利-HY2-中转")["type"] != "hysteria2" {
		t.Fatalf("下发的 JSON 里 HY2 节点 type 不对")
	}
	if outboundByTag1339(t, doc, "智利-TUIC-直连")["type"] != "tuic" {
		t.Fatalf("下发的 JSON 里 TUIC 节点 type 不对")
	}

	rec = callSub(t, s, tok, "/sub/"+tok+"?format=clash", "")
	if rec.Code != 200 {
		t.Fatalf("clash 应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"type: hysteria2", "type: tuic", "congestion-controller: bbr"} {
		if !strings.Contains(body, want) {
			t.Fatalf("下发的 Clash YAML 缺 %q：\n%s", want, body)
		}
	}
}
