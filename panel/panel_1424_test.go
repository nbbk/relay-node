package main

// v1.4.24：永久订阅的 subscription-userinfo 头补 expire=0。
//
// 根因：永久订阅无到期，subUserinfoHeader／subUserinfoHeaderForUser
// 拼头时直接不带 expire，头是 `upload=X; download=Y; total=Z`；
// 小火箭解析这条头时缺 expire 就失败、把整条头原样显示成
// UPLOAD=...; DOWNLOAD=...; TOTAL=...（无箭头、无 GB 单位）。
// 有 expire 的订阅都显示格式化（↑↓箭头＋GB）。Worker 原样透传
// 响应头，不是它的问题。
//
// 修法（只改两处拼头）：头非空但无到期时追加 expire=0；有到期时
// 仍用 expire=<unix> 不变；三项全无时仍返回空、不发头（不编数据
// 口径不变）。订阅内容与节点逻辑一字不动。
//
// 覆盖：
//   - 版本断言 1.4.24。
//   - 通用订阅（永久）：有流量＋上限时头含 expire=0；三项全无时不发头。
//   - 一人一条订阅（永久）：有用量＋上限时头含 expire=0。

import (
	"strings"
	"testing"
)

func TestVersion1424(t *testing.T) {
	if Version != "1.4.38" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
}

func TestUserinfoPermanentExpireZero1424(t *testing.T) {
	s := newTestSubServer(t)

	// 三项全无（永久令牌、无落地数据）→ 仍不发头。
	// 1.4.27：通用订阅已退场，改走永久的订阅用户。
	tok := generateSubToken()
	subUserForToken1427(t, s, tok)
	rec := callSub(t, s, tok, "/sub/"+tok, "")
	if rec.Code != 200 {
		t.Fatalf("订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if h := rec.Header().Get("subscription-userinfo"); h != "" {
		t.Fatalf("三项全无时不应发 userinfo 头，实际 %q", h)
	}

	// 订阅用户（永久）：落地有流量累计与上限、无到期 → 头含 expire=0。
	s.stateMu.Lock()
	s.state.Landings["203.0.113.1"] = &LandingExt{TrafficLimitBytes: 100 << 30}
	s.stateMu.Unlock()
	lt := s.traffic.get("203.0.113.1")
	lt.UpTotal = 123456
	lt.DownTotal = 789012
	rec = callSub(t, s, tok, "/sub/"+tok, "")
	h := rec.Header().Get("subscription-userinfo")
	want := "upload=123456; download=789012; total=107374182400; expire=0"
	if h != want {
		t.Fatalf("永久订阅用户头应为 %q，实际 %q", want, h)
	}

	// 一人一条订阅（永久）：有用量＋上限、无到期 → 头含 expire=0。
	tok2 := generateSubToken()
	enc2, err := s.encryptSubTokenNow(tok2)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{
		ID: "u-1424", Name: "永久的人", TokenHash: hashSubToken(tok2), TokenEnc: enc2,
		ExpiresAt: "", TrafficLimitBytes: 2 << 30, TrafficLimitMode: "total",
	})
	key := userSubKey("subuser", "u-1424")
	urec := s.userTraffic.get(key)
	urec.UpTotal = 1000
	urec.DownTotal = 2000
	rec = callSub(t, s, tok2, "/sub/"+tok2, "")
	if rec.Code != 200 {
		t.Fatalf("一人一条订阅应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	h = rec.Header().Get("subscription-userinfo")
	want = "upload=1000; download=2000; total=2147483648; expire=0"
	if h != want {
		t.Fatalf("永久一人一条订阅头应为 %q，实际 %q", want, h)
	}
	// 头里 expire=0 不是编数据：upload/download/total 都是真实用量与上限，
	// 0 只表示“无到期”（小火箭据此显示 N/A），不是一个真实的到期时间。
	if strings.Contains(h, "expire=0;") || strings.HasPrefix(h, "expire=") {
		t.Fatalf("expire=0 应是最后一项，实际 %q", h)
	}
}
