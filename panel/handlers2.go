package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- 设置 ----------

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	latest, update := s.latestVersion()
	relayAddr := ""
	if rc, _, err := s.readRelayConf(); err == nil {
		relayAddr = rc.RelayAddr
	}
	writeJSON(w, 200, okResp(map[string]any{
		"port": s.cfg.Port, "https_enabled": s.cfg.HTTPSEnabled, "https_domain": s.cfg.HTTPSDomain,
		"ip_whitelist": s.cfg.IPWhitelist, "relay_addr": relayAddr,
		"manager_only":  s.cfg.ManagerOnly,
		"panel_name":    s.panelName(),
		"telegram":      map[string]any{"enabled": s.cfg.Telegram.Enabled, "chat_id": s.cfg.Telegram.ChatID, "has_token": s.cfg.Telegram.BotToken != ""},
		"monitor":       s.cfg.Monitor,
		"latency":       s.cfg.Latency,
		"backup":        s.cfg.Backup,
		"central_token": map[string]any{"has_token": s.cfg.RemoteTokenHash != "", "created_at": s.cfg.RemoteTokenCreatedAt},
		"subscription":  map[string]any{"has_token": s.cfg.SubTokenHash != "", "has_link": s.cfg.SubTokenEnc != "", "created_at": s.cfg.SubTokenCreatedAt, "public_domain": s.cfg.SubPublicDomain, "worker_origin": s.cfg.WorkerOrigin},
		// 登录安全（v1.3.8，可选，默认关）：telegram_configured 指 Telegram 提醒
		// 真能发（开关开着且 Token 与 Chat ID 都填了），没配好时登录告警开关置灰；
		// 密钥与备用码明文绝不经这个接口返回。
		"security": map[string]any{
			"login_alert": s.cfg.LoginAlert, "telegram_configured": s.telegramReady(),
			"totp_enabled": s.cfg.TOTPEnabled, "totp_has_secret": s.cfg.TOTPSecret != "",
			"backup_codes_remaining": len(s.cfg.TOTPBackupCodes),
			// server_time 供二次验证卡片显示：扫码/输验证码前核对手机时间（带时区）。
			"server_time": serverTimeDisplay(),
		},
		"remotes_count": len(s.cfg.Remotes),
		"cloudflare":    map[string]any{"has_token": s.cfg.CloudflareToken != ""},
		"version":       Version, "latest_version": latest, "update_available": update,
	}))
}

// ---------- 面板名称（v1.3.4） ----------

// handlePanelNameSet 改面板名称：存 config.json 的 panel_name。
// 改名后登录页标题/品牌、侧边栏品牌、manifest 都跟着变（下次打开页面即
// 生效，本次保存后前端刷新页面）；订阅导入客户端后配置名也用这个名字
// （订阅响应带 Content-Disposition 文件名）。名字为空、超 24 字或含
// 文件名/响应头非法字符时返回 400。
func (s *Server) handlePanelNameSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if !validPanelName(name) {
		writeJSON(w, 400, errResp("面板名称不对：1–24 个字，不能含 / \\ : * ? \" < > |，不能以 . 开头"))
		return
	}
	old := s.cfg.PanelName
	s.cfg.PanelName = name
	if err := s.saveConfig(); err != nil {
		s.cfg.PanelName = old
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "改面板名称", "改成 "+name, "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"panel_name": s.panelName()}))
}

// ---------- 管理面板模式切换（v1.2.8） ----------

