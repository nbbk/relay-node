#!/usr/bin/env bash
# 中转节点一键安装：realm 纯转发（客户端 -> 中转机 -> 落地机）
# 用法：
#   本地：bash install.sh [落地IP或域名] [预设 1/2/3]
#   远程一键：curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/install.sh | bash
#   （不给参数则交互询问；落地不用装任何东西，转发目标就是落地现成的端口）
# 架构：中转只做原样字节转发，不解密、不改协议。TLS 握手仍是客户端和落地机之间
#        端到端完成，UUID、SNI、WS 路径全部不变；域名 A 记录改指到中转机后，
#        客户端配置一个字都不用改。
# DNS 防泄漏：中转不产生、不改写客户端的任何 DNS 请求（纯字节转发）；防泄漏
#        仍由落地节点（DoH）和客户端配置负责。中转只在启动时解析一次落地域名，
#        填落地 IP 可完全跳过 DNS；解析走 TCP + 加密方向的公共 DNS。
# 速度：realm 零拷贝转发（splice，内建）+ 本机开 BBR + fq + 大 TCP 缓冲；
#        中转多一跳，但两段好线路能换掉一段烂直连——中转机要选离用户近、
#        到落地线路好的（香港/新加坡/日本）。
set -euo pipefail

# 工作目录：本地跑 bash install.sh 时用脚本所在目录；
# 远程一键（curl | bash）时用 ~/relay-node-kit
if [ -f "$0" ]; then
  cd "$(dirname "$0")"
else
  mkdir -p "$HOME/relay-node-kit"
  cd "$HOME/relay-node-kit"
fi

REALM_VERSION="${REALM_VERSION:-}"   # 可手动指定，如 REALM_VERSION=v2.9.6
REPO_RAW="${REPO_RAW:-https://raw.githubusercontent.com/nbbk/relay-node/main}"
LANDING="${1:-${LANDING:-}}"
PRESET="${2:-${PRESET:-}}"
DRY_RUN="${DRY_RUN:-0}"

# 交互输入：远程一键时 stdin 是管道，用 /dev/tty 直接问终端
# 已通过命令行参数/环境变量预设的值不再询问（便于自动化与测试）
ask() {
  local prompt="$1" varname="$2" default="${3:-}"
  if [ -n "${!varname:-}" ]; then return 0; fi
  local ans=""
  if [ -t 0 ]; then
    read -rp "$prompt" ans || true
  elif [ -r /dev/tty ]; then
    read -rp "$prompt" ans </dev/tty 2>/dev/null || true
  fi
  [ -z "$ans" ] && ans="$default"
  printf -v "$varname" '%s' "$ans"
}

# ---------- 端口占用预检（ss -> netstat -> /proc 回退，与 tiktok/sing-box 包一致） ----------
port_in_use() {
  local proto="$1" port="$2"
  local hex
  if [ "$proto" = "tcp" ]; then
    if command -v ss >/dev/null 2>&1; then
      ss -tln 2>/dev/null | grep -qE "[:.]${port}[[:space:]]"
    elif command -v netstat >/dev/null 2>&1; then
      netstat -tln 2>/dev/null | grep -qE "[:.]${port}[[:space:]]"
    else
      hex=$(printf '%04X' "$port")
      grep -qiE "^[[:space:]]*[0-9]+:[[:space:]]+[0-9A-Fa-f]+:${hex}[[:space:]]" /proc/net/tcp /proc/net/tcp6 2>/dev/null
    fi
  else
    if command -v ss >/dev/null 2>&1; then
      ss -uln 2>/dev/null | grep -qE "[:.]${port}[[:space:]]"
    elif command -v netstat >/dev/null 2>&1; then
      netstat -uln 2>/dev/null | grep -qE "[:.]${port}[[:space:]]"
    else
      hex=$(printf '%04X' "$port")
      grep -qiE "^[[:space:]]*[0-9]+:[[:space:]]+[0-9A-Fa-f]+:${hex}[[:space:]]" /proc/net/udp /proc/net/udp6 2>/dev/null
    fi
  fi
}

is_ip() { [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; }

valid_port() { [[ "$1" =~ ^[0-9]+$ ]] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]; }

echo "== 1/6 落地目标与转发预设 =="
if [ -z "$LANDING" ]; then
  ask "输入落地服务器 IP 或域名（就是你 tiktok-node / sing-box-node 跑的那台）: " LANDING
fi
[ -n "$LANDING" ] || { echo "没给落地地址，装不了。重跑并输入落地 IP 或域名。"; exit 1; }
if is_ip "$LANDING"; then
  LANDING_IS_DOMAIN=0
  DOMAIN=""
  echo "落地：$LANDING（IP 直连，中转完全不碰 DNS）"
