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
//   - 令牌只放行白名单里的 central/* 接口（status / nodes 详情 / links /
//     订阅内容 / 加落地 / 编辑落地 / 补原始链接 / 删落地 / 给落地加节点 /
//     删子节点 / 续期 / 续量 / 设流量限制 / 证书 / 流量 / 体检 / 一键修复 /
//     服务操作），改密码、改端口、改设置等接口令牌调不通。
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
	RelayAddr     string                 `json:"relay_addr"`
	Landings      []remoteLandingSummary `json:"landings"`
	LastCheckedAt string                 `json:"last_checked_at"`
	LastOKAt      string                 `json:"last_ok_at"`
	Error         string                 `json:"error"`
	// Cached 为 true 表示本次返回的是上次成功时的缓存（本次拉取失败）。
	Cached bool `json:"cached"`
	// Nodes 该远程面板的后端节点摘要（v1.4.19，切片 9）：随 central/status
	// 下发，走已有的 2 分钟拉取链。失联时保留上次拉到的，不清零（与
	// Landings 同款）；从没拉成功过时为空，界面标“尚未拉取”。
	Nodes []centralNodeSummary `json:"nodes"`
	// NodesSupported 该远程面板的版本支不支持节点摘要（v1.4.19+ 才下发
	// nodes；更旧的版本拉得到状态但看不到节点，前端据此写明版本太旧）。
	NodesSupported bool `json:"nodes_supported"`
}

// centralNodeSummary 一台后端节点的摘要（v1.4.19，切片 9）：集中管理页
// 每台远程面板行下的节点子列表用。health 是 nodeHealth 口径
// （online／offline／never）；traffic 是本期按人合计＋累计，与节点
// 详情的 traffic 块同口径（nodeBackendTrafficBlock）。
type centralNodeSummary struct {
	NodeID  int64               `json:"node_id"`
	Name    string              `json:"name"`
	Health  string              `json:"health"`
	Traffic centralNodeTraffic  `json:"traffic"`
}

// centralNodeTraffic 一台后端节点的流量：本期（up_period＋down_period）
// 与累计（up_total＋down_total），字节数。
type centralNodeTraffic struct {
	UpPeriod   int64 `json:"up_period"`
	DownPeriod int64 `json:"down_period"`
	UpTotal    int64 `json:"up_total"`
	DownTotal  int64 `json:"down_total"`
}

// centralNodesState 一台远程面板的节点数据状态（v1.4.19，切片 9）：
// online＝刚拉到；offline_cached＝失联、显示上次数据；never＝从没拉成功过。
// 前端按它写行上标注，数字不清零（快照里留着上次的）。
func centralNodesState(snap *RemoteStatusSnapshot) string {
	if snap == nil || snap.LastOKAt == "" {
		return "never"
	}
	if !snap.Online {
		return "offline_cached"
	}
	return "online"
}

// remoteTraffic 远程落地的一份流量快照（字段与本地总览的 traffic 一致）。
type remoteTraffic struct {
	TodayUp    int64   `json:"today_up"`
	TodayDown  int64   `json:"today_down"`
	MonthUp    int64   `json:"month_up"`
	MonthDown  int64   `json:"month_down"`
	TotalUp    int64   `json:"total_up"`
	TotalDown  int64   `json:"total_down"`
	LimitBytes int64   `json:"limit_bytes"`
	LimitMode  string  `json:"limit_mode"`
	UsedBytes  int64   `json:"used_bytes"`
	Pct        float64 `json:"pct"`
}

