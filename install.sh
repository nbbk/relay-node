#!/usr/bin/env bash
# 中转节点一键安装：realm 纯转发（客户端 -> 中转机 -> 落地机）
# 用法：
#   本地：bash install.sh [落地IP或域名] [预设 1/2/3]
#   远程一键：curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/install.sh | bash
#   只补生成双节点链接：bash install.sh --nodes（读 /etc/realm/relay.conf，不重装）
#   追加一个落地节点：bash install.sh --add（不覆盖已有配置，直接往后加）
#   （不给参数则交互询问；落地不用装任何东西，转发目标就是落地现成的端口）
# 多落地：一台中转机可以转多个落地节点（装完第 1 个后会问要不要再加一个，
#        或事后跑 bash install.sh --add）。每个落地各有一组转发端口，监听端口
#        不能撞：被占就改监听端口（只改中转这边，落地端口不变），建议值是高位
#        端口（原端口+10000，如 443→10443），客户端链接端口跟着改。
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
SNI_ASKED=0
STEP7_INDEX=1

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

link_port() { # $1=节点链接 -> 打印端口号（vless/trojan/ss SIP002/vmess），取不到返回 1
  local link="$1"
  local scheme="${link%%://*}"
  scheme="$(printf '%s' "$scheme" | tr '[:upper:]' '[:lower:]')"
  local rest="${link#*://}"
  local core="${rest%%#*}"
  core="${core%%\?*}" # 先去掉 query，不然 host:port 后面会粘着一串参数
  case "$scheme" in
    vless|trojan)
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
    vless|trojan)
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

frag_direct() { # $1=frag $2=原来有没有 fragment(0/1) -> 加 -直连 后缀的新 frag（已有不再重复加）
  local frag="$1" has="$2"
  if [ "$has" = "1" ]; then
    case "$frag" in
      *"%E7%9B%B4%E8%BF%9E"*|*"直连"*) printf '%s' "$frag" ;;
      *) printf '%s' "$frag-%E7%9B%B4%E8%BF%9E" ;;
    esac
  else
    printf '%s' "%E7%9B%B4%E8%BF%9E"
  fi
}

twin_plain() { # vless/trojan：换地址 host，加/改 sni，名字加 -直连
  local link="$1" lip="$2" sni="$3"
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
  frag="$(frag_direct "$frag" "$has_frag")"
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

twin_ss() { # ss：只换地址 host，不加 sni；SIP002 直接换，传统 base64 形式走 python3 解码
  local link="$1" lip="$2"
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
  frag="$(frag_direct "$frag" "$has_frag")"
  local out="ss://$out_core"
  [ "$has_query" = "1" ] && out+="?$query"
  out+="#$frag"
  printf '%s' "$out"
}

vmess_twin() { # vmess：python3 解 base64 JSON，改 add=落地IP、sni=域名、ps 加 -直连
  local b64="$1" lip="$2" sni="$3"
  if ! command -v python3 >/dev/null 2>&1; then
    echo "__MANUAL__:vmess 链接需要 python3 解码"
    return 1
  fi
  local out=""
  out="$(python3 - "$b64" "$lip" "$sni" <<'PYEOF' 2>/dev/null || true
import sys, json, base64
b64, lip, sni = sys.argv[1], sys.argv[2], sys.argv[3]
b64 += '=' * (-len(b64) % 4)
try:
    j = json.loads(base64.urlsafe_b64decode(b64))
except Exception:
    sys.exit(1)
j['add'] = lip
if sni:
    j['sni'] = sni
ps = j.get('ps') or 'vmess'
if '直连' not in ps:
    j['ps'] = ps + '-直连'
print('vmess://' + base64.b64encode(json.dumps(j, ensure_ascii=False, separators=(',', ':')).encode()).decode())
PYEOF
)"
  if [ -z "$out" ]; then
    echo "__MANUAL__:vmess 链接解码失败"
    return 1
  fi
  printf '%s' "$out"
}

