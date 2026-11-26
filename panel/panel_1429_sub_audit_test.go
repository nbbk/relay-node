package main

// ---------- v1.4.28：审计日志记用户名＋令牌成对自检 ----------
//
// 反馈 2：handleSub 四处审计原来全写死 user="订阅客户端"，现在记该订阅
// 用户的 Name（空回落 Username）；退场记合并订阅 Name／通用订阅记
// “通用订阅”；令牌不对时 detail 补哈希前 8 位（绝不记明文）；429 也记
// 一条。反馈 4 顺手修：客户端登录失败 detail 按真实原因记（停用／过期／
// 密码错误），响应仍是统一 401。反馈 5 服务端部分：启动／恢复后自检
// TokenHash 与 TokenEnc 是否成对，备份恢复按“对”取。

import (
	"strings"
	"testing"
)

// lastAudit1429 取审计日志里最新一条指定操作的记录。
func lastAudit1429(t *testing.T, s *Server, action string) AuditEntry {
	t.Helper()
	entries, _ := s.readAuditPage(1, 1000, "")
	for _, e := range entries {
		if e.Action == action {
			return e
		}
	}
	t.Fatalf("审计日志里没有 %q 操作", action)
	return AuditEntry{}
}

// TestSubAuditUserName1429 拉取订阅审计记用户名：成功记 SubUser 的 Name，
// Name 为空回落 Username；过期记 Name；退场记合并订阅 Name／通用订阅。
func TestSubAuditUserName1429(t *testing.T) {
	s := newTestSubServer(t)
	// 成功：Name 在，用 Name。
	tok := generateSubToken()
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-a", Name: "小明", Username: "u-1111", TokenHash: hashSubToken(tok), CreatedAt: "2026-11-25T00:00:00Z"})
	if rec := callSub(t, s, tok, "/sub/"+tok, ""); rec.Code != 200 {
		t.Fatalf("对令牌应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	e := lastAudit1429(t, s, "拉取订阅")
	if e.User != "小明" || e.Result != "成功" {
		t.Fatalf("成功审计用户应记 Name 小明，实际 %+v", e)
	}
	if !strings.Contains(e.Detail, hashSubToken(tok)[:8]) || strings.Contains(e.Detail, tok) {
		t.Fatalf("成功审计 detail 应含哈希前 8 位、不含令牌明文，实际 %q", e.Detail)
	}
	// Name 为空时回落 Username。
	tok2 := generateSubToken()
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-b", Name: "", Username: "u-2222", TokenHash: hashSubToken(tok2), CreatedAt: "2026-11-25T00:00:00Z"})
	if rec := callSub(t, s, tok2, "/sub/"+tok2, ""); rec.Code != 200 {
		t.Fatalf("对令牌应 200，实际 %d", rec.Code)
	}
	e = lastAudit1429(t, s, "拉取订阅")
	if e.User != "u-2222" {
		t.Fatalf("Name 为空时审计用户应回落 Username u-2222，实际 %+v", e)
	}
	// 过期：记 Name。
	tok3 := generateSubToken()
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-c", Name: "过期户", Username: "u-3333", TokenHash: hashSubToken(tok3), ExpiresAt: "2020-01-01T00:00:00Z", CreatedAt: "2026-11-25T00:00:00Z"})
	if rec := callSub(t, s, tok3, "/sub/"+tok3, ""); rec.Code != 410 {
		t.Fatalf("过期令牌应 410，实际 %d", rec.Code)
	}
	e = lastAudit1429(t, s, "拉取订阅")
	if e.User != "过期户" || e.Detail != "订阅已过期" {
		t.Fatalf("过期审计应记 Name 过期户／订阅已过期，实际 %+v", e)
	}
	// 退场：合并订阅记它的 Name。
	mergedTok := generateSubToken()
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: "ms-a", Name: "合并一", TokenHash: hashSubToken(mergedTok), CreatedAt: "2026-11-25T00:00:00Z"})
	if rec := callSub(t, s, mergedTok, "/sub/"+mergedTok, ""); rec.Code != 410 {
		t.Fatalf("合并订阅令牌应 410，实际 %d", rec.Code)
	}
	e = lastAudit1429(t, s, "拉取订阅")
	if e.User != "合并一" || e.Detail != "订阅已退场" {
		t.Fatalf("合并退场审计应记合并订阅 Name，实际 %+v", e)
	}
	// 退场：通用订阅记“通用订阅”。
	genTok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(genTok)
	if rec := callSub(t, s, genTok, "/sub/"+genTok, ""); rec.Code != 410 {
		t.Fatalf("通用订阅令牌应 410，实际 %d", rec.Code)
	}
	e = lastAudit1429(t, s, "拉取订阅")
	if e.User != "通用订阅" {
		t.Fatalf("通用退场审计应记“通用订阅”，实际 %+v", e)
	}
}

