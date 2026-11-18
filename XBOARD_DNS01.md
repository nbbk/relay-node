# Xboard 节点用 DNS 验证拿证书（域名永久指中转，不用来回改）

## 为什么要这样做

现在要改来改去域名指向，根因是证书续签走 HTTP-01：签证书和续签都要求域名解析到落地机，流量又要求域名解析到中转机，两边抢一个 A 记录。

改走 DNS 验证（DNS-01）就没这个矛盾了：签证书直接找 Cloudflare API 写一条 `_acme-challenge` TXT 记录验证域名所有权，**完全不看 A 记录指到哪、不占 80 端口**。域名从一开始就指中转机，永久不用动；续签由 Xboard-Node 内置的 ACME 客户端全自动完成。

前提：域名 DNS 托管在 Cloudflare（Xboard-Node 的 DNS 验证支持 cloudflare 和 alidns，本教程只写 Cloudflare）。

## 1. 开一个 Cloudflare API 令牌

按 `CLOUDFLARE_TOKEN.md` 一步步开（本仓库同目录有这份教程）：

- My Profile > API Tokens → Create Token → 选 **Edit zone DNS** 模板
- 权限只留 **Zone | DNS | Edit** 和 **Zone | Zone | Read**
- Zone Resources 选 Include → Specific zone → 你的域名（不要 All zones）
- 建好复制令牌（只显示一次，新令牌是 `cfut_` 开头）

令牌只用来写验证用的 TXT 记录，不要用 Global API Key。

## 2. 落地机上配 Xboard-Node（DNS 验证模式）

落地机上建工作目录，写 `config.yml`，证书这段是关键：

```yaml
# 面板对接信息
panel:
  url: "https://example.com"        # 你的 Xboard 面板地址
  token: "xxxxxxx"                 # 面板里的通信密钥
  node_id: 8                       # 面板里的节点 ID

kernel:
  type: "singbox"                  # 或 "xray"

# TLS 证书（DNS 验证模式）
cert:
  cert_mode: "dns"                 # dns = DNS-01 验证，不用 80 端口
  domain: "node.example.com"       # 节点域名
  email: "admin@example.com"
  dns_provider: "cloudflare"       # 支持 cloudflare、alidns
  dns_env:
    CF_API_TOKEN: "cfut_你的令牌"   # 第 1 步开的令牌，只存这个文件里
```

`docker-compose.yml`（host 网络，不用映射端口）：

```yaml
services:
  xboard-node:
    image: ghcr.io/cedar2025/xboard-node:latest
    container_name: xboard-node
    restart: always
    network_mode: host
    volumes:
      - ./config.yml:/etc/xboard-node/config.yml
      - ./certs:/etc/xboard-node/certs
```

启动：

```bash
docker compose up -d
docker logs -f xboard-node
```

看到下面三行就是成了：

```
DNS-01 challenge
certificate obtained successfully
TLS certificate synced
```

## 3. 域名直接指中转机（一次，永久）

把节点域名 `node.example.com` 的 A 记录指到**中转机 IP**，以后再也不用动。

顺序无所谓：域名可以一开始就指中转机——因为签证书走的是 Cloudflare API，不需要域名先指落地。签完证书、装完中转（relay-node 预设 1，80+443），客户端用原来的订阅链接就能走中转。

多节点：每个节点域名各走一次上面的流程；或者申请一张泛域名证书 `*.example.com`，一张证书通吃所有子域名节点（证书文件在 `./certs` 里，节点按需引用）。

## 4. 验证

- 面板里节点显示在线。
- 拿订阅链接测一条直连、一条中转，对比延迟和速度。
- 证书续签不用管：到期前 ACME 客户端自动续签。中转机的 `check.sh` 第 5 节会显示每张证书的剩余天数，顺手看一眼就行。

## 排错

- **端口被占**：日志报 `bind: address already in use`，是落地机上已有程序（比如 Nginx）占了面板下发的 80 或 443。停掉它，或在面板里改节点服务端口。
- **被限流**：日志报 `too many failed authorizations`，是配置写错导致反复签失败，被 Let's Encrypt 封禁。先关掉容器、改对配置，**等 1 小时**后再重启。
- **令牌不对**：确认令牌是 `cfut_` 开头、权限只有 Zone DNS Edit + Zone Zone Read、Zone 只选你的域名。令牌泄露了就删掉重建，证书不用重签。
- **域名不要开 Cloudflare 小云朵**：节点域名保持仅 DNS（灰云），不要代理。开了小云朵流量会先过 Cloudflare，节点就不是直连了。

## 和现有落地节点的关系

tiktok-node、sing-box-node 的安装里早就有了同样的选项：装到选证书方式那一步选 **[2] DNS 验证 Cloudflare**，效果一样，域名同样永久指中转。已装好的旧节点想换成 DNS 验证，下次重装时选 [2] 即可。
