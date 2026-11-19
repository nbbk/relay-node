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
	mux.HandleFunc("PUT /api/v1/landings/{index}", s.requireAuth(s.handleUpdateLanding))
	mux.HandleFunc("POST /api/v1/landings/{index}/orig-link", s.requireAuth(s.handleSetOrigLink))
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
	mux.HandleFunc("POST /api/v1/links/delete", s.requireAuth(s.handleDeleteLinksGroups))
	// 订阅链接（v1.2.7）：/sub/{token} 是公开端点（令牌本身就是鉴权），不走登录会话
	mux.HandleFunc("GET /sub/{token}", s.handleSub)
	mux.HandleFunc("GET /api/v1/settings/subscription", s.requireAuth(s.handleSubTokenGet))
	mux.HandleFunc("POST /api/v1/settings/subscription/generate", s.requireAuth(s.handleSubTokenGenerate))
	mux.HandleFunc("POST /api/v1/settings/subscription/revoke", s.requireAuth(s.handleSubTokenRevoke))
	mux.HandleFunc("POST /api/v1/subscription/qr", s.requireAuth(s.handleSubQR))
	mux.HandleFunc("GET /api/v1/subscriptions/merged", s.requireAuth(s.handleMergedSubsList))
	mux.HandleFunc("POST /api/v1/subscriptions/merged", s.requireAuth(s.handleMergedSubsCreate))
	mux.HandleFunc("DELETE /api/v1/subscriptions/merged/{id}", s.requireAuth(s.handleMergedSubsDelete))
	mux.HandleFunc("GET /api/v1/subscriptions/users", s.requireAuth(s.handleSubUsersList))
	mux.HandleFunc("POST /api/v1/subscriptions/users", s.requireAuth(s.handleSubUsersCreate))
	mux.HandleFunc("POST /api/v1/subscriptions/users/{id}/rotate", s.requireAuth(s.handleSubUsersRotate))
	mux.HandleFunc("DELETE /api/v1/subscriptions/users/{id}", s.requireAuth(s.handleSubUsersDelete))
	mux.HandleFunc("GET /api/v1/subscriptions/worker", s.requireAuth(s.handleWorkerCode))
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
	mux.HandleFunc("POST /api/v1/settings/manager-only", s.requireAuth(s.handleManagerOnly))
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
	mux.HandleFunc("GET /api/v1/central/subscription", s.requireCentralToken(s.handleCentralSubscription))
	mux.HandleFunc("POST /api/v1/central/landings", s.requireCentralToken(s.handleAddLanding))
	mux.HandleFunc("PUT /api/v1/central/landings/{index}", s.requireCentralToken(s.handleUpdateLanding))
	mux.HandleFunc("POST /api/v1/central/landings/{index}/orig-link", s.requireCentralToken(s.handleSetOrigLink))
	mux.HandleFunc("DELETE /api/v1/central/landings/{index}", s.requireCentralToken(s.handleDeleteLanding))
	mux.HandleFunc("POST /api/v1/central/landings/{index}/add-node", s.requireCentralToken(s.handleAddNode))
	mux.HandleFunc("DELETE /api/v1/central/landings/{index}/subnodes/{domain}", s.requireCentralToken(s.handleDeleteSubnode))
	mux.HandleFunc("POST /api/v1/central/landings/{index}/renew", s.requireCentralToken(s.handleRenew))
	mux.HandleFunc("POST /api/v1/central/landings/{index}/add-traffic", s.requireCentralToken(s.handleAddTraffic))
	mux.HandleFunc("POST /api/v1/central/landings/{index}/traffic", s.requireCentralToken(s.handleSetTraffic))
	mux.HandleFunc("GET /api/v1/central/certs", s.requireCentralToken(s.handleCerts))
	mux.HandleFunc("GET /api/v1/central/traffic", s.requireCentralToken(s.handleTrafficGet))
	mux.HandleFunc("POST /api/v1/central/check", s.requireCentralToken(s.handleCheck))
	mux.HandleFunc("POST /api/v1/central/fix", s.requireCentralToken(s.handleFix))
	mux.HandleFunc("POST /api/v1/central/service/{action}", s.requireCentralToken(s.handleService))
	mux.HandleFunc("GET /api/v1/central/remotes", s.requireAuth(s.handleCentralRemotesList))
	mux.HandleFunc("POST /api/v1/central/remotes", s.requireAuth(s.handleCentralRemotesAdd))
	mux.HandleFunc("DELETE /api/v1/central/remotes/{id}", s.requireAuth(s.handleCentralRemotesDelete))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/poll", s.requireAuth(s.handleCentralRemotesPoll))
	mux.HandleFunc("GET /api/v1/central/remotes/{id}/landings", s.requireAuth(s.handleCentralRemoteLandings))
	mux.HandleFunc("GET /api/v1/central/remotes/{id}/links", s.requireAuth(s.handleCentralRemoteLinks))
	mux.HandleFunc("GET /api/v1/central/remotes/{id}/certs", s.requireAuth(s.handleCentralRemoteCerts))
	mux.HandleFunc("GET /api/v1/central/remotes/{id}/traffic", s.requireAuth(s.handleCentralRemoteTraffic))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/landings", s.requireAuth(s.handleCentralRemoteAddLanding))
	mux.HandleFunc("PUT /api/v1/central/remotes/{id}/landings/{index}", s.requireAuth(s.handleCentralRemoteUpdateLanding))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/landings/{index}/orig-link", s.requireAuth(s.handleCentralRemoteSetOrigLink))
	mux.HandleFunc("DELETE /api/v1/central/remotes/{id}/landings/{index}", s.requireAuth(s.handleCentralRemoteDeleteLanding))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/landings/{index}/add-node", s.requireAuth(s.handleCentralRemoteAddNode))
	mux.HandleFunc("DELETE /api/v1/central/remotes/{id}/landings/{index}/subnodes/{domain}", s.requireAuth(s.handleCentralRemoteDeleteSubnode))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/landings/{index}/renew", s.requireAuth(s.handleCentralRemoteRenew))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/landings/{index}/add-traffic", s.requireAuth(s.handleCentralRemoteAddTraffic))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/landings/{index}/traffic", s.requireAuth(s.handleCentralRemoteSetTraffic))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/service/{action}", s.requireAuth(s.handleCentralRemoteService))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/check", s.requireAuth(s.handleCentralRemoteCheck))
	mux.HandleFunc("POST /api/v1/central/remotes/{id}/fix", s.requireAuth(s.handleCentralRemoteFix))
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
// 例外：/sub/ 订阅端点不走白名单（订阅客户端从任意 IP 拉取，令牌本身就是鉴权）。
func (s *Server) ipWhitelistMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sub/") {
			next.ServeHTTP(w, r)
			return
		}
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
	writeJSON(w, 200, okResp(map[string]any{"username": u, "version": Version, "manager_only": s.cfg.ManagerOnly}))
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
		// check.sh days_left 为 null 的条目也会让 ok==true（自定义端口 TLS 落地
		// 就是这种情况），null 不算拿到证书，要继续走下面的握手回退。
		hasCert := ok && ci.DaysLeft != nil
		if !hasCert {
			// 回退：按落地实际的 TCP 监听端口逐个握手拿证书。
			// 以前只认 443，自定义端口 TLS 落地（如 24443）永远显示无证书。
			if ci2, ok2 := probeCertForLanding(l); ok2 {
				ci, hasCert = ci2, true
			}
		}
		renewMode, _ := renewModeFor(l, rc.Share443, hasCert)
		// 卡片直接出中转链接：加落地时存了原始链接的，这里现场算好返回；
		// 没存的卡片上显示补粘贴入口（前端按 orig_link 为空判断）。
		var links []labelledLink
		qr := map[string]string{}
		if ext.OrigLink != "" {
			if rl, dl, label, manual := s.computeTwinLinks(ext.OrigLink, l.Landing, l.CertDomain, &l, rc, ext.OrigLinkName); manual == "" {
				links = []labelledLink{{Label: label, Link: rl}, {Label: "直连", Link: dl}}
				qr = linksQR(links)
			}
		}
		if links == nil {
			links = []labelledLink{}
		}
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
			"index": l.Index, "landing": l.Landing, "name": l.displayName(), "domain": l.Domain, "cert_domain": l.CertDomain, "cert_mode": l.CertMode,
			"preset": l.Preset, "sb_mode": deriveSBMode(l), "custom_ports": portsToCustomString(l.Ports), "want_udp": hasUDPPorts(l.Ports),
			"mode_note": l.ModeNote, "mode": l.Mode, "ports": l.Ports,
			"online": online, "latency_ms": latencyMs,
			"latency_error": latErr, "latency_checked_at": latCheckedAt, "latency_target": latTarget,
			"cert_days_left": ci.DaysLeft, "cert_expiry": ci.Expiry, "renew_mode": renewMode,
			"expiry": ext.Expiry, "days_left": daysLeft,
			"orig_link": ext.OrigLink, "orig_link_name": ext.OrigLinkName, "links": links, "qr": qr,
			"speed_limit_mbps": ext.SpeedLimitMbps,
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

