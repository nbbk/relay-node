package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------- 1.3.23：订阅兼容（P0+P1） ----------
// Clash YAML 补 dns 段 + udp: true + 分流规则出口改自动选择；顶层补
// ipv6/mode/sniffer；新增 sing-box JSON 格式；TikTok 清单加 isnssdk.com；
// clashProxyFromLink 加 ss 分支；订阅选项卡三档链接。

var vlessPair1323 = []subLink{
	{Name: "智利-中转", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转"},
	{Name: "智利-直连", Link: "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-直连"},
}

// TestVersion1348 VERSION 文件是 1.4.12。
func TestVersion1348(t *testing.T) {
	if Version != "1.4.42" {
		t.Fatalf("VERSION 应是 1.4.33，实际 %q", Version)
	}
}

// TestClashDNSSection1323 Clash YAML 顶部有完整的 dns 段：enable、
// fake-ip、国内可达的 DoH 上游、fallback 与 fallback-filter；
// 节点域名（fake-ip-filter）从订阅里实际的域名动态生成，不写死。
func TestClashDNSSection1323(t *testing.T) {
	out := renderClashYAML(vlessPair1323, "split")
	for _, want := range []string{
		"dns:\n  enable: true",
		"listen: 0.0.0.0:1053",
		"ipv6: false",
		"enhanced-mode: fake-ip",
		"fake-ip-range: 198.18.0.1/16",
		"use-hosts: true",
		"- 223.5.5.5",
		"- 114.114.114.114",
		"- https://doh.pub/dns-query",
		"- https://dns.alidns.com/dns-query",
		"proxy-server-nameserver:",
		"- https://dns.google/dns-query",
		"- https://1.1.1.1/dns-query",
		"fallback-filter:",
		"geoip: true",
		"geoip-code: CN",
		"- 240.0.0.0/4",
		"- 0.0.0.0/32",
		"fake-ip-filter:",
		"- \"*.lan\"",
		"- \"time.windows.com\"",
		"- \"ntp.*.com\"",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Clash YAML 的 dns 段缺 %q：\n%s", want, out)
		}
	}
	// 节点域名 example.com 动态生成了 fake-ip-filter 条目；
	// 任何写死的域名都不应出现。
	if !strings.Contains(out, "- \"*.example.com\"") || !strings.Contains(out, "- \"example.com\"") {
		t.Fatalf("fake-ip-filter 应含动态生成的 *.example.com 与 example.com：\n%s", out)
	}
	if strings.Contains(out, "930327.xyz") {
		t.Fatalf("YAML 里不应出现写死的域名 930327.xyz：\n%s", out)
	}
	// dns 段在 proxies 之前。
	if strings.Index(out, "dns:") > strings.Index(out, "proxies:") {
		t.Fatalf("dns 段应在 proxies 之前：\n%s", out)
	}
}

