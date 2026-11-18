package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- 路由 ----------

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	// 静态前端（panel/web 内嵌：index.html + app.js + style.css）
	mux.HandleFunc("/", s.handleRoot)
	// 认证
	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("GET /api/v1/me", s.requireAuth(s.handleMe))
	// 总览与体检
	mux.HandleFunc("GET /api/v1/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("POST /api/v1/check", s.requireAuth(s.handleCheck))
	mux.HandleFunc("POST /api/v1/fix", s.requireAuth(s.handleFix))
	// 落地
	mux.HandleFunc("POST /api/v1/landings", s.requireAuth(s.handleAddLanding))
	mux.HandleFunc("POST /api/v1/landings/{index}/add-node", s.requireAuth(s.handleAddNode))
	mux.HandleFunc("POST /api/v1/landings/{index}/renew", s.requireAuth(s.handleRenew))
	mux.HandleFunc("POST /api/v1/landings/{index}/add-traffic", s.requireAuth(s.handleAddTraffic))
	mux.HandleFunc("POST /api/v1/landings/{index}/traffic", s.requireAuth(s.handleSetTraffic))
	mux.HandleFunc("DELETE /api/v1/landings/{index}", s.requireAuth(s.handleDeleteLanding))
	mux.HandleFunc("DELETE /api/v1/landings/{index}/subnodes/{domain}", s.requireAuth(s.handleDeleteSubnode))
	// 回收站
	mux.HandleFunc("GET /api/v1/recycle", s.requireAuth(s.handleRecycleList))
	mux.HandleFunc("POST /api/v1/recycle/{id}/restore", s.requireAuth(s.handleRecycleRestore))
	mux.HandleFunc("DELETE /api/v1/recycle/{id}", s.requireAuth(s.handleRecycleDelete))
	// 节点链接
	mux.HandleFunc("GET /api/v1/links", s.requireAuth(s.handleLinks))
	mux.HandleFunc("POST /api/v1/links/twin", s.requireAuth(s.handleTwin))
	// 证书
	mux.HandleFunc("GET /api/v1/certs", s.requireAuth(s.handleCerts))
	mux.HandleFunc("GET /api/v1/certs/renew-guide", s.requireAuth(s.handleRenewGuide))
	mux.HandleFunc("POST /api/v1/certs/renew-guide/progress", s.requireAuth(s.handleRenewProgress))
	// 服务与日志
	mux.HandleFunc("POST /api/v1/service/{action}", s.requireAuth(s.handleService))
	mux.HandleFunc("GET /api/v1/logs", s.requireAuth(s.handleLogs))
	mux.HandleFunc("GET /api/v1/ports", s.requireAuth(s.handlePorts))
	// 设置
	mux.HandleFunc("GET /api/v1/settings", s.requireAuth(s.handleSettings))
	mux.HandleFunc("POST /api/v1/settings/password", s.requireAuth(s.handleChangePassword))
	mux.HandleFunc("POST /api/v1/settings/port", s.requireAuth(s.handleChangePort))
	mux.HandleFunc("POST /api/v1/settings/ip-whitelist", s.requireAuth(s.handleIPWhitelist))
	mux.HandleFunc("POST /api/v1/settings/https", s.requireAuth(s.handleHTTPS))
	mux.HandleFunc("POST /api/v1/settings/cloudflare", s.requireAuth(s.handleCloudflare))
	mux.HandleFunc("POST /api/v1/settings/relay-addr", s.requireAuth(s.handleRelayAddr))
	mux.HandleFunc("POST /api/v1/settings/telegram", s.requireAuth(s.handleTelegram))
	mux.HandleFunc("POST /api/v1/settings/telegram/test", s.requireAuth(s.handleTelegramTest))
	mux.HandleFunc("GET /api/v1/update/check", s.requireAuth(s.handleUpdateCheck))
	mux.HandleFunc("POST /api/v1/update/apply", s.requireAuth(s.handleUpdateApply))
	// 机器状态监控告警（v1.1.10）
	mux.HandleFunc("GET /api/v1/monitor", s.requireAuth(s.handleMonitor))
	mux.HandleFunc("POST /api/v1/settings/monitor", s.requireAuth(s.handleMonitorSettings))
	// 落地延迟探测（v1.1.11）
	mux.HandleFunc("GET /api/v1/latency", s.requireAuth(s.handleLatency))
	mux.HandleFunc("POST /api/v1/settings/latency", s.requireAuth(s.handleLatencySettings))
	// 多台集中管理（v1.2.0）：被管端令牌认证 + 主面板端会话认证
	mux.HandleFunc("GET /api/v1/settings/central-token", s.requireAuth(s.handleCentralTokenGet))
	mux.HandleFunc("POST /api/v1/settings/central-token/generate", s.requireAuth(s.handleCentralTokenGenerate))
	mux.HandleFunc("POST /api/v1/settings/central-token/revoke", s.requireAuth(s.handleCentralTokenRevoke))
	mux.HandleFunc("GET /api/v1/central/status", s.requireCentralToken(s.handleCentralStatus))
	mux.HandleFunc("GET /api/v1/central/links", s.requireCentralToken(s.handleLinks))
	mux.HandleFunc("POST /api/v1/central/landings", s.requireCentralToken(s.handleAddLanding))
	mux.HandleFunc("DELETE /api/v1/central/landings/{index}", s.requireCentralToken(s.handleDeleteLanding))
	mux.HandleFunc("POST /api/v1/central/service/{action}", s.requireCentralToken(s.handleService))
	mux.HandleFunc("GET /api/v1/central/remotes", s.requireAuth(s.handleCentralRemotesList))
	mux.HandleFunc("POST /api/v1/central/remotes", s.requireAuth(s.handleCentralRemotesAdd))
	mux.HandleFunc("DELETE /api/v1/central/remotes/{id}", s.requireAuth(s.handleCentralRemotesDelete))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/poll", s.requireAuth(s.handleCentralRemotesPoll))
	mux.HandleFunc("GET /api/v1/central/remotes/{id}/landings", s.requireAuth(s.handleCentralRemoteLandings))
	mux.HandleFunc("GET /api/v1/central/remotes/{id}/links", s.requireAuth(s.handleCentralRemoteLinks))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/landings", s.requireAuth(s.handleCentralRemoteAddLanding))
	mux.HandleFunc("DELETE /api/v1/central/remotes/{id}/landings/{index}", s.requireAuth(s.handleCentralRemoteDeleteLanding))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/service/{action}", s.requireAuth(s.handleCentralRemoteService))
	// 端口管理 / 流量 / 备份导出导入 / 审计
	mux.HandleFunc("GET /api/v1/firewall", s.requireAuth(s.handleFirewallGet))
	mux.HandleFunc("POST /api/v1/firewall", s.requireAuth(s.handleFirewallPost))
	mux.HandleFunc("GET /api/v1/traffic", s.requireAuth(s.handleTrafficGet))
	mux.HandleFunc("POST /api/v1/traffic/reset", s.requireAuth(s.handleTrafficReset))
	mux.HandleFunc("GET /api/v1/backups", s.requireAuth(s.handleBackupsList))
	mux.HandleFunc("POST /api/v1/backups", s.requireAuth(s.handleBackupsCreate))
	mux.HandleFunc("POST /api/v1/backups/{id}/restore", s.requireAuth(s.handleBackupRestore))
	mux.HandleFunc("GET /api/v1/export", s.requireAuth(s.handleExport))
	mux.HandleFunc("POST /api/v1/import", s.requireAuth(s.handleImport))
	mux.HandleFunc("GET /api/v1/audit", s.requireAuth(s.handleAudit))
	return s.ipWhitelistMiddleware(mux)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	// 前端文件由 embed 提供（panel/web/index.html 等），按路径分发；
	// API 路由（/api/v1/...）在 ServeMux 里更具体，优先于这里的 catch-all。
	// 注意：这里不能按路径 404——/style.css、/app.js 也要走 serveEmbeddedWeb。
	serveEmbeddedWeb(w, r)
}

// requireAuth 会话校验（登录接口除外）。
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.sessions.get(r); !ok {
			writeJSON(w, 401, errResp("未登录，请先登录"))
			return
		}
		next(w, r)
	}
}

