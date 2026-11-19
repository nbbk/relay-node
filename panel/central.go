package main

// ---------- 多台集中管理（v1.2.0） ----------
// 设计见方案文档 §4.13。角色不是安装时定的，是用法定的：
// 登记了别的面板的就是主面板，生成了远程管理令牌的就是被管面板，
// 一台面板可以同时是两者。主面板只定期拉取远程状态、代理操作，
// 没有中心服务器；被管面板挂了，主面板照常用，远程那张卡片变离线而已。
//
// 安全要点：
//   - 被管面板只存令牌的 SHA-256 哈希，绝不存明文、绝不回显明文；
//     生成时明文只显示一次。校验用常数时间比较。
//   - 主面板必须拿着令牌原文去调远程接口，所以主面板 config.json 里
//     存的是令牌明文（remotes[].token），与 Telegram Token、Cloudflare
//     令牌同等待遇：文件 600 权限，绝不进审计日志、绝不通过接口返回。
//   - 令牌只放行白名单里的 7 个 central/* 接口（status / links /
//     订阅内容 / 加落地 / 删落地 / 给落地加节点 / 服务操作），改密码、改端口、改设置等接口令牌调不通。
//   - 代理操作时主面板服务端先按本地同样的规则二次校验参数，
//     用户输入绝不直接拼进 shell，也不原样转发给远程而不校验。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---------- 令牌 ----------

const remoteTokenPrefix = "rpt_"

// generateRemoteToken 生成远程管理令牌：rpt_ + 64 位随机十六进制（32 字节随机数）。
func generateRemoteToken() string {
	return remoteTokenPrefix + randomTokenHex(32)
}

// hashRemoteToken 令牌的 SHA-256 哈希（十六进制），这是被管面板唯一存的东西。
func hashRemoteToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// verifyRemoteToken 校验令牌：同样算 SHA-256，再常数时间比较，防时序攻击。
func verifyRemoteToken(tok, hash string) bool {
	if tok == "" || hash == "" {
		return false
	}
	sum := sha256.Sum256([]byte(tok))
	want, err := hex.DecodeString(hash)
	if err != nil || len(want) != len(sum) {
		return false
	}
	return subtle.ConstantTimeCompare(sum[:], want) == 1
}

// ---------- 配置里的远程面板登记 ----------

// RemotePanel 主面板登记的一台远程面板。Token 是明文（必须拿原文去调远程接口），
// 与 Telegram Token、Cloudflare 令牌同等待遇：只存在 600 权限的 config.json 里，
// 绝不通过任何接口返回、绝不进审计日志。
type RemotePanel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}

// RemoteStatusSnapshot 主面板缓存的远程面板状态（内存里，不落盘）。
type RemoteStatusSnapshot struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	BaseURL       string                 `json:"base_url"`
	Online        bool                   `json:"online"`
	Version       string                 `json:"version"`
	LandingCount  int                    `json:"landing_count"`
	DiskPct       float64                `json:"disk_pct"`
	RealmRunning  bool                   `json:"realm_running"`
	Landings      []remoteLandingSummary `json:"landings"`
	LastCheckedAt string                 `json:"last_checked_at"`
	LastOKAt      string                 `json:"last_ok_at"`
	Error         string                 `json:"error"`
	// Cached 为 true 表示本次返回的是上次成功时的缓存（本次拉取失败）。
	Cached bool `json:"cached"`
}

type remoteLandingSummary struct {
	Index     int        `json:"index"`
	Name      string     `json:"name"`
	Landing   string     `json:"landing"`
	Domain    string     `json:"domain"`
	Mode      string     `json:"mode"`
	Ports     []PortSpec `json:"ports"`
	Online    *bool      `json:"online"`
	LatencyMs *int       `json:"latency_ms"`
}

