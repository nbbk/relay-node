package main

// ---------- 1.4.26：客户端登录返回合并订阅链接 ----------

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// setupMergedSub1426 给测试面板加一份合并订阅，返回令牌明文。
// withToken=false 时只占位（TokenEnc 为空，算“无链接”）；disabled=true
// 时是停用那份。调用方按需要多调几次、按顺序加多份。
func setupMergedSub1426(t *testing.T, s *Server, id string, disabled, withToken bool) string {
	t.Helper()
	ms := MergedSub{ID: id, Name: "测试合并订阅 " + id, CreatedAt: "2026-11-25T00:00:00Z", Disabled: disabled}
	if withToken {
		tok := generateSubToken()
		enc, err := s.encryptSubTokenNow(tok)
		if err != nil {
			t.Fatal(err)
		}
		ms.TokenHash = hashSubToken(tok)
		ms.TokenEnc = enc
		s.cfg.MergedSubs = append(s.cfg.MergedSubs, ms)
		return tok
	}
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, ms)
	return ""
}

func TestVersion1426(t *testing.T) {
	if Version != "1.4.26" {
		t.Fatalf("Version 应为 1.4.26，实际 %q", Version)
	}
}

// TestClientLoginMergedSub1426 有可用合并订阅时登录返回合并订阅链接，
// 不是通用订阅——管理面板本机没节点，通用订阅是空的，绑它客户端看不到节点。
func TestClientLoginMergedSub1426(t *testing.T) {
	s := newTestServer144(t)
	genTok := setupGenericSub1345(t, s)
	mergedTok := setupMergedSub1426(t, s, "ms-1426-a", false, true)
	code, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if code != 200 {
		t.Fatalf("应 200，实际 %d：%v", code, resp)
	}
	u, _ := resp["subscription_url"].(string)
	if !strings.Contains(u, "/"+s.subPath()+"/"+mergedTok) {
		t.Fatalf("订阅链接应含合并订阅令牌，实际 %q", u)
	}
	if strings.Contains(u, genTok) {
		t.Fatalf("有可用合并订阅时不应返回通用订阅链接，实际 %q", u)
	}
}

// TestClientLoginMergedSubFirstEnabled1426 多份合并订阅时按顺序取第一份
// 已启用的：两份都可用取第一份；第一份停用取第二份。
func TestClientLoginMergedSubFirstEnabled1426(t *testing.T) {
	s := newTestServer144(t)
	first := setupMergedSub1426(t, s, "ms-1426-first", false, true)
	setupMergedSub1426(t, s, "ms-1426-second", false, true)
	_, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	u, _ := resp["subscription_url"].(string)
	if !strings.Contains(u, "/"+s.subPath()+"/"+first) {
		t.Fatalf("两份都可用时应取第一份，实际 %q", u)
	}

	s2 := newTestServer144(t)
	setupMergedSub1426(t, s2, "ms-1426-off", true, true)
	second := setupMergedSub1426(t, s2, "ms-1426-on", false, true)
	_, resp2 := clientLogin1345(t, s2, `{"username":"admin","password":"test-password-123"}`)
	u2, _ := resp2["subscription_url"].(string)
	if !strings.Contains(u2, "/"+s2.subPath()+"/"+second) {
		t.Fatalf("第一份停用时应取第二份，实际 %q", u2)
	}
}

// TestClientLoginMergedSubFallback1426 合并订阅不可用（停用／无链接）时
// 回落到通用订阅（向后兼容，老行为不变）。
func TestClientLoginMergedSubFallback1426(t *testing.T) {
	// 停用的合并订阅＋通用订阅：回落通用。
	s := newTestServer144(t)
	setupMergedSub1426(t, s, "ms-1426-off", true, true)
	genTok := setupGenericSub1345(t, s)
	_, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	u, _ := resp["subscription_url"].(string)
	if !strings.Contains(u, "/"+s.subPath()+"/"+genTok) {
		t.Fatalf("合并订阅停用时应回落通用订阅，实际 %q", u)
	}
	// 无链接（TokenEnc 为空）的合并订阅＋通用订阅：同样回落通用。
	s2 := newTestServer144(t)
	setupMergedSub1426(t, s2, "ms-1426-nolink", false, false)
	genTok2 := setupGenericSub1345(t, s2)
	_, resp2 := clientLogin1345(t, s2, `{"username":"admin","password":"test-password-123"}`)
	u2, _ := resp2["subscription_url"].(string)
	if !strings.Contains(u2, "/"+s2.subPath()+"/"+genTok2) {
		t.Fatalf("合并订阅无链接时应回落通用订阅，实际 %q", u2)
	}
}

