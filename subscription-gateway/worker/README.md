# 选项 A：Cloudflare Worker（现有部署的完整版）

## 现状

线上跑的 Worker 只接了 `/sub/*`，公告与远程配置经它还是 404（2026-11-23 实测）。这个 `worker.js` 把三条路补全了，重新部署就全通。

回源地址填的是 https（用户 2026-11-23 确认），与面板 HTTPS 状态一致，所以面板重开 HTTPS 后订阅没断。

## 部署

方法一（wrangler）：

```bash
npm i -g wrangler
cd worker
# 先改 wrangler.toml 里的 ORIGIN、SUB_PATH 与 CONFIG_UPSTREAM（示例是 example.com，按实改）
wrangler deploy
```

方法二（Cloudflare 后台）：Workers & Pages -> 选你的 Worker -> Edit code，粘贴 `worker.js` 的内容，Save and deploy。

## 绑定三个变量

| 变量 | 填什么 |
| --- | --- |
| `ORIGIN` | 面板回源地址，例如 `https://ccpanel.example.com:11111` |
| `SUB_PATH` | 面板自定义订阅路径段（面板 1.3.20 反特征）：没改过填 `sub`，改了填改的值，例如 `x9f2`。订阅路由同时接 `SUB_PATH` 与默认 `/sub/`，面板改了订阅路径后要同步改这里（改完重新部署） |
| `CONFIG_UPSTREAM` | 签好的 `client-config.json` 的直链，例如 `https://raw.githubusercontent.com/example/example/main/client-config.json` |

在 wrangler.toml 的 `[vars]` 里填，或在 Worker 后台 Settings -> Variables 里填（后者不会进仓库，更稳妥；正式用建议走后台）。

## 最重要的一条：回源协议跟面板状态走

- 面板 HTTP 时 `ORIGIN` 填 `http://...:11111`
- 面板 HTTPS 时 `ORIGIN` 填 `https://...:11111`

面板开关 HTTPS 后，一定要同步改 `ORIGIN` 再重新部署，不改订阅就断。症状：订阅报 502，或面板日志出现 `Client sent an HTTP request to an HTTPS server`。

## 自测三条路（把 example.com 换成你的 Worker 域名）

```bash
curl -s -D - -o /dev/null 'https://example.com/sub/11111111-2222-3333-4444-555555555555?format=singbox' | grep -i 'subscription-userinfo'
curl -s https://example.com/api/v1/public-info
curl -s https://example.com/client-config.json | sha256sum
```

## 费用

Workers 免费额度每天 10 万请求，订阅拉取这点量够用。超出后按量计费，见 Cloudflare 定价页。
