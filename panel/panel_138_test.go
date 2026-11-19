package main

// v1.3.8 登录告警 + 二次验证 TOTP 测试。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RFC 6238 的 SHA1 测试向量（8 位验证码，取后 6 位就是本面板的 6 位验证码）：
// 密钥原文 "12345678901234567890"，base32 是 GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ。
func TestTOTPCodeRFCVector(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},         // 94287082
		{1111111109, "081804"}, // 07081804
		{1234567890, "005924"}, // 89005924
	} {
		got, err := totpCodeAt(secret, time.Unix(tc.unix, 0))
		if err != nil {
			t.Fatalf("totpCodeAt(%d): %v", tc.unix, err)
		}
		if got != tc.want {
			t.Fatalf("totpCodeAt(%d)=%q want %q", tc.unix, got, tc.want)
		}
	}
	// 容差：当前时间步前后一个时间步的码都认，前后两个时间步的不认。
	now := time.Unix(1234567890, 0)
	code, _ := totpCodeAt(secret, now)
	if !verifyTOTP(secret, code, now) {
		t.Fatal("当前时间步的验证码应通过")
	}
	prev, _ := totpCodeAt(secret, now.Add(-30*time.Second))
	if !verifyTOTP(secret, prev, now) {
		t.Fatal("上一个时间步的验证码应通过（容差）")
	}
	next, _ := totpCodeAt(secret, now.Add(30*time.Second))
	if !verifyTOTP(secret, next, now) {
		t.Fatal("下一个时间步的验证码应通过（容差）")
	}
	old, _ := totpCodeAt(secret, now.Add(-60*time.Second))
	if verifyTOTP(secret, old, now) {
		t.Fatal("两个时间步之前的验证码不应通过")
	}
	// 非 6 位、非数字、空密钥都不认。
	for _, bad := range []string{"12345", "1234567", "abcdef", "12345a", ""} {
		if verifyTOTP(secret, bad, now) {
			t.Fatalf("验证码 %q 不应通过", bad)
		}
	}
	if _, err := totpCodeAt("!!!不是base32!!!", now); err == nil {
		t.Fatal("非法密钥应报错")
	}
	// 带空格的验证码（验证器常显示成 123 456）应通过。
	spaced := code[:3] + " " + code[3:]
	if !verifyTOTP(secret, spaced, now) {
		t.Fatal("带空格的验证码应通过")
	}
}

func TestBackupCodesGenerateConsume(t *testing.T) {
	codes, hashes := generateBackupCodes()
	if len(codes) != 8 || len(hashes) != 8 {
		t.Fatalf("备用码应为 8 个，实际 %d/%d", len(codes), len(hashes))
	}
	for _, c := range codes {
		if len(c) != 10 {
			t.Fatalf("备用码长度应为 10，实际 %q", c)
		}
	}
	// 每个码都能用一次，用过就废。
	for _, c := range codes {
		if _, ok := verifyBackupCode(c, hashes); !ok {
			t.Fatalf("备用码 %q 应通过", c)
		}
	}
	// 明文与哈希不能相等（存的是哈希不是明文）。
	for i, c := range codes {
		if hashes[i] == c {
			t.Fatal("备用码存的应是哈希，不是明文")
		}
	}
	// 模拟消费第一个：按哈希删掉后，再验同一个应失败。
	if idx, ok := verifyBackupCode(codes[0], hashes); !ok {
		t.Fatal("第一个备用码应通过")
	} else {
		hashes = append(append([]string{}, hashes[:idx]...), hashes[idx+1:]...)
	}
	if _, ok := verifyBackupCode(codes[0], hashes); ok {
		t.Fatal("用过的备用码不应再通过")
	}
	if _, ok := verifyBackupCode("不存在的码", hashes); ok {
		t.Fatal("错的备用码不应通过")
	}
}

func postLogin(t *testing.T, h http.Handler, body string) (int, string, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header().Get("Set-Cookie")
}

