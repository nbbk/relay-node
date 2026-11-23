// 订阅网关 Cloudflare Worker（选项 A，现有部署的完整版）
//
// 三条路，全部挂在一个域名下：
//   /<SUB_PATH>/* 与 /sub/* -> 面板回源 ORIGIN（订阅拉取）
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
//   SUB_PATH        面板自定义订阅路径段（面板 1.3.20 反特征），例如 x9f2；没改过就填 sub。
//                   订阅路由同时接 SUB_PATH 与默认 /sub/：路径改了之后老链接还能走 /sub/，
//                   新链接走 /<SUB_PATH>/，两条都转发到回源同路径。面板改了订阅路径后要同步改这里（改完重新部署）。
//                   面板生成版：已按面板当前值填好，不用再填，面板改了路径后重新生成一次。
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
    // 订阅路径段：套件通用版用 env.SUB_PATH（默认 sub），面板生成版直接填面板当前值。
    const subPath = (env.SUB_PATH || "sub").replace(/^\/+|\/+$/g, "") || "sub";

    let target = null;
    if (url.pathname.startsWith("/" + subPath + "/") || url.pathname.startsWith("/sub/")) {
      // 订阅拉取：自定义路径与默认 /sub/ 都转发，路径里的令牌与查询参数原样带上。
      // 自定义路径是反特征（面板 1.3.20，订阅路径可改）；/sub/ 是兜底——
      // 2026-11-23 真机教训：新版 Worker 只写了 /sub/，面板订阅路径是自定义的
      // /x9f2/，订阅经 Worker 全是 404、直连面板却是 200。两条都写就不会再断。
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