else
  LANDING_IS_DOMAIN=1
  DOMAIN="$LANDING"
  echo "落地：$LANDING（域名，中转启动时解析一次）"
  if ! getent hosts "$LANDING" >/dev/null 2>&1; then
    echo "注意：本机现在解析不到 $LANDING（DNS 可能还没生效或出站 DNS 被拦）。"
    echo "      realm 启动时会再解析，解析失败则转发不通；确认域名能解析后再继续更稳。"
  fi
fi

if [ -z "$PRESET" ]; then
  echo ""
  echo "转发预设："
  echo "  1) tiktok-node（Xray）：80 + 443（TCP）"
  echo "  2) sing-box-node：80 + 443（TCP）+ 8388（TCP+UDP）+ 8443（UDP）+ 8444（UDP）"
  echo "  3) 自定义端口（自己输）"
  ask "选预设 [1/2/3，默认 1]: " PRESET "1"
fi

# PORTS 格式：以空格分隔的 "协议:监听端口:落地端口"，如 "tcp:443:443 udp:8388:8388"
PORTS=""
MODE_NOTE=""
case "$PRESET" in
  1)
    PORTS="tcp:80:80 tcp:443:443"
    MODE_NOTE="tiktok-node（Xray）"
    ;;
  2)
    ask "落地 sing-box-node 是 TLS 模式还是明文模式？[1=TLS(443) 2=明文(8080-8082)，默认 1]: " SB_MODE "1"
    if [ "$SB_MODE" = "2" ]; then
      PORTS="tcp:8080:8080 tcp:8081:8081 tcp:8082:8082 tcp:8388:8388 udp:8388:8388"
      MODE_NOTE="sing-box-node 明文模式（HY2/TUIC 不启用）"
    else
      PORTS="tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388 udp:8443:8443 udp:8444:8444"
      MODE_NOTE="sing-box-node TLS 模式"
    fi
    ;;
  3)
    echo "自定义：输入要转发的端口，逗号分隔；监听端口默认与落地端口相同，"
    echo "  如需改监听端口，用 监听:落地 的写法，例如：443,8388,8443:9443"
    ask "端口列表: " CUSTOM_PORTS
    [ -n "$CUSTOM_PORTS" ] || { echo "没给端口，装不了。"; exit 1; }
    ask "这些端口要不要同时转 UDP？[y/N]: " WANT_UDP "N"
    IFS=',' read -ra _plist <<< "$CUSTOM_PORTS"
    for _e in "${_plist[@]}"; do
      _e="${_e//[[:space:]]/}"
      [ -n "$_e" ] || continue
      if [[ "$_e" == *:* ]]; then
        _lp="${_e%%:*}"; _rp="${_e##*:}"
      else
        _lp="$_e"; _rp="$_e"
      fi
      valid_port "$_lp" && valid_port "$_rp" || { echo "端口不合法：$_e（1-65535）"; exit 1; }
      PORTS="$PORTS tcp:$_lp:$_rp"
      if [ "$WANT_UDP" = "y" ] || [ "$WANT_UDP" = "Y" ]; then
        PORTS="$PORTS udp:$_lp:$_rp"
      fi
    done
    PORTS="${PORTS# }"
    MODE_NOTE="自定义端口"
    ;;
  *)
    echo "预设只能是 1/2/3。"; exit 1
    ;;
esac
echo "预设：$MODE_NOTE"
echo "转发：$PORTS"

# ---------- 装前端口预检：监听端口被占即退出 ----------
echo ""
echo "== 2/6 端口预检（中转监听端口） =="
OCCUPIED=""
for _spec in $PORTS; do
  _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"
  if port_in_use "$_proto" "$_lp"; then
    OCCUPIED="$OCCUPIED $_lp/$_proto"
  fi
done
if [ -n "$OCCUPIED" ]; then
  echo "端口已被占用：$OCCUPIED"
  echo "中转需要独占这些监听端口，装不上。"
  echo "先停掉占着它们的程序（ss -tlnp / ss -ulnp 可查是谁），或换一台中转服务器再跑。"
  exit 1
fi
echo "监听端口全部空闲。"

# ---------- 下载 realm ----------
echo ""
echo "== 3/6 下载 realm =="
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ASSET_ARCH="x86_64" ;;
  aarch64|arm64) ASSET_ARCH="aarch64" ;;
  *) echo "不支持的架构：$ARCH（realm 只提供 x86_64 / aarch64 的 Linux 包）"; exit 1 ;;
esac
if [ -z "$REALM_VERSION" ]; then
  REALM_VERSION="$(curl -fsSL --max-time 15 "https://api.github.com/repos/zhboner/realm/releases/latest" 2>/dev/null | grep -o '"tag_name": *"v[^"]*"' | head -1 | sed 's/.*"v/v/;s/"//')"
  if [ -z "$REALM_VERSION" ]; then
    REALM_VERSION="v2.9.6"
    echo "取 latest 失败，用固定回退版本 $REALM_VERSION（2026-08-30 发布，已核实直链可用）"
  else
    echo "最新版本：$REALM_VERSION"
  fi
