#!/usr/bin/env bash
# relay-node 分流配置管理（从 install.sh 抽取，与 install.sh 同源逻辑）
#   bash proxy.sh --regen-80        # 按 relay.conf 重生成 nginx 80 分流配置并重载
#   bash proxy.sh --regen-443       # 按 relay.conf 重生成 haproxy 443 SNI 分流配置并重载
#   bash proxy.sh --restart-proxies # 重启/重载已启用的分流组件
#   bash proxy.sh --enable-share80 # 开 80 按域名分流
#   bash proxy.sh --enable-share443# 开 443 按 SNI 分流
# 面板调用；环境变量 REALM_DIR（默认 /etc/realm）、DRY_RUN=1（只写配置不启动）。
# 注意：本文件由 install.sh 的同名函数抽取而成，改分流逻辑时两边同步。
set -uo pipefail
REALM_DIR="${REALM_DIR:-/etc/realm}"
DRY_RUN="${DRY_RUN:-0}"
LANDING="${LANDING:-}"; PORTS="${PORTS:-}"; CERT_DOMAIN="${CERT_DOMAIN:-}"
LANDING_COUNT="${LANDING_COUNT:-1}"
if [ -f "${REALM_DIR}/relay.conf" ]; then
  # 只读 SHARE 标志（不覆盖本轮 LANDING/PORTS/CERT_DOMAIN）
  _s80="$(grep -E '^SHARE80=' "${REALM_DIR}/relay.conf" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"')"; [ -n "$_s80" ] && SHARE80="$_s80"
  _s443="$(grep -E '^SHARE443=' "${REALM_DIR}/relay.conf" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"')"; [ -n "$_s443" ] && SHARE443="$_s443"
  _lc="$(grep -E '^LANDING_COUNT=' "${REALM_DIR}/relay.conf" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"')"; [ -n "$_lc" ] && LANDING_COUNT="$_lc"
fi
SHARE80="${SHARE80:-0}"; SHARE443="${SHARE443:-0}"

ask() {
  local prompt="$1" varname="$2" default="${3:-}"
  if [ -n "${!varname:-}" ]; then return 0; fi
  local ans=""
  if [ -t 0 ]; then
    read -rp "$prompt" ans || true
  elif [ -r /dev/tty ]; then
    { read -rp "$prompt" ans </dev/tty || true; } 2>/dev/null
  fi
  [ -z "$ans" ] && ans="$default"
  printf -v "$varname" '%s' "$ans"
}

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

# ---------- 多落地辅助函数 ----------
suggest_port() { # $1=原端口 -> 打印建议的新监听端口（原端口+10000 走高位段；超了就 +1000，再超就 10000）
  local p="$1" s
  s=$((p + 10000))
  if [ "$s" -gt 65535 ]; then s=$((p + 1000)); fi
  if [ "$s" -gt 65535 ]; then s=10000; fi
  printf '%s' "$s"
}

port_in_config() { # $1=端口 -> ${REALM_DIR}/config.toml 里已有该监听端口返回 0
  [ -f ${REALM_DIR}/config.toml ] && grep -qE "listen = \"0\.0\.0\.0:$1\"" ${REALM_DIR}/config.toml
}

listen_for_remote() { # $1=PORTS $2=落地端口 -> 打印对应的监听端口，找不到返回 1
  local ports="$1" rp="$2" _spec _proto _rest _lp _rp
  for _spec in $ports; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
    if [ "$_rp" = "$rp" ]; then printf '%s' "$_lp"; return 0; fi
  done
  return 1
}

landing_field() { # $1=落地编号 $2=字段名（LANDING/PORTS/MODE_NOTE/DOMAIN/...）-> 打印值；编号 1 用平铺变量，>=2 用 _编号 后缀
  local _idx="$1" _field="$2"
  local _var="$_field"
  [ "$_idx" != "1" ] && _var="${_field}_$_idx"
  printf '%s' "${!_var:-}"
}

