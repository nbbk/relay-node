package main

// ---------- 自动定时备份（v1.3.6） ----------
// 每天一次把面板整机配置备份到面板本机 scheduled-backups/ 目录，带轮转
// （默认保留最近 7 份，防爆盘）。备份内容与手动导出同口径（relay.conf、
// config.toml、state.json、脱敏后的 panel_config），外加流量库与订阅用户
// （只留 id/name/note/selection，不含 token 哈希）；同样不含密码哈希与
// Telegram token。恢复走导入流程（applyImportPayload），不另造格式。

import (
	"encoding/json"
	"fmt"
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
	ID    string `json:"id"`    // 文件名（不含 .json），如 backup-20261119-030001
	TS    string `json:"ts"`    // RFC3339
	Label string `json:"label"` // 自动定时备份 / 手动立即备份
	Size  int64  `json:"size"`
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
// 流量库原文 + 脱敏的订阅用户（只留 id/name/note/selection，不含 token 哈希）。
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
	out["sub_users"] = sanitizeSubUsers(s.cfg.SubUsers)
	out["merged_subs"] = sanitizeMergedSubs(s.cfg.MergedSubs)
	return out
}

// subUserBackup / mergedSubBackup 备份文件里的订阅用户（脱敏：不含 token 哈希与 token 密文）。
// Mode（v1.3.17）是内容范围（both|relay|direct），跟着记录走，恢复时不丢；
// 老备份里没有该字段时按空处理（= 全部）。
type subUserBackup struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Note      string         `json:"note,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	Selection []SubSelection `json:"selection,omitempty"`
	Mode      string         `json:"mode,omitempty"`
}

type mergedSubBackup struct {
	ID        string         `json:"id"`
	Name      string         `json:"name,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	Selection []SubSelection `json:"selection"`
	Mode      string         `json:"mode,omitempty"`
}

func sanitizeSubUsers(users []SubUser) []subUserBackup {
	out := make([]subUserBackup, 0, len(users))
	for _, u := range users {
		out = append(out, subUserBackup{ID: u.ID, Name: u.Name, Note: u.Note, CreatedAt: u.CreatedAt, Selection: u.Selection, Mode: u.Mode})
	}
	return out
}

