#!/usr/bin/env bash
# 订阅网关本地测试：一键起桩回源 + 真 Caddy（生产 Caddyfile）+ 真 Worker 逻辑（Node），断言三条路。
#
# 用法：bash tests/run-tests.sh
# 要求：python3、curl、node、本套件 tests/fixtures 与 ~/workspace/toolchains/caddy/caddy。
# Docker 不参与（本机没有 Docker，docker-compose.yml 只做 YAML 校验，真机上再跑）。
#
# 测什么：
#   Caddy（生产 Caddyfile）对桩回源：订阅三格式（头、文件名、节点数）、公告、远程配置字节精确、
#     其它路径 404、回源收到的 Host 与 UA；install.sh 干跑两档；update-config.sh 三档；
#     Worker（Node 直跑 worker.js）：三条路 + 未配上游 404；配置文件语法（compose/vercel/netlify）。

set -u
KIT="$(cd "$(dirname "$0")/.." && pwd)"
CADDY_BIN="${CADDY_BIN:-$HOME/workspace/toolchains/caddy/caddy}"
TMP="$(mktemp -d /tmp/gw-test-XXXXXX)"
STUB_PORT=18081
GW_PORT=18080
PASS=0
FAIL=0

ok()   { PASS=$((PASS+1)); echo "PASS $1"; }
bad()  { FAIL=$((FAIL+1)); echo "FAIL $1 $2"; }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "(want $3, got $2)"; fi; }

cleanup() {
  [ -n "${STUB_PID:-}" ] && kill "$STUB_PID" 2>/dev/null
  [ -n "${CADDY_PID:-}" ] && kill "$CADDY_PID" 2>/dev/null
  wait 2>/dev/null
}
trap cleanup EXIT

export HOME="$TMP/home"
export XDG_CONFIG_HOME="$TMP/home/.config"
export XDG_DATA_HOME="$TMP/home/.local/share"
mkdir -p "$HOME"

echo "== 0. 工具 =="
[ -x "$CADDY_BIN" ] && ok "caddy binary present ($("$CADDY_BIN" version))" || bad "caddy binary present" "(missing $CADDY_BIN)"
command -v node >/dev/null && ok "node present ($(node --version))" || bad "node present" ""
command -v python3 >/dev/null && ok "python3 present" || bad "python3 present" ""
bash -n "$KIT/scripts/install.sh" && ok "install.sh syntax" || bad "install.sh syntax" ""
bash -n "$KIT/scripts/update-config.sh" && ok "update-config.sh syntax" || bad "update-config.sh syntax" ""
node --check "$KIT/worker/worker.js" && ok "worker.js syntax" || bad "worker.js syntax" ""

echo "== 1. 起桩回源（模拟面板） =="
python3 "$KIT/tests/stub-origin.py" --port "$STUB_PORT" --log "$TMP/stub.log" --fixtures "$KIT/tests/fixtures" >"$TMP/stub.out" 2>&1 &
STUB_PID=$!
for _ in $(seq 1 30); do
  curl -fsS --max-time 2 "http://127.0.0.1:$STUB_PORT/api/v1/public-info" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -fsS --max-time 2 "http://127.0.0.1:$STUB_PORT/api/v1/public-info" >/dev/null 2>&1 && ok "stub origin up" || bad "stub origin up" ""

echo "== 2. Caddy validate（生产 Caddyfile） =="
# 自定义订阅路径（SUB_PATH=x9f2）与默认（SUB_PATH=sub）两档都要能加载：
# SUB_PATH 没填时两条 handle 都是 /sub/*，Caddy 也要认。
GATEWAY_DOMAIN="http://127.0.0.1:$GW_PORT" PANEL_ORIGIN="http://127.0.0.1:$STUB_PORT" CONFIG_DIR="$KIT/tests/fixtures" SUB_PATH="x9f2" \
  "$CADDY_BIN" validate --config "$KIT/caddy/Caddyfile" --adapter caddyfile >"$TMP/validate.out" 2>&1 \
  && ok "caddy validate production Caddyfile (SUB_PATH=x9f2)" || { bad "caddy validate production Caddyfile (SUB_PATH=x9f2)" ""; cat "$TMP/validate.out"; }