// TestClashProxyUDP1323 全部 proxy 条目都写 udp: true，且不写 packet-encoding。
func TestClashProxyUDP1323(t *testing.T) {
	links := []subLink{
		{Name: "V", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:24443?security=tls&type=ws&host=example.com&path=%2Fexample-path#V"},
		{Name: "T", Link: "trojan://pass@example.com:443?security=tls&type=ws&host=example.com&path=%2Fexample-path#T"},
		{Name: "SS", Link: "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388#SS"},
	}
	out := renderClashYAML(links, "split")
	if strings.Count(out, "udp: true") != 3 {
		t.Fatalf("3 个节点应各有一行 udp: true，实际 %d 处：\n%s", strings.Count(out, "udp: true"), out)
	}
	if strings.Contains(out, "packet-encoding") {
		t.Fatalf("不应写 packet-encoding：\n%s", out)
	}
}

// TestClashRuleTarget1323 分流规则的 TikTok/社交/流媒体三组出口都是
// 自动选择（url-test 组）；MATCH 行不变。
func TestClashRuleTarget1323(t *testing.T) {
	out := renderClashYAML(vlessPair1323, "split")
	for _, want := range []string{
		"- DOMAIN-SUFFIX,tiktok.com,自动选择",
		"- DOMAIN-SUFFIX,tiktokv.com,自动选择",
		"- DOMAIN-SUFFIX,isnssdk.com,自动选择",
		"- DOMAIN-KEYWORD,tiktok,自动选择",
		"- DOMAIN-SUFFIX,facebook.com,自动选择",
		"- DOMAIN-SUFFIX,youtube.com,自动选择",
		"  - MATCH,DIRECT",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("分流规则缺 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "tiktok.com,手动选择") || strings.Contains(out, "tiktok,手动选择") {
		t.Fatalf("TikTok 规则不应再指向手动选择：\n%s", out)
	}
	if strings.Contains(out, "facebook.com,社交") || strings.Contains(out, "youtube.com,流媒体") {
		t.Fatalf("社交/流媒体规则不应再指向各自的组：\n%s", out)
	}
	out = renderClashYAML(vlessPair1323, "global")
	if !strings.Contains(out, "  - MATCH,手动选择") {
		t.Fatalf("global 模式 MATCH 应仍是手动选择：\n%s", out)
	}
	if !strings.Contains(out, "- DOMAIN-SUFFIX,tiktok.com,自动选择") {
		t.Fatalf("global 模式 TikTok 规则也应指向自动选择：\n%s", out)
	}
}

// TestClashTopLevel1323 顶层补 ipv6: false、mode: rule 与 sniffer 段，
// skip-domain 从节点域名动态生成。
func TestClashTopLevel1323(t *testing.T) {
	out := renderClashYAML(vlessPair1323, "split")
	for _, want := range []string{
		"ipv6: false",
		"mode: rule",
		"sniffer:\n  enable: true",
		"TLS:\n      ports: [443, 8443]",
		"HTTP:\n      ports: [80, 8080]",
		"skip-domain:",
		"- \"*.example.com\"",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Clash YAML 顶层缺 %q：\n%s", want, out)
		}
	}
	for _, bad := range []string{"mixed-port", "tun:", "allow-lan", "log-level", "external-controller"} {
		if strings.Contains(out, bad) {
			t.Fatalf("YAML 不应含 %q：\n%s", bad, out)
		}
	}
}

// TestClashEmptyUnchanged1323 空订阅保持 1.3.22 原样（不写 dns 段）。
func TestClashEmptyUnchanged1323(t *testing.T) {
	out := renderClashYAML(nil, "split")
	if out != "proxies: []\nproxy-groups: []\nrules:\n  - MATCH,DIRECT\n" {
		t.Fatalf("空订阅应与 1.3.22 字节一致，实际：\n%s", out)
	}
}

// TestSingboxFormat1323 ?format=singbox 返回合法的 sing-box JSON：
// dns 双 DoH、mixed 入站、节点 + selector + urltest + direct/block、
// route 规则含 TikTok 清单与 isnssdk.com、.cn 直连、final direct。
func TestSingboxFormat1323(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	subUserForToken1427(t, s, tok)
	rec := callSub(t, s, tok, "/sub/"+tok+"?format=singbox", "")
	if rec.Code != 200 {
		t.Fatalf("singbox 应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("singbox Content-Type 应是 application/json，实际 %s", ct)
	}
	var cfg map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("singbox 应是合法 JSON：%v\n%s", err, rec.Body.String())
	}
	dns, _ := cfg["dns"].(map[string]any)
	if dns == nil {
		t.Fatalf("singbox 应有 dns 段：\n%s", rec.Body.String())
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("dns.servers 应有 2 个，实际 %v", dns["servers"])
	}
	obs, _ := cfg["outbounds"].([]any)
	tags := map[string]bool{}
	for _, o := range obs {
		m, _ := o.(map[string]any)
		if m != nil {
			tags[m["tag"].(string)] = true
		}
	}
	for _, want := range []string{"Proxy", "自动选择", "direct", "block"} {
		if !tags[want] {
			t.Fatalf("outbounds 缺 %q：%v", want, tags)
		}
	}
	// selector 的 default 应是直连节点（中转已死时默认不钉死节点）。
	body := rec.Body.String()
	if !strings.Contains(body, `"default": "智利-直连"`) {
		t.Fatalf("selector default 应是直连节点：\n%s", body)
	}
	route, _ := cfg["route"].(map[string]any)
	if route["final"] != "direct" {
		t.Fatalf("route.final 应是 direct，实际 %v", route["final"])
	}
	for _, want := range []string{"isnssdk.com", "tiktokv.com", "facebook.com", "youtube.com"} {
		if !strings.Contains(body, want) {
			t.Fatalf("sing-box route 里缺 %q：\n%s", want, body)
		}
	}
	// 文件名后缀是 .json。
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, ".json") {
		t.Fatalf("singbox 下载文件名应是 .json，实际 %q", cd)
	}
	// ?format=sing-box 别名同样接受。
	rec2 := callSub(t, s, tok, "/sub/"+tok+"?format=sing-box", "")
	if rec2.Code != 200 || !strings.HasPrefix(rec2.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("?format=sing-box 别名应同样返回 JSON：%d %s", rec2.Code, rec2.Header().Get("Content-Type"))
	}
}

