#!/usr/bin/env bash
# 停止或删除中转节点（realm + 分流组件），释放端口
#   bash stop.sh            停止中转，保留配置（之后 systemctl start realm 可再启动）
#   bash stop.sh --remove   彻底删除中转（realm 二进制、配置、服务、sysctl、目录全部删除）
# 分流组件（80 按域名分流的 nginx、443 按 SNI 分流的 haproxy）也会一起停/删，
# 但不卸载软件包（nginx/haproxy 可能别处还在用）；只删 relay-node 生成的配置文件。
# 远程一键（不用先下载）：
#   curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/stop.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/stop.sh | bash -s -- --remove
set -euo pipefail

REMOVE=0
[ "${1:-}" = "--remove" ] && REMOVE=1

# 读 install.sh 写的状态文件，拿到本次转发的端口表；没有就用默认全表
# 多落地：优先用 ALL_PORTS（全部落地的端口并集），老 relay.conf 没有 ALL_PORTS 就用 PORTS
# SHARE80/SHARE443：80/443 是否走了分流共享（老 relay.conf 没有，按 0 读）
PORTS=""
SHARE80=0; SHARE443=0
[ -f /etc/realm/relay.conf ] && set -a && . /etc/realm/relay.conf && set +a
[ -n "${ALL_PORTS:-}" ] && PORTS="$ALL_PORTS"
[ -n "${PORTS:-}" ] || PORTS="tcp:80:80 tcp:443:443 tcp:8080:8080 tcp:8081:8081 tcp:8082:8082 tcp:8388:8388 udp:8388:8388 udp:8443:8443 udp:8444:8444"

# 找到安装目录（解析为绝对路径）
KIT_DIR=""
for d in "$(dirname "$0")" "$PWD" "$HOME/relay-node-kit"; do
  if [ -f "$d/install.sh" ] || [ -f "$d/stop.sh" ]; then KIT_DIR="$(cd "$d" && pwd)"; break; fi
done

# ---------- 1. 停 realm ----------
echo "停止 realm..."
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  systemctl stop realm 2>/dev/null || true
  if [ "$REMOVE" = "1" ]; then
    systemctl disable realm 2>/dev/null || true
    echo "已停止并取消开机自启"
  else
    echo "已停止（配置保留）"
  fi
fi
# 兜底：按进程名直接杀（无 systemd 环境，或服务停不干净时）
if pgrep -x realm >/dev/null 2>&1; then
  pkill -x realm 2>/dev/null || true
  sleep 1
  pgrep -x realm >/dev/null 2>&1 && pkill -9 -x realm 2>/dev/null || true
  echo "已结束 realm 进程"
fi

# ---------- 1b. 停分流组件（只停开着的那个；端口检查要等它们停了才准） ----------
if [ "$SHARE80" = "1" ]; then
  echo "停止 80 分流（nginx）..."
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl stop nginx 2>/dev/null || true
    [ "$REMOVE" = "1" ] && systemctl disable nginx 2>/dev/null || true
  fi
  if pgrep -x nginx >/dev/null 2>&1; then
    pkill -x nginx 2>/dev/null || true
    sleep 1
    pgrep -x nginx >/dev/null 2>&1 && pkill -9 -x nginx 2>/dev/null || true
    echo "已结束 nginx 进程"
  fi
fi
if [ "$SHARE443" = "1" ]; then
  echo "停止 443 分流（haproxy）..."
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl stop haproxy 2>/dev/null || true
    [ "$REMOVE" = "1" ] && systemctl disable haproxy 2>/dev/null || true
  fi
  if pgrep -x haproxy >/dev/null 2>&1; then
    pkill -x haproxy 2>/dev/null || true
    sleep 1
    pgrep -x haproxy >/dev/null 2>&1 && pkill -9 -x haproxy 2>/dev/null || true
    echo "已结束 haproxy 进程"
  fi
fi

# ---------- 2. 验证端口是否已释放 ----------
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

echo ""
echo "端口检查："
STILL_HELD=""
for _spec in $PORTS; do
  _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"
  if port_in_use "$_proto" "$_lp"; then
    echo "  端口 $_lp/$_proto 仍被占用："
    if command -v ss >/dev/null 2>&1; then
      if [ "$_proto" = "tcp" ]; then
        ss -tlnp 2>/dev/null | grep -E "[:.]${_lp}[[:space:]]" | sed 's/^/    /' || true
      else
        ss -ulnp 2>/dev/null | grep -E "[:.]${_lp}[[:space:]]" | sed 's/^/    /' || true
      fi
    fi
    STILL_HELD="$STILL_HELD $_lp/$_proto"
  else
    echo "  端口 $_lp/$_proto 已释放"
  fi
