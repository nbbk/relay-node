package main

// ---------- 自动定时备份（v1.3.6） ----------
// 每天一次把面板整机配置备份到面板本机 scheduled-backups/ 目录，带轮转
// （默认保留最近 7 份，防爆盘）。备份内容与手动导出同口径（relay.conf、
// config.toml、state.json、脱敏后的 panel_config），外加流量库与订阅用户
// （只留 id/name/note/selection，不含 token 哈希）；同样不含密码哈希与
// Telegram token。恢复走导入流程（applyImportPayload），不另造格式。
//
// v1.3.29 整机迁移：备份口径升级——订阅用户/合并订阅补全到期与限流字段，
// 令牌哈希与密文在已设异地备份口令时随备份走（未设口令时保持旧脱敏口径，
// 恢复后须轮换令牌，备份列表里明示）；面板设置、按人转发、每订阅用量、
// nodes.txt、Logo、面板证书随备份走；密码/TOTP/令牌密钥等敏感项打包进
// 迁移信封 migration_enc（复用异地备份口令与 encryptRemoteBackup 信封），
// 恢复时口令错整单失败、不写盘。

import (
	"bytes"
	"encoding/base64"
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

// ---------- 备份排程配置（存在 config.json 的 backup 一节） ----------

type BackupCfg struct {
	Enabled bool   `json:"enabled"`
	Time    string `json:"time"` // 每天备份的时刻，HH:MM，如 "03:00"
	Keep    int    `json:"keep"` // 保留份数
}

// 默认：每天 03:00 备份一次，保留最近 7 份。
func defaultBackupCfg() BackupCfg {
	return BackupCfg{Enabled: true, Time: "03:00", Keep: 7}
}

// applyDefaults 只补没填过的值。注意 Enabled 是 bool，零值 false 分不清
// “用户关了”还是“旧 config 里根本没有这一节”，所以用 Time/Keep 全空
// 判断从没存过：从没存过就整节按默认来（默认开启）；存过的值不动。
func (c *BackupCfg) applyDefaults() {
	if c.Time == "" && c.Keep == 0 {
		*c = defaultBackupCfg()
		return
	}
	if c.Time == "" {
		c.Time = "03:00"
	}
	if c.Keep == 0 {
		c.Keep = 7
	}
}

// validate 校验设置页提交的值：时间必须是 HH:MM；保留份数钳制到 1–30。
func (c *BackupCfg) validate() error {
	if _, _, err := parseBackupTime(c.Time); err != nil {
		return fmt.Errorf("备份时间请填 HH:MM，如 03:00")
	}
	if c.Keep < 1 {
		c.Keep = 1
	}
	if c.Keep > 30 {
		c.Keep = 30
	}
	return nil
}

func parseBackupTime(v string) (int, int, error) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, 0, err
	}
	return t.Hour(), t.Minute(), nil
}

// ---------- 定时备份文件 ----------

type ScheduledBackupMeta struct {
	ID                  string `json:"id"`    // 文件名（不含 .json），如 backup-20261119-030001
	TS                  string `json:"ts"`    // RFC3339
	Label               string `json:"label"` // 自动定时备份 / 手动立即备份
	Size                int64  `json:"size"`
	HasMigrationEnc     bool   `json:"has_migration_enc"`     // v1.3.29：含迁移信封，可整机迁移
	MigrationEncMissing bool   `json:"migration_enc_missing"` // v1.3.29：未设迁移口令，换机须轮换令牌
}

func (s *Server) scheduledBackupDir() string {
	return filepath.Join(s.env.PanelHome, "scheduled-backups")
}

// runScheduledBackup 写一份定时备份（导出同款 JSON + 流量库 + 脱敏订阅用户），
// 写完按保留份数轮转。返回备份 id（文件名，不含 .json）。
func (s *Server) runScheduledBackup(label string) (string, error) {
	dir := s.scheduledBackupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	now := time.Now()
	base := "backup-" + now.Format("20060102-150405")
	// 同一秒多次备份的 id：plain 被轮转删掉后不能再复用——否则新备份顶掉
	// 旧 id，远程裁剪会按 id 把它当最旧的删掉（最新备份反而被裁）。
	// 做法：该秒已有任何带后缀的文件时 plain 不再用，后缀取现存最大 +1。
	id := base
	next, maxSuffix, havePlain := 0, 0, false
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if name == base+".json" {
				havePlain = true
				continue
			}
			if strings.HasPrefix(name, base+"-") && strings.HasSuffix(name, ".json") {
				var n int
				if _, err := fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(name, base+"-"), ".json"), "%d", &n); err == nil && n > maxSuffix {
					maxSuffix = n
				}
			}
		}
	}
	if havePlain {
		next = 2
	}
	if maxSuffix >= 2 {
		next = maxSuffix + 1
	}
	if next > 0 {
		id = base + "-" + strconv.Itoa(next)
	}
	path := filepath.Join(dir, id+".json")
	b, err := json.MarshalIndent(s.buildScheduledBackupData(label), "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, b, 0o600); err != nil {
		return "", err
	}
	s.pruneScheduledBackups(s.cfg.Backup.Keep)
	return id, nil
}

// buildScheduledBackupData 定时备份的数据口径：手动导出同款 + kind/label +
// 流量库原文 + 订阅用户（v1.3.29：到期与限流字段全量；令牌哈希与密文只在
// 已设异地备份口令时才进备份——未设口令时保持旧脱敏口径，恢复后须轮换）。
func (s *Server) buildScheduledBackupData(label string) map[string]any {
	out := s.exportData()
	out["kind"] = "scheduled"
	out["label"] = label
	if b, err := os.ReadFile(filepath.Join(s.env.PanelHome, "traffic.json")); err == nil {
		var tj any
		if json.Unmarshal(b, &tj) == nil {
			out["traffic"] = tj
		}
	}
	withTokens := s.migrationPassphraseSet()
	out["sub_users"] = sanitizeSubUsers(s.cfg.SubUsers, withTokens)
	out["merged_subs"] = sanitizeMergedSubs(s.cfg.MergedSubs, withTokens)
	// 独立合并订阅（v1.4.34）：新切片分支，与旧合并订阅同款口径。
	out["independent_merged_subs"] = sanitizeIndMergedSubs(s.cfg.IndependentMergedSubs, withTokens)
	// 套餐档位（v1.4.27）：全量跟走（不含敏感字段，什么时候都进备份）。
	out["plans"] = sanitizePlans(s.cfg.Plans)
	return out
}

// migrationPassphraseSet 迁移口令是否已设：复用异地备份口令——
// 主密钥与盐都在 config.json 里（remoteBackupMasterKey 能读出来）
// 才算设了；没设时备份不带任何令牌哈希/密文与迁移信封。
func (s *Server) migrationPassphraseSet() bool {
	_, err := s.remoteBackupMasterKey()
	return err == nil
}

// subUserBackup / mergedSubBackup 备份文件里的订阅用户。
// v1.3.29：到期与限流字段全量跟走（恢复后不再变永久/不限流）；
// token_hash/token_enc 只在已设迁移口令时才进备份（sanitize 时按
// withTokens 开关控制）。安全性：token_hash 单独拿不到订阅（鉴权要算
// 明文令牌的哈希）；token_enc 没 sub_token.key 解不开，是不透明密文；
// 真正敏感的密钥与口令类字段在迁移信封里（见 migrationEnvelope）。
// Mode（v1.3.17）是内容范围（both|relay|direct），跟着记录走，恢复时不丢；
// 老备份里没有该字段时按空处理（= 全部）。
// GroupID（v1.3.26，分组）：跟着记录走；恢复时指向的分组在备份里没有
// 就清掉（恢复为自己勾选），并在导入结果里写明。
type subUserBackup struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Note      string         `json:"note,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	Selection []SubSelection `json:"selection,omitempty"`
	Mode      string         `json:"mode,omitempty"`
	// LinkMode 订阅级链接模式（Unreleased）："" = 跟随节点。
	LinkMode          string `json:"link_mode,omitempty"`
	GroupID           string `json:"group_id,omitempty"`
	ExpiresAt         string `json:"expires_at,omitempty"`
	TrafficLimitBytes int64  `json:"traffic_limit_bytes,omitempty"`
	TrafficLimitMode  string `json:"traffic_limit_mode,omitempty"`
	ResetMode         string `json:"reset_mode,omitempty"`
	ResetDay          int    `json:"reset_day,omitempty"`
	NextResetAt       string `json:"next_reset_at,omitempty"`
	LastResetAt       string `json:"last_reset_at,omitempty"`
	TrafficWarned80   bool   `json:"traffic_warned_80,omitempty"`
	TrafficWarned100  bool   `json:"traffic_warned_100,omitempty"`
	Disabled          bool   `json:"disabled,omitempty"`
	DisabledReason    string `json:"disabled_reason,omitempty"`
	// NodeUUIDs（v1.3.30）：每落地一个 UUID；备份/恢复全量跟走，丢了它
	// Phase 1 的用户就得重新导入。
	NodeUUIDs map[string]string `json:"node_uuids,omitempty"`
	TokenHash string            `json:"token_hash,omitempty"`
	TokenEnc  string            `json:"token_enc,omitempty"`
	// 套餐档位与登录账号（v1.4.27）：PlanID/Username 全量跟走；
	// PasswordHash 是登录密码的哈希，只在已设迁移口令时才进备份
	// （与 token_hash/token_enc 同款开关），没口令的备份恢复后该
	// 用户须重设密码才能登录。
	PlanID       string `json:"plan_id,omitempty"`
	Username     string `json:"username,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
}

