package main

// 客户端账号密码登录（v1.3.45，契约见客户端仓 docs/api/client-login.md）：
// 面板在 9870-9880 上另起 HTTPS 监听（只挂 /api/v1/client/*），
// POST /api/v1/client/login 用管理后台同一套账号校验，成功返回会话令牌
// sess_+64 位十六进制（只存 SHA-256 哈希）与该账号的通用订阅链接。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// clientLoginPortMin/Max 客户端登录监听的端口段（契约 §2）。
	clientLoginPortMin = 9870
	clientLoginPortMax = 9880
	// clientLoginPortDefault 默认登录端口（客户端先试它）。
	clientLoginPortDefault = 9876
	// clientSessionTTL 客户端会话令牌有效期（30 天，写死常量；过期清理
	// 不起定时任务，校验时惰性判断，见 clientSessionStore.validate）。
	clientSessionTTL = 30 * 24 * time.Hour
)

// ---------- 客户端会话令牌 ----------

type clientSession struct {
	username  string
	expiresAt time.Time
}

// clientSessionStore 客户端会话令牌表：key 是令牌的 SHA-256（hex），
// 绝不存令牌明文；只在内存里（与管理员会话的 sessionStore 同款做法，
// 面板重启后会话失效，客户端重新登录即可）。
type clientSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*clientSession
}

func newClientSessionStore() *clientSessionStore {
	return &clientSessionStore{sessions: map[string]*clientSession{}}
}

func hashClientSessionToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// create 生成一个会话令牌并记下（只存哈希），返回令牌明文与过期时间。
// 明文只返回给这一次登录响应，调用方不得再存。
func (st *clientSessionStore) create(username string) (string, time.Time) {
	tok := "sess_" + randomTokenHex(32)
	exp := time.Now().Add(clientSessionTTL)
	st.mu.Lock()
	st.sessions[hashClientSessionToken(tok)] = &clientSession{username: username, expiresAt: exp}
	st.mu.Unlock()
	return tok, exp
}

// validate 校验令牌：哈希查表、过期惰性清理（不起定时任务）。
func (st *clientSessionStore) validate(tok string) (string, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.sessions[hashClientSessionToken(tok)]
	if !ok {
		return "", false
	}
	if time.Now().After(s.expiresAt) {
		delete(st.sessions, hashClientSessionToken(tok))
		return "", false
	}
	return s.username, true
}

// revoke 吊销一个令牌（面板内口子，MVP 不做管理页面、不出 HTTP 接口）。
func (st *clientSessionStore) revoke(tok string) {
	st.mu.Lock()
	delete(st.sessions, hashClientSessionToken(tok))
	st.mu.Unlock()
}

// revokeAllForUser 吊销某账号的全部会话令牌（改密码/封账号时调，面板内口子）。
func (st *clientSessionStore) revokeAllForUser(username string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for h, s := range st.sessions {
		if s.username == username {
			delete(st.sessions, h)
		}
	}
}

// clientAuthUsernameKey 校验通过的客户端账号名在 request context 里的键。
type clientAuthUsernameKey struct{}

// requireClientAuth 校验 Authorization: Bearer <sess_ 令牌>，只放行
// /api/v1/client/* 这一组接口；管理接口仍只认管理员登录会话（requireAuth），
// 会话令牌在任何管理接口上都无效。MVP 只有登录接口（公开端点）用不到它，
// 先留给后续客户端接口复用。
func (s *Server) requireClientAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" || !strings.HasPrefix(tok, "sess_") {
			writeJSON(w, 401, errResp("会话令牌无效"))
			return
		}
		username, ok := s.clientSessions.validate(tok)
		if !ok {
			writeJSON(w, 401, errResp("会话令牌无效"))
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), clientAuthUsernameKey{}, username)))
	}
}

// ---------- 登录接口 ----------