// remoteLandingSummary 远程落地的一行摘要：字段与本地总览的落地对象一致，
// 集中管理代理操作（编辑/续期/流量/证书/删子节点）靠它预填表单、展示状态；
// 链接与二维码不走这里，仍由 central/links 另外取。
type remoteLandingSummary struct {
	Index      int        `json:"index"`
	Name       string     `json:"name"`
	Landing    string     `json:"landing"`
	Domain     string     `json:"domain"`
	Mode       string     `json:"mode"`
	Ports      []PortSpec `json:"ports"`
	Online     *bool      `json:"online"`
	LatencyMs  *int       `json:"latency_ms"`
	CertDomain string     `json:"cert_domain"`
	CertMode   string     `json:"cert_mode"`
	// TrafficMultiplier 流量倍率（v1.3.40）：远程面板 central/status
	// 下发的落地对象里带，集中管理编辑表单靠它预填。
	TrafficMultiplier float64       `json:"traffic_multiplier"`
	Preset            string        `json:"preset"`
	SBMode            string        `json:"sb_mode"`
	CustomPorts       string        `json:"custom_ports"`
	WantUDP           bool          `json:"want_udp"`
	CertDaysLeft      *int          `json:"cert_days_left"`
	CertExpiry        string        `json:"cert_expiry"`
	RenewMode         string        `json:"renew_mode"`
	Expiry            string        `json:"expiry"`
	DaysLeft          *int          `json:"days_left"`
	OrigLink          string        `json:"orig_link"`
	OrigLinkName      string        `json:"orig_link_name"`
	SpeedLimitMbps    int           `json:"speed_limit_mbps"`
	Traffic           remoteTraffic `json:"traffic"`
	Disabled          bool          `json:"disabled"`
	DisabledReason    string        `json:"disabled_reason"`
	SubNodes          []SubNode     `json:"subnodes"`
	// Links 该落地的节点链接（remote central/status 下发的 links：中转+
	// 直连，按 Label 分中转/直连/任意门；节点统计按它计数。远程面板本来
	// 就下发，这里只是接住、不再丢掉，不新增轮询与探测）。
	Links []labelledLink `json:"links"`
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

// centralNodesSummary 本机全部后端节点的摘要（v1.4.19，切片 9）：
// 供 handleCentralStatus 下发。自己拿 nodes.mu（调用方不用持锁）；
// 流量走 nodeBackendTrafficBlock（与节点详情同一块：本期按人合计＋累计）。
// 没有节点时返回空数组（不是 null，前端不用判空）。
func (s *Server) centralNodesSummary() []centralNodeSummary {
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	out := make([]centralNodeSummary, 0, len(s.nodes.data.Nodes))
	num := func(v any) int64 {
		if n, ok := v.(int64); ok {
			return n
		}
		return 0
	}
	for _, n := range s.nodes.listNodes() {
		tb := s.nodeBackendTrafficBlock(&n)
		out = append(out, centralNodeSummary{
			NodeID: n.ID,
			Name:   n.Name,
			Health: nodeHealth(&n),
			Traffic: centralNodeTraffic{
				UpPeriod:   num(tb["up_period"]),
				DownPeriod: num(tb["down_period"]),
				UpTotal:    num(tb["up_total"]),
				DownTotal:  num(tb["down_total"]),
			},
		})
	}
	return out
}

// handleCentralStatus 被管端总览摘要（令牌认证）：版本、realm 是否在跑、
// 落地列表（域名/IP、监听端口、在线状态、延迟）、根分区磁盘使用率。
// v1.4.19：多带本机后端节点摘要（nodes），主面板随 2 分钟拉取链收走，
// 不另起轮询。
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
		"relay_addr":       data["relay_addr"],
		"disk_pct":         st.DiskPct,
		"disk_total_bytes": st.DiskTotalBytes,
		"disk_used_bytes":  st.DiskUsedBytes,
		"disk_free_bytes":  st.DiskFreeBytes,
		"nodes":            s.centralNodesSummary(),
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
		RelayAddr    string                 `json:"relay_addr"`
		Landings     []remoteLandingSummary `json:"landings"`
		Nodes        []centralNodeSummary   `json:"nodes"`
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
		DiskPct: resp.DiskPct, RealmRunning: resp.Realm.Running, RelayAddr: resp.RelayAddr,
		Landings: resp.Landings, LastCheckedAt: now, LastOKAt: now,
		Nodes: resp.Nodes,
		// 节点摘要是 1.4.19 才下发的：远程版本低于 1.4.19 时 nodes 为空不是
		// “没有节点”，是“看不到”，前端据此写明版本太旧；版本号空着时放行。
		NodesSupported: resp.PanelVersion == "" || !isNewerVersion("1.4.19", resp.PanelVersion),
	}
	if snap.Landings == nil {
		snap.Landings = []remoteLandingSummary{}
	}
	if snap.Nodes == nil {
		snap.Nodes = []centralNodeSummary{}
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
	// 每人 UUID（v1.3.35）：快照刚刷新完，给已有用户补齐远程落地的
	// UUID；写锁被占时跳过这一轮（下次轮询再来），只改了才落盘。
	if s.opMu.TryLock() {
		changed := s.backfillNodeUUIDs()
		if changed {
			_ = s.saveConfig()
		}
		s.opMu.Unlock()
	}
	// 集中用户表（v1.3.36）：每 2 分钟兜底全量同步一次（新建/编辑/
	// 删除时已立即同步过，这里只补漏：远程重启丢表、管理面板漏推、
	// 远程刚登记）。不新增后台任务与轮询，复用本轮询；失败只记审计
	// （每远程每小时最多一次，见 healCentralUsers），不阻断轮询。
	s.healCentralUsers()
}