// planBackup 备份文件里的套餐档位（v1.4.27）：全量跟走，不含敏感字段。
type planBackup struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	GroupID           string `json:"group_id,omitempty"`
	TrafficLimitBytes int64  `json:"traffic_limit_bytes,omitempty"`
	TrafficLimitMode  string `json:"traffic_limit_mode,omitempty"`
	ResetMode         string `json:"reset_mode,omitempty"`
	ResetDay          int    `json:"reset_day,omitempty"`
	ExpiresDays       int    `json:"expires_days,omitempty"`
	Mode              string `json:"mode,omitempty"`
	Note              string `json:"note,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
}

func sanitizePlans(plans []Plan) []planBackup {
	out := make([]planBackup, 0, len(plans))
	for _, p := range plans {
		out = append(out, planBackup{ID: p.ID, Name: p.Name, GroupID: p.GroupID,
			TrafficLimitBytes: p.TrafficLimitBytes, TrafficLimitMode: p.TrafficLimitMode,
			ResetMode: p.ResetMode, ResetDay: p.ResetDay, ExpiresDays: p.ExpiresDays,
			Mode: p.Mode, Note: p.Note, CreatedAt: p.CreatedAt})
	}
	return out
}

type mergedSubBackup struct {
	ID                string         `json:"id"`
	Name              string         `json:"name,omitempty"`
	CreatedAt         string         `json:"created_at,omitempty"`
	Selection         []SubSelection `json:"selection"`
	Mode              string         `json:"mode,omitempty"`
	GroupID           string         `json:"group_id,omitempty"`
	ExpiresAt         string         `json:"expires_at,omitempty"`
	TrafficLimitBytes int64          `json:"traffic_limit_bytes,omitempty"`
	TrafficLimitMode  string         `json:"traffic_limit_mode,omitempty"`
	ResetMode         string         `json:"reset_mode,omitempty"`
	ResetDay          int            `json:"reset_day,omitempty"`
	NextResetAt       string         `json:"next_reset_at,omitempty"`
	LastResetAt       string         `json:"last_reset_at,omitempty"`
	TrafficWarned80   bool           `json:"traffic_warned_80,omitempty"`
	TrafficWarned100  bool           `json:"traffic_warned_100,omitempty"`
	Disabled          bool           `json:"disabled,omitempty"`
	DisabledReason    string         `json:"disabled_reason,omitempty"`
	// NodeUUIDs（v1.3.30）：与 subUserBackup 同款，见上。
	NodeUUIDs map[string]string `json:"node_uuids,omitempty"`
	TokenHash string            `json:"token_hash,omitempty"`
	TokenEnc  string            `json:"token_enc,omitempty"`
}

// indMergedSubBackup 备份文件里的独立合并订阅（v1.4.34）：与
// mergedSubBackup 同款字段，但没有 GroupID（独立订阅不进分组）；
// token_hash/token_enc 只在已设迁移口令时才进备份（与旧合并订阅同款开关）。
type indMergedSubBackup struct {
	ID                string            `json:"id"`
	Name              string            `json:"name,omitempty"`
	CreatedAt         string            `json:"created_at,omitempty"`
	Selection         []SubSelection    `json:"selection"`
	Mode              string            `json:"mode,omitempty"`
	ExpiresAt         string            `json:"expires_at,omitempty"`
	TrafficLimitBytes int64             `json:"traffic_limit_bytes,omitempty"`
	TrafficLimitMode  string            `json:"traffic_limit_mode,omitempty"`
	ResetMode         string            `json:"reset_mode,omitempty"`
	ResetDay          int               `json:"reset_day,omitempty"`
	NextResetAt       string            `json:"next_reset_at,omitempty"`
	LastResetAt       string            `json:"last_reset_at,omitempty"`
	TrafficWarned80   bool              `json:"traffic_warned_80,omitempty"`
	TrafficWarned100  bool              `json:"traffic_warned_100,omitempty"`
	Disabled          bool              `json:"disabled,omitempty"`
	DisabledReason    string            `json:"disabled_reason,omitempty"`
	NodeUUIDs         map[string]string `json:"node_uuids,omitempty"`
	TokenHash         string            `json:"token_hash,omitempty"`
	TokenEnc          string            `json:"token_enc,omitempty"`
	BackendScope      string            `json:"backend_scope,omitempty"`
	BackendTargets    []string          `json:"backend_targets,omitempty"`
}

// subTokenBackup 备份文件里的通用订阅令牌（v1.3.29）：只在已设迁移口令
// 时才进备份；未设口令时这一整节都不写，恢复后通用订阅按本机原值不动。
type subTokenBackup struct {
	Hash      string `json:"hash"`
	Enc       string `json:"enc,omitempty"`
	Mode      string `json:"mode,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

func sanitizeSubUsers(users []SubUser, withTokens bool) []subUserBackup {
	out := make([]subUserBackup, 0, len(users))
	for _, u := range users {
		b := subUserBackup{ID: u.ID, Name: u.Name, Note: u.Note, CreatedAt: u.CreatedAt, Selection: u.Selection, Mode: u.Mode, GroupID: u.GroupID,
			ExpiresAt: u.ExpiresAt, TrafficLimitBytes: u.TrafficLimitBytes, TrafficLimitMode: u.TrafficLimitMode,
			ResetMode: u.ResetMode, ResetDay: u.ResetDay, NextResetAt: u.NextResetAt, LastResetAt: u.LastResetAt,
			TrafficWarned80: u.TrafficWarned80, TrafficWarned100: u.TrafficWarned100, Disabled: u.Disabled, DisabledReason: u.DisabledReason,
			NodeUUIDs: u.NodeUUIDs, PlanID: u.PlanID, Username: u.Username, LinkMode: u.LinkMode}
		if withTokens {
			b.TokenHash, b.TokenEnc = u.TokenHash, u.TokenEnc
			b.PasswordHash = u.PasswordHash
		}
		out = append(out, b)
	}
	return out
}

func sanitizeMergedSubs(subs []MergedSub, withTokens bool) []mergedSubBackup {
	out := make([]mergedSubBackup, 0, len(subs))
	for _, m := range subs {
		b := mergedSubBackup{ID: m.ID, Name: m.Name, CreatedAt: m.CreatedAt, Selection: m.Selection, Mode: m.Mode, GroupID: m.GroupID,
			ExpiresAt: m.ExpiresAt, TrafficLimitBytes: m.TrafficLimitBytes, TrafficLimitMode: m.TrafficLimitMode,
			ResetMode: m.ResetMode, ResetDay: m.ResetDay, NextResetAt: m.NextResetAt, LastResetAt: m.LastResetAt,
			TrafficWarned80: m.TrafficWarned80, TrafficWarned100: m.TrafficWarned100, Disabled: m.Disabled, DisabledReason: m.DisabledReason,
			NodeUUIDs: m.NodeUUIDs}
		if withTokens {
			b.TokenHash, b.TokenEnc = m.TokenHash, m.TokenEnc
		}
		out = append(out, b)
	}
	return out
}

func sanitizeIndMergedSubs(subs []IndependentMergedSub, withTokens bool) []indMergedSubBackup {
	out := make([]indMergedSubBackup, 0, len(subs))
	for _, m := range subs {
		b := indMergedSubBackup{ID: m.ID, Name: m.Name, CreatedAt: m.CreatedAt, Selection: m.Selection, Mode: m.Mode,
			ExpiresAt: m.ExpiresAt, TrafficLimitBytes: m.TrafficLimitBytes, TrafficLimitMode: m.TrafficLimitMode,
			ResetMode: m.ResetMode, ResetDay: m.ResetDay, NextResetAt: m.NextResetAt, LastResetAt: m.LastResetAt,
			TrafficWarned80: m.TrafficWarned80, TrafficWarned100: m.TrafficWarned100, Disabled: m.Disabled, DisabledReason: m.DisabledReason,
			NodeUUIDs: m.NodeUUIDs, BackendScope: m.BackendScope, BackendTargets: m.BackendTargets}
		if withTokens {
			b.TokenHash, b.TokenEnc = m.TokenHash, m.TokenEnc
		}
		out = append(out, b)
	}
	return out
}