GATEWAY_DOMAIN="http://127.0.0.1:$GW_PORT" PANEL_ORIGIN="http://127.0.0.1:$STUB_PORT" CONFIG_DIR="$KIT/tests/fixtures" SUB_PATH="sub" \
  "$CADDY_BIN" validate --config "$KIT/caddy/Caddyfile" --adapter caddyfile >"$TMP/validate-sub.out" 2>&1 \
  && ok "caddy validate production Caddyfile (SUB_PATH=sub)" || { bad "caddy validate production Caddyfile (SUB_PATH=sub)" ""; cat "$TMP/validate-sub.out"; }

echo "== 3. 起 Caddy（生产 Caddyfile，SUB_PATH=x9f2） =="
export GATEWAY_DOMAIN="http://127.0.0.1:$GW_PORT" PANEL_ORIGIN="http://127.0.0.1:$STUB_PORT" CONFIG_DIR="$KIT/tests/fixtures" SUB_PATH="x9f2"
"$CADDY_BIN" run --config "$KIT/caddy/Caddyfile" --adapter caddyfile >"$TMP/caddy.log" 2>&1 &
CADDY_PID=$!
for _ in $(seq 1 30); do
  curl -fsS --max-time 2 "http://127.0.0.1:$GW_PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
H="$(curl -s --max-time 5 "http://127.0.0.1:$GW_PORT/healthz")"
check "gateway /healthz" "$H" "ok"

GW="http://127.0.0.1:$GW_PORT"
UA="Zhexuexi/1.0"
TOKEN="11111111-2222-3333-4444-555555555555"

echo "== 4. Caddy：订阅三格式 + 自定义路径 =="
curl -s -D "$TMP/h1" -o "$TMP/b1" --max-time 10 -A "$UA" "$GW/sub/$TOKEN?format=singbox"
S1="$(grep -i '^HTTP/' "$TMP/h1" | tail -1 | awk '{print $2}')"
check "sub singbox status 200" "$S1" "200"
grep -qi '^subscription-userinfo: upload=0; download=0; total=3221225472; expire=1798732799' "$TMP/h1" && ok "sub singbox userinfo passthrough" || bad "sub singbox userinfo passthrough" ""
grep -qi 'content-disposition: attachment; filename="subscription.json"' "$TMP/h1" && ok "sub singbox filename passthrough" || bad "sub singbox filename passthrough" ""
N1="$(python3 -c "import json;print(sum(1 for o in json.load(open('$TMP/b1'))['outbounds'] if o['type']=='vless'))")"
check "sub singbox 2 vless outbounds" "$N1" "2"

# 自定义订阅路径 /x9f2/：与 /sub/ 同款（2026-11-23 真机教训，只接 /sub/ 时自定义路径全 404）
curl -s -D "$TMP/h1x" -o "$TMP/b1x" --max-time 10 -A "$UA" "$GW/x9f2/$TOKEN?format=singbox"
S1X="$(grep -i '^HTTP/' "$TMP/h1x" | tail -1 | awk '{print $2}')"
check "sub-path singbox status 200" "$S1X" "200"
grep -qi '^subscription-userinfo: upload=0; download=0; total=3221225472; expire=1798732799' "$TMP/h1x" && ok "sub-path singbox userinfo passthrough" || bad "sub-path singbox userinfo passthrough" ""
N1X="$(python3 -c "import json;print(sum(1 for o in json.load(open('$TMP/b1x'))['outbounds'] if o['type']=='vless'))")"
check "sub-path singbox 2 vless outbounds" "$N1X" "2"

curl -s -D "$TMP/h2" -o "$TMP/b2" --max-time 10 -A "$UA" "$GW/sub/$TOKEN?format=clash"
S2="$(grep -i '^HTTP/' "$TMP/h2" | tail -1 | awk '{print $2}')"
check "sub clash status 200" "$S2" "200"
N2="$(grep -c -- '- name: example-node-' "$TMP/b2")"
check "sub clash 2 proxies" "$N2" "2"

