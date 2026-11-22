#!/usr/bin/env bash
# 中转节点一键安装：realm 纯转发（客户端 -> 中转机 -> 落地机）
# 用法：
#   本地：bash install.sh [落地IP或域名] [预设 1/2/3]
#   远程一键：curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/install.sh | bash
#   只补生成双节点链接：bash install.sh --nodes（读 /etc/realm/relay.conf，不重装）
#   追加一个落地节点：bash install.sh --add（不覆盖已有配置，直接往后加）
#   只生成一条链接的直连 twin：bash install.sh --twin '<链接>' <落地IP> [sni域名]（面板调用）
#   只生成一条任意门链接：bash install.sh --door '<链接>' <中转地址> <转发端口> [sni域名]（面板调用）
#   （不给参数则交互询问；落地不用装任何东西，转发目标就是落地现成的端口）
# 任意门模式：节点域名永远指着落地不动；中转机上每个落地分一个专用转发端口
#   （转发端口 = 落地端口 + 20000，如 443→20443、8388→28388）；链接用“中转地址:转发端口”，
#   SNI 仍是节点域名，TLS 还是客户端和落地端到端直通；证书续签走落地直连，永远不用改指向。
#   环境变量：RELAY_MODE=standard|door（1/2 也接受）、RELAY_ADDR（中转地址）、
#   DOOR_PORT_OVERRIDES="落地端口:转发端口"（逗号分隔，如 "443:30443"）。
# 多落地：一台中转机可以转多个落地节点（装完第 1 个后会问要不要再加一个，
#        或事后跑 bash install.sh --add）。每个落地各有一组转发端口，监听端口
#        不能撞。加第 2 个及以上落地、80 或 443 被已装好的落地占了时，会让你
#        选解决方式（默认 1，直接回车就是 1）：
#        [1] 改监听走高位端口（现状，最简单；续签要手动改指向）
#        [2] 80 按域名分流：中转机上跑 nginx 监听 80，按 Host 把验证请求分到
#            对应落地的 80，几个落地续签都自动通；443 还是各走各的高位端口
#        [3] 443 按 SNI 分流：中转机上跑 haproxy 监听 443，按 TLS 握手里的域
#            名（SNI）分到对应落地的 443，几个落地共用 443，客户端链接不用带
#            高位端口，续签走 TLS-ALPN-01 自动通；2 和 3 可以一起选
#        单节点（第 1 个落地）默认走现在最简单的（HTTP-01 + realm 纯转发）。
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

# 面板（systemd 服务）调脚本时环境里可能没有 HOME；没它后面 $HOME 会触发 set -u 报错，先兜底
HOME="${HOME:-/root}"

# 工作目录：本地跑 bash install.sh 时用脚本所在目录；
# 远程一键（curl | bash）时用 ~/relay-node-kit
if [ -f "$0" ]; then
  cd "$(dirname "$0")"
else
  mkdir -p "$HOME/relay-node-kit"
  cd "$HOME/relay-node-kit"
fi

# 只跑第 7 步：bash install.sh --nodes（下面解析到 --nodes 后跳过 1-6 步）
NODES_ONLY=0
if [ "${1:-}" = "--nodes" ]; then
  NODES_ONLY=1
  shift
fi
# 追加模式：bash install.sh --add（不覆盖已有配置，直接往后加一个落地）
ADD_MODE=0
if [ "${1:-}" = "--add" ]; then
  ADD_MODE=1
  shift
fi
# 只生成直连 twin：bash install.sh --twin '<链接>' <落地IP> [sni域名]（面板调用，标准输出只打印 twin 一行）
TWIN_MODE=0
if [ "${1:-}" = "--twin" ]; then
  TWIN_MODE=1
  shift
fi
# 只生成一条任意门链接：bash install.sh --door '<链接>' <中转地址> <转发端口> [sni域名]（面板调用，标准输出只打印任意门链接一行）
DOOR_MODE=0
if [ "${1:-}" = "--door" ]; then
  DOOR_MODE=1
  shift
fi
SNI_ASKED=0
STEP7_INDEX=1
SHARE80=0        # 80 是否走按域名分流（nginx 监听 80，按 Host 分到各落地 80）
SHARE443=0       # 443 是否走按 SNI 分流（haproxy 监听 443，按 SNI 分到各落地 443）
SHARE_ASKED=0    # 本轮加落地时是否已问过分流方式（每加一个落地重置一次）
CERT_DOMAIN="${CERT_DOMAIN:-}"   # 本轮落地的证书域名（分流按它认落地；落地填域名时就是它，填 IP 时再问；面板非交互调用时可用环境变量预设）
RELAY_MODE="${RELAY_MODE:-}"     # 转发模式：standard（标准）/door（任意门）；1/2 也接受；没给就交互问
RELAY_ADDR="${RELAY_ADDR:-}"     # 任意门模式用的中转地址（IP 或域名）；没给就自动探测本机公网 IP
DOOR_PORT_OVERRIDES="${DOOR_PORT_OVERRIDES:-}"  # 任意门转发端口覆盖："落地端口:转发端口"，逗号分隔，如 "443:30443"

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
    # 注意：/dev/tty 节点存在但没有控制终端时，直接重定向会报 No such device；
    # 包一层 group 再重定向 stderr 才能真正静默
    { read -rp "$prompt" ans </dev/tty || true; } 2>/dev/null
  fi
  [ -z "$ans" ] && ans="$default"
  printf -v "$varname" '%s' "$ans"
}

# ---------- 端口占用预检（ss -> netstat -> /proc 回退，与 landing-node 包一致） ----------
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

port_in_config() { # $1=端口 -> /etc/realm/config.toml 里已有该监听端口返回 0
  [ -f /etc/realm/config.toml ] && grep -qE "listen = \"0\.0\.0\.0:$1\"" /etc/realm/config.toml
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

# ---------- 任意门模式辅助函数 ----------
normalize_relay_mode() { # $1=模式值（1/2/standard/door，大小写不敏感）-> 打印 standard 或 door；认不出返回 1
  local m
  m="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  case "$m" in
    1|standard|std) printf '%s' "standard" ;;
    2|door) printf '%s' "door" ;;
    *) return 1 ;;
  esac
}

detect_relay_ip() { # 打印本机公网 IP；拿不到返回 1
  local ip=""
  ip="$(curl -s --max-time 5 https://api.ipify.org 2>/dev/null || true)"
  [ -n "$ip" ] || ip="$(curl -s --max-time 5 https://ifconfig.me 2>/dev/null || true)"
  [ -n "$ip" ] && printf '%s' "$ip"
}

door_listen_port() { # $1=落地端口 -> 打印转发端口（+20000；超了依次试 +10000、+1000；都不行返回 1）
  local p="$1" s _off
  for _off in 20000 10000 1000; do
    s=$((p + _off))
    if [ "$s" -ge 1 ] && [ "$s" -le 65535 ]; then printf '%s' "$s"; return 0; fi
  done
  return 1
}

door_map_ports() { # $1=PORTS -> 打印映射后的 PORTS（监听端口按落地端口 +20000，落地端口不变）
  local ports="$1" _spec _proto _rest _lp _rp _nlp _out=""
  for _spec in $ports; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
    _nlp="$(door_listen_port "$_rp" || true)"
    [ -n "$_nlp" ] || { echo "转发端口算不出来：落地端口 $_rp 太大，加 20000 超出范围。" >&2; return 1; }
    _out="$_out $_proto:$_nlp:$_rp"
  done
  printf '%s' "${_out# }"
}

apply_door_overrides() { # $1=PORTS $2=覆盖串（"落地端口:转发端口"，逗号分隔）-> 打印应用后的 PORTS；格式不对返回 1
  local ports="$1" ov="$2" _o _rp _nlp _spec _proto _rest _lp _rp2 _out="" _seen="" _found=0
  local -a _ov=()
  IFS=',' read -ra _ov <<< "$ov"
  for _o in "${_ov[@]}"; do
    _o="${_o//[[:space:]]/}"
    [ -n "$_o" ] || continue
    [[ "$_o" == *:* ]] || { echo "转发端口覆盖格式不对：$_o（应为 落地端口:转发端口，如 443:30443）。" >&2; return 1; }
    _rp="${_o%%:*}"; _nlp="${_o##*:}"
    valid_port "$_rp" && valid_port "$_nlp" || { echo "转发端口覆盖不合法：$_o（端口须在 1-65535 之间）。" >&2; return 1; }
    _found=0
    for _spec in $ports; do
      _rest="${_spec#*:}"; _rp2="${_rest##*:}"
      [ "$_rp2" = "$_rp" ] && _found=1
    done
    [ "$_found" = "1" ] || { echo "转发端口覆盖里的落地端口 $_rp 不在本次转发表里。" >&2; return 1; }
    _out=""
    for _spec in $ports; do
      _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp2="${_rest##*:}"
      [ "$_rp2" = "$_rp" ] && _lp="$_nlp"
      _out="$_out $_proto:$_lp:$_rp2"
    done
    ports="${_out# }"
  done
  # 覆盖后监听端口不能撞（同一协议内）
  _seen=""
  for _spec in $ports; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"
    case " $_seen " in *" $_proto:$_lp "*) echo "转发端口覆盖后监听端口撞了：$_proto:$_lp 出现两次。" >&2; return 1 ;; esac
    _seen="$_seen $_proto:$_lp"
  done
  printf '%s' "$ports"
}

ensure_relay_addr() { # 任意门模式：RELAY_ADDR 为空时自动探测公网 IP（失败就问用户）
  if [ -n "$RELAY_ADDR" ]; then
    echo "中转地址：$RELAY_ADDR"
    return 0
  fi
  local _detected=""
  _detected="$(detect_relay_ip || true)"
  if [ -n "$_detected" ]; then
    echo "没填中转地址，自动探测到本机公网 IP：$_detected"
    echo "请确认中转地址（任意门链接里用的地址，填域名或 IP）："
    ask "中转地址 [直接回车就是 $_detected]： " RELAY_ADDR "$_detected"
  else
    echo "自动探测本机公网 IP 失败（可能出站网络不通）。"
    echo "请填写中转地址（任意门链接里用的地址，填域名或 IP，例如 relay.example.com）："
    ask "中转地址： " RELAY_ADDR
  fi
  [ -n "$RELAY_ADDR" ] || { echo "没给中转地址，任意门模式装不了。"; exit 1; }
  echo "中转地址：$RELAY_ADDR"
}

