#!/usr/bin/env bash
# 中转自检：在中转机上运行，检查 realm 进程、监听端口、TLS 直通（证书与落地一致）
# 用法：bash check.sh
#   bash check.sh --json   机器可读输出（面板调用）
set -uo pipefail

JSON_MODE=0
[ "${1:-}" = "--json" ] && JSON_MODE=1

jesc() { # $1=字符串 -> JSON 转义后输出（不带外层引号）
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  s="${s//$'\n'/\\n}"
  s="${s//$'\r'/}"
  s="${s//$'\t'/\\t}"
  printf '%s' "$s"
}

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

if [ "$JSON_MODE" = "1" ]; then
  # ---- realm 运行状态 ----
  _rj_running=0; _rj_version=""; _rj_bbr=""
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl is-active --quiet realm 2>/dev/null && _rj_running=1
  elif pgrep -x realm >/dev/null 2>&1; then
    _rj_running=1
  fi
  _rj_version="$(/usr/local/bin/realm --version 2>/dev/null | head -1 || true)"
  _rj_bbr="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo "?")"
  if [ "$_rj_running" = "1" ]; then
    _sec_realm_status="pass"; _sec_realm_items='{"level":"pass","message":"realm 正在运行"}'
  else
    _sec_realm_status="fail"; _sec_realm_items='{"level":"fail","message":"realm 没在运行"}'
  fi
  if [ "$_rj_bbr" = "bbr" ]; then _sec_bbr_status="pass"; else _sec_bbr_status="warn"; fi

  # ---- 逐落地：端口监听 + TLS 指纹 + 证书天数 ----
  _ports_json=""; _certs_json=""; _issues_json=""
  _sec_ports_items=""; _sec_ports_status="pass"
  _sec_tls_items=""; _sec_tls_status="pass"
  _sec_cert_items=""; _sec_cert_status="pass"
  [ "$_rj_running" = "0" ] && _issues_json="{\"code\":\"realm_down\",\"landing\":0,\"message\":\"realm 没在运行\",\"fix\":\"restart_realm\"}"
  _i=1
  while [ "$_i" -le "$LANDING_COUNT" ]; do
    if [ "$_i" = "1" ]; then
      _L="$LANDING"; _P="$PORTS"; _D="$DOMAIN"
    else
      _L="$(landing_field "$_i" LANDING)"; _P="$(landing_field "$_i" PORTS)"; _D="$(landing_field "$_i" DOMAIN)"
    fi
    if [ -z "$_L" ]; then _i=$((_i + 1)); continue; fi
    # 端口监听
    for _spec in $_P; do
      _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
      if port_listening "$_proto" "$_lp"; then
        _lis="true"
      else
        _lis="false"; _sec_ports_status="fail"
        _m="落地 $_i 端口 $_lp/$_proto 没监听"
        [ -n "$_sec_ports_items" ] && _sec_ports_items="$_sec_ports_items,"
        _sec_ports_items="$_sec_ports_items{\"level\":\"fail\",\"message\":\"$(jesc "$_m")\"}"
        [ -n "$_issues_json" ] && _issues_json="$_issues_json,"
        _issues_json="$_issues_json{\"code\":\"port_not_listening\",\"landing\":$_i,\"message\":\"$(jesc "$_m")\",\"fix\":\"restart_realm\"}"
      fi
      [ -n "$_ports_json" ] && _ports_json="$_ports_json,"
      _ports_json="$_ports_json{\"landing_index\":$_i,\"proto\":\"$_proto\",\"listen\":$_lp,\"remote\":$_rp,\"listening\":$_lis}"
    done
    # TLS 端点：优先监听 443，其次落地 443
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
    # 续签方式（与 5/5 同一套判定）
    _renew="自动"; _need_manual=false
    case " $_P " in
      *" tcp:80:80 "*) _renew="自动（走 80）" ;;
      *) if [ "$SHARE443" = "1" ]; then _renew="自动（走 443 SNI 分流）"; else _renew="手动（80 改了监听端口，见 README“手动续签证书”）"; _need_manual=true; fi ;;
    esac
    if [ -z "$TLS_LISTEN" ]; then
      # 明文落地，没有证书
      [ -n "$_certs_json" ] && _certs_json="$_certs_json,"
      _certs_json="$_certs_json{\"index\":$_i,\"landing\":\"$(jesc "$_L")\",\"domain\":\"$(jesc "$_D")\",\"days_left\":null,\"expiry\":null,\"renew_mode\":\"$(jesc "$_renew")\",\"need_manual\":$_need_manual}"
      _i=$((_i + 1))
      continue
    fi
    SNI_ARGS=()
    [ -n "$_D" ] && SNI_ARGS=(-servername "$_D")
    if command -v openssl >/dev/null 2>&1; then
      FP_RELAY="$(echo | timeout 10 openssl s_client -connect 127.0.0.1:$TLS_LISTEN "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -fingerprint -sha256 -noout 2>/dev/null)"
      ENDDATE="$(echo | timeout 10 openssl s_client -connect 127.0.0.1:$TLS_LISTEN "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -enddate -noout 2>/dev/null)"
    else
      FP_RELAY=""; ENDDATE=""
    fi
    # TLS 直通
    if [ -z "$FP_RELAY" ]; then
      _sec_tls_status="fail"
      _m="落地 $_i 穿过中转连 $TLS_LISTEN 拿不到证书"
      [ -n "$_sec_tls_items" ] && _sec_tls_items="$_sec_tls_items,"
      _sec_tls_items="$_sec_tls_items{\"level\":\"fail\",\"message\":\"$(jesc "$_m")\"}"
      [ -n "$_issues_json" ] && _issues_json="$_issues_json,"
      _issues_json="$_issues_json{\"code\":\"tls_unreachable\",\"landing\":$_i,\"message\":\"$(jesc "$_m")\",\"fix\":\"restart_realm\"}"
    elif [ "$_L" = "<未知，/etc/realm/relay.conf 缺失>" ]; then
      _m="落地 $_i 经中转的证书已拿到（指纹 $(jesc "$FP_RELAY")），不知道落地地址，没法直连对比"
      [ -n "$_sec_tls_items" ] && _sec_tls_items="$_sec_tls_items,"
      _sec_tls_items="$_sec_tls_items{\"level\":\"warn\",\"message\":\"$(jesc "$_m")\"}"
      [ "$_sec_tls_status" = "pass" ] && _sec_tls_status="warn"
    else
      FP_DIRECT="$(echo | timeout 10 openssl s_client -connect "$_L:$TLS_REMOTE" "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -fingerprint -sha256 -noout 2>/dev/null)"
      if [ -z "$FP_DIRECT" ]; then
        _m="落地 $_i 直连拿不到证书（经中转的证书已拿到）"
        [ -n "$_sec_tls_items" ] && _sec_tls_items="$_sec_tls_items,"
        _sec_tls_items="$_sec_tls_items{\"level\":\"warn\",\"message\":\"$(jesc "$_m")\"}"
        [ "$_sec_tls_status" = "pass" ] && _sec_tls_status="warn"
      elif [ "$FP_RELAY" = "$FP_DIRECT" ]; then
        _m="落地 $_i 证书指纹与直连落地完全一致"
        [ -n "$_sec_tls_items" ] && _sec_tls_items="$_sec_tls_items,"
        _sec_tls_items="$_sec_tls_items{\"level\":\"pass\",\"message\":\"$(jesc "$_m")\"}"
      else
        _sec_tls_status="fail"
        _m="落地 $_i 证书指纹与直连落地不一致"
        [ -n "$_sec_tls_items" ] && _sec_tls_items="$_sec_tls_items,"
        _sec_tls_items="$_sec_tls_items{\"level\":\"fail\",\"message\":\"$(jesc "$_m")\"}"
        [ -n "$_issues_json" ] && _issues_json="$_issues_json,"
        _issues_json="$_issues_json{\"code\":\"fingerprint_mismatch\",\"landing\":$_i,\"message\":\"$(jesc "$_m")\",\"fix\":\"manual\"}"
      fi
    fi
    # 证书天数
    _days_json="null"; _exp_json="null"; _cert_level="pass"; _cert_msg=""
    if [ -n "$ENDDATE" ]; then
      END_STR="${ENDDATE#notAfter=}"
      END_TS="$(date -d "$END_STR" +%s 2>/dev/null || echo "")"
      if [ -n "$END_TS" ]; then
        NOW_TS="$(date +%s)"
        DAYS=$(( (END_TS - NOW_TS) / 86400 ))
        END_FMT="$(date -d "@$END_TS" +%Y-%m-%d 2>/dev/null || echo "$END_STR")"
        _days_json="$DAYS"; _exp_json="\"$(jesc "$END_FMT")\""
        if [ "$DAYS" -lt 0 ]; then
          _cert_level="fail"; _cert_msg="落地 $_i 证书已过期 $((0 - DAYS)) 天（到期 $END_FMT）"
          _sec_cert_status="fail"
          [ -n "$_issues_json" ] && _issues_json="$_issues_json,"
          _issues_json="$_issues_json{\"code\":\"cert_expired\",\"landing\":$_i,\"message\":\"$(jesc "$_cert_msg")\",\"fix\":\"manual_renew\"}"
        elif [ "$DAYS" -lt 7 ]; then
          _cert_level="fail"; _cert_msg="落地 $_i 证书只剩 $DAYS 天（到期 $END_FMT）"
          _sec_cert_status="fail"
          [ -n "$_issues_json" ] && _issues_json="$_issues_json,"
          _issues_json="$_issues_json{\"code\":\"cert_expiring\",\"landing\":$_i,\"message\":\"$(jesc "$_cert_msg")\",\"fix\":\"manual_renew\"}"
        elif [ "$DAYS" -lt 30 ]; then
          _cert_level="warn"; _cert_msg="落地 $_i 证书剩余 $DAYS 天（到期 $END_FMT）"
          [ "$_sec_cert_status" = "pass" ] && _sec_cert_status="warn"
          [ -n "$_issues_json" ] && _issues_json="$_issues_json,"
          _issues_json="$_issues_json{\"code\":\"cert_expiring\",\"landing\":$_i,\"message\":\"$(jesc "$_cert_msg")\",\"fix\":\"manual_renew\"}"
        else
          _cert_msg="落地 $_i 证书剩余 $DAYS 天（到期 $END_FMT）"
        fi
      else
        _cert_level="warn"; _cert_msg="落地 $_i 证书到期日解析失败"
        [ "$_sec_cert_status" = "pass" ] && _sec_cert_status="warn"
      fi
    else
      _cert_level="warn"; _cert_msg="落地 $_i 经中转拿不到证书，算不出到期天数"
      [ "$_sec_cert_status" = "pass" ] && _sec_cert_status="warn"
    fi
    [ -n "$_sec_cert_items" ] && _sec_cert_items="$_sec_cert_items,"
    _sec_cert_items="$_sec_cert_items{\"level\":\"$_cert_level\",\"message\":\"$(jesc "$_cert_msg")\"}"
    [ -n "$_certs_json" ] && _certs_json="$_certs_json,"
    _certs_json="$_certs_json{\"index\":$_i,\"landing\":\"$(jesc "$_L")\",\"domain\":\"$(jesc "$_D")\",\"days_left\":$_days_json,\"expiry\":$_exp_json,\"renew_mode\":\"$(jesc "$_renew")\",\"need_manual\":$_need_manual}"
    _i=$((_i + 1))
  done
  [ -z "$_sec_ports_items" ] && _sec_ports_items='{"level":"pass","message":"监听端口全部正常"}'
  [ -z "$_sec_tls_items" ] && _sec_tls_items='{"level":"pass","message":"无 TLS 落地，跳过直通验证"}'
  [ -z "$_sec_cert_items" ] && _sec_cert_items='{"level":"pass","message":"无证书落地"}'
  printf '%s' "{\"ok\":true,\"realm\":{\"running\":$([ "$_rj_running" = "1" ] && echo true || echo false),\"version\":\"$(jesc "$_rj_version")\",\"bbr\":\"$(jesc "$_rj_bbr")\"},\"sections\":[{\"name\":\"realm 进程\",\"status\":\"$_sec_realm_status\",\"items\":[$_sec_realm_items]},{\"name\":\"监听端口\",\"status\":\"$_sec_ports_status\",\"items\":[$_sec_ports_items]},{\"name\":\"BBR\",\"status\":\"$_sec_bbr_status\",\"items\":[{\"level\":\"$_sec_bbr_status\",\"message\":\"BBR：$(jesc "$_rj_bbr")\"}]},{\"name\":\"TLS 直通验证\",\"status\":\"$_sec_tls_status\",\"items\":[$_sec_tls_items]},{\"name\":\"证书到期天数\",\"status\":\"$_sec_cert_status\",\"items\":[$_sec_cert_items]}],\"certs\":[$_certs_json],\"ports\":[$_ports_json],\"issues\":[$_issues_json]}"
  echo ""
  exit 0
fi

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
