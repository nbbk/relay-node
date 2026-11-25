package main

// v1.3.37 sing-box DNS 格式迁移：面板生成的 sing-box JSON 里 dns 段改用
// sing-box 1.14 的新格式（legacy 的 address 写法会被 sing-box check 拒绝）。
// 两台 DoH 服务器都写 type=https + server + server_port，route 里加
// default_domain_resolver；Clash YAML 不动。

import (
	"encoding/json"
	"strings"
	"testing"
)

// dnsServers1337 从 sing-box JSON 里取 dns.servers（已解析成 []any）。
func dnsServers1337(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	dns, _ := doc["dns"].(map[string]any)
	if dns == nil {
		t.Fatalf("sing-box JSON 应有 dns 段")
	}
	raw, _ := dns["servers"].([]any)
	if len(raw) != 2 {
		t.Fatalf("dns.servers 应有 2 个，实际 %v", dns["servers"])
	}
	var out []map[string]any
	for _, s := range raw {
		m, _ := s.(map[string]any)
		if m == nil {
			t.Fatalf("dns.servers 元素应是对象：%v", s)
		}
		out = append(out, m)
	}
	return out
}

// TestSingboxDNSNewFormat1337 有节点时 sing-box JSON 的 dns 段是 1.14 新格式：
// 两台服务器都有 type=https、server、server_port=443，没有 legacy 的
// address 字段；detour 按 1.3.38（直连无 detour、代理 detour 是大写
// Proxy）；route.default_domain_resolver 是 dns-direct；
// dns rules 与 final 不变。
func TestSingboxDNSNewFormat1337(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(renderSingboxJSON(vlessPair1323)), &doc); err != nil {
		t.Fatalf("sing-box JSON 解析失败：%v", err)
	}
	servers := dnsServers1337(t, doc)
	want := map[string]string{"dns-direct": "223.5.5.5", "dns-proxy": "dns.google"}
	for _, s := range servers {
		tag, _ := s["tag"].(string)
		host, ok := want[tag]
		if !ok {
			t.Fatalf("dns 服务器 tag 应是 dns-direct/dns-proxy，实际 %q", tag)
		}
		if s["type"] != "https" {
			t.Fatalf("%s 的 type 应是 https，实际 %v", tag, s["type"])
		}
		if s["server"] != host {
			t.Fatalf("%s 的 server 应是 %s，实际 %v", tag, host, s["server"])
		}
		if port, _ := s["server_port"].(float64); port != 443 {
			t.Fatalf("%s 的 server_port 应是 443，实际 %v", tag, s["server_port"])
		}
		if _, has := s["address"]; has {
			t.Fatalf("%s 不应再有 legacy 的 address 字段：%v", tag, s)
		}
		if _, has := s["path"]; has {
			t.Fatalf("%s 不应写 path（默认就是 /dns-query）：%v", tag, s)
		}
	}
	if _, has := servers[0]["detour"]; has {
		t.Fatalf("直连 DNS 不应再写 detour（v1.3.38）：%v", servers)
	}
	if servers[1]["detour"] != "Proxy" {
		t.Fatalf("代理 DNS 的 detour 应是大写 Proxy（v1.3.38）：%v", servers)
	}
	route, _ := doc["route"].(map[string]any)
	if route == nil {
		t.Fatalf("sing-box JSON 应有 route 段")
	}
	if route["default_domain_resolver"] != "dns-direct" {
		t.Fatalf("route.default_domain_resolver 应是 dns-direct，实际 %v", route["default_domain_resolver"])
	}
	if route["final"] != "direct" {
		t.Fatalf("route.final 应仍是 direct，实际 %v", route["final"])
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

// TestSingboxDNSEmptyConfig1337 空订阅的 sing-box JSON 同样是新格式
// （dns 段与 default_domain_resolver 都在，客户端能正常导入）。
func TestSingboxDNSEmptyConfig1337(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(renderSingboxJSON(nil)), &doc); err != nil {
		t.Fatalf("sing-box 空 JSON 解析失败：%v", err)
	}
	servers := dnsServers1337(t, doc)
	for _, s := range servers {
		if s["type"] != "https" {
			t.Fatalf("空配置里 %v 的 type 应是 https，实际 %v", s["tag"], s["type"])
		}
		if _, has := s["address"]; has {
			t.Fatalf("空配置里 %v 不应再有 legacy 的 address 字段", s["tag"])
		}
	}
	route, _ := doc["route"].(map[string]any)
	if route["default_domain_resolver"] != "dns-direct" {
		t.Fatalf("空配置的 route.default_domain_resolver 应是 dns-direct，实际 %v", route["default_domain_resolver"])
	}
}

// TestSingboxDNSViaHTTP1337 ?format=singbox 下发的 JSON 同样是新格式
// （走真实 handler，不止渲染函数）。
func TestSingboxDNSViaHTTP1337(t *testing.T) {
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
	for _, s := range servers {
		if s["type"] != "https" {
			t.Fatalf("下发的 JSON 里 %v 的 type 应是 https，实际 %v", s["tag"], s["type"])
		}
		if _, has := s["address"]; has {
			t.Fatalf("下发的 JSON 里 %v 不应再有 legacy 的 address 字段", s["tag"])
		}
	}
	route, _ := doc["route"].(map[string]any)
	if route["default_domain_resolver"] != "dns-direct" {
		t.Fatalf("下发的 JSON 里 route.default_domain_resolver 应是 dns-direct，实际 %v", route["default_domain_resolver"])
	}
}

// TestClashDNSUnchanged1337 Clash YAML 不受迁移影响：dns 段仍是原来的
// DoH 地址写法（Clash 格式本来就长这样），且没有 sing-box 的
// default_domain_resolver 字段。
func TestClashDNSUnchanged1337(t *testing.T) {
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
	if strings.Contains(out, `"type": "https"`) {
		t.Fatalf("Clash YAML 不应含 sing-box 的 type=https 写法：\n%s", out)
	}
}