// TestClientLoginMergedSubUndecryptable1426 合并订阅的密文解不开时跳过
// 它、继续找下一份；没有下一份就回落通用订阅，不直接给空。
func TestClientLoginMergedSubUndecryptable1426(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: "ms-1426-bad", TokenEnc: "not-valid-ciphertext"})
	mergedTok := setupMergedSub1426(t, s, "ms-1426-good", false, true)
	_, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	u, _ := resp["subscription_url"].(string)
	if !strings.Contains(u, "/"+s.subPath()+"/"+mergedTok) {
		t.Fatalf("密文解不开的合并订阅应跳过、取下一份，实际 %q", u)
	}

	s2 := newTestServer144(t)
	s2.cfg.MergedSubs = append(s2.cfg.MergedSubs, MergedSub{ID: "ms-1426-bad", TokenEnc: "not-valid-ciphertext"})
	genTok := setupGenericSub1345(t, s2)
	_, resp2 := clientLogin1345(t, s2, `{"username":"admin","password":"test-password-123"}`)
	u2, _ := resp2["subscription_url"].(string)
	if !strings.Contains(u2, "/"+s2.subPath()+"/"+genTok) {
		t.Fatalf("合并订阅都解不开时应回落通用订阅，实际 %q", u2)
	}
}

// TestClientLoginNoMergedNoGeneric1426 既无可用合并订阅、又无通用订阅时
// 登录成功但不给 subscription_url 字段。
func TestClientLoginNoMergedNoGeneric1426(t *testing.T) {
	s := newTestServer144(t)
	code, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if code != 200 {
		t.Fatalf("应 200，实际 %d：%v", code, resp)
	}
	if _, ok := resp["subscription_url"]; ok {
		t.Fatalf("既无合并订阅又无通用订阅时不应给 subscription_url，实际 %v", resp["subscription_url"])
	}
	// 只有停用的合并订阅、没有通用订阅：同样不给。
	s2 := newTestServer144(t)
	setupMergedSub1426(t, s2, "ms-1426-off", true, true)
	code2, resp2 := clientLogin1345(t, s2, `{"username":"admin","password":"test-password-123"}`)
	if code2 != 200 {
		t.Fatalf("应 200，实际 %d：%v", code2, resp2)
	}
	if _, ok := resp2["subscription_url"]; ok {
		t.Fatalf("只有停用的合并订阅时不应给 subscription_url，实际 %v", resp2["subscription_url"])
	}
}

// TestClientSubURLMerged1426 合并订阅链接的拼法与通用订阅一致：
// 对外域名优先；没填时用主端口（不是登录监听的 9876 端口），
// scheme 跟主端口的 HTTPS 开关。
func TestClientSubURLMerged1426(t *testing.T) {
	s := newTestServer144(t)
	mergedTok := setupMergedSub1426(t, s, "ms-1426-a", false, true)
	req := httptest.NewRequest("POST", "/api/v1/client/login", nil)
	req.Host = "panel.example.com:9876"
	// 对外域名：https://域名/路径/合并订阅令牌。
	s.cfg.SubPublicDomain = "cc.example.com"
	if got := s.clientSubscriptionURL(req); got != "https://cc.example.com/"+s.subPath()+"/"+mergedTok {
		t.Fatalf("对外域名时合并订阅链接应为 https://cc.example.com/路径/令牌，实际 %q", got)
	}
	// 没对外域名、主端口开 HTTPS：https://主端口。
	s.cfg.SubPublicDomain = ""
	s.cfg.HTTPSEnabled = true
	if got := s.clientSubscriptionURL(req); got != "https://panel.example.com:11111/"+s.subPath()+"/"+mergedTok {
		t.Fatalf("主端口 HTTPS 时合并订阅链接应走主端口，实际 %q", got)
	}
	// 没对外域名、主端口 HTTP：http://主端口。
	s.cfg.HTTPSEnabled = false
	if got := s.clientSubscriptionURL(req); got != "http://panel.example.com:11111/"+s.subPath()+"/"+mergedTok {
		t.Fatalf("主端口 HTTP 时合并订阅链接应为 http，实际 %q", got)
	}
}
