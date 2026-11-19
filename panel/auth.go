package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const pbkdf2Iterations = 100000

// pbkdf2SHA256 手写 PBKDF2-HMAC-SHA256（只用标准库）。
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	var dk []byte
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(block))
		prf.Write(b[:])
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

// hashPassword 生成 config.json 里的哈希串：pbkdf2-sha256$迭代$盐$哈希。
func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk := pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, 32)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations,
		base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(dk)), nil
}

// verifyPassword 校验明文密码与哈希串；格式不对返回 false。
func verifyPassword(password, hash string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1000 || iter > 1000000 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iter, len(want))
	return hmac.Equal(got, want)
}

func randomTokenHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// 极小概率回退：时间戳混入（仍不可预测性较差，仅兜底）
		binary.BigEndian.PutUint64(b, uint64(time.Now().UnixNano()))
	}
	return fmt.Sprintf("%x", b)
}

func randomPassword(n int) string {
	const chars = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "admin-" + randomTokenHex(6)
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

type session struct {
	username  string
	expiresAt time.Time
	lastSeen  time.Time
}

type loginAttempt struct {
	failures int
	lockedAt time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*session
	attempts map[string]*loginAttempt
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]*session{}, attempts: map[string]*loginAttempt{}}
}

func (st *sessionStore) create(username string) string {
	tok := randomTokenHex(32)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.sessions[tok] = &session{username: username, expiresAt: time.Now().Add(24 * time.Hour), lastSeen: time.Now()}
	return tok
}

func (st *sessionStore) get(r *http.Request) (string, bool) {
	c, err := r.Cookie("relay_panel_session")
	if err != nil || c.Value == "" {
		return "", false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.sessions[c.Value]
	if !ok {
		return "", false
	}
	now := time.Now()
	if now.After(s.expiresAt) || now.Sub(s.lastSeen) > 2*time.Hour {
		delete(st.sessions, c.Value)
		return "", false
	}
	s.lastSeen = now
	return s.username, true
}

func (st *sessionStore) destroy(r *http.Request) {
	c, err := r.Cookie("relay_panel_session")
	if err != nil {
		return
	}
	st.mu.Lock()
	delete(st.sessions, c.Value)
	st.mu.Unlock()
}

// loginLocked 同一 IP 5 次失败锁 5 分钟。
func (st *sessionStore) loginLocked(ip string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	a, ok := st.attempts[ip]
	if !ok {
		return false
	}
	if a.failures >= 5 && time.Since(a.lockedAt) < 5*time.Minute {
		return true
	}
	if time.Since(a.lockedAt) >= 5*time.Minute {
		a.failures = 0
	}
	return false
}

// loginFailed 记一次失败；返回这次失败是不是刚好触发锁定（第 5 次）。
// 触发锁定只会发生一次：锁定期内 handleLogin 直接返回 429，不再记失败；
// 锁定过期后 loginLocked 会先把计数清零，重新从 0 开始数。
func (st *sessionStore) loginFailed(ip string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	a, ok := st.attempts[ip]
	if !ok {
		a = &loginAttempt{}
		st.attempts[ip] = a
	}
	a.failures++
	a.lockedAt = time.Now()
	return a.failures == 5
}

func (st *sessionStore) loginOK(ip string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.attempts, ip)
}

func clientIP(r *http.Request) string {
	// 只取直连对端 IP（面板不经过可信代理，不信任 X-Forwarded-For）。
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}