install_pkg() { # $1=包名 -> 装好返回 0；没包管理器或装不上返回 1（不报错，由调用方决定回退）
  local pkg="$1" _to=""
  command -v timeout >/dev/null 2>&1 && _to="timeout 180"
  if command -v apt-get >/dev/null 2>&1; then
    $_to apt-get install -y "$pkg" >/dev/null 2>&1 && return 0
    $_to apt-get update >/dev/null 2>&1 || true
    $_to apt-get install -y "$pkg" >/dev/null 2>&1 && return 0
  elif command -v dnf >/dev/null 2>&1; then
    $_to dnf install -y "$pkg" >/dev/null 2>&1 && return 0
  elif command -v yum >/dev/null 2>&1; then
    $_to yum install -y "$pkg" >/dev/null 2>&1 && return 0
  fi
  return 1
}

resolve_landing_ip() { # $1=落地地址（IP 或域名）-> 打印 IPv4；解析不到返回 1
  local a="$1" ip=""
  if is_ip "$a"; then printf '%s' "$a"; return 0; fi
  ip="$(getent hosts "$a" 2>/dev/null | awk '{print $1}' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)"
  [ -n "$ip" ] && printf '%s' "$ip"
}

landing_cert_domain() { # $1=落地编号 -> 打印该落地的证书域名（CERT_DOMAIN，没有就 fallback 到 DOMAIN）
  local d
  d="$(landing_field "$1" CERT_DOMAIN || true)"
  [ -n "$d" ] || d="$(landing_field "$1" DOMAIN || true)"
  printf '%s' "$d"
}

conf_get() { # $1=键名 -> 打印 ${REALM_DIR}/relay.conf 里该键的值（去引号），没有返回 1
  local v
  v="$(grep -E "^$1=" ${REALM_DIR}/relay.conf 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"' || true)"
  [ -n "$v" ] && printf '%s' "$v"
}

conf_set() { # $1=键名 $2=值 -> 写进 ${REALM_DIR}/relay.conf（有则替换，没有则追加）
  local k="$1" v="$2"
  [ -f ${REALM_DIR}/relay.conf ] || return 1
  if grep -qE "^$k=" ${REALM_DIR}/relay.conf; then
    sed -i "s|^$k=.*|$k=\"$v\"|" ${REALM_DIR}/relay.conf
  else
    echo "$k=\"$v\"" >> ${REALM_DIR}/relay.conf
  fi
}

remove_realm_listen() { # $1=端口 -> 从 ${REALM_DIR}/config.toml 里删掉监听该端口的 [[endpoints]] 段（改前先备份）
  local port="$1"
  [ -f ${REALM_DIR}/config.toml ] || return 0
  cp ${REALM_DIR}/config.toml ${REALM_DIR}/config.toml.bak 2>/dev/null || true
  awk -v RS="" -v ORS="\n\n" -v port="$port" '
    index($0, "listen = \"0.0.0.0:" port "\"") > 0 { next }
    { print }
  ' ${REALM_DIR}/config.toml > /tmp/relay-config.toml.new 2>/dev/null || return 0
  mv /tmp/relay-config.toml.new ${REALM_DIR}/config.toml
  # 去掉文件末尾多余的空行
  sed -i -e :a -e '/^\n*$/{$d;N;};/\n$/ba' ${REALM_DIR}/config.toml 2>/dev/null || true
  ensure_realm_has_endpoint
  return 0
}

ensure_realm_has_endpoint() { # realm 没有 [[endpoints]] 会直接 panic 退出，所以全部分流后要留一个无害的回环占位
  [ -f ${REALM_DIR}/config.toml ] || return 0
  if ! grep -q '^\[\[endpoints\]\]' ${REALM_DIR}/config.toml; then
    cat >> ${REALM_DIR}/config.toml <<'EOF'

# 占位：所有端口都走分流时 realm 一个转发也没有，它会直接退出；留一个只监听本机的
# 丢弃端口（127.0.0.1:9）让它能正常跑，不影响任何转发，也不出现在检查里。
[[endpoints]]
listen = "127.0.0.1:9"
remote = "127.0.0.1:9"
EOF
    echo "realm 已没有转发端口，留了一个本机占位端口（127.0.0.1:9）让它能正常跑。"
  fi
}