// handleManagerOnly 在完整面板与管理面板模式之间切换：只改 config.json 的
// manager_only 开关，本地落地/服务数据全部保留，切回来即恢复；
// 切到管理面板后前端隐藏本地区，只显示集中管理、合并订阅与设置。
func (s *Server) handleManagerOnly(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ManagerOnly bool `json:"manager_only"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	old := s.cfg.ManagerOnly
	s.cfg.ManagerOnly = req.ManagerOnly
	if err := s.saveConfig(); err != nil {
		s.cfg.ManagerOnly = old
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	// 切换立即生效：马上刷新健康基线（不发提醒），不用等下一个 5 分钟 tick；
	// 切到管理面板后健康检查不再提醒，切回完整面板时也不拿旧基线误报。
	s.refreshHealthBaseline()
	mode := "完整面板"
	if req.ManagerOnly {
		mode = "管理面板"
	}
	s.audit(s.currentUser(r), "切换面板模式", "切到"+mode, "成功", "本地数据保留，切回来即恢复")
	writeJSON(w, 200, okResp(map[string]any{"manager_only": s.cfg.ManagerOnly}))
}

// ---------- 任意门地址 ----------

// handleRelayAddr 设置/清除任意门地址（relay.conf 的 RELAY_ADDR）。
// 空字符串 = 清除已保存值（删掉 RELAY_ADDR 键）；非空须是合法 IP 或域名。
// 保存成功后把 nodes.txt 里所有 label 为“任意门”的链接地址换成新值（端口不动；
// nodes.txt 不存在就跳过）。RELAY_ADDR 只用于生成链接，不用重启 realm。
func (s *Server) handleRelayAddr(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RelayAddr string `json:"relay_addr"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	addr := strings.TrimSpace(req.RelayAddr)
	if addr != "" && !validIPOrDomain(addr) {
		writeJSON(w, 400, errResp("中转地址不对：填 IP 或域名"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	user := s.currentUser(r)
	rc, _, err := s.readRelayConf()
	if err != nil {
		writeJSON(w, 500, errResp("relay.conf 不存在或读不到：中转还没装好"))
		return
	}
	if _, err := s.backupNow("改任意门地址前自动备份", "realm"); err != nil {
		writeJSON(w, 500, errResp("备份失败，操作已取消："+err.Error()))
		return
	}
	lines := rc.Lines
	if addr == "" {
		lines = removeRelayConfValue(lines, "RELAY_ADDR")
	} else {
		lines = setRelayConfValue(lines, "RELAY_ADDR", addr)
	}
	if err := s.writeRelayConfAtomic([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		writeJSON(w, 500, errResp("写 relay.conf 失败："+err.Error()))
		return
	}
	// 同步改 nodes.txt 里所有“任意门”链接的地址（端口不动；没有 nodes.txt 就跳过；
	// 清除地址时没东西可换，也跳过——下次加任意门落地时再自动探测）。
	if addr != "" {
		if p := s.nodesFilePath(); p != "" {
			if b, err := os.ReadFile(p); err == nil {
				if nb, changed := rewriteDoorLinks(b, addr); changed {
					_ = writeFileAtomic(p, nb, 0o644)
				}
			}
		}
	}
	s.audit(user, "设置任意门地址", addr, "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"relay_addr": addr}))
}

// rewriteDoorLinks 把文本里所有“任意门：<链接>”行的地址换成新地址（端口不动），
// 返回新内容与是否改过。
func rewriteDoorLinks(data []byte, newAddr string) ([]byte, bool) {
	changed := false
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "任意门：") {
			continue
		}
		link := strings.TrimSpace(strings.TrimPrefix(t, "任意门："))
		if nl := setLinkHost(link, newAddr); nl != link {
			lines[i] = "任意门：" + nl
			changed = true
		}
	}
	if !changed {
		return data, false
	}
	return []byte(strings.Join(lines, "\n")), true
}

func (s *Server) latestVersion() (string, bool) {
	// 缓存 1 小时，避免每次打开设置都拉网
	if v, ok := latestVersionCache.get(); ok {
		return v, isNewerVersion(v, Version)
	}
	v, err := fetchLatestVersion()
	if err != nil {
		// 拉取失败：不缓存，界面显示“—”，不冒充已是最新
		return "", false
	}
	latestVersionCache.set(v)
	return v, isNewerVersion(v, Version)
}

// latestVersionFresh 手动“检查更新”用：绕过 1 小时缓存，强制重新拉取。
func (s *Server) latestVersionFresh() (string, bool, error) {
	v, err := fetchLatestVersion()
	if err != nil {
		return "", false, err
	}
	latestVersionCache.set(v)
	return v, isNewerVersion(v, Version), nil
}

// latestVersionURL 远端 VERSION 地址（包级变量，方便测试指向本地 mock 服务）。
var latestVersionURL = "https://raw.githubusercontent.com/nbbk/relay-node/main/panel/VERSION"

func fetchLatestVersion() (string, error) {
	ctx, cancel := ctxTimeout(10 * time.Second)
	defer cancel()
	u := latestVersionURL
	// RELAY_PANEL_VERSION_URL：测试/调试用，覆盖远端 VERSION 地址
	if override := os.Getenv("RELAY_PANEL_VERSION_URL"); override != "" {
		u = override
	}
	// cache-buster：避免 raw.githubusercontent.com 的 CDN 缓存滞后
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	u += sep + "t=" + strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("远端版本号请求失败（HTTP %d）", resp.StatusCode)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("远端版本号为空")
	}
	return v, nil
}

// isNewerVersion 只有远端版本号大于本地才算有更新；解析失败时回退到字符串不等。
func isNewerVersion(latest, current string) bool {
	ln, lok := parseVersionNums(latest)
	cn, cok := parseVersionNums(current)
	if !lok || !cok {
		return latest != "" && latest != current
	}
	n := len(ln)
	if len(cn) > n {
		n = len(cn)
	}
	for i := 0; i < n; i++ {
		var a, b int
		if i < len(ln) {
			a = ln[i]
		}
		if i < len(cn) {
			b = cn[i]
		}
		if a != b {
			return a > b
		}
	}
	return false
}

func parseVersionNums(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		if i := strings.IndexAny(p, "-+"); i >= 0 {
			p = p[:i]
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		nums = append(nums, n)
	}
	return nums, true
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSON(w, 400, errResp("新密码至少 8 位"))
		return
	}
	if !verifyPassword(req.OldPassword, s.cfg.PasswordHash) {
		// 业务失败用 400：旧密码不对不是会话过期，前端不应踢回登录页。
		writeJSON(w, 400, errResp("旧密码不对"))
		return
	}
	h, err := hashPassword(req.NewPassword)
	if err != nil {
		writeJSON(w, 500, errResp("生成密码哈希失败"))
		return
	}
	s.cfg.PasswordHash = h
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存配置失败："+err.Error()))
		return
	}
	// 同步更新 PANEL_HOME/credentials（只更新“密码”行，地址/端口行不动；600 权限，绝不经 API 返回）
	s.syncCredentials(req.NewPassword, 0)
	s.audit(s.currentUser(r), "改密码", "", "成功", "")
	writeJSON(w, 200, okResp(nil))
}

