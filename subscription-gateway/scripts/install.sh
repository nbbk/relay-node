#!/usr/bin/env bash
# 订阅网关脚本直装（选项 C，不用 Docker）
#
# 一条命令装在任意 Linux 服务器上（面板机本身也行）：
#   curl -fsSL https://example.com/subscription-gateway-kit/scripts/install.sh | bash
#
# 干的事：装 Caddy 二进制 -> 写 /etc/caddy/Caddyfile -> 写 systemd 服务 -> 起服务。
# 幂等：重复跑不会重复装（Caddy 已是目标版本就跳过下载），配置按本次输入重写，已在跑的服务用 reload。
#
# 非交互用法（环境变量都给了就不问）：
#   GATEWAY_DOMAIN=example.com PANEL_ORIGIN=https://ccpanel.example.com:11111 bash install.sh
#   GATEWAY_DOMAIN        网关域名（必填；须先把 A 记录指到这台服务器 IP）
#   PANEL_ORIGIN          面板回源地址（默认 https://127.0.0.1:11111）
#   SUB_PATH              面板自定义订阅路径段（默认 sub；改了填改的值，例如 x9f2；面板生成版已按面板当前值预填，不用再填）
#   PANEL_SELF_SIGNED     面板证书是不是自签的（y/N，默认 N；自签时放行证书校验，有中间人风险）
#   CONFIG_UPSTREAM       远程配置的上游直链（可空；填了会记下来供 update-config.sh 定时同步用）
#   CADDY_VERSION         要装的 Caddy 版本（默认 2.10.2）
#
# 测试：DRY_RUN=1 ROOT_DIR=/tmp/gw-dryrun bash install.sh
#   只打印步骤、把文件写到 ROOT_DIR 下面，不动 systemd、不下载、不改系统。
#
# 注意：正式安装须以 root 运行（要写 /usr/local/bin、/etc、起 systemd 服务）。

set -euo pipefail

CADDY_VERSION="${CADDY_VERSION:-2.10.2}"
ROOT_DIR="${ROOT_DIR:-/}"
DRY_RUN="${DRY_RUN:-0}"

# ROOT_DIR 为 / 时就是真路径；测试时指向临时目录
prefix() { echo "${ROOT_DIR%/}$1"; }
BIN_DIR="$(prefix /usr/local/bin)"
CADDY_BIN="${BIN_DIR}/caddy"
CADDYFILE="$(prefix /etc/caddy/Caddyfile)"
CONFIG_DIR="$(prefix /etc/sub-gateway/config)"
UPSTREAM_FILE="$(prefix /etc/sub-gateway/upstream.url)"
UNIT_FILE="$(prefix /etc/systemd/system/sub-gateway.service)"

say() { echo "==> $*"; }

if [ "$DRY_RUN" != "1" ] && [ "$(id -u)" -ne 0 ]; then
  echo "请以 root 运行（要写 /usr/local/bin、/etc 并起 systemd 服务）；只想先看效果可加 DRY_RUN=1" >&2
  exit 1
fi

# ---------- 交互输入：每项输入前先有一句带冒号的独立提示行，默认值标明“直接回车就是默认值” ----------
echo "网关域名（须先把 A 记录指到这台服务器 IP，例如 example.com）："
if [ -z "${GATEWAY_DOMAIN:-}" ]; then
  read -r GATEWAY_DOMAIN
fi
if [ -z "${GATEWAY_DOMAIN:-}" ]; then
  echo "网关域名必填" >&2
  exit 1
fi

echo "面板回源地址（装在面板机上就直接回车就是默认值，装在别的机器上填面板的地址；协议必须跟面板当前状态一致：面板 HTTP 填 http://，面板 HTTPS 填 https://）："
if [ -z "${PANEL_ORIGIN:-}" ]; then
  read -r PANEL_ORIGIN
fi
PANEL_ORIGIN="${PANEL_ORIGIN:-https://127.0.0.1:11111}"

echo "面板自定义订阅路径段（面板 1.3.20 反特征，设置页订阅选项卡里改的那个值；没改过就直接回车就是默认值 sub，改了填改的值，例如 x9f2）："
if [ -z "${SUB_PATH:-}" ]; then
  read -r SUB_PATH
fi
SUB_PATH="${SUB_PATH:-sub}"