// ---------- 迁移信封 migration_enc（v1.3.29） ----------
// 敏感项（密码哈希、TOTP、令牌密钥、Telegram/Cloudflare Token、远程面板
// 明文令牌、远程备份配置）打包进一个信封，用异地备份口令加密：密钥用面板
// 已存的主密钥（remoteBackupMasterKey），信封格式复用 encryptRemoteBackup
// （RPBK‖版本‖盐‖nonce‖密文，AES-256-GCM），不发明新格式；解密复用
// decryptRemoteBackup（用户输口令、按信封里的盐重派生），口令错 GCM 认证
// 失败。未设口令时不写信封，备份文件里记 migration_enc_missing:true，
// 备份列表与恢复入口据此提示，不静默降级。
type migrationEnvelope struct {
	SubTokenKey      string          `json:"sub_token_key,omitempty"` // base64，32 字节
	PasswordHash     string          `json:"password_hash,omitempty"`
	TOTPSecret       string          `json:"totp_secret,omitempty"`
	TOTPBackupCodes  []string        `json:"totp_backup_codes,omitempty"`
	TelegramBotToken string          `json:"telegram_bot_token,omitempty"`
	CloudflareToken  string          `json:"cloudflare_token,omitempty"`
	Remotes          []RemotePanel   `json:"remotes,omitempty"`
	RemoteTokenHash  string          `json:"remote_token_hash,omitempty"`
	RemoteBackup     RemoteBackupCfg `json:"remote_backup,omitempty"`
}

// buildMigrationEnvelope 打包并加密迁移信封（调用方须先确认已设口令）。
// sub_token.key 文件不存在（还没生成过令牌密钥）时信封里不带它。
func (s *Server) buildMigrationEnvelope() (string, error) {
	key, err := s.remoteBackupMasterKey()
	if err != nil {
		return "", err
	}
	salt, err := s.remoteBackupSalt()
	if err != nil {
		return "", err
	}
	env := migrationEnvelope{
		PasswordHash: s.cfg.PasswordHash, TOTPSecret: s.cfg.TOTPSecret, TOTPBackupCodes: s.cfg.TOTPBackupCodes,
		TelegramBotToken: s.cfg.Telegram.BotToken, CloudflareToken: s.cfg.CloudflareToken,
		Remotes: s.cfg.Remotes, RemoteTokenHash: s.cfg.RemoteTokenHash, RemoteBackup: s.cfg.RemoteBackup,
	}
	if b, err := os.ReadFile(filepath.Join(s.env.PanelHome, subTokenKeyFile)); err == nil && len(b) == 32 {
		env.SubTokenKey = base64.StdEncoding.EncodeToString(b)
	}
	plain, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	enc, err := encryptRemoteBackup(key, salt, plain)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

// decryptMigrationEnvelope 解迁移信封：没信封返回 (nil, nil)；有信封没
// 口令、口令错、信封损坏都返回 *importFailure，且调用方在它之后才写盘，
// 口令错整单失败、不写任何文件、不拍恢复前快照。
func (s *Server) decryptMigrationEnvelope(p importPayload, passphrase string) (*migrationEnvelope, *importFailure) {
	if strings.TrimSpace(p.MigrationEnc) == "" {
		return nil, nil
	}
	if strings.TrimSpace(passphrase) == "" {
		return nil, &importFailure{400, "这份备份含迁移信封（登录密码、TOTP、令牌密钥等），恢复需要输入异地备份口令"}
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(p.MigrationEnc))
	if err != nil {
		return nil, &importFailure{400, "迁移信封格式不对"}
	}
	plain, err := decryptRemoteBackup(passphrase, raw)
	if err != nil {
		if err == errRemoteBadPassphrase {
			return nil, &importFailure{400, "迁移口令不对，备份未改动"}
		}
		return nil, &importFailure{400, err.Error()}
	}
	var env migrationEnvelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, &importFailure{400, "迁移信封格式不对"}
	}
	return &env, nil
}

// backupIDOrder 解析备份 id/键 backup-YYYYMMDD-HHMMSS[-N]，返回（时间戳，序号）：
// 没后缀的序号是 0。字典序在同一秒多次备份时会排错（".json" 的 . 比 "-2" 的 -
// 大、无后缀那份会被当成最新；"-10" 又比 "-9" 小），列表与轮转不能只按字典序。
func backupIDOrder(id string) (string, int) {
	rest := strings.TrimPrefix(id, "backup-")
	if len(rest) < 15 {
		return rest, 0
	}
	ts, suffix := rest[:15], rest[15:]
	n := 0
	if strings.HasPrefix(suffix, "-") {
		fmt.Sscanf(suffix[1:], "%d", &n)
	}
	return ts, n
}

// listScheduledBackups 列出定时备份（新的在前）。文件损坏只丢 label，不丢整条。
func (s *Server) listScheduledBackups() []ScheduledBackupMeta {
	entries, err := os.ReadDir(s.scheduledBackupDir())
	if err != nil {
		return nil
	}
	var out []ScheduledBackupMeta
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "backup-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		m := ScheduledBackupMeta{ID: id, TS: parseScheduledBackupTS(id), Size: info.Size()}
		if b, err := os.ReadFile(filepath.Join(s.scheduledBackupDir(), name)); err == nil {
			var head struct {
				Label               string `json:"label"`
				MigrationEnc        string `json:"migration_enc"`
				MigrationEncMissing bool   `json:"migration_enc_missing"`
			}
			if json.Unmarshal(b, &head) == nil {
				m.Label = head.Label
				m.HasMigrationEnc = strings.TrimSpace(head.MigrationEnc) != ""
				m.MigrationEncMissing = head.MigrationEncMissing
			}
		}
		out = append(out, m)
	}
	// 新的在前：按（时间戳，序号）倒序，不只按 id 字典序（见 backupIDOrder）。
	sort.Slice(out, func(i, j int) bool {
		ti, ni := backupIDOrder(out[i].ID)
		tj, nj := backupIDOrder(out[j].ID)
		if ti != tj {
			return ti > tj
		}
		return ni > nj
	})
	return out
}

func (s *Server) lastScheduledBackup() *ScheduledBackupMeta {
	list := s.listScheduledBackups()
	if len(list) == 0 {
		return nil
	}
	return &list[0]
}

// scheduledBackupDoneToday 当天（YYYYMMDD）是否已有自动定时备份。
// v1.3.29：只认 label == "自动定时备份" 的文件——手动“立即备份”
// 不再顶掉当天 03:00 的自动备份（之前只看文件名前缀，手动点一次
// 当天自动备份就被跳过）。
func (s *Server) scheduledBackupDoneToday(yyyymmdd string) bool {
	for _, b := range s.listScheduledBackups() {
		if b.Label == "自动定时备份" && strings.HasPrefix(b.ID, "backup-"+yyyymmdd+"-") {
			return true
		}
	}
	return false
}

// pruneScheduledBackups 轮转：只留最新的 keep 份，多的删掉（防爆盘）。
func (s *Server) pruneScheduledBackups(keep int) {
	if keep <= 0 {
		keep = 7
	}
	list := s.listScheduledBackups() // 新的在前
	if len(list) <= keep {
		return
	}
	for _, b := range list[keep:] {
		_ = os.Remove(filepath.Join(s.scheduledBackupDir(), b.ID+".json"))
	}
}

// parseScheduledBackupTS 从文件名 backup-YYYYMMDD-HHMMSS[-N] 还原备份时间。
func parseScheduledBackupTS(id string) string {
	rest := strings.TrimPrefix(id, "backup-")
	if len(rest) < 15 {
		return ""
	}
	t, err := time.ParseInLocation("20060102-150405", rest[:15], time.Local)
	if err != nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func (s *Server) readScheduledBackup(id string) ([]byte, error) {
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(s.scheduledBackupDir(), id+".json"))
}

func (s *Server) deleteScheduledBackup(id string) error {
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return os.ErrNotExist
	}
	return os.Remove(filepath.Join(s.scheduledBackupDir(), id+".json"))
}

// ---------- 导出数据的共用口径（手动导出与定时备份共用） ----------

