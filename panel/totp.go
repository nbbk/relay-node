package main

// 二次验证 TOTP（v1.3.8，可选，默认关）与登录告警。
// TOTP 只用 Go 标准库实现（RFC 6238：HMAC-SHA1 + 30 秒时间步 + 6 位验证码），
// 不引新依赖。密钥是 base32 明文，只存 600 权限的 config.json；备用码只存
// SHA-256 哈希，用一次废一个。旧 config.json 里没有这些字段时一律按未开启
// 处理，不用迁移。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	totpDigits      = 6  // 验证码位数
	totpStepSec     = 30 // 时间步（秒）
	totpSkew        = 1  // 容差：前后各一个时间步（时钟偏差）
	totpBackupCount = 8  // 备用码个数
	totpBackupLen   = 10 // 备用码长度
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// generateTOTPSecret 生成 20 字节随机密钥的 base32（无填充）表示。
func generateTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// totpCodeAt 按给定时间算 6 位验证码。
func totpCodeAt(secret string, t time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		// 兼容带填充的 base32（有的验证器导出时带 =）
		key, err = base32.StdEncoding.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
		if err != nil {
			return "", err
		}
	}
	counter := uint64(t.Unix() / totpStepSec)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[offset]&0x7f) << 24) | (uint32(sum[offset+1]) << 16) | (uint32(sum[offset+2]) << 8) | uint32(sum[offset+3])
	return fmt.Sprintf("%06d", code%1000000), nil
}

// verifyTOTP 校验验证码：6 位数字，允许前后一个时间步的容差。
func verifyTOTP(secret, code string, now time.Time) bool {
	code = strings.Join(strings.Fields(code), "") // 验证器常显示成 123 456，空格去掉
	if len(code) != totpDigits {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	for _, skew := range []int64{-1, 0, 1} {
		want, err := totpCodeAt(secret, now.Add(time.Duration(skew*totpStepSec)*time.Second))
		if err != nil {
			return false
		}
		if hmac.Equal([]byte(want), []byte(code)) {
			return true
		}
	}
	return false
}

// totpIssuerURL 生成验证器扫码用的 otpauth:// 链接。
func totpIssuerURL(secret, account, issuer string) string {
	label := url.QueryEscape(issuer) + ":" + url.QueryEscape(account)
	return "otpauth://totp/" + label + "?secret=" + secret + "&issuer=" + url.QueryEscape(issuer) + "&algorithm=SHA1&digits=6&period=30"
}

// generateBackupCodes 生成 8 个备用码：明文只返回一次（确认开启时显示），
// 存的是 SHA-256 哈希。字母表与随机强密码同一套（无歧义字符）。
func generateBackupCodes() (codes []string, hashes []string) {
	for i := 0; i < totpBackupCount; i++ {
		c := randomPassword(totpBackupLen)
		codes = append(codes, c)
		hashes = append(hashes, sha256Hex(c))
	}
	return codes, hashes
}

// verifyBackupCode 按哈希比对备用码，返回它在列表里的下标（用一次废一个）。
func verifyBackupCode(code string, hashes []string) (int, bool) {
	code = strings.Join(strings.Fields(code), "")
	if code == "" {
		return -1, false
	}
	want := sha256Hex(code)
	for i, h := range hashes {
		if hmac.Equal([]byte(h), []byte(want)) {
			return i, true
		}
	}
	return -1, false
}

// verifySecondFactor 校验第二因素：先试 6 位 TOTP 验证码（前后一个时间步容差），
// 再试备用码；备用码对上就从列表里删掉（用一次废一个）并保存配置。
// 密码已经验过的登录/关闭流程才能调它。
func (s *Server) verifySecondFactor(code string) bool {
	if s.cfg.TOTPSecret != "" && verifyTOTP(s.cfg.TOTPSecret, code, time.Now()) {
		return true
	}
	if idx, ok := verifyBackupCode(code, s.cfg.TOTPBackupCodes); ok {
		old := s.cfg.TOTPBackupCodes
		s.cfg.TOTPBackupCodes = append(append([]string{}, old[:idx]...), old[idx+1:]...)
		if err := s.saveConfig(); err != nil {
			s.cfg.TOTPBackupCodes = old
			return false
		}
		return true
	}
	return false
}

// ---------- 登录告警 ----------

// telegramReady 登录告警能不能发：Telegram 开关开着且 Token 与 Chat ID 都填了。
func (s *Server) telegramReady() bool {
	return s.cfg.Telegram.Enabled && s.cfg.Telegram.BotToken != "" && s.cfg.Telegram.ChatID != ""
}

// noteLoginIP 登记一个成功登录的 IP，返回它是不是第一次出现。
// 无论登录告警开关开没开都登记，免得以后开了开关对老 IP 狂发提醒；
// 只在新增时才写 state.json（成功登录才调，频率低）。
func (s *Server) noteLoginIP(ip string) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.state.LoginSeenIPs == nil {
		s.state.LoginSeenIPs = map[string]bool{}
	}
	if s.state.LoginSeenIPs[ip] {
		return false
	}
	s.state.LoginSeenIPs[ip] = true
	_ = s.saveStateLocked()
	return true
}

// maybeLoginAlert 成功登录后：新 IP 且登录告警开着就发一条 Telegram 提醒。
// IP 无论如何都登记（见 noteLoginIP），提醒只对新 IP 发一次。
func (s *Server) maybeLoginAlert(ip, username string) {
	isNew := s.noteLoginIP(ip)
	if !isNew || !s.cfg.LoginAlert {
		return
	}
	s.sendTelegram("登录告警：新 IP " + ip + " 成功登录面板（账号 " + username + "，" + time.Now().Format("2006-01-02 15:04:05") + "）。如果不是你本人，请立即改密码并检查 IP 白名单。")
}

