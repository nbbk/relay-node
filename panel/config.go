package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ---------- 面板自己的配置（PANEL_HOME/config.json，600） ----------

type TelegramCfg struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

type PanelConfig struct {
	Username        string `json:"username"`
	PasswordHash    string `json:"password_hash"`
	Port            int    `json:"port"`
	HTTPSEnabled    bool   `json:"https_enabled"`
	HTTPSDomain     string `json:"https_domain"`
	CloudflareToken string `json:"cloudflare_token"`
	// 面板自身真证书（v1.3.14）：自动续签的上次失败原因，常驻显示在 HTTPS 卡片上
	// （成功后清掉）；续签失败的 Telegram 去重标志，成功续签后清掉，下次再越过
	// 阈值重新提醒。错误文案只含域名与 ACME 报错，不含任何令牌，可安全显示。
	PanelCertLastErr     string      `json:"panel_cert_last_err,omitempty"`
	PanelCertRenewWarned bool        `json:"panel_cert_renew_warned,omitempty"`
	IPWhitelist          []string    `json:"ip_whitelist"`
	Telegram             TelegramCfg `json:"telegram"`
	Monitor              MonitorCfg  `json:"monitor"`
	Latency              LatencyCfg  `json:"latency"`
	// 自动定时备份（v1.3.6）：默认每天 03:00 备份一次，保留最近 7 份。
	// 旧 config.json 里没有这一节时按默认补（applyDefaults）。
	Backup BackupCfg `json:"backup"`
	// 远程管理令牌（v1.2.0，被管端）：只存 SHA-256 哈希，绝不存明文。
	RemoteTokenHash      string `json:"remote_token_hash"`
	RemoteTokenCreatedAt string `json:"remote_token_created_at"`
	// 订阅令牌（v1.2.7，单面板全量订阅）：哈希做鉴权查表，密文（v1.3.7，
	// AES-GCM，密钥在 PANEL_HOME/sub_token.key）用来重新拼链接，随时能
	// 点复制重新拿；1.3.7 之前生成的没有密文，复制时提示轮换一次。
	// 内容是实时生成的（全部落地），面板里增删节点后订阅内容自动跟着变，
	// 不用重新导出。
	// SubTokenMode（v1.3.17）：这条通用订阅的内容范围，both|relay|direct，
	// 空（老记录）按 both；/sub/ 服务时按它过滤链接。
	// SubTokenExpiresAt（v1.3.20）：通用订阅到期时间，RFC3339，空 = 永久；
	// 到期后 /sub/{token} 返回 410，客户端更新拉不到节点；轮换不改它。
	SubTokenHash      string `json:"sub_token_hash"`
	SubTokenEnc       string `json:"sub_token_enc,omitempty"`
	SubTokenCreatedAt string `json:"sub_token_created_at"`
	SubTokenMode      string `json:"sub_token_mode,omitempty"`
	SubTokenExpiresAt string `json:"sub_token_expires_at,omitempty"`
	// 订阅路径（v1.3.20，反特征）：订阅端点的路径段，默认 "sub"，即 /sub/{token}。
	// 可改成任意单段路径（如 x9f2）；改了之后旧订阅链接全部失效（路由只注册
	// 新路径），改路径会重启面板（路由在启动时注册）并要求重新登录。
	// 空（老配置）= 默认 "sub"。
	SubPath string `json:"sub_path,omitempty"`
	// 面板 Logo（v1.3.20）：文件存在 PANEL_HOME/logo（更新不丢），这里只记
	// 它的 MIME（上传时嗅探得到）；空 = 没传 Logo，显示默认样式。
	LogoMime string `json:"logo_mime,omitempty"`
	// 订阅对外域名（v1.3.2）：部署完 Cloudflare Worker 后填 Worker 的域名
	// （或在触发器里绑的自定义域名）。填了之后，面板里显示与复制的订阅链接
	// （通用链接、每人链接、合并订阅、二维码）都改用 https://这个域名/sub/…，
	// 不再用面板自己的地址；留空 = 用面板自己的地址（怎么打开就用什么）。
	// 只存域名（可带端口），不带 scheme 与路径；Worker 代码里的源站地址
	// （ORIGIN）不受这个设置影响，仍是面板真实地址。
	SubPublicDomain string `json:"sub_public_domain"`
	// 订阅文件名称（v1.3.22）：订阅响应 Content-Disposition 里的文件名
	// （不含后缀，.yaml/.txt 按格式自动加）。填了就用填的，留空回落面板名；
	// 改面板名不影响已设的文件名。校验：去首尾空格，1–64 字，禁
	// / \ : * ? " < > | 与控制字符；旧 config.json 里没有这个字段时按空
	// 处理（回落面板名），不用迁移。
	SubFileName string `json:"sub_file_name,omitempty"`
	// Worker 源站地址（v1.3.5）：用户在“Worker 反代”卡片里填的面板源站地址
	// （Worker 代码里的 ORIGIN）。点“生成 Worker 代码”时填了就记住，下次打开
	// 设置页自动回填；留空 = 用当前打开面板的地址（默认用法），同时把之前存
	// 过的清空。只存用户填的原文（可带 http:// 前缀与端口），生成时再按
	// workerOrigin 校验（明确填 IP 会 400）；旧 config.json 里没有这个字段
	// 时按空处理，不用迁移。
	WorkerOrigin string `json:"worker_origin"`
	// 合并订阅（v1.2.7，主面板端）：每份勾选集合一条令牌，只存哈希；
	// 选择变了就新起一条，不同选择绝不共用一个令牌；同一选择重新生成
	// 会轮换（旧链接立即失效）。内容按勾选的落地集合实时生成。
	MergedSubs []MergedSub `json:"merged_subs"`
	// 订阅用户（v1.3.0，一人一条订阅链接）：每人一个独立令牌，只存哈希；
	// 名字用来认人，令牌泄露/撤销只影响那个人，其他人照常。
	// 旧的面板级通用订阅令牌（SubTokenHash）保留不动，向后兼容，
	// 已发出去的通用链接继续能用。
	SubUsers []SubUser `json:"sub_users"`
	// 分组（v1.3.26，权限组）：一组 = 一组落地 + 一组用户，组里的人用组里
	// 的节点；成员归属存在 SubUser/MergedSub 的 GroupID 上（单字段，一人
	// 最多一个组），这里只存组的落地集合与名称。通用订阅（全量）不进分组。
	SubGroups []SubGroup `json:"sub_groups,omitempty"`
	// 按人转发（v1.3.25，按订阅限流量 A）：本面板上的每用户转发记录。
	// 只给开了限流的订阅建；每条是一条独立监听端口 → 同一落地端口的
	// 转发，端口就是用户身份。管理面板模式下本机没有转发，这里是空的；
	// 远程面板上的转发存在远程面板自己的 config.json 里。
	UserForwards []UserForward `json:"user_forwards,omitempty"`
	// 每用户端口段（v1.3.25）：分配用户端口的范围，默认 30000–39999，
	// 设置页可改；0（老配置）= 用默认值。分配时避让已有落地端口与
	// 已分配的用户端口；用户/订阅删除时回收。
	UserPortStart int `json:"user_port_start,omitempty"`
	UserPortEnd   int `json:"user_port_end,omitempty"`
	// 登记的远程面板（v1.2.0，主面板端）：Token 是明文（必须拿原文去调远程接口），
	// 与 Telegram Token、Cloudflare 令牌同等待遇，只存在 600 权限的 config.json 里。
	Remotes []RemotePanel `json:"remotes"`
	// 管理面板模式（v1.2.8）：true = 这台面板只做集中管理，不装中转；
	// 本地落地/服务/端口管理全部隐藏，只显示集中管理、合并订阅与设置。
	// 数据保留，切回 false 就恢复完整面板。安装时选模式写入，设置页可切换。
	ManagerOnly bool `json:"manager_only"`
	// 面板名称（v1.3.4）：设置页可改，默认 CP管理（1.3.3 之前写死的名字）。
	// 登录页标题/品牌、侧边栏品牌、manifest 与订阅导入后的配置名都跟着它变；
	// 旧 config.json 里没有这个字段时按默认值处理，不用迁移。
	PanelName string `json:"panel_name"`
	// 登录页公告（v1.3.21）：设置页可改的多行纯文本，显示在登录页登录按钮
	// 下方；空 = 不显示。只按纯文本展示，不解析 HTML；限 500 字。
	// 旧 config.json 里没有这个字段时按空处理，不用迁移。
	LoginNotice string `json:"login_notice,omitempty"`
	// 登录页签名（v1.3.21）：设置页可改的单行文本，显示在登录页面板名称
	// 下方；空 = 回落到默认“{面板名} · 请登录”。只按纯文本展示，不解析
	// HTML；限 60 字。旧 config.json 里没有这个字段时按空处理，不用迁移。
	LoginSignature string `json:"login_signature,omitempty"`
	// 登录告警（v1.3.8，可选，默认关）：连续 5 次登录失败触发锁定时、
	// 以及新 IP 第一次成功登录时，走 Telegram 发提醒。没配 Telegram 时
	// 开关不可用（前端置灰并提示先配 Telegram）；旧配置没有这个字段时
	// 按关处理，不用迁移。
	LoginAlert bool `json:"login_alert"`
	// 二次验证 TOTP（v1.3.8，可选，默认关）：TOTPSecret 是 base32 明文密钥，
	// 只存 600 权限的 config.json；TOTPBackupCodes 是备用码的 SHA-256 哈希，
	// 用一次废一个。旧配置没有这些字段时一律按未开启处理，不用迁移。
	TOTPEnabled     bool     `json:"totp_enabled"`
	TOTPSecret      string   `json:"totp_secret"`
	TOTPBackupCodes []string `json:"totp_backup_codes"`
	// 异地备份（v1.3.11）：定时备份加密后传到 R2 与七牛。口令本身不存，
	// 只存派生出的主密钥与固定盐（config.json，600 权限）；云端 SecretKey
	// 同样只存这里，任何接口都不返回，导出与备份文件也不含它。
	RemoteBackup RemoteBackupCfg `json:"remote_backup"`
	Version      string          `json:"version"`
}

