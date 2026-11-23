# 选项 E：Vercel / Netlify（一键部署）

纯声明式跳转，不用写代码：`vercel.json` 与 `netlify.toml` 各三条跳转规则，分别对应网关的三条路。

| 平台 | 文件 | 部署 |
| --- | --- | --- |
| Vercel | `vercel.json` | Import 仓库（放这个文件的目录）-> 改目标地址 -> Deploy；或 `vercel` CLI |
| Netlify | `netlify.toml` | 连仓库或拖文件夹到 Netlify Drop -> 改目标地址 -> Deploy |

两个文件里的目标地址示例都是 `example.com`，部署前按实改（面板回源与 GitHub Raw 直链）。

## 注意

- **本地未验证实际部署**：本机没有 Vercel/Netlify 账号与 CLI，没法真跑一次。首次部署后按主 README 的自测三条路自己验一遍（订阅头、公告、配置 sha256）。
- 面板是自签证书时反代会失败（平台要验上游证书），须先给面板签真证书。
- 查询参数（`?format=singbox/clash`）与响应头（`subscription-userinfo`、Content-Disposition）都是原样透传的，平台不改。