func (s *Server) handleChangePort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port int `json:"port"`
	}
	if err := decodeJSON(r, &req); err != nil || !validPort(req.Port) {
		writeJSON(w, 400, errResp("端口须在 1-65535 之间"))
		return
	}
	oldPort := s.cfg.Port
	s.cfg.Port = req.Port
	if err := s.saveConfig(); err != nil {
		s.cfg.Port = oldPort
		writeJSON(w, 500, errResp("保存配置失败"))
		return
	}
	// credentials 里端口行跟着改（地址行端口一并改）
	s.syncCredentials("", req.Port)
	s.audit(s.currentUser(r), "改面板端口", fmt.Sprintf("%d -> %d", oldPort, req.Port), "成功", "面板正在重启，请用新端口访问")
	writeJSON(w, 200, okResp(map[string]any{"msg": fmt.Sprintf("面板正在重启，请用新端口 %d 访问", req.Port)}))
	// 异步重启自身（先回包再重启）
	go func() {
		time.Sleep(300 * time.Millisecond)
		restartSelf(s.env)
	}()
}

func (s *Server) handleIPWhitelist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Whitelist []string `json:"whitelist"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	for _, ip := range req.Whitelist {
		if ip != "" && !isIP(ip) {
			writeJSON(w, 400, errResp("白名单里有非法 IP："+ip))
			return
		}
	}
	if req.Whitelist == nil {
		req.Whitelist = []string{}
	}
	s.cfg.IPWhitelist = req.Whitelist
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	s.audit(s.currentUser(r), "改 IP 白名单", "", "成功", "")
	writeJSON(w, 200, okResp(nil))
}

func (s *Server) handleHTTPS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enable bool   `json:"enable"`
		Domain string `json:"domain"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if req.Domain != "" && !validDomain(req.Domain) {
		writeJSON(w, 400, errResp("域名格式不对"))
		return
	}
	if req.Enable {
		if req.Domain != "" {
			// 有域名：尝试 ACME DNS-01；失败保留自签并返回错误说明
			if err := s.obtainACMECert(req.Domain); err != nil {
				_ = ensureSelfSigned(s.env.PanelHome, req.Domain)
				s.cfg.HTTPSEnabled = true
				s.cfg.HTTPSDomain = req.Domain
				_ = s.saveConfig()
				writeJSON(w, 200, map[string]any{"ok": true, "warning": "真证书申请失败（" + err.Error() + "），已先用自签证书开启 HTTPS，稍后可在设置里重试"})
				go func() { time.Sleep(300 * time.Millisecond); restartSelf(s.env) }()
				return
			}
		} else {
			if err := ensureSelfSigned(s.env.PanelHome, ""); err != nil {
				writeJSON(w, 500, errResp("自签证书生成失败："+err.Error()))
				return
			}
		}
		s.cfg.HTTPSEnabled = true
		s.cfg.HTTPSDomain = req.Domain
	} else {
		s.cfg.HTTPSEnabled = false
	}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	s.audit(s.currentUser(r), "HTTPS 开关", fmt.Sprintf("enable=%v domain=%s", req.Enable, req.Domain), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"msg": "面板正在重启以切换 HTTPS"}))
	go func() { time.Sleep(300 * time.Millisecond); restartSelf(s.env) }()
}

