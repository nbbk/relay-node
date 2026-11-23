# 订阅网关（Subscription Gateway）

一个域名，三条路，全部挂在网关域名下面：

| 路径 | 去向 | 说明 |
| --- | --- | --- |
| `/<订阅路径>/* 与 /sub/*` | 面板回源 | 订阅拉取：路径里的令牌、查询参数原样透传；响应头（`subscription-userinfo`、Content-Disposition 文件名）原样回传。订阅路径是面板的自定义值（面板 1.3.20 反特征，默认 `sub`），两条都接 |
| `/api/v1/public-info` | 面板回源 | 公告 |
| `/client-config.json` | 本地签名文件 / 上游直链 | 远程配置：字节精确，半个字节都不能差（客户端 Ed25519 验签） |
| `/healthz` | 网关自己 | 自检，返回 `ok` |

网关不知道你的节点、用户、令牌是什么，也不存它们——它只负责把请求原样送到面板、把响应原样送回来，外加一份签好的远程配置文件。

> 与面板里“订阅网关”卡片的关系：这份是通用版，里面的值都是占位（`example.com`、`ccpanel.example.com`），按自己面板的实际值改了再部署。面板 1.3.50 起在设置页订阅位置加了“订阅网关”卡片，点部署方式就能直接生成填好当前值的版本（回源地址、订阅对外域名、订阅路径都是面板当前值，不用再填），还能一键下载每档的文件、点“检测网关”看三条路通不通。日常用面板生成的那份就行；这份通用版是给想手动部署、或看明白网关是怎么搭的人准备的。两份内容同源：面板生成用的模板就是这里的这几个文件，只差填进去的值。

## 为什么要网关

原来订阅域名与客户端远程配置只走 Cloudflare Worker 一条路，三个问题：

1. Worker 是唯一选择，Worker 一出问题订阅就断；
2. 面板开关 HTTPS 后要同步改 Worker 回源协议，忘了改订阅就断；
3. 线上跑的 Worker 只接了 `/sub/*`，公告与远程配置经它还是 404（2026-11-23 实测）。

网关域名是稳定层：背后换哪种部署，客户端不用动。面板的订阅对外域名本来就是手动填的，改填网关域名就行；面板 1.3.50 起在设置页订阅位置有了“订阅网关”卡片，能直接生成填好当前值的部署代码、下载文件、检测三条路。

## 五个部署方式，怎么选

| 方式 | 要不要服务器 | 费用 | HTTPS 怎么办 | 搭建耗时 | 适合 |
| --- | --- | --- | --- | --- | --- |
| A. Cloudflare Worker（`worker/`） | 不要 | 免费额度够用 | Cloudflare 自带 | 10 分钟 | 已有 Worker、想零服务器 |
| B. Caddy 一键容器（`caddy/`） | 要一台任意服务器 | 服务器钱 | Caddy 自动签（HTTP-01，需 80 端口放行） | 15 分钟 | 有服务器、想一键起 |
| C. 脚本直装（`scripts/install.sh`） | 要一台任意 Linux 机（面板机也行） | 服务器钱 | 同上（Caddy 自动签） | 15 分钟 | 不想用 Docker |
| D. 静态托管（`static/`） | 不要（GitHub Pages / nginx 静态目录） | 免费 | 托管方自带 | 10 分钟 | 只解决远程配置；订阅与公告仍要走 A/B/C |
| E. Vercel / Netlify（`serverless/`） | 不要 | 免费额度够用 | 平台自带 | 10 分钟 | 想再多一个备份；本地未验证实际部署，首次部署后按下面自测 |

细步骤见各目录的 README。

## DNS 怎么设

- 网关域名建 A 记录指到服务器 IP（用 B/C 时）。
- 域名 DNS 在 Cloudflare 时用**灰云 DNS-only**：网关是反代层，不要走橙云代理（代理层会插手缓存与响应头，订阅出问题难查；11111 端口也走不了橙云代理）。
- 换部署方式：只改 A 记录指向（或面板订阅对外域名），客户端不用动。

## client-config.json 怎么更新

远程配置文件是签过名的，网关只负责字节精确地送到客户端，不验签（验签是客户端拉取时做的事）。