func sanitizeMergedSubs(subs []MergedSub) []mergedSubBackup {
	out := make([]mergedSubBackup, 0, len(subs))
	for _, m := range subs {
		out = append(out, mergedSubBackup{ID: m.ID, Name: m.Name, CreatedAt: m.CreatedAt, Selection: m.Selection, Mode: m.Mode})
	}
	return out
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
				Label string `json:"label"`
			}
			if json.Unmarshal(b, &head) == nil {
				m.Label = head.Label
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

// scheduledBackupDoneToday 当天（YYYYMMDD）是否已有定时备份。
func (s *Server) scheduledBackupDoneToday(yyyymmdd string) bool {
	for _, b := range s.listScheduledBackups() {
		if strings.HasPrefix(b.ID, "backup-"+yyyymmdd+"-") {
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
func (s *Server) exportData() map[string]any {
	relayConf, _ := os.ReadFile(filepath.Join(s.env.RealmDir, "relay.conf"))
	toml, _ := os.ReadFile(filepath.Join(s.env.RealmDir, "config.toml"))
	state, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "state.json"))
	var stateJSON any
	_ = json.Unmarshal(state, &stateJSON)
	// 脱敏：不含密码哈希和 telegram token
	panelCfg := map[string]any{
		"username": s.cfg.Username, "port": s.cfg.Port,
		"https_enabled": s.cfg.HTTPSEnabled, "https_domain": s.cfg.HTTPSDomain,
		"ip_whitelist": s.cfg.IPWhitelist,
		"telegram":     map[string]any{"enabled": s.cfg.Telegram.Enabled, "chat_id": s.cfg.Telegram.ChatID},
		"monitor":      s.cfg.Monitor,
		"backup":       s.cfg.Backup,
		"version":      Version,
	}
	return map[string]any{
		"version": Version, "exported_at": time.Now().UTC().Format(time.RFC3339),
		"relay_conf": string(relayConf), "config_toml": string(toml),
		"state": stateJSON, "panel_config": panelCfg,
	}
}

// ---------- 导入 / 恢复的共用落地逻辑 ----------

// importPayload 导入文件（手动导出与定时备份共用格式；定时备份多了
// kind/label/traffic/sub_users/merged_subs，手动导出没有这些键时不走那几步）。
type importPayload struct {
	RelayConf   string            `json:"relay_conf"`
	ConfigToml  string            `json:"config_toml"`
	State       json.RawMessage   `json:"state"`
	Traffic     json.RawMessage   `json:"traffic"`
	SubUsers    []subUserBackup   `json:"sub_users"`
	MergedSubs  []mergedSubBackup `json:"merged_subs"`
	PanelConfig *struct {
		Backup *BackupCfg `json:"backup"`
	} `json:"panel_config"`
}

type importFailure struct {
	status int
	msg    string
}

func (e *importFailure) Error() string { return e.msg }

type importApplyOpts struct {
	preBackupLabel    string // 恢复前自动备份的 label
	auditAction       string // 审计动作，如“导入配置”/“恢复定时备份”
	auditDetail       string
	restartWarnPrefix string // 写盘后重启失败的文案前缀
	verifyWarnPrefix  string // "" = 不做端口校验；否则为端口校验失败的文案前缀
	user              string
}

// validateImportPayload 校验导入文件（只读，不写盘；调用方先调它再拿写锁）。
func (s *Server) validateImportPayload(p importPayload) *importFailure {
	if rc := parseRelayConf([]byte(p.RelayConf)); len(rc.Landings) == 0 && !strings.Contains(p.RelayConf, "LANDING=") {
		return &importFailure{400, "relay_conf 里没有落地"}
	}
	if err := validateConfigToml([]byte(p.ConfigToml)); err != nil {
		return &importFailure{400, "config_toml 校验失败：" + err.Error()}
	}
	return nil
}

// applyImportPayload 导入/恢复的共用落地逻辑（调用方须先拿 op 锁）：
// 备份当前 → 写 relay/config/state → 恢复流量库与订阅用户（文件里有才走）
// → 恢复备份排程 → regenProxies → 重启 realm → 可选端口校验。
// 写盘前的硬失败返回 *importFailure；写盘后重启/校验失败只返回 warn
// （配置已生效），调用方按 200 + errResp 处理。成功时记审计。
func (s *Server) applyImportPayload(p importPayload, o importApplyOpts) (warn string, err *importFailure) {
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
	// 订阅用户：快照整体替换；同 ID 的保留本机现有 token 哈希与 token 密文
	// （同面板恢复不丢链接、恢复后仍能重新复制），备份里没有的（恢复后
	// 新增的）以空哈希/空密文插入，须经“轮换”重新生成令牌；备份快照里
	// 没有的用户会被移除（快照语义）。
	cfgChanged := false
	if p.SubUsers != nil {
		keepHash := map[string]string{}
		keepEnc := map[string]string{}
		for _, u := range s.cfg.SubUsers {
			if u.TokenHash != "" {
				keepHash[u.ID] = u.TokenHash
			}
			if u.TokenEnc != "" {
				keepEnc[u.ID] = u.TokenEnc
			}
		}
		users := make([]SubUser, 0, len(p.SubUsers))
		for _, bu := range p.SubUsers {
			u := SubUser{ID: bu.ID, Name: bu.Name, Note: bu.Note, CreatedAt: bu.CreatedAt, Selection: bu.Selection, Mode: bu.Mode}
			if h, ok := keepHash[bu.ID]; ok {
				u.TokenHash = h
			}
			if e, ok := keepEnc[bu.ID]; ok {
				u.TokenEnc = e
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
			m := MergedSub{ID: bm.ID, Name: bm.Name, CreatedAt: bm.CreatedAt, Selection: bm.Selection, Mode: bm.Mode}
			if h, ok := keepHash[bm.ID]; ok {
				m.TokenHash = h
			}
			if e, ok := keepEnc[bm.ID]; ok {
				m.TokenEnc = e
			}
			subs = append(subs, m)
		}
		s.cfg.MergedSubs = subs
		cfgChanged = true
	}
	// 备份排程：文件里有就恢复（老导出没有这一项时不动）。
	if p.PanelConfig != nil && p.PanelConfig.Backup != nil {
		nb := *p.PanelConfig.Backup
		nb.applyDefaults()
		if err := nb.validate(); err == nil {
			s.cfg.Backup = nb
			cfgChanged = true
		}
	}
	if cfgChanged {
		_ = s.saveConfig()
	}
	_ = s.regenProxies()
	if err := s.restartRealm(); err != nil {
		return o.restartWarnPrefix + err.Error(), nil
	}
	if o.verifyWarnPrefix != "" {
		if rc, _, err := s.readRelayConf(); err == nil {
			var ports []PortSpec
			for _, l := range rc.Landings {
				ports = append(ports, l.Ports...)
			}
			if err := verifyPorts(ports); err != nil {
				return o.verifyWarnPrefix + err.Error(), nil
			}
		}
	}
	s.audit(o.user, o.auditAction, "", "成功", o.auditDetail)
	return "", nil
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
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	warn, ferr := s.applyImportPayload(imp, importApplyOpts{
		preBackupLabel: "恢复定时备份前自动备份", auditAction: "恢复定时备份",
		auditDetail:       "密码哈希与 Telegram token 保持本机原值；订阅用户按 ID 保留现有令牌，不在备份里的用户会被移除，新用户需重新生成令牌",
		restartWarnPrefix: "配置已恢复，但重启 realm 失败：",
		verifyWarnPrefix:  "恢复后端口校验失败：", user: s.currentUser(r),
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

func (s *Server) handleScheduledBackupDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deleteScheduledBackup(id); err != nil {
		writeJSON(w, 404, errResp("没有这份备份"))
		return
	}
	s.audit(s.currentUser(r), "删除定时备份", id, "成功", "")
	writeJSON(w, 200, okResp(nil))
}