func (s *Server) handleTelegram(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled  bool   `json:"enabled"`
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	// 空 token 表示保持原值（前端只传 has_token 时不覆盖）
	if req.BotToken == "" {
		req.BotToken = s.cfg.Telegram.BotToken
	}
	s.cfg.Telegram = TelegramCfg{Enabled: req.Enabled, BotToken: req.BotToken, ChatID: req.ChatID}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	s.audit(s.currentUser(r), "设置 Telegram", "", "成功", "")
	writeJSON(w, 200, okResp(nil))
}

func (s *Server) handleTelegramTest(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Telegram.BotToken == "" || s.cfg.Telegram.ChatID == "" {
		writeJSON(w, 400, errResp("先填 Bot Token 和 Chat ID 再测试"))
		return
	}
	s.sendTelegram("relay-panel 测试消息：Telegram 提醒已接通。")
	writeJSON(w, 200, okResp(map[string]any{"msg": "测试消息已发送，请到 Telegram 查看"}))
}

// handleCloudflare 保存 Cloudflare API Token（绑域名开 HTTPS 签证书用）。
// 令牌只保存、不回显、不随导出走；新版令牌以 cfut_ 开头，旧版是 40 位十六进制，
// 两者都接受，格式不对只提醒、不拦（以免将来格式再变把有效令牌挡在外面）。
func (s *Server) handleCloudflare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeJSON(w, 400, errResp("令牌不能为空"))
		return
	}
	s.cfg.CloudflareToken = token
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	s.audit(s.currentUser(r), "设置 Cloudflare Token", "", "成功", "")
	if !strings.HasPrefix(token, "cfut_") && len(token) != 40 {
		writeJSON(w, 200, map[string]any{"ok": true, "warning": "新版令牌以 cfut_ 开头，旧版是 40 位；当前令牌是 " + strconv.Itoa(len(token)) + " 位，请核对是否复制完整"})
		return
	}
	writeJSON(w, 200, okResp(nil))
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	// 手动检查必须绕过缓存、强制重拉；拉取失败要明确报错，不能返回“已是最新”
	latest, update, err := s.latestVersionFresh()
	if err != nil {
		writeJSON(w, 502, errResp("检查更新失败："+err.Error()))
		return
	}
	writeJSON(w, 200, okResp(map[string]any{"version": Version, "latest_version": latest, "update_available": update}))
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	// 后台重跑 panel-install.sh（只重启面板，不动 realm）
	go func() {
		script := filepath.Join(s.env.KitDir, "panel-install.sh")
		if _, err := os.Stat(script); err != nil {
			// 远程拉取安装脚本跑一次
			_, _ = runCmdOut(nil, 300*time.Second, "bash", "-c", "curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/panel-install.sh | bash")
			return
		}
		_, _ = s.runScript("panel-install.sh", 300*time.Second, nil)
		// 更新完跑一遍 check 存下来
		if res, err := s.runScript("check.sh", 60*time.Second, nil, "--json"); err == nil {
			_ = os.WriteFile(filepath.Join(s.env.PanelHome, "last-check.json"), []byte(res.Stdout), 0o600)
		}
	}()
	s.audit(s.currentUser(r), "更新面板", "", "成功", "正在后台更新，面板稍后重启")
	writeJSON(w, 200, okResp(map[string]any{"msg": "正在更新，面板稍后重启"}))
}

// ---------- 端口管理（ufw） ----------