// exportData 导出/定时备份的共用数据口径：relay.conf、config.toml、state.json
// 与脱敏后的 panel_config（不含密码哈希与 Telegram token，备份排程也记一份）。
//
// v1.3.29 整机迁移：另补按人转发与端口段、每订阅用量、nodes.txt、Logo、
// 面板证书（只含 cert.pem/key.pem，不含 acme-key.pem，可重建）；通用订阅
// 令牌与迁移信封只在已设异地备份口令（= 迁移口令）时才写——未设口令时
// 保持旧脱敏口径，备份文件里记 migration_enc_missing:true，恢复后须
// 轮换令牌，不静默降级。
func (s *Server) exportData() map[string]any {
	relayConf, _ := os.ReadFile(filepath.Join(s.env.RealmDir, "relay.conf"))
	toml, _ := os.ReadFile(filepath.Join(s.env.RealmDir, "config.toml"))
	state, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "state.json"))
	var stateJSON any
	_ = json.Unmarshal(state, &stateJSON)
	// 脱敏：不含密码哈希和 telegram token（这两项走迁移信封，见下）。
	panelCfg := map[string]any{
		"username": s.cfg.Username, "port": s.cfg.Port,
		"https_enabled": s.cfg.HTTPSEnabled, "https_domain": s.cfg.HTTPSDomain,
		"ip_whitelist": s.cfg.IPWhitelist,
		"telegram":     map[string]any{"enabled": s.cfg.Telegram.Enabled, "chat_id": s.cfg.Telegram.ChatID},
		"monitor":      s.cfg.Monitor,
		"latency":      s.cfg.Latency,
		"backup":       s.cfg.Backup,
		"version":      Version,
		// v1.3.29：面板设置全量写出，恢复时由 importPanelConfig 读回
		// （之前只写出不读回，是死数据；敏感字段仍不走这里，走信封）。
		"panel_name": s.cfg.PanelName, "login_notice": s.cfg.LoginNotice, "login_signature": s.cfg.LoginSignature,
		"sub_path": s.cfg.SubPath, "sub_public_domain": s.cfg.SubPublicDomain, "sub_file_name": s.cfg.SubFileName,
		"worker_origin": s.cfg.WorkerOrigin, "manager_only": s.cfg.ManagerOnly, "login_alert": s.cfg.LoginAlert,
	}
	out := map[string]any{
		"version": Version, "exported_at": time.Now().UTC().Format(time.RFC3339),
		"relay_conf": string(relayConf), "config_toml": string(toml),
		"state": stateJSON, "panel_config": panelCfg,
		// 分组（v1.3.26）：随整机配置一起导出/导入（不含令牌与密码哈希，
		// 与订阅用户同一口径）；手动导出与定时备份都带。
		"sub_groups": s.cfg.SubGroups,
		// 公告（v1.4.29）：随整机配置一起导出/导入（与 sub_groups 同款
		// 顶级字段口径）；手动导出与定时备份都带。
		"announcements": s.cfg.Announcements,
		// 远程配置（v1.4.29）：已签名信封原文随整机配置一起导出/导入
		// （与 announcements 同款顶级字段口径）；手动导出与定时备份
		// 都带。信封是公开内容（客户端验签用），进备份文件无妨。
		"client_config_json": s.cfg.ClientConfigJSON,
		// v1.3.29：按人转发与端口段（恢复后按人段不再成孤儿）。
		"user_forwards":   s.cfg.UserForwards,
		"user_port_start": s.cfg.UserPortStart,
		"user_port_end":   s.cfg.UserPortEnd,
	}
	if b, err := os.ReadFile(filepath.Join(s.env.PanelHome, "user_traffic.json")); err == nil {
		var ut any
		if json.Unmarshal(b, &ut) == nil {
			out["user_traffic"] = ut
		}
	}
	// v1.3.30：推送去重表（恢复后重放仍会被挡住，否则恢复完 agent 一重推
	// 就 double-count；老备份没有这一项时恢复后按空表处理）。
	if b, err := os.ReadFile(filepath.Join(s.env.PanelHome, "node_push_seen.json")); err == nil {
		var seen any
		if json.Unmarshal(b, &seen) == nil {
			out["node_push_seen"] = seen
		}
	}
	if b, err := os.ReadFile(s.nodesFilePath()); err == nil {
		out["nodes_txt"] = string(b)
	}
	if b, err := os.ReadFile(s.logoPath()); err == nil && len(b) > 0 {
		out["logo_b64"] = base64.StdEncoding.EncodeToString(b)
		out["logo_mime"] = s.cfg.LogoMime
	}
	if b, err := os.ReadFile(filepath.Join(s.env.PanelHome, "certs", "cert.pem")); err == nil && len(b) > 0 {
		if kb, err := os.ReadFile(filepath.Join(s.env.PanelHome, "certs", "key.pem")); err == nil && len(kb) > 0 {
			out["cert_pem"] = string(b)
			out["cert_key_pem"] = string(kb)
		}
	}
	if s.migrationPassphraseSet() {
		if s.cfg.SubTokenHash != "" {
			out["sub_token"] = subTokenBackup{Hash: s.cfg.SubTokenHash, Enc: s.cfg.SubTokenEnc, Mode: s.cfg.SubTokenMode, ExpiresAt: s.cfg.SubTokenExpiresAt, CreatedAt: s.cfg.SubTokenCreatedAt}
		}
		if enc, err := s.buildMigrationEnvelope(); err == nil {
			out["migration_enc"] = enc
		} else {
			out["migration_enc_missing"] = true
		}
	} else {
		out["migration_enc_missing"] = true
	}
	return out
}

// ---------- 导入 / 恢复的共用落地逻辑 ----------

// importPayload 导入文件（手动导出与定时备份共用格式；定时备份多了
// kind/label/traffic/sub_users/merged_subs，手动导出没有这些键时不走那几步）。
// v1.3.29：补 sub_token、按人转发、每订阅用量、nodes.txt、Logo、证书、
// 迁移信封与版本号；passphrase 只在导入请求体里带（前端恢复时用户现输），
// 绝不进备份文件。
type importPayload struct {
	Version       string               `json:"version"`
	RelayConf     string               `json:"relay_conf"`
	ConfigToml    string               `json:"config_toml"`
	State         json.RawMessage      `json:"state"`
	Traffic       json.RawMessage      `json:"traffic"`
	SubUsers      []subUserBackup      `json:"sub_users"`
	MergedSubs    []mergedSubBackup    `json:"merged_subs"`
	IndMergedSubs []indMergedSubBackup `json:"independent_merged_subs"`
	Plans         []planBackup         `json:"plans"`
	SubGroups     []SubGroup           `json:"sub_groups"`
	Announcements []Announcement       `json:"announcements"`
	// ClientConfigJSON 远程配置信封原文（v1.4.29）：指针 = “文件里有
	// 才恢复，没有不动”（空字符串也是合法值，表示“已撤下”，不能用
	// 零值判断）；恢复时不校验内容（备份是本面板自己导出的，发布时
	// 已校验过形状）。
	ClientConfigJSON    *string            `json:"client_config_json"`
	SubToken            *subTokenBackup    `json:"sub_token"`
	UserForwards        []UserForward      `json:"user_forwards"`
	UserPortStart       int                `json:"user_port_start"`
	UserPortEnd         int                `json:"user_port_end"`
	UserTraffic         json.RawMessage    `json:"user_traffic"`
	NodePushSeen        json.RawMessage    `json:"node_push_seen"`
	NodesTxt            string             `json:"nodes_txt"`
	LogoB64             string             `json:"logo_b64"`
	LogoMime            string             `json:"logo_mime"`
	CertPem             string             `json:"cert_pem"`
	CertKeyPem          string             `json:"cert_key_pem"`
	MigrationEnc        string             `json:"migration_enc"`
	MigrationEncMissing bool               `json:"migration_enc_missing"`
	Passphrase          string             `json:"passphrase,omitempty"`
	PanelConfig         *importPanelConfig `json:"panel_config"`
}

// importPanelConfig 导入时真正读回的面板设置（v1.3.29）：
// 指针字段 = “文件里有才恢复，没有不动”（0/空/false 也是合法值，不能
// 用零值判断）；敏感字段（密码哈希、TOTP、Telegram/Cloudflare Token、
// 远程面板明文令牌、远程备份密钥）不走这里，走迁移信封。
type importPanelConfig struct {
	Username     *string  `json:"username"`
	Port         *int     `json:"port"`
	HTTPSEnabled *bool    `json:"https_enabled"`
	HTTPSDomain  *string  `json:"https_domain"`
	IPWhitelist  []string `json:"ip_whitelist"`
	Telegram     *struct {
		Enabled *bool   `json:"enabled"`
		ChatID  *string `json:"chat_id"`
	} `json:"telegram"`
	Monitor         *MonitorCfg `json:"monitor"`
	Latency         *LatencyCfg `json:"latency"`
	PanelName       *string     `json:"panel_name"`
	LoginNotice     *string     `json:"login_notice"`
	LoginSignature  *string     `json:"login_signature"`
	SubPath         *string     `json:"sub_path"`
	SubPublicDomain *string     `json:"sub_public_domain"`
	SubFileName     *string     `json:"sub_file_name"`
	WorkerOrigin    *string     `json:"worker_origin"`
	ManagerOnly     *bool       `json:"manager_only"`
	LoginAlert      *bool       `json:"login_alert"`
	Backup          *BackupCfg  `json:"backup"`
}

type importFailure struct {
	status int
	msg    string
}

func (e *importFailure) Error() string { return e.msg }

type importApplyOpts struct {
	preBackupLabel      string // 恢复前自动备份的 label
	auditAction         string // 审计动作，如“导入配置”/“恢复定时备份”
	auditDetail         string
	restartWarnPrefix   string // 写盘后重启失败的文案前缀
	verifyWarnPrefix    string // "" = 不做端口校验；否则为端口校验失败的文案前缀
	user                string
	migrationPassphrase string    // v1.3.29：解迁移信封用的异地备份口令（远程恢复已输过的不再多问）
	notes               *[]string // v1.3.29：恢复结果里要向用户写明的事项（令牌状态、订阅路径等），nil = 不收集
}

func (o importApplyOpts) addNote(n string) {
	if o.notes != nil && n != "" {
		*o.notes = append(*o.notes, n)
	}
}

