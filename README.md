# 中转节点一键包（realm 纯转发，线路不好时提速）

**核心：realm**（[zhboner/realm](https://github.com/zhboner/realm)，Rust 写的纯 TCP/UDP 转发，单二进制、内存几 MB、零拷贝）。

> 落地节点看这里：[tiktok-node](https://github.com/nbbk/tiktok-node)（核心：Xray，VLESS + WS + TLS 单协议）｜[sing-box-node](https://github.com/nbbk/sing-box-node)（核心：sing-box，6 协议合一）。中转只转发、不解密、不改协议，落地不用装任何东西。

> 📖 详细教程（安装、卸载、排查运维全流程）：**[TUTORIAL.md](TUTORIAL.md)**

## 相关仓库

- [tiktok-node](https://github.com/nbbk/tiktok-node)：落地节点，Xray 内核，VLESS + WS + TLS 单协议
- [sing-box-node](https://github.com/nbbk/sing-box-node)：落地节点，sing-box 内核，VLESS/Trojan/VMess/Shadowsocks/Hysteria2/TUIC 6 协议合一
- [relay-node](https://github.com/nbbk/relay-node)（本仓库）：中转，realm 纯转发，落地线路不好时提速

## 这是什么

服务器线路不好时，直连落地节点又慢又卡。加一台中转机：

```text
客户端 → 中转机（realm 原样转发）→ 落地机（你现成的节点）
```

中转只做字节转发：TLS 握手仍是客户端和落地机之间端到端完成，UUID、SNI、WS 路径全部不变；域名 A 记录改指到中转机后，**客户端配置一个字都不用改**。

## 为什么这样设计（新手先看这几条）

- **落地填域名，中转填 IP**：证书按域名签，有 TLS 才像普通网站流量；中转和直连都直接用落地 IP，不经过 DNS，也不会因为域名改指后解析错地方。
- **域名分两步指**：先指到落地机（装节点、签证书、验证能用），中转装好验证通过后再改指到中转机 IP；证书还是原来那张，续签照常走转发的 80 端口。改完等几分钟 DNS 生效再测。
- **直连和中转能同时留着**：域名改指后原来那条自动走中转；再加一条直连，地址填落地 IP、SNI 填域名，两条互为备份。
- **中转机端口要空着**：80、443 这些端口要留给 realm 监听，被占了装不上（安装脚本装前会检查）。
- **双节点怎么来**：安装最后一步粘贴原链接，直接出两条链接和两个二维码；跳过的话事后跑 `bash install.sh --nodes` 补生成。

详细解释见 [TUTORIAL.md](TUTORIAL.md) 开头的“先搞懂为什么”。

## 前置条件

- 一台中转服务器（Ubuntu/Debian，root 权限，x86_64 或 aarch64），离用户近、到落地线路好（香港/新加坡/日本优先）
- 落地服务器上节点已在跑（tiktok-node 或 sing-box-node），记下它的 IP 或域名
- 中转机放行要转发的端口（云厂商安全组 + ufw）

## 使用步骤

### 1. 落地侧先检查（不用装东西）

在落地服务器上跑一条命令，确认节点在跑、打印中转安装要填的信息：

```bash
curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/landing.sh | bash
```

### 2. 中转侧一键安装

在中转服务器上跑一条命令，按提示输入落地 IP 或域名、选预设即可：

```bash
curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/install.sh | bash
```

预设：`1` = tiktok-node（80 + 443）｜`2` = sing-box-node（80 + 443 + 8388 + 8443 + 8444，含 UDP）｜`3` = 自定义端口。装完自动跑 `check.sh` 自检。

填写示例（落地是 tiktok-node，落地 IP `202.155.138.195`）：

```text
请填写你的落地 IP 或域名：
① 落地服务器 IP（推荐填 IP，例如 202.155.138.195）或域名，填完按回车：202.155.138.195
② 选预设 [1/2/3]，直接回车选默认 1：1
```

落地是 sing-box-node 就选 `2`（再问 TLS 还是明文：TLS 选 `1`）。

装完最后一步会问你要不要粘贴原节点链接，粘贴后直接给出直连 + 中转两条链接和二维码：

```text
请粘贴你原来的节点链接：
（可多条，一行一条；粘完按一次回车（空行）结束；不想生成就直接回车跳过）
vless://58087f8b-f340-4721-beb7-5aae486ba0e7@yinni.930327.xyz:443?encryption=none&security=tls&type=ws&host=yinni.930327.xyz&path=%2Fs1momobvfylwonz4#TikTok-Node
（空行回车结束）
```

输出里 `中转：` 是原链接（域名改指后自动走中转），`直连：` 是地址换成落地 IP 并加 `sni=域名` 的版本；两条都存进 `~/relay-node-kit/nodes.txt`，有 qrencode 时还各给一个二维码（终端里直接显示 + 存 PNG）。跳过也没事，装完随时可以只补生成一次：`bash install.sh --nodes`。

### 3. 域名改指（只做一次）

把域名 A 记录从落地 IP 改指到**中转机 IP**，等 DNS 生效后客户端配置一个字都不用改，直接走中转。也可以不改域名、只把客户端地址改成中转 IP（SNI 保持域名不变）。

## 防 DNS 泄露

中转不产生、不改写客户端的任何 DNS 请求（纯字节转发）；防泄漏仍由落地节点（DoH）和客户端配置负责。中转只在启动时解析一次落地域名，填落地 IP 可完全跳过 DNS；解析走 TCP + 公共 DNS（1.1.1.1/8.8.8.8），不走明文 UDP 53。

## 速度

- realm 零拷贝转发（splice，内建）+ 中转机开 BBR + fq + 大 TCP 缓冲
- 中转多一跳，但两段好线路能换掉一段烂直连；中转机选址是关键：离用户近、到落地线路好
- 中转后反而更慢 = 中转到落地那段线路差，换一台中转机（排查见 [TUTORIAL.md](TUTORIAL.md)）

## 停止 / 删除中转

```bash
bash stop.sh            # 停止中转，保留配置
bash stop.sh --remove   # 彻底删除（realm 二进制、配置、服务、sysctl、目录全删）
```

再启动的命令就是下面这条（只针对 `bash stop.sh` 保留配置的情况，跑完中转按原配置重新起来）：

```bash
systemctl start realm
# 查是不是起来了：
systemctl status realm
```

远程一键停：`curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/stop.sh | bash`（`--remove` 用 `| bash -s -- --remove`）。删完记得把域名 A 记录改回指向落地 IP。

## 常见问题

- **证书报错 / 浏览器报不安全**：多半是域名还没指到中转机，或 DNS 还没生效；`bash check.sh` 会核对经中转的证书指纹与落地是否一致
- **UDP 不通（HY2/TUIC/SS 连不上，但 VLESS 能连）**：先查云厂商安全组有没有放行对应 UDP 端口（TCP 放了不等于 UDP 放了）
- **中转后更慢**：中转到落地那段线路差，换中转机；不要只看一跳的延迟
- **多落地 / 故障转移**：realm 支持多 remote（`extra_remotes` + `balance`），后续版本再做

详细排查见 **[TUTORIAL.md](TUTORIAL.md)**。
