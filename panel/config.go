package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
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
	// DNS-01 服务商（v1.3.49）：绑域名签真证书时，用哪个 DNS 写 _acme-challenge
	// TXT 记录。空 = cloudflare（老用户已配 CF Token 的不受影响；没配过的新用户
	// 默认也是 cloudflare，选别的再填）。凭证只存不回显，跟 CloudflareToken
	// 同等待遇（config.json 600 权限，不随导出走）。
	DNSProvider            string `json:"dns_provider,omitempty"`
	AliyunAccessKeyID      string `json:"aliyun_access_key_id,omitempty"`
	AliyunAccessKeySecret  string `json:"aliyun_access_key_secret,omitempty"`
	DNSPodTokenID          string `json:"dnspod_token_id,omitempty"`
	DNSPodToken            string `json:"dnspod_token,omitempty"`
	Route53AccessKeyID     string `json:"route53_access_key_id,omitempty"`
	Route53SecretAccessKey string `json:"route53_secret_access_key,omitempty"`
	GoDaddyPAT             string `json:"godaddy_pat,omitempty"`
	NamecheapAPIUser       string `json:"namecheap_api_user,omitempty"`
	NamecheapAPIKey        string `json:"namecheap_api_key,omitempty"`
	NamecheapClientIP      string `json:"namecheap_client_ip,omitempty"`
	// DNS-01 服务商第一批扩充（v1.4.15）：DigitalOcean、Linode、Vultr、
	// Bunny DNS、Hetzner 各 1 字段；Spaceship、Porkbun 各 2 字段；Gandi、
	// NameSilo、deSEC 各 1 字段；CloudNS 3 字段（auth-id＋auth-password，
	// 子账号再填 sub-auth-user）。凭证只存不回显，跟 CloudflareToken
	// 同等待遇（config.json 600 权限，不随导出走）。
	DigitalOceanToken string `json:"digitalocean_token,omitempty"`
	LinodeToken       string `json:"linode_token,omitempty"`
	VultrAPIKey       string `json:"vultr_api_key,omitempty"`
	BunnyAPIKey       string `json:"bunny_api_key,omitempty"`
	HetznerToken      string `json:"hetzner_token,omitempty"`
	SpaceshipAPIKey   string `json:"spaceship_api_key,omitempty"`
	SpaceshipSecret   string `json:"spaceship_api_secret,omitempty"`
	GandiToken        string `json:"gandi_token,omitempty"`
	NameSiloAPIKey    string `json:"namesilo_api_key,omitempty"`
	DesecToken        string `json:"desec_token,omitempty"`
	PorkbunAPIKey     string `json:"porkbun_api_key,omitempty"`
	PorkbunSecret     string `json:"porkbun_secret,omitempty"`
	CloudNSAuthID     string `json:"cloudns_auth_id,omitempty"`
	CloudNSPassword   string `json:"cloudns_auth_password,omitempty"`
	CloudNSSubUser    string `json:"cloudns_sub_auth_user,omitempty"`
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
	// 独立合并订阅（v1.4.34，第 4 批，方案 A 彻底独立）：与 SubUser
	// 用户订阅链接完全不关联、不混合。只是一份“勾选了哪些落地”的
	// 订阅内容：不登录、不绑用户、不挂档位、不进分组。旧合并订阅已在
	// v1.4.40 彻底移除（记录连同退场分支一起删），这里是唯一的合并订阅。
	IndependentMergedSubs []IndependentMergedSub `json:"independent_merged_subs,omitempty"`
	// 订阅用户（v1.3.0，一人一条订阅链接）：每人一个独立令牌，只存哈希；
	// 名字用来认人，令牌泄露/撤销只影响那个人，其他人照常。
	// 旧的面板级通用订阅令牌（SubTokenHash）保留不动，向后兼容，
	// 已发出去的通用链接继续能用。
	SubUsers []SubUser `json:"sub_users"`
	// 分组（v1.3.26，权限组）：一组 = 一组落地 + 一组用户，组里的人用组里
	// 的节点；成员归属存在 SubUser 的 GroupID 上（单字段，一人
	// 最多一个组），这里只存组的落地集合与名称。通用订阅（全量）不进分组。
	SubGroups []SubGroup `json:"sub_groups,omitempty"`
	// 套餐档位（v1.4.27，方案 B）：管理员自定义创建的档位模板；订阅用户
	// 挂 PlanID，建用户时档位值作默认值（个人值优先覆盖）。空 = 还没建
	// 过档位，读时按“默认”档位兜底（见 plans.go 的 ensureDefaultPlan）。
	Plans []Plan `json:"plans,omitempty"`
	// 按人转发（v1.3.25，按订阅限流量 A）：本面板上的每用户转发记录。
	// 只给开了限流的订阅建；每条是一条独立监听端口 → 同一落地端口的
	// 转发，端口就是用户身份。管理面板模式下本机没有转发，这里是空的；
	// 远程面板上的转发存在远程面板自己的 config.json 里。
	UserForwards []UserForward `json:"user_forwards,omitempty"`
	// 集中用户（v1.3.36，Phase 1 切片 2）：管理面板同步来的用户记录，
	// 按落地地址分表存放；与本面板本地用户（SubUsers）分开，
	// node/users 下发时按落地合并。键是落地地址，值是该落地上的用户行
	// （nodeUser 形状，每行只带该落地的 UUID）。管理面板按落地全量替
	// 换：空表 = 该落地无集中用户。备份/恢复全量跟走（丢了它下次同步
	// ——每 2 分钟的轮询兜底——会重新补齐）。
	CentralUsers map[string][]nodeUser `json:"central_users,omitempty"`
	// 每用户端口段（v1.3.25）：分配用户端口的范围，默认 30000–39999，
	// 设置页可改；0（老配置）= 用默认值。分配时避让已有落地端口与
	// 已分配的用户端口；用户/订阅删除时回收。
	UserPortStart int `json:"user_port_start,omitempty"`
	UserPortEnd   int `json:"user_port_end,omitempty"`
	// 中转倍率（v1.3.43）：本台中转机的流量倍率，与单落地倍率相乘——
	// 实际流量 × 落地倍率 × 中转倍率计入订阅用量（userinfo 与 80%/100%
	// 限流都按折算后的用量算）；0 = 没填（旧面板，按 1 算）。存在
	// config.json，设置页订阅选项卡可改；管理面板模式下本机没有中转，
	// 这张卡隐藏（见 loadSettingsPage），倍率由各中转面板自己设。
	RelayMultiplier float64 `json:"relay_multiplier,omitempty"`
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
	// 公告（v1.4.29）：多条公告，见 announcements.go；公开拉取与
	// public-info 回包共用同一份列表。旧 config.json 里没有这个字段时
	// 按空处理（公开接口返回 []），不用迁移。
	Announcements []Announcement `json:"announcements,omitempty"`
	// 远程配置（v1.4.29）：管理员粘贴的已签名信封原文（字符串原样存，
	// 见 client_config.go）。面板没有签名私钥、也不能现场签名，只原样
	// served，不生成、不改内容、不验签（验签是客户端拿编译进的公钥做）。
	// 空 = 还没发布，公开 GET /client-config.json 返回 404。旧
	// config.json 里没有这个字段时按空处理，不用迁移。
	ClientConfigJSON string `json:"client_config_json,omitempty"`
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