restore_realm_config() { # 分流启用失败时回滚：把 config.toml 从备份恢复
  [ -f ${REALM_DIR}/config.toml.bak ] && mv ${REALM_DIR}/config.toml.bak ${REALM_DIR}/config.toml
}

ensure_cert_domain_current() { # 本轮落地的证书域名：落地填域名就是它；填 IP 时问用户一次；问不到返回 1
  if [ -n "$CERT_DOMAIN" ]; then return 0; fi
  if [ -n "$DOMAIN" ]; then CERT_DOMAIN="$DOMAIN"; return 0; fi
  echo "请填写这个落地用的域名："
  local _d=""
  ask "落地是填 IP 的，分流要按域名认落地，请填写它的域名（证书上的域名，就是节点链接里的域名；不知道就直接回车）： " _d
  [ -n "$_d" ] || return 1
  CERT_DOMAIN="$_d"
  return 0
}

ensure_cert_domains_all() { # $1=端口（80 或 443）：给 relay.conf 里所有带该端口的落地补齐 CERT_DOMAIN；补不齐返回 1
  local _port="$1" _count _i _L _P _D
  _count="$(conf_get LANDING_COUNT || echo 1)"
  _i=1
  while [ "$_i" -le "$_count" ]; do
    if [ "$_i" = "1" ]; then
      _L="$(conf_get LANDING || echo "")"; _P="$(conf_get PORTS || echo "")"; _D="$(conf_get CERT_DOMAIN || conf_get DOMAIN || echo "")"
    else
      _L="$(conf_get "LANDING_$_i" || echo "")"; _P="$(conf_get "PORTS_$_i" || echo "")"; _D="$(conf_get "CERT_DOMAIN_$_i" || conf_get "DOMAIN_$_i" || echo "")"
    fi
    if [ -n "$_L" ] && [ -n "$_P" ]; then
      case " $_P " in
        *" tcp:$_port:$_port "*)
          if [ -z "$_D" ]; then
            echo "请填写落地 $_i（$_L）用的域名："
            local _d=""
            ask "落地 $_i 是填 IP 的，分流要按域名认落地，请填写它的域名（证书上的域名，就是节点链接里的域名；不知道就直接回车）： " _d
            [ -n "$_d" ] || return 1
            if [ "$_i" = "1" ]; then conf_set CERT_DOMAIN "$_d"; else conf_set "CERT_DOMAIN_$_i" "$_d"; fi
          fi
          ;;
      esac
    fi
    _i=$((_i + 1))
  done
  # 本轮正在加的落地（还没写进 relay.conf）：同样补齐
  case " $PORTS " in
    *" tcp:$_port:$_port "*)
      ensure_cert_domain_current || return 1
      ;;
  esac
  return 0
}