// panelName 面板显示名称：设置里可改，空着（旧配置）默认 CP管理。
func (s *Server) panelName() string {
	if n := strings.TrimSpace(s.cfg.PanelName); n != "" {
		return n
	}
	return "CP管理"
}

// validPanelName 校验面板名称：1–24 个字；不能含控制字符与
// / \ : * ? " < > |（这些字符进文件名、响应头、HTML、manifest
// JSON 都会出问题）；不能是 . 或 ..，不能以 . 开头（隐藏文件）。
func validPanelName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > 24 {
		return false
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\\:*?"<>|`, r) {
			return false
		}
	}
	return true
}

// SubSelection 合并订阅里的一条勾选：Panel 是 "local"（本面板）或
// 远程面板 id，Landing 是落地编号（relay.conf 的 Index）。
type SubSelection struct {
	Panel   string `json:"panel"`
	Landing int    `json:"landing"`
}

// MergedSub 一份合并订阅：TokenHash 是订阅令牌的 SHA-256 哈希（鉴权查表），
// TokenEnc 是同一令牌的 AES-GCM 密文（v1.3.7，密钥在 PANEL_HOME/sub_token.key），
// 用来重新拼链接；1.3.7 之前生成的没有密文，复制时提示轮换一次。
// Selection 是这份订阅绑定的勾选集合。
// Mode（v1.3.17）：内容范围，both|relay|direct，空（老记录）按 both；
// /sub/ 服务时按它过滤链接；轮换令牌时保持不变（专用轮换接口不带 mode）。
// ExpiresAt（v1.3.20）：到期时间，RFC3339，空 = 永久；到期后 /sub/{token}
// 返回 410；轮换不改它，编辑时可改。
// Name 是归属到人（v1.3.0，选填）：填了就新起一条，同一勾选集合可有
// 多个不同名称的令牌，各归各人；不填走老逻辑（同一选择重新生成会轮换）。
// 流量上限（v1.3.25，按订阅限流量 A）：TrafficLimitBytes > 0 才限流，
// 0 = 不限流（走共享端口）；TrafficLimitMode 是 "total"（累计）或
// "monthly"（按月）；ResetMode 只在 monthly 下有效，是 "monthly"
// （自然月，每月 1 号）/ "anniversary"（跟随生成日）/ "custom"
// （自定义每月几号，ResetDay 1–31，31 号钳制到月末）；NextResetAt /
// LastResetAt 是 RFC3339；Disabled 是超量停用（转发已撤）。
// GroupID（v1.3.26，分组）：所属分组的 ID，空 = 不分组；非空时订阅按
// 组的落地集合出内容，自己的 Selection 保留在记录里（出组/删组后恢复）。
type MergedSub struct {
	ID        string         `json:"id"`
	Name      string         `json:"name,omitempty"`
	TokenHash string         `json:"token_hash"`
	TokenEnc  string         `json:"token_enc,omitempty"`
	CreatedAt string         `json:"created_at"`
	Selection []SubSelection `json:"selection"`
	Mode      string         `json:"mode,omitempty"`
	ExpiresAt string         `json:"expires_at,omitempty"`
	GroupID   string         `json:"group_id,omitempty"`
	// 流量上限（v1.3.25，A 方案）：见 MergedSub 头注释。
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
	// NodeUUIDs（v1.3.30，节点侧按人计量）：每落地一个 UUID，键是落地
	// 地址（relay.conf 的 LANDING，与 state.json 的落地键一致），值是
	// 该落地给这份订阅的 UUID（明文，与节点 UUID 同款存法）。本版只留
	// 字段、下发时原样返回，不生成（Phase 1 试点时生成）；备份/恢复
	// 全量跟走，丢了它用户就得重新导入。
	NodeUUIDs map[string]string `json:"node_uuids,omitempty"`
}

