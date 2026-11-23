package main

import (
	"context"
	"sync"
	"time"
)

// ---------- 后台任务 ----------

func (s *Server) startBackground() {
	go s.trafficLoop()
	go s.expiryLoop()
	go s.healthLoop()
	go s.certLoop()
	go s.backupLoop()
	go s.recycleCleanupLoop()
	go s.monitorLoop()
	go s.latencyLoop()
	go s.centralLoop()
	go s.centralUserTrafficLoop() // 按订阅限流聚合（v1.3.25，每 60 秒）
}

// 流量采样每 60 秒；先采样再执法：enforceTrafficLimits 末尾调
// ensureTrafficChains 会把链 -F 清掉重建（计数器归零），按人采样
// 若跑在清链之后，读到的永远是刚重建的 ≈0（v1.3.25 的采样 bug：
// 用户用量一直是 0 / 1.69 KB）。管理面板模式不跑：本地流量、限量
// 停用都依赖本地中转，管理面板不管本地。
func (s *Server) trafficLoop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for range t.C {
		// v1.4.4：后端节点限流执法（直连）——管理面板模式也要跑（它
		// 不依赖本地 relay），所以放在 managesLocalRelay 门前面；跑在
		// 已有的 60 秒循环里，不新开后台任务与轮询。
		s.enforceBackendNodeTrafficLimits()
		if !s.managesLocalRelay() {
			continue
		}
		// 顺序是关键：先把落地与按人两路采样都读完，再执法（清链重建）。
		// ensureTrafficChains 重建后会把两路采样基线清零（resetBaselines），
		// 下一分钟从 0 起算，不丢数。
		s.sampleTraffic()
		s.sampleUserTraffic()
		s.enforceTrafficLimits()
		s.enforceUserTrafficLimits()
	}
}

func (s *Server) enforceTrafficLimits() {
	if !s.managesLocalRelay() {
		return
	}
	rc, _, err := s.readRelayConf()
	if err != nil {
		return
	}
	changed := false
	for _, l := range rc.Landings {
		ext := s.state.ext(l.Landing)
		if ext.TrafficLimitBytes <= 0 || ext.Disabled {
			continue
		}
		lt := s.traffic.get(l.Landing)
		used := trafficUsed(lt, ext.TrafficLimitMode)
		pct := float64(used) / float64(ext.TrafficLimitBytes) * 100
		today := time.Now().Format("2006-01-02")
		if pct >= 80 && !ext.TrafficWarned80 {
			s.sendTelegram("流量提醒：落地 " + l.Landing + " 流量已用 " + formatPct(pct) + "%")
			ext.TrafficWarned80 = true
			changed = true
			_ = today
		}
		if overLimit(lt, ext.TrafficLimitBytes, ext.TrafficLimitMode) {
			s.disableLanding(l, "traffic")
			s.sendTelegram("流量用完：落地 " + l.Landing + " 已自动停用（流量上限），续量后恢复")
			if !ext.TrafficWarned100 {
				ext.TrafficWarned100 = true
				changed = true
			}
		}
	}
	if changed {
		_ = s.saveState()
	}
	// quota 重建：每次采样后按剩余量重建（ensureTrafficChains 是幂等的）；
	// 重建会把链清掉（计数器归零），函数末尾把两路采样基线清零，下分钟从 0 起算。
	_ = s.ensureTrafficChains(rc.Landings)
}

// disableLanding 权威停用：把 endpoints 从 config.toml 撤掉（不断其他落地）。
func (s *Server) disableLanding(l Landing, reason string) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	ext := s.state.ext(l.Landing)
	if ext.Disabled {
		return
	}
	// reason 合并
	if ext.DisabledReason == "expired" && reason == "traffic" || ext.DisabledReason == "traffic" && reason == "expired" {
		reason = "both"
	}
	tomlData, err := s.readConfigToml()
	if err != nil {
		return
	}
	newToml := removeEndpointsFromToml(tomlData, l.Landing)
	if string(newToml) == string(tomlData) {
		return
	}
	if _, err := s.backupNow("停用落地前自动备份", "realm"); err == nil {
		// 备份成功才继续
	}
	if err := s.writeConfigTomlAtomic(newToml); err != nil {
		return
	}
	if err := s.restartRealm(); err != nil {
		return
	}
	ext.Disabled = true
	ext.DisabledReason = reason
	_ = s.saveState()
	s.audit("system", "自动停用落地", l.Landing, "成功", "原因："+reason)
}