write_proxy80_config() { # 按 relay.conf 重新生成 nginx 80 分流配置（所有带 80 的落地按 Host 分流）
  local nginx_conf="/etc/nginx/conf.d/relay-80.conf"
  # 本轮正在加的落地还没写进 relay.conf，先存下来（子 shell 里 source relay.conf 会覆盖同名变量）
  local _cur_landing="$LANDING" _cur_ports="$PORTS" _cur_cert="$CERT_DOMAIN"
  echo "生成 80 分流配置：$nginx_conf"
  mkdir -p /etc/nginx/conf.d 2>/dev/null || true
  (
    set -a; . ${REALM_DIR}/relay.conf 2>/dev/null; set +a
    local _count="${LANDING_COUNT:-1}" _i=1 _L="" _P="" _D="" _map="" _first_up=""
    while [ "$_i" -le "$_count" ]; do
      if [ "$_i" = "1" ]; then
        _L="$LANDING"; _P="$PORTS"; _D="$(landing_cert_domain 1)"
      else
        _L="$(landing_field "$_i" LANDING)"; _P="$(landing_field "$_i" PORTS)"; _D="$(landing_cert_domain "$_i")"
      fi
      if [ -n "$_L" ] && [ -n "$_P" ] && [ -n "$_D" ]; then
        case " $_P " in
          *" tcp:80:80 "*)
            _map="$_map    $_D    $_L:80;\n"
            [ -z "$_first_up" ] && _first_up="$_L:80"
            ;;
        esac
      fi
      _i=$((_i + 1))
    done
    # 本轮正在加的落地（还没写进 relay.conf）：用上面存下来的值补一条
    if [ -n "$_cur_cert" ] && [ -n "$_cur_landing" ]; then
      case " $_cur_ports " in
        *" tcp:80:80 "*)
          case "$_map" in
            *"$_cur_cert"*) ;;
            *) _map="$_map    $_cur_cert    $_cur_landing:80;\n" ;;
          esac
          [ -z "$_first_up" ] && _first_up="$_cur_landing:80"
          ;;
      esac
    fi
    if [ -z "$_map" ]; then
      echo "没有带 80 端口的落地，80 分流配置没生成。"
      exit 1
    fi
    {
      echo "# relay-node 自动生成：80 按域名分流（ACME HTTP-01 续签用）"
      echo "# 各落地按 HTTP 的 Host 分到对应落地的 80；匹配不到的走第 1 个带 80 的落地。"
      echo "# 不要手改：下次加落地/重跑安装会按 relay.conf 重新生成覆盖。"
      echo "# 落地 IP 变了不用改这里：proxy_pass 用变量 + resolver，每次请求现解析域名。"
      echo ""
      echo "map \$host \$relay80_upstream {"
      printf '%b' "$_map"
      echo "    default    $_first_up;"
      echo "}"
      echo ""
      echo "server {"
      echo "    listen 80 default_server;"
      echo "    server_name _;"
      echo "    resolver 1.1.1.1 8.8.8.8 valid=60s;"
      echo "    location / {"
      echo "        proxy_pass http://\$relay80_upstream;"
      echo "        proxy_set_header Host \$host;"
      echo "        proxy_set_header X-Real-IP \$remote_addr;"
      echo "        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;"
      echo "    }"
      echo "}"
    } > "$nginx_conf"
  )
}