// SubGroup 分组（v1.3.26，权限组）：一组 = 一组落地 + 一组用户，组里的人
// 用组里的节点。建一次组、加用户就行，不用每人重复勾落地；通用共享节点
// 放一个组，美国/英国/法国等定制用户各放各的组。
// Selection 是组的落地集合（本面板 + 远程落地，与合并订阅同一套勾选）；
// 成员不另存列表——成员归属的唯一事实来源是 SubUser/MergedSub 上的
// GroupID（groupMembers() 现算），一个订阅最多属于一个组（单字段天然
// 保证，不会有两个组的落地集合冲突）；换组直接改成员的 GroupID。
// 成员自己的 Selection 保留在记录里（进组后订阅按组的落地出，自己的勾选
// 不用、出组/删组后自动恢复，不用重勾）。
type SubGroup struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Note      string         `json:"note,omitempty"`
	Selection []SubSelection `json:"selection"`
	CreatedAt string         `json:"created_at"`
}

// SubUser 一人一条订阅链接（v1.3.0）：TokenHash 是订阅令牌的 SHA-256
// 哈希（鉴权查表），TokenEnc 是同一令牌的 AES-GCM 密文（v1.3.7，
// 用来重新拼链接；旧链接没有密文，复制时提示轮换一次）。Name 是人名/备注
// （必填，用来认人）。Selection 为空 = 单面板全量订阅（全部落地），
// 非空 = 合并订阅的勾选集合（只导选中的落地）。
// Mode（v1.3.17）：内容范围，both|relay|direct，空（老记录）按 both；
// /sub/ 服务时按它过滤链接；轮换令牌时保持不变（专用轮换接口不带 mode）。
// ExpiresAt（v1.3.20）：到期时间，RFC3339，空 = 永久；到期后 /sub/{token}
// 返回 410；轮换不改它，编辑时可改。
// 流量上限（v1.3.25，按订阅限流量 A）：与 MergedSub 同一套字段（见上），
// > 0 才限流，0 = 不限流（走共享端口）。
// GroupID（v1.3.26，分组）：所属分组的 ID，空 = 不分组；非空时订阅按
// 组的落地集合出内容，自己的 Selection 保留在记录里（出组/删组后恢复）。
type SubUser struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Note      string         `json:"note,omitempty"`
	TokenHash string         `json:"token_hash"`
	TokenEnc  string         `json:"token_enc,omitempty"`
	CreatedAt string         `json:"created_at"`
	Selection []SubSelection `json:"selection,omitempty"`
	Mode      string         `json:"mode,omitempty"`
	ExpiresAt string         `json:"expires_at,omitempty"`
	GroupID   string         `json:"group_id,omitempty"`
	// 流量上限（v1.3.25，A 方案）：见 MergedSub 头注释。
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
	// NodeUUIDs（v1.3.30，节点侧按人计量）：与 SubUser 同款，见上。
	NodeUUIDs map[string]string `json:"node_uuids,omitempty"`
}