curl -s -D "$TMP/h3" -o "$TMP/b3" --max-time 10 -A "$UA" "$GW/sub/$TOKEN"
S3="$(grep -i '^HTTP/' "$TMP/h3" | tail -1 | awk '{print $2}')"
check "sub plain status 200" "$S3" "200"
L3="$(python3 -c "import base64;print(len([l for l in base64.b64decode(open('$TMP/b3','rb').read()).decode().splitlines() if l.strip()]))")"
check "sub plain 2 links" "$L3" "2"

echo "== 5. Caddy：公告 + 远程配置 + 其它路径 =="
curl -s --max-time 10 "$GW/api/v1/public-info" -o "$TMP/b4"
N4="$(python3 -c "import json;print(json.load(open('$TMP/b4'))['login_notice'])")"
check "public-info notice" "$N4" "test notice"

curl -s -D "$TMP/h5" -o "$TMP/b5" --max-time 10 "$GW/client-config.json"
S5="$(grep -i '^HTTP/' "$TMP/h5" | tail -1 | awk '{print $2}')"
check "config status 200" "$S5" "200"
F5="$(sha256sum "$KIT/tests/fixtures/client-config.json" | awk '{print $1}')"
G5="$(sha256sum "$TMP/b5" | awk '{print $1}')"
check "config byte-exact (sha256)" "$G5" "$F5"

S6="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$GW/api/v1/settings")"
check "unknown path 404" "$S6" "404"

echo "== 6. Caddy：回源收到了什么（Host 与 UA 透传） =="
SH="$(python3 -c "import json;rows=[json.loads(l) for l in open('$TMP/stub.log') if l.strip()];print([r for r in rows if r['path'].startswith('/sub/')][0]['host'])")"
check "origin saw upstream Host" "$SH" "127.0.0.1:$STUB_PORT"
SU="$(python3 -c "import json;rows=[json.loads(l) for l in open('$TMP/stub.log') if l.strip()];print([r for r in rows if r['path'].startswith('/sub/')][0]['ua'])")"
check "origin saw client UA" "$SU" "$UA"

echo "== 7. Worker（Node 直跑 worker.js） =="
STUB_PORT="$STUB_PORT" node "$KIT/tests/worker-test.mjs" >"$TMP/worker.out" 2>&1
if [ $? -eq 0 ]; then ok "worker-test all passed"; else bad "worker-test" ""; cat "$TMP/worker.out"; fi
grep -c '^PASS' "$TMP/worker.out" | xargs echo "worker-test PASS lines:"

echo "== 8. install.sh 干跑 =="
DRY_ROOT="$TMP/dryrun"
DRY_RUN=1 ROOT_DIR="$DRY_ROOT" GATEWAY_DOMAIN=example.com PANEL_ORIGIN=https://ccpanel.example.com:11111 SUB_PATH=x9f2 PANEL_SELF_SIGNED=N CADDY_VERSION=2.10.2 \
  bash "$KIT/scripts/install.sh" </dev/null >"$TMP/dry1.out" 2>&1
[ $? -eq 0 ] && ok "install.sh dry-run exit 0" || bad "install.sh dry-run exit 0" ""
[ -f "$DRY_ROOT/etc/caddy/Caddyfile" ] && ok "dry-run Caddyfile written" || bad "dry-run Caddyfile written" ""
grep -q 'handle /x9f2/\*' "$DRY_ROOT/etc/caddy/Caddyfile" && ok "dry-run Caddyfile has custom sub path" || bad "dry-run Caddyfile has custom sub path" ""
grep -q 'handle /sub/\*' "$DRY_ROOT/etc/caddy/Caddyfile" && ok "dry-run Caddyfile keeps /sub/ fallback" || bad "dry-run Caddyfile keeps /sub/ fallback" ""
GATEWAY_DOMAIN="http://127.0.0.1:18080" SUB_PATH=x9f2 "$CADDY_BIN" validate --config "$DRY_ROOT/etc/caddy/Caddyfile" --adapter caddyfile >"$TMP/dry1v.out" 2>&1 \
  && ok "dry-run Caddyfile validates" || { bad "dry-run Caddyfile validates" ""; cat "$TMP/dry1v.out"; }
grep -q 'tls_insecure_skip_verify' "$DRY_ROOT/etc/caddy/Caddyfile" && bad "dry-run self-signed N: no insecure" "(found)" || ok "dry-run self-signed N: no insecure"
[ -f "$DRY_ROOT/etc/systemd/system/sub-gateway.service" ] && ok "dry-run systemd unit written" || bad "dry-run systemd unit written" ""