// TestSingboxUA1323 UA 嗅探：sing-box 官方系拿 JSON，Clash 系拿 YAML，
// 其他拿 base64；手动 format 优先于 UA。
func TestSingboxUA1323(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	subUserForToken1427(t, s, tok)
	url := "/sub/" + tok
	cases := []struct {
		ua   string
		want string // "json" | "yaml" | "base64"
	}{
		{"sing-box/1.12.0", "json"},
		{"SFA/1.5.0", "json"},
		{"SFI/1.5.0", "json"},
		{"SFM/1.5.0", "json"},
		{"Clash/1.0", "yaml"},
		{"FlClash/0.8.0", "yaml"},
		{"v2rayNG/1.9.0", "base64"},
		{"Shadowrocket/2.2", "base64"},
		{"", "base64"},
	}
	for _, c := range cases {
		rec := callSub(t, s, tok, url, c.ua)
		if rec.Code != 200 {
			t.Fatalf("UA %q 应 200，实际 %d", c.ua, rec.Code)
		}
		body := rec.Body.String()
		got := "base64"
		if strings.HasPrefix(strings.TrimSpace(body), "{") {
			got = "json"
		} else if strings.Contains(body, "proxies:") {
			got = "yaml"
		}
		if got != c.want {
			t.Fatalf("UA %q 应返回 %s，实际 %s（%.60s）", c.ua, c.want, got, body)
		}
	}
	// 手动 format 优先：sing-box UA + ?format=clash → YAML。
	rec := callSub(t, s, tok, url+"?format=clash", "sing-box/1.12.0")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "proxies:") {
		t.Fatalf("手动 format=clash 应覆盖 sing-box UA：%d %.60s", rec.Code, rec.Body.String())
	}
}

// TestSingboxEmpty1323 空节点时 sing-box JSON 仍合法：不引用不存在的
// Proxy 组，只有 direct/block。
func TestSingboxEmpty1323(t *testing.T) {
	body, ctype, err := renderSubContent(nil, "singbox", "split")
	if err != nil {
		t.Fatalf("空节点 singbox 失败：%v", err)
	}
	if !strings.HasPrefix(ctype, "application/json") {
		t.Fatalf("ctype 应是 application/json，实际 %s", ctype)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("空节点 sing-box JSON 不合法：%v\n%s", err, body)
	}
	if strings.Contains(body, `"tag": "Proxy"`) || strings.Contains(body, `"outbound": "Proxy"`) {
		t.Fatalf("空节点 JSON 不应引用 Proxy：\n%s", body)
	}
	obs, _ := cfg["outbounds"].([]any)
	if len(obs) != 2 {
		t.Fatalf("空节点 outbounds 应只有 direct/block，实际 %v", cfg["outbounds"])
	}
}

// TestClashSSBranch1323 ss 分支：两种 base64 写法都能解析，
// cipher/password/server/port 都对；坏链接跳过。
func TestClashSSBranch1323(t *testing.T) {
	// 写法 ①：base64(method:password)@host:port
	p, ok := clashProxyFromLink(subLink{Name: "SS1", Link: "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388#SS1"})
	if !ok {
		t.Fatal("写法 ① 的 ss 链接应解析成功")
	}
	for _, want := range []string{"type: ss", "cipher: \"aes-256-gcm\"", "password: \"password\"", "server: \"example.com\"", "port: 8388", "udp: true"} {
		if !strings.Contains(p, want) {
			t.Fatalf("ss 条目缺 %q：\n%s", want, p)
		}
	}
	// 写法 ②：整体 base64(method:password@host:port)
	p2, ok := clashProxyFromLink(subLink{Name: "SS2", Link: "ss://YWVzLTI1Ni1nY206cGFzc3dvcmRAZXhhbXBsZS5uZXQ6ODM4OA#SS2"})
	if !ok {
		t.Fatal("写法 ② 的 ss 链接应解析成功")
	}
	if !strings.Contains(p2, "server: \"example.net\"") || !strings.Contains(p2, "cipher: \"aes-256-gcm\"") {
		t.Fatalf("写法 ② 应解出 example.net 与 aes-256-gcm：\n%s", p2)
	}
	// sing-box 里 ss 也进 outbound。
	ob, ok := singboxOutboundFromLink(subLink{Name: "SS1", Link: "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388#SS1"})
	if !ok || ob["type"] != "shadowsocks" || ob["method"] != "aes-256-gcm" {
		t.Fatalf("sing-box ss outbound 不对：%v ok=%v", ob, ok)
	}
	// 坏链接跳过。
	if _, ok := clashProxyFromLink(subLink{Name: "坏SS", Link: "ss://not-base64!!!@example.com:8388#坏SS"}); ok {
		t.Fatal("坏 ss 链接应跳过")
	}
}

// TestSubLinkDomains1323 节点域名收集：vless 的 host/sni 都收，
// 纯 IP 的节点没有域名，去重。
func TestSubLinkDomains1323(t *testing.T) {
	d := subLinkDomains("vless://11111111-2222-3333-4444-555555555555@example.com:24443?security=tls&type=ws&host=example.com&path=%2Fexample-path#X")
	if len(d) != 1 || d[0] != "example.com" {
		t.Fatalf("vless 域名应去重成 [example.com]，实际 %v", d)
	}
	d = subLinkDomains("vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?security=tls&sni=node.example.net&type=ws&host=node.example.net#X")
	if len(d) != 1 || d[0] != "node.example.net" {
		t.Fatalf("IP 直连 + sni 时只应收 sni 域名，实际 %v", d)
	}
	d = subLinkDomains("vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?security=none&type=ws#X")
	if len(d) != 0 {
		t.Fatalf("纯 IP 且无 sni 的节点不应有域名，实际 %v", d)
	}
}