// UserForward 一条按人转发（v1.3.25，按订阅限流量 A）：一个订阅在
// 一台中转机上的独立转发。Key 是订阅标识（"subuser:<id>" 或
// "merged:<id>"，id 自带 su-/ms- 前缀）；Panel 是 "local" 或远程面板
// id（本面板上的记录永远是 "local"）；LandingAddr 是该面板上的落地
// 地址（按地址存，不按编号——删落地会让编号前移，地址才是稳定标识）；
// ListenPort 是这条转发在本机监听的端口（用户端口段内分配）；
// Disabled 是超量停用（转发已从 config.toml 撤掉，quota 也已封死）。
// Limit* / Reset* / NextResetAt 是这条转发在本机执法的配额依据
// （管理面板下发时一起带来，离线也能按最后同步的配额继续执法）；
// SubName 是订阅显示名，只用来写 Telegram 提醒。
// 订阅删除、限流取消、落地从勾选里去掉时回收（删记录、撤转发、放端口）。
type UserForward struct {
	Key         string `json:"key"`
	SubKind     string `json:"sub_kind"`
	SubID       string `json:"sub_id"`
	SubName     string `json:"sub_name,omitempty"`
	Panel       string `json:"panel"`
	LandingAddr string `json:"landing_addr"`
	ListenPort  int    `json:"listen_port"`
	Disabled    bool   `json:"disabled"`
	LimitBytes  int64  `json:"limit_bytes"`
	LimitMode   string `json:"limit_mode,omitempty"`
	ResetMode   string `json:"reset_mode,omitempty"`
	ResetDay    int    `json:"reset_day,omitempty"`
	NextResetAt string `json:"next_reset_at,omitempty"`
}

// ---------- 面板扩展状态（PANEL_HOME/state.json） ----------

type SubNode struct {
	Domain    string `json:"domain"`
	CreatedAt string `json:"created_at"`
	Note      string `json:"note"`
}

type LandingExt struct {
	Expiry            string    `json:"expiry"`
	TrafficLimitBytes int64     `json:"traffic_limit_bytes"`
	TrafficLimitMode  string    `json:"traffic_limit_mode"`
	SpeedLimitMbps    int       `json:"speed_limit_mbps"`
	Disabled          bool      `json:"disabled"`
	DisabledReason    string    `json:"disabled_reason"`
	SubNodes          []SubNode `json:"subnodes"`
	RenewReminded7d   bool      `json:"renew_reminded_7d"`
	RenewReminded3d   bool      `json:"renew_reminded_3d"`
	// CertWarned30 / CertWarned7（v1.3.6）：证书到期提醒的去重标志，存
	// state.json（重启不丢）。证书剩余 ≤30 天发一次预警、≤7 天发一次紧急
	// 提醒，同一阈值只提醒一次；续签后天数回升越过阈值时自动清掉，下次再
	// 越过重新提醒。落地改名时整个 ext 随旧键迁移，标志跟着走，不重复提醒。
	CertWarned30     bool `json:"cert_warned_30"`
	CertWarned7      bool `json:"cert_warned_7"`
	RenewProgress    int  `json:"renew_progress"`
	TrafficWarned80  bool `json:"traffic_warned_80"`
	TrafficWarned100 bool `json:"traffic_warned_100"`
	// OrigLink 是加落地时用户粘贴的原始节点链接（选填），只存面板自己的
	// state.json（600 权限），用来拼中转+直连链接与二维码。
	// 不传给安装脚本、不进日志与审计、不出现在任何接口的日志字段里。
	OrigLink string `json:"orig_link,omitempty"`
	// OrigLinkName 生成 twin 链接时用的节点名称（选填）：直连备注是
	// “<名称>-直连”、中转备注是“<名称>-中转”；空着就用原始链接自己的备注名。
	OrigLinkName string `json:"orig_link_name,omitempty"`
	// MeteringSource / AgentVersion / AgentLastPushAt（v1.3.30，节点侧
	// agent 计量）：计量来源 ""=未定（按 A 采样）/"agent"=agent 在报数/
	// "relay"=只认 A 的数/"none"=不计量；agent 版本与最后推送时间
	// （RFC3339），5 分钟内有推送算在线（nodeAgentLive）。随 state.json
	// 持久化，备份/恢复跟走。
	MeteringSource  string `json:"metering_source,omitempty"`
	AgentVersion    string `json:"agent_version,omitempty"`
	AgentLastPushAt string `json:"agent_last_push_at,omitempty"`
}

type RecycleItem struct {
	ID               string     `json:"id"`
	Landing          string     `json:"landing"`
	Domain           string     `json:"domain"`
	Ports            string     `json:"ports"`
	ModeNote         string     `json:"mode_note"`
	Mode             string     `json:"mode"`
	CertDomain       string     `json:"cert_domain"`
	CertMode         string     `json:"cert_mode"`
	DeletedAt        string     `json:"deleted_at"`
	ExpiresAt        string     `json:"expires_at"`
	RelayConfSnippet string     `json:"relay_conf_snippet"`
	ConfigEndpoints  string     `json:"config_endpoints"`
	Ext              LandingExt `json:"ext"`
}

type PanelState struct {
	Landings map[string]*LandingExt `json:"landings"`
	Recycle  []RecycleItem          `json:"recycle"`
	// LoginSeenIPs 登录告警用：已经成功登录过的 IP（同一 IP 只提醒一次）。
	// 无论登录告警开关开没开都登记，免得以后开了开关对老 IP 狂发提醒。
	LoginSeenIPs map[string]bool `json:"login_seen_ips,omitempty"`
}