else
  echo "指定版本：$REALM_VERSION"
fi

TMPDIR_RELM="$(mktemp -d)"
REALM_BIN=""
# 按顺序试：glibc 通用版 -> glibc 2.28 版 -> musl 版（仅 x86_64 有 musl 包）
CANDIDATES="realm-${ASSET_ARCH}-unknown-linux-gnu.tar.gz realm-${ASSET_ARCH}-unknown-linux-gnu-glibc2.28.tar.gz"
[ "$ASSET_ARCH" = "x86_64" ] && CANDIDATES="$CANDIDATES realm-x86_64-unknown-linux-musl.tar.gz"
for _asset in $CANDIDATES; do
  _url="https://github.com/zhboner/realm/releases/download/${REALM_VERSION}/${_asset}"
  echo "下载 $_asset ..."
  if curl -fsSL --max-time 60 -o "$TMPDIR_RELM/realm.tar.gz" "$_url" 2>/dev/null \
     && tar xzf "$TMPDIR_RELM/realm.tar.gz" -C "$TMPDIR_RELM" 2>/dev/null \
     && [ -x "$TMPDIR_RELM/realm" ] \
     && "$TMPDIR_RELM/realm" --version >/dev/null 2>&1; then
    REALM_BIN="$TMPDIR_RELM/realm"
    echo "realm 可用：$("$TMPDIR_RELM/realm" --version 2>/dev/null || echo "$REALM_VERSION / $_asset")"
    break
  fi
  rm -f "$TMPDIR_RELM/realm.tar.gz" "$TMPDIR_RELM/realm"
done
[ -n "$REALM_BIN" ] || { echo "realm 下载/解压失败：请检查本机能否访问 github.com，或手动下载后重跑"; exit 1; }

if [ "$DRY_RUN" != "1" ]; then
  install -m 755 "$REALM_BIN" /usr/local/bin/realm
  mkdir -p /etc/realm
else
  echo "[dry-run] 跳过安装二进制到 /usr/local/bin"
  mkdir -p /etc/realm
fi
rm -rf "$TMPDIR_RELM"

# ---------- 写配置 ----------
echo ""
echo "== 4/6 写 realm 配置 =="
HAS_UDP=0
for _spec in $PORTS; do
  [ "${_spec%%:*}" = "udp" ] && HAS_UDP=1
done
USE_UDP="false"
[ "$HAS_UDP" = "1" ] && USE_UDP="true"

# 落地域名解析：走 TCP + 公共 DNS，不经过本地明文 UDP 53；填 IP 时完全不解析
cat > /etc/realm/config.toml <<EOF
# relay-node 自动生成（$(date -u +%Y-%m-%dT%H:%M:%SZ)）：$MODE_NOTE，落地 $LANDING
# 改落地目标：改下面各 remote 的地址后 systemctl restart realm
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
use_udp = $USE_UDP
tcp_timeout = 5
udp_timeout = 30
tcp_keepalive = 15
tcp_keepalive_probe = 3
EOF
for _spec in $PORTS; do
  _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
  cat >> /etc/realm/config.toml <<EOF

[[endpoints]]
listen = "0.0.0.0:$_lp"
remote = "$LANDING:$_rp"
EOF
done
# 给 check.sh / stop.sh 用的状态文件
cat > /etc/realm/relay.conf <<EOF
LANDING=$LANDING
LANDING_IS_DOMAIN=$LANDING_IS_DOMAIN
DOMAIN="$DOMAIN"
PRESET=$PRESET
MODE_NOTE="$MODE_NOTE"
PORTS="$PORTS"
REALM_VERSION=$REALM_VERSION
EOF
echo "配置已写：/etc/realm/config.toml"

# ---------- BBR + fq + 大 TCP 缓冲（只加网络调优，不碰 IPv6、不破坏已有配置） ----------
echo ""
echo "== 5/6 网络加速（BBR / fq / TCP 缓冲 / TFO） =="
if [ "$DRY_RUN" != "1" ]; then
  cat > /etc/sysctl.d/99-relay-node.conf <<'SYSCTL'
