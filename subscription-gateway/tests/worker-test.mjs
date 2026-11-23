// Worker 逻辑测试（Node 直跑，不经过 Cloudflare）：
// 把 worker/worker.js 原样拷成 .mjs 导入，用桩回源当 ORIGIN 与 CONFIG_UPSTREAM，
// 断言三条路的路由、透传头与字节精确。
// 用法：STUB_PORT=18081 node tests/worker-test.mjs
// 退出码：全过 0，任一失败 1。

import { readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const STUB = `http://127.0.0.1:${process.env.STUB_PORT || 18081}`;
const FIXTURE = readFileSync(new URL("./fixtures/client-config.json", import.meta.url));

// worker.js 是 .js 后缀但写的是 ESM，拷成 .mjs 再导入，保证测的是原文件内容。
const src = readFileSync(new URL("../worker/worker.js", import.meta.url), "utf-8");
const tmpMod = join(tmpdir(), `gw-worker-test-${process.pid}.mjs`);
writeFileSync(tmpMod, src);
const worker = (await import(tmpMod)).default;

const env = {
  ORIGIN: STUB,
  // 自定义订阅路径段（面板 1.3.20）：/x9f2/ 与默认 /sub/ 两条都要通。
  SUB_PATH: "x9f2",
  CONFIG_UPSTREAM: `${STUB}/upstream-config.json`,
};

let pass = 0;
let fail = 0;
function check(name, cond, extra = "") {
  if (cond) {
    pass++;
    console.log(`PASS ${name}`);
  } else {
    fail++;
    console.log(`FAIL ${name} ${extra}`);
  }
}

async function call(path, headers = {}) {
  const req = new Request(`https://gw.example.com${path}`, { headers });
  const res = await worker.fetch(req, env, {});
  const buf = Buffer.from(await res.arrayBuffer());
  return { res, buf };
}

// 1. 订阅 singbox（自定义路径 /x9f2/）：200 + userinfo 头 + 文件名 + 2 个节点
{
  const { res, buf } = await call("/x9f2/FAKETOKEN?format=singbox", { "User-Agent": "Zhexuexi/1.0" });
  check("worker sub-path singbox status 200", res.status === 200, `got ${res.status}`);
  check("worker sub-path userinfo passthrough", (res.headers.get("subscription-userinfo") || "").includes("total=3221225472"));
  check("worker sub-path content-disposition", (res.headers.get("content-disposition") || "").includes("subscription.json"));
  const j = JSON.parse(buf.toString("utf-8"));
  const vless = j.outbounds.filter((o) => o.type === "vless");
  check("worker sub-path 2 vless outbounds", vless.length === 2, `got ${vless.length}`);
}

// 1b. 订阅 singbox（默认 /sub/ 兜底）：SUB_PATH 改了之后老路径也要通
{
  const { res, buf } = await call("/sub/FAKETOKEN?format=singbox", { "User-Agent": "Zhexuexi/1.0" });
  check("worker sub fallback status 200", res.status === 200, `got ${res.status}`);
  const j = JSON.parse(buf.toString("utf-8"));
  const vless = j.outbounds.filter((o) => o.type === "vless");
  check("worker sub fallback 2 vless outbounds", vless.length === 2, `got ${vless.length}`);
}

// 2. 订阅 clash：200 + YAML 里 2 个 proxy
{
  const { res, buf } = await call("/sub/FAKETOKEN?format=clash", { "User-Agent": "Zhexuexi/1.0" });
  const txt = buf.toString("utf-8");
  check("worker sub clash status 200", res.status === 200, `got ${res.status}`);
  check("worker sub clash 2 proxies", (txt.match(/- name:/g) || []).length === 2);
}

// 3. 公告：200 + login_notice
{
  const { res, buf } = await call("/api/v1/public-info");
  const j = JSON.parse(buf.toString("utf-8"));
  check("worker public-info 200 + notice", res.status === 200 && j.login_notice === "test notice", `got ${res.status}`);
}

// 4. 远程配置：字节精确等于 fixture（上游原字节）
{
  const { res, buf } = await call("/client-config.json");
  check("worker config 200", res.status === 200, `got ${res.status}`);
  check("worker config byte-exact", buf.equals(FIXTURE), `got ${buf.length} bytes, want ${FIXTURE.length}`);
}

// 5. 其它路径 404
{
  const { res } = await call("/api/v1/settings");
  check("worker unknown path 404", res.status === 404, `got ${res.status}`);
}

// 6. 未配 CONFIG_UPSTREAM 时 /client-config.json 404（不回源、不报错 500）
{
  const req = new Request("https://gw.example.com/client-config.json");
  const res = await worker.fetch(req, { ORIGIN: STUB }, {});
  check("worker config without upstream 404", res.status === 404, `got ${res.status}`);
}

console.log(`worker-test: ${pass} passed, ${fail} failed`);
process.exit(fail ? 1 : 0);