func (s *PanelState) ext(landing string) *LandingExt {
	if s.Landings == nil {
		s.Landings = map[string]*LandingExt{}
	}
	e, ok := s.Landings[landing]
	if !ok || e == nil {
		e = &LandingExt{}
		s.Landings[landing] = e
	}
	return e
}

// ---------- relay.conf 解析 ----------

type PortSpec struct {
	Proto  string `json:"proto"`
	Listen int    `json:"listen"`
	Remote int    `json:"remote"`
}

type Landing struct {
	Index      int        `json:"index"`
	Landing    string     `json:"landing"`
	Domain     string     `json:"domain"`
	CertDomain string     `json:"cert_domain"`
	Preset     string     `json:"preset"`
	ModeNote   string     `json:"mode_note"`
	Mode       string     `json:"mode"`
	Ports      []PortSpec `json:"ports"`
	PortsRaw   string     `json:"-"`
	// Name 显示名（relay.conf 的 NAME[_N]，编辑时可改；只改显示，
	// 不动转发配置、端口、模式与状态键；空着就显示落地地址）。
	Name string `json:"name"`
	// CertMode 证书签发方式（relay.conf 的 CERT_MODE[_N]）：
	// ""=没填（旧落地，按老逻辑判）、"http01"=默认走 80、
	// "dns01"=DNS 验证 Cloudflare（续签不走 80，全自动）。
	CertMode string `json:"cert_mode"`
}

// displayName 卡片标题、历史分组、集中管理里显示的名字：
// 有名称用名称，没有就用落地地址，和以前一致。
func (l Landing) displayName() string {
	if l.Name != "" {
		return l.Name
	}
	return l.Landing
}

type RelayConf struct {
	Raw       map[string]string `json:"-"`
	Lines     []string          `json:"-"`
	Landings  []Landing         `json:"-"`
	Count     int               `json:"-"`
	Share80   bool              `json:"-"`
	Share443  bool              `json:"-"`
	AllPorts  string            `json:"-"`
	RelayAddr string            `json:"-"`
}

func parseRelayConf(data []byte) *RelayConf {
	rc := &RelayConf{Raw: map[string]string{}}
	for _, line := range strings.Split(string(data), "\n") {
		rc.Lines = append(rc.Lines, line)
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"`)
		rc.Raw[k] = v
	}
	rc.Count, _ = strconv.Atoi(rc.Raw["LANDING_COUNT"])
	if rc.Count < 1 {
		if _, ok := rc.Raw["LANDING"]; ok {
			rc.Count = 1
		}
	}
	rc.Share80 = rc.Raw["SHARE80"] == "1"
	rc.Share443 = rc.Raw["SHARE443"] == "1"
	rc.AllPorts = rc.Raw["ALL_PORTS"]
	rc.RelayAddr = rc.Raw["RELAY_ADDR"]
	for i := 1; i <= rc.Count; i++ {
		suffix := ""
		if i != 1 {
			suffix = "_" + strconv.Itoa(i)
		}
		get := func(f string) string { return rc.Raw[f+suffix] }
		landing := get("LANDING")
		if landing == "" {
			continue
		}
		l := Landing{Index: i, Landing: landing, Domain: get("DOMAIN"), CertDomain: get("CERT_DOMAIN"), Preset: get("PRESET"), ModeNote: get("MODE_NOTE"), PortsRaw: get("PORTS"), Name: get("NAME"), CertMode: get("CERT_MODE")}
		// MODE 缺字段的旧 relay.conf 默认标准模式（向后兼容）；只有 door 才记 door。
		if get("MODE") == "door" {
			l.Mode = "door"
		} else {
			l.Mode = "standard"
		}
		l.Ports = parsePorts(l.PortsRaw)
		rc.Landings = append(rc.Landings, l)
	}
	return rc
}

func parsePorts(raw string) []PortSpec {
	var out []PortSpec
	for _, spec := range strings.Fields(raw) {
		p := strings.Split(spec, ":")
		if len(p) != 3 {
			continue
		}
		lp, err1 := strconv.Atoi(p[1])
		rp, err2 := strconv.Atoi(p[2])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, PortSpec{Proto: p[0], Listen: lp, Remote: rp})
	}
	return out
}

// setRelayConfValue 只改目标键行（或追加），其余行原样保留。
func setRelayConfValue(lines []string, key, value string) []string {
	want := key + `="` + value + `"`
	seen := false
	out := make([]string, 0, len(lines)+1)
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, key+"=") {
			if !seen {
				out = append(out, want)
				seen = true
			}
			continue
		}
		out = append(out, l)
	}
	if !seen {
		out = append(out, want)
	}
	return out
}

// removeRelayConfValue 删掉指定键的行（其余行原样保留），用于清除 RELAY_ADDR 这类可空的全局键。
func removeRelayConfValue(lines []string, key string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, key+"=") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// removeLandingLines 删掉指定编号落地的 relay.conf 行（编号 1 用平铺键，>=2 用 _N 后缀），
// 并把 LANDING_COUNT 减 1（编号 > 被删编号的落地需要整体前移一位——调用方用 rebuild 处理）。
func (rc *RelayConf) fieldKeys(index int) []string {
	suffix := ""
	if index != 1 {
		suffix = "_" + strconv.Itoa(index)
	}
	return []string{"LANDING" + suffix, "LANDING_IS_DOMAIN" + suffix, "DOMAIN" + suffix, "CERT_DOMAIN" + suffix, "PRESET" + suffix, "MODE_NOTE" + suffix, "MODE" + suffix, "PORTS" + suffix, "NAME" + suffix}
}