func (s *Server) handleFirewallGet(w http.ResponseWriter, r *http.Request) {
	if !commandExists("ufw") {
		writeJSON(w, 200, okResp(map[string]any{"available": false, "active": false, "rules": []any{}, "note": "本机没有 ufw，端口管理不可用；只管本机防火墙（ufw），云厂商安全组还得去控制台开"}))
		return
	}
	res, err := runCmdOut(nil, 10*time.Second, "ufw", "status")
	if err != nil {
		writeJSON(w, 200, okResp(map[string]any{"available": false, "active": false, "rules": []any{}, "note": "ufw 状态读不到：" + err.Error()}))
		return
	}
	active := strings.Contains(res.Stdout, "Status: active")
	var rules []map[string]any
	for _, line := range strings.Split(res.Stdout, "\n") {
		// "443/tcp  ALLOW  Anywhere" / "8000:8100/tcp ALLOW ..."
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		portPart := f[0]
		proto := "tcp"
		if strings.Contains(portPart, "/") {
			parts := strings.SplitN(portPart, "/", 2)
			portPart, proto = parts[0], parts[1]
		}
		if strings.Contains(portPart, ":") {
			continue // 范围规则在列表里只做展示，跳过单端口解析
		}
		p, err := strconv.Atoi(portPart)
		if err != nil {
			continue
		}
		action := "allow"
		if strings.EqualFold(f[1], "DENY") {
			action = "deny"
		}
		rules = append(rules, map[string]any{"port": p, "proto": proto, "action": action})
	}
	if rules == nil {
		rules = []map[string]any{}
	}
	writeJSON(w, 200, okResp(map[string]any{"available": true, "active": active, "rules": rules, "note": "只管本机防火墙（ufw），云厂商安全组还得去控制台开"}))
}

