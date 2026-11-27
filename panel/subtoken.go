package main

// ---------- 订阅令牌加密存储与 /sub/ 限流（v1.3.7） ----------
// 背景：以前订阅令牌只存 SHA-256 哈希，明文只在生成/轮换时显示一次；
// 弹窗关掉就再也找不回来，没复制到就等于丢了，只能再轮换一次。
// 现在生成/轮换时除了存哈希，再存一份 AES-GCM 密文，密钥放面板本机
// PANEL_HOME/sub_token.key（600 权限，首次使用自动生成 32 字节随机数）。
// 哈希继续做鉴权查表，密文只用来重新拼链接（会话认证接口取回）。
// 密钥文件丢失/损坏时之前存的密文解不开，复制时按旧链接提示轮换。
// 密钥绝不进日志、审计、导出、备份。

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const subTokenKeyFile = "sub_token.key"

// subTokenKey 读或创建订阅令牌加密密钥：文件不存在就生成 32 字节随机数
// 写盘（600 权限）；文件损坏（长度不对）时重新生成，旧密文随之失效，
// 复制时按旧链接提示轮换，不静默编一个链接出来。
func (s *Server) subTokenKey() ([]byte, error) {
	s.subKeyMu.Lock()
	defer s.subKeyMu.Unlock()
	if s.subKey != nil {
		return s.subKey, nil
	}
	p := filepath.Join(s.env.PanelHome, subTokenKeyFile)
	if b, err := os.ReadFile(p); err == nil {
		if len(b) == 32 {
			s.subKey = append([]byte(nil), b...)
			return s.subKey, nil
		}
		// 文件损坏：重新生成（旧密文失效，走旧链接提示）。
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(p, k, 0o600); err != nil {
		return nil, err
	}
	s.subKey = k
	return k, nil
}

// encryptSubToken AES-GCM 加密订阅令牌：输出 base64(nonce‖密文)。
func encryptSubToken(key []byte, tok string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := g.Seal(nonce, nonce, []byte(tok), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// decryptSubToken 解密订阅令牌密文：格式不对、密钥不对、密文被改都返回错误，
// 调用方按旧链接处理（提示轮换），不返回半截令牌。
func decryptSubToken(key []byte, enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(enc))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < g.NonceSize() {
		return "", errors.New("密文太短")
	}
	nonce, ct := raw[:g.NonceSize()], raw[g.NonceSize():]
	pt, err := g.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// encryptSubTokenNow 生成一条令牌的密文（加密失败时返回错误，调用方直接
// 500，不生成没有密文的令牌——否则复制按钮点开又是旧链接提示）。
func (s *Server) encryptSubTokenNow(tok string) (string, error) {
	key, err := s.subTokenKey()
	if err != nil {
		return "", err
	}
	return encryptSubToken(key, tok)
}

// decryptSubTokenNow 解一条令牌的密文（密钥丢了/密文损坏时返回错误，
// 调用方按旧链接处理）。
func (s *Server) decryptSubTokenNow(enc string) (string, error) {
	key, err := s.subTokenKey()
	if err != nil {
		return "", err
	}
	return decryptSubToken(key, enc)
}

// tokenPairMatches 哈希与密文是不是一对：解密密文后验哈希。任一为空、
// 解不开、哈希对不上都返回 false（v1.4.28，备份恢复成对取用时校验混配）。
func (s *Server) tokenPairMatches(hash, enc string) bool {
	if hash == "" || enc == "" {
		return false
	}
	tok, err := s.decryptSubTokenNow(enc)
	if err != nil {
		return false
	}
	return verifySubToken(tok, hash)
}

// ---------- /sub/ 限流（v1.3.7） ----------
// 令牌本身是 sub_+64 位十六进制（256 位随机），扫是扫不出来的；这套是
// 防机器 hammer 和留证据：同一 IP 10 次失败锁 10 分钟，返回 429。
// 对的令牌永远放行（先验令牌再看锁），正常客户端拿对的令牌不会触发；
// 换 IP 不受影响。失败只记 IP 与时间，绝不记令牌本身。

const (
	subFailLimit   = 10
	subFailLockDur = 10 * time.Minute
)

type subFailState struct {
	failures int
	lockedAt time.Time
}

// validSubTokenFormat 令牌格式：sub_ + 64 位小写十六进制。格式不对直接 404，
// 不查库（省一次哈希比较，也让扫库的人拿不到任何信息）。
func validSubTokenFormat(tok string) bool {
	if len(tok) != 68 || !strings.HasPrefix(tok, subTokenPrefix) {
		return false
	}
	for _, c := range tok[4:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// 独立合并订阅令牌前缀（v1.4.34，第 4 批）：mrg_ + 64 位十六进制，
// 总长仍 68 字符；与 sub_ 前缀一眼分清，令牌本身就能认出种类。
const indMergedTokenPrefix = "mrg_"

// generateIndMergedToken 生成一条独立合并订阅令牌。
func generateIndMergedToken() string {
	return indMergedTokenPrefix + randomTokenHex(32)
}

// validIndependentMergedTokenFormat 独立合并订阅令牌格式：
// mrg_ + 64 位小写十六进制。
func validIndependentMergedTokenFormat(tok string) bool {
	if len(tok) != 68 || !strings.HasPrefix(tok, indMergedTokenPrefix) {
		return false
	}
	for _, c := range tok[4:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// validAnySubTokenFormat 任一种订阅令牌格式（sub_ 或 mrg_）。
func validAnySubTokenFormat(tok string) bool {
	return validSubTokenFormat(tok) || validIndependentMergedTokenFormat(tok)
}

// subTokenValid 令牌是否有效（格式 + 三处存储任一命中）：限流前先验，
// 对的令牌永远放行，不受锁影响。
func (s *Server) subTokenValid(tok string) bool {
	if !validAnySubTokenFormat(tok) {
		return false
	}
	if s.cfg.SubTokenHash != "" && verifySubToken(tok, s.cfg.SubTokenHash) {
		return true
	}
	if _, ok := s.findSubUser(tok); ok {
		return true
	}
	if _, ok := s.findMergedSub(tok); ok {
		return true
	}
	if _, ok := s.findIndependentMergedSub(tok); ok {
		return true
	}
	return false
}

// subLocked 同一 IP 10 次失败锁 10 分钟（照搬登录锁的路子）。
func (s *Server) subLocked(ip string) bool {
	s.subFailMu.Lock()
	defer s.subFailMu.Unlock()
	a, ok := s.subFails[ip]
	if !ok {
		return false
	}
	if a.failures >= subFailLimit && time.Since(a.lockedAt) < subFailLockDur {
		return true
	}
	if time.Since(a.lockedAt) >= subFailLockDur {
		a.failures = 0
	}
	return false
}

func (s *Server) subFailed(ip string) {
	s.subFailMu.Lock()
	defer s.subFailMu.Unlock()
	a, ok := s.subFails[ip]
	if !ok {
		a = &subFailState{}
		if s.subFails == nil {
			s.subFails = map[string]*subFailState{}
		}
		s.subFails[ip] = a
	}
	a.failures++
	a.lockedAt = time.Now()
	// 顺手清理过期条目，防 map 无限长大。
	if len(s.subFails) > 1000 {
		for k, v := range s.subFails {
			if v.failures < subFailLimit && time.Since(v.lockedAt) >= subFailLockDur {
				delete(s.subFails, k)
			}
		}
	}
}

func (s *Server) subOK(ip string) {
	s.subFailMu.Lock()
	defer s.subFailMu.Unlock()
	delete(s.subFails, ip)
}