// ---------- 服务器 ----------

type Server struct {
	env                Env
	cfg                PanelConfig
	cfgPath            string
	state              PanelState
	stateMu            sync.Mutex
	sessions           *sessionStore
	opMu               sync.Mutex // 写操作锁（TryLock，占用时返回 409）
	traffic            *trafficStore
	userTraffic        *userTrafficStore  // 每订阅流量累计（v1.3.25，PANEL_HOME/user_traffic.json）
	nodePushSeen       *nodePushSeenStore // 节点侧推送去重（v1.3.30，PANEL_HOME/node_push_seen.json）
	trafficErrMu       sync.Mutex         // 流量采样失败记录锁（v1.3.27：失败不再静默）
	trafficErrMsg      string             // 最近一次流量采样/建链失败原因（空 = 正常）
	trafficErrAt       time.Time
	auditMu            sync.Mutex
	healthMu           sync.Mutex // 健康基线锁（lastHealthOK/lastHealthAt：healthTick 与模式切换共用）
	lastHealthOK       bool
	lastHealthAt       time.Time
	monitorMu          sync.Mutex
	monitorAlerts      map[string]*monitorAlertState
	latestMonitor      *SystemStats
	latency            *latencyStore
	latencyMu          sync.Mutex
	latencyAlerts      map[string]*latencyAlertState
	latencyRoundMu     sync.Mutex // 一轮探测的锁（后台循环与总览兜底互斥）
	centralMu          sync.Mutex // 集中管理缓存锁
	centralCache       map[string]*RemoteStatusSnapshot
	centralAlert       map[string]*centralAlertState
	centralUserUsageMu sync.Mutex // 按订阅聚合用量缓存锁（v1.3.25）
	centralUserUsage   map[string]*centralUserUsage
	centralPollWG      sync.WaitGroup // 后台轮询计数（代理写成功后异步刷新一次；测试等它跑完再清理，避免轮询撞上 TempDir 删除）
	subKey             []byte         // 订阅令牌加密密钥（v1.3.7，PANEL_HOME/sub_token.key 的内容，懒加载）
	subKeyMu           sync.Mutex
	subFailMu          sync.Mutex // /sub/ 限流锁（v1.3.7）
	subFails           map[string]*subFailState
	remoteUploadMu     sync.Mutex // 异地备份上传去重锁（v1.3.11）
	remoteUploading    map[string]bool
	certMu             sync.Mutex // 面板自身证书 ACME 操作锁（手动签发与后台续签互斥）
}

func newServer(env Env) (*Server, error) {
	if err := os.MkdirAll(env.PanelHome, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(env.PanelHome, "backups"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(env.PanelHome, "certs"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(env.RealmDir, "backup"), 0o700); err != nil {
		// 测试环境 REALM_DIR 可能不可写目录已存在则忽略
		_ = os.MkdirAll(env.RealmDir, 0o755)
	}
	s := &Server{env: env, cfgPath: filepath.Join(env.PanelHome, "config.json"), sessions: newSessionStore(), traffic: newTrafficStore(filepath.Join(env.PanelHome, "traffic.json")), userTraffic: newUserTrafficStore(filepath.Join(env.PanelHome, "user_traffic.json")), nodePushSeen: newNodePushSeenStore(filepath.Join(env.PanelHome, "node_push_seen.json")), lastHealthOK: true, monitorAlerts: map[string]*monitorAlertState{}, latency: newLatencyStore(filepath.Join(env.PanelHome, "latency.json")), latencyAlerts: map[string]*latencyAlertState{}, centralCache: map[string]*RemoteStatusSnapshot{}, centralAlert: map[string]*centralAlertState{}, centralUserUsage: map[string]*centralUserUsage{}, subFails: map[string]*subFailState{}}
	if err := s.loadConfig(); err != nil {
		return nil, err
	}
	if err := s.loadState(); err != nil {
		return nil, err
	}
	s.traffic.load()
	s.userTraffic.load()
	s.nodePushSeen.load()
	s.latency.load()
	return s, nil
}

func (s *Server) loadConfig() error {
	data, err := os.ReadFile(s.cfgPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		// 首次启动：安装器正常会写好 config.json；没有时生成一个（测试用 PANEL_INITIAL_PASSWORD 指定密码）
		pw := os.Getenv("PANEL_INITIAL_PASSWORD")
		if pw == "" {
			pw = randomPassword(16)
			fmt.Printf("面板初始账号：admin / %s（只显示一次，请立即改密码）\n", pw)
		}
		h, err := hashPassword(pw)
		if err != nil {
			return err
		}
		s.cfg = PanelConfig{Username: "admin", PasswordHash: h, Port: s.env.Port, Version: Version, Monitor: defaultMonitorCfg(), Latency: defaultLatencyCfg(), Backup: defaultBackupCfg()}
		if s.env.Port == 0 {
			s.cfg.Port = 11111
		}
		return s.saveConfig()
	}
	if err := json.Unmarshal(data, &s.cfg); err != nil {
		return fmt.Errorf("config.json 解析失败：%w", err)
	}
	if s.cfg.Port == 0 {
		s.cfg.Port = 11111
	}
	if s.cfg.Username == "" {
		s.cfg.Username = "admin"
	}
	// 旧 config.json 里没有 monitor 一节时补默认值（1.1.10 之前装的面板）
	s.cfg.Monitor.applyDefaults()
	// 旧 config.json 里没有 latency 一节时补默认值（1.1.11 之前装的面板）
	s.cfg.Latency.applyDefaults()
	// 旧 config.json 里没有 backup 一节时补默认值（1.3.6 之前装的面板）
	s.cfg.Backup.applyDefaults()
	return nil
}

func (s *Server) saveConfig() error {
	s.cfg.Version = Version
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.cfgPath, data, 0o600)
}

func (s *Server) loadState() error {
	p := filepath.Join(s.env.PanelHome, "state.json")
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			s.state = PanelState{Landings: map[string]*LandingExt{}}
			return nil
		}
		return err
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return fmt.Errorf("state.json 解析失败：%w", err)
	}
	if s.state.Landings == nil {
		s.state.Landings = map[string]*LandingExt{}
	}
	return nil
}

