package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1.2.3：DNS-01 落地不再误判手动续签 ----------

func TestRenewModeDNS01(t *testing.T) {
	// 用户真实案例：智利落地 24443，标准模式，DNS-01 证书，剩余 89 天。
	// 以前只认端口（没 80:80 就判手动），现在 dns01 一律自动。
	chile := Landing{
		Landing: "203.0.113.1", CertDomain: "example.com", CertMode: "dns01",
		Mode:  "standard",
		Ports: []PortSpec{{Proto: "tcp", Listen: 24443, Remote: 24443}, {Proto: "udp", Listen: 24443, Remote: 24443}},
	}
	mode, manual := renewModeFor(chile, false, true)
	if mode != "自动（DNS 验证）" || manual {
		t.Fatalf("dns01+有证书应判自动（DNS 验证），实际：%s manual=%v", mode, manual)
	}
	// 暂时探不到证书也不改判手动（续签方式说的是机制，不是探测结果）
	mode, manual = renewModeFor(chile, false, false)
	if mode != "自动（DNS 验证）" || manual {
		t.Fatalf("dns01+暂时无证书也应判自动（DNS 验证），实际：%s manual=%v", mode, manual)
	}
	// http01 同端口同模式：80 改了监听，仍判手动（老逻辑不变）
	http01 := chile
	http01.CertMode = "http01"
	mode, manual = renewModeFor(http01, false, true)
	if !strings.Contains(mode, "手动") || !manual {
		t.Fatalf("http01+80 改监听应判手动，实际：%s manual=%v", mode, manual)
	}
	// 没填：走老逻辑，同样判手动
	unknown := chile
	unknown.CertMode = ""
	mode, manual = renewModeFor(unknown, false, true)
	if !strings.Contains(mode, "手动") || !manual {
		t.Fatalf("没填+80 改监听应判手动（老逻辑），实际：%s manual=%v", mode, manual)
	}
	// 没填 + 明文端口：仍判明文
	plain := Landing{
		Landing: "203.0.113.1", CertDomain: "example.com",
		Mode:  "standard",
		Ports: []PortSpec{{Proto: "tcp", Listen: 8080, Remote: 8080}},
	}
	mode, manual = renewModeFor(plain, false, false)
	if mode != "无证书（明文模式）" || manual {
		t.Fatalf("明文落地应判无证书（明文模式），实际：%s manual=%v", mode, manual)
	}
	// http01 + 80 直通：自动（走 80）
	auto80 := Landing{
		Landing: "203.0.113.1", CertDomain: "example.com", CertMode: "http01",
		Mode:  "standard",
		Ports: []PortSpec{{Proto: "tcp", Listen: 80, Remote: 80}, {Proto: "tcp", Listen: 443, Remote: 443}},
	}
	mode, manual = renewModeFor(auto80, false, true)
	if mode != "自动（走 80）" || manual {
		t.Fatalf("http01+80 直通应判自动（走 80），实际：%s manual=%v", mode, manual)
	}
}

func TestCertModeText(t *testing.T) {
	if certModeText("dns01") != "DNS 验证" || certModeText("http01") != "默认走 80" || certModeText("") != "未设置" {
		t.Fatalf("certModeText 不对：%q %q %q", certModeText("dns01"), certModeText("http01"), certModeText(""))
	}
}

func TestParseRelayConfCertMode(t *testing.T) {
	conf := parseRelayConf([]byte("LANDING=203.0.113.1\nCERT_DOMAIN=\"example.com\"\nCERT_MODE=\"dns01\"\nPORTS=\"tcp:24443:24443\"\nLANDING_2=203.0.113.2\nCERT_DOMAIN_2=\"example.net\"\nCERT_MODE_2=\"http01\"\nPORTS_2=\"tcp:80:80 tcp:443:443\"\nLANDING_COUNT=2\n"))
	if len(conf.Landings) != 2 {
		t.Fatalf("落地数=%d want 2", len(conf.Landings))
	}
	if conf.Landings[0].CertMode != "dns01" {
		t.Fatalf("1 号落地 CertMode=%q want dns01", conf.Landings[0].CertMode)
	}
	if conf.Landings[1].CertMode != "http01" {
		t.Fatalf("2 号落地 CertMode=%q want http01", conf.Landings[1].CertMode)
	}
	// 旧 relay.conf 没有 CERT_MODE：不报错，读出来是空串，按老逻辑判
	old := parseRelayConf([]byte("LANDING=203.0.113.1\nPORTS=\"tcp:80:80 tcp:443:443\"\nLANDING_COUNT=1\n"))
	if old.Landings[0].CertMode != "" {
		t.Fatalf("旧配置 CertMode 应为空，实际 %q", old.Landings[0].CertMode)
	}
}