write_sni_config() { # 按 relay.conf 重新生成 haproxy 443 SNI 分流配置（TLS 透传，不解密）
  local haproxy_cfg="/etc/haproxy/haproxy.cfg"
  # 本轮正在加的落地还没写进 relay.conf，先存下来（子 shell 里 source relay.conf 会覆盖同名变量）
  local _cur_landing="$LANDING" _cur_ports="$PORTS" _cur_cert="$CERT_DOMAIN"
  local _new_idx=$((LANDING_COUNT + 1))
  echo "生成 443 SNI 分流配置：$haproxy_cfg"
  mkdir -p /etc/haproxy 2>/dev/null || true
  (
    set -a; . ${REALM_DIR}/relay.conf 2>/dev/null; set +a
    local _count="${LANDING_COUNT:-1}" _i=1 _L="" _P="" _D="" _use="" _backends="" _first=""
    while [ "$_i" -le "$_count" ]; do
      if [ "$_i" = "1" ]; then
        _L="$LANDING"; _P="$PORTS"; _D="$(landing_cert_domain 1)"
      else
        _L="$(landing_field "$_i" LANDING)"; _P="$(landing_field "$_i" PORTS)"; _D="$(landing_cert_domain "$_i")"
      fi
      if [ -n "$_L" ] && [ -n "$_P" ] && [ -n "$_D" ]; then
        case " $_P " in
          *" tcp:443:443 "*)
            _use="$_use    use_backend be_L$_i if { req.ssl_sni -i $_D }\n"
            if is_ip "$_L"; then
              _backends="$_backends\nbackend be_L$_i\n    mode tcp\n    server L$_i $_L:443 check\n"
            else
              _backends="$_backends\nbackend be_L$_i\n    mode tcp\n    server L$_i $_L:443 resolvers relaydns resolve-prefer ipv4 check\n"
            fi
            [ -z "$_first" ] && _first="be_L$_i"
            ;;
        esac
      fi
      _i=$((_i + 1))
    done
    # 本轮正在加的落地（还没写进 relay.conf）：用上面存下来的值补一条
    if [ -n "$_cur_cert" ] && [ -n "$_cur_landing" ]; then
      case " $_cur_ports " in
        *" tcp:443:443 "*)
          case "$_use" in
            *"$_cur_cert"*) ;;
            *)
              _use="$_use    use_backend be_L$_new_idx if { req.ssl_sni -i $_cur_cert }\n"
              if is_ip "$_cur_landing"; then
                _backends="$_backends\nbackend be_L$_new_idx\n    mode tcp\n    server L$_new_idx $_cur_landing:443 check\n"
              else
                _backends="$_backends\nbackend be_L$_new_idx\n    mode tcp\n    server L$_new_idx $_cur_landing:443 resolvers relaydns resolve-prefer ipv4 check\n"
              fi
              ;;
          esac
          [ -z "$_first" ] && _first="be_L$_new_idx"
          ;;
      esac
    fi
    if [ -z "$_use" ]; then
      echo "没有带 443 端口的落地，443 SNI 分流配置没生成。"
      exit 1
    fi
    {
      echo "# relay-node 自动生成：443 按 SNI 分流（TLS 透传，不解密）"
      echo "# 各落地按 TLS 握手里的域名（SNI）分到对应落地的 443；匹配不到/没带 SNI 的走第 1 个带 443 的落地。"
      echo "# 不要手改：下次加落地/重跑安装会按 relay.conf 重新生成覆盖。"
      echo "# TLS-ALPN-01 能穿过：分流只读 ClientHello 里的 SNI，不改任何字节，落地 Caddy 自己完成验证。"
      echo "global"
      echo "    log /dev/log local0"
      echo "    maxconn 1024"
      echo "    # maxconn 只给 1024：个人中转同时连上千个连接用不上；1024 需要的句柄数能塞进"
      echo "    # 4096 的系统上限（极简系统/容器常见），4096 反而会让 haproxy 因提不上句柄上限而拒绝启动"
      echo ""
      echo "defaults"
      echo "    log global"
      echo "    mode tcp"
      echo "    option tcplog"
      echo "    timeout connect 5s"
      echo "    timeout client 60s"
      echo "    timeout server 60s"
      echo ""
      echo "resolvers relaydns"
      echo "    nameserver dns1 1.1.1.1:53"
      echo "    nameserver dns2 8.8.8.8:53"
      echo "    resolve_retries 3"
      echo "    timeout retry 1s"
      echo "    hold valid 60s"
      echo ""
      echo "frontend relay443"
      echo "    bind *:443"
      echo "    mode tcp"
      echo "    option tcplog"
      echo "    tcp-request inspect-delay 5s"
      echo "    tcp-request content accept if { req.ssl_hello_type 1 }"
      echo "    tcp-request content accept"
      printf '%b' "$_use"
      echo "    default_backend $_first"
      printf '%b\n' "$_backends"
    } > "$haproxy_cfg"
  )
}

start_proxy80() { # 启动或重载 nginx（80 分流）；DRY_RUN 只写配置不启动
  if [ "$DRY_RUN" = "1" ]; then echo "[dry-run] 跳过 nginx 启动/重载"; return 0; fi
  if ! nginx -t 2>&1; then
    echo "nginx 配置校验没过（上面是报错），80 分流没启用。"
    return 1
  fi
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    if systemctl is-active --quiet nginx 2>/dev/null; then
      systemctl reload nginx && echo "nginx 已重载（80 按域名分流）"
    else
      systemctl enable --now nginx && echo "nginx 已启动（80 按域名分流）"
    fi
  else
    if pgrep -x nginx >/dev/null 2>&1; then
      nginx -s reload 2>/dev/null && echo "nginx 已重载（80 按域名分流）"
    else
      nginx 2>/dev/null && echo "nginx 已启动（80 按域名分流，无 systemd 环境）"
    fi
  fi
}