1. 签名：在客户端仓用 `tools/remote-config/sign.py` + 私钥签好新的 `client-config.json`（私钥在 `~/workspace/user/remote-config-private-key.pem`，不要进仓库）。
2. 同步到网关，二选一：
   - 本地上传：`CONFIG_DIR=<网关配置目录> bash scripts/update-config.sh <签好的 client-config.json>`（原子写，写完打印新旧 sha256；Caddy 不用重启，立即生效）。
   - 从上游拉取：`bash scripts/update-config.sh --from-url <GitHub Raw 直链>`；或把直链记下来定时同步（`scripts/install.sh` 装的时候会问，记在 `/etc/sub-gateway/upstream.url`）。
   - Worker / Vercel / Netlify：改 `CONFIG_UPSTREAM` 变量指向新的直链，重新部署。
3. 验证：`curl https://<网关域名>/client-config.json | sha256sum`，跟签名时那份的 sha256 对一下；再在客户端里拉一次远程配置，看版本变了没有。

轮换密钥时：先把新公钥编进客户端发版，再用新私钥签配置；顺序反了客户端会验签失败回退旧配置。

## 排障

### 订阅突然断了：先看回源协议（最常见）

回源协议必须跟面板当前状态一致：

- 面板 HTTP 时回源填 `http://...:11111`
- 面板 HTTPS 时回源填 `https://...:11111`

面板开关 HTTPS 后，要同步改回源（Worker 改 `ORIGIN` 重新部署；Caddy 改 `.env` / Caddyfile 里的回源地址再 `docker compose restart` / `systemctl reload sub-gateway`），不改就断。

症状对照：

- 回源是 `http://` 但面板已是 HTTPS：网关日志出现 `Client sent an HTTP request to an HTTPS server`（Go 的 TLS 栈回的 400），或 `502 Bad Gateway`。
- 回源是 `https://` 但面板是 HTTP：TLS 握手失败，网关报 `502`。
- 直连面板 `https://<面板域名>:11111/sub/...` 通、经网关不通：基本就是回源协议或回源地址错了。
- 直连面板也不通：问题在面板侧，不在网关。

### 面板是自签证书

Caddy 反代默认要验上游证书，自签会失败（`502`）。过渡期可以在 Caddyfile 里取消 `tls_insecure_skip_verify` 三行的注释（有中间人风险，仅过渡用）；脚本直装时选“自签 y”会自动放开。正式运行请给面板签真证书（面板自带 ACME DNS-01）。

### Caddy 拿不到证书

自动签证书走 HTTP-01，要 80 端口放行且域名 A 记录已指过来。80 被占或拿不到时：放行 80 端口，或改走 DNS-01（见 `caddy/README.md`），或先用已有的证书。

### /client-config.json 404

- Caddy/脚本：文件没放进配置目录（`CONFIG_DIR`）。放进去立即生效，不用重启。
- Worker：`CONFIG_UPSTREAM` 没配，或上游直链错了（GitHub Raw 须是 `raw.githubusercontent.com` 的直链）。

### 公告 / 远程配置经 Worker 404

线上老 Worker 只有 `/sub/*` 路由。用 `worker/worker.js` 重新部署就全通。

### 订阅返回 200 但 0 节点

不是网关问题。2026-11-23 实测：同一条订阅经 Worker 与直连面板都返回 200，但 0 节点（06:07 联调时还是 2 节点），是面板侧合并订阅当前没节点。去面板侧查：合并订阅的远程来源是否在线、订阅配置是否被改过。

## 测试

本地测试（桩回源 + 真 Caddy + Node 直跑 Worker 逻辑）：

```bash
bash tests/run-tests.sh
```

47 项全过（2026-11-23 实测）：订阅三格式（头、文件名、节点数）、自定义订阅路径（/x9f2/ 与 /sub/ 兜底，真 Caddy 与 Node 各测）、公告、远程配置字节精确（sha256 对照）、其它路径 404、回源收到的 Host 与 UA、Worker 路由 5 条、install.sh 干跑两档、update-config.sh 三档、配置文件语法。

没条件验的（实话）：Docker 容器实跑（本机没 Docker，compose 只做了 YAML 校验）、真面板、真域名、真机、Vercel/Netlify 实际部署。首次上真机按各目录 README 自测三条路。

## 脱敏

本文档与套件里的脚本、示例一律用 `example.com`、`203.0.113.1`、假 UUID `11111111-2222-3333-4444-555555555555`，不要出现真实域名与 IP。`.env` 不要进公开仓库。