confirm_door_ports() { # 任意门模式：打印转发端口映射表，让用户确认或覆盖（直接回车就是默认映射）
  echo ""
  echo "转发端口映射（任意门模式：转发端口 = 落地端口 + 20000，落地端口不变）："
  local _spec _proto _rest _lp _rp
  for _spec in $PORTS; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
    echo "  $_proto  落地端口 $_rp  ->  中转转发端口 $_lp"
  done
  # 面板非交互调用：DOOR_PORT_OVERRIDES 环境变量预设覆盖，格式不对直接退出（不交互问）
  if [ -n "${DOOR_PORT_OVERRIDES:-}" ]; then
    local _new=""
    _new="$(apply_door_overrides "$PORTS" "$DOOR_PORT_OVERRIDES")" || exit 1
    PORTS="$_new"
    echo "转发端口已按预设覆盖：$PORTS"
    return 0
  fi
  echo "请确认转发端口（直接回车不改；要改按“落地端口:转发端口”填，多个逗号分隔，例如 443:30443）："
  local _ov="" _new=""
  while true; do
    _ov=""
    ask "转发端口覆盖 [直接回车就是上面这张表]： " _ov ""
    [ -z "$_ov" ] && return 0
    if _new="$(apply_door_overrides "$PORTS" "$_ov")"; then
      PORTS="$_new"
      echo "转发端口已按覆盖改好：$PORTS"
      return 0
    fi
    echo "上面那条覆盖没用上，重新填，或直接回车用默认映射。"
  done
}

# ---------- 分流（80 按域名分流 / 443 按 SNI 分流）辅助函数 ----------
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

conf_get() { # $1=键名 -> 打印 /etc/realm/relay.conf 里该键的值（去引号），没有返回 1
  local v
  v="$(grep -E "^$1=" /etc/realm/relay.conf 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"' || true)"
  [ -n "$v" ] && printf '%s' "$v"
}

conf_set() { # $1=键名 $2=值 -> 写进 /etc/realm/relay.conf（有则替换，没有则追加）
  local k="$1" v="$2"
  [ -f /etc/realm/relay.conf ] || return 1
  if grep -qE "^$k=" /etc/realm/relay.conf; then
    sed -i "s|^$k=.*|$k=\"$v\"|" /etc/realm/relay.conf
  else
    echo "$k=\"$v\"" >> /etc/realm/relay.conf
  fi
}

remove_realm_listen() { # $1=端口 -> 从 /etc/realm/config.toml 里删掉监听该端口的 [[endpoints]] 段（改前先备份）
  local port="$1"
  [ -f /etc/realm/config.toml ] || return 0
  cp /etc/realm/config.toml /etc/realm/config.toml.bak 2>/dev/null || true
  awk -v RS="" -v ORS="\n\n" -v port="$port" '
    index($0, "listen = \"0.0.0.0:" port "\"") > 0 { next }
    { print }
  ' /etc/realm/config.toml > /tmp/relay-config.toml.new 2>/dev/null || return 0
  mv /tmp/relay-config.toml.new /etc/realm/config.toml
  # 去掉文件末尾多余的空行
  sed -i -e :a -e '/^\n*$/{$d;N;};/\n$/ba' /etc/realm/config.toml 2>/dev/null || true
  ensure_realm_has_endpoint
  return 0
}

ensure_realm_has_endpoint() { # realm 没有 [[endpoints]] 会直接 panic 退出，所以全部分流后要留一个无害的回环占位
  [ -f /etc/realm/config.toml ] || return 0
  if ! grep -q '^\[\[endpoints\]\]' /etc/realm/config.toml; then
    cat >> /etc/realm/config.toml <<'EOF'

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
  [ -f /etc/realm/config.toml.bak ] && mv /etc/realm/config.toml.bak /etc/realm/config.toml
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
    set -a; . /etc/realm/relay.conf 2>/dev/null; set +a
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
    set -a; . /etc/realm/relay.conf 2>/dev/null; set +a
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
    nohup /usr/local/bin/realm -c /etc/realm/config.toml >/var/log/realm.log 2>&1 &
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

choose_share_mode() { # $1=80 是否被已装落地占（0/1） $2=443 是否被已装落地占（0/1）：打印带详细解释的选项并执行选择
  local _c80="$1" _c443="$2"
  echo ""
  echo "端口冲突了，要选一种办法解决（只影响新加的这个落地，已经装好的落地不动）："
  echo "  1) 改监听走高位端口  ← 默认，直接回车就是 1"
  echo "     最简单，什么都不用准备，装完就能用。第二个落地的 443 会改成 10443 这类高位端口，"
  echo "     客户端链接要带上高位端口；证书续签要手动：到期前把域名 A 记录改回指向落地、续完再改回来。"
  if [ "$_c80" = "1" ]; then
    echo "  2) 80 按域名分流"
    echo "     中转机上跑一个轻量代理监听 80，按域名把验证请求分到对应落地的 80，几个落地的证书续签都自动通，"
    echo "     不用令牌、落地不用改。代价是中转 80 不再是纯转发、多一个代理组件；443 还是各走各的高位端口。"
  fi
  if [ "$_c443" = "1" ]; then
    echo "  3) 443 按 SNI 分流"
    echo "     中转机按 TLS 握手里的域名分流，几个落地共用中转的 443，客户端链接不用带高位端口，"
    echo "     证书续签走 443 自动通。效果最彻底，但改动最大：中转 443 不再是 realm 纯转发，要多装一个分流组件。"
  fi
  if [ "$_c80" = "1" ] && [ "$_c443" = "1" ]; then
    echo "  4) 80 分流 + 443 分流一起用"
    echo "     2 和 3 一起：80 和 443 都能共用，几个落地续签都自动通，客户端链接也不用带高位端口。"
    echo "     代价是中转机上要多两个分流组件，80 和 443 都不再是 realm 纯转发。"
  fi
  local _opts="1"
  [ "$_c80" = "1" ] && _opts="$_opts/2"
  [ "$_c443" = "1" ] && _opts="$_opts/3"
  [ "$_c80" = "1" ] && [ "$_c443" = "1" ] && _opts="$_opts/4"
  echo "请输入你的选择："
  # 面板非交互调用：环境变量 SHARE_MODE 预设选择（1/2/3/4），不预设就交互问
  local _choice="${SHARE_MODE:-}"
  if [ -z "$_choice" ]; then
    ask "选哪种方式？[$_opts]，直接回车选默认 1： " _choice "1"
  fi
  case "$_choice" in
    1)
      return 0
      ;;
    2)
      [ "$_c80" = "1" ] || { echo "选不了 2：这次 80 没冲突。"; return 1; }
      enable_share80 || return 1
      return 0
      ;;
    3)
      [ "$_c443" = "1" ] || { echo "选不了 3：这次 443 没冲突。"; return 1; }
      enable_share443 || return 1
      return 0
      ;;
    4)
      { [ "$_c80" = "1" ] && [ "$_c443" = "1" ]; } || { echo "选不了 4：这次 80 和 443 不是都冲突。"; return 1; }
      enable_share80 && enable_share443 || return 1
      return 0
      ;;
    *)
      echo "只能选 $_opts，默认按 1（改监听走高位端口）处理。"
      return 0
      ;;
  esac
}

link_port() { # $1=节点链接 -> 打印端口号（vless/trojan/ss SIP002/vmess），取不到返回 1
  local link="$1"
  local scheme="${link%%://*}"
  scheme="$(printf '%s' "$scheme" | tr '[:upper:]' '[:lower:]')"
  local rest="${link#*://}"
  local core="${rest%%#*}"
  core="${core%%\?*}" # 先去掉 query，不然 host:port 后面会粘着一串参数
  case "$scheme" in
    vless|trojan|hysteria2|hy2|tuic)
      local auth="${core%%/*}"
      local hostport="${auth##*@}"
      if [[ "$hostport" == *:* ]]; then
        local p="${hostport##*:}"
        if [[ "$p" =~ ^[0-9]+$ ]]; then printf '%s' "$p"; return 0; fi
      fi
      ;;
    ss)
      if [[ "$core" == *"@"* ]]; then
        local after="${core##*@}"
        local hostport="${after%%/*}"
        if [[ "$hostport" == *:* ]]; then
          local p="${hostport##*:}"
          if [[ "$p" =~ ^[0-9]+$ ]]; then printf '%s' "$p"; return 0; fi
        fi
      fi
      ;;
    vmess)
      if command -v python3 >/dev/null 2>&1; then
        python3 - "$core" <<'PYEOF' 2>/dev/null || true
import sys, json, base64
s = sys.argv[1]
s += '=' * (-len(s) % 4)
try:
    j = json.loads(base64.urlsafe_b64decode(s))
except Exception:
    sys.exit(0)
print(j.get('port', ''))
PYEOF
      fi
      return 0
      ;;
  esac
  return 1
}

set_link_port() { # $1=节点链接 $2=新端口 -> 打印改了端口的链接；改不了就原样打印
  local link="$1" new_port="$2"
  local scheme="${link%%://*}"
  scheme="$(printf '%s' "$scheme" | tr '[:upper:]' '[:lower:]')"
  local rest="${link#*://}"
  local frag="" base="$rest" has_frag=0
  if [[ "$base" == *"#"* ]]; then frag="${base##*#}"; base="${base%#*}"; has_frag=1; fi
  local query="" has_query=0
  if [[ "$base" == *"?"* ]]; then query="${base#*\?}"; base="${base%%\?*}"; has_query=1; fi
  case "$scheme" in
    vless|trojan|hysteria2|hy2|tuic)
      local auth="$base" path=""
      if [[ "$base" == */* ]]; then auth="${base%%/*}"; path="/${base#*/}"; fi
      local userinfo="" hostport="$auth"
      if [[ "$auth" == *"@"* ]]; then userinfo="${auth%@*}"; hostport="${auth##*@}"; fi
      local host="$hostport"
      if [[ "$hostport" == *:* ]]; then
        local p="${hostport##*:}"
        if [[ "$p" =~ ^[0-9]+$ ]]; then host="${hostport%:*}"; fi
      fi
      local out="$scheme://"
      [ -n "$userinfo" ] && out+="$userinfo@"
      out+="$host:$new_port"
      out+="$path"
      [ "$has_query" = "1" ] && out+="?$query"
      out+="#$frag"
      printf '%s' "$out"
      ;;
    ss)
      if [[ "$base" == *"@"* ]]; then
        local userinfo="${base%@*}" after="${base##*@}"
        local hostport="${after%%/*}" tail=""
        if [ "$after" != "$hostport" ]; then tail="/${after#*/}"; fi
        local host="$hostport"
        if [[ "$hostport" == *:* ]]; then
          local p="${hostport##*:}"
          if [[ "$p" =~ ^[0-9]+$ ]]; then host="${hostport%:*}"; fi
        fi
        local out="ss://$userinfo@$host:$new_port$tail"
        [ "$has_query" = "1" ] && out+="?$query"
        out+="#$frag"
        printf '%s' "$out"
      else
        local new_core=""
        if command -v python3 >/dev/null 2>&1; then
          new_core="$(python3 - "$base" "$new_port" <<'PYEOF' 2>/dev/null || true
import sys, base64
s, np = sys.argv[1], sys.argv[2]
s += '=' * (-len(s) % 4)
try:
    raw = base64.urlsafe_b64decode(s).decode()
except Exception:
    sys.exit(1)
if '@' not in raw:
    sys.exit(1)
ui, hp = raw.rsplit('@', 1)
if ':' in hp:
    host, port = hp.rsplit(':', 1)
    new = ui + '@' + host + ':' + np