// versionNewer 备份版本是否高于当前面板版本（v1.3.29：新备份恢复到旧面板
// 直接拒绝，提示先升级目标面板；版本号解析不出时不拦，老备份照旧）。
func versionNewer(have, current string) bool {
	parse := func(v string) (int, int, int, bool) {
		var a, b, c int
		v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
		if _, err := fmt.Sscanf(v, "%d.%d.%d", &a, &b, &c); err != nil {
			return 0, 0, 0, false
		}
		return a, b, c, true
	}
	ha, hb, hc, ok1 := parse(have)
	ca, cb, cc, ok2 := parse(current)
	if !ok1 || !ok2 {
		return false
	}
	if ha != ca {
		return ha > ca
	}
	if hb != cb {
		return hb > cb
	}
	return hc > cc
}

// validateImportPayload 校验导入文件（只读，不写盘；调用方先调它再拿写锁）。
func (s *Server) validateImportPayload(p importPayload) *importFailure {
	if versionNewer(p.Version, Version) {
		return &importFailure{400, "这份备份来自新版面板 v" + strings.TrimSpace(strings.TrimPrefix(p.Version, "v")) + "，请先把目标面板升级到该版本或更高再恢复"}
	}
	if rc := parseRelayConf([]byte(p.RelayConf)); len(rc.Landings) == 0 && !strings.Contains(p.RelayConf, "LANDING=") {
		return &importFailure{400, "relay_conf 里没有落地"}
	}
	if err := validateConfigToml([]byte(p.ConfigToml)); err != nil {
		return &importFailure{400, "config_toml 校验失败：" + err.Error()}
	}
	return nil
}