// pollOneRemote 拉一台远程面板并更新缓存；在线/离线变化与持续离线
// 走主面板的 Telegram 提醒（复用去重节流：变离线提醒一次，持续离线
// 每小时最多再提醒一次，恢复后复位并报恢复；没配 Telegram 就只记审计日志）。
// pollOneRemoteAsync 代理写操作成功后异步刷新一次远程快照（落地数会变）。
// 用 WaitGroup 记数：测试能在清理前等它跑完，生产行为不变（不等它）。
func (s *Server) pollOneRemoteAsync(rem RemotePanel) {
	s.centralPollWG.Add(1)
	go func() {
		defer s.centralPollWG.Done()
		s.pollOneRemote(rem)
	}()
}

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
			prev = &RemoteStatusSnapshot{ID: rem.ID, Name: rem.Name, BaseURL: rem.BaseURL, Landings: []remoteLandingSummary{}, Nodes: []centralNodeSummary{}}
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
			out = append(out, RemoteStatusSnapshot{ID: rem.ID, Name: rem.Name, BaseURL: rem.BaseURL, Landings: []remoteLandingSummary{}, Nodes: []centralNodeSummary{}})
		}
	}
	// 节点统计（v1.3.52）：总数 + 本机一行 + 每台远程一行，只计数；
	// 本机读 nodes.txt、远程用快照里落地自带的 links，都是已有登记，
	// 不新增轮询与探测。
	writeJSON(w, 200, okResp(map[string]any{"remotes": out, "node_stats": computeNodeStats(s.localNodeCount(), out)}))
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
	// v1.3.33：登记后立即把远程已有的按人转发记录导进来（Panel 写成
	// 这台远程的 id），之后分配端口就能避开它们；撤销重登记也不再变
	// 瞎。导不进来（远程不可达、旧版本）只记警告，不阻断登记成功。
	imported, updated, importErr := s.importRemoteUserForwards(rem)
	resp := map[string]any{"id": rem.ID, "imported": imported, "updated": updated}
	if importErr != nil {
		_, msg := remoteSideOf(importErr)
		resp["import_warning"] = "远程转发记录没导进来（" + msg + "），分配端口时仍按旧口径避让本地记录"
		s.audit(s.currentUser(r), "导入远程转发记录", name+"（"+baseURL+"）", "警告", resp["import_warning"].(string))
	} else if imported+updated > 0 {
		s.audit(s.currentUser(r), "导入远程转发记录", name+"（"+baseURL+"）", "成功", fmt.Sprintf("导入 %d 条，更新 %d 条", imported, updated))
	}
	s.audit(s.currentUser(r), "登记远程面板", name+"（"+baseURL+"）", "成功", "")
	writeJSON(w, 200, okResp(resp))
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