else:
    new = ui + '@' + hp + ':' + np
print(base64.b64encode(new.encode()).decode())
PYEOF
)"
        fi
        if [ -n "$new_core" ]; then
          local out="ss://$new_core"
          [ "$has_query" = "1" ] && out+="?$query"
          out+="#$frag"
          printf '%s' "$out"
        else
          printf '%s' "$link"
        fi
      fi
      ;;
    vmess)
      local new_b64=""
      if command -v python3 >/dev/null 2>&1; then
        new_b64="$(python3 - "$base" "$new_port" <<'PYEOF' 2>/dev/null || true
import sys, json, base64
b64, np = sys.argv[1], sys.argv[2]
b64 += '=' * (-len(b64) % 4)
try:
    j = json.loads(base64.urlsafe_b64decode(b64))
except Exception:
    sys.exit(1)
j['port'] = str(np)
print('vmess://' + base64.b64encode(json.dumps(j, ensure_ascii=False, separators=(',', ':')).encode()).decode())
PYEOF
)"
      fi
      if [ -n "$new_b64" ]; then
        local out="$new_b64"
        [ "$has_query" = "1" ] && out+="?$query"
        out+="#$frag"
        printf '%s' "$out"
      else
        printf '%s' "$link"
      fi
      ;;
    *)
      printf '%s' "$link"
      ;;
  esac
}

# ---------- 第 7 步：双节点链接（粘贴原链接 -> 中转原链接 + 直连 twin） ----------
# sni 域名只从原链接里取（链接地址是域名 -> query 的 sni= -> query 的 host=），
# 实在取不到才问用户一次；LANDING 是 IP 时 DOMAIN 为空，不能依赖它。
query_param() { # $1=query $2=name -> 打印值，找不到返回 1
  local q="$1" name="$2" kv
  local -a parts=()
  IFS='&' read -ra parts <<< "$q" || true
  for kv in "${parts[@]}"; do
    case "$kv" in
      "$name="*) printf '%s' "${kv#"$name="}"; return 0 ;;
    esac
  done
  return 1
}

query_set_param() { # $1=query $2=name $3=value -> 新 query（有则替换，没有则追加）
  local q="$1" name="$2" value="$3" kv found=0 out=""
  local -a parts=()
  IFS='&' read -ra parts <<< "$q" || true
  for kv in "${parts[@]}"; do
    case "$kv" in
      "$name="*) kv="$name=$value"; found=1 ;;
    esac
    if [ -z "$out" ]; then out="$kv"; else out="$out&$kv"; fi
  done
  if [ "$found" = "0" ]; then
    if [ -z "$out" ]; then out="$name=$value"; else out="$out&$name=$value"; fi
  fi
  printf '%s' "$out"
}

frag_marked() { # $1=frag $2=原来有没有 fragment(0/1) $3=标记（direct=直连 / door=任意门）-> 加后缀的新 frag（已有不再重复加）
  local frag="$1" has="$2" mark="$3" enc suffix
  if [ "$mark" = "door" ]; then enc="%E4%BB%BB%E6%84%8F%E9%97%A8"; suffix="任意门"; else enc="%E7%9B%B4%E8%BF%9E"; suffix="直连"; fi
  if [ "$has" = "1" ]; then
    case "$frag" in
      *"$enc"*|*"$suffix"*) printf '%s' "$frag" ;;
      *) printf '%s' "$frag-$enc" ;;
    esac
  else
    printf '%s' "$enc"
  fi
}

frag_direct() { # $1=frag $2=原来有没有 fragment(0/1) -> 加 -直连 后缀的新 frag
  frag_marked "$1" "$2" direct
}

frag_door() { # $1=frag $2=原来有没有 fragment(0/1) -> 加 -任意门 后缀的新 frag
  frag_marked "$1" "$2" door
}

twin_plain() { # vless/trojan/hysteria2/tuic：换地址 host，加/改 sni，名字加 -直连（$4=door 时加 -任意门）
  # 四者都是 userinfo@host:port?query#frag 的 URL 形状，同一套拆法通用。
  local link="$1" lip="$2" sni="$3" mark="${4:-direct}"
  local scheme="${link%%://*}"
  scheme="$(printf '%s' "$scheme" | tr '[:upper:]' '[:lower:]')"
  local rest="${link#*://}"
  local frag="" base="$rest" has_frag=0
  if [[ "$base" == *"#"* ]]; then frag="${base##*#}"; base="${base%#*}"; has_frag=1; fi
  local query="" has_query=0
  if [[ "$base" == *"?"* ]]; then query="${base#*\?}"; base="${base%%\?*}"; has_query=1; fi
  local auth="$base" path=""
  if [[ "$base" == */* ]]; then auth="${base%%/*}"; path="/${base#*/}"; fi
  local userinfo="" hostport="$auth"
  if [[ "$auth" == *"@"* ]]; then userinfo="${auth%@*}"; hostport="${auth##*@}"; fi
  local port=""
  if [[ "$hostport" == *:* ]]; then
    local p="${hostport##*:}"
    if [[ "$p" =~ ^[0-9]+$ ]]; then port="$p"; fi
  fi
  if [ -n "$sni" ]; then
    query="$(query_set_param "$query" "sni" "$sni")"
    has_query=1
  fi
  frag="$(frag_marked "$frag" "$has_frag" "$mark")"
  local out="$scheme://"
  [ -n "$userinfo" ] && out+="$userinfo@"
  out+="$lip"
  [ -n "$port" ] && out+=":$port"
  out+="$path"
  [ "$has_query" = "1" ] && out+="?$query"
  out+="#$frag"
  printf '%s' "$out"
}

ss_legacy_twin() { # 传统 base64 形式 ss://base64(method:password@host:port) -> 新 base64 体，失败返回 1
  local core="$1" lip="$2"
  python3 - "$core" "$lip" <<'PYEOF' 2>/dev/null || return 1
import sys, base64
s, lip = sys.argv[1], sys.argv[2]
s += '=' * (-len(s) % 4)
try:
    raw = base64.urlsafe_b64decode(s).decode()
except Exception:
    sys.exit(1)
if '@' not in raw:
    sys.exit(1)
ui, hp = raw.rsplit('@', 1)
if ':' in hp:
    host, port = hp.rsplit(':', 1)
    new = ui + '@' + lip + ':' + port
else:
    new = ui + '@' + lip
print(base64.b64encode(new.encode()).decode())
PYEOF
}

twin_ss() { # ss：只换地址 host，不加 sni；SIP002 直接换，传统 base64 形式走 python3 解码（$3=door 时名字加 -任意门）
  local link="$1" lip="$2" mark="${3:-direct}"
  local rest="${link#*://}"
  local frag="" core="$rest" has_frag=0
  if [[ "$core" == *"#"* ]]; then frag="${core##*#}"; core="${core%#*}"; has_frag=1; fi
  local query="" has_query=0
  if [[ "$core" == *"?"* ]]; then query="${core#*\?}"; core="${core%%\?*}"; has_query=1; fi
  local out_core="" tail=""
  if [[ "$core" == *"@"* ]]; then
    local userinfo="${core%@*}" after="${core##*@}"
    local hostport="${after%%/*}"
    if [ "$after" != "$hostport" ]; then tail="/${after#*/}"; fi
    local port=""
    if [[ "$hostport" == *:* ]]; then
      local p="${hostport##*:}"
      if [[ "$p" =~ ^[0-9]+$ ]]; then port="$p"; fi
    fi
    out_core="$userinfo@$lip"
    [ -n "$port" ] && out_core+=":$port"
    out_core+="$tail"
  else
    if ! command -v python3 >/dev/null 2>&1; then
      echo "__MANUAL__:传统 base64 格式的 ss 链接需要 python3 才能改地址"
      return 1
    fi
    out_core="$(ss_legacy_twin "$core" "$lip" || true)"
    if [ -z "$out_core" ]; then
      echo "__MANUAL__:传统 base64 格式的 ss 链接解码失败"
      return 1
    fi
  fi
  frag="$(frag_marked "$frag" "$has_frag" "$mark")"
  local out="ss://$out_core"
  [ "$has_query" = "1" ] && out+="?$query"
  out+="#$frag"
  printf '%s' "$out"
}

vmess_twin() { # vmess：python3 解 base64 JSON，改 add=落地IP、sni=域名、ps 加 -直连（$4=door 时加 -任意门）
  local b64="$1" lip="$2" sni="$3" mark="${4:-direct}"
  local suffix="直连"
  [ "$mark" = "door" ] && suffix="任意门"
  if ! command -v python3 >/dev/null 2>&1; then
    echo "__MANUAL__:vmess 链接需要 python3 解码"
    return 1
  fi
  local out=""
  out="$(python3 - "$b64" "$lip" "$sni" "$suffix" <<'PYEOF' 2>/dev/null || true
import sys, json, base64
b64, lip, sni, mark = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
b64 += '=' * (-len(b64) % 4)
try:
    j = json.loads(base64.urlsafe_b64decode(b64))
except Exception:
    sys.exit(1)
j['add'] = lip
if sni:
    j['sni'] = sni
ps = j.get('ps') or 'vmess'
if mark not in ps:
    j['ps'] = ps + '-' + mark
print('vmess://' + base64.b64encode(json.dumps(j, ensure_ascii=False, separators=(',', ':')).encode()).decode())
PYEOF
)"
  if [ -z "$out" ]; then
    echo "__MANUAL__:vmess 链接解码失败"
    return 1
  fi
  printf '%s' "$out"
}

twin_uri() { # $1=link $2=落地IP $3=sni域名 [$4=标记 direct/door] -> echo twin；失败 echo __MANUAL__:原因 并 return 1
  local link="$1" lip="$2" sni="$3" mark="${4:-direct}"
  local scheme="${link%%://*}"
  scheme="$(printf '%s' "$scheme" | tr '[:upper:]' '[:lower:]')"
  case "$scheme" in
    vless|trojan|hysteria2|hy2|tuic)
      twin_plain "$link" "$lip" "$sni" "$mark"
      ;;
    ss)
      twin_ss "$link" "$lip" "$mark"
      ;;
    vmess)
      local b64="${link#*://}"
      b64="${b64%%#*}"
      vmess_twin "$b64" "$lip" "$sni" "$mark"
      ;;
    *)
      echo "__MANUAL__:不识别的链接格式"
      return 1
      ;;
  esac
}

