# 选项 B：Caddy 一键容器

## 前置

- 一台任意服务器（面板机也行），装好 Docker 与 Docker Compose
- 一个域名，A 记录已指到这台服务器 IP（域名 DNS 在 Cloudflare 时用灰云 DNS-only）
- 服务器 80 与 443 端口放行（Caddy 自动签证书走 HTTP-01，要 80 端口）

## 步骤

```bash
cd caddy
cp .env.example .env
```

填 `.env` 四个值（示例里是 `example.com`，按实改）：

```ini
GATEWAY_DOMAIN=example.com
PANEL_ORIGIN=https://ccpanel.example.com:11111
SUB_PATH=sub
CONFIG_DIR=/data/config
```

注意 `PANEL_ORIGIN` 的协议必须跟面板当前状态一致：面板 HTTP 填 `http://`，面板 HTTPS 填 `https://`。面板开关 HTTPS 后同步改这里。`SUB_PATH` 是面板自定义订阅路径段（面板 1.3.20 反特征）：没改过填 `sub`，改了填改的值，例如 `x9f2`；面板改了订阅路径后要同步改这里，改完 `docker compose restart`。

把签好的 `client-config.json` 放进 `data/config/`（没有就先空着，`/client-config.json` 会 404）：

```bash
mkdir -p data/config
cp /path/to/client-config.json data/config/
```

启动：

```bash
docker compose up -d
docker compose ps
docker logs sub-gateway
```

## 自测三条路（把 example.com 换成你的网关域名）

```bash
# 1. 订阅：应返回 200，响应头带 subscription-userinfo
curl -s -D - -o /dev/null 'https://example.com/sub/11111111-2222-3333-4444-555555555555?format=singbox' | grep -i 'subscription-userinfo'

# 2. 公告：应返回 JSON（login_notice）
curl -s https://example.com/api/v1/public-info

# 3. 远程配置：sha256 应与签好的那份一致
curl -s https://example.com/client-config.json | sha256sum

# 4. 自检
curl -s https://example.com/healthz
```

## 日常操作

```bash
# 改回源地址（面板开关 HTTPS 后）：改 .env 的 PANEL_ORIGIN，再
docker compose restart

# 更新远程配置（文件放主机目录，容器是只读挂载，主机上能写）：
CONFIG_DIR=$PWD/data/config bash ../scripts/update-config.sh <签好的 client-config.json>

# 看日志（排障先看这里）
docker logs sub-gateway --tail 100

# 停掉
docker compose down
```

## 面板是自签证书

Caddyfile 里 `tls_insecure_skip_verify` 三行默认是注释掉的。面板自签时取消注释再 `docker compose restart`（有中间人风险，仅过渡期用；正式运行请给面板签真证书）。

## 80 端口被占 / 拿不到证书

- 80 端口被别的程序占了：停掉它，或换一台服务器。
- 不想走 HTTP-01：Caddyfile 里把自动签改成 DNS-01（需 DNS 服务商插件，`caddy:2-alpine` 镜像不带插件，要自己 build 镜像；或先用脚本直装，装的时候一样处理）。
