#!/usr/bin/env bash
# relay-node 可视化面板一键安装（现场编译，不依赖预编译二进制）
#   新装：curl -fsSL https://raw.githubusercontent.com/nbbk/relay-node/main/panel-install.sh | bash
#   本地：bash panel-install.sh
#   升级：重跑安装命令，账号密码与配置保留，只换二进制并重启面板（不动 realm，转发不断线）
#   卸载：bash panel-install.sh --remove
# 安装时问账号密码：直接回车用默认（用户名 admin、随机强密码），输入自己的就用输入的。
# 装完最后显示一次全部信息（地址、端口、账号、密码）；忘了跑 panel-info 找回。
set -uo pipefail

REPO_RAW="${REPO_RAW:-https://raw.githubusercontent.com/nbbk/relay-node/main}"
REPO_TARBALL="${REPO_TARBALL:-https://codeload.github.com/nbbk/relay-node/tar.gz/refs/heads/main}"
PANEL_HOME="${PANEL_HOME:-/etc/relay-panel}"
BIN="/usr/local/bin/relay-panel"
KIT_DIR="${KIT_DIR:-/usr/local/share/relay-node}"
PANEL_PORT_DEFAULT="${PANEL_PORT:-11111}"
DRY_RUN="${DRY_RUN:-0}"
REMOVE=0
[ "${1:-}" = "--remove" ] && REMOVE=1

