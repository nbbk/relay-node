#!/usr/bin/env bash
# 同步 client-config.json 到网关本地目录（供 Caddy file_server 原样 served）
#
# 用法：
#   update-config.sh <签好的 client-config.json>   # 本地上传（签名流程见 README：先用客户端仓的
#                                                   # tools/remote-config/sign.py 签好，再传过来）
#   update-config.sh --from-url <直链>              # 从上游拉取（例如 GitHub Raw）
#   update-config.sh --from-url                     # 用 /etc/sub-gateway/upstream.url 里记的上游地址
#
# 默认写到 $CONFIG_DIR/client-config.json（默认 /etc/sub-gateway/config，可用 CONFIG_DIR 环境变量改）。
# 原子写（同目录临时文件 + mv），写完打印新旧 sha256；校验是合法 JSON。
# 不重启 Caddy：file_server 每次请求都读盘，换文件立即生效。
#
# 注意：这里不验 Ed25519 签名——网关没有私钥，验签是客户端拉取时做的事；
# 网关只保证一件事：收到的文件是什么字节，客户端拿到的就是什么字节。

set -euo pipefail

CONFIG_DIR="${CONFIG_DIR:-/etc/sub-gateway/config}"
UPSTREAM_FILE="${UPSTREAM_FILE:-/etc/sub-gateway/upstream.url}"
TARGET="$CONFIG_DIR/client-config.json"

usage() {
  echo "用法：$0 <签好的 client-config.json> | $0 --from-url [直链]" >&2
  exit 1
}

SRC=""
MODE=""
if [ "${1:-}" = "--from-url" ]; then
  MODE=url
  if [ -n "${2:-}" ]; then
    SRC="$2"
  elif [ -f "$UPSTREAM_FILE" ]; then
    SRC="$(cat "$UPSTREAM_FILE")"
  else
    echo "没有给直链，$UPSTREAM_FILE 里也没有记上游地址" >&2
    exit 1
  fi
elif [ -n "${1:-}" ]; then
  MODE=file
  SRC="$1"
else
  usage
fi

mkdir -p "$CONFIG_DIR"
TMPF="$(mktemp "$CONFIG_DIR/.client-config.json.XXXXXX")"
trap 'rm -f "$TMPF"' EXIT

if [ "$MODE" = "url" ]; then
  echo "从上游拉取：$SRC"
  curl -fsSL -o "$TMPF" "$SRC"
else
  echo "从本地文件同步：$SRC"
  cp "$SRC" "$TMPF"
fi

# 校验是合法 JSON（不是 JSON 就拒绝，避免把坏文件推上线）
if command -v python3 >/dev/null 2>&1; then
  if ! python3 -m json.tool "$TMPF" >/dev/null 2>&1; then
    echo "拒绝：文件不是合法 JSON" >&2
    exit 1
  fi
else
  echo "警告：本机没有 python3，跳过 JSON 校验" >&2
fi

OLD_SUM="（无旧文件）"
[ -f "$TARGET" ] && OLD_SUM="$(sha256sum "$TARGET" | awk '{print $1}')"
mv "$TMPF" "$TARGET"
trap - EXIT
chmod 0644 "$TARGET"
NEW_SUM="$(sha256sum "$TARGET" | awk '{print $1}')"

echo "旧 sha256：$OLD_SUM"
echo "新 sha256：$NEW_SUM"
echo "已同步到 $TARGET（Caddy 立即生效，不用重启）"
