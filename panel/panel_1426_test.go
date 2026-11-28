package main

// ---------- 1.4.27：客户端登录不再取合并/通用订阅 ----------
// 1.4.26 的行为（登录回包优先合并订阅、回落通用订阅）已随方案 B 退场
// 而废止：SubUser 登录取他自己的订阅，管理员登录兜底取第一份已启用的
// SubUser 的订阅；只有独立合并/通用订阅时不再给 subscription_url。
// 1.4.40：旧合并订阅彻底移除，夹具改用独立合并订阅（setupIndMergedSub1426）。
// 本文件保留 1426 的脚手架（setupIndMergedSub1426），断言改成新口径。

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// setupIndMergedSub1426 给测试面板加一份独立合并订阅，返回令牌明文。
// withToken=false 时只占位（TokenEnc 为空，算“无链接”）；disabled=true
// 时是停用那份。调用方按需要多调几次、按顺序加多份。
func setupIndMergedSub1426(t *testing.T, s *Server, id string, disabled, withToken bool) string {
	t.Helper()
	ims := IndependentMergedSub{ID: id, Name: "测试独立合并订阅 " + id, CreatedAt: "2026-11-25T00:00:00Z", Disabled: disabled}
	if withToken {
		tok := generateIndMergedToken()
		enc, err := s.encryptSubTokenNow(tok)
		if err != nil {
			t.Fatal(err)
		}
		ims.TokenHash = hashSubToken(tok)
		ims.TokenEnc = enc
		s.cfg.IndependentMergedSubs = append(s.cfg.IndependentMergedSubs, ims)
		return tok
	}
	s.cfg.IndependentMergedSubs = append(s.cfg.IndependentMergedSubs, ims)
	return ""
}

// setupSubUser1426 给测试面板直接加一个订阅用户（不走创建接口），
// 返回令牌明文。
func setupSubUser1426(t *testing.T, s *Server, id, username string) string {
	t.Helper()
	tok := generateSubToken()
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: id, Name: "测试用户 " + id, Username: username, TokenHash: hashSubToken(tok), TokenEnc: enc, CreatedAt: "2026-11-25T00:00:00Z"})
	return tok
}

// TestClientLoginIndMergedSub1426 只有独立合并订阅与通用订阅时，管理员登录
// 不再给 subscription_url（两类订阅已退场，不再从登录回包取）。
func TestClientLoginIndMergedSub1426(t *testing.T) {
	s := newTestServer144(t)
	setupGenericSub1345(t, s)
	setupIndMergedSub1426(t, s, "ims-1426-a", false, true)
	code, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if code != 200 {
		t.Fatalf("应 200，实际 %d：%v", code, resp)
	}
	if _, ok := resp["subscription_url"]; ok {
		t.Fatalf("只有独立合并/通用订阅时不应给 subscription_url，实际 %v", resp["subscription_url"])
	}
	if resp["account_type"] != "admin" || resp["plan"] != nil {
		t.Fatalf("管理员登录 account_type 应为 admin、plan 为 null，实际 %v", resp)
	}
}

// TestClientLoginIndMergedSubFirstEnabled1426 有订阅用户时管理员登录取
// 第一份已启用的 SubUser 的订阅，不是独立合并订阅也不是通用订阅。
func TestClientLoginIndMergedSubFirstEnabled1426(t *testing.T) {
	s := newTestServer144(t)
	setupGenericSub1345(t, s)
	setupIndMergedSub1426(t, s, "ims-1426-first", false, true)
	firstTok := setupSubUser1426(t, s, "su-1426-first", "u-first")
	setupSubUser1426(t, s, "su-1426-second", "u-second")
	_, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	u, _ := resp["subscription_url"].(string)
	if !strings.Contains(u, "/"+s.subPath()+"/"+firstTok) {
		t.Fatalf("应取第一份 SubUser 的订阅，实际 %q", u)
	}

	// 第一份 SubUser 停用时取第二份。
	s2 := newTestServer144(t)
	setupSubUser1426(t, s2, "su-1426-off", "u-off")
	secondTok := setupSubUser1426(t, s2, "su-1426-on", "u-on")
	s2.cfg.SubUsers[0].Disabled = true
	_, resp2 := clientLogin1345(t, s2, `{"username":"admin","password":"test-password-123"}`)
	u2, _ := resp2["subscription_url"].(string)
	if !strings.Contains(u2, "/"+s2.subPath()+"/"+secondTok) {
		t.Fatalf("第一份停用时应取第二份 SubUser 的订阅，实际 %q", u2)
	}
}