DRY_RUN=1 ROOT_DIR="$TMP/dryrun2" GATEWAY_DOMAIN=example.com PANEL_ORIGIN=https://ccpanel.example.com:11111 PANEL_SELF_SIGNED=y CADDY_VERSION=2.10.2 \
  bash "$KIT/scripts/install.sh" </dev/null >"$TMP/dry2.out" 2>&1
grep -q 'tls_insecure_skip_verify' "$TMP/dryrun2/etc/caddy/Caddyfile" && ok "dry-run self-signed y: insecure present" || bad "dry-run self-signed y: insecure present" ""
! grep -q '@SELF_SIGNED_' "$TMP/dryrun2/etc/caddy/Caddyfile" && ok "dry-run self-signed y: markers removed" || bad "dry-run self-signed y: markers removed" ""
# 默认 SUB_PATH=sub 时两条 handle 都是 /sub/*，也要能加载。
GATEWAY_DOMAIN="http://127.0.0.1:18080" SUB_PATH=sub "$CADDY_BIN" validate --config "$TMP/dryrun2/etc/caddy/Caddyfile" --adapter caddyfile >"$TMP/dry2v.out" 2>&1 \
  && ok "dry-run Caddyfile validates (SUB_PATH=sub)" || { bad "dry-run Caddyfile validates (SUB_PATH=sub)" ""; cat "$TMP/dry2v.out"; }

echo "== 9. update-config.sh =="
mkdir -p "$TMP/cfg1" "$TMP/cfg2"
CONFIG_DIR="$TMP/cfg1" bash "$KIT/scripts/update-config.sh" "$KIT/tests/fixtures/client-config.json" >"$TMP/upd1.out" 2>&1
[ $? -eq 0 ] && ok "update-config local upload exit 0" || bad "update-config local upload exit 0" ""
U1="$(sha256sum "$TMP/cfg1/client-config.json" | awk '{print $1}')"
check "update-config local byte-exact" "$U1" "$F5"
CONFIG_DIR="$TMP/cfg2" bash "$KIT/scripts/update-config.sh" --from-url "http://127.0.0.1:$STUB_PORT/upstream-config.json" >"$TMP/upd2.out" 2>&1
[ $? -eq 0 ] && ok "update-config --from-url exit 0" || bad "update-config --from-url exit 0" ""
U2="$(sha256sum "$TMP/cfg2/client-config.json" | awk '{print $1}')"
check "update-config url byte-exact" "$U2" "$F5"
echo "not json" > "$TMP/bad.json"
mkdir -p "$TMP/cfg3"
if CONFIG_DIR="$TMP/cfg3" bash "$KIT/scripts/update-config.sh" "$TMP/bad.json" >"$TMP/upd3.out" 2>&1; then
  bad "update-config rejects invalid JSON" "(exit 0)"
else
  ok "update-config rejects invalid JSON"
fi
[ ! -f "$TMP/cfg3/client-config.json" ] && ok "invalid JSON not written" || bad "invalid JSON not written" ""

echo "== 10. 配置文件语法 =="
python3 -c "import yaml; d=yaml.safe_load(open('$KIT/caddy/docker-compose.yml')); assert d['services']['gateway']['image']=='caddy:2-alpine'; assert '80:80' in d['services']['gateway']['ports']; assert '443:443' in d['services']['gateway']['ports']; assert '.env' in d['services']['gateway']['env_file']" \
  && ok "docker-compose.yml parses (image/ports/env_file)" || bad "docker-compose.yml parses" ""
python3 -c "import json; d=json.load(open('$KIT/serverless/vercel.json')); assert len(d['rewrites'])==3" \
  && ok "vercel.json parses (3 rewrites)" || bad "vercel.json parses" ""
python3 -c "import tomllib; d=tomllib.load(open('$KIT/serverless/netlify.toml','rb')); assert len(d['redirects'])==3" \
  && ok "netlify.toml parses (3 redirects)" || bad "netlify.toml parses" ""

echo ""
echo "RESULT: $PASS passed, $FAIL failed (tmp: $TMP)"
[ "$FAIL" -eq 0 ]