// handleClientLogin POST /api/v1/client/login（公开端点，无须事先认证）。
// 账号与管理后台同一套（cfg.Username + cfg.PasswordHash，PBKDF2）；
// 成功返回会话令牌与通用订阅链接，失败一律 401（账号不存在与密码不对
// 不区分，不给枚举账号的口子）。防刷复用管理登录的同一套逻辑：同一 IP
// 5 次失败锁 5 分钟，锁定期内返回 429 + Retry-After（契约 §3.3）。
func (s *Server) handleClientLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.sessions.loginLocked(ip) {
		w.Header().Set("Retry-After", strconv.Itoa(s.sessions.loginRetryAfter(ip)))
		writeJSON(w, 429, map[string]any{"ok": false, "error": "locked"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if strings.TrimSpace(req.Username) == "" || req.Password == "" {
		writeJSON(w, 400, errResp("账号或密码不能为空"))
		return
	}
	okCreds := req.Username == s.cfg.Username && verifyPassword(req.Password, s.cfg.PasswordHash)
	// 密码对了、二次验证开着时：客户端发不了验证码，直接按失败处理
	// （不给密码单因素绕过二次验证的口子）；响应与密码错误完全一致，
	// 只在审计日志里区分原因。
	if !okCreds || s.cfg.TOTPEnabled {
		justLocked := s.sessions.loginFailed(ip)
		detail := "密码错误"
		if okCreds {
			detail = "二次验证未通过（客户端登录不支持验证码）"
		}
		s.audit(req.Username, "客户端登录", "", "失败", detail)
		if justLocked {
			s.onLoginLock(ip, req.Username)
		}
		writeJSON(w, 401, errResp("账号或密码不对"))
		return
	}
	s.sessions.loginOK(ip)
	tok, exp := s.clientSessions.create(req.Username)
	s.audit(req.Username, "客户端登录", "", "成功", "")
	resp := map[string]any{
		"token":      tok,
		"username":   req.Username,
		"expires_at": exp.Unix(),
	}
	if u := s.clientSubscriptionURL(r); u != "" {
		resp["subscription_url"] = u
	}
	writeJSON(w, 200, resp)
}

// clientSubscriptionURL 登录成功返回的通用订阅完整链接（契约 §3.2）。
// 口径与 subURL 一致（对外域名 + 自定义路径）；没生成过通用订阅、
// 或只有旧链接（1.3.7 之前，无密文）时返回 ""，调用方不给 subscription_url 字段。
// 注意：subURL 用 r.Host 拼链接；登录请求走的是 9870-9880 监听，订阅端点
// 只在主端口上服务，Host 里的端口不能直接用，换成主端口。
func (s *Server) clientSubscriptionURL(r *http.Request) string {
	if s.cfg.SubTokenHash == "" || s.cfg.SubTokenEnc == "" {
		return ""
	}
	tok, err := s.decryptSubTokenNow(s.cfg.SubTokenEnc)
	if err != nil {
		return ""
	}
	if d := strings.TrimSpace(s.cfg.SubPublicDomain); d != "" {
		return "https://" + d + "/" + s.subPath() + "/" + tok
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	scheme := "http"
	if s.cfg.HTTPSEnabled {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(s.cfg.Port)) + "/" + s.subPath() + "/" + tok
}

// ---------- 登录监听 ----------

// clientLoginMux 客户端登录监听的路由：只挂 /api/v1/client/*，其它路径
// 一律 404（管理后台的路由一个都不暴露）。
func (s *Server) clientLoginMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/client/login", s.handleClientLogin)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, errResp("没有这个接口"))
	})
	return mux
}

// startClientLoginListeners 在 9870-9880 上起客户端登录监听（HTTPS，
// 与主端口共用同一套证书）。每个端口独立 bind：被占用就记日志跳过，
// 不影响主端口与其他端口；全部被占用时一个都不起，只记日志。
// 返回成功起来的监听器（测试用来关闭；main 里忽略返回值）。
// 自签证书时监听照常起，客户端证书校验过不了、连不上，这是契约预期行为。
func (s *Server) startClientLoginListeners(certFile, keyFile string) []net.Listener {
	var started []net.Listener
	for port := clientLoginPortMin; port <= clientLoginPortMax; port++ {
		addr := fmt.Sprintf("0.0.0.0:%d", port)
		ln, err := listenWithRetry1345(addr)
		if err != nil {
			log.Printf("客户端登录监听 %s 起不来（跳过）：%v", addr, err)
			continue
		}
		srv := &http.Server{Addr: addr, Handler: s.clientLoginMux(), ReadHeaderTimeout: 10 * time.Second}
		go func(srv *http.Server, ln net.Listener) {
			if err := srv.ServeTLS(ln, certFile, keyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("客户端登录监听 %s 退出：%v", srv.Addr, err)
			}
		}(srv, ln)
		started = append(started, ln)
		log.Printf("客户端登录监听 https://%s（只服务 /api/v1/client/*）", addr)
	}
	return started
}

// listenWithRetry1345 抢绑端口：重启瞬间旧进程还没退出时会撞
// "address already in use"，重试 10 次（每 0.5 秒），与主端口的
// serveWithRetry 同款逻辑；不是端口占用直接返回错误。
func listenWithRetry1345(addr string) (net.Listener, error) {
	var ln net.Listener
	var err error
	for i := 0; i < 10; i++ {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
		if !isAddrInUse(err) {
			return nil, err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, err
}