// addLandingReq 添加/编辑落地的请求体（两者字段一致：编辑是全量替换，
// 表单预填当前值）。DurationDays：0=永久，>0=从现在起 N 天后到期，
// -1=保持不变（仅编辑时用，添加落地不发 -1）。
type addLandingReq struct {
	Landing     string `json:"landing"`
	Preset      string `json:"preset"`
	SBMode      string `json:"sb_mode"`
	CustomPorts string `json:"custom_ports"`
	WantUDP     bool   `json:"want_udp"`
	CertDomain  string `json:"cert_domain"`
	// CertMode 证书签发方式：""=没填（按老逻辑判）、"http01"=默认走 80、
	// "dns01"=DNS 验证 Cloudflare（续签不走 80，全自动）。
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
	// OrigLink 原始节点链接（选填）：存面板 state，用来拼中转+直连链接；
	// 不传给安装脚本、不进日志与审计。Name 显示名（选填，默认=落地地址）。
	OrigLink string `json:"orig_link"`
	Name     string `json:"name"`
	// LinkName 生成 twin 链接时用的节点名称（选填）：直连备注是
	// “<名称>-直连”、中转备注是“<名称>-中转”；空着用原始链接自己的备注名。
	LinkName string `json:"link_name"`
}

// validateLandingReqFields 添加/编辑落地共用的服务端二次校验，
// 返回错误文案，空串=通过。
func validateLandingReqFields(req addLandingReq) string {
	if !validIPOrDomain(req.Landing) {
		return "落地地址不对：填 IP 或域名"
	}
	if req.Preset != "1" && req.Preset != "2" && req.Preset != "3" {
		return "预设只能是 1/2/3"
	}
	if req.Preset == "2" && req.SBMode != "" && req.SBMode != "1" && req.SBMode != "2" {
		return "sing-box 模式只能是 1（TLS）或 2（明文）"
	}
	if req.Preset == "3" {
		if _, err := parseCustomPorts(req.CustomPorts, req.WantUDP); err != nil {
			return err.Error()
		}
	}
	if req.CertDomain != "" && !validDomain(req.CertDomain) {
		return "证书域名格式不对"
	}
	if req.CertMode != "" && req.CertMode != "http01" && req.CertMode != "dns01" {
		return "证书签发方式只能是 http01（默认走 80）或 dns01（DNS 验证）"
	}
	if req.TrafficLimitMode != "" && req.TrafficLimitMode != "total" && req.TrafficLimitMode != "monthly" {
		return "流量限制模式只能是 total 或 monthly"
	}
	if req.TrafficLimitBytes < 0 || req.SpeedLimitMbps < 0 || req.DurationDays < -1 {
		return "数值不能为负"
	}
	if req.ShareMode != "" && req.ShareMode != "1" && req.ShareMode != "2" && req.ShareMode != "3" && req.ShareMode != "4" {
		return "分流方式只能是 1/2/3/4"
	}
	if req.Mode != "" && req.Mode != "standard" && req.Mode != "door" {
		return "模式只能是 standard（标准模式）或 door（任意门模式）"
	}
	if req.RelayAddr != "" && !validIPOrDomain(req.RelayAddr) {
		return "中转地址不对：填 IP 或域名"
	}
	for k, v := range req.DoorPortOverrides {
		rp, err := strconv.Atoi(strings.TrimSpace(k))
		if err != nil || !validPort(rp) || !validPort(v) {
			return "转发端口覆盖格式不对：键和值都须是 1-65535 的端口，如 {\"443\": 30443}"
		}
	}
	for k, v := range req.ListenOverrides {
		rp, err := strconv.Atoi(strings.TrimSpace(k))
		if err != nil || !validPort(rp) || !validPort(v) {
			return "监听端口覆盖格式不对：键和值都须是 1-65535 的端口，如 {\"443\": 10443}"
		}
	}
	if req.OrigLink != "" {
		if _, err := parseNodeLink(req.OrigLink); err != nil {
			return "原始节点链接不对：" + err.Error()
		}
	}
	if req.Name != "" && (len(req.Name) > 64 || strings.ContainsAny(req.Name, "\"\n\r")) {
		return "名称太长或含有非法字符（不能超过 64 字，不能含引号与换行）"
	}
	if req.LinkName != "" && (len(req.LinkName) > 64 || strings.ContainsAny(req.LinkName, "\n\r")) {
		return "节点名称太长或含有非法字符（不能超过 64 字，不能含换行）"
	}
	return ""
}