ask() { # $1=提示 $2=变量名 $3=默认值；已通过环境变量预设的值不再询问
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

ask_secret() { # $1=提示 $2=变量名；不回显；空输入返回空（调用方再定默认值）
  local prompt="$1" varname="$2"
  if [ -n "${!varname:-}" ]; then return 0; fi
  local ans=""
  if [ -t 0 ]; then
    read -rsp "$prompt" ans || true; echo ""
  elif [ -r /dev/tty ]; then
    { read -rsp "$prompt" ans </dev/tty || true; } 2>/dev/null; echo "" >&2
  fi
  printf -v "$varname" '%s' "$ans"
}

rand_password() {
  tr -dc 'A-Za-z0-9' </dev/urandom 2>/dev/null | head -c 16
  echo ""
}

public_ip() {
  local ip=""
  ip="$(curl -s --max-time 5 https://api.ipify.org 2>/dev/null || true)"
  [ -n "$ip" ] || ip="$(curl -s --max-time 5 https://ifconfig.me 2>/dev/null || true)"
  [ -n "$ip" ] || ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [ -n "$ip" ] || ip="<本机公网 IP>"
  printf '%s' "$ip"
}

# ---------- 卸载 ----------
if [ "$REMOVE" = "1" ]; then
  echo "卸载可视化面板（不动 realm 转发）..."
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl stop relay-panel 2>/dev/null || true
    systemctl disable relay-panel 2>/dev/null || true
    echo "  已停面板服务"
  fi
  if pgrep -x relay-panel >/dev/null 2>&1; then
    pkill -x relay-panel 2>/dev/null || true
    sleep 1
    pgrep -x relay-panel >/dev/null 2>&1 && pkill -9 -x relay-panel 2>/dev/null || true
    echo "  已结束面板进程"
  fi
  [ -f /etc/systemd/system/relay-panel.service ] && rm -f /etc/systemd/system/relay-panel.service && systemctl daemon-reload 2>/dev/null || true
  [ -x "$BIN" ] && rm -f "$BIN" && echo "  已删除 $BIN"
  [ -f /usr/local/bin/panel-info ] && rm -f /usr/local/bin/panel-info && echo "  已删除 /usr/local/bin/panel-info"
  # 面板数据（账号、配置、流量累计）默认保留，免得手滑丢了；想连数据一起删就选 y
  PANEL_REMOVE_DATA="${PANEL_REMOVE_DATA:-}"
  if [ -z "$PANEL_REMOVE_DATA" ]; then
    ask "面板数据（账号、配置、流量累计）要不要一起删？[y/N]，直接回车选默认 N（保留）： " PANEL_REMOVE_DATA "N"
  fi
  if [ "$PANEL_REMOVE_DATA" = "y" ] || [ "$PANEL_REMOVE_DATA" = "Y" ]; then
    rm -rf "$PANEL_HOME"
    echo "  已删除 $PANEL_HOME（面板数据）"
  else
    echo "  面板数据保留在 $PANEL_HOME，重装面板时账号密码还在。"
  fi
  echo "面板已卸载（中转转发不受影响）。"
  echo "注意：ufw 放行面板端口的规则不会自动删，如需清理：ufw status numbered，ufw delete <编号>"
  exit 0
fi

# ---------- 安装 ----------
[ "$(id -u)" = "0" ] || { echo "请用 root 跑（面板要改 /etc/realm、管 systemd）：sudo bash panel-install.sh"; exit 1; }

echo "安装 relay-node 可视化面板..."
echo "（现场编译：下载源码 -> 装 Go -> 编译出单二进制；不需要预编译好的二进制文件）"

# 1. 下载源码包
SRC_DIR="$(mktemp -d)"
trap 'rm -rf "$SRC_DIR"' EXIT
if [ "$DRY_RUN" = "1" ]; then
  echo "[dry-run] 跳过下载源码包（$REPO_TARBALL）"
else
  echo "下载源码包..."
  if ! curl -fsSL --max-time 60 -o "$SRC_DIR/repo.tar.gz" "$REPO_TARBALL" 2>/dev/null; then
    if command -v git >/dev/null 2>&1 && git clone --depth 1 https://github.com/nbbk/relay-node.git "$SRC_DIR/repo" 2>/dev/null; then
      echo "tarball 下载失败，已用 git clone 拿到源码。"
    else
      echo "源码下载失败：请检查本机能否访问 github.com，或手动下载后重跑。"
      exit 1
    fi
  else
    tar xzf "$SRC_DIR/repo.tar.gz" -C "$SRC_DIR" 2>/dev/null || { echo "源码包解压失败。"; exit 1; }
    mv "$SRC_DIR"/relay-node-main "$SRC_DIR/repo" 2>/dev/null || true
  fi
  [ -d "$SRC_DIR/repo/panel" ] || { echo "源码包里没有 panel/ 目录，下载可能不对。"; exit 1; }
fi

# 2. 装 Go（没有才装）
if command -v go >/dev/null 2>&1; then
  echo "Go 已有：$(go version)"
else
  echo "本机没有 Go，正在装..."
  if [ "$DRY_RUN" = "1" ]; then
    echo "[dry-run] 跳过装 Go"
  elif command -v apt-get >/dev/null 2>&1; then
    (apt-get update -qq 2>/dev/null || true) && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq golang-go 2>/dev/null || true
  fi
  if ! command -v go >/dev/null 2>&1; then
    # apt 没装上：直接下官方包
    GO_VER="1.23.0"
    ARCH="$(uname -m)"
    case "$ARCH" in
      x86_64|amd64) GO_ARCH="amd64" ;;
      aarch64|arm64) GO_ARCH="arm64" ;;
      *) echo "不支持的架构：$ARCH（Go 官方只提供 amd64 / arm64 的 Linux 包）"; exit 1 ;;
    esac
    echo "apt 没装上 Go，改下官方包 go$GO_VER..."
    curl -fsSL --max-time 120 -o "$SRC_DIR/go.tar.gz" "https://go.dev/dl/go$GO_VER.linux-$GO_ARCH.tar.gz" 2>/dev/null \
      || curl -fsSL --max-time 120 -o "$SRC_DIR/go.tar.gz" "https://dl.google.com/go/go$GO_VER.linux-$GO_ARCH.tar.gz" 2>/dev/null \
      || { echo "Go 下载失败：请检查网络，或手动装好 Go 再跑。"; exit 1; }
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$SRC_DIR/go.tar.gz" 2>/dev/null || { echo "Go 解压失败。"; exit 1; }
    export PATH="$PATH:/usr/local/go/bin"
  fi
  command -v go >/dev/null 2>&1 || { echo "Go 还是没有，请手动装好 Go 再跑。"; exit 1; }
  echo "Go 已装好：$(go version)"
fi

# 3. 编译出单二进制
if [ "$DRY_RUN" = "1" ]; then
  echo "[dry-run] 跳过编译（go build）"