// TestSubAuditInvalidAndLock1429 令牌不对时审计 detail 补哈希前 8 位（不记
// 明文）；同一 IP 连续失败 10 次后第 11 次 429，也记一条审计。
func TestSubAuditInvalidAndLock1429(t *testing.T) {
	s := newTestSubServer(t)
	bad := generateSubToken()
	if rec := callSub(t, s, bad, "/sub/"+bad, ""); rec.Code != 404 {
		t.Fatalf("错令牌应 404，实际 %d", rec.Code)
	}
	e := lastAudit1429(t, s, "拉取订阅")
	if e.User != "订阅客户端" {
		t.Fatalf("令牌不对时审计用户仍是“订阅客户端”，实际 %+v", e)
	}
	if !strings.Contains(e.Detail, hashSubToken(bad)[:8]) || strings.Contains(e.Detail, bad) {
		t.Fatalf("令牌不对的 detail 应含哈希前 8 位、不含令牌明文，实际 %q", e.Detail)
	}
	for i := 0; i < 9; i++ {
		_ = callSub(t, s, generateSubToken(), "/sub/x", "")
	}
	if rec := callSub(t, s, generateSubToken(), "/sub/x", ""); rec.Code != 429 {
		t.Fatalf("10 次失败后应 429，实际 %d", rec.Code)
	}
	e = lastAudit1429(t, s, "拉取订阅")
	if e.Result != "失败" || e.Detail != "请求太频繁" {
		t.Fatalf("429 应记一条“请求太频繁”审计，实际 %+v", e)
	}
}

// TestClientLoginFailDetail1429 登录失败 detail 按真实原因记：停用／过期
// 的 SubUser 用正确密码登录时不再记成“密码错误”；密码错仍记密码错误；
// 响应一律 401，不向调用方区分原因。
func TestClientLoginFailDetail1429(t *testing.T) {
	s := newTestServer144(t)
	h, err := hashPassword("user-pass-1429")
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.SubUsers = []SubUser{
		{ID: "su-off", Name: "停用户", Username: "off1429", PasswordHash: h, Disabled: true, CreatedAt: "2026-11-25T00:00:00Z"},
		{ID: "su-exp", Name: "过期户", Username: "exp1429", PasswordHash: h, ExpiresAt: "2020-01-01T00:00:00Z", CreatedAt: "2026-11-25T00:00:00Z"},
		{ID: "su-ok", Name: "正常户", Username: "ok1429", PasswordHash: h, CreatedAt: "2026-11-25T00:00:00Z"},
	}
	cases := []struct {
		body   string
		detail string
	}{
		{`{"username":"off1429","password":"user-pass-1429"}`, "账号已停用"},
		{`{"username":"exp1429","password":"user-pass-1429"}`, "订阅已过期"},
		{`{"username":"ok1429","password":"wrong-pass"}`, "密码错误"},
	}
	for _, c := range cases {
		code, _ := clientLogin1345(t, s, c.body)
		if code != 401 {
			t.Fatalf("登录失败应 401，实际 %d（%s）", code, c.body)
		}
		e := lastAudit1429(t, s, "客户端登录")
		if e.Result != "失败" || e.Detail != c.detail {
			t.Fatalf("登录失败审计 detail 应为 %q，实际 %+v", c.detail, e)
		}
	}
}