// TestLoginTOTPFlow 二次验证完整流程：默认关（旧配置）→ 开启后密码对了
// 还差验证码 → 验证码错了计入锁定 → 验证码对了进门 → 备用码能登录且用一次废一个。
func TestLoginTOTPFlow(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

	// 默认关：旧 config.json 里没有新字段，密码登录与原来一样。
	if s.cfg.TOTPEnabled || s.cfg.LoginAlert {
		t.Fatal("新字段默认应为关闭")
	}
	if code, _, cookie := postLogin(t, h, `{"username":"admin","password":"test-password-123"}`); code != 200 || !strings.Contains(cookie, "relay_panel_session") {
		t.Fatalf("默认关时密码登录应成功发会话：code=%d cookie=%q", code, cookie)
	}

	// 开启二次验证。
	s.cfg.TOTPSecret = secret
	s.cfg.TOTPEnabled = true
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
	// 密码对了、没传验证码：返回 need_totp，不发会话、不记失败。
	code, body, cookie := postLogin(t, h, `{"username":"admin","password":"test-password-123"}`)
	if code != 200 || !strings.Contains(body, "need_totp") {
		t.Fatalf("没传验证码应返回 need_totp：code=%d body=%s", code, body)
	}
	if strings.Contains(cookie, "relay_panel_session") {
		t.Fatal("need_totp 时不应发会话 cookie")
	}
	// 密码对了、验证码错了：401，且计入锁定（后面单独测 5 次锁）。
	if code, body, _ := postLogin(t, h, `{"username":"admin","password":"test-password-123","code":"000000"}`); code != 401 || !strings.Contains(body, "验证码不对") {
		t.Fatalf("错验证码应 401：code=%d body=%s", code, body)
	}
	// 密码对了、验证码对了：200 + 会话。
	good, err := totpCodeAt(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if code, _, cookie := postLogin(t, h, `{"username":"admin","password":"test-password-123","code":"`+good+`"}`); code != 200 || !strings.Contains(cookie, "relay_panel_session") {
		t.Fatalf("正确验证码应登录成功：code=%d cookie=%q", code, cookie)
	}
	// 备用码能登录，且用一次就废。
	codes, hashes := generateBackupCodes()
	s.cfg.TOTPBackupCodes = hashes
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := postLogin(t, h, `{"username":"admin","password":"test-password-123","code":"`+codes[0]+`"}`); code != 200 {
		t.Fatalf("备用码登录应成功：code=%d", code)
	}
	if len(s.cfg.TOTPBackupCodes) != 7 {
		t.Fatalf("备用码用掉一个后应剩 7 个，实际 %d", len(s.cfg.TOTPBackupCodes))
	}
	if code, _, _ := postLogin(t, h, `{"username":"admin","password":"test-password-123","code":"`+codes[0]+`"}`); code != 401 {
		t.Fatalf("用过的备用码应 401：code=%d", code)
	}
}

// TestLoginTOTPWrongCodeLocks 验证码错 5 次同样触发锁定（与密码失败共用一套）。
func TestLoginTOTPWrongCodeLocks(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	s.cfg.TOTPSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	s.cfg.TOTPEnabled = true
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if code, _, _ := postLogin(t, h, `{"username":"admin","password":"test-password-123","code":"000000"}`); code != 401 {
			t.Fatalf("第 %d 次错验证码应 401，实际 %d", i+1, code)
		}
	}
	if code, _, _ := postLogin(t, h, `{"username":"admin","password":"test-password-123","code":"000000"}`); code != 429 {
		t.Fatalf("第 6 次应 429 锁定，实际 %d", code)
	}
}

