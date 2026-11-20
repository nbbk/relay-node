package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------- v1.3.24：userinfo 的 expire 带上订阅本身的到期时间 ----------
// 根因：subUserinfoHeader 只取落地到期；管理面板模式下本机没有落地，
// 合并订阅的响应里根本没有 subscription-userinfo 头，小火箭收不到
// 到期时间，只能显示更新/生成时间。

func unixOf(t *testing.T, rfc3339 string) int64 {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatal(err)
	}
	return ts.Unix()
}

func TestUserinfoMergedTokenExpiry1324(t *testing.T) {
	s := newTestSubServer(t)
	// 合并订阅：只勾远程落地（本面板无本地落地数据——用户管理面板的情形），
	// 令牌设了到期时间。远程离线时链接为空，但头里必须有 expire。
	tok := generateSubToken()
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatal(err)
	}
	exp := "2027-05-01T23:59:59Z"
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{
		ID: "m-1324", Name: "合并", TokenHash: hashSubToken(tok), TokenEnc: enc,
		Selection: []SubSelection{{Panel: "remote-id-x", Landing: 3}},
		ExpiresAt: exp,
	})
	rec := callSub(t, s, tok, "/sub/"+tok, "")
	if rec.Code != 200 {
		t.Fatalf("合并订阅应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	h := rec.Header().Get("subscription-userinfo")
	want := "expire=" + strconv.FormatInt(unixOf(t, exp), 10)
	if h != want {
		t.Fatalf("无本地落地数据时 userinfo 应只有令牌到期 %q，实际 %q", want, h)
	}
}

func TestUserinfoTokenVsLandingExpiry1324(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)
	// 落地 1 有到期日（比令牌到期晚）
	s.stateMu.Lock()
	s.state.Landings["203.0.113.1"] = &LandingExt{Expiry: "2027-02-16T00:00:00Z"}
	s.stateMu.Unlock()

	// 令牌到期比落地早 → 取令牌到期
	s.cfg.SubTokenExpiresAt = "2027-01-10T23:59:59Z"
	h := callSub(t, s, tok, "/sub/"+tok, "").Header().Get("subscription-userinfo")
	if !strings.Contains(h, "expire="+strconv.FormatInt(unixOf(t, "2027-01-10T23:59:59Z"), 10)) {
		t.Fatalf("令牌到期更早时 expire 应取令牌到期，实际 %q", h)
	}
	// 令牌到期比落地晚 → 取落地到期
	s.cfg.SubTokenExpiresAt = "2027-03-01T23:59:59Z"
	h = callSub(t, s, tok, "/sub/"+tok, "").Header().Get("subscription-userinfo")
	if !strings.Contains(h, "expire="+strconv.FormatInt(unixOf(t, "2027-02-16T00:00:00Z"), 10)) {
		t.Fatalf("落地到期更早时 expire 应取落地到期，实际 %q", h)
	}
	// 令牌永久 → 仍取落地到期（1.3.23 行为不变）
	s.cfg.SubTokenExpiresAt = ""
	h = callSub(t, s, tok, "/sub/"+tok, "").Header().Get("subscription-userinfo")
	if !strings.Contains(h, "expire="+strconv.FormatInt(unixOf(t, "2027-02-16T00:00:00Z"), 10)) {
		t.Fatalf("令牌永久时 expire 应取落地到期，实际 %q", h)
	}
	// 两者都没有 → 不含 expire 项
	s.stateMu.Lock()
	delete(s.state.Landings, "203.0.113.1")
	s.stateMu.Unlock()
	h = callSub(t, s, tok, "/sub/"+tok, "").Header().Get("subscription-userinfo")
	if strings.Contains(h, "expire=") {
		t.Fatalf("两者都无到期时不应有 expire 项，实际 %q", h)
	}
}

func TestUserinfoSubUserExpiry1324(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatal(err)
	}
	exp := "2027-04-15T23:59:59Z"
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{
		ID: "u-1324", Name: "张三", TokenHash: hashSubToken(tok), TokenEnc: enc,
		ExpiresAt: exp,
	})
	rec := callSub(t, s, tok, "/sub/"+tok, "")
	if rec.Code != 200 {
		t.Fatalf("一人一条订阅应 200，实际 %d", rec.Code)
	}
	h := rec.Header().Get("subscription-userinfo")
	want := "expire=" + strconv.FormatInt(unixOf(t, exp), 10)
	if h != want {
		t.Fatalf("一人一条订阅无本地数据时 userinfo 应只有令牌到期 %q，实际 %q", want, h)
	}
}