// onLoginLock 一次登录失败刚好触发锁定时（第 5 次）：记审计并发 Telegram 提醒
// （开关开着才发，没配 Telegram 时 sendTelegram 本来就是空操作）。
func (s *Server) onLoginLock(ip, username string) {
	s.audit(username, "登录锁定", ip, "失败", "连续 5 次登录失败，已临时锁定 5 分钟")
	if !s.cfg.LoginAlert {
		return
	}
	s.sendTelegram("登录告警：IP " + ip + " 连续 5 次登录失败，面板已临时锁定 5 分钟（" + time.Now().Format("2006-01-02 15:04:05") + "）。如果不是你本人，请检查 IP 白名单。")
}

// ---------- 登录告警开关 ----------

// handleLoginAlert 开/关登录告警（v1.3.8，可选，默认关）：只改 config.json
// 的 login_alert 开关。没配 Telegram 时前端把开关置灰；后端照样保存
// （sendTelegram 没配好时本来就是空操作，不会报错）。
func (s *Server) handleLoginAlert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	old := s.cfg.LoginAlert
	s.cfg.LoginAlert = req.Enabled
	if err := s.saveConfig(); err != nil {
		s.cfg.LoginAlert = old
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	state := "关"
	if req.Enabled {
		state = "开"
	}
	s.audit(s.currentUser(r), "登录告警开关", state, "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"login_alert": s.cfg.LoginAlert}))
}

// ---------- 二次验证 ----------

// handleTOTPSetup 生成二次验证密钥（v1.3.8）：
// 密钥存进 config.json（此时还不算开启），返回密钥、otpauth 链接与二维码，
// 前端展示后让用户输一次验证码确认。已开启时先关掉再重新设置。
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	if s.cfg.TOTPEnabled {
		writeJSON(w, 400, errResp("二次验证已开启，先关闭再重新设置"))
		return
	}
	secret, err := generateTOTPSecret()
	if err != nil {
		writeJSON(w, 500, errResp("生成密钥失败"))
		return
	}
	oldSecret := s.cfg.TOTPSecret
	s.cfg.TOTPSecret = secret
	if err := s.saveConfig(); err != nil {
		s.cfg.TOTPSecret = oldSecret
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	u := totpIssuerURL(secret, s.cfg.Username, s.panelName())
	qr, err := qrDataURI(u)
	if err != nil {
		writeJSON(w, 500, errResp("生成二维码失败"))
		return
	}
	s.audit(s.currentUser(r), "二次验证", "生成密钥", "成功", "还未确认开启")
	writeJSON(w, 200, okResp(map[string]any{"secret": secret, "otpauth_url": u, "qr": qr}))
}

// handleTOTPConfirm 确认开启二次验证：输一次验证器里的 6 位验证码，
// 对上才算开启成功，同时生成 8 个备用码（明文只在这里显示一次）。
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if s.cfg.TOTPEnabled {
		writeJSON(w, 400, errResp("二次验证已开启"))
		return
	}
	if s.cfg.TOTPSecret == "" {
		writeJSON(w, 400, errResp("先点“开启二次验证”生成密钥，用验证器扫码再确认"))
		return
	}
	if !verifyTOTP(s.cfg.TOTPSecret, req.Code, time.Now()) {
		writeJSON(w, 401, errResp("验证码不对，请核对验证器时间后重试"))
		return
	}
	codes, hashes := generateBackupCodes()
	oldEnabled, oldCodes := s.cfg.TOTPEnabled, s.cfg.TOTPBackupCodes
	s.cfg.TOTPEnabled = true
	s.cfg.TOTPBackupCodes = hashes
	if err := s.saveConfig(); err != nil {
		s.cfg.TOTPEnabled, s.cfg.TOTPBackupCodes = oldEnabled, oldCodes
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	s.audit(s.currentUser(r), "二次验证", "开启", "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"backup_codes": codes}))
}

// handleTOTPDisable 关闭二次验证：须同时验证当前密码和验证码
// （验证器里的 6 位数字，或一个没用过的备用码）。
// 验证器丢了、备用码也没了时走 SSH 命令 panel-totp-reset，不要在这里硬关。
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if !s.cfg.TOTPEnabled {
		writeJSON(w, 400, errResp("二次验证未开启"))
		return
	}
	if !verifyPassword(req.Password, s.cfg.PasswordHash) {
		writeJSON(w, 401, errResp("密码不对"))
		return
	}
	if !s.verifySecondFactor(req.Code) {
		writeJSON(w, 401, errResp("验证码不对"))
		return
	}
	oldEnabled, oldSecret, oldCodes := s.cfg.TOTPEnabled, s.cfg.TOTPSecret, s.cfg.TOTPBackupCodes
	s.cfg.TOTPEnabled = false
	s.cfg.TOTPSecret = ""
	s.cfg.TOTPBackupCodes = nil
	if err := s.saveConfig(); err != nil {
		s.cfg.TOTPEnabled, s.cfg.TOTPSecret, s.cfg.TOTPBackupCodes = oldEnabled, oldSecret, oldCodes
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	s.audit(s.currentUser(r), "二次验证", "关闭", "成功", "")
	writeJSON(w, 200, okResp(nil))
}