func (s *Server) handleFirewallPost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action  string `json:"action"`
		Port    int    `json:"port"`
		Proto   string `json:"proto"`
		From    int    `json:"from"`
		To      int    `json:"to"`
		Confirm bool   `json:"confirm"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if req.Proto == "" {
		req.Proto = "tcp"
	}
	if req.Proto != "tcp" && req.Proto != "udp" {
		writeJSON(w, 400, errResp("协议只能是 tcp 或 udp"))
		return
	}
	if !commandExists("ufw") {
		writeJSON(w, 200, errResp("本机没有 ufw，端口管理不可用"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	user := s.currentUser(r)
	runUfw := func(args ...string) error {
		_, err := runCmdOut(nil, 30*time.Second, "ufw", args...)
		return err
	}
	switch req.Action {
	case "allow":
		if !validPort(req.Port) {
			writeJSON(w, 400, errResp("端口不对"))
			return
		}
		if err := runUfw("allow", fmt.Sprintf("%d/%s", req.Port, req.Proto)); err != nil {
			writeJSON(w, 200, errResp("ufw 放行失败："+err.Error()))
			return
		}
	case "allow-range":
		if !validPort(req.From) || !validPort(req.To) || req.From > req.To {
			writeJSON(w, 400, errResp("端口范围不对"))
			return
		}
		if err := runUfw("allow", fmt.Sprintf("%d:%d/%s", req.From, req.To, req.Proto)); err != nil {
			writeJSON(w, 200, errResp("ufw 放行失败："+err.Error()))
			return
		}
	case "open-all":
		if !req.Confirm {
			writeJSON(w, 400, errResp("开放全部端口风险很大（所有端口都会暴露，包括 SSH），请二次确认后再提交"))
			return
		}
		if err := runUfw("allow", "1:65535/tcp"); err != nil {
			writeJSON(w, 200, errResp("ufw 放行失败："+err.Error()))
			return
		}
		if err := runUfw("allow", "1:65535/udp"); err != nil {
			writeJSON(w, 200, errResp("ufw 放行失败："+err.Error()))
			return
		}
	case "close":
		if !validPort(req.Port) {
			writeJSON(w, 400, errResp("端口不对"))
			return
		}
		if err := runUfw("delete", "allow", fmt.Sprintf("%d/%s", req.Port, req.Proto)); err != nil {
			writeJSON(w, 200, errResp("ufw 关闭失败："+err.Error()))
			return
		}
	case "restore-default":
		// 只开必要端口：面板端口 + 各落地监听端口 + 22
		_ = runUfw("delete", "allow", "1:65535/tcp")
		_ = runUfw("delete", "allow", "1:65535/udp")
		_ = runUfw("allow", "22/tcp")
		_ = runUfw("allow", fmt.Sprintf("%d/tcp", s.cfg.Port))
		if rc, _, err := s.readRelayConf(); err == nil {
			seen := map[int]bool{}
			for _, l := range rc.Landings {
				for _, p := range l.Ports {
					if !seen[p.Listen] {
						seen[p.Listen] = true
						_ = runUfw("allow", fmt.Sprintf("%d/%s", p.Listen, p.Proto))
					}
				}
			}
		}
	default:
		writeJSON(w, 400, errResp("未知操作："+req.Action))
		return
	}
	s.audit(user, "端口管理", req.Action, "成功", "")
	writeJSON(w, 200, okResp(nil))
}

// ---------- 流量 ----------

func (s *Server) handleTrafficGet(w http.ResponseWriter, r *http.Request) {
	be := detectTrafficBackend()
	available := be.Kind != ""
	idxStr := r.URL.Query().Get("landing_index")
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "day"
	}
	// 汇总全部落地
	rc, _, _ := s.readRelayConf()
	var landing string
	var lt *landingTraffic
	var ext *LandingExt
	if idxStr != "" {
		idx, _ := strconv.Atoi(idxStr)
		if l, _, err := s.landingByIndex(idx); err == nil {
			landing = l.Landing
			lt = s.traffic.get(landing)
			ext = s.state.ext(landing)
		}
	}
	if lt == nil {
		// 全部落地汇总
		lt = &landingTraffic{MonthKey: time.Now().Format("2006-01")}
		ext = &LandingExt{}
		if rc != nil {
			for _, l := range rc.Landings {
				one := s.traffic.get(l.Landing)
				lt.UpTotal += one.UpTotal
				lt.DownTotal += one.DownTotal
				lt.UpMonth += one.UpMonth
				lt.DownMonth += one.DownMonth
				e := s.state.ext(l.Landing)
				ext.TrafficLimitBytes += e.TrafficLimitBytes
			}
		}
	}
	var series []map[string]any
	switch rng {
	case "month":
		for _, d := range lt.Daily {
			series = append(series, map[string]any{"ts": d.Date, "up": d.Up, "down": d.Down})
		}
	case "total":
		series = []map[string]any{{"ts": "total", "up": lt.UpTotal, "down": lt.DownTotal}}
	default: // day：最近 24 小时按小时桶
		for _, h := range lt.Hourly {
			series = append(series, map[string]any{"ts": h.TS, "up": h.Up, "down": h.Down})
		}
	}
	if series == nil {
		series = []map[string]any{}
	}
	used := trafficUsed(lt, ext.TrafficLimitMode)
	pct := 0.0
	if ext.TrafficLimitBytes > 0 {
		pct = float64(used) / float64(ext.TrafficLimitBytes) * 100
	}
	note := ""
	if !available {
		note = "本机无 iptables/nftables，流量统计不可用"
	}
	writeJSON(w, 200, okResp(map[string]any{
		"available": available, "series": series, "note": note,
		"summary": map[string]any{
			"today_up": dayUp(lt), "today_down": dayDown(lt),
			"month_up": lt.UpMonth, "month_down": lt.DownMonth,
			"total_up": lt.UpTotal, "total_down": lt.DownTotal,
			"limit_bytes": ext.TrafficLimitBytes, "limit_mode": ext.TrafficLimitMode,
			"used_bytes": used, "pct": pct,
		},
	}))
}

func (s *Server) handleTrafficReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LandingIndex int    `json:"landing_index"`
		Scope        string `json:"scope"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if req.Scope != "month" && req.Scope != "total" {
		writeJSON(w, 400, errResp("scope 只能是 month 或 total"))
		return
	}
	l, _, err := s.landingByIndex(req.LandingIndex)
	if err != nil {
		writeJSON(w, 404, errResp(err.Error()))
		return
	}
	s.traffic.mu.Lock()
	lt := s.traffic.data[l.Landing]
	if lt != nil {
		if req.Scope == "month" {
			lt.UpMonth, lt.DownMonth = 0, 0
		} else {
			lt.UpTotal, lt.DownTotal = 0, 0
			lt.UpMonth, lt.DownMonth = 0, 0
		}
		s.traffic.saveLocked()
	}
	s.traffic.mu.Unlock()
	s.audit(s.currentUser(r), "重置流量", l.Landing+" / "+req.Scope, "成功", "")
	writeJSON(w, 200, okResp(nil))
}

