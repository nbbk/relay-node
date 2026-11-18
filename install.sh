#!/usr/bin/env bash
# 中转节点一键安装：realm 纯转发（客户端 -> 中转机 -> 落地机）
# 用法：
#   本地：bash install.sh [落地IP或域名] [预设 1/2/3]
#   远程一键：curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/install.sh | bash
#   只补生成双节点链接：bash install.sh --nodes（读 /etc/realm/relay.conf，不重装）
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

# 只跑第 7 步：bash install.sh --nodes（下面解析到 --nodes 后跳过 1-6 步）
NODES_ONLY=0
if [ "${1:-}" = "--nodes" ]; then
  NODES_ONLY=1
  shift
fi
SNI_ASKED=0

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
  echo ""
  echo "== 7/7 双节点链接（直连 + 中转） =="
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
  echo "粘贴你原来的节点链接（可多条，一行一条，粘完空行回车结束；直接回车跳过）："
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
    echo "# relay-node 双节点（$(date -u +%Y-%m-%dT%H:%M:%SZ)）"
    echo "# 落地：$LANDING（${MODE_NOTE:-}）"
    echo "# 中转：原链接不动，域名改指到中转机后自动走中转；直连：地址换成落地 IP $landing_ip + sni=域名"
  } > "$nodes_file" 2>/dev/null || nodes_file="/dev/null"
  local -a ORIGS=() TWINS=()
  local i=0 _orig="" _sni="" _out="" _twin="" _reason="" _scheme_l=""
  for _orig in "${links[@]}"; do
    i=$((i + 1))
    _sni="$(link_sni_domain "$_orig" || true)"
    _scheme_l="$(printf '%s' "${_orig%%://*}" | tr '[:upper:]' '[:lower:]')"
    if [ -z "$_sni" ] && [ "$SNI_ASKED" != "1" ]; then
      case "$_scheme_l" in
        vless|trojan|vmess)
          SNI_ASKED=1
          ask "第 $i 条链接里没找到域名，直连要用的域名（SNI，就是证书上的那个域名；不知道就直接回车）: " SNI_DOMAIN
          _sni="${SNI_DOMAIN:-}"
          ;;
      esac
    fi
    echo ""
    echo "---- 第 $i 条 ----"
    echo "中转：$_orig"
    _twin=""
    if _out="$(twin_uri "$_orig" "$landing_ip" "$_sni" 2>/dev/null)"; then
      _twin="$_out"
      echo "直连：$_twin"
    else
      _reason="${_out#__MANUAL__:}"
      [ -n "$_reason" ] || _reason="这条不会自动生成"
      echo "直连：$_reason，请手动改地址（把地址换成 $landing_ip，TLS 的 SNI 填域名）。"
    fi
    ORIGS+=("$_orig")
    TWINS+=("$_twin")
    {
      echo ""
      echo "[第 $i 条]"
      echo "中转：$_orig"
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
      echo "中转二维码（第 $n 条）："
      qrencode -t ANSIUTF8 "${ORIGS[$j]}" 2>/dev/null || echo "（这条二维码生成失败，复制链接导入）"
      qrencode -o "$nodes_dir/nodes-relay-$n.png" "${ORIGS[$j]}" 2>/dev/null || true
      if [ -n "${TWINS[$j]}" ]; then
        echo "直连二维码（第 $n 条）："
        qrencode -t ANSIUTF8 "${TWINS[$j]}" 2>/dev/null || echo "（这条二维码生成失败，复制链接导入）"
        qrencode -o "$nodes_dir/nodes-direct-$n.png" "${TWINS[$j]}" 2>/dev/null || true
      fi
    done
    echo ""
    echo "二维码 PNG 已存到 $nodes_dir/nodes-relay-N.png、nodes-direct-N.png"
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
  step7_nodes
  exit 0
fi

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

# ---------- 第 7 步：双节点链接（可选，跳过不报错） ----------
step7_nodes || true