// centralAlertState 远程面板离线提醒的去重状态（复用延迟探测/监控告警的模式：
// 在线变离线提醒一次，持续离线每小时最多再提醒一次，恢复后复位并报恢复）。
type centralAlertState struct {
	seen      bool
	wasOnline bool
	lastAlert time.Time
}

// ctxUserKey currentUser 从请求上下文里取“central”身份（令牌进来的请求记 central）。
type ctxKey string

const ctxUserKey ctxKey = "central-user"

// ---------- 地址校验 ----------

// normalizeRemoteBaseURL 校验并归一化远程面板地址：只要写到端口为止，
// 须以 http:// 或 https:// 开头，不带路径、查询串、用户信息。
func normalizeRemoteBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("远程面板地址为空")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return "", fmt.Errorf("地址须以 http:// 或 https:// 开头，如 http://203.0.113.1:11111")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("地址格式不对")
	}
	if u.User != nil {
		return "", fmt.Errorf("地址里别带用户名密码")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("地址只要写到端口为止，别带路径")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("地址只要写到端口为止，别带参数")
	}
	if !validIPOrDomain(u.Hostname()) {
		return "", fmt.Errorf("地址里的主机不对：填 IP 或域名")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || !validPort(n) {
			return "", fmt.Errorf("地址里的端口不对：填 1-65535")
		}
	}
	return strings.TrimSuffix(raw, "/"), nil
}

func (s *Server) remoteByID(id string) *RemotePanel {
	for i := range s.cfg.Remotes {
		if s.cfg.Remotes[i].ID == id {
			return &s.cfg.Remotes[i]
		}
	}
	return nil
}

// ---------- 被管端：令牌认证 ----------

// requireCentralToken 令牌认证中间件（被管端 central/* 接口用）：
// 请求头 Authorization: Bearer <令牌>，与登录会话互不干扰。
// 同一 IP 5 次校验失败锁 5 分钟（复用登录限流），失败记审计日志（不记令牌本身）。
func (s *Server) requireCentralToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if s.sessions.loginLocked(ip) {
			writeJSON(w, 429, map[string]any{"ok": false, "error": "locked"})
			return
		}
		auth := r.Header.Get("Authorization")
		tok := ""
		if strings.HasPrefix(auth, "Bearer ") {
			tok = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}
		if tok == "" || s.cfg.RemoteTokenHash == "" || !verifyRemoteToken(tok, s.cfg.RemoteTokenHash) {
			s.sessions.loginFailed(ip)
			s.audit("central", "远程令牌校验", "", "失败", "令牌不对或未设置")
			writeJSON(w, 401, errResp("远程管理令牌不对或未设置"))
			return
		}
		s.sessions.loginOK(ip)
		ctx := context.WithValue(r.Context(), ctxUserKey, "central")
		next(w, r.WithContext(ctx))
	}
}

// ---------- 被管端：令牌管理（会话认证） ----------

func (s *Server) handleCentralTokenGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, okResp(map[string]any{
		"has_token":  s.cfg.RemoteTokenHash != "",
		"created_at": s.cfg.RemoteTokenCreatedAt,
	}))
}

// handleCentralTokenGenerate 生成（或轮换）远程管理令牌：明文只在这一次返回，
// 之后再也看不到；已有令牌时旧令牌立即失效。记审计日志（不记令牌本身）。
func (s *Server) handleCentralTokenGenerate(w http.ResponseWriter, r *http.Request) {
	tok := generateRemoteToken()
	rotated := s.cfg.RemoteTokenHash != ""
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)
	s.cfg.RemoteTokenCreatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	action := "生成远程管理令牌"
	if rotated {
		action = "轮换远程管理令牌"
	}
	s.audit(s.currentUser(r), action, "", "成功", "旧令牌已失效")
	writeJSON(w, 200, okResp(map[string]any{"token": tok, "rotated": rotated}))
}