// IndependentMergedSub 一份独立合并订阅（v1.4.34，第 4 批）：字段与
// 旧合并订阅同款，但删掉登录与档位字段——没有 PlanID / Username /
// PasswordHash / LinkMode，也没有 GroupID（方案 A：不进分组，节点范围
// 就是自己的 Selection）。ID 前缀 ims- ＋ 6 位随机十六进制；令牌前缀
// mrg_ ＋ 64 位十六进制（见 subtoken.go）；链接路径 /m/<token>（见
// handlers.go）；转发/用量 key 用 indmerged:<id>（见 user_traffic.go）。
// Selection 允许空集合——先建空订阅、稍后勾落地；空订阅拉取返回空内容，
// 不 410、不报错。ExpiresAt 空 = 永久，到期后拉订阅回 410“订阅已过期”
// （与退场 410 不同文案）。限流全套字段照搬旧合并订阅。
type IndependentMergedSub struct {
	ID        string         `json:"id"`
	Name      string         `json:"name,omitempty"`
	TokenHash string         `json:"token_hash"`
	TokenEnc  string         `json:"token_enc,omitempty"`
	CreatedAt string         `json:"created_at"`
	Selection []SubSelection `json:"selection"`
	Mode      string         `json:"mode,omitempty"`
	ExpiresAt string         `json:"expires_at,omitempty"`
	// 流量上限（v1.3.25，A 方案）：TrafficLimitBytes > 0 才限流，0 = 不限流；
	// ResetMode 只在 monthly 下有效（monthly/anniversary/custom）。
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
	// 地址，值是该落地给这份订阅的 UUID（明文，与节点 UUID 同款存法）。
	NodeUUIDs map[string]string `json:"node_uuids,omitempty"`
	// BackendScope/BackendTargets（v1.4.4，切片 4，四级指派，见
	// node_assign.go）：与 SubUser 同款字段；没有分组，组级指派不可用
	// （validateBackendAssignment 要求 groupID，独立订阅没有分组）。
	BackendScope   string   `json:"backend_scope,omitempty"`
	BackendTargets []string `json:"backend_targets,omitempty"`
}

