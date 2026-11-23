#!/usr/bin/env python3
"""订阅网关测试桩回源：模拟面板的三条路。

  /sub/<token>?format=singbox  -> 200，带 subscription-userinfo 头与 Content-Disposition 文件名，
                                  body 是固定 sing-box JSON（2 个假节点）
  /sub/<token>?format=clash    -> 200，同上，body 是固定 Clash YAML（2 个假节点）
  /sub/<token>                  -> 200，base64 明文（2 条假 vless 链接）
  自定义订阅路径（例如 /x9f2/<token>）与 /sub/ 同款：两段路径都按订阅处理，
  模拟面板 1.3.20 的自定义订阅路径（反特征）。
  /api/v1/public-info           -> 200，{"login_notice": "test notice"}
  /client-config.json           -> 200，b'{"from":"origin"}'（故意与本地 fixture 不同，
                                  用来证明网关返回的是本地文件、不是回源）
  /upstream-config.json         -> 200，fixture 文件原字节（供 Worker 测试当 CONFIG_UPSTREAM）

每个请求都记一行 JSONL 日志（path、Host、User-Agent），供测试断言网关透传了什么。

用法：python3 stub-origin.py --port 18081 --log /tmp/stub.log --fixtures tests/fixtures
脱敏：节点全是 example.com 与假 UUID 11111111-2222-3333-4444-555555555555。
"""

import argparse
import base64
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

FAKE_UUID = "11111111-2222-3333-4444-555555555555"
USERINFO = "upload=0; download=0; total=3221225472; expire=1798732799"

SINGBOX = {
    "outbounds": [
        {
            "type": "vless", "tag": "example-node-1",
            "server": "node1.example.com", "server_port": 443,
            "uuid": FAKE_UUID, "tls": {"enabled": True, "server_name": "node1.example.com"},
            "transport": {"type": "ws", "path": "/example-path"},
        },
        {
            "type": "vless", "tag": "example-node-2",
            "server": "node2.example.com", "server_port": 443,
            "uuid": FAKE_UUID, "tls": {"enabled": True, "server_name": "node2.example.com"},
            "transport": {"type": "ws", "path": "/example-path"},
        },
        {"type": "direct", "tag": "direct"},
        {"type": "block", "tag": "block"},
    ]
}

CLASH = """proxies:
  - name: example-node-1
    type: vless
    server: node1.example.com
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
    tls: true
    network: ws
    ws-path: /example-path
  - name: example-node-2
    type: vless
    server: node2.example.com
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
    tls: true
    network: ws
    ws-path: /example-path
"""

PLAIN_LINKS = (
    f"vless://{FAKE_UUID}@node1.example.com:443?encryption=none&security=tls&sni=node1.example.com&type=ws&path=%2Fexample-path#example-node-1\n"
    f"vless://{FAKE_UUID}@node2.example.com:443?encryption=none&security=tls&sni=node2.example.com&type=ws&path=%2Fexample-path#example-node-2\n"
)


class Handler(BaseHTTPRequestHandler):
    fixtures_dir = "."
    log_path = "/tmp/stub-origin.log"

    def log_message(self, *args):
        pass

    def _log(self, path):
        rec = {
            "method": self.command,
            "path": path,
            "host": self.headers.get("Host", ""),
            "ua": self.headers.get("User-Agent", ""),
        }
        with open(self.log_path, "a", encoding="utf-8") as f:
            f.write(json.dumps(rec) + "\n")

    def _send(self, status, body, ctype, extra=None):
        if isinstance(body, str):
            body = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        u = urlparse(self.path)
        self._log(self.path)
        parts = [p for p in u.path.split("/") if p != ""]
        if u.path == "/api/v1/public-info":
            self._send(200, json.dumps({"login_notice": "test notice"}), "application/json")
        elif u.path == "/client-config.json":
            self._send(200, b'{"from":"origin"}', "application/json")
        elif u.path == "/upstream-config.json":
            with open(os.path.join(self.fixtures_dir, "client-config.json"), "rb") as f:
                self._send(200, f.read(), "application/json")
        elif len(parts) == 2:
            # 订阅：/<路径段>/<令牌>，/sub/ 与自定义路径（例如 /x9f2/）都接，
            # 模拟面板的订阅路由（1.3.20 自定义订阅路径）。
            fmt = parse_qs(u.query).get("format", [""])[0]
            if fmt == "singbox":
                self._send(200, json.dumps(SINGBOX), "application/json; charset=utf-8", {
                    "subscription-userinfo": USERINFO,
                    "Content-Disposition": 'attachment; filename="subscription.json"',
                })
            elif fmt == "clash":
                self._send(200, CLASH, "text/yaml; charset=utf-8", {
                    "subscription-userinfo": USERINFO,
                    "Content-Disposition": 'attachment; filename="subscription.yaml"',
                })
            else:
                self._send(200, base64.b64encode(PLAIN_LINKS.encode()).decode(), "text/plain; charset=utf-8", {
                    "subscription-userinfo": USERINFO,
                    "Content-Disposition": 'attachment; filename="subscription.txt"',
                })
        else:
            self._send(404, b"Not Found", "text/plain")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=18081)
    ap.add_argument("--log", default="/tmp/stub-origin.log")
    ap.add_argument("--fixtures", default=".")
    args = ap.parse_args()
    Handler.fixtures_dir = args.fixtures
    Handler.log_path = args.log
    if os.path.exists(args.log):
        os.remove(args.log)
    srv = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"stub origin on 127.0.0.1:{args.port}", flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
