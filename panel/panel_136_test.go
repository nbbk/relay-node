package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------- v1.3.6 ----------

// TestCertAlertThresholds136 证书提醒的阈值与去重（纯决策 certAlertFor）：
// 30 天档发一次预警、7 天档发一次紧急提醒、已过期按紧急提醒、同一阈值
// 不重复发、文案里写明续签方式（自动还是手动）。
func TestCertAlertThresholds136(t *testing.T) {
	// 手动续签的落地：只有 24443 端口，没 80、没 DNS-01、没分流
	manual := Landing{Landing: "203.0.113.1", CertDomain: "example.com",
		Ports: []PortSpec{{Proto: "tcp", Listen: 24443, Remote: 24443}}}
	// 自动续签的落地：DNS-01
	auto := Landing{Landing: "203.0.113.2", CertDomain: "example.com", CertMode: "dns01",
		Ports: []PortSpec{{Proto: "tcp", Listen: 443, Remote: 443}}}

	// 25 天：预警一次，文案带续签方式
	ext := &LandingExt{}
	msg, set30, set7 := certAlertFor(manual, 25, "2026-12-14", ext, false)
	if !strings.Contains(msg, "证书预警") || !strings.Contains(msg, "25 天") || !strings.Contains(msg, "手动") {
		t.Fatalf("25 天应发预警并写明手动续签，实际 msg=%q", msg)
	}
	if !set30 || set7 {
		t.Fatalf("25 天应只置 30 天标志，实际 set30=%v set7=%v", set30, set7)
	}
	// 同一阈值不重复发
	ext.CertWarned30 = true
	if msg, _, _ := certAlertFor(manual, 25, "2026-12-14", ext, false); msg != "" {
		t.Fatalf("30 天档已提醒过不应再发，实际 msg=%q", msg)
	}
	// 5 天：紧急提醒一次（30 天档发过也不影响，7 天档是另一阈值）
	msg, set30, set7 = certAlertFor(manual, 5, "2026-11-24", ext, false)
	if !strings.Contains(msg, "证书提醒") || !strings.Contains(msg, "只剩 5 天") || !strings.Contains(msg, "手动") {
		t.Fatalf("5 天应发紧急提醒并写明手动续签，实际 msg=%q", msg)
	}
	if set30 || !set7 {
		t.Fatalf("5 天应只置 7 天标志，实际 set30=%v set7=%v", set30, set7)
	}
	ext.CertWarned7 = true
	if msg, _, _ := certAlertFor(manual, 5, "2026-11-24", ext, false); msg != "" {
		t.Fatalf("7 天档已提醒过不应再发，实际 msg=%q", msg)
	}
	// 已过期 3 天：按紧急提醒，文案写“已过期 3 天”
	ext2 := &LandingExt{}
	msg, _, set7 = certAlertFor(manual, -3, "2026-11-16", ext2, false)
	if !strings.Contains(msg, "已过期 3 天") || !set7 {
		t.Fatalf("过期应发紧急提醒并写已过期天数，实际 msg=%q set7=%v", msg, set7)
	}
	// 今天到期
	msg, _, _ = certAlertFor(manual, 0, "2026-11-19", &LandingExt{}, false)
	if !strings.Contains(msg, "今天到期") {
		t.Fatalf("今天到期文案不对，实际 msg=%q", msg)
	}
	// 89 天：不发
	if msg, _, _ := certAlertFor(manual, 89, "2027-02-16", &LandingExt{}, false); msg != "" {
		t.Fatalf("89 天不应发提醒，实际 msg=%q", msg)
	}
	// 自动续签的落地：文案写自动（DNS 验证）
	msg, _, _ = certAlertFor(auto, 25, "2026-12-14", &LandingExt{}, false)
	if !strings.Contains(msg, "自动（DNS 验证）") {
		t.Fatalf("DNS-01 落地文案应写自动（DNS 验证），实际 msg=%q", msg)
	}
}

// startFixedPortTLS136 在指定端口起一个自签 TLS 服务（证书 days 天后到期）。
func startFixedPortTLS136(t *testing.T, days int, port int) func() {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		DNSNames:     []string{"example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Duration(days) * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port), &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
				time.Sleep(200 * time.Millisecond)
			}(c)
		}
	}()
	return func() { _ = ln.Close() }
}