// Plan 套餐档位（v1.4.27，方案 B，XBoard 式）：管理员在面板里自定义
// 创建的档位模板。订阅用户挂 PlanID：建用户时没给分组/限流/模式/重置/
// 到期就用档位的值（档位 ExpiresDays>0 时建用户时 ExpiresAt=now+天数），
// 给了的个人值优先（档位默认值＋个人覆盖）。档位关联的权限组决定节点
// 范围（GroupID）；档位本身不存落地勾选，节点范围只走分组。
// TrafficLimitBytes 0 = 不限流；TrafficLimitMode total/monthly；
// ResetMode 只在 monthly 下有效（monthly/anniversary/custom）；
// ExpiresDays 0 = 永久；Mode both/relay/direct。
// 默认档位（Plans 为空时）：Name "默认"，无分组、不限流、永不过期、both，
// 由 plans.go 的 ensureDefaultPlan() 保证（写路径上建，GET 不写盘）。
type Plan struct {
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
	CreatedAt         string `json:"created_at"`
}

// SubGroup 分组（v1.3.26，权限组）：一组 = 一组落地 + 一组用户，组里的人
// 用组里的节点。建一次组、加用户就行，不用每人重复勾落地；通用共享节点
// 放一个组，美国/英国/法国等定制用户各放各的组。
// Selection 是组的落地集合（本面板 + 远程落地，与合并订阅同一套勾选）；
// 成员不另存列表——成员归属的唯一事实来源是 SubUser 上的
// GroupID（groupMembers() 现算），一个订阅最多属于一个组（单字段天然
// 保证，不会有两个组的落地集合冲突）；换组直接改成员的 GroupID。
// 成员自己的 Selection 保留在记录里（进组后订阅按组的落地出，自己的勾选
// 不用、出组/删组后自动恢复，不用重勾）。
// BackendNodeIDs（v1.4.4，切片 4）：该组的后端节点（轻量节点后端）——
// 组级指派（见 node_assign.go）时，组内成员上这些节点；空 = 该组没配
// 后端节点，组级指派下成员上不了任何后端节点。
type SubGroup struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Note      string         `json:"note,omitempty"`
	Selection []SubSelection `json:"selection"`
	CreatedAt string         `json:"created_at"`
	// BackendNodeIDs 该组用的后端节点 id 列表（v1.4.4）。
	BackendNodeIDs []int64 `json:"backend_node_ids,omitempty"`
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
// 流量上限（v1.3.25，按订阅限流量 A）：与独立合并订阅同一套字段，
// > 0 才限流，0 = 不限流（走共享端口）。
// GroupID（v1.3.26，分组）：所属分组的 ID，空 = 不分组；非空时订阅按
// 组的落地集合出内容，自己的 Selection 保留在记录里（出组/删组后恢复）。
// PlanID/Username/PasswordHash（v1.4.27，方案 B）：PlanID 是套餐档位
// （空 = 没挂档位）；Username 是该用户的登录账号（唯一，不可与管理员
// 账号重名，空着创建时自动生成 u-＋6 位随机十六进制）；PasswordHash
// 是登录密码的 PBKDF2 哈希（与管理员密码同款 hashPassword，绝不返回
// 给前端；空 = 这个用户还没设登录密码、登不进来）。
type SubUser struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Note      string         `json:"note,omitempty"`
	TokenHash string         `json:"token_hash"`
	TokenEnc  string         `json:"token_enc,omitempty"`
	CreatedAt string         `json:"created_at"`
	Selection []SubSelection `json:"selection,omitempty"`
	Mode      string         `json:"mode,omitempty"`
	// LinkMode 订阅级链接模式（Unreleased，订阅体系统一 B 追加）：
	// "" = 跟随节点（默认，按节点的 LinkMode 出）；"direct" = 仅 A
	// 直连；"relay" = 仅 B 中转；"both" = A 和 B 都出。订阅级显式
	// 设置优先于节点级；只影响订阅拼链接（backendSubLinksForUser），
	// 单条入站链接不动；改它不 bump 节点 ConfigVersion（订阅内容
	// 实时生成，agent 不用重新拉配置）。
	LinkMode  string `json:"link_mode,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
	// 套餐档位（v1.4.27）：见 SubUser 头注释。
	PlanID       string `json:"plan_id,omitempty"`
	Username     string `json:"username,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
	// 流量上限（v1.3.25，A 方案）：见本类型头注释。
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
	// 地址，值是该落地给这个用户的 UUID（明文，与节点 UUID 同款存法）；
	// 1.3.35 起生成（新建即生成、已有补生成、删除随记录回收）。
	NodeUUIDs map[string]string `json:"node_uuids,omitempty"`
	// BackendScope/BackendTargets（v1.4.4，切片 4，四级指派，见
	// node_assign.go）：该用户在后端节点上的指派级别。"" 或 "all" =
	// 全部节点（默认）；"group" = 跟所在分组走（targets 是分组 id，
	// 节点取分组的 BackendNodeIDs）；"node" = 只上 targets 里的节点
	// id；"inbound" = 只上 targets 里的 "节点id:入站tag" 指定的入站，
	// 下发时写进 users[].inbounds。空 scope 与空 targets 都按全部算。
	BackendScope   string   `json:"backend_scope,omitempty"`
	BackendTargets []string `json:"backend_targets,omitempty"`
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
	ID                string     `json:"id"`
	Landing           string     `json:"landing"`
	Domain            string     `json:"domain"`
	Ports             string     `json:"ports"`
	ModeNote          string     `json:"mode_note"`
	Mode              string     `json:"mode"`
	CertDomain        string     `json:"cert_domain"`
	CertMode          string     `json:"cert_mode"`
	TrafficMultiplier float64    `json:"traffic_multiplier"`
	DeletedAt         string     `json:"deleted_at"`
	ExpiresAt         string     `json:"expires_at"`
	RelayConfSnippet  string     `json:"relay_conf_snippet"`
	ConfigEndpoints   string     `json:"config_endpoints"`
	Ext               LandingExt `json:"ext"`
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
	// TrafficMultiplier 流量倍率（relay.conf 的 MULTIPLIER[_N]，v1.3.40）：
	// 实际流量 × 倍率计入订阅用量（userinfo 与 80%/100% 限流都按折算后
	// 的用量算）；0 = 没填（旧落地，按 1 算）。
	TrafficMultiplier float64 `json:"traffic_multiplier"`
	// NeedsUDP 该落地是否需要 UDP 转发（relay.conf 的 NEEDS_UDP[_N]，v1.3.46）：
	// 1=需要（有 Hysteria2/TUIC 节点），0=不需要（纯 TCP 协议，如 VLESS），
	// -1=没填（老 relay.conf，体检按旧行为检查 UDP 监听）。
	NeedsUDP int `json:"needs_udp"`
}