echo "面板证书是不是自签的（自签时网关要放行证书校验，有中间人风险，仅过渡期用；直接回车就是默认值 N）："
if [ -z "${PANEL_SELF_SIGNED:-}" ]; then
  read -r PANEL_SELF_SIGNED
fi
PANEL_SELF_SIGNED="${PANEL_SELF_SIGNED:-N}"

echo "远程配置的上游直链（可空；填了会记下来供 update-config.sh 定时同步用，例如 GitHub Raw 地址；直接回车就是不填）："
if [ -z "${CONFIG_UPSTREAM:-}" ]; then
  read -r CONFIG_UPSTREAM || true
fi

echo "要装的 Caddy 版本（直接回车就是默认值 ${CADDY_VERSION}）："
if [ -z "${CADDY_VERSION_INPUT:-}" ]; then
  read -r CADDY_VERSION_INPUT || true
fi
CADDY_VERSION="${CADDY_VERSION_INPUT:-$CADDY_VERSION}"

say "网关域名：$GATEWAY_DOMAIN"
say "面板回源：$PANEL_ORIGIN"
say "订阅路径：$SUB_PATH"
say "面板自签：$PANEL_SELF_SIGNED"
say "Caddy 版本：$CADDY_VERSION"

# ---------- 装 Caddy ----------
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  CADDY_ARCH=amd64 ;;
  aarch64) CADDY_ARCH=arm64 ;;
  *) echo "不支持的架构：$ARCH（只支持 x86_64 / aarch64）" >&2; exit 1 ;;
esac

if [ -x "$CADDY_BIN" ] && "$CADDY_BIN" version 2>/dev/null | grep -q "$CADDY_VERSION"; then
  say "Caddy $CADDY_VERSION 已装，跳过下载"
else
  say "下载 Caddy $CADDY_VERSION（$CADDY_ARCH）"
  if [ "$DRY_RUN" = "1" ]; then
    say "[DRY_RUN] 本应下载 https://github.com/caddyserver/caddy/releases/download/v${CADDY_VERSION}/caddy_${CADDY_VERSION}_linux_${CADDY_ARCH}.tar.gz 到 ${CADDY_BIN}"
    mkdir -p "$BIN_DIR"
    printf '#!/bin/sh\necho "v%s placeholder"\n' "$CADDY_VERSION" > "$CADDY_BIN"
    chmod +x "$CADDY_BIN"
  else
    TMPD="$(mktemp -d)"
    trap 'rm -rf "$TMPD"' EXIT
    curl -fsSL -o "$TMPD/caddy.tar.gz" "https://github.com/caddyserver/caddy/releases/download/v${CADDY_VERSION}/caddy_${CADDY_VERSION}_linux_${CADDY_ARCH}.tar.gz"
    tar --no-same-owner -xzf "$TMPD/caddy.tar.gz" -C "$TMPD" caddy
    mkdir -p "$BIN_DIR"
    install -m 0755 "$TMPD/caddy" "$CADDY_BIN"
    trap - EXIT
    rm -rf "$TMPD"
  fi
fi

# ---------- 写配置 ----------
say "写 Caddyfile：$CADDYFILE"
mkdir -p "$(dirname "$CADDYFILE")" "$CONFIG_DIR"
cat > "$CADDYFILE" <<EOF
# 订阅网关 Caddyfile（由 scripts/install.sh 生成；容器版见 caddy/Caddyfile，两者同款）
# 三条路：/<SUB_PATH>/* 与 /sub/* 反代到面板，/api/v1/public-info 反代到面板，/client-config.json 只读本地签名文件。
# 自定义订阅路径与默认 /sub/ 都接：2026-11-23 真机教训，只写 /sub/ 时面板订阅路径是自定义的
# /x9f2/，订阅经网关全是 404、直连面板却是 200。SUB_PATH 没填时默认 sub，两条就是同一条路。
# 注意：这里不做 gzip，client-config.json 要求字节精确，不压缩最稳妥。