// handleCentralRemotesUpdateToken 更换一台已登记远程面板的令牌（v1.3.31）：
// 只换令牌，不动地址与其他字段。提交后先拿新令牌当场调一次该远程的
// /api/v1/central/status（与登记时同一套 fetchRemoteStatus 校验）：
// 连不上、令牌不对、版本太旧都直接拒绝，原令牌不动；验过才保存并记审计。
// 令牌明文只存在 600 权限的 config.json 里，接口不返回、审计不记原文。
func (s *Server) handleCentralRemotesUpdateToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
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
	candidate := *rem
	candidate.Token = tok
	if _, err := s.fetchRemoteStatus(candidate); err != nil {
		side, msg := remoteSideOf(err)
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "side": side})
		return
	}
	rem.Token = tok
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.pollOneRemote(*rem)
	s.audit(s.currentUser(r), "更换远程面板令牌", rem.Name+"（"+rem.BaseURL+"）", "成功", "新令牌已验证通过，原令牌即刻失效")
	writeJSON(w, 200, okResp(nil))
}

// handleCentralRemotesUpdate 编辑一台已登记远程面板的名字与地址（v1.3.49）：
// 只改 Name 与 BaseURL，ID、Token、CreatedAt 不动。提交后先拿存的令牌
// 当场调一次新地址的 /api/v1/central/status（与登记、换令牌同一套
// fetchRemoteStatus 校验）：连不上、令牌不对、版本太旧都直接拒绝，
// 原值不动；新地址与其它已登记远程撞了也拒绝；验过才保存并记审计。
// 只改管理面板去拉远程的地址，不动订阅链接本身——原先的订阅链接与
// 令牌不变，客户端不用重新导入；改完地址、探通后节点会回到同一条订阅里。
func (s *Server) handleCentralRemotesUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	var req struct {
		Name    string `json:"name"`
		BaseURL string `json:"base_url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, 400, errResp("名称不能为空"))
		return
	}
	if len(name) > 64 {
		writeJSON(w, 400, errResp("名称太长，最多 64 个字"))
		return
	}
	baseURL, err := normalizeRemoteBaseURL(req.BaseURL)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	for _, x := range s.cfg.Remotes {
		if x.ID != id && x.BaseURL == baseURL {
			writeJSON(w, 400, errResp("这个地址已经登记过另一台远程面板了"))
			return
		}
	}
	candidate := *rem
	candidate.Name = name
	candidate.BaseURL = baseURL
	if _, err := s.fetchRemoteStatus(candidate); err != nil {
		side, msg := remoteSideOf(err)
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "side": side})
		return
	}
	oldName, oldURL := rem.Name, rem.BaseURL
	rem.Name = name
	rem.BaseURL = baseURL
	if err := s.saveConfig(); err != nil {
		// 保存失败时内存里也还原，不留半截状态。
		rem.Name = oldName
		rem.BaseURL = oldURL
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	// 地址变了，旧地址拉回来的快照与离线提醒状态都作废（复用取消登记那两行），
	// 再当场重探一次新地址，列表立刻显示新状态。
	s.centralMu.Lock()
	delete(s.centralCache, id)
	delete(s.centralAlert, id)
	s.centralMu.Unlock()
	s.pollOneRemote(*rem)
	s.audit(s.currentUser(r), "编辑远程面板", oldName+"（"+oldURL+"）", "成功", "改为 "+name+"（"+baseURL+"）")
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

// handleCentralNodeDetail GET /api/v1/central/nodes/{id}：被管端节点详情
// （令牌认证，v1.4.19，切片 9）。主面板的详情代理调的就是它；形状与
// 本地 GET /api/v1/nodes/{id} 一致（节点行＋入站＋流量＋健康）。
func (s *Server) handleCentralNodeDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	s.nodes.mu.Lock()
	node := s.nodes.getNode(id)
	if node == nil {
		s.nodes.mu.Unlock()
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	payload := s.nodeDetailPayload(node)
	s.nodes.mu.Unlock()
	writeJSON(w, 200, okResp(payload))
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

// ---------- 主面板端：节点汇总与详情代理（v1.4.19，切片 9） ----------

// centralRemoteNodesEntry 聚合接口里一台远程面板的节点行：快照里的节点
// 摘要＋状态标注（online／offline_cached／never）。快照没有时按从没
// 拉过处理（state=never，nodes 为空数组）。
func centralRemoteNodesEntry(rem RemotePanel, snap *RemoteStatusSnapshot) map[string]any {
	nodes := []centralNodeSummary{}
	supported := true
	state := "never"
	var lastOK, lastChecked string
	online := false
	if snap != nil {
		nodes = snap.Nodes
		if nodes == nil {
			nodes = []centralNodeSummary{}
		}
		supported = snap.NodesSupported
		state = centralNodesState(snap)
		lastOK, lastChecked = snap.LastOKAt, snap.LastCheckedAt
		online = snap.Online
	}
	return map[string]any{
		"remote_id": rem.ID, "remote_name": rem.Name, "online": online,
		"state": state, "last_ok_at": lastOK, "last_checked_at": lastChecked,
		"nodes_supported": supported, "nodes": nodes,
	}
}

// handleCentralNodesAggregate GET /api/v1/central/nodes：管理面板汇总
// 各远程的节点（读 2 分钟拉取链的缓存，不当场拉远程、不新增轮询）。
// 带 ?remote_id= 只回那一台：{remote_id, nodes:[...]}；不带回全部：
// {remotes:[{remote_id, ..., nodes:[...]}]}。没登记过该远程 404。
// 令牌绝不经过这里（只读缓存里的快照，快照里本来就没有令牌）。
func (s *Server) handleCentralNodesAggregate(w http.ResponseWriter, r *http.Request) {
	s.centralMu.Lock()
	defer s.centralMu.Unlock()
	if rid := strings.TrimSpace(r.URL.Query().Get("remote_id")); rid != "" {
		rem := s.remoteByID(rid)
		if rem == nil {
			writeJSON(w, 404, errResp("没有登记过这台远程面板"))
			return
		}
		writeJSON(w, 200, okResp(centralRemoteNodesEntry(*rem, s.centralCache[rid])))
		return
	}
	entries := []map[string]any{}
	for _, rem := range s.cfg.Remotes {
		entries = append(entries, centralRemoteNodesEntry(rem, s.centralCache[rem.ID]))
	}
	// local 本机面板的节点（v1.4.19，切片 9）：集中管理页同一页可见；
	// 现场算（不走缓存），与远程快照里的 nodes 同形状。
	writeJSON(w, 200, okResp(map[string]any{"remotes": entries, "local": map[string]any{"nodes": s.centralNodesSummary()}}))
}

// handleCentralNodeDetailProxy GET /api/v1/central/nodes/{remote_id}/{node_id}：
// 管理面板经代理看某远程某节点的详情（v1.4.19，切片 9）。只经完整面板
// 代理：调远程的 /api/v1/central/nodes/{node_id}（令牌认证），不直连
// 节点后端。远程低于 1.4.19 时按版本门提示升级。
func (s *Server) handleCentralNodeDetailProxy(w http.ResponseWriter, r *http.Request) {
	rem := s.remoteByID(r.PathValue("remote_id"))
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	nodeID, err := strconv.ParseInt(r.PathValue("node_id"), 10, 64)
	if err != nil || nodeID <= 0 {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	s.proxyRemoteRead(w, r, *rem, "/api/v1/central/nodes/"+strconv.FormatInt(nodeID, 10), 10*time.Second, "1.4.19")
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
		Landing     string `json:"landing"`
		Preset      string `json:"preset"`
		SBMode      string `json:"sb_mode"`
		CustomPorts string `json:"custom_ports"`
		WantUDP     bool   `json:"want_udp"`
		CertDomain  string `json:"cert_domain"`
		CertMode    string `json:"cert_mode"`
		// TrafficMultiplier 流量倍率（v1.3.40）：DisallowUnknownFields，
		// 这里不加字段，主面板代理加/编辑落地会被拦在本地。
		TrafficMultiplier float64        `json:"traffic_multiplier"`
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
	if req.TrafficMultiplier < 0 || req.TrafficMultiplier > maxTrafficMultiplier {
		return fmt.Errorf("流量倍率只能填 0–100（0 = 不改，留空默认 1）")
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
	s.pollOneRemoteAsync(rem)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(respBody)
}

// remoteVersionOK 检查缓存的远程版本是否满足最低版本：
// 缓存里没有版本时放行（靠远程的 404 兜底）；低于最低版本返回提示文案。
func (s *Server) remoteVersionOK(id, minVer string) (bool, string) {
	s.centralMu.Lock()
	snap := s.centralCache[id]
	s.centralMu.Unlock()
	if snap != nil && snap.Version != "" && isNewerVersion(minVer, snap.Version) {
		return false, fmt.Sprintf("远程面板版本太旧（%s，需要 %s+），请先升级远程面板", snap.Version, minVer)
	}
	return true, ""
}

// proxyRemoteWriteMinVer 代理一个写操作到远程面板（带版本门）：
// 缓存的远程版本低于 minVer 直接拒绝；远程返回 404 时也按 minVer 提示升级。
// 其余失败与成功都与 proxyRemoteWrite 一致。
func (s *Server) proxyRemoteWriteMinVer(w http.ResponseWriter, r *http.Request, rem RemotePanel, method, path string, body []byte, timeout time.Duration, minVer string) {
	if ok, msg := s.remoteVersionOK(rem.ID, minVer); !ok {
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "side": "remote"})
		return
	}
	status, respBody, err := s.remoteCall(rem, method, path, body, timeout)
	if err != nil {
		side, msg := remoteSideOf(err)
		if status == 404 {
			// 老面板没有这个接口：404 是版本问题，不是地址问题
			msg = fmt.Sprintf("远程面板版本太旧（需要 %s+），请先升级远程面板", minVer)
			side = "remote"
		}
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "side": side})
		return
	}
	// 远程面板的写操作成功后，刷新一次缓存的状态（落地数会变）。
	s.pollOneRemoteAsync(rem)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(respBody)
}

// proxyRemoteRead 代理一个读操作到远程面板：远程的返回原样写回；
// 缓存的远程版本低于 minVer 直接拒绝，远程 404 同样按 minVer 提示升级。
func (s *Server) proxyRemoteRead(w http.ResponseWriter, r *http.Request, rem RemotePanel, path string, timeout time.Duration, minVer string) {
	if ok, msg := s.remoteVersionOK(rem.ID, minVer); !ok {
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "side": "remote"})
		return
	}
	status, respBody, err := s.remoteCall(rem, "GET", path, nil, timeout)
	if err != nil {
		side, msg := remoteSideOf(err)
		if status == 404 {
			msg = fmt.Sprintf("远程面板版本太旧（需要 %s+），请先升级远程面板", minVer)
			side = "remote"
		}
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "side": side})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(respBody)
}

// remoteLandingIndex 校验代理操作里的落地编号（须是正整数）。
func remoteLandingIndex(r *http.Request) (int, bool) {
	index, err := strconv.Atoi(r.PathValue("index"))
	return index, err == nil && index >= 1
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
	s.pollOneRemoteAsync(*rem)
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

// ---------- 主面板端：代理操作（v1.2.8 补齐，与本地操作一一对应） ----------
// 以下接口都是 1.2.8 才有的远程接口：版本门统一用 proxyRemoteWriteMinVer /
// proxyRemoteRead（缓存版本低于 1.2.8 拒绝，远程 404 提示升级）。
// 校验规则与本地 handler 里的同一套（主面板先验一遍，远程再验一遍）。

// validateRemoteOrigLinkBody 补粘贴原始链接的二次校验（与本地 handleSetOrigLink 同规则）。
func validateRemoteOrigLinkBody(body []byte) error {
	var req struct {
		OrigLink string `json:"orig_link"`
		LinkName string `json:"link_name"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("请求格式不对")
	}
	if strings.TrimSpace(req.OrigLink) == "" {
		return fmt.Errorf("原始节点链接不能为空")
	}
	if _, err := parseNodeLink(strings.TrimSpace(req.OrigLink)); err != nil {
		return fmt.Errorf("原始节点链接不对：%v", err)
	}
	if req.LinkName != "" && (len(req.LinkName) > 64 || strings.ContainsAny(req.LinkName, "\n\r")) {
		return fmt.Errorf("节点名称太长或含有非法字符（不能超过 64 字，不能含换行）")
	}
	return nil
}