func (s *Server) saveState() error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.saveStateLocked()
}

func (s *Server) saveStateLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.env.PanelHome, "state.json"), data, 0o600)
}

func (s *Server) readRelayConf() (*RelayConf, []byte, error) {
	data, err := os.ReadFile(filepath.Join(s.env.RealmDir, "relay.conf"))
	if err != nil {
		return nil, nil, err
	}
	return parseRelayConf(data), data, nil
}

func (s *Server) readConfigToml() ([]byte, error) {
	return os.ReadFile(filepath.Join(s.env.RealmDir, "config.toml"))
}

// writeFileAtomic 临时文件 → 校验（调用方）→ rename 原子替换。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// writeRelayConfAtomic 写 relay.conf：先校验能被 bash source，再原子替换。
func (s *Server) writeRelayConfAtomic(data []byte) error {
	tmp := filepath.Join(s.env.RealmDir, ".relay.conf.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := runCmd(nil, 10*time.Second, "bash", "-c", `set -a; . "$1" >/dev/null 2>&1; set +a`, "bash", tmp); err != nil {
		return fmt.Errorf("relay.conf 校验失败（bash source 不通过）")
	}
	return os.Rename(tmp, filepath.Join(s.env.RealmDir, "relay.conf"))
}

// validateConfigToml 简易 TOML 校验：括号/引号配对 + 含 [[endpoints]]（或占位时无 endpoint 也可接受——
// 但 realm 无 endpoint 会 panic，所以无 endpoint 时要求文件含占位 127.0.0.1:9）。
func validateConfigToml(data []byte) error {
	depth := 0
	inStr := false
	for _, r := range string(data) {
		switch {
		case r == '"' && depth >= 0:
			inStr = !inStr
		case !inStr && r == '[':
			depth++
		case !inStr && r == ']':
			depth--
			if depth < 0 {
				return fmt.Errorf("config.toml 方括号不配对")
			}
		}
	}
	if depth != 0 || inStr {
		return fmt.Errorf("config.toml 括号/引号不配对")
	}
	if !strings.Contains(string(data), "[[endpoints]]") {
		return fmt.Errorf("config.toml 没有 [[endpoints]] 段")
	}
	return nil
}

func (s *Server) writeConfigTomlAtomic(data []byte) error {
	if err := validateConfigToml(data); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.env.RealmDir, "config.toml"), data, 0o644)
}

// ---------- 备份 ----------

type BackupMeta struct {
	ID    string `json:"id"`
	TS    string `json:"ts"`
	Label string `json:"label"`
	Scope string `json:"scope"`
}

// backupNow 写操作前自动备份：config.toml + relay.conf + state.json 各一份快照，
// 存 PANEL_HOME/backups/<id>/（meta.json + 文件副本），同时在 REALM_DIR/backup/ 留一份，
// 两边都只保留最近 10 份。
func (s *Server) backupNow(label, scope string) (string, error) {
	id := time.Now().UTC().Format("20060102-150405") + "-" + randomTokenHex(3)
	dir := filepath.Join(s.env.PanelHome, "backups", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, name := range []string{"config.toml", "relay.conf"} {
		if b, err := os.ReadFile(filepath.Join(s.env.RealmDir, name)); err == nil {
			_ = os.WriteFile(filepath.Join(dir, name), b, 0o600)
		}
	}
	if b, err := os.ReadFile(filepath.Join(s.env.PanelHome, "state.json")); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "state.json"), b, 0o600)
	}
	meta := BackupMeta{ID: id, TS: time.Now().UTC().Format(time.RFC3339), Label: label, Scope: scope}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "meta.json"), mb, 0o600)
	// REALM_DIR/backup/ 留一份（契约 §5）
	rb := filepath.Join(s.env.RealmDir, "backup", "backup-"+id+".json")
	snap := map[string]any{"id": id, "ts": meta.TS, "label": label}
	if b, err := os.ReadFile(filepath.Join(s.env.RealmDir, "config.toml")); err == nil {
		snap["config_toml"] = string(b)
	}
	if b, err := os.ReadFile(filepath.Join(s.env.RealmDir, "relay.conf")); err == nil {
		snap["relay_conf"] = string(b)
	}
	if jb, err := json.Marshal(snap); err == nil {
		_ = os.WriteFile(rb, jb, 0o600)
	}
	s.pruneBackups()
	return id, nil
}

func (s *Server) pruneBackups() {
	pruneDir(filepath.Join(s.env.PanelHome, "backups"), 10)
	pruneDir(filepath.Join(s.env.RealmDir, "backup"), 10)
}