// 到期检查每小时。
func (s *Server) expiryLoop() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	s.checkExpiry()
	for range t.C {
		s.checkExpiry()
	}
}

func (s *Server) checkExpiry() {
	if !s.managesLocalRelay() {
		return
	}
	rc, _, err := s.readRelayConf()
	if err != nil {
		return
	}
	now := time.Now()
	for _, l := range rc.Landings {
		ext := s.state.ext(l.Landing)
		if ext.Expiry == "" {
			continue
		}
		exp, err := time.Parse(time.RFC3339, ext.Expiry)
		if err != nil {
			continue
		}
		days := int(time.Until(exp).Hours() / 24)
		if days == 7 && !ext.RenewReminded7d {
			s.sendTelegram("到期提醒：落地 " + l.Landing + " 还有 7 天到期")
			ext.RenewReminded7d = true
			_ = s.saveState()
		}
		if days == 3 && !ext.RenewReminded3d {
			s.sendTelegram("到期提醒：落地 " + l.Landing + " 还有 3 天到期")
			ext.RenewReminded3d = true
			_ = s.saveState()
		}
		if days == 0 {
			s.sendTelegram("到期提醒：落地 " + l.Landing + " 今天到期")
		}
		if now.After(exp) && !ext.Disabled {
			s.disableLanding(l, "expired")
			s.sendTelegram("节点到期：落地 " + l.Landing + " 已到期自动停用，续期后恢复")
		}
	}
}

// managesLocalRelay 本面板是否在管本地中转/本地落地：管理面板模式只管
// 远程面板，依赖本地中转/本地落地的后台检查一律跳过（健康、证书、到期、
// 延迟、流量）；机器级监控、面板自身备份、集中管理轮询不受影响。
func (s *Server) managesLocalRelay() bool {
	return !s.cfg.ManagerOnly
}

// watchRealmDecision 纯决策：只在完整面板且本机装了 realm 时监控 realm 健康。
func watchRealmDecision(managerOnly, installed bool) bool {
	return !managerOnly && installed
}

// shouldWatchRealm 只在完整面板且本机装了 realm 时才监控 realm 健康：
// 管理面板本地没有中转、面板只装没装中转时，都不该发“realm 挂了”提醒。
func (s *Server) shouldWatchRealm() bool {
	return watchRealmDecision(s.cfg.ManagerOnly, realmInstalledFunc())
}

// refreshHealthBaseline 模式切换后立即刷新健康基线：不发提醒，只把
// 当前 realm 状态记下来，切回完整面板时不拿旧基线误报。
// （healthTick 每 5 分钟也会刷新基线，这里是让切换立即生效，不用等下一个 tick。）
func (s *Server) refreshHealthBaseline() {
	ok := realmRunning()
	s.healthMu.Lock()
	s.lastHealthOK = ok
	s.lastHealthAt = time.Now()
	s.healthMu.Unlock()
}

// 健康自检每 5 分钟：realm 挂了发 Telegram（只在状态变化时发，避免刷屏）。
// 只在监控中转时才看运行→停止的变化；不监控时也刷新基线，
// 这样切回完整面板时不会拿旧基线误报。
func (s *Server) healthTick() {
	ok := realmRunning()
	s.healthMu.Lock()
	alert := s.shouldWatchRealm() && !ok && s.lastHealthOK
	s.lastHealthOK = ok
	s.lastHealthAt = time.Now()
	s.healthMu.Unlock()
	if alert {
		s.sendTelegram("故障提醒：realm 挂了（中转已停），请到面板服务页重启")
	}
}

func (s *Server) healthLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.healthTick()
	}
}