func (s *Server) handleCentralTokenRevoke(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RemoteTokenHash == "" {
		writeJSON(w, 400, errResp("本来就没有令牌，不用撤销"))
		return
	}
	s.cfg.RemoteTokenHash = ""
	s.cfg.RemoteTokenCreatedAt = ""
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "撤销远程管理令牌", "", "成功", "此后任何主面板都连不进来")
	writeJSON(w, 200, okResp(nil))
}

// ---------- 被管端：总览摘要 ----------

// localStatusPayload 复用本地总览 handler 的输出（内部走一遍 handleStatus，
// 不经过 HTTP），供 central/status 摘字段用；避免把 handleStatus 的
// 落地组装逻辑复制第二份。
func (s *Server) localStatusPayload() (map[string]any, error) {
	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	rec := httptest.NewRecorder()
	s.handleStatus(rec, req)
	if rec.Code != 200 {
		return nil, fmt.Errorf("总览读取失败")
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("总览解析失败")
	}
	if resp["ok"] != true {
		return nil, fmt.Errorf("总览返回失败")
	}
	return resp, nil
}

// handleCentralStatus 被管端总览摘要（令牌认证）：版本、realm 是否在跑、
// 落地列表（域名/IP、监听端口、在线状态、延迟）、根分区磁盘使用率。
func (s *Server) handleCentralStatus(w http.ResponseWriter, r *http.Request) {
	data, err := s.localStatusPayload()
	if err != nil {
		writeJSON(w, 500, errResp(err.Error()))
		return
	}
	landings, _ := data["landings"].([]any)
	if landings == nil {
		landings = []any{}
	}
	st := sampleSystem() // 复用 1.1.10 机器监控采样（根分区 statfs）
	writeJSON(w, 200, okResp(map[string]any{
		"panel_version":    Version,
		"realm":            data["realm"],
		"landings":         landings,
		"disk_pct":         st.DiskPct,
		"disk_total_bytes": st.DiskTotalBytes,
		"disk_used_bytes":  st.DiskUsedBytes,
		"disk_free_bytes":  st.DiskFreeBytes,
	}))
}

// ---------- 主面板端：调远程面板的 HTTP 客户端 ----------

// remoteCall 调远程面板的一个 central/* 接口。返回 HTTP 状态码与原始响应体。
// 网络层失败（连不上、超时、DNS 失败）返回 side=local 的错：这是主面板
// 这边连不上的问题；远程面板返回了非 2xx 则返回 side=remote 的错，
// 文案里带远程面板报的错原文。
func (s *Server) remoteCall(rem RemotePanel, method, path string, body []byte, timeout time.Duration) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rem.BaseURL+path, rd)
	if err != nil {
		return 0, nil, fmt.Errorf("local：构造请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+rem.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("local：主面板连不上远程面板：%v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := remoteErrorText(b)
		if resp.StatusCode == 401 {
			msg = "远程管理令牌不对或已被撤销，请到被管面板重新生成后重新登记"
		} else if resp.StatusCode == 404 {
			msg = "远程面板没有集中管理接口，版本太旧（需要 1.2.0+），请先升级远程面板"
		}
		return resp.StatusCode, b, fmt.Errorf("remote：远程面板：%s", msg)
	}
	return resp.StatusCode, b, nil
}

// remoteErrorText 从远程面板的错误响应里抠出 error 字段；抠不出就给通用文案。
func remoteErrorText(b []byte) string {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err == nil {
		if e, ok := m["error"].(string); ok && e != "" {
			return e
		}
	}
	return "远程面板返回了错误"
}

// remoteSideOf 把 remoteCall 的错按前缀拆成 side（local/remote）与文案。
func remoteSideOf(err error) (string, string) {
	msg := err.Error()
	if strings.HasPrefix(msg, "remote：") {
		return "remote", strings.TrimPrefix(msg, "remote：")
	}
	return "local", strings.TrimPrefix(msg, "local：")
}