// ipWhitelistMiddleware IP 白名单：空=不限制；白名单外任何接口都 403。
func (s *Server) ipWhitelistMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.IPWhitelist) > 0 {
			ip := clientIP(r)
			allowed := false
			for _, wip := range s.cfg.IPWhitelist {
				if wip == ip {
					allowed = true
					break
				}
			}
			if !allowed {
				writeJSON(w, 403, errResp("你的 IP 不在面板白名单里，无法访问"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) currentUser(r *http.Request) string {
	// 令牌进来的请求（central/*）记 central，审计里能分清是主面板代理的操作
	if v, ok := r.Context().Value(ctxUserKey).(string); ok && v != "" {
		return v
	}
	if u, ok := s.sessions.get(r); ok {
		return u
	}
	return "admin"
}

// tryOpLock 写操作锁：占用时返回 409。
func (s *Server) tryOpLock(w http.ResponseWriter) bool {
	if !s.opMu.TryLock() {
		writeJSON(w, 409, errResp("正在操作，请稍等"))
		return false
	}
	return true
}

func pathIndex(r *http.Request) (int, error) {
	return strconv.Atoi(r.PathValue("index"))
}

func (s *Server) landingByIndex(index int) (*Landing, *RelayConf, error) {
	rc, _, err := s.readRelayConf()
	if err != nil {
		return nil, nil, fmt.Errorf("relay.conf 不存在或读不到：中转还没装好")
	}
	for i := range rc.Landings {
		if rc.Landings[i].Index == index {
			return &rc.Landings[i], rc, nil
		}
	}
	return nil, rc, fmt.Errorf("没有编号为 %d 的落地", index)
}

// ---------- 认证 ----------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.sessions.loginLocked(ip) {
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
	if req.Username == s.cfg.Username && verifyPassword(req.Password, s.cfg.PasswordHash) {
		s.sessions.loginOK(ip)
		tok := s.sessions.create(req.Username)
		http.SetCookie(w, &http.Cookie{Name: "relay_panel_session", Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 24 * 3600})
		s.audit(req.Username, "登录", "", "成功", "")
		writeJSON(w, 200, okResp(nil))
		return
	}
	s.sessions.loginFailed(ip)
	s.audit(req.Username, "登录", "", "失败", "密码错误")
	writeJSON(w, 401, errResp("账号或密码不对"))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sessions.destroy(r)
	http.SetCookie(w, &http.Cookie{Name: "relay_panel_session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, okResp(nil))
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := s.sessions.get(r)
	writeJSON(w, 200, okResp(map[string]any{"username": u, "version": Version}))
}

// ---------- 总览与体检 ----------

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	rc, _, err := s.readRelayConf()
	if err != nil {
		writeJSON(w, 200, okResp(map[string]any{
			"realm":   map[string]any{"running": realmRunning(), "version": realmVersion(), "uptime_seconds": realmUptimeSeconds(), "bbr": bbrEnabled()},
			"share80": false, "share443": false, "panel_version": Version, "relay_addr": "",
			"landings": []any{}, "note": "relay.conf 不存在：中转还没装好",
		}))
		return
	}
	// 证书信息（check.sh --json 的 certs 优先，失败则逐落地 TLS 直取）
	certByIndex := map[int]certInfo{}
	if res, err := s.runScript("check.sh", 60*time.Second, nil, "--json"); err == nil {
		var cj struct {
			Certs []struct {
				Index    int     `json:"index"`
				DaysLeft *int    `json:"days_left"`
				Expiry   *string `json:"expiry"`
			} `json:"certs"`
		}
		if json.Unmarshal([]byte(res.Stdout), &cj) == nil {
			for _, c := range cj.Certs {
				ci := certInfo{DaysLeft: c.DaysLeft}
				if c.Expiry != nil {
					ci.Expiry = *c.Expiry
				}
				certByIndex[c.Index] = ci
			}
		}
	}
	var landings []any
	// 总览打开时若有落地还没探测结果，补跑一轮（有 3 秒上限），保证卡片能看到数
	s.ensureLatencyProbed()
	for _, l := range rc.Landings {
		ext := s.state.ext(l.Landing)
		// 在线/延迟：用后台延迟探测的结果（探测目标是落地地址:落地端口，
		// 1.1.11 起不再测中转机本地监听端口）；没结果时为未知（null）。
		var online any
		var latencyMs any
		var latErr, latCheckedAt, latTarget string
		if lat := s.latency.get(latencyStoreKey(l)); lat != nil {
			online = lat.Online
			latencyMs = lat.LatencyMs
			latErr, latCheckedAt, latTarget = lat.Error, lat.CheckedAt, lat.Target
		}
		ci, ok := certByIndex[l.Index]
		if !ok {
			// 回退：TLS 直取
			tlsPort := 0
			for _, p := range l.Ports {
				if p.Proto == "tcp" && p.Listen == 443 {
					tlsPort = p.Listen
				}
			}
			if tlsPort > 0 {
				ci = fetchCert(tlsPort, l.CertDomain)
			}
		}
		renewMode, _ := renewModeFor(l, rc.Share443)
		lt := s.traffic.get(l.Landing)
		used := trafficUsed(lt, ext.TrafficLimitMode)
		pct := 0.0
		if ext.TrafficLimitBytes > 0 {
			pct = float64(used) / float64(ext.TrafficLimitBytes) * 100
		}
		var daysLeft any
		if ext.Expiry == "" {
			daysLeft = nil
		} else if t, err := time.Parse(time.RFC3339, ext.Expiry); err == nil {
			daysLeft = int(time.Until(t).Hours() / 24)
		}
		subnodes := ext.SubNodes
		if subnodes == nil {
			subnodes = []SubNode{}
		}
		landings = append(landings, map[string]any{
			"index": l.Index, "landing": l.Landing, "domain": l.Domain, "cert_domain": l.CertDomain,
			"preset": l.Preset, "mode_note": l.ModeNote, "mode": l.Mode, "ports": l.Ports,
			"online": online, "latency_ms": latencyMs,
			"latency_error": latErr, "latency_checked_at": latCheckedAt, "latency_target": latTarget,
			"cert_days_left": ci.DaysLeft, "cert_expiry": ci.Expiry, "renew_mode": renewMode,
			"expiry": ext.Expiry, "days_left": daysLeft,
			"traffic": map[string]any{
				"today_up": dayUp(lt), "today_down": dayDown(lt),
				"month_up": lt.UpMonth, "month_down": lt.DownMonth,
				"total_up": lt.UpTotal, "total_down": lt.DownTotal,
				"limit_bytes": ext.TrafficLimitBytes, "limit_mode": ext.TrafficLimitMode,
				"used_bytes": used, "pct": pct,
			},
			"disabled": ext.Disabled, "disabled_reason": ext.DisabledReason, "subnodes": subnodes,
		})
	}
	if landings == nil {
		landings = []any{}
	}
	writeJSON(w, 200, okResp(map[string]any{
		"realm":   map[string]any{"running": realmRunning(), "version": realmVersion(), "uptime_seconds": realmUptimeSeconds(), "bbr": bbrEnabled()},
		"share80": rc.Share80, "share443": rc.Share443, "panel_version": Version, "relay_addr": rc.RelayAddr, "landings": landings,
	}))
}

func dayUp(lt *landingTraffic) int64 {
	if len(lt.Daily) == 0 {
		return 0
	}
	if lt.Daily[len(lt.Daily)-1].Date == time.Now().Format("2006-01-02") {
		return lt.Daily[len(lt.Daily)-1].Up
	}
	return 0
}
func dayDown(lt *landingTraffic) int64 {
	if len(lt.Daily) == 0 {
		return 0
	}
	if lt.Daily[len(lt.Daily)-1].Date == time.Now().Format("2006-01-02") {
		return lt.Daily[len(lt.Daily)-1].Down
	}
	return 0
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	res, err := s.runScript("check.sh", 60*time.Second, nil, "--json")
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "check.sh 跑失败：" + sanitize(res.Stdout+res.Stderr)})
		return
	}
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(res.Stdout), &raw); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "check.sh 输出不是合法 JSON"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(res.Stdout))
}

