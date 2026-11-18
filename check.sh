#!/usr/bin/env bash
# 中转自检：在中转机上运行，检查 realm 进程、监听端口、TLS 直通（证书与落地一致）
# 用法：bash check.sh
set -uo pipefail

pass() { echo "  [通过] $1"; }
warn() { echo "  [注意] $1"; }
fail() { echo "  [风险] $1"; }

# 读 install.sh 写的状态文件；没有就用默认端口表
# 多落地：1 号落地用平铺变量（LANDING/PORTS），2 号起用 _编号 后缀；老版本单落地
# relay.conf 没有 LANDING_COUNT，按 1 个落地读，向后兼容。
# SHARE80/SHARE443：80/443 是否走了分流共享（老 relay.conf 没有，按 0 读）。
LANDING=""; LANDING_IS_DOMAIN=0; DOMAIN=""; PORTS=""
[ -f /etc/realm/relay.conf ] && set -a && . /etc/realm/relay.conf && set +a
LANDING_COUNT="${LANDING_COUNT:-1}"
SHARE80="${SHARE80:-0}"
SHARE443="${SHARE443:-0}"
[ -n "$PORTS" ] || PORTS="tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388 udp:8443:8443 udp:8444:8444"
[ -n "$LANDING" ] || LANDING="<未知，/etc/realm/relay.conf 缺失>"

landing_field() { # $1=落地编号 $2=字段名 -> 打印值；编号 1 用平铺变量，>=2 用 _编号 后缀
  local _idx="$1" _field="$2"
  local _var="$_field"
  [ "$_idx" != "1" ] && _var="${_field}_$_idx"
  printf '%s' "${!_var:-}"
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

echo "== 1/5 realm 进程 =="
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
echo "== 2/5 监听端口（共 $LANDING_COUNT 个落地） =="
_i=1
while [ "$_i" -le "$LANDING_COUNT" ]; do
  if [ "$_i" = "1" ]; then
    _L="$LANDING"; _P="$PORTS"
  else
    _L="$(landing_field "$_i" LANDING)"; _P="$(landing_field "$_i" PORTS)"
  fi
  if [ -z "$_L" ]; then _i=$((_i + 1)); continue; fi
  echo "  落地 $_i：$_L"
  for _spec in $_P; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
    if port_listening "$_proto" "$_lp"; then
      pass "$_lp/$_proto 监听中 -> $_L:$_rp"
    else
      fail "$_lp/$_proto 没监听：realm 可能没起来，或端口被别的程序占了（ss -tlnp / ss -ulnp 查）"
    fi
  done
  _i=$((_i + 1))
done

echo ""
echo "== 3/5 BBR =="
BBR="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo "?")"
if [ "$BBR" = "bbr" ]; then
  pass "BBR 已启用（$BBR）"
else
  warn "BBR 未启用（当前 $BBR）。重跑 install.sh 会补开；不影响转发，只影响弱网速度。"
fi

echo ""
echo "== 4/5 TLS 直通验证（每个落地各验一次，证书必须与落地一模一样） =="
# 原理：realm 只转发字节不解密，穿过中转拿到的证书指纹必须和直连落地的一致。
# 有域名就用它做 SNI；没有就直接连（单域名落地也能比出指纹）。
if ! command -v openssl >/dev/null 2>&1; then
  warn "本机无 openssl，装一个再验：apt install openssl"
else
  _i=1
  while [ "$_i" -le "$LANDING_COUNT" ]; do
    if [ "$_i" = "1" ]; then
      _L="$LANDING"; _P="$PORTS"; _D="$DOMAIN"
    else
      _L="$(landing_field "$_i" LANDING)"; _P="$(landing_field "$_i" PORTS)"; _D="$(landing_field "$_i" DOMAIN)"
    fi
    if [ -z "$_L" ]; then _i=$((_i + 1)); continue; fi
    echo "  落地 $_i：$_L"
    # 找到 TLS 端点：优先监听端口是 443 的那条，其次找 remote 是 443 的那条
    TLS_LISTEN=""; TLS_REMOTE=""
    for _spec in $_P; do
      _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
      if [ "$_proto" = "tcp" ] && [ "$_lp" = "443" ]; then TLS_LISTEN="$_lp"; TLS_REMOTE="$_rp"; break; fi
    done
    if [ -z "$TLS_LISTEN" ]; then
      for _spec in $_P; do
        _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
        if [ "$_proto" = "tcp" ] && [ "$_rp" = "443" ]; then TLS_LISTEN="$_lp"; TLS_REMOTE="$_rp"; break; fi
      done
    fi
    if [ -z "$TLS_LISTEN" ]; then
      warn "落地 $_i 没配到落地 443 的端口，跳过 TLS 直通验证。"
      _i=$((_i + 1))
      continue
    fi
    SNI_ARGS=()
    [ -n "$_D" ] && SNI_ARGS=(-servername "$_D")
    FP_RELAY="$(echo | timeout 10 openssl s_client -connect 127.0.0.1:$TLS_LISTEN "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -fingerprint -sha256 -noout 2>/dev/null)"
    SUBJ_RELAY="$(echo | timeout 10 openssl s_client -connect 127.0.0.1:$TLS_LISTEN "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -subject -noout 2>/dev/null)"
    if [ -z "$FP_RELAY" ]; then
      fail "穿过中转连 $TLS_LISTEN 拿不到证书：realm 没转发通，或落地 $TLS_REMOTE 本身不通。先查落地端口，再查中转到落地的网络."
    else
      echo "  经中转的证书：$SUBJ_RELAY"
      echo "  经中转的指纹：$FP_RELAY"
      if [ "$_L" = "<未知，/etc/realm/relay.conf 缺失>" ]; then
        warn "不知道落地地址，没法直连对比；指纹本身已打印，可拿去和落地证书人工核对。"
      else
        FP_DIRECT="$(echo | timeout 10 openssl s_client -connect "$_L:$TLS_REMOTE" "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -fingerprint -sha256 -noout 2>/dev/null)"
        if [ -z "$FP_DIRECT" ]; then
          warn "直连落地 $_L:$TLS_REMOTE 拿不到证书（落地可能只允许中转 IP 访问，或网络不通）；经中转的证书已打印。"
        elif [ "$FP_RELAY" = "$FP_DIRECT" ]; then
          pass "证书指纹与直连落地完全一致：TLS 直通，中转没动过任何字节"
        else
          fail "证书指纹与直连落地不一致！中转可能被中间人动过，或域名同时指了别的机器。立即排查，不要用。"
        fi
      fi
    fi
    _i=$((_i + 1))
  done