done

echo ""
if [ -n "$STILL_HELD" ]; then
  echo "注意：端口$STILL_HELD 仍被占用（上面列出了占用它的进程）。"
  echo "如果占用它的是 realm，说明没停干净，请手动执行："
  echo "  ps aux | grep -E '[r]ealm'"
  echo "  kill <PID>  或  kill -9 <PID>"
  echo "如果是别的程序占了这些端口，需要先停掉那个程序，或换一台服务器。"
else
  echo "转发端口已全部释放，装新中转不会再报端口占用。"
fi

# ---------- 3. 彻底删除时清理系统改动与目录 ----------
if [ "$REMOVE" = "1" ]; then
  echo ""
  echo "彻底删除："
  if [ -f /etc/systemd/system/realm.service ]; then
    rm -f /etc/systemd/system/realm.service
    systemctl daemon-reload 2>/dev/null || true
    echo "  已删除 /etc/systemd/system/realm.service"
  fi
  if [ -f /usr/local/bin/realm ]; then
    rm -f /usr/local/bin/realm
    echo "  已删除 /usr/local/bin/realm"
  fi
  if [ -d /etc/realm ]; then
    rm -rf /etc/realm
    echo "  已删除 /etc/realm（config.toml 与 relay.conf）"
  fi
  if [ -f /etc/sysctl.d/99-relay-node.conf ]; then
    rm -f /etc/sysctl.d/99-relay-node.conf
    sysctl -p >/dev/null 2>&1 || true
    echo "  已删除 /etc/sysctl.d/99-relay-node.conf（BBR 与网络调优已恢复）"
  fi
  if [ -f /var/log/realm.log ]; then
    rm -f /var/log/realm.log
    echo "  已删除 /var/log/realm.log"
  fi
  # 分流配置（只删 relay-node 生成的；软件包本身不卸载）
  if [ -f /etc/nginx/conf.d/relay-80.conf ]; then
    rm -f /etc/nginx/conf.d/relay-80.conf
    echo "  已删除 /etc/nginx/conf.d/relay-80.conf（80 分流配置）"
    # 当初开分流时停用了 nginx 自带默认站点，这里把它恢复（源文件还在 sites-available 里）
    if [ -f /etc/nginx/sites-available/default ] && [ ! -e /etc/nginx/sites-enabled/default ]; then
      ln -s /etc/nginx/sites-available/default /etc/nginx/sites-enabled/default 2>/dev/null || true
      echo "  已恢复 nginx 自带默认站点"
    fi
  fi
  if grep -q "relay-node 自动生成" /etc/haproxy/haproxy.cfg 2>/dev/null; then
    rm -f /etc/haproxy/haproxy.cfg
    echo "  已删除 /etc/haproxy/haproxy.cfg（443 SNI 分流配置，relay-node 生成的）"
  elif [ "$SHARE443" = "1" ]; then
    echo "  注意：/etc/haproxy/haproxy.cfg 不是 relay-node 生成的，没动它，请手动检查。"
  fi
  if [ -f /etc/realm/config.toml.bak ]; then
    rm -f /etc/realm/config.toml.bak
    echo "  已删除 /etc/realm/config.toml.bak（分流切换时的备份）"
  fi
  echo "  注意：ufw 放行规则不会自动删，如需清理：ufw status numbered，ufw delete <编号>"
  # 只删名字里带 relay-node-kit 的目录，脚本退出后再删（避免删正在执行的文件）
  if [ -n "$KIT_DIR" ]; then
    case "$KIT_DIR" in
      *relay-node-kit*)
        trap 'rm -rf "$KIT_DIR"' EXIT
        echo "  中转目录 $KIT_DIR 将在脚本退出后删除"
        ;;
      *)
        echo "  中转目录 $KIT_DIR 请手动删除：rm -rf $KIT_DIR"
        ;;
    esac
  fi
  echo ""
  echo "中转已彻底删除。"
  echo "别忘了：域名 A 记录要改回指向落地 IP，否则客户端还会连到这台已删中转的机器。"
else
  echo ""
  echo "中转已停止（配置保留）。"
  echo "再启动：systemctl start realm"
  echo "（无 systemd 的机器：nohup /usr/local/bin/realm -c /etc/realm/config.toml >/var/log/realm.log 2>&1 &）"
  if [ "$SHARE80" = "1" ]; then
    echo "80 分流也要起来：systemctl start nginx（无 systemd：nginx）"
  fi
  if [ "$SHARE443" = "1" ]; then
    echo "443 分流也要起来：systemctl start haproxy（无 systemd：haproxy -D -f /etc/haproxy/haproxy.cfg）"
  fi
fi