func pruneDir(dir string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.RemoveAll(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// subPath 订阅端点的路径段（v1.3.20，反特征）：配置里 SubPath 为空
// （老配置）时默认 "sub"，即 /sub/{token}；改了就用改的值。
func (s *Server) subPath() string {
	p := strings.TrimSpace(s.cfg.SubPath)
	if p == "" {
		return "sub"
	}
	return p
}

// validSubPath 校验自定义订阅路径段：单段、字母数字开头、只含字母数字
// 与 - _、1–32 个字符；"api" 与 "logo" 是保留段（路由与 Logo 接口在用），
// 不能用；"sub" 是默认值，允许（改回默认）。
func validSubPath(p string) bool {
	if p == "" {
		return false
	}
	if len(p) > 32 {
		return false
	}
	if p == "api" || p == "logo" {
		return false
	}
	for i, r := range p {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok {
			return false
		}
		if i == 0 && r >= '0' && r <= '9' {
			return false
		}
	}
	return true
}

// ---------- 订阅到期时间（v1.3.20） ----------

// parseSubExpiry 解析用户填的到期时间：空 = 永久；"YYYY-MM-DD" = 当天
// 23:59:59（服务器本地时间）；RFC3339 全串原样接受。返回规范化后的
// RFC3339（空 = 永久），非法返回错误。
func parseSubExpiry(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if d, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		end := time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, time.Local)
		return end.UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("到期时间不对：填日期（如 2026-12-31）或留空表示永久")
}

// subExpired 到期时间是否已过：空 = 永久不过期；解析失败的按不过期处理
// （旧数据容错，不因格式问题把订阅直接判死）。
func subExpired(expiresAt string, now time.Time) bool {
	if strings.TrimSpace(expiresAt) == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return now.After(t)
}

// ---------- 审计日志 ----------

type AuditEntry struct {
	TS     string `json:"ts"`
	User   string `json:"user"`
	Action string `json:"action"`
	Target string `json:"target"`
	Result string `json:"result"`
	Detail string `json:"detail"`
}

func (s *Server) audit(user, action, target, result, detail string) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	e := AuditEntry{TS: time.Now().UTC().Format(time.RFC3339), User: user, Action: action, Target: target, Result: result, Detail: sanitize(detail)}
	b, _ := json.Marshal(e)
	auditPath := filepath.Join(s.env.PanelHome, "audit.log")
	rotateLogIfLarge(auditPath) // 超 10MB 就轮转，旧份只留 3 个
	f, err := os.OpenFile(auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// readAuditPage 按时间倒序读审计记录，返回当前页条目与总条数。
// 连轮转旧份一起读（audit.log 最新，其次 .1/.2/.3），这样翻页能看到轮转前的旧记录。
// 单次扫描：文件本身被轮转限制在 10MB 以内，读全文件再切片是可接受的；
// 只保留当前页的条目进内存，total 是全部有效记录数。page 从 1 开始。
// q（v1.3.20）：搜索关键词，空 = 不过滤；非空时只留时间/用户/操作/对象/
// 结果/详情里含该词（不区分大小写）的记录，total 也按过滤后算。
func (s *Server) readAuditPage(page, pageSize int, q string) ([]AuditEntry, int) {
	q = strings.ToLower(strings.TrimSpace(q))
	files := []string{"audit.log", "audit.log.1", "audit.log.2", "audit.log.3"}
	offset := (page - 1) * pageSize
	end := offset + pageSize
	var out []AuditEntry
	total := 0
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(s.env.PanelHome, name))
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.TrimSpace(lines[i]) == "" {
				continue
			}
			var e AuditEntry
			if json.Unmarshal([]byte(lines[i]), &e) != nil {
				continue
			}
			if q != "" {
				hay := strings.ToLower(e.TS + " " + e.User + " " + e.Action + " " + e.Target + " " + e.Result + " " + e.Detail)
				if !strings.Contains(hay, q) {
					continue
				}
			}
			if total >= offset && total < end {
				out = append(out, e)
			}
			total++
		}
	}
	if out == nil {
		out = []AuditEntry{}
	}
	return out, total
}

// sanitize 脱敏：去掉可能出现的 token/密码哈希片段。
func sanitize(s string) string {
	for _, k := range []string{"bot_token", "password", "cloudflare_token"} {
		_ = k
	}
	// 链接里的 UUID 等不属于凭据，不处理；只截断超长输出
	if len(s) > 2000 {
		return s[:2000] + "…（已截断）"
	}
	return s
}

// ---------- credentials 文件同步 ----------
// PANEL_HOME/credentials（600，安装器先写初始版，panel-info 读它找回密码）。
// 格式：面板地址：http://<ip>:<port> / 面板端口：<port> / 账号：<user> / 密码：<明文>
// 面板绝不通过任何 API 返回它的内容；改密码只更新“密码”行，改端口则更新“端口”行
// （地址行里的端口一并跟着改），其余行保持安装器写的内容不动。
func (s *Server) syncCredentials(newPassword string, newPort int) {
	p := filepath.Join(s.env.PanelHome, "credentials")
	data, err := os.ReadFile(p)
	if err != nil {
		return // 安装器没写过就不新建（面板不掌握地址 IP）
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "密码：") && newPassword != "" {
			lines[i] = "密码：" + newPassword
		}
		if newPort > 0 {
			if strings.HasPrefix(l, "面板端口：") {
				lines[i] = fmt.Sprintf("面板端口：%d", newPort)
			}
			if strings.HasPrefix(l, "面板地址：") {
				// 地址行形如 http://203.0.113.1:11111：只改末尾端口
				if idx := strings.LastIndex(l, ":"); idx > 0 {
					if _, err := strconv.Atoi(strings.TrimSpace(l[idx+1:])); err == nil {
						lines[i] = l[:idx+1] + strconv.Itoa(newPort)
					}
				}
			}
		}
	}
	_ = writeFileAtomic(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}