// TestCheckSubTokenPairs1429 自检：成对的通过；哈希与密文不是一对时报出
// 该用户 ID；密文解不开的跳过（密钥丢了走 legacy 提示）。
func TestCheckSubTokenPairs1429(t *testing.T) {
	s := newTestSubServer(t)
	goodTok := generateSubToken()
	goodEnc, err := s.encryptSubTokenNow(goodTok)
	if err != nil {
		t.Fatal(err)
	}
	badTok := generateSubToken()
	badEnc, err := s.encryptSubTokenNow(badTok)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.SubUsers = []SubUser{
		{ID: "su-good", Name: "成对", TokenHash: hashSubToken(goodTok), TokenEnc: goodEnc},
		{ID: "su-bad", Name: "失配", TokenHash: hashSubToken(generateSubToken()), TokenEnc: badEnc},
		{ID: "su-unreadable", Name: "解不开", TokenHash: hashSubToken(goodTok), TokenEnc: "!!!不是密文!!!"},
		{ID: "su-noenc", Name: "没密文", TokenHash: hashSubToken(goodTok), TokenEnc: ""},
	}
	bad := s.checkSubTokenPairs()
	if len(bad) != 1 || bad[0] != "su-bad" {
		t.Fatalf("自检应只报出 su-bad，实际 %v", bad)
	}
	// 失配用户的卡片令牌确实拉不到订阅（根因复现：显示旧令牌、校验新哈希）。
	if rec := callSub(t, s, badTok, "/sub/"+badTok, ""); rec.Code != 404 {
		t.Fatalf("失配令牌拉订阅应 404，实际 %d", rec.Code)
	}
}

// TestRestoreTokenPairNotMixed1429 备份恢复按“对”取：本机已有该用户且
// 哈希是轮换后的新令牌、备份密文是旧令牌时，不再把本机哈希＋备份密文
// 混配——恢复后本机哈希保留、密文留空，旧令牌（新哈希的令牌）拉订阅
// 仍通；备份密文与本机哈希是同一令牌时，密文可以补进来。
func TestRestoreTokenPairNotMixed1429(t *testing.T) {
	src, _, userTok, _ := setupMigrationSource1329(t)
	p := backupPayload1329(t, src)
	newTok := generateSubToken()

	// ① 混配场景：本机 su-1 的哈希已轮换成新令牌，密文空着。
	dst := newTestSubServer(t)
	dst.cfg.SubUsers = []SubUser{{ID: "su-1", Name: "小明", TokenHash: hashSubToken(newTok), CreatedAt: "2026-11-20T00:00:00Z"}}
	if _, fail := dst.applyImportPayload(p, importApplyOpts{preBackupLabel: "测试", auditAction: "测试恢复", user: "test", migrationPassphrase: testMigrationPassphrase}); fail != nil {
		t.Fatalf("恢复应成功，实际失败：%v", fail)
	}
	u := dst.cfg.SubUsers[0]
	if u.TokenHash != hashSubToken(newTok) || u.TokenEnc != "" {
		t.Fatalf("混配场景：应保留本机新哈希、密文留空，实际 hash 前 8=%q enc=%q", u.TokenHash[:8], u.TokenEnc)
	}
	if rec := callSub(t, dst, newTok, "/sub/"+newTok, ""); rec.Code != 200 {
		t.Fatalf("本机新令牌恢复后应仍能拉订阅，实际 %d", rec.Code)
	}
	if rec := callSub(t, dst, userTok, "/sub/"+userTok, ""); rec.Code != 404 {
		t.Fatalf("备份旧令牌不应被复活，实际 %d", rec.Code)
	}
	if bad := dst.checkSubTokenPairs(); len(bad) != 0 {
		t.Fatalf("恢复后自检应无失配，实际 %v", bad)
	}

	// ② 同令牌补密文：本机只有备份令牌的哈希、没密文，恢复后密文补进来。
	dst2 := newTestSubServer(t)
	dst2.cfg.SubUsers = []SubUser{{ID: "su-1", Name: "小明", TokenHash: hashSubToken(userTok), CreatedAt: "2026-11-20T00:00:00Z"}}
	if _, fail := dst2.applyImportPayload(p, importApplyOpts{preBackupLabel: "测试", auditAction: "测试恢复", user: "test", migrationPassphrase: testMigrationPassphrase}); fail != nil {
		t.Fatalf("恢复应成功，实际失败：%v", fail)
	}
	u2 := dst2.cfg.SubUsers[0]
	if u2.TokenEnc != src.cfg.SubUsers[0].TokenEnc {
		t.Fatalf("同令牌场景：备份密文应补进来，实际 enc=%q", u2.TokenEnc)
	}
	if rec := callSub(t, dst2, userTok, "/sub/"+userTok, ""); rec.Code != 200 {
		t.Fatalf("补回密文后旧令牌应能拉订阅，实际 %d", rec.Code)
	}
}