door_uri() { # $1=link $2=中转地址 $3=sni域名 $4=转发端口 -> echo 任意门链接；失败 echo __MANUAL__:原因 并 return 1
  local link="$1" addr="$2" sni="$3" lp="$4"
  local scheme="${link%%://*}"
  scheme="$(printf '%s' "$scheme" | tr '[:upper:]' '[:lower:]')"
  local out=""
  case "$scheme" in
    vless|trojan|hysteria2|hy2|tuic)
      out="$(twin_plain "$link" "$addr" "$sni" door)" || { printf '%s' "$out"; return 1; }
      ;;
    ss)
      out="$(twin_ss "$link" "$addr" door)" || { printf '%s' "$out"; return 1; }
      ;;
    vmess)
      local b64="${link#*://}"
      b64="${b64%%#*}"
      out="$(vmess_twin "$b64" "$addr" "$sni" door)" || { printf '%s' "$out"; return 1; }
      ;;
    *)
      echo "__MANUAL__:不识别的链接格式"
      return 1
      ;;
  esac
  # twin_* 保留了原链接的端口；任意门要换成转发端口
  set_link_port "$out" "$lp"
}

link_sni_domain() { # $1=link -> 打印 sni 用的域名（链接地址是域名 -> query 的 sni= -> query 的 host=），找不到返回 1
  local link="$1"
  local scheme="${link%%://*}"
  scheme="$(printf '%s' "$scheme" | tr '[:upper:]' '[:lower:]')"
  local rest="${link#*://}"
  local core="${rest%%#*}"
  local q=""
  if [[ "$core" == *"?"* ]]; then q="${core#*\?}"; core="${core%%\?*}"; fi
  local host="" v=""
  case "$scheme" in
    vless|trojan|hysteria2|hy2|tuic)
      local auth="${core%%/*}"
      host="${auth##*@}"
      host="${host%%:*}"
      host="${host#[}"
      host="${host%]}"
      if [ -n "$host" ] && ! is_ip "$host"; then printf '%s' "$host"; return 0; fi
      ;;
    ss)
      if [[ "$core" == *"@"* ]]; then
        local after="${core##*@}"
        host="${after%%/*}"
        host="${host%%:*}"
        if [ -n "$host" ] && ! is_ip "$host"; then printf '%s' "$host"; return 0; fi
      fi
      ;;
    vmess)
      if command -v python3 >/dev/null 2>&1; then
        python3 - "$core" <<'PYEOF' 2>/dev/null || true
import sys, json, base64
s = sys.argv[1]
s += '=' * (-len(s) % 4)
try:
    j = json.loads(base64.urlsafe_b64decode(s))
except Exception:
    sys.exit(0)
def isdom(x):
    return isinstance(x, str) and x and ('.' in x) and not all(c.isdigit() or c == '.' for c in x)
for k in ('sni', 'host', 'add'):
    v = j.get(k)
    if isdom(v):
        print(v)
        break
PYEOF
      fi
      return 0
      ;;
  esac
  v="$(query_param "$q" "sni" || true)"
  if [ -n "$v" ]; then printf '%s' "$v"; return 0; fi
  v="$(query_param "$q" "host" || true)"
  if [ -n "$v" ]; then printf '%s' "$v"; return 0; fi
  return 1
}

try_install_qrencode() { # 有就直接用；没有就试着装一个，装不上也不报错
  command -v qrencode >/dev/null 2>&1 && return 0
  echo "没找到 qrencode，尝试安装（装不上也不影响链接生成）..."
  local _to=""
  command -v timeout >/dev/null 2>&1 && _to="timeout 120"
  if command -v apt-get >/dev/null 2>&1; then
    $_to apt-get install -y qrencode >/dev/null 2>&1 || true
    command -v qrencode >/dev/null 2>&1 && return 0
    $_to apt-get update >/dev/null 2>&1 || true
    $_to apt-get install -y qrencode >/dev/null 2>&1 || true
  elif command -v dnf >/dev/null 2>&1; then
    $_to dnf install -y qrencode >/dev/null 2>&1 || true
  elif command -v yum >/dev/null 2>&1; then
    $_to yum install -y qrencode >/dev/null 2>&1 || true
  fi
  command -v qrencode >/dev/null 2>&1
}

step7_nodes() { # 第 7 步：双节点链接（可选；跳过不报错，始终 return 0）
  # 依赖全局变量：LANDING / PORTS / MODE_NOTE / STEP7_INDEX / RELAY_MODE / RELAY_ADDR；nodes.txt 由调用方先截断一次，这里只追加
  local _relay_label="中转" _qr_file="relay"
  if [ "$RELAY_MODE" = "door" ]; then _relay_label="任意门"; _qr_file="door"; fi
  echo ""
  echo "== 7/7 双节点链接（直连 + $_relay_label）——落地 $STEP7_INDEX：$LANDING =="
  local landing_ip="" _resolve_note=""
  if is_ip "$LANDING"; then
    landing_ip="$LANDING"
  else
    landing_ip="$(getent hosts "$LANDING" 2>/dev/null | awk '{print $1}' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)"
    if [ -z "$landing_ip" ]; then
      landing_ip="$LANDING"
      _resolve_note="（域名现在解析不到，直连先用域名本身；等 DNS 恢复后重跑 bash install.sh --nodes 再生成）"
    fi
  fi
  echo "请粘贴你原来的节点链接："
  echo "（可多条，一行一条；粘完按一次回车（空行）结束；不想生成就直接回车跳过）"
  local -a links=()
  local _line="" _tty=0
  if [ -t 0 ] && exec 3<&0 2>/dev/null; then
    _tty=1
  elif [ -r /dev/tty ] && { exec 3</dev/tty; } 2>/dev/null; then
    _tty=1
  fi
  if [ "$_tty" = "1" ]; then
    while IFS= read -r _line <&3; do
      _line="$(printf '%s' "$_line" | tr -d ' \t\r')"
      [ -z "$_line" ] && break
      links+=("$_line")
    done
    exec 3<&- 2>/dev/null || true
  else
    echo "（当前没有可交互的终端，跳过这一步；装完可再跑 bash install.sh --nodes 补生成。）"
    return 0
  fi
  if [ "${#links[@]}" = "0" ]; then
    echo "已跳过双节点生成（以后随时跑 bash install.sh --nodes 补生成）。"
    return 0
  fi
  if [ -n "$_resolve_note" ]; then
    echo "注意：$landing_ip $_resolve_note"
  fi
  local nodes_dir="$HOME/relay-node-kit"
  mkdir -p "$nodes_dir" 2>/dev/null || true
  local nodes_file="$nodes_dir/nodes.txt"
  {
    echo ""
    echo "## 落地 $STEP7_INDEX：$LANDING（${MODE_NOTE:-}）"
    if [ "$RELAY_MODE" = "door" ]; then
      echo "# 任意门：地址换成中转地址 $RELAY_ADDR、端口换成转发端口；直连：地址换成落地 IP $landing_ip + sni=域名"
    else
      echo "# 中转：原链接按实际监听端口走（监听端口改过时中转链接端口跟着改）；直连：地址换成落地 IP $landing_ip + sni=域名"
    fi
  } >> "$nodes_file" 2>/dev/null || nodes_file="/dev/null"
  local -a ORIGS=() TWINS=()
  local i=0 _orig="" _sni="" _out="" _twin="" _relay="" _orig_port="" _listen_port="" _reason="" _scheme_l=""
  for _orig in "${links[@]}"; do
    i=$((i + 1))
    _sni="$(link_sni_domain "$_orig" || true)"
    _scheme_l="$(printf '%s' "${_orig%%://*}" | tr '[:upper:]' '[:lower:]')"
    if [ -z "$_sni" ] && [ "$SNI_ASKED" != "1" ]; then
      case "$_scheme_l" in
        vless|trojan|vmess|hysteria2|hy2|tuic)
          SNI_ASKED=1
          echo "请填写直连要用的域名（SNI）："
          ask "第 $i 条链接里没找到域名，直连要用的域名（SNI，就是证书上的那个域名；不知道就直接回车）: " SNI_DOMAIN
          _sni="${SNI_DOMAIN:-}"
          ;;
      esac
    fi
    # 中转/任意门链接：标准模式只换端口（监听≠落地端口时才换）；
    # 任意门模式换地址+端口（中转地址:转发端口），名字后缀 -任意门
    _relay="$_orig"
    _orig_port="$(link_port "$_orig" || true)"
    _listen_port=""
    if [ -n "$_orig_port" ]; then
      _listen_port="$(listen_for_remote "$PORTS" "$_orig_port" || true)"
    fi
    if [ "$RELAY_MODE" = "door" ]; then
      if [ -n "$_listen_port" ]; then
        if _out="$(door_uri "$_orig" "$RELAY_ADDR" "$_sni" "$_listen_port" 2>/dev/null)"; then
          _relay="$_out"
        else
          _reason="${_out#__MANUAL__:}"
          [ -n "$_reason" ] || _reason="这条不会自动生成"
          _relay=""
        fi
      else
        _reason="端口 $_orig_port 不在本次转发表里"
        _relay=""
      fi
    else
      if [ -n "$_listen_port" ] && [ "$_listen_port" != "$_orig_port" ]; then
        _relay="$(set_link_port "$_orig" "$_listen_port")"
      fi
    fi
    echo ""
    echo "---- 第 $i 条 ----"
    if [ -n "$_relay" ]; then
      echo "$_relay_label：$_relay"
      if [ "$RELAY_MODE" = "door" ]; then
        echo "（任意门转发端口是 $_listen_port，原链接端口 $_orig_port 已换成转发端口；直连仍走落地端口 $_orig_port）"
      elif [ -n "$_listen_port" ] && [ "$_listen_port" != "$_orig_port" ]; then
        echo "（中转监听端口是 $_listen_port，原链接端口 $_orig_port 已换成监听端口；直连仍走落地端口 $_orig_port）"
      elif [ -n "$_orig_port" ] && [ -z "$_listen_port" ]; then
        echo "（注意：这条链接的端口 $_orig_port 不在本次转发表里，中转可能不通，请确认端口对不对）"
      fi
    else
      echo "$_relay_label：$_reason，请手动改地址（地址换成 $RELAY_ADDR、端口换成 $_listen_port，TLS 的 SNI 填域名）。"
    fi
    _twin=""
    if _out="$(twin_uri "$_orig" "$landing_ip" "$_sni" 2>/dev/null)"; then
      _twin="$_out"
      echo "直连：$_twin"
    else
      _reason="${_out#__MANUAL__:}"
      [ -n "$_reason" ] || _reason="这条不会自动生成"
      echo "直连：$_reason，请手动改地址（把地址换成 $landing_ip，TLS 的 SNI 填域名）。"
    fi
    ORIGS+=("$_relay")
    TWINS+=("$_twin")
    {
      echo ""
      echo "[第 $i 条]"
      if [ -n "$_relay" ]; then echo "$_relay_label：$_relay"; else echo "$_relay_label：$_reason（手动改）"; fi
      [ -n "$_twin" ] && echo "直连：$_twin"
    } >> "$nodes_file" 2>/dev/null || true
  done
  echo ""
  echo "两条链接已存到 $nodes_file"
  if try_install_qrencode; then
    local j=0 n=0
    for j in "${!ORIGS[@]}"; do
      n=$((j + 1))
      [ -n "${ORIGS[$j]}" ] || continue
      echo ""
      echo "$_relay_label二维码（落地 $STEP7_INDEX 第 $n 条）："
      qrencode -t ANSIUTF8 "${ORIGS[$j]}" 2>/dev/null || echo "（这条二维码生成失败，复制链接导入）"
      qrencode -o "$nodes_dir/nodes-L${STEP7_INDEX}-${_qr_file}-$n.png" "${ORIGS[$j]}" 2>/dev/null || true
      if [ -n "${TWINS[$j]}" ]; then
        echo "直连二维码（落地 $STEP7_INDEX 第 $n 条）："
        qrencode -t ANSIUTF8 "${TWINS[$j]}" 2>/dev/null || echo "（这条二维码生成失败，复制链接导入）"
        qrencode -o "$nodes_dir/nodes-L${STEP7_INDEX}-direct-$n.png" "${TWINS[$j]}" 2>/dev/null || true
      fi
    done
    echo ""
    echo "二维码 PNG 已存到 $nodes_dir/nodes-L${STEP7_INDEX}-${_qr_file}-N.png、nodes-L${STEP7_INDEX}-direct-N.png"
  else
    echo "没有 qrencode（也没装上），复制上面两条链接导入即可；想看二维码可装好 qrencode 后再跑 bash install.sh --nodes。"
  fi
  return 0
}

