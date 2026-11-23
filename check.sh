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
# 测试可覆盖：RELAY_CONF/REALM_TOML 环境变量（默认 /etc/realm 下的真实文件）；
# 其余命令（ss/openssl/systemctl/sysctl/timeout）走 PATH，测试用桩脚本。
RELAY_CONF="${RELAY_CONF:-/etc/realm/relay.conf}"
REALM_TOML="${REALM_TOML:-/etc/realm/config.toml}"
LANDING=""; LANDING_IS_DOMAIN=0; DOMAIN=""; PORTS=""
[ -f "$RELAY_CONF" ] && set -a && . "$RELAY_CONF" && set +a
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

pick_tls_endpoint() { # $1=PORTS $2=证书域名(SNI，可空) -> 打印 "监听端口 落地端口"，找不到返回 1
  # 找 TLS 端点：优先监听 443，其次落地 443，再逐个 TCP 端口握手试
  # （自定义端口 TLS 如 24443 就靠握手这一档；明文端口握手立刻失败，不误判）。
  # 端口在监听但握手失败的也算端点（返回它，下游会报“拿不到证书”而不是误判明文）。
  local _spec _proto _rest _lp _rp _fp _first_listening_lp="" _first_listening_rp=""
  for _spec in $1; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
    if [ "$_proto" = "tcp" ] && [ "$_lp" = "443" ]; then printf '%s %s' "$_lp" "$_rp"; return 0; fi
  done
  for _spec in $1; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
    if [ "$_proto" = "tcp" ] && [ "$_rp" = "443" ]; then printf '%s %s' "$_lp" "$_rp"; return 0; fi
  done
  if command -v openssl >/dev/null 2>&1; then
    for _spec in $1; do
      _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
      [ "$_proto" = "tcp" ] || continue
      if port_listening tcp "$_lp"; then
        [ -z "$_first_listening_lp" ] && _first_listening_lp="$_lp" && _first_listening_rp="$_rp"
        if [ -n "$2" ]; then
          _fp="$(echo | timeout 3 openssl s_client -connect 127.0.0.1:"$_lp" -servername "$2" 2>/dev/null | openssl x509 -fingerprint -sha256 -noout 2>/dev/null)"
        else
          _fp="$(echo | timeout 3 openssl s_client -connect 127.0.0.1:"$_lp" 2>/dev/null | openssl x509 -fingerprint -sha256 -noout 2>/dev/null)"
        fi
        if [ -n "$_fp" ]; then printf '%s %s' "$_lp" "$_rp"; return 0; fi
      fi
    done
  fi
  if [ -n "$_first_listening_lp" ]; then printf '%s %s' "$_first_listening_lp" "$_first_listening_rp"; return 0; fi
  return 1
}

tcp_probe() { # $1=本机端口 -> TCP 能连通返回 0（bash /dev/tcp，不依赖 nc）
  timeout 3 bash -c ": >/dev/tcp/127.0.0.1/$1" 2>/dev/null
}

