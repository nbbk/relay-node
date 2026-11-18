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
	go s.recycleCleanupLoop()
	go s.monitorLoop()
}

// 流量采样每 60 秒；采样后检查限量（剩余变化大时重建 quota）。
func (s *Server) trafficLoop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for range t.C {
		s.sampleTraffic()
		s.enforceTrafficLimits()
	}
}

func (s *Server) enforceTrafficLimits() {
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
	// quota 重建：每次采样后按剩余量重建（ensureTrafficChains 是幂等的）
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

// 健康自检每 5 分钟：realm 挂了发 Telegram（只在状态变化时发，避免刷屏）。
func (s *Server) healthLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		ok := realmRunning()
		if !ok && s.lastHealthOK {
			s.sendTelegram("故障提醒：realm 挂了（中转已停），请到面板服务页重启")
		}
		s.lastHealthOK = ok
		s.lastHealthAt = time.Now()
	}
}

// 证书检查每天：<30 天 / <7 天各每天最多 1 条。
func (s *Server) certLoop() {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	s.checkCerts()
	for range t.C {
		s.checkCerts()
	}
}

func (s *Server) checkCerts() {
	rc, _, err := s.readRelayConf()
	if err != nil {
		return
	}
	today := time.Now().Format("2006-01-02")
	for _, l := range rc.Landings {
		tlsPort := 0
		for _, p := range l.Ports {
			if p.Proto == "tcp" && p.Listen == 443 {
				tlsPort = p.Listen
			}
		}
		if tlsPort == 0 {
			continue
		}
		ci := fetchCert(tlsPort, l.CertDomain)
		if ci.DaysLeft == nil {
			continue
		}
		if *ci.DaysLeft < 30 && s.certAlertDay[l.Landing] != today {
			if *ci.DaysLeft < 7 {
				s.sendTelegram("证书提醒：落地 " + l.Landing + " 证书只剩 " + itoa(*ci.DaysLeft) + " 天")
			} else {
				s.sendTelegram("证书提醒：落地 " + l.Landing + " 证书剩余 " + itoa(*ci.DaysLeft) + " 天")
			}
			s.certAlertDay[l.Landing] = today
		}
	}
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