// validateRemoteRenewBody 续期的二次校验：天数 1–3650。
func validateRemoteRenewBody(body []byte) error {
	var req struct {
		Days int `json:"days"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Days < 1 || req.Days > 3650 {
		return fmt.Errorf("续期天数须在 1–3650 天之间")
	}
	return nil
}

// validateRemoteAddTrafficBody 续量的二次校验：字节数须大于 0。
func validateRemoteAddTrafficBody(body []byte) error {
	var req struct {
		Bytes int64 `json:"bytes"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Bytes <= 0 {
		return fmt.Errorf("续量字节数须大于 0")
	}
	return nil
}

// validateRemoteSetTrafficBody 流量限制的二次校验（与本地 handleSetTraffic 同规则）。
func validateRemoteSetTrafficBody(body []byte) error {
	var req struct {
		LimitBytes     int64  `json:"limit_bytes"`
		LimitMode      string `json:"limit_mode"`
		SpeedLimitMbps int    `json:"speed_limit_mbps"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("请求格式不对")
	}
	if req.LimitBytes < 0 || req.SpeedLimitMbps < 0 {
		return fmt.Errorf("数值不能为负")
	}
	if req.LimitMode != "" && req.LimitMode != "total" && req.LimitMode != "monthly" {
		return fmt.Errorf("流量限制模式只能是 total 或 monthly")
	}
	return nil
}

// validateRemoteFixBody 一键修复的二次校验：问题码必填。
func validateRemoteFixBody(body []byte) error {
	var req struct {
		IssueCode string `json:"issue_code"`
		Landing   int    `json:"landing"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("请求格式不对")
	}
	if strings.TrimSpace(req.IssueCode) == "" {
		return fmt.Errorf("问题码不能为空")
	}
	return nil
}

// centralRemoteLandingOp 代理类落地写操作的公共前置：取远程面板 + 落地编号 +
// 读请求体；失败直接写回，成功返回 (rem, index, body)。
func (s *Server) centralRemoteLandingOp(w http.ResponseWriter, r *http.Request) (RemotePanel, int, []byte, bool) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return RemotePanel{}, 0, nil, false
	}
	index, ok := remoteLandingIndex(r)
	if !ok {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "落地编号不对", "side": "local"})
		return RemotePanel{}, 0, nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, 400, errResp("请求读失败"))
		return RemotePanel{}, 0, nil, false
	}
	return *rem, index, body, true
}