// TestLoginAlertDedupAndLock 登录告警：新 IP 只记一次；连续 5 次失败触发锁定时记审计。
func TestLoginAlertDedupAndLock(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	s.cfg.LoginAlert = true
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
	// 成功登录两次（同一 IP）：IP 只记一次。
	for i := 0; i < 2; i++ {
		if code, _, _ := postLogin(t, h, `{"username":"admin","password":"test-password-123"}`); code != 200 {
			t.Fatalf("第 %d 次登录应 200，实际 %d", i+1, code)
		}
	}
	if len(s.state.LoginSeenIPs) != 1 {
		t.Fatalf("同一 IP 登录两次应只记一个 IP，实际 %v", s.state.LoginSeenIPs)
	}
	// 连续 5 次错密码：第 5 次触发锁定，审计日志里应有“登录锁定”。
	for i := 0; i < 5; i++ {
		if code, _, _ := postLogin(t, h, `{"username":"admin","password":"wrong"}`); code != 401 {
			t.Fatalf("第 %d 次错密码应 401，实际 %d", i+1, code)
		}
	}
	if code, _, _ := postLogin(t, h, `{"username":"admin","password":"wrong"}`); code != 429 {
		t.Fatalf("第 6 次应 429 锁定，实际 %d", code)
	}
	audit, err := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "登录锁定") {
		t.Fatalf("审计日志应有“登录锁定”，实际：%s", string(audit))
	}
}

// TestLoginAlertToggle 登录告警开关接口：开/关落盘。
func TestLoginAlertToggle(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	post := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/settings/login-alert", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleLoginAlert(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, _ := post(`{"enabled":true}`); code != 200 || !s.cfg.LoginAlert {
		t.Fatalf("开登录告警应 200 且落盘：code=%d", code)
	}
	if code, _ := post(`{"enabled":false}`); code != 200 || s.cfg.LoginAlert {
		t.Fatalf("关登录告警应 200 且落盘：code=%d", code)
	}
}

