# 中转节点一键包（realm 纯转发，线路不好时提速）

**核心：realm**（[zhboner/realm](https://github.com/zhboner/realm)，Rust 写的纯 TCP/UDP 转发，单二进制、内存几 MB、零拷贝）。

> 落地节点看这里：[tiktok-node](https://github.com/nbbk/tiktok-node)（核心：Xray，VLESS + WS + TLS 单协议）｜[sing-box-node](https://github.com/nbbk/sing-box-node)（核心：sing-box，6 协议合一）。中转只转发、不解密、不改协议，落地不用装任何东西。

> 📖 详细教程（安装、卸载、排查运维全流程）：**[TUTORIAL.md](TUTORIAL.md)**

## 这是什么

服务器线路不好时，直连落地节点又慢又卡。加一台中转机：

```text
客户端 → 中转机（realm 原样转发）→ 落地机（你现成的节点）
```

中转只做字节转发：TLS 握手仍是客户端和落地机之间端到端完成，UUID、SNI、WS 路径全部不变；域名 A 记录改指到中转机后，**客户端配置一个字都不用改**。

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
bash stop.sh            # 停止中转，保留配置（systemctl start realm 可再启动）
bash stop.sh --remove   # 彻底删除（realm 二进制、配置、服务、sysctl、目录全删）
```

远程一键停：`curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/stop.sh | bash`（`--remove` 用 `| bash -s -- --remove`）。删完记得把域名 A 记录改回指向落地 IP。

## 常见问题

- **证书报错 / 浏览器报不安全**：多半是域名还没指到中转机，或 DNS 还没生效；`bash check.sh` 会核对经中转的证书指纹与落地是否一致
- **UDP 不通（HY2/TUIC/SS 连不上，但 VLESS 能连）**：先查云厂商安全组有没有放行对应 UDP 端口（TCP 放了不等于 UDP 放了）
- **中转后更慢**：中转到落地那段线路差，换中转机；不要只看一跳的延迟
- **多落地 / 故障转移**：realm 支持多 remote（`extra_remotes` + `balance`），后续版本再做

详细排查见 **[TUTORIAL.md](TUTORIAL.md)**。
