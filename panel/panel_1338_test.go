package main

// v1.3.38 修 1.3.37 的 detour 问题：1.3.37 下发的 sing-box JSON 里
// dns-direct 写了 detour: direct、dns-proxy 写了 detour: proxy，
// sing-box check 能过，但 sing-box 1.14.4 run 起不来——
// 直连 DNS 报 "detour to an empty direct outbound makes no sense"；
// 代理 DNS 的 detour 是小写 proxy，而 selector 的 tag 是大写 Proxy，
// tag 大小写敏感，同样指不到。修法：直连 DNS 去掉 detour，有节点时
// 代理 DNS 的 detour 改成 "Proxy"；节点为空时没有 Proxy 组，代理 DNS
// 改用 IP 8.8.8.8、不写 detour。另：直连 DNS 的 server 由 doh.pub
// 改成 IP 223.5.5.5——域名地址须有另一台 IP 服务器当解析器，否则
// 1.14.4 的 check 就报 missing domain resolver。新格式其余部分与
// Clash YAML 不动。

import (
	"encoding/json"
	"strings"
	"testing"
)

// outboundTags1338 从 sing-box JSON 里取全部出站的 tag。
func outboundTags1338(t *testing.T, doc map[string]any) []string {
	t.Helper()
	raw, _ := doc["outbounds"].([]any)
	var tags []string
	for _, o := range raw {
		m, _ := o.(map[string]any)
		if m == nil {
			continue
		}
		if tag, _ := m["tag"].(string); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

// TestSingboxDNSDetour1338 有节点时：直连 DNS 没有 detour 字段，代理 DNS
// 的 detour 是大写 "Proxy"（对上 selector 的 tag）；两台服务器仍是
// type=https + server + server_port=443 的新格式；
// route.default_domain_resolver 仍是 dns-direct；dns rules 与 final 不变。
func TestSingboxDNSDetour1338(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(renderSingboxJSON(vlessPair1323)), &doc); err != nil {
		t.Fatalf("sing-box JSON 解析失败：%v", err)
	}
	servers := dnsServers1337(t, doc)
	direct, proxy := servers[0], servers[1]
	if direct["tag"] != "dns-direct" || proxy["tag"] != "dns-proxy" {
		t.Fatalf("dns 服务器 tag 应是 dns-direct/dns-proxy，实际 %v", servers)
	}
	if _, has := direct["detour"]; has {
		t.Fatalf("直连 DNS 不应再写 detour（1.14.4 run 会报 detour to an empty direct outbound）：%v", direct)
	}
	if proxy["detour"] != "Proxy" {
		t.Fatalf("代理 DNS 的 detour 应是大写 Proxy（对上 selector 的 tag），实际 %v", proxy["detour"])
	}
	for _, s := range servers {
		if s["type"] != "https" {
			t.Fatalf("%v 的 type 应仍是 https，实际 %v", s["tag"], s["type"])
		}
		if port, _ := s["server_port"].(float64); port != 443 {
			t.Fatalf("%v 的 server_port 应仍是 443，实际 %v", s["tag"], s["server_port"])
		}
		if _, has := s["address"]; has {
			t.Fatalf("%v 不应出现 legacy 的 address 字段", s["tag"])
		}
	}
	if direct["server"] != "223.5.5.5" || proxy["server"] != "dns.google" {
		t.Fatalf("两台服务器的 server 应是 223.5.5.5 / dns.google：%v", servers)
	}
	tags := outboundTags1338(t, doc)
	found := false
	for _, tag := range tags {
		if tag == "Proxy" {
			found = true
		}
	}
	if !found {
		t.Fatalf("有节点时出站里应有 tag 为 Proxy 的 selector，实际 %v", tags)
	}
	route, _ := doc["route"].(map[string]any)
	if route["default_domain_resolver"] != "dns-direct" {
		t.Fatalf("route.default_domain_resolver 应仍是 dns-direct，实际 %v", route["default_domain_resolver"])
	}
	dns, _ := doc["dns"].(map[string]any)
	if dns["final"] != "dns-direct" {
		t.Fatalf("dns.final 应仍是 dns-direct，实际 %v", dns["final"])
	}
	rules, _ := dns["rules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("dns.rules 应仍是 3 条，实际 %v", rules)
	}
}

// TestSingboxDNSEmptyConfig1338 空订阅时：两台 DNS 服务器都不写 detour
// （空配置里没有 Proxy 组，detour 指不到不存在的出站）；代理 DNS 改用
// IP 8.8.8.8（域名在启动时无人解析）；直连 DNS 仍是 doh.pub；
// default_domain_resolver 仍在。
func TestSingboxDNSEmptyConfig1338(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(renderSingboxJSON(nil)), &doc); err != nil {
		t.Fatalf("sing-box 空 JSON 解析失败：%v", err)
	}
	servers := dnsServers1337(t, doc)
	direct, proxy := servers[0], servers[1]
	if _, has := direct["detour"]; has {
		t.Fatalf("空配置里直连 DNS 不应写 detour：%v", direct)
	}
	if _, has := proxy["detour"]; has {
		t.Fatalf("空配置里代理 DNS 不应写 detour（没有 Proxy 组）：%v", proxy)
	}
	if direct["server"] != "223.5.5.5" {
		t.Fatalf("空配置里直连 DNS 的 server 应是 IP 223.5.5.5，实际 %v", direct["server"])
	}
	if proxy["server"] != "8.8.8.8" {
		t.Fatalf("空配置里代理 DNS 的 server 应是 IP 8.8.8.8，实际 %v", proxy["server"])
	}
	if proxy["type"] != "https" {
		t.Fatalf("空配置里代理 DNS 的 type 应仍是 https，实际 %v", proxy["type"])
	}
	tags := outboundTags1338(t, doc)
	for _, tag := range tags {
		if tag == "Proxy" {
			t.Fatalf("空配置里不应有 Proxy 组，实际 %v", tags)
		}
	}
	route, _ := doc["route"].(map[string]any)
	if route["default_domain_resolver"] != "dns-direct" {
		t.Fatalf("空配置的 route.default_domain_resolver 应仍是 dns-direct，实际 %v", route["default_domain_resolver"])
	}
}

// TestSingboxDNSDetourViaHTTP1338 ?format=singbox 下发的 JSON 同样修好
// （走真实 handler，不止渲染函数）。
func TestSingboxDNSDetourViaHTTP1338(t *testing.T) {
	s := newTestSubServer(t)
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
	servers := dnsServers1337(t, doc)
	if _, has := servers[0]["detour"]; has {
		t.Fatalf("下发的 JSON 里直连 DNS 不应写 detour：%v", servers[0])
	}
	if servers[1]["detour"] != "Proxy" {
		t.Fatalf("下发的 JSON 里代理 DNS 的 detour 应是大写 Proxy，实际 %v", servers[1]["detour"])
	}
}

// TestClashDNSUnchanged1338 Clash YAML 不受本次修法影响：dns 段仍是
// 原来的 DoH 地址写法，且没有 sing-box 的 detour /
// default_domain_resolver 字段。
func TestClashDNSUnchanged1338(t *testing.T) {
	out := renderClashYAML(vlessPair1323, "split")
	for _, want := range []string{
		"dns:\n  enable: true",
		"- https://doh.pub/dns-query",
		"- https://dns.google/dns-query",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Clash YAML 的 dns 段缺 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "default_domain_resolver") {
		t.Fatalf("Clash YAML 不应含 default_domain_resolver：\n%s", out)
	}
	if strings.Contains(out, "detour") {
		t.Fatalf("Clash YAML 不应含 detour：\n%s", out)
	}
}
