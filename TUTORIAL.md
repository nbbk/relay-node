# 中转节点详细教程（relay-node）

> 本教程覆盖**安装、卸载/删除、排查运维**三部分，所有命令都可直接复制跑。
> 简版说明看 [README.md](README.md)。中转核心是 [realm](https://github.com/zhboner/realm)（v2.9.6，Rust 纯转发）。
>
> 先记住架构：客户端 → **中转机（realm 原样转发，不解密）** → 落地机（你现成的 tiktok-node / sing-box-node）。
> TLS 握手是客户端和落地机之间端到端完成的，中转看不到、也改不了任何内容。

---

## 零、先搞懂为什么（新手必读）

看不懂原理也能照着装，但这几条搞懂了，出问题才知道查哪里。

### 落地填域名，中转填 IP

- **落地机装节点时填域名**：证书是按域名签的。有证书才有 TLS，流量看起来就是普通网站、不容易被识别；以后换服务器、换地区，域名不变、只改 A 记录，客户端不用动。填 IP 只能跑明文（没证书），临时测试可以，长期运营不合适。
- **中转机装 relay 时填落地 IP**：转发和直连那条都直接用这个 IP，不经过 DNS；也不会因为域名改指后解析到中转机自己身上、把直连指错地方。

### 域名为什么要分两步指

一个域名同一时间只能指到一台机器，所以分两步：

1. **先指到落地机**：装节点时 Caddy 靠这个域名签证书，签完直接验证节点能用。
2. **中转装好、验证通过后，再改指到中转机 IP**：之后客户端连域名先到中转机，再转给落地。证书还是原来那张，不用重签；续签也照常走转发的 80 端口。

改完等 DNS 生效一下（一般几分钟），用手机浏览器或 `dig 你的域名` 查一下返回的是不是中转机 IP，确认变了再测节点。

### 域名改指后，原来的节点为什么还能用，还能同时留两条

域名改指后，原来那条链接不用改、自动走中转；再加一条直连就行：

| 节点 | 地址填什么 | 怎么来的 |
|---|---|---|
| 中转 | 域名（已指到中转机） | 原来那条链接，原样不动 |
| 直连 | 落地 IP，SNI 填域名 | 安装最后一步粘贴原链接自动生成 |

直连那条关键是 **SNI 必须填域名**：证书是按域名签的，SNI 不填域名、证书对不上，握手会失败。两条同时能用，中转挂了切直连，直连线路差切中转，互为备份。

### 中转机端口为什么要空着

中转机要监听 80、443 这些端口来收客户端流量，一个端口同一时间只能被一个程序占着。如果中转机上已经跑了网站、面板或者另一个节点，realm 就监听失败、装不上。安装脚本装之前会先检查，被占了会直接退出告诉你。80 还要留给证书续签用（验证请求走 80 端口）。

### 双节点链接和二维码是怎么来的

安装最后一步会让你粘贴原来的节点链接，粘完直接给你两条链接（中转、直连）和两个二维码，存进 `~/relay-node-kit/nodes.txt`。当时跳过也没事，装完随时跑 `bash install.sh --nodes` 就能单独补生成一次，不用重装。二维码要机器上有 qrencode，没有它会自己试着装，装不上也不影响，链接照常给你、复制导入就行。

### 总流程（照这个顺序做）

1. 落地机装 tiktok-node / sing-box-node（填域名），确认节点能用。
2. 落地机跑 `landing.sh` 检查，记下落地 IP。
3. 中转机装 relay-node（填落地 IP、选预设），最后粘贴原链接拿双节点。
4. 域名 A 记录改指到中转机 IP，等 DNS 生效。
5. 测中转节点能用，再测直连节点能用，两条都留着。

---

## 一、安装篇

### 1.1 前置条件

| 项目 | 要求 |
|---|---|
| 中转服务器 | Ubuntu / Debian，root 权限，x86_64 或 aarch64 架构 |
| 落地服务器 | tiktok-node 或 sing-box-node 已在跑，记下它的 IP 或域名 |
| 域名 | 一个已解析到落地的域名（改指用；没有域名也能中转，见 1.5 第二种用法） |
| 端口 | 中转机要转发的端口全部空闲（安装脚本装前会自动检查，被占会直接退出） |
| 云厂商安全组 | 中转机放行要转发的端口，**TCP 和 UDP 分开算**：放了 8388/TCP 不等于放了 8388/UDP；落地机也要放行这些端口给中转机访问 |

**中转机选址（直接决定提速效果）：** 离用户近、到落地线路好。常见搭配：用户在国内 → 中转选香港/新加坡/日本；落地在印尼 → 中转选新加坡。不要只看中转机到你的延迟，中转到落地那段同样重要。

**先确认中转机架构**（x86_64 还是 aarch64，装错包跑不起来）：

```bash
uname -m
# 输出 x86_64 或 aarch64
```

### 1.2 落地侧检查（不用装任何东西）

在**落地服务器**上跑一条命令，确认节点在跑，并打印中转安装要填的信息：

```bash
curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/landing.sh | bash
```

输出示例（tiktok-node 在跑）：

```text
== 1/4 落地节点容器 ==
运行中的相关容器：
tiktok-caddy Up 3 days
tiktok-xray Up 3 days
识别到：tiktok-node（Xray），中转预设选 1

== 2/4 端口监听 ==
  [监听中] 80/tcp
  [监听中] 443/tcp
  [未监听] 8080/tcp
  ...

== 3/4 本机公网 IP ==
公网 IP：202.155.138.195

== 4/4 给中转侧安装用的信息（照抄过去填） ==
  落地地址：202.155.138.195
  预设：1（tiktok-node：80 + 443）
  落地不用装任何东西，也不用重启；装完中转后把域名 A 记录改指到中转 IP 即可。
```

**验证点：** 容器在跑、80/443 显示 `[监听中]`、公网 IP 拿到手。如果 443 显示 `[未监听]`，先去落地把节点修好再装中转（中转转发的是落地端口，落地不通中转也不通）。

**可选：把 80/443 收紧到只允许中转机访问。** 脚本最后会问你，要不要做（默认不做，直接回车跳过）。做了之后只有中转机能连落地 80/443，别人直连落地会被挡。注意两点：

- 脚本只加放行规则（`ufw allow from <中转IP> to any port 80/443`）。想彻底挡住其他 IP，要确认 ufw 默认策略是 deny：`ufw status verbose | grep Default`。如果是 allow，改默认策略会动全局规则、有锁死 SSH 的风险，脚本不会自动改，请自己在云厂商安全组里做。
- 收紧后落地证书续签不受影响：续签走转发的 80 端口，请求从中转机来，仍在放行范围内。

### 1.3 中转侧安装（一步步来）

在**中转服务器**上跑一条命令：

```bash
curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/install.sh | bash
```

也可以先把包传到中转机再装（传包安装）：

```bash
bash install.sh [落地IP或域名] [预设 1/2/3]
# 例如：bash install.sh 202.155.138.195 1
# 不给参数就交互询问
```

也可以用环境变量一次写死、不交互（适合写进自己的部署脚本）：

```bash
LANDING=202.155.138.195 PRESET=2 bash install.sh
# 自定义端口也一样：LANDING=127.0.0.1 PRESET=3 CUSTOM_PORTS="443,8388" WANT_UDP=y bash install.sh
```

安装分 6 步，每一步跑完都可以这样验证：

**第 1 步：落地目标与转发预设。** 脚本问落地 IP 或域名，再问预设：

```text
转发预设：
  1) tiktok-node（Xray）：80 + 443（TCP）
  2) sing-box-node：80 + 443（TCP）+ 8388（TCP+UDP）+ 8443（UDP）+ 8444（UDP）
  3) 自定义端口（自己输）
选预设 [1/2/3，默认 1]:
```

填写示例（落地是 tiktok-node，落地 IP `202.155.138.195`）：

```text
输入落地服务器 IP 或域名：202.155.138.195
选预设 [1/2/3，默认 1]：1
```

落地是 sing-box-node 就选 `2`，再问 TLS 还是明文时 TLS 选 `1`、明文选 `2`。

- 选 `2` 会再问落地是 TLS 模式还是明文模式：TLS 选 `1`；明文（VLESS 8080 / Trojan 8081 / VMess 8082）选 `2`。
- 选 `3` 自定义：输入端口，逗号分隔；监听端口默认与落地端口相同，如需改监听端口用 `监听:落地` 的写法，例如 `443,8388,8443:9443` 表示监听 8443 转到落地 9443；再问要不要同时转 UDP（`y` 就是全部端口都加 UDP）。

验证：屏幕打印的 `预设` 和 `转发` 两行与你的落地一致，例如：

```text
预设：sing-box-node TLS 模式
转发：tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388 udp:8443:8443 udp:8444:8444
```

**第 2 步：端口预检。** 脚本检查中转监听端口是否空闲。全部空闲会打印 `监听端口全部空闲。`；被占会直接退出并提示是哪个端口被占（见三、4）。

**第 3 步：下载 realm。** 脚本先取 GitHub 最新版（取不到就用固定回退版 v2.9.6，2026-08-30 发布），按架构下载对应包并验证能运行：

```text
最新版本：v2.9.6
下载 realm-x86_64-unknown-linux-gnu.tar.gz ...
realm 可用：realm 2.9.6
```

下载顺序是：glibc 通用版 → glibc 2.28 版 → musl 版（仅 x86_64），前一个跑不起来自动试下一个。二进制装到 `/usr/local/bin/realm`。想锁定版本：`REALM_VERSION=v2.9.6 bash install.sh`。

验证：

```bash
/usr/local/bin/realm --version
# 输出示例：realm 2.9.6
```

**第 4 步：写 realm 配置。** 生成两个文件：

- `/etc/realm/config.toml`：转发规则（每个端口一条 `[[endpoints]]`，`listen` 是中转监听，`remote` 是落地目标）+ DNS 设置（只解析落地域名用，走 TCP + 1.1.1.1/8.8.8.8，不走明文 UDP 53；`max_ttl = 300` 表示落地域名 IP 变化后最长 5 分钟自动切过去）+ 网络参数（`use_udp` 按有没有 UDP 端口自动开关；`tcp_timeout = 5`；`udp_timeout = 30`）。
- `/etc/realm/relay.conf`：状态文件（落地地址、预设、端口表），供 `check.sh` / `stop.sh` 读取。

验证：

```bash
cat /etc/realm/config.toml
```

```toml
[log]
level = "warn"
output = "/var/log/realm.log"

[dns]
mode = "ipv4_only"
protocol = "tcp"
nameservers = ["1.1.1.1:53", "8.8.8.8:53"]
min_ttl = 60
max_ttl = 300
cache_size = 32

[network]
no_tcp = false
use_udp = true
tcp_timeout = 5
udp_timeout = 30
tcp_keepalive = 15
tcp_keepalive_probe = 3

[[endpoints]]
listen = "0.0.0.0:443"
remote = "202.155.138.195:443"
```

**第 5 步：网络加速（BBR / fq / TCP 缓冲 / TFO）。** 写入 `/etc/sysctl.d/99-relay-node.conf` 并应用。中转不碰 IPv6，不禁用（只加加速，不改其他已有配置）。

验证：

```bash
sysctl -n net.ipv4.tcp_congestion_control
# 输出 bbr 就是开了；如果内核不支持 BBR，脚本会自动去掉 BBR 两行再应用，并打印提示
```

**第 6 步：启动 realm 服务。** 写 systemd 服务 `/etc/systemd/system/realm.service`，开机自启、异常 3 秒后自动拉起（`Restart=always`），然后启动。

验证：

```bash
systemctl status realm --no-pager
```

```text
● realm.service - realm relay (TCP/UDP forwarding for relay-node)
   Loaded: loaded (/etc/systemd/system/realm.service; enabled; vendor preset: enabled)
   Active: active (running) since Wed 2026-11-18 10:40:00 CST; 5s ago
 Main PID: 1234 (realm)
```

`Active: active (running)` 就是起来了。没有 systemd 的机器（极少数精简镜像）会改用后台运行（`nohup` + PID 文件 `/var/run/realm.pid`），重启后需手动再跑一次 realm，启动命令见 README 的提示。

**装完汇总。** 脚本最后打印中转 IP、落地目标、转发端口表和下一步（域名改指），然后自动跑一遍 `check.sh` 自检（见 1.7）。远程一键安装时，`check.sh` 和 `stop.sh` 会自动从仓库拉到当前目录（`~/relay-node-kit/`）。

**第 7 步：双节点链接（可选）。** 汇总之后脚本会问你要不要粘贴原节点链接，粘贴后直接给出直连 + 中转两条链接和二维码（直接回车跳过，装完随时可以只补生成一次：`bash install.sh --nodes`，它读 `/etc/realm/relay.conf` 里的落地信息，不重装）：

```text
粘贴你原来的节点链接（可多条，一行一条，粘完空行回车结束；直接回车跳过）：
vless://58087f8b-f340-4721-beb7-5aae486ba0e7@yinni.930327.xyz:443?encryption=none&security=tls&type=ws&host=yinni.930327.xyz&path=%2Fs1momobvfylwonz4#TikTok-Node
（空行回车结束）
```

输出示例：

```text
---- 第 1 条 ----
中转：vless://58087f8b-f340-4721-beb7-5aae486ba0e7@yinni.930327.xyz:443?encryption=none&security=tls&type=ws&host=yinni.930327.xyz&path=%2Fs1momobvfylwonz4#TikTok-Node
直连：vless://58087f8b-f340-4721-beb7-5aae486ba0e7@202.155.138.195:443?encryption=none&security=tls&type=ws&host=yinni.930327.xyz&path=%2Fs1momobvfylwonz4&sni=yinni.930327.xyz#TikTok-Node-%E7%9B%B4%E8%BF%9E
```

- `中转：` 是你粘贴的原链接，原样不动；域名改指到中转机后它自动走中转。
- `直连：` 是自动生成的 twin：地址换成落地 IP，其余参数（UUID、WS 路径、端口）都不动，TLS 的加 `sni=域名`、名字后加 `-直连`。**直连必须带 SNI**：直连地址是 IP，证书是按域名签的，不填 SNI 的话 Caddy 拿不出对应的证书，TLS 握手会失败。
- 生成规则：vless/trojan 换地址 + 加/改 `sni=`；ss 只换地址（不加 sni，插件参数原样保留）；vmess 解 base64 JSON 改 `add`（落地 IP）、`sni`（域名）、`ps` 后加 `-直连`（需要 python3，没有就提示手动改）；不认识的格式不报错，只提示手动改。
- sni 域名按这个顺序取：原链接地址是域名就用它 → 链接 query 里的 `sni=` → query 里的 `host=` → 问你一次；都取不到就不加 sni 并说明直连可能证书报错。
- 两条链接都会存进 `~/relay-node-kit/nodes.txt`（分组写清，方便以后翻）；有 qrencode 时终端里直接打印二维码，并存 PNG（`nodes-relay-N.png`、`nodes-direct-N.png`）。没有 qrencode 会试着装一个（apt/dnf/yum），装不上也不报错，只打印链接、复制导入即可。

### 1.4 域名 A 记录改指 + 验证生效

装完中转后只做一次：去域名服务商（或 Cloudflare）把域名的 A 记录从**落地 IP** 改成**中转机 IP**，其他记录不动。

**验证 DNS 已生效**（改完可能要等几分钟到几十分钟，各地生效时间不同）：

```bash
# 查域名现在指到哪（应为中转机 IP）
dig +short yinni.930327.xyz
# 换个公共 DNS 再查一次，排除本地缓存干扰
dig +short yinni.930327.xyz @8.8.8.8
# 没装 dig 就用 nslookup
nslookup yinni.930327.xyz 8.8.8.8
```

两条都返回中转机 IP 才算生效。如果还返回落地 IP，就是 DNS 还没生效，等一会儿再查；不要急着改客户端。

**验证证书没被动过**（最关键的一步，中转必须原样转发）：

```bash
bash check.sh
```

`check.sh` 第 4 节会分别穿过中转和直连落地拿证书，比对 SHA256 指纹，看到下面这行就是对的：

```text
  [通过] 证书指纹与直连落地完全一致：TLS 直通，中转没动过任何字节
```

也可以手动验：

```bash
# 经中转的证书
echo | openssl s_client -connect yinni.930327.xyz:443 -servername yinni.930327.xyz 2>/dev/null | openssl x509 -subject -dates -noout
# 直连落地的证书（指纹/签发日期应完全一致）
echo | openssl s_client -connect 202.155.138.195:443 -servername yinni.930327.xyz 2>/dev/null | openssl x509 -subject -dates -noout
```

### 1.5 客户端两种用法

**用法 A：域名改指后零改动（推荐）。** 域名 A 记录改指到中转机后，客户端配置一个字都不用改——地址还是域名、端口还是 443、UUID/WS 路径都不变，流量自动走中转。这也是改指方式最大的好处：以后换中转机、换落地机都只要改 DNS，客户端永远不用动。

**用法 B：只改地址为中转 IP（不改域名时用）。** 客户端把地址改成中转机 IP，其他全部不动，**SNI/域名保持落地时的域名不变**（不要改成 IP，否则 TLS 握手会报证书域名不匹配）。以 Clash 为例：

```yaml
proxies:
  - name: "VLESS-经中转"
    type: vless
    server: 1.2.3.4          # 改成中转机 IP
    port: 443
    uuid: <原来那个 UUID，不变>
    tls: true
    servername: yinni.930327.xyz   # 保持域名，不变
    network: ws
    ws-opts:
      path: /原来那个路径，不变
      headers:
        Host: yinni.930327.xyz    # 保持域名，不变
```

### 1.6 端口对照表

中转转发的是端口，不是协议——下面是两套节点各协议对应的端口，转哪个取决于你用的协议：

**tiktok-node（Xray，预设 1）：**

| 协议 | 端口 | 传输 | 中转转发 |
|---|---|---|---|
| VLESS + WS + TLS | 443 | TCP | 是（预设 1） |
| Caddy / 证书续签 | 80 | TCP | 是（预设 1） |

**sing-box-node（预设 2）：**

| 协议 | 端口 | 传输 | TLS 模式 | 明文模式 |
|---|---|---|---|---|
| VLESS + WS | 443 / 8080 | TCP | 转发 | 转发（8080） |
| Trojan + WS | 443 / 8081 | TCP | 转发 | 转发（8081） |
| VMess + WS | 443 / 8082 | TCP | 转发 | 转发（8082） |
| Shadowsocks 2022 | 8388 | TCP+UDP | 转发 | 转发 |
| Hysteria2 | 8443 | UDP | 转发 | 不启用 |
| TUIC | 8444 | UDP | 转发 | 不启用 |
| Caddy / 证书续签 | 80 | TCP | 转发 | — |

注意：明文模式没有 HY2/TUIC（它们需要域名证书），所以明文预设里没有 8443/8444。

### 1.7 安装后检查清单（check.sh 解读）

在中转机上跑：

```bash
bash check.sh
```

它分 4 节，每节都有 `[通过] / [注意] / [风险]`：

1. **realm 进程**：`systemd 服务 realm 正在运行`（或无 systemd 时 `realm 进程正在运行`）+ 打印 realm 版本。
2. **监听端口**：逐条核对本次转发的每个端口都在监听，例如 `[通过] 443/tcp 监听中 -> 202.155.138.195:443`。
3. **BBR**：`[通过] BBR 已启用（bbr）`；内核不支持时会显示 `[注意]`，不影响转发。
4. **TLS 直通验证**：比对经中转与直连落地的证书指纹（见 1.4）。一致就是 `[通过]`；不一致会报 `[风险]`，立即排查，不要用。

最后还有一段 UDP 端口说明：UDP 没法用 curl 测，SS / HY2 / TUIC 用客户端实测最准——连上、速度正常就是通的。

---

## 二、卸载/删除篇

### 2.1 stop.sh 两种用法

在中转机上，进到安装目录（远程一键装的就是 `~/relay-node-kit/`）再跑。远程一键停（不用先下载）：

```bash
curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/stop.sh | bash
# 彻底删除用：
curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/stop.sh | bash -s -- --remove
```

|  | `bash stop.sh`（停并保留配置） | `bash stop.sh --remove`（彻底删除） |
|---|---|---|
| realm 服务 | 停止（`systemctl stop realm`），取消自启？**不取消**，只停 | 停止 + 取消开机自启（`systemctl disable realm`） |
| realm 进程 | 结束（停不干净时按进程名强杀） | 同左 |
| `/usr/local/bin/realm` | 保留 | **删除** |
| `/etc/realm/`（config.toml、relay.conf） | 保留 | **删除** |
| `/etc/systemd/system/realm.service` | 保留 | **删除**（并 `systemctl daemon-reload`） |
| `/etc/sysctl.d/99-relay-node.conf`（BBR 调优） | 保留 | **删除**（并恢复系统默认） |
| `/var/log/realm.log` | 保留 | **删除** |
| 安装目录（`~/relay-node-kit/`） | 保留 | **删除**（只删名字里带 `relay-node-kit` 的目录，脚本退出后删） |
| ufw 放行规则 | 保留 | 保留（不会自动删，手动清理见 2.2） |
| 停完验证 | 逐个检查转发端口已释放，仍被占会列出占用进程 | 同左 |

**停完再启动**（只针对 `bash stop.sh` 保留配置的情况）：再启动的命令就是下面这条，跑完中转按原配置重新起来：

```bash
systemctl start realm
# 查是不是起来了：
systemctl status realm
# 无 systemd 的机器：
nohup /usr/local/bin/realm -c /etc/realm/config.toml >/var/log/realm.log 2>&1 &
```

改了配置要生效不用停了再起，一条 `systemctl restart realm` 就行（停 + 起一次完成）。

### 2.2 手动彻底清理清单

`--remove` 已经删了上面表格里的东西。如果想再确认一遍、或当初是手动装的，按下面逐条清理（复制粘贴跑，报错“没有那个文件”就是已经删了）：

```bash
# 1. 停服务 + 取消自启
systemctl stop realm 2>/dev/null; systemctl disable realm 2>/dev/null
pkill -x realm 2>/dev/null

# 2. 删 realm 二进制
rm -f /usr/local/bin/realm

# 3. 删配置目录（config.toml、relay.conf）
rm -rf /etc/realm

# 4. 删 systemd 服务
rm -f /etc/systemd/system/realm.service
systemctl daemon-reload 2>/dev/null

# 5. 删 sysctl 调优文件并恢复默认
rm -f /etc/sysctl.d/99-relay-node.conf
sysctl -p 2>/dev/null

# 6. 删日志
rm -f /var/log/realm.log

# 7. 删安装目录
rm -rf ~/relay-node-kit

# 8. 清理 ufw 放行规则（stop.sh 不会自动删）
ufw status numbered
# 找到 realm 相关规则的编号，逐条删，例如编号是 3：
ufw delete 3
# 删一条后编号会变，每删一条重新 ufw status numbered 看一次
```

**确认删干净了：**

```bash
ls /usr/local/bin/realm /etc/realm /etc/systemd/system/realm.service /etc/sysctl.d/99-relay-node.conf 2>&1
# 应全部报 No such file or directory
systemctl status realm 2>&1 | head -3
# 应报 Unit realm.service could not be found.
ss -tlnp | grep -E ':(80|443|8388)\b' || echo "转发端口已无监听"
```

### 2.3 卸载后域名要改回 + 客户端改回来

**这是最容易忘的一步。** 中转删了之后，域名还指着中转机 IP 的话，客户端会连到一台已经没有中转的机器，全断。

1. 去域名服务商把 A 记录**改回指向落地 IP**（就是 1.2 里落地检查打印的那个公网 IP）。
2. 等 DNS 生效（`dig +short 你的域名` 返回落地 IP，见 1.4 的验证方法）。
3. 客户端改回来：
   - 用法 A（域名改指、客户端零改动）的：**什么都不用改**，DNS 生效后自动直连落地。
   - 用法 B（地址改成中转 IP 的）：把地址改回域名或落地 IP，SNI/域名保持不变。
4. 验证直连落地恢复：手机连节点刷 TikTok 正常；或 `bash check.sh` 已不可用（realm 已删），改用 `curl -sk -o /dev/null -w "%{http_code}\n" https://你的域名/` 应返回 200。

**中转机退役**（整台中转服务器不要了）：先按 2.3 把域名改回落地，再在中转机上跑 `bash stop.sh --remove`，最后在云厂商控制台销毁机器。顺序别反——先删中转再改域名，中间会断网。

---

## 三、排查运维篇

每个问题都是：**现象 → 怎么查（命令可直接复制）→ 原因 → 怎么修**。下面命令默认在中转机上跑，特别注明才去落地机或本地跑。

### 0. 日常运维命令速查

```bash
# 服务状态
systemctl status realm --no-pager
# 实时日志（Ctrl+C 退出）
journalctl -u realm -f
# realm 自己的日志文件
tail -f /var/log/realm.log
# 监听端口
ss -tlnp | grep realm
ss -ulnp | grep realm
# 进程与内存
ps -o pid,rss,vsz,cmd -C realm
# 配置文件
cat /etc/realm/config.toml
cat /etc/realm/relay.conf
```

### 1. 中转后反而更慢

**现象：** 直连落地能用，走中转后刷视频更卡、延迟更高。

**怎么查：**

```bash
# 在中转机上测中转到落地的延迟（这是决定上限的一段）
ping -c 10 202.155.138.195
# 在本地分别测直连落地和走中转的延迟（以你的域名为例）
ping -c 10 yinni.930327.xyz        # 现在指着中转机
ping -c 10 202.155.138.195         # 直连落地
# 对比打开首页耗时
curl -o /dev/null -s -w "经中转：%{time_total}s\n" https://yinni.930327.xyz/
curl -o /dev/null -s -w "直连落地：%{time_total}s\n" --resolve yinni.930327.xyz:443:202.155.138.195 https://yinni.930327.xyz/
```

**原因：** 中转多一跳，总延迟 ≈ 你→中转 + 中转→落地。只要中转→落地那段线路差，总和就会比直连还慢——这不是 realm 的问题，realm 转发本身几乎不增加延迟（零拷贝，内存几 MB）。

**怎么修：**

- 换一台中转机：选离用户更近、到落地线路更好的地区（例如落地在印尼，中转从美国换成新加坡）。
- 先确认 BBR 开了（`sysctl -n net.ipv4.tcp_congestion_control` 应为 `bbr`），没开重跑一次 install.sh 会补开。
- 如果中转→落地延迟本身就很高，任何中转软件都救不了，只能换中转机或换落地机。

### 2. 证书报错 / 浏览器报不安全

**现象：** 客户端报证书错误、浏览器打开域名显示“不安全”或证书域名不匹配。

**怎么查：**

```bash
# 先查域名现在指到哪
dig +short yinni.930327.xyz
dig +short yinni.930327.xyz @8.8.8.8
# 再跑自检（会比对经中转与直连落地的证书指纹）
bash check.sh
# 手动看经中转的证书是谁签的、域名对不对
echo | openssl s_client -connect yinni.930327.xyz:443 -servername yinni.930327.xyz 2>/dev/null | openssl x509 -subject -issuer -dates -noout
```

**原因（按常见程度）：**

1. 域名还没指到中转机，或 DNS 还没生效——客户端实际连到的还是落地或其他机器，证书对不上。
2. 客户端用了用法 B（地址改成中转 IP），但 SNI/域名被改成了 IP 或别的域名。
3. 落地证书本身过期或没签下来（落地 Caddy 的问题，与中转无关）。

**怎么修：**

- 等 DNS 生效（`dig` 两条都返回中转机 IP 再试），各地生效要几分钟到几十分钟。
- 用法 B 的检查 SNI/域名保持落地时的域名不变（见一、1.5）。
- `check.sh` 报指纹不一致：立即停用这条中转，按三、11 临时绕过中转直连落地，再排查是不是域名同时指了别的机器。
- 落地证书过期：去落地机看 Caddy 日志，`docker logs tiktok-caddy` 或 `docker logs singbox-caddy`，修好落地再说。

### 3. UDP 不通（HY2 / TUIC / SS 连不上，但 VLESS 能连）

**现象：** VLESS/Trojan/VMess（走 443 TCP）正常，SS 连不上，HY2/TUIC 一直转圈或超时。

**怎么查：**

```bash
# 中转机：确认 UDP 端口在监听
ss -ulnp | grep realm
# 应有 8388、8443、8444 的 UDP 监听；只有 TCP 没有 UDP 就是问题
# 再看配置里 use_udp 是不是 true
grep -A2 '\[network\]' /etc/realm/config.toml
# 落地机：确认落地 UDP 端口也在监听
ss -ulnp | grep -E '8388|8443|8444'
```

**原因（按常见程度）：**

1. 云厂商安全组只放了 TCP，没放 UDP——TCP 放了不等于 UDP 放了，这是最常见的原因。
2. 预设选错：落地是 sing-box 但中转选了预设 1（只有 80/443），根本没转 UDP 端口。
3. 落地是明文模式：明文模式没有 HY2/TUIC，8443/8444 本来就不开。

**怎么修：**

- 云厂商控制台把 8388、8443、8444 的 UDP 入方向放行（中转机和落地机都要放）。
- 预设选错：重跑一次 install.sh 选预设 2（重跑会按新预设重写配置；注意重跑不改落地配置，只改中转）。
- 明文模式：HY2/TUIC 用不了是正常的，改用 VLESS/Trojan/VMess/SS；想用 HY2/TUIC 就把落地升级成 TLS 模式再重跑中转安装。

### 4. 端口被占，装不上

**现象：** install.sh 第 2 步直接退出，打印 `端口已被占用：443/tcp` 之类。

**怎么查：**

```bash
ss -tlnp | grep -E ':(80|443|8388|8443|8444)\b'
ss -ulnp | grep -E ':(8388|8443|8444)\b'
```

输出里会显示占用进程，例如 `users:(("nginx",pid=1234,fd=6))`。

**原因：** 中转机上已有程序占了要监听的端口（另一套节点、nginx、之前装过的 realm 没停干净）。

**怎么修：**

- 占着的是旧中转：`bash stop.sh` 停掉它再重装；或直接重跑 install.sh 覆盖（重跑会停掉旧服务再写新配置）。
- 占着的是别的程序：先停掉那个程序，或换一台中转机。
- 占着的是之前装过的节点：中转机和落地节点不要装在同一台机器上（同台会抢 80/443），换一台干净的中转机。

### 5. 中转机重启后没起来

**现象：** 服务器重启后客户端连不上，域名能解析但端口不通。

**怎么查：**

```bash
systemctl status realm --no-pager
systemctl is-enabled realm
journalctl -u realm -n 50 --no-pager
```

**原因：** 服务没设开机自启（极少数情况），或启动时网络还没好、realm 起来又退了，或配置被改坏了。

**怎么修：**

```bash
# 设开机自启并立即启动
systemctl enable --now realm
# 看日志找启动失败原因（常见：配置文件写错了、落地解析失败）
journalctl -u realm -n 50 --no-pager
# 配置语法有疑问时，先让 realm 自己校验（不启动，只读配置）
/usr/local/bin/realm -c /etc/realm/config.toml --help >/dev/null && echo "二进制正常"
# 怀疑配置写坏：按二、2.2 删干净重装一次最快
```

如果 `systemctl status` 显示 `Active: failed` 且日志里是落地域名解析失败，检查中转机出站 DNS（`getent hosts 落地域名`），解析通了再 `systemctl restart realm`。

### 6. realm 占用异常 / 内存跑高

**现象：** `top` 里 realm 占 CPU 或内存明显偏高（正常是几 MB 内存、CPU 接近 0 空闲）。

**怎么查：**

```bash
ps -o pid,rss,vsz,%cpu,cmd -C realm
ss -s
ss -tn state established | grep -c ':443'
journalctl -u realm -n 30 --no-pager
```

**原因：** 正常转发时 realm 内存只有几 MB。跑高通常是 UDP 关联没释放（大量短连接的 UDP 扫描/攻击打到 8388/8443/8444），或有人在扫端口。

**怎么修：**

- 先确认是不是被扫：`ss -tn state established | wc -l` 看连接数是否异常大；日志里大量超时/失败就是被扫。
- 重启释放关联：`systemctl restart realm`。
- 长期被扫：把 UDP 端口收紧（云厂商安全组只放行你的使用地区，或落地侧用 `landing.sh` 的收紧功能只允许中转 IP 访问——反过来，中转机的 UDP 端口没法按客户端 IP 白名单，因为客户端 IP 不固定；被扫严重就换个不常用的监听端口，见三、10 的换端口思路）。
- 内存持续涨、重启也压不住：升级 realm 到新版本（见三、9），或反馈给仓库。

### 7. 落地 IP 变了怎么办

**现象：** 落地服务器换 IP（重装、换机），中转突然不通。

**怎么查：**

```bash
# 中转机：看现在配的落地是谁
cat /etc/realm/relay.conf
# 看 realm 日志有没有连接失败
journalctl -u realm -n 30 --no-pager | grep -iE 'error|failed|timeout'
```

**分两种情况：**

- **落地填的是域名：** 不用改任何配置。realm 按 `dns.max_ttl = 300` 最长 5 分钟自动切到新 IP；等不及就立即刷新：

  ```bash
  systemctl restart realm
  ```

- **落地填的是 IP：** 改配置再重启：

  ```bash
  # 把 config.toml 里所有 remote 的旧 IP 改成新 IP
  sed -i 's/旧IP/新IP/g' /etc/realm/config.toml
  # 同步改状态文件，否则 check.sh / stop.sh 还按旧 IP 查
  sed -i 's/旧IP/新IP/g' /etc/realm/relay.conf
  grep remote /etc/realm/config.toml
  systemctl restart realm
  bash check.sh
  ```

  建议：落地尽量填域名不填 IP，以后换 IP 不用动中转。

### 8. 怎么看实时日志

```bash
# systemd 日志（推荐，实时跟随，Ctrl+C 退出）
journalctl -u realm -f
# 只看最近 100 行
journalctl -u realm -n 100 --no-pager
# realm 自己的日志文件
tail -f /var/log/realm.log
tail -n 200 /var/log/realm.log
```

日志级别是 `warn`（只记警告和错误），日常很干净；想看详细连接日志，把 `/etc/realm/config.toml` 里 `[log]` 的 `level` 改成 `info` 再 `systemctl restart realm`（连接量大时日志会很多，用完改回 `warn`）。

### 9. 怎么看转发量 / 连接数

realm 不自带流量统计页面，看连接数和系统流量：

```bash
# 当前经过 realm 的 TCP 连接（按端口分）
ss -tnp | grep realm
# 443 上的已建立连接数
ss -tn state established '( sport = :443 )' | wc -l
# 全部已建立连接数
ss -tn state established | wc -l
# 系统总流量（看 eth0 的收发，替换成你的网卡名）
cat /proc/net/dev
# 实时流量（没装 iftop 就用 nload / vnstat，apt install iftop）
iftop -i eth0
```

想长期看流量：装 `vnstat`（`apt install vnstat`），`vnstat -l` 看实时，`vnstat -d` 看每天汇总。

### 10. 怎么更新 realm 到新版本

中转配置和服务不用动，只换二进制：

```bash
# 查最新版（和 install.sh 同一套逻辑）
curl -fsSL --max-time 15 "https://api.github.com/repos/zhboner/realm/releases/latest" | grep -o '"tag_name": *"v[^"]*"'
# 以下载 v2.9.6 的 x86_64 版为例（aarch64 把 x86_64 换成 aarch64）
cd /tmp
curl -fsSL -O https://github.com/zhboner/realm/releases/download/v2.9.6/realm-x86_64-unknown-linux-gnu.tar.gz
tar xzf realm-x86_64-unknown-linux-gnu.tar.gz
./realm --version
# 停服务、换二进制、启动、验证
systemctl stop realm
install -m 755 realm /usr/local/bin/realm
systemctl start realm
systemctl status realm --no-pager | head -3
bash check.sh
```

懒得手动下：重跑一次 install.sh 也会拉最新版并重写配置，但**重跑会重新问落地和预设**，等于重装一遍，只适合本来就想重配的情况。

### 11. 怎么换落地服务器（改配置 + 重启，域名不动）

域名不用动（域名指着中转机），只改中转的转发目标：

```bash
# 1. 先在新落地跑 landing.sh，确认节点在跑、拿到新落地 IP
# 2. 在中转机上改配置：把所有 remote 的旧地址改成新地址
sed -i 's/旧落地IP或域名/新落地IP或域名/g' /etc/realm/config.toml
sed -i 's/旧落地IP或域名/新落地IP或域名/g' /etc/realm/relay.conf
# 3. 核对改对了
grep remote /etc/realm/config.toml
cat /etc/realm/relay.conf
# 4. 重启并自检
systemctl restart realm
bash check.sh
```

如果新落地同时换了节点类型（例如从 tiktok-node 换成 sing-box-node），端口表也变了：直接重跑 install.sh 重新选预设比重写配置更省事。

### 12. 怎么临时绕过中转，直连落地做对比测试

怀疑是中转的问题时，先直连落地对比，定位是哪一段慢：

**方法一：客户端临时改回直连（推荐，不动 DNS）。** 按一、1.5 的反向操作：用法 A 的客户端不用改配置、只要临时把域名解析改回来太麻烦，改用用法 B——把客户端地址临时改成落地 IP（SNI 保持域名），测完再改回中转 IP 或域名。测速度时用同一个视频/同一个测速点对比。

**方法二：本地命令行对比（不用改客户端）。**

```bash
# 直连落地首页耗时
curl -o /dev/null -s -w "直连落地：%{time_total}s HTTP %{http_code}\n" --resolve yinni.930327.xyz:443:202.155.138.195 https://yinni.930327.xyz/
# 经中转首页耗时（域名当前指着中转机）
curl -o /dev/null -s -w "经中转：%{time_total}s HTTP %{http_code}\n" https://yinni.930327.xyz/
# 直连落地 443 通不通
echo | openssl s_client -connect 202.155.138.195:443 -servername yinni.930327.xyz 2>/dev/null | grep -E 'Verify return code|subject='
```

**结论判断：**

- 直连快、经中转慢 → 中转机或中转→落地那段有问题，换中转机（见三、1）。
- 直连也慢 → 落地本身线路差，加中转也救不了，换落地机。
- 直连不通、经中转通 → 落地直连被拦但中转到落地通，中转反而绕过了问题，留着中转。

---

## 附：文件与路径一览

| 内容 | 路径 |
|---|---|
| realm 二进制 | `/usr/local/bin/realm` |
| 转发配置 | `/etc/realm/config.toml` |
| 状态文件（落地、预设、端口表） | `/etc/realm/relay.conf` |
| systemd 服务 | `/etc/systemd/system/realm.service` |
| 网络调优 | `/etc/sysctl.d/99-relay-node.conf` |
| 日志 | `/var/log/realm.log`（+ `journalctl -u realm`） |
| 安装目录 | `~/relay-node-kit/`（远程一键安装时） |
| 本仓库脚本 | `install.sh`（中转侧安装）、`landing.sh`（落地侧检查）、`check.sh`（自检）、`stop.sh`（停止/删除） |

> 教程与脚本不一致的地方，以脚本为准，请反馈给仓库（提 issue 或找作者）。