twin_uri() { # $1=link $2=落地IP $3=sni域名 -> echo twin；失败 echo __MANUAL__:原因 并 return 1
  local link="$1" lip="$2" sni="$3"
  local scheme="${link%%://*}"
  scheme="$(printf '%s' "$scheme" | tr '[:upper:]' '[:lower:]')"
  case "$scheme" in
    vless|trojan)
      twin_plain "$link" "$lip" "$sni"
      ;;
    ss)
      twin_ss "$link" "$lip"
      ;;
    vmess)
      local b64="${link#*://}"
      b64="${b64%%#*}"
      vmess_twin "$b64" "$lip" "$sni"
      ;;
    *)
      echo "__MANUAL__:不识别的链接格式"
      return 1
      ;;
  esac
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
    vless|trojan)
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
  # 依赖全局变量：LANDING / PORTS / MODE_NOTE / STEP7_INDEX；nodes.txt 由调用方先截断一次，这里只追加
  echo ""
  echo "== 7/7 双节点链接（直连 + 中转）——落地 $STEP7_INDEX：$LANDING =="
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
  elif [ -r /dev/tty ] && exec 3</dev/tty 2>/dev/null; then
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
    echo "# 中转：原链接按实际监听端口走（监听端口改过时中转链接端口跟着改）；直连：地址换成落地 IP $landing_ip + sni=域名"
  } >> "$nodes_file" 2>/dev/null || nodes_file="/dev/null"
  local -a ORIGS=() TWINS=()
  local i=0 _orig="" _sni="" _out="" _twin="" _relay="" _orig_port="" _listen_port="" _reason="" _scheme_l=""
  for _orig in "${links[@]}"; do
    i=$((i + 1))
    _sni="$(link_sni_domain "$_orig" || true)"
    _scheme_l="$(printf '%s' "${_orig%%://*}" | tr '[:upper:]' '[:lower:]')"
    if [ -z "$_sni" ] && [ "$SNI_ASKED" != "1" ]; then
      case "$_scheme_l" in
        vless|trojan|vmess)
          SNI_ASKED=1
          echo "请填写直连要用的域名（SNI）："
          ask "第 $i 条链接里没找到域名，直连要用的域名（SNI，就是证书上的那个域名；不知道就直接回车）: " SNI_DOMAIN
          _sni="${SNI_DOMAIN:-}"
          ;;
      esac
    fi
    # 中转链接：端口换成实际监听端口（监听≠落地端口时才换；直连那条保持落地端口不变）
    _relay="$_orig"
    _orig_port="$(link_port "$_orig" || true)"
    _listen_port=""
    if [ -n "$_orig_port" ]; then
      _listen_port="$(listen_for_remote "$PORTS" "$_orig_port" || true)"
      if [ -n "$_listen_port" ] && [ "$_listen_port" != "$_orig_port" ]; then
        _relay="$(set_link_port "$_orig" "$_listen_port")"
      fi
    fi
    echo ""
    echo "---- 第 $i 条 ----"
    echo "中转：$_relay"
    if [ -n "$_listen_port" ] && [ "$_listen_port" != "$_orig_port" ]; then
      echo "（中转监听端口是 $_listen_port，原链接端口 $_orig_port 已换成监听端口；直连仍走落地端口 $_orig_port）"
    elif [ -n "$_orig_port" ] && [ -z "$_listen_port" ]; then
      echo "（注意：这条链接的端口 $_orig_port 不在本次转发表里，中转可能不通，请确认端口对不对）"
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
      echo "中转：$_relay"
      [ -n "$_twin" ] && echo "直连：$_twin"
    } >> "$nodes_file" 2>/dev/null || true
  done
  echo ""
  echo "两条链接已存到 $nodes_file"
  if try_install_qrencode; then
    local j=0 n=0
    for j in "${!ORIGS[@]}"; do
      n=$((j + 1))
      echo ""
      echo "中转二维码（落地 $STEP7_INDEX 第 $n 条）："
      qrencode -t ANSIUTF8 "${ORIGS[$j]}" 2>/dev/null || echo "（这条二维码生成失败，复制链接导入）"
      qrencode -o "$nodes_dir/nodes-L${STEP7_INDEX}-relay-$n.png" "${ORIGS[$j]}" 2>/dev/null || true
      if [ -n "${TWINS[$j]}" ]; then
        echo "直连二维码（落地 $STEP7_INDEX 第 $n 条）："
        qrencode -t ANSIUTF8 "${TWINS[$j]}" 2>/dev/null || echo "（这条二维码生成失败，复制链接导入）"
        qrencode -o "$nodes_dir/nodes-L${STEP7_INDEX}-direct-$n.png" "${TWINS[$j]}" 2>/dev/null || true
      fi
    done
    echo ""
    echo "二维码 PNG 已存到 $nodes_dir/nodes-L${STEP7_INDEX}-relay-N.png、nodes-L${STEP7_INDEX}-direct-N.png"
  else
    echo "没有 qrencode（也没装上），复制上面两条链接导入即可；想看二维码可装好 qrencode 后再跑 bash install.sh --nodes。"
  fi
  return 0
}

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
  # nodes.txt 本次运行先截断一次，各落地按分组追加（不互相覆盖）
  mkdir -p "$HOME/relay-node-kit" 2>/dev/null || true
  {
    echo "# relay-node 双节点（$(date -u +%Y-%m-%dT%H:%M:%SZ)）"
    echo "# 中转：原链接按实际监听端口走（监听端口改过时中转链接端口跟着改）；直连：地址换成落地 IP + sni=域名"
  } > "$HOME/relay-node-kit/nodes.txt" 2>/dev/null || true
  _count="${LANDING_COUNT:-1}"
  _idx=1
  while [ "$_idx" -le "$_count" ]; do
    if [ "$_idx" = "1" ]; then
      LANDING="${LANDING:-}"; PORTS="${PORTS:-}"; MODE_NOTE="${MODE_NOTE:-}"; DOMAIN="${DOMAIN:-}"
    else
      LANDING="$(landing_field "$_idx" LANDING)"
      PORTS="$(landing_field "$_idx" PORTS)"
      MODE_NOTE="$(landing_field "$_idx" MODE_NOTE)"
      DOMAIN="$(landing_field "$_idx" DOMAIN)"
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
  exit 0