// TestClientLoginFirstEnabledSkipsExpired1426 管理员登录兜底跳过已过期的
// SubUser：第一份已过期时取第二份未过期的；全部过期时不给 subscription_url；
// 未到期的第一份仍选中（对照）。
func TestClientLoginFirstEnabledSkipsExpired1426(t *testing.T) {
	s := newTestServer144(t)
	expiredTok := setupSubUser1426(t, s, "su-1426-expired", "u-expired")
	validTok := setupSubUser1426(t, s, "su-1426-valid", "u-valid")
	s.cfg.SubUsers[0].ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
	_, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	u, _ := resp["subscription_url"].(string)
	if !strings.Contains(u, "/"+s.subPath()+"/"+validTok) {
		t.Fatalf("第一份已过期时应取第二份未过期的订阅，实际 %q（过期那条不应被选中）", u)
	}
	if strings.Contains(u, expiredTok) {
		t.Fatalf("已过期的订阅不应被兜底选中，实际 %q", u)
	}
	// 全部过期：不给 subscription_url。
	s2 := newTestServer144(t)
	setupSubUser1426(t, s2, "su-1426-expired-only", "u-expired-only")
	s2.cfg.SubUsers[0].ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
	_, resp2 := clientLogin1345(t, s2, `{"username":"admin","password":"test-password-123"}`)
	if _, ok := resp2["subscription_url"]; ok {
		t.Fatalf("全部过期时不应给 subscription_url，实际 %v", resp2)
	}
	// 未到期的第一份仍选中（对照）。
	s3 := newTestServer144(t)
	futureTok := setupSubUser1426(t, s3, "su-1426-future", "u-future")
	s3.cfg.SubUsers[0].ExpiresAt = time.Now().Add(time.Hour).Format(time.RFC3339)
	_, resp3 := clientLogin1345(t, s3, `{"username":"admin","password":"test-password-123"}`)
	u3, _ := resp3["subscription_url"].(string)
	if !strings.Contains(u3, "/"+s3.subPath()+"/"+futureTok) {
		t.Fatalf("未到期的第一份应被选中，实际 %q", u3)
	}
}

// TestClientLoginIndMergedSubFallback1426 合并订阅停用/无链接/密文解不开
// 时，管理员登录仍不回落通用订阅（通用订阅同样已退场）。
func TestClientLoginIndMergedSubFallback1426(t *testing.T) {
	s := newTestServer144(t)
	setupIndMergedSub1426(t, s, "ims-1426-off", true, true)
	setupGenericSub1345(t, s)
	_, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if _, ok := resp["subscription_url"]; ok {
		t.Fatalf("独立合并订阅停用时不应回落通用订阅，实际 %v", resp["subscription_url"])
	}

	s2 := newTestServer144(t)
	s2.cfg.IndependentMergedSubs = append(s2.cfg.IndependentMergedSubs, IndependentMergedSub{ID: "ims-1426-bad", TokenEnc: "not-valid-ciphertext"})
	setupGenericSub1345(t, s2)
	_, resp2 := clientLogin1345(t, s2, `{"username":"admin","password":"test-password-123"}`)
	if _, ok := resp2["subscription_url"]; ok {
		t.Fatalf("独立合并订阅密文解不开时不应回落通用订阅，实际 %v", resp2["subscription_url"])
	}
}

// TestClientLoginNoSubUserNoGeneric1426 既无 SubUser、又无可用合并/通用
// 订阅时，登录成功但不给 subscription_url 字段。
func TestClientLoginNoSubUserNoGeneric1426(t *testing.T) {
	s := newTestServer144(t)
	code, resp := clientLogin1345(t, s, `{"username":"admin","password":"test-password-123"}`)
	if code != 200 {
		t.Fatalf("应 200，实际 %d：%v", code, resp)
	}
	if _, ok := resp["subscription_url"]; ok {
		t.Fatalf("没有订阅用户时不应给 subscription_url，实际 %v", resp["subscription_url"])
	}
}

// TestClientSubURLSubUser1426 订阅链接拼法（现取 SubUser 的订阅）：
// 对外域名优先；没填时用主端口（不是登录监听的 9876 端口），
// scheme 跟主端口的 HTTPS 开关。
func TestClientSubURLSubUser1426(t *testing.T) {
	s := newTestServer144(t)
	subTok := setupSubUser1426(t, s, "su-1426-a", "u-a")
	req := httptest.NewRequest("POST", "/api/v1/client/login", nil)
	req.Host = "panel.example.com:9876"
	// 对外域名：https://域名/路径/订阅令牌。
	s.cfg.SubPublicDomain = "cc.example.com"
	if got := s.clientSubscriptionURL(req); got != "https://cc.example.com/"+s.subPath()+"/"+subTok {
		t.Fatalf("对外域名时订阅链接应为 https://cc.example.com/路径/令牌，实际 %q", got)
	}
	// 没对外域名、主端口开 HTTPS：https://主端口。
	s.cfg.SubPublicDomain = ""
	s.cfg.HTTPSEnabled = true
	if got := s.clientSubscriptionURL(req); got != "https://panel.example.com:11111/"+s.subPath()+"/"+subTok {
		t.Fatalf("主端口 HTTPS 时订阅链接应走主端口，实际 %q", got)
	}
	// 没对外域名、主端口 HTTP：http://主端口。
	s.cfg.HTTPSEnabled = false
	if got := s.clientSubscriptionURL(req); got != "http://panel.example.com:11111/"+s.subPath()+"/"+subTok {
		t.Fatalf("主端口 HTTP 时订阅链接应为 http，实际 %q", got)
	}
}
