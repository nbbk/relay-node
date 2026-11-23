// 订阅网关 Cloudflare Worker（选项 A，现有部署的完整版）
//
// 三条路，全部挂在一个域名下：
//   /sub/*                -> 面板回源 ORIGIN（订阅拉取）
//   /api/v1/public-info    -> 面板回源 ORIGIN（公告）
//   /client-config.json    -> 上游签名文件 CONFIG_UPSTREAM（例如 GitHub Raw）
//
// 注意：现在线上跑的 Worker 只接了 /sub/*，/api/v1/public-info 与 /client-config.json
// 还是 404（2026-11-23 实测）。要让公告与远程配置也走 Worker，就用这个文件重新部署。
//
// 绑定（wrangler.toml 的 [vars] 或 Worker 后台 Settings -> Variables）：
//   ORIGIN          面板回源地址，例如 https://ccpanel.example.com:11111
//                   协议必须跟面板当前状态一致：面板 HTTP 时填 http://，面板 HTTPS 时填 https://
//                   面板开关 HTTPS 后要同步改这里（改完重新部署，不用改客户端）。
//   CONFIG_UPSTREAM 签好的 client-config.json 的直链，例如
//                   https://raw.githubusercontent.com/example/example/main/client-config.json
//
// 透传原则：
//   - 请求方法、查询参数（?format=singbox/clash）、请求头（含 UA，面板会按 UA 自适应）原样带过去；
//   - 响应状态码、响应头（subscription-userinfo、Content-Disposition 文件名）与 body 原样回传；
//   - 不加任何自定义头，不给订阅加机场特征。

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const origin = (env.ORIGIN || "").replace(/\/$/, "");

    let target = null;
    if (url.pathname.startsWith("/sub/")) {
      // 订阅拉取：路径里的令牌与查询参数原样带上
      target = origin + url.pathname + url.search;
    } else if (url.pathname === "/api/v1/public-info") {
      target = origin + url.pathname + url.search;
    } else if (url.pathname === "/client-config.json") {
      // 远程配置：Worker 没有本地文件，直接反代上游签名文件，字节精确（不改不动）
      if (!env.CONFIG_UPSTREAM) {
        return new Response("client-config.json upstream not configured", { status: 404 });
      }
      target = env.CONFIG_UPSTREAM;
    } else {
      return new Response("Not Found", { status: 404 });
    }

    if (!origin && !url.pathname.startsWith("/client-config.json")) {
      return new Response("ORIGIN not configured", { status: 500 });
    }

    const init = {
      method: request.method,
      headers: new Headers(request.headers),
      redirect: "manual",
    };
    if (request.method !== "GET" && request.method !== "HEAD" && request.body) {
      init.body = request.body;
      init.duplex = "half";
    }

    const res = await fetch(target, init);
    // 原样回传：状态码、响应头与 body 都不动
    return new Response(res.body, {
      status: res.status,
      statusText: res.statusText,
      headers: res.headers,
    });
  },
};