fi

echo ""
echo "== 5/5 证书到期天数（每个落地，不用自己记日子） =="
# 原理：经中转连落地 443 拿证书，读 notAfter 算剩余天数；<30 天提醒，<7 天警告。
# Caddy 只在到期前 30 天内才开始续签，所以手动续签也别太早做，建议到期前 2 周内做。
if ! command -v openssl >/dev/null 2>&1; then
  warn "本机无 openssl，装一个再看：apt install openssl"
else
  _i=1
  while [ "$_i" -le "$LANDING_COUNT" ]; do
    if [ "$_i" = "1" ]; then
      _L="$LANDING"; _P="$PORTS"; _D="$DOMAIN"
    else
      _L="$(landing_field "$_i" LANDING)"; _P="$(landing_field "$_i" PORTS)"; _D="$(landing_field "$_i" DOMAIN)"
    fi
    if [ -z "$_L" ]; then _i=$((_i + 1)); continue; fi
    # 找 TLS 端点（和 4/5 同一套逻辑：优先监听 443，其次落地 443）
    TLS_LISTEN=""; TLS_REMOTE=""
    for _spec in $_P; do
      _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
      if [ "$_proto" = "tcp" ] && [ "$_lp" = "443" ]; then TLS_LISTEN="$_lp"; TLS_REMOTE="$_rp"; break; fi
    done
    if [ -z "$TLS_LISTEN" ]; then
      for _spec in $_P; do
        _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
        if [ "$_proto" = "tcp" ] && [ "$_rp" = "443" ]; then TLS_LISTEN="$_lp"; TLS_REMOTE="$_rp"; break; fi
      done
    fi
    if [ -z "$TLS_LISTEN" ]; then
      echo "  落地 $_i：$_L（没配 443，明文模式，没有证书）"
      _i=$((_i + 1))
      continue
    fi
    # 续签方式：80 直通或 80 分流都是自动；443 分流也是自动；只有 80 改了监听端口的才要手动
    _renew="自动"
    case " $_P " in
      *" tcp:80:80 "*) _renew="自动（走 80）" ;;
      *)
        if [ "$SHARE443" = "1" ]; then _renew="自动（走 443 SNI 分流）"; else _renew="手动（80 改了监听端口，见 README“手动续签证书”）"; fi
        ;;
    esac
    SNI_ARGS=()
    [ -n "$_D" ] && SNI_ARGS=(-servername "$_D")
    ENDDATE="$(echo | timeout 10 openssl s_client -connect 127.0.0.1:$TLS_LISTEN "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -enddate -noout 2>/dev/null)"
    if [ -z "$ENDDATE" ]; then
      warn "落地 $_i（$_L）：经中转拿不到证书，算不出到期天数（先看 4/5 那节）。"
      _i=$((_i + 1))
      continue
    fi
    END_STR="${ENDDATE#notAfter=}"
    END_TS="$(date -d "$END_STR" +%s 2>/dev/null || echo "")"
    if [ -z "$END_TS" ]; then
      warn "落地 $_i（$_L）：证书到期日解析失败：$END_STR"
      _i=$((_i + 1))
      continue
    fi
    NOW_TS="$(date +%s)"
    DAYS=$(( (END_TS - NOW_TS) / 86400 ))
    END_FMT="$(date -d "@$END_TS" +%Y-%m-%d 2>/dev/null || echo "$END_STR")"
    if [ "$DAYS" -lt 0 ]; then
      fail "落地 $_i（$_L）：证书已过期 $((0 - DAYS)) 天（到期 $END_FMT）！立即按 README“手动续签证书”处理。"
    elif [ "$DAYS" -lt 7 ]; then
      fail "落地 $_i（$_L）：证书只剩 $DAYS 天（到期 $END_FMT），续签方式：$_renew。"
    elif [ "$DAYS" -lt 30 ]; then
      warn "落地 $_i（$_L）：证书剩余 $DAYS 天（到期 $END_FMT），续签方式：$_renew。"
    else
      pass "落地 $_i（$_L）：证书剩余 $DAYS 天（到期 $END_FMT），续签方式：$_renew。"
    fi
    _i=$((_i + 1))
  done
fi

echo ""
echo "== UDP 端口说明（curl 测不了 UDP） =="
echo "  UDP 端口（如 8388/8443/8444）用客户端实测最准："
echo "  手机连上节点后，SS / HY2 / TUIC 能连上、速度正常就是通的。"
echo "  若 UDP 不通，先查云厂商安全组有没有放行对应 UDP 端口（TCP 放了不等于 UDP 放了），"
echo "  再查 ss -ulnp 确认 realm 在监听。"
echo ""
echo "自检完成。"