// 流量倍率上限（v1.3.40）：100 倍已是极端值，再高多半是手误。
const maxTrafficMultiplier = 100

// normalizeTrafficMultiplier 倍率归一化：<=0 / 非法 = 1（默认）；
// 超过上限钳制到上限。文件解析与落点都走它，API 校验另在
// validateLandingReqFields 里拒绝非法值。
func normalizeTrafficMultiplier(m float64) float64 {
	if m <= 0 || math.IsNaN(m) || math.IsInf(m, 0) {
		return 1
	}
	if m > maxTrafficMultiplier {
		return maxTrafficMultiplier
	}
	return m
}

// multiplier 落地的有效倍率（旧落地无该字段时为 0，按 1 算）。
func (l Landing) multiplier() float64 {
	return normalizeTrafficMultiplier(l.TrafficMultiplier)
}

// setMultiplierLine 写/删 relay.conf 里某落地的 MULTIPLIER 键：
// 倍率 1 不存键（默认），其他值存起来；lines 是当前 relay.conf 行。
func setMultiplierLine(lines []string, suffix string, mult float64) []string {
	if m := normalizeTrafficMultiplier(mult); m != 1 {
		return setRelayConfValue(lines, "MULTIPLIER"+suffix, strconv.FormatFloat(m, 'f', -1, 64))
	}
	return removeRelayConfValue(lines, "MULTIPLIER"+suffix)
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
		// 流量倍率（v1.3.40）：没填/非法按 1 算（见 multiplier()），不报错。
		if mv, err := strconv.ParseFloat(get("MULTIPLIER"), 64); err == nil {
			l.TrafficMultiplier = mv
		}
		// NEEDS_UDP（v1.3.46）：没填/非法 = -1（未知，老 relay.conf）。
		l.NeedsUDP = -1
		if nv, err := strconv.Atoi(get("NEEDS_UDP")); err == nil {
			l.NeedsUDP = nv
		}
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
	return []string{"LANDING" + suffix, "LANDING_IS_DOMAIN" + suffix, "DOMAIN" + suffix, "CERT_DOMAIN" + suffix, "CERT_MODE" + suffix, "MULTIPLIER" + suffix, "PRESET" + suffix, "MODE_NOTE" + suffix, "MODE" + suffix, "PORTS" + suffix, "NAME" + suffix, "NEEDS_UDP" + suffix}
}