func (s *Server) handleCentralRemoteUpdateLanding(w http.ResponseWriter, r *http.Request) {
	rem, index, body, ok := s.centralRemoteLandingOp(w, r)
	if !ok {
		return
	}
	// 编辑落地与加落地同一套请求体，复用加落地的二次校验
	//（duration_days=-1 表示保持不变，校验里已放行）。
	if err := validateRemoteAddLandingBody(body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "side": "local"})
		return
	}
	// 编辑可能重启 realm 并重探测证书，超时放宽到 120 秒。
	s.proxyRemoteWriteMinVer(w, r, rem, "PUT", "/api/v1/central/landings/"+strconv.Itoa(index), body, 120*time.Second, "1.2.8")
	s.audit(s.currentUser(r), "代理编辑落地", rem.Name+" / 落地 "+strconv.Itoa(index), "已转发到远程面板", "")
}

func (s *Server) handleCentralRemoteSetOrigLink(w http.ResponseWriter, r *http.Request) {
	rem, index, body, ok := s.centralRemoteLandingOp(w, r)
	if !ok {
		return
	}
	if err := validateRemoteOrigLinkBody(body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "side": "local"})
		return
	}
	s.proxyRemoteWriteMinVer(w, r, rem, "POST", "/api/v1/central/landings/"+strconv.Itoa(index)+"/orig-link", body, 60*time.Second, "1.2.8")
	s.audit(s.currentUser(r), "代理补粘贴原始链接", rem.Name+" / 落地 "+strconv.Itoa(index), "已转发到远程面板", "")
}