// fetchRemoteStatus 拉一台远程面板的 central/status 并解析成快照。
func (s *Server) fetchRemoteStatus(rem RemotePanel) (*RemoteStatusSnapshot, error) {
	_, body, err := s.remoteCall(rem, "GET", "/api/v1/central/status", nil, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var resp struct {
		OK           bool                   `json:"ok"`
		PanelVersion string                 `json:"panel_version"`
		Realm        struct{ Running bool } `json:"realm"`
		DiskPct      float64                `json:"disk_pct"`
		Landings     []remoteLandingSummary `json:"landings"`
		Error        string                 `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("local：远程面板返回的内容解析失败：%v", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("remote：远程面板：%s", resp.Error)
	}
	// 名字是被管面板 displayName() 的结果：没设备注名称时回退成落地地址；
	// 这里把回退值清掉，主面板只保留真正设过的备注名，前端据此决定
	// 行标题是“名称 地址 #编号”还是只有“地址 #编号”。远程面板版本再旧
	// 也一样（displayName 进 central/status 很久了），不用设版本门。
	for i := range resp.Landings {
		resp.Landings[i].Name = strings.TrimSpace(resp.Landings[i].Name)
		if resp.Landings[i].Name == "" || resp.Landings[i].Name == resp.Landings[i].Landing {
			resp.Landings[i].Name = ""
		}
	}
	// 版本太旧（低于 1.2.0）直接拒掉：老面板没有 central 接口，
	// 这里能调通只可能是新面板，但版本号仍要验，防代理/缓存冒充。
	if resp.PanelVersion != "" && isNewerVersion("1.2.0", resp.PanelVersion) {
		return nil, fmt.Errorf("remote：远程面板版本太旧（%s，需要 1.2.0+），请先升级远程面板", resp.PanelVersion)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	snap := &RemoteStatusSnapshot{
		ID: rem.ID, Name: rem.Name, BaseURL: rem.BaseURL,
		Online: true, Version: resp.PanelVersion, LandingCount: len(resp.Landings),
		DiskPct: resp.DiskPct, RealmRunning: resp.Realm.Running,
		Landings: resp.Landings, LastCheckedAt: now, LastOKAt: now,
	}
	if snap.Landings == nil {
		snap.Landings = []remoteLandingSummary{}
	}
	return snap, nil
}

// ---------- 主面板端：后台拉取与离线提醒 ----------

func (s *Server) centralLoop() {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	s.pollAllRemotes()
	for range t.C {
		s.pollAllRemotes()
	}
}

func (s *Server) pollAllRemotes() {
	for _, rem := range append([]RemotePanel{}, s.cfg.Remotes...) {
		s.pollOneRemote(rem)
	}
}

// pollOneRemote 拉一台远程面板并更新缓存；在线/离线变化与持续离线
// 走主面板的 Telegram 提醒（复用去重节流：变离线提醒一次，持续离线
// 每小时最多再提醒一次，恢复后复位并报恢复；没配 Telegram 就只记审计日志）。
func (s *Server) pollOneRemote(rem RemotePanel) *RemoteStatusSnapshot {
	snap, err := s.fetchRemoteStatus(rem)
	now := time.Now()
	s.centralMu.Lock()
	prev := s.centralCache[rem.ID]
	st := s.centralAlert[rem.ID]
	if st == nil {
		st = &centralAlertState{}
		s.centralAlert[rem.ID] = st
	}
	var alert string
	if err != nil {
		if prev == nil {
			prev = &RemoteStatusSnapshot{ID: rem.ID, Name: rem.Name, BaseURL: rem.BaseURL, Landings: []remoteLandingSummary{}}
		}
		wasOnline := st.seen && st.wasOnline
		prev.Online = false
		_, msg := remoteSideOf(err)
		prev.Error = msg
		prev.LastCheckedAt = now.UTC().Format(time.RFC3339)
		s.centralCache[rem.ID] = prev
		if !st.seen || wasOnline {
			alert = "远程面板离线：" + rem.Name + "（" + rem.BaseURL + "），" + msg + "，将继续每 1 小时提醒一次"
		} else if now.Sub(st.lastAlert) >= time.Hour {
			alert = "远程面板仍离线：" + rem.Name + "（" + rem.BaseURL + "），" + msg
		}
		st.seen = true
		st.wasOnline = false
		if alert != "" {
			st.lastAlert = now
		}
		s.centralMu.Unlock()
		if alert != "" {
			s.sendTelegram(alert)
			s.audit("system", "远程面板离线", rem.Name, "失败", msg)
		}
		return prev
	}
	wasOnline := st.seen && st.wasOnline
	s.centralCache[rem.ID] = snap
	if st.seen && !wasOnline {
		alert = "远程面板恢复：" + rem.Name + "（" + rem.BaseURL + "）已恢复在线"
	}
	st.seen = true
	st.wasOnline = true
	s.centralMu.Unlock()
	if alert != "" {
		s.sendTelegram(alert)
		s.audit("system", "远程面板恢复", rem.Name, "成功", "")
	}
	return snap
}

// ---------- 主面板端：登记管理 ----------

func (s *Server) handleCentralRemotesList(w http.ResponseWriter, r *http.Request) {
	s.centralMu.Lock()
	defer s.centralMu.Unlock()
	out := []RemoteStatusSnapshot{}
	for _, rem := range s.cfg.Remotes {
		if snap, ok := s.centralCache[rem.ID]; ok && snap != nil {
			out = append(out, *snap)
		} else {
			out = append(out, RemoteStatusSnapshot{ID: rem.ID, Name: rem.Name, BaseURL: rem.BaseURL, Landings: []remoteLandingSummary{}})
		}
	}
	writeJSON(w, 200, okResp(map[string]any{"remotes": out}))
}

func (s *Server) handleCentralRemotesAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		BaseURL string `json:"base_url"`
		Token   string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	baseURL, err := normalizeRemoteBaseURL(req.BaseURL)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	tok := strings.TrimSpace(req.Token)
	if tok == "" {
		writeJSON(w, 400, errResp("令牌为空：到被管面板的设置页生成远程管理令牌后填进来"))
		return
	}
	if !strings.HasPrefix(tok, remoteTokenPrefix) {
		writeJSON(w, 400, errResp("令牌格式不对：应以 rpt_ 开头"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		u, _ := url.Parse(baseURL)
		name = u.Host
	}
	if len(name) > 64 {
		writeJSON(w, 400, errResp("名称太长，最多 64 个字"))
		return
	}
	for _, rem := range s.cfg.Remotes {
		if rem.BaseURL == baseURL {
			writeJSON(w, 400, errResp("这台远程面板已经登记过了"))
			return
		}
	}
	rem := RemotePanel{
		ID:   "rm-" + time.Now().UTC().Format("20060102-150405") + "-" + randomTokenHex(3),
		Name: name, BaseURL: baseURL, Token: tok,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	// 登记前当场连一次：连不上、令牌不对、版本太旧都直接拒绝，并写清是哪种问题。
	if _, err := s.fetchRemoteStatus(rem); err != nil {
		side, msg := remoteSideOf(err)
		_ = side
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "side": side})
		return
	}
	s.cfg.Remotes = append(s.cfg.Remotes, rem)
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.pollOneRemote(rem)
	s.audit(s.currentUser(r), "登记远程面板", name+"（"+baseURL+"）", "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"id": rem.ID}))
}

func (s *Server) handleCentralRemotesDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	kept := s.cfg.Remotes[:0]
	for _, x := range s.cfg.Remotes {
		if x.ID != id {
			kept = append(kept, x)
		}
	}
	s.cfg.Remotes = kept
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.centralMu.Lock()
	delete(s.centralCache, id)
	delete(s.centralAlert, id)
	s.centralMu.Unlock()
	s.audit(s.currentUser(r), "取消登记远程面板", rem.Name+"（"+rem.BaseURL+"）", "成功", "只删主面板的登记，不影响远程面板本身")
	writeJSON(w, 200, okResp(nil))
}

func (s *Server) handleCentralRemotesPoll(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	snap := s.pollOneRemote(*rem)
	if !snap.Online {
		writeJSON(w, 200, map[string]any{"ok": false, "error": snap.Error, "side": "local", "remote": snap})
		return
	}
	writeJSON(w, 200, okResp(map[string]any{"remote": snap}))
}

// ---------- 主面板端：聚合读取 ----------

// handleCentralRemoteLandings 该远程面板的落地列表：当场拉一次；
// 拉失败时返回上次成功的缓存并标 cached=true，界面写清是缓存。
func (s *Server) handleCentralRemoteLandings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	snap, err := s.fetchRemoteStatus(*rem)
	if err != nil {
		s.centralMu.Lock()
		cached := s.centralCache[id]
		s.centralMu.Unlock()
		if cached != nil && cached.LastOKAt != "" {
			cpy := *cached
			cpy.Cached = true
			cpy.Error = ""
			writeJSON(w, 200, okResp(map[string]any{
				"remote": cpy, "landings": cpy.Landings,
				"note": "远程面板暂时连不上，显示的是上次成功时的数据",
			}))
			return
		}
		side, msg := remoteSideOf(err)
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "side": side})
		return
	}
	s.centralMu.Lock()
	s.centralCache[id] = snap
	s.centralMu.Unlock()
	writeJSON(w, 200, okResp(map[string]any{"remote": snap, "landings": snap.Landings}))
}

// handleCentralRemoteLinks 代理看远程面板的节点链接/二维码：原样透传远程的返回。
func (s *Server) handleCentralRemoteLinks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	_, body, err := s.remoteCall(*rem, "GET", "/api/v1/central/links", nil, 30*time.Second)
	if err != nil {
		side, msg := remoteSideOf(err)
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "side": side})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}

// ---------- 主面板端：代理操作 ----------

// validateRemoteAddLandingBody 主面板端的二次校验（与本地加落地同样的规则）：
// 校验通过后才把原始请求体发给远程面板；远程面板还会再校验一遍。
func validateRemoteAddLandingBody(body []byte) error {
	var req struct {
		Landing           string         `json:"landing"`
		Preset            string         `json:"preset"`
		SBMode            string         `json:"sb_mode"`
		CustomPorts       string         `json:"custom_ports"`
		WantUDP           bool           `json:"want_udp"`
		CertDomain        string         `json:"cert_domain"`
		CertMode          string         `json:"cert_mode"`
		DurationDays      int            `json:"duration_days"`
		TrafficLimitBytes int64          `json:"traffic_limit_bytes"`
		TrafficLimitMode  string         `json:"traffic_limit_mode"`
		SpeedLimitMbps    int            `json:"speed_limit_mbps"`
		ShareMode         string         `json:"share_mode"`
		ListenOverrides   map[string]int `json:"listen_overrides"`
		Mode              string         `json:"mode"`
		RelayAddr         string         `json:"relay_addr"`
		DoorPortOverrides map[string]int `json:"door_port_overrides"`
		OrigLink          string         `json:"orig_link"`
		Name              string         `json:"name"`
		LinkName          string         `json:"link_name"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("请求格式不对")
	}
	if !validIPOrDomain(req.Landing) {
		return fmt.Errorf("落地地址不对：填 IP 或域名")
	}
	if req.Preset != "1" && req.Preset != "2" && req.Preset != "3" {
		return fmt.Errorf("预设只能是 1/2/3")
	}
	if req.Preset == "2" && req.SBMode != "" && req.SBMode != "1" && req.SBMode != "2" {
		return fmt.Errorf("sing-box 模式只能是 1（TLS）或 2（明文）")
	}
	if req.Preset == "3" {
		if _, err := parseCustomPorts(req.CustomPorts, req.WantUDP); err != nil {
			return err
		}
	}
	if req.CertDomain != "" && !validDomain(req.CertDomain) {
		return fmt.Errorf("证书域名格式不对")
	}
	if req.CertMode != "" && req.CertMode != "http01" && req.CertMode != "dns01" {
		return fmt.Errorf("证书签发方式只能是 http01（默认走 80）或 dns01（DNS 验证）")
	}
	if req.TrafficLimitMode != "" && req.TrafficLimitMode != "total" && req.TrafficLimitMode != "monthly" {
		return fmt.Errorf("流量限制模式只能是 total 或 monthly")
	}
	if req.TrafficLimitBytes < 0 || req.SpeedLimitMbps < 0 || req.DurationDays < -1 {
		return fmt.Errorf("数值不能为负")
	}
	if req.ShareMode != "" && req.ShareMode != "1" && req.ShareMode != "2" && req.ShareMode != "3" && req.ShareMode != "4" {
		return fmt.Errorf("分流方式只能是 1/2/3/4")
	}
	if req.Mode != "" && req.Mode != "standard" && req.Mode != "door" {
		return fmt.Errorf("模式只能是 standard（标准模式）或 door（任意门模式）")
	}
	if req.RelayAddr != "" && !validIPOrDomain(req.RelayAddr) {
		return fmt.Errorf("中转地址不对：填 IP 或域名")
	}
	for k, v := range req.ListenOverrides {
		p, err := strconv.Atoi(strings.TrimSpace(k))
		if err != nil || !validPort(p) || !validPort(v) {
			return fmt.Errorf("监听端口覆盖格式不对：键和值都须是 1-65535 的端口，如 {\"443\": 10443}")
		}
	}
	for k, v := range req.DoorPortOverrides {
		rp, err := strconv.Atoi(strings.TrimSpace(k))
		if err != nil || !validPort(rp) || !validPort(v) {
			return fmt.Errorf("转发端口覆盖格式不对：键和值都须是 1-65535 的端口，如 {\"443\": 30443}")
		}
	}
	if req.OrigLink != "" {
		if _, err := parseNodeLink(req.OrigLink); err != nil {
			return fmt.Errorf("原始节点链接不对：%v", err)
		}
	}
	if req.Name != "" && (len(req.Name) > 64 || strings.ContainsAny(req.Name, "\"\n\r")) {
		return fmt.Errorf("名称太长或含有非法字符（不能超过 64 字，不能含引号与换行）")
	}
	if req.LinkName != "" && (len(req.LinkName) > 64 || strings.ContainsAny(req.LinkName, "\n\r")) {
		return fmt.Errorf("节点名称太长或含有非法字符（不能超过 64 字，不能含换行）")
	}
	return nil
}

// validateRemoteAddNodeBody 主面板端的二次校验（与本地给落地加节点同样的规则）：
// 子域名必填；只填子域名是第 1 步（返回落地机命令），带 new_link 是第 2 步
// （链接域名须与子域名一致）。校验通过后才把原始请求体发给远程面板。
func validateRemoteAddNodeBody(body []byte) error {
	var req struct {
		Subdomain string `json:"subdomain"`
		NewLink   string `json:"new_link"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("请求格式不对")
	}
	if req.Subdomain == "" || !validDomain(req.Subdomain) {
		return fmt.Errorf("子域名格式不对")
	}
	if req.NewLink != "" {
		host, err := linkHostDomain(req.NewLink)
		if err != nil {
			return fmt.Errorf("链接校验失败：%v", err)
		}
		if !strings.EqualFold(host, req.Subdomain) {
			return fmt.Errorf("链接里的域名（%s）与填的子域名（%s）不一致", host, req.Subdomain)
		}
	}
	return nil
}

// proxyRemoteWrite 代理一个写操作到远程面板：主面板先校验（调用方已做），
// 再用令牌调远程，远程的返回原样写回；失败时带 side 区分是哪边的问题。
func (s *Server) proxyRemoteWrite(w http.ResponseWriter, r *http.Request, rem RemotePanel, method, path string, body []byte, timeout time.Duration) {
	_, respBody, err := s.remoteCall(rem, method, path, body, timeout)
	if err != nil {
		side, msg := remoteSideOf(err)
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "side": side})
		return
	}
	// 远程面板的写操作成功后，刷新一次缓存的状态（落地数会变）。
	go s.pollOneRemote(rem)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(respBody)
}

func (s *Server) handleCentralRemoteAddLanding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, 400, errResp("请求读失败"))
		return
	}
	if err := validateRemoteAddLandingBody(body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "side": "local"})
		return
	}
	// 加落地是重操作（远程要跑安装脚本），超时与本地一致放宽到 300 秒。
	s.proxyRemoteWrite(w, r, *rem, "POST", "/api/v1/central/landings", body, 300*time.Second)
	s.audit(s.currentUser(r), "代理加落地", rem.Name, "已转发到远程面板", "")
}

func (s *Server) handleCentralRemoteDeleteLanding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 1 {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "落地编号不对", "side": "local"})
		return
	}
	s.proxyRemoteWrite(w, r, *rem, "DELETE", "/api/v1/central/landings/"+strconv.Itoa(index), nil, 60*time.Second)
	s.audit(s.currentUser(r), "代理删除落地", rem.Name+" / 落地 "+strconv.Itoa(index), "已转发到远程面板", "")
}

func (s *Server) handleCentralRemoteAddNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 1 {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "落地编号不对", "side": "local"})
		return
	}
	// 给落地加节点是 1.2.5 才有的远程接口：缓存的远程版本低于 1.2.5
	// 直接拒绝，不发请求；缓存里没有版本时放行，靠远程的 404 兜底。
	s.centralMu.Lock()
	snap := s.centralCache[id]
	s.centralMu.Unlock()
	if snap != nil && snap.Version != "" && isNewerVersion("1.2.5", snap.Version) {
		writeJSON(w, 200, map[string]any{"ok": false, "error": fmt.Sprintf("远程面板版本太旧（%s，需要 1.2.5+），请先升级远程面板", snap.Version), "side": "remote"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, 400, errResp("请求读失败"))
		return
	}
	if err := validateRemoteAddNodeBody(body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "side": "local"})
		return
	}
	// 第 2 步要跑 install.sh --twin（远程 60 秒），超时放宽到 90 秒。
	status, respBody, err := s.remoteCall(*rem, "POST", "/api/v1/central/landings/"+strconv.Itoa(index)+"/add-node", body, 90*time.Second)
	if err != nil {
		side, msg := remoteSideOf(err)
		if status == 404 {
			// 老面板没有这个接口：404 是版本问题，不是地址问题
			msg = "远程面板版本太旧（需要 1.2.5+），请先升级远程面板"
			side = "remote"
		}
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "side": side})
		return
	}
	go s.pollOneRemote(*rem)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(respBody)
	s.audit(s.currentUser(r), "代理给落地加节点", rem.Name+" / 落地 "+strconv.Itoa(index), "已转发到远程面板", "")
}

func (s *Server) handleCentralRemoteService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	action := r.PathValue("action")
	if action != "start" && action != "stop" && action != "restart" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "操作只能是 start/stop/restart", "side": "local"})
		return
	}
	s.proxyRemoteWrite(w, r, *rem, "POST", "/api/v1/central/service/"+action, []byte("{}"), 60*time.Second)
	s.audit(s.currentUser(r), "代理服务操作", rem.Name+" / realm "+action, "已转发到远程面板", "")
}
