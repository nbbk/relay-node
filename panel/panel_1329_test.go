package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- v1.3.29：备份整机迁移 ----------
// 目标：定时备份从“同机恢复”升级为“整机迁移”——全新空面板恢复后，
// 订阅令牌、到期与按人限流、面板设置、集中管理、通知与证书都在，
// 旧订阅链接直接能用；口令错整单失败、不写盘。

const testMigrationPassphrase = "test-migration-pass-123"

// setMigrationPassphrase1329 直接在配置里设好异地备份口令（= 迁移口令）：
// 随机盐 + PBKDF2 派生主密钥，与设置页走同一条路（handleRemoteBackupPassphrasePost）。
func setMigrationPassphrase1329(t *testing.T, s *Server, pass string) {
	t.Helper()
	salt := make([]byte, remoteBackupSaltLen)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	s.cfg.RemoteBackup.Salt = base64.StdEncoding.EncodeToString(salt)
	s.cfg.RemoteBackup.Key = base64.StdEncoding.EncodeToString(deriveRemoteBackupKey(pass, salt))
	s.cfg.RemoteBackup.PassphraseSet = true
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
}

// setupMigrationSource1329 造一台“源面板”A：带到期+限流的订阅用户与合并订阅、
// 通用订阅、集中管理登记、通知与证书、Logo、nodes.txt、每订阅用量，
// 外加 config.toml 里一条无主按人段（30099）与一条合法按人段（30001）。
func setupMigrationSource1329(t *testing.T) (*Server, string, string, string) {
	t.Helper()
	s := newTestSubServer(t)
	setMigrationPassphrase1329(t, s, testMigrationPassphrase)

	// 通用订阅令牌（明文留着，恢复后要在 B 上验旧链接能用）。
	genTok := generateSubToken()
	enc, err := s.encryptSubTokenNow(genTok)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.SubTokenHash = hashSubToken(genTok)
	s.cfg.SubTokenEnc = enc
	s.cfg.SubTokenMode = "both"
	s.cfg.SubTokenCreatedAt = "2026-11-21T00:00:00Z"

	// 订阅用户：带到期 + 限流（100MB，total），令牌明文留着验旧链接。
	userTok := generateSubToken()
	userEnc, err := s.encryptSubTokenNow(userTok)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.SubUsers = []SubUser{{
		ID: "su-1", Name: "小明", TokenHash: hashSubToken(userTok), TokenEnc: userEnc,
		CreatedAt: "2026-11-20T00:00:00Z", Selection: []SubSelection{{Panel: "local", Landing: 1}},
		Mode: "both", ExpiresAt: "2027-05-01T23:59:59Z",
		TrafficLimitBytes: 100 * 1024 * 1024, TrafficLimitMode: "total",
		ResetMode: "monthly", ResetDay: 1, NextResetAt: "",
	}}
	// 合并订阅：带到期 + 限流（200MB，monthly）。
	mergedTok := generateSubToken()
	mergedEnc, err := s.encryptSubTokenNow(mergedTok)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.MergedSubs = []MergedSub{{
		ID: "ms-1", Name: "合并一", TokenHash: hashSubToken(mergedTok), TokenEnc: mergedEnc,
		CreatedAt: "2026-11-20T00:00:00Z", Selection: []SubSelection{{Panel: "local", Landing: 1}},
		Mode: "both", ExpiresAt: "2027-06-01T23:59:59Z",
		TrafficLimitBytes: 200 * 1024 * 1024, TrafficLimitMode: "monthly",
		ResetMode: "monthly", ResetDay: 1, NextResetAt: "2026-12-01T00:00:00Z",
	}}
	// 按人转发记录 + 端口段。
	s.cfg.UserForwards = []UserForward{{
		Key: "subuser:su-1", SubKind: "subuser", SubID: "su-1", SubName: "小明",
		Panel: "local", LandingAddr: "203.0.113.1", ListenPort: 30001,
		LimitBytes: 100 * 1024 * 1024, LimitMode: "total", ResetMode: "monthly", ResetDay: 1,
	}}
	s.cfg.UserPortStart, s.cfg.UserPortEnd = 30000, 39999
	// 面板设置、通知、集中管理。
	s.cfg.PasswordHash = "test-password-hash-1329"
	s.cfg.TOTPSecret = "JBSWY3DPEHPK3PXP"
	s.cfg.TOTPBackupCodes = []string{"backup-code-hash-1", "backup-code-hash-2"}
	s.cfg.Telegram = TelegramCfg{Enabled: true, BotToken: "bot-test-token-1329", ChatID: "999"}
	s.cfg.CloudflareToken = "cfut_test-token-1329"
	s.cfg.Remotes = []RemotePanel{{ID: "rm-1", Name: "香港plus", BaseURL: "http://127.0.0.1:11837", Token: "rpt_testtoken1329", CreatedAt: "2026-11-20T00:00:00Z"}}
	s.cfg.RemoteTokenHash = "test-remote-token-hash"
	s.cfg.SubPath = "x9f2"
	s.cfg.SubPublicDomain = "cc.example.com"
	s.cfg.SubFileName = "mynodes"
	s.cfg.WorkerOrigin = "http://ccpanel.example.com:11111"
	s.cfg.PanelName = "测试面板"
	s.cfg.LoginNotice = "测试公告"
	s.cfg.LoginSignature = "测试签名"
	s.cfg.LoginAlert = true
	s.cfg.IPWhitelist = []string{"203.0.113.5"}
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
	// 每订阅用量：累计有数，采样基数故意给非零（恢复后应清零）。
	ut := map[string]*userTrafficRec{
		"subuser:su-1": {UpTotal: 12345, DownTotal: 67890, UpPeriod: 100, DownPeriod: 200, PeriodID: 3, LastCounters: map[string][2]int64{"tcp:30001": {100, 200}}},
	}
	utb, _ := json.MarshalIndent(ut, "", "  ")
	if err := os.WriteFile(filepath.Join(s.env.PanelHome, "user_traffic.json"), utb, 0o600); err != nil {
		t.Fatal(err)
	}
	s.userTraffic.mu.Lock()
	s.userTraffic.load()
	s.userTraffic.mu.Unlock()
	// nodes.txt：标准内容 + 一组手动 twin 分组。
	nodesTxt := subTestNodesTxt + "\n## 手动 twin 组\n中转：vless://11111111-2222-3333-4444-555555555555@example.com:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#手动-中转\n直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#手动-直连\n"
	if err := os.WriteFile(s.nodesFilePath(), []byte(nodesTxt), 0o644); err != nil {
		t.Fatal(err)
	}
	// Logo 与证书（假 PEM，只验内容原样恢复，不验真实签发）。
	logoBytes := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 'f', 'a', 'k', 'e'}
	if err := writeFileAtomic(s.logoPath(), logoBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	s.cfg.LogoMime = "image/png"
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
	certPem := "-----BEGIN CERTIFICATE-----\nZmFrZS1jZXJ0LTEzMjk=\n-----END CERTIFICATE-----\n"
	keyPem := "-----BEGIN PRIVATE KEY-----\nZmFrZS1rZXktMTMyOQ==\n-----END PRIVATE KEY-----\n"
	_ = os.MkdirAll(filepath.Join(s.env.PanelHome, "certs"), 0o700)
	if err := os.WriteFile(filepath.Join(s.env.PanelHome, "certs", "cert.pem"), []byte(certPem), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.env.PanelHome, "certs", "key.pem"), []byte(keyPem), 0o600); err != nil {
		t.Fatal(err)
	}
	// config.toml：共享段 + 合法按人段 30001 + 无主按人段 30099。
	toml := "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n\n[[endpoints]]\nlisten = \"0.0.0.0:30001\"\nremote = \"203.0.113.1:24443\"\n\n[[endpoints]]\nlisten = \"0.0.0.0:30099\"\nremote = \"203.0.113.1:24443\"\n"
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return s, genTok, userTok, nodesTxt
}