func (s *Server) handleCentralRemoteDeleteSubnode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	index, ok := remoteLandingIndex(r)
	if !ok {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "落地编号不对", "side": "local"})
		return
	}
	domain := strings.TrimSpace(r.PathValue("domain"))
	if domain == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "子节点域名为空", "side": "local"})
		return
	}
	s.proxyRemoteWriteMinVer(w, r, *rem, "DELETE", "/api/v1/central/landings/"+strconv.Itoa(index)+"/subnodes/"+domain, nil, 60*time.Second, "1.2.8")
	s.audit(s.currentUser(r), "代理删除子节点", rem.Name+" / 落地 "+strconv.Itoa(index)+" / "+domain, "已转发到远程面板", "")
}

func (s *Server) handleCentralRemoteRenew(w http.ResponseWriter, r *http.Request) {
	rem, index, body, ok := s.centralRemoteLandingOp(w, r)
	if !ok {
		return
	}
	if err := validateRemoteRenewBody(body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "side": "local"})
		return
	}
	s.proxyRemoteWriteMinVer(w, r, rem, "POST", "/api/v1/central/landings/"+strconv.Itoa(index)+"/renew", body, 60*time.Second, "1.2.8")
	s.audit(s.currentUser(r), "代理续期", rem.Name+" / 落地 "+strconv.Itoa(index), "已转发到远程面板", "")
}