// applyImportPayload 导入/恢复的共用落地逻辑（调用方须先拿 op 锁）：
// 先解迁移信封（口令错整单失败、不写盘）→ 备份当前 → 写 relay/config/state
// → 恢复流量库与订阅用户（文件里有才走）→ 恢复面板设置与迁移信封
// → 清孤儿按人段、按限流重建按人转发 → 恢复备份排程 → regenProxies
// → 重启 realm → 可选端口校验。
// 写盘前的硬失败返回 *importFailure；写盘后重启/校验失败只返回 warn
// （配置已生效），调用方按 200 + errResp 处理。成功时记审计。
func (s *Server) applyImportPayload(p importPayload, o importApplyOpts) (warn string, err *importFailure) {
	// v1.3.29：先解迁移信封再动任何文件——口令错直接整体失败，
	// 不写盘、不拍恢复前快照。没信封时返回 nil，照旧流程走。
	mig, ferr := s.decryptMigrationEnvelope(p, o.migrationPassphrase)
	if ferr != nil {
		return "", ferr
	}
	// v1.4.28：先恢复令牌密钥，再核对订阅令牌成对性——成对校验要解密
	// 备份密文，跨机恢复必须用备份里的密钥解，不能用本机旧密钥。密钥
	// 写盘在下面迁移信封段还会再做一次（同值，幂等），这里提前是为了
	// 让 SubUsers/MergedSubs 恢复循环里的 tokenPairMatches 能解开。
	if mig != nil && mig.SubTokenKey != "" {
		if kb, err := base64.StdEncoding.DecodeString(strings.TrimSpace(mig.SubTokenKey)); err == nil && len(kb) == 32 {
			if err := writeFileAtomic(filepath.Join(s.env.PanelHome, subTokenKeyFile), kb, 0o600); err == nil {
				s.subKeyMu.Lock()
				s.subKey = append([]byte(nil), kb...)
				s.subKeyMu.Unlock()
			}
		}
	}
	_, _ = s.backupNow(o.preBackupLabel, "realm")
	if err := s.writeRelayConfAtomic([]byte(p.RelayConf)); err != nil {
		return "", &importFailure{500, "写入 relay.conf 失败：" + err.Error()}
	}
	if err := s.writeConfigTomlAtomic([]byte(p.ConfigToml)); err != nil {
		return "", &importFailure{500, "写入 config.toml 失败：" + err.Error()}
	}
	if len(p.State) > 0 {
		var st PanelState
		if json.Unmarshal(p.State, &st) == nil {
			s.stateMu.Lock()
			s.state = st
			if s.state.Landings == nil {
				s.state.Landings = map[string]*LandingExt{}
			}
			s.stateMu.Unlock()
			_ = s.saveState()
		}
	}
	// 流量库：快照整体写回，采样基数清掉（下次采样重记基数，不把恢复前
	// 的旧基数算成一笔增量，避免恢复后流量突然暴涨）。
	if len(p.Traffic) > 0 {
		var td map[string]*landingTraffic
		if json.Unmarshal(p.Traffic, &td) == nil {
			for _, lt := range td {
				if lt != nil {
					lt.LastCounters = nil
				}
			}
			if b, err := json.MarshalIndent(td, "", "  "); err == nil {
				_ = writeFileAtomic(filepath.Join(s.env.PanelHome, "traffic.json"), b, 0o600)
				s.traffic.mu.Lock()
				s.traffic.load()
				s.traffic.mu.Unlock()
			}
		}
	}
	// 每订阅用量（v1.3.29）：整体写回，采样基数清零（与 traffic.json
	// 同款做法：LastCounters 置 nil，下次采样重记基数，避免恢复后
	// 流量突然暴涨；get/addDelta 里有 nil 重建保护）。
	if len(p.UserTraffic) > 0 {
		var ut map[string]*userTrafficRec
		if json.Unmarshal(p.UserTraffic, &ut) == nil {
			for _, rec := range ut {
				if rec != nil {
					rec.LastCounters = nil
				}
			}
			if b, err := json.MarshalIndent(ut, "", "  "); err == nil {
				_ = writeFileAtomic(filepath.Join(s.env.PanelHome, "user_traffic.json"), b, 0o600)
				s.userTraffic.mu.Lock()
				s.userTraffic.load()
				s.userTraffic.mu.Unlock()
			}
		}
	}
	// 推送去重表（v1.3.30）：整体写回并重载；老备份没有这一项时不动。
	if len(p.NodePushSeen) > 0 {
		var seen map[string]int64
		if json.Unmarshal(p.NodePushSeen, &seen) == nil {
			if b, err := json.MarshalIndent(seen, "", "  "); err == nil {
				_ = writeFileAtomic(filepath.Join(s.env.PanelHome, "node_push_seen.json"), b, 0o600)
				s.nodePushSeen.mu.Lock()
				s.nodePushSeen.load()
				s.nodePushSeen.mu.Unlock()
			}
		}
	}
	// 分组（v1.3.26）：备份里有分组就整体恢复（老备份没有这一项时不动）；
	// 成员的 GroupID 在下面恢复订阅用户/合并订阅时一起带回来，恢复完再
	// 核对指向（指向的分组不在备份里就清掉，恢复为自己勾选）。
	var groupWarns []string
	var tokenWarn string
	cfgChanged := false
	if p.SubGroups != nil {
		s.cfg.SubGroups = p.SubGroups
		cfgChanged = true
	}
	// 公告（v1.4.29）：备份里有公告就整体恢复（老备份没有这一项时不动，
	// 与 sub_groups 同款口径）；恢复时不校验内容（备份是本面板自己导出的）。
	if p.Announcements != nil {
		s.cfg.Announcements = p.Announcements
		cfgChanged = true
	}
	// 远程配置（v1.4.29）：备份里有就整体恢复（老备份没有这一项时不动，
	// 与 announcements 同款口径）；恢复时不校验内容（备份是本面板自己
	// 导出的，发布时已校验过形状）。
	if p.ClientConfigJSON != nil {
		s.cfg.ClientConfigJSON = *p.ClientConfigJSON
		cfgChanged = true
	}
	// 套餐档位（v1.4.27）：快照整体替换；档位关联的分组在备份里没有
	// 时清掉该档位的分组（只影响档位默认值，不删用户、不删链接）。
	if p.Plans != nil {
		plans := make([]Plan, 0, len(p.Plans))
		for _, bp := range p.Plans {
			pl := Plan{ID: bp.ID, Name: bp.Name, GroupID: bp.GroupID,
				TrafficLimitBytes: bp.TrafficLimitBytes, TrafficLimitMode: bp.TrafficLimitMode,
				ResetMode: bp.ResetMode, ResetDay: bp.ResetDay, ExpiresDays: bp.ExpiresDays,
				Mode: bp.Mode, Note: bp.Note, CreatedAt: bp.CreatedAt}
			if pl.GroupID != "" && s.findSubGroup(pl.GroupID) == nil {
				groupWarns = append(groupWarns, "套餐档位「"+pl.Name+"」的分组在备份里没有，已清除档位的分组")
				pl.GroupID = ""
			}
			plans = append(plans, pl)
		}
		s.cfg.Plans = plans
		cfgChanged = true
	}
	// 订阅用户：快照整体替换；v1.3.29：到期与限流字段全量带回（不再变
	// 永久/不限流）。令牌：同机恢复（本机已有该 ID）优先本机现有哈希与
	// 密文（备份后轮换过的令牌不被旧备份顶掉）；跨机（本机无此 ID）只有
	// 在迁移信封已解开（mig != nil，即备份时设了口令、本次口令也对）
	// 时才用备份里的 token_hash/token_enc，旧链接继续可用；没信封的
	// 备份正文里本就不应含令牌（sanitize 脱敏），这里再兜一道：没解开
	// 信封就以空哈希插入，须经“轮换”重新生成令牌。备份快照里没有的
	// 用户会被移除（快照语义）。
	tokenRestored, tokenEmpty := 0, 0
	if p.SubUsers != nil {
		keepHash := map[string]string{}
		keepEnc := map[string]string{}
		keepPassword := map[string]string{}
		for _, u := range s.cfg.SubUsers {
			if u.TokenHash != "" {
				keepHash[u.ID] = u.TokenHash
			}
			if u.TokenEnc != "" {
				keepEnc[u.ID] = u.TokenEnc
			}
			if u.PasswordHash != "" {
				keepPassword[u.ID] = u.PasswordHash
			}
		}
		users := make([]SubUser, 0, len(p.SubUsers))
		for _, bu := range p.SubUsers {
			u := SubUser{ID: bu.ID, Name: bu.Name, Note: bu.Note, CreatedAt: bu.CreatedAt, Selection: bu.Selection, Mode: bu.Mode, GroupID: bu.GroupID,
				ExpiresAt: bu.ExpiresAt, TrafficLimitBytes: bu.TrafficLimitBytes, TrafficLimitMode: bu.TrafficLimitMode,
				ResetMode: bu.ResetMode, ResetDay: bu.ResetDay, NextResetAt: bu.NextResetAt, LastResetAt: bu.LastResetAt,
				TrafficWarned80: bu.TrafficWarned80, TrafficWarned100: bu.TrafficWarned100, Disabled: bu.Disabled, DisabledReason: bu.DisabledReason,
				NodeUUIDs: bu.NodeUUIDs, PlanID: bu.PlanID, Username: bu.Username, LinkMode: bu.LinkMode}
			// v1.4.28：哈希与密文按“对”取，绝不分开混配（本机哈希＋备份
			// 密文拼出来的对校验不过，卡片链接 404）。本机对完整时优先
			// 本机对（备份后轮换过的令牌不被旧备份顶掉）；本机只有单边时，
			// 只接受解密校验通过的另一边（令牌没轮换过时本机哈希能对上
			// 备份密文），校验不过就只留本机单边（哈希还在，旧令牌拉取
			// 仍通；卡片显示走 legacy 提示，须轮换重发）；本机两边都
			// 没有时才用备份对（须 mig 已解开），并计 tokenRestored。
			localH, hasLocalH := keepHash[bu.ID]
			localE, hasLocalE := keepEnc[bu.ID]
			switch {
			case hasLocalH && hasLocalE:
				u.TokenHash, u.TokenEnc = localH, localE
			case hasLocalH:
				u.TokenHash = localH
				if mig != nil && bu.TokenEnc != "" && s.tokenPairMatches(localH, bu.TokenEnc) {
					u.TokenEnc = bu.TokenEnc
				}
			case hasLocalE:
				u.TokenEnc = localE
				if mig != nil && bu.TokenHash != "" && s.tokenPairMatches(bu.TokenHash, localE) {
					u.TokenHash = bu.TokenHash
				}
			case mig != nil && bu.TokenHash != "" && bu.TokenEnc != "":
				u.TokenHash, u.TokenEnc = bu.TokenHash, bu.TokenEnc
				tokenRestored++
			case mig != nil && bu.TokenHash != "":
				// 备份只有哈希没有密文：单留哈希（旧令牌拉取仍通，卡片
				// 显示走 legacy 提示）；单边不存在混配问题。
				u.TokenHash = bu.TokenHash
			case mig != nil && bu.TokenEnc != "":
				// 备份只有密文没有哈希：单留密文（与旧行为一致）。
				u.TokenEnc = bu.TokenEnc
			}
			// 登录密码哈希（v1.4.27）：同机恢复优先本机现有哈希；跨机
			// 只有迁移信封已解开时才用备份里的；都没拿到时该用户须重设
			// 密码才能登录（备份里没口令时正文里本就不含它）。
			if h, ok := keepPassword[bu.ID]; ok {
				u.PasswordHash = h
			} else if mig != nil && bu.PasswordHash != "" {
				u.PasswordHash = bu.PasswordHash
			}
			if u.TokenHash == "" {
				tokenEmpty++
			}
			users = append(users, u)
		}
		s.cfg.SubUsers = users
		cfgChanged = true
	}
	if p.MergedSubs != nil {
		keepHash := map[string]string{}
		keepEnc := map[string]string{}
		for _, m := range s.cfg.MergedSubs {
			if m.TokenHash != "" {
				keepHash[m.ID] = m.TokenHash
			}
			if m.TokenEnc != "" {
				keepEnc[m.ID] = m.TokenEnc
			}
		}
		subs := make([]MergedSub, 0, len(p.MergedSubs))
		for _, bm := range p.MergedSubs {
			m := MergedSub{ID: bm.ID, Name: bm.Name, CreatedAt: bm.CreatedAt, Selection: bm.Selection, Mode: bm.Mode, GroupID: bm.GroupID,
				ExpiresAt: bm.ExpiresAt, TrafficLimitBytes: bm.TrafficLimitBytes, TrafficLimitMode: bm.TrafficLimitMode,
				ResetMode: bm.ResetMode, ResetDay: bm.ResetDay, NextResetAt: bm.NextResetAt, LastResetAt: bm.LastResetAt,
				TrafficWarned80: bm.TrafficWarned80, TrafficWarned100: bm.TrafficWarned100, Disabled: bm.Disabled, DisabledReason: bm.DisabledReason,
				NodeUUIDs: bm.NodeUUIDs}
			// v1.4.28：与 SubUser 同款按“对”取（见上），绝不把本机哈希与
			// 备份密文分开混配；合并订阅已退场，这里只保记录本身不错配。
			localH, hasLocalH := keepHash[bm.ID]
			localE, hasLocalE := keepEnc[bm.ID]
			switch {
			case hasLocalH && hasLocalE:
				m.TokenHash, m.TokenEnc = localH, localE
			case hasLocalH:
				m.TokenHash = localH
				if mig != nil && bm.TokenEnc != "" && s.tokenPairMatches(localH, bm.TokenEnc) {
					m.TokenEnc = bm.TokenEnc
				}
			case hasLocalE:
				m.TokenEnc = localE
				if mig != nil && bm.TokenHash != "" && s.tokenPairMatches(bm.TokenHash, localE) {
					m.TokenHash = bm.TokenHash
				}
			case mig != nil && bm.TokenHash != "" && bm.TokenEnc != "":
				m.TokenHash, m.TokenEnc = bm.TokenHash, bm.TokenEnc
				tokenRestored++
			case mig != nil && bm.TokenHash != "":
				m.TokenHash = bm.TokenHash
			case mig != nil && bm.TokenEnc != "":
				m.TokenEnc = bm.TokenEnc
			}
			if m.TokenHash == "" {
				tokenEmpty++
			}
			subs = append(subs, m)
		}
		s.cfg.MergedSubs = subs
		cfgChanged = true
	}
	if p.IndMergedSubs != nil {
		keepHash := map[string]string{}
		keepEnc := map[string]string{}
		for _, m := range s.cfg.IndependentMergedSubs {
			if m.TokenHash != "" {
				keepHash[m.ID] = m.TokenHash
			}
			if m.TokenEnc != "" {
				keepEnc[m.ID] = m.TokenEnc
			}
		}
		subs := make([]IndependentMergedSub, 0, len(p.IndMergedSubs))
		for _, bm := range p.IndMergedSubs {
			m := IndependentMergedSub{ID: bm.ID, Name: bm.Name, CreatedAt: bm.CreatedAt, Selection: bm.Selection, Mode: bm.Mode,
				ExpiresAt: bm.ExpiresAt, TrafficLimitBytes: bm.TrafficLimitBytes, TrafficLimitMode: bm.TrafficLimitMode,
				ResetMode: bm.ResetMode, ResetDay: bm.ResetDay, NextResetAt: bm.NextResetAt, LastResetAt: bm.LastResetAt,
				TrafficWarned80: bm.TrafficWarned80, TrafficWarned100: bm.TrafficWarned100, Disabled: bm.Disabled, DisabledReason: bm.DisabledReason,
				NodeUUIDs: bm.NodeUUIDs, BackendScope: bm.BackendScope, BackendTargets: bm.BackendTargets}
			// v1.4.28：与旧合并订阅同款按“对”取（见上），绝不把本机哈希与
			// 备份密文分开混配；成对校验用同一密钥文件与机制。
			localH, hasLocalH := keepHash[bm.ID]
			localE, hasLocalE := keepEnc[bm.ID]
			switch {
			case hasLocalH && hasLocalE:
				m.TokenHash, m.TokenEnc = localH, localE
			case hasLocalH:
				m.TokenHash = localH
				if mig != nil && bm.TokenEnc != "" && s.tokenPairMatches(localH, bm.TokenEnc) {
					m.TokenEnc = bm.TokenEnc
				}
			case hasLocalE:
				m.TokenEnc = localE
				if mig != nil && bm.TokenHash != "" && s.tokenPairMatches(bm.TokenHash, localE) {
					m.TokenHash = bm.TokenHash
				}
			case mig != nil && bm.TokenHash != "" && bm.TokenEnc != "":
				m.TokenHash, m.TokenEnc = bm.TokenHash, bm.TokenEnc
				tokenRestored++
			case mig != nil && bm.TokenHash != "":
				m.TokenHash = bm.TokenHash
			case mig != nil && bm.TokenEnc != "":
				m.TokenEnc = bm.TokenEnc
			}
			if m.TokenHash == "" {
				tokenEmpty++
			}
			subs = append(subs, m)
		}
		s.cfg.IndependentMergedSubs = subs
		cfgChanged = true
	}
	if tokenRestored > 0 {
		o.addNote(fmt.Sprintf("订阅令牌已随备份恢复 %d 条，旧链接继续可用", tokenRestored))
	}
	if tokenEmpty > 0 {
		tokenWarn = fmt.Sprintf("备份里没有令牌，%d 条订阅的旧链接已失效，请逐个轮换后重新发链接", tokenEmpty)
	}
	// 分组指向核对（v1.3.26）：成员的 GroupID 指向的分组不在恢复后的分组
	// 列表里时，清掉该成员的分组（恢复为自己勾选），并在导入结果里写明。
	// 档位指向核对（v1.4.27）：同理，PlanID 指向的档位不在恢复后的档位
	// 列表里时清掉该用户的档位（只清档位挂接，不动限流/到期现值）。
	if p.SubGroups != nil || p.SubUsers != nil || p.MergedSubs != nil || p.Plans != nil {
		for i := range s.cfg.SubUsers {
			if s.cfg.SubUsers[i].PlanID != "" && s.findPlan(s.cfg.SubUsers[i].PlanID) == nil {
				groupWarns = append(groupWarns, "订阅用户「"+s.cfg.SubUsers[i].Name+"」的套餐档位在备份里没有，已取消档位")
				s.cfg.SubUsers[i].PlanID = ""
				cfgChanged = true
			}
			if s.cfg.SubUsers[i].GroupID != "" && s.findSubGroup(s.cfg.SubUsers[i].GroupID) == nil {
				groupWarns = append(groupWarns, "订阅用户「"+s.cfg.SubUsers[i].Name+"」的分组在备份里没有，已恢复为自己勾选")
				s.cfg.SubUsers[i].GroupID = ""
				cfgChanged = true
			}
		}
		for i := range s.cfg.MergedSubs {
			if s.cfg.MergedSubs[i].GroupID != "" && s.findSubGroup(s.cfg.MergedSubs[i].GroupID) == nil {
				name := s.cfg.MergedSubs[i].Name
				if name == "" {
					name = "未命名合并订阅"
				}
				groupWarns = append(groupWarns, "合并订阅「"+name+"」的分组在备份里没有，已恢复为自己勾选")
				s.cfg.MergedSubs[i].GroupID = ""
				cfgChanged = true
			}
		}
	}
	// 面板设置（v1.3.29）：白名单里的非敏感字段，文件里有才恢复，没有不动；
	// 敏感字段（密码哈希、TOTP、Telegram/Cloudflare Token、远程面板明文令牌、
	// 远程备份密钥）不走这里，走下面的迁移信封。
	if pc := p.PanelConfig; pc != nil {
		if pc.Username != nil {
			s.cfg.Username = *pc.Username
			cfgChanged = true
		}
		if pc.Port != nil {
			s.cfg.Port = *pc.Port
			cfgChanged = true
			o.addNote("面板端口已恢复，重启面板后生效")
		}
		if pc.HTTPSEnabled != nil {
			s.cfg.HTTPSEnabled = *pc.HTTPSEnabled
			cfgChanged = true
			o.addNote("面板 HTTPS 设置已恢复，重启面板后生效")
		}
		if pc.HTTPSDomain != nil {
			s.cfg.HTTPSDomain = *pc.HTTPSDomain
			cfgChanged = true
		}
		if pc.IPWhitelist != nil {
			s.cfg.IPWhitelist = pc.IPWhitelist
			cfgChanged = true
		}
		if pc.Telegram != nil {
			if pc.Telegram.Enabled != nil {
				s.cfg.Telegram.Enabled = *pc.Telegram.Enabled
				cfgChanged = true
			}
			if pc.Telegram.ChatID != nil {
				s.cfg.Telegram.ChatID = *pc.Telegram.ChatID
				cfgChanged = true
			}
		}
		if pc.Monitor != nil {
			s.cfg.Monitor = *pc.Monitor
			cfgChanged = true
		}
		if pc.Latency != nil {
			s.cfg.Latency = *pc.Latency
			cfgChanged = true
		}
		if pc.PanelName != nil {
			s.cfg.PanelName = *pc.PanelName
			cfgChanged = true
		}
		if pc.LoginNotice != nil {
			s.cfg.LoginNotice = *pc.LoginNotice
			cfgChanged = true
		}
		if pc.LoginSignature != nil {
			s.cfg.LoginSignature = *pc.LoginSignature
			cfgChanged = true
		}
		if pc.SubPath != nil {
			s.cfg.SubPath = *pc.SubPath
			cfgChanged = true
			o.addNote(fmt.Sprintf("订阅路径已恢复为 /%s（以备份为准），旧路径链接以此为准", strings.Trim(*pc.SubPath, "/")))
		}
		if pc.SubPublicDomain != nil {
			s.cfg.SubPublicDomain = *pc.SubPublicDomain
			cfgChanged = true
		}
		if pc.SubFileName != nil {
			s.cfg.SubFileName = *pc.SubFileName
			cfgChanged = true
		}
		if pc.WorkerOrigin != nil {
			s.cfg.WorkerOrigin = *pc.WorkerOrigin
			cfgChanged = true
		}
		if pc.ManagerOnly != nil {
			s.cfg.ManagerOnly = *pc.ManagerOnly
			cfgChanged = true
		}
		if pc.LoginAlert != nil {
			s.cfg.LoginAlert = *pc.LoginAlert
			cfgChanged = true
		}
		if pc.Backup != nil {
			nb := *pc.Backup
			nb.applyDefaults()
			if err := nb.validate(); err == nil {
				s.cfg.Backup = nb
				cfgChanged = true
			}
		}
	}
	// 通用订阅令牌（v1.3.29）：备份里有才走；本机已有令牌时优先本机
	// （同机恢复不顶掉备份后轮换过的令牌），本机没有时用备份里的。
	if p.SubToken != nil && s.cfg.SubTokenHash == "" && p.SubToken.Hash != "" {
		s.cfg.SubTokenHash = p.SubToken.Hash
		s.cfg.SubTokenEnc = p.SubToken.Enc
		s.cfg.SubTokenMode = p.SubToken.Mode
		s.cfg.SubTokenExpiresAt = p.SubToken.ExpiresAt
		s.cfg.SubTokenCreatedAt = p.SubToken.CreatedAt
		cfgChanged = true
		o.addNote("通用订阅令牌已随备份恢复，旧链接继续可用")
	}
	// 按人转发与端口段（v1.3.29）：随备份恢复，恢复后按订阅逐条核对。
	if p.UserForwards != nil {
		s.cfg.UserForwards = p.UserForwards
		cfgChanged = true
	}
	if p.UserPortStart > 0 {
		s.cfg.UserPortStart = p.UserPortStart
		cfgChanged = true
	}
	if p.UserPortEnd > 0 {
		s.cfg.UserPortEnd = p.UserPortEnd
		cfgChanged = true
	}
	// nodes.txt（v1.3.29）：写回 KIT_DIR，订阅链接本源随之回来，
	// 手动 twin 分组不再永久丢失。
	if strings.TrimSpace(p.NodesTxt) != "" {
		if err := writeFileAtomic(s.nodesFilePath(), []byte(p.NodesTxt), 0o644); err == nil {
			o.addNote("订阅链接本源 nodes.txt 已恢复")
		}
	}
	// Logo（v1.3.29）：文件写回 PANEL_HOME/logo（600），MIME 记回配置。
	if strings.TrimSpace(p.LogoB64) != "" {
		if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(p.LogoB64)); err == nil && len(raw) > 0 && len(raw) <= logoMaxBytes {
			if err := writeFileAtomic(s.logoPath(), raw, 0o600); err == nil {
				s.cfg.LogoMime = p.LogoMime
				if s.cfg.LogoMime == "" {
					s.cfg.LogoMime = http.DetectContentType(raw)
				}
				cfgChanged = true
			}
		}
	}
	// 面板证书（v1.3.29）：cert.pem/key.pem 写回 certs/；acme-key.pem
	// 不备份，续签时重建。格式不对（不是 PEM）时跳过，不写坏文件。
	if strings.TrimSpace(p.CertPem) != "" && strings.TrimSpace(p.CertKeyPem) != "" {
		if strings.Contains(p.CertPem, "BEGIN CERTIFICATE") && strings.Contains(p.CertKeyPem, "BEGIN") {
			_ = os.MkdirAll(filepath.Join(s.env.PanelHome, "certs"), 0o700)
			if err := writeFileAtomic(filepath.Join(s.env.PanelHome, "certs", "cert.pem"), []byte(p.CertPem), 0o644); err == nil {
				if err := writeFileAtomic(filepath.Join(s.env.PanelHome, "certs", "key.pem"), []byte(p.CertKeyPem), 0o600); err == nil {
					o.addNote("面板证书已随备份恢复")
				}
			}
		}
	}
	// 迁移信封（v1.3.29）：已在函数开头解开（口令错整单失败）；这里才
	// 写盘。sub_token.key 写盘后要清掉内存里的缓存密钥，否则后续
	// encrypt/decrypt 还用旧缓存，恢复回来的密文解不开。
	if mig != nil {
		if mig.SubTokenKey != "" {
			if kb, err := base64.StdEncoding.DecodeString(strings.TrimSpace(mig.SubTokenKey)); err == nil && len(kb) == 32 {
				if err := writeFileAtomic(filepath.Join(s.env.PanelHome, subTokenKeyFile), kb, 0o600); err == nil {
					s.subKeyMu.Lock()
					s.subKey = append([]byte(nil), kb...)
					s.subKeyMu.Unlock()
				}
			}
		}
		if mig.PasswordHash != "" {
			s.cfg.PasswordHash = mig.PasswordHash
			cfgChanged = true
		}
		if mig.TOTPSecret != "" {
			s.cfg.TOTPSecret = mig.TOTPSecret
			cfgChanged = true
		}
		if mig.TOTPBackupCodes != nil {
			s.cfg.TOTPBackupCodes = mig.TOTPBackupCodes
			cfgChanged = true
		}
		if mig.TelegramBotToken != "" {
			s.cfg.Telegram.BotToken = mig.TelegramBotToken
			cfgChanged = true
		}
		if mig.CloudflareToken != "" {
			s.cfg.CloudflareToken = mig.CloudflareToken
			cfgChanged = true
		}
		if mig.Remotes != nil {
			s.cfg.Remotes = mig.Remotes
			cfgChanged = true
		}
		if mig.RemoteTokenHash != "" {
			s.cfg.RemoteTokenHash = mig.RemoteTokenHash
			cfgChanged = true
		}
		if mig.RemoteBackup.PassphraseSet {
			s.cfg.RemoteBackup = mig.RemoteBackup
			cfgChanged = true
		}
		o.addNote("迁移信封已恢复：登录密码、TOTP、Telegram/Cloudflare 令牌、集中管理登记与异地备份配置已随备份恢复")
	}
	if cfgChanged {
		_ = s.saveConfig()
	}
	// 按人转发对齐（v1.3.29）：先清掉 config.toml 里恢复出来的无主按人段
	// （applyUserForwards/reconcile 都删不掉它们），再按恢复后的订阅限流
	// 重建合法段；没有限流订阅时只清孤儿、不新建。
	if p.UserForwards != nil || p.SubUsers != nil || p.MergedSubs != nil || p.IndMergedSubs != nil {
		if n := s.removeOrphanUserEndpoints(); n > 0 {
			o.addNote(fmt.Sprintf("已清理 %d 个无主按人转发段", n))
		}
		s.reconcileAllUserForwards()
	}
	_ = s.regenProxies()
	// 分组指向核对与令牌缺失的结果都带回给调用方（导入已生效，只是提醒）。
	groupWarn := strings.Join(groupWarns, "；")
	allWarn := groupWarn
	if tokenWarn != "" {
		if allWarn != "" {
			allWarn += "；"
		}
		allWarn += tokenWarn
	}
	if err := s.restartRealm(); err != nil {
		w := o.restartWarnPrefix + err.Error()
		if allWarn != "" {
			w += "；" + allWarn
		}
		return w, nil
	}
	if o.verifyWarnPrefix != "" {
		if rc, _, err := s.readRelayConf(); err == nil {
			var ports []PortSpec
			for _, l := range rc.Landings {
				ports = append(ports, l.Ports...)
			}
			if err := verifyPorts(ports); err != nil {
				w := o.verifyWarnPrefix + err.Error()
				if allWarn != "" {
					w += "；" + allWarn
				}
				return w, nil
			}
		}
	}
	// v1.4.28：恢复/导入后自检令牌成对性，失配只写服务端日志（见
	// checkSubTokenPairs），不拦恢复——数据不一致靠轮换修复。
	s.checkSubTokenPairs()
	s.audit(o.user, o.auditAction, "", "成功", o.auditDetail)
	return allWarn, nil
}