// backupPayload1329 从源面板取一份定时备份并解析成 importPayload。
func backupPayload1329(t *testing.T, s *Server) importPayload {
	t.Helper()
	raw, err := json.Marshal(s.buildScheduledBackupData("测试备份"))
	if err != nil {
		t.Fatalf("备份数据序列化失败：%v", err)
	}
	var p importPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("备份数据解析失败：%v", err)
	}
	return p
}

// TestMigrationBackupContainsTokens1329 设了迁移口令的备份应含令牌、
// 到期/限流、按人转发、用量、nodes、Logo、证书与迁移信封；
// 没设口令的备份保持旧脱敏口径（无令牌、无信封，migration_enc_missing=true）。
func TestMigrationBackupContainsTokens1329(t *testing.T) {
	s, _, _, _ := setupMigrationSource1329(t)
	data := s.buildScheduledBackupData("测试备份")
	raw, _ := json.Marshal(data)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if _, ok := m["migration_enc"]; !ok {
		t.Fatalf("设了口令的备份应有 migration_enc")
	}
	if _, ok := m["migration_enc_missing"]; ok {
		t.Fatalf("设了口令的备份不应有 migration_enc_missing")
	}
	subs, _ := m["sub_users"].([]any)
	if len(subs) != 1 {
		t.Fatalf("备份应有 1 个订阅用户，实际 %v", subs)
	}
	u := subs[0].(map[string]any)
	for _, k := range []string{"token_hash", "token_enc", "expires_at", "traffic_limit_bytes", "traffic_limit_mode", "reset_mode"} {
		if _, ok := u[k]; !ok {
			t.Fatalf("订阅用户备份缺字段 %s：%v", k, u)
		}
	}
	if u["expires_at"] != "2027-05-01T23:59:59Z" {
		t.Fatalf("到期应是 2027-05-01，实际 %v", u["expires_at"])
	}
	if u["traffic_limit_bytes"] != float64(100*1024*1024) {
		t.Fatalf("限流应是 100MB，实际 %v", u["traffic_limit_bytes"])
	}
	st, _ := m["sub_token"].(map[string]any)
	if st == nil || st["hash"] == "" {
		t.Fatalf("备份应有通用订阅 sub_token.hash，实际 %v", m["sub_token"])
	}
	for _, k := range []string{"user_forwards", "user_traffic", "nodes_txt", "logo_b64", "cert_pem", "cert_key_pem"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("备份缺字段 %s", k)
		}
	}
	if !strings.Contains(m["nodes_txt"].(string), "手动 twin 组") {
		t.Fatalf("nodes_txt 应含手动 twin 分组")
	}
	if !strings.Contains(m["cert_pem"].(string), "ZmFrZS1jZXJ0LTEzMjk=") {
		t.Fatalf("cert_pem 内容不对")
	}

	// 没设口令：旧脱敏口径。
	s2 := newTestSubServer(t)
	s2.cfg.SubUsers = []SubUser{{ID: "su-9", Name: "没口令", TokenHash: "hash9", TokenEnc: "enc9", ExpiresAt: "2027-05-01T23:59:59Z", TrafficLimitBytes: 100}}
	data2 := s2.buildScheduledBackupData("测试备份")
	raw2, _ := json.Marshal(data2)
	var m2 map[string]any
	_ = json.Unmarshal(raw2, &m2)
	if _, ok := m2["migration_enc"]; ok {
		t.Fatalf("没设口令的备份不应有 migration_enc")
	}
	if m2["migration_enc_missing"] != true {
		t.Fatalf("没设口令的备份应有 migration_enc_missing=true，实际 %v", m2["migration_enc_missing"])
	}
	subs2, _ := m2["sub_users"].([]any)
	u2 := subs2[0].(map[string]any)
	if _, ok := u2["token_hash"]; ok {
		t.Fatalf("没设口令的备份里订阅用户不应有 token_hash")
	}
	if _, ok := m2["sub_token"]; ok {
		t.Fatalf("没设口令的备份不应有 sub_token")
	}
	// 到期与限流（非敏感字段）仍在，不因没设口令而丢。
	if u2["expires_at"] != "2027-05-01T23:59:59Z" || u2["traffic_limit_bytes"] != float64(100) {
		t.Fatalf("没设口令的备份仍应保留到期与限流，实际 %v", u2)
	}
}

