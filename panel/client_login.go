package main

// 客户端账号密码登录（v1.3.45，契约见客户端仓 docs/api/client-login.md）：
// 面板在 9870-9880 上另起 HTTPS 监听（只挂 /api/v1/client/*），
// POST /api/v1/client/login 用管理后台同一套账号校验，成功返回会话令牌
// sess_+64 位十六进制（只存 SHA-256 哈希）与该账号的订阅链接。
// v1.4.27（方案 B）：每人登录账号——先验管理员账号（TOTP 只拦管理员），
// 再按 Username 验订阅用户（跳过 TOTP，密码是唯一因素）；登录回包除
// subscription_url 外还带 account_type、plan_name、plan（套餐对象，
// 含 traffic_used）与 nodes（该用户有权限的全部节点，见 loginNodesForUser）。
// 管理员登录兜底取第一份已启用的 SubUser 的订阅与节点，plan 为 null。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
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
	// subUserID 该会话属于哪个订阅用户（v1.4.27）：空 = 管理员会话；
	// 非空 = SubUser 的 ID，改密码/删用户时按它吊销（见 revokeBySubUserID）。
	subUserID string
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
// subUserID 可选（v1.4.27）：SubUser 登录时传该用户的 ID，管理员登录
// 时不传（空 = 管理员会话）。
func (st *clientSessionStore) create(username string, subUserID ...string) (string, time.Time) {
	tok := "sess_" + randomTokenHex(32)
	exp := time.Now().Add(clientSessionTTL)
	sid := ""
	if len(subUserID) > 0 {
		sid = subUserID[0]
	}
	st.mu.Lock()
	st.sessions[hashClientSessionToken(tok)] = &clientSession{username: username, expiresAt: exp, subUserID: sid}
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

// revokeBySubUserID 吊销某订阅用户的全部客户端会话（v1.4.27：改密码/
// 删用户时调，按会话上的 subUserID 匹配，与用户名无关——用户名改了
// 旧会话照样吊销）。
func (st *clientSessionStore) revokeBySubUserID(subUserID string) {
	if subUserID == "" {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for h, s := range st.sessions {
		if s.subUserID == subUserID {
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
// v1.4.27（方案 B）：先按现有逻辑验管理员账号（TOTP 检查只对管理员
// 生效，保持现有行为）；管理员不对时，按 Username 找 SubUser，
// verifyPassword 验密码。SubUser 登录跳过 TOTP（他们没有 TOTP 密钥，
// 密码是唯一因素，与 XBoard 同款）。SubUser 被停用（Disabled）或已过期
// （ExpiresAt 已过）时登录 401，与密码错误同一响应，不区分原因。
// 成功返回会话令牌、订阅链接（SubUser 是他自己的，管理员是第一份已启用
// 的 SubUser 的兜底）、account_type、plan_name、plan 与 nodes。
// 失败一律 401（账号不存在与密码不对不区分，不给枚举账号的口子）。防刷
// 复用管理登录的同一套逻辑：同一 IP 5 次失败锁 5 分钟，锁定期内返回
// 429 + Retry-After（契约 §3.3）。
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
	// 管理员账号：密码对了、二次验证开着时，客户端发不了验证码，
	// 直接按失败处理（不给密码单因素绕过二次验证的口子）；响应与
	// 密码错误完全一致，只在审计日志里区分原因。
	adminOK := req.Username == s.cfg.Username && verifyPassword(req.Password, s.cfg.PasswordHash)
	if adminOK && !s.cfg.TOTPEnabled {
		s.sessions.loginOK(ip)
		tok, exp := s.clientSessions.create(req.Username)
		s.audit(req.Username, "客户端登录", "", "成功", "")
		resp := map[string]any{
			"token":        tok,
			"username":     req.Username,
			"expires_at":   exp.Unix(),
			"account_type": "admin",
			"plan_name":    "",
			"plan":         nil,
		}
		if u := s.firstEnabledSubUser(); u != nil {
			if url := s.clientSubscriptionURLForUser(r, u); url != "" {
				resp["subscription_url"] = url
			}
			resp["nodes"] = s.loginNodesForUser(u)
		} else {
			resp["nodes"] = []any{}
		}
		writeJSON(w, 200, resp)
		return
	}
	// 订阅用户账号：按 Username 找人，密码是唯一因素（跳过 TOTP）；
	// 停用/过期与密码错误同一响应，不区分原因。
	subUserOK := false
	var subUser *SubUser
	if !adminOK {
		if u := s.findSubUserByUsername(req.Username); u != nil {
			subUser = u
			if u.PasswordHash != "" && verifyPassword(req.Password, u.PasswordHash) && !u.Disabled && !subExpired(u.ExpiresAt, time.Now()) {
				subUserOK = true
			}
		}
	}
	if subUserOK {
		s.sessions.loginOK(ip)
		tok, exp := s.clientSessions.create(subUser.Username, subUser.ID)
		s.audit(subUser.Username, "客户端登录", "", "成功", "订阅用户")
		resp := map[string]any{
			"token":        tok,
			"username":     subUser.Username,
			"expires_at":   exp.Unix(),
			"account_type": "user",
			"plan_name":    s.planNameOf(subUser.PlanID),
			"plan":         s.loginPlanForUser(subUser),
			"nodes":        s.loginNodesForUser(subUser),
		}
		if url := s.clientSubscriptionURLForUser(r, subUser); url != "" {
			resp["subscription_url"] = url
		}
		writeJSON(w, 200, resp)
		return
	}
	justLocked := s.sessions.loginFailed(ip)
	// v1.4.28：失败 detail 按真实原因记，别一律写“密码错误”——停用/过期
	// 的 SubUser 用正确密码登录失败时，审计也要能分清。响应仍是统一 401，
	// 不向调用方区分原因。
	detail := "密码错误"
	if adminOK {
		detail = "二次验证未通过（客户端登录不支持验证码）"
	} else if subUser != nil && subUser.PasswordHash != "" && verifyPassword(req.Password, subUser.PasswordHash) {
		if subUser.Disabled {
			detail = "账号已停用"
		} else if subExpired(subUser.ExpiresAt, time.Now()) {
			detail = "订阅已过期"
		}
	}
	s.audit(req.Username, "客户端登录", "", "失败", detail)
	if justLocked {
		s.onLoginLock(ip, req.Username)
	}
	writeJSON(w, 401, errResp("账号或密码不对"))
}

// firstEnabledSubUser 第一份已启用且未过期的订阅用户（cfg.SubUsers 的顺序，
// 即创建顺序）：管理员登录回包的订阅与节点兜底都取他。停用或已过期
// （ExpiresAt 已过，口径与 SubUser 登录时停用/过期 401 一致）的都跳过。
// 没有返回 nil。
func (s *Server) firstEnabledSubUser() *SubUser {
	now := time.Now()
	for i := range s.cfg.SubUsers {
		if !s.cfg.SubUsers[i].Disabled && !subExpired(s.cfg.SubUsers[i].ExpiresAt, now) {
			return &s.cfg.SubUsers[i]
		}
	}
	return nil
}

// clientSubscriptionURLForUser 某订阅用户自己的订阅完整链接（解密他
// 的 TokenEnc 后拼链接；没链接/解不开返回 ""，调用方不给
// subscription_url 字段）。
func (s *Server) clientSubscriptionURLForUser(r *http.Request, u *SubUser) string {
	if u == nil || strings.TrimSpace(u.TokenEnc) == "" {
		return ""
	}
	tok, err := s.decryptSubTokenNow(u.TokenEnc)
	if err != nil {
		return ""
	}
	return s.clientSubscriptionLink(r, tok)
}

// clientSubscriptionURL 登录成功返回的订阅完整链接（契约 §3.2）。
// v1.4.27：管理员登录兜底取第一份已启用的 SubUser 的订阅（见
// firstEnabledSubUser）；没有 SubUser 时返回 ""，调用方不给
// subscription_url 字段。通用订阅与合并订阅已退场，不再从这里取。
func (s *Server) clientSubscriptionURL(r *http.Request) string {
	if u := s.firstEnabledSubUser(); u != nil {
		return s.clientSubscriptionURLForUser(r, u)
	}
	return ""
}

// loginPlanForUser 登录回包里的 plan 对象（v1.4.27）：取该用户生效的
// 值，不是档位模板值——id/name 是档位（没挂档位时 id 与 name 为空），
// group_id/group_name 是该用户生效的分组，流量上限/模式/重置与到期
// 取该用户自己的字段；traffic_used 是已用字节数（与订阅用户列表里
// 的 traffic_used 同款算法：userUsageSummary 按 limit_mode 取本期
// 或累计；不限流时为 0）。
func (s *Server) loginPlanForUser(u *SubUser) map[string]any {
	if u == nil {
		return nil
	}
	planID, planName := "", ""
	if p := s.findPlan(u.PlanID); u.PlanID != "" && p != nil {
		planID, planName = p.ID, p.Name
	}
	used, _, _ := s.userUsageSummary(userSubKey("subuser", u.ID), u.TrafficLimitBytes, u.TrafficLimitMode)
	return map[string]any{
		"id":                 planID,
		"name":               planName,
		"group_id":           u.GroupID,
		"group_name":         s.groupNameOf(u.GroupID),
		"traffic_limit_mb":   u.TrafficLimitBytes / 1024 / 1024,
		"traffic_limit_mode": u.TrafficLimitMode,
		"reset_mode":         u.ResetMode,
		"traffic_used":       used,
		"expires_at":         u.ExpiresAt,
	}
}

// loginNodesForUser 登录回包里的 nodes 数组（v1.4.27）：该用户有权限的
// 全部节点，按档位权限组算出的节点范围，分两类：
//   - backend（后端节点）：遍历节点列表，过 nodeHasUser（节点侧门）＋
//     backendAssignAllows（用户侧四级门）两道门才进；
//   - landing（中转落地）：取该用户生效的勾选 effectiveSelectionForSubUser，
//     空勾选 = 全部本地落地；本地落地取 relay.conf 里的名字与地址，
//     远程落地不调远程面板，name/address 取不到时填编号原文。
//
// 顺序：后端节点在前（按节点 id），中转落地在后（按勾选顺序）。
func (s *Server) loginNodesForUser(u *SubUser) []map[string]any {
	out := []map[string]any{}
	if u == nil {
		return out
	}
	key := userSubKey("subuser", u.ID)
	s.nodes.mu.Lock()
	nodes := make([]*BackendNode, 0, len(s.nodes.data.Nodes))
	for _, n := range s.nodes.data.Nodes {
		nodes = append(nodes, n)
	}
	s.nodes.mu.Unlock()
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	for _, n := range nodes {
		if !nodeHasUser(n, key) {
			continue
		}
		if !s.backendAssignAllows(u.BackendScope, u.BackendTargets, u.GroupID, n) {
			continue
		}
		out = append(out, map[string]any{"kind": "backend", "id": n.ID, "name": n.Name, "address": n.Address})
	}
	sel := s.effectiveSelectionForSubUser(u)
	if len(sel) == 0 {
		if rc, _, err := s.readRelayConf(); err == nil && rc != nil {
			for _, l := range rc.Landings {
				out = append(out, map[string]any{"kind": "landing", "id": fmt.Sprintf("local:%d", l.Index), "name": l.displayName(), "address": l.Landing})
			}
		}
		return out
	}
	// 非空勾选：本地落地按 relay.conf 取名/地址，远程落地不调远程，
	// 取不到时 name/address 填编号原文。
	var localByIndex map[int]Landing
	if rc, _, err := s.readRelayConf(); err == nil && rc != nil {
		localByIndex = map[int]Landing{}
		for _, l := range rc.Landings {
			localByIndex[l.Index] = l
		}
	}
	for _, ss := range sel {
		if ss.Panel == "local" {
			id := fmt.Sprintf("local:%d", ss.Landing)
			if l, ok := localByIndex[ss.Landing]; ok {
				out = append(out, map[string]any{"kind": "landing", "id": id, "name": l.displayName(), "address": l.Landing})
			} else {
				out = append(out, map[string]any{"kind": "landing", "id": id, "name": id, "address": id})
			}
			continue
		}
		id := fmt.Sprintf("%s:%d", ss.Panel, ss.Landing)
		out = append(out, map[string]any{"kind": "landing", "id": id, "name": id, "address": id})
	}
	return out
}

// clientSubscriptionLink 按令牌拼订阅完整链接：口径与 subURL 一致（对外
// 域名 + 自定义路径）；没填对外域名时用请求 Host 但把端口换成主端口。
// 注意：subURL 用 r.Host 拼链接；登录请求走的是 9870-9880 监听，订阅端点
// 只在主端口上服务，Host 里的端口不能直接用，换成主端口。
func (s *Server) clientSubscriptionLink(r *http.Request, tok string) string {
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