# 只生成一条链接的直连 twin：bash install.sh --twin '<链接>' <落地IP> [sni域名]
# （面板调用；标准输出只打印 twin 链接一行，失败打印 __MANUAL__:原因）
if [ "$TWIN_MODE" = "1" ]; then
  _twin_link="${1:-}"; _twin_ip="${2:-}"; _twin_sni="${3:-}"
  if [ -z "$_twin_link" ] || [ -z "$_twin_ip" ]; then
    echo "__MANUAL__:用法：bash install.sh --twin '<链接>' <落地IP> [sni域名]"
    exit 1
  fi
  if [ -z "$_twin_sni" ]; then
    _twin_sni="$(link_sni_domain "$_twin_link" 2>/dev/null || true)"
  fi
  twin_uri "$_twin_link" "$_twin_ip" "$_twin_sni"
  exit $?
fi

# 只生成一条任意门链接：bash install.sh --door '<链接>' <中转地址> <转发端口> [sni域名]
# （面板调用；标准输出只打印任意门链接一行，失败打印 __MANUAL__:原因）
if [ "$DOOR_MODE" = "1" ]; then
  _door_link="${1:-}"; _door_addr="${2:-}"; _door_port="${3:-}"; _door_sni="${4:-}"
  if [ -z "$_door_link" ] || [ -z "$_door_addr" ] || [ -z "$_door_port" ]; then
    echo "__MANUAL__:用法：bash install.sh --door '<链接>' <中转地址> <转发端口> [sni域名]"
    exit 1
  fi
  if ! valid_port "$_door_port"; then
    echo "__MANUAL__:转发端口不合法：$_door_port（1-65535）"
    exit 1
  fi
  if [ -z "$_door_sni" ]; then
    _door_sni="$(link_sni_domain "$_door_link" 2>/dev/null || true)"
  fi
  door_uri "$_door_link" "$_door_addr" "$_door_sni" "$_door_port"
  exit $?
fi

# 只生成双节点：bash install.sh --nodes（读 /etc/realm/relay.conf 里的落地信息，不重装）
if [ "$NODES_ONLY" = "1" ]; then
  if [ ! -f /etc/realm/relay.conf ]; then
    echo "没找到 /etc/realm/relay.conf：中转还没装过，先正常跑一次安装（bash install.sh），再跑 bash install.sh --nodes。"
    exit 1
  fi
  # shellcheck disable=SC1091
  . /etc/realm/relay.conf
  if [ -z "${LANDING:-}" ]; then
    echo "relay.conf 里没有 LANDING，先正常安装一次。"
    exit 1
  fi
  # nodes.txt 本次运行先截断一次，各落地按分组追加（不互相覆盖）。
  # 但面板 twin 生成的手动分组（分组头带“手动 twin”标记）要原样保留：先抽出来，再生成完追加回去。
  _twin_keep="$(mktemp 2>/dev/null || true)"
  if [ -n "$_twin_keep" ] && [ -f "$HOME/relay-node-kit/nodes.txt" ]; then
    awk '/^## 落地 /{twin=($0 ~ /手动 twin/)} twin' "$HOME/relay-node-kit/nodes.txt" > "$_twin_keep" 2>/dev/null || true
  fi
  mkdir -p "$HOME/relay-node-kit" 2>/dev/null || true
  {
    echo "# relay-node 双节点（$(date -u +%Y-%m-%dT%H:%M:%SZ)）"
    echo "# 中转：原链接按实际监听端口走（监听端口改过时中转链接端口跟着改）；直连：地址换成落地 IP + sni=域名"
    echo "# 任意门：地址换成中转地址、端口换成转发端口；直连：地址换成落地 IP + sni=域名"
  } > "$HOME/relay-node-kit/nodes.txt" 2>/dev/null || true
  _count="${LANDING_COUNT:-1}"
  _idx=1
  while [ "$_idx" -le "$_count" ]; do
    if [ "$_idx" = "1" ]; then
      LANDING="${LANDING:-}"; PORTS="${PORTS:-}"; MODE_NOTE="${MODE_NOTE:-}"; DOMAIN="${DOMAIN:-}"
      RELAY_MODE="${MODE:-standard}"
    else
      LANDING="$(landing_field "$_idx" LANDING)"
      PORTS="$(landing_field "$_idx" PORTS)"
      MODE_NOTE="$(landing_field "$_idx" MODE_NOTE)"
      DOMAIN="$(landing_field "$_idx" DOMAIN)"
      RELAY_MODE="$(landing_field "$_idx" MODE)"
      [ -n "$RELAY_MODE" ] || RELAY_MODE="standard"
    fi
    if [ -z "$LANDING" ]; then
      echo "relay.conf 里落地 $_idx 没有 LANDING，跳过。"
      _idx=$((_idx + 1))
      continue
    fi
    STEP7_INDEX="$_idx"
    step7_nodes
    _idx=$((_idx + 1))
  done
  # 把前面抽出来的手动 twin 分组原样追加回去（不洗掉面板 twin 的历史）
  if [ -n "${_twin_keep:-}" ] && [ -s "$_twin_keep" ]; then
    cat "$_twin_keep" >> "$HOME/relay-node-kit/nodes.txt" 2>/dev/null || true
  fi
  [ -n "${_twin_keep:-}" ] && rm -f "$_twin_keep"
  exit 0
fi


# ---------- 主流程函数（一个落地一轮：收集 -> 预检 -> 写配置/追加 -> 启动/重启 -> 汇总） ----------
collect_landing() { # $1=标题（如 "1/6 落地目标与转发预设"），收集一个落地的 LANDING/PRESET/PORTS/MODE_NOTE
  echo ""
  echo "== $1 =="
  echo "这一步要填两样：① 落地服务器的 IP 或域名 ② 转发预设（选 1/2/3）。"
  if [ -z "$LANDING" ]; then
    echo "请填写你的落地 IP 或域名："
    ask "① 落地服务器 IP（推荐填 IP，例如 203.0.113.1）或域名，填完按回车： " LANDING
  fi
  [ -n "$LANDING" ] || { echo "没给落地地址，装不了。重跑并输入落地 IP 或域名。"; exit 1; }
  if is_ip "$LANDING"; then
    LANDING_IS_DOMAIN=0
    DOMAIN=""
    CERT_DOMAIN="${CERT_DOMAIN:-}"
    echo "落地：$LANDING（IP 直连，中转完全不碰 DNS）"
  else
    LANDING_IS_DOMAIN=1
    DOMAIN="$LANDING"
    CERT_DOMAIN="$LANDING"
    echo "落地：$LANDING（域名，中转启动时解析一次）"
    if ! getent hosts "$LANDING" >/dev/null 2>&1; then
      echo "注意：本机现在解析不到 $LANDING（DNS 可能还没生效或出站 DNS 被拦）。"
      echo "      realm 启动时会再解析，解析失败则转发不通；确认域名能解析后再继续更稳。"
    fi
  fi

  # 转发模式：面板非交互调用用 RELAY_MODE 环境变量预设（standard/door，或 1/2），不预设就交互问
  if [ -z "$RELAY_MODE" ]; then
    echo ""
    echo "转发模式："
    echo "  1) 标准模式  ← 默认，直接回车就是 1"
    echo "     域名 A 记录指到中转机，客户端用节点域名直连中转的 80/443，链接干净、不用带端口。"
    echo "     适合：域名能自由改指向、只转一两个落地。代价：第 2 个及以后的落地证书续签走不通中转，到期前要手动改指向续签。"
    echo "  2) 任意门模式"
    echo "     节点域名永远指着落地不动，中转机上每个落地分一个专用转发端口，客户端用“中转地址:转发端口”连过来，SNI 还是节点域名。"
    echo "     适合：不想改来改去域名指向、要转多个落地、或域名动不了。代价：链接要带转发端口，中转地址会暴露在链接里，转发端口要在云厂商安全组和 ufw 里放行。"
    echo "请输入你的选择："
    ask "选哪种模式？[1/2]，直接回车选默认 1（标准模式）： " RELAY_MODE "1"
  fi
  RELAY_MODE="$(normalize_relay_mode "$RELAY_MODE" || true)"
  if [ -z "$RELAY_MODE" ]; then
    echo "模式只能选 1/2，默认按 1（标准模式）处理。"
    RELAY_MODE="standard"
  fi
  echo "转发模式：$RELAY_MODE"

  if [ -z "$PRESET" ]; then
    echo ""
    echo "转发预设："
    echo "  1) landing-node（Xray 核心）：80 + 443（TCP）  ← 默认，直接回车就是 1"
    echo "  2) landing-node（sing-box 核心）：80 + 443（TCP）+ 8388（TCP+UDP）+ 8443（UDP）+ 8444（UDP）"
    echo "  3) 自定义端口（自己输）"
    echo "请输入你的选择："
    ask "② 选预设 [1/2/3]，直接回车选默认 1： " PRESET "1"
  fi

  # PORTS 格式：以空格分隔的 "协议:监听端口:落地端口"，如 "tcp:443:443 udp:8388:8388"
  PORTS=""
  MODE_NOTE=""
  case "$PRESET" in
    1)
      PORTS="tcp:80:80 tcp:443:443"
      MODE_NOTE="landing-node（Xray 核心）"
      ;;
    2)
      echo "  1) TLS 模式（443）  ← 默认，直接回车就是 1"
      echo "  2) 明文模式（8080-8082）"
      echo "请输入你的选择："
      ask "落地 sing-box 核心是 TLS 还是明文？[1/2]，直接回车选默认 1（TLS）： " SB_MODE "1"
      if [ "$SB_MODE" = "2" ]; then
        PORTS="tcp:8080:8080 tcp:8081:8081 tcp:8082:8082 tcp:8388:8388 udp:8388:8388"
        MODE_NOTE="landing-node（sing-box 核心）明文模式（HY2/TUIC 不启用）"
      else
        PORTS="tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388 udp:8443:8443 udp:8444:8444"
        MODE_NOTE="landing-node（sing-box 核心）TLS 模式"
      fi
      ;;
    3)
      echo "自定义：输入要转发的端口，逗号分隔；监听端口默认与落地端口相同，"
      echo "  如需改监听端口，用 监听:落地 的写法，例如：443,8388,8443:9443"
      echo "请填写要转发的端口列表："
      ask "端口列表（逗号分隔，填完按回车继续）： " CUSTOM_PORTS
      [ -n "$CUSTOM_PORTS" ] || { echo "没给端口，装不了。"; exit 1; }
      echo "请输入你的选择："
      ask "这些端口要不要同时转 UDP？[y/N]，直接回车选默认 N： " WANT_UDP "N"
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
  # 任意门模式：监听端口按落地端口 +20000 映射，中转地址问一次（面板用 RELAY_ADDR 预设）
  if [ "$RELAY_MODE" = "door" ]; then
    ensure_relay_addr
    PORTS="$(door_map_ports "$PORTS")" || exit 1
    confirm_door_ports
    MODE_NOTE="$MODE_NOTE（任意门）"
  fi
  echo "预设：$MODE_NOTE"
  echo "转发：$PORTS"
}