func (s *Server) handleAddLanding(w http.ResponseWriter, r *http.Request) {
	var req addLandingReq
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if msg := validateLandingReqFields(req); msg != "" {
		writeJSON(w, 400, errResp(msg))
		return
	}
	if req.Mode == "" {
		req.Mode = "standard"
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
	ext.OrigLink = req.OrigLink
	ext.OrigLinkName = req.LinkName
	s.stateMu.Unlock()
	_ = s.saveState()
	// 跑 --nodes 生成链接（失败不阻塞）
	_, _ = s.runScript("install.sh", 120*time.Second, nil, "--nodes")
	// 读 nodes.txt 返回新落地链接
	groups := []linkGroup{}
	if b, err := os.ReadFile(s.nodesFilePath()); err == nil {
		groups = parseNodesTxt(b)
	}
	// 找新落地的分组（最后一个匹配该 landing 的）与编号
	var links []labelledLink
	landingIndex := 0
	var newLanding *Landing
	var rc2 *RelayConf
	if rc2x, _, err := s.readRelayConf(); err == nil {
		rc2 = rc2x
		landingIndex = len(rc2.Landings)
		for i := range rc2.Landings {
			if rc2.Landings[i].Landing == req.Landing {
				newLanding = &rc2.Landings[i]
				landingIndex = rc2.Landings[i].Index
			}
		}
		for _, g := range groups {
			if g.Landing == req.Landing {
				links = g.Links
				landingIndex = g.LandingIndex
			}
		}
	}
	// 名称写进 relay.conf（NAME[_N]，只改显示，不动转发）
	if req.Name != "" && newLanding != nil {
		suffix := ""
		if newLanding.Index != 1 {
			suffix = "_" + strconv.Itoa(newLanding.Index)
		}
		if rc2 != nil {
			lines := setRelayConfValue(rc2.Lines, "NAME"+suffix, req.Name)
			_ = s.writeRelayConfAtomic([]byte(strings.Join(lines, "\n") + "\n"))
		}
	}
	// 证书签发方式写进 relay.conf（CERT_MODE[_N]；install.sh 不写这项，
	// 面板自己补。没填就不写，旧逻辑继续生效）
	if req.CertMode != "" && newLanding != nil && rc2 != nil {
		suffix := ""
		if newLanding.Index != 1 {
			suffix = "_" + strconv.Itoa(newLanding.Index)
		}
		if rc3, _, err := s.readRelayConf(); err == nil {
			lines := setRelayConfValue(rc3.Lines, "CERT_MODE"+suffix, req.CertMode)
			_ = s.writeRelayConfAtomic([]byte(strings.Join(lines, "\n") + "\n"))
		}
	}
	// 存了原始链接：现场算中转+直连链接，写进 nodes.txt（显式传落地真实编号，
	// 不用最大编号+1，避免 1.2.1 已知的撞号问题），卡片上直接出链接。
	manualNote := ""
	if req.OrigLink != "" && newLanding != nil {
		landingIP := req.Landing
		if !isIP(landingIP) {
			if ips, err := net.LookupIP(landingIP); err == nil && len(ips) > 0 {
				landingIP = ips[0].String()
			}
		}
		sni := req.CertDomain
		if sni == "" {
			if pl, err := parseNodeLink(req.OrigLink); err == nil {
				sni = pl.SNI
			}
		}
		rl, dl, label, manual := s.computeTwinLinks(req.OrigLink, landingIP, sni, newLanding, rc2, req.LinkName)
		if manual != "" {
			manualNote = manual
		} else {
			links = []labelledLink{{Label: label, Link: rl}, {Label: "直连", Link: dl}}
			if err := upsertLandingLinks(s.nodesFilePath(), landingIP, req.Landing, landingIndex, label, rl, dl); err != nil {
				manualNote = fmt.Sprintf("链接已生成，但写历史失败：%v", err)
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
	resp := map[string]any{"landing_index": landingIndex, "links": links, "qr": qr, "door_note": doorNote}
	if manualNote != "" {
		resp["manual"] = manualNote
	}
	writeJSON(w, 200, okResp(resp))
}

// ---------- 补粘贴原始链接 ----------

// handleSetOrigLink 给已加的落地补粘贴原始节点链接（卡片上的
// “粘贴原始链接生成中转链接”入口与管理页共用）：存 state、现场算链接、
// 写进 nodes.txt（显式传落地真实编号），卡片上直接出中转链接。
// 原始链接只存 state.json（600），不进日志与审计。
func (s *Server) handleSetOrigLink(w http.ResponseWriter, r *http.Request) {
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
	var req struct {
		OrigLink string `json:"orig_link"`
		LinkName string `json:"link_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	req.OrigLink = strings.TrimSpace(req.OrigLink)
	req.LinkName = strings.TrimSpace(req.LinkName)
	if req.LinkName != "" && (len(req.LinkName) > 64 || strings.ContainsAny(req.LinkName, "\n\r")) {
		writeJSON(w, 400, errResp("节点名称太长或含有非法字符（不能超过 64 字，不能含换行）"))
		return
	}
	if req.OrigLink == "" {
		writeJSON(w, 400, errResp("原始节点链接不能为空"))
		return
	}
	if _, err := parseNodeLink(req.OrigLink); err != nil {
		writeJSON(w, 400, errResp("原始节点链接不对："+err.Error()))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	user := s.currentUser(r)
	s.stateMu.Lock()
	s.state.ext(l.Landing).OrigLink = req.OrigLink
	s.state.ext(l.Landing).OrigLinkName = req.LinkName
	s.stateMu.Unlock()
	_ = s.saveState()
	landingIP := l.Landing
	if !isIP(landingIP) {
		if ips, err := net.LookupIP(landingIP); err == nil && len(ips) > 0 {
			landingIP = ips[0].String()
		}
	}
	sni := l.CertDomain
	if sni == "" {
		if pl, err := parseNodeLink(req.OrigLink); err == nil {
			sni = pl.SNI
		}
	}
	rl, dl, label, manual := s.computeTwinLinks(req.OrigLink, landingIP, sni, l, rc, req.LinkName)
	if manual != "" {
		// 链接已存下（以后模式/地址改了能重算），这次只是生成失败：照实返回原因
		writeJSON(w, 200, map[string]any{"ok": true, "relay_link": rl, "direct_link": dl, "manual": manual, "relay_label": label})
		return
	}
	links := []labelledLink{{Label: label, Link: rl}, {Label: "直连", Link: dl}}
	persistErr := ""
	if err := upsertLandingLinks(s.nodesFilePath(), landingIP, l.Landing, index, label, rl, dl); err != nil {
		persistErr = fmt.Sprintf("历史记录保存失败：%v", err)
	}
	s.audit(user, "补粘贴原始链接", fmt.Sprintf("落地 %d（%s）", index, l.displayName()), "成功", "")
	resp := okResp(map[string]any{"links": links, "qr": linksQR(links), "relay_label": label})
	if persistErr != "" {
		resp["persist_error"] = persistErr
	}
	writeJSON(w, 200, resp)
}

// ---------- 编辑落地 ----------

func boolTo01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func boolToOnOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

// appendEndpointsForLanding 把一个落地的 endpoints 段追加到 config.toml 文本后
// （分流走的端口不写进 realm：80 归 nginx、443 归 haproxy）。
func appendEndpointsForLanding(tomlData []byte, l Landing, share80, share443 bool) []byte {
	var add strings.Builder
	for _, p := range l.Ports {
		if p.Proto == "tcp" && p.Listen == 80 && share80 {
			continue
		}
		if p.Proto == "tcp" && p.Listen == 443 && share443 {
			continue
		}
		add.WriteString(fmt.Sprintf("\n[[endpoints]]\nlisten = \"0.0.0.0:%d\"\nremote = \"%s:%d\"\n", p.Listen, l.Landing, p.Remote))
	}
	return append(append([]byte{}, tomlData...), []byte(add.String())...)
}

// rebuildRelayConfForEdit 编辑后重建 relay.conf 行：只改目标落地那几行
// （地址、预设、模式、端口、证书域名、名称），其余行原样保留；
// ALL_PORTS 按全部落地重算；SHARE80/SHARE443 按 newShare 落盘；
// RELAY_ADDR 只在任意门模式给了新值时才改。
func rebuildRelayConfForEdit(rc *RelayConf, index int, nl Landing, newShare80, newShare443 bool, relayAddr string) []string {
	suffix := ""
	if index != 1 {
		suffix = "_" + strconv.Itoa(index)
	}
	lines := rc.Lines
	set := func(key, value string) { lines = setRelayConfValue(lines, key, value) }
	set("LANDING"+suffix, nl.Landing)
	if !isIP(nl.Landing) {
		set("LANDING_IS_DOMAIN"+suffix, "1")
		set("DOMAIN"+suffix, nl.Landing)
	} else {
		set("LANDING_IS_DOMAIN"+suffix, "0")
		set("DOMAIN"+suffix, "")
	}
	set("CERT_DOMAIN"+suffix, nl.CertDomain)
	// 证书签发方式：有值才写，没值就删掉这键（旧 relay.conf 没这字段时不改写）
	if nl.CertMode != "" {
		set("CERT_MODE"+suffix, nl.CertMode)
	} else {
		lines = removeRelayConfValue(lines, "CERT_MODE"+suffix)
	}
	set("PRESET"+suffix, nl.Preset)
	set("MODE_NOTE"+suffix, nl.ModeNote)
	if nl.Mode == "door" {
		set("MODE"+suffix, "door")
	} else {
		lines = removeRelayConfValue(lines, "MODE"+suffix)
	}
	set("PORTS"+suffix, portsToSpecString(nl.Ports))
	// 名称只改显示；等于落地地址或空着就不存 NAME 键，显示时回落到地址
	if nl.Name != "" && nl.Name != nl.Landing {
		set("NAME"+suffix, nl.Name)
	} else {
		lines = removeRelayConfValue(lines, "NAME"+suffix)
	}
	if relayAddr != "" {
		set("RELAY_ADDR", relayAddr)
	}
	set("SHARE80", boolTo01(newShare80))
	set("SHARE443", boolTo01(newShare443))
	// ALL_PORTS 重算（其余落地的端口不变，只把本落地换成新的）
	var all []string
	seen := map[string]bool{}
	for _, l := range rc.Landings {
		ports := l.PortsRaw
		if l.Index == index {
			ports = portsToSpecString(nl.Ports)
		}
		for _, spec := range strings.Fields(ports) {
			if !seen[spec] {
				seen[spec] = true
				all = append(all, spec)
			}
		}
	}
	lines = removeRelayConfValue(lines, "ALL_PORTS")
	lines = append(lines, `ALL_PORTS="`+strings.Join(all, " ")+`"`)
	return lines
}

// regenShareConfigs 编辑后重建分流配置：
// 仍开着的分流跑 proxy.sh --regen-80/--regen-443（按新 relay.conf 重生成配置并重载）；
// 刚关掉的分流清掉旧配置、释放端口，让 realm 能重新监听。
// proxy.sh 不在时直接返回 nil（测试环境）。失败返回错误说明，由调用方回滚。
func (s *Server) regenShareConfigs(oldShare80, oldShare443, newShare80, newShare443 bool) error {
	proxy := filepath.Join(s.env.KitDir, "proxy.sh")
	if _, err := os.Stat(proxy); err != nil {
		return nil
	}
	env := []string{"REALM_DIR=" + s.env.RealmDir}
	if newShare80 {
		if _, err := s.runScript("proxy.sh", 60*time.Second, env, "--regen-80"); err != nil {
			return fmt.Errorf("80 分流配置重生成失败：%v", err)
		}
	} else if oldShare80 {
		// 80 分流刚关：删掉 relay-80.conf，重载 nginx 释放 80
		_ = os.Remove("/etc/nginx/conf.d/relay-80.conf")
		if err := runCmd(nil, 10*time.Second, "nginx", "-t"); err == nil {
			_ = runCmd(nil, 10*time.Second, "systemctl", "reload", "nginx")
		}
	}
	if newShare443 {
		if _, err := s.runScript("proxy.sh", 60*time.Second, env, "--regen-443"); err != nil {
			return fmt.Errorf("443 分流配置重生成失败：%v", err)
		}
	} else if oldShare443 {
		// 443 分流刚关：haproxy 整个配置都是 relay-node 生成的，停掉它释放 443
		_ = runCmd(nil, 10*time.Second, "systemctl", "stop", "haproxy")
		_ = runCmd(nil, 10*time.Second, "pkill", "-x", "haproxy")
	}
	return nil
}

func (s *Server) handleUpdateLanding(w http.ResponseWriter, r *http.Request) {
	index, err := pathIndex(r)
	if err != nil {
		writeJSON(w, 400, errResp("落地编号不对"))
		return
	}
	old, rc, err := s.landingByIndex(index)
	if err != nil {
		writeJSON(w, 404, errResp(err.Error()))
		return
	}
	var req addLandingReq
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if msg := validateLandingReqFields(req); msg != "" {
		writeJSON(w, 400, errResp(msg))
		return
	}
	if req.Mode == "" {
		req.Mode = "standard"
	}
	// 地址不能撞上别的落地（否则扩展状态与 twin 分组都会认错落地）
	for _, l := range rc.Landings {
		if l.Index != index && l.Landing == req.Landing {
			writeJSON(w, 400, errResp("该落地地址已存在：先改掉另一个落地的地址"))
			return
		}
	}
	// ---- 算新端口（与添加落地同一套规则，但排除自己旧端口）----
	ports := presetPorts(req.Preset, req.SBMode, req.CustomPorts, req.WantUDP)
	rcExcl := &RelayConf{Share80: rc.Share80, Share443: rc.Share443}
	for _, l := range rc.Landings {
		if l.Index != index {
			rcExcl.Landings = append(rcExcl.Landings, l)
		}
	}
	ignoreSelf := map[string]bool{}
	for _, p := range old.Ports {
		ignoreSelf[fmt.Sprintf("%s:%d", p.Proto, p.Listen)] = true
	}
	doorNote := ""
	if req.Mode == "door" {
		ports = doorMapPorts(ports)
		if len(req.DoorPortOverrides) > 0 {
			ov := map[int]int{}
			for k, v := range req.DoorPortOverrides {
				rp, _ := strconv.Atoi(strings.TrimSpace(k))
				ov[rp] = v
			}
			ports = applyDoorPortOverrides(ports, ov)
			if conflicts, _, _ := s.checkPortConflictsExcluding(ports, rcExcl, ignoreSelf); len(conflicts) > 0 {
				writeJSON(w, 400, errResp("端口已被占用："+strings.Join(conflicts, ", ")+"。改一下转发端口覆盖再提交。"))
				return
			}
		} else if newPorts, bumps, err := s.bumpDoorPortsExcluding(ports, rcExcl, ignoreSelf); err != nil {
			writeJSON(w, 400, errResp(err.Error()))
			return
		} else {
			ports = newPorts
			if len(bumps) > 0 {
				var notes []string
				for _, b := range bumps {
					notes = append(notes, fmt.Sprintf("转发端口 %d 已被占用，已改用 %d", b.Old, b.New))
				}
				doorNote = strings.Join(notes, "；")
			}
		}
	} else {
		if len(req.ListenOverrides) > 0 {
			for k, v := range req.ListenOverrides {
				rp, _ := strconv.Atoi(strings.TrimSpace(k))
				for i := range ports {
					if ports[i].Listen == rp {
						ports[i].Listen = v
					}
				}
			}
		}
		conflicts, realm80, realm443 := s.checkPortConflictsExcluding(ports, rcExcl, ignoreSelf)
		if len(conflicts) > 0 && req.ShareMode == "" {
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
		if req.ShareMode != "" {
			// 分流方式只解决 80/443 的冲突，其余端口冲突仍要拒绝
			var rest []string
			for _, c := range conflicts {
				if (req.ShareMode == "2" || req.ShareMode == "4") && c == "80/tcp" {
					continue
				}
				if (req.ShareMode == "3" || req.ShareMode == "4") && c == "443/tcp" {
					continue
				}
				rest = append(rest, c)
			}
			if len(rest) > 0 {
				writeJSON(w, 400, errResp("端口已被占用："+strings.Join(rest, ", ")+"。分流只能解决 80/443 的冲突，其余端口请改监听端口。"))
				return
			}
			if req.ShareMode == "1" && len(conflicts) > 0 {
				writeJSON(w, 400, errResp("端口已被占用："+strings.Join(conflicts, ", ")+"。方式 1 需要填高位监听端口覆盖。"))
				return
			}
		}
	}
	// ---- 新落地字段 ----
	isDomain := !isIP(req.Landing)
	domain := ""
	if isDomain {
		domain = req.Landing
	}
	certDomain := req.CertDomain
	if certDomain == "" {
		if isDomain {
			certDomain = req.Landing
		} else {
			certDomain = old.CertDomain
		}
	}
	// 证书签发方式：没填就沿用旧值（旧落地没这字段时保持空白、按老逻辑判）；
	// 填了就写入，编辑时可补填。
	certMode := req.CertMode
	if certMode == "" {
		certMode = old.CertMode
	}
	newPortsStr := portsToSpecString(ports)
	newLanding := Landing{Index: index, Landing: req.Landing, Domain: domain, CertDomain: certDomain, Preset: req.Preset, ModeNote: modeNoteFor(req.Preset, req.SBMode, req.Mode == "door"), Mode: req.Mode, Ports: ports, PortsRaw: newPortsStr, Name: req.Name, CertMode: certMode}
	// 分流开关：显式选 2/3/4 就开对应项；否则沿用旧开关，但没落地再用 80/443 就关掉
	newShare80, newShare443 := rc.Share80, rc.Share443
	if req.ShareMode == "2" || req.ShareMode == "4" {
		newShare80 = true
	}
	if req.ShareMode == "3" || req.ShareMode == "4" {
		newShare443 = true
	}
	has80, has443 := false, false
	for _, l := range rc.Landings {
		pl := l.Ports
		if l.Index == index {
			pl = ports
		}
		for _, p := range pl {
			if p.Proto == "tcp" && p.Listen == 80 && p.Remote == 80 {
				has80 = true
			}
			if p.Proto == "tcp" && p.Listen == 443 && p.Remote == 443 {
				has443 = true
			}
		}
	}
	if !has80 {
		newShare80 = false
	}
	if !has443 {
		newShare443 = false
	}
	relayAddrForConf := ""
	if req.Mode == "door" && req.RelayAddr != "" && req.RelayAddr != rc.RelayAddr {
		relayAddrForConf = req.RelayAddr
	}
	needsRestart := old.Landing != req.Landing || old.PortsRaw != newPortsStr || old.Mode != req.Mode ||
		old.CertDomain != certDomain || old.Preset != req.Preset || relayAddrForConf != "" ||
		newShare80 != rc.Share80 || newShare443 != rc.Share443
	// ---- 审计用的改动清单（不含原始链接本身，只记“已更新/已清空”）----
	var changes []string
	if old.Landing != req.Landing {
		changes = append(changes, fmt.Sprintf("地址：%s→%s", old.Landing, req.Landing))
	}
	if old.PortsRaw != newPortsStr {
		changes = append(changes, fmt.Sprintf("端口：%s→%s", old.PortsRaw, newPortsStr))
	}
	if old.Mode != req.Mode {
		changes = append(changes, fmt.Sprintf("模式：%s→%s", old.Mode, req.Mode))
	}
	if old.CertDomain != certDomain {
		changes = append(changes, fmt.Sprintf("证书域名：%s→%s", old.CertDomain, certDomain))
	}
	if old.CertMode != certMode {
		changes = append(changes, fmt.Sprintf("证书签发方式：%s→%s", certModeText(old.CertMode), certModeText(certMode)))
	}
	if old.displayName() != newLanding.displayName() {
		changes = append(changes, fmt.Sprintf("名称：%s→%s", old.displayName(), newLanding.displayName()))
	}
	if relayAddrForConf != "" {
		changes = append(changes, fmt.Sprintf("中转地址：%s→%s", rc.RelayAddr, relayAddrForConf))
	}
	if newShare80 != rc.Share80 {
		changes = append(changes, "80 分流："+boolToOnOff(rc.Share80)+"→"+boolToOnOff(newShare80))
	}
	if newShare443 != rc.Share443 {
		changes = append(changes, "443 分流："+boolToOnOff(rc.Share443)+"→"+boolToOnOff(newShare443))
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	user := s.currentUser(r)
	if _, err := s.backupNow("编辑落地前自动备份", "realm"); err != nil {
		writeJSON(w, 500, errResp("备份失败，操作已取消："+err.Error()))
		return
	}
	// 旧扩展状态（改地址前先取出来，写完再决定迁不迁移）
	s.stateMu.Lock()
	oldExt := *s.state.ext(old.Landing)
	s.stateMu.Unlock()
	// 1. relay.conf + config.toml（原子写，失败回滚）
	newConfLines := rebuildRelayConfForEdit(rc, index, newLanding, newShare80, newShare443, relayAddrForConf)
	newConf := []byte(strings.Join(newConfLines, "\n") + "\n")
	oldConf := []byte(strings.Join(rc.Lines, "\n"))
	tomlData, _ := s.readConfigToml()
	oldToml := tomlData
	var newToml []byte
	if needsRestart {
		newToml = removeEndpointsFromToml(tomlData, old.Landing)
		newToml = appendEndpointsForLanding(newToml, newLanding, newShare80, newShare443)
		// realm 无 endpoint 会 panic：留占位
		if !strings.Contains(string(newToml), "[[endpoints]]") {
			newToml = append(newToml, []byte("\n[[endpoints]]\nlisten = \"127.0.0.1:9\"\nremote = \"127.0.0.1:9\"\n")...)
		}
		if err := s.writeConfigTomlAtomic(newToml); err != nil {
			writeJSON(w, 500, errResp("写 config.toml 失败："+err.Error()))
			return
		}
	}
	if err := s.writeRelayConfAtomic(newConf); err != nil {
		if needsRestart {
			_ = s.writeConfigTomlAtomic(oldToml)
		}
		writeJSON(w, 500, errResp("写 relay.conf 失败，已回滚："+err.Error()))
		return
	}
	rollback := func() {
		if needsRestart {
			_ = s.writeConfigTomlAtomic(oldToml)
		}
		_ = s.writeRelayConfAtomic(oldConf)
		_ = s.regenShareConfigs(newShare80, newShare443, rc.Share80, rc.Share443)
		_ = s.restartRealm()
	}
	// 2. 新开分流：跑 proxy.sh --enable-share*（装组件、写配置、挪端口、重启 realm）
	if needsRestart && ((newShare80 && !rc.Share80) || (newShare443 && !rc.Share443)) {
		env := []string{"REALM_DIR=" + s.env.RealmDir}
		if newShare80 && !rc.Share80 {
			if res, err := s.runScript("proxy.sh", 120*time.Second, env, "--enable-share80"); err != nil {
				rollback()
				writeJSON(w, 500, errResp("开 80 分流失败，已回滚："+sanitize(lastLines(res.Stdout+res.Stderr, 5))))
				return
			}
		}
		if newShare443 && !rc.Share443 {
			if res, err := s.runScript("proxy.sh", 120*time.Second, env, "--enable-share443"); err != nil {
				rollback()
				writeJSON(w, 500, errResp("开 443 分流失败，已回滚："+sanitize(lastLines(res.Stdout+res.Stderr, 5))))
				return
			}
		}
	}
	// 3. 分流配置重生成（仍开着的重生成，刚关的清配置放端口）+ 重启 realm + 验端口
	if needsRestart {
		if err := s.regenShareConfigs(rc.Share80, rc.Share443, newShare80, newShare443); err != nil {
			rollback()
			writeJSON(w, 500, errResp("分流配置重生成失败，已回滚："+err.Error()))
			return
		}
		if err := s.restartRealm(); err != nil {
			rollback()
			writeJSON(w, 500, errResp("重启 realm 失败，已回滚："+err.Error()))
			return
		}
		if err := verifyPorts(newLanding.Ports); err != nil {
			// 端口没起来只警告：配置已生效，可能是落地机本身不通，不回滚
			doorNote = appendNote(doorNote, "注意："+err.Error())
		}
	}
	// 4. 扩展状态：改地址时旧键整体迁移到新键（到期、流量限制、限速、子节点、原始链接全在里面），不能丢
	s.stateMu.Lock()
	ext := s.state.ext(old.Landing)
	if req.Landing != old.Landing {
		if _, exists := s.state.Landings[req.Landing]; !exists {
			s.state.Landings[req.Landing] = ext
		}
		delete(s.state.Landings, old.Landing)
		ext = s.state.ext(req.Landing)
		// 流量计数跟着地址走（累计、月度、采样基数一起搬）
		s.traffic.rename(old.Landing, req.Landing)
		// 延迟探测结果跟新地址走：旧探测目标的结果删掉，下一轮重探；
		// 证书提醒日期也跟着新地址走，避免重复提醒
		s.latency.remove(latencyStoreKey(*old))
		s.latencyMu.Lock()
		delete(s.latencyAlerts, latencyStoreKey(*old))
		s.latencyMu.Unlock()
		if d, ok := s.certAlertDay[old.Landing]; ok {
			s.certAlertDay[req.Landing] = d
			delete(s.certAlertDay, old.Landing)
		}
	}
	if req.DurationDays > 0 {
		ext.Expiry = time.Now().Add(time.Duration(req.DurationDays) * 24 * time.Hour).UTC().Format(time.RFC3339)
		changes = append(changes, fmt.Sprintf("有效期：从现在起 %d 天", req.DurationDays))
	} else if req.DurationDays == 0 && oldExt.Expiry != "" {
		ext.Expiry = ""
		changes = append(changes, "有效期：改为永久")
	}
	gb := func(b int64) string {
		if b <= 0 {
			return "不限"
		}
		return fmt.Sprintf("%.0f GB", float64(b)/1073741824)
	}
	if ext.TrafficLimitBytes != req.TrafficLimitBytes || ext.TrafficLimitMode != req.TrafficLimitMode {
		changes = append(changes, fmt.Sprintf("流量上限：%s→%s", gb(oldExt.TrafficLimitBytes), gb(req.TrafficLimitBytes)))
	}
	ext.TrafficLimitBytes = req.TrafficLimitBytes
	ext.TrafficLimitMode = req.TrafficLimitMode
	if ext.SpeedLimitMbps != req.SpeedLimitMbps {
		changes = append(changes, fmt.Sprintf("限速：%d→%d Mbps", oldExt.SpeedLimitMbps, req.SpeedLimitMbps))
	}
	ext.SpeedLimitMbps = req.SpeedLimitMbps
	if ext.OrigLink != req.OrigLink {
		if req.OrigLink == "" {
			changes = append(changes, "原始链接：已清空")
		} else {
			changes = append(changes, "原始链接：已更新")
		}
	}
	if ext.OrigLinkName != req.LinkName {
		if req.LinkName == "" {
			changes = append(changes, "节点名称：已清空（改回用原始链接备注名）")
		} else {
			changes = append(changes, fmt.Sprintf("节点名称：%s", req.LinkName))
		}
	}
	ext.OrigLink = req.OrigLink
	ext.OrigLinkName = req.LinkName
	s.stateMu.Unlock()
	_ = s.saveState()
	// 5. 流量链与限速重建（端口或限额变了才重建）
	if needsRestart || ext.TrafficLimitBytes != oldExt.TrafficLimitBytes || ext.TrafficLimitMode != oldExt.TrafficLimitMode || ext.SpeedLimitMbps != oldExt.SpeedLimitMbps {
		if rc2, _, err := s.readRelayConf(); err == nil {
			_ = s.ensureTrafficChains(rc2.Landings)
		}
		if err := s.applySpeedLimit(newLanding, req.SpeedLimitMbps); err != nil {
			doorNote = appendNote(doorNote, "限速未生效："+err.Error())
		}
	}
	// 6. 原始链接还在：用新地址/端口重算链接，更新 nodes.txt；
	//    地址变了先删旧分组，避免历史里同时出现新旧两个分组
	var links []labelledLink
	qr := map[string]string{}
	if req.OrigLink != "" {
		landingIP := req.Landing
		if !isIP(landingIP) {
			if ips, err := net.LookupIP(landingIP); err == nil && len(ips) > 0 {
				landingIP = ips[0].String()
			}
		}
		sni := certDomain
		if sni == "" {
			if pl, err := parseNodeLink(req.OrigLink); err == nil {
				sni = pl.SNI
			}
		}
		rc2, _, _ := s.readRelayConf()
		rl, dl, label, manual := s.computeTwinLinks(req.OrigLink, landingIP, sni, &newLanding, rc2, req.LinkName)
		if manual == "" {
			links = []labelledLink{{Label: label, Link: rl}, {Label: "直连", Link: dl}}
			qr = linksQR(links)
			if req.Landing != old.Landing {
				_ = removeLinksGroup(s.nodesFilePath(), old.Landing)
			}
			if err := upsertLandingLinks(s.nodesFilePath(), landingIP, req.Landing, index, label, rl, dl); err != nil {
				doorNote = appendNote(doorNote, "历史记录保存失败："+err.Error())
			}
		} else {
			doorNote = appendNote(doorNote, "链接生成失败："+manual)
		}
	} else if req.Landing != old.Landing {
		// 地址变了、原始链接已清空：旧分组也删掉，别留着指向旧地址的链接
		_ = removeLinksGroup(s.nodesFilePath(), old.Landing)
	}
	if len(changes) == 0 {
		changes = append(changes, "无实质改动")
	}
	s.audit(user, "编辑落地", fmt.Sprintf("落地 %d（%s）", index, newLanding.displayName()), "成功", strings.Join(changes, "；"))
	resp := okResp(map[string]any{"landing_index": index, "links": links, "qr": qr, "door_note": doorNote, "restarted": needsRestart})
	writeJSON(w, 200, resp)
}

// appendNote 把一条提示拼到已有提示后面（空串时不留前导分号）。
func appendNote(base, addition string) string {
	if addition == "" {
		return base
	}
	if base == "" {
		return addition
	}
	return base + "；" + addition
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
	return s.checkPortConflictsExcluding(ports, rc, nil)
}

// checkPortConflictsExcluding 与 checkPortConflicts 同一套规则，多一个 ignore：
// 键是 "协议:端口"（如 "tcp:443"），落在里面的端口不算冲突。
// 编辑落地时用它排除被编辑落地自己的旧监听端口——realm 正监听着它们，
// 重启后会释放重绑，不算真冲突；不排除的话只改个证书域名都会报端口冲突。
func (s *Server) checkPortConflictsExcluding(ports []PortSpec, rc *RelayConf, ignore map[string]bool) ([]string, bool, bool) {
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
		if ignore[key] {
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
	return s.bumpDoorPortsExcluding(ports, rc, nil)
}

// bumpDoorPortsExcluding 与 bumpDoorPorts 同一套顺延逻辑，多一个 ignore
// （"协议:端口"，如 "tcp:20443"）：落在里面的端口在找空闲候选时也算空闲。
// 编辑落地时传被编辑落地自己的旧监听端口——realm 正监听着它们，
// 重启后会释放，候选端口撞上它们不算真冲突。
func (s *Server) bumpDoorPortsExcluding(ports []PortSpec, rc *RelayConf, ignore map[string]bool) ([]PortSpec, []doorBump, error) {
	conflicts, _, _ := s.checkPortConflictsExcluding(ports, rc, ignore)
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
					// ignore 里的端口是被编辑落地自己的旧监听，realm 重启后释放，算空闲
					if (listening[ck] && !ignore[ck]) || batch[ck] {
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
	if err := decodeJSON(r, &req); err != nil || req.Days < 1 || req.Days > 3650 {
		writeJSON(w, 400, errResp("续期天数须在 1–3650 天之间"))
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
	type lf struct{ landing, isDomain, domain, cert, preset, mode, doorMode, ports, name string }
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
		list = append(list, lf{get("LANDING"), get("LANDING_IS_DOMAIN"), get("DOMAIN"), get("CERT_DOMAIN"), get("PRESET"), get("MODE_NOTE"), get("MODE"), get("PORTS"), get("NAME")})
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
		// NAME 同理：只在非空时写回，删落地不丢其余落地的名称
		if f.name != "" {
			lines = append(lines, `NAME`+suffix+`="`+f.name+`"`)
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
		CertDomain: l.CertDomain, CertMode: l.CertMode, DeletedAt: now.Format(time.RFC3339),
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
	// 恢复证书签发方式（旧回收站记录没存时不写）
	if it.CertMode != "" {
		lines = append(lines, "CERT_MODE"+suffix+"=\""+it.CertMode+"\"")
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
	groups, err := s.loadLinkGroups()
	if err != nil {
		writeJSON(w, 200, okResp(map[string]any{"groups": []any{}, "note": "nodes.txt 不存在：先在落地机上跑安装第 7 步生成链接，或跑 bash install.sh --nodes"}))
		return
	}
	for i := range groups {
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

// handleDeleteLinksGroups 删除历史记录里的指定分组（只删 nodes.txt 里的
// 历史，不动落地转发与节点本身）：POST /api/v1/links/delete，
// body {indices:[...]}，indices 是 GET /api/v1/links 返回的分组在文件里的
// 序号（0 起，按文件顺序，与 parseNodesTxt / splitNodesTxt 同序）。
// 删完写审计日志一次。注意：之后再跑 install.sh --nodes 会把已登记落地的
// 历史重新生成出来（--nodes 本来就会给登记过的落地补链接），手动 twin 分组
// 删了就不再回来。
func (s *Server) handleDeleteLinksGroups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Indices []int `json:"indices"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if len(req.Indices) == 0 {
		writeJSON(w, 400, errResp("请先勾选要删除的历史记录"))
		return
	}
	user := s.currentUser(r)
	removed, landings, err := removeLinksGroups(s.nodesFilePath(), req.Indices)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	if removed == 0 {
		writeJSON(w, 400, errResp("没有删掉任何记录：序号可能已过期，请刷新页面再试"))
		return
	}
	s.audit(user, "删除历史记录", strings.Join(landings, "、"), "成功", fmt.Sprintf("删了 %d 组历史记录（只删历史，不影响落地转发与节点本身）", removed))
	writeJSON(w, 200, okResp(map[string]any{"removed": removed}))
}

// computeTwinLinks 从一条原始节点链接算出中转+直连链接（twin/door 逻辑唯一入口，
// 节点链接页、加落地、补粘贴、编辑后重算都走这里，不再各写一份）。
// linkName 是节点名称（选填）：直连备注改成“<名称>-直连”，中转备注改成
// “<名称>-中转”；空着就用原始链接自己的备注名；原始链接也没有备注名就不改名。
// 返回 relayLink、directLink、relayLabel（"中转"或"任意门"）、manual
// （空串=成功；非空=只能手动处理的原因，此时 relayLink/directLink 按下面规则回退）。
func (s *Server) computeTwinLinks(origLink, landingIP, sni string, landing *Landing, rc *RelayConf, linkName string) (relayLink, directLink, relayLabel, manual string) {
	if _, err := parseNodeLink(origLink); err != nil {
		return origLink, "", "中转", "链接不对：" + err.Error()
	}
	relayLabel = "中转"
	isDoor := landing != nil && landing.Mode == "door"
	if isDoor {
		relayLabel = "任意门"
	}
	res, err := s.runScript("install.sh", 60*time.Second, nil, "--twin", origLink, landingIP, sni)
	twin := strings.TrimSpace(res.Stdout)
	if err != nil || strings.HasPrefix(twin, "__MANUAL__:") {
		reason := strings.TrimPrefix(twin, "__MANUAL__:")
		if reason == "" {
			reason = "这条链接不会自动生成直连，请手动改地址（把地址换成落地 IP，TLS 的 SNI 填域名）"
		}
		return origLink, "", relayLabel, reason
	}
	// 中转链接：原链接按实际监听端口换（install.sh --nodes 的逻辑在面板侧简化：端口不变，
	// 除非该落地监听端口与落地端口不同——这里用 relay.conf 查）
	relayLink = origLink
	if isDoor {
		// 任意门模式：跑 install.sh --door 生成任意门链接（地址换成中转地址、端口换成转发端口）。
		forwardPort := 0
		if pl, err := parseNodeLink(origLink); err == nil && pl.Port > 0 {
			forwardPort = pl.Port
			for _, p := range landing.Ports {
				if p.Remote == pl.Port {
					forwardPort = p.Listen
					break
				}
			}
		}
		if rc.RelayAddr == "" {
			return "", twin, relayLabel, "任意门地址还没设置：先到「设置 → 任意门地址」填好中转地址，再重新生成"
		}
		dres, derr := s.runScript("install.sh", 60*time.Second, nil, "--door", origLink, rc.RelayAddr, strconv.Itoa(forwardPort), sni)
		doorLink := strings.TrimSpace(dres.Stdout)
		if derr != nil || strings.HasPrefix(doorLink, "__MANUAL__:") {
			reason := strings.TrimPrefix(doorLink, "__MANUAL__:")
			if reason == "" {
				reason = "任意门链接生成失败，请手动改地址（把地址换成中转地址、端口换成转发端口）"
			}
			return "", twin, relayLabel, reason
		}
		relayLink = doorLink
	} else if landing != nil {
		if pl, err := parseNodeLink(origLink); err == nil && pl.Port > 0 {
			for _, p := range landing.Ports {
				if p.Remote == pl.Port && p.Listen != p.Remote {
					relayLink = setLinkPort(origLink, p.Listen)
				}
			}
		}
	}
	// 备注改名：直连“<名称>-直连”、中转“<名称>-中转”，导进手机能分清；
	// 名称空着就用原始链接自己的备注名，都没有就不改名。
	base := strings.TrimSpace(linkName)
	if base == "" {
		if pl, err := parseNodeLink(origLink); err == nil {
			base = pl.Name
		}
	}
	if base != "" {
		relayLink = setLinkName(relayLink, base+"-中转")
		twin = setLinkName(twin, base+"-直连")
	}
	return relayLink, twin, relayLabel, ""
}

func (s *Server) handleTwin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Link      string `json:"link"`
		LandingIP string `json:"landing_ip"`
		SNI       string `json:"sni"`
		// Name 节点名称（选填）：直连备注“<名称>-直连”、中转备注“<名称>-中转”；
		// 空着用原始链接自己的备注名。
		Name string `json:"name"`
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
	relayLink, twin, relayLabel, manual := s.computeTwinLinks(req.Link, landingIP, sni, landing, rc, req.Name)
	if manual != "" {
		writeJSON(w, 200, map[string]any{"ok": true, "relay_link": relayLink, "direct_link": twin, "manual": manual, "relay_label": relayLabel})
		return
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
		var daysLeft any
		var expiry string
		hasCert := false
		if c, ok := certByIndex[l.Index]; ok {
			// days_left 为 null 不算拿到证书（自定义端口 TLS 落地就是这种情况），
			// 继续走下面的握手回退。
			if dl, ok := c["days_left"].(float64); ok {
				daysLeft = int(dl)
				hasCert = true
			} else if c["days_left"] != nil {
				daysLeft = c["days_left"]
				hasCert = true
			}
			if e, ok := c["expiry"].(string); ok {
				expiry = e
			}
		}
		if !hasCert {
			// 回退：按落地实际的 TCP 监听端口逐个握手（以前只认 443）。
			if ci, ok := probeCertForLanding(l); ok {
				daysLeft = *ci.DaysLeft
				expiry = ci.Expiry
				hasCert = true
			}
		}
		// renewMode 按探测结果判：握手成功就不是明文落地。
		renewMode, needManual := renewModeFor(l, rc.Share443, hasCert)
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
	out, note := collectLogLines(b, err, lines, func(n int) (string, error) {
		res, err2 := runCmdOut(nil, 10*time.Second, "journalctl", "-u", "realm", "-n", strconv.Itoa(n), "--no-pager")
		if err2 != nil {
			return "", err2
		}
		return res.Stdout, nil
	})
	resp := map[string]any{"lines": out}
	if note != "" {
		resp["note"] = note
	}
	writeJSON(w, 200, okResp(resp))
}

// filterLogLines 去掉空行（空字符串或只含空白字符的行）与 journalctl 的
// “无记录”标记行（-- No entries --）。
func filterLogLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if t := strings.TrimSpace(l); t == "" || t == "-- No entries --" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// collectLogLines 决定实时日志页显示什么。
// fileContent/fileErr 是 /var/log/realm.log 的读取结果；journal 是 journalctl 回退（出错返回 error）。
// 返回要显示的行与空时的提示（有行时提示为空串）。
// realm 日志级别是 warn，正常运行时不产生日志，文件常常是空的：这时回退查 journalctl
// （systemd 的启动/停止记录），有内容就显示出来；两边都没有才返回 warn 级别的说明。
func collectLogLines(fileContent []byte, fileErr error, n int, journal func(int) (string, error)) ([]string, string) {
	if fileErr == nil {
		all := filterLogLines(strings.Split(string(fileContent), "\n"))
		if len(all) > n {
			all = all[len(all)-n:]
		}
		if len(all) > 0 {
			return all, ""
		}
		// 文件为空（或只有空行）：回退查 journalctl
	}
	if journal != nil {
		if out, err := journal(n); err == nil {
			jl := filterLogLines(strings.Split(out, "\n"))
			if len(jl) > n {
				jl = jl[len(jl)-n:]
			}
			if len(jl) > 0 {
				return jl, ""
			}
		}
	}
	if fileErr != nil {
		return []string{}, "日志文件不存在：realm 还没跑过，或日志不在 /var/log/realm.log"
	}
	return []string{}, "暂无日志：realm 日志级别为 warn，正常运行时不产生日志，只有警告或错误时才会记录。"
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