${GATEWAY_DOMAIN} {

	handle /healthz {
		respond "ok" 200
	}

	handle /client-config.json {
		file_server {
			root ${CONFIG_DIR}
		}
	}

	handle /api/v1/public-info {
		reverse_proxy ${PANEL_ORIGIN} {
			header_up Host {http.reverse_proxy.upstream.hostport}
			# @SELF_SIGNED_BEGIN@
			# transport http {
			# 	tls_insecure_skip_verify
			# }
			# @SELF_SIGNED_END@
		}
	}

	handle /${SUB_PATH}/* {
		reverse_proxy ${PANEL_ORIGIN} {
			header_up Host {http.reverse_proxy.upstream.hostport}
			# @SELF_SIGNED_BEGIN@
			# transport http {
			# 	tls_insecure_skip_verify
			# }
			# @SELF_SIGNED_END@
		}
	}

	handle /sub/* {
		reverse_proxy ${PANEL_ORIGIN} {
			header_up Host {http.reverse_proxy.upstream.hostport}
			# @SELF_SIGNED_BEGIN@
			# transport http {
			# 	tls_insecure_skip_verify
			# }
			# @SELF_SIGNED_END@
		}
	}

	handle {
		respond "Not Found" 404
	}
}
EOF

if [ "$PANEL_SELF_SIGNED" = "y" ] || [ "$PANEL_SELF_SIGNED" = "Y" ]; then
  say "面板是自签证书：放行回源证书校验（有中间人风险，仅过渡期用）"
  sed -i '/# @SELF_SIGNED_BEGIN@/,/# @SELF_SIGNED_END@/{s/^\(\t*\)# /\1/; /@SELF_SIGNED_/d}' "$CADDYFILE"
else
  sed -i '/# @SELF_SIGNED_BEGIN@/,/# @SELF_SIGNED_END@/d' "$CADDYFILE"
fi

if [ -n "${CONFIG_UPSTREAM:-}" ]; then
  say "记下远程配置上游：$CONFIG_UPSTREAM"
  mkdir -p "$(dirname "$UPSTREAM_FILE")"
  printf '%s\n' "$CONFIG_UPSTREAM" > "$UPSTREAM_FILE"
fi

# 远程配置文件占位（没有就先空着，/client-config.json 会 404；以后用 update-config.sh 同步）
touch "${CONFIG_DIR}/.gitkeep" 2>/dev/null || true

# ---------- systemd 服务 ----------
say "写 systemd 服务：$UNIT_FILE"
mkdir -p "$(dirname "$UNIT_FILE")"
cat > "$UNIT_FILE" <<EOF
[Unit]
Description=Subscription Gateway (Caddy)
Documentation=https://example.com/subscription-gateway-kit
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
User=caddy
Group=caddy
ExecStart=${CADDY_BIN} run --config ${CADDYFILE} --adapter caddyfile
ExecReload=${CADDY_BIN} reload --config ${CADDYFILE} --adapter caddyfile
TimeoutStopSec=5s
LimitNOFILE=1048576
PrivateTmp=true
ProtectSystem=full
AmbientCapabilities=CAP_NET_BIND_SERVICE
Environment=HOME=/var/lib/caddy

[Install]
WantedBy=multi-user.target
EOF

if [ "$DRY_RUN" = "1" ]; then
  say "[DRY_RUN] 本应执行：建 caddy 用户、systemctl daemon-reload、systemctl enable --now sub-gateway"
  say "[DRY_RUN] 完成，文件都在 ${ROOT_DIR} 下面"
  exit 0
fi

if ! id caddy >/dev/null 2>&1; then
  useradd --system --home /var/lib/caddy --shell /usr/sbin/nologin caddy
fi
mkdir -p /var/lib/caddy
chown caddy:caddy /var/lib/caddy "$CONFIG_DIR"
systemctl daemon-reload
if systemctl is-active --quiet sub-gateway; then
  say "服务已在跑，reload 配置"
  systemctl reload sub-gateway
else
  say "启动服务"
  systemctl enable --now sub-gateway
fi

say "完成。自测三条路（把 example.com 换成你的网关域名）："
say "  curl -sI https://$GATEWAY_DOMAIN/healthz"
say "  curl -s https://$GATEWAY_DOMAIN/api/v1/public-info"
say "  curl -s -D - -o /dev/null 'https://$GATEWAY_DOMAIN/sub/xxxx?format=singbox' | grep -i 'subscription-userinfo'"
say "面板开关 HTTPS 后，记得同步改回源：改 Caddyfile 里的回源地址，再 systemctl reload sub-gateway"