func freePort136(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// testCertServer136 起一个只有单个自定义端口 TLS 落地的面板（证书探测用）。
func testCertServer136(t *testing.T, port int) *Server {
	t.Helper()
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	relayConf := "LANDING_COUNT=1\nLANDING=\"203.0.113.1\"\nDOMAIN=\"example.com\"\nCERT_DOMAIN=\"example.com\"\nPRESET=1\nMODE_NOTE=\"test\"\nPORTS=\"tcp:" + strconv.Itoa(port) + ":" + strconv.Itoa(port) + "\"\n"
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(relayConf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	return newTestAddLandingServer(t, kitDir, realmDir)
}

// TestCheckCertsPersistentFlags136 checkCerts 端到端（真实 TLS 握手）：
// 自定义端口证书能探到；5 天证书置 7 天档标志、第二次不重复；续签成 89 天
// 后标志自动清掉；标志落盘到 state.json（重启不丢）。
func TestCheckCertsPersistentFlags136(t *testing.T) {
	port := freePort136(t)
	stop := startFixedPortTLS136(t, 5, port)
	defer stop()
	s := testCertServer136(t, port)

	s.checkCerts()
	ext := s.state.ext("203.0.113.1")
	if !ext.CertWarned7 || ext.CertWarned30 {
		t.Fatalf("5 天证书应只置 7 天档标志，实际 warned30=%v warned7=%v", ext.CertWarned30, ext.CertWarned7)
	}
	// 标志落盘
	raw, err := os.ReadFile(filepath.Join(s.env.PanelHome, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"cert_warned_7": true`) {
		t.Fatalf("state.json 里应有 cert_warned_7=true，实际：%s", raw)
	}
	// 第二次检查：不重复（标志还在，不会再发）
	s.checkCerts()
	if !s.state.ext("203.0.113.1").CertWarned7 {
		t.Fatalf("第二次检查后 7 天档标志不应丢")
	}
	// 续签成 89 天：标志自动清掉
	stop()
	stop2 := startFixedPortTLS136(t, 89, port)
	defer stop2()
	s.checkCerts()
	ext = s.state.ext("203.0.113.1")
	if ext.CertWarned7 || ext.CertWarned30 {
		t.Fatalf("续签到 89 天后标志应清掉，实际 warned30=%v warned7=%v", ext.CertWarned30, ext.CertWarned7)
	}
}

// TestCheckCertsWarn30Flags136 25 天证书只置 30 天档标志。
func TestCheckCertsWarn30Flags136(t *testing.T) {
	port := freePort136(t)
	stop := startFixedPortTLS136(t, 25, port)
	defer stop()
	s := testCertServer136(t, port)
	s.checkCerts()
	ext := s.state.ext("203.0.113.1")
	if !ext.CertWarned30 || ext.CertWarned7 {
		t.Fatalf("25 天证书应只置 30 天档标志，实际 warned30=%v warned7=%v", ext.CertWarned30, ext.CertWarned7)
	}
}

// TestCertsTelegramConfigured136 证书接口下发 telegram_configured：
// 没配时 false（前端提示去设置里配），配好后 true。
func TestCertsTelegramConfigured136(t *testing.T) {
	s := newTestSubServer(t)
	call := func() map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/certs", nil)
		rec := httptest.NewRecorder()
		s.handleCerts(rec, req)
		if rec.Code != 200 {
			t.Fatalf("证书接口应 200，实际 %d", rec.Code)
		}
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	if got, _ := call()["telegram_configured"].(bool); got {
		t.Fatalf("没配 Telegram 时 telegram_configured 应为 false")
	}
	s.cfg.Telegram.Enabled = true
	if got, _ := call()["telegram_configured"].(bool); got {
		t.Fatalf("只开了开关、没填 Token/ChatID 时应为 false")
	}
	s.cfg.Telegram.BotToken = "token-xxx"
	s.cfg.Telegram.ChatID = "12345"
	if got, _ := call()["telegram_configured"].(bool); !got {
		t.Fatalf("配好 Telegram 后 telegram_configured 应为 true")
	}
}

// TestBackupCfg136 备份排程配置：默认值、旧配置补默认、校验与钳制。
func TestBackupCfg136(t *testing.T) {
	d := defaultBackupCfg()
	if !d.Enabled || d.Time != "03:00" || d.Keep != 7 {
		t.Fatalf("默认备份排程应为开启/03:00/7 份，实际 %+v", d)
	}
	// 旧 config（整节没存过）：按默认补，默认开启
	var c BackupCfg
	c.applyDefaults()
	if c != defaultBackupCfg() {
		t.Fatalf("全空应补成默认，实际 %+v", c)
	}
	// 用户明确关掉且存过 time/keep：不动
	c = BackupCfg{Enabled: false, Time: "02:30", Keep: 3}
	c.applyDefaults()
	if c.Enabled || c.Time != "02:30" || c.Keep != 3 {
		t.Fatalf("存过的值不应被默认值覆盖，实际 %+v", c)
	}
	// 校验：时间格式
	if err := (&BackupCfg{Enabled: true, Time: "25:00", Keep: 7}).validate(); err == nil {
		t.Fatalf("25:00 应校验失败")
	}
	// "3:00" 没补零也能解析（Go 的 time.Parse 接受一位小时），不拦
	// 保留份数钳制到 1–30
	c = BackupCfg{Enabled: true, Time: "03:00", Keep: 0}
	if err := c.validate(); err != nil || c.Keep != 1 {
		t.Fatalf("Keep=0 应钳制到 1，实际 %+v err=%v", c, err)
	}
	c = BackupCfg{Enabled: true, Time: "03:00", Keep: 99}
	if err := c.validate(); err != nil || c.Keep != 30 {
		t.Fatalf("Keep=99 应钳制到 30，实际 %+v err=%v", c, err)
	}
}

// TestScheduledBackupWriteRotate136 定时备份写入与轮转：保留份数到了就删
// 最旧的；文件里不含密码哈希与令牌哈希；订阅用户只留脱敏字段。
func TestScheduledBackupWriteRotate136(t *testing.T) {
	s := newTestSubServer(t)
	s.cfg.Backup = BackupCfg{Enabled: true, Time: "03:00", Keep: 2}
	s.cfg.SubUsers = []SubUser{{ID: "u1", Name: "张三", Note: "测试", TokenHash: "hash-should-not-appear-aaa", CreatedAt: "2026-01-01T00:00:00Z"}}
	s.cfg.MergedSubs = []MergedSub{{ID: "m1", Name: "合并", TokenHash: "hash-should-not-appear-bbb", Selection: []SubSelection{{Landing: 1}}}}
	if _, err := s.runScheduledBackup("测试1"); err != nil {
		t.Fatalf("第一次备份失败：%v", err)
	}
	id2, err := s.runScheduledBackup("测试2")
	if err != nil {
		t.Fatalf("第二次备份失败：%v", err)
	}
	list := s.listScheduledBackups()
	if len(list) != 2 {
		t.Fatalf("应有 2 份备份，实际 %d", len(list))
	}
	// 第三份触发轮转：只留 2 份，最旧的被删
	if _, err := s.runScheduledBackup("测试3"); err != nil {
		t.Fatalf("第三次备份失败：%v", err)
	}
	list = s.listScheduledBackups()
	if len(list) != 2 {
		t.Fatalf("轮转后应只剩 2 份，实际 %d：%v", len(list), list)
	}
	// 文件内容：导出同款 + 流量/订阅用户；不含任何哈希与令牌
	raw, err := s.readScheduledBackup(id2)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"password_hash", "token_hash", "hash-should-not-appear-aaa", "hash-should-not-appear-bbb", s.cfg.PasswordHash} {
		if bad != "" && strings.Contains(string(raw), bad) {
			t.Fatalf("备份文件里不应出现 %q", bad)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("备份文件应是合法 JSON：%v", err)
	}
	for _, k := range []string{"relay_conf", "config_toml", "state", "panel_config", "kind", "label", "sub_users", "merged_subs"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("备份文件缺字段 %s", k)
		}
	}
	if m["kind"] != "scheduled" {
		t.Fatalf("kind 应为 scheduled，实际 %v", m["kind"])
	}
	users, _ := m["sub_users"].([]any)
	if len(users) != 1 {
		t.Fatalf("sub_users 应有 1 条，实际 %v", m["sub_users"])
	}
	if u, _ := users[0].(map[string]any); u["name"] != "张三" || u["id"] != "u1" {
		t.Fatalf("sub_users 脱敏条目不对：%v", u)
	}
}

// TestScheduledBackupRestore136 定时备份恢复：流量库回来了、订阅用户按 ID
// 保留现有令牌哈希、备份后新增的用户被移除（快照语义）。
func TestScheduledBackupRestore136(t *testing.T) {
	s := newTestSubServer(t)
	// 夹具的 config.toml 是空的（真实面板有 [[endpoints]] 段）：先写一份
	// 合法的，备份才 captur 到能通过校验的 toml（导入一直要求有该段）。
	validToml := "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte(validToml), 0o644); err != nil {
		t.Fatal(err)
	}
	s.cfg.SubUsers = []SubUser{
		{ID: "u1", Name: "张三", TokenHash: "hash-aaa", CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "u3", Name: "李四", TokenHash: "hash-ddd", CreatedAt: "2026-01-01T00:00:00Z"},
	}
	s.cfg.MergedSubs = []MergedSub{{ID: "m1", Name: "合并", TokenHash: "hash-bbb", CreatedAt: "2026-01-01T00:00:00Z", Selection: []SubSelection{{Landing: 1}}}}
	// 流量库先写一份已知数据
	trafficJSON := `{"203.0.113.1": {"up_total": 1000, "down_total": 2000, "up_month": 100, "down_month": 200, "month_key": "2026-11", "hourly": [], "daily": [], "last_counters": {"tcp:24443": [10, 20]}}}`
	if err := os.WriteFile(filepath.Join(s.env.PanelHome, "traffic.json"), []byte(trafficJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	s.traffic.load()
	id, err := s.runScheduledBackup("恢复测试")
	if err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	// 改掉现状：流量变了；u1 删了（恢复后应回来，但令牌哈希找不回来——
	// 备份里本来就不含令牌，须重新生成）；u3 留着；备份后新增 u2、m2。
	if err := os.WriteFile(filepath.Join(s.env.PanelHome, "traffic.json"), []byte(`{"203.0.113.1": {"up_total": 99999, "down_total": 99999, "up_month": 0, "down_month": 0, "month_key": "2026-11", "hourly": [], "daily": []}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.traffic.load()
	s.cfg.SubUsers = []SubUser{
		{ID: "u3", Name: "李四", TokenHash: "hash-ddd", CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "u2", Name: "备份后新增", TokenHash: "hash-ccc"},
	}
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: "m2", Name: "备份后新增", TokenHash: "hash-eee"})
	// 恢复（测试环境 realm 起不来，走“配置已恢复，但重启失败”分支，仍是 200）
	req := httptest.NewRequest("POST", "/api/v1/scheduled-backups/"+id+"/restore", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	s.handleScheduledBackupRestore(rec, req)
	if rec.Code != 200 {
		t.Fatalf("恢复应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 流量库回来了，采样基数清掉（不把旧基数算成增量；get() 会把 nil
	// 规范成空 map，所以断言“没有旧键”而不是 nil）
	lt := s.traffic.get("203.0.113.1")
	if lt == nil || lt.UpTotal != 1000 || lt.DownTotal != 2000 {
		t.Fatalf("流量库应恢复成备份时的 1000/2000，实际 %+v", lt)
	}
	if len(lt.LastCounters) != 0 {
		t.Fatalf("恢复后采样基数应清掉，实际 %v", lt.LastCounters)
	}
	raw, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "traffic.json"))
	if strings.Contains(string(raw), "tcp:24443") {
		t.Fatalf("落盘的流量库里不应再有旧采样基数，实际：%s", raw)
	}
	// 订阅用户：u1 回来了但令牌哈希为空（备份里不含令牌，须重新生成）；
	// 还在的 u3 保留原令牌哈希（链接不断）；备份后新增的 u2 被移除（快照语义）
	if len(s.cfg.SubUsers) != 2 {
		t.Fatalf("订阅用户应恢复成 u1+u3 共 2 条，实际 %+v", s.cfg.SubUsers)
	}
	byID := map[string]SubUser{}
	for _, u := range s.cfg.SubUsers {
		byID[u.ID] = u
	}
	if u1, ok := byID["u1"]; !ok || u1.Name != "张三" || u1.TokenHash != "" {
		t.Fatalf("u1 应恢复回来且令牌哈希为空（须重新生成），实际 %+v", u1)
	}
	if u3, ok := byID["u3"]; !ok || u3.TokenHash != "hash-ddd" {
		t.Fatalf("还在的 u3 应保留原令牌哈希，实际 %+v", u3)
	}
	if _, ok := byID["u2"]; ok {
		t.Fatalf("备份后新增的 u2 应被移除，实际 %+v", s.cfg.SubUsers)
	}
	if len(s.cfg.MergedSubs) != 1 || s.cfg.MergedSubs[0].ID != "m1" || s.cfg.MergedSubs[0].TokenHash != "hash-bbb" {
		t.Fatalf("合并订阅应恢复成 m1 且保留原令牌哈希，备份后新增的 m2 被移除，实际 %+v", s.cfg.MergedSubs)
	}
}

// TestBackupSettingsAPI136 备份设置与定时备份接口：查/改/立即备份/删除。
func TestBackupSettingsAPI136(t *testing.T) {
	s := newTestSubServer(t)
	get := func() map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/settings/backup", nil)
		rec := httptest.NewRecorder()
		s.handleBackupSettingsGet(rec, req)
		if rec.Code != 200 {
			t.Fatalf("备份设置接口应 200，实际 %d", rec.Code)
		}
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	post := func(body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/settings/backup", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleBackupSettingsPost(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	// 默认：开启/03:00/7 份（旧 config 补默认）
	bk, _ := get()["backup"].(map[string]any)
	if bk["enabled"] != true || bk["time"] != "03:00" || bk["keep"] != float64(7) {
		t.Fatalf("默认备份排程不对：%v", bk)
	}
	// 改成关闭/02:30/3 份
	code, _ := post(`{"enabled": false, "time": "02:30", "keep": 3}`)
	if code != 200 || s.cfg.Backup.Enabled || s.cfg.Backup.Time != "02:30" || s.cfg.Backup.Keep != 3 {
		t.Fatalf("改排程应 200 且生效，实际 code=%d cfg=%+v", code, s.cfg.Backup)
	}
	// 只发 enabled：time/keep 不丢（合并语义，不会被默认值复活）
	code, _ = post(`{"enabled": true}`)
	if code != 200 || !s.cfg.Backup.Enabled || s.cfg.Backup.Time != "02:30" || s.cfg.Backup.Keep != 3 {
		t.Fatalf("只改开关时 time/keep 应保留，实际 code=%d cfg=%+v", code, s.cfg.Backup)
	}
	// 非法时间：400
	code, _ = post(`{"enabled": true, "time": "25:00", "keep": 7}`)
	if code != 400 {
		t.Fatalf("非法时间应 400，实际 %d", code)
	}
	// 定时备份列表：初始为空；立即备份一次后有 1 份
	req := httptest.NewRequest("GET", "/api/v1/scheduled-backups", nil)
	rec := httptest.NewRecorder()
	s.handleScheduledBackupsList(rec, req)
	var lm map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &lm)
	if l, _ := lm["backups"].([]any); len(l) != 0 {
		t.Fatalf("初始定时备份应为空，实际 %v", lm["backups"])
	}
	req = httptest.NewRequest("POST", "/api/v1/scheduled-backups/now", nil)
	rec = httptest.NewRecorder()
	s.handleScheduledBackupNow(rec, req)
	if rec.Code != 200 {
		t.Fatalf("立即备份应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var nm map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &nm)
	id, _ := nm["id"].(string)
	if id == "" {
		t.Fatalf("立即备份应返回 id，实际 %v", nm)
	}
	// 删除：删完再删 404
	req = httptest.NewRequest("DELETE", "/api/v1/scheduled-backups/"+id, nil)
	req.SetPathValue("id", id)
	rec = httptest.NewRecorder()
	s.handleScheduledBackupDelete(rec, req)
	if rec.Code != 200 {
		t.Fatalf("删除应 200，实际 %d", rec.Code)
	}
	req = httptest.NewRequest("DELETE", "/api/v1/scheduled-backups/"+id, nil)
	req.SetPathValue("id", id)
	rec = httptest.NewRecorder()
	s.handleScheduledBackupDelete(rec, req)
	if rec.Code != 404 {
		t.Fatalf("重复删除应 404，实际 %d", rec.Code)
	}
	// 恢复不存在的备份：404
	req = httptest.NewRequest("POST", "/api/v1/scheduled-backups/no-such-id/restore", nil)
	req.SetPathValue("id", "no-such-id")
	rec = httptest.NewRecorder()
	s.handleScheduledBackupRestore(rec, req)
	if rec.Code != 404 {
		t.Fatalf("恢复不存在的备份应 404，实际 %d", rec.Code)
	}
}

// TestImportStillWorks136 导入重构回归：手动导出格式照常导入（relay/
// toml/state 落盘，重启失败时走 200+警告分支，与旧版一致）。
func TestImportStillWorks136(t *testing.T) {
	s := newTestSubServer(t)
	relayConf, _ := os.ReadFile(filepath.Join(s.env.RealmDir, "relay.conf"))
	body, _ := json.Marshal(map[string]any{
		"relay_conf":  string(relayConf),
		"config_toml": "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n",
		"state":       map[string]any{"landings": map[string]any{"203.0.113.1": map[string]any{"expiry": "2027-01-01T00:00:00Z"}}},
	})
	req := httptest.NewRequest("POST", "/api/v1/import", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.handleImport(rec, req)
	if rec.Code != 200 {
		t.Fatalf("导入应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "导入已写入，但重启 realm 失败") {
		t.Fatalf("测试环境 realm 起不来，应走 200+警告分支，实际：%s", rec.Body.String())
	}
	if got := s.state.ext("203.0.113.1").Expiry; got != "2027-01-01T00:00:00Z" {
		t.Fatalf("导入的 state 应落盘，实际 expiry=%q", got)
	}
	// 缺字段：400
	req = httptest.NewRequest("POST", "/api/v1/import", strings.NewReader(`{"relay_conf": ""}`))
	rec = httptest.NewRecorder()
	s.handleImport(rec, req)
	if rec.Code != 400 {
		t.Fatalf("缺 relay_conf 应 400，实际 %d", rec.Code)
	}
	// relay 里没落地：400（config_toml 要合法，否则先被缺字段拦住）
	req = httptest.NewRequest("POST", "/api/v1/import", strings.NewReader(`{"relay_conf": "FOO=1", "config_toml": "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"}`))
	rec = httptest.NewRecorder()
	s.handleImport(rec, req)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "relay_conf 里没有落地") {
		t.Fatalf("无落地的 relay_conf 应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestScheduledBackupToday136 定时备份的“当天已有”判定与补跑逻辑：
// 到了设定时间跑一次；同一天第二次不重复；未到时间不跑；开关关了不跑。
// （用当前分钟当“已到时间”，避免午夜跨天 flaky。）
func TestScheduledBackupToday136(t *testing.T) {
	s := newTestSubServer(t)
	now := time.Now()
	// 设定时间 = 当前分钟（已到），应跑一次
	s.cfg.Backup = BackupCfg{Enabled: true, Time: now.Format("15:04"), Keep: 7}
	if s.scheduledBackupDoneToday(now.Format("20060102")) {
		t.Fatalf("还没备份过，当天判定应为 false")
	}
	s.maybeScheduledBackup()
	if !s.scheduledBackupDoneToday(now.Format("20060102")) {
		t.Fatalf("已到设定时间应备份一次")
	}
	n := len(s.listScheduledBackups())
	s.maybeScheduledBackup()
	if len(s.listScheduledBackups()) != n {
		t.Fatalf("同一天第二次不应重复备份")
	}
	// 设定时间 = 两小时后：若还在今天内（未到）不应跑；若跨了午夜（算到今天
	// 凌晨）则已到、应跑一次——按实际 due 时间断言，不 flaky。
	s2 := newTestSubServer(t)
	future := now.Add(2 * time.Hour).Format("15:04")
	s2.cfg.Backup = BackupCfg{Enabled: true, Time: future, Keep: 7}
	s2.maybeScheduledBackup()
	hh, mm, _ := parseBackupTime(future)
	due := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if now.Before(due) && len(s2.listScheduledBackups()) != 0 {
		t.Fatalf("未到设定时间不应备份")
	}
	if !now.Before(due) && len(s2.listScheduledBackups()) == 0 {
		t.Fatalf("已过设定时间应补跑一次")
	}
	// 开关关了：不跑
	s3 := newTestSubServer(t)
	s3.cfg.Backup = BackupCfg{Enabled: false, Time: now.Format("15:04"), Keep: 7}
	s3.maybeScheduledBackup()
	if len(s3.listScheduledBackups()) != 0 {
		t.Fatalf("开关关了不应备份")
	}
}
