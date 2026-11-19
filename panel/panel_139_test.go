package main

// v1.3.9 二次验证确认流程真 bug 修复测试。
//
// 用户真机上报：扫码输验证码点保存，直接退出到登录页，toast 显示
// “确认失败：登录已过期，请重新登录”；重新登录后二次验证没开上，备用码也没出。
// 根因：验证码不对时 handleTOTPConfirm 返回 401，前端 api() 把除登录请求外的
// 所有 401 都当成会话过期，showLogin() 踢回登录页。修法：业务失败一律 400，
// 只有真正的未登录/会话失效才返回 401。

import (
	"encoding/base32"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// postAuthed139 走完整路由（带会话 cookie），返回状态码与响应体。
func postAuthed139(t *testing.T, h http.Handler, cookie, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestTOTPConfirmWrongCode400SessionAlive 错码确认返回 400（不是 401），
// 文案带服务器时间，且会话仍然有效（随后调设置接口还是 200）。
func TestTOTPConfirmWrongCode400SessionAlive(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	cookie := loginTestCookie(t, h)

	// 生成密钥：响应里带 server_time（服务器当前时间）。
	code, body := postAuthed139(t, h, cookie, "POST", "/api/v1/settings/totp/setup", `{}`)
	if code != 200 {
		t.Fatalf("生成密钥应 200，实际 %d：%s", code, body)
	}
	var setup map[string]any
	if err := json.Unmarshal([]byte(body), &setup); err != nil {
		t.Fatalf("生成密钥响应不是 JSON：%v", err)
	}
	secret, _ := setup["secret"].(string)
	serverTime, _ := setup["server_time"].(string)
	if secret == "" {
		t.Fatal("生成密钥应返回密钥")
	}
	if serverTime == "" {
		t.Fatal("生成密钥响应应带 server_time（供核对手机时间）")
	}
	if len(serverTime) < 19 {
		t.Fatalf("server_time 太短，实际 %q", serverTime)
	}
	if _, err := time.Parse("2006-01-02 15:04:05", serverTime[:19]); err != nil {
		t.Fatalf("server_time 前 19 位应为 2006-01-02 15:04:05，实际 %q", serverTime)
	}
	if !strings.Contains(serverTime, "UTC") {
		t.Fatalf("server_time 应带时区偏移与 UTC 时间，实际 %q", serverTime)
	}

	// 错码确认：400，文案是服务端原样（验证码不对＋服务器时间），不是“登录已过期”。
	code, body = postAuthed139(t, h, cookie, "POST", "/api/v1/settings/totp/confirm", `{"code":"000000"}`)
	if code != 400 {
		t.Fatalf("错码确认应 400，实际 %d：%s", code, body)
	}
	if !strings.Contains(body, "验证码不对") || !strings.Contains(body, "服务器时间") {
		t.Fatalf("错码确认文案应含“验证码不对”与服务器时间，实际：%s", body)
	}
	if !strings.Contains(body, "时间校正") || !strings.Contains(body, "只认最新一次生成") {
		t.Fatalf("错码确认文案应含时间校正指引与“只认最新一次生成”，实际：%s", body)
	}
	if strings.Contains(body, "登录已过期") {
		t.Fatalf("错码确认文案不应是“登录已过期”，实际：%s", body)
	}
	if s.cfg.TOTPEnabled {
		t.Fatal("错码确认后二次验证不应开启")
	}

	// 会话仍然有效：随后调设置接口还是 200（之前是 401 会被前端踢回登录页）。
	code, _ = postAuthed139(t, h, cookie, "GET", "/api/v1/settings", ``)
	if code != 200 {
		t.Fatalf("错码确认后会话应仍有效，调设置接口应 200，实际 %d", code)
	}

	// 对码确认：200，返回 8 个备用码，开启成功（全程不掉登录）。
	good, err := totpCodeAt(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	code, body = postAuthed139(t, h, cookie, "POST", "/api/v1/settings/totp/confirm", `{"code":"`+good+`"}`)
	if code != 200 {
		t.Fatalf("对码确认应 200，实际 %d：%s", code, body)
	}
	var confirm map[string]any
	_ = json.Unmarshal([]byte(body), &confirm)
	backup, _ := confirm["backup_codes"].([]any)
	if len(backup) != 8 {
		t.Fatalf("对码确认应返回 8 个备用码，实际 %v", confirm["backup_codes"])
	}
	if !s.cfg.TOTPEnabled {
		t.Fatal("对码确认后二次验证应开启")
	}
}

// TestTOTPDisableWrongPasswordCode400 关闭时密码/验证码错了返回 400，
// 二次验证仍开着；都对了才 200 关掉。
func TestTOTPDisableWrongPasswordCode400(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	cookie := loginTestCookie(t, h)

	// 先开起来。
	code, body := postAuthed139(t, h, cookie, "POST", "/api/v1/settings/totp/setup", `{}`)
	if code != 200 {
		t.Fatalf("生成密钥应 200，实际 %d", code)
	}
	var setup map[string]any
	_ = json.Unmarshal([]byte(body), &setup)
	secret, _ := setup["secret"].(string)
	good, err := totpCodeAt(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	code, _ = postAuthed139(t, h, cookie, "POST", "/api/v1/settings/totp/confirm", `{"code":"`+good+`"}`)
	if code != 200 || !s.cfg.TOTPEnabled {
		t.Fatalf("对码确认应 200 且开启，实际 %d", code)
	}

	// 密码错了：400，仍开着。
	code, body = postAuthed139(t, h, cookie, "POST", "/api/v1/settings/totp/disable", `{"password":"wrong","code":"`+good+`"}`)
	if code != 400 || !strings.Contains(body, "密码不对") {
		t.Fatalf("关闭时密码错了应 400 且文案“密码不对”，实际 %d：%s", code, body)
	}
	if !s.cfg.TOTPEnabled {
		t.Fatal("关闭失败后二次验证应仍开着")
	}
	// 验证码错了：400，仍开着。
	code, body = postAuthed139(t, h, cookie, "POST", "/api/v1/settings/totp/disable", `{"password":"test-password-123","code":"000000"}`)
	if code != 400 || !strings.Contains(body, "验证码不对") {
		t.Fatalf("关闭时验证码错了应 400 且文案“验证码不对”，实际 %d：%s", code, body)
	}
	if !s.cfg.TOTPEnabled {
		t.Fatal("关闭失败后二次验证应仍开着")
	}
	// 会话仍然有效。
	code, _ = postAuthed139(t, h, cookie, "GET", "/api/v1/settings", ``)
	if code != 200 {
		t.Fatalf("关闭失败后会话应仍有效，实际 %d", code)
	}
	// 都对了：200，关掉。
	code, _ = postAuthed139(t, h, cookie, "POST", "/api/v1/settings/totp/disable", `{"password":"test-password-123","code":"`+good+`"}`)
	if code != 200 || s.cfg.TOTPEnabled {
		t.Fatalf("关闭时都对了应 200 且关闭，实际 %d", code)
	}
}

// TestChangePasswordWrongOld400 改密码时旧密码错了返回 400（不是 401），
// 旧密码对了才 200 改掉。
func TestChangePasswordWrongOld400(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	cookie := loginTestCookie(t, h)

	code, body := postAuthed139(t, h, cookie, "POST", "/api/v1/settings/password", `{"old_password":"wrong","new_password":"new-password-123"}`)
	if code != 400 || !strings.Contains(body, "旧密码不对") {
		t.Fatalf("旧密码错了应 400 且文案“旧密码不对”，实际 %d：%s", code, body)
	}
	code, _ = postAuthed139(t, h, cookie, "POST", "/api/v1/settings/password", `{"old_password":"test-password-123","new_password":"new-password-123"}`)
	if code != 200 {
		t.Fatalf("旧密码对了应 200，实际 %d", code)
	}
}

// TestAuthFailuresStill401 真正的鉴权失败仍返回 401（回归保护）：
// 未登录调设置、登录错密码、登录（二次验证开着）错验证码。
func TestAuthFailuresStill401(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()

	// 未登录调设置：401。
	if code, _ := postAuthed139(t, h, "", "GET", "/api/v1/settings", ``); code != 401 {
		t.Fatalf("未登录调设置应 401，实际 %d", code)
	}
	// 登录错密码：401。
	if code, body, _ := postLogin(t, h, `{"username":"admin","password":"wrong"}`); code != 401 || !strings.Contains(body, "账号或密码不对") {
		t.Fatalf("登录错密码应 401，实际 %d：%s", code, body)
	}

	// 二次验证开着时，登录错验证码：401。
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	s.cfg.TOTPSecret = secret
	s.cfg.TOTPEnabled = true
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := postLogin(t, h, `{"username":"admin","password":"test-password-123","code":"000000"}`); code != 401 || !strings.Contains(body, "验证码不对") {
		t.Fatalf("登录错验证码应 401，实际 %d：%s", code, body)
	}
	// 对的验证码：200。
	good, err := totpCodeAt(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if code, _, cookie := postLogin(t, h, `{"username":"admin","password":"test-password-123","code":"`+good+`"}`); code != 200 || !strings.Contains(cookie, "relay_panel_session") {
		t.Fatalf("登录对验证码应 200 且发会话，实际 %d", code)
	}
}

// TestTOTPSetupOTPAUTHURL otpauth URL 格式：Google Authenticator 要能直接扫。
// 校验 scheme、label 编码、secret/issuer/algorithm/digits/period 五个参数——
// URL 错了真验证器扫了也白扫。
func TestTOTPSetupOTPAUTHURL(t *testing.T) {
	// 模拟用户现场：面板名是中文（CP管理），账号 admin。
	// 注意参数顺序：totpIssuerURL(secret, account, issuer)。
	u := totpIssuerURL("JBSWY3DPEHPK3PXP", "admin", "CP管理")
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("otpauth URL 解析失败：%v（%s）", err, u)
	}
	if parsed.Scheme != "otpauth" || parsed.Host != "totp" {
		t.Fatalf("otpauth URL 应为 otpauth://totp/…，实际 %s", u)
	}
	// label 是 issuer:account，中文部分须百分号编码（扫码器按编码后的串解出原文）。
	// 注意：url.Parse 会把 Path 解码回中文，要用 EscapedPath() 看原始编码形式。
	if !strings.HasPrefix(parsed.EscapedPath(), "/CP%E7%AE%A1%E7%90%86:") {
		t.Fatalf("otpauth label 应以 /CP%%E7%%AE%%A1%%E7%%90%%86: 开头，实际路径 %q（%s）", parsed.EscapedPath(), u)
	}
	q := parsed.Query()
	if q.Get("secret") != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("otpauth secret 参数应与密钥一致，实际 %q", q.Get("secret"))
	}
	if q.Get("issuer") != "CP管理" {
		t.Fatalf("otpauth issuer 参数应为 CP管理，实际 %q", q.Get("issuer"))
	}
	if q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Fatalf("otpauth 应为 SHA1/6 位/30 秒，实际 algorithm=%q digits=%q period=%q", q.Get("algorithm"), q.Get("digits"), q.Get("period"))
	}
}

// TestTOTPNewSecretIsStandardBase32 密钥是 20 字节随机数的 base32（无填充）：
// 标准解码器必须能解回 20 字节，否则真验证器拿到的是另一串密钥。
func TestTOTPNewSecretIsStandardBase32(t *testing.T) {
	secret, err := generateTOTPSecret()
	if err != nil {
		t.Fatalf("生成密钥失败：%v", err)
	}
	// base32 标准解码需要填充到 8 的倍数；面板存的是无填充形式，补齐再解。
	padded := secret
	for len(padded)%8 != 0 {
		padded += "="
	}
	raw, err := base32.StdEncoding.DecodeString(padded)
	if err != nil {
		t.Fatalf("新密钥 %q 不是标准 base32：%v", secret, err)
	}
	if len(raw) != 20 {
		t.Fatalf("新密钥解码后应为 20 字节，实际 %d 字节", len(raw))
	}
}

// TestTOTPSkewTwoSteps 容差应为前后两个时间步（v1.3.9 从 ±1 放宽到 ±2）。
func TestTOTPSkewTwoSteps(t *testing.T) {
	if totpSkew != 2 {
		t.Fatalf("容差 totpSkew 应为 2，实际 %d", totpSkew)
	}
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	now := time.Unix(1234567890, 0)
	for _, d := range []time.Duration{-60, -30, 0, 30, 60} {
		c, err := totpCodeAt(secret, now.Add(d*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if !verifyTOTP(secret, c, now) {
			t.Fatalf("时间步 %v 的验证码应通过", d)
		}
	}
	for _, d := range []time.Duration{-90, 90} {
		c, err := totpCodeAt(secret, now.Add(d*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if verifyTOTP(secret, c, now) {
			t.Fatalf("时间步 %v 的验证码不应通过", d)
		}
	}
}

// TestSettingsSecurityHasServerTime 设置接口的 security 一节带 server_time，
// 供二次验证卡片显示服务器当前时间。
func TestSettingsSecurityHasServerTime(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	h := s.routes()
	cookie := loginTestCookie(t, h)
	code, body := postAuthed139(t, h, cookie, "GET", "/api/v1/settings", ``)
	if code != 200 {
		t.Fatalf("调设置接口应 200，实际 %d", code)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("设置响应不是 JSON：%v", err)
	}
	sec, _ := resp["security"].(map[string]any)
	st, _ := sec["server_time"].(string)
	if st == "" {
		t.Fatal("security 一节应带 server_time")
	}
	if len(st) < 19 {
		t.Fatalf("server_time 太短，实际 %q", st)
	}
	if _, err := time.Parse("2006-01-02 15:04:05", st[:19]); err != nil {
		t.Fatalf("server_time 前 19 位应为 2006-01-02 15:04:05，实际 %q", st)
	}
	if !strings.Contains(st, "UTC") {
		t.Fatalf("server_time 应带 UTC 时间，实际 %q", st)
	}
}

// TestServerTimeDisplay 服务器时间显示带时区：本地时间 + 数字偏移 + UTC。
func TestServerTimeDisplay(t *testing.T) {
	s := serverTimeDisplay()
	// 如 2026-11-19 17:52:03 +0800（UTC 09:52:03）。
	if len(s) < 19 {
		t.Fatalf("serverTimeDisplay 太短：%q", s)
	}
	if _, err := time.Parse("2006-01-02 15:04:05", s[:19]); err != nil {
		t.Fatalf("serverTimeDisplay 前 19 位应为本地时间：%q", s)
	}
	if !strings.Contains(s, "UTC") {
		t.Fatalf("serverTimeDisplay 应含 UTC：%q", s)
	}
	// 数字时区偏移：+0800 / -0500 / +0000 这类。
	if len(s) < 25 || (s[20] != '+' && s[20] != '-') {
		t.Fatalf("serverTimeDisplay 第 21 位应为时区偏移符号：%q", s)
	}
	// UTC 部分应为时:分:秒。
	i := strings.Index(s, "UTC ")
	if i < 0 {
		t.Fatalf("serverTimeDisplay 应含 'UTC '：%q", s)
	}
	utcPart := strings.TrimSuffix(s[i+4:], "）")
	if _, err := time.Parse("15:04:05", utcPart); err != nil {
		t.Fatalf("serverTimeDisplay 的 UTC 部分应为时:分:秒：%q", s)
	}
}