// 证书检查每天一次。提醒策略（v1.3.6）：
//   - 检查用 probeCertForLanding，按落地实际的 TCP 监听端口逐个握手
//     （以前只认 443，自定义端口 TLS 落地根本不检查）；
//   - 剩余 ≤30 天发一次预警、≤7 天发一次紧急提醒，同一阈值只提醒一次
//     （标志存 state.json，重启不丢；续签后天数回升越过阈值时自动清掉，
//     下次再越过重新提醒）；已过期按紧急提醒发，文案写“已过期 N 天”；
//   - 文案里写明续签方式（renewModeFor：自动还是需手动去落地机操作）；
//   - 没配 Telegram 时 sendTelegram 直接空操作，不报错。
func (s *Server) certLoop() {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	s.checkCerts()
	s.maybeRenewPanelCert()
	for range t.C {
		s.checkCerts()
		s.maybeRenewPanelCert()
	}
}

// maybeRenewPanelCert 面板自身真证书自动续签（v1.3.14）：每天检查一次，
// 真证书剩余 ≤30 天时用已存凭证走 DNS-01 续签，续好重启面板生效。
// 手动模式不自动续签（要人加 TXT，自动续不了；到期前用户手动重签）。
// 完整面板与管理面板都跑（不经过 managesLocalRelay 判断，面板自身证书与
// 本地中转无关）。续失败发 Telegram 提醒（同一轮只提醒一次，成功后清掉），
// 失败原因写进 config 常驻显示在 HTTPS 卡片上。
func (s *Server) maybeRenewPanelCert() {
	if s.dnsProviderID() == "manual" {
		return
	}
	configured := s.dnsProviderConfigured(s.dnsProviderID())
	if !shouldRenewPanelCert(s.cfg.HTTPSEnabled, s.cfg.HTTPSDomain, configured, s.panelCertInfo()) {
		return
	}
	s.certMu.Lock()
	defer s.certMu.Unlock()
	// 锁内再核对一次（手动“重试签发”可能刚续好）。
	if !shouldRenewPanelCert(s.cfg.HTTPSEnabled, s.cfg.HTTPSDomain, configured, s.panelCertInfo()) {
		return
	}
	domain := s.cfg.HTTPSDomain
	if err := s.obtainACMECert(domain); err != nil {
		s.cfg.PanelCertLastErr = err.Error()
		if !s.cfg.PanelCertRenewWarned {
			s.sendTelegram("面板证书续签失败：域名 " + domain + " 的真证书剩余不足 30 天，自动续签失败（" + err.Error() + "），请到设置页 HTTPS 卡片点“重试签发”，或检查 " + dnsProviderName(s.dnsProviderID()) + " 凭证。")
			s.cfg.PanelCertRenewWarned = true
		}
		_ = s.saveConfig()
		return
	}
	s.onPanelCertRenewed(domain)
}

// onPanelCertRenewed 面板自身真证书续签成功后的收尾：清失败记录与去重标志，
// 发 Telegram 告知，重启面板让新证书生效（复用 restartSelf 路子）。
func (s *Server) onPanelCertRenewed(domain string) {
	s.cfg.PanelCertLastErr = ""
	s.cfg.PanelCertRenewWarned = false
	_ = s.saveConfig()
	s.sendTelegram("面板真证书已自动续签：域名 " + domain + " 的证书已换成新的，面板正在重启生效。")
	go func() {
		time.Sleep(300 * time.Millisecond)
		restartPanelFunc(s.env)
	}()
}

func (s *Server) checkCerts() {
	if !s.managesLocalRelay() {
		return
	}
	rc, _, err := s.readRelayConf()
	if err != nil {
		return
	}
	changed := false
	for _, l := range rc.Landings {
		ci, ok := probeCertForLanding(l)
		if !ok || ci.DaysLeft == nil {
			continue
		}
		days := *ci.DaysLeft
		ext := s.state.ext(l.Landing)
		// 续签后天数回升：清掉已发的标志，下次再越过阈值重新提醒。
		if days > 30 && (ext.CertWarned30 || ext.CertWarned7) {
			ext.CertWarned30, ext.CertWarned7 = false, false
			changed = true
		} else if days > 7 && days <= 30 && ext.CertWarned7 {
			ext.CertWarned7 = false
			changed = true
		}
		msg, set30, set7 := certAlertFor(l, days, ci.Expiry, ext, rc.Share443)
		if msg == "" {
			continue
		}
		s.sendTelegram(msg)
		if set30 {
			ext.CertWarned30 = true
			changed = true
		}
		if set7 {
			ext.CertWarned7 = true
			changed = true
		}
	}
	if changed {
		_ = s.saveState()
	}
}