# relay-node 中转：BBR 加速 + fq + TCP 缓冲 + TFO（中转不碰 IPv6，不禁用）
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.core.rmem_max = 67108864
net.core.wmem_max = 67108864
net.ipv4.tcp_rmem = 4096 87380 67108864
net.ipv4.tcp_wmem = 4096 65536 67108864
net.ipv4.tcp_fastopen = 3
net.core.netdev_max_backlog = 250000
SYSCTL
  if ! sysctl -n net.ipv4.tcp_available_congestion_control 2>/dev/null | grep -q bbr; then
    modprobe tcp_bbr 2>/dev/null || true
  fi
  if sysctl -n net.ipv4.tcp_available_congestion_control 2>/dev/null | grep -q bbr; then
    sysctl -p /etc/sysctl.d/99-relay-node.conf >/dev/null 2>&1 || true
    echo "BBR 已启用：$(sysctl -n net.ipv4.tcp_congestion_control)"
  else
    sed -i '/tcp_congestion_control/d; /default_qdisc/d' /etc/sysctl.d/99-relay-node.conf
    sysctl -p /etc/sysctl.d/99-relay-node.conf >/dev/null 2>&1 || true
    echo "当前内核不支持 BBR，已跳过 BBR（不影响转发）"
  fi
else
  echo "[dry-run] 跳过写 /etc/sysctl.d"
fi

# ---------- systemd 服务 + 启动 ----------
echo ""
echo "== 6/6 启动 realm 服务 =="
if [ "$DRY_RUN" != "1" ]; then
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    cat > /etc/systemd/system/realm.service <<'UNIT'
[Unit]
Description=realm relay (TCP/UDP forwarding for relay-node)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/realm -c /etc/realm/config.toml
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT
    systemctl daemon-reload
    systemctl enable --now realm
    sleep 2
    if systemctl is-active --quiet realm; then
      echo "realm 服务已启动（开机自启，异常自动拉起）"
    else
      echo "realm 服务启动失败，看日志：journalctl -u realm -n 50"
      exit 1
    fi
  else
    echo "本机没有 systemd，改用后台运行："
    nohup /usr/local/bin/realm -c /etc/realm/config.toml >/var/log/realm.log 2>&1 &
    echo $! > /var/run/realm.pid
    sleep 2
    kill -0 "$(cat /var/run/realm.pid)" 2>/dev/null && echo "realm 已在后台运行（PID $(cat /var/run/realm.pid)）" || { echo "realm 启动失败，看日志：tail -50 /var/log/realm.log"; exit 1; }
    echo "注意：无 systemd 的机器重启后需手动再跑一次 realm，启动命令见 README。"
  fi

  # ufw 放行监听端口（只在 ufw 已启用时）
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    for _spec in $PORTS; do
      _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"
      ufw allow "$_lp/$_proto" >/dev/null 2>&1 || true
    done
    echo "ufw 已放行监听端口"
  fi
else
  echo "[dry-run] 跳过启动服务"
fi

# ---------- 自检 + 汇总 ----------
RELAY_IP="$(curl -s --max-time 5 https://api.ipify.org || true)"
[ -n "$RELAY_IP" ] || RELAY_IP="$(curl -s --max-time 5 https://ifconfig.me || true)"
[ -n "$RELAY_IP" ] || RELAY_IP="<拿不到本机公网 IP>"

echo ""
echo "================ 中转部署完成 ================"
echo "中转机：$RELAY_IP"
echo "落地：  $LANDING（$MODE_NOTE）"
echo "转发："
for _spec in $PORTS; do
  _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
  echo "  $_proto  $RELAY_IP:$_lp  ->  $LANDING:$_rp"
done
echo ""
echo "下一步（只做一次）："
if [ "$LANDING_IS_DOMAIN" = "1" ]; then
  echo "  把域名 $DOMAIN 的 A 记录从落地 IP 改指到中转 IP $RELAY_IP，"
  echo "  等 DNS 生效后客户端配置一个字都不用改，直接就能走中转。"
else
  echo "  客户端把地址改成中转 IP $RELAY_IP，SNI/域名保持落地时的域名不变，"
  echo "  其他（UUID、WS 路径、端口）都不用动。"
fi
echo "  落地不用装任何东西，也不用重启；证书续签走转发的 80 端口照常能通。"
echo ""
echo "换落地目标：改 /etc/realm/config.toml 里各 remote 的地址，再 systemctl restart realm。"
if [ "$LANDING_IS_DOMAIN" = "1" ]; then
  echo "落地域名 IP 变了：最长 5 分钟自动切到新 IP（dns.max_ttl=300），也可立即 systemctl restart realm。"
fi
echo ""
echo "自检：bash check.sh（会穿过中转连落地 443，核对证书与落地一致）"

# 装完自动跑自检：本地有 check.sh 就用，没有就从仓库拉（拉不到也不报错）
if [ ! -f check.sh ]; then
  curl -fsSL --max-time 15 -o check.sh "$REPO_RAW/check.sh" 2>/dev/null || true
  curl -fsSL --max-time 15 -o stop.sh "$REPO_RAW/stop.sh" 2>/dev/null || true
  chmod +x check.sh stop.sh 2>/dev/null || true
fi
if [ "$DRY_RUN" != "1" ] && [ -f check.sh ]; then
  echo ""
  bash check.sh || true
fi