start_proxy443() { # 启动或重载 haproxy（443 SNI 分流）；DRY_RUN 只写配置不启动
  if [ "$DRY_RUN" = "1" ]; then echo "[dry-run] 跳过 haproxy 启动/重载"; return 0; fi
  if ! haproxy -c -f /etc/haproxy/haproxy.cfg 2>&1; then
    echo "haproxy 配置校验没过（上面是报错），443 分流没启用。"
    return 1
  fi
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    if systemctl is-active --quiet haproxy 2>/dev/null; then
      systemctl reload haproxy && echo "haproxy 已重载（443 按 SNI 分流）"
    else
      systemctl enable --now haproxy && echo "haproxy 已启动（443 按 SNI 分流）"
    fi
  else
    if pgrep -x haproxy >/dev/null 2>&1; then
      pkill -x haproxy 2>/dev/null || true; sleep 1
    fi
    haproxy -D -f /etc/haproxy/haproxy.cfg 2>/dev/null && echo "haproxy 已启动（443 按 SNI 分流，无 systemd 环境）"
  fi
}

restart_realm_now() { # 重启 realm（分流启用时用：让它释放 80/443，代理才能监听）；DRY_RUN 跳过
  if [ "$DRY_RUN" = "1" ]; then return 0; fi
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl restart realm 2>/dev/null || return 1
    sleep 1
    systemctl is-active --quiet realm 2>/dev/null || return 1
  elif [ -f /var/run/realm.pid ] && kill -0 "$(cat /var/run/realm.pid)" 2>/dev/null; then
    kill "$(cat /var/run/realm.pid)" 2>/dev/null || true
    sleep 1
    nohup /usr/local/bin/realm -c ${REALM_DIR}/config.toml >/var/log/realm.log 2>&1 &
    echo $! > /var/run/realm.pid
    sleep 1
    kill -0 "$(cat /var/run/realm.pid)" 2>/dev/null || return 1
  fi
  return 0
}

enable_share80() { # 开 80 按域名分流：补域名 -> 装 nginx -> 写配置 -> 校验 -> 挪走 realm 的 80 -> 启动；失败回滚并返回 1
  echo "开 80 按域名分流：中转机上跑 nginx 监听 80，按域名把请求分到各落地的 80。"
  ensure_cert_domains_all 80 || { echo "没拿到分流要用的域名，80 分流开不了。"; return 1; }
  if [ "$DRY_RUN" != "1" ] && ! command -v nginx >/dev/null 2>&1; then
    echo "装 nginx（80 分流要用）..."
    install_pkg nginx || { echo "nginx 装不上：包管理器源可能有问题，80 分流开不了。"; return 1; }
  fi
  if [ "$DRY_RUN" = "1" ] && ! command -v nginx >/dev/null 2>&1; then
    echo "[dry-run] 本机没装 nginx，跳过安装与校验，只生成配置。"
  fi
  # 关掉 Debian/Ubuntu 自带的默认站点，避免和我们的 80 抢 default_server（正式安装才动，dry-run 不动）
  if [ "$DRY_RUN" != "1" ] && [ -L /etc/nginx/sites-enabled/default ]; then
    rm -f /etc/nginx/sites-enabled/default
    echo "已停用 nginx 自带默认站点（它也监听 80，会和分流冲突）。"
  elif [ "$DRY_RUN" = "1" ] && [ -L /etc/nginx/sites-enabled/default ]; then
    echo "[dry-run] 正式安装时会停用 nginx 自带默认站点（它也监听 80）。"
  fi
  write_proxy80_config || return 1
  if [ "$DRY_RUN" != "1" ] && ! nginx -t >/dev/null 2>&1; then
    echo "nginx 配置校验没过，80 分流没启用。"
    return 1
  fi
  remove_realm_listen 80
  # realm 还在用旧配置占着 80，先重启让它释放端口，nginx 才能监听
  if ! restart_realm_now; then
    restore_realm_config
    restart_realm_now || true
    echo "realm 重启失败，已把配置恢复原样，80 分流没启用。"
    return 1
  fi
  if ! start_proxy80; then
    restore_realm_config
    restart_realm_now || true
    echo "nginx 启动失败，已把 realm 配置恢复原样，80 分流没启用。"
    return 1
  fi
  SHARE80=1
  conf_set SHARE80 1
  echo "80 按域名分流已启用：几个落地的证书续签都自动通，不用手动改指向了。"
  return 0
}