// ---------- v1.3.24：客户端测 IP 域名走代理 ----------
// 规则模式下 FlClash/Verge 等的测 IP 请求本来走 DIRECT，在 fake-ip 下
// 失败；规则指自动选择后走代理，显示代理出口 IP。

func TestIpCheckRules1324(t *testing.T) {
	out := renderClashYAML(vlessPair1323, "split")
	// 12 个域名全在规则里、都指自动选择，且在 MATCH 之前。
	for _, d := range ipCheckSuffixes {
		want := "DOMAIN-SUFFIX," + d + ",自动选择"
		if !strings.Contains(out, want) {
			t.Fatalf("Clash YAML 缺测 IP 规则 %q", want)
		}
	}
	if strings.Index(out, "DOMAIN-SUFFIX,"+ipCheckSuffixes[0]+",自动选择") > strings.Index(out, "MATCH,DIRECT") {
		t.Fatalf("测 IP 规则应在 MATCH 之前")
	}
	// 空配置里不写测 IP 规则（没有自动选择组可指）。
	empty := renderClashYAML(nil, "split")
	for _, d := range ipCheckSuffixes {
		if strings.Contains(empty, d) {
			t.Fatalf("空配置不应含测 IP 域名 %q", d)
		}
	}
	// sing-box JSON：route 规则与 dns 规则里都有这组域名→Proxy/dns-proxy。
	js := renderSingboxJSON(vlessPair1323)
	var doc map[string]any
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		t.Fatalf("sing-box JSON 解析失败：%v", err)
	}
	routeRules, _ := doc["route"].(map[string]any)["rules"].([]any)
	foundRoute := false
	for _, r := range routeRules {
		m, _ := r.(map[string]any)
		if m["outbound"] == "Proxy" {
			if hasAllDomains(m["domain_suffix"], ipCheckSuffixes) {
				foundRoute = true
			}
		}
	}
	if !foundRoute {
		t.Fatalf("sing-box route 里缺测 IP 域名→Proxy 的规则：%v", routeRules)
	}
	dnsRules, _ := doc["dns"].(map[string]any)["rules"].([]any)
	foundDNS := false
	for _, r := range dnsRules {
		m, _ := r.(map[string]any)
		if m["server"] == "dns-proxy" {
			if hasAllDomains(m["domain_suffix"], ipCheckSuffixes) {
				foundDNS = true
			}
		}
	}
	if !foundDNS {
		t.Fatalf("sing-box dns 里缺测 IP 域名→dns-proxy 的规则：%v", dnsRules)
	}
	// sing-box 空配置：route 里不引用 Proxy、也不含测 IP 域名（dns 段照
	// 1.3.23 原样仍带 tiktok/测 IP 的 dns-proxy 规则，sing-box check
	// 通过，不动它）。
	var emptyDoc map[string]any
	if err := json.Unmarshal([]byte(renderSingboxJSON(nil)), &emptyDoc); err != nil {
		t.Fatalf("sing-box 空 JSON 解析失败：%v", err)
	}
	emptyRoute, _ := emptyDoc["route"].(map[string]any)["rules"].([]any)
	for _, r := range emptyRoute {
		m, _ := r.(map[string]any)
		if m["outbound"] == "Proxy" {
			t.Fatalf("sing-box 空配置的 route 不应引用 Proxy：%v", emptyRoute)
		}
		if hasAnyDomain(m["domain_suffix"], ipCheckSuffixes) {
			t.Fatalf("sing-box 空配置的 route 不应含测 IP 域名：%v", emptyRoute)
		}
	}
}

func hasAnyDomain(v any, want []string) bool {
	list, _ := v.([]any)
	set := map[string]bool{}
	for _, d := range list {
		if s, ok := d.(string); ok {
			set[s] = true
		}
	}
	for _, d := range want {
		if set[d] {
			return true
		}
	}
	return false
}

func hasAllDomains(v any, want []string) bool {
	list, _ := v.([]any)
	set := map[string]bool{}
	for _, d := range list {
		if s, ok := d.(string); ok {
			set[s] = true
		}
	}
	for _, d := range want {
		if !set[d] {
			return false
		}
	}
	return true
}