// restartForSubPathChange 恢复/导入后订阅路径变了：订阅路由是面板启动时
// 按旧路径注册的（handlers.go 注释：改路径要重启面板），不重启新路径
// 不生效、旧订阅链接 404；与设置页改路径同一做法：先回包，再异步重启
// 自身。路径没变返回 ""（不用重启）；变了返回写进恢复结果里的提示。
// 只在 applyImportPayload 成功后由调用方调（失败时配置没动，不重启）。
func (s *Server) restartForSubPathChange(oldPath string) string {
	if s.subPath() == oldPath {
		return ""
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		restartPanelFunc(s.env)
	}()
	return "订阅路径已恢复为 /" + s.subPath() + "，面板正在重启，重启后旧订阅链接生效"
}

// ---------- 自动备份设置与定时备份接口 ----------

func (s *Server) handleBackupSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, okResp(map[string]any{
		"backup": s.cfg.Backup, "last_backup": s.lastScheduledBackup(),
		"count": len(s.listScheduledBackups()),
	}))
}

func (s *Server) handleBackupSettingsPost(w http.ResponseWriter, r *http.Request) {
	// 以当前值为底合并：只发 enabled 时 time/keep 不丢，避免“只关开关”
	// 却被 applyDefaults 误判成从没存过、又把开关复活。
	req := s.cfg.Backup
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	req.applyDefaults()
	if err := req.validate(); err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	s.cfg.Backup = req
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "修改自动备份设置", "", "成功", fmt.Sprintf("启用=%v 时间=%s 保留=%d 份", req.Enabled, req.Time, req.Keep))
	writeJSON(w, 200, okResp(map[string]any{"backup": s.cfg.Backup}))
}