func TestRebuildRelayConfCertMode(t *testing.T) {
	rc := parseRelayConf([]byte("LANDING=203.0.113.1\nCERT_DOMAIN=\"example.com\"\nPORTS=\"tcp:80:80 tcp:443:443\"\nLANDING_2=203.0.113.2\nCERT_DOMAIN_2=\"example.net\"\nPORTS_2=\"tcp:24443:24443\"\nLANDING_COUNT=2\nALL_PORTS=\"tcp:80:80 tcp:443:443 tcp:24443:24443\"\nSHARE80=0\nSHARE443=0\n"))
	nl := rc.Landings[1]
	nl.CertMode = "dns01"
	lines := rebuildRelayConfForEdit(rc, 2, nl, false, false, "")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `CERT_MODE_2="dns01"`) {
		t.Fatalf("编辑后 2 号落地没有 CERT_MODE_2：\n%s", joined)
	}
	if strings.Contains(joined, "CERT_MODE=") {
		t.Fatalf("1 号落地不应被写上 CERT_MODE：\n%s", joined)
	}
	// 没填时不写这键（旧配置不改写）
	nl.CertMode = ""
	lines = rebuildRelayConfForEdit(rc, 2, nl, false, false, "")
	joined = strings.Join(lines, "\n")
	if strings.Contains(joined, "CERT_MODE") {
		t.Fatalf("没填签发方式时不应写 CERT_MODE：\n%s", joined)
	}
}

func TestValidateCertMode(t *testing.T) {
	base := addLandingReq{Landing: "203.0.113.1", Preset: "1", Mode: "standard", CertDomain: "example.com"}
	for _, ok := range []string{"", "http01", "dns01"} {
		r := base
		r.CertMode = ok
		if msg := validateLandingReqFields(r); msg != "" {
			t.Fatalf("cert_mode=%q 应通过，实际：%s", ok, msg)
		}
	}
	r := base
	r.CertMode = "bogus"
	if msg := validateLandingReqFields(r); msg == "" {
		t.Fatalf("cert_mode=bogus 应被拒绝")
	}
}

// 编辑补填签发方式：旧落地没 CERT_MODE，编辑时选 DNS 验证，
// 保存后 relay.conf 里有了，卡片与证书页的续签方式变自动（DNS 验证），
// 只改签发方式不重启 realm（转发配置没动）。
func TestEditLandingCertMode(t *testing.T) {
	s, _, realmDir := setupEditServer122(t)
	code, resp := callHandler122(t, s.handleUpdateLanding, "PUT", "/api/v1/landings/1",
		`{"landing":"203.0.113.1","preset":"1","mode":"standard","cert_domain":"example.com","cert_mode":"dns01","duration_days":-1,"traffic_limit_bytes":0,"speed_limit_mbps":0}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("编辑补填签发方式应成功：code=%d resp=%v", code, resp)
	}
	if resp["restarted"] != false {
		t.Fatalf("只改签发方式不应重启 realm：%v", resp)
	}
	conf, _ := os.ReadFile(filepath.Join(realmDir, "relay.conf"))
	if !strings.Contains(string(conf), `CERT_MODE="dns01"`) {
		t.Fatalf("relay.conf 里没有 CERT_MODE：\n%s", string(conf))
	}
	// 读盘确认：字段在，续签方式判自动（DNS 验证）
	rc, _, err := s.readRelayConf()
	if err != nil || rc.Landings[0].CertMode != "dns01" {
		t.Fatalf("读盘后 CertMode 丢了：%v %+v", err, rc)
	}
	l := landingByIndex122(t, statusLandings122(t, s), 1)
	if l["cert_mode"] != "dns01" {
		t.Fatalf("状态里 cert_mode=%v want dns01", l["cert_mode"])
	}
	if l["renew_mode"] != "自动（DNS 验证）" {
		t.Fatalf("DNS-01 落地续签方式=%v want 自动（DNS 验证）", l["renew_mode"])
	}
}

// 编辑不填签发方式：旧落地保持原样，relay.conf 不新增 CERT_MODE，
// 续签方式仍按老逻辑判（80 直通就是自动（走 80））。
func TestEditLandingCertModeUnset(t *testing.T) {
	s, _, realmDir := setupEditServer122(t)
	code, resp := callHandler122(t, s.handleUpdateLanding, "PUT", "/api/v1/landings/1",
		`{"landing":"203.0.113.1","preset":"1","mode":"standard","cert_domain":"example.com","duration_days":-1,"traffic_limit_bytes":0,"speed_limit_mbps":0}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("编辑应成功：code=%d resp=%v", code, resp)
	}
	conf, _ := os.ReadFile(filepath.Join(realmDir, "relay.conf"))
	if strings.Contains(string(conf), "CERT_MODE") {
		t.Fatalf("没填签发方式时不应写 CERT_MODE：\n%s", string(conf))
	}
	l := landingByIndex122(t, statusLandings122(t, s), 1)
	if l["renew_mode"] != "自动（走 80）" {
		t.Fatalf("没填时应按老逻辑判自动（走 80），实际：%v", l["renew_mode"])
	}
}