fi


# ---------- 主流程函数（一个落地一轮：收集 -> 预检 -> 写配置/追加 -> 启动/重启 -> 汇总） ----------
collect_landing() { # $1=标题（如 "1/6 落地目标与转发预设"），收集一个落地的 LANDING/PRESET/PORTS/MODE_NOTE
  echo ""
  echo "== $1 =="
  echo "这一步要填两样：① 落地服务器的 IP 或域名 ② 转发预设（选 1/2/3）。"
  if [ -z "$LANDING" ]; then
    echo "请填写你的落地 IP 或域名："
    ask "① 落地服务器 IP（推荐填 IP，例如 202.155.138.195）或域名，填完按回车： " LANDING
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
    echo "  1) tiktok-node（Xray）：80 + 443（TCP）  ← 默认，直接回车就是 1"
    echo "  2) sing-box-node：80 + 443（TCP）+ 8388（TCP+UDP）+ 8443（UDP）+ 8444（UDP）"
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
      MODE_NOTE="tiktok-node（Xray）"
      ;;
    2)
      echo "  1) TLS 模式（443）  ← 默认，直接回车就是 1"
      echo "  2) 明文模式（8080-8082）"
      echo "请输入你的选择："
      ask "落地 sing-box-node 是 TLS 还是明文？[1/2]，直接回车选默认 1（TLS）： " SB_MODE "1"
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
  echo "预设：$MODE_NOTE"
  echo "转发：$PORTS"
}

precheck_ports() { # $1=标题（如 "2/6 端口预检（中转监听端口）"）：查 $PORTS 的监听端口
  # 同时查系统已占端口和 config.toml 里已配好的 realm 监听端口（追加的落地不能和已有的抢端口）
  # 被占就问要不要改监听端口（只改中转这边，落地端口不变）；建议值是高位端口（原端口+10000）
  echo ""
  echo "== $1 =="
  local _occupied="" _spec _proto _rest _lp _rp
  while true; do
    _occupied=""
    for _spec in $PORTS; do
      _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"
      if port_in_use "$_proto" "$_lp" || port_in_config "$_lp"; then
        case " $_occupied " in *" $_lp/$_proto "*) ;; *) _occupied="$_occupied $_lp/$_proto";; esac
      fi
    done
    _occupied="${_occupied# }"
    if [ -z "$_occupied" ]; then
      echo "监听端口全部空闲。"
      return 0
    fi
    echo "端口已被占用：$_occupied"
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
}

write_config_fresh() { # 第 1 个落地：全新写 config.toml 与 relay.conf
  echo ""
  echo "== 4/6 写 realm 配置 =="
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
  cat > /etc/realm/relay.conf <<EOF
LANDING=$LANDING
LANDING_IS_DOMAIN=$LANDING_IS_DOMAIN
DOMAIN="$DOMAIN"
PRESET=$PRESET
MODE_NOTE="$MODE_NOTE"
PORTS="$PORTS"
REALM_VERSION="$REALM_VERSION"
LANDING_COUNT=1
ALL_PORTS="$PORTS"
EOF
  LANDING_COUNT=1
  ALL_PORTS="$PORTS"
  echo "配置已写：/etc/realm/config.toml"
}