func (s *Server) handleFix(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IssueCode string `json:"issue_code"`
		Landing   int    `json:"landing"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	user := s.currentUser(r)
	switch req.IssueCode {
	case "restart_realm", "realm_down", "port_not_listening":
		if !s.tryOpLock(w) {
			return
		}
		defer s.opMu.Unlock()
		if err := s.restartRealm(); err != nil {
			s.audit(user, "一键修复", fmt.Sprintf("落地 %d", req.Landing), "失败", err.Error())
			writeJSON(w, 200, errResp("重启 realm 失败："+err.Error()))
			return
		}
		s.audit(user, "一键修复", fmt.Sprintf("落地 %d", req.Landing), "成功", "restart_realm")
		writeJSON(w, 200, okResp(map[string]any{"msg": "realm 已重启"}))
	case "restart_proxy":
		if !s.tryOpLock(w) {
			return
		}
		defer s.opMu.Unlock()
		if err := s.regenProxies(); err != nil {
			writeJSON(w, 200, errResp("重启分流组件失败："+err.Error()))
			return
		}
		writeJSON(w, 200, okResp(map[string]any{"msg": "分流组件已重启"}))
	case "choose_share":
		writeJSON(w, 200, map[string]any{"ok": true, "need_choice": true, "options": shareOptions(true, true)})
	case "manual_renew", "cert_expiring", "cert_expired", "fingerprint_mismatch":
		writeJSON(w, 200, map[string]any{"ok": true, "need_manual": true, "guide": "/api/v1/certs/renew-guide"})
	default:
		writeJSON(w, 400, errResp("不知道怎么修这个问题："+req.IssueCode))
	}
}

// regenProxies 调 proxy.sh 重生成分流配置（契约 §4）；proxy.sh 不在时用 sed 级最小改动回退。
func (s *Server) regenProxies() error {
	proxy := filepath.Join(s.env.KitDir, "proxy.sh")
	if _, err := os.Stat(proxy); err == nil {
		env := []string{"REALM_DIR=" + s.env.RealmDir}
		if _, err := s.runScript("proxy.sh", 60*time.Second, env, "--restart-proxies"); err != nil {
			return fmt.Errorf("proxy.sh --restart-proxies 失败：%v", err)
		}
		return nil
	}
	// 回退：只重载已启用的组件并校验
	rc, _, err := s.readRelayConf()
	if err != nil {
		return err
	}
	if rc.Share80 {
		if err := runCmd(nil, 10*time.Second, "nginx", "-t"); err != nil {
			return fmt.Errorf("nginx -t 没过：%v", err)
		}
		_ = runCmd(nil, 10*time.Second, "systemctl", "reload", "nginx")
	}
	if rc.Share443 {
		if err := runCmd(nil, 10*time.Second, "haproxy", "-c", "-f", "/etc/haproxy/haproxy.cfg"); err != nil {
			return fmt.Errorf("haproxy -c 没过：%v", err)
		}
		_ = runCmd(nil, 10*time.Second, "systemctl", "reload", "haproxy")
	}
	return nil
}

func shareOptions(c80, c443 bool) []map[string]string {
	opts := []map[string]string{
		{"id": "1", "title": "改监听走高位端口", "desc": "最简单，什么都不用准备，装完就能用。第二个落地的 443 会改成 10443 这类高位端口，客户端链接要带上高位端口；证书续签要手动：到期前把域名 A 记录改回指向落地、续完再改回来。"},
	}
	if c80 {
		opts = append(opts, map[string]string{"id": "2", "title": "80 按域名分流", "desc": "中转机上跑一个轻量代理监听 80，按域名把验证请求分到对应落地的 80，几个落地的证书续签都自动通，不用令牌、落地不用改。代价是中转 80 不再是纯转发、多一个代理组件；443 还是各走各的高位端口。"})
	}
	if c443 {
		opts = append(opts, map[string]string{"id": "3", "title": "443 按 SNI 分流", "desc": "中转机按 TLS 握手里的域名分流，几个落地共用中转的 443，客户端链接不用带高位端口，证书续签走 443 自动通。效果最彻底，但改动最大：中转 443 不再是 realm 纯转发，要多装一个分流组件。"})
	}
	if c80 && c443 {
		opts = append(opts, map[string]string{"id": "4", "title": "80 分流 + 443 分流一起用", "desc": "2 和 3 一起：80 和 443 都能共用，几个落地续签都自动通，客户端链接也不用带高位端口。代价是中转机上要多两个分流组件，80 和 443 都不再是 realm 纯转发。"})
	}
	return opts
}

// ---------- 添加落地 ----------

func (s *Server) handleAddLanding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Landing           string         `json:"landing"`
		Preset            string         `json:"preset"`
		SBMode            string         `json:"sb_mode"`
		CustomPorts       string         `json:"custom_ports"`
		WantUDP           bool           `json:"want_udp"`
		CertDomain        string         `json:"cert_domain"`
		DurationDays      int            `json:"duration_days"`
		TrafficLimitBytes int64          `json:"traffic_limit_bytes"`
		TrafficLimitMode  string         `json:"traffic_limit_mode"`
		SpeedLimitMbps    int            `json:"speed_limit_mbps"`
		ShareMode         string         `json:"share_mode"`
		ListenOverrides   map[string]int `json:"listen_overrides"`
		Mode              string         `json:"mode"`
		RelayAddr         string         `json:"relay_addr"`
		DoorPortOverrides map[string]int `json:"door_port_overrides"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if !validIPOrDomain(req.Landing) {
		writeJSON(w, 400, errResp("落地地址不对：填 IP 或域名"))
		return
	}
	if req.Preset != "1" && req.Preset != "2" && req.Preset != "3" {
		writeJSON(w, 400, errResp("预设只能是 1/2/3"))
		return
	}
	if req.Preset == "2" && req.SBMode != "" && req.SBMode != "1" && req.SBMode != "2" {
		writeJSON(w, 400, errResp("sing-box 模式只能是 1（TLS）或 2（明文）"))
		return
	}
	if req.Preset == "3" {
		if _, err := parseCustomPorts(req.CustomPorts, req.WantUDP); err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		}
	}
	if req.CertDomain != "" && !validDomain(req.CertDomain) {
		writeJSON(w, 400, errResp("证书域名格式不对"))
		return
	}
	if req.TrafficLimitMode != "" && req.TrafficLimitMode != "total" && req.TrafficLimitMode != "monthly" {
		writeJSON(w, 400, errResp("流量限制模式只能是 total 或 monthly"))
		return
	}
	if req.TrafficLimitBytes < 0 || req.SpeedLimitMbps < 0 || req.DurationDays < 0 {
		writeJSON(w, 400, errResp("数值不能为负"))
		return
	}
	if req.ShareMode != "" && req.ShareMode != "1" && req.ShareMode != "2" && req.ShareMode != "3" && req.ShareMode != "4" {
		writeJSON(w, 400, errResp("分流方式只能是 1/2/3/4"))
		return
	}
	if req.Mode != "" && req.Mode != "standard" && req.Mode != "door" {
		writeJSON(w, 400, errResp("模式只能是 standard（标准模式）或 door（任意门模式）"))
		return
	}
	if req.Mode == "" {
		req.Mode = "standard"
	}
	if req.RelayAddr != "" && !validIPOrDomain(req.RelayAddr) {
		writeJSON(w, 400, errResp("中转地址不对：填 IP 或域名"))
		return
	}
	// door_port_overrides：键是落地端口、值是转发端口，统一归一化成 map[int]int。
	doorOverridesReq := map[int]int{}
	for k, v := range req.DoorPortOverrides {
		rp, err := strconv.Atoi(strings.TrimSpace(k))
		if err != nil || !validPort(rp) || !validPort(v) {
			writeJSON(w, 400, errResp("转发端口覆盖格式不对：键和值都须是 1-65535 的端口，如 {\"443\": 30443}"))
			return
		}
		doorOverridesReq[rp] = v
	}
	// 预检端口
	ports := presetPorts(req.Preset, req.SBMode, req.CustomPorts, req.WantUDP)
	rc, _, _ := s.readRelayConf()
	doorNote := ""
	doorOverrides := map[int]int{}
	if req.Mode == "door" {
		// 任意门模式：先按落地端口 +20000 映射转发端口，再验冲突；不走标准模式的分流选择。
		ports = doorMapPorts(ports)
		if len(doorOverridesReq) > 0 {
			ports = applyDoorPortOverrides(ports, doorOverridesReq)
			doorOverrides = doorOverridesReq
			if conflicts, _, _ := s.checkPortConflicts(ports, rc); len(conflicts) > 0 {
				writeJSON(w, 400, errResp("端口已被占用："+strings.Join(conflicts, ", ")+"。改一下转发端口覆盖再提交。"))
				return
			}
		} else if newPorts, bumps, err := s.bumpDoorPorts(ports, rc); err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		} else {
			ports = newPorts
			if len(bumps) > 0 {
				var notes []string
				for _, b := range bumps {
					notes = append(notes, fmt.Sprintf("转发端口 %d 已被占用，已改用 %d", b.Old, b.New))
					doorOverrides[b.Remote] = b.New
				}
				doorNote = strings.Join(notes, "；")
			}
		}
	} else {
		conflicts, realm80, realm443 := s.checkPortConflicts(ports, rc)
		if len(conflicts) > 0 && req.ShareMode == "" && len(req.ListenOverrides) == 0 {
			if realm80 || realm443 {
				writeJSON(w, 200, map[string]any{"ok": false, "need_choice": true,
					"conflicts":           conflicts,
					"conflict_with_realm": map[string]bool{"80": realm80, "443": realm443},
					"options":             shareOptions(realm80, realm443)})
				return
			}
			writeJSON(w, 400, errResp("端口已被占用："+strings.Join(conflicts, ", ")+"。先停掉占端口的程序，或用 listen_overrides 改监听端口。"))
			return
		}
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	user := s.currentUser(r)
	if _, err := s.backupNow("添加落地前自动备份", "realm"); err != nil {
		writeJSON(w, 500, errResp("备份失败，操作已取消："+err.Error()))
		return
	}
	env := []string{
		"LANDING=" + req.Landing, "PRESET=" + req.Preset,
		"SB_MODE=" + req.SBMode, "CUSTOM_PORTS=" + req.CustomPorts,
		"WANT_UDP=" + boolToYN(req.WantUDP), "CERT_DOMAIN=" + req.CertDomain,
		"ADD_ANOTHER=N",
	}
	if req.ShareMode != "" && req.Mode != "door" {
		env = append(env, "SHARE_MODE="+req.ShareMode)
	}
	if len(req.ListenOverrides) > 0 && req.Mode != "door" {
		var parts []string
		for k := range req.ListenOverrides {
			parts = append(parts, k)
		}
		sort.Strings(parts)
		var ov []string
		for _, k := range parts {
			ov = append(ov, fmt.Sprintf("%s:%d", k, req.ListenOverrides[k]))
		}
		env = append(env, "LISTEN_OVERRIDES="+strings.Join(ov, ","))
	}
	if req.Mode == "door" {
		env = append(env, "RELAY_MODE=door")
		if req.RelayAddr != "" {
			env = append(env, "RELAY_ADDR="+req.RelayAddr)
		}
		if len(doorOverrides) > 0 {
			env = append(env, "DOOR_PORT_OVERRIDES="+formatDoorOverrides(doorOverrides))
		}
	}
	// 中转还没装（relay.conf 不存在）时改跑完整安装（不带 --add），
	// 面板里加第一个落地就把中转一起装好；完整安装要下载 realm、建服务，给更长的超时。
	// 已装过中转时走 --add，只追加一个落地。
	relayInstalled := rc != nil
	installArgs := []string{"--add"}
	installTimeout := 300 * time.Second
	if !relayInstalled {
		installArgs = nil
		installTimeout = 600 * time.Second
	}
	res, err := s.runScript("install.sh", installTimeout, env, installArgs...)
	if err != nil {
		s.audit(user, "添加落地", req.Landing, "失败", sanitize(res.Stdout+res.Stderr))
		writeJSON(w, 200, errResp("添加落地失败："+sanitize(lastLines(res.Stdout+res.Stderr, 5))))
		return
	}
	// 成功后写扩展状态
	s.stateMu.Lock()
	ext := s.state.ext(req.Landing)
	if req.DurationDays > 0 {
		ext.Expiry = time.Now().Add(time.Duration(req.DurationDays) * 24 * time.Hour).UTC().Format(time.RFC3339)
	}
	ext.TrafficLimitBytes = req.TrafficLimitBytes
	ext.TrafficLimitMode = req.TrafficLimitMode
	ext.SpeedLimitMbps = req.SpeedLimitMbps
	s.stateMu.Unlock()
	_ = s.saveState()
	// 跑 --nodes 生成链接（失败不阻塞）
	_, _ = s.runScript("install.sh", 120*time.Second, nil, "--nodes")
	// 读 nodes.txt 返回新落地链接
	groups := []linkGroup{}
	if b, err := os.ReadFile(s.nodesFilePath()); err == nil {
		groups = parseNodesTxt(b)
	}
	// 找新落地的分组（最后一个匹配该 landing 的）
	var links []labelledLink
	landingIndex := 0
	if rc2, _, err := s.readRelayConf(); err == nil {
		landingIndex = len(rc2.Landings)
		for _, g := range groups {
			if g.Landing == req.Landing {
				links = g.Links
				landingIndex = g.LandingIndex
			}
		}
	}
	qr := map[string]string{}
	for _, l := range links {
		if q, err := qrDataURI(l.Link); err == nil {
			qr[l.Label] = q
		}
	}
	if qr == nil {
		qr = map[string]string{}
	}
	// 流量链重建（best-effort）
	if rc2, _, err := s.readRelayConf(); err == nil {
		_ = s.ensureTrafficChains(rc2.Landings)
		if landingIndex > 0 {
			if l, _, err := s.landingByIndex(landingIndex); err == nil && req.SpeedLimitMbps > 0 {
				if err := s.applySpeedLimit(*l, req.SpeedLimitMbps); err != nil {
					// 限速失败不阻塞，只提示
					s.audit(user, "添加落地", req.Landing, "成功（限速未生效）", err.Error())
				}
			}
		}
	}
	s.audit(user, "添加落地", req.Landing, "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"landing_index": landingIndex, "links": links, "qr": qr, "door_note": doorNote}))
}

func boolToYN(b bool) string {
	if b {
		return "y"
	}
	return "N"
}

func presetPorts(preset, sbMode, customPorts string, wantUDP bool) []PortSpec {
	switch preset {
	case "1":
		return parsePorts("tcp:80:80 tcp:443:443")
	case "2":
		if sbMode == "2" {
			return parsePorts("tcp:8080:8080 tcp:8081:8081 tcp:8082:8082 tcp:8388:8388 udp:8388:8388")
		}
		return parsePorts("tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388 udp:8443:8443 udp:8444:8444")
	case "3":
		ports, _ := parseCustomPorts(customPorts, wantUDP)
		return ports
	}
	return nil
}

// checkPortConflicts 预检：返回冲突 "80/tcp" 列表 + 是否 80/443 被已装落地（config.toml）占用。
func (s *Server) checkPortConflicts(ports []PortSpec, rc *RelayConf) ([]string, bool, bool) {
	listening := listeningPorts()
	inConfig := map[int]bool{}
	if rc != nil {
		for _, l := range rc.Landings {
			for _, p := range l.Ports {
				inConfig[p.Listen] = true
			}
		}
	}
	// SHARE 下的端口不算冲突
	skip := map[string]bool{}
	if rc != nil {
		if rc.Share80 {
			skip["tcp:80"] = true
		}
		if rc.Share443 {
			skip["tcp:443"] = true
		}
	}
	var conflicts []string
	seen := map[string]bool{}
	realm80, realm443 := false, false
	for _, p := range ports {
		key := fmt.Sprintf("%s:%d", p.Proto, p.Listen)
		if skip[key] {
			continue
		}
		if listening[key] || inConfig[p.Listen] {
			label := fmt.Sprintf("%d/%s", p.Listen, p.Proto)
			if !seen[label] {
				seen[label] = true
				conflicts = append(conflicts, label)
			}
			if inConfig[p.Listen] {
				if p.Listen == 80 {
					realm80 = true
				}
				if p.Listen == 443 {
					realm443 = true
				}
			}
		}
	}
	return conflicts, realm80, realm443
}

// ---------- 任意门模式：转发端口覆盖与冲突自动顺延 ----------

// applyDoorPortOverrides 把转发端口覆盖（键=落地端口，值=转发端口）套到 ports 上：
// 同一落地端口的 TCP/UDP 一起改（与 install.sh 的 apply_door_overrides 语义一致）。
func applyDoorPortOverrides(ports []PortSpec, overrides map[int]int) []PortSpec {
	out := make([]PortSpec, len(ports))
	copy(out, ports)
	for rp, nl := range overrides {
		for i := range out {
			if out[i].Remote == rp {
				out[i].Listen = nl
			}
		}
	}
	return out
}

// formatDoorOverrides 把覆盖 map 编成 install.sh 的 DOOR_PORT_OVERRIDES 串
// （"落地端口:转发端口"，逗号分隔，按落地端口从小到大排）。
func formatDoorOverrides(overrides map[int]int) string {
	keys := make([]int, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d:%d", k, overrides[k]))
	}
	return strings.Join(parts, ",")
}

// doorBump 一条自动顺延记录：落地端口 remote 的转发端口从 Old 改到 New。
type doorBump struct {
	Remote int
	Old    int
	New    int
}

// bumpDoorPorts 任意门模式的冲突自动顺延：按落地端口分组，组里任一端口冲突就整组顺延
// （转发端口是按落地端口定的，同一落地端口的 TCP/UDP 必须走同一个转发端口）。
// 冲突的转发端口 +10000，还冲突再 +1000，直到空闲且不与本批其他 listen 撞；
// 顺延后仍找不到空闲端口时返回错误。无冲突时返回原 ports 与空记录。
func (s *Server) bumpDoorPorts(ports []PortSpec, rc *RelayConf) ([]PortSpec, []doorBump, error) {
	conflicts, _, _ := s.checkPortConflicts(ports, rc)
	if len(conflicts) == 0 {
		return ports, nil, nil
	}
	conflictSet := map[string]bool{}
	for _, c := range conflicts {
		parts := strings.SplitN(c, "/", 2)
		if len(parts) == 2 {
			conflictSet[parts[1]+":"+parts[0]] = true
		}
	}
	listening := listeningPorts()
	inConfig := map[int]bool{}
	if rc != nil {
		for _, l := range rc.Landings {
			for _, p := range l.Ports {
				inConfig[p.Listen] = true
			}
		}
	}
	// 本批最终 listen 集合（proto:port），顺延时实时更新
	batch := map[string]bool{}
	for _, p := range ports {
		batch[fmt.Sprintf("%s:%d", p.Proto, p.Listen)] = true
	}
	out := make([]PortSpec, len(ports))
	copy(out, ports)
	// 按落地端口分组（保持首次出现顺序）
	var remotes []int
	groups := map[int][]int{}
	for i, p := range out {
		if _, ok := groups[p.Remote]; !ok {
			remotes = append(remotes, p.Remote)
		}
		groups[p.Remote] = append(groups[p.Remote], i)
	}
	var bumps []doorBump
	for _, remote := range remotes {
		idxs := groups[remote]
		hit := false
		for _, i := range idxs {
			if conflictSet[fmt.Sprintf("%s:%d", out[i].Proto, out[i].Listen)] {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		old := out[idxs[0]].Listen
		for _, i := range idxs {
			delete(batch, fmt.Sprintf("%s:%d", out[i].Proto, out[i].Listen))
		}
		cand := old + 10000
		ok := false
		for tries := 0; tries < 100; tries++ {
			if cand > 65535 {
				break
			}
			free := !inConfig[cand]
			if free {
				for _, i := range idxs {
					ck := fmt.Sprintf("%s:%d", out[i].Proto, cand)
					if listening[ck] || batch[ck] {
						free = false
						break
					}
				}
			}
			if free {
				ok = true
				break
			}
			cand += 1000
		}
		if !ok {
			return nil, nil, fmt.Errorf("转发端口 %d 已被占用，顺延后仍找不到空闲端口", old)
		}
		for _, i := range idxs {
			out[i].Listen = cand
			batch[fmt.Sprintf("%s:%d", out[i].Proto, cand)] = true
		}
		bumps = append(bumps, doorBump{Remote: remote, Old: old, New: cand})
	}
	return out, bumps, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ---------- 给落地加节点（向导式） ----------

func (s *Server) handleAddNode(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeJSON(w, 400, errResp("落地编号不对"))
		return
	}
	l, _, err := s.landingByIndex(index)
	if err != nil {
		writeJSON(w, 404, errResp(err.Error()))
		return
	}
	var req struct {
		Subdomain string `json:"subdomain"`
		NewLink   string `json:"new_link"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if req.Subdomain == "" || !validDomain(req.Subdomain) {
		writeJSON(w, 400, errResp("子域名格式不对"))
		return
	}
	// 第 1 步：只填子域名 → 返回落地机命令（按该落地的预选仓库）
	if req.NewLink == "" {
		cmd := landingAddNodeCmd(l.Preset, req.Subdomain)
		writeJSON(w, 200, okResp(map[string]any{"wizard": map[string]any{
			"step": 1, "landing_cmd": cmd,
			"note": "到落地机上跑完这条命令，把生成的新节点链接粘贴回来",
		}}))
		return
	}
	// 第 2 步：校验链接域名 == 子域名
	host, err := linkHostDomain(req.NewLink)
	if err != nil {
		writeJSON(w, 400, errResp("链接校验失败："+err.Error()))
		return
	}
	if !strings.EqualFold(host, req.Subdomain) {
		writeJSON(w, 400, errResp(fmt.Sprintf("链接里的域名（%s）与填的子域名（%s）不一致", host, req.Subdomain)))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	user := s.currentUser(r)
	// 调 --twin 生成直连
	landingIP := l.Landing
	if !isIP(landingIP) {
		if ips, err := net.LookupIP(landingIP); err == nil && len(ips) > 0 {
			landingIP = ips[0].String()
		}
	}
	pl, _ := parseNodeLink(req.NewLink)
	sni := ""
	if pl != nil {
		sni = pl.SNI
	}
	res, err := s.runScript("install.sh", 60*time.Second, nil, "--twin", req.NewLink, landingIP, sni)
	twin := strings.TrimSpace(res.Stdout)
	var links []labelledLink
	if err != nil || strings.HasPrefix(twin, "__MANUAL__:") {
		reason := strings.TrimPrefix(twin, "__MANUAL__:")
		if reason == "" {
			reason = "这条链接不会自动生成直连，请手动改地址"
		}
		links = []labelledLink{{Label: "中转", Link: req.NewLink}}
		_ = reason
	} else {
		links = []labelledLink{{Label: "中转", Link: req.NewLink}, {Label: "直连", Link: twin}}
	}
	// 登记到 subnodes
	s.stateMu.Lock()
	ext := s.state.ext(l.Landing)
	found := false
	for _, sn := range ext.SubNodes {
		if strings.EqualFold(sn.Domain, req.Subdomain) {
			found = true
		}
	}
	if !found {
		ext.SubNodes = append(ext.SubNodes, SubNode{Domain: req.Subdomain, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	}
	s.stateMu.Unlock()
	if err := s.saveState(); err != nil {
		writeJSON(w, 500, errResp("登记子节点失败："+err.Error()))
		return
	}
	qr := map[string]string{}
	for _, lk := range links {
		if q, err := qrDataURI(lk.Link); err == nil {
			qr[lk.Label] = q
		}
	}
	s.audit(user, "给落地加节点", l.Landing+" / "+req.Subdomain, "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"links": links, "qr": qr}))
}

// DELETE /api/v1/landings/{index}/subnodes/{domain}
// 只删面板 subnodes 里的登记，不碰中转转发、不动 config.toml/relay.conf
// （同一台落地机的多个节点共用一条 443 转发）。
func (s *Server) handleDeleteSubnode(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeJSON(w, 400, errResp("落地编号不对"))
		return
	}
	domain := r.PathValue("domain")
	if domain == "" {
		writeJSON(w, 400, errResp("子节点域名为空"))
		return
	}
	l, _, err := s.landingByIndex(index)
	if err != nil {
		writeJSON(w, 404, errResp(err.Error()))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.stateMu.Lock()
	ext := s.state.ext(l.Landing)
	kept := ext.SubNodes[:0]
	removed := false
	for _, sn := range ext.SubNodes {
		if strings.EqualFold(sn.Domain, domain) {
			removed = true
			continue
		}
		kept = append(kept, sn)
	}
	if !removed {
		s.stateMu.Unlock()
		writeJSON(w, 404, errResp(fmt.Sprintf("该落地没有名为 %s 的子节点", domain)))
		return
	}
	ext.SubNodes = kept
	s.stateMu.Unlock()
	if err := s.saveState(); err != nil {
		writeJSON(w, 500, errResp("删除子节点失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "删除子节点", l.Landing+" / "+domain, "成功", "只删面板登记，不影响中转转发；如需彻底删除该节点，请到落地机上删对应节点")
	writeJSON(w, 200, okResp(map[string]any{"msg": "子节点登记已删除；如需彻底删除该节点，请到落地机上删对应节点"}))
}

// ---------- 续期 / 续量 / 流量设置 ----------

func (s *Server) handleRenew(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeJSON(w, 400, errResp("落地编号不对"))
		return
	}
	var req struct {
		Days int `json:"days"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Days <= 0 {
		writeJSON(w, 400, errResp("续期天数须大于 0"))
		return
	}
	l, _, err := s.landingByIndex(index)
	if err != nil {
		writeJSON(w, 404, errResp(err.Error()))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.stateMu.Lock()
	ext := s.state.ext(l.Landing)
	base := time.Now()
	if ext.Expiry != "" {
		if t, err := time.Parse(time.RFC3339, ext.Expiry); err == nil && t.After(base) {
			base = t
		}
	}
	ext.Expiry = base.Add(time.Duration(req.Days) * 24 * time.Hour).UTC().Format(time.RFC3339)
	ext.RenewReminded7d, ext.RenewReminded3d = false, false
	wasDisabled := ext.Disabled
	s.stateMu.Unlock()
	if err := s.saveState(); err != nil {
		writeJSON(w, 500, errResp("续期失败："+err.Error()))
		return
	}
	if wasDisabled && ext.DisabledReason == "expired" {
		// 恢复转发
		if err := s.restoreLandingEndpoints(*l); err != nil {
			writeJSON(w, 200, errResp("续期已记，但恢复转发失败："+err.Error()))
			return
		}
		s.stateMu.Lock()
		s.state.ext(l.Landing).Disabled = false
		s.state.ext(l.Landing).DisabledReason = ""
		s.stateMu.Unlock()
		_ = s.saveState()
	}
	s.audit(s.currentUser(r), "续期", l.Landing, "成功", fmt.Sprintf("+%d 天", req.Days))
	writeJSON(w, 200, okResp(map[string]any{"expiry": s.state.ext(l.Landing).Expiry}))
}

func (s *Server) handleAddTraffic(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeJSON(w, 400, errResp("落地编号不对"))
		return
	}
	var req struct {
		Bytes int64 `json:"bytes"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Bytes <= 0 {
		writeJSON(w, 400, errResp("续量字节数须大于 0"))
		return
	}
	l, _, err := s.landingByIndex(index)
	if err != nil {
		writeJSON(w, 404, errResp(err.Error()))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.stateMu.Lock()
	ext := s.state.ext(l.Landing)
	ext.TrafficLimitBytes += req.Bytes
	ext.TrafficWarned80, ext.TrafficWarned100 = false, false
	wasDisabled := ext.Disabled && ext.DisabledReason == "traffic"
	s.stateMu.Unlock()
	_ = s.saveState()
	if wasDisabled {
		lt := s.traffic.get(l.Landing)
		if !overLimit(lt, s.state.ext(l.Landing).TrafficLimitBytes, s.state.ext(l.Landing).TrafficLimitMode) {
			if err := s.restoreLandingEndpoints(*l); err == nil {
				s.stateMu.Lock()
				s.state.ext(l.Landing).Disabled = false
				s.state.ext(l.Landing).DisabledReason = ""
				s.stateMu.Unlock()
				_ = s.saveState()
			}
		}
	}
	// 重建 quota
	if rc, _, err := s.readRelayConf(); err == nil {
		_ = s.ensureTrafficChains(rc.Landings)
	}
	s.audit(s.currentUser(r), "续量", l.Landing, "成功", fmt.Sprintf("+%d 字节", req.Bytes))
	writeJSON(w, 200, okResp(map[string]any{"limit_bytes": s.state.ext(l.Landing).TrafficLimitBytes}))
}

func (s *Server) handleSetTraffic(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeJSON(w, 400, errResp("落地编号不对"))
		return
	}
	var req struct {
		LimitBytes     int64  `json:"limit_bytes"`
		LimitMode      string `json:"limit_mode"`
		SpeedLimitMbps int    `json:"speed_limit_mbps"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if req.LimitBytes < 0 || req.SpeedLimitMbps < 0 {
		writeJSON(w, 400, errResp("数值不能为负"))
		return
	}
	if req.LimitMode != "" && req.LimitMode != "total" && req.LimitMode != "monthly" {
		writeJSON(w, 400, errResp("流量限制模式只能是 total 或 monthly"))
		return
	}
	l, _, err := s.landingByIndex(index)
	if err != nil {
		writeJSON(w, 404, errResp(err.Error()))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.stateMu.Lock()
	ext := s.state.ext(l.Landing)
	ext.TrafficLimitBytes = req.LimitBytes
	ext.TrafficLimitMode = req.LimitMode
	ext.SpeedLimitMbps = req.SpeedLimitMbps
	ext.TrafficWarned80, ext.TrafficWarned100 = false, false
	s.stateMu.Unlock()
	_ = s.saveState()
	if rc, _, err := s.readRelayConf(); err == nil {
		_ = s.ensureTrafficChains(rc.Landings)
	}
	if err := s.applySpeedLimit(*l, req.SpeedLimitMbps); err != nil {
		writeJSON(w, 200, map[string]any{"ok": true, "warning": err.Error()})
		return
	}
	s.audit(s.currentUser(r), "设置流量限制", l.Landing, "成功", "")
	writeJSON(w, 200, okResp(nil))
}

// ---------- 删除落地（软删除） ----------

// removeEndpointsFromToml 从 config.toml 删掉指定落地（按 remote 地址匹配）的 [[endpoints]] 段。
func removeEndpointsFromToml(data []byte, landing string) []byte {
	// 按空行分段：[[endpoints]] 段以空行分隔
	text := string(data)
	parts := strings.Split(text, "\n\n")
	var kept []string
	for _, seg := range parts {
		if strings.Contains(seg, "[[endpoints]]") && strings.Contains(seg, `remote = "`+landing+":") {
			continue
		}
		kept = append(kept, seg)
	}
	return []byte(strings.Join(kept, "\n\n"))
}

// endpointsForLanding 取出指定落地的 [[endpoints]] 段文本（供回收站恢复用）。
func endpointsForLanding(data []byte, landing string) string {
	text := string(data)
	parts := strings.Split(text, "\n\n")
	var got []string
	for _, seg := range parts {
		if strings.Contains(seg, "[[endpoints]]") && strings.Contains(seg, `remote = "`+landing+":") {
			got = append(got, seg)
		}
	}
	return strings.Join(got, "\n\n")
}

// rebuildRelayConfAfterDelete 删落地后重建 relay.conf：被删编号之后的前移一位，LANDING_COUNT-1。
func rebuildRelayConfAfterDelete(rc *RelayConf, delIndex int) []string {
	// 收集各落地字段
	type lf struct{ landing, isDomain, domain, cert, preset, mode, doorMode, ports string }
	var list []lf
	for _, l := range rc.Landings {
		if l.Index == delIndex {
			continue
		}
		suffix := ""
		if l.Index != 1 {
			suffix = "_" + strconv.Itoa(l.Index)
		}
		get := func(f string) string { return rc.Raw[f+suffix] }
		list = append(list, lf{get("LANDING"), get("LANDING_IS_DOMAIN"), get("DOMAIN"), get("CERT_DOMAIN"), get("PRESET"), get("MODE_NOTE"), get("MODE"), get("PORTS")})
	}
	// 保留非落地行
	landingKeys := map[string]bool{}
	for i := 1; i <= rc.Count; i++ {
		for _, k := range rc.fieldKeys(i) {
			landingKeys[k] = true
		}
	}
	landingKeys["LANDING_COUNT"] = true
	landingKeys["ALL_PORTS"] = true
	var lines []string
	for _, line := range rc.Lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			lines = append(lines, line)
			continue
		}
		k, _, ok := strings.Cut(t, "=")
		if ok && landingKeys[strings.TrimSpace(k)] {
			continue
		}
		lines = append(lines, line)
	}
	// 重新写落地块
	for i, f := range list {
		idx := i + 1
		suffix := ""
		if idx != 1 {
			suffix = "_" + strconv.Itoa(idx)
		}
		lines = append(lines,
			"LANDING"+suffix+"="+f.landing,
			"LANDING_IS_DOMAIN"+suffix+"="+f.isDomain,
			`DOMAIN`+suffix+`="`+f.domain+`"`,
			`CERT_DOMAIN`+suffix+`="`+f.cert+`"`,
			"PRESET"+suffix+"="+f.preset,
			`MODE_NOTE`+suffix+`="`+f.mode+`"`,
			`PORTS`+suffix+`="`+f.ports+`"`,
		)
		// MODE 只在非空时写回（标准模式是缺字段时的默认值，不用写）；
		// 不写的话删一个落地会把其余 door 落地的模式丢掉。
		if f.doorMode != "" {
			lines = append(lines, "MODE"+suffix+"="+f.doorMode)
		}
	}
	lines = append(lines, fmt.Sprintf("LANDING_COUNT=%d", len(list)))
	// ALL_PORTS 重算
	var all []string
	seen := map[string]bool{}
	for _, f := range list {
		for _, spec := range strings.Fields(f.ports) {
			if !seen[spec] {
				seen[spec] = true
				all = append(all, spec)
			}
		}
	}
	lines = append(lines, `ALL_PORTS="`+strings.Join(all, " ")+`"`)
	return lines
}

func (s *Server) handleDeleteLanding(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeJSON(w, 400, errResp("落地编号不对"))
		return
	}
	l, rc, err := s.landingByIndex(index)
	if err != nil {
		writeJSON(w, 404, errResp(err.Error()))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	user := s.currentUser(r)
	if _, err := s.backupNow("删除落地前自动备份", "realm"); err != nil {
		writeJSON(w, 500, errResp("备份失败，操作已取消"))
		return
	}
	tomlData, _ := s.readConfigToml()
	endpointsText := endpointsForLanding(tomlData, l.Landing)
	// relay.conf 片段
	var snippet []string
	for _, line := range rc.Lines {
		for _, k := range rc.fieldKeys(index) {
			if strings.HasPrefix(strings.TrimSpace(line), k+"=") {
				snippet = append(snippet, line)
			}
		}
	}
	// 1. config.toml 删 endpoints
	newToml := removeEndpointsFromToml(tomlData, l.Landing)
	// realm 无 endpoint 会 panic：留占位
	if !strings.Contains(string(newToml), "[[endpoints]]") {
		newToml = append(newToml, []byte("\n[[endpoints]]\nlisten = \"127.0.0.1:9\"\nremote = \"127.0.0.1:9\"\n")...)
	}
	// 2. relay.conf 重建
	newConfLines := rebuildRelayConfAfterDelete(rc, index)
	newConf := []byte(strings.Join(newConfLines, "\n") + "\n")
	// 原子写 + 失败回滚
	oldToml, oldConf := tomlData, []byte(strings.Join(rc.Lines, "\n"))
	if err := s.writeConfigTomlAtomic(newToml); err != nil {
		writeJSON(w, 500, errResp("写 config.toml 失败："+err.Error()))
		return
	}
	if err := s.writeRelayConfAtomic(newConf); err != nil {
		_ = s.writeConfigTomlAtomic(oldToml)
		writeJSON(w, 500, errResp("写 relay.conf 失败，已回滚："+err.Error()))
		return
	}
	// 3. 重生成分流配置 + 重启 realm + 验端口
	_ = s.regenProxies()
	if err := s.restartRealm(); err != nil {
		_ = s.writeConfigTomlAtomic(oldToml)
		_ = s.writeRelayConfAtomic(oldConf)
		_ = s.restartRealm()
		writeJSON(w, 500, errResp("重启 realm 失败，已回滚："+err.Error()))
		return
	}
	// 4. 进回收站
	s.stateMu.Lock()
	ext := *s.state.ext(l.Landing)
	delete(s.state.Landings, l.Landing)
	id := "rc-" + time.Now().UTC().Format("20060102-150405") + "-" + randomTokenHex(3)
	now := time.Now().UTC()
	s.state.Recycle = append(s.state.Recycle, RecycleItem{
		ID: id, Landing: l.Landing, Domain: l.Domain, Ports: l.PortsRaw, ModeNote: l.ModeNote, Mode: l.Mode,
		CertDomain: l.CertDomain, DeletedAt: now.Format(time.RFC3339),
		ExpiresAt:        now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
		RelayConfSnippet: strings.Join(snippet, "\n"), ConfigEndpoints: endpointsText, Ext: ext,
	})
	s.stateMu.Unlock()
	_ = s.saveState()
	// 删 iptables 计数规则（重建即可）
	if rc2, _, err := s.readRelayConf(); err == nil {
		_ = s.ensureTrafficChains(rc2.Landings)
	}
	s.audit(user, "删除落地", l.Landing, "成功", "软删除，进回收站 7 天")
	writeJSON(w, 200, okResp(map[string]any{"recycle_id": id}))
}

// restoreLandingEndpoints 把落地的 endpoints 加回 config.toml（续期/续量恢复用）。
func (s *Server) restoreLandingEndpoints(l Landing) error {
	tomlData, err := s.readConfigToml()
	if err != nil {
		return err
	}
	// 已存在则不重复加
	for _, p := range l.Ports {
		if strings.Contains(string(tomlData), fmt.Sprintf(`listen = "0.0.0.0:%d"`, p.Listen)) {
			continue
		}
	}
	// 简单追加缺失的段
	var add strings.Builder
	for _, p := range l.Ports {
		add.WriteString(fmt.Sprintf("\n[[endpoints]]\nlisten = \"0.0.0.0:%d\"\nremote = \"%s:%d\"\n", p.Listen, l.Landing, p.Remote))
	}
	newToml := append(append([]byte{}, tomlData...), []byte(add.String())...)
	if err := s.writeConfigTomlAtomic(newToml); err != nil {
		return err
	}
	if err := s.restartRealm(); err != nil {
		return err
	}
	return verifyPorts(l.Ports)
}

// ---------- 回收站 ----------

func (s *Server) handleRecycleList(w http.ResponseWriter, r *http.Request) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	items := s.state.Recycle
	if items == nil {
		items = []RecycleItem{}
	}
	var out []map[string]any
	for _, it := range items {
		rest := ""
		if t, err := time.Parse(time.RFC3339, it.ExpiresAt); err == nil {
			d := time.Until(t)
			rest = fmt.Sprintf("%d 天 %d 小时", int(d.Hours())/24, int(d.Hours())%24)
		}
		out = append(out, map[string]any{
			"id": it.ID, "landing": it.Landing, "domain": it.Domain, "ports": it.Ports,
			"mode_note": it.ModeNote, "cert_domain": it.CertDomain,
			"deleted_at": it.DeletedAt, "expires_at": it.ExpiresAt, "restore_before": rest,
		})
	}
	writeJSON(w, 200, okResp(map[string]any{"recycle": out}))
}

func (s *Server) handleRecycleRestore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.stateMu.Lock()
	var item *RecycleItem
	idx := -1
	for i := range s.state.Recycle {
		if s.state.Recycle[i].ID == id {
			item = &s.state.Recycle[i]
			idx = i
			break
		}
	}
	if item == nil {
		s.stateMu.Unlock()
		writeJSON(w, 404, errResp("回收站里没有这一项"))
		return
	}
	it := *item
	s.stateMu.Unlock()
	// 恢复：relay.conf 追加编号块 + config.toml 追加 endpoints
	rc, _, err := s.readRelayConf()
	if err != nil {
		// relay.conf 没了：从片段重建一个最小版
		rc = parseRelayConf([]byte("LANDING_COUNT=0\n"))
	}
	newIdx := len(rc.Landings) + 1
	suffix := ""
	if newIdx != 1 {
		suffix = "_" + strconv.Itoa(newIdx)
	}
	lines := append(append([]string{}, rc.Lines...),
		"LANDING"+suffix+"="+it.Landing,
		"DOMAIN"+suffix+`="`+it.Domain+`"`,
		"CERT_DOMAIN"+suffix+`="`+it.CertDomain+`"`,
		"MODE_NOTE"+suffix+`="`+it.ModeNote+`"`,
		"PORTS"+suffix+`="`+it.Ports+`"`,
	)
	// 恢复 door 落地的模式（旧回收站记录没存 Mode 时不写，默认标准模式）
	if it.Mode != "" {
		lines = append(lines, "MODE"+suffix+"="+it.Mode)
	}
	lines = setRelayConfValue(lines, "LANDING_COUNT", strconv.Itoa(newIdx))
	// ALL_PORTS 合并
	all := rc.AllPorts
	for _, spec := range strings.Fields(it.Ports) {
		if !strings.Contains(" "+all+" ", " "+spec+" ") {
			all = strings.TrimSpace(all + " " + spec)
		}
	}
	lines = setRelayConfValue(lines, "ALL_PORTS", all)
	if err := s.writeRelayConfAtomic([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		writeJSON(w, 500, errResp("恢复 relay.conf 失败："+err.Error()))
		return
	}
	if it.ConfigEndpoints != "" {
		tomlData, _ := s.readConfigToml()
		newToml := append(append([]byte{}, tomlData...), []byte("\n"+it.ConfigEndpoints+"\n")...)
		if err := s.writeConfigTomlAtomic(newToml); err != nil {
			writeJSON(w, 500, errResp("恢复 config.toml 失败："+err.Error()))
			return
		}
	}
	_ = s.regenProxies()
	if err := s.restartRealm(); err != nil {
		writeJSON(w, 500, errResp("重启 realm 失败："+err.Error()))
		return
	}
	s.stateMu.Lock()
	s.state.Recycle = append(s.state.Recycle[:idx], s.state.Recycle[idx+1:]...)
	s.state.Landings[it.Landing] = &it.Ext
	s.stateMu.Unlock()
	_ = s.saveState()
	s.audit(s.currentUser(r), "恢复落地", it.Landing, "成功", "从回收站恢复")
	writeJSON(w, 200, okResp(map[string]any{"landing_index": newIdx}))
}

func (s *Server) handleRecycleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	for i := range s.state.Recycle {
		if s.state.Recycle[i].ID == id {
			landing := s.state.Recycle[i].Landing
			s.state.Recycle = append(s.state.Recycle[:i], s.state.Recycle[i+1:]...)
			_ = s.saveStateLocked()
			s.audit(s.currentUser(r), "彻底删除落地", landing, "成功", "回收站记录已清除")
			writeJSON(w, 200, okResp(nil))
			return
		}
	}
	writeJSON(w, 404, errResp("回收站里没有这一项"))
}

// ---------- 节点链接 ----------

func (s *Server) handleLinks(w http.ResponseWriter, r *http.Request) {
	if _, err := os.Stat(s.nodesFilePath()); err != nil {
		_, _ = s.runScript("install.sh", 120*time.Second, nil, "--nodes")
	}
	b, err := os.ReadFile(s.nodesFilePath())
	if err != nil {
		writeJSON(w, 200, okResp(map[string]any{"groups": []any{}, "note": "nodes.txt 不存在：先在落地机上跑安装第 7 步生成链接，或跑 bash install.sh --nodes"}))
		return
	}
	groups := parseNodesTxt(b)
	if groups == nil {
		groups = []linkGroup{}
	}
	// 每个分组标模式：按 landing_index 查 relay.conf，查不到默认标准模式。
	modeByIndex := map[int]string{}
	if rc, _, err := s.readRelayConf(); err == nil {
		for _, l := range rc.Landings {
			modeByIndex[l.Index] = l.Mode
		}
	}
	for i := range groups {
		if m, ok := modeByIndex[groups[i].LandingIndex]; ok && m != "" {
			groups[i].Mode = m
		}
		// 没登记过的下标（如手动 twin 分组）保留 parseNodesTxt 按链接行推断的模式
		groups[i].QR = map[string]string{}
		for _, l := range groups[i].Links {
			if q, err := qrDataURI(l.Link); err == nil {
				groups[i].QR[l.Label] = q
			}
		}
	}
	writeJSON(w, 200, okResp(map[string]any{"groups": groups}))
}

func (s *Server) handleTwin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Link      string `json:"link"`
		LandingIP string `json:"landing_ip"`
		SNI       string `json:"sni"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if _, err := parseNodeLink(req.Link); err != nil {
		writeJSON(w, 400, errResp("链接不对："+err.Error()))
		return
	}
	if !validIPOrDomain(req.LandingIP) {
		writeJSON(w, 400, errResp("落地 IP/域名不对"))
		return
	}
	sni := req.SNI
	if sni == "" {
		if pl, err := parseNodeLink(req.Link); err == nil {
			sni = pl.SNI
		}
	}
	landingIP := req.LandingIP
	if !isIP(landingIP) {
		if ips, err := net.LookupIP(landingIP); err == nil && len(ips) > 0 {
			landingIP = ips[0].String()
		}
	}
	// 先按 landing_ip 在 relay.conf 里找落地，定模式（door 落地走 --door 生成任意门链接）。
	var landing *Landing
	var rc *RelayConf
	if rc2, _, err := s.readRelayConf(); err == nil {
		rc = rc2
		for i := range rc.Landings {
			if rc.Landings[i].Landing == req.LandingIP || rc.Landings[i].Landing == landingIP {
				landing = &rc.Landings[i]
				break
			}
		}
	}
	relayLabel := "中转"
	isDoor := landing != nil && landing.Mode == "door"
	if isDoor {
		relayLabel = "任意门"
	}
	res, err := s.runScript("install.sh", 60*time.Second, nil, "--twin", req.Link, landingIP, sni)
	twin := strings.TrimSpace(res.Stdout)
	if err != nil || strings.HasPrefix(twin, "__MANUAL__:") {
		reason := strings.TrimPrefix(twin, "__MANUAL__:")
		if reason == "" {
			reason = "这条链接不会自动生成直连，请手动改地址（把地址换成落地 IP，TLS 的 SNI 填域名）"
		}
		writeJSON(w, 200, map[string]any{"ok": true, "relay_link": req.Link, "direct_link": "", "manual": reason, "relay_label": relayLabel})
		return
	}
	// 中转链接：原链接按实际监听端口换（install.sh --nodes 的逻辑在面板侧简化：端口不变，
	// 除非该落地监听端口与落地端口不同——这里用 relay.conf 查）
	relayLink := req.Link
	if isDoor {
		// 任意门模式：跑 install.sh --door 生成任意门链接（地址换成中转地址、端口换成转发端口）。
		forwardPort := 0
		if pl, err := parseNodeLink(req.Link); err == nil && pl.Port > 0 {
			forwardPort = pl.Port
			for _, p := range landing.Ports {
				if p.Remote == pl.Port {
					forwardPort = p.Listen
					break
				}
			}
		}
		if rc.RelayAddr == "" {
			writeJSON(w, 200, map[string]any{"ok": true, "relay_link": "", "direct_link": twin, "manual": "任意门地址还没设置：先到「设置 → 任意门地址」填好中转地址，再重新生成", "relay_label": relayLabel})
			return
		}
		dres, derr := s.runScript("install.sh", 60*time.Second, nil, "--door", req.Link, rc.RelayAddr, strconv.Itoa(forwardPort), sni)
		doorLink := strings.TrimSpace(dres.Stdout)
		if derr != nil || strings.HasPrefix(doorLink, "__MANUAL__:") {
			reason := strings.TrimPrefix(doorLink, "__MANUAL__:")
			if reason == "" {
				reason = "任意门链接生成失败，请手动改地址（把地址换成中转地址、端口换成转发端口）"
			}
			writeJSON(w, 200, map[string]any{"ok": true, "relay_link": "", "direct_link": twin, "manual": reason, "relay_label": relayLabel})
			return
		}
		relayLink = doorLink
	} else if landing != nil {
		if pl, err := parseNodeLink(req.Link); err == nil && pl.Port > 0 {
			for _, p := range landing.Ports {
				if p.Remote == pl.Port && p.Listen != p.Remote {
					relayLink = setLinkPort(req.Link, p.Listen)
				}
			}
		}
	}
	qr := map[string]string{}
	if q, err := qrDataURI(relayLink); err == nil {
		qr[relayLabel] = q
	}
	if q, err := qrDataURI(twin); err == nil {
		qr["直连"] = q
	}
	// 写历史：twin 成功就持久化进 nodes.txt，刷新后历史页还能看到。
	// 保存失败不拦本次返回，只在响应里带上原因。
	persistErr := ""
	if err := upsertTwinLinks(s.nodesFilePath(), landingIP, req.LandingIP, relayLabel, relayLink, twin); err != nil {
		persistErr = fmt.Sprintf("历史记录保存失败：%v", err)
	}
	resp := okResp(map[string]any{"relay_link": relayLink, "direct_link": twin, "qr": qr, "relay_label": relayLabel})
	if persistErr != "" {
		resp["persist_error"] = persistErr
	}
	writeJSON(w, 200, resp)
}

// setLinkPort 换链接端口（vless/trojan；vmess/ss base64 换不了时原样返回）。
func setLinkPort(link string, newPort int) string {
	pl, err := parseNodeLink(link)
	if err != nil || pl.Port == 0 {
		return link
	}
	switch pl.Scheme {
	case "vless", "trojan":
		u, err := neturlParse(link)
		if err != nil {
			return link
		}
		host := u.Hostname()
		u.Host = net.JoinHostPort(host, strconv.Itoa(newPort))
		return u.String()
	}
	return link
}

// ---------- 证书 ----------

func (s *Server) handleCerts(w http.ResponseWriter, r *http.Request) {
	rc, _, err := s.readRelayConf()
	if err != nil {
		writeJSON(w, 200, okResp(map[string]any{"certs": []any{}}))
		return
	}
	var certs []any
	// 优先 check.sh --json
	certByIndex := map[int]map[string]any{}
	if res, err := s.runScript("check.sh", 60*time.Second, nil, "--json"); err == nil {
		var cj struct {
			Certs []map[string]any `json:"certs"`
		}
		if json.Unmarshal([]byte(res.Stdout), &cj) == nil {
			for _, c := range cj.Certs {
				if idx, ok := c["index"].(float64); ok {
					certByIndex[int(idx)] = c
				}
			}
		}
	}
	for _, l := range rc.Landings {
		renewMode, needManual := renewModeFor(l, rc.Share443)
		var daysLeft any
		var expiry string
		if c, ok := certByIndex[l.Index]; ok {
			daysLeft = c["days_left"]
			if e, ok := c["expiry"].(string); ok {
				expiry = e
			}
		} else {
			tlsPort := 0
			for _, p := range l.Ports {
				if p.Proto == "tcp" && p.Listen == 443 {
					tlsPort = p.Listen
				}
			}
			if tlsPort > 0 {
				ci := fetchCert(tlsPort, l.CertDomain)
				daysLeft = ci.DaysLeft
				expiry = ci.Expiry
			}
		}
		certs = append(certs, map[string]any{
			"landing_index": l.Index, "landing": l.Landing, "domain": l.CertDomain,
			"days_left": daysLeft, "expiry": expiry, "renew_mode": renewMode, "need_manual": needManual,
		})
	}
	if certs == nil {
		certs = []any{}
	}
	writeJSON(w, 200, okResp(map[string]any{"certs": certs}))
}

func renewGuideSteps(l Landing) []map[string]string {
	domain := l.CertDomain
	if domain == "" {
		domain = l.Domain
	}
	landing := l.Landing
	return []map[string]string{
		{"n": "1", "title": "看剩余天数", "detail": "先确认证书还剩几天，别太早续签（Caddy 只在到期前 30 天内才续）。", "cmd": "bash check.sh"},
		{"n": "2", "title": "A 记录改回落地 IP", "detail": "到域名 DNS 控制台，把 " + domain + " 的 A 记录从中转 IP 改回指向落地 IP " + landing + "。", "cmd": ""},
		{"n": "3", "title": "等 DNS 生效", "detail": "等几分钟，用本机查一下解析是不是已经指到落地了。", "cmd": "getent hosts " + domain},
		{"n": "4", "title": "重启落地 Caddy", "detail": "到落地机上重启 Caddy，让它重新签证书（容器名按落地实际的来）。", "cmd": "docker restart tiktok-caddy # 或 singbox-caddy，按落地实际容器名"},
		{"n": "5", "title": "核对新到期日", "detail": "再跑一次自检，看证书剩余天数是不是回到 90 天左右。", "cmd": "bash check.sh"},
		{"n": "6", "title": "A 记录改回中转", "detail": "确认新证书没问题后，把 " + domain + " 的 A 记录改回指向中转机 IP，客户端不用改。", "cmd": ""},
	}
}

func (s *Server) handleRenewGuide(w http.ResponseWriter, r *http.Request) {
	idxStr := r.URL.Query().Get("landing_index")
	idx, _ := strconv.Atoi(idxStr)
	if idx <= 0 {
		// 没指定落地：返回通用六步（用 example.com 占位）
		l := Landing{Landing: "203.0.113.1", CertDomain: "example.com", Domain: "example.com"}
		writeJSON(w, 200, okResp(map[string]any{"steps": renewGuideSteps(l)}))
		return
	}
	l, _, err := s.landingByIndex(idx)
	if err != nil {
		writeJSON(w, 404, errResp(err.Error()))
		return
	}
	steps := renewGuideSteps(*l)
	// 附上当前进度
	progress := s.state.ext(l.Landing).RenewProgress
	writeJSON(w, 200, okResp(map[string]any{"steps": steps, "progress": progress}))
}

func (s *Server) handleRenewProgress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LandingIndex int `json:"landing_index"`
		Step         int `json:"step"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Step < 0 || req.Step > 6 {
		writeJSON(w, 400, errResp("步骤须在 0-6 之间"))
		return
	}
	l, _, err := s.landingByIndex(req.LandingIndex)
	if err != nil {
		writeJSON(w, 404, errResp(err.Error()))
		return
	}
	s.stateMu.Lock()
	s.state.ext(l.Landing).RenewProgress = req.Step
	s.stateMu.Unlock()
	_ = s.saveState()
	writeJSON(w, 200, okResp(nil))
}

// ---------- 服务与日志 ----------

func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "start" && action != "stop" && action != "restart" {
		writeJSON(w, 400, errResp("操作只能是 start/stop/restart"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	if err := s.controlRealm(action); err != nil {
		writeJSON(w, 200, errResp("操作失败："+err.Error()))
		return
	}
	// 分流组件随 SHARE 一起启停
	if action != "stop" {
		_ = s.regenProxies()
	} else {
		if rc, _, err := s.readRelayConf(); err == nil {
			if rc.Share80 {
				_ = runCmd(nil, 10*time.Second, "systemctl", "stop", "nginx")
			}
			if rc.Share443 {
				_ = runCmd(nil, 10*time.Second, "systemctl", "stop", "haproxy")
			}
		}
	}
	s.audit(s.currentUser(r), "服务"+action, "realm", "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"running": realmRunning()}))
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	lines := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
			lines = n
		}
	}
	b, err := os.ReadFile("/var/log/realm.log")
	if err != nil {
		// journalctl 回退
		if res, err2 := runCmdOut(nil, 10*time.Second, "journalctl", "-u", "realm", "-n", strconv.Itoa(lines), "--no-pager"); err2 == nil {
			writeJSON(w, 200, okResp(map[string]any{"lines": strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")}))
			return
		}
		writeJSON(w, 200, okResp(map[string]any{"lines": []string{}, "note": "日志文件不存在：realm 还没跑过，或日志不在 /var/log/realm.log"}))
		return
	}
	all := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	writeJSON(w, 200, okResp(map[string]any{"lines": all}))
}

func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request) {
	var list []map[string]any
	if res, err := runCmdOut(nil, 5*time.Second, "ss", "-tlnp"); err == nil {
		list = append(list, parseSS(res.Stdout, "tcp")...)
	}
	if res, err := runCmdOut(nil, 5*time.Second, "ss", "-ulnp"); err == nil {
		list = append(list, parseSS(res.Stdout, "udp")...)
	}
	if list == nil {
		// /proc/net 回退（无进程名）
		for k := range listeningPorts() {
			parts := strings.Split(k, ":")
			p, _ := strconv.Atoi(parts[1])
			list = append(list, map[string]any{"proto": parts[0], "port": p, "process": ""})
		}
		sort.Slice(list, func(i, j int) bool { return list[i]["port"].(int) < list[j]["port"].(int) })
	}
	writeJSON(w, 200, okResp(map[string]any{"listening": list}))
}

// parseSS 解析 ss -tlnp 输出：取本地端口与进程名。
func parseSS(out, proto string) []map[string]any {
	var list []map[string]any
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "LISTEN" && proto == "tcp" {
			if proto == "tcp" && (len(f) == 0 || f[0] != "LISTEN") {
				continue
			}
		}
		if len(f) < 4 {
			continue
		}
		addr := f[3]
		port := portFromAddr(addr)
		if port == 0 {
			continue
		}
		proc := ""
		// 进程名在 users:(("realm",pid=...,fd=...)) 里
		joined := strings.Join(f, " ")
		if i := strings.Index(joined, `"`); i >= 0 {
			rest := joined[i+1:]
			if j := strings.Index(rest, `"`); j >= 0 {
				proc = rest[:j]
			}
		}
		list = append(list, map[string]any{"proto": proto, "port": port, "process": proc})
	}
	return list
}

func neturlParse(s string) (*url.URL, error) { return url.Parse(s) }

var _ = tls.VersionTLS12
var _ = io.Discard
var _ = filepath.Base