func (s *Server) handleCentralRemoteAddTraffic(w http.ResponseWriter, r *http.Request) {
	rem, index, body, ok := s.centralRemoteLandingOp(w, r)
	if !ok {
		return
	}
	if err := validateRemoteAddTrafficBody(body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "side": "local"})
		return
	}
	s.proxyRemoteWriteMinVer(w, r, rem, "POST", "/api/v1/central/landings/"+strconv.Itoa(index)+"/add-traffic", body, 60*time.Second, "1.2.8")
	s.audit(s.currentUser(r), "代理续量", rem.Name+" / 落地 "+strconv.Itoa(index), "已转发到远程面板", "")
}

func (s *Server) handleCentralRemoteSetTraffic(w http.ResponseWriter, r *http.Request) {
	rem, index, body, ok := s.centralRemoteLandingOp(w, r)
	if !ok {
		return
	}
	if err := validateRemoteSetTrafficBody(body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "side": "local"})
		return
	}
	s.proxyRemoteWriteMinVer(w, r, rem, "POST", "/api/v1/central/landings/"+strconv.Itoa(index)+"/traffic", body, 60*time.Second, "1.2.8")
	s.audit(s.currentUser(r), "代理设置流量限制", rem.Name+" / 落地 "+strconv.Itoa(index), "已转发到远程面板", "")
}

func (s *Server) handleCentralRemoteCerts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	// 证书探测要跑 check.sh + 逐落地握手，超时放宽到 90 秒。
	s.proxyRemoteRead(w, r, *rem, "/api/v1/central/certs", 90*time.Second, "1.2.8")
}

func (s *Server) handleCentralRemoteTraffic(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	path := "/api/v1/central/traffic"
	if q := r.URL.RawQuery; q != "" {
		path += "?" + q
	}
	s.proxyRemoteRead(w, r, *rem, path, 30*time.Second, "1.2.8")
}

func (s *Server) handleCentralRemoteCheck(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rem := s.remoteByID(id)
	if rem == nil {
		writeJSON(w, 404, errResp("没有登记过这台远程面板"))
		return
	}
	// 体检跑 check.sh，超时放宽到 90 秒。
	s.proxyRemoteWriteMinVer(w, r, *rem, "POST", "/api/v1/central/check", []byte("{}"), 90*time.Second, "1.2.8")
	s.audit(s.currentUser(r), "代理远程体检", rem.Name, "已转发到远程面板", "")
}

func (s *Server) handleCentralRemoteFix(w http.ResponseWriter, r *http.Request) {
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
	if err := validateRemoteFixBody(body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "side": "local"})
		return
	}
	// 一键修复可能重启 realm，超时放宽到 120 秒。
	s.proxyRemoteWriteMinVer(w, r, *rem, "POST", "/api/v1/central/fix", body, 120*time.Second, "1.2.8")
	s.audit(s.currentUser(r), "代理一键修复", rem.Name, "已转发到远程面板", "")
}