precheck_ports() { # $1=标题（如 "2/6 端口预检（中转监听端口）"）：查 $PORTS 的监听端口
  # 同时查系统已占端口和 config.toml 里已配好的 realm 监听端口（追加的落地不能和已有的抢端口）
  # 已走分流的端口（SHARE80/SHARE443）不再当作冲突，新落地直接加入分流
  # 80/443 被已装好的落地占了时，先让用户选解决方式（默认改监听走高位端口；也可以选分流）
  # 其余端口被占，或分流选完还剩冲突，仍走原来的改监听流程
  echo ""
  echo "== $1 =="
  # 面板非交互调用：LISTEN_OVERRIDES="80:10080,443:10443" 预设监听端口改法（只改中转监听，落地端口不变）
  if [ -n "${LISTEN_OVERRIDES:-}" ]; then
    local _ov _o _old _new _np _spec _proto _rest _lp _rp
    IFS=',' read -ra _ov <<< "$LISTEN_OVERRIDES"
    for _o in "${_ov[@]}"; do
      _o="${_o//[[:space:]]/}"
      [ -n "$_o" ] || continue
      [[ "$_o" == *:* ]] || { echo "LISTEN_OVERRIDES 格式不对：$_o（应为 原端口:新端口）"; exit 1; }
      _old="${_o%%:*}"; _new="${_o##*:}"
      valid_port "$_old" && valid_port "$_new" || { echo "LISTEN_OVERRIDES 端口不合法：$_o（1-65535）"; exit 1; }
      case " $PORTS " in
        *" tcp:$_old:"*|*" udp:$_old:"*)
          if port_in_use tcp "$_new" || port_in_use udp "$_new" || port_in_config "$_new"; then
            echo "LISTEN_OVERRIDES 里新端口 $_new 已被占用，换一个。"
            exit 1
          fi
          _np=""
          for _spec in $PORTS; do
            _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
            [ "$_lp" = "$_old" ] && _lp="$_new"
            _np="$_np $_proto:$_lp:$_rp"
          done
          PORTS="${_np# }"
          echo "监听端口已按预设改好：$_old -> $_new（落地端口不变）"
          ;;
      esac
    done
  fi
  local _occupied="" _spec _proto _rest _lp _rp
  local _realm80=0 _realm443=0
  while true; do
    _occupied=""; _realm80=0; _realm443=0
    for _spec in $PORTS; do
      _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"
      if [ "$_proto" = "tcp" ] && [ "$_lp" = "80" ] && [ "$SHARE80" = "1" ]; then continue; fi
      if [ "$_proto" = "tcp" ] && [ "$_lp" = "443" ] && [ "$SHARE443" = "1" ]; then continue; fi
      if port_in_use "$_proto" "$_lp" || port_in_config "$_lp"; then
        case " $_occupied " in *" $_lp/$_proto "*) ;; *) _occupied="$_occupied $_lp/$_proto";; esac
        if port_in_config "$_lp"; then
          [ "$_lp" = "80" ] && _realm80=1
          [ "$_lp" = "443" ] && _realm443=1
        fi
      fi
    done
    _occupied="${_occupied# }"
    if [ -z "$_occupied" ]; then
      echo "监听端口全部空闲。"
      return 0
    fi
    echo "端口已被占用：$_occupied"
    if [ "$SHARE_ASKED" = "0" ] && { [ "$_realm80" = "1" ] || [ "$_realm443" = "1" ]; }; then
      SHARE_ASKED=1
      if ! choose_share_mode "$_realm80" "$_realm443"; then
        echo "分流没启用，改回改监听走高位端口的方式。"
      fi
      echo "选完重查一遍："
      continue
    fi
    echo "请输入你的选择："
    CHANGE_LISTEN=""
    ask "这些端口被占了，要不要改监听端口（只改中转这边监听的端口，落地端口不变）？[y/N]，直接回车选默认 N： " CHANGE_LISTEN "N"
    if [ "$CHANGE_LISTEN" != "y" ] && [ "$CHANGE_LISTEN" != "Y" ]; then
      echo "中转需要独占这些监听端口，装不上。"
      echo "先停掉占着它们的程序（ss -tlnp / ss -ulnp 可查是谁），或换一台中转服务器再跑。"
      exit 1
    fi
    # 逐个被占端口问新监听端口（按端口号去重；TCP/UDP 同号一起改，端口号保持一致）
    local _done_ports="" _pp _p _sugg _new_lp _new_ports _clash
    for _pp in $_occupied; do
      _p="${_pp%%/*}"
      case " $_done_ports " in *" $_p "*) continue ;; esac
      _done_ports="$_done_ports $_p"
      _sugg="$(suggest_port "$_p")"
      if [ "$_p" = "80" ]; then
        echo "注意：80 端口是给证书续签用的，改监听端口后这个落地的证书续签走不通这条路（验证只认 80 端口）；续签时把域名临时改回指向落地，续完再改回来。"
      fi
      while true; do
        echo "请填写新的监听端口（原端口 $_p）："
        _new_lp=""
        ask "新监听端口 [建议值 $_sugg，直接回车就是 $_sugg]： " _new_lp "$_sugg"
        if ! valid_port "$_new_lp"; then
          echo "端口不合法：$_new_lp（1-65535），重新填。"
          continue
        fi
        _clash=0
        for _spec in $PORTS; do
          _rest="${_spec#*:}"; _lp="${_rest%%:*}"
          if [ "$_lp" = "$_new_lp" ] && [ "$_lp" != "$_p" ]; then _clash=1; fi
        done
        if [ "$_clash" = "1" ] || port_in_use tcp "$_new_lp" || port_in_use udp "$_new_lp" || port_in_config "$_new_lp"; then
          echo "端口 $_new_lp 也被占了，换一个再填。"
          continue
        fi
        break
      done
      _new_ports=""
      for _spec in $PORTS; do
        _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
        if [ "$_lp" = "$_p" ]; then _lp="$_new_lp"; fi
        _new_ports="$_new_ports $_proto:$_lp:$_rp"
      done
      PORTS="${_new_ports# }"
      echo "已改：监听端口 $_p -> $_new_lp（落地端口不变）"
    done
    echo "改完重查一遍："
  done
}

setup_realm_once() { # 下载 realm；已存在且能跑就直接复用，不重复下载
  echo ""
  echo "== 3/6 下载 realm =="
  if [ -x /usr/local/bin/realm ] && /usr/local/bin/realm --version >/dev/null 2>&1; then
    echo "realm 已存在（$(/usr/local/bin/realm --version 2>/dev/null | head -1)），直接复用，不重复下载。"
    local _v
    _v="$(/usr/local/bin/realm --version 2>/dev/null | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
    [ -n "$_v" ] && REALM_VERSION="$_v"
    return 0
  fi
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64|amd64) ASSET_ARCH="x86_64" ;;
    aarch64|arm64) ASSET_ARCH="aarch64" ;;
    *) echo "不支持的架构：$ARCH（realm 只提供 x86_64 / aarch64 的 Linux 包）"; exit 1 ;;
  esac
  if [ -z "$REALM_VERSION" ]; then
    # 注意：pipefail 下 curl 失败会直接杀脚本（set -e），加 || true 让失败走下面的固定回退版本
    REALM_VERSION="$(curl -fsSL --max-time 15 "https://api.github.com/repos/zhboner/realm/releases/latest" 2>/dev/null | grep -o '"tag_name": *"v[^"]*"' | head -1 | sed 's/.*"v/v/;s/"//' || true)"
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
}

write_config_fresh() { # 第 1 个落地：全新写 config.toml 与 relay.conf
  echo ""
  echo "== 4/6 写 realm 配置 =="
  mkdir -p /etc/realm
  local HAS_UDP=0
  local _spec
  for _spec in $PORTS; do
    [ "${_spec%%:*}" = "udp" ] && HAS_UDP=1
  done
  local USE_UDP="false"
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
    local _proto="${_spec%%:*}"; local _rest="${_spec#*:}"; local _lp="${_rest%%:*}"; local _rp="${_rest##*:}"
    cat >> /etc/realm/config.toml <<EOF

[[endpoints]]
listen = "0.0.0.0:$_lp"
remote = "$LANDING:$_rp"
EOF
  done
  # 给 check.sh / stop.sh 用的状态文件（多落地：1 号落地用平铺变量，2 号起用 _编号 后缀）
  # CERT_DOMAIN 是证书上的域名（分流按它认落地）；SHARE80/SHARE443 是 80/443 是否走了分流共享
  cat > /etc/realm/relay.conf <<EOF
LANDING=$LANDING
MODE="$RELAY_MODE"
LANDING_IS_DOMAIN=$LANDING_IS_DOMAIN
DOMAIN="$DOMAIN"
CERT_DOMAIN="$CERT_DOMAIN"
PRESET=$PRESET
MODE_NOTE="$MODE_NOTE"
PORTS="$PORTS"
RELAY_ADDR="$RELAY_ADDR"
REALM_VERSION="$REALM_VERSION"
LANDING_COUNT=1
ALL_PORTS="$PORTS"
SHARE80=0
SHARE443=0
EOF
  LANDING_COUNT=1
  ALL_PORTS="$PORTS"
  echo "配置已写：/etc/realm/config.toml"
}

