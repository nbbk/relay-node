# DNS 服务商（ACME DNS-01 签真证书）

面板绑域名开 HTTPS 时，用 DNS-01 验证签 Let's Encrypt 真证书（不占 80 端口）。
设置页 → HTTPS 卡片 → 选服务商，填凭证，点开启。凭证只存 config.json（600 权限），不回显。

## 各服务商要填什么

| 服务商 | 填什么 | 去哪申请 |
|---|---|---|
| Cloudflare（默认） | API Token | Cloudflare 后台 → 我的个人资料 → API 令牌，开 Zone:DNS:Edit 权限的 Token（cfut_ 开头）。详见 CLOUDFLARE_TOKEN.md |
| 阿里云 DNS | AccessKey ID、AccessKey Secret | 阿里云控制台 → AccessKey 管理。域名要先在阿里云 DNS 解析控制台添加 |
| 腾讯云 DNSPod | Token ID、Token | DNSPod 控制台 → 我的账号 → API Token（经典版，dnsapi.cn）。域名要先在 DNSPod 添加 |
| AWS Route 53 | AccessKey ID、Secret Access Key | AWS 控制台 → IAM，建一个用户（建议只给 Route53 相关权限，不要给管理员权限），拿它的 AccessKey。域名要先在 Route 53 建公有 Hosted Zone |
| GoDaddy | Personal Access Token（PAT） | GoDaddy 开发者后台（developer.godaddy.com）开 PAT，权限要含 domains.domain:read 与 domains.dns:update。域名要在 GoDaddy 的 DNS 托管 |
| Namecheap | API User、API Key、Client IP | Namecheap 后台 → Profile → Security → API Access：开 API、拿 Key、把本机公网 IPv4 加进白名单。域名要用 Namecheap BasicDNS |
| 手动模式 | 不用填 | 域名在哪都行，见下 |

## 坑点

- **Namecheap 白名单 IP**：调用方 IPv4 必须在 Namecheap 后台白名单里，且 Client IP 要填本机公网 IPv4（不是面板的内网 IP）。没加白名单时 API 直接拒绝，报错里会明说白名单这一点。只支持 BasicDNS 的域名（PremiumDNS/FreeDNS、或指到别家 DNS 的管不了）。
- **GoDaddy 用 v3＋PAT，不用 v1**：v1 的 sso-key 已被 GoDaddy 官方列为计划废弃（through 2026），v3 于 2026-08-10 发布。v3 是单条记录增删，不会一把冲掉同名记录。
- **Route 53 / Namecheap 是整组回写**：Route 53 的 UPSERT 会替换同名 TXT 整组，Namecheap 的 setHosts 会替换域名下全部记录。面板在建/删时都会先读现有值再合并写回，不会删掉你的其它记录。
- **切换服务商不影响旧凭证**：切到别家再切回来，之前填过的凭证还在。换服务商或换凭证后，之前签发失败的原因会自动清掉。
- **自动清理残留**：建新 TXT 记录前，面板先删掉同名残留记录（只删名字与类型都对得上的）。万一清理没成功，报错里会写去哪手动删。

## 手动模式（通用兜底）

域名在任何一家 DNS（包括上面没有的）都能签，不用填凭证：

1. 服务商选“手动添加 TXT”，填域名，点“开启 HTTPS”。
2. 面板先下单，把 TXT 记录名（`_acme-challenge.<域名>`）与值显示在 HTTPS 卡片上。
3. 去你的 DNS 后台加这条 TXT 记录（类型 TXT，值照抄），等几分钟让它生效。
4. 回面板点“已添加，继续验证”。面板先查本机 DNS 能不能看到这条记录（最多等 2 分钟），看不到就报错但不提交——订单还在，等几分钟再点。验证通过后证书签下，面板重启生效。

注意：

- 订单 1 小时过期，过期作废、请重新开始。订单状态存在 `$PANEL_HOME/certs/acme-pending.json`（600 权限），面板重启后还在，不用担心重启丢单。
- 一旦提交后验证失败（记录加错了、值抄错了），订单作废，须重新开始拿新的 TXT 值。
- 手动模式不自动续签：真证书到期前（面板会显示剩余天数），手动再走一遍两步签发。