func (s *Server) handleScheduledBackupsList(w http.ResponseWriter, r *http.Request) {
	list := s.listScheduledBackups()
	if list == nil {
		list = []ScheduledBackupMeta{}
	}
	writeJSON(w, 200, okResp(map[string]any{"backups": list, "last_backup": s.lastScheduledBackup()}))
}

func (s *Server) handleScheduledBackupNow(w http.ResponseWriter, r *http.Request) {
	id, err := s.runScheduledBackup("手动立即备份")
	if err != nil {
		writeJSON(w, 500, errResp("备份失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "立即备份", id, "成功", "定时备份")
	// 手动立即备份也一样传异地（异步，不阻塞返回）。
	s.uploadBackupToRemoteAsync(id)
	writeJSON(w, 200, okResp(map[string]any{"id": id}))
}

func (s *Server) handleScheduledBackupRestore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	data, err := s.readScheduledBackup(id)
	if err != nil {
		writeJSON(w, 404, errResp("没有这份备份"))
		return
	}
	var imp importPayload
	if err := json.Unmarshal(data, &imp); err != nil {
		writeJSON(w, 400, errResp("备份文件格式不对"))
		return
	}
	if ferr := s.validateImportPayload(imp); ferr != nil {
		writeJSON(w, ferr.status, errResp(ferr.msg))
		return
	}
	// v1.3.29：恢复请求体里可带 passphrase（有迁移信封时用它解信封；
	// 没信封时忽略；空体也行，老客户端照旧）。
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(bytes.TrimSpace(body)) > 0 {
		_ = json.Unmarshal(body, &req)
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	oldSubPath := s.subPath()
	var notes []string
	warn, ferr := s.applyImportPayload(imp, importApplyOpts{
		preBackupLabel: "恢复定时备份前自动备份", auditAction: "恢复定时备份",
		auditDetail:       "订阅用户按 ID 保留现有令牌，不在备份里的用户会被移除；有迁移信封时密码/TOTP/令牌密钥随备份恢复",
		restartWarnPrefix: "配置已恢复，但重启 realm 失败：",
		verifyWarnPrefix:  "恢复后端口校验失败：", user: s.currentUser(r),
		migrationPassphrase: req.Passphrase, notes: &notes,
	})
	if ferr != nil {
		writeJSON(w, ferr.status, errResp(ferr.msg))
		return
	}
	if msg := s.restartForSubPathChange(oldSubPath); msg != "" {
		notes = append(notes, msg)
		if warn != "" {
			warn += "；" + msg
		}
	}
	if warn != "" {
		writeJSON(w, 200, errResp(warn))
		return
	}
	writeJSON(w, 200, okResp(map[string]any{"notes": notes}))
}

func (s *Server) handleScheduledBackupDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deleteScheduledBackup(id); err != nil {
		writeJSON(w, 404, errResp("没有这份备份"))
		return
	}
	s.audit(s.currentUser(r), "删除定时备份", id, "成功", "")
	writeJSON(w, 200, okResp(nil))
}