// needsUDPKey NEEDS_UDP 键名（编号 1 用平铺键，>=2 用 _N 后缀，v1.3.46）。
func needsUDPKey(index int) string {
	if index == 1 {
		return "NEEDS_UDP"
	}
	return "NEEDS_UDP_" + strconv.Itoa(index)
}

// computeNeedsUDP 按落地实际节点协议算 NEEDS_UDP（v1.3.46）：有 Hysteria2/TUIC
// 节点→1（走 UDP），其余协议→0；链接解析不出（没贴原始链接等）时回落到
// 当初勾没勾 WANT_UDP。注意：不能按 WANT_UDP 算——香港 2 的落地 1 就是
// 预设 3 + WANT_UDP=y 勾出来的 VLESS，UDP 根本用不上。
func computeNeedsUDP(origLink string, wantUDP bool) int {
	if pl, err := parseNodeLink(origLink); err == nil {
		switch pl.Scheme {
		case "hysteria2", "hy2", "tuic":
			return 1
		default:
			return 0
		}
	}
	if wantUDP {
		return 1
	}
	return 0
}

// readUseUDP 读 config.toml 里 use_udp 的值（v1.3.46）：返回 (值，找没找到)。
func readUseUDP(tomlData []byte) (bool, bool) {
	for _, l := range strings.Split(string(tomlData), "\n") {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "use_udp") {
			continue
		}
		eq := strings.Index(t, "=")
		if eq < 0 {
			continue
		}
		v := strings.ToLower(strings.TrimSpace(t[eq+1:]))
		v = strings.Trim(v, `"`)
		if ci := strings.Index(v, "#"); ci >= 0 {
			v = strings.TrimSpace(v[:ci])
		}
		if v == "true" {
			return true, true
		}
		if v == "false" {
			return false, true
		}
	}
	return false, false
}

// setUseUDP 改 config.toml 里 use_udp 开关（v1.3.46）：只改已有的 use_udp 行
// （行尾注释保留）；没有这行时返回 false——缺行由 ensureUseUDP 经
// appendUseUDPLine 补（v1.4.19），不要在这里凭空加行。
func setUseUDP(tomlData []byte, on bool) ([]byte, bool) {
	lines := strings.Split(string(tomlData), "\n")
	found := false
	want := "use_udp = false"
	if on {
		want = "use_udp = true"
	}
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "use_udp") {
			continue
		}
		eq := strings.Index(t, "=")
		if eq < 0 {
			continue
		}
		rest := strings.TrimSpace(t[eq+1:])
		comment := ""
		if ci := strings.Index(rest, "#"); ci >= 0 {
			comment = " " + strings.TrimSpace(rest[ci:])
		}
		lines[i] = want + comment
		found = true
	}
	if !found {
		return tomlData, false
	}
	return []byte(strings.Join(lines, "\n")), true
}