// TestTOTPSetupConfirmDisable 二次验证开关全流程：生成密钥 → 错码确认失败 →
// 对码确认开启（备用码只显示一次）→ 已开启时重复生成被拒 → 密码+验证码关闭。
func TestTOTPSetupConfirmDisable(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	call := func(fn http.HandlerFunc, body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		fn(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	// 确认前没有密钥：400。
	if code, _ := call(s.handleTOTPConfirm, `{"code":"123456"}`); code != 400 {
		t.Fatalf("没生成密钥就确认应 400，实际 %d", code)
	}
	// 生成密钥：返回密钥与二维码，密钥落盘、还不算开启。
	code, resp := call(s.handleTOTPSetup, `{}`)
	if code != 200 {
		t.Fatalf("生成密钥应 200，实际 %d", code)
	}
	secret, _ := resp["secret"].(string)
	qr, _ := resp["qr"].(string)
	if secret == "" || !strings.HasPrefix(qr, "data:image/png;base64,") {
		t.Fatalf("生成密钥应返回密钥与二维码 data URI：secret=%q qr前缀=%q", secret, qr[:min(30, len(qr))])
	}
	if s.cfg.TOTPEnabled {
		t.Fatal("只生成密钥时还不算开启")
	}
	// 错码确认：401，仍未开启。
	if code, _ := call(s.handleTOTPConfirm, `{"code":"000000"}`); code != 401 || s.cfg.TOTPEnabled {
		t.Fatalf("错码确认应 401 且仍未开启，实际 %d", code)
	}
	// 对码确认：200，返回 8 个备用码（只显示一次），开启成功。
	good, err := totpCodeAt(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	code, resp = call(s.handleTOTPConfirm, `{"code":"`+good+`"}`)
	if code != 200 {
		t.Fatalf("对码确认应 200，实际 %d", code)
	}
	backup, _ := resp["backup_codes"].([]any)
	if len(backup) != 8 {
		t.Fatalf("确认开启应返回 8 个备用码，实际 %v", resp["backup_codes"])
	}
	if !s.cfg.TOTPEnabled || len(s.cfg.TOTPBackupCodes) != 8 {
		t.Fatal("确认后应开启且存 8 个备用码哈希")
	}
	// 备用码返回的是明文，存的是哈希：逐个比对。
	for i, b := range backup {
		bs, _ := b.(string)
		if sha256Hex(bs) != s.cfg.TOTPBackupCodes[i] {
			t.Fatal("返回的备用码与存的哈希对不上")
		}
	}
	// 已开启时再生成密钥：400。
	if code, _ := call(s.handleTOTPSetup, `{}`); code != 400 {
		t.Fatalf("已开启时重复生成应 400，实际 %d", code)
	}
	// 关闭：密码错了 401；密码对了验证码错了 401；都对了 200，密钥与备用码清掉。
	if code, _ := call(s.handleTOTPDisable, `{"password":"wrong","code":"`+good+`"}`); code != 401 {
		t.Fatalf("关二次验证密码错了应 401，实际 %d", code)
	}
	if code, _ := call(s.handleTOTPDisable, `{"password":"test-password-123","code":"000000"}`); code != 401 {
		t.Fatalf("关二次验证验证码错了应 401，实际 %d", code)
	}
	if code, _ := call(s.handleTOTPDisable, `{"password":"test-password-123","code":"`+good+`"}`); code != 200 {
		t.Fatalf("关二次验证都对了应 200，实际 %d", code)
	}
	if s.cfg.TOTPEnabled || s.cfg.TOTPSecret != "" || len(s.cfg.TOTPBackupCodes) != 0 {
		t.Fatal("关闭后密钥与备用码应清掉")
	}
	// 未开启时再关：400。
	if code, _ := call(s.handleTOTPDisable, `{"password":"test-password-123","code":"`+good+`"}`); code != 400 {
		t.Fatalf("未开启时关闭应 400，实际 %d", code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestDisableTOTPInConfigFile SSH 恢复命令的磁盘逻辑：开着 → 关掉清干净；
// 本来没开 → 不动；文件缺失/损坏 → 报错。
func TestDisableTOTPInConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfg := PanelConfig{Username: "admin", TOTPEnabled: true, TOTPSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", TOTPBackupCodes: []string{"abc", "def"}}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := disableTOTPInConfigFile(dir)
	if err != nil || !changed {
		t.Fatalf("开着时应关掉：changed=%v err=%v", changed, err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got PanelConfig
	if err := json.Unmarshal(after, &got); err != nil {
		t.Fatal(err)
	}
	if got.TOTPEnabled || got.TOTPSecret != "" || len(got.TOTPBackupCodes) != 0 {
		t.Fatalf("关掉后应清干净：%+v", got)
	}
	if got.Username != "admin" {
		t.Fatal("关二次验证不应动其他配置")
	}
	changed, err = disableTOTPInConfigFile(dir)
	if err != nil || changed {
		t.Fatalf("本来没开时应不动：changed=%v err=%v", changed, err)
	}
	if _, err := disableTOTPInConfigFile(t.TempDir()); err == nil {
		t.Fatal("config.json 缺失时应报错")
	}
	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, "config.json"), []byte("{不是json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := disableTOTPInConfigFile(badDir); err == nil {
		t.Fatal("config.json 损坏时应报错")
	}
}

// TestSecuritySettingsResponse 设置接口返回 security 一节：默认值全是关，
// 且绝不返回密钥与备用码明文。
func TestSecuritySettingsResponse(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	req := httptest.NewRequest("GET", "/api/v1/settings", nil)
	rec := httptest.NewRecorder()
	s.handleSettings(rec, req)
	if rec.Code != 200 {
		t.Fatalf("设置接口应 200，实际 %d", rec.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	sec, ok := resp["security"].(map[string]any)
	if !ok {
		t.Fatalf("设置接口应有 security 一节：%s", rec.Body.String())
	}
	if sec["login_alert"] != false || sec["totp_enabled"] != false || sec["totp_has_secret"] != false {
		t.Fatalf("新字段默认应全关：%v", sec)
	}
	if sec["backup_codes_remaining"] != float64(0) {
		t.Fatalf("备用码剩余默认应为 0：%v", sec["backup_codes_remaining"])
	}
	if strings.Contains(rec.Body.String(), "totp_secret") || strings.Contains(rec.Body.String(), "backup_codes\":") {
		t.Fatal("设置接口绝不能返回密钥与备用码")
	}
}