listener_proc() { # $1=TCP 端口 -> 监听进程名（ss -tlnp 的 users 字段），查不到为空
  command -v ss >/dev/null 2>&1 || return 0
  ss -tlnp 2>/dev/null | grep -E "[:.]$1[[:space:]]" | grep -oE 'users:\(\("[^"]+"' | head -1 | sed -E 's/^users:\(\("//; s/"$//'
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
  # use_udp 全局开关（config.toml）：UDP 条目没监听时，先看它是不是根本没开。
  _USE_UDP=""
  if [ -f "$REALM_TOML" ]; then
    _USE_UDP="$(grep -E '^[[:space:]]*use_udp[[:space:]]*=' "$REALM_TOML" 2>/dev/null | tail -1 | sed -E 's/^[^=]*=[[:space:]]*//; s/[[:space:]]*(#.*)?$//; s/"//g' | tr '[:upper:]' '[:lower:]')"
  fi
  # 任一落地 TLS 握手失败过（证书段没有任何证书项时，默认文案不能再写“无证书落地”）
  _tls_failed_any=0
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
      _L="$LANDING"; _P="$PORTS"; _D="$DOMAIN"; _M="${MODE:-standard}"
    else
      _L="$(landing_field "$_i" LANDING)"; _P="$(landing_field "$_i" PORTS)"; _D="$(landing_field "$_i" DOMAIN)"; _M="$(landing_field "$_i" MODE)"
      [ -n "$_M" ] || _M="standard"
    fi
    if [ -z "$_L" ]; then _i=$((_i + 1)); continue; fi
    # 证书签发方式（relay.conf 的 CERT_MODE[_N]，没有就是老落地，按原逻辑）
    _CM="$(landing_field "$_i" CERT_MODE)"
    # UDP 需不需要（relay.conf 的 NEEDS_UDP[_N]，面板按落地实际节点协议算：
    # 有 Hysteria2/TUIC 节点→1，否则 0；老 relay.conf 没这标记时按旧行为检查）。
    _NEEDS_UDP="$(landing_field "$_i" NEEDS_UDP)"
    # 端口监听
    for _spec in $_P; do
      _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
      if [ "$_proto" = "udp" ] && [ "$_NEEDS_UDP" = "0" ]; then
        # 该落地用不上 UDP（纯 TCP 协议，如 VLESS），不查监听，记一条通过原因。
        _m="落地 $_i 端口 $_lp/udp 跳过：该落地无 UDP 协议节点（NEEDS_UDP=0），TCP 协议不查 UDP"
        [ -n "$_sec_ports_items" ] && _sec_ports_items="$_sec_ports_items,"
        _sec_ports_items="$_sec_ports_items{\"level\":\"pass\",\"message\":\"$(jesc "$_m")\"}"
        continue
      fi
      if port_listening "$_proto" "$_lp"; then
        _lis="true"
      else
        _lis="false"; _sec_ports_status="fail"
        if [ "$_proto" = "udp" ] && [ "$_USE_UDP" = "false" ]; then
          # 真因：中转根本没开 UDP 转发（use_udp 关着），不是端口本身的问题。
          _m="落地 $_i 端口 $_lp/udp 没监听：中转没开 UDP 转发（config.toml 里 use_udp 关着），开了开关再重启 realm 才能转 UDP"
          [ -n "$_sec_ports_items" ] && _sec_ports_items="$_sec_ports_items,"
          _sec_ports_items="$_sec_ports_items{\"level\":\"fail\",\"message\":\"$(jesc "$_m")\"}"
          [ -n "$_issues_json" ] && _issues_json="$_issues_json,"
          _issues_json="$_issues_json{\"code\":\"udp_disabled\",\"landing\":$_i,\"message\":\"$(jesc "$_m")\",\"fix\":\"enable_udp\"}"
        else
          _m="落地 $_i 端口 $_lp/$_proto 没监听"
          [ -n "$_sec_ports_items" ] && _sec_ports_items="$_sec_ports_items,"
          _sec_ports_items="$_sec_ports_items{\"level\":\"fail\",\"message\":\"$(jesc "$_m")\"}"
          [ -n "$_issues_json" ] && _issues_json="$_issues_json,"
          _issues_json="$_issues_json{\"code\":\"port_not_listening\",\"landing\":$_i,\"message\":\"$(jesc "$_m")\",\"fix\":\"restart_realm\"}"
        fi
      fi
      [ -n "$_ports_json" ] && _ports_json="$_ports_json,"
      _ports_json="$_ports_json{\"landing_index\":$_i,\"proto\":\"$_proto\",\"listen\":$_lp,\"remote\":$_rp,\"listening\":$_lis}"
    done
    # TLS 端点：优先监听 443，其次落地 443，再逐个 TCP 端口握手试（自定义端口 TLS 如 24443）
    TLS_LISTEN=""; TLS_REMOTE=""
    if _ep="$(pick_tls_endpoint "$_P" "$_D")"; then
      TLS_LISTEN="${_ep%% *}"; TLS_REMOTE="${_ep##* }"
    fi
    # 续签方式（与 5/5 同一套判定）；DNS-01 走 Cloudflare DNS 验证续签，
    # 根本不走 80 端口，落地机自动续；任意门模式域名指落地，续签直接走落地
    _renew="自动"; _need_manual=false
    if [ "$_CM" = "dns01" ]; then
      _renew="自动（DNS 验证）"
    elif [ "$_M" = "door" ]; then
      _renew="自动（域名指落地，直接续签）"
    else
      case " $_P " in
        *" tcp:80:80 "*) _renew="自动（走 80）" ;;
        *) if [ "$SHARE443" = "1" ]; then _renew="自动（走 443 SNI 分流）"; else _renew="手动（80 改了监听端口，见 README“手动续签证书”）"; _need_manual=true; fi ;;
      esac
    fi
    if [ -z "$TLS_LISTEN" ]; then
      # 明文落地，没有证书（续签方式也按明文算：前面按“没 80 就手动”算出的不适用）；
      # DNS-01 落地即使暂时拿不到证书，续签方式仍是自动（DNS 验证），不改。
      if [ "$_CM" != "dns01" ]; then
        _renew="无证书（明文模式）"; _need_manual=false
      fi
      [ -n "$_certs_json" ] && _certs_json="$_certs_json,"
      _certs_json="$_certs_json{\"index\":$_i,\"landing\":\"$(jesc "$_L")\",\"domain\":\"$(jesc "$_D")\",\"mode\":\"$_M\",\"days_left\":null,\"expiry\":null,\"renew_mode\":\"$(jesc "$_renew")\",\"need_manual\":$_need_manual}"
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
    # TLS 直通（分段定位：①TCP 连监听端口 → ②看谁在监听 → ③经中转握手 →
    # ④直连落地对比；TLS 项先攒在 _tls_msg 里，证书段可能改文案，所以等
    # 证书段跑完才追加到 _sec_tls_items）。
    _tls_msg=""; _tls_lvl="pass"; _tls_failed=0; _tls_issue_fix=""
    if [ -z "$FP_RELAY" ]; then
      _tls_failed=1; _tls_lvl="fail"; _tls_issue_fix="restart_realm"
      _sec_tls_status="fail"
      if ! tcp_probe "$TLS_LISTEN"; then
        # ① TCP 都不通：看端口谁在监听
        _occ="$(listener_proc "$TLS_LISTEN")"
        if [ -n "$_occ" ] && [ "$_occ" != "realm" ]; then
          _tls_msg="落地 $_i 监听端口 $TLS_LISTEN 被非 realm 程序占用（进程：$_occ），TLS 握手连不上；先停掉它或改端口"
          _tls_issue_fix="manual"
        elif [ -n "$_occ" ]; then
          _tls_msg="落地 $_i 穿过中转连 $TLS_LISTEN 拿不到证书：realm 在监听但握手失败，重启 realm 试试"
        else
          _tls_msg="落地 $_i 监听端口 $TLS_LISTEN 没人监听（realm 可能没起来或转发没配好），TLS 握手连不上"
        fi
      elif [ "$_L" = "<未知，/etc/realm/relay.conf 缺失>" ]; then
        _tls_msg="落地 $_i 穿过中转连 $TLS_LISTEN 拿不到证书（不知道落地地址，没法直连对比）"
      else
        # ② TCP 通但握手失败：直连落地对比，分清中转还是落地的问题
        FP_DIRECT="$(echo | timeout 10 openssl s_client -connect "$_L:$TLS_REMOTE" "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -fingerprint -sha256 -noout 2>/dev/null)"
        if [ -n "$FP_DIRECT" ]; then
          _tls_msg="落地 $_i 直连落地正常，中转转发有问题（经中转连 $TLS_LISTEN 拿不到证书，直连 $_L:$TLS_REMOTE 能拿到）"
        else
          # 直连也失败：再试一次不带 SNI，通了就是 SNI/域名问题
          FP_NOSNI="$(echo | timeout 10 openssl s_client -connect "$_L:$TLS_REMOTE" 2>/dev/null | openssl x509 -fingerprint -sha256 -noout 2>/dev/null)"
          if [ -n "$FP_NOSNI" ]; then
            _tls_msg="落地 $_i 落地 TLS 本身拿不到证书（带 SNI 失败、不带 SNI 能拿到，可能是 SNI/域名不对）"
            _tls_issue_fix="manual"
          else
            _tls_msg="落地 $_i 落地 TLS 本身拿不到证书（直连 $_L:$TLS_REMOTE 也失败），先查落地证书与端口"
            _tls_issue_fix="manual"
          fi
        fi
      fi
    elif [ "$_L" = "<未知，/etc/realm/relay.conf 缺失>" ]; then
      _tls_msg="落地 $_i 经中转的证书已拿到（指纹 $(jesc "$FP_RELAY")），不知道落地地址，没法直连对比"
      _tls_lvl="warn"
      [ "$_sec_tls_status" = "pass" ] && _sec_tls_status="warn"
    else
      FP_DIRECT="$(echo | timeout 10 openssl s_client -connect "$_L:$TLS_REMOTE" "${SNI_ARGS[@]}" 2>/dev/null | openssl x509 -fingerprint -sha256 -noout 2>/dev/null)"
      if [ -z "$FP_DIRECT" ]; then
        _tls_msg="落地 $_i 直连拿不到证书（经中转的证书已拿到）"
        _tls_lvl="warn"
        [ "$_sec_tls_status" = "pass" ] && _sec_tls_status="warn"
      elif [ "$FP_RELAY" = "$FP_DIRECT" ]; then
        _tls_msg="落地 $_i 证书指纹与直连落地完全一致"
        _tls_lvl="pass"
      else
        _sec_tls_status="fail"
        _tls_msg="落地 $_i 证书指纹与直连落地不一致"
        _tls_lvl="fail"
        [ -n "$_issues_json" ] && _issues_json="$_issues_json,"
        _issues_json="$_issues_json{\"code\":\"fingerprint_mismatch\",\"landing\":$_i,\"message\":\"$(jesc "$_tls_msg")\",\"fix\":\"manual\"}"
      fi
    fi
    # 证书天数（ENDDATE 为空 = 握手失败：不再单独记 warn 回声，把原因合并进 TLS 项）
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
    elif [ "$_tls_failed" = "1" ]; then
      # 握手失败的回声：不单独记证书项，原因合并进 TLS 项文案。
      _tls_msg="$_tls_msg（证书到期天数随本次失败，未单独计算）"
      _tls_failed_any=1
    else
      _cert_level="warn"; _cert_msg="落地 $_i 经中转拿不到证书，算不出到期天数"
      [ "$_sec_cert_status" = "pass" ] && _sec_cert_status="warn"
    fi
    # TLS 项（证书段可能改过文案，所以这里才追加）与证书项
    if [ -n "$_tls_msg" ]; then
      [ -n "$_sec_tls_items" ] && _sec_tls_items="$_sec_tls_items,"
      _sec_tls_items="$_sec_tls_items{\"level\":\"$_tls_lvl\",\"message\":\"$(jesc "$_tls_msg")\"}"
    fi
    if [ -n "$_tls_issue_fix" ]; then
      [ -n "$_issues_json" ] && _issues_json="$_issues_json,"
      _issues_json="$_issues_json{\"code\":\"tls_unreachable\",\"landing\":$_i,\"message\":\"$(jesc "$_tls_msg")\",\"fix\":\"$_tls_issue_fix\"}"
    fi
    if [ -n "$_cert_msg" ]; then
      [ -n "$_sec_cert_items" ] && _sec_cert_items="$_sec_cert_items,"
      _sec_cert_items="$_sec_cert_items{\"level\":\"$_cert_level\",\"message\":\"$(jesc "$_cert_msg")\"}"
    fi
    [ -n "$_certs_json" ] && _certs_json="$_certs_json,"
    _certs_json="$_certs_json{\"index\":$_i,\"landing\":\"$(jesc "$_L")\",\"domain\":\"$(jesc "$_D")\",\"mode\":\"$_M\",\"days_left\":$_days_json,\"expiry\":$_exp_json,\"renew_mode\":\"$(jesc "$_renew")\",\"need_manual\":$_need_manual}"
    _i=$((_i + 1))
  done
  [ -z "$_sec_ports_items" ] && _sec_ports_items='{"level":"pass","message":"监听端口全部正常"}'
  [ -z "$_sec_tls_items" ] && _sec_tls_items='{"level":"pass","message":"无 TLS 落地，跳过直通验证"}'
  if [ -z "$_sec_cert_items" ]; then
    if [ "$_tls_failed_any" = "1" ]; then
      # 握手失败导致没有任何证书项：不是“无证书落地”，是没拿到证书算不出。
      _sec_cert_items='{"level":"warn","message":"证书到期随 TLS 直通验证失败，未单独计算"}'
      _sec_cert_status="warn"
    else
      _sec_cert_items='{"level":"pass","message":"无证书落地"}'
    fi
  fi
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
    _L="$LANDING"; _P="$PORTS"; _M="${MODE:-standard}"
  else
    _L="$(landing_field "$_i" LANDING)"; _P="$(landing_field "$_i" PORTS)"; _M="$(landing_field "$_i" MODE)"
    [ -n "$_M" ] || _M="standard"
  fi
  if [ -z "$_L" ]; then _i=$((_i + 1)); continue; fi
  if [ "$_M" = "door" ]; then
    echo "  落地 $_i：$_L（任意门模式，中转地址 ${RELAY_ADDR:-未设置}，转发端口见下）"
  else
    echo "  落地 $_i：$_L"
  fi
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
      _L="$LANDING"; _P="$PORTS"; _D="$DOMAIN"; _M="${MODE:-standard}"
    else
      _L="$(landing_field "$_i" LANDING)"; _P="$(landing_field "$_i" PORTS)"; _D="$(landing_field "$_i" DOMAIN)"; _M="$(landing_field "$_i" MODE)"
      [ -n "$_M" ] || _M="standard"
    fi
    if [ -z "$_L" ]; then _i=$((_i + 1)); continue; fi
    if [ "$_M" = "door" ]; then
      echo "  落地 $_i：$_L（任意门模式，转发端口验 TLS 指纹）"
    else
      echo "  落地 $_i：$_L"
    fi
    # 找到 TLS 端点：优先监听 443，其次落地 443，再逐个 TCP 端口握手试（自定义端口 TLS 如 24443）
    TLS_LISTEN=""; TLS_REMOTE=""
    if _ep="$(pick_tls_endpoint "$_P" "$_D")"; then
      TLS_LISTEN="${_ep%% *}"; TLS_REMOTE="${_ep##* }"
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
      _L="$LANDING"; _P="$PORTS"; _D="$DOMAIN"; _M="${MODE:-standard}"
    else
      _L="$(landing_field "$_i" LANDING)"; _P="$(landing_field "$_i" PORTS)"; _D="$(landing_field "$_i" DOMAIN)"; _M="$(landing_field "$_i" MODE)"
      [ -n "$_M" ] || _M="standard"
    fi
    if [ -z "$_L" ]; then _i=$((_i + 1)); continue; fi
    # 证书签发方式（relay.conf 的 CERT_MODE[_N]，没有就是老落地，按原逻辑）
    _CM="$(landing_field "$_i" CERT_MODE)"
    # 找 TLS 端点（和 4/5 同一套逻辑：优先监听 443，其次落地 443，再逐个 TCP 端口握手试）
    TLS_LISTEN=""; TLS_REMOTE=""
    if _ep="$(pick_tls_endpoint "$_P" "$_D")"; then
      TLS_LISTEN="${_ep%% *}"; TLS_REMOTE="${_ep##* }"
    fi
    if [ -z "$TLS_LISTEN" ]; then
      echo "  落地 $_i：$_L（没配 443，明文模式，没有证书）"
      _i=$((_i + 1))
      continue
    fi
    # 续签方式：80 直通或 80 分流都是自动；443 分流也是自动；只有 80 改了监听端口的才要手动
    # 任意门模式域名指落地，续签直接走落地，也是自动；
    # DNS-01 走 Cloudflare DNS 验证续签，不走 80 端口，落地机自动续
    _renew="自动"
    if [ "$_CM" = "dns01" ]; then
      _renew="自动（DNS 验证）"
    elif [ "$_M" = "door" ]; then
      _renew="自动（域名指落地，直接续签）"
    else
      case " $_P " in
        *" tcp:80:80 "*) _renew="自动（走 80）" ;;
        *)
          if [ "$SHARE443" = "1" ]; then _renew="自动（走 443 SNI 分流）"; else _renew="手动（80 改了监听端口，见 README“手动续签证书”）"; fi
          ;;
      esac
    fi
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