// certAlertFor 证书提醒的纯决策：该不该发、发什么文案、置哪个去重标志。
// 返回文案为空 = 不发。days<=0 是已过期（按紧急提醒走 7 天档）。
func certAlertFor(l Landing, days int, expiry string, ext *LandingExt, share443 bool) (msg string, set30, set7 bool) {
	mode, _ := renewModeFor(l, share443, true)
	name := l.Landing
	if days <= 0 {
		if ext.CertWarned7 {
			return "", false, false
		}
		if days == 0 {
			return "证书提醒：落地 " + name + " 证书今天到期（到期 " + expiry + "），续签方式：" + mode + "，请立即续签", false, true
		}
		return "证书过期：落地 " + name + " 证书已过期 " + itoa(-days) + " 天（到期 " + expiry + "），续签方式：" + mode + "，请立即处理", false, true
	}
	if days <= 7 {
		if ext.CertWarned7 {
			return "", false, false
		}
		return "证书提醒：落地 " + name + " 证书只剩 " + itoa(days) + " 天（到期 " + expiry + "），续签方式：" + mode + "，请及时续签", false, true
	}
	if days <= 30 {
		if ext.CertWarned30 {
			return "", false, false
		}
		return "证书预警：落地 " + name + " 证书剩余 " + itoa(days) + " 天（到期 " + expiry + "），续签方式：" + mode, true, false
	}
	return "", false, false
}

// 自动定时备份（v1.3.6）：每分钟看一次，到了设定时间且今天还没做过就备份
// 一次。面板在设定时间没在跑时，启动后补跑一次（当天还没备份过的话）；
// 当天已有备份就不重复。备份本身是只读快照，不拿写锁。
func (s *Server) backupLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	s.maybeScheduledBackup()
	for range t.C {
		s.maybeScheduledBackup()
	}
}

func (s *Server) maybeScheduledBackup() {
	if !s.cfg.Backup.Enabled {
		return
	}
	hh, mm, err := parseBackupTime(s.cfg.Backup.Time)
	if err != nil {
		return
	}
	now := time.Now()
	if s.scheduledBackupDoneToday(now.Format("20060102")) {
		return
	}
	due := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if now.Before(due) {
		return
	}
	id, err := s.runScheduledBackup("自动定时备份")
	if err != nil {
		// v1.3.29：定时备份失败不再静默——记审计 + 发 Telegram，与异地
		// 上传失败同等待遇；否则磁盘满/权限错会无声无息丢掉当天备份。
		// 开关关闭仍静默返回（预期行为，与报错区分开，见函数开头）。
		s.audit("system", "自动定时备份", "", "失败", err.Error())
		s.sendTelegram("自动定时备份失败：" + err.Error())
		return
	}
	s.audit("system", "自动定时备份", "", "成功", "")
	// 定时备份写完就异步传异地（goroutine，不阻塞备份循环）。
	s.uploadBackupToRemoteAsync(id)
}

// 回收站清理：7 天后真正清除面板记录（realm 配置删除时已生效）。
func (s *Server) recycleCleanupLoop() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		s.stateMu.Lock()
		now := time.Now()
		var kept []RecycleItem
		for _, it := range s.state.Recycle {
			if exp, err := time.Parse(time.RFC3339, it.ExpiresAt); err == nil && now.After(exp) {
				continue
			}
			kept = append(kept, it)
		}
		if len(kept) != len(s.state.Recycle) {
			s.state.Recycle = kept
			_ = s.saveStateLocked()
		}
		s.stateMu.Unlock()
	}
}

func formatPct(p float64) string {
	return itoa(int(p)) + "%"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ---------- 小缓存 ----------

type versionCache struct {
	mu sync.Mutex
	v  string
	at time.Time
}

var latestVersionCache = &versionCache{}

func (c *versionCache) get() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) < time.Hour && c.v != "" {
		return c.v, true
	}
	return "", false
}

func (c *versionCache) set(v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.v, c.at = v, time.Now()
}

func ctxTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