append_config() { # 再加一个落地：只往 config.toml 后追加 endpoints 段，不重写头部；更新 relay.conf
  local _idx=$((LANDING_COUNT + 1))
  local _spec _proto _rest _lp _rp
  mkdir -p /etc/realm
  # 新落地要 UDP 但配置里关着，就打开
  local _has_udp=0
  for _spec in $PORTS; do
    [ "${_spec%%:*}" = "udp" ] && _has_udp=1
  done
  if [ "$_has_udp" = "1" ] && grep -q '^use_udp = false' /etc/realm/config.toml 2>/dev/null; then
    sed -i 's/^use_udp = false/use_udp = true/' /etc/realm/config.toml
    echo "新落地需要 UDP，已把 use_udp 打开。"
  fi
  {
    echo ""
    echo "# 追加落地 $_idx：$MODE_NOTE，落地 $LANDING（$(date -u +%Y-%m-%dT%H:%M:%SZ)）"
  } >> /etc/realm/config.toml
  # 走分流的端口不写进 realm（80 归 nginx、443 归 haproxy），其余端口照常追加
  local _skipped=""
  for _spec in $PORTS; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
    if [ "$_proto" = "tcp" ] && [ "$_lp" = "80" ] && [ "$SHARE80" = "1" ]; then _skipped="$_skipped 80"; continue; fi
    if [ "$_proto" = "tcp" ] && [ "$_lp" = "443" ] && [ "$SHARE443" = "1" ]; then _skipped="$_skipped 443"; continue; fi
    cat >> /etc/realm/config.toml <<EOF

[[endpoints]]
listen = "0.0.0.0:$_lp"
remote = "$LANDING:$_rp"
EOF
  done
  [ -n "$_skipped" ] && echo "端口${_skipped} 已走分流，不再写进 realm 配置（80 归 nginx、443 归 haproxy）。"
  # relay.conf：追加编号块，更新 LANDING_COUNT 与 ALL_PORTS（老 relay.conf 没有这两行也能补上）
  {
    echo "LANDING_$_idx=$LANDING"
    echo "MODE_$_idx=\"$RELAY_MODE\""
    echo "LANDING_IS_DOMAIN_$_idx=$LANDING_IS_DOMAIN"
    echo "DOMAIN_$_idx=\"$DOMAIN\""
    echo "CERT_DOMAIN_$_idx=\"$CERT_DOMAIN\""
    echo "PRESET_$_idx=$PRESET"
    echo "MODE_NOTE_$_idx=\"$MODE_NOTE\""
    echo "PORTS_$_idx=\"$PORTS\""
  } >> /etc/realm/relay.conf
  if grep -q '^LANDING_COUNT=' /etc/realm/relay.conf; then
    sed -i "s/^LANDING_COUNT=.*/LANDING_COUNT=$_idx/" /etc/realm/relay.conf
  else
    echo "LANDING_COUNT=$_idx" >> /etc/realm/relay.conf
  fi
  local _all
  _all="$(grep '^ALL_PORTS=' /etc/realm/relay.conf | head -1 | cut -d= -f2- | tr -d '"')"
  if [ -z "$_all" ]; then
    _all="$(grep '^PORTS=' /etc/realm/relay.conf | head -1 | cut -d= -f2- | tr -d '"')"
  fi
  for _spec in $PORTS; do
    case " $_all " in *" $_spec "*) ;; *) _all="$_all $_spec";; esac
  done
  _all="${_all# }"
  sed -i '/^ALL_PORTS=/d' /etc/realm/relay.conf
  echo "ALL_PORTS=\"$_all\"" >> /etc/realm/relay.conf
  ALL_PORTS="$_all"
  LANDING_COUNT="$_idx"
  echo "配置已追加：/etc/realm/config.toml（落地 $_idx，$MODE_NOTE）"
  # 分流开着时，分流配置也要重新生成（把新落地加进去），否则新落地分不到流量
  # （用 if 而不用 && 链：没开分流时 && 链返回 1，set -e 会直接杀掉脚本）
  if [ "$SHARE80" = "1" ]; then write_proxy80_config; fi
  if [ "$SHARE443" = "1" ]; then write_sni_config; fi
}

setup_sysctl_once() { # BBR + fq + 大 TCP 缓冲；已存在就跳过，只建一次
  echo ""
  echo "== 5/6 网络加速（BBR / fq / TCP 缓冲 / TFO） =="
  if [ -f /etc/sysctl.d/99-relay-node.conf ]; then
    echo "网络调优已存在（/etc/sysctl.d/99-relay-node.conf），跳过。"
    return 0
  fi
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
}

ensure_service() { # 有 systemd 服务就重启（新落地生效），没有就新建并启动；无 systemd 就后台运行
  echo ""
  echo "== 6/6 启动 realm 服务 =="
  if [ "$DRY_RUN" = "1" ]; then
    echo "[dry-run] 跳过启动服务"
    return 0
  fi
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    if [ ! -f /etc/systemd/system/realm.service ]; then
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
      systemctl daemon-reload
      systemctl restart realm
      sleep 2
      if systemctl is-active --quiet realm; then
        echo "realm 服务已重启（新落地已生效）"
      else
        echo "realm 服务重启失败，看日志：journalctl -u realm -n 50"
        exit 1
      fi
    fi
  else
    echo "本机没有 systemd，改用后台运行："
    if [ -f /var/run/realm.pid ] && kill -0 "$(cat /var/run/realm.pid)" 2>/dev/null; then
      kill "$(cat /var/run/realm.pid)" 2>/dev/null || true
      sleep 1
    fi
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

  # 分流组件也要起来（中转重启后它们也要跟上；没开分流时这两行什么都不做）
  ensure_proxies_running
}

print_summary() { # 打印当前落地的汇总（中转 IP 只查一次）
  if [ -z "${RELAY_IP:-}" ]; then
    RELAY_IP="$(curl -s --max-time 5 https://api.ipify.org || true)"
    [ -n "$RELAY_IP" ] || RELAY_IP="$(curl -s --max-time 5 https://ifconfig.me || true)"
    [ -n "$RELAY_IP" ] || RELAY_IP="<拿不到本机公网 IP>"
  fi
  echo ""
  echo "================ 中转部署完成 ================"
  echo "中转机：$RELAY_IP"
  echo "落地：  $LANDING（$MODE_NOTE）"
  echo "转发："
  local _disp_ip="$RELAY_IP"
  if [ "$RELAY_MODE" = "door" ] && [ -n "$RELAY_ADDR" ]; then _disp_ip="$RELAY_ADDR"; fi
  for _spec in $PORTS; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
    echo "  $_proto  $_disp_ip:$_lp  ->  $LANDING:$_rp"
  done
  if [ "$RELAY_MODE" = "door" ]; then
    echo "中转地址：$RELAY_ADDR（任意门链接里用的地址）"
  fi
  echo ""
  echo "下一步（只做一次）："
  if [ "$RELAY_MODE" = "door" ]; then
    if [ "$LANDING_IS_DOMAIN" = "1" ]; then
      echo "  节点域名 $DOMAIN 不用动，永远指着落地；客户端用“$RELAY_ADDR:转发端口”连过来，SNI 还是节点域名。"
    else
      echo "  落地不用动；客户端用“$RELAY_ADDR:转发端口”连过来，SNI 还是节点域名。"
    fi
    echo "  落地不用装任何东西，也不用重启；证书续签走落地直连，自动通，不用手动改指向。"
    echo "  注意：转发端口要在云厂商安全组和中转机 ufw 里放行，否则连不上。"
  elif [ "$LANDING_IS_DOMAIN" = "1" ]; then
    echo "  把域名 $DOMAIN 的 A 记录从落地 IP 改指到中转 IP $RELAY_IP，"
    echo "  等 DNS 生效后客户端配置一个字都不用改，直接就能走中转。"
  else
    echo "  客户端把地址改成中转 IP $RELAY_IP，SNI/域名保持落地时的域名不变，"
    echo "  其他（UUID、WS 路径、端口）都不用动。"
  fi
  local _is_tls=0 _has80=0 _spec
  for _spec in $PORTS; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _rp="${_rest##*:}"
    [ "$_proto" = "tcp" ] && [ "$_rp" = "443" ] && _is_tls=1
    [ "$_spec" = "tcp:80:80" ] && _has80=1
  done
  if [ "$RELAY_MODE" = "door" ]; then
    : # 任意门：续签说明已在上面“下一步”里讲了（域名指落地，续签走落地直连）
  elif [ "$_is_tls" = "1" ]; then
    if [ "$SHARE80" = "1" ]; then
      echo "  落地不用装任何东西，也不用重启；证书续签走 80 分流自动通，不用手动改指向。"
    elif [ "$SHARE443" = "1" ]; then
      echo "  落地不用装任何东西，也不用重启；证书续签走 443 分流（TLS-ALPN-01）自动通，不用手动改指向。"
    elif [ "$_has80" = "1" ]; then
      echo "  落地不用装任何东西，也不用重启；证书续签走转发的 80 端口照常能通。"
    else
      echo "  注意：这个落地的 80 端口没走中转（被占了改了监听端口），证书续签时把域名临时改回指向落地，续完再改回来。"
      echo "  具体步骤见 README / TUTORIAL 的“手动续签证书”一节；平时跑 bash check.sh 能看到各落地证书剩余天数。"
    fi
  else
    echo "  落地不用装任何东西，也不用重启。"
  fi
  echo ""
  echo "换落地目标：改 /etc/realm/config.toml 里各 remote 的地址，再 systemctl restart realm。"
  if [ "$LANDING_IS_DOMAIN" = "1" ]; then
    echo "落地域名 IP 变了：最长 5 分钟自动切到新 IP（dns.max_ttl=300），也可立即 systemctl restart realm。"
  fi
  echo ""
  echo "自检：bash check.sh（会穿过中转连落地 443，核对证书与落地一致）"
}