// ---------- 备份 / 导出导入 / 审计 ----------

func (s *Server) handleBackupsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, okResp(map[string]any{"backups": s.listBackups()}))
}

func (s *Server) handleBackupsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label string `json:"label"`
	}
	_ = decodeJSON(r, &req)
	if req.Label == "" {
		req.Label = "手动备份"
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	id, err := s.backupNow(req.Label, "realm")
	if err != nil {
		writeJSON(w, 500, errResp("备份失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "备份", req.Label, "成功", id)
	writeJSON(w, 200, okResp(map[string]any{"id": id}))
}

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dir := filepath.Join(s.env.PanelHome, "backups", id)
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		writeJSON(w, 404, errResp("没有这份备份"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	// 先备份当前再恢复
	_, _ = s.backupNow("恢复备份前自动备份", "realm")
	if b, err := os.ReadFile(filepath.Join(dir, "config.toml")); err == nil {
		if err := s.writeConfigTomlAtomic(b); err != nil {
			writeJSON(w, 500, errResp("恢复 config.toml 失败："+err.Error()))
			return
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "relay.conf")); err == nil {
		if err := s.writeRelayConfAtomic(b); err != nil {
			writeJSON(w, 500, errResp("恢复 relay.conf 失败："+err.Error()))
			return
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		_ = writeFileAtomic(filepath.Join(s.env.PanelHome, "state.json"), b, 0o600)
		_ = s.loadState()
	}
	_ = s.regenProxies()
	if err := s.restartRealm(); err != nil {
		writeJSON(w, 200, errResp("配置已恢复，但重启 realm 失败："+err.Error()))
		return
	}
	if rc, _, err := s.readRelayConf(); err == nil {
		var ports []PortSpec
		for _, l := range rc.Landings {
			ports = append(ports, l.Ports...)
		}
		if err := verifyPorts(ports); err != nil {
			writeJSON(w, 200, errResp("恢复后端口校验失败："+err.Error()))
			return
		}
	}
	s.audit(s.currentUser(r), "恢复备份", id, "成功", "")
	writeJSON(w, 200, okResp(nil))
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	// 数据口径见 exportData（与定时备份共用；不含密码哈希和 Telegram Token）。
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="relay-panel-export.json"`)
	_ = json.NewEncoder(w).Encode(s.exportData())
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		writeJSON(w, 400, errResp("读取导入文件失败"))
		return
	}
	var imp importPayload
	if err := json.Unmarshal(body, &imp); err != nil {
		writeJSON(w, 400, errResp("导入文件格式不对"))
		return
	}
	if imp.RelayConf == "" || imp.ConfigToml == "" {
		writeJSON(w, 400, errResp("导入文件缺 relay_conf 或 config_toml"))
		return
	}
	if ferr := s.validateImportPayload(imp); ferr != nil {
		writeJSON(w, ferr.status, errResp(ferr.msg))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	// 落地逻辑见 applyImportPayload（与定时备份恢复共用；不做端口校验，
	// 与旧版 handleImport 一致）。
	warn, ferr := s.applyImportPayload(imp, importApplyOpts{
		preBackupLabel: "导入前自动备份", auditAction: "导入配置",
		auditDetail:       "密码哈希与 Telegram token 保持本机原值",
		restartWarnPrefix: "导入已写入，但重启 realm 失败：",
		verifyWarnPrefix:  "", user: s.currentUser(r),
	})
	if ferr != nil {
		writeJSON(w, ferr.status, errResp(ferr.msg))
		return
	}
	if warn != "" {
		writeJSON(w, 200, errResp(warn))
		return
	}
	writeJSON(w, 200, okResp(nil))
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			page = n
		}
	}
	pageSize := 20
	if v := r.URL.Query().Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			pageSize = n
		}
	}
	if pageSize < 1 {
		pageSize = 1
	}
	if pageSize > 100 {
		pageSize = 100
	}
	entries, total := s.readAuditPage(page, pageSize)
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	writeJSON(w, 200, okResp(map[string]any{
		"entries": entries, "total": total, "page": page,
		"page_size": pageSize, "total_pages": totalPages,
	}))
}

var _ = sort.Strings
var _ = filepath.Base
