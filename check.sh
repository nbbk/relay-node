#!/usr/bin/env bash
# 中转自检：在中转机上运行，检查 realm 进程、监听端口、TLS 直通（证书与落地一致）
# 用法：bash check.sh
set -uo pipefail

pass() { echo "  [通过] $1"; }
warn() { echo "  [注意] $1"; }
fail() { echo "  [风险] $1"; }

# 读 install.sh 写的状态文件；没有就用默认端口表
LANDING=""; LANDING_IS_DOMAIN=0; DOMAIN=""; PORTS=""
[ -f /etc/realm/relay.conf ] && set -a && . /etc/realm/relay.conf && set +a
[ -n "$PORTS" ] || PORTS="tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388 udp:8443:8443 udp:8444:8444"
[ -n "$LANDING" ] || LANDING="<未知，/etc/realm/relay.conf 缺失>"

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

echo "== 1/4 realm 进程 =="
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  if systemctl is-active --quiet realm 2>/dev/null; then
    pass "systemd 服务 realm 正在运行"
  else
    fail "systemd 服务 realm 没在运行：systemctl status realm / journalctl -u realm -n 50"
  fi
elif pgrep -x realm >/dev/null 2>&1; then
  pass "realm 进程正在运行（PID $(pgrep -x realm | head -1)，无 systemd 环境）"
else
  fail "realm 进程没在运行：/usr/local/bin/realm -c /etc/realm/config.toml"
fi
/usr/local/bin/realm --version 2>/dev/null && echo "  realm 版本：$(/usr/local/bin/realm --version 2>/dev/null)"

echo ""
echo "== 2/4 监听端口（落地：$LANDING） =="
for _spec in $PORTS; do
  _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
  if port_listening "$_proto" "$_lp"; then
    pass "$_lp/$_proto 监听中 -> $LANDING:$_rp"
  else
    fail "$_lp/$_proto 没监听：realm 可能没起来，或端口被别的程序占了（ss -tlnp / ss -ulnp 查）"
  fi
done

echo ""
echo "== 3/4 BBR =="
BBR="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo "?")"
if [ "$BBR" = "bbr" ]; then
  pass "BBR 已启用（$BBR）"
else
  warn "BBR 未启用（当前 $BBR）。重跑 install.sh 会补开；不影响转发，只影响弱网速度。"
fi

echo ""
echo "== 4/4 TLS 直通验证（证书必须与落地一模一样） =="
# 原理：realm 只转发字节不解密，穿过中转拿到的证书指纹必须和直连落地的一致。
# 有域名就用它做 SNI；没有就直接连（单域名落地也能比出指纹）。
if ! port_listening tcp 443 && ! echo "$PORTS" | grep -qE "(^| )tcp:443:[0-9]+($| )|(^| )tcp:[0-9]+:443($| )"; then
  warn "本次转发没配到落地 443 的端口，跳过 TLS 直通验证。"
elif ! command -v openssl >/dev/null 2>&1; then
  warn "本机无 openssl，装一个再验：apt install openssl"
else
  # 找到 TLS 端点：优先监听端口是 443 的那条（如 443:14443 就连本地 443、直连落地 14443），
  # 其次找 remote 是 443 的那条
  TLS_LISTEN=""; TLS_REMOTE=""
  for _spec in $PORTS; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
    if [ "$_proto" = "tcp" ] && [ "$_lp" = "443" ]; then TLS_LISTEN="$_lp"; TLS_REMOTE="$_rp"; break; fi
  done
  if [ -z "$TLS_LISTEN" ]; then
    for _spec in $PORTS; do
      _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
      if [ "$_proto" = "tcp" ] && [ "$_rp" = "443" ]; then TLS_LISTEN="$_lp"; TLS_REMOTE="$_rp"; break; fi
    done
  fi
  [ -n "$TLS_LISTEN" ] || { TLS_LISTEN="443"; TLS_REMOTE="443"; }
  SNI_ARGS=()
  [ -n "$DOMAIN" ] && SNI_ARGS=(-servername "$DOMAIN")
  FP_RELAY="$(echo | timeout 10 openssl s_client -connect 127.0.0.1:$TLS_LISTEN "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -fingerprint -sha256 -noout 2>/dev/null)"
  SUBJ_RELAY="$(echo | timeout 10 openssl s_client -connect 127.0.0.1:$TLS_LISTEN "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -subject -noout 2>/dev/null)"
  if [ -z "$FP_RELAY" ]; then
    fail "穿过中转连 $TLS_LISTEN 拿不到证书：realm 没转发通，或落地 $TLS_REMOTE 本身不通。先查落地端口，再查中转到落地的网络."
  else
    echo "  经中转的证书：$SUBJ_RELAY"
    echo "  经中转的指纹：$FP_RELAY"
    if [ "$LANDING" = "<未知，/etc/realm/relay.conf 缺失>" ]; then
      warn "不知道落地地址，没法直连对比；指纹本身已打印，可拿去和落地证书人工核对。"
    else
      FP_DIRECT="$(echo | timeout 10 openssl s_client -connect "$LANDING:$TLS_REMOTE" "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -fingerprint -sha256 -noout 2>/dev/null)"
      if [ -z "$FP_DIRECT" ]; then
        warn "直连落地 $LANDING:$TLS_REMOTE 拿不到证书（落地可能只允许中转 IP 访问，或网络不通）；经中转的证书已打印。"
      elif [ "$FP_RELAY" = "$FP_DIRECT" ]; then
        pass "证书指纹与直连落地完全一致：TLS 直通，中转没动过任何字节"
      else
        fail "证书指纹与直连落地不一致！中转可能被中间人动过，或域名同时指了别的机器。立即排查，不要用。"
      fi
    fi
  fi
fi

echo ""
echo "== UDP 端口说明（curl 测不了 UDP） =="
echo "  8388/8443/8444 这类 UDP 端口用客户端实测最准："
echo "  手机连上节点后，SS / HY2 / TUIC 能连上、速度正常就是通的。"
echo "  若 UDP 不通，先查云厂商安全组有没有放行对应 UDP 端口（TCP 放了不等于 UDP 放了），"
echo "  再查 ss -ulnp 确认 realm 在监听。"
echo ""
echo "自检完成。"