enable_share443() { # 开 443 按 SNI 分流：补域名 -> 装 haproxy -> 写配置 -> 校验 -> 挪走 realm 的 443 -> 启动；失败回滚并返回 1
  echo "开 443 按 SNI 分流：中转机上跑 haproxy 监听 443，按 TLS 握手里的域名分到各落地的 443。"
  ensure_cert_domains_all 443 || { echo "没拿到分流要用的域名，443 分流开不了。"; return 1; }
  if [ "$DRY_RUN" != "1" ] && ! command -v haproxy >/dev/null 2>&1; then
    echo "装 haproxy（443 SNI 分流要用）..."
    install_pkg haproxy || { echo "haproxy 装不上：包管理器源可能有问题，443 分流开不了。"; return 1; }
  fi
  if [ "$DRY_RUN" = "1" ] && ! command -v haproxy >/dev/null 2>&1; then
    echo "[dry-run] 本机没装 haproxy，跳过安装与校验，只生成配置。"
  fi
  write_sni_config || return 1
  if [ "$DRY_RUN" != "1" ] && ! haproxy -c -f /etc/haproxy/haproxy.cfg >/dev/null 2>&1; then
    echo "haproxy 配置校验没过，443 分流没启用。"
    return 1
  fi
  remove_realm_listen 443
  # realm 还在用旧配置占着 443，先重启让它释放端口，haproxy 才能监听
  if ! restart_realm_now; then
    restore_realm_config
    restart_realm_now || true
    echo "realm 重启失败，已把配置恢复原样，443 分流没启用。"
    return 1
  fi
  if ! start_proxy443; then
    restore_realm_config
    restart_realm_now || true
    echo "haproxy 启动失败，已把 realm 配置恢复原样，443 分流没启用。"
    return 1
  fi
  SHARE443=1
  conf_set SHARE443 1
  echo "443 按 SNI 分流已启用：几个落地共用中转 443，客户端链接不用带高位端口，续签走 443 自动通。"
  return 0
}

ensure_proxies_running() { # realm 启动后，顺带确认分流组件也在跑（重启后它们也要跟上）
  if [ "$SHARE80" = "1" ]; then
    if [ "$DRY_RUN" != "1" ]; then
      start_proxy80 || echo "注意：nginx 没起来，80 分流暂时不通；先查 nginx -t，再 systemctl status nginx。"
    fi
  fi
  if [ "$SHARE443" = "1" ]; then
    if [ "$DRY_RUN" != "1" ]; then
      start_proxy443 || echo "注意：haproxy 没起来，443 分流暂时不通；先查 haproxy -c -f /etc/haproxy/haproxy.cfg，再 systemctl status haproxy。"
    fi
  fi
  return 0
}

case "${1:-}" in
  --regen-80) write_proxy80_config && start_proxy80 ;;
  --regen-443) write_sni_config && start_proxy443 ;;
  --restart-proxies) ensure_proxies_running ;;
  --enable-share80) enable_share80 ;;
  --enable-share443) enable_share443 ;;
  *) echo "用法：bash proxy.sh {--regen-80|--regen-443|--restart-proxies|--enable-share80|--enable-share443}"; exit 1 ;;
esac