// TestMigrationCrossMachineRestore1329 关键验收：A 面板备份 → 全新空 B 面板
// 恢复，旧订阅链接直接能用、到期与限流都在、超量停用一致、config.toml
// 无孤儿按人段、远程登记在、Telegram/Cloudflare Token 在、证书原样回来。
func TestMigrationCrossMachineRestore1329(t *testing.T) {
	src, genTok, userTok, nodesTxt := setupMigrationSource1329(t)
	p := backupPayload1329(t, src)
	if p.MigrationEnc == "" {
		t.Fatalf("备份应有 migration_enc")
	}

	dst := newTestSubServer(t)
	var notes []string
	warn, fail := dst.applyImportPayload(p, importApplyOpts{
		preBackupLabel: "测试恢复前备份", auditAction: "测试恢复", auditDetail: "跨机恢复测试",
		restartWarnPrefix: "导入成功，但重启失败：", verifyWarnPrefix: "", user: "test",
		migrationPassphrase: testMigrationPassphrase, notes: &notes,
	})
	if fail != nil {
		t.Fatalf("跨机恢复应成功，实际失败：%v", fail)
	}
	if strings.Contains(warn, "旧链接已失效") {
		t.Fatalf("令牌已随备份恢复，warn 不应说旧链接失效，实际 warn=%q", warn)
	}
	joined := strings.Join(notes, "；")
	for _, want := range []string{"订阅令牌已随备份恢复", "迁移信封已恢复", "订阅路径已恢复为 /x9f2"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("恢复结果应写明 %q，实际 notes=%v", want, notes)
		}
	}
	// 订阅用户：令牌、到期、限流全量回来。
	if len(dst.cfg.SubUsers) != 1 {
		t.Fatalf("恢复后应有 1 个订阅用户，实际 %d", len(dst.cfg.SubUsers))
	}
	u := dst.cfg.SubUsers[0]
	if u.TokenHash != src.cfg.SubUsers[0].TokenHash || u.TokenEnc != src.cfg.SubUsers[0].TokenEnc {
		t.Fatalf("订阅用户令牌应与源面板一致")
	}
	if u.ExpiresAt != "2027-05-01T23:59:59Z" || u.TrafficLimitBytes != 100*1024*1024 || u.TrafficLimitMode != "total" || u.ResetMode != "monthly" {
		t.Fatalf("订阅用户到期/限流应全量恢复，实际 %+v", u)
	}
	if len(dst.cfg.MergedSubs) != 1 {
		t.Fatalf("恢复后应有 1 个合并订阅，实际 %d", len(dst.cfg.MergedSubs))
	}
	ms := dst.cfg.MergedSubs[0]
	if ms.TokenHash != src.cfg.MergedSubs[0].TokenHash || ms.ExpiresAt != "2027-06-01T23:59:59Z" || ms.TrafficLimitBytes != 200*1024*1024 || ms.TrafficLimitMode != "monthly" || ms.NextResetAt != "2026-12-01T00:00:00Z" {
		t.Fatalf("合并订阅令牌/到期/限流应全量恢复，实际 %+v", ms)
	}
	// 通用订阅：哈希回来，密文用恢复回来的 sub_token.key 能解出原令牌。
	if dst.cfg.SubTokenHash != src.cfg.SubTokenHash {
		t.Fatalf("通用订阅令牌哈希应恢复")
	}
	plain, err := dst.decryptSubTokenNow(dst.cfg.SubTokenEnc)
	if err != nil || plain != genTok {
		t.Fatalf("通用订阅密文应用恢复回来的密钥解出原令牌，实际 err=%v", err)
	}
	// 旧链接：每人订阅直接能用；通用订阅已退场，旧链接 410。
	if rec := callSub(t, dst, genTok, "/sub/"+genTok, ""); rec.Code != 410 {
		t.Fatalf("恢复后通用订阅旧链接应 410（已退场），实际 %d %s", rec.Code, rec.Body.String())
	}
	if rec := callSub(t, dst, userTok, "/sub/"+userTok, ""); rec.Code != 200 {
		t.Fatalf("恢复后每人订阅旧链接应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	// 面板设置。
	if dst.cfg.SubPath != "x9f2" || dst.cfg.SubPublicDomain != "cc.example.com" || dst.cfg.PanelName != "测试面板" || dst.cfg.LoginNotice != "测试公告" || dst.cfg.LoginSignature != "测试签名" || !dst.cfg.LoginAlert {
		t.Fatalf("面板设置应恢复，实际 sub_path=%q panel=%q", dst.cfg.SubPath, dst.cfg.PanelName)
	}
	if len(dst.cfg.IPWhitelist) != 1 || dst.cfg.IPWhitelist[0] != "203.0.113.5" {
		t.Fatalf("IP 白名单应恢复，实际 %v", dst.cfg.IPWhitelist)
	}
	// 迁移信封：密码、TOTP、Telegram/Cloudflare、集中管理、异地备份配置。
	if dst.cfg.PasswordHash != "test-password-hash-1329" || dst.cfg.TOTPSecret != "JBSWY3DPEHPK3PXP" || len(dst.cfg.TOTPBackupCodes) != 2 {
		t.Fatalf("密码/TOTP 应随信封恢复")
	}
	if dst.cfg.Telegram.BotToken != "bot-test-token-1329" || dst.cfg.Telegram.ChatID != "999" || !dst.cfg.Telegram.Enabled {
		t.Fatalf("Telegram Token 应随信封恢复，实际 %+v", dst.cfg.Telegram)
	}
	if dst.cfg.CloudflareToken != "cfut_test-token-1329" {
		t.Fatalf("Cloudflare Token 应随信封恢复")
	}
	if len(dst.cfg.Remotes) != 1 || dst.cfg.Remotes[0].Token != "rpt_testtoken1329" || dst.cfg.Remotes[0].BaseURL != "http://127.0.0.1:11837" {
		t.Fatalf("远程面板登记应随信封恢复，实际 %+v", dst.cfg.Remotes)
	}
	if dst.cfg.RemoteTokenHash != "test-remote-token-hash" {
		t.Fatalf("被管端 remote_token_hash 应随信封恢复")
	}
	if !dst.cfg.RemoteBackup.PassphraseSet || dst.cfg.RemoteBackup.Key != src.cfg.RemoteBackup.Key {
		t.Fatalf("异地备份配置应随信封恢复")
	}
	// sub_token.key 文件本身也回来了（与源面板同一份密钥）。
	kb, err := os.ReadFile(filepath.Join(dst.env.PanelHome, subTokenKeyFile))
	if err != nil || len(kb) != 32 {
		t.Fatalf("恢复后 sub_token.key 应是 32 字节，实际 err=%v len=%d", err, len(kb))
	}
	srcKey, _ := os.ReadFile(filepath.Join(src.env.PanelHome, subTokenKeyFile))
	if string(kb) != string(srcKey) {
		t.Fatalf("恢复后的 sub_token.key 应与源面板一致")
	}
	// 每订阅用量：累计回来，采样基数清零。
	up, down, _, _, ok := dst.userTraffic.totals("subuser:su-1")
	if !ok || up != 12345 || down != 67890 {
		t.Fatalf("每订阅用量累计应恢复，实际 up=%d down=%d ok=%v", up, down, ok)
	}
	utb, _ := os.ReadFile(filepath.Join(dst.env.PanelHome, "user_traffic.json"))
	var utm map[string]*userTrafficRec
	_ = json.Unmarshal(utb, &utm)
	if rec := utm["subuser:su-1"]; rec != nil && len(rec.LastCounters) != 0 {
		t.Fatalf("恢复后采样基数应清零，实际 %v", rec.LastCounters)
	}
	// nodes.txt、Logo、证书原样回来。
	nb, _ := os.ReadFile(dst.nodesFilePath())
	if string(nb) != nodesTxt {
		t.Fatalf("nodes.txt 应原样恢复")
	}
	lb, _ := os.ReadFile(dst.logoPath())
	if string(lb) != string([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 'f', 'a', 'k', 'e'}) || dst.cfg.LogoMime != "image/png" {
		t.Fatalf("Logo 应原样恢复")
	}
	cb, _ := os.ReadFile(filepath.Join(dst.env.PanelHome, "certs", "cert.pem"))
	if !strings.Contains(string(cb), "ZmFrZS1jZXJ0LTEzMjk=") {
		t.Fatalf("面板证书应原样恢复")
	}
	// 按人转发：合法段 30001 在，无主段 30099 已清，记录在。
	tomlB, _ := os.ReadFile(filepath.Join(dst.env.RealmDir, "config.toml"))
	if strings.Contains(string(tomlB), "30099") {
		t.Fatalf("config.toml 里不应再有无主按人段 30099：\n%s", tomlB)
	}
	if !strings.Contains(string(tomlB), "30001") {
		t.Fatalf("config.toml 里合法按人段 30001 应在：\n%s", tomlB)
	}
	found := false
	for _, f := range dst.cfg.UserForwards {
		if f.ListenPort == 30001 && f.Key == "subuser:su-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("UserForwards 里应有 30001 的记录，实际 %+v", dst.cfg.UserForwards)
	}
	if dst.cfg.UserPortStart != 30000 || dst.cfg.UserPortEnd != 39999 {
		t.Fatalf("用户端口段应恢复，实际 %d–%d", dst.cfg.UserPortStart, dst.cfg.UserPortEnd)
	}
}

// TestMigrationWrongPassphraseNoWrite1329 口令错整单失败：不写任何文件，
// 目标面板配置原样不动；没输口令也一样拦住。
func TestMigrationWrongPassphraseNoWrite1329(t *testing.T) {
	src, _, _, _ := setupMigrationSource1329(t)
	p := backupPayload1329(t, src)
	dst := newTestSubServer(t)
	cfgBefore, _ := os.ReadFile(filepath.Join(dst.env.PanelHome, "config.json"))
	relayBefore, _ := os.ReadFile(filepath.Join(dst.env.RealmDir, "relay.conf"))

	_, fail := dst.applyImportPayload(p, importApplyOpts{
		preBackupLabel: "测试", auditAction: "测试恢复", user: "test", migrationPassphrase: "wrong-passphrase-123",
	})
	if fail == nil {
		t.Fatalf("口令错应整单失败")
	}
	if !strings.Contains(fail.msg, "口令不对") {
		t.Fatalf("口令错应报“口令不对”，实际 %q", fail.msg)
	}
	cfgAfter, _ := os.ReadFile(filepath.Join(dst.env.PanelHome, "config.json"))
	relayAfter, _ := os.ReadFile(filepath.Join(dst.env.RealmDir, "relay.conf"))
	if string(cfgAfter) != string(cfgBefore) || string(relayAfter) != string(relayBefore) {
		t.Fatalf("口令错不应写盘：config.json/relay.conf 应原样不动")
	}
	if len(dst.cfg.SubUsers) != 0 {
		t.Fatalf("口令错不应恢复出订阅用户")
	}
	if _, err := os.Stat(filepath.Join(dst.env.PanelHome, subTokenKeyFile)); !os.IsNotExist(err) {
		t.Fatalf("口令错不应写出 sub_token.key")
	}

	_, fail2 := dst.applyImportPayload(p, importApplyOpts{
		preBackupLabel: "测试", auditAction: "测试恢复", user: "test", migrationPassphrase: "",
	})
	if fail2 == nil || !strings.Contains(fail2.msg, "异地备份口令") {
		t.Fatalf("没输口令应拦住并提示输口令，实际 %v", fail2)
	}
}

// TestMigrationNoPassphraseRestoreWarns1329 没设迁移口令的备份恢复到
// 全新面板：到期与限流仍在，但令牌为空，warn 应写明须逐个轮换。
func TestMigrationNoPassphraseRestoreWarns1329(t *testing.T) {
	s := newTestSubServer(t)
	s.cfg.SubUsers = []SubUser{{ID: "su-9", Name: "没口令", TokenHash: "hash9", TokenEnc: "enc9", ExpiresAt: "2027-05-01T23:59:59Z", TrafficLimitBytes: 100}}
	// 真机上 config.toml 一直有 [[endpoints]]，测试夹具默认是空文件，先补一份最小配置。
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte("[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s.buildScheduledBackupData("测试备份"))
	var p importPayload
	_ = json.Unmarshal(raw, &p)
	dst := newTestSubServer(t)
	warn, fail := dst.applyImportPayload(p, importApplyOpts{
		preBackupLabel: "测试", auditAction: "测试恢复", user: "test",
	})
	if fail != nil {
		t.Fatalf("没口令的备份恢复应成功（只是令牌丢了），实际失败：%v", fail)
	}
	if !strings.Contains(warn, "轮换") {
		t.Fatalf("warn 应写明须逐个轮换，实际 %q", warn)
	}
	if len(dst.cfg.SubUsers) != 1 || dst.cfg.SubUsers[0].TokenHash != "" {
		t.Fatalf("没口令的备份恢复后令牌应为空，实际 %+v", dst.cfg.SubUsers)
	}
	if dst.cfg.SubUsers[0].ExpiresAt != "2027-05-01T23:59:59Z" || dst.cfg.SubUsers[0].TrafficLimitBytes != 100 {
		t.Fatalf("没口令的备份恢复后到期与限流仍应保留，实际 %+v", dst.cfg.SubUsers[0])
	}
}

// TestMigrationTokenGateNoEnvelope1329 手造备份：正文里带令牌哈希、但没有
// 迁移信封（例如旧口径备份被手工改过，或 sanitize 被绕过）——跨机恢复
// 时也不得用正文里的令牌，须以空哈希插入、走轮换；只有信封解开才认
// 正文令牌。钉住 applyImportPayload 里 mig != nil 这道门。
func TestMigrationTokenGateNoEnvelope1329(t *testing.T) {
	s := newTestSubServer(t)
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte("[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := importPayload{
		Version: "1.3.29", RelayConf: subTestRelayConf,
		ConfigToml: "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n",
		SubUsers:   []subUserBackup{{ID: "su-gate", Name: "门控", TokenHash: "hash-should-not-survive", TokenEnc: "enc-should-not-survive", ExpiresAt: "2027-05-01T23:59:59Z"}},
		MergedSubs: []mergedSubBackup{{ID: "ms-gate", Name: "门控合并", TokenHash: "hash-m-should-not-survive"}},
		// 注意：MigrationEnc 故意留空。
	}
	dst := newTestSubServer(t)
	if _, fail := dst.applyImportPayload(p, importApplyOpts{preBackupLabel: "测试", auditAction: "测试恢复", user: "test"}); fail != nil {
		t.Fatalf("手造备份恢复应成功，实际失败：%v", fail)
	}
	if len(dst.cfg.SubUsers) != 1 || dst.cfg.SubUsers[0].TokenHash != "" || dst.cfg.SubUsers[0].TokenEnc != "" {
		t.Fatalf("没信封时正文令牌不得恢复，用户实际 %+v", dst.cfg.SubUsers)
	}
	if len(dst.cfg.MergedSubs) != 1 || dst.cfg.MergedSubs[0].TokenHash != "" {
		t.Fatalf("没信封时正文令牌不得恢复，合并订阅实际 %+v", dst.cfg.MergedSubs)
	}
}

// TestRestartForSubPathChange1329 恢复后订阅路径变了才重启面板：
// 订阅路由是启动时按旧路径注册的，不重启新路径不生效、旧链接 404。
// 路径不变不重启；变了返回提示并异步重启（stub 计数）。
func TestRestartForSubPathChange1329(t *testing.T) {
	s := newTestSubServer(t)
	n := stubRestart144(t)
	if msg := s.restartForSubPathChange("sub"); msg != "" {
		t.Fatalf("路径没变不应重启，实际 %q", msg)
	}
	time.Sleep(700 * time.Millisecond)
	if n.Load() != 0 {
		t.Fatalf("路径没变时重启次数应为 0，实际 %d", n.Load())
	}
	s.cfg.SubPath = "x9f2"
	msg := s.restartForSubPathChange("sub")
	if !strings.Contains(msg, "/x9f2") || !strings.Contains(msg, "正在重启") {
		t.Fatalf("路径变了应提示重启，实际 %q", msg)
	}
	waitRestart144(t, n, 1)
}

// TestMigrationVersionGate1329 新版备份恢复到旧面板直接拒绝，
// 提示先升级目标面板；老版本备份照旧放行。
func TestMigrationVersionGate1329(t *testing.T) {
	s := newTestSubServer(t)
	p := importPayload{Version: "9.9.9", RelayConf: subTestRelayConf, ConfigToml: "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"}
	if ferr := s.validateImportPayload(p); ferr == nil || !strings.Contains(ferr.msg, "升级") {
		t.Fatalf("新版备份应拒绝并提示升级，实际 %v", ferr)
	}
	p.Version = "1.3.28"
	if ferr := s.validateImportPayload(p); ferr != nil {
		t.Fatalf("老版本备份应放行，实际 %v", ferr)
	}
	p.Version = ""
	if ferr := s.validateImportPayload(p); ferr != nil {
		t.Fatalf("无版本号的老备份应放行，实际 %v", ferr)
	}
}

// TestRemoveOrphanUserEndpoints1329 孤儿段清理：用户端口段内、非共享、
// 不在 UserForwards 里的段删掉；共享段与合法段不动。
func TestRemoveOrphanUserEndpoints1329(t *testing.T) {
	s := newTestSubServer(t)
	s.cfg.UserForwards = []UserForward{{Key: "subuser:su-1", SubKind: "subuser", SubID: "su-1", Panel: "local", LandingAddr: "203.0.113.1", ListenPort: 30001}}
	s.cfg.UserPortStart, s.cfg.UserPortEnd = 30000, 39999
	toml := "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n\n[[endpoints]]\nlisten = \"0.0.0.0:30001\"\nremote = \"203.0.113.1:24443\"\n\n[[endpoints]]\nlisten = \"0.0.0.0:30099\"\nremote = \"203.0.113.1:24443\"\n"
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := s.removeOrphanUserEndpoints(); n != 1 {
		t.Fatalf("应清掉 1 个孤儿段，实际 %d", n)
	}
	after, _ := os.ReadFile(filepath.Join(s.env.RealmDir, "config.toml"))
	if strings.Contains(string(after), "30099") {
		t.Fatalf("孤儿段 30099 应被清掉：\n%s", after)
	}
	if !strings.Contains(string(after), "30001") || !strings.Contains(string(after), "24443") {
		t.Fatalf("合法段与共享段应保留：\n%s", after)
	}
	if n := s.removeOrphanUserEndpoints(); n != 0 {
		t.Fatalf("第二次清理应 0 个，实际 %d", n)
	}
}

// TestScheduledBackupDoneTodayLabel1329 手动“立即备份”不再顶掉当天
// 03:00 的自动备份：只有 label == "自动定时备份" 才算当天已做。
func TestScheduledBackupDoneTodayLabel1329(t *testing.T) {
	s := newTestSubServer(t)
	today := time.Now().Format("20060102")
	// 先写一份当天的手动备份（label 手动立即备份）。
	if _, err := s.runScheduledBackup("手动立即备份"); err != nil {
		t.Fatalf("手动备份应成功：%v", err)
	}
	// 文件名日期就是今天，手动那份不应算。
	if s.scheduledBackupDoneToday(today) {
		t.Fatalf("手动备份不应算当天已做自动备份")
	}
	if _, err := s.runScheduledBackup("自动定时备份"); err != nil {
		t.Fatalf("自动备份应成功：%v", err)
	}
	if !s.scheduledBackupDoneToday(today) {
		t.Fatalf("自动备份后当天应算已做")
	}
}

// TestMaybeScheduledBackupFailureAudited1329 定时备份写盘失败时记审计
// （不再静默），且不发 Telegram（没配 Telegram 时 sendTelegram 直接返回）。
func TestMaybeScheduledBackupFailureAudited1329(t *testing.T) {
	s := newTestSubServer(t)
	s.cfg.Backup = BackupCfg{Enabled: true, Time: "00:00", Keep: 7}
	// scheduled-backups 写成一个文件，MkdirAll 失败，runScheduledBackup 报错。
	if err := os.WriteFile(filepath.Join(s.env.PanelHome, "scheduled-backups"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.maybeScheduledBackup()
	audit, err := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if err != nil {
		t.Fatalf("应写出审计日志，实际 err=%v", err)
	}
	if !strings.Contains(string(audit), "自动定时备份") || !strings.Contains(string(audit), "失败") {
		t.Fatalf("审计日志应记一条自动定时备份失败，实际：\n%s", audit)
	}
	if entries, err := os.ReadDir(filepath.Join(s.env.PanelHome, "scheduled-backups")); err == nil {
		t.Fatalf("失败时不应写出备份文件，实际 %v", entries)
	}
}