// appendUseUDPLine config.toml 里没有 use_udp 行时补一行（v1.4.19）：
// 已有 [network] 小节就把 use_udp = true 插到它后面；没有就追加
// [network] 小节。TOML 表顺序无所谓，插哪都行。返回（新 toml，是否改过）。
// v1.4.18 的缺口：setUseUDP 只改已有行，缺行时 ensureUseUDP 静默跳过，
// UDP 路由加了也不生效（中转机 config.toml 从来只有 TCP 转发、从来没有
// use_udp 行，真机验收 2026-11-24 当场发现，只能手动补行）。
func appendUseUDPLine(tomlData []byte) ([]byte, bool) {
	lines := strings.Split(string(tomlData), "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") {
			continue
		}
		if t == "[network]" || strings.HasPrefix(t, "[network] ") || strings.HasPrefix(t, "[network]\t") || strings.HasPrefix(t, "[network]#") {
			out := make([]string, 0, len(lines)+1)
			out = append(out, lines[:i+1]...)
			out = append(out, "use_udp = true")
			out = append(out, lines[i+1:]...)
			return []byte(strings.Join(out, "\n")), true
		}
	}
	s := string(tomlData)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return []byte(s + "[network]\nuse_udp = true\n"), true
}

// ensureUseUDP needUDP 时保证 config.toml 的 use_udp 是 true（v1.3.46）；
// 不需要时保持原值，绝不乱关。v1.4.19：use_udp 行缺失时自动补上
// （appendUseUDPLine），不再静默跳过。返回 (新 toml, 是否改过)。
func ensureUseUDP(tomlData []byte, needUDP bool) ([]byte, bool) {
	if !needUDP {
		return tomlData, false
	}
	if on, ok := readUseUDP(tomlData); ok && on {
		return tomlData, false
	}
	if out, ok := setUseUDP(tomlData, true); ok {
		return out, true
	}
	return appendUseUDPLine(tomlData)
}

// ensureUseUDPForConf 任一落地 PORTS 里有 udp: 条目就保证 config.toml 的
// use_udp 是 true（v1.3.46）；没有 udp: 条目时保持原值，绝不乱关。
func ensureUseUDPForConf(rc *RelayConf, tomlData []byte) ([]byte, bool) {
	need := false
	for _, l := range rc.Landings {
		for _, p := range l.Ports {
			if p.Proto == "udp" {
				need = true
			}
		}
	}
	return ensureUseUDP(tomlData, need)
}

// ---------- 服务器 ----------