append_config() { # 再加一个落地：只往 config.toml 后追加 endpoints 段，不重写头部；更新 relay.conf
  local _idx=$((LANDING_COUNT + 1))
  local _spec _proto _rest _lp _rp
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
  for _spec in $PORTS; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _lp="${_rest%%:*}"; _rp="${_rest##*:}"
    cat >> /etc/realm/config.toml <<EOF

[[endpoints]]
listen = "0.0.0.0:$_lp"
remote = "$LANDING:$_rp"
EOF
  done
  # relay.conf：追加编号块，更新 LANDING_COUNT 与 ALL_PORTS（老 relay.conf 没有这两行也能补上）
  {
    echo "LANDING_$_idx=$LANDING"
    echo "LANDING_IS_DOMAIN_$_idx=$LANDING_IS_DOMAIN"
    echo "DOMAIN_$_idx=\"$DOMAIN\""
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
  local _is_tls=0 _has80=0 _spec
  for _spec in $PORTS; do
    _proto="${_spec%%:*}"; _rest="${_spec#*:}"; _rp="${_rest##*:}"
    [ "$_proto" = "tcp" ] && [ "$_rp" = "443" ] && _is_tls=1
    [ "$_spec" = "tcp:80:80" ] && _has80=1
  done
  if [ "$_is_tls" = "1" ]; then
    if [ "$_has80" = "1" ]; then
      echo "  落地不用装任何东西，也不用重启；证书续签走转发的 80 端口照常能通。"
    else
      echo "  注意：这个落地的 80 端口没走中转（被占了改了监听端口），证书续签时把域名临时改回指向落地，续完再改回来。"
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
  _cli_landing="${LANDING:-}"; _cli_preset="${PRESET:-}"
  # shellcheck disable=SC1091
  . /etc/realm/relay.conf
  LANDING_COUNT="${LANDING_COUNT:-1}"
  ALL_PORTS="${ALL_PORTS:-${PORTS:-}}"
  echo "追加模式：当前已有 $LANDING_COUNT 个落地，继续加一个（不覆盖已有配置）。"
  LANDING="$_cli_landing"; PRESET="$_cli_preset"
  SB_MODE=""; CUSTOM_PORTS=""; WANT_UDP=""; PORTS=""; MODE_NOTE=""; DOMAIN=""; LANDING_IS_DOMAIN=0
fi

echo "一共 7 步（前 6 步装中转，第 7 步生成双节点链接，可跳过）。每一步填完按回车继续；标了“默认”的，直接回车就是默认值。"
echo "装完第 1 个落地后会问要不要再加一个落地节点，一台中转机可以转多个落地（事后追加：bash install.sh --add）。"
echo ""

declare -a ADDED_LANDING=() ADDED_PORTS=() ADDED_MODE=() ADDED_DOMAIN=() ADDED_INDEX=()
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
  FIRST=0
  echo ""
  echo "请输入你的选择："
  ADD_ANOTHER=""
  ask "还要再加一个落地节点吗？[y/N]，直接回车选默认 N： " ADD_ANOTHER "N"
  if [ "$ADD_ANOTHER" = "y" ] || [ "$ADD_ANOTHER" = "Y" ]; then
    LANDING=""; PRESET=""; SB_MODE=""; CUSTOM_PORTS=""; WANT_UDP=""; PORTS=""; MODE_NOTE=""; DOMAIN=""; LANDING_IS_DOMAIN=0; CHANGE_LISTEN=""
    continue
  fi
  break
done

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
} > "$HOME/relay-node-kit/nodes.txt" 2>/dev/null || true
_n=0
for _k in "${!ADDED_LANDING[@]}"; do
  _n=$((_n + 1))
  LANDING="${ADDED_LANDING[$_k]}"
  PORTS="${ADDED_PORTS[$_k]}"
  MODE_NOTE="${ADDED_MODE[$_k]}"
  DOMAIN="${ADDED_DOMAIN[$_k]}"
  STEP7_INDEX="${ADDED_INDEX[$_k]}"
  step7_nodes || true
done