else
  echo "编译面板（静态单二进制）..."
  (cd "$SRC_DIR/repo/panel" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BIN" .) || { echo "编译失败：看上面的 Go 报错。"; exit 1; }
  chmod +x "$BIN"
  "$BIN" version 2>/dev/null || echo "（version 子命令没输出，不影响使用）"
  echo "二进制已装好：$BIN"
fi

# 4. 同步脚本到 KIT_DIR（面板调它们干活）
if [ "$DRY_RUN" != "1" ]; then
  mkdir -p "$KIT_DIR"
  for _f in install.sh check.sh stop.sh landing.sh proxy.sh; do
    [ -f "$SRC_DIR/repo/$_f" ] && cp -f "$SRC_DIR/repo/$_f" "$KIT_DIR/$_f" && chmod +x "$KIT_DIR/$_f"
  done
  echo "脚本已同步到 $KIT_DIR"
fi

# 5. systemd 服务（有 systemd 才建；没有就给手动启动命令）
HAS_SYSTEMD=0
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then HAS_SYSTEMD=1; fi
if [ "$HAS_SYSTEMD" = "1" ] && [ "$DRY_RUN" != "1" ]; then
  cat > /etc/systemd/system/relay-panel.service <<UNIT
[Unit]
Description=relay-node visual panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BIN
Environment=PANEL_HOME=$PANEL_HOME
Environment=KIT_DIR=$KIT_DIR
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  echo "systemd 服务 relay-panel 已写好"
fi

# 6. 账号密码（已装过就保留，只在全新安装时问）
mkdir -p "$PANEL_HOME"
chmod 700 "$PANEL_HOME"
PANEL_USER=""; PANEL_PASS=""
UPGRADED=0
if [ -f "$PANEL_HOME/config.json" ] && grep -q password_hash "$PANEL_HOME/config.json" 2>/dev/null; then
  UPGRADED=1
  echo "检测到已装过面板：账号密码与配置保留，只升级二进制。"
else
  echo ""
  echo "设置面板登录账号和密码："
  echo "请填写面板登录用户名（直接回车就是 admin）："
  ask "用户名 [默认 admin]： " PANEL_USER "admin"
  echo "请填写面板登录密码（直接回车用随机强密码，输入不回显）："
  ask_secret "密码（直接回车用随机强密码）： " PANEL_PASS
  if [ -z "$PANEL_PASS" ]; then
    PANEL_PASS="$(rand_password)"
    echo "已生成随机强密码（装完只显示一次）。"
  fi
  if [ "${#PANEL_PASS}" -lt 8 ]; then
    echo "密码至少 8 位，请重跑安装再设一个。"
    exit 1
  fi
  if [ "$DRY_RUN" = "1" ]; then
    echo "[dry-run] 跳过写 $PANEL_HOME/config.json（密码哈希由 relay-panel hash-password 生成）"
  else
    _hash="$("$BIN" hash-password <<<"$PANEL_PASS" 2>/dev/null | tr -d '\n' || true)"
    if [ -z "$_hash" ]; then
      echo "生成密码哈希失败（$BIN hash-password 没输出），面板装不上。"
      exit 1
    fi
    _port="$PANEL_PORT_DEFAULT"
    # 升级重装但 config.json 丢了的极少数情况：沿用旧端口（凭据文件里有）
    if [ -f "$PANEL_HOME/credentials" ]; then
      _old_port="$(grep -E '^面板端口：' "$PANEL_HOME/credentials" 2>/dev/null | grep -oE '[0-9]+' | head -1 || true)"
      [ -n "$_old_port" ] && _port="$_old_port"
    fi
    cat > "$PANEL_HOME/config.json" <<EOF
{
  "username": "$PANEL_USER",
  "password_hash": "$_hash",
  "port": $_port,
  "https_enabled": false,
  "https_domain": "",
  "ip_whitelist": [],
  "telegram": {"enabled": false, "bot_token": "", "chat_id": ""}
}
EOF
    chmod 600 "$PANEL_HOME/config.json"
    echo "账号密码已保存（$PANEL_HOME/config.json，600 权限）"
  fi
fi

# 7. panel-info（找回地址、账号和密码的短命令）
if [ "$DRY_RUN" != "1" ]; then
  cat > /usr/local/bin/panel-info <<'INFOEOF'
#!/usr/bin/env bash
# 找回面板地址、账号和密码：panel-info
set -uo pipefail
PANEL_HOME="${PANEL_HOME:-/etc/relay-panel}"
CRED="$PANEL_HOME/credentials"
if [ ! -f "$CRED" ]; then
  echo "没找到面板凭据文件 $CRED：面板可能没装，先跑 panel-install.sh。"
  exit 1
fi
_ip="$(curl -s --max-time 5 https://api.ipify.org 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}')"
_port="$(grep -oE '"port": *[0-9]+' "$PANEL_HOME/config.json" 2>/dev/null | grep -oE '[0-9]+' | head -1 || true)"
[ -n "$_port" ] || _port="$(grep -E '^面板端口：' "$CRED" | grep -oE '[0-9]+' | head -1 || true)"
[ -n "$_port" ] || _port="11111"
[ -n "$_ip" ] || _ip="<本机公网 IP>"
_user="$(grep -E '^账号：' "$CRED" | sed 's/^账号：//' || true)"
_pass="$(grep -E '^密码：' "$CRED" | sed 's/^密码：//' || true)"
echo "面板地址：http://${_ip}:${_port}"
echo "面板端口：${_port}"
echo "账号：${_user}"
echo "密码：${_pass}"
echo "（如果在面板里改过密码，以面板里的为准；改密码后本文件由面板自动更新。）"
INFOEOF
  chmod +x /usr/local/bin/panel-info
fi

# 8. ufw 放行面板端口（只在 ufw 已启用时）
PANEL_PORT_NOW="$PANEL_PORT_DEFAULT"
if [ -f "$PANEL_HOME/config.json" ]; then
  _p="$(grep -oE '"port": *[0-9]+' "$PANEL_HOME/config.json" 2>/dev/null | grep -oE '[0-9]+' | head -1 || true)"
  [ -n "$_p" ] && PANEL_PORT_NOW="$_p"
fi
if [ "$DRY_RUN" != "1" ] && command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
  ufw allow "$PANEL_PORT_NOW/tcp" >/dev/null 2>&1 || true
  echo "ufw 已放行面板端口 $PANEL_PORT_NOW/tcp"
fi

# 9. 启动面板（只重启面板，不动 realm）
if [ "$HAS_SYSTEMD" = "1" ]; then
  if [ "$DRY_RUN" = "1" ]; then
    echo "[dry-run] 跳过 systemctl enable + restart relay-panel"
  else
    # 升级重装时也必须 restart（enable --now 对已在跑的服务不会重启，旧进程会一直跑着）
    systemctl enable relay-panel 2>/dev/null || true
    systemctl restart relay-panel 2>/dev/null || { echo "面板服务启动失败，看日志：journalctl -u relay-panel -n 50"; echo "（中转转发不受影响，realm 还在跑。）"; exit 1; }
    sleep 2
    if systemctl is-active --quiet relay-panel; then
      echo "面板服务已启动（开机自启）"
    else
      echo "面板服务启动失败，看日志：journalctl -u relay-panel -n 50"
      echo "（中转转发不受影响，realm 还在跑。）"
      exit 1
    fi
  fi
else
  echo "本机没有 systemd，面板需要手动启动："
  echo "  nohup $BIN >/var/log/relay-panel.log 2>&1 &"
  echo "注意：无 systemd 的机器重启后需手动再启动面板。"
  if [ "$DRY_RUN" != "1" ]; then
    PANEL_HOME="$PANEL_HOME" KIT_DIR="$KIT_DIR" nohup "$BIN" >/var/log/relay-panel.log 2>&1 &
    sleep 2
    kill -0 $! 2>/dev/null && echo "面板已在后台运行（PID $!）" || { echo "面板启动失败，看日志：tail -50 /var/log/relay-panel.log"; exit 1; }
  fi
fi

# 10. 最后显示一次全部信息（只显示一次）
if [ "$UPGRADED" = "1" ]; then
  echo ""
  echo "================ 面板已升级 ================"
  echo "账号密码与配置保留，没变过。"
  echo "找回地址和账号：SSH 跑 panel-info"
  echo "面板本身只重启了自己，realm 转发没动过，正在用的节点不断线。"
else
  _ip="$(public_ip)"
  if [ "$DRY_RUN" = "1" ]; then
    echo "[dry-run] 跳过写 $PANEL_HOME/credentials"
  else
    cat > "$PANEL_HOME/credentials" <<EOF
面板地址：http://${_ip}:${PANEL_PORT_NOW}
面板端口：${PANEL_PORT_NOW}
账号：${PANEL_USER}
密码：${PANEL_PASS}
EOF
    chmod 600 "$PANEL_HOME/credentials"
  fi
  echo ""
  echo "================ 面板装好了（只显示一次） ================"
  echo "面板地址：http://${_ip}:${PANEL_PORT_NOW}"
  echo "面板端口：${PANEL_PORT_NOW}"
  echo "账号：${PANEL_USER}"
  echo "密码：${PANEL_PASS}"
  echo ""
  echo "浏览器打开上面这个地址，登录就能管中转。"
  echo "忘了地址或密码：SSH 上服务器跑 panel-info 就能重新看到。"
  echo "HTTPS：先用 http 打开，面板设置里可以一键开 HTTPS（IP 访问是自签证书，浏览器首次提示一次；绑域名可申请真证书）。"
fi