type Server struct {
	env                   Env
	cfg                   PanelConfig
	cfgPath               string
	state                 PanelState
	stateMu               sync.Mutex
	sessions              *sessionStore
	clientSessions        *clientSessionStore // 客户端会话令牌（v1.3.45，只存 SHA-256 哈希）
	opMu                  sync.Mutex          // 写操作锁（TryLock，占用时返回 409）
	traffic               *trafficStore
	userTraffic           *userTrafficStore     // 每订阅流量累计（v1.3.25，PANEL_HOME/user_traffic.json）
	nodePushSeen          *nodePushSeenStore    // 节点侧推送去重（v1.3.30，PANEL_HOME/node_push_seen.json）
	nodeTrafficSeen       *nodeTrafficSeenStore // 后端节点流量窗口去重（v1.4.4，PANEL_HOME/node_traffic_seen.json）
	nodes                 *nodeBackendStore     // 轻量节点后端（v1.4.0，PANEL_HOME/nodes.json）
	trafficErrMu          sync.Mutex            // 流量采样失败记录锁（v1.3.27：失败不再静默）
	trafficErrMsg         string                // 最近一次流量采样/建链失败原因（空 = 正常）
	trafficErrAt          time.Time
	auditMu               sync.Mutex
	healthMu              sync.Mutex // 健康基线锁（lastHealthOK/lastHealthAt：healthTick 与模式切换共用）
	lastHealthOK          bool
	lastHealthAt          time.Time
	monitorMu             sync.Mutex
	monitorAlerts         map[string]*monitorAlertState
	latestMonitor         *SystemStats
	latency               *latencyStore
	latencyMu             sync.Mutex
	latencyAlerts         map[string]*latencyAlertState
	latencyRoundMu        sync.Mutex // 一轮探测的锁（后台循环与总览兜底互斥）
	centralMu             sync.Mutex // 集中管理缓存锁
	centralCache          map[string]*RemoteStatusSnapshot
	centralAlert          map[string]*centralAlertState
	centralUsersFailAudit map[string]time.Time // 集中用户表兜底同步失败审计节流（v1.3.36）：每远程每小时最多记一次
	centralUserUsageMu    sync.Mutex           // 按订阅聚合用量缓存锁（v1.3.25）
	centralUserUsage      map[string]*centralUserUsage
	centralPollWG         sync.WaitGroup // 后台轮询计数（代理写成功后异步刷新一次；测试等它跑完再清理，避免轮询撞上 TempDir 删除）
	subKey                []byte         // 订阅令牌加密密钥（v1.3.7，PANEL_HOME/sub_token.key 的内容，懒加载）
	subKeyMu              sync.Mutex
	subFailMu             sync.Mutex // /sub/ 限流锁（v1.3.7）
	subFails              map[string]*subFailState
	remoteUploadMu        sync.Mutex // 异地备份上传去重锁（v1.3.11）
	remoteUploading       map[string]bool
	certMu                sync.Mutex // 面板自身证书 ACME 操作锁（手动签发与后台续签互斥）
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
	s := &Server{env: env, cfgPath: filepath.Join(env.PanelHome, "config.json"), sessions: newSessionStore(), clientSessions: newClientSessionStore(), traffic: newTrafficStore(filepath.Join(env.PanelHome, "traffic.json")), userTraffic: newUserTrafficStore(filepath.Join(env.PanelHome, "user_traffic.json")), nodePushSeen: newNodePushSeenStore(filepath.Join(env.PanelHome, "node_push_seen.json")), nodeTrafficSeen: newNodeTrafficSeenStore(filepath.Join(env.PanelHome, "node_traffic_seen.json")), nodes: newNodeBackendStore(env.PanelHome), lastHealthOK: true, monitorAlerts: map[string]*monitorAlertState{}, latency: newLatencyStore(filepath.Join(env.PanelHome, "latency.json")), latencyAlerts: map[string]*latencyAlertState{}, centralCache: map[string]*RemoteStatusSnapshot{}, centralAlert: map[string]*centralAlertState{}, centralUsersFailAudit: map[string]time.Time{}, centralUserUsage: map[string]*centralUserUsage{}, subFails: map[string]*subFailState{}}
	if err := s.loadConfig(); err != nil {
		return nil, err
	}
	// v1.4.28 启动自检：订阅用户 TokenHash 与 TokenEnc 失配时只写服务端
	// 日志告警（checkSubTokenPairs），不拦启动、不自动轮换。
	s.checkSubTokenPairs()
	if err := s.loadState(); err != nil {
		return nil, err
	}
	s.traffic.load()
	s.userTraffic.load()
	s.nodePushSeen.load()
	s.nodeTrafficSeen.load()
	s.nodes.load()
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
	// v1.4.40 迁移：旧合并订阅已彻底移除（用户 2026-11-28 13:53 拍板，
	// 失效的直接移除、不留只读列表）。结构体里已无 merged_subs 字段，
	// 上面这行 unmarshal 本来就忽略它；这里探到文件里还残留记录就立
	// 即落盘清掉（saveConfig 按新结构体写，merged_subs 键就没了），
	// 并记一条日志。清盘失败只记日志、不拦启动（记录反正不再被读）。
	var probe struct {
		MergedSubs []json.RawMessage `json:"merged_subs"`
	}
	if json.Unmarshal(data, &probe) == nil && len(probe.MergedSubs) > 0 {
		n := len(probe.MergedSubs)
		if err := s.saveConfig(); err != nil {
			log.Printf("旧合并订阅迁移：config.json 里 %d 条已退场记录清盘失败：%v（记录已不再被读）", n, err)
		} else {
			log.Printf("旧合并订阅迁移：已从 config.json 移除 %d 条已退场的旧合并订阅记录（B 方案，旧链接已失效）", n)
		}
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
// 23:59:59（浏览器所在时区）；RFC3339 全串原样接受。返回规范化后的
// RFC3339（空 = 永久），非法返回错误。
func parseSubExpiry(v string) (string, error) {
	return parseSubExpiryIn(v, "")
}

// parseSubExpiryIn 同 parseSubExpiry，但纯日期按 tz（IANA 时区名，如
// "Asia/Shanghai"）算当天结束。tz 为空或 LoadLocation 失败时回退
// time.Local（老客户端/纯 API 调用不带时区，行为与原来一致）。
// v1.3.32：以前纯日期按服务器本地时间（EST）算当天结束，CST 用户选
// 12 月 1 日会存成 2026-12-02T04:59:59Z、显示与实际到期都跳到 2 号。
// 日期是在浏览器里选的，“那天”只能是浏览器所在时区的当天。
func parseSubExpiryIn(v, tz string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	loc := time.Local
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	if d, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		end := time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, loc)
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