# ---------- 日志上限（防爆盘：docker 日志 + journald） ----------
# docker 容器日志默认无上限（tm 容器就这么长到 8.9G 撑爆过 20G 盘），
# journald 默认能用到磁盘 10%（20G 盘就是 2G）。
# 注意：daemon.json 的限制只对新建容器生效，已有容器要重建才生效；本函数不重启 docker
# （重启会断正在跑的容器），只写配置 + 打印说明。
# 与 panel-install.sh 里的 setup_log_limits 同一份逻辑（两边改动要同步）。
setup_log_limits() {
  # 1) docker：daemon.json 合并 log-driver/log-opts，不覆盖用户原有配置
  if command -v docker >/dev/null 2>&1; then
    local dj="/etc/docker/daemon.json"
    if [ -f "$dj" ] && grep -q '"max-size"' "$dj" 2>/dev/null; then
      echo "docker 日志上限已配过（$dj 里已有 max-size），不动。"
    elif [ "$DRY_RUN" = "1" ]; then
      echo "[dry-run] 会写 $dj：log-driver=json-file，log-opts max-size=10m、max-file=3（已有文件则合并，不覆盖原有配置）"
    else
      if command -v python3 >/dev/null 2>&1; then
        python3 - "$dj" <<'PYEOF' || echo "daemon.json 合并失败：请手动配，见下面说明。"
import json, os, sys
p = sys.argv[1]
existed = os.path.exists(p)
try:
    with open(p) as f:
        cfg = json.load(f)
    if not isinstance(cfg, dict):
        cfg = {}
except FileNotFoundError:
    cfg = {}
except Exception as e:
    if existed:
        print("daemon.json 不是合法 JSON，自动合并不了，请手动改：", e, file=sys.stderr)
        sys.exit(1)
    cfg = {}
cfg["log-driver"] = "json-file"
opts = cfg.get("log-opts")
if not isinstance(opts, dict):
    opts = {}
opts["max-size"] = "10m"
opts["max-file"] = "3"
cfg["log-opts"] = opts
with open(p, "w") as f:
    json.dump(cfg, f, indent=2)
    f.write("\n")
PYEOF
      elif command -v jq >/dev/null 2>&1; then
        _old="$(cat "$dj" 2>/dev/null || echo '{}')"
        _new="$(printf '%s' "$_old" | jq '.["log-driver"]="json-file" | .["log-opts"]=(.["log-opts"] // {}) | .["log-opts"]["max-size"]="10m" | .["log-opts"]["max-file"]="3"' 2>/dev/null || true)"
        if [ -n "$_new" ]; then printf '%s\n' "$_new" > "$dj"; else echo "daemon.json 合并失败：请手动配，见下面说明。"; fi
      else
        # 既没 python3 也没 jq：只有文件不存在或为空对象时才敢写，有内容的不动（免得改坏 JSON）
        if [ ! -f "$dj" ] || [ ! -s "$dj" ]; then
          printf '{\n  "log-driver": "json-file",\n  "log-opts": {\n    "max-size": "10m",\n    "max-file": "3"\n  }\n}\n' > "$dj"
        else
          echo "daemon.json 已存在且本机没有 python3/jq，自动合并不了：请手动加 log-driver/log-opts，见下面说明。"
        fi
      fi
      if grep -q '"max-size"' "$dj" 2>/dev/null; then
        echo "docker 日志上限已写好（$dj：每个容器日志最多 10m x 3 份）。"
      fi
    fi
  else
    echo "本机没有 docker，跳过 docker 日志上限。"
  fi
  # 2) journald：SystemMaxUse=100M（没配过才加，已配的不动），并立即腾出超出的旧日志
  if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
    local jc="/etc/systemd/journald.conf"
    if grep -Eq '^[[:space:]]*SystemMaxUse=' "$jc" 2>/dev/null; then
      echo "journald 上限已配过（$jc 里已有 SystemMaxUse），不动。"
    elif [ "$DRY_RUN" = "1" ]; then
      echo "[dry-run] 会在 $jc 末尾加 [Journal] SystemMaxUse=100M，并重启 systemd-journald、跑 journalctl --vacuum-size=100M"
    else
      printf '\n[Journal]\nSystemMaxUse=100M\n' >> "$jc"
      systemctl restart systemd-journald 2>/dev/null || true
      journalctl --vacuum-size=100M >/dev/null 2>&1 || true
      echo "journald 上限已设为 100M（旧日志已按 100M 清过一次）。"
    fi
  else
    echo "本机没有 systemd，跳过 journald 上限。"
  fi
  echo "说明：docker 日志上限只对新建容器生效，已有容器要重建才限流（本脚本不重启 docker，免得断正在跑的容器）。"
  echo "  compose 管的容器：在安装目录跑 docker compose down && docker compose up -d"
  echo "  单个容器：docker rm -f <名字> 后用原来的 docker run 命令重建"
  echo "  检查各容器日志占了多少：docker system df -v | grep -A2 Containers，或 du -sh /var/lib/docker/containers/* | sort -rh | head"
}

# ---------- 主流程 ----------
LANDING_COUNT=1
ALL_PORTS=""
RELAY_IP=""
if [ "$ADD_MODE" = "1" ]; then
  if [ ! -f /etc/realm/relay.conf ] || [ ! -f /etc/realm/config.toml ]; then
    echo "没找到已装好的中转（/etc/realm/relay.conf 或 config.toml 缺失），先正常跑一次安装（bash install.sh），再跑 bash install.sh --add。"
    exit 1
  fi
  # 先留住命令行给的落地/预设（下面读 relay.conf 会覆盖它们，读完再还回去）
  _cli_landing="${LANDING:-}"; _cli_preset="${PRESET:-}"; _cli_cert_domain="${CERT_DOMAIN:-}"
  _cli_relay_mode="${RELAY_MODE:-}"; _cli_relay_addr="${RELAY_ADDR:-}"
  # shellcheck disable=SC1091
  . /etc/realm/relay.conf
  LANDING_COUNT="${LANDING_COUNT:-1}"
  ALL_PORTS="${ALL_PORTS:-${PORTS:-}}"
  SHARE80="${SHARE80:-0}"
  SHARE443="${SHARE443:-0}"
  echo "追加模式：当前已有 $LANDING_COUNT 个落地，继续加一个（不覆盖已有配置）。"
  LANDING="$_cli_landing"; PRESET="$_cli_preset"; CERT_DOMAIN="$_cli_cert_domain"
  # RELAY_MODE/RELAY_ADDR 只在命令行/环境变量给了新值时才覆盖 relay.conf 里的旧值（没给就沿用旧值）
  [ -n "$_cli_relay_mode" ] && RELAY_MODE="$_cli_relay_mode"
  [ -n "$_cli_relay_addr" ] && RELAY_ADDR="$_cli_relay_addr"
  # 注意：SB_MODE/CUSTOM_PORTS/WANT_UDP 不在这里清空——面板非交互调用时靠环境变量预设这三项，
  # 清空会把面板传的值丢掉（collect_landing 里 ask() 会跳过已预设的值）；relay.conf 里也没有这三项，不用防泄漏
  PORTS=""; MODE_NOTE=""; DOMAIN=""; LANDING_IS_DOMAIN=0; SHARE_ASKED=0
fi

echo "一共 7 步（前 6 步装中转，第 7 步生成双节点链接，可跳过）。每一步填完按回车继续；标了“默认”的，直接回车就是默认值。"
echo "装完第 1 个落地后会问要不要再加一个落地节点，一台中转机可以转多个落地（事后追加：bash install.sh --add）。"
echo "加第 2 个落地、80 或 443 被已装好的落地占了时，会让你选解决方式：默认改监听走高位端口，也可以选 80 分流 / 443 分流（续签自动）。"
echo ""

declare -a ADDED_LANDING=() ADDED_PORTS=() ADDED_MODE=() ADDED_DOMAIN=() ADDED_INDEX=() ADDED_RELAY_MODE=()
FIRST=1
ADD_ANOTHER=""
while true; do
  if [ "$FIRST" = "1" ] && [ "$ADD_MODE" = "0" ]; then
    collect_landing "1/6 落地目标与转发预设"
    precheck_ports "2/6 端口预检（中转监听端口）"
    setup_realm_once
    write_config_fresh
    setup_sysctl_once
    ensure_service
  else
    collect_landing "追加落地 $((LANDING_COUNT + 1))：落地目标与转发预设"
    precheck_ports "追加落地 $((LANDING_COUNT + 1)) 端口预检"
    setup_realm_once
    append_config
    setup_sysctl_once
    ensure_service
  fi
  print_summary
  ADDED_LANDING+=("$LANDING")
  ADDED_PORTS+=("$PORTS")
  ADDED_MODE+=("$MODE_NOTE")
  ADDED_DOMAIN+=("$DOMAIN")
  ADDED_INDEX+=("$LANDING_COUNT")
  ADDED_RELAY_MODE+=("$RELAY_MODE")
  FIRST=0
  echo ""
  echo "请输入你的选择："
  ADD_ANOTHER=""
  ask "还要再加一个落地节点吗？[y/N]，直接回车选默认 N： " ADD_ANOTHER "N"
  if [ "$ADD_ANOTHER" = "y" ] || [ "$ADD_ANOTHER" = "Y" ]; then
    LANDING=""; PRESET=""; SB_MODE=""; CUSTOM_PORTS=""; WANT_UDP=""; PORTS=""; MODE_NOTE=""; DOMAIN=""; LANDING_IS_DOMAIN=0; CHANGE_LISTEN=""; CERT_DOMAIN=""; SHARE_ASKED=0; RELAY_MODE=""
    # RELAY_ADDR 不清空：同一台中转机上的落地共用一个中转地址
    continue
  fi
  break
done

# ---------- 日志上限（防爆盘：docker 日志 + journald；重跑/追加也走同一逻辑） ----------
echo ""
echo "设置日志上限（防爆盘）..."
setup_log_limits

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

# ---------- 第 7 步：双节点链接（每个新增落地各跑一次） ----------
mkdir -p "$HOME/relay-node-kit" 2>/dev/null || true
{
  echo "# relay-node 双节点（$(date -u +%Y-%m-%dT%H:%M:%SZ)）"
  echo "# 中转：原链接按实际监听端口走（监听端口改过时中转链接端口跟着改）；直连：地址换成落地 IP + sni=域名"
  echo "# 任意门：地址换成中转地址、端口换成转发端口；直连：地址换成落地 IP + sni=域名"
} > "$HOME/relay-node-kit/nodes.txt" 2>/dev/null || true
_n=0
for _k in "${!ADDED_LANDING[@]}"; do
  _n=$((_n + 1))
  LANDING="${ADDED_LANDING[$_k]}"
  PORTS="${ADDED_PORTS[$_k]}"
  MODE_NOTE="${ADDED_MODE[$_k]}"
  DOMAIN="${ADDED_DOMAIN[$_k]}"
  STEP7_INDEX="${ADDED_INDEX[$_k]}"
  RELAY_MODE="${ADDED_RELAY_MODE[$_k]}"
  step7_nodes || true
done

# ---------- 最后一步：可视化面板（只在全新安装结束时问一次；--add/--nodes/--twin 不问） ----------
if [ "$ADD_MODE" = "0" ] && [ "$NODES_ONLY" = "0" ] && [ "$TWIN_MODE" = "0" ] && [ "$DRY_RUN" != "1" ]; then
  if [ -x /usr/local/bin/relay-panel ]; then
    echo ""
    echo "可视化面板已装好（/usr/local/bin/relay-panel），SSH 跑 panel-info 可看地址和账号。"
  else
    echo ""
    echo "请输入你的选择："
    PANEL_INSTALL=""
    ask "要不要装可视化面板？[y/N]，直接回车选默认 N（浏览器里点几下就能管中转，不装也不影响转发）： " PANEL_INSTALL "N"
    if [ "$PANEL_INSTALL" = "y" ] || [ "$PANEL_INSTALL" = "Y" ]; then
      if [ ! -f panel-install.sh ]; then
        curl -fsSL --max-time 30 -o panel-install.sh "$REPO_RAW/panel-install.sh" 2>/dev/null || true
        chmod +x panel-install.sh 2>/dev/null || true
      fi
      if [ -f panel-install.sh ]; then
        echo "正在装面板..."
        bash panel-install.sh || echo "面板没装上（不影响中转），以后随时跑 bash panel-install.sh 再装。"
      else
        echo "没拉到 panel-install.sh（网络问题），以后随时跑下面这条命令装面板："
        echo "  curl -fsSL $REPO_RAW/panel-install.sh | bash"
      fi
    fi
  fi
fi
