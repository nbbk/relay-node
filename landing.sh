#!/usr/bin/env bash
# 落地侧一键检查：落地不用装任何东西，这个脚本只检查 + 打印中转侧安装要填的信息
# 用法：
#   本地：bash landing.sh
#   远程一键：curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/landing.sh | bash
# 可选：把 80/443 收紧到只允许中转机 IP 访问（默认不做，脚本会问你）
set -uo pipefail

# 交互输入：远程一键时 stdin 是管道，用 /dev/tty 直接问终端
ask() {
  local prompt="$1" varname="$2" default="${3:-}"
  local ans=""
  if [ -t 0 ]; then
    read -rp "$prompt" ans || true
  elif [ -r /dev/tty ]; then
    { read -rp "$prompt" ans </dev/tty || true; } 2>/dev/null
  fi
  [ -z "$ans" ] && ans="$default"
  printf -v "$varname" '%s' "$ans"
}

port_listening() {
  local proto="$1" port="$2"
  if [ "$proto" = "tcp" ]; then
    if command -v ss >/dev/null 2>&1; then
      ss -tln 2>/dev/null | grep -qE "[:.]${port}[[:space:]]"
    elif command -v netstat >/dev/null 2>&1; then
      netstat -tln 2>/dev/null | grep -qE "[:.]${port}[[:space:]]"
    else
      local hex; hex=$(printf '%04X' "$port")
      grep -qiE "^[[:space:]]*[0-9]+:[[:space:]]+[0-9A-Fa-f]+:${hex}[[:space:]]" /proc/net/tcp /proc/net/tcp6 2>/dev/null
    fi
  else
    if command -v ss >/dev/null 2>&1; then
      ss -uln 2>/dev/null | grep -qE "[:.]${port}[[:space:]]"
    elif command -v netstat >/dev/null 2>&1; then
      netstat -uln 2>/dev/null | grep -qE "[:.]${port}[[:space:]]"
    else
      local hex; hex=$(printf '%04X' "$port")
      grep -qiE "^[[:space:]]*[0-9]+:[[:space:]]+[0-9A-Fa-f]+:${hex}[[:space:]]" /proc/net/udp /proc/net/udp6 2>/dev/null
    fi
  fi
}

echo "== 1/4 落地节点容器 =="
FOUND_NODE=""
if command -v docker >/dev/null 2>&1; then
  echo "运行中的相关容器："
  docker ps --format '{{.Names}} {{.Status}}' 2>/dev/null | grep -E 'tiktok|singbox' || echo "  （没找到 tiktok-/singbox- 容器）"
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -qE 'tiktok-caddy|tiktok-xray'; then FOUND_NODE="tiktok"; fi
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -qE 'singbox-caddy|singbox-node'; then FOUND_NODE="singbox"; fi
else
  echo "本机没有 docker 命令；如果节点是别的方式跑的，下面只按端口检查。"
fi
case "$FOUND_NODE" in
  tiktok) echo "识别到：tiktok-node（Xray），中转预设选 1" ;;
  singbox) echo "识别到：sing-box-node，中转预设选 2" ;;
  *) echo "没识别到已知的节点容器：可能节点没启动，或容器改了名；下面按端口继续检查。" ;;
esac

echo ""
echo "== 2/4 端口监听 =="
for spec in "tcp 80" "tcp 443" "tcp 8080" "tcp 8081" "tcp 8082" "tcp 8388" "udp 8388" "udp 8443" "udp 8444"; do
  set -- $spec
  if port_listening "$1" "$2"; then
    echo "  [监听中] $2/$1"
  else
    echo "  [未监听] $2/$1"
  fi
done

echo ""
echo "== 3/4 本机公网 IP =="
PUB_IP="$(curl -s --max-time 5 https://api.ipify.org || true)"
[ -n "$PUB_IP" ] || PUB_IP="$(curl -s --max-time 5 https://ifconfig.me || true)"
[ -n "$PUB_IP" ] || PUB_IP="<拿不到>"
echo "公网 IP：$PUB_IP"

echo ""
echo "== 4/4 给中转侧安装用的信息（照抄过去填） =="
echo "  落地地址：$PUB_IP"
case "$FOUND_NODE" in
  tiktok) echo "  预设：1（tiktok-node：80 + 443）" ;;
  singbox) echo "  预设：2（sing-box-node：按落地是 TLS 还是明文再选）" ;;
  *) echo "  预设：按上面监听中的端口选 1/2，端口对不上就选 3 自定义" ;;
esac
echo "  落地不用装任何东西，也不用重启；装完中转后把域名 A 记录改指到中转 IP 即可。"
echo ""
echo "注意：云厂商安全组/防火墙要放行上面监听中的端口（否则中转连不上落地）。"
echo "      落地 443 的证书仍由落地 Caddy 负责签发和续签，续签走转发的 80 端口照常能通。"

# ---------- 可选：只允许中转机 IP 访问 80/443 ----------
echo ""
ask "要不要把 80/443 收紧到只允许中转机 IP 访问？（默认不做，直接回车跳过）[y/N]: " LOCKDOWN "N"
if [ "$LOCKDOWN" = "y" ] || [ "$LOCKDOWN" = "Y" ]; then
  ask "输入中转机 IP: " RELAY_IP
  if [ -z "$RELAY_IP" ]; then
    echo "没给中转机 IP，跳过收紧。"
    exit 0
  fi
  if ! command -v ufw >/dev/null 2>&1; then
    echo "本机没有 ufw，收紧做不了。请在云厂商安全组里把 80/443 的来源改成只允许 $RELAY_IP。"
    exit 0
  fi
  if ! ufw status 2>/dev/null | grep -q "Status: active"; then
    echo "ufw 未启用，本机防火墙不在 ufw 管辖内，规则加了也不生效。"
    echo "请在云厂商安全组里把 80/443 的来源改成只允许 $RELAY_IP。"
    exit 0
  fi
  echo "注意：只加放行规则。想彻底挡住其他 IP 直连，需确认 ufw 默认策略是 deny："
  echo "  ufw status verbose | grep Default"
  echo "如果是 allow，改默认策略会动全局规则（有锁死 SSH 的风险），不建议在这里自动改。"
  for p in 80 443; do
    ufw allow from "$RELAY_IP" to any port "$p" proto tcp >/dev/null 2>&1 && echo "  已放行：$RELAY_IP -> $p/tcp" || echo "  放行 $p 失败"
  done
  if [ "$FOUND_NODE" = "singbox" ]; then
    for spec in "8388/tcp" "8388/udp" "8443/udp" "8444/udp"; do
      p="${spec%%/*}"; pr="${spec##*/}"
      ufw allow from "$RELAY_IP" to any port "$p" proto "$pr" >/dev/null 2>&1 && echo "  已放行：$RELAY_IP -> $p/$pr" || echo "  放行 $p/$pr 失败"
    done
  fi
  echo "收紧完成。验证：从别的机器 curl 你的域名 443 应不通，从中转机走则正常。"
fi
