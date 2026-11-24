/* relay-node 面板前端：原生 JS，无外部依赖。所有数据走 /api/v1/*（契约 §3）。 */
'use strict';

/* ---------- 工具 ---------- */
const $ = (s, r) => (r || document).querySelector(s);
const $$ = (s, r) => Array.from((r || document).querySelectorAll(s));
// 安全绑定：元素不存在时静默跳过，绝不抛错（新旧 index.html 混用时新 app.js
// 引用的新元素可能不在旧页面里；单个绑定失败也不能拖垮后面的初始化）。
// on() 查整个 document；onIn() 在给定的根（弹窗/分页容器）里查。
function on(sel, ev, fn, root) { const el = $(sel, root); if (el) el.addEventListener(ev, fn); return el; }
function onIn(root, sel, ev, fn) { return on(sel, ev, fn, root); }
const esc = (v) => String(v == null ? '' : v).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

function toast(msg, kind) {
  const el = document.createElement('div');
  el.className = 'toast' + (kind ? ' ' + kind : '');
  el.textContent = msg;
  const wrap = $('#toast-wrap');
  if (!wrap) return; // 容器缺失时静默丢弃，绝不抛错
  wrap.appendChild(el);
  setTimeout(() => el.remove(), 4500);
}
function fmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + ' B';
  const u = ['KB', 'MB', 'GB', 'TB'];
  let v = n, i = -1;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return v.toFixed(v >= 100 ? 0 : 2) + ' ' + u[i];
}
function fmtDur(sec) {
  sec = Math.max(0, Math.floor(Number(sec) || 0));
  const d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60);
  if (d > 0) return d + ' 天 ' + h + ' 小时';
  if (h > 0) return h + ' 小时 ' + m + ' 分钟';
  return m + ' 分钟';
}
function fmtDate(s) {
  if (!s) return '—';
  const d = new Date(s);
  return isNaN(d) ? String(s) : d.toLocaleDateString('zh-CN') + ' ' + d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
}
function daysLeftText(expiry) {
  if (!expiry) return { text: '永久', cls: 'grey', days: null };
  const days = Math.ceil((new Date(expiry) - Date.now()) / 86400000);
  if (days < 0) return { text: '已到期 ' + Math.abs(days) + ' 天', cls: 'red', days };
  if (days === 0) return { text: '今天到期', cls: 'red', days };
  return { text: '剩余 ' + days + ' 天', cls: days <= 7 ? 'red' : days <= 30 ? 'yellow' : 'green', days };
}
function disabledReasonText(r) {
  // v1.4.3：手动停用的原因原文就是“手动停用”（disableUserForwardsManual
  // 传的字面量），老代码里的 'manual' 也认；其它原因沿用旧映射。
  return r === 'expired' ? '已到期' : r === 'traffic' ? '流量用完' : r === 'both' ? '流量用完且已到期' : r === 'manual' || r === '手动停用' ? '手动停用' : '';
}
function copyText(text, btn) {
  const done = () => { toast('已复制'); if (btn) { const t = btn.textContent; btn.textContent = '已复制 ✓'; setTimeout(() => { btn.textContent = t; }, 1500); } };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(done).catch(() => fallbackCopy(text, done));
  } else fallbackCopy(text, done);
}
function fallbackCopy(text, done) {
  const ta = document.createElement('textarea');
  ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
  document.body.appendChild(ta); ta.select();
  // execCommand 返回 false 或抛错都算复制失败，要如实提示，不能报“已复制”：
  // 面板常是 IP＋明文 HTTP 访问，navigator.clipboard 在非安全上下文里不可用，
  // 兜底这条路就是复制成败的最后一道关。
  let ok = false;
  try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
  if (ok) done(); else toast('复制失败，请手动复制', 'err');
  ta.remove();
}
// 复制二维码图片到剪贴板（需要浏览器支持 ClipboardItem）；不支持就提示改用下载
function copyImage(dataURI, btn) {
  const done = () => { toast('二维码图片已复制'); if (btn) { const t = btn.textContent; btn.textContent = '已复制 ✓'; setTimeout(() => { btn.textContent = t; }, 1500); } };
  const fail = () => toast('浏览器不支持复制图片，请用下载保存', 'err');
  if (!window.ClipboardItem || !navigator.clipboard || !navigator.clipboard.write) { fail(); return; }
  fetch(dataURI).then((r) => r.blob()).then((blob) =>
    navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })]).then(done, fail)
  ).catch(fail);
}
// 下载二维码图片为 PNG 文件（走 blob URL，各浏览器都认）
function downloadImage(dataURI, name) {
  fetch(dataURI).then((r) => r.blob()).then((blob) => {
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob); a.download = name || '二维码.png'; a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 5000);
    toast('二维码图片已开始下载');
  }).catch(() => toast('下载失败，请重试', 'err'));
}
// 历史记录导出：纯文本，一行一条链接；每组先中转（/任意门）后直连，组与组之间空一行
function linksExportText(groups) {
  const parts = (groups || []).map((g) => (g.links || []).map((lk) => lk.link).join('\n')).filter((t) => t !== '');
  if (!parts.length) return '';
  return parts.join('\n\n') + '\n';
}
function linksExportFileName() {
  const d = new Date();
  const p2 = (n) => String(n).padStart(2, '0');
  return '节点链接-' + d.getFullYear() + p2(d.getMonth() + 1) + p2(d.getDate()) + '.txt';
}
function downloadTextFile(name, text) {
  const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob); a.download = name; a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 5000);
  toast('已导出 ' + name, 'ok');
}
function downloadFile(name, text) {
  const blob = new Blob([text], { type: 'application/json' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob); a.download = name; a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 5000);
}

/* ---------- API ---------- */
async function api(path, opts, noAuthRedirect) {
  const res = await fetch(path, Object.assign({ headers: { 'Content-Type': 'application/json' } }, opts || {}));
  let data = null;
  try { data = await res.json(); } catch (e) { /* 非 JSON 响应 */ }
  // 登录请求本身的 401 是“密码错了”，不是会话过期：不走过期分支，让服务端文案（账号或密码不对）原样透到登录框
  if (res.status === 401 && !noAuthRedirect) { showLogin(); throw new Error('登录已过期，请重新登录'); }
  if (res.status === 403) throw new Error((data && data.error) || '无权限（403）：当前 IP 不在白名单内，或会话无效');
  if (res.status === 409) throw new Error((data && data.error) || '正在操作，请稍等（409）');
  if (res.status === 429) throw new Error((data && data.error) || '请求过于频繁或登录失败过多被锁定，请稍后再试（429）');
  if (!res.ok) throw new Error((data && data.error) || ('请求失败（HTTP ' + res.status + '）'));
  if (data && data.ok === false) throw new Error(data.error || '操作失败');
  return data || {};
}
async function apiPost(path, body, noAuthRedirect) { return api(path, { method: 'POST', body: JSON.stringify(body || {}) }, noAuthRedirect); }
// apiPostForm: multipart 上传（不设 Content-Type，让浏览器自己带 boundary，
// 否则后端收不到文件）。401 照样当会话过期处理。
async function apiPostForm(path, formData) {
  const res = await fetch(path, { method: 'POST', body: formData });
  let data = null;
  try { data = await res.json(); } catch (e) { /* 非 JSON 响应 */ }
  if (res.status === 401) { showLogin(); throw new Error('登录已过期，请重新登录'); }
  if (!res.ok) throw new Error((data && data.error) || ('请求失败（HTTP ' + res.status + '）'));
  if (data && data.ok === false) throw new Error(data.error || '操作失败');
  return data || {};
}

/* ---------- 弹窗 ---------- */
// 统一弹窗（v1.3.21）：全面板唯一一种弹窗样式——同一遮罩、同一卡片、同一关闭
// 方式（右上角 × 按钮 + 点遮罩关闭 + Esc 关闭）。内容包在 .modal-body 里；
// 弹窗里再有异步改写（如体检结果、中央详情）只改 .modal-body 或里面的子节点，
// 绝不整块替换 .modal，保证 × 按钮不被洗掉。
function openModal(html, wide) {
  const root = $('#modal-root');
  if (!root) return null; // 弹窗容器缺失时直接放弃，绝不抛错
  root.innerHTML = '<div class="modal-backdrop"><div class="modal' + (wide ? ' wide' : '') + '" role="dialog" aria-modal="true">' +
    '<button class="modal-x" type="button" aria-label="关闭弹窗">×</button><div class="modal-body">' + html + '</div></div></div>';
  onIn(root, '.modal-backdrop', 'click', (e) => { if (e.target.classList.contains('modal-backdrop')) closeModal(); });
  onIn(root, '.modal-x', 'click', () => closeModal());
  return $('.modal', root);
}
function closeModal() { const r = $('#modal-root'); if (r) r.innerHTML = ''; }
function confirmModal(title, bodyHtml, okLabel, danger) {
  return new Promise((resolve) => {
    const m = openModal('<h2>' + esc(title) + '</h2><div>' + bodyHtml + '</div>' +
      '<div class="btn-row"><button class="btn ' + (danger ? 'danger' : '') + '" id="cf-ok" type="button">' + esc(okLabel || '确认') + '</button>' +
      '<button class="btn secondary" id="cf-cancel" type="button">取消</button></div>');
    onIn(m, '#cf-ok', 'click', () => { closeModal(); resolve(true); });
    onIn(m, '#cf-cancel', 'click', () => { closeModal(); resolve(false); });
  });
}

/* ---------- 主题 ---------- */
function applyTheme(mode) {
  const root = document.documentElement;
  if (mode === 'auto') {
    root.dataset.theme = window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  } else root.dataset.theme = mode;
  const btn = $('#theme-btn');
  if (btn) btn.textContent = root.dataset.theme === 'dark' ? '☀️ 浅色' : '🌙 深色';
}
function initTheme() {
  const saved = localStorage.getItem('relay-panel-theme');
  applyTheme(saved || 'auto');
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if (!localStorage.getItem('relay-panel-theme')) applyTheme('auto');
  });
  on('#theme-btn', 'click', () => {
    const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
    localStorage.setItem('relay-panel-theme', next);
    applyTheme(next);
  });
}

/* ---------- 导航 / 路由 ---------- */
const PAGES = [
  { id: 'overview', title: '总览', icon: '🏠' },
  { id: 'add', title: '添加落地', icon: '➕' },
  { id: 'manage', title: '落地管理', icon: '🗂️' },
  { id: 'nodes', title: '节点', icon: '🖧' },
  { id: 'links', title: '节点链接', icon: '🔗' },
  { id: 'certs', title: '证书', icon: '🔒' },
  { id: 'service', title: '服务与日志', icon: '⚙️' },
  { id: 'settings', title: '设置', icon: '🔧' },
  { id: 'firewall', title: '端口管理', icon: '🧱' },
  { id: 'traffic', title: '流量', icon: '📊' },
  { id: 'central', title: '集中管理', icon: '🖥️' },
];
let currentPage = 'overview';
let landingsCache = [];
let statusCache = null;
let settingsCache = null;
let logsTimer = null;
// 管理面板模式（v1.2.8）：true = 这台面板只做集中管理；本地页全部隐藏，
// 只留集中管理与设置；由 /api/v1/me 下发，设置页可切换（切完整页刷新）。
let managerOnly = false;
const MANAGER_PAGES = ['central', 'nodes', 'settings'];
function visiblePages() {
  if (!managerOnly) return PAGES;
  // 管理面板模式：按 MANAGER_PAGES 的顺序排（集中管理在前，设置在后），
  // 不能直接 filter PAGES——PAGES 里设置排在集中管理前面，导航会先显示设置。
  return MANAGER_PAGES.map((id) => PAGES.find((p) => p.id === id)).filter(Boolean);
}

function buildNav() {
  const side = $('#side-nav');
  if (!side) return; // 导航容器缺失时直接跳过
  side.innerHTML = '';
  visiblePages().forEach((p) => {
    const b1 = document.createElement('button');
    b1.type = 'button'; b1.className = 'nav-item'; b1.dataset.page = p.id;
    b1.innerHTML = '<span class="ico">' + p.icon + '</span><span>' + esc(p.title) + '</span>';
    b1.addEventListener('click', () => goto(p.id));
    side.appendChild(b1);
  });
}
/* ---------- 手机抽屉菜单 ---------- */
function openDrawer() {
  const bar = $('#sidebar');
  if (!bar) return;
  bar.classList.add('open');
  const scrim = $('#scrim');
  if (scrim) { scrim.classList.remove('hidden'); scrim.hidden = false; }
  const btn = $('#menu-btn');
  if (btn) btn.setAttribute('aria-expanded', 'true');
  document.body.style.overflow = 'hidden';
}
function closeDrawer() {
  const bar = $('#sidebar');
  if (!bar) return;
  bar.classList.remove('open');
  const scrim = $('#scrim');
  if (scrim) { scrim.classList.add('hidden'); scrim.hidden = true; }
  const btn = $('#menu-btn');
  if (btn) btn.setAttribute('aria-expanded', 'false');
  document.body.style.overflow = '';
}
function initDrawer() {
  on('#menu-btn', 'click', () => {
    const bar = $('#sidebar');
    if (bar && bar.classList.contains('open')) closeDrawer(); else openDrawer();
  });
  on('#sidebar-close', 'click', closeDrawer);
  on('#scrim', 'click', closeDrawer);
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeDrawer(); });
  window.addEventListener('resize', () => { if (window.innerWidth > 640) closeDrawer(); });
}
function goto(page) {
  closeDrawer(); // 手机上选了页面就收起抽屉；桌面端是空操作
  // 管理面板模式下本地页不存在：直接跳到集中管理
  if (managerOnly && !MANAGER_PAGES.includes(page)) page = 'central';
  currentPage = page;
  $$('.page').forEach((s) => s.classList.toggle('active', s.id === 'page-' + page));
  $$('.nav-item').forEach((b) => b.classList.toggle('active', b.dataset.page === page));
  const def = PAGES.find((p) => p.id === page);
  const title = $('#page-title');
  if (title) title.textContent = def ? def.title : '总览';
  if (location.hash !== '#' + page) history.replaceState(null, '', '#' + page);
  window.scrollTo(0, 0);
  loadPage(page);
}
function loadPage(page) {
  stopLogsTimer();
  if (page === 'overview') return loadOverview();
  else if (page === 'manage') return loadManage();
  else if (page === 'nodes') { loadNodes(); return loadNodeCerts(); }
  else if (page === 'links') return loadLinks();
  else if (page === 'certs') return loadCerts();
  else if (page === 'service') return loadService();
  else if (page === 'settings') return loadSettingsPage();
  else if (page === 'firewall') return loadFirewall();
  else if (page === 'traffic') return loadTrafficPage();
  else if (page === 'central') return loadCentral();
  return Promise.resolve();
}
function stopLogsTimer() { if (logsTimer) { clearInterval(logsTimer); logsTimer = null; } }

/* ---------- 登录 ---------- */
function hideLoading() {
  const lv = $('#loading-view');
  if (lv) { lv.classList.add('hidden'); lv.hidden = true; }
}
function showLogin() {
  // class 与 hidden 属性一起切：只靠 class 的话，style.css 加载失败时两边会堆在一起
  hideLoading();
  const app = $('#app'), login = $('#login-view');
  if (app) { app.classList.add('hidden'); app.hidden = true; }
  if (login) { login.classList.remove('hidden'); login.hidden = false; }
  loadLoginPublic();
}
function showApp() {
  hideLoading();
  const app = $('#app'), login = $('#login-view');
  if (login) { login.classList.add('hidden'); login.hidden = true; }
  if (app) { app.classList.remove('hidden'); app.hidden = false; }
}

// 登录页公开信息（v1.3.21）：公告与签名走公开接口读，不走登录会话；
// 公告用 textContent 输出（纯文本，不解析 HTML），没设置就不显示；
// 签名设置了就替换副标题，没设置就留服务端渲染的默认文案不动。
// 拉不到（网络问题/接口异常）就静默跳过，不影响登录。
async function loadLoginPublic() {
  const noticeBox = $('#login-notice'), sub = $('#login-sub');
  if (!noticeBox && !sub) return;
  let d = null;
  try {
    const r = await fetch('/api/v1/public-info');
    if (!r.ok) return;
    d = await r.json();
  } catch (e) { return; }
  if (!d) return;
  if (noticeBox) {
    const notice = (d.login_notice || '').trim();
    if (!notice) { noticeBox.classList.add('hidden'); noticeBox.hidden = true; }
    else { noticeBox.textContent = d.login_notice; noticeBox.classList.remove('hidden'); noticeBox.hidden = false; }
  }
  if (sub) {
    const sig = (d.login_signature || '').trim();
    if (sig) sub.textContent = d.login_signature;
  }
}
// 显隐切换：class 与 hidden 属性一起切（只靠 class 的话，style.css 加载失败时会堆在一起）
function setVisible(el, show) {
  if (!el) return;
  el.classList.toggle('hidden', !show);
  el.hidden = !show;
}
// 刷新当前页（弹窗里的续期/续量/编辑等操作完调它：人在哪页就刷哪页，不硬跳管理页）
function refreshCurrentPage() { return loadPage(currentPage); }
// 落地显示名：有名称用名称，没有用落地地址（后端 status 的 name 字段已按这个规则给）
function displayNameOf(l) { return (l && (l.name || l.cert_domain || l.domain || l.landing)) || ''; }
async function checkMe() {
  try {
    const me = await api('/api/v1/me');
    const userEl = $('#side-user');
    if (userEl) userEl.textContent = me.username || 'admin';
    if (me.version) { const verEl = $('#side-version'); if (verEl) verEl.textContent = me.version; }
    managerOnly = !!me.manager_only;
    buildNav(); // 模式定了才建导航：管理面板只显示集中管理与设置
    showApp();
    const hash = (location.hash || '').replace('#', '');
    goto(visiblePages().some((p) => p.id === hash) ? hash : (managerOnly ? 'central' : 'overview'));
    return true;
  } catch (e) {
    showLogin();
    return false;
  }
}
// 整页重载的小包装：生产行为与直接调 location.reload() 完全一致，
// 包一层只是方便 jsdom 测试截住调用再断言（jsdom 的 location.reload
// 是不可重定义的自有属性，没法直接打桩）。
function reloadPage() { location.reload(); }
function initLogin() {
  on('#login-form', 'submit', async (e) => {
    e.preventDefault();
    const username = $('#login-user').value.trim();
    const password = $('#login-pass').value;
    // 二次验证开着时，第二步才填验证码：第一步服务端返回 need_totp，
    // 展开验证码输入框，等用户再提交一次（账号密码留在框里，不用重填）。
    const totpRow = $('#login-totp-row');
    const code = (totpRow && !totpRow.hidden) ? $('#login-totp').value.trim() : '';
    if (!username || !password) { $('#login-error').textContent = '请输入用户名和密码'; $('#login-error').classList.remove('hidden'); return; }
    const btn = $('#login-btn'); btn.disabled = true; btn.textContent = '登录中…';
    $('#login-error').classList.add('hidden'); $('#login-locked').classList.add('hidden');
    try {
      const r = await apiPost('/api/v1/login', { username, password, code: code || undefined }, true);
      if (r && r.need_totp) {
        setVisible(totpRow, true);
        $('#login-totp').focus();
        return;
      }
      $('#login-pass').value = '';
      const t = $('#login-totp'); if (t) t.value = '';
      // 登录成功后重载一次页面：标签页里跑的可能是升级前的旧版 app.js
      //（index.html/app.js 虽然都发 no-cache，但只对新请求有效，管不到
      // 已经跑起来的旧 bundle）。重载后浏览器重新拉最新版；重载后会话
      // 已建立，不会再走登录提交，不会循环。need_totp 那路（只展开验证
      // 码框）与登录失败不走这里，不重载。
      if (await checkMe()) reloadPage();
    } catch (err) {
      const msg = String(err.message || '');
      if (msg.indexOf('429') >= 0 || msg.indexOf('锁定') >= 0 || msg.indexOf('locked') >= 0) $('#login-locked').classList.remove('hidden');
      const box = $('#login-error');
      if (msg.indexOf('验证码') >= 0) box.textContent = '验证码不对，请重试';
      else if (msg.indexOf('401') >= 0 || msg.indexOf('账号或密码') >= 0 || msg.indexOf('用户名或密码') >= 0) box.textContent = '用户名或密码错误，请重试';
      else box.textContent = msg;
      box.classList.remove('hidden');
    } finally { btn.disabled = false; btn.textContent = '登录'; }
  });
  on('#logout-btn', 'click', async () => {
    try { await apiPost('/api/v1/logout', {}); } catch (e) { /* 忽略 */ }
    showLogin();
  });
}

/* ---------- 状态灯 / 用量条 ---------- */
function statusBadge(l) {
  if (l.disabled) return '<span class="badge grey">已停用' + (disabledReasonText(l.disabled_reason) ? '（' + disabledReasonText(l.disabled_reason) + '）' : '') + '</span>';
  if (l.online === false) return '<span class="badge red">离线</span>';
  if (l.online === true) return '<span class="badge green">在线</span>';
  return '<span class="badge grey">未知</span>';
}
/* ---------- 落地延迟显示（v1.1.11） ---------- */
// 在线只显示毫秒数（延迟高不高不做红黄判定，避免误判），后面跟上次探测时间；
// 离线标红并写清失败原因（超时 / 拒绝连接 / DNS 解析失败等），后面同样跟上次探测时间；
// 还没探测过显示“尚未探测”。
function latencyHtml(l) {
  const when = l.latency_checked_at ? ' <span class="small">（上次探测 ' + esc(fmtDate(l.latency_checked_at)) + '）</span>' : '';
  if (l.online === true) return esc(l.latency_ms == null ? '—' : l.latency_ms + ' ms') + when;
  if (l.online === false) return '<span style="color:var(--red)">' + esc(l.latency_error || '离线') + '</span>' + when;
  return '— <span class="small">（尚未探测）</span>';
}
function trafficBarHtml(l) {
  const t = l.traffic || {};
  // v1.3.32：agent 在线时流量是节点侧推来的总量，标来源。
  const src = l.metering_source === 'agent' ? ' · 节点侧' : '';
  // v1.3.40：倍率不是 1 时标出来（订阅用量已按倍率折算）。
  const mult = (l.traffic_multiplier && l.traffic_multiplier !== 1) ? ' · 倍率 ×' + l.traffic_multiplier : '';
  if (!t.limit_bytes) return '<div class="bar-row"><span>流量：不限' + src + mult + '</span><span>' + fmtBytes(t.used_bytes || 0) + ' 已用</span></div>';
  const pct = Math.min(100, Number(t.pct) || 0);
  const cls = pct >= 100 ? 'bad' : pct >= 80 ? 'warn' : '';
  const mode = t.limit_mode === 'monthly' ? '按月' : '总量';
  return '<div class="bar-row"><span>流量（' + mode + '）' + src + mult + '</span><span>' + pct + '% · ' + fmtBytes(t.used_bytes || 0) + ' / ' + fmtBytes(t.limit_bytes) + '</span></div>' +
    '<div class="bar"><i class="' + cls + '" style="width:' + pct + '%"></i></div>';
}
function certBadge(days) {
  if (days == null) return '<span class="badge grey">未知</span>';
  if (days < 0) return '<span class="badge red">已过期 ' + Math.abs(days) + ' 天</span>';
  if (days < 7) return '<span class="badge red">剩余 ' + days + ' 天</span>';
  if (days < 30) return '<span class="badge yellow">剩余 ' + days + ' 天</span>';
  return '<span class="badge green">剩余 ' + days + ' 天</span>';
}
function linksHtml(links, qr) {
  if (!links || !links.length) return '<p class="muted">暂无链接。</p>';
  // 组内链接太多时只展开前 6 条，其余收起，避免一张卡片无限长
  const SHOW = 6;
  let h = '';
  links.forEach((lk, i) => {
    if (i === SHOW) h += '<div class="extra-links hidden">';
    h += '<div class="link-box"><div class="small">' + esc(lk.label || '链接') + '</div><code>' + esc(lk.link) + '</code>' +
      '<div class="btn-row"><button class="btn secondary sm" type="button" data-copy="' + esc(lk.link) + '">复制链接</button></div></div>';
  });
  if (links.length > SHOW) h += '</div><div class="btn-row"><button class="btn secondary sm" type="button" data-expand-links="' + (links.length - SHOW) + '">展开其余 ' + (links.length - SHOW) + ' 条链接</button></div>';
  if (qr) {
    h += '<div class="qr-wrap">';
    Object.keys(qr).forEach((k) => {
      if (qr[k]) h += '<figure data-qr="' + esc(qr[k]) + '"><img src="' + esc(qr[k]) + '" alt="' + esc(k) + ' 二维码"><figcaption>' + esc(k) + ' 二维码</figcaption>' +
        '<div class="btn-row"><button class="btn secondary sm" type="button" data-copy-img>复制图片</button>' +
        '<button class="btn secondary sm" type="button" data-download-img="' + esc(qrFileName(k)) + '">下载图片</button></div></figure>';
    });
    h += '</div>';
  }
  return h;
}
// 二维码下载文件名：标签只留字母数字中文横线，如 中转-二维码.png
function qrFileName(label) {
  const s = String(label || '链接').replace(/[^\w\u4e00-\u9fa5-]+/g, '');
  return (s || '链接') + '-二维码.png';
}
// 事件委托：所有 data-copy 按钮
document.addEventListener('click', (e) => {
  const b = e.target.closest('[data-copy]');
  if (b) copyText(b.dataset.copy, b);
  const ci = e.target.closest('[data-copy-img]');
  if (ci) { const f = ci.closest('figure'); if (f && f.dataset.qr) copyImage(f.dataset.qr, ci); }
  const di = e.target.closest('[data-download-img]');
  if (di) { const f = di.closest('figure'); if (f && f.dataset.qr) downloadImage(f.dataset.qr, di.dataset.downloadImg); }
  const g = e.target.closest('[data-goto]');
  if (g) goto(g.dataset.goto);
  const x = e.target.closest('[data-expand-links]');
  if (x) {
    const box = x.closest('.card, .central-link-body, .modal-body');
    const extra = box ? box.querySelector('.extra-links') : null;
    if (!extra) return;
    const nowHidden = extra.classList.toggle('hidden');
    x.textContent = nowHidden ? '展开其余 ' + x.dataset.expandLinks + ' 条链接' : '收起';
  }
});

/* ---------- 总览 ---------- */
async function loadOverview() {
  loadMachineCard(); // 机器状态卡片独立加载，总览其他部分失败也不影响它
  try {
    const s = await api('/api/v1/status');
    statusCache = s;
    landingsCache = s.landings || [];
    const r = s.realm || {};
    $('#overview-stats').innerHTML =
      statCard('realm 状态', r.running ? '<span class="dot green"></span>运行中' : '<span class="dot red"></span>未运行', '版本 ' + esc(r.version || '—')) +
      statCard('已运行时间', fmtDur(r.uptime_seconds || 0), 'BBR：' + (r.bbr ? '已开启' : '未开启')) +
      statCard('落地数量', String(landingsCache.length) + ' 个', '分流：80 ' + (s.share80 ? '分流中' : '直通') + ' / 443 ' + (s.share443 ? '分流中' : '直通')) +
      statCard('面板版本', esc(s.panel_version || '—'), '端口默认 11111，可在设置里改');
    renderLandingCards();
    // v1.3.20：落地列表搜索（输入即过滤，回到第 1 页）。
    wireListSearch('#landing-search', (q) => { landingSearch = q; overviewPage = 1; renderLandingCards(); });
    // 空状态文案：中转还没装（status 带 note）时提示加第一个落地会自动装中转；
    // 中转已装但一个落地都没有时只提示去添加落地。
    if (!landingsCache.length) {
      if (s.note) {
        $('#overview-empty').innerHTML = '中转还没装好，也没有落地节点。<br>去「添加落地」加第一个落地，中转会自动装好。<br><button class="btn" type="button" data-goto="add" style="margin-top:10px">＋ 添加第一个落地</button>';
      } else {
        $('#overview-empty').innerHTML = '还没有落地节点，去「添加落地」加第一个吧。';
      }
    }
    if (s.panel_version) $('#side-version').textContent = s.panel_version;
  } catch (e) {
    $('#overview-stats').innerHTML = '<div class="card"><div class="error-box">总览加载失败：' + esc(e.message) + '</div></div>';
  }
  // 更新横幅（上次更新后的体检结果）
  try {
    const st = await api('/api/v1/settings');
    settingsCache = st;
    if (st.update_available) {
      $('#update-banner').innerHTML = '<div class="warn-box">发现新版本 ' + esc(st.latest_version || '') + '（当前 ' + esc(st.version || '') + '），去「设置 → 检查更新」可以更新。更新只重启面板，不影响转发。</div>';
    } else $('#update-banner').innerHTML = '';
  } catch (e) { /* 设置读不到不阻塞总览 */ }
}
function statCard(label, value, sub) {
  return '<div class="card"><div class="stat-label">' + esc(label) + '</div><div class="stat-num" style="font-size:22px">' + value + '</div><div class="small">' + esc(sub || '') + '</div></div>';
}
/* ---------- 机器状态卡片（总览顶部） ---------- */
async function loadMachineCard() {
  const wrap = $('#machine-card');
  if (!wrap) return;
  try {
    const m = await api('/api/v1/monitor');
    renderMachineCard(m);
  } catch (e) { wrap.innerHTML = ''; }
}
function renderMachineCard(m) {
  const wrap = $('#machine-card');
  if (!wrap) return;
  const st = (m && m.stats) || {};
  const alerts = (m && m.alerts) || {};
  const row = (label, pct, sub, key) => {
    const lvl = alerts[key] || 'ok';
    const badge = lvl === 'critical' ? '<span class="badge red">严重</span>' : lvl === 'warn' ? '<span class="badge yellow">警告</span>' : '<span class="badge green">正常</span>';
    const cls = lvl === 'critical' ? 'bad' : lvl === 'warn' ? 'warn' : '';
    const shown = pct == null ? '—' : (Math.round(pct * 10) / 10) + '%';
    return '<div class="card"><div style="display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap"><div class="stat-label">' + esc(label) + '</div>' + badge + '</div>' +
      '<div class="stat-num" style="font-size:22px">' + shown + '</div>' +
      '<div class="small">' + esc(sub || '') + '</div>' +
      '<div class="bar"><i class="' + cls + '" style="width:' + Math.min(100, Number(pct) || 0) + '%"></i></div></div>';
  };
  const diskSub = '根分区：共 ' + fmtBytes(st.disk_total_bytes || 0) + '，可用 ' + fmtBytes(st.disk_free_bytes || 0);
  const memSub = '共 ' + fmtBytes(st.mem_total_bytes || 0) + '，可用 ' + fmtBytes(st.mem_avail_bytes || 0);
  const cpuSub = '1 分钟平均负载 ' + (st.cpu_load1 == null ? '—' : st.cpu_load1) + ' / ' + (st.cpu_cores || '—') + ' 核';
  wrap.innerHTML = '<h2 style="margin-top:4px">机器状态</h2><div class="grid cols-3">' +
    row('磁盘（根分区）', st.disk_pct, diskSub, 'disk') +
    row('内存', st.mem_pct, memSub, 'mem') +
    row('CPU 负载（1 分钟平均负载 / 核心数）', st.cpu_pct, cpuSub, 'cpu') + '</div>';
}
// 紧凑卡片分页：每页 12 张（桌面 3 列 × 4 行），与列表分页每页 20 条区分开
const CARD_PAGE_SIZE = 12;
function paginateCards(list, page) {
  const total = list.length;
  const totalPages = Math.max(1, Math.ceil(total / CARD_PAGE_SIZE));
  const cur = Math.min(Math.max(page || 1, 1), totalPages);
  return { slice: list.slice((cur - 1) * CARD_PAGE_SIZE, cur * CARD_PAGE_SIZE), total, totalPages, cur };
}
// 小卡片一行流量文字：已用 / 上限（百分比），不限只显示已用
function trafficMiniText(l) {
  const t = l.traffic || {};
  // v1.3.32：agent 在线时卡片流量是节点侧推来的总量，标来源免得和中转侧混淆。
  const src = l.metering_source === 'agent' ? ' · 节点侧' : '';
  // v1.3.40：倍率不是 1 时标出来。
  const mult = (l.traffic_multiplier && l.traffic_multiplier !== 1) ? ' · 倍率 ×' + l.traffic_multiplier : '';
  if (!t.limit_bytes) return '不限 · 已用 ' + fmtBytes(t.used_bytes || 0) + src + mult;
  return fmtBytes(t.used_bytes || 0) + ' / ' + fmtBytes(t.limit_bytes) + '（' + (Math.min(100, Number(t.pct) || 0)) + '%）' + src + mult;
}
// 紧凑小卡片：只显示名称、在线状态、模式、证书剩余天数、流量已用/上限、到期；
// 点小卡片弹完整卡片弹窗；右上角小“编辑”按钮直接开编辑弹窗（点它不弹完整卡片）
function landingMiniCard(l, relayAddr) {
  const exp = daysLeftText(l.expiry);
  return '<div class="mini-card' + (l.online === false ? ' offline' : '') + '" data-mini="' + l.index + '" role="button" tabindex="0">' +
    '<div class="mini-top"><h3>' + esc(displayNameOf(l)) + '</h3>' + statusBadge(l) + '</div>' +
    '<div class="mini-row"><span>模式</span><b>' + (l.mode === 'door' ? '任意门' : '标准') + '</b></div>' +
    '<div class="mini-row"><span>证书</span><b>' + certBadge(l.cert_days_left) + '</b></div>' +
    '<div class="mini-row"><span>流量</span><b>' + esc(trafficMiniText(l)) + '</b></div>' +
    '<div class="mini-row"><span>到期</span><b><span class="badge ' + exp.cls + '">' + esc(exp.text) + '</span></b></div>' +
    '<div class="btn-row"><button class="btn secondary sm" type="button" data-mini-edit="' + l.index + '">编辑</button>' +
    '<button class="btn secondary sm" type="button" data-mini-open="' + l.index + '">详情</button>' +
    '<button class="btn secondary sm" type="button" data-mini-sub="' + l.index + '">订阅</button></div></div>';
}
// 完整卡片内容（弹窗里用）：全部字段 + 链接/二维码 + 全部操作按钮
function landingDetailHtml(l, relayAddr) {
  const exp = daysLeftText(l.expiry);
  const ports = (l.ports || []).map((p) => esc(p.proto) + ' ' + esc(p.listen) + '→' + esc(p.remote)).join('、') || '—';
  let h = '<div style="display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap">' +
    '<h2>' + esc(displayNameOf(l)) + '</h2>' + statusBadge(l) + '</div>' +
    '<div class="kv"><span>落地</span><b>' + esc(l.landing) + '</b></div>' +
    '<div class="kv"><span>监听端口</span><b>' + ports + '</b></div>' +
    '<div class="kv"><span>类型</span><b>' + esc(l.mode_note || '—') + '</b></div>' +
    '<div class="kv"><span>模式</span><b>' + modeText(l, relayAddr) + '</b></div>' +
    '<div class="kv"><span>延迟</span><b>' + latencyHtml(l) + '</b></div>' +
    '<div class="kv"><span>证书</span><b>' + certBadge(l.cert_days_left) + ' · ' + esc(l.renew_mode || '') + '</b></div>' +
    '<div class="kv"><span>到期</span><b><span class="badge ' + exp.cls + '">' + esc(exp.text) + '</span>' + (l.expiry ? '（' + esc(fmtDate(l.expiry)) + '）' : '') + '</b></div>' +
    '<div style="margin-top:10px">' + trafficBarHtml(l) + '</div>' +
    ((l.subnodes && l.subnodes.length) ? '<div class="small">子节点：' + l.subnodes.map((sn) => esc(sn.domain)).join('、') + '</div>' : '');
  if (l.subnodes && l.subnodes.length) {
    h += '<h3 style="margin-top:14px">子节点（同一落地机上的多个节点，共用一条转发）</h3>' +
      l.subnodes.map((sn) =>
        '<div class="kv"><span>🔹 ' + esc(sn.domain) + '</span><span><button class="btn ghost sm" type="button" data-del-sub="' + esc(sn.domain) + '" data-idx="' + l.index + '">删除该节点</button></span></div>').join('');
  }
  h += '<h3 style="margin-top:14px">节点链接</h3>';
  if (l.links && l.links.length) h += linksHtml(l.links, l.qr);
  else h += '<p class="muted">还没存原始节点链接，卡片上拼不出中转链接。</p><div class="btn-row"><button class="btn secondary sm" type="button" data-paste-orig="' + l.index + '">粘贴原始链接生成中转链接</button></div>';
  h += '<div class="btn-row">' +
    '<button class="btn secondary sm" type="button" data-addnode="' + l.index + '">＋ 给落地加节点</button>' +
    '<button class="btn secondary sm" type="button" data-sub-landing="' + l.index + '">订阅</button>' +
    '<button class="btn secondary sm" type="button" data-renew="' + l.index + '">续期</button>' +
    '<button class="btn secondary sm" type="button" data-addtraffic="' + l.index + '">续量</button>' +
    '<button class="btn secondary sm" type="button" data-trafficset="' + l.index + '">流量限制</button>' +
    '<button class="btn sm" type="button" data-edit="' + l.index + '">编辑</button>' +
    '<button class="btn secondary sm" type="button" data-goto="links">查看链接</button>' +
    '<button class="btn secondary sm" type="button" data-goto="certs">证书</button>' +
    '<button class="btn danger sm" type="button" data-del="' + l.index + '">删除（进回收站）</button></div>';
  return h;
}
// 点小卡片弹完整卡片弹窗；弹窗里的操作按钮走同一套函数
function showLandingDetail(l) {
  const relayAddr = (statusCache && statusCache.relay_addr) || '';
  const m = openModal(landingDetailHtml(l, relayAddr) +
    '<div class="btn-row"><button class="btn secondary" id="detail-close" type="button">关闭</button></div>');
  if (!m) return;
  onIn(m, '#detail-close', 'click', closeModal);
  $$('[data-addnode]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); addNodeWizard(Number(b.dataset.addnode)); }));
  $$('[data-sub-landing]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); const l = landingsCache.find((x) => x.index === Number(b.dataset.subLanding)); askSubModeForSelection([{ panel: 'local', landing: Number(b.dataset.subLanding) }], (l ? displayNameOf(l) : '落地') + ' 的订阅'); }));
  $$('[data-renew]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); renewLanding(Number(b.dataset.renew)); }));
  $$('[data-addtraffic]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); addTraffic(Number(b.dataset.addtraffic)); }));
  $$('[data-trafficset]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); trafficSettings(Number(b.dataset.trafficset)); }));
  $$('[data-edit]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); editLanding(Number(b.dataset.edit)); }));
  $$('[data-del]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); deleteLanding(Number(b.dataset.del)); }));
  $$('[data-paste-orig]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); pasteOrigLink(Number(b.dataset.pasteOrig)); }));
  $$('[data-del-sub]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); deleteSubnode(Number(b.dataset.idx), b.dataset.delSub); }));
  $$('[data-goto]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); goto(b.dataset.goto); }));
}
// 给一组小卡片容器接点击事件：点卡片弹详情，点“编辑”直接开编辑弹窗
function wireMiniCards(wrap) {
  $$('[data-mini]', wrap).forEach((card) => {
    card.addEventListener('click', (e) => {
      if (e.target.closest('[data-mini-edit]') || e.target.closest('[data-mini-open]') || e.target.closest('[data-mini-sub]')) return;
      const l = landingsCache.find((x) => x.index === Number(card.dataset.mini));
      if (l) showLandingDetail(l);
    });
    card.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        const l = landingsCache.find((x) => x.index === Number(card.dataset.mini));
        if (l) showLandingDetail(l);
      }
    });
  });
  $$('[data-mini-edit]', wrap).forEach((b) => b.addEventListener('click', (e) => { e.stopPropagation(); editLanding(Number(b.dataset.miniEdit)); }));
  $$('[data-mini-open]', wrap).forEach((b) => b.addEventListener('click', (e) => {
    e.stopPropagation();
    const l = landingsCache.find((x) => x.index === Number(b.dataset.miniOpen));
    if (l) showLandingDetail(l);
  }));
  $$('[data-mini-sub]', wrap).forEach((b) => b.addEventListener('click', (e) => {
    e.stopPropagation();
    const l = landingsCache.find((x) => x.index === Number(b.dataset.miniSub));
    askSubModeForSelection([{ panel: 'local', landing: Number(b.dataset.miniSub) }], (l ? displayNameOf(l) : '落地') + ' 的订阅');
  }));
}
let overviewPage = 1;
function modeText(l, relayAddr) {
  if (l.mode === 'door') return '任意门模式' + (relayAddr ? '（中转地址：' + esc(relayAddr) + '）' : '');
  return '标准模式';
}
let landingSearch = ''; // v1.3.20：落地列表搜索关键词（分页前先过滤）
function renderLandingCards(page) {
  if (page) overviewPage = page;
  const wrap = $('#landing-cards');
  $('#overview-empty').classList.toggle('hidden', landingsCache.length > 0);
  if (!landingsCache.length) { wrap.innerHTML = ''; return; }
  // v1.3.20：落地列表搜索（名称/地址/证书域名，分页前先过滤）。
  const filtered = landingsCache.filter((l) => matchSubText(landingSearch, [displayNameOf(l), l.landing, l.domain, l.cert_domain]));
  if (!filtered.length) { wrap.innerHTML = '<div class="empty">没有匹配的落地，换个关键词试试。</div>'; return; }
  const pg = paginateCards(filtered, overviewPage);
  overviewPage = pg.cur;
  const relayAddr = (statusCache && statusCache.relay_addr) || '';
  wrap.innerHTML = pg.slice.map((l) => landingMiniCard(l, relayAddr)).join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'ovcards');
  wireMiniCards(wrap);
  if (pg.totalPages > 1) wirePager(wrap, 'ovcards', pg.cur, pg.totalPages, (p) => renderLandingCards(p));
}

/* ---------- 一键体检 + 一键修复 ---------- */
// 体检结果走统一弹窗（v1.3.21）：以前是服务页行内卡片，现在点“体检”弹弹窗，
// 异步回来的结果只写弹窗里的 .modal-body，右上角 × 不会被洗掉。
function initCheck() {
  on('#check-btn', 'click', async () => {
    const m = openModal('<h2>体检结果</h2><p class="muted">体检中，请稍候…</p>');
    if (!m) return;
    const body = m.querySelector('.modal-body');
    try {
      const c = await apiPost('/api/v1/check', {});
      let h = '<h2>体检结果</h2>';
      (c.sections || []).forEach((sec) => {
        h += '<h3 style="margin-top:12px">' + esc(sec.name) + ' <span class="badge ' + (sec.status === 'pass' ? 'green' : sec.status === 'warn' ? 'yellow' : 'red') + '">' + esc(sec.status) + '</span></h3>';
        (sec.items || []).forEach((it) => {
          h += '<div class="kv"><span>' + (it.level === 'pass' ? '✅' : it.level === 'warn' ? '⚠️' : '❌') + ' ' + esc(it.message) + '</span></div>';
        });
      });
      const issues = c.issues || [];
      if (issues.length) {
        h += '<h3 style="margin-top:12px">发现的问题（一键修复）</h3>';
        issues.forEach((is, i) => {
          // v1.3.46：只有可修复的才画“一键修复”按钮；没有修复办法的
          // （fix=manual，如指纹不一致）改成“查看原因”按钮。
          const fixable = is.fix && is.fix !== 'manual';
          h += '<div class="kv"><span>❌ ' + esc(is.message || is.code) + '</span><span><button class="btn sm" type="button" data-fix="' + i + '">' + (fixable ? '一键修复' : '查看原因') + '</button></span></div>';
        });
        body.dataset.issues = JSON.stringify(issues);
      } else h += '<div class="ok-box" style="margin-top:12px">全部正常，没有发现问题。</div>';
      body.innerHTML = h;
      $$('[data-fix]', body).forEach((b) => b.addEventListener('click', () => {
        const is = JSON.parse(body.dataset.issues)[Number(b.dataset.fix)];
        if (is.fix && is.fix !== 'manual') fixIssue(is); else viewIssue(is);
      }));
    } catch (e) {
      body.innerHTML = '<div class="error-box">体检失败：' + esc(e.message) + '</div>';
    }
  });
}
// “查看原因”按钮（v1.3.46）：没有一键修复办法的问题，直接弹原因文案，
// 不发修复请求（以前点“一键修复”会调后端拿 400 / 误 toast“修复已执行”）。
function viewIssue(issue) {
  if (!issue) return;
  openModal('<h2>问题原因</h2><p>' + esc(issue.message || issue.code) + '</p><p class="muted">这个问题没有一键修复办法，请按上面原因手动处理。</p>');
}
async function fixIssue(issue) {
  if (!issue) return;
  if (issue.fix === 'manual_renew') { toast('该问题需要手动续签，请到「证书」页按六步引导操作'); goto('certs'); return; }
  if (issue.fix === 'choose_share') {
    const ok = await confirmModal('端口冲突', '<p>该问题需要选择分流方式。请到「添加落地」重新提交，提交时端口冲突会弹出方式选择。</p>', '去添加落地');
    if (ok) goto('add');
    return;
  }
  try {
    const r = await apiPost('/api/v1/fix', { issue_code: issue.code, landing: issue.landing });
    if (r.need_choice) { toast('需要先选择分流方式，请到「添加落地」重新提交'); goto('add'); return; }
    // v1.3.46：后端回 need_manual（tls_unreachable 定位到 realm 之外的原因等）
    // 时展示原因 + 指引，绝不再 toast“修复已执行”。
    if (r.need_manual) {
      await confirmModal('需要手动处理', '<p>' + esc(r.reason || issue.message || issue.code) + '</p>' + (r.guide ? '<p class="muted">指引：' + esc(r.guide) + '</p>' : ''), '知道了');
      return;
    }
    toast('修复已执行：' + (r.msg || issue.code), 'ok');
    loadOverview();
  } catch (e) { toast('修复失败：' + e.message, 'err'); }
}

/* ---------- 添加落地（含端口冲突方式选择） ---------- */
function initAddForm() {
  const preset = $('#add-preset');
  if (!preset) return; // 添加落地表单不在旧页面里时直接跳过
  const syncPreset = () => {
    const sbWrap = $('#sb-mode-wrap');
    if (sbWrap) sbWrap.classList.toggle('hidden', preset.value !== '2');
    const cpWrap = $('#custom-ports-wrap');
    if (cpWrap) cpWrap.classList.toggle('hidden', preset.value !== '3');
  };
  preset.addEventListener('change', syncPreset); syncPreset();
  const modeSel = $('#add-mode');
  const syncMode = () => {
    const w = $('#add-relay-addr-wrap');
    if (w) w.classList.toggle('hidden', !modeSel || modeSel.value !== 'door');
  };
  if (modeSel) { modeSel.addEventListener('change', syncMode); syncMode(); }
  on('#add-duration', 'change', (e) => {
    const w = $('#duration-custom-wrap');
    if (w) w.classList.toggle('hidden', e.target.value !== 'custom');
  });
  on('#add-form', 'submit', async (e) => {
    e.preventDefault();
    const payload = buildAddPayload();
    if (!payload) return;
    await submitAdd(payload, null);
  });
}
// 流量倍率取值（v1.3.40）：空着 = 1；须在 0.1–100 之间，否则报错返回 null。
function parseTrafficMultiplier(el) {
  if (!el) return 1;
  const raw = el.value.trim();
  if (raw === '') return 1;
  const v = Number(raw);
  if (!v || v < 0.1 || v > 100) { toast('流量倍率填 0.1–100，默认 1', 'err'); return null; }
  return v;
}
function buildAddPayload() {
  const landing = $('#add-landing').value.trim();
  const certDomain = $('#add-cert-domain').value.trim();
  if (!landing) { toast('请填写落地 IP 或域名', 'err'); return null; }
  if (!/^[A-Za-z0-9.-]+$/.test(landing)) { toast('落地 IP 或域名格式不正确', 'err'); return null; }
  if (certDomain && !/^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/.test(certDomain)) { toast('证书域名格式不正确，如 example.com', 'err'); return null; }
  const preset = $('#add-preset').value;
  let customPorts = '';
  if (preset === '3') {
    customPorts = $('#add-custom-ports').value.trim();
    if (!customPorts) { toast('自定义端口必填，如 443,8388', 'err'); return null; }
    if (!/^[\d,: ]+$/.test(customPorts)) { toast('自定义端口格式不正确，只能是数字、逗号和冒号', 'err'); return null; }
  }
  let duration = Number($('#add-duration').value) || 0;
  if ($('#add-duration').value === 'custom') {
    duration = Number($('#add-duration-custom').value) || 0;
    if (duration < 1 || duration > 3650) { toast('自定义天数请填 1–3650', 'err'); return null; }
  }
  const trafficGb = Number($('#add-traffic').value) || 0;
  if (trafficGb < 0) { toast('流量上限不能为负', 'err'); return null; }
  const speed = Number($('#add-speed').value) || 0;
  if (speed < 0) { toast('限速不能为负', 'err'); return null; }
  const mult = parseTrafficMultiplier($('#add-traffic-multiplier'));
  if (mult == null) return null;
  const mode = $('#add-mode') ? $('#add-mode').value : 'standard';
  const relayAddr = (mode === 'door' && $('#add-relay-addr')) ? $('#add-relay-addr').value.trim() : '';
  const name = $('#add-name') ? $('#add-name').value.trim() : '';
  if (name && name.length > 64) { toast('名称不能超过 64 字', 'err'); return null; }
  const origLink = $('#add-orig-link') ? $('#add-orig-link').value.trim() : '';
  if (origLink && !/^(vless|trojan|ss|vmess|hysteria2|hy2|tuic):\/\//.test(origLink)) { toast('原始节点链接格式不正确，需以 vless://、trojan://、ss://、vmess://、hysteria2:// 或 tuic:// 开头', 'err'); return null; }
  const linkName = $('#add-link-name') ? $('#add-link-name').value.trim() : '';
  if (linkName && linkName.length > 64) { toast('节点名称不能超过 64 字', 'err'); return null; }
  return {
    landing, preset, mode,
    name: name || undefined,
    orig_link: origLink || undefined,
    link_name: linkName || undefined,
    relay_addr: relayAddr || undefined,
    sb_mode: preset === '2' ? $('#add-sb-mode').value : undefined,
    custom_ports: customPorts || undefined,
    want_udp: $('#add-want-udp').checked,
    cert_domain: certDomain || undefined,
    cert_mode: $('#add-cert-mode') ? $('#add-cert-mode').value : undefined,
    duration_days: duration,
    traffic_limit_bytes: Math.round(trafficGb * 1073741824),
    traffic_limit_mode: trafficGb > 0 ? $('#add-traffic-mode').value : undefined,
    traffic_multiplier: mult,
    speed_limit_mbps: speed,
  };
}
async function submitAdd(payload, shareChoice) {
  const btn = $('#add-submit'); btn.disabled = true; btn.textContent = '添加中，请稍候…';
  try {
    const body = Object.assign({}, payload);
    if (shareChoice) { body.share_mode = shareChoice.mode; if (shareChoice.overrides) body.listen_overrides = shareChoice.overrides; }
    const r = await apiPost('/api/v1/landings', body);
    if (r.need_choice) { await showShareChoice(r, payload); return; }
    openModal('<h2>✅ 落地添加成功</h2>' +
      (r.door_note ? '<div class="warn-box">' + esc(r.door_note) + '</div>' : '') +
      ((r.qr && Object.keys(r.qr).length) ? '' : '<p class="muted">二维码生成失败，仅显示链接。</p>') +
      linksHtml(r.links, r.qr));
    toast('落地添加成功', 'ok');
    loadOverview();
  } catch (e) {
    const msg = String(e.message || '');
    // 后端也可能把 need_choice 放在错误体里
    toast('添加失败：' + msg, 'err');
  } finally { btn.disabled = false; btn.textContent = '提交并添加落地'; }
}
async function showShareChoice(resp, payload, submitFn) {
  // submitFn 可选：不传时走本地提交（submitAdd）；集中管理传自己的提交函数
  // （调主面板代理接口），复用同一个分流选择弹窗。
  const options = resp.options || [];
  const conflicts = (resp.conflicts || []).join('、') || '端口冲突';
  const m = openModal('<h2>端口冲突，请选择处理方式</h2>' +
    '<p>以下端口已被占用：<b>' + esc(conflicts) + '</b>。请选择一种方式继续添加：</p>' +
    '<div id="share-opts">' + options.map((o) =>
      '<label class="radio-card" data-opt="' + esc(o.id) + '"><input type="radio" name="share-mode" value="' + esc(o.id) + '"> ' +
      '<span class="opt-title">' + esc(o.title) + '</span><div class="opt-desc">' + esc(o.desc || '') + '</div></label>').join('') + '</div>' +
    '<div id="share-overrides" class="hidden"><label>高位监听端口 <span class="hint">方式 1 需要：把被占端口改到高位端口，如 80→10080、443→10443</span></label>' +
    '<input id="override-80" placeholder="80 → 10080（可空）"><input id="override-443" placeholder="443 → 10443（可空）" style="margin-top:8px"></div>' +
    '<div class="btn-row"><button class="btn" id="share-ok" type="button">按此方式继续添加</button>' +
    '<button class="btn secondary" id="share-cancel" type="button">取消</button></div>');
  $$('.radio-card', m).forEach((card) => card.addEventListener('click', () => {
    $$('.radio-card', m).forEach((c) => c.classList.remove('selected'));
    card.classList.add('selected');
    const v = card.dataset.opt;
    $('#share-overrides', m).classList.toggle('hidden', v !== '1');
  }));
  onIn(m, '#share-cancel', 'click', closeModal);
  onIn(m, '#share-ok', 'click', async () => {
    const sel = $('input[name="share-mode"]:checked', m);
    if (!sel) { toast('请先选择一种方式', 'err'); return; }
    const overrides = {};
    const o80 = $('#override-80', m).value.trim(), o443 = $('#override-443', m).value.trim();
    if (sel.value === '1') {
      if (o80) { const n = Number(o80); if (n < 1 || n > 65535) { toast('10080 这类高位端口请填 1–65535', 'err'); return; } overrides['80'] = n; }
      if (o443) { const n = Number(o443); if (n < 1 || n > 65535) { toast('10443 这类高位端口请填 1–65535', 'err'); return; } overrides['443'] = n; }
    }
    closeModal();
    const choice = { mode: sel.value, overrides: Object.keys(overrides).length ? overrides : undefined };
    if (submitFn) { await submitFn(choice); return; }
    await submitAdd(payload, choice);
  });
}

/* ---------- 落地管理 ---------- */
let managePage = 1;
async function loadManage(page) {
  if (page) managePage = page;
  const wrap = $('#manage-list');
  try {
    const s = await api('/api/v1/status');
    statusCache = s;
    landingsCache = s.landings || [];
    if (!landingsCache.length) { wrap.innerHTML = '<div class="empty">还没有落地节点。</div>'; }
    else {
      // 与总览同一套紧凑小卡片：点卡片弹完整卡片（子节点删除等操作都在弹窗里）
      const pg = paginateCards(landingsCache, managePage);
      managePage = pg.cur;
      const relayAddr = s.relay_addr || '';
      wrap.innerHTML = '<div class="landing-grid">' + pg.slice.map((l) => landingMiniCard(l, relayAddr)).join('') + '</div>' +
        pagerHtml(pg.total, pg.cur, pg.totalPages, 'mgcards');
      wireMiniCards(wrap);
      if (pg.totalPages > 1) wirePager(wrap, 'mgcards', pg.cur, pg.totalPages, (p) => loadManage(p));
    }
  } catch (e) {
    wrap.innerHTML = '<div class="card"><div class="error-box">加载失败：' + esc(e.message) + '</div></div>';
  }
  loadRecycle();
}
let recyclePage = 1;
async function loadRecycle(page) {
  if (page) recyclePage = page;
  const wrap = $('#recycle-list');
  try {
    const r = await api('/api/v1/recycle');
    const items = r.items || r.recycle || [];
    if (!items.length) { wrap.innerHTML = '<div class="empty">回收站是空的。</div>'; return; }
    const pg = paginate(items, recyclePage);
    recyclePage = pg.cur;
    wrap.innerHTML = pg.slice.map((it) => '<div class="card" style="margin-bottom:12px"><div class="kv"><span>落地</span><b>' + esc(it.landing || '') + '（' + esc(it.domain || '') + '）</b></div>' +
      '<div class="kv"><span>删除时间</span><b>' + esc(fmtDate(it.deleted_at)) + '</b></div>' +
      '<div class="kv"><span>可恢复至</span><b>' + esc(fmtDate(it.expires_at || it.restore_before)) + '</b></div>' +
      '<div class="btn-row"><button class="btn sm" type="button" data-restore="' + esc(it.id || it.recycle_id || '') + '">恢复</button>' +
      '<button class="btn danger sm" type="button" data-purge="' + esc(it.id || it.recycle_id || '') + '">彻底删除</button></div></div>').join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'recycle');
    $$('[data-restore]', wrap).forEach((b) => b.addEventListener('click', async () => {
      try { await apiPost('/api/v1/recycle/' + encodeURIComponent(b.dataset.restore) + '/restore', {}); toast('已恢复', 'ok'); loadManage(); }
      catch (e) { toast('恢复失败：' + e.message, 'err'); }
    }));
    $$('[data-purge]', wrap).forEach((b) => b.addEventListener('click', async () => {
      const ok = await confirmModal('彻底删除', '<p>彻底删除后该落地无法再从回收站恢复（只清面板记录）。确定吗？</p>', '彻底删除', true);
      if (!ok) return;
      try { await api('/api/v1/recycle/' + encodeURIComponent(b.dataset.purge), { method: 'DELETE' }); toast('已彻底删除', 'ok'); loadManage(); }
      catch (e) { toast('删除失败：' + e.message, 'err'); }
    }));
    if (pg.totalPages > 1) wirePager(wrap, 'recycle', pg.cur, pg.totalPages, (p) => loadRecycle(p));
  } catch (e) {
    wrap.innerHTML = '<div class="empty">回收站不可用：' + esc(e.message) + '</div>';
  }
}
/* ---------- 节点（轻量节点后端，v1.4.0 切片 1） ---------- */
function nodeHealthLabel(h) {
  if (h === 'online') return '在线';
  if (h === 'offline') return '离线';
  return '尚未入网';
}
// 健康圆点（v1.4.5）：在线绿点、离线红点、尚未入网灰点；与文字同义，
// 颜色只做一眼区分，不替代文字（色弱用户仍能读文字）。
// v1.4.8：在线绿点改成呼吸式（.dot.green.pulse，CSS 脉动动画），
// 列表与详情弹窗都走这个函数，一处改全覆盖；红点与灰点保持静态。
function nodeHealthDot(h) {
  if (h === 'online') return '<span class="dot green pulse"></span>';
  if (h === 'offline') return '<span class="dot red"></span>';
  return '<span class="dot grey"></span>';
}
// 列表健康标题（v1.4.6）：优先跟上报健康（backend_health.status），
// 而不是只看最后拉配置的时间（n.health）。agent 还在拉配置但 Xray
// 停了时，标题应显示红字“Xray 未运行”，而不是绿“在线”——
// 1.4.5 真机实测：详情已红、列表标题仍绿，两边打架。
function nodeListHealth(n) {
  const bh = n.backend_health || {};
  if (bh.status === 'online') return { status: 'online', label: '在线' };
  if (bh.status === 'offline') {
    if ((bh.detail || '').indexOf('Xray 未运行') === 0) return { status: 'offline', label: 'Xray 未运行' };
    return { status: 'offline', label: '离线' };
  }
  // 没上报过：回退到 LastSeen 口径（n.health）。
  if (n.health === 'online') return { status: 'online', label: '在线' };
  if (n.health === 'offline') return { status: 'offline', label: '离线' };
  return { status: 'never', label: '尚未入网' };
}
function nodeListHealthHtml(n) {
  const h = nodeListHealth(n);
  return nodeHealthDot(h.status) + esc(h.label);
}
let nodesCache = []; // v1.4.12：节点列表全量缓存（搜索／分页都在前端做，不新加后端接口）
let nodesPage = 1;
let nodesSearch = '';
let nodesPageSize = 10; // v1.4.12：每页条数，10／20／50，默认 10
// v1.4.14：列表视图切换（卡片↔列表）：节点与证书各记一个选择，
// localStorage 键与设置页／twin 模式同款 relay-panel-* 前缀；
// 读不到／值不对／隐私模式存不了都回默认卡片视图。
const NODES_VIEW_KEY = 'relay-panel-nodes-view';
const CERTS_VIEW_KEY = 'relay-panel-certs-view';
function loadListView(key) {
  try { const v = localStorage.getItem(key); return v === 'list' || v === 'card' ? v : 'card'; } catch (e) { return 'card'; }
}
function saveListView(key, v) {
  try { localStorage.setItem(key, v); } catch (e) { /* 存不了就每次回卡片 */ }
}
let nodesView = loadListView(NODES_VIEW_KEY);
let certsView = loadListView(CERTS_VIEW_KEY);
// syncViewToggle 切换按钮的文案与 aria-pressed：卡片视图时按钮写
// “列表视图”（点一下切过去），列表视图时写“卡片视图”。
function syncViewToggle(btnId, view) {
  const b = $(btnId);
  if (!b) return;
  b.textContent = view === 'list' ? '卡片视图' : '列表视图';
  b.setAttribute('aria-pressed', view === 'list' ? 'true' : 'false');
}
// applyListViewClass 列表容器在两种视图间切 class：卡片视图用
// .nodes-grid（方形小卡片网格），列表视图用 .list-view（紧凑行）。
function applyListViewClass(wrap, view) {
  if (!wrap) return;
  wrap.classList.toggle('nodes-grid', view === 'card');
  wrap.classList.toggle('list-view', view === 'list');
}
// 前端列表按指定条数切页（节点列表每页条数可选，不沿用 PAGE_SIZE=20；
// 总页数变少、当前页越界时自动落到最后一页，与 paginate 同口径）。
function paginateSize(list, page, size) {
  const total = list.length;
  const per = Math.max(1, Number(size) || 10);
  const totalPages = Math.max(1, Math.ceil(total / per));
  const cur = Math.min(Math.max(page || 1, 1), totalPages);
  return { slice: list.slice((cur - 1) * per, cur * per), total, totalPages, cur };
}
// 紧凑小卡片（v1.4.12）：照抄 landingMiniCard 那套 .mini-card（顶行
// 名字＋健康、下面几行 mini-row、底部 btn-row 四个小按钮），v1.4.13 起
// 排成方形小卡片网格（#nodes-list 加 nodes-grid 类，见 style.css）。
// 健康标题与圆点走 nodeListHealthHtml（1.4.6 口径：跟上报健康走，
// Xray 未运行时标题就是“Xray 未运行”；圆点与文字的相邻写法别改，
// test-1406／1408 靠它断言）；“上报健康”行保留完整 detail（1.4.4 的
// 回归断言要读它）；“入网”行不单列——没入网时健康标题就是“尚未入网”。
// 节点四个操作按钮（v1.4.14：卡片与列表行共用同一套，免得两边 drift）：
// 详情／入站模板／重新生成入网令牌／删除，data-* 属性与 wireNodeCardButtons
// 绑的四种一致，行里点了一样生效。
function nodeActionButtons(n) {
  return '<button class="btn secondary sm" type="button" data-nodedetail="' + esc(String(n.id)) + '" data-detailname="' + esc(n.name || '') + '">详情</button>' +
    '<button class="btn secondary sm" type="button" data-inbounds="' + esc(String(n.id)) + '" data-inbname="' + esc(n.name || '') + '" data-inbaddr="' + esc(n.address || '') + '">入站模板</button>' +
    '<button class="btn secondary sm" type="button" data-regen="' + esc(String(n.id)) + '" data-regenname="' + esc(n.name || '') + '">重新生成入网令牌</button>' +
    '<button class="btn danger sm" type="button" data-delnodes="' + esc(String(n.id)) + '" data-delname="' + esc(n.name || '') + '">删除</button>';
}
function nodeMiniCard(n) {
  const h = nodeListHealth(n);
  return '<div class="mini-card static' + (h.status === 'offline' ? ' offline' : '') + '" data-node-card="' + esc(String(n.id)) + '">' +
    '<div class="mini-top"><h3>' + esc(n.name || '') + '（' + esc(n.address || '') + '）</h3><span>' + nodeListHealthHtml(n) + '</span></div>' +
    '<div class="mini-row"><span>上报健康</span><b>' + esc((n.backend_health && n.backend_health.detail) || '尚未上报') + '</b></div>' +
    '<div class="mini-row"><span>agent 版本</span><b>' + esc(n.agent_version || '—') + '</b></div>' +
    '<div class="mini-row"><span>已应用配置版本</span><b>' + esc(String(n.config_version == null ? '—' : n.config_version)) + '</b></div>' +
    '<div class="mini-row"><span>最后在线</span><b>' + esc(n.last_seen ? fmtDate(n.last_seen) : '—') + '</b></div>' +
    '<div class="btn-row">' + nodeActionButtons(n) + '</div></div>';
}
// 节点列表行（v1.4.14，列表视图）：紧凑行，一行一条；卡片里的信息
// 一个不少（名称、地址、健康、上报健康、agent 版本、已应用配置版本、
// 最后在线、四个操作按钮），只是横向排。健康标题与圆点仍走
// nodeListHealthHtml（1.4.6 口径，Xray 未运行时标题就是“Xray 未运行”）。
function nodeListRow(n) {
  return '<div class="list-row" data-node-row="' + esc(String(n.id)) + '">' +
    '<span class="cell cell-name">' + esc(n.name || '') + '</span>' +
    '<span class="cell">' + esc(n.address || '') + '</span>' +
    '<span class="cell">' + nodeListHealthHtml(n) + '</span>' +
    '<span class="cell cell-wrap"><span class="muted">上报健康：</span>' + esc((n.backend_health && n.backend_health.detail) || '尚未上报') + '</span>' +
    '<span class="cell"><span class="muted">agent 版本：</span>' + esc(n.agent_version || '—') + '</span>' +
    '<span class="cell"><span class="muted">已应用配置版本：</span>' + esc(String(n.config_version == null ? '—' : n.config_version)) + '</span>' +
    '<span class="cell"><span class="muted">最后在线：</span>' + esc(n.last_seen ? fmtDate(n.last_seen) : '—') + '</span>' +
    '<span class="btn-row">' + nodeActionButtons(n) + '</span></div>';
}
// 节点列表渲染（v1.4.12）：先按搜索词过滤（节点名／地址，前端本地过滤），
// 再按每页条数切页；搜索后总页数跟着变，当前页越界时 paginateSize 会
// 落到最后一页。分页条复用 pagerHtml（上一页／下一页／页码／跳至第 N 页）。
function renderNodesList() {
  const wrap = $('#nodes-list');
  if (!wrap) return;
  // v1.4.14：按记住的视图切容器 class 与切换按钮文案（默认卡片视图）。
  applyListViewClass(wrap, nodesView);
  syncViewToggle('#nodes-view-toggle', nodesView);
  if (!nodesCache.length) { wrap.innerHTML = '<div class="empty">还没有节点。上面填名字与地址，生成入网令牌后去新落地机上跑安装命令。</div>'; return; }
  const filtered = nodesCache.filter((n) => matchSubText(nodesSearch, [n.name, n.address]));
  if (!filtered.length) { wrap.innerHTML = '<div class="empty">没有匹配的节点，换个关键词试试。</div>'; return; }
  const pg = paginateSize(filtered, nodesPage, nodesPageSize);
  nodesPage = pg.cur;
  wrap.innerHTML = pg.slice.map((n) => nodesView === 'list' ? nodeListRow(n) : nodeMiniCard(n)).join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'nodes');
  if (pg.totalPages > 1) wirePager(wrap, 'nodes', pg.cur, pg.totalPages, (p) => { nodesPage = p; renderNodesList(); });
  wireNodeCardButtons(wrap);
}
function wireNodeCardButtons(wrap) {
  $$('[data-nodedetail]', wrap).forEach((b) => b.addEventListener('click', () => openNodeDetailModal(Number(b.dataset.nodedetail), b.dataset.detailname)));
  $$('[data-inbounds]', wrap).forEach((b) => b.addEventListener('click', () => loadNodeInbounds(Number(b.dataset.inbounds), b.dataset.inbname, b.dataset.inbaddr)));
  $$('[data-regen]', wrap).forEach((b) => b.addEventListener('click', async () => {
    // v1.4.12：重新生成入网令牌先弹二次确认（与删除节点同一个
    // confirmModal）。文案按 node_backend.go 核过：令牌一次性、30 分钟
    // 有效（nodeBackendJoinTokenTTL）；入网时令牌换长期 mTLS 客户端证书
    // 后即作废（handleNodeBackendJoin），之后拉配置走证书、不走令牌；
    // 重新生成只改 JoinTokenHash／JoinTokenExpires（handleNodesRegenJoinToken），
    // 已入网的节点不受影响。
    const ok = await confirmModal('重新生成入网令牌：' + (b.dataset.regenname || b.dataset.regen),
      '<p>重新生成后，旧的入网令牌立即作废；已入网的节点不受影响，照常用（节点入网时已用令牌换过长期 mTLS 客户端证书，之后拉配置走证书，不走令牌）；新令牌 30 分钟内有效，只用于重装节点时入网。</p>', '重新生成', false);
    if (!ok) return;
    try {
      const r2 = await apiPost('/api/v1/nodes/' + encodeURIComponent(b.dataset.regen) + '/join-token', {});
      showNodesOnce(r2.join_token || '', r2.install_command || '');
      toast('已重新生成入网令牌，旧令牌立即作废', 'ok');
    } catch (e) { toast('重新生成失败：' + e.message, 'err'); }
  }));
  $$('[data-delnodes]', wrap).forEach((b) => b.addEventListener('click', () => deleteNode(b.dataset.delnodes, b.dataset.delname)));
}
async function loadNodes() {
  const wrap = $('#nodes-list');
  if (!wrap) return;
  try {
    const r = await api('/api/v1/nodes');
    nodesCache = r.nodes || [];
    renderNodesList();
  } catch (e) {
    wrap.innerHTML = '<div class="card"><div class="error-box">加载失败：' + esc(e.message) + '</div></div>';
  }
}
function showNodesOnce(token, cmd) {
  const box = $('#nodes-once');
  if (!box) return;
  $('#nodes-once-token').value = token;
  $('#nodes-once-cmd').value = cmd;
  box.classList.remove('hidden'); box.hidden = false;
  window.scrollTo(0, 0);
}
// 节点详情弹窗（v1.4.4，切片 4）：上报健康块＋流量块（本期按人合计、
// 各入站总量）。数字全部来自 agent 的 60 秒上报，面板不主动探节点；
// 失联时显示上次数据，不清零。
async function openNodeDetailModal(nodeId, nodeName) {
  let d;
  try { d = await api('/api/v1/nodes/' + encodeURIComponent(nodeId)); }
  catch (e) { toast('加载节点详情失败：' + e.message, 'err'); return; }
  const n = d.node || {};
  const bh = d.backend_health || n.backend_health || {};
  const tr = d.traffic || { users: [], inbounds: [], up_period: 0, down_period: 0, up_total: 0, down_total: 0 };
  const users = tr.users || [];
  const inbounds = tr.inbounds || [];
  // sing-box 行（v1.4.7，切片 5）：只在节点配了 sing-box 入站
  // （HY2／TUIC／AnyTLS）时显示；无 sing-box 入站的节点上 agent
  // 本就不起 sing-box，Running=false 不是故障，不显示。
  const hasSingbox = (d.inbounds || []).some((ib) => ib.protocol === 'hysteria2' || ib.protocol === 'tuic' || ib.protocol === 'anytls');
  const sbRow = hasSingbox ? '<div class="kv"><span>sing-box</span><b>' + esc(bh.singbox_running ? '运行中' : '未运行') + '（重启 ' + esc(String(bh.singbox_restarts == null ? '—' : bh.singbox_restarts)) + ' 次' + (bh.singbox_version ? '，' + esc(bh.singbox_version) : '') + '）</b></div>' : '';
  // 核心版本（v1.4.10）：以上报的 xray_version 为准（backend_health
  // 里新加的 xray_version，节点行里本来就有）；没上报过就不编版本号，显示 —。
  const coreVer = String(bh.xray_version || n.xray_version || '').trim();
  const m = openModal('<h2>节点详情：' + esc(nodeName || n.name || nodeId) + '</h2>' +
    '<h3>上报健康</h3>' +
    '<div class="kv"><span>状态</span><b>' + nodeHealthDot(bh.status) + esc(bh.detail || '尚未上报') + '</b></div>' +
    '<div class="kv"><span>最后上报</span><b>' + esc(bh.last_traffic_at ? fmtDate(typeof bh.last_traffic_at === 'number' ? bh.last_traffic_at * 1000 : bh.last_traffic_at) : '—') + '</b></div>' +
    (bh.last_error ? '<div class="kv"><span>最后错误</span><b>' + esc(bh.last_error) + '</b></div>' : '') +
    '<div class="kv"><span>核心版本</span><b>' + esc(coreVer ? 'Xray ' + coreVer : '—') + '</b></div>' +
    '<div class="kv"><span>Xray 重启次数</span><b>' + esc(String(bh.xray_restarts == null ? '—' : bh.xray_restarts)) + '</b></div>' +
    sbRow +
    '<div class="kv"><span>CPU／内存</span><b>' + esc(bh.cpu_percent == null ? '—' : String(bh.cpu_percent) + '% ／ ' + String(bh.mem_mb == null ? '—' : bh.mem_mb) + ' MB') + '</b></div>' +
    '<h3>流量（本期按人合计）</h3>' +
    '<div class="kv"><span>本期上行／下行</span><b>' + esc(fmtBytes(tr.up_period || 0)) + ' ／ ' + esc(fmtBytes(tr.down_period || 0)) + '</b></div>' +
    '<div class="kv"><span>累计上行／下行</span><b>' + esc(fmtBytes(tr.up_total || 0)) + ' ／ ' + esc(fmtBytes(tr.down_total || 0)) + '</b></div>' +
    (users.length ? users.map((u) => '<div class="kv"><span>' + esc(u.username || u.user_id || '') + '</span><b>本期 ' + esc(fmtBytes((u.up_period || 0) + (u.down_period || 0))) + '（上 ' + esc(fmtBytes(u.up_period || 0)) + '／下 ' + esc(fmtBytes(u.down_period || 0)) + '）</b></div>').join('') : '<p class="muted small">本期还没有按人流量。</p>') +
    '<h3>各入站总量（累计）</h3>' +
    (inbounds.length ? inbounds.map((ib) => '<div class="kv"><span>' + esc(ib.inbound_tag || '') + '</span><b>上 ' + esc(fmtBytes(ib.up || 0)) + ' ／ 下 ' + esc(fmtBytes(ib.down || 0)) + '</b></div>').join('') : '<p class="muted small">还没有入站流量。</p>') +
    '<div class="btn-row"><button class="btn secondary" id="node-detail-close" type="button">关闭</button></div>');
  if (m) onIn(m, '#node-detail-close', 'click', closeModal);
}
async function deleteNode(id, name) {
  const ok = await confirmModal('删除节点', '<p>确定删除节点 <b>' + esc(name || id) + '</b> 吗？</p><p>删除后节点下次拉配置会拿到空配置并自停；流量历史保留。</p>', '删除', true);
  if (!ok) return;
  try {
    await api('/api/v1/nodes/' + encodeURIComponent(id), { method: 'DELETE', body: JSON.stringify({}) });
    toast('已删除', 'ok'); loadNodes();
  } catch (e) {
    if (String(e.message || '').indexOf('中转路由') !== -1) {
      const ok2 = await confirmModal('有中转路由指向该节点', '<p>有中转路由指向该节点，一起删除这些路由吗？</p>', '一起删除', true);
      if (!ok2) return;
      try {
        await api('/api/v1/nodes/' + encodeURIComponent(id), { method: 'DELETE', body: JSON.stringify({ delete_routes: true }) });
        toast('已删除（路由一起删了）', 'ok'); loadNodes();
      } catch (e2) { toast('删除失败：' + e2.message, 'err'); }
      return;
    }
    toast('删除失败：' + e.message, 'err');
  }
}
/* ---------- 入站模板（v1.4.1 切片 2） ---------- */
let currentInboundsNodeId = null;
let currentInboundsNodeName = '';
let currentInboundsNodeAddress = '';
let currentEditInboundId = null;

const IB_PROTOCOLS = {
  'vless-reality': { protocol: 'vless', transport: 'reality', security: 'reality' },
  'vless-xhttp': { protocol: 'vless', transport: 'xhttp', security: 'reality' },
  'vless-tls': { protocol: 'vless', transport: 'tcp', security: 'tls' },
  'trojan': { protocol: 'trojan', transport: 'tcp', security: 'tls' },
  'hysteria2': { protocol: 'hysteria2', transport: 'quic', security: 'tls' },
  'tuic': { protocol: 'tuic', transport: 'quic', security: 'tls' },
  'anytls': { protocol: 'anytls', transport: 'tcp', security: 'tls' },
  'shadowsocks': { protocol: 'shadowsocks', transport: 'tcp', security: 'none' },
  'ss2022': { protocol: 'shadowsocks', transport: 'tcp', security: 'none' },
  'socks': { protocol: 'socks', transport: 'tcp', security: 'none' },
};
// SNI 与 REALITY 指纹预设（v1.4.4，切片 4）：用户亲口定的 UI 预设，
// 下拉框里列全部预设＋“自定义”选项，点开不按输入过滤；选“自定义”
// 才出文本框手填。SNI 五个是 1.4.2 定的，datalist 做法已删掉；指纹
// 八个是 1.4.4 新加的。
const SNI_PRESETS = ['www.microsoft.com', 'www.apple.com', 'www.amazon.com', 'www.cloudflare.com', 'www.bing.com'];
const REALITY_FP_PRESETS = ['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', 'qq', 'random'];
const PRESET_CUSTOM_VALUE = '__custom';
// presetOptionsHtml 一组预设下拉框的 option 列表（v1.4.13 起 presetSelectHtml
// 与 rebuildSniSelect 共用）：（留空）＋ extraOptions（已签发证书域名，
// 在前）＋ presets ＋自定义…。返回 { opts, selVal }：current 在预设或
// extraOptions 里时选中它，否则落到“自定义”。泛域名条目只用来帮填：
// current 本身是泛域名（如 *.example.com）时不算“选中了预设”，落到
// “自定义”文本框里（让用户把 * 改成子域名再提交）。
function presetOptionsHtml(presets, current, extraOptions) {
  const cur = String(current || '');
  const extra = extraOptions || [];
  const inOptions = presets.indexOf(cur) !== -1 || extra.some((o) => o.value === cur && o.value.slice(0, 2) !== '*.');
  const selVal = cur === '' ? '' : (inOptions ? cur : PRESET_CUSTOM_VALUE);
  const opts = '<option value="">（留空）</option>' +
    extra.map((o) => '<option value="' + esc(o.value) + '"' + (o.value === selVal ? ' selected' : '') + '>' + esc(o.label || o.value) + '</option>').join('') +
    presets.map((p) => '<option value="' + esc(p) + '"' + (p === selVal ? ' selected' : '') + '>' + esc(p) + '</option>').join('') +
    '<option value="' + PRESET_CUSTOM_VALUE + '"' + (selVal === PRESET_CUSTOM_VALUE ? ' selected' : '') + '>自定义…</option>';
  return { opts, selVal };
}
// presetSelectHtml 一组“下拉框＋自定义文本框”的 HTML：current 在预设
// 里时下拉框选中它、文本框隐藏；不在预设里时下拉框选“自定义”、文本
// 框显示并填 current。selectId／customId 由调用方定（页面与弹窗各
// 用一套 id，不撞）。extraOptions 可选：插在预设前面的头部选项
// （v1.4.13：编辑弹窗的 SNI 框传已签发证书域名，见 certSniOptions）。
function presetSelectHtml(selectId, customId, presets, current, customPlaceholder, extraOptions) {
  const cur = String(current || '');
  const built = presetOptionsHtml(presets, current, extraOptions);
  return '<select id="' + selectId + '">' + built.opts + '</select>' +
    '<input id="' + customId + '" placeholder="' + esc(customPlaceholder || '') + '" value="' + esc(built.selVal === PRESET_CUSTOM_VALUE ? cur : '') + '"' + (built.selVal === PRESET_CUSTOM_VALUE ? '' : ' style="display:none;margin-top:6px"') + '>';
}
// certSniOptions 已签发证书域名拼成 SNI 下拉框的头部选项（v1.4.13）：
// 只列有效的（未过期，nodeCertValid 为真），去重；泛域名条目标出来
// （如 *.example.com（一级子域名都可用）），option 的 value 必须是
// 干净的域名，括号说明只放显示文本里。复用 nodeCertsCache，不新存
// 一份、不新加后端接口。缓存为空时返回 []，下拉框退化成原来的预设＋
// 自定义，不报错。
function certSniOptions() {
  const seen = {};
  const out = [];
  nodeCertsCache.forEach((c) => {
    if (!nodeCertValid(c)) return;
    const d = String(c.domain || '').trim().toLowerCase();
    if (!d || seen[d]) return;
    seen[d] = true;
    out.push({ value: d, label: d.slice(0, 2) === '*.' ? d + '（一级子域名都可用）' : d });
  });
  return out;
}
// rebuildSniSelect 把一个已在 DOM 里的 SNI 下拉框重建成“已签发证书域名
// 在前＋5 个 SNI 预设＋自定义…”（v1.4.13，添加弹窗打开时调一次；证书
// 列表是 loadNodeInbounds 时 loadNodeCerts 拉过的，缓存是新鲜的）。
// 当前值能保留就保留：已选证书域名／预设留在下拉框里，手填的值留在
// 自定义文本框里；缓存为空时退化成原来的预设＋自定义。
function rebuildSniSelect(sel, cus) {
  if (!sel) return;
  const cur = sel.value === PRESET_CUSTOM_VALUE ? (cus ? cus.value : '') : sel.value;
  const built = presetOptionsHtml(SNI_PRESETS, cur, certSniOptions());
  sel.innerHTML = built.opts;
  if (!cus) return;
  if (built.selVal === PRESET_CUSTOM_VALUE) { cus.value = cur; cus.style.display = ''; }
  else { cus.value = ''; cus.style.display = 'none'; }
}
// wireCertSniSelect 泛域名条目选后的处理（v1.4.13）：选了泛域名不直接
// 提交——切到“自定义…”，把 *.example.com 预填进自定义文本框，让用户
// 把 * 改成自己的子域名再提交（入站域名必须是具体子域名）。在
// wirePresetSelect 之后绑：先让它把自定义框隐藏，再由这里重新显示并
// 预填（光标落在 * 上，方便直接改）。改完值后照常调一次 syncCertGate，
// 让置灰按文本框里的具体子域名算。
// 注意：这里只帮填域名框，不决定用哪张证书——证书绑定是后端下发时
// nodeCertForDomain 自动匹配（精确优先、再泛域名），下拉选择不影响它，
// 也不存证书 ref；DNS 解析也不变，每个子域名仍各做 A 记录。
function wireCertSniSelect(selectId, customId, root) {
  const sel = (root || document).querySelector('#' + selectId);
  const cus = (root || document).querySelector('#' + customId);
  if (!sel || !cus) return;
  sel.addEventListener('change', () => {
    const v = sel.value;
    if (v.slice(0, 2) !== '*.') return;
    cus.value = v;
    sel.value = PRESET_CUSTOM_VALUE;
    cus.style.display = '';
    cus.focus();
    try { cus.setSelectionRange(0, 1); } catch (e) { /* 选不中 * 也不影响提交 */ }
    syncCertGate();
  });
}
// presetSelectValue 读一组预设下拉框的值：选了预设就返回预设，选了
// “自定义”就返回文本框里手填的值。
function presetSelectValue(selectEl, customEl) {
  if (!selectEl) return '';
  if (selectEl.value === PRESET_CUSTOM_VALUE) return (customEl ? customEl.value : '').trim();
  return selectEl.value;
}
// wirePresetSelect 给一组预设下拉框绑 change：切到“自定义”时显示文本
// 框、切回预设时隐藏。root 不传时在整个 document 里找。
function wirePresetSelect(selectId, customId, root) {
  const sel = (root || document).querySelector('#' + selectId);
  const cus = (root || document).querySelector('#' + customId);
  if (!sel || !cus) return;
  sel.addEventListener('change', () => {
    if (sel.value === PRESET_CUSTOM_VALUE) { cus.style.display = ''; cus.focus(); }
    else cus.style.display = 'none';
  });
}
function ibProtocolLabel(inb) {
  if (inb.protocol === 'vless' && inb.transport === 'reality') return 'VLESS＋REALITY＋Vision';
  if (inb.protocol === 'vless' && inb.transport === 'xhttp') return 'VLESS＋REALITY＋XHTTP';
  if (inb.protocol === 'vless' && inb.security === 'tls') return 'VLESS＋TLS＋Vision';
  if (inb.protocol === 'vless') return 'VLESS';
  if (inb.protocol === 'shadowsocks') return (String(inb.ss_method || '').indexOf('2022-') === 0 ? 'Shadowsocks 2022' : 'Shadowsocks');
  if (inb.protocol === 'hysteria2') return 'Hysteria2';
  if (inb.protocol === 'tuic') return 'TUIC';
  if (inb.protocol === 'anytls') return 'AnyTLS';
  if (inb.protocol === 'trojan') return 'Trojan';
  if (inb.protocol === 'socks') return 'SOCKS5';
  return inb.protocol || '';
}
function ibIsTCP(inb) { return ['tcp', 'reality', 'xhttp', 'tls'].indexOf(inb.transport) !== -1; }
function showInboundsError(msg) {
  const box = $('#nodes-inbounds-error');
  if (!box) return;
  if (!msg) { box.classList.add('hidden'); box.hidden = true; box.textContent = ''; return; }
  box.textContent = msg; box.classList.remove('hidden'); box.hidden = false;
}
// 入站模板改弹窗（v1.4.12）：以前点“入站模板”是在节点列表下方 inline
// 展开一张表单，现在点开统一弹窗（openModal，遮罩／Esc／×／点别处关、
// 一次只开一个，与详情弹窗和入站编辑弹窗同一套）。表单 HTML 放在
// index.html 的 <template id="nodes-inbounds-template"> 里，每次打开
// 时克隆一份放进弹窗、现场绑定；关弹窗即丢弃，下次打开是全新表单。
// 帮助提示框 #ib-help-pop 本来就挂在 body 下、position:fixed，不会
// 被弹窗裁掉。编辑弹窗保存后调 loadNodeInbounds：弹窗已关就重新打开
// 入站模板弹窗（回到刚才的位置、列表已刷新）；添加表单在 v1.4.13 起是
// 单独的弹窗（openNodeIbAddModal），提交成功后同样关掉添加弹窗、调
// loadNodeInbounds 回到入站模板弹窗；提交失败时弹窗保持打开，报错显示
// 在弹窗内，表单原样保留不用重填。
function openNodeInboundsModal() {
  const tpl = document.getElementById('nodes-inbounds-template');
  if (!tpl) return null;
  const m = openModal('', true);
  if (!m) return null;
  const body = m.querySelector('.modal-body');
  if (!body) return null;
  body.appendChild(tpl.content.cloneNode(true));
  const title = body.querySelector('#nodes-inbounds-title');
  if (title) title.textContent = '入站模板 —— ' + currentInboundsNodeName;
  wireNodeInboundsModal(m);
  return m;
}
function wireNodeInboundsModal(m) {
  // 入站模板（v1.4.1 切片 2）：只留三件套按钮与“手动添加入站”按钮
  // （v1.4.13 起添加表单搬进单独的弹窗，见 openNodeIbAddModal／
  // wireNodeIbAddModal）。
  onIn(m, '#nodes-trio-btn', 'click', async () => {
    if (!currentInboundsNodeId) { toast('先在上面点一个节点的“入站模板”', 'err'); return; }
    try {
      const r = await apiPost('/api/v1/nodes/' + encodeURIComponent(currentInboundsNodeId) + '/inbounds/default-trio', { domain: $('#nodes-trio-domain', m).value.trim() });
      toast('已生成 ' + (r.created || []).length + ' 条入站' + (r.note ? '：' + r.note : ''), 'ok');
      loadNodeInbounds(currentInboundsNodeId, currentInboundsNodeName);
    } catch (e) { showInboundsError(e.message); }
  });
  onIn(m, '#nodes-ib-add-open', 'click', () => { openNodeIbAddModal(); });
  syncCertGate();
}
// 手动添加入站改单独弹窗（v1.4.13）：以前添加表单嵌在入站模板弹窗里
// （列表下方、“添加入站” h3 下面），现在拆出来单独成一个弹窗；入站模板
// 弹窗保持原样（只留三件套＋入站列表），两个入口不合并。表单 HTML 放在
// index.html 的 <template id="nodes-ib-add-template"> 里，每次打开时克隆
// 一份放进弹窗、现场绑定；关弹窗即丢弃，下次打开是全新表单。提交成功后
// 关掉添加弹窗、重新打开入站模板弹窗（列表已刷新，照编辑弹窗
// openInboundEditModal 的做法）；提交失败时弹窗保持打开，报错显示在
// 弹窗内，表单原样保留不用重填。
function openNodeIbAddModal() {
  const tpl = document.getElementById('nodes-ib-add-template');
  if (!tpl) return null;
  const m = openModal('', true);
  if (!m) return null;
  const body = m.querySelector('.modal-body');
  if (!body) return null;
  body.appendChild(tpl.content.cloneNode(true));
  const title = body.querySelector('#nodes-ib-add-title');
  if (title) title.textContent = '手动添加入站 —— ' + currentInboundsNodeName;
  wireNodeIbAddModal(m);
  return m;
}
function wireNodeIbAddModal(m) {
  // 添加表单的绑定（v1.4.13 前在 wireNodeInboundsModal 里，整块搬过来）。
  // SNI 与 REALITY 指纹是真正下拉框（v1.4.4）：select＋自定义文本框，见 presetSelectHtml。
  // v1.4.13：打开时按已签发证书重建 SNI 下拉框（有效证书域名在前＋5 个
  // 预设＋自定义…；缓存为空时退化成原来的预设＋自定义），泛域名条目选
  // 后进自定义框（wireCertSniSelect，在 wirePresetSelect 之后绑）。
  rebuildSniSelect($('#nodes-ib-sni', m), $('#nodes-ib-sni-custom', m));
  wirePresetSelect('nodes-ib-sni', 'nodes-ib-sni-custom', m);
  wireCertSniSelect('nodes-ib-sni', 'nodes-ib-sni-custom', m);
  wirePresetSelect('nodes-ib-reality-fingerprint', 'nodes-ib-reality-fingerprint-custom', m);
  const ssMethodWrap = $('#nodes-ib-ss-method-wrap', m);
  const ssPwWrap = $('#nodes-ib-ss-pw-wrap', m);
  const socksUserWrap = $('#nodes-ib-socks-user-wrap', m);
  const socksPwWrap = $('#nodes-ib-socks-pw-wrap', m);
  if (ssMethodWrap) ssMethodWrap.style.display = 'none';
  if (ssPwWrap) ssPwWrap.style.display = 'none';
  if (socksUserWrap) socksUserWrap.style.display = 'none';
  if (socksPwWrap) socksPwWrap.style.display = 'none';
  onIn(m, '#nodes-ib-protocol', 'change', () => {
    const v = $('#nodes-ib-protocol', m).value;
    const ss = v === 'shadowsocks' || v === 'ss2022';
    const sk = v === 'socks';
    if (ssMethodWrap) ssMethodWrap.style.display = ss ? '' : 'none';
    if (ssPwWrap) ssPwWrap.style.display = ss ? '' : 'none';
    if (socksUserWrap) socksUserWrap.style.display = sk ? '' : 'none';
    if (socksPwWrap) socksPwWrap.style.display = sk ? '' : 'none';
    // 备用协议默认端口（v1.4.10，切片 6）：SS／SS2022 8388、SOCKS5 1080；
    // 只在端口空着时填，不覆盖用户手填的值。
    const portEl = $('#nodes-ib-port', m);
    if (portEl && !portEl.value.trim()) {
      if (ss) portEl.value = '8388';
      else if (sk) portEl.value = '1080';
    }
    if (v === 'ss2022') $('#nodes-ib-ss-method', m).value = '2022-blake3-aes-128-gcm';
    if (v === 'shadowsocks') $('#nodes-ib-ss-method', m).value = 'aes-256-gcm';
    // v1.4.11：提示框还开着时，内容按新协议刷新（端口／SNI／密码等口径会变）。
    ibHelpRefresh();
    // v1.4.12：换协议时证书置灰跟着刷新（TLS 协议没证书选不了）。
    syncCertGate();
  });
  // v1.4.12：改 SNI／域名时证书置灰跟着刷新（按域名有没有证书）。
  onIn(m, '#nodes-ib-sni', 'change', syncCertGate);
  onIn(m, '#nodes-ib-sni-custom', 'input', syncCertGate);
  onIn(m, '#nodes-inbound-add-form', 'submit', async (e) => {
    e.preventDefault();
    ibHelpHide(); // v1.4.11：提交时关掉字段帮助提示框。
    if (!currentInboundsNodeId) { toast('先在上面点一个节点的“入站模板”', 'err'); return; }
    const proto = $('#nodes-ib-protocol', m).value;
    const mm = IB_PROTOCOLS[proto];
    const tag = $('#nodes-ib-tag', m).value.trim();
    const port = parseInt($('#nodes-ib-port', m).value, 10);
    // 备用协议端口留空时走后端模板默认值（SS／SS2022 8388、SOCKS5 1080），
    // 其他协议端口必填（v1.4.10，切片 6）。
    const isBackupProto = proto === 'shadowsocks' || proto === 'ss2022' || proto === 'socks';
    if (!tag || (port && (port < 1 || port > 65535)) || (!port && !isBackupProto)) { toast('tag 与端口（1–65535）都要填', 'err'); return; }
    const body = { inbound_tag: tag, protocol: mm.protocol, transport: mm.transport, security: mm.security, port: port || 0, listen: $('#nodes-ib-listen', m).value.trim(), sni: presetSelectValue($('#nodes-ib-sni', m), $('#nodes-ib-sni-custom', m)) };
    // v1.4.12：TLS 入站＋域名没有可用证书就拦住（先在下方节点证书里签发）。
    const block = certGateBlocks(proto, body.sni, '', '');
    if (block) { toast(block, 'err'); return; }
    // v1.4.13：入站域名不能是泛域名——泛域名条目选后已进自定义框预填，
    // 用户须把 * 改成具体子域名再提交；原样留着 *. 的直接拦住（后端
    // validateInbound 只判空，不拦泛域名，这里是唯一一道关）。
    if (body.sni.slice(0, 2) === '*.') { toast('入站域名不能是泛域名：把 * 改成具体子域名再提交（如 hy2ceshi.example.com）', 'err'); return; }
    const fpAdd = presetSelectValue($('#nodes-ib-reality-fingerprint', m), $('#nodes-ib-reality-fingerprint-custom', m));
    if (fpAdd) body.reality_fingerprint = fpAdd;
    if (proto === 'shadowsocks' || proto === 'ss2022') {
      body.ss_method = $('#nodes-ib-ss-method', m).value;
      const ssPw = $('#nodes-ib-ss-password', m).value.trim();
      if (ssPw) body.ss_password = ssPw;
      // SS2022 密码不是普通随机串：按方法长度生成 base64 密钥
      // （128-gcm 16 字节，其余 32 字节；agent 0.1.7 实测口径），
      // 否则后端校验 400、节点上 Xray 也会报 bad key 拒掉。
      else if (proto === 'ss2022') body.ss_password = randomSS2022Key($('#nodes-ib-ss-method', m).value === '2022-blake3-aes-128-gcm' ? 16 : 32);
      else body.ss_password = randomPassword1401(16);
    }
    if (proto === 'socks') {
      const su = $('#nodes-ib-socks-username', m).value.trim();
      if (su) body.socks_username = su;
      body.socks_password = $('#nodes-ib-socks-password', m).value.trim() || randomPassword1401(16);
    }
    try {
      await apiPost('/api/v1/nodes/' + encodeURIComponent(currentInboundsNodeId) + '/inbounds', body);
      toast('入站已添加，节点 60 秒内应用', 'ok');
      // v1.4.13：提交成功后关掉添加弹窗、重新打开入站模板弹窗（列表已刷新，
      // 照编辑弹窗 openInboundEditModal 的做法）。提交失败走 catch，弹窗保持
      // 打开、报错显示在弹窗内，表单原样保留不用重填。
      closeModal();
      loadNodeInbounds(currentInboundsNodeId, currentInboundsNodeName);
    } catch (err2) { showInboundsError(err2.message); }
  });
  syncCertGate();
}
async function loadNodeInbounds(nodeId, nodeName, nodeAddress) {
  currentInboundsNodeId = nodeId; currentInboundsNodeName = nodeName || '';
  // 节点地址只在点“入站模板”时传进来（SOCKS5 配置卡要显示它）；
  // 列表内刷新（停用／编辑／换端口后重载）不传时保留上次的值。
  if (nodeAddress != null) currentInboundsNodeAddress = nodeAddress;
  // 弹窗还没开就先开（点“入站模板”走这里）；已经开着只刷新列表，
  // 不重建弹窗（表单里填了一半的值不丢）。
  if (!document.querySelector('#nodes-inbounds-list')) {
    if (!openNodeInboundsModal()) return;
  } else {
    const t = document.querySelector('#nodes-inbounds-title');
    if (t) t.textContent = '入站模板 —— ' + currentInboundsNodeName;
  }
  showInboundsError('');
  try {
    // 证书先拉（入站列表的证书列与置灰都读它；拉不到就当作没有证书）。
    await loadNodeCerts();
    const r = await api('/api/v1/nodes/' + encodeURIComponent(nodeId) + '/inbounds');
    renderNodeInbounds(r.inbounds || []);
  } catch (e) { showInboundsError('加载入站失败：' + e.message); }
}
function renderNodeInbounds(inbounds) {
  const wrap = $('#nodes-inbounds-list');
  if (!wrap) return;
  // 443 端口主人：TCP 443 与 UDP 443 各是谁在占。
  let tcp443 = null, udp443 = null;
  inbounds.forEach((inb) => {
    if (!inb.enabled || inb.port !== 443) return;
    if (ibIsTCP(inb) && !tcp443) tcp443 = inb;
    if (inb.transport === 'quic' && !udp443) udp443 = inb;
  });
  $('#nodes-443-owner').textContent = 'TCP 443：' + (tcp443 ? tcp443.inbound_tag + ' 占用' : '空闲') + '；UDP 443：' + (udp443 ? udp443.inbound_tag + ' 占用' : '空闲') + '。TCP 443 与 UDP 443 各只能一个入站占，冲突时保存会报错并写明是哪两个入站。';
  if (!inbounds.length) { wrap.innerHTML = '<div class="empty">还没有入站。点“一键生成默认三件套”，或点“手动添加入站”。</div>'; return; }
  wrap.innerHTML = inbounds.map((inb) => {
    const isReality = inb.transport === 'reality' || inb.transport === 'xhttp';
    // 证书列（v1.4.12，切片 7）：REALITY 行显示“无证书”；TLS 入站
    // （HY2／TUIC／AnyTLS／Trojan／VLESS＋TLS）有证书显示域名与剩余
    // 天数，没有显示“无证书（须先签发）”且整行置灰、启用／编辑／换端口／
    // 轮换密钥点不开（删除保留可用，免得坏入站删不掉）；其它协议不显示
    // 证书行（用不上证书）。
    const needsCert = inb.protocol === 'hysteria2' || inb.protocol === 'tuic' || inb.protocol === 'anytls' || inb.protocol === 'trojan' || (inb.protocol === 'vless' && inb.security === 'tls');
    const certDomain = needsCert ? (inb.domain || inb.sni || '') : '';
    const cert = needsCert ? nodeCertForDomain(certDomain) : null;
    const noCert = needsCert && !cert;
    const certCell = isReality ? '无证书' : (needsCert ? (cert ? '证书：' + cert.domain + '（剩余 ' + (cert.days_left == null ? '—' : cert.days_left) + ' 天）' : '无证书（须先签发）') : null);
    const dis = noCert ? ' disabled title="没有可用证书，先在下方节点证书里签发"' : '';
    return '<div class="card' + (noCert ? ' ib-no-cert' : '') + '" style="margin-bottom:12px"><div class="kv"><span>入站</span><b>' + esc(inb.inbound_tag || '') + '</b></div>' +
      '<div class="kv"><span>协议</span><b>' + esc(ibProtocolLabel(inb)) + '</b></div>' +
      '<div class="kv"><span>端口</span><b>' + esc(String(inb.port)) + '（' + (ibIsTCP(inb) ? 'TCP' : (inb.transport === 'quic' ? 'UDP' : inb.transport)) + '）</b></div>' +
      '<div class="kv"><span>监听地址</span><b>' + esc(inb.listen || '0.0.0.0') + '</b></div>' +
      '<div class="kv"><span>SNI / 域名</span><b>' + esc(inb.sni || inb.domain || '—') + '</b></div>' +
      (certCell != null ? '<div class="kv"><span>证书</span><b>' + esc(certCell) + '</b></div>' : '') +
      (isReality ? '<div class="kv"><span>REALITY 公钥</span><b style="word-break:break-all">' + esc(inb.reality_public_key || '—') + '</b></div><div class="kv"><span>short id</span><b>' + esc(inb.reality_short_id || '—') + '</b></div>' : '') +
      (inb.protocol === 'shadowsocks' ? '<div class="kv"><span>加密方法</span><b>' + esc(inb.ss_method || '—') + '</b></div>' : '') +
      // SOCKS5 手动配置卡（v1.4.10，切片 6；§8.4）：地址用节点地址，
      // 端口／用户名／密码照入站字段显示；SOCKS5 不进订阅，手动填客户端。
      (inb.protocol === 'socks' ? '<div class="kv"><span>SOCKS5 地址</span><b>' + esc(currentInboundsNodeAddress || inb.listen || '—') + '</b></div>' +
      '<div class="kv"><span>SOCKS5 端口</span><b>' + esc(String(inb.port)) + '</b></div>' +
      '<div class="kv"><span>SOCKS5 用户名</span><b>' + esc(inb.socks_username || '—') + '</b></div>' +
      '<div class="kv"><span>SOCKS5 密码</span><b>' + esc(inb.socks_password || '—') + '</b></div>' : '') +
      '<div class="kv"><span>状态</span><b>' + (inb.enabled ? '启用' : '停用') + '</b></div>' +
      '<div class="btn-row">' +
      '<button class="btn secondary sm" type="button" data-ib-toggle="' + esc(String(inb.inbound_id)) + '" data-ib-enabled="' + (inb.enabled ? '1' : '0') + '"' + dis + '>' + (inb.enabled ? '停用' : '启用') + '</button>' +
      '<button class="btn secondary sm" type="button" data-ib-edit="' + esc(String(inb.inbound_id)) + '"' + dis + '>编辑</button>' +
      '<button class="btn secondary sm" type="button" data-ib-change-port="' + esc(String(inb.inbound_id)) + '" data-ib-tag="' + esc(inb.inbound_tag || '') + '"' + dis + '>换端口</button>' +
      (isReality ? '<button class="btn secondary sm" type="button" data-ib-rotate="' + esc(String(inb.inbound_id)) + '" data-ib-tag="' + esc(inb.inbound_tag || '') + '"' + dis + '>轮换密钥</button>' : '') +
      '<button class="btn danger sm" type="button" data-ib-del="' + esc(String(inb.inbound_id)) + '" data-ib-tag="' + esc(inb.inbound_tag || '') + '">删除</button></div></div>';
  }).join('');
  $$('[data-ib-toggle]', wrap).forEach((b) => b.addEventListener('click', async () => {
    try {
      await api('/api/v1/nodes/' + encodeURIComponent(currentInboundsNodeId) + '/inbounds/' + encodeURIComponent(b.dataset.ibToggle), { method: 'PUT', body: JSON.stringify({ enabled: b.dataset.ibEnabled !== '1' }) });
      toast('已' + (b.dataset.ibEnabled === '1' ? '停用' : '启用') + '，节点 60 秒内应用', 'ok');
      loadNodeInbounds(currentInboundsNodeId, currentInboundsNodeName);
    } catch (e) { showInboundsError(e.message); }
  }));
  $$('[data-ib-edit]', wrap).forEach((b) => b.addEventListener('click', () => {
    const inb = inbounds.find((x) => String(x.inbound_id) === String(b.dataset.ibEdit));
    if (!inb) return;
    openInboundEditModal(inb);
  }));
  $$('[data-ib-change-port]', wrap).forEach((b) => b.addEventListener('click', async () => {
    const ok = await confirmModal('一键换端口', '<p>确定给入站 <b>' + esc(b.dataset.ibTag || '') + '</b> 换一个随机空闲端口吗？</p><p>换完节点 60 秒内应用，订阅自动更新，不用重新导入。</p>', '换端口', false);
    if (!ok) return;
    try {
      const r = await apiPost('/api/v1/nodes/' + encodeURIComponent(currentInboundsNodeId) + '/inbounds/' + encodeURIComponent(b.dataset.ibChangePort) + '/change-port', {});
      toast('端口已换成 ' + r.new_port + '，节点 60 秒内应用，订阅自动更新', 'ok');
      loadNodeInbounds(currentInboundsNodeId, currentInboundsNodeName);
    } catch (e) { showInboundsError(e.message); }
  }));
  $$('[data-ib-del]', wrap).forEach((b) => b.addEventListener('click', async () => {
    const ok = await confirmModal('删除入站', '<p>确定删除入站 <b>' + esc(b.dataset.ibTag || b.dataset.ibDel) + '</b> 吗？</p><p>节点 60 秒内下线该入站。</p>', '删除', true);
    if (!ok) return;
    try {
      await api('/api/v1/nodes/' + encodeURIComponent(currentInboundsNodeId) + '/inbounds/' + encodeURIComponent(b.dataset.ibDel), { method: 'DELETE' });
      toast('已删除', 'ok'); loadNodeInbounds(currentInboundsNodeId, currentInboundsNodeName);
    } catch (e) { showInboundsError(e.message); }
  }));
  $$('[data-ib-rotate]', wrap).forEach((b) => b.addEventListener('click', async () => {
    const ok = await confirmModal('轮换 REALITY 密钥', '<p>确定轮换入站 <b>' + esc(b.dataset.ibTag || '') + '</b> 的 REALITY 密钥吗？</p><p>轮换后旧订阅链接失效，用户须重新导入。</p>', '轮换', true);
    if (!ok) return;
    try {
      const r = await apiPost('/api/v1/nodes/' + encodeURIComponent(currentInboundsNodeId) + '/inbounds/' + encodeURIComponent(b.dataset.ibRotate) + '/rotate-reality-key', {});
      toast(r.note || '密钥已换，旧订阅链接失效', 'ok');
      loadNodeInbounds(currentInboundsNodeId, currentInboundsNodeName);
    } catch (e) { showInboundsError(e.message); }
  }));
}
function ibCompositeValue(inb) {
  if (inb.protocol === 'vless' && inb.transport === 'reality') return 'vless-reality';
  if (inb.protocol === 'vless' && inb.transport === 'xhttp') return 'vless-xhttp';
  if (inb.protocol === 'vless' && inb.security === 'tls') return 'vless-tls';
  if (inb.protocol === 'vless') return 'vless-tls';
  if (inb.protocol === 'shadowsocks') return (String(inb.ss_method || '').indexOf('2022-') === 0 ? 'ss2022' : 'shadowsocks');
  if (inb.protocol === 'hysteria2') return 'hysteria2';
  if (inb.protocol === 'tuic') return 'tuic';
  if (inb.protocol === 'anytls') return 'anytls';
  if (inb.protocol === 'trojan') return 'trojan';
  if (inb.protocol === 'socks') return 'socks';
  return 'vless-reality';
}
// 编辑入站（v1.4.2，切片 3）：点编辑弹 modal 对话框改，不在页面最下方
// 展开表单。能改全部字段，包括入站 tag（改名后端按 PUT 改 tag 处理，
// 同一条入站换名，订阅跟着新 tag 走，不用删了重建）。
// SNI 与 REALITY 指纹是真正下拉框（v1.4.4）：select 列全部预设＋“自定义”
// 出文本框，不按输入过滤（datalist 做法已删）；改 SNI 时后端同步 dest
// 与 serverNames（SNI 是唯一事实来源，见 syncRealitySNIFields）。
function openInboundEditModal(inb) {
  currentEditInboundId = inb.inbound_id;
  const proto = ibCompositeValue(inb);
  const opt = (v, label) => '<option value="' + v + '"' + (v === proto ? ' selected' : '') + '>' + label + '</option>';
  const m = openModal('<h2>编辑入站</h2>' +
    '<form id="nodes-inbound-edit-form">' +
    '<div class="form-grid">' +
    '<div><label for="nodes-ib-edit-tag">入站 tag/名称 <span class="hint">必填，节点内唯一；改名即换 tag</span></label><input id="nodes-ib-edit-tag" value="' + esc(inb.inbound_tag || '') + '" maxlength="64" required></div>' +
    '<div><label for="nodes-ib-edit-protocol">协议</label><select id="nodes-ib-edit-protocol">' +
    opt('vless-reality', 'VLESS＋REALITY＋Vision（TCP）') + opt('vless-xhttp', 'VLESS＋REALITY＋XHTTP（TCP）') +
    opt('vless-tls', 'VLESS＋TLS＋Vision（TCP）') + opt('trojan', 'Trojan（TCP＋TLS）') +
    opt('hysteria2', 'Hysteria2（UDP）') + opt('tuic', 'TUIC（UDP）') + opt('anytls', 'AnyTLS（TCP＋TLS）') +
    opt('shadowsocks', 'Shadowsocks') + opt('ss2022', 'Shadowsocks 2022') + opt('socks', 'SOCKS5') +
    '</select></div>' +
    '<div><label for="nodes-ib-edit-port">端口 <span class="hint">必填，1–65535</span></label><input id="nodes-ib-edit-port" type="number" min="1" max="65535" value="' + esc(String(inb.port)) + '" required></div>' +
    '<div><label for="nodes-ib-edit-listen">监听地址 <span class="hint">0.0.0.0 = 全部网卡</span></label><input id="nodes-ib-edit-listen" value="' + esc(inb.listen || '0.0.0.0') + '" placeholder="0.0.0.0"></div>' +
    '<div><label for="nodes-ib-edit-sni">SNI / 域名 <span class="hint">点下拉选常用大站，选“自定义”可手填</span></label>' + presetSelectHtml('nodes-ib-edit-sni', 'nodes-ib-edit-sni-custom', SNI_PRESETS, inb.sni || inb.domain || '', '手填 SNI / 域名，如 example.com', certSniOptions()) + '</div>' +
    '<div id="nodes-ib-edit-ss-method-wrap"><label for="nodes-ib-edit-ss-method">SS 加密方法</label><select id="nodes-ib-edit-ss-method">' +
    ['aes-256-gcm', 'aes-128-gcm', 'chacha20-poly1305', '2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm', '2022-blake3-chacha20-poly1305'].map((mm) => '<option value="' + mm + '"' + (inb.ss_method === mm ? ' selected' : '') + '>' + mm + '</option>').join('') +
    '</select></div>' +
    '<div id="nodes-ib-edit-ss-pw-wrap"><label for="nodes-ib-edit-ss-password">SS 密码 <span class="hint">留空不改；SS2022 密码须是 base64 定长密钥</span></label><input id="nodes-ib-edit-ss-password" placeholder="留空不改"></div>' +
    '<div id="nodes-ib-edit-socks-user-wrap"><label for="nodes-ib-edit-socks-username">SOCKS5 用户名</label><input id="nodes-ib-edit-socks-username" value="' + esc(inb.socks_username || '') + '"></div>' +
    '<div id="nodes-ib-edit-socks-pw-wrap"><label for="nodes-ib-edit-socks-password">SOCKS5 密码 <span class="hint">留空不改</span></label><input id="nodes-ib-edit-socks-password" placeholder="留空不改"></div>' +
    '<div id="nodes-ib-edit-flow-wrap"><label for="nodes-ib-edit-flow">VLESS flow <span class="hint">Vision 入站填 xtls-rprx-vision，空着不填也行</span></label><input id="nodes-ib-edit-flow" value="' + esc(inb.flow || '') + '" placeholder="xtls-rprx-vision"></div>' +
    '<div id="nodes-ib-edit-reality-dest-wrap"><label for="nodes-ib-edit-reality-dest">REALITY 回落目标 <span class="hint">默认跟着 SNI 走（SNI:443），一般不用改</span></label><input id="nodes-ib-edit-reality-dest" value="' + esc(inb.reality_dest || '') + '" placeholder="www.example.com:443"></div>' +
    '<div id="nodes-ib-edit-reality-sn-wrap"><label for="nodes-ib-edit-reality-server-names">REALITY serverNames <span class="hint">默认跟着 SNI 走，一般不用改</span></label><input id="nodes-ib-edit-reality-server-names" value="' + esc(inb.reality_server_names || '') + '" placeholder="www.example.com"></div>' +
    '<div id="nodes-ib-edit-reality-fp-wrap"><label for="nodes-ib-edit-reality-fingerprint">REALITY 指纹 <span class="hint">点下拉选预设，选“自定义”可手填</span></label>' + presetSelectHtml('nodes-ib-edit-reality-fingerprint', 'nodes-ib-edit-reality-fingerprint-custom', REALITY_FP_PRESETS, inb.reality_fingerprint || 'chrome', '手填指纹，如 chrome') + '</div>' +
    '<div><label for="nodes-ib-edit-multiplier">流量倍率 <span class="hint">默认 1，0.1–100</span></label><input id="nodes-ib-edit-multiplier" type="number" min="0.1" max="100" step="0.1" value="' + esc(String(inb.multiplier == null ? 1 : inb.multiplier)) + '"></div>' +
    '<div><label for="nodes-ib-edit-enabled">启用 <span class="hint">关掉后节点 60 秒内下线该入站</span></label><select id="nodes-ib-edit-enabled"><option value="true"' + (inb.enabled ? ' selected' : '') + '>启用</option><option value="false"' + (!inb.enabled ? ' selected' : '') + '>停用</option></select></div>' +
    '</div>' +
    '<div class="btn-row"><button class="btn" type="submit">保存修改</button><button class="btn secondary" type="button" id="nodes-ib-edit-cancel">取消</button></div>' +
    '</form>');
  if (!m) return;
  // v1.4.12：没有可用证书时 TLS 协议选项置灰（点不开）；已是该协议的
  // 老入站仍可打开（选中的 disabled option 值照常读，不影响保存其它字段）。
  const editProtoSel = $('#nodes-ib-edit-protocol', m);
  if (editProtoSel) {
    const hasAnyCert = nodeCertsCache.some((c) => nodeCertValid(c));
    Array.from(editProtoSel.options).forEach((o) => { if (certGateNeedsCert(o.value)) o.disabled = !hasAnyCert; });
  }
  const syncEdit = () => {
    const v = $('#nodes-ib-edit-protocol', m).value;
    const ss = v === 'shadowsocks' || v === 'ss2022';
    const sk = v === 'socks';
    const rl = v === 'vless-reality' || v === 'vless-xhttp';
    const vl = v === 'vless-reality' || v === 'vless-xhttp' || v === 'vless-tls';
    const show = (id, on) => { const el = $(id, m); if (el) el.style.display = on ? '' : 'none'; };
    show('#nodes-ib-edit-ss-method-wrap', ss);
    show('#nodes-ib-edit-ss-pw-wrap', ss);
    show('#nodes-ib-edit-socks-user-wrap', sk);
    show('#nodes-ib-edit-socks-pw-wrap', sk);
    show('#nodes-ib-edit-flow-wrap', vl);
    show('#nodes-ib-edit-reality-dest-wrap', rl);
    show('#nodes-ib-edit-reality-sn-wrap', rl);
    show('#nodes-ib-edit-reality-fp-wrap', rl);
    if (v === 'ss2022' && $('#nodes-ib-edit-ss-method', m).value.indexOf('2022-') !== 0) $('#nodes-ib-edit-ss-method', m).value = '2022-blake3-aes-128-gcm';
    if (v === 'shadowsocks' && $('#nodes-ib-edit-ss-method', m).value.indexOf('2022-') === 0) $('#nodes-ib-edit-ss-method', m).value = 'aes-256-gcm';
  };
  syncEdit();
  wirePresetSelect('nodes-ib-edit-sni', 'nodes-ib-edit-sni-custom', m);
  wireCertSniSelect('nodes-ib-edit-sni', 'nodes-ib-edit-sni-custom', m); // v1.4.13：与添加表单同一套做法
  wirePresetSelect('nodes-ib-edit-reality-fingerprint', 'nodes-ib-edit-reality-fingerprint-custom', m);
  onIn(m, '#nodes-ib-edit-protocol', 'change', syncEdit);
  onIn(m, '#nodes-ib-edit-cancel', 'click', closeModal);
  onIn(m, '#nodes-inbound-edit-form', 'submit', async (e) => {
    e.preventDefault();
    if (!currentInboundsNodeId || !currentEditInboundId) return;
    const tag = $('#nodes-ib-edit-tag', m).value.trim();
    const port = parseInt($('#nodes-ib-edit-port', m).value, 10);
    if (!tag) { toast('入站 tag/名称不能为空', 'err'); return; }
    if (!port || port < 1 || port > 65535) { toast('端口要在 1–65535 之间', 'err'); return; }
    const p = IB_PROTOCOLS[$('#nodes-ib-edit-protocol', m).value];
    const sni = presetSelectValue($('#nodes-ib-edit-sni', m), $('#nodes-ib-edit-sni-custom', m));
    // v1.4.12：TLS 入站＋域名没有可用证书就拦住（原来就是这套协议＋域名
    // 的老入站只改其它字段不拦，免得无证书的老入站连端口都改不了）。
    const block = certGateBlocks($('#nodes-ib-edit-protocol', m).value, sni, ibCompositeValue(inb), inb.domain || inb.sni || '');
    if (block) { toast(block, 'err'); return; }
    // v1.4.13：入站域名不能是泛域名（与添加表单同一道关，见 wireNodeIbAddModal）。
    if (sni.slice(0, 2) === '*.') { toast('入站域名不能是泛域名：把 * 改成具体子域名再提交（如 hy2ceshi.example.com）', 'err'); return; }
    const body = { inbound_tag: tag, protocol: p.protocol, transport: p.transport, security: p.security, port, listen: $('#nodes-ib-edit-listen', m).value.trim(), sni, enabled: $('#nodes-ib-edit-enabled', m).value === 'true' };
    const mult = parseFloat($('#nodes-ib-edit-multiplier', m).value);
    if (mult) body.multiplier = mult;
    if (p.protocol === 'vless' && $('#nodes-ib-edit-flow', m).value.trim()) body.flow = $('#nodes-ib-edit-flow', m).value.trim();
    if (p.protocol === 'hysteria2' || p.protocol === 'tuic' || p.protocol === 'anytls' || p.protocol === 'trojan' || (p.protocol === 'vless' && p.security === 'tls')) body.domain = sni;
    if ($('#nodes-ib-edit-protocol', m).value === 'shadowsocks' || $('#nodes-ib-edit-protocol', m).value === 'ss2022') {
      body.ss_method = $('#nodes-ib-edit-ss-method', m).value;
      if ($('#nodes-ib-edit-ss-password', m).value.trim()) body.ss_password = $('#nodes-ib-edit-ss-password', m).value.trim();
    }
    if ($('#nodes-ib-edit-protocol', m).value === 'socks') {
      if ($('#nodes-ib-edit-socks-username', m).value.trim()) body.socks_username = $('#nodes-ib-edit-socks-username', m).value.trim();
      if ($('#nodes-ib-edit-socks-password', m).value.trim()) body.socks_password = $('#nodes-ib-edit-socks-password', m).value.trim();
    }
    if (p.transport === 'reality' || p.transport === 'xhttp') {
      if ($('#nodes-ib-edit-reality-dest', m).value.trim()) body.reality_dest = $('#nodes-ib-edit-reality-dest', m).value.trim();
      if ($('#nodes-ib-edit-reality-server-names', m).value.trim()) body.reality_server_names = $('#nodes-ib-edit-reality-server-names', m).value.trim();
      const fp = presetSelectValue($('#nodes-ib-edit-reality-fingerprint', m), $('#nodes-ib-edit-reality-fingerprint-custom', m));
      if (fp) body.reality_fingerprint = fp;
    }
    try {
      await api('/api/v1/nodes/' + encodeURIComponent(currentInboundsNodeId) + '/inbounds/' + encodeURIComponent(currentEditInboundId), { method: 'PUT', body: JSON.stringify(body) });
      toast('已保存，节点 60 秒内应用', 'ok');
      closeModal();
      loadNodeInbounds(currentInboundsNodeId, currentInboundsNodeName);
    } catch (err2) { showInboundsError(err2.message); closeModal(); }
  });
}
function randomPassword1401(n) {
  // 字符集与后端 auth.go 的 randomPassword 一致（去掉 0／1／I／L／O／i／l／o
  // 这些易混淆字符；v1.4.10 前前端这里多一个 L，两边已对齐）。
  const chars = 'ABCDEFGHJKMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789';
  const buf = new Uint32Array(n || 16);
  window.crypto.getRandomValues(buf);
  let out = '';
  for (let i = 0; i < buf.length; i++) out += chars[buf[i] % chars.length];
  return out;
}
// randomSS2022Key 生成 SS2022 的 base64 密钥（agent 0.1.7 实测口径）：
// n 字节随机数，标准 base64 编码（带 = 补齐，与 Xray 要求的格式一致）。
function randomSS2022Key(n) {
  const buf = new Uint8Array(n);
  window.crypto.getRandomValues(buf);
  let s = '';
  for (let i = 0; i < buf.length; i++) s += String.fromCharCode(buf[i]);
  return btoa(s);
}
// ---------- 字段帮助（v1.4.11 入站表单，v1.4.13 起与证书卡片共用） ----------
// 每个字段 label 旁的 ⓘ 按钮点开一个共享提示框（#ib-help-pop）：
// 入站表单 10 个字段＋证书卡片 2 个字段（cert-domain／cert-provider）
// 共用同一套机制（ibHelpWire 是 document 级委托，点任意 [data-ib-help]
// 都弹提示框；Esc／×／点别处关、一次只开一个都现成，不用另起一套）。
// 点 ⓘ 打开、再点关、点别处关、Esc 关、一次只开一个。
// 内容以实现为准：端口／SNI／密码等口径见 node_inbounds.go 的
// validateInbound 与新建默认值（1.4.10 切片 6）；SS2022 密钥长度是
// agent 0.1.7 实测口径；SOCKS5 用户名空着节点上应用失败（agent 的
// renderSocksInbound 要求用户名非空）。带 proto 参数的是跟协议联动
// 的字段：打开时按当前选的协议生成内容，切换协议时若提示框还开着，
// 内容跟着刷新（见协议 change 处理里的 ibHelpRefresh）。
const IB_HELP = {
  tag: {
    title: '入站 tag/名称',
    body: () => [
      '填什么：给这条入站起个名字，节点内唯一，列表里显示的就是它。',
      '必填：不能为空，同一个节点里不能重复，重复会保存失败。',
      '建议：协议加端口，如 ss-8388、reality-443，一眼看出是哪条入站。',
    ],
  },
  protocol: {
    title: '协议',
    body: () => [
      '填什么：这条入站跑什么协议，决定客户端用什么方式连接。',
      '必填，默认 VLESS＋REALITY。',
      '建议：新节点先用上面“一键生成默认三件套”（REALITY＋XHTTP＋HY2）；SS、SS2022、SOCKS5 是备用协议，默认不开，按需添加；XHTTP 只作服务端 fallback，不进订阅。',
    ],
  },
  port: {
    title: '端口',
    body: (p) => {
      const lines = ['填什么：入站监听的端口，客户端连的就是这个端口，范围 1–65535。'];
      if (p === 'shadowsocks' || p === 'ss2022' || p === 'socks') {
        lines.push('选填：留空走默认端口（SS／SS2022 是 8388，SOCKS5 是 1080）；想换就手填。切换协议时端口空着会自动填默认端口，手填的值不会被覆盖。');
      } else {
        lines.push('必填：这个协议端口不能为空。');
      }
      lines.push('建议：REALITY、TLS 类一般用 443；TCP 443 与 UDP 443 各只能一个启用的入站占着（HY2 和 TUIC 都是 UDP 443，同时只能开一个）；停用的入站不占端口。');
      return lines;
    },
  },
  listen: {
    title: '监听地址',
    body: () => [
      '填什么：入站绑哪块网卡的 IP。',
      '选填：留空等于 0.0.0.0，监听全部网卡。',
      '建议：一般留空；多 IP 的机器想只走某块网卡时再填指定 IP；填了一定要是合法 IP，不然保存失败。',
    ],
  },
  sni: {
    title: 'SNI / 域名',
    body: (p) => {
      if (p === 'vless-reality' || p === 'vless-xhttp') return [
        '填什么：REALITY 的伪装目标域名，握手时对外看起来像在访问这个网站；回落目标与 serverNames 都从它派生。',
        '必填：留空会先填占位值 www.example.com 顶上，但占位值不能用，部署前必须改成真实域名。',
        '建议：下拉选五个预设大站之一（如 www.microsoft.com），或选“自定义”手填。',
      ];
      if (p === 'vless-tls' || p === 'trojan' || p === 'anytls') return [
        '填什么：TLS 证书的域名，也是客户端连接时用的 SNI。',
        '必填：必须是真实域名，证书就签给这个域名。',
        '建议：填节点域名，如 example.com；域名须先 A 记录解析到这台节点。',
      ];
      if (p === 'hysteria2' || p === 'tuic') return [
        '填什么：HY2／TUIC 的域名，客户端连接时用的 SNI，证书按这个域名签发。',
        '必填：必须是真实域名。',
        '建议：填节点域名，如 example.com；域名须先 A 记录解析到这台节点；HY2 和 TUIC 都是 UDP 443，同时只能开一个。',
      ];
      return [
        '这个协议不用填 SNI：没有 TLS 握手，填了也不起作用。',
        '留空即可。',
      ];
    },
  },
  'reality-fingerprint': {
    title: 'REALITY 指纹',
    body: (p) => {
      if (p === 'vless-reality' || p === 'vless-xhttp') return [
        '填什么：REALITY 握手时模仿的浏览器 TLS 指纹。',
        '选填：留空默认 chrome。',
        '建议：一般用默认 chrome；下拉有 8 个预设（chrome、firefox、safari、ios、android、edge、qq、random），选“自定义”可手填；只能用字母、数字、下划线、连字符，最多 64 个字。',
      ];
      return ['只对 REALITY 入站有效；当前协议不是 REALITY，填了也不起作用。'];
    },
  },
  'ss-method': {
    title: 'SS 加密方法',
    body: (p) => {
      if (p === 'ss2022') return [
        '填什么：SS2022 的加密方法，决定密码密钥要多长。',
        '必填。',
        '建议：默认 2022-blake3-aes-128-gcm（16 字节密钥）；2022-blake3-aes-256-gcm 与 2022-blake3-chacha20-poly1305 要 32 字节密钥；只能选 2022- 开头的方法。',
      ];
      return [
        '填什么：Shadowsocks 的加密方法。',
        '必填。',
        '建议：默认 aes-256-gcm；三种可选（aes-256-gcm、aes-128-gcm、chacha20-poly1305），不要选 2022- 开头的方法，那是 SS2022 用的。',
      ];
    },
  },
  'ss-password': {
    title: 'SS 密码',
    body: (p) => {
      if (p === 'ss2022') return [
        '填什么：SS2022 的密钥，必须是 base64 编码的定长密钥，不是普通密码。',
        '选填：留空按方法自动生成（128-gcm 16 字节，256-gcm／chacha20 32 字节）。',
        '建议：留空自动生成最省事；手填必须是合法 base64 且长度对上，否则保存时直接报 400；普通随机串不能用，节点上 Xray 会报 bad key 拒掉。',
      ];
      return [
        '填什么：客户端连接用的密码。',
        '选填：留空自动生成 16 位随机密码。',
        '建议：留空让它生成就行；手填也行，任意密码都可以。',
      ];
    },
  },
  'socks-username': {
    title: 'SOCKS5 用户名',
    body: () => [
      '填什么：SOCKS5 的登录用户名，客户端填这个用户名连接。',
      '必填：不填的话节点上应用这条入站会失败（agent 要求用户名非空）。',
      '建议：填一个好记的用户名，如 user1；和密码一起填进客户端。',
    ],
  },
  'socks-password': {
    title: 'SOCKS5 密码',
    body: () => [
      '填什么：SOCKS5 的登录密码。',
      '选填：留空自动生成 16 位随机密码。',
      '建议：留空自动生成；手填也行。',
    ],
  },
  'cert-domain': {
    title: '证书域名',
    body: () => [
      '填什么：要签证书的域名。',
      '必填。',
      '建议：填单个子域名（如 hy2ceshi.example.com）只给它自己用；填泛域名（如 *.example.com）一张覆盖该域名下所有一级子域名，两个都有时单个优先；泛域名不覆盖裸域（如 example.com 本身）；签发前须先把 A 记录解析到节点：每个子域名各做一条 A 记录，指到自己节点的 IP（不同节点 IP 不同，一条泛解析指不到多台机器）。',
    ],
  },
  'cert-provider': {
    title: 'DNS 服务商',
    body: () => [
      '填什么：写 TXT 记录验证域名归属的 DNS 服务商。',
      '必填：下拉选一家。',
      '建议：凭证在设置页 DNS 服务商卡片里配，配好后这里才会出现；手动模式不用配凭证。Cloudflare 须先在面板设置里配好 Zone DNS Edit 权限的 Token（cfut_ 开头）；手动模式走两步签发（面板给 TXT 值，用户自己去 DNS 加记录，再回来点验证）。',
    ],
  },
};
let ibHelpOpenKey = null;
let ibHelpOpenBtn = null;
let ibHelpWired = false;
function ibHelpProto() { const s = $('#nodes-ib-protocol'); return s ? s.value : ''; }
function ibHelpPopEl() {
  let pop = document.getElementById('ib-help-pop');
  if (pop) return pop;
  pop = document.createElement('div');
  pop.id = 'ib-help-pop';
  pop.className = 'ib-help-pop';
  pop.setAttribute('role', 'dialog');
  pop.setAttribute('aria-label', '填写说明');
  pop.hidden = true;
  const x = document.createElement('button');
  x.type = 'button';
  x.className = 'ib-help-x';
  x.setAttribute('aria-label', '关闭提示');
  x.textContent = '×';
  x.addEventListener('click', (e) => { e.stopPropagation(); ibHelpHide(); });
  pop.appendChild(x);
  const t = document.createElement('h4');
  t.className = 'ib-help-title';
  pop.appendChild(t);
  const b = document.createElement('div');
  b.className = 'ib-help-body';
  pop.appendChild(b);
  document.body.appendChild(pop);
  return pop;
}
function ibHelpRender(key) {
  const entry = IB_HELP[key];
  const pop = ibHelpPopEl();
  if (!entry) return;
  pop.querySelector('.ib-help-title').textContent = entry.title;
  const body = pop.querySelector('.ib-help-body');
  body.textContent = '';
  entry.body(ibHelpProto()).forEach((line) => {
    const p = document.createElement('p');
    p.textContent = line;
    body.appendChild(p);
  });
}
function ibHelpPlace(btn) {
  const pop = ibHelpPopEl();
  pop.hidden = false;
  const r = btn.getBoundingClientRect();
  const w = Math.min(340, window.innerWidth - 16);
  let left = r.left;
  if (left + w > window.innerWidth - 8) left = Math.max(8, window.innerWidth - 8 - w);
  let top = r.bottom + 6;
  const h = pop.offsetHeight;
  if (top + h > window.innerHeight - 8 && r.top - 6 - h > 8) top = r.top - 6 - h;
  pop.style.left = left + 'px';
  pop.style.top = top + 'px';
}
function ibHelpShow(key, btn) {
  ibHelpRender(key);
  ibHelpPlace(btn);
  ibHelpOpenKey = key;
  ibHelpOpenBtn = btn;
  document.querySelectorAll('[data-ib-help]').forEach((b) => b.setAttribute('aria-expanded', b === btn ? 'true' : 'false'));
}
function ibHelpHide() {
  const pop = document.getElementById('ib-help-pop');
  if (pop) pop.hidden = true;
  ibHelpOpenKey = null;
  ibHelpOpenBtn = null;
  document.querySelectorAll('[data-ib-help]').forEach((b) => b.setAttribute('aria-expanded', 'false'));
}
function ibHelpRefresh() {
  if (ibHelpOpenKey && ibHelpOpenBtn) ibHelpShow(ibHelpOpenKey, ibHelpOpenBtn);
}
function ibHelpWire() {
  if (ibHelpWired) return;
  ibHelpWired = true;
  document.addEventListener('click', (e) => {
    const t = e.target;
    const btn = t && t.closest ? t.closest('[data-ib-help]') : null;
    if (btn) {
      // ⓘ 在 label 里：preventDefault 拦住 label 把焦点抢到输入框上。
      e.preventDefault();
      const key = btn.getAttribute('data-ib-help');
      if (ibHelpOpenKey === key) ibHelpHide();
      else ibHelpShow(key, btn);
      return;
    }
    if (!ibHelpOpenKey) return;
    // 点提示框里、协议下拉框上不关：前者是框内操作，后者是换协议看
    // 联动内容（change 时由 ibHelpRefresh 刷新）；其余点别处都关。
    if (t && t.closest && (t.closest('#ib-help-pop') || t.closest('#nodes-ib-protocol'))) return;
    ibHelpHide();
  });
  document.addEventListener('keydown', (e) => {
    // v1.4.12：提示框开着时 Esc 只关提示框，不关弹窗——入站模板表单搬进
    // 弹窗后，全局 Esc 处理（关弹窗）也在 document 上，注册在本处理之后；
    // 这里拦住事件，免得关了提示框又顺手把弹窗也关了（表单里填了一半的值会丢）。
    if (e.key === 'Escape' && ibHelpOpenKey) { ibHelpHide(); e.stopImmediatePropagation(); }
  });
  window.addEventListener('scroll', () => { if (ibHelpOpenKey) ibHelpHide(); }, true);
  window.addEventListener('resize', () => { if (ibHelpOpenKey) ibHelpHide(); });
}
function initNodes() {
  on('#nodes-create-form', 'submit', async (e) => {
    e.preventDefault();
    const name = $('#nodes-name').value.trim();
    const address = $('#nodes-address').value.trim();
    if (!name || !address) { toast('名字与地址都要填', 'err'); return; }
    if (name.length > 64) { toast('节点名称不能超过 64 字', 'err'); return; }
    try {
      const r = await apiPost('/api/v1/nodes', { name, address });
      showNodesOnce(r.join_token || '', r.install_command || '');
      toast('节点已建，入网令牌只显示一次', 'ok');
      loadNodes();
    } catch (err) { toast('建节点失败：' + err.message, 'err'); }
  });
  on('#nodes-once-token-copy', 'click', (e) => copyText($('#nodes-once-token').value, e.target));
  on('#nodes-once-cmd-copy', 'click', (e) => copyText($('#nodes-once-cmd').value, e.target));
  ibHelpWire(); // v1.4.11：添加入站表单字段帮助（ⓘ 按钮＋提示框，document 级委托，弹窗里同样生效）。
  // 入站模板弹窗的绑定（v1.4.12）：wireNodeInboundsModal 在每次打开入站模板
  // 弹窗时现场绑（三件套＋“手动添加入站”按钮）；手动添加入站是单独的弹窗
  // （v1.4.13）：wireNodeIbAddModal 在每次打开时现场绑（协议联动／提交）；
  // 入站编辑走弹窗（v1.4.2，切片 3）：openInboundEditModal 里绑提交与取消。
  // v1.4.12：节点列表搜索（节点名／地址，前端本地过滤）与每页条数
  // （10／20／50，默认 10）：切了立即生效、回到第 1 页重渲染。
  wireListSearch('#nodes-search', (q) => { nodesSearch = q; nodesPage = 1; renderNodesList(); });
  on('#nodes-page-size', 'change', () => {
    const v = Number(($('#nodes-page-size') || {}).value) || 10;
    nodesPageSize = [10, 20, 50].indexOf(v) !== -1 ? v : 10;
    nodesPage = 1; renderNodesList();
  });
  // v1.4.14：节点列表视图切换（卡片↔列表），点一下换视图、记 localStorage、
  // 立即重渲染；按钮文案由 renderNodesList 里的 syncViewToggle 跟着改。
  on('#nodes-view-toggle', 'click', () => {
    nodesView = nodesView === 'list' ? 'card' : 'list';
    saveListView(NODES_VIEW_KEY, nodesView);
    renderNodesList();
  });
  syncViewToggle('#nodes-view-toggle', nodesView);
}
/* ---------- 节点证书（v1.4.12，切片 7） ---------- */
// 证书页落在节点页下方：签发（域名＋服务商下拉，六家自动＋手动两步走）、
// 列表（域名／服务商／到期／状态／引用）、立即续签、删除（被引用时拦住）。
// 缓存在 nodeCertsCache 里：入站列表的证书列与添加／编辑表单的置灰都
// 读它；拉不到（旧接口／网络问题）就当作没有证书，不抛错。
let nodeCertsCache = [];
let nodeCertProvidersCache = [];
// v1.4.14：上次见到的手动签发 pending（renderNodeCerts 不带 r 重渲染时
// 沿用它，免得点视图切换就把“继续验证”那块藏起来；loadNodeCerts 每次
// 都带 r，值照常刷新）。
let nodeCertManualPending = null;
function nodeCertValid(c) {
  if (!c || !c.expires_at) return false;
  const t = new Date(c.expires_at).getTime();
  return !isNaN(t) && t > Date.now();
}
// nodeCertDomainMatches 证书域名是否覆盖查询域名（v1.4.13，双模式）：
// 精确相等算覆盖；泛域名只覆盖一级子域名（*.example.com 覆盖
// hy2ceshi.example.com，不覆盖裸域 example.com，也不覆盖多级
// a.b.example.com）。后端 nodeCertDomainMatches 同口径。
function nodeCertDomainMatches(certDomain, domain) {
  const c = String(certDomain || '').trim().toLowerCase();
  const d = String(domain || '').trim().toLowerCase();
  if (!c || !d) return false;
  if (c === d) return true;
  if (c.slice(0, 2) !== '*.') return false;
  const base = c.slice(2);
  if (d === base || !d.endsWith('.' + base)) return false;
  const rest = d.slice(0, d.length - base.length - 1);
  return rest !== '' && rest.indexOf('.') === -1;
}
function nodeCertForDomain(domain) {
  const d = String(domain || '').trim().toLowerCase();
  if (!d) return null;
  let exact = null, wild = null;
  nodeCertsCache.forEach((c) => {
    if (!nodeCertValid(c)) return;
    const cd = String(c.domain || '').trim().toLowerCase();
    if (cd === d) {
      if (!exact || new Date(c.expires_at) > new Date(exact.expires_at)) exact = c;
      return;
    }
    if (nodeCertDomainMatches(cd, d)) {
      if (!wild || new Date(c.expires_at) > new Date(wild.expires_at)) wild = c;
    }
  });
  return exact || wild; // 精确和泛域名两个都有时精确优先
}
async function loadNodeCerts() {
  const wrap = $('#node-certs-list');
  try {
    const r = await api('/api/v1/node-certs');
    nodeCertsCache = r.certs || [];
    nodeCertProvidersCache = r.providers || [];
    renderNodeCerts(r);
    syncCertGate();
    return r;
  } catch (e) {
    nodeCertsCache = [];
    if (wrap) wrap.innerHTML = '<div class="error-box">加载证书失败：' + esc(e.message) + '</div>';
    syncCertGate();
    return null;
  }
}
// 证书状态徽章（v1.4.14：卡片与列表行共用）：valid 绿“有效”、
// expiring 黄“快到期”、expired 红“已过期”，其余灰“未知”。
function certStatusOf(c) {
  const st = c.status || 'unknown';
  if (st === 'valid') return { badge: 'green', text: '有效' };
  if (st === 'expiring') return { badge: 'yellow', text: '快到期' };
  if (st === 'expired') return { badge: 'red', text: '已过期' };
  return { badge: 'grey', text: '未知' };
}
// 证书三个操作按钮（v1.4.14：卡片与列表行共用同一套）：复制域名、
// 立即续签（手动模式的证书 disabled，须重新走手动签发）、删除。
function certActionButtons(c) {
  return '<button class="btn secondary sm" type="button" data-cert-copy="' + esc(c.domain || '') + '">复制</button>' +
    '<button class="btn secondary sm" type="button" data-cert-renew="' + esc(String(c.ref)) + '"' + (c.provider === 'manual' ? ' disabled title="手动模式的证书不能一键续签，请重新走手动签发"' : '') + '>立即续签</button>' +
    '<button class="btn danger sm" type="button" data-cert-del="' + esc(String(c.ref)) + '" data-cert-domain="' + esc(c.domain || '') + '">删除</button>';
}
// 证书小卡片（v1.4.14）：复用节点页同款 .mini-card，放在 #node-certs-list
// 的 .nodes-grid 网格里（方形小卡片，min-height 260px、按钮沉底）；
// 旧 .card＋.kv 那版的全部信息都在：域名（ref）、服务商、状态／到期、
// 引用它的入站、上次续签失败、复制／续签／删除。
function certMiniCard(c) {
  const s = certStatusOf(c);
  const used = (c.used_by || []).join('、') || '没被入站引用';
  return '<div class="mini-card static" data-cert-card="' + esc(String(c.ref)) + '">' +
    '<div class="mini-top"><h3>' + esc(c.domain || '') + '（ref ' + esc(String(c.ref)) + '）</h3><span class="badge ' + s.badge + '">' + s.text + '</span></div>' +
    '<div class="mini-row"><span>服务商</span><b>' + esc(c.provider_name || c.provider || '') + '</b></div>' +
    '<div class="mini-row"><span>状态</span><b>' + esc(c.expires_at ? fmtDate(c.expires_at) + '（剩余 ' + String(c.days_left == null ? '—' : c.days_left) + ' 天）' : '—') + '</b></div>' +
    '<div class="mini-row"><span>引用它的入站</span><b>' + esc(used) + '</b></div>' +
    (c.last_error ? '<div class="mini-row"><span>上次续签失败</span><b>' + esc(c.last_error) + '</b></div>' : '') +
    '<div class="btn-row">' + certActionButtons(c) + '</div></div>';
}
// 证书列表行（v1.4.14，列表视图）：紧凑行，一行一条；卡片里的信息
// 一个不少（域名、服务商、状态／到期、引用它的入站、上次续签失败、
// 复制／续签／删除），只是横向排。
function certListRow(c) {
  const s = certStatusOf(c);
  const used = (c.used_by || []).join('、') || '没被入站引用';
  return '<div class="list-row" data-cert-row="' + esc(String(c.ref)) + '">' +
    '<span class="cell cell-name">' + esc(c.domain || '') + '（ref ' + esc(String(c.ref)) + '）</span>' +
    '<span class="cell">' + esc(c.provider_name || c.provider || '') + '</span>' +
    '<span class="cell"><span class="badge ' + s.badge + '">' + s.text + '</span> ' + esc(c.expires_at ? fmtDate(c.expires_at) + '（剩余 ' + String(c.days_left == null ? '—' : c.days_left) + ' 天）' : '—') + '</span>' +
    '<span class="cell cell-wrap"><span class="muted">引用：</span>' + esc(used) + '</span>' +
    (c.last_error ? '<span class="cell cell-wrap"><span class="muted">上次续签失败：</span>' + esc(c.last_error) + '</span>' : '') +
    '<span class="btn-row">' + certActionButtons(c) + '</span></div>';
}
function renderNodeCerts(r) {
  const wrap = $('#node-certs-list');
  if (!wrap) return;
  // v1.4.14：按记住的视图切容器 class 与切换按钮文案（默认卡片视图）。
  applyListViewClass(wrap, certsView);
  syncViewToggle('#node-certs-view-toggle', certsView);
  // v1.4.13：服务商下拉框只列已配置的服务商＋手动模式（手动恒可用）：
  // 未配置的不出现（不是灰掉、不是 disabled，选都选不到才算数），免得
  // 选了点签发报 400。
  // v1.4.16：每次渲染前按当前 providers 列表重建 option（不只是删）：
  // 1.4.13 只删不补，同会话内新配好的服务商要刷新页面才出现；重建后
  // 已配置的出现、未配置的不出现、手动恒保留，顺序与 providers 列表
  // 一致（后端按 dnsProviderIDs 顺序发，即下拉框顺序）。重建前记下当
  // 前选中值，建完能留就留，留不住（刚被取消配置）才退到剩下的第一
  // 个可用项（有已配置的就选第一个已配置的，没有就选 manual）。
  const sel = $('#node-cert-provider');
  if (sel && nodeCertProvidersCache.length) {
    const configuredCount = nodeCertProvidersCache.filter((x) => x.id !== 'manual' && x.configured).length;
    const prev = sel.value;
    sel.innerHTML = '';
    nodeCertProvidersCache.forEach((p) => {
      if (p.id !== 'manual' && !p.configured) return; // 未配置的不出现
      const o = document.createElement('option');
      o.value = p.id;
      o.textContent = p.name || dnsProviderDisplay(p.id) || p.id;
      sel.appendChild(o);
    });
    if (Array.from(sel.options).some((o) => o.value === prev)) sel.value = prev;
    else if (sel.options.length) sel.value = sel.options[0].value;
    // 一个都没配：下拉框只剩手动模式，同时在证书卡片给一句提示。
    const hint = $('#node-cert-provider-hint');
    if (hint) {
      const show = configuredCount === 0;
      hint.classList.toggle('hidden', !show);
      hint.hidden = !show;
    }
  }
  // 手动签发进行中：回填 TXT 名与值，显示继续验证／取消。
  // v1.4.14：不带 r 重渲染（视图切换）时沿用上次的 pending。
  const mp = r ? r.manual_pending : nodeCertManualPending;
  nodeCertManualPending = mp || null;
  const box = $('#node-cert-manual-box');
  if (box) {
    if (mp) {
      $('#node-cert-manual-txt-name').textContent = mp.txt_name || '';
      $('#node-cert-manual-txt-value').textContent = mp.txt_value || '';
      box.dataset.ref = String(mp.ref || '');
      box.dataset.domain = mp.domain || '';
      box.classList.remove('hidden'); box.hidden = false;
    } else { box.classList.add('hidden'); box.hidden = true; }
  }
  const certs = nodeCertsCache;
  if (!certs.length) { wrap.innerHTML = '<div class="empty">还没有节点证书。上面填域名、选服务商，签发后 HY2／TUIC／AnyTLS／Trojan／VLESS＋TLS 入站才能用。</div>'; return; }
  wrap.innerHTML = certs.map((c) => certsView === 'list' ? certListRow(c) : certMiniCard(c)).join('');
  // v1.4.13：每行一个“复制”按钮，点一下复制该行证书的域名（粘到三件套
  // 的 HY2 域名框或添加／编辑表单的 SNI 框里用；样式与同行的续签／删除
  // 按钮一致，btn secondary sm）。走现成的 copyText，成功自带 toast。
  $$('[data-cert-copy]', wrap).forEach((b) => b.addEventListener('click', () => copyText(b.dataset.certCopy, b)));
  $$('[data-cert-renew]', wrap).forEach((b) => b.addEventListener('click', async () => {
    b.disabled = true;
    try {
      await apiPost('/api/v1/node-certs/' + encodeURIComponent(b.dataset.certRenew) + '/renew', {});
      toast('已续签，节点 60 秒内拉到新证书', 'ok');
      loadNodeCerts();
    } catch (e) { showNodeCertError(e.message); }
    b.disabled = false;
  }));
  $$('[data-cert-del]', wrap).forEach((b) => b.addEventListener('click', async () => {
    const ok = await confirmModal('删除证书', '<p>确定删除域名 <b>' + esc(b.dataset.certDomain || '') + '</b> 的证书吗？</p><p>正在被入站引用的证书删不掉（先删入站或换域名）；删掉后文件与元数据一起清除。</p>', '删除', true);
    if (!ok) return;
    try {
      await api('/api/v1/node-certs/' + encodeURIComponent(b.dataset.certDel), { method: 'DELETE' });
      toast('已删除', 'ok'); loadNodeCerts(); if (currentInboundsNodeId) loadNodeInbounds(currentInboundsNodeId, currentInboundsNodeName);
    } catch (e) { showNodeCertError(e.message); }
  }));
}
function showNodeCertError(msg) {
  const box = $('#node-cert-error');
  if (!box) return;
  if (!msg) { box.classList.add('hidden'); box.hidden = true; box.textContent = ''; return; }
  box.textContent = msg; box.classList.remove('hidden'); box.hidden = false;
}
// certGateNeedsCert 某协议值是不是要证书的 TLS 入站（与后端
// inboundNeedsCert 同口径：HY2／TUIC／AnyTLS／Trojan／VLESS＋TLS。
// agent 0.1.8 起 Trojan 也是 Xray TLS 入站，缺证书同样 fail-closed
// 跳过，所以 Trojan 与 HY2／TUIC／AnyTLS／TLS 一样处理）。
function certGateNeedsCert(proto) {
  return proto === 'hysteria2' || proto === 'tuic' || proto === 'anytls' || proto === 'trojan' || proto === 'vless-tls';
}
// syncCertGate 添加入站表单的证书置灰（v1.4.12）：
// ① 一张证书都没有时，HY2／TUIC／AnyTLS／Trojan／VLESS＋TLS 五个协议
//    选项 disabled（点不开），先去下方签发证书；
// ② 有证书但当前 SNI／域名没有可用证书时，同样禁用这五个选项，提示
//    栏写明该域名暂无证书；
// ③ SNI 空着时只看有没有证书（还没选域名，不按域名拦）。
// 注意：disabled 的 option 程序上仍能设值（回归测试编程式选值提交不受
// 影响），拦的是用户点选；真正提交时下面 certGateBlocks 再拦一次。
function syncCertGate() {
  const sel = $('#nodes-ib-protocol');
  if (!sel) return;
  const sni = presetSelectValue($('#nodes-ib-sni'), $('#nodes-ib-sni-custom'));
  const domainCert = sni ? nodeCertForDomain(sni) : null;
  const hasAny = nodeCertsCache.some((c) => nodeCertValid(c));
  const allow = sni ? !!domainCert : hasAny;
  Array.from(sel.options).forEach((o) => {
    if (certGateNeedsCert(o.value)) o.disabled = !allow;
  });
  const hint = $('#nodes-ib-cert-hint');
  if (hint) {
    if (sni && !domainCert) hint.textContent = '域名 ' + sni + ' 暂无证书：' + (hasAny ? '这五个 TLS 协议暂时选不了，先在下方签发该域名的证书。' : 'HY2／TUIC／AnyTLS／Trojan／VLESS＋TLS 需要真证书，当前一张证书都没有，先在下方“节点证书”里签发。');
    else if (!hasAny) hint.textContent = 'HY2／TUIC／AnyTLS／Trojan／VLESS＋TLS 需要真证书：当前一张证书都没有，先在下方“节点证书”里签发。';
    else if (sni && domainCert) hint.textContent = '域名 ' + sni + ' 有可用证书（ref ' + domainCert.ref + '），TLS 入站可以直接用。';
    else hint.textContent = '';
  }
}
// certGateBlocks 提交时再拦一次：TLS 入站＋域名没有可用证书就拦住，
// 并提示先签发；origProto／origDomain 是编辑时原入站的值——
// 原来就是这套协议＋域名（已置灰的老入站）时只改其它字段不拦，
// 免得无证书的老入站连端口都改不了。
function certGateBlocks(proto, domain, origProto, origDomain) {
  if (!certGateNeedsCert(proto)) return '';
  if (origProto === proto && String(origDomain || '') === String(domain || '')) return '';
  if (!nodeCertForDomain(domain)) return '域名 ' + (domain || '（空）') + ' 还没有可用证书：请先在下方“节点证书”里签发，签发后 60 秒内节点换上';
  return '';
}
function initNodeCerts() {
  // v1.4.14：证书列表视图切换（卡片↔列表），点一下换视图、记 localStorage、
  // 用缓存重渲染（不重新拉接口；手动 pending 由 nodeCertManualPending 保住）。
  on('#node-certs-view-toggle', 'click', () => {
    certsView = certsView === 'list' ? 'card' : 'list';
    saveListView(CERTS_VIEW_KEY, certsView);
    renderNodeCerts();
  });
  syncViewToggle('#node-certs-view-toggle', certsView);
  on('#node-cert-issue-form', 'submit', async (e) => {
    e.preventDefault();
    showNodeCertError('');
    // v1.4.13：域名 trim＋小写化（泛域名 `*.` 前缀原样保留发给后端）。
    const domain = $('#node-cert-domain').value.trim().toLowerCase();
    const provider = $('#node-cert-provider').value;
    if (!domain) { toast('证书域名要填', 'err'); return; }
    if (provider === 'manual') {
      try {
        const r = await apiPost('/api/v1/node-certs/manual/start', { domain });
        toast('手动签发已下单：去 DNS 后台加 TXT 记录，再点“继续验证”', 'ok');
        if (r.warning) toast(r.warning, 'ok'); // v1.4.13：覆盖关系只警告、不拦截
        loadNodeCerts();
      } catch (err) { showNodeCertError(err.message); }
      return;
    }
    try {
      const r = await apiPost('/api/v1/node-certs', { domain, provider });
      toast('证书已签发，节点 60 秒内拉到', 'ok');
      if (r.warning) toast(r.warning, 'ok'); // v1.4.13：覆盖关系只警告、不拦截
      $('#node-cert-issue-form').reset();
      loadNodeCerts();
      if (currentInboundsNodeId) loadNodeInbounds(currentInboundsNodeId, currentInboundsNodeName);
    } catch (err) { showNodeCertError(err.message); }
  });
  on('#node-cert-manual-confirm', 'click', async () => {
    const box = $('#node-cert-manual-box');
    const ref = box ? Number(box.dataset.ref || 0) : 0;
    const domain = box ? box.dataset.domain || '' : '';
    if (!ref || !domain) { toast('没有进行中的手动签发', 'err'); return; }
    try {
      await apiPost('/api/v1/node-certs/manual/confirm', { ref, domain });
      toast('证书已签发，节点 60 秒内拉到', 'ok');
      loadNodeCerts();
      if (currentInboundsNodeId) loadNodeInbounds(currentInboundsNodeId, currentInboundsNodeName);
    } catch (err) { showNodeCertError(err.message); }
  });
  on('#node-cert-manual-cancel', 'click', async () => {
    try {
      await apiPost('/api/v1/node-certs/manual/cancel', {});
      toast('已取消手动签发', 'ok');
      loadNodeCerts();
    } catch (err) { showNodeCertError(err.message); }
  });
}
async function deleteLanding(index) {
  const l = landingsCache.find((x) => x.index === index);
  const ok = await confirmModal('删除落地', '<p>确定删除落地 <b>' + esc(l ? displayNameOf(l) : index) + '</b> 吗？</p><p>删除是软删除：先进入回收站保留 7 天，期间可以一键恢复；7 天后才真正清除。转发规则会立即撤掉。</p>', '删除（进回收站）', true);
  if (!ok) return;
  try { await api('/api/v1/landings/' + index, { method: 'DELETE' }); toast('已移入回收站', 'ok'); refreshCurrentPage(); }
  catch (e) { toast('删除失败：' + e.message, 'err'); }
}
async function renewLanding(index) {
  const m = openModal('<h2>续期</h2><p>续期只延长时限；如果是因为流量用完被停用，请另外「续量」。</p>' +
    '<label>快捷天数</label><select id="renew-days"><option value="30">30 天</option><option value="90" selected>90 天</option><option value="180">180 天</option><option value="365">365 天</option></select>' +
    '<label>自定义天数 <span class="hint">填了就用自定义（1–3650 天），不填用上面的快捷天数</span></label><input id="renew-days-custom" type="number" min="1" max="3650" placeholder="如 45">' +
    '<div class="btn-row"><button class="btn" id="renew-ok" type="button">确认续期</button><button class="btn secondary" id="renew-cancel" type="button">取消</button></div>');
  onIn(m, '#renew-cancel', 'click', closeModal);
  onIn(m, '#renew-ok', 'click', async () => {
    const custom = $('#renew-days-custom', m).value.trim();
    let days;
    if (custom !== '') {
      days = Number(custom);
      if (!days || days < 1 || days > 3650) { toast('自定义天数请填 1–3650', 'err'); return; }
    } else days = Number($('#renew-days', m).value);
    closeModal();
    try { await apiPost('/api/v1/landings/' + index + '/renew', { days }); toast('续期成功，已延长 ' + days + ' 天', 'ok'); refreshCurrentPage(); }
    catch (e) { toast('续期失败：' + e.message, 'err'); }
  });
}
// 编辑落地：表单预填当前值，校验规则与添加落地一致；保存前二次确认
// （写明重启 realm 会瞬断）。时长默认“保持不变”，改了才重算到期。
function editLanding(index) {
  const l = landingsCache.find((x) => x.index === index);
  if (!l) { toast('找不到这个落地，请刷新页面再试', 'err'); return; }
  const t = l.traffic || {};
  const customPorts = l.custom_ports || '';
  const sbMode = l.sb_mode || '';
  const relayAddr = (statusCache && statusCache.relay_addr) || '';
  const m = openModal('<h2>编辑落地：' + esc(displayNameOf(l)) + '</h2>' +
    '<p class="muted">改地址、端口、模式后会重写转发配置并重启 realm，正在用的节点会瞬断几秒；到期与流量状态会跟着新地址走，不会丢。</p>' +
    '<label>落地 IP 或域名</label><input id="edit-landing" value="' + esc(l.landing) + '">' +
    '<label>名称 / 备注 <span class="hint">显示名，空着显示落地地址；改名只改显示，不动转发配置</span></label><input id="edit-name" value="' + esc(l.name && l.name !== l.landing ? l.name : '') + '" placeholder="' + esc(l.landing) + '" maxlength="64">' +
    '<label>预设</label><select id="edit-preset">' +
    '<option value="1"' + (l.preset === '1' ? ' selected' : '') + '>预设 1：landing-node（Xray 核心）</option>' +
    '<option value="2"' + (l.preset === '2' ? ' selected' : '') + '>预设 2：landing-node（sing-box 核心）</option>' +
    '<option value="3"' + (l.preset === '3' ? ' selected' : '') + '>预设 3：自定义端口</option></select>' +
    '<label>模式</label><select id="edit-mode">' +
    '<option value="standard"' + (l.mode !== 'door' ? ' selected' : '') + '>标准模式</option>' +
    '<option value="door"' + (l.mode === 'door' ? ' selected' : '') + '>任意门模式</option></select>' +
    '<div id="edit-relay-addr-wrap"' + (l.mode === 'door' ? '' : ' class="hidden"') + '><label>中转地址 <span class="hint">任意门链接用这个地址生成，空着用当前设置</span></label>' +
    '<input id="edit-relay-addr" value="' + esc(relayAddr) + '" placeholder="留空 = 自动用中转机公网 IP"></div>' +
    '<div id="edit-sb-mode-wrap"' + (l.preset === '2' ? '' : ' class="hidden"') + '><label>sing-box 模式</label><select id="edit-sb-mode">' +
    '<option value="1"' + (sbMode !== '2' ? ' selected' : '') + '>TLS（443）</option>' +
    '<option value="2"' + (sbMode === '2' ? ' selected' : '') + '>明文（8080）</option></select></div>' +
    '<div id="edit-custom-ports-wrap"' + (l.preset === '3' ? '' : ' class="hidden"') + '><label>自定义端口 <span class="hint">逗号分隔，如 443,8388；可写 监听:落地，如 10443:443</span></label>' +
    '<input id="edit-custom-ports" value="' + esc(customPorts) + '" placeholder="443,8388"></div>' +
    '<label>证书域名</label><input id="edit-cert-domain" value="' + esc(l.cert_domain || '') + '" placeholder="example.com">' +
    '<label>证书签发方式 <span class="hint">决定续签方式怎么判；DNS 验证的落地续签不走 80，判自动</span></label><select id="edit-cert-mode">' +
    '<option value=""' + (!l.cert_mode ? ' selected' : '') + '>未设置（按旧逻辑判断）</option>' +
    '<option value="http01"' + (l.cert_mode === 'http01' ? ' selected' : '') + '>默认走 80（HTTP-01）</option>' +
    '<option value="dns01"' + (l.cert_mode === 'dns01' ? ' selected' : '') + '>DNS 验证 Cloudflare（DNS-01）</option></select>' +
    '<p class="small">默认走 80：落地机用 80 端口验证域名签证书，80 改了监听端口时续签要手动。DNS 验证：落地机用 Cloudflare 令牌在 DNS 里加验证记录签证书，续签全自动，不用 80；前提是装节点时选了 DNS 验证并配好令牌，没配令牌的落地别选这个。</p>' +
    '<label>节点时长</label><select id="edit-duration">' +
    '<option value="-1" selected>保持不变</option><option value="0">改为永久</option><option value="30">从现在起 30 天</option>' +
    '<option value="90">从现在起 90 天</option><option value="180">从现在起 180 天</option><option value="365">从现在起 365 天</option>' +
    '<option value="custom">自定义天数</option></select>' +
    '<div id="edit-duration-custom-wrap" class="hidden"><label>自定义天数</label><input id="edit-duration-custom" type="number" min="1" max="3650" placeholder="如 60"></div>' +
    '<label>流量上限（GB，0 = 不限）</label><input id="edit-traffic" type="number" min="0" step="1" value="' + Math.round((t.limit_bytes || 0) / 1073741824) + '">' +
    '<label>流量上限算法</label><select id="edit-traffic-mode"><option value="total"' + (t.limit_mode !== 'monthly' ? ' selected' : '') + '>总量（整个生命周期）</option><option value="monthly"' + (t.limit_mode === 'monthly' ? ' selected' : '') + '>按月（每月重置）</option></select>' +
    '<label>流量倍率 <span class="hint">实际流量 × 倍率计入订阅用量，默认 1；贵的线路调高</span></label><input id="edit-traffic-multiplier" type="number" min="0.1" max="100" step="0.1" value="' + (l.traffic_multiplier || 1) + '">' +
    '<label>限速（Mbps，0 = 不限速）</label><input id="edit-speed" type="number" min="0" step="1" value="' + (l.speed_limit_mbps || 0) + '">' +
    '<label class="checkbox-row"><input type="checkbox" id="edit-want-udp"' + (l.want_udp ? ' checked' : '') + '><span style="font-weight:400">同时转发 UDP（HY2 / TUIC 需要）</span></label>' +
    '<label>原始节点链接 <span class="hint">选填；有它卡片上才出中转链接，改地址/端口后链接自动跟着重算</span></label><input id="edit-orig-link" value="' + esc(l.orig_link || '') + '" placeholder="vless://…">' +
    '<label>节点名称 <span class="hint">选填；直连备注“&lt;名称&gt;-直连”、中转备注“&lt;名称&gt;-中转”</span></label><input id="edit-link-name" value="' + esc(l.orig_link_name || '') + '" placeholder="如 香港-01" maxlength="64">' +
    '<div class="btn-row"><button class="btn" id="edit-ok" type="button">保存（重启 realm 生效）</button><button class="btn secondary" id="edit-cancel" type="button">取消</button></div>');
  onIn(m, '#edit-cancel', 'click', closeModal);
  const syncEdit = () => {
    const pv = $('#edit-preset', m).value;
    const sbw = $('#edit-sb-mode-wrap', m); if (sbw) sbw.classList.toggle('hidden', pv !== '2');
    const cpw = $('#edit-custom-ports-wrap', m); if (cpw) cpw.classList.toggle('hidden', pv !== '3');
    const raw = $('#edit-relay-addr-wrap', m); if (raw) raw.classList.toggle('hidden', $('#edit-mode', m).value !== 'door');
  };
  onIn(m, '#edit-preset', 'change', syncEdit);
  onIn(m, '#edit-mode', 'change', syncEdit);
  onIn(m, '#edit-duration', 'change', () => {
    const w = $('#edit-duration-custom-wrap', m);
    if (w) w.classList.toggle('hidden', $('#edit-duration', m).value !== 'custom');
  });
  onIn(m, '#edit-ok', 'click', async () => {
    const payload = buildEditPayload(m);
    if (!payload) return;
    const ok = await confirmModal('保存编辑', '<p>保存后会重写转发配置并<b>重启 realm，正在用的节点会瞬断几秒</b>。确定保存吗？</p>', '保存并重启 realm');
    if (!ok) return;
    const btn = $('#edit-ok', m); if (btn) { btn.disabled = true; btn.textContent = '保存中…'; }
    try {
      const d = await api('/api/v1/landings/' + index, { method: 'PUT', body: JSON.stringify(payload) });
      if (d && d.ok === false && d.need_choice) {
        if (btn) { btn.disabled = false; btn.textContent = '保存（重启 realm 生效）'; }
        await showShareChoice(d, payload, async (choice) => {
          try {
            const d2 = await api('/api/v1/landings/' + index, { method: 'PUT', body: JSON.stringify(Object.assign({}, payload, { share_mode: choice.mode, listen_overrides: choice.overrides })) });
            if (d2 && d2.ok === false) { toast(d2.error || '保存失败', 'err'); return; }
            closeModal();
            toast('落地已更新，realm 已重启', 'ok');
            refreshCurrentPage();
          } catch (e) { toast('保存失败：' + e.message, 'err'); }
        });
        return;
      }
      if (d && d.ok === false) {
        if (btn) { btn.disabled = false; btn.textContent = '保存（重启 realm 生效）'; }
        toast(d.error || '保存失败', 'err'); return;
      }
      closeModal();
      toast('落地已更新，realm 已重启', 'ok');
      refreshCurrentPage();
    } catch (e) {
      if (btn) { btn.disabled = false; btn.textContent = '保存（重启 realm 生效）'; }
      toast('保存失败：' + e.message, 'err');
    }
  });
}
// 编辑表单取值：与 buildAddPayload 同一套校验（落地、证书域名、自定义端口、
// 自定义天数、流量/限速非负、原始链接格式、名称长度）
function buildEditPayload(m) {
  const landing = $('#edit-landing', m).value.trim();
  const certDomain = $('#edit-cert-domain', m).value.trim();
  if (!landing) { toast('请填写落地 IP 或域名', 'err'); return null; }
  if (!/^[A-Za-z0-9.-]+$/.test(landing)) { toast('落地 IP 或域名格式不正确', 'err'); return null; }
  if (certDomain && !/^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/.test(certDomain)) { toast('证书域名格式不正确，如 example.com', 'err'); return null; }
  const preset = $('#edit-preset', m).value;
  let customPorts = '';
  if (preset === '3') {
    customPorts = $('#edit-custom-ports', m).value.trim();
    if (!customPorts) { toast('自定义端口必填，如 443,8388', 'err'); return null; }
    if (!/^[\d,: ]+$/.test(customPorts)) { toast('自定义端口格式不正确，只能是数字、逗号和冒号', 'err'); return null; }
  }
  let duration = Number($('#edit-duration', m).value);
  if ($('#edit-duration', m).value === 'custom') {
    duration = Number($('#edit-duration-custom', m).value) || 0;
    if (duration < 1 || duration > 3650) { toast('自定义天数请填 1–3650', 'err'); return null; }
  }
  const trafficGb = Number($('#edit-traffic', m).value) || 0;
  if (trafficGb < 0) { toast('流量上限不能为负', 'err'); return null; }
  const speed = Number($('#edit-speed', m).value) || 0;
  if (speed < 0) { toast('限速不能为负', 'err'); return null; }
  const mult = parseTrafficMultiplier($('#edit-traffic-multiplier', m));
  if (mult == null) return null;
  const mode = $('#edit-mode', m).value;
  const relayAddr = (mode === 'door' && $('#edit-relay-addr', m)) ? $('#edit-relay-addr', m).value.trim() : '';
  const name = $('#edit-name', m).value.trim();
  if (name && name.length > 64) { toast('名称不能超过 64 字', 'err'); return null; }
  const origLink = $('#edit-orig-link', m).value.trim();
  if (origLink && !/^(vless|trojan|ss|vmess|hysteria2|hy2|tuic):\/\//.test(origLink)) { toast('原始节点链接格式不正确，需以 vless://、trojan://、ss://、vmess://、hysteria2:// 或 tuic:// 开头', 'err'); return null; }
  const linkName = $('#edit-link-name', m).value.trim();
  if (linkName && linkName.length > 64) { toast('节点名称不能超过 64 字', 'err'); return null; }
  return {
    landing, preset, mode,
    name: name || undefined,
    orig_link: origLink || undefined,
    link_name: linkName || undefined,
    relay_addr: relayAddr || undefined,
    sb_mode: preset === '2' ? $('#edit-sb-mode', m).value : undefined,
    custom_ports: customPorts || undefined,
    want_udp: $('#edit-want-udp', m).checked,
    cert_domain: certDomain || undefined,
    cert_mode: $('#edit-cert-mode', m).value || undefined,
    duration_days: duration,
    traffic_limit_bytes: Math.round(trafficGb * 1073741824),
    traffic_limit_mode: trafficGb > 0 ? $('#edit-traffic-mode', m).value : undefined,
    traffic_multiplier: mult,
    speed_limit_mbps: speed,
  };
}
// 补粘贴原始链接：给已加的落地补一个原始链接（+节点名称），卡片上立刻出中转链接
async function pasteOrigLink(index) {
  const l = landingsCache.find((x) => x.index === index);
  const m = openModal('<h2>补粘贴原始链接' + (l ? '：' + esc(displayNameOf(l)) : '') + '</h2>' +
    '<p class="muted">把落地机上生成的节点链接粘贴进来，面板会算出中转 + 直连链接，卡片上直接显示，历史记录里也会记一笔。原始链接只存在面板自己的状态文件里，不会进日志与审计。</p>' +
    '<label>原始节点链接 <span class="hint">vless:// / trojan:// / ss:// / vmess:// 开头</span></label><input id="paste-orig-link" placeholder="vless://…">' +
    '<label>节点名称 <span class="hint">选填；直连备注“&lt;名称&gt;-直连”、中转备注“&lt;名称&gt;-中转”，空着用原链接备注名</span></label><input id="paste-link-name" placeholder="如 香港-01" maxlength="64">' +
    '<div class="btn-row"><button class="btn" id="paste-ok" type="button">生成中转链接</button><button class="btn secondary" id="paste-cancel" type="button">取消</button></div>');
  onIn(m, '#paste-cancel', 'click', closeModal);
  onIn(m, '#paste-ok', 'click', async () => {
    const origLink = $('#paste-orig-link', m).value.trim();
    const linkName = $('#paste-link-name', m).value.trim();
    if (!origLink || !/^(vless|trojan|ss|vmess|hysteria2|hy2|tuic):\/\//.test(origLink)) { toast('请粘贴正确的节点链接（vless:// 等开头）', 'err'); return; }
    if (linkName && linkName.length > 64) { toast('节点名称不能超过 64 字', 'err'); return; }
    closeModal();
    try {
      const r = await apiPost('/api/v1/landings/' + index + '/orig-link', { orig_link: origLink, link_name: linkName || undefined });
      openModal('<h2>✅ 中转链接已生成</h2>' + linksHtml(r.links, r.qr) +
        '<div class="btn-row"><button class="btn" id="paste-done" type="button">完成</button></div>');
      on('#paste-done', 'click', () => { closeModal(); refreshCurrentPage(); });
    } catch (e) { toast('生成失败：' + e.message, 'err'); }
  });
}

async function addTraffic(index) {
  const m = openModal('<h2>续量</h2><p>给流量上限增加额度。续完仍有余额且之前因流量停用的，会自动恢复转发。</p>' +
    '<label>增加多少 GB</label><input id="addtraffic-gb" type="number" min="1" step="1" value="50">' +
    '<div class="btn-row"><button class="btn" id="at-ok" type="button">确认续量</button><button class="btn secondary" id="at-cancel" type="button">取消</button></div>');
  onIn(m, '#at-cancel', 'click', closeModal);
  onIn(m, '#at-ok', 'click', async () => {
    const gb = Number($('#addtraffic-gb', m).value);
    if (!gb || gb < 1) { toast('请填大于 0 的 GB 数', 'err'); return; }
    closeModal();
    try { await apiPost('/api/v1/landings/' + index + '/add-traffic', { bytes: Math.round(gb * 1073741824) }); toast('续量成功，已增加 ' + gb + ' GB', 'ok'); refreshCurrentPage(); }
    catch (e) { toast('续量失败：' + e.message, 'err'); }
  });
}
async function trafficSettings(index) {
  const l = landingsCache.find((x) => x.index === index);
  const t = (l && l.traffic) || {};
  const m = openModal('<h2>流量限制</h2>' +
    '<label>流量上限（GB，0 = 不限）</label><input id="ts-gb" type="number" min="0" step="1" value="' + Math.round((t.limit_bytes || 0) / 1073741824) + '">' +
    '<label>算法</label><select id="ts-mode"><option value="total"' + (t.limit_mode !== 'monthly' ? ' selected' : '') + '>总量</option><option value="monthly"' + (t.limit_mode === 'monthly' ? ' selected' : '') + '>按月</option></select>' +
    '<label>限速（Mbps，0 = 不限速）</label><input id="ts-speed" type="number" min="0" step="1" value="' + (l ? (l.speed_limit_mbps || 0) : 0) + '">' +
    '<div class="btn-row"><button class="btn" id="ts-ok" type="button">保存</button><button class="btn secondary" id="ts-cancel" type="button">取消</button></div>');
  onIn(m, '#ts-cancel', 'click', closeModal);
  onIn(m, '#ts-ok', 'click', async () => {
    const gb = Number($('#ts-gb', m).value) || 0;
    if (gb < 0) { toast('流量上限不能为负', 'err'); return; }
    closeModal();
    try {
      await apiPost('/api/v1/landings/' + index + '/traffic', {
        limit_bytes: Math.round(gb * 1073741824),
        limit_mode: gb > 0 ? $('#ts-mode', m).value : 'total',
        speed_limit_mbps: Number($('#ts-speed', m).value) || 0,
      });
      toast('流量限制已保存', 'ok'); refreshCurrentPage();
    } catch (e) { toast('保存失败：' + e.message, 'err'); }
  });
}
async function deleteSubnode(index, domain) {
  const ok = await confirmModal('删除子节点', '<p>确定删除子节点 <b>' + esc(domain) + '</b> 吗？</p><p>只删面板里的登记，不影响该落地的转发和其他节点。落地机上的对应节点请另外到落地机上删除。</p>', '删除该节点', true);
  if (!ok) return;
  try {
    await api('/api/v1/landings/' + index + '/subnodes/' + encodeURIComponent(domain), { method: 'DELETE' });
    toast('子节点已删除', 'ok'); refreshCurrentPage();
  } catch (e) { toast('删除失败：' + e.message + '（若接口未提供，请到落地机手动处理）', 'err'); }
}

/* ---------- 给落地加节点：两步向导 ---------- */
async function addNodeWizard(index) {
  const l = landingsCache.find((x) => x.index === index);
  const m = openModal('<h2>给落地加节点（第 1 步，共 2 步）</h2>' +
    '<p>面板在中转机上，够不着落地机，所以分两步走：先填新子域名，拿到落地机命令；到落地机跑完命令，再把新链接粘贴回来登记。</p>' +
    '<p class="small">落地：' + esc(l ? displayNameOf(l) : index) + '</p>' +
    '<label>新子域名 <span class="hint">如 node2.example.com</span></label><input id="wizard-sub" placeholder="node2.example.com">' +
    '<div class="btn-row"><button class="btn" id="wz-next" type="button">生成落地机命令</button><button class="btn secondary" id="wz-cancel" type="button">取消</button></div>');
  onIn(m, '#wz-cancel', 'click', closeModal);
  onIn(m, '#wz-next', 'click', async () => {
    const sub = $('#wizard-sub', m).value.trim();
    if (!sub || !/^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/.test(sub)) { toast('请填写正确的子域名，如 node2.example.com', 'err'); return; }
    try {
      const r = await apiPost('/api/v1/landings/' + index + '/add-node', { subdomain: sub });
      const cmd = (r.wizard && r.wizard.landing_cmd) || r.landing_cmd || '';
      const m2 = openModal('<h2>给落地加节点（第 2 步，共 2 步）</h2>' +
        '<p>到落地机上执行下面这条命令，跑完会生成新节点链接：</p>' +
        (cmd ? '<pre class="cmd">' + esc(cmd) + '</pre><div class="btn-row"><button class="btn secondary sm" type="button" data-copy="' + esc(cmd) + '">复制命令</button></div>' : '<p class="muted">后端未返回命令，请联系管理员确认命令模板。</p>') +
        '<label>粘贴新节点链接 <span class="hint">落地机跑完命令后生成的那条链接</span></label><input id="wz-link" placeholder="vless://…@' + esc(sub) + ':443?…">' +
        '<div class="btn-row"><button class="btn" id="wz-done" type="button">登记并生成 twin</button><button class="btn secondary" id="wz-back" type="button">上一步</button></div>');
      onIn(m2, '#wz-back', 'click', () => addNodeWizard(index));
      onIn(m2, '#wz-done', 'click', async () => {
        const link = $('#wz-link', m2).value.trim();
        if (!link || !/^(vless|trojan|ss|vmess|hysteria2|hy2|tuic):\/\//.test(link)) { toast('请粘贴正确的节点链接（vless:// 等开头）', 'err'); return; }
        try {
          const r2 = await apiPost('/api/v1/landings/' + index + '/add-node', { subdomain: sub, new_link: link });
          closeModal();
          openModal('<h2>✅ 子节点已登记</h2>' + linksHtml(r2.links, r2.qr) +
            '<div class="btn-row"><button class="btn" id="wz-close" type="button">完成</button></div>');
          on('#wz-close', 'click', () => { closeModal(); refreshCurrentPage(); });
        } catch (e) { toast('登记失败：' + e.message, 'err'); }
      });
    } catch (e) { toast('生成命令失败：' + e.message, 'err'); }
  });
}

/* ---------- 节点链接 ---------- */
let linksPage = 1;
let linksGroupsCache = []; // 本页接口返回的全部历史分组（按文件顺序）
// 勾选的分组：存全局序号（0 起，按文件顺序），翻页也不丢
let linksSelected = new Set();
function groupTagsHtml(g) {
  const labels = (g.links || []).map((lk) => lk.label);
  let h = '';
  if (labels.indexOf('中转') >= 0) h += '<span class="tag relay">中转</span>';
  if (labels.indexOf('任意门') >= 0) h += '<span class="tag relay">任意门</span>';
  if (labels.indexOf('直连') >= 0) h += '<span class="tag direct">直连</span>';
  return h;
}
function renderLinksToolbar() {
  const bar = $('#links-toolbar');
  if (!bar) return;
  const n = linksSelected.size;
  bar.innerHTML = '<div class="links-toolbar">' +
    '<label><input type="checkbox" id="links-select-all"> 全选（本页）</label>' +
    '<span class="small" id="links-selected-count">' + (n ? '已选 ' + n + ' 组' : '未选中') + '</span>' +
    '<button class="btn secondary sm" type="button" id="links-batch-export" ' + (n ? '' : 'disabled') + '>批量导出</button>' +
    '<button class="btn danger sm" type="button" id="links-batch-delete" ' + (n ? '' : 'disabled') + '>批量删除</button></div>';
  onIn(bar, '#links-select-all', 'change', (e) => {
    const boxes = $$('[data-group-check]', $('#links-history'));
    boxes.forEach((cb) => {
      cb.checked = e.target.checked;
      const ord = Number(cb.dataset.groupCheck);
      if (e.target.checked) linksSelected.add(ord); else linksSelected.delete(ord);
    });
    renderLinksToolbar();
  });
  onIn(bar, '#links-batch-export', 'click', () => {
    const groups = linksGroupsCache.filter((g, i) => linksSelected.has(i));
    const text = linksExportText(groups);
    if (!text) { toast('选中的组里没有链接', 'err'); return; }
    downloadTextFile(linksExportFileName(), text);
  });
  onIn(bar, '#links-batch-delete', 'click', async () => {
    const ords = Array.from(linksSelected).sort((a, b) => a - b);
    const ok = await confirmModal('批量删除历史记录', '<p>确定删除选中的 <b>' + ords.length + ' 组</b>历史记录吗？</p><p>只删历史记录（nodes.txt 里的这几组），不影响落地转发与节点本身。</p>', '删除 ' + ords.length + ' 组', true);
    if (!ok) return;
    try {
      await apiPost('/api/v1/links/delete', { indices: ords });
      linksSelected.clear();
      toast('已删除 ' + ords.length + ' 组历史记录', 'ok');
      loadLinks();
    } catch (e) { toast('删除失败：' + e.message, 'err'); }
  });
}
async function loadLinks(page) {
  if (page) linksPage = page;
  const wrap = $('#links-history');
  try {
    const r = await api('/api/v1/links');
    const groups = r.groups || [];
    linksGroupsCache = groups;
    if (!groups.length) {
      wrap.innerHTML = '<div class="empty">暂无历史记录（nodes.txt 为空）。</div>';
      const bar = $('#links-toolbar'); if (bar) bar.innerHTML = '';
      return;
    }
    const pg = paginate(groups, linksPage);
    linksPage = pg.cur;
    const start = (pg.cur - 1) * PAGE_SIZE;
    wrap.innerHTML = pg.slice.map((g, i) => {
      const ord = start + i;
      const checked = linksSelected.has(ord) ? ' checked' : '';
      return '<div class="card" style="margin-bottom:16px">' +
        '<div class="group-head" data-group-head="' + ord + '">' +
        '<input type="checkbox" data-group-check="' + ord + '"' + checked + ' aria-label="选中该组历史记录">' +
        '<span class="group-title">' + esc(g.name || g.landing || '') + ' <span class="small">#' + esc(g.landing_index) + ' · ' + esc(g.landing || '') + '</span></span>' +
        groupTagsHtml(g) +
        (g.mode === 'door' ? ' <span class="badge blue">任意门模式</span>' : '') +
        '<button class="btn secondary sm" type="button" data-group-export="' + ord + '">导出</button>' +
        '<button class="btn danger sm" type="button" data-group-del="' + ord + '">删除</button>' +
        '<button class="btn secondary sm" type="button" data-group-toggle="' + ord + '">查看</button></div></div>';
    }).join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'links');
    renderLinksToolbar();
    // 全选框状态：本页全勾才勾上
    const all = $('#links-select-all');
    if (all) {
      const boxes = $$('[data-group-check]', wrap);
      all.checked = boxes.length > 0 && boxes.every((cb) => cb.checked);
    }
    // 勾选框：只改选中集，不触发折叠
    $$('[data-group-check]', wrap).forEach((cb) => cb.addEventListener('change', () => {
      const ord = Number(cb.dataset.groupCheck);
      if (cb.checked) linksSelected.add(ord); else linksSelected.delete(ord);
      renderLinksToolbar();
      const all2 = $('#links-select-all');
      if (all2) {
        const boxes = $$('[data-group-check]', wrap);
        all2.checked = boxes.length > 0 && boxes.every((b) => b.checked);
      }
    }));
    // 点行（除勾选框与按钮外）弹出该组详情弹窗
    $$('[data-group-head]', wrap).forEach((head) => head.addEventListener('click', (e) => {
      if (e.target.closest('input[type="checkbox"]') || e.target.closest('button')) return;
      showLinksGroupModal(Number(head.dataset.groupHead));
    }));
    $$('[data-group-toggle]', wrap).forEach((b) => b.addEventListener('click', (e) => { e.stopPropagation(); showLinksGroupModal(Number(b.dataset.groupToggle)); }));
    $$('[data-group-export]', wrap).forEach((b) => b.addEventListener('click', (e) => {
      e.stopPropagation();
      const g = linksGroupsCache[Number(b.dataset.groupExport)];
      const text = linksExportText([g]);
      if (!text) { toast('该组没有链接可导出', 'err'); return; }
      downloadTextFile(linksExportFileName(), text);
    }));
    $$('[data-group-del]', wrap).forEach((b) => b.addEventListener('click', async (e) => {
      e.stopPropagation();
      const ord = Number(b.dataset.groupDel);
      const g = linksGroupsCache[ord];
      const ok = await confirmModal('删除历史记录', '<p>确定删除 <b>' + esc(g ? (g.name || g.landing || '') : ord) + '</b> 这一组历史记录吗？</p><p>只删历史记录，不影响落地转发与节点本身。</p>', '删除该组', true);
      if (!ok) return;
      try {
        await apiPost('/api/v1/links/delete', { indices: [ord] });
        linksSelected.delete(ord);
        toast('该组历史记录已删除', 'ok');
        loadLinks();
      } catch (err) { toast('删除失败：' + err.message, 'err'); }
    }));
    if (pg.totalPages > 1) wirePager(wrap, 'links', pg.cur, pg.totalPages, (p) => loadLinks(p));
  } catch (e) {
    wrap.innerHTML = '<div class="card"><div class="error-box">加载失败：' + esc(e.message) + '</div></div>';
  }
}
// 展开/收起一组历史记录（默认折叠）
// 历史记录组详情弹窗（v1.3.21）：以前点行是行内展开，现在统一走弹窗。
function showLinksGroupModal(ord) {
  const g = linksGroupsCache[ord];
  if (!g) return;
  openModal('<h2>' + esc(g.name || g.landing || '') + '</h2>' + linksHtml(g.links, g.qr));
}
// v1.3.16：节点链接生成方式（三选一），上次的选择记在 localStorage，默认全部。
const TWIN_MODE_KEY = 'relay-panel-twin-mode';
function twinMode() {
  const s = document.querySelector('select[name="twin-mode"]');
  const v = s ? s.value : 'both';
  return (v === 'relay' || v === 'direct' || v === 'both') ? v : 'both';
}
function initTwin() {
  // 恢复上次的生成方式（非法值回落到全部）
  const sel = document.querySelector('select[name="twin-mode"]');
  try {
    const saved = localStorage.getItem(TWIN_MODE_KEY);
    if (sel && (saved === 'relay' || saved === 'direct' || saved === 'both')) sel.value = saved;
  } catch (e) { /* 隐私模式等存不了就每次回默认 */ }
  if (sel) sel.addEventListener('change', () => {
    try { localStorage.setItem(TWIN_MODE_KEY, twinMode()); } catch (e) {}
  });
  on('#twin-form', 'submit', async (e) => {
    e.preventDefault();
    const link = $('#twin-link').value.trim();
    const landingIp = $('#twin-landing').value.trim();
    const sni = $('#twin-sni').value.trim();
    const name = $('#twin-name') ? $('#twin-name').value.trim() : '';
    if (name && name.length > 64) { toast('节点名称不能超过 64 字', 'err'); return; }
    if (!/^(vless|trojan|ss|vmess|hysteria2|hy2|tuic):\/\//.test(link)) { toast('链接格式不正确，需以 vless://、trojan://、ss://、vmess://、hysteria2:// 或 tuic:// 开头', 'err'); return; }
    if (!landingIp) { toast('请填写落地 IP', 'err'); return; }
    try {
      const r = await apiPost('/api/v1/links/twin', { link, landing_ip: landingIp, sni, name: name || undefined, mode: twinMode() });
      const relayLabel = r.relay_label || '中转';
      const links = [];
      if (r.relay_link) links.push({ label: relayLabel, link: r.relay_link });
      if (r.direct_link) links.push({ label: '直连', link: r.direct_link });
      openModal('<h2>生成结果</h2>' +
        (r.manual ? '<div class="warn-box">' + esc(r.manual) + '</div>' : '') +
        (r.persist_error ? '<div class="warn-box">' + esc(r.persist_error) + '</div>' : '') +
        ((r.qr && Object.keys(r.qr).length) ? '' : '<p class="muted">二维码生成失败，仅显示链接。</p>') +
        linksHtml(links, r.qr));
      toast('twin 生成成功', 'ok');
    } catch (err) { toast('生成失败：' + err.message, 'err'); }
  });
}

/* ---------- 证书 ---------- */
let certsPage = 1;
async function loadCerts(page) {
  if (page) certsPage = page;
  const wrap = $('#cert-list');
  try {
    const r = await api('/api/v1/certs');
    const certs = r.certs || [];
    // 证书到期提醒需要 Telegram：没配时证书页提示去设置里配（v1.3.6）。
    const tgHint = $('#cert-tg-hint');
    if (tgHint) tgHint.innerHTML = r.telegram_configured === false ? '<div class="warn-box">证书到期提醒需要配置 Telegram：到设置页 → Telegram 填好 Bot Token 与 Chat ID，否则证书快到期时只在面板里标红，不会发消息提醒。</div>' : '';
    if (!certs.length) { wrap.innerHTML = '<div class="empty">暂无证书信息。</div>'; }
    else {
      const pg = paginate(certs, certsPage);
      certsPage = pg.cur;
      wrap.innerHTML = pg.slice.map((c) => {
        const days = c.days_left;
        const pct = days == null ? 0 : Math.max(0, Math.min(100, Math.round(days / 90 * 100)));
        const cls = days != null && days < 7 ? 'bad' : days != null && days < 30 ? 'warn' : '';
        // 续签方式本身已含“自动/手动”，后缀只在没写明时补一个“（自动）”；
        // 以前这里无条件补，自动的会叠成“自动（走 80）（自动）”，明文的会错写成“（自动）”。
        const rm = c.renew_mode || '';
        const renewSuffix = c.need_manual ? '（需手动续签，见下方六步引导）' : (rm.indexOf('自动') === 0 || rm.indexOf('无证书') === 0 ? '' : '（自动）');
        return '<div class="card" style="margin-bottom:16px"><div style="display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap">' +
          '<h2>' + esc(c.domain || '') + '</h2>' + certBadge(days) + '</div>' +
          '<div class="kv"><span>落地</span><b>' + esc(c.landing || '') + '</b></div>' +
          '<div class="kv"><span>到期日</span><b>' + esc(c.expiry || '—') + '</b></div>' +
          '<div class="kv"><span>续签方式</span><b>' + esc(rm) + renewSuffix + '</b></div>' +
          '<div class="bar-row"><span>剩余天数进度（按 90 天有效期）</span><span>' + (days == null ? '未知' : days + ' 天') + '</span></div>' +
          '<div class="bar"><i class="' + cls + '" style="width:' + pct + '%"></i></div></div>';
      }).join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'certs');
      if (pg.totalPages > 1) wirePager(wrap, 'certs', pg.cur, pg.totalPages, (p) => loadCerts(p));
    }
    // 续签引导的落地选择
    const sel = $('#renew-landing');
    const opts = (landingsCache.length ? landingsCache : certs.map((c) => ({ index: c.landing_index, cert_domain: c.domain, landing: c.landing })));
    sel.innerHTML = opts.map((l) => '<option value="' + l.index + '">' + esc(l.name || l.cert_domain || l.domain || l.landing) + '</option>').join('');
    if (opts.length) loadRenewGuide(Number(sel.value));
    sel.onchange = () => loadRenewGuide(Number(sel.value));
  } catch (e) {
    wrap.innerHTML = '<div class="card"><div class="error-box">加载失败：' + esc(e.message) + '</div></div>';
    $('#renew-steps').innerHTML = '<li>续签引导加载失败：' + esc(e.message) + '</li>';
  }
}
async function loadRenewGuide(landingIndex) {
  const list = $('#renew-steps');
  try {
    const r = await api('/api/v1/certs/renew-guide');
    const steps = r.steps || [];
    if (!steps.length) { list.innerHTML = '<li>暂无续签引导。</li>'; return; }
    const doneKey = 'relay-panel-renew-' + landingIndex;
    let done = [];
    try { done = JSON.parse(localStorage.getItem(doneKey) || '[]'); } catch (e) { done = []; }
    list.innerHTML = steps.map((s, i) =>
      '<li class="' + (done.indexOf(s.n) >= 0 ? 'done' : '') + '"><button class="step-check" type="button" data-step="' + s.n + '" aria-label="标记第 ' + s.n + ' 步完成">' + (done.indexOf(s.n) >= 0 ? '✓' : '') + '</button>' +
      '<div style="flex:1"><div class="step-title">第 ' + s.n + ' 步：' + esc(s.title || '') + '</div>' +
      '<div class="step-detail">' + esc(s.detail || '') + '</div>' +
      (s.cmd ? '<pre class="cmd">' + esc(s.cmd) + '</pre><div class="btn-row"><button class="btn secondary sm" type="button" data-copy="' + esc(s.cmd) + '">复制命令</button></div>' : '') +
      '</div></li>').join('');
    $$('[data-step]', list).forEach((b) => b.addEventListener('click', async () => {
      const n = Number(b.dataset.step);
      const idx = done.indexOf(n);
      if (idx >= 0) done.splice(idx, 1); else done.push(n);
      localStorage.setItem(doneKey, JSON.stringify(done));
      try { await apiPost('/api/v1/certs/renew-guide/progress', { landing_index: landingIndex, step: Math.max.apply(null, done.concat([0])) }); } catch (e) { /* 进度保存失败不阻塞 */ }
      loadRenewGuide(landingIndex);
    }));
  } catch (e) {
    list.innerHTML = '<li>续签引导加载失败：' + esc(e.message) + '</li>';
  }
}

/* ---------- 服务与日志 ---------- */
async function loadService() {
  try {
    const s = await api('/api/v1/status');
    const r = s.realm || {};
    $('#service-status').innerHTML =
      '<div class="kv"><span>状态</span><b>' + (r.running ? '<span class="dot green"></span>运行中' : '<span class="dot red"></span>未运行') + '</b></div>' +
      '<div class="kv"><span>版本</span><b>' + esc(r.version || '—') + '</b></div>' +
      '<div class="kv"><span>已运行</span><b>' + fmtDur(r.uptime_seconds || 0) + '</b></div>';
  } catch (e) { $('#service-status').innerHTML = '<div class="error-box">状态加载失败：' + esc(e.message) + '</div>'; }
  loadLogs(); loadPortsTable();
  logsTimer = setInterval(() => { if ($('#logs-auto').checked) loadLogs(); }, 10000);
}
async function loadLogs() {
  const box = $('#log-box');
  try {
    const r = await api('/api/v1/logs?lines=200');
    // 后端可能返回空字符串行（如空日志文件切出来的 [""]）：先过滤再渲染，
    // 否则 lines.length=1 会被判为有内容，日志框只剩空白。
    const lines = (r.lines || []).filter((l) => String(l).trim() !== '');
    box.textContent = lines.length ? lines.join('\n') : (r.note || '暂无日志（日志文件不存在或为空）。');
    box.scrollTop = box.scrollHeight;
  } catch (e) { box.textContent = '日志加载失败：' + e.message; }
}
let portsPage = 1;
async function loadPortsTable(page) {
  if (page) portsPage = page;
  const wrap = $('#ports-table');
  try {
    const r = await api('/api/v1/ports');
    const list = r.listening || [];
    if (!list.length) { wrap.innerHTML = '<div class="empty">暂无监听端口。</div>'; return; }
    const pg = paginate(list, portsPage);
    portsPage = pg.cur;
    let html = '<div class="table-wrap"><table><thead><tr><th>协议</th><th>端口</th><th>进程</th></tr></thead><tbody>' +
      pg.slice.map((p) => '<tr><td>' + esc(p.proto) + '</td><td>' + esc(p.port) + '</td><td>' + esc(p.process || '—') + '</td></tr>').join('') + '</tbody></table></div>';
    html += pagerHtml(pg.total, pg.cur, pg.totalPages, 'ports');
    wrap.innerHTML = html;
    if (pg.totalPages > 1) wirePager(wrap, 'ports', pg.cur, pg.totalPages, (p) => loadPortsTable(p));
  } catch (e) { wrap.innerHTML = '<div class="error-box">端口表加载失败：' + esc(e.message) + '</div>'; }
}
function initService() {
  $$('[data-service]').forEach((b) => b.addEventListener('click', async () => {
    const act = b.dataset.service;
    if (act === 'stop') {
      const ok = await confirmModal('停止 realm', '<p>停止后所有中转都会断开，节点不可用。确定停止吗？</p>', '停止', true);
      if (!ok) return;
    }
    try {
      await apiPost('/api/v1/service/' + act, {});
      toast('已执行：' + act, 'ok'); loadService();
    } catch (e) { toast('操作失败：' + e.message, 'err'); }
  }));
  on('#logs-refresh', 'click', loadLogs);
}

/* ---------- 设置 ---------- */
// 登录安全卡片渲染（v1.3.8，顶层函数：不能写进 initSettings 里，
// 否则 loadSettingsPage 调用时找不到——1.1.8 白页就是这么来的）。
// 密钥与二维码不经设置接口返回：中途刷新页面后扫码区是空的，
// 点“重新生成密钥”拿新的；备用码也只在确认开启那一刻显示。
function renderSecurityCard(sec) {
  sec = sec || {};
  const st = $('#totp-status');
  if (st) {
    st.textContent = sec.totp_enabled
      ? '二次验证已开启（剩余备用码 ' + (sec.backup_codes_remaining || 0) + ' 个）。登录时除了密码，还要输验证器里的 6 位验证码。'
      : (sec.totp_has_secret ? '二次验证还没确认开启：密钥已生成过，刷新后二维码不保留，点“重新生成密钥”拿新的。' : '二次验证未开启：登录只用账号密码。');
    // 服务器当前时间（v1.3.9）：扫码/输验证码前核对手机时间，时钟差是验证码对不上的最常见原因。
    if (sec.server_time) st.textContent += ' 服务器当前时间：' + sec.server_time + '。';
  }
  setVisible($('#totp-setup-area'), false);
  setVisible($('#totp-backup-area'), false);
  setVisible($('#totp-disable-area'), !!sec.totp_enabled);
  const setupBtn = $('#totp-setup-btn');
  if (setupBtn) {
    setupBtn.style.display = sec.totp_enabled ? 'none' : '';
    setupBtn.textContent = sec.totp_has_secret ? '重新生成密钥' : '开启二次验证';
  }
}
// 设置页选项卡（v1.3.12）：一个卡片外壳＋顶部选项卡栏，点哪个只显示哪个的内容，
// 其他面板隐藏；上次点的选项卡记在 localStorage，刷新、重启面板、检查更新回来
// 后仍停在那里。选项卡是原生 button：Tab 能走到，回车/空格切换；方向键在
// 选项卡之间移动并切换，Home/End 跳到两端。
const SETTINGS_TABS = ['basic', 'security', 'audit', 'subscription', 'domain', 'alerts', 'backup', 'system'];
const SETTINGS_TAB_KEY = 'relay-panel-settings-tab';
function settingsTabValid(id) { return SETTINGS_TABS.indexOf(id) >= 0; }
function showSettingsTab(id) {
  if (!settingsTabValid(id)) id = 'basic';
  SETTINGS_TABS.forEach((t) => {
    const btn = document.getElementById('settings-tab-' + t);
    if (btn) btn.setAttribute('aria-selected', t === id ? 'true' : 'false');
    setVisible(document.getElementById('settings-panel-' + t), t === id);
  });
  try { localStorage.setItem(SETTINGS_TAB_KEY, id); } catch (e) { /* 隐私模式等存不了就每次回基础 */ }
}
function initSettingsTabs() {
  let saved = null;
  try { saved = localStorage.getItem(SETTINGS_TAB_KEY); } catch (e) { /* 读不到就用默认 */ }
  showSettingsTab(settingsTabValid(saved) ? saved : 'basic');
  // 绑定只做一次：重复调用 initSettingsTabs（例如重跑初始化恢复选项卡）时
  // 不能再叠加监听，否则一次按键会连跳多个选项卡。
  if (initSettingsTabs.bound) return;
  initSettingsTabs.bound = true;
  SETTINGS_TABS.forEach((t) => {
    on('#settings-tab-' + t, 'click', () => showSettingsTab(t));
  });
  const bar = document.querySelector('.settings-tabs');
  if (bar) bar.addEventListener('keydown', (e) => {
    const cur = document.activeElement && document.activeElement.dataset
      ? document.activeElement.dataset.settingsTab : null;
    if (!cur || !settingsTabValid(cur)) return;
    let idx = SETTINGS_TABS.indexOf(cur);
    if (e.key === 'ArrowRight') idx = (idx + 1) % SETTINGS_TABS.length;
    else if (e.key === 'ArrowLeft') idx = (idx - 1 + SETTINGS_TABS.length) % SETTINGS_TABS.length;
    else if (e.key === 'Home') idx = 0;
    else if (e.key === 'End') idx = SETTINGS_TABS.length - 1;
    else return;
    e.preventDefault();
    showSettingsTab(SETTINGS_TABS[idx]);
    const btn = document.getElementById('settings-tab-' + SETTINGS_TABS[idx]);
    if (btn) btn.focus();
  });
}
// 异地备份卡片内子选项卡（v1.3.13）：口令设置 / R2 / 七牛云，复用设置页选项卡模式
//（原生 button＋aria-selected＋方向键，localStorage 记住上次，键与设置页分开）。
const RB_TABS = ['passphrase', 'r2', 'qiniu'];
const RB_TAB_KEY = 'relay-panel-remote-backup-tab';
function rbTabValid(id) { return RB_TABS.indexOf(id) >= 0; }
function showRemoteBackupTab(id) {
  if (!rbTabValid(id)) id = 'passphrase';
  RB_TABS.forEach((t) => {
    const btn = document.getElementById('rb-tab-' + t);
    if (btn) btn.setAttribute('aria-selected', t === id ? 'true' : 'false');
    setVisible(document.getElementById('rb-panel-' + t), t === id);
  });
  try { localStorage.setItem(RB_TAB_KEY, id); } catch (e) { /* 隐私模式等存不了就每次回口令 */ }
}
function initRemoteBackupTabs() {
  let saved = null;
  try { saved = localStorage.getItem(RB_TAB_KEY); } catch (e) { /* 读不到就用默认 */ }
  showRemoteBackupTab(rbTabValid(saved) ? saved : 'passphrase');
  // 绑定只做一次：重复调用不能再叠加监听，否则一次按键会连跳多个选项卡。
  if (initRemoteBackupTabs.bound) return;
  initRemoteBackupTabs.bound = true;
  RB_TABS.forEach((t) => {
    on('#rb-tab-' + t, 'click', () => showRemoteBackupTab(t));
  });
  const bar = document.querySelector('.rb-tabs');
  if (bar) bar.addEventListener('keydown', (e) => {
    const cur = document.activeElement && document.activeElement.dataset
      ? document.activeElement.dataset.rbTab : null;
    if (!cur || !rbTabValid(cur)) return;
    let idx = RB_TABS.indexOf(cur);
    if (e.key === 'ArrowRight') idx = (idx + 1) % RB_TABS.length;
    else if (e.key === 'ArrowLeft') idx = (idx - 1 + RB_TABS.length) % RB_TABS.length;
    else if (e.key === 'Home') idx = 0;
    else if (e.key === 'End') idx = RB_TABS.length - 1;
    else return;
    e.preventDefault();
    showRemoteBackupTab(RB_TABS[idx]);
    const btn = document.getElementById('rb-tab-' + RB_TABS[idx]);
    if (btn) btn.focus();
  });
}
// 添加落地表单选项卡（v1.3.41）：基本 / 证书 / 流量与限速，复用设置页选项卡模式
//（原生 button＋aria-selected＋方向键，localStorage 记住上次，键与设置页分开）。
// 提交按钮在选项卡之外、一直可见；切选项卡只是显隐，不影响表单取值与提交。
const ADD_TABS = ['basic', 'cert', 'traffic'];
const ADD_TAB_KEY = 'relay-panel-add-tab';
function addTabValid(id) { return ADD_TABS.indexOf(id) >= 0; }
function showAddTab(id) {
  if (!addTabValid(id)) id = 'basic';
  ADD_TABS.forEach((t) => {
    const btn = document.getElementById('add-tab-' + t);
    if (btn) btn.setAttribute('aria-selected', t === id ? 'true' : 'false');
    setVisible(document.getElementById('add-panel-' + t), t === id);
  });
  try { localStorage.setItem(ADD_TAB_KEY, id); } catch (e) { /* 隐私模式等存不了就每次回基本 */ }
}
function initAddTabs() {
  let saved = null;
  try { saved = localStorage.getItem(ADD_TAB_KEY); } catch (e) { /* 读不到就用默认 */ }
  showAddTab(addTabValid(saved) ? saved : 'basic');
  if (initAddTabs.bound) return;
  initAddTabs.bound = true;
  ADD_TABS.forEach((t) => {
    on('#add-tab-' + t, 'click', () => showAddTab(t));
  });
  const bar = document.querySelector('.add-tabs');
  if (bar) bar.addEventListener('keydown', (e) => {
    const cur = document.activeElement && document.activeElement.dataset
      ? document.activeElement.dataset.addTab : null;
    if (!cur || !addTabValid(cur)) return;
    let idx = ADD_TABS.indexOf(cur);
    if (e.key === 'ArrowRight') idx = (idx + 1) % ADD_TABS.length;
    else if (e.key === 'ArrowLeft') idx = (idx - 1 + ADD_TABS.length) % ADD_TABS.length;
    else if (e.key === 'Home') idx = 0;
    else if (e.key === 'End') idx = ADD_TABS.length - 1;
    else return;
    e.preventDefault();
    showAddTab(ADD_TABS[idx]);
    const btn = document.getElementById('add-tab-' + ADD_TABS[idx]);
    if (btn) btn.focus();
  });
}
// 服务与日志页选项卡（v1.3.41）：服务 / 实时日志 / 端口占用，复用设置页选项卡模式。
const SERVICE_TABS = ['service', 'logs', 'ports'];
const SERVICE_TAB_KEY = 'relay-panel-service-tab';
function serviceTabValid(id) { return SERVICE_TABS.indexOf(id) >= 0; }
function showServiceTab(id) {
  if (!serviceTabValid(id)) id = 'service';
  SERVICE_TABS.forEach((t) => {
    const btn = document.getElementById('service-tab-' + t);
    if (btn) btn.setAttribute('aria-selected', t === id ? 'true' : 'false');
    setVisible(document.getElementById('service-panel-' + t), t === id);
  });
  try { localStorage.setItem(SERVICE_TAB_KEY, id); } catch (e) { /* 隐私模式等存不了就每次回服务 */ }
}
function initServiceTabs() {
  let saved = null;
  try { saved = localStorage.getItem(SERVICE_TAB_KEY); } catch (e) { /* 读不到就用默认 */ }
  showServiceTab(serviceTabValid(saved) ? saved : 'service');
  if (initServiceTabs.bound) return;
  initServiceTabs.bound = true;
  SERVICE_TABS.forEach((t) => {
    on('#service-tab-' + t, 'click', () => showServiceTab(t));
  });
  const bar = document.querySelector('.service-tabs');
  if (bar) bar.addEventListener('keydown', (e) => {
    const cur = document.activeElement && document.activeElement.dataset
      ? document.activeElement.dataset.serviceTab : null;
    if (!cur || !serviceTabValid(cur)) return;
    let idx = SERVICE_TABS.indexOf(cur);
    if (e.key === 'ArrowRight') idx = (idx + 1) % SERVICE_TABS.length;
    else if (e.key === 'ArrowLeft') idx = (idx - 1 + SERVICE_TABS.length) % SERVICE_TABS.length;
    else if (e.key === 'Home') idx = 0;
    else if (e.key === 'End') idx = SERVICE_TABS.length - 1;
    else return;
    e.preventDefault();
    showServiceTab(SERVICE_TABS[idx]);
    const btn = document.getElementById('service-tab-' + SERVICE_TABS[idx]);
    if (btn) btn.focus();
  });
}
// 端口管理页选项卡（v1.3.41）：开放端口 / 已开放的端口，复用设置页选项卡模式。
const FW_TABS = ['open', 'rules'];
const FW_TAB_KEY = 'relay-panel-firewall-tab';
function fwTabValid(id) { return FW_TABS.indexOf(id) >= 0; }
function showFwTab(id) {
  if (!fwTabValid(id)) id = 'open';
  FW_TABS.forEach((t) => {
    const btn = document.getElementById('fw-tab-' + t);
    if (btn) btn.setAttribute('aria-selected', t === id ? 'true' : 'false');
    setVisible(document.getElementById('fw-panel-' + t), t === id);
  });
  try { localStorage.setItem(FW_TAB_KEY, id); } catch (e) { /* 隐私模式等存不了就每次回开放端口 */ }
}
function initFirewallTabs() {
  let saved = null;
  try { saved = localStorage.getItem(FW_TAB_KEY); } catch (e) { /* 读不到就用默认 */ }
  showFwTab(fwTabValid(saved) ? saved : 'open');
  if (initFirewallTabs.bound) return;
  initFirewallTabs.bound = true;
  FW_TABS.forEach((t) => {
    on('#fw-tab-' + t, 'click', () => showFwTab(t));
  });
  const bar = document.querySelector('.fw-tabs');
  if (bar) bar.addEventListener('keydown', (e) => {
    const cur = document.activeElement && document.activeElement.dataset
      ? document.activeElement.dataset.fwTab : null;
    if (!cur || !fwTabValid(cur)) return;
    let idx = FW_TABS.indexOf(cur);
    if (e.key === 'ArrowRight') idx = (idx + 1) % FW_TABS.length;
    else if (e.key === 'ArrowLeft') idx = (idx - 1 + FW_TABS.length) % FW_TABS.length;
    else if (e.key === 'Home') idx = 0;
    else if (e.key === 'End') idx = FW_TABS.length - 1;
    else return;
    e.preventDefault();
    showFwTab(FW_TABS[idx]);
    const btn = document.getElementById('fw-tab-' + FW_TABS[idx]);
    if (btn) btn.focus();
  });
}
async function loadSettingsPage() {
  try {
    const st = await api('/api/v1/settings');
    settingsCache = st;
    // 面板模式（v1.2.8）：管理面板模式下隐藏本机中转专属设置卡
    //（任意门地址、延迟探测、中转倍率都是本机中转的配置，管理面板本机没有中转）。
    // Cloudflare Token 与机器监控在管理面板下也可见：前者是面板自身 HTTPS
    // 签证书要用的（v1.3.14，管理面板没有它就签不到真证书），后者是面板本机
    // 的磁盘/内存/CPU 监控，后台循环在管理面板下照常跑。
    const modeOn = !!st.manager_only;
    const modeStatus = $('#mode-status');
    if (modeStatus) modeStatus.textContent = modeOn ? '当前：管理面板（只做集中管理，本地页已隐藏）' : '当前：完整面板';
    ['card-door', 'card-latency', 'card-relay-multiplier'].forEach((cid) => {
      const c = document.getElementById(cid);
      if (c) c.classList.toggle('hidden', modeOn);
    });
    const toMgr = $('#mode-to-manager'), toFull = $('#mode-to-full');
    if (toMgr) toMgr.disabled = modeOn;
    if (toFull) toFull.disabled = !modeOn;
    $('#panel-port').value = st.port || 11111;
    // 每用户端口段（v1.3.25）：没设过 = 默认 30000–39999。
    const up = st.user_ports || {};
    $('#user-port-start').value = up.start || 30000;
    $('#user-port-end').value = up.end || 39999;
    // 中转倍率（v1.3.43）：没设过 = 1，输入框留空；设过就预填。
    const rm = st.relay_multiplier || 1;
    $('#relay-multiplier').value = rm === 1 ? '' : rm;
    $('#whitelist-input').value = (st.ip_whitelist || []).join('\n');
    $('#https-domain').value = st.https_domain || '';
    $('#https-status').textContent = st.https_enabled ? 'HTTPS 已开启' + (st.https_domain ? '（域名：' + st.https_domain + '）' : '（自签证书）') : '当前为 HTTP，未开启 HTTPS。';
    // DNS 服务商（v1.3.49）：下拉框回填 + 只显示所选服务商的凭证表单 + 已配置状态。
    renderDNSProvider(st);
    // 面板自身证书状态（v1.3.14）：证书信息、无凭证提示、上次失败原因常驻显示。
    const pc = st.panel_cert || {};
    const provider = dnsCurrentProvider(st);
    const certInfo = $('#https-cert-info');
    if (certInfo) {
      if (!st.https_enabled) certInfo.textContent = '';
      else if (!pc.exists) certInfo.textContent = '证书：未生成，重启面板后会自动生成。';
      else if (pc.self_signed) certInfo.textContent = '证书：自签证书（浏览器会报不安全），真证书没签下来。';
      else certInfo.textContent = '证书：真证书，剩余 ' + (pc.days_left ?? '?') + ' 天（到期 ' + (pc.expires || '?') + '）' + (provider === 'manual' ? '，手动模式不自动续签，到期前请手动重新签发。' : '，剩余 ≤30 天自动续签。');
    }
    const tokenHint = $('#https-token-hint');
    if (tokenHint) {
      let hint = '';
      if (st.https_domain && provider === 'manual') {
        hint = '手动模式：点开启后按提示加 TXT 记录再继续验证（不自动续签，到期前手动重新签发）。';
      } else if (st.https_domain && !dnsIsConfigured(st, provider)) {
        hint = provider === 'cloudflare'
          ? '未填 Cloudflare Token，真证书签不下来：在上方选 Cloudflare 并填写 Token，填好后点“重试签发”。'
          : '未填 ' + dnsProviderDisplay(provider) + ' 凭证，真证书签不下来：在上方填写凭证，填好后点“重试签发”。';
      }
      tokenHint.textContent = hint;
      setVisible(tokenHint, !!hint);
    }
    const certErr = $('#https-cert-error');
    const retryRow = $('#https-retry-row');
    const lastErr = pc.last_error || '';
    if (certErr) {
      certErr.textContent = lastErr ? '上次签发/续签失败：' + lastErr : '';
      setVisible(certErr, !!lastErr);
    }
    if (retryRow) setVisible(retryRow, !!(lastErr && st.https_domain));
    // 手动签发进行中的订单（v1.3.49）：回填 TXT 名与值 + 继续/取消按钮。
    renderManualPending(st);
    $('#door-addr').value = st.relay_addr || '';
    $('#door-status').textContent = st.relay_addr ? '当前任意门地址：' + st.relay_addr : '未设置：下次加任意门落地时自动探测中转机公网 IP';
    const tg = st.telegram || {};
    $('#tg-enabled').checked = !!tg.enabled;
    $('#tg-chat').value = tg.chat_id || '';
    const mon = st.monitor || {};
    $('#mon-disk-warn').value = mon.disk_warn || 85;
    $('#mon-disk-crit').value = mon.disk_crit || 95;
    $('#mon-mem-warn').value = mon.mem_warn || 80;
    $('#mon-mem-crit').value = mon.mem_crit || 90;
    $('#mon-cpu-warn').value = mon.cpu_warn || 80;
    $('#mon-cpu-crit').value = mon.cpu_crit || 95;
    $('#mon-interval').value = mon.interval_min || 5;
    const lat = st.latency || {};
    $('#lat-enabled').checked = lat.enabled !== false;
    $('#lat-interval').value = lat.interval_min || 5;
    const ct = st.central_token || {};
    $('#central-token-status').textContent = ct.has_token ? '已生成令牌' + (ct.created_at ? '（生成于 ' + fmtDate(ct.created_at) + '）' : '') + '：主面板登记时用它；轮换后旧令牌立即失效，撤销后任何主面板都连不进来。' : '未生成令牌：没生成就等于没开集中管理，别人连不进来。';
    const sub = st.subscription || {};
    if (!sub.has_token) {
      $('#sub-status').textContent = '未生成订阅链接：点“生成订阅链接”，复制链接粘贴到客户端订阅里即可。';
      const box0 = $('#sub-once');
      if (box0) { box0.classList.add('hidden'); box0.hidden = true; }
    } else if (!sub.has_link) {
      // 1.3.7 之前的旧链接：没有密文，无法恢复，提示轮换一次。
      $('#sub-status').textContent = '已生成订阅链接' + (sub.created_at ? '（生成于 ' + fmtDate(sub.created_at) + '）' : '') + '：这条是旧链接，面板里没有存它的密文，无法恢复，点“轮换订阅链接”生成一条新的。';
      const box0 = $('#sub-once');
      if (box0) { box0.classList.add('hidden'); box0.hidden = true; }
    } else {
      $('#sub-status').textContent = '已生成订阅链接' + (sub.created_at ? '（生成于 ' + fmtDate(sub.created_at) + '）' : '') + '：内容' + subModeLabel(sub.mode) + '，链接已加密存在面板里，随时能点复制重新拿。';
      loadGenericSubLink();
    }
    // 通用订阅的三选一下拉按存下来的模式预选（没生成过时按上次选择）。
    if (sub.has_token && sub.mode) syncSubModeSelect($('#sub-token-mode'), sub.mode);
    const subDomain = $('#sub-public-domain');
    if (subDomain) subDomain.value = sub.public_domain || '';
    // 订阅文件名称（v1.3.22）：存过就回填；留空就是回落面板名。
    const sfn = $('#sub-file-name');
    if (sfn) sfn.value = sub.file_name || '';
    // 订阅网关卡片：当前订阅对外域名（v1.3.50）。
    const gdn = $('#gateway-domain-now');
    if (gdn) gdn.textContent = sub.public_domain || '未配置';
    // 自动定时备份（v1.3.6）：设置接口下发的排程填进表单。
    const bk = st.backup || {};
    const bke = $('#bk-enabled'); if (bke) bke.checked = bk.enabled !== false;
    const bkt = $('#bk-time'); if (bkt) bkt.value = bk.time || '03:00';
    const bkk = $('#bk-keep'); if (bkk) bkk.value = bk.keep || 7;
    // 异地备份（v1.3.11）：设置接口下发的脱敏状态填进表单。
    renderRemoteBackupCard(st.remote_backup || {});
    // 面板名称（v1.3.4）：设置接口下发的当前名填进输入框。
    const pn = $('#panel-name');
    if (pn) pn.value = st.panel_name || 'CP管理';
    // 登录页公告与签名（v1.3.21）：设置接口下发的当前值填进输入框。
    const ln = $('#login-notice-text');
    if (ln) ln.value = st.login_notice || '';
    const ls = $('#login-signature');
    if (ls) ls.value = st.login_signature || '';
    // 登录安全（v1.3.8）：登录告警开关（没配 Telegram 时置灰）、二次验证状态。
    const sec = st.security || {};
    const laEnabled = $('#login-alert-enabled');
    if (laEnabled) {
      laEnabled.checked = !!sec.login_alert;
      laEnabled.disabled = !sec.telegram_configured;
    }
    const laStatus = $('#login-alert-status');
    if (laStatus) {
      laStatus.textContent = sec.telegram_configured
        ? '已配好 Telegram：开启后，连续输错 5 次触发锁定时、或新 IP 第一次成功登录时，都会发一条 Telegram 提醒。'
        : '未配置 Telegram：先在“Telegram 提醒”卡片里配好 Bot Token 和 Chat ID，再回来开这个开关。';
    }
    renderSecurityCard(sec);
    // 订阅到期时间（v1.3.20）：通用订阅的到期时间回填到设置页；永久时
    // 日期框置灰、勾选永久。
    const subExpDate = $('#sub-expiry-date');
    if (subExpDate) { subExpDate.value = dateInputOfExpiry(sub.expires_at); subExpDate.disabled = !sub.expires_at; }
    const subExpPerm = $('#sub-expiry-permanent');
    if (subExpPerm) subExpPerm.checked = !sub.expires_at;
    const subExpStatus = $('#sub-expiry-status');
    if (subExpStatus) subExpStatus.textContent = sub.expires_at ? '当前：' + fmtExpiry(sub.expires_at) : '当前：永久';
    // 订阅路径（v1.3.20）：回填当前路径段（空 = 默认 sub）。
    const subPathEl = $('#sub-path');
    if (subPathEl) subPathEl.value = sub.sub_path || 'sub';
    const subPathEx = $('#sub-path-example');
    if (subPathEx) subPathEx.textContent = sub.sub_path || 'sub';
    // 面板 Logo（v1.3.20）：登录页、侧边栏与设置页预览按有没有 Logo 显示。
    refreshLogoViews(!!(st.logo && st.logo.has_logo));
    $('#update-box').innerHTML = '<div class="kv"><span>当前版本</span><b>' + esc(st.version || '—') + '</b></div>' +
      '<div class="kv"><span>最新版本</span><b>' + esc(st.latest_version || '—') + '</b></div>' +
      '<div class="kv"><span>状态</span><b>' + (st.update_available ? '<span class="badge yellow">有更新</span>' : '<span class="badge green">已是最新</span>') + '</b></div>' +
      '<div class="kv"><span>仓库</span><b><a href="https://github.com/nbbk/relay-node" target="_blank" rel="noopener">relay-node</a></b></div>';
  } catch (e) { toast('设置加载失败：' + e.message, 'err'); }
  loadAudit(); loadSubUsers(); loadSubGroups(); loadSchedBackups();
}
// 切换面板模式（v1.2.8）：只改 manager_only 开关，数据保留；切完刷新页面重建导航。
async function switchPanelMode(toManager) {
  const ok = await confirmModal(toManager ? '切到管理面板' : '切回完整面板',
    toManager ? '<p>切过去后本地落地、服务、端口管理全部隐藏，只留集中管理、合并订阅与设置；数据保留，切回来即恢复。确定吗？</p>' : '<p>切回来后本地落地、服务、端口管理全部恢复。确定吗？</p>',
    toManager ? '切到管理面板' : '切回完整面板');
  if (!ok) return;
  try {
    await apiPost('/api/v1/settings/manager-only', { manager_only: toManager });
    toast(toManager ? '已切到管理面板，页面刷新后生效' : '已切回完整面板，页面刷新后生效', 'ok');
    setTimeout(() => location.reload(), 600);
  } catch (e) { toast('切换失败：' + e.message, 'err'); }
}
/* ---------- HTTPS 开关后跳协议（v1.3.48） ---------- */
// 根因：api() 用的全是相对路径，请求都发往页面当前 origin；开关成功后
// 只调 loadSettingsPage()、从来不跳协议，页面就还停在旧协议上，之后所有
// 请求都握手失败报 Failed to fetch（面板本身没事）。所以开关成功后必须
// 跳到新协议。目标地址：新协议 + location.host（端口不变）+ 当前
// path+query，回到设置页。
// 测试可覆盖的定时（默认：每 1 秒探测一次、最多等 30 秒；关闭成功后旧
// origin 断开再多等 1.5 秒才跳，等新进程起来）。
var httpsJumpPollMs = 1000;
var httpsJumpTimeoutMs = 30000;
var httpsJumpGraceMs = 1500;
function httpsTargetURL(enable) {
  return (enable ? 'https:' : 'http:') + '//' + location.host + location.pathname + location.search;
}
function httpsProbeURL(enable) {
  return (enable ? 'https:' : 'http:') + '//' + location.host + '/api/v1/me';
}
// 跳转入口（包一层只为测试能 stub 掉真导航：jsdom 不实现 location 跳转）。
function jumpToURL(url) { location.href = url; }
// ---------- DNS 服务商（v1.3.49） ----------
// id 到中文名的映射（与后端 dnsProviderNames 一致）。
const DNS_PROVIDER_NAMES = {
  cloudflare: 'Cloudflare', aliyun: '阿里云 DNS', dnspod: '腾讯云 DNSPod',
  route53: 'AWS Route 53', godaddy: 'GoDaddy', namecheap: 'Namecheap',
  digitalocean: 'DigitalOcean', linode: 'Linode', vultr: 'Vultr', bunny: 'Bunny DNS',
  hetzner: 'Hetzner', spaceship: 'Spaceship', gandi: 'Gandi', namesilo: 'NameSilo',
  desec: 'deSEC', porkbun: 'Porkbun', cloudns: 'CloudNS', manual: '手动模式',
};
function dnsProviderDisplay(id) { return DNS_PROVIDER_NAMES[id] || id; }
// 当前选中的服务商（后端没给或给了空值时默认 cloudflare，老 payload 兼容）。
function dnsCurrentProvider(st) {
  const p = (st && st.dns_provider) || '';
  return DNS_PROVIDER_NAMES[p] ? p : 'cloudflare';
}
// 某服务商凭证是否配齐（后端没给 dns_configured 时，老 payload 只认 cloudflare 的 has_token）。
function dnsIsConfigured(st, provider) {
  if (provider === 'manual') return true;
  const m = (st && st.dns_configured) || {};
  if (m[provider] !== undefined) return !!m[provider];
  if (provider === 'cloudflare') return !!((st && st.cloudflare) || {}).has_token;
  return false;
}
// 渲染服务商下拉框与凭证表单：选哪个显示哪个，状态行写已配置/未配置。
function renderDNSProvider(st) {
  const provider = dnsCurrentProvider(st);
  const sel = $('#dns-provider');
  if (sel) sel.value = provider;
  document.querySelectorAll('.dns-form').forEach((f) => {
    setVisible(f, f.id === 'dns-form-' + provider);
  });
  const status = $('#dns-cred-status');
  if (status) {
    status.textContent = provider === 'manual'
      ? '手动模式，无需凭证'
      : (dnsIsConfigured(st, provider) ? '已配置 ' + dnsProviderDisplay(provider) + ' 凭证（绑域名开 HTTPS 可用）' : '未配置 ' + dnsProviderDisplay(provider) + ' 凭证（绑域名开 HTTPS 前要先填）');
  }
}
// 渲染手动签发进行中的订单（有 pending 才显示 TXT 名与值）。
function renderManualPending(st) {
  const box = $('#manual-pending-box');
  if (!box) return;
  const p = (st && st.manual_pending) || null;
  if (!p) { setVisible(box, false); return; }
  const d = $('#manual-pending-domain'); if (d) d.textContent = p.domain || '';
  const e = $('#manual-pending-expires'); if (e) e.textContent = p.expires_at ? String(p.expires_at).replace('T', ' ').slice(0, 16) : '';
  const n = $('#manual-pending-name'); if (n) n.textContent = p.txt_name || '';
  const v = $('#manual-pending-value'); if (v) v.textContent = p.txt_value || '';
  setVisible(box, true);
}
// 探测新地址通了再跳。开启（→https）时从 http 页探 https 可行；关闭
// （→http）时从 https 页探 http 会被浏览器当混合内容拦掉，所以改探旧
// origin：旧 origin 一断（面板已重启）再等一个 grace 就跳。
async function waitAndJumpHTTPS(enable) {
  const target = httpsTargetURL(enable);
  const scheme = enable ? 'https://' : 'http://';
  const status = $('#https-status');
  if (status) status.textContent = '面板正在重启并切换到 ' + scheme + '…，稍后自动跳转';
  toast('面板正在重启并切换到 ' + scheme + '…，稍后自动跳转', 'ok');
  const deadline = Date.now() + httpsJumpTimeoutMs;
  if (enable) {
    // 探新 origin：no-cors 下只要服务器有响应（哪怕 401/404）就算通，
    // 握手/联网失败才会 reject。
    while (Date.now() < deadline) {
      try { await fetch(httpsProbeURL(true), { mode: 'no-cors' }); jumpToURL(target); return; }
      catch (e) { /* 还没起来，继续等 */ }
      await new Promise((r) => setTimeout(r, httpsJumpPollMs));
    }
  } else {
    // 探旧 origin：还通说明面板没重启；一断说明已重启，再等 grace 才跳。
    while (Date.now() < deadline) {
      try { await fetch('/api/v1/me'); }
      catch (e) { await new Promise((r) => setTimeout(r, httpsJumpGraceMs)); jumpToURL(target); return; }
      await new Promise((r) => setTimeout(r, httpsJumpPollMs));
    }
  }
  // 超时：给手动打开的链接（弹窗里可点）。
  const m = openModal('<h2>面板还没起来</h2><div><p>等了 ' + Math.round(httpsJumpTimeoutMs / 1000) + ' 秒还没探测到 ' + scheme + ' 的面板，请稍后手动打开：</p>' +
    '<p><a href="' + esc(target) + '" target="_blank" rel="noopener">' + esc(target) + '</a></p></div>' +
    '<div class="btn-row"><button class="btn" id="https-jump-ok" type="button">知道了</button></div>');
  onIn(m, '#https-jump-ok', 'click', () => closeModal());
}
function initSettings() {
  initSettingsTabs(); // v1.3.12：选项卡栏绑定 + 恢复上次选项卡
  initRemoteBackupTabs(); // v1.3.13：异地备份卡片内子选项卡绑定 + 恢复上次子选项卡
  on('#mode-to-manager', 'click', () => switchPanelMode(true));
  on('#mode-to-full', 'click', () => switchPanelMode(false));
  // 面板名称（v1.3.4）：保存后刷新页面，登录页标题/品牌与订阅配置名跟着变。
  on('#panel-name-form', 'submit', async (e) => {
    e.preventDefault();
    const name = $('#panel-name').value.trim();
    if (!name) { toast('面板名称不能为空', 'err'); return; }
    try {
      await apiPost('/api/v1/settings/panel-name', { name });
      toast('面板名称已保存，页面刷新后登录页标题、品牌和订阅配置名都会跟着变', 'ok');
      setTimeout(() => location.reload(), 600);
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
  on('#pw-form', 'submit', async (e) => {
    e.preventDefault();
    const oldPw = $('#pw-old').value, nw = $('#pw-new').value, nw2 = $('#pw-new2').value;
    if (nw.length < 8) { toast('新密码至少 8 位', 'err'); return; }
    if (nw !== nw2) { toast('两次输入的新密码不一致', 'err'); return; }
    try {
      await apiPost('/api/v1/settings/password', { old_password: oldPw, new_password: nw });
      toast('密码已修改', 'ok'); e.target.reset();
    } catch (err) { toast('修改失败：' + err.message, 'err'); }
  });
  // 登录安全（v1.3.8）
  on('#login-alert-save', 'click', async () => {
    try {
      await apiPost('/api/v1/settings/login-alert', { enabled: $('#login-alert-enabled').checked });
      toast('登录告警已保存', 'ok');
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
  on('#totp-setup-btn', 'click', async () => {
    try {
      const r = await apiPost('/api/v1/settings/totp/setup', {});
      const qr = $('#totp-qr'); if (qr) qr.src = r.qr || '';
      const sc = $('#totp-secret'); if (sc) sc.textContent = r.secret || '';
      const st2 = $('#totp-server-time'); if (st2) st2.textContent = r.server_time || '';
      setVisible($('#totp-setup-area'), true);
      setVisible($('#totp-backup-area'), false);
      const cc = $('#totp-confirm-code'); if (cc) cc.value = '';
      toast('密钥已生成，扫码后输一次验证码确认', 'ok');
    } catch (err) { toast('生成失败：' + err.message, 'err'); }
  });
  on('#totp-copy-secret', 'click', async () => {
    const sc = $('#totp-secret');
    if (sc && sc.textContent) {
      try { await navigator.clipboard.writeText(sc.textContent); toast('密钥已复制', 'ok'); }
      catch (e) { toast('复制失败，请手动选中复制', 'err'); }
    }
  });
  on('#totp-confirm-form', 'submit', async (e) => {
    e.preventDefault();
    const code = $('#totp-confirm-code').value.trim();
    if (code.length !== 6) { toast('验证码是 6 位数字', 'err'); return; }
    try {
      const r = await apiPost('/api/v1/settings/totp/confirm', { code });
      const codes = r.backup_codes || [];
      $('#totp-backup-codes').textContent = codes.join('  ');
      await loadSettingsPage();
      setVisible($('#totp-backup-area'), codes.length > 0);
      toast('二次验证已开启，备用码只显示一次，请抄下来收好', 'ok');
    } catch (err) { toast('确认失败：' + err.message, 'err'); }
  });
  // 一键复制备用码（v1.3.10）：显示区里的 8 个码按两空格分隔，复制时改成
  // 一行一个，方便粘进备忘录/密码管理器；走 copyText（clipboard 不可用时
  // 自动走 textarea 兜底，成功/失败都有 toast 反馈）。
  on('#totp-copy-backup', 'click', (e) => {
    const txt = ($('#totp-backup-codes').textContent || '').trim();
    if (!txt) { toast('没有可复制的备用码', 'err'); return; }
    copyText(txt.split(/\s+/).join('\n'), e.target);
  });
  on('#totp-disable-form', 'submit', async (e) => {
    e.preventDefault();
    const ok = await confirmModal('关闭二次验证', '<p>关闭后登录只用账号密码。确定吗？</p>', '关闭二次验证', true);
    if (!ok) return;
    try {
      await apiPost('/api/v1/settings/totp/disable', { password: $('#totp-disable-pass').value, code: $('#totp-disable-code').value.trim() });
      e.target.reset();
      await loadSettingsPage();
      toast('二次验证已关闭', 'ok');
    } catch (err) { toast('关闭失败：' + err.message, 'err'); }
  });
  on('#port-form', 'submit', async (e) => {
    e.preventDefault();
    const port = Number($('#panel-port').value);
    if (!port || port < 1 || port > 65535) { toast('端口请填 1–65535', 'err'); return; }
    const ok = await confirmModal('改面板端口', '<p>改成端口 <b>' + port + '</b> 后，面板会重启自身，当前页面会断开，请用新端口重新打开。</p>', '保存并重启');
    if (!ok) return;
    try { await apiPost('/api/v1/settings/port', { port }); toast('面板正在重启，请用新端口重新打开', 'ok'); }
    catch (err) { toast('操作失败：' + err.message, 'err'); }
  });
  on('#whitelist-form', 'submit', async (e) => {
    e.preventDefault();
    const list = $('#whitelist-input').value.split('\n').map((s) => s.trim()).filter(Boolean);
    const bad = list.find((ip) => !/^\d{1,3}(\.\d{1,3}){3}$/.test(ip));
    if (bad) { toast('IP 格式不正确：' + bad, 'err'); return; }
    try { await apiPost('/api/v1/settings/ip-whitelist', { whitelist: list }); toast('白名单已保存', 'ok'); }
    catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
  on('#https-enable', 'click', async () => {
    const domain = $('#https-domain').value.trim();
    if (domain && !/^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/.test(domain)) { toast('域名格式不正确', 'err'); return; }
    const btn = $('#https-enable');
    const oldText = btn ? btn.textContent : '';
    const provider = ($('#dns-provider') || {}).value || 'cloudflare';
    // 手动模式 + 有域名：走两步走（先切到 manual 再下单拿 TXT 值，不重启面板；
    // TXT 值显示在下方卡片里，用户去 DNS 后台加上再点“已添加，继续验证”）。
    if (domain && provider === 'manual') {
      if (btn) { btn.disabled = true; btn.textContent = '正在下单…'; }
      try {
        await apiPost('/api/v1/settings/dns-provider', { provider: 'manual' });
        await apiPost('/api/v1/settings/https/manual/start', { domain });
        toast('订单已下：去 DNS 后台加 TXT 记录，再点“已添加，继续验证”', 'ok');
        await loadSettingsPage();
      } catch (err) {
        toast('下单失败：' + err.message, 'err');
      } finally {
        if (btn) { btn.disabled = false; btn.textContent = oldText; }
      }
      return;
    }
    // 带域名开启会同步跑 ACME DNS-01（DNS 传播 10 秒＋两轮最多各 150 秒轮询），
    // 请求可能挂几分钟：先禁用按钮并给等待提示，别让用户以为卡死而重复点。
    if (btn) { btn.disabled = true; if (domain) btn.textContent = '正在申请证书…'; }
    if (domain) toast('正在申请证书，请稍等（可能需要几分钟），不要重复点击', 'ok');
    try {
      const r = await apiPost('/api/v1/settings/https', { enable: true, domain });
      // v1.3.14：真证书申请失败时后端回 warning（以前前端只读 r.msg，警告被吞了，
      // 用户只看到“HTTPS 已开启”，不知道证书是自签的）。警告要当场报出来，
      // 失败原因还会常驻显示在 HTTPS 卡片上。
      if (r.warning) toast(r.warning, 'err');
      else toast('HTTPS 已开启' + (r.msg ? '：' + r.msg : ''), 'ok');
      await waitAndJumpHTTPS(true);
    } catch (err) {
      if (btn) { btn.disabled = false; btn.textContent = oldText; }
      toast('开启失败：' + err.message + '（已保留原有方式，面板未中断）', 'err');
    }
  });
  // v1.3.14：重试签发就是再调一次开启接口（域名取输入框当前值）。
  on('#https-retry', 'click', async () => {
    const b = $('#https-enable');
    if (b) b.click();
  });
  on('#https-disable', 'click', async () => {
    const ok = await confirmModal('关闭 HTTPS', '<p>关闭后回到 HTTP 明文访问。确定吗？</p>', '关闭 HTTPS');
    if (!ok) return;
    try { await apiPost('/api/v1/settings/https', { enable: false, domain: '' }); toast('已关闭 HTTPS', 'ok'); await waitAndJumpHTTPS(false); }
    catch (err) { toast('操作失败：' + err.message, 'err'); }
  });
  on('#tg-form', 'submit', async (e) => {
    e.preventDefault();
    const token = $('#tg-token').value.trim();
    const chatId = $('#tg-chat').value.trim();
    if ($('#tg-enabled').checked && !chatId) { toast('启用提醒需要填写 Chat ID', 'err'); return; }
    try {
      await apiPost('/api/v1/settings/telegram', { enabled: $('#tg-enabled').checked, bot_token: token || undefined, chat_id: chatId });
      toast('Telegram 设置已保存', 'ok'); $('#tg-token').value = '';
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
  // DNS 服务商（v1.3.49）：下拉框只切 UI（显示对应凭证表单），真正保存
  // 靠提交凭证表单（服务商 + 凭证一起存）；手动模式点开启时先调接口切到 manual。
  on('#dns-provider', 'change', () => {
    const sel = $('#dns-provider');
    if (!sel) return;
    const st = Object.assign({}, settingsCache || {}, { dns_provider: sel.value });
    renderDNSProvider(st);
  });
  // 凭证表单通用绑定：收集填了的字段（密码留空 = 保持原值），连同服务商一起存。
  function bindDNSForm(formSel, provider, fieldMap) {
    on(formSel, 'submit', async (e) => {
      e.preventDefault();
      const body = { provider };
      for (const [inputId, key] of Object.entries(fieldMap)) {
        const el = $(inputId);
        const v = el ? el.value.trim() : '';
        if (v) body[key] = v;
      }
      try {
        const r = await apiPost('/api/v1/settings/dns-provider', body);
        toast(dnsProviderDisplay(provider) + ' 凭证已保存' + (r.warning ? '：' + r.warning : ''), 'ok');
        for (const inputId of Object.keys(fieldMap)) { const el = $(inputId); if (el) el.value = ''; }
        await loadSettingsPage();
      } catch (err) { toast('保存失败：' + err.message, 'err'); }
    });
  }
  bindDNSForm('#dns-form-cloudflare', 'cloudflare', { '#dns-cf-token': 'token' });
  bindDNSForm('#dns-form-aliyun', 'aliyun', { '#dns-aliyun-id': 'access_key_id', '#dns-aliyun-secret': 'access_key_secret' });
  bindDNSForm('#dns-form-dnspod', 'dnspod', { '#dns-dnspod-id': 'token_id', '#dns-dnspod-token': 'token' });
  bindDNSForm('#dns-form-route53', 'route53', { '#dns-route53-id': 'access_key_id', '#dns-route53-secret': 'secret_access_key' });
  bindDNSForm('#dns-form-godaddy', 'godaddy', { '#dns-godaddy-pat': 'pat' });
  bindDNSForm('#dns-form-namecheap', 'namecheap', { '#dns-namecheap-user': 'api_user', '#dns-namecheap-key': 'api_key', '#dns-namecheap-ip': 'client_ip' });
  // v1.4.15：第一批 11 家（简报 §5.1 的顺序）。
  bindDNSForm('#dns-form-digitalocean', 'digitalocean', { '#dns-do-token': 'token' });
  bindDNSForm('#dns-form-linode', 'linode', { '#dns-linode-token': 'token' });
  bindDNSForm('#dns-form-vultr', 'vultr', { '#dns-vultr-key': 'token' });
  bindDNSForm('#dns-form-bunny', 'bunny', { '#dns-bunny-key': 'token' });
  bindDNSForm('#dns-form-hetzner', 'hetzner', { '#dns-hetzner-token': 'token' });
  bindDNSForm('#dns-form-spaceship', 'spaceship', { '#dns-spaceship-key': 'api_key', '#dns-spaceship-secret': 'api_secret' });
  bindDNSForm('#dns-form-gandi', 'gandi', { '#dns-gandi-token': 'token' });
  bindDNSForm('#dns-form-namesilo', 'namesilo', { '#dns-namesilo-key': 'token' });
  bindDNSForm('#dns-form-desec', 'desec', { '#dns-desec-token': 'token' });
  bindDNSForm('#dns-form-porkbun', 'porkbun', { '#dns-porkbun-key': 'api_key', '#dns-porkbun-secret': 'api_secret' });
  bindDNSForm('#dns-form-cloudns', 'cloudns', { '#dns-cloudns-id': 'auth_id', '#dns-cloudns-password': 'auth_password', '#dns-cloudns-subuser': 'sub_auth_user' });
  // 手动签发第二步：已添加，继续验证（长请求，带等待状态；成功后跳新协议）。
  on('#manual-continue', 'click', async () => {
    const domain = $('#https-domain').value.trim();
    const btn = $('#manual-continue');
    const oldText = btn ? btn.textContent : '';
    if (btn) { btn.disabled = true; btn.textContent = '正在验证证书…'; }
    toast('正在验证证书，请稍等（可能需要几分钟），不要重复点击', 'ok');
    try {
      const r = await apiPost('/api/v1/settings/https/manual/confirm', { domain });
      if (r.warning) toast(r.warning, 'err');
      else toast('HTTPS 已开启' + (r.msg ? '：' + r.msg : ''), 'ok');
      await waitAndJumpHTTPS(true);
    } catch (err) {
      toast('验证失败：' + err.message, 'err');
      await loadSettingsPage();
    } finally {
      if (btn) { btn.disabled = false; btn.textContent = oldText; }
    }
  });
  on('#manual-cancel', 'click', async () => {
    try { await apiPost('/api/v1/settings/https/manual/cancel', {}); toast('已取消本次签发', 'ok'); await loadSettingsPage(); }
    catch (err) { toast('取消失败：' + err.message, 'err'); }
  });
  on('#door-form', 'submit', async (e) => {
    e.preventDefault();
    const addr = $('#door-addr').value.trim();
    if (addr && !/^[A-Za-z0-9.-]+$/.test(addr)) { toast('任意门地址格式不正确，填 IP 或域名', 'err'); return; }
    try {
      await apiPost('/api/v1/settings/relay-addr', { relay_addr: addr });
      toast(addr ? '任意门地址已保存' : '任意门地址已清除', 'ok'); loadSettingsPage();
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
  on('#tg-test', 'click', async () => {
    try { await apiPost('/api/v1/settings/telegram/test', {}); toast('测试消息已发送，请查看 Telegram', 'ok'); }
    catch (err) { toast('发送失败：' + err.message, 'err'); }
  });
  on('#central-token-generate', 'click', async () => { await centralTokenGenerate(false); });
  on('#central-token-rotate', 'click', async () => {
    const ok = await confirmModal('轮换令牌', '<p>轮换后旧令牌立即失效，已登记这台面板的主面板都要重新登记。确定吗？</p>', '轮换令牌', true);
    if (!ok) return;
    await centralTokenGenerate(true);
  });
  on('#central-token-revoke', 'click', async () => {
    const ok = await confirmModal('撤销令牌', '<p>撤销后任何主面板都连不进来，集中管理立即断开。确定吗？</p>', '撤销令牌', true);
    if (!ok) return;
    try { await apiPost('/api/v1/settings/central-token/revoke', {}); toast('令牌已撤销', 'ok'); loadSettingsPage(); }
    catch (err) { toast('撤销失败：' + err.message, 'err'); }
  });
  on('#central-token-copy', 'click', (e) => { copyText($('#central-token-value').textContent, e.target); });
  on('#sub-generate', 'click', async () => { await subTokenGenerate(false); });
  on('#sub-rotate', 'click', async () => {
    const ok = await confirmModal('轮换订阅链接', '<p>轮换后旧订阅链接立即失效，客户端里已导入的订阅要换成新链接。确定吗？</p>', '轮换订阅链接', true);
    if (!ok) return;
    await subTokenGenerate(true);
  });
  on('#sub-revoke', 'click', async () => {
    const ok = await confirmModal('撤销订阅链接', '<p>撤销后订阅链接立即打不开，客户端里已导入的订阅会失效。确定吗？</p>', '撤销订阅链接', true);
    if (!ok) return;
    try { await apiPost('/api/v1/settings/subscription/revoke', {}); toast('订阅链接已撤销', 'ok'); loadSettingsPage(); }
    catch (err) { toast('撤销失败：' + err.message, 'err'); }
  });
  on('#sub-public-domain-save', 'click', async () => { await saveSubPublicDomain(); });
  on('#sub-file-name-save', 'click', async () => { await saveSubFileName(); });
  // 订阅到期时间与订阅路径（v1.3.20）
  on('#sub-expiry-permanent', 'change', () => { const d = $('#sub-expiry-date'); if (d) d.disabled = $('#sub-expiry-permanent').checked; });
  // 订阅用户创建表单“永久”勾选（v1.3.28）：与通用订阅同一模式，勾选时日期框置灰。
  on('#sub-user-expiry-permanent', 'change', () => { const d = $('#sub-user-expiry-date'); if (d) d.disabled = $('#sub-user-expiry-permanent').checked; });
  on('#sub-expiry-save', 'click', async () => { await saveSubExpiry(); });
  on('#sub-path-save', 'click', async () => { await saveSubPath(); });
  // 每用户端口段（v1.3.25）：限流订阅的独立转发从这里分配。
  on('#user-port-save', 'click', async () => {
    const start = Number($('#user-port-start').value) || 0;
    const end = Number($('#user-port-end').value) || 0;
    try {
      const r = await apiPost('/api/v1/settings/user-ports', { start, end });
      toast('已保存：' + (r.start || '') + '–' + (r.end || '') + '（已分配的转发不受影响，新分配才用新段）', 'ok');
      loadSettingsPage();
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
  // 中转倍率（v1.3.43）：与单落地倍率相乘，空着 = 1。
  on('#relay-multiplier-save', 'click', async () => {
    const mult = parseTrafficMultiplier($('#relay-multiplier'));
    if (mult == null) return;
    try {
      const r = await apiPost('/api/v1/settings/relay-multiplier', { multiplier: mult });
      toast('中转倍率已保存：×' + (r.relay_multiplier != null ? r.relay_multiplier : mult) + '（改完才按新倍率算，之前已记的用量不追溯）', 'ok');
      loadSettingsPage();
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
  // 限流表单联动（v1.3.25）：静态表单里的重置字段按算法与重置模式启用/置灰。
  wireTrafficLimitUI($('#sub-user-name') ? $('#sub-user-name').closest('.card') : null);
  wireTrafficLimitUI($('#central-sub-traffic-row'));
  // 面板 Logo（v1.3.20）
  on('#btn-logo-upload', 'click', async () => { await uploadLogo(); });
  on('#btn-logo-delete', 'click', async () => { await deleteLogo(); });
  // 登录页公告与签名（v1.3.21）
  on('#login-notice-save', 'click', async () => { await saveLoginNotice(); });
  on('#login-signature-save', 'click', async () => { await saveLoginSignature(); });
  // 列表搜索（v1.3.20）：订阅用户、审计日志、备份（落地列表在总览页接线，
  // 合并订阅在集中管理页接线）。
  wireListSearch('#sub-user-search', (q) => { subUserSearch = q; subUserPage = 1; loadSubUsers(); });
  wireListSearch('#audit-search', (q) => { auditSearch = q; auditPage = 1; loadAudit(); });
  wireListSearch('#sched-backup-search', (q) => { schedBackupSearch = q; schedBackupPage = 1; loadSchedBackups(); });
  on('#sub-copy', 'click', (e) => { copyText(subLinkWithMode($('#sub-url-value').textContent, ''), e.target); });
  on('#sub-copy-clash', 'click', (e) => { copyText(subLinkWithMode($('#sub-url-value').textContent, 'clash'), e.target); });
  on('#sub-copy-singbox', 'click', (e) => { copyText(subLinkWithMode($('#sub-url-value').textContent, 'singbox'), e.target); });
  on('#sub-copy-base64', 'click', (e) => { copyText(subLinkWithMode($('#sub-url-value').textContent, 'base64'), e.target); });
  on('#sub-copy-plain', 'click', (e) => { copyText(subLinkWithMode($('#sub-url-value').textContent, 'plain'), e.target); });
  on('#sub-qr', 'click', async () => { await showSubQrModal('generic'); });
  on('#sub-mode', 'change', async () => { if (openQrKind === 'generic') await refreshSubQrModal('generic'); });
  // 三选一（内容模式）改了，二维码正显示着就跟着重生成（v1.3.20，v1.3.21 起二维码在弹窗里）：
  // 二维码编的是令牌 URL，模式跟令牌走，重新拉一次保证扫出来就是新内容。
  on('#sub-token-mode', 'change', async () => {
    if (openQrKind === 'generic') await refreshSubQrModal('generic');
  });
  on('#sub-user-mode', 'change', async () => {
    if (openQrKind === 'user') await refreshSubQrModal('user');
  });
  on('#sub-user-create', 'click', async () => { await subUserCreate(); });
  on('#sub-user-copy', 'click', (e) => { copyText(subLinkWithMode($('#sub-user-url-value').textContent, ''), e.target); });
  on('#sub-user-copy-clash', 'click', (e) => { copyText(subLinkWithMode($('#sub-user-url-value').textContent, 'clash'), e.target); });
  on('#sub-user-copy-plain', 'click', (e) => { copyText(subLinkWithMode($('#sub-user-url-value').textContent, 'plain'), e.target); });
  on('#sub-user-qr', 'click', async () => { await showSubQrModal('user'); });
  document.querySelectorAll('[data-gateway-type]').forEach((b) => b.addEventListener('click', async () => { await loadGatewayCode(b.dataset.gatewayType); }));
  on('#gateway-copy', 'click', (e) => { copyText($('#gateway-code').textContent, e.target); });
  on('#gateway-check', 'click', async () => { await checkGateway(); });
  on('#update-check', 'click', async () => {
    try {
      const r = await api('/api/v1/update/check');
      await loadSettingsPage();
      toast(r.update_available ? '发现新版本 ' + (r.latest_version || '') : '已是最新版本', r.update_available ? undefined : 'ok');
    } catch (err) { toast('检查失败：' + err.message, 'err'); }
  });
  // 自动定时备份设置（v1.3.6）
  on('#bk-save', 'click', async () => {
    try {
      await apiPost('/api/v1/settings/backup', { enabled: $('#bk-enabled').checked, time: $('#bk-time').value, keep: Number($('#bk-keep').value) });
      toast('自动备份设置已保存', 'ok'); loadSchedBackups();
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
  on('#bk-now', 'click', async () => {
    try { await apiPost('/api/v1/scheduled-backups/now', {}); toast('备份已创建', 'ok'); loadSchedBackups(); }
    catch (err) { toast('备份失败：' + err.message, 'err'); }
  });
  // 异地备份（v1.3.11）
  on('#rb-pass-set', 'click', async () => {
    const np = $('#rb-passphrase').value;
    if (!np || np.length < 8) { toast('口令至少 8 位', 'err'); return; }
    try {
      const r = await apiPost('/api/v1/settings/remote-backup/passphrase', { old_passphrase: $('#rb-old-passphrase').value, new_passphrase: np });
      $('#rb-passphrase').value = ''; $('#rb-old-passphrase').value = '';
      toast(r.warn ? r.warn : '口令已设置', 'ok');
      renderRemoteBackupCard(r.remote_backup || {});
    } catch (err) { toast('设置失败：' + err.message, 'err'); }
  });
  on('#rb-pass-clear', 'click', async () => {
    const ok = await confirmModal('清除异地备份口令', '<p>清除后两个上传开关同时关闭，面板不再上传新备份。之前传上去的备份只要还记得口令，恢复时输口令照样解得开。确定清除吗？</p>', '清除口令', true);
    if (!ok) return;
    try { const r = await apiPost('/api/v1/settings/remote-backup/passphrase/clear', {}); toast('口令已清除', 'ok'); renderRemoteBackupCard(r.remote_backup || {}); }
    catch (err) { toast('清除失败：' + err.message, 'err'); }
  });
  on('#rb-save', 'click', async () => {
    try {
      const r = await apiPost('/api/v1/settings/remote-backup', {
        r2: { enabled: $('#rb-r2-enabled').checked, account_id: $('#rb-r2-account').value.trim(), bucket: $('#rb-r2-bucket').value.trim(), access_key: $('#rb-r2-access').value.trim(), secret_key: $('#rb-r2-secret').value, endpoint: $('#rb-r2-endpoint').value.trim() },
        qiniu: { enabled: $('#rb-qn-enabled').checked, region: $('#rb-qn-region').value, endpoint: $('#rb-qn-endpoint').value.trim(), bucket: $('#rb-qn-bucket').value.trim(), access_key: $('#rb-qn-access').value.trim(), secret_key: $('#rb-qn-secret').value },
      });
      $('#rb-r2-secret').value = ''; $('#rb-qn-secret').value = '';
      toast('异地备份设置已保存', 'ok'); renderRemoteBackupCard(r.remote_backup || {});
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
  on('#rb-r2-test', 'click', async () => {
    try { const r = await apiPost('/api/v1/remote-backup/test/r2', {}); toast(r.detail || '测试通过', 'ok'); }
    catch (err) { toast('测试失败：' + err.message, 'err'); }
  });
  on('#rb-qn-test', 'click', async () => {
    try { const r = await apiPost('/api/v1/remote-backup/test/qiniu', {}); toast(r.detail || '测试通过', 'ok'); }
    catch (err) { toast('测试失败：' + err.message, 'err'); }
  });
  on('#export-btn', 'click', async () => {
    try {
      const res = await fetch('/api/v1/export');
      if (!res.ok) throw new Error('导出失败（HTTP ' + res.status + '）');
      const text = await res.text();
      downloadFile('relay-panel-export-' + new Date().toISOString().slice(0, 10) + '.json', text);
      toast('导出完成', 'ok');
    } catch (err) { toast(err.message, 'err'); }
  });
  on('#import-file', 'change', async (e) => {
    const file = e.target.files[0];
    if (!file) return;
    const text = await file.text();
    let data;
    try { data = JSON.parse(text); } catch (err) { $('#import-result').innerHTML = '<div class="error-box">文件不是合法的 JSON：' + esc(err.message) + '</div>'; e.target.value = ''; return; }
    if (!data.relay_conf && !data.config_toml && !data.state) {
      $('#import-result').innerHTML = '<div class="error-box">文件格式不正确：缺少 relay_conf / config_toml / state，不是面板导出的文件。</div>';
      e.target.value = ''; return;
    }
    const hasMig = !!data.migration_enc;
    const ok = await confirmModal('导入配置', '<p>导入会覆盖当前全部落地与设置（导入前会自动备份当前配置）。' + (hasMig ? '这份文件含迁移信封，订阅令牌、到期与限流随导入恢复，旧链接继续可用；导入时要输异地备份口令。' : '这份文件没设迁移口令，导入后旧订阅链接失效，要逐个轮换后重新发链接；密码与 Telegram Token 保持本机原值，不随导入走。') + '确定导入 <b>' + esc(file.name) + '</b> 吗？</p>', '确认导入', true);
    if (!ok) { e.target.value = ''; return; }
    if (hasMig) {
      const pass = await promptMigrationPassphrase();
      if (pass == null) { e.target.value = ''; return; }
      data.passphrase = pass;
    }
    try {
      const r = await apiPost('/api/v1/import', data);
      $('#import-result').innerHTML = '<div class="ok-box">导入成功，realm 已重启并验过端口。' + ((r && r.notes && r.notes.length) ? esc(r.notes.join('；')) : '') + '</div>';
      toast('导入成功', 'ok'); loadOverview();
    } catch (err) { $('#import-result').innerHTML = '<div class="error-box">导入失败：' + esc(err.message) + '</div>'; }
    e.target.value = '';
  });
}
/* ---------- 更新面板：轮询等重启，完成后自动跳登录页 ---------- */
let updatePollTimer = null;
function stopUpdatePoll() { if (updatePollTimer) { clearInterval(updatePollTimer); updatePollTimer = null; } }
function pollUpdateDone(btn) {
  // 每 3 秒调一次 /api/v1/me（noAuthRedirect 豁免，自己判断状态）：
  // 200 = 旧面板还在跑，继续等；连不上（重启中）继续等；401 = 新面板已起来、旧会话失效，跳登录页。
  const started = Date.now();
  stopUpdatePoll();
  updatePollTimer = setInterval(async () => {
    if (Date.now() - started > 5 * 60 * 1000) {
      stopUpdatePoll();
      btn.disabled = false; btn.textContent = '更新面板';
      $('#update-box').innerHTML = '<div class="warn-box">更新超时，请手动刷新页面。<button class="btn sm" type="button" onclick="location.reload()">刷新页面</button></div>';
      return;
    }
    try {
      await api('/api/v1/me', {}, true);
    } catch (err) {
      const msg = String(err.message || '');
      if (msg.indexOf('401') >= 0 || msg.indexOf('未登录') >= 0) {
        stopUpdatePoll();
        btn.disabled = false; btn.textContent = '更新面板';
        showLogin();
        toast('更新完成，请重新登录', 'ok');
      }
      // 其他失败（连不上、面板重启中）继续等
    }
  }, 3000);
}
/* ---------- 机器监控告警设置（顶层 init：绝不写在别的函数里面） ---------- */
function initMonitorSettings() {
  on('#monitor-form', 'submit', async (e) => {
    e.preventDefault();
    const num = (id, def) => { const v = Number($(id).value); return v ? v : def; };
    const body = {
      disk_warn: num('#mon-disk-warn', 85), disk_crit: num('#mon-disk-crit', 95),
      mem_warn: num('#mon-mem-warn', 80), mem_crit: num('#mon-mem-crit', 90),
      cpu_warn: num('#mon-cpu-warn', 80), cpu_crit: num('#mon-cpu-crit', 95),
      interval_min: num('#mon-interval', 5),
    };
    if (body.disk_warn >= body.disk_crit || body.mem_warn >= body.mem_crit || body.cpu_warn >= body.cpu_crit) {
      toast('警告阈值必须小于严重阈值，请检查后重填', 'err'); return;
    }
    try {
      await apiPost('/api/v1/settings/monitor', body);
      toast('监控设置已保存，立即生效', 'ok');
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
}
/* ---------- 延迟探测设置（顶层 init：绝不写在别的函数里面） ---------- */
function initLatencySettings() {
  on('#latency-form', 'submit', async (e) => {
    e.preventDefault();
    const interval = Number($('#lat-interval').value) || 5;
    if (interval < 1 || interval > 60) { toast('探测间隔请填 1–60 分钟', 'err'); return; }
    try {
      await apiPost('/api/v1/settings/latency', { enabled: $('#lat-enabled').checked, interval_min: interval });
      toast('延迟探测设置已保存，立即生效', 'ok');
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
}
function initUpdatePoll() {
  on('#update-apply', 'click', async () => {
    const ok = await confirmModal('更新面板', '<p>更新只重启面板自己，不动 realm，正在用的节点不断线。更新完自动跳到登录页重新登录。确定更新吗？</p>', '开始更新');
    if (!ok) return;
    const btn = $('#update-apply');
    btn.disabled = true; btn.textContent = '正在更新…';
    $('#update-box').innerHTML = '<div class="info-box">正在更新，面板重启后会自动跳到登录页，请稍候…</div>';
    try {
      await apiPost('/api/v1/update/apply', {});
      pollUpdateDone(btn);
    } catch (err) {
      btn.disabled = false; btn.textContent = '更新面板';
      $('#update-box').innerHTML = '';
      toast('更新失败：' + err.message, 'err');
    }
  });
}
// 自动定时备份列表（v1.3.6）：恢复/删除，恢复前弹确认（会覆盖当前配置并重启 realm）。
let schedBackupPage = 1, schedBackupSearch = ''; // v1.3.20：备份列表分页与搜索
async function loadSchedBackups() {
  const wrap = $('#sched-backup-list');
  if (!wrap) return;
  try {
    const r = await api('/api/v1/scheduled-backups');
    const list = r.backups || [];
    const last = $('#bk-last');
    if (last) last.textContent = r.last_backup ? '上次备份：' + fmtDate(r.last_backup.ts) + '（' + (r.last_backup.label || '') + '）' : '还没有定时备份。';
    if (!list.length) { wrap.innerHTML = '<div class="empty">暂无定时备份。</div>'; return; }
    // v1.3.20：备份列表搜索（时间/类型/编号，分页前先过滤）+ 分页（每页 20 条）。
    const filtered = list.filter((b) => matchSubText(schedBackupSearch, [fmtDate(b.ts), b.label, b.id]));
    if (!filtered.length) { wrap.innerHTML = '<div class="empty">没有匹配的备份，换个关键词试试。</div>'; return; }
    const pg = paginate(filtered, schedBackupPage);
    schedBackupPage = pg.cur;
    wrap.innerHTML = pg.slice.map((b) =>
      '<div class="kv"><span>' + esc(fmtDate(b.ts)) + '<br><span class="small">' + esc(b.label || '') + ' · ' + (b.size != null ? (b.size / 1024).toFixed(1) + ' KB' : '') + (b.has_migration_enc ? ' · 可整机迁移' : (b.migration_enc_missing ? ' · 未设迁移口令，换机需重发链接' : '')) + '</span></span>' +
      '<span><button class="btn secondary sm" type="button" data-restore-sbk="' + esc(b.id) + '" data-mig-enc="' + (b.has_migration_enc ? '1' : '') + '">恢复</button> ' +
      '<button class="btn secondary sm" type="button" data-del-sbk="' + esc(b.id) + '">删除</button></span></div>').join('') +
      pagerHtml(pg.total, pg.cur, pg.totalPages, 'schedbackup');
    if (pg.totalPages > 1) wirePager(wrap, 'schedbackup', pg.cur, pg.totalPages, (p) => { schedBackupPage = p; loadSchedBackups(); });
    $$('[data-restore-sbk]', wrap).forEach((btn) => btn.addEventListener('click', async () => {
      // v1.3.29：含迁移信封的备份恢复前先弹口令框；口令错整单失败、当前配置不动。
      const hasMig = btn.dataset.migEnc === '1';
      const ok = await confirmModal('恢复定时备份', '<p>恢复会用这份备份覆盖当前配置并重启 realm；恢复后不在备份里的订阅用户会被移除。' + (hasMig ? '这份备份含迁移信封，订阅令牌、到期与限流随备份恢复，旧链接继续可用。' : '这份备份没设迁移口令，恢复后旧订阅链接失效，要逐个轮换后重新发链接。') + '确定恢复吗？</p>', '恢复备份', true);
      if (!ok) return;
      let pass = '';
      if (hasMig) {
        pass = await promptMigrationPassphrase();
        if (pass == null) return;
      }
      try {
        const r = await apiPost('/api/v1/scheduled-backups/' + encodeURIComponent(btn.dataset.restoreSbk) + '/restore', { passphrase: pass });
        toast('备份已恢复' + (r && r.notes && r.notes.length ? '：' + r.notes.join('；') : ''), 'ok'); loadOverview();
      }
      catch (e) { toast('恢复失败：' + e.message, 'err'); }
    }));
    $$('[data-del-sbk]', wrap).forEach((btn) => btn.addEventListener('click', async () => {
      const ok = await confirmModal('删除定时备份', '<p>确定删除这份定时备份吗？删了找不回来。</p>', '删除', true);
      if (!ok) return;
      try { await api('/api/v1/scheduled-backups/' + encodeURIComponent(btn.dataset.delSbk), { method: 'DELETE' }); toast('已删除', 'ok'); loadSchedBackups(); }
      catch (e) { toast('删除失败：' + e.message, 'err'); }
    }));
  } catch (e) { wrap.innerHTML = '<div class="empty">定时备份列表不可用：' + esc(e.message) + '</div>'; }
}
/* ---------- 异地备份（v1.3.11） ---------- */
// renderRemoteBackupCard 设置页回填：口令状态、两个目的地的开关与字段、
// 上次上传状态；开启的目的地拉远程列表。SecretKey 从不回填（留空 = 不变）。
function renderRemoteBackupCard(rb) {
  rb = rb || {};
  const ps = $('#rb-pass-status');
  if (ps) ps.textContent = rb.passphrase_set ? '口令已设置。这个口令同时是迁移口令：设了口令后备份才含订阅令牌与迁移信封，换机恢复不用重发链接。换口令在上面填新口令与旧口令；清除口令会同时关闭两个上传开关，旧备份仍可用旧口令恢复。' : '还没设置异地备份口令。设了口令后备份才能整机迁移（换机不用重发链接）；没设口令的备份换机要逐个轮换令牌。';
  const r2 = rb.r2 || {}, qn = rb.qiniu || {};
  const set = (id, v) => { const el = $(id); if (el) el.value = v || ''; };
  const chk = (id, v) => { const el = $(id); if (el) el.checked = !!v; };
  chk('#rb-r2-enabled', r2.enabled); set('#rb-r2-account', r2.account_id); set('#rb-r2-bucket', r2.bucket);
  set('#rb-r2-access', r2.access_key); set('#rb-r2-endpoint', r2.endpoint_override);
  chk('#rb-qn-enabled', qn.enabled); set('#rb-qn-bucket', qn.bucket); set('#rb-qn-access', qn.access_key);
  set('#rb-qn-endpoint', qn.endpoint_override);
  const qnr = $('#rb-qn-region'); if (qnr) qnr.value = qn.region || 'cn-east-1';
  const st2 = $('#rb-r2-status'); if (st2) st2.textContent = r2.last_status ? '上次上传：' + r2.last_status : '';
  const stq = $('#rb-qn-status'); if (stq) stq.textContent = qn.last_status ? '上次上传：' + qn.last_status : '';
  if (r2.enabled) loadRemoteList('r2'); else { const w = $('#rb-r2-list'); if (w) w.innerHTML = ''; }
  if (qn.enabled) loadRemoteList('qiniu'); else { const w = $('#rb-qn-list'); if (w) w.innerHTML = ''; }
}
// loadRemoteList 远程备份列表：每行恢复（弹口令框）与下载（原样下 .enc）。
// v1.3.13：每行标出来源（R2/七牛云），和本机备份列表区分开。
async function loadRemoteList(kind) {
  const wrap = $(kind === 'r2' ? '#rb-r2-list' : '#rb-qn-list');
  if (!wrap) return;
  const srcTag = kind === 'r2' ? 'R2' : '七牛云';
  try {
    const r = await api('/api/v1/remote-backup/list/' + kind);
    const list = r.objects || [];
    if (!list.length) { wrap.innerHTML = '<div class="empty">远程没有备份。</div>'; return; }
    wrap.innerHTML = list.map((o) =>
      '<div class="kv"><span><span class="tag">' + srcTag + '</span> ' + esc(o.key) + '<br><span class="small">' + (o.last_modified ? esc(fmtDate(o.last_modified)) + ' · ' : '') + (o.size != null ? (o.size / 1024).toFixed(1) + ' KB' : '') + '</span></span>' +
      '<span><button class="btn secondary sm" type="button" data-rb-restore="' + esc(o.key) + '" data-rb-dest="' + kind + '">恢复</button> ' +
      '<button class="btn secondary sm" type="button" data-rb-download="' + esc(o.key) + '" data-rb-dest="' + kind + '">下载</button></span></div>').join('');
    $$('[data-rb-restore]', wrap).forEach((btn) => btn.addEventListener('click', async () => {
      const pass = await promptRemotePassphrase();
      if (pass == null) return;
      try {
        await apiPost('/api/v1/remote-backup/restore/' + btn.dataset.rbDest + '/' + encodeURIComponent(btn.dataset.rbRestore), { passphrase: pass });
        toast('备份已恢复', 'ok'); loadOverview();
      } catch (e) { toast('恢复失败：' + e.message, 'err'); }
    }));
    $$('[data-rb-download]', wrap).forEach((btn) => btn.addEventListener('click', async () => {
      try {
        const res = await fetch('/api/v1/remote-backup/download/' + btn.dataset.rbDest + '/' + encodeURIComponent(btn.dataset.rbDownload));
        if (!res.ok) throw new Error('下载失败（HTTP ' + res.status + '）');
        const blob = await res.blob();
        const a = document.createElement('a');
        a.href = URL.createObjectURL(blob); a.download = btn.dataset.rbDownload; a.click();
        setTimeout(() => URL.revokeObjectURL(a.href), 5000);
        toast('已下载 ' + btn.dataset.rbDownload, 'ok');
      } catch (e) { toast(e.message, 'err'); }
    }));
  } catch (e) { wrap.innerHTML = '<div class="empty">远程列表不可用：' + esc(e.message) + '</div>'; }
}
// promptRemotePassphrase 恢复远程备份时弹口令框：确认返回口令，取消返回 null。
// v1.3.29：标题与提示可覆盖，迁移信封恢复复用同一口令框。
function promptRemotePassphrase(title, hint) {
  return new Promise((resolve) => {
    const m = openModal('<h2>' + esc(title || '恢复远程备份') + '</h2><div><p>' + esc(hint || '这份备份是加密的，恢复要输口令。如果是换口令之前传的，请输旧口令。') + '</p>' +
      '<label for="rb-restore-pass">口令</label><input id="rb-restore-pass" type="password" autocomplete="current-password" placeholder="异地备份口令"></div>' +
      '<div class="btn-row"><button class="btn danger" id="rb-restore-ok" type="button">恢复备份</button>' +
      '<button class="btn secondary" id="rb-restore-cancel" type="button">取消</button></div>');
    if (!m) { resolve(null); return; }
    const input = $('#rb-restore-pass', m);
    if (input) input.focus();
    onIn(m, '#rb-restore-ok', 'click', () => { const v = input ? input.value : ''; closeModal(); resolve(v); });
    onIn(m, '#rb-restore-cancel', 'click', () => { closeModal(); resolve(null); });
  });
}
// promptMigrationPassphrase 恢复含迁移信封的备份时弹口令框：确认返回口令，取消返回 null。
function promptMigrationPassphrase() {
  return promptRemotePassphrase('恢复备份：输入迁移口令', '这份备份含迁移信封（登录密码、TOTP、令牌密钥等），恢复要输异地备份口令。口令错了整单失败，当前配置不动。');
}
/* ---------- 通用前端分页（审计/端口表/各列表共用） ---------- */
const PAGE_SIZE = 20;
// 跳页输入钳制：非数字返回 null（调用方给轻提示），小于 1 取 1，大于总页数取总页数
function clampPage(v, totalPages) {
  const p = parseInt(v, 10);
  if (isNaN(p)) return null;
  if (p < 1) return 1;
  if (p > totalPages) return totalPages;
  return p;
}
// 对前端列表切页：返回当前页切片 + 总数/总页数/钳制后的当前页（总页数变少、当前页越界时自动落到最后一页）
function paginate(list, page) {
  const total = list.length;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const cur = Math.min(Math.max(page || 1, 1), totalPages);
  return { slice: list.slice((cur - 1) * PAGE_SIZE, cur * PAGE_SIZE), total, totalPages, cur };
}
// 分页条 HTML：总页数 ≤1 时返回空串（不显示）。ns 是命名空间（audit/ports/overview…），
// 区分同一页里多个分页条的 data-<ns>-page 与 <ns>-jump。
function pagerHtml(total, cur, totalPages, ns) {
  if (totalPages <= 1) return '';
  // 最多显示 5 个页码，当前页前后各留 2 个
  let start = Math.max(1, Math.min(cur - 2, totalPages - 4)), end = Math.min(totalPages, start + 4);
  if (end - start < 4) start = Math.max(1, end - 4);
  let nums = '';
  for (let p = start; p <= end; p++) nums += '<button class="btn sm' + (p === cur ? ' active' : '') + '" type="button" data-' + ns + '-page="' + p + '"' + (p === cur ? ' disabled' : '') + '>' + p + '</button>';
  return '<div class="pager"><span class="pager-info">共 ' + total + ' 条，第 ' + cur + ' / ' + totalPages + ' 页</span>' +
    '<button class="btn sm" type="button" data-' + ns + '-page="prev"' + (cur <= 1 ? ' disabled' : '') + '>上一页</button>' + nums +
    '<button class="btn sm" type="button" data-' + ns + '-page="next"' + (cur >= totalPages ? ' disabled' : '') + '>下一页</button>' +
    '<span class="pager-jump">跳至 <input id="' + ns + '-jump" class="pager-input" type="number" min="1" max="' + totalPages + '" value="' + cur + '" aria-label="页码"> 页 <button class="btn sm" type="button" id="' + ns + '-jump-btn">跳转</button></span></div>';
}
// 绑定分页条事件：onPage(页码) 由调用方接（通常是重新渲染该列表）。只在 totalPages > 1 时调用。
function wirePager(wrap, ns, cur, totalPages, onPage) {
  $$('[data-' + ns + '-page]', wrap).forEach((b) => b.addEventListener('click', () => {
    const v = b.dataset[ns + 'Page'];
    onPage(v === 'prev' ? cur - 1 : v === 'next' ? cur + 1 : Number(v));
  }));
  const doJump = () => {
    const inp = $('#' + ns + '-jump', wrap);
    const p = clampPage(inp.value, totalPages);
    if (p === null) { toast('请输入有效页码', 'err'); inp.value = cur; return; }
    if (p === cur) { inp.value = cur; return; }
    onPage(p);
  };
  onIn(wrap, '#' + ns + '-jump-btn', 'click', doJump);
  onIn(wrap, '#' + ns + '-jump', 'keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); doJump(); } });
}
let auditPage = 1;
const AUDIT_PAGE_SIZE = 10; // v1.3.13：审计日志每页 10 条
let auditSearch = ''; // v1.3.20：审计日志搜索关键词（服务端按 q 过滤）
async function loadAudit(page) {
  if (page) auditPage = page;
  const wrap = $('#audit-list');
  try {
    const q = auditSearch || '';
    const r = await api('/api/v1/audit?page=' + auditPage + '&page_size=' + AUDIT_PAGE_SIZE + (q ? '&q=' + encodeURIComponent(q) : ''));
    const entries = r.entries || [];
    const total = r.total || 0, totalPages = r.total_pages || 0, cur = r.page || auditPage;
    if (!total) { wrap.innerHTML = '<div class="empty">暂无审计记录。</div>'; return; }
    let html = '<div class="table-wrap"><table><thead><tr><th>时间</th><th>用户</th><th>操作</th><th>对象</th><th>结果</th></tr></thead><tbody>' +
      entries.map((en) => '<tr><td>' + esc(fmtDate(en.ts)) + '</td><td>' + esc(en.user || '') + '</td><td>' + esc(en.action || '') + '</td><td>' + esc(en.target || '') + '</td><td>' + esc(en.result || '') + (en.detail ? '<br><span class="small">' + esc(en.detail) + '</span>' : '') + '</td></tr>').join('') + '</tbody></table></div>';
    html += pagerHtml(total, cur, totalPages, 'audit');
    wrap.innerHTML = html;
    if (totalPages > 1) wirePager(wrap, 'audit', cur, totalPages, (p) => loadAudit(p));
  } catch (e) { wrap.innerHTML = '<div class="empty">审计日志不可用：' + esc(e.message) + '</div>'; }
}

/* ---------- 端口管理 ---------- */
let firewallPage = 1;
async function loadFirewall(page) {
  if (page) firewallPage = page;
  const st = $('#firewall-status'), rules = $('#firewall-rules');
  try {
    const r = await api('/api/v1/firewall');
    if (!r.available) {
      st.innerHTML = '<div class="warn-box">防火墙不可用：' + esc(r.note || '本机未安装或未启用 ufw，端口管理不可用。请先在服务器上安装并启用 ufw。') + '</div>';
      rules.innerHTML = '<div class="empty">ufw 不可用，无法列出已开放端口。</div>';
      return;
    }
    st.innerHTML = '<div class="info-box">ufw 状态：' + (r.active ? '已启用' : '未启用') + '。' + esc(r.note || '只管本机防火墙（ufw），云厂商安全组还得去控制台开。') + '</div>';
    const list = r.rules || [];
    if (!list.length) rules.innerHTML = '<div class="empty">当前没有已开放的端口规则。</div>';
    else {
      const pg = paginate(list, firewallPage);
      firewallPage = pg.cur;
      rules.innerHTML = '<div class="table-wrap"><table><thead><tr><th>端口</th><th>协议</th><th>动作</th><th></th></tr></thead><tbody>' +
        pg.slice.map((ru) => '<tr><td>' + esc(ru.port) + '</td><td>' + esc(ru.proto || 'tcp') + '</td><td>' + esc(ru.action || 'allow') + '</td>' +
          '<td><button class="btn danger sm" type="button" data-close-fw="' + esc(ru.port) + '" data-proto="' + esc(ru.proto || 'tcp') + '">关闭</button></td></tr>').join('') + '</tbody></table></div>' +
        pagerHtml(pg.total, pg.cur, pg.totalPages, 'firewall');
      $$('[data-close-fw]', rules).forEach((b) => b.addEventListener('click', async () => {
        const ok = await confirmModal('关闭端口', '<p>确定关闭端口 <b>' + esc(b.dataset.closeFw) + '/' + esc(b.dataset.proto) + '</b> 吗？关闭后该端口的入站访问会被拦截。</p>', '关闭端口', true);
        if (!ok) return;
        try { await apiPost('/api/v1/firewall', { action: 'close', port: Number(b.dataset.closeFw), proto: b.dataset.proto }); toast('端口已关闭', 'ok'); loadFirewall(); }
        catch (e) { toast('关闭失败：' + e.message, 'err'); }
      }));
      if (pg.totalPages > 1) wirePager(rules, 'firewall', pg.cur, pg.totalPages, (p) => loadFirewall(p));
    }
  } catch (e) {
    st.innerHTML = '<div class="error-box">端口管理加载失败：' + esc(e.message) + '</div>';
  }
}
function initFirewall() {
  on('#fw-one-form', 'submit', async (e) => {
    e.preventDefault();
    const port = Number($('#fw-port').value);
    if (!port || port < 1 || port > 65535) { toast('端口请填 1–65535', 'err'); return; }
    try { await apiPost('/api/v1/firewall', { action: 'allow', port, proto: $('#fw-proto').value }); toast('端口 ' + port + ' 已放行', 'ok'); loadFirewall(); }
    catch (err) { toast('放行失败：' + err.message, 'err'); }
  });
  on('#fw-range-form', 'submit', async (e) => {
    e.preventDefault();
    const from = Number($('#fw-from').value), to = Number($('#fw-to').value);
    if (!from || !to || from < 1 || to > 65535 || from > to) { toast('请填写正确的端口范围（起始 ≤ 结束，1–65535）', 'err'); return; }
    if (to - from > 1000) { toast('范围过大（超过 1000 个端口），请缩小范围', 'err'); return; }
    try { await apiPost('/api/v1/firewall', { action: 'allow-range', from, to, proto: $('#fw-range-proto').value }); toast('端口范围 ' + from + '–' + to + ' 已放行', 'ok'); loadFirewall(); }
    catch (err) { toast('放行失败：' + err.message, 'err'); }
  });
  on('#fw-open-all', 'click', async () => {
    const ok = await confirmModal('一键开放全部端口', '<p><b>⚠️ 高风险操作：</b>将放行全部入站端口，所有端口都会暴露，包括 SSH 等服务端口，可能被扫描攻击。</p><p>建议只在需要时使用，用完立即「一键恢复只开必要端口」。确定继续吗？</p>', '我明白，全部开放', true);
    if (!ok) return;
    try { await apiPost('/api/v1/firewall', { action: 'open-all', confirm: true }); toast('已开放全部端口，请用完尽快恢复', 'ok'); loadFirewall(); }
    catch (err) { toast('操作失败：' + err.message, 'err'); }
  });
  on('#fw-restore', 'click', async () => {
    const ok = await confirmModal('恢复只开必要端口', '<p>将只保留必要端口：面板端口 + 各落地监听端口 + 22（SSH），其余全部关闭。确定吗？</p>', '恢复');
    if (!ok) return;
    try { await apiPost('/api/v1/firewall', { action: 'restore-default' }); toast('已恢复只开必要端口', 'ok'); loadFirewall(); }
    catch (err) { toast('操作失败：' + err.message, 'err'); }
  });
}

/* ---------- 流量（纯 Canvas 折线图） ---------- */
let trafficRange = 'day';
let trafficLanding = null;
async function loadTrafficPage() {
  try {
    const s = await api('/api/v1/status');
    landingsCache = s.landings || [];
    const sel = $('#traffic-landing');
    if (!landingsCache.length) {
      sel.innerHTML = '';
      $('#traffic-summary').innerHTML = '<div class="empty">还没有落地节点，无法统计流量。</div>';
      drawChart([], '暂无数据');
      return;
    }
    sel.innerHTML = landingsCache.map((l) => '<option value="' + l.index + '">' + esc(displayNameOf(l)) + '</option>').join('');
    if (!trafficLanding || !landingsCache.some((l) => l.index === trafficLanding)) trafficLanding = landingsCache[0].index;
    sel.value = String(trafficLanding);
    sel.onchange = () => { trafficLanding = Number(sel.value); loadTrafficData(); };
    // 填充限制表单默认值
    const l = landingsCache.find((x) => x.index === trafficLanding);
    if (l && l.traffic) {
      $('#limit-bytes-gb').value = Math.round((l.traffic.limit_bytes || 0) / 1073741824);
      $('#limit-mode').value = l.traffic.limit_mode || 'total';
      $('#limit-speed').value = l.speed_limit_mbps || 0;
    }
    loadTrafficData();
  } catch (e) {
    $('#traffic-summary').innerHTML = '<div class="error-box">流量页加载失败：' + esc(e.message) + '</div>';
  }
}
async function loadTrafficData() {
  const box = $('#traffic-summary'), warn = $('#traffic-unavailable');
  warn.classList.add('hidden');
  try {
    const r = await api('/api/v1/traffic?landing_index=' + trafficLanding + '&range=' + trafficRange);
    if (r.available === false) {
      warn.textContent = '流量统计不可用：本机无 iptables / nftables，无法统计流量。请在服务器上安装 iptables 或 nftables 后再试。';
      warn.classList.remove('hidden');
      drawChart([], '流量统计不可用');
      const sum = r.summary || {};
      box.innerHTML = trafficSummaryHtml(sum, trafficLanding);
      return;
    }
    const series = r.series || [];
    drawChart(series, trafficRange === 'day' ? '最近 24 小时（按小时）' : trafficRange === 'month' ? '最近 30 天（按天）' : '累计');
    box.innerHTML = trafficSummaryHtml(r.summary || {}, trafficLanding);
  } catch (e) {
    box.innerHTML = '<div class="error-box">流量加载失败：' + esc(e.message) + '</div>';
    drawChart([], '加载失败');
  }
}
function trafficSummaryHtml(sum, landingIndex) {
  const l = landingsCache.find((x) => x.index === landingIndex);
  const pct = Number(sum.pct) || 0;
  const cls = pct >= 100 ? 'bad' : pct >= 80 ? 'warn' : '';
  let h = '<div class="grid cols-4">' +
    statCard('今日上行', fmtBytes(sum.today_up), '今日下行 ' + fmtBytes(sum.today_down)) +
    statCard('本月上行', fmtBytes(sum.month_up), '本月下行 ' + fmtBytes(sum.month_down)) +
    statCard('累计上行', fmtBytes(sum.total_up), '累计下行 ' + fmtBytes(sum.total_down)) +
    statCard('已用 / 上限', (sum.limit_bytes ? pct + '%' : '不限'), sum.limit_bytes ? fmtBytes(sum.used_bytes || 0) + ' / ' + fmtBytes(sum.limit_bytes) : fmtBytes(sum.used_bytes || 0) + ' 已用') +
    '</div>';
  if (sum.limit_bytes) {
    h += '<div style="margin-top:12px"><div class="bar-row"><span>用量（' + (sum.limit_mode === 'monthly' ? '按月' : '总量') + '）</span><span>' + pct + '%</span></div><div class="bar"><i class="' + cls + '" style="width:' + Math.min(100, pct) + '%"></i></div></div>';
    if (pct >= 100) h += '<div class="error-box">流量已用完，节点已停用（停用原因：流量用完）。请「续量」恢复。</div>';
    else if (pct >= 80) h += '<div class="warn-box">流量已用 ' + pct + '%，接近上限，请留意续量。</div>';
  }
  if (l && l.disabled && l.disabled_reason) h += '<div class="error-box">当前已停用，原因：' + esc(disabledReasonText(l.disabled_reason)) + '。' + (l.disabled_reason.indexOf('traffic') >= 0 ? '请续量；' : '') + (l.disabled_reason.indexOf('expired') >= 0 ? '请续期；' : '') + '两者都到了就两个都要续。</div>';
  else if (l && l.expiry) { const d = daysLeftText(l.expiry); h += '<div class="info-box">到期：' + esc(d.text) + (l.expiry ? '（' + esc(fmtDate(l.expiry)) + '）' : '') + '。到期与流量是“或”的关系，哪个先到节点就停用。</div>'; }
  return h;
}
function drawChart(series, label) {
  const cv = $('#traffic-chart');
  const ctx = cv.getContext('2d');
  const W = cv.width, H = cv.height;
  ctx.clearRect(0, 0, W, H);
  const dark = document.documentElement.dataset.theme === 'dark';
  const grid = dark ? '#334155' : '#e3e8f0';
  const text = dark ? '#94a3b8' : '#667085';
  ctx.font = '13px sans-serif';
  if (!series.length) {
    ctx.fillStyle = text; ctx.textAlign = 'center';
    ctx.fillText(label + '：暂无数据', W / 2, H / 2);
    return;
  }
  const pts = series.map((p) => ({ ts: p.ts, up: Number(p.up) || 0, down: Number(p.down) || 0 }));
  const maxV = Math.max.apply(null, pts.map((p) => Math.max(p.up, p.down)).concat([1]));
  const padL = 64, padR = 16, padT = 28, padB = 30;
  const iw = W - padL - padR, ih = H - padT - padB;
  // 网格 + Y 轴
  ctx.strokeStyle = grid; ctx.fillStyle = text; ctx.textAlign = 'right';
  for (let i = 0; i <= 4; i++) {
    const y = padT + ih - ih * i / 4;
    ctx.beginPath(); ctx.moveTo(padL, y); ctx.lineTo(W - padR, y); ctx.stroke();
    ctx.fillText(fmtBytes(maxV * i / 4), padL - 8, y + 4);
  }
  // X 轴标签（首 / 中 / 末）
  ctx.textAlign = 'center';
  const fmtTs = (ts) => { const d = new Date(ts); return isNaN(d) ? String(ts) : (d.getMonth() + 1) + '/' + d.getDate() + ' ' + String(d.getHours()).padStart(2, '0') + ':00'; };
  ctx.fillText(fmtTs(pts[0].ts), padL, H - 8);
  ctx.fillText(fmtTs(pts[Math.floor(pts.length / 2)].ts), padL + iw / 2, H - 8);
  ctx.fillText(fmtTs(pts[pts.length - 1].ts), W - padR, H - 8);
  const line = (key, color) => {
    ctx.strokeStyle = color; ctx.lineWidth = 2; ctx.beginPath();
    pts.forEach((p, i) => {
      const x = padL + (pts.length === 1 ? iw / 2 : iw * i / (pts.length - 1));
      const y = padT + ih - ih * p[key] / maxV;
      if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    });
    ctx.stroke();
  };
  line('down', '#2563eb');
  line('up', '#16a34a');
  ctx.lineWidth = 1;
  // 图例
  ctx.textAlign = 'left';
  ctx.fillStyle = '#2563eb'; ctx.fillRect(padL, 8, 14, 4);
  ctx.fillStyle = text; ctx.fillText('下行', padL + 20, 14);
  ctx.fillStyle = '#16a34a'; ctx.fillRect(padL + 80, 8, 14, 4);
  ctx.fillStyle = text; ctx.fillText('上行', padL + 100, 14);
  ctx.fillStyle = text; ctx.textAlign = 'right'; ctx.fillText(label, W - padR, 14);
}
function initTraffic() {
  $$('[data-range]').forEach((b) => b.addEventListener('click', () => {
    trafficRange = b.dataset.range;
    $$('[data-range]').forEach((x) => x.classList.toggle('active', x === b));
    loadTrafficData();
  }));
  on('#traffic-limit-form', 'submit', async (e) => {
    e.preventDefault();
    if (!trafficLanding) { toast('请先选择落地', 'err'); return; }
    const gb = Number($('#limit-bytes-gb').value) || 0;
    if (gb < 0) { toast('流量上限不能为负', 'err'); return; }
    try {
      await apiPost('/api/v1/landings/' + trafficLanding + '/traffic', {
        limit_bytes: Math.round(gb * 1073741824),
        limit_mode: gb > 0 ? $('#limit-mode').value : 'total',
        speed_limit_mbps: Number($('#limit-speed').value) || 0,
      });
      toast('流量限制已保存', 'ok'); loadTrafficData();
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
  const reset = async (scope) => {
    if (!trafficLanding) { toast('请先选择落地', 'err'); return; }
    const ok = await confirmModal('重置用量', '<p>确定重置' + (scope === 'month' ? '当月' : '累计') + '用量吗？重置后计数从 0 开始算。</p>', '重置');
    if (!ok) return;
    try { await apiPost('/api/v1/traffic/reset', { landing_index: trafficLanding, scope }); toast('用量已重置', 'ok'); loadTrafficData(); }
    catch (e) { toast('重置失败：' + e.message, 'err'); }
  };
  on('#traffic-reset-month', 'click', () => reset('month'));
  on('#traffic-reset-total', 'click', () => reset('total'));
}

/* ---------- 集中管理（v1.2.0） ---------- */
let centralRemotes = [];
let centralNodeStats = null; // v1.3.52：集中管理页顶部的节点统计（/api/v1/central/remotes 回的 node_stats）
let centralDetailId = null;
let centralDetailPage = 1;
let centralDetailLandings = [];
let centralDetailRelayAddr = ''; // 当前展开的远程面板的任意门地址（编辑任意门落地时预填）
let centralDetailLinksRemote = null; // 落地链接缓存属于哪台远程
let centralDetailLinksGroups = null; // 该远程的 /links 分组缓存（展开时才取）

// apiRaw：与 api() 一样走 fetch，但业务失败（ok=false）时不抛错、把整包返回，
// 调用方才能按 side 写清是主面板的问题还是远程面板的问题。
async function apiRaw(path, opts) {
  const res = await fetch(path, Object.assign({ headers: { 'Content-Type': 'application/json' } }, opts || {}));
  let data = null;
  try { data = await res.json(); } catch (e) { /* 非 JSON 响应 */ }
  if (res.status === 401) { showLogin(); throw new Error('登录已过期，请重新登录'); }
  if (!res.ok) throw new Error((data && data.error) || ('请求失败（HTTP ' + res.status + '）'));
  return data || {};
}
async function apiPostRaw(path, body) { return apiRaw(path, { method: 'POST', body: JSON.stringify(body || {}) }); }
// 代理操作失败时的文案：side=remote 是远程面板的问题，side=local 是主面板的问题。
function centralOpError(d) {
  if (!d) return '操作失败';
  if (d.ok === false) return '失败（' + (d.side === 'remote' ? '远程面板的问题' : '主面板的问题') + '）：' + (d.error || '操作失败');
  return d.error || '操作失败';
}
async function centralTokenGenerate(rotated) {
  try {
    const r = await apiPost('/api/v1/settings/central-token/generate', {});
    $('#central-token-value').textContent = r.token || '';
    const box = $('#central-token-once');
    if (box) { box.classList.remove('hidden'); box.hidden = false; }
    toast(rotated ? '令牌已轮换，旧令牌已失效' : '令牌已生成，记得复制保存', 'ok');
    loadSettingsPage();
  } catch (err) { toast('生成失败：' + err.message, 'err'); }
}
// ---------- 订阅内容三选一（v1.3.17，下拉版 v1.3.21） ----------
// 模式存在令牌记录上（通用订阅、每人链接、合并订阅各一条），/sub/ 服务时按
// 令牌过滤链接；与 ?mode=split|global 的分流模式无关（那个只改 Clash 规则
// 的 MATCH 走向，不改链接集合）。
// v1.3.21：三选一从 radio-card 改成 <select> 下拉；每个表单各记各的上次
// 选择（key 不同），改一边不再同步另一边；编辑弹窗不记键，只按记录预选。
const SUB_MODE_LABELS = { both: '全部', relay: '仅中转', direct: '仅直连' };
function subModeValid(v) { return (v === 'relay' || v === 'direct' || v === 'both') ? v : 'both'; }
function subModeLabel(v) { return SUB_MODE_LABELS[subModeValid(v)] || '全部'; }
function savedSubMode(key) {
  try { return subModeValid(localStorage.getItem(key) || 'both'); } catch (e) { return 'both'; }
}
// 一组三选一下拉的 HTML（name 是 select 的名字；key 是这个表单专用的
// localStorage 键，空串 = 不记上次选择；单落地小窗与编辑弹窗里动态生成，
// 静态表单直接写在 index.html 里，结构与这里一致）。
function subModeSelectHTML(name, key) {
  return '<label class="small">订阅内容：<select name="' + esc(name) + '"' + (key ? ' data-sub-mode-select data-sub-mode-key="' + esc(key) + '"' : '') + '>' +
    '<option value="both">全部</option>' +
    '<option value="relay">仅中转</option>' +
    '<option value="direct">仅直连</option>' +
    '</select></label>' +
    '<span class="small muted">全部：中转和直连的节点都进订阅；仅中转：只要走中转机的节点；仅直连：只要直连落地机的节点。</span>';
}
function subModeOf(name) {
  const s = document.querySelector('select[name="' + name + '"]');
  return subModeValid(s ? s.value : 'both');
}
// 把一个下拉同步成指定值。
function syncSubModeSelect(sel, value) {
  if (!sel) return;
  sel.value = subModeValid(value);
}
// 绑定一个下拉：改了就只记自己表单的键，不动其他表单。
function bindSubModeSelect(sel) {
  if (!sel || sel._subModeBound) return;
  sel._subModeBound = true;
  const key = sel.dataset.subModeKey || '';
  if (!key) return;
  sel.addEventListener('change', () => {
    try { localStorage.setItem(key, subModeValid(sel.value)); } catch (e) {}
  });
}
function initSubMode() {
  $$('select[data-sub-mode-select]').forEach((sel) => {
    bindSubModeSelect(sel);
    syncSubModeSelect(sel, savedSubMode(sel.dataset.subModeKey || ''));
  });
}
/* ---------- 订阅到期时间（v1.3.20） ---------- */
// browserTimeZone: 浏览器所在 IANA 时区（v1.3.32：选日期时随请求发给后端，
// 后端按该时区算当天结束；拿不到时返回 ''，后端回退服务器本地时间）。
function browserTimeZone() {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ''; } catch (e) { return ''; }
}
// expiryDay: RFC3339 -> 浏览器本地日期 YYYY-MM-DD（v1.3.32：以前直接取
// UTC 字符串前 10 位，UTC− 用户选 12 月 1 日存的是 2026-12-02T04:59:59Z，
// 显示会跳到 12 月 2 日；现在按浏览器本地年月日显示）。
function expiryDay(expiresAt) {
  const d = new Date(expiresAt);
  if (isNaN(d.getTime())) return '';
  const m = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  return d.getFullYear() + '-' + m + '-' + day;
}
// fmtExpiry: "永久" / "2027-02-17 到期" / "已过期（2026-11-19）"。
function fmtExpiry(expiresAt) {
  if (!expiresAt) return '永久';
  const d = new Date(expiresAt);
  if (isNaN(d.getTime())) return '永久';
  const day = expiryDay(expiresAt);
  return d.getTime() < Date.now() ? '已过期（' + day + '）' : day + ' 到期';
}
// expiryBadge: 列表行里的到期徽章（永久灰、30 天内黄、7 天内与已过期红）。
function expiryBadge(expiresAt) {
  if (!expiresAt) return '<span class="badge grey">永久</span>';
  const d = new Date(expiresAt);
  if (isNaN(d.getTime())) return '<span class="badge grey">永久</span>';
  const day = expiryDay(expiresAt);
  if (d.getTime() < Date.now()) return '<span class="badge red">已过期 ' + esc(day) + '</span>';
  const days = Math.ceil((d.getTime() - Date.now()) / 86400000);
  const cls = days <= 7 ? 'red' : days <= 30 ? 'yellow' : 'green';
  return '<span class="badge ' + cls + '">' + esc(day) + ' 到期</span>';
}
// dateInputOfExpiry: RFC3339 -> date input 的 YYYY-MM-DD（空 = 永久）。
function dateInputOfExpiry(expiresAt) {
  if (!expiresAt) return '';
  const d = new Date(expiresAt);
  return isNaN(d.getTime()) ? '' : expiryDay(expiresAt);
}
/* ---------- 列表搜索（v1.3.20） ---------- */
// matchSubText: 不区分大小写，任一字段包含关键词即命中；关键词为空全过。
function matchSubText(q, fields) {
  if (!q) return true;
  const needle = String(q).toLowerCase();
  return fields.some((f) => String(f || '').toLowerCase().includes(needle));
}
// wireListSearch: 搜索框只绑一次（dataset.wired 守卫，多次调 loadX 也不会叠加）。
function wireListSearch(sel, onInput) {
  const el = $(sel);
  if (!el || el.dataset.wired) return;
  el.dataset.wired = '1';
  el.addEventListener('input', () => onInput(el.value.trim()));
}
/* ---------- 面板 Logo（v1.3.20） ---------- */
// initLogos: 登录页/侧边栏/设置页三处 Logo 的加载成败回退——/logo 200 就显示
// 图片、404 就显示默认样式。登录页在登录前就要能显示，所以启动时就绑。
function initLogos() {
  [['#login-logo', '#login-logo-default'], ['#brand-logo', '#brand-logo-default'], ['#logo-preview', '#logo-preview-default']].forEach(([imgSel, defSel]) => {
    const img = $(imgSel), def = $(defSel);
    if (!img || !def) return;
    img.addEventListener('error', () => { img.classList.add('hidden'); def.classList.remove('hidden'); });
    img.addEventListener('load', () => { img.classList.remove('hidden'); def.classList.add('hidden'); });
    if (img.complete && img.naturalWidth > 0) { img.classList.remove('hidden'); def.classList.add('hidden'); }
  });
}
// refreshLogoViews: 设置页回填后按有没有 Logo 切换三处显示（有 Logo 时带
// 时间戳重新拉一次，换图后立即生效，不用等缓存过期）。
function refreshLogoViews(hasLogo) {
  [['#login-logo', '#login-logo-default'], ['#brand-logo', '#brand-logo-default'], ['#logo-preview', '#logo-preview-default']].forEach(([imgSel, defSel]) => {
    const img = $(imgSel), def = $(defSel);
    if (!img || !def) return;
    if (hasLogo) { img.src = '/logo?' + Date.now(); img.classList.remove('hidden'); def.classList.add('hidden'); }
    else { img.classList.add('hidden'); def.classList.remove('hidden'); }
  });
}
// 上传 Logo：只接受图片（后端按文件头嗅探，超 2MB 后端直接拒）。
async function uploadLogo() {
  const input = $('#logo-input');
  const file = input && input.files && input.files[0];
  if (!file) { toast('请先选一张图片', 'err'); return; }
  const fd = new FormData();
  fd.append('logo', file);
  try {
    const r = await apiPostForm('/api/v1/settings/logo', fd);
    const msg = $('#logo-msg');
    if (msg) msg.textContent = '已上传，圆形显示在面板左上角与登录页。';
    toast('Logo 已上传', 'ok');
    refreshLogoViews(!!(r && r.has_logo));
    input.value = '';
  } catch (err) { toast('上传失败：' + err.message, 'err'); }
}
// 删除 Logo：删文件、清 MIME，之后显示默认样式。
async function deleteLogo() {
  const ok = await confirmModal('删除面板 Logo', '<p>删掉后面板左上角与登录页恢复默认样式。确定吗？</p>', '删除 Logo', true);
  if (!ok) return;
  try {
    await api('/api/v1/settings/logo', { method: 'DELETE' });
    const msg = $('#logo-msg');
    if (msg) msg.textContent = '已删除，恢复默认样式。';
    toast('Logo 已删除', 'ok');
    refreshLogoViews(false);
    const input = $('#logo-input');
    if (input) input.value = '';
  } catch (err) { toast('删除失败：' + err.message, 'err'); }
}
// ---------- 订阅链接 ----------
// 订阅链接默认不带 mode 参数（服务端缺省就是分流 MATCH,DIRECT）；只有在
// 模式选择里显式选了分流/全局，才在复制的链接与二维码里带上 ?mode=。
// ?mode= 只在一种情况下才需要：规则模式下想全走代理、又保留 TikTok/社交/
// 流媒体的分类分流（客户端切全局模式会忽略全部 rules，分类分流就没了）。
function subLinkWithMode(url, format) {
  const sel = $('#sub-mode');
  const mode = (sel && sel.value) || '';
  const q = [];
  if (format) q.push('format=' + format);
  if (mode) q.push('mode=' + mode);
  if (!q.length) return url;
  return url + '?' + q.join('&');
}
// 订阅链接二维码弹窗（v1.3.21）：设置页通用订阅与订阅用户的二维码以前是卡片内
// 行内展开，现在统一走弹窗。弹窗打开时改模式（内容三选一或 ?mode= 分流/全局）
// 也要同步更新：改模式的 change 监听里，弹窗开着就重新拉一次写进弹窗。
// 图片的复制/下载走页面已有的 data-copy-img / data-download-img 事件委托。
// 二维码编的是 Clash 订阅链接（?format=clash，v1.3.23 起显式指定，不再靠
// UA 嗅探：多数用户是 Clash 系，扫码导入直接拿 YAML）。
let openQrKind = null; // 当前打开的设置页二维码弹窗：'generic' | 'user' | null
async function showSubQrModal(kind) {
  openQrKind = kind;
  const m = openModal('<h2>订阅链接二维码</h2><p class="muted small">扫码后导入的是 Clash 订阅链接，直接进 Clash 系客户端；默认不带模式参数，选了全局才带 ?mode=global；换模式后二维码跟着重新生成。</p>' +
    '<div class="qr-wrap"><figure data-qr-modal-figure><img data-qr-modal-img alt="订阅链接二维码"><figcaption>订阅链接二维码</figcaption>' +
    '<div class="btn-row"><button class="btn secondary sm" type="button" data-copy-img>复制图片</button>' +
    '<button class="btn secondary sm" type="button" data-download-img="订阅链接-二维码.png">下载图片</button></div></figure></div>');
  if (!m) { openQrKind = null; return; }
  await refreshSubQrModal(kind);
}
async function refreshSubQrModal(kind) {
  const img = document.querySelector('[data-qr-modal-img]');
  const fig = document.querySelector('[data-qr-modal-figure]');
  if (!img) { openQrKind = null; return; } // 弹窗已经关了（或被别的弹窗顶掉），不用再拉
  const urlEl = kind === 'user' ? $('#sub-user-url-value') : $('#sub-url-value');
  const url = subLinkWithMode(urlEl ? urlEl.textContent : '', 'clash');
  try {
    const r = await apiPost('/api/v1/subscription/qr', { url });
    if (!r.qr) throw new Error('后端没有返回二维码');
    img.src = r.qr;
    if (fig) fig.dataset.qr = r.qr;
  } catch (err) { toast('二维码生成失败：' + err.message, 'err'); }
}
// ---------- 重新复制订阅链接（v1.3.7） ----------
// 取回一条订阅链接：kind=generic|user|merged，id 只在 user/merged 时传。
// 有密文返回 {url, legacy:false}；旧链接返回 {legacy:true, message}。
async function fetchSubLink(kind, id) {
  const q = new URLSearchParams({ kind });
  if (id) q.set('id', id);
  return await api('/api/v1/subscriptions/link?' + q.toString());
}
// 旧链接提示：没有密文，无法恢复；点轮换就调传入的轮换函数（轮换函数自己
// 负责把新链接显示出来），新链接随时能复制。
async function legacySubPrompt(onRotate) {
  const ok = await confirmModal('旧链接无法恢复',
    '<p>这条是 1.3.7 之前生成的旧链接，面板里没有存它的密文，无法恢复。点“轮换生成新链接”，旧链接立即失效，新链接随时能复制。</p>',
    '轮换生成新链接', true);
  if (!ok) return false;
  await onRotate();
  return true;
}
// 取回通用订阅链接并显示在设置页：有密文就显示链接与复制按钮，
// 旧链接显示轮换提示（取不到就留着状态文案，不报错刷屏）。
async function loadGenericSubLink() {
  try {
    const r = await fetchSubLink('generic');
    if (r.legacy) {
      $('#sub-status').textContent = '已生成订阅链接：这条是旧链接，面板里没有存它的密文，无法恢复，点“轮换订阅链接”生成一条新的。';
      const box = $('#sub-once');
      if (box) { box.classList.add('hidden'); box.hidden = true; }
      return;
    }
    $('#sub-url-value').textContent = r.url || '';
    const box = $('#sub-once');
    if (box) { box.classList.remove('hidden'); box.hidden = false; }
  } catch (e) { /* 取不到就留着状态文案 */ }
}
// 单面板订阅令牌生成/轮换：链接显示在设置页的订阅卡片里，之后随时能复制。
// 生成方式按卡片上三选一的当前选择存进令牌（轮换也一样，表单的显式选择）。
async function subTokenGenerate(rotated) {
  try {
    const r = await apiPost('/api/v1/settings/subscription/generate', { mode: subModeOf('sub-token-mode') });
    $('#sub-url-value').textContent = r.url || '';
    const box = $('#sub-once');
    if (box) { box.classList.remove('hidden'); box.hidden = false; }
    toast(rotated ? '订阅链接已轮换，旧链接已失效' : '订阅链接已生成，随时能复制', 'ok');
    loadSettingsPage();
  } catch (err) { toast('生成失败：' + err.message, 'err'); }
}
// ---------- 订阅对外域名（v1.3.2） ----------
// currentSubPath: 当前订阅路径段（settingsCache 里回填的 sub_path，
// 空 = 默认 sub）。v1.3.20：对外域名、Worker 示例、链接换基地址都走它，
// 不再写死 'sub'。
function currentSubPath() {
  const s = settingsCache && settingsCache.subscription;
  return (s && s.sub_path) || 'sub';
}
// 保存订阅到期时间（v1.3.20）：勾永久 = 空串（永不过期）；填日期 =
// 当天 23:59:59 到期。只改到期时间，不换令牌，链接不变。
async function saveSubExpiry() {
  const perm = $('#sub-expiry-permanent');
  const dateEl = $('#sub-expiry-date');
  const expiresAt = (perm && perm.checked) ? '' : (dateEl ? dateEl.value : '');
  try {
    const r = await apiPost('/api/v1/settings/subscription/expiry', { expires_at: expiresAt, timezone: browserTimeZone() });
    const status = $('#sub-expiry-status');
    if (status) status.textContent = r.expires_at ? '当前：' + fmtExpiry(r.expires_at) : '当前：永久';
    if (dateEl) dateEl.disabled = !r.expires_at;
    toast(r.expires_at ? '到期时间已保存：' + fmtExpiry(r.expires_at) : '到期时间已保存：永久', 'ok');
    loadSettingsPage();
  } catch (err) { toast('保存失败：' + err.message, 'err'); }
}
// 登录页公告（v1.3.21）：多行纯文本，留空 = 清掉，登录页不再显示。
async function saveLoginNotice() {
  const el = $('#login-notice-text');
  const status = $('#login-notice-status');
  const notice = el ? el.value.trim() : '';
  try {
    const r = await apiPost('/api/v1/settings/login-notice', { notice });
    if (status) status.textContent = r.login_notice ? '已保存，登录页会显示这段公告。' : '已清空，登录页不再显示公告。';
    toast('登录页公告已保存', 'ok');
  } catch (err) { toast('保存失败：' + err.message, 'err'); }
}
// 登录页签名（v1.3.21）：单行文本，留空 = 清掉，登录页副标题回落默认文案。
async function saveLoginSignature() {
  const el = $('#login-signature');
  const status = $('#login-signature-status');
  const signature = el ? el.value.trim() : '';
  try {
    const r = await apiPost('/api/v1/settings/login-signature', { signature });
    if (status) status.textContent = r.login_signature ? '已保存，登录页副标题会显示这句签名。' : '已清空，登录页副标题恢复默认。';
    toast('登录页签名已保存', 'ok');
  } catch (err) { toast('保存失败：' + err.message, 'err'); }
}
// 保存订阅路径（v1.3.20）：改路径 = 旧订阅链接全部失效 + 面板重启 +
// 重新登录 + 客户端重新导入，先弹确认框说清楚再调接口。
async function saveSubPath() {
  const input = $('#sub-path');
  const path = input ? input.value.trim() : '';
  const target = path || 'sub';
  const cur = currentSubPath();
  if (target === cur) { toast('订阅路径没变', 'ok'); return; }
  const ok = await confirmModal('改订阅路径',
    '<p>订阅路径将从 <b>/' + esc(cur) + '/</b> 改成 <b>/' + esc(target) + '/</b>。</p>' +
    '<p>改完之后：<b>旧订阅链接全部失效</b>，客户端须重新导入新链接；面板会重启一次，要重新登录；Worker 反代代码也要按新路径重新生成、重新部署。确定吗？</p>',
    '改路径并重启', true);
  if (!ok) return;
  try {
    const r = await apiPost('/api/v1/settings/sub-path', { path: target });
    toast(r.msg || '订阅路径已改，面板正在重启', 'ok');
    setTimeout(() => location.reload(), 1200);
  } catch (err) { toast('保存失败：' + err.message, 'err'); }
}
// 保存订阅对外域名：空 = 清除（恢复成面板自己的地址）；保存后把页面上
// 正在显示的一次性订阅链接就地换成新域名（只换地址部分，令牌与参数不动），
// 二维码收起来（里面还是旧链接，重新点一次就好）。
async function saveSubPublicDomain() {
  const input = $('#sub-public-domain');
  if (!input) return;
  const domain = input.value.trim();
  try {
    const r = await apiPost('/api/v1/settings/subscription-domain', { domain });
    input.value = r.domain || '';
    rebaseShownSubUrls(r.domain || '');
    const gdn = $('#gateway-domain-now');
    if (gdn) gdn.textContent = r.domain || '未配置';
    toast(r.domain ? '订阅对外域名已保存，订阅链接改用 https://' + r.domain + '/' + currentSubPath() + '/…' : '订阅对外域名已清除，订阅链接恢复成面板自己的地址', 'ok');
    loadSettingsPage();
  } catch (err) { toast('保存失败：' + err.message, 'err'); }
}
// 保存订阅文件名称（v1.3.22）：空 = 清除（回落面板名）；非法字符服务端
// 返回 400，这里如实提示服务端的原话。
async function saveSubFileName() {
  const input = $('#sub-file-name');
  if (!input) return;
  const name = input.value.trim();
  try {
    const r = await apiPost('/api/v1/settings/subscription-filename', { name });
    input.value = r.file_name || '';
    toast(r.file_name ? '订阅文件名称已保存：' + r.file_name + '.yaml/.txt' : '订阅文件名称已清除，改回用面板名', 'ok');
  } catch (err) { toast('保存失败：' + err.message, 'err'); }
}
// 把页面上正在显示的一次性订阅链接换成新域名（只换地址部分）：
// 有域名 → https://域名 + 原路径；没域名 → 当前打开面板的地址 + 原路径。
// 路径段走当前订阅路径（v1.3.20 可自定义），不是写死的 /sub/。
function rebaseShownSubUrls(domain) {
  const base = domain ? 'https://' + domain : location.origin;
  const seg = currentSubPath();
  ['#sub-url-value', '#sub-user-url-value'].forEach((sel) => {
    const el = $(sel);
    if (!el || !el.textContent) return;
    try {
      const u = new URL(el.textContent.trim());
      if (!u.pathname.startsWith('/' + seg + '/')) return;
      el.textContent = base + u.pathname + u.search + u.hash;
    } catch (e) { /* 不是合法 URL 就不动 */ }
  });
}
// ---------- 订阅用户：一人一条（v1.3.0） ----------
// 列表只含名字与生成时间，不含令牌；每行都有“复制”按钮，随时能重新拿链接。
// v1.3.20：列表搜索（名字/备注/勾选摘要）+ 分页（每页 20 条）+ 到期徽章 +
// 每行“二维码”按钮（扫码导入，编的是这条订阅的链接）。
let subUserPage = 1, subUserSearch = '';
// ---------- 按订阅限流量（v1.3.25） ----------
// 限流表单共用：data-tl-gb（上限 GB，0=不限流）、data-tl-mode（total|monthly）、
// data-tl-reset（monthly|anniversary|custom）、data-tl-day（custom 时 1–31）。
// scope 是包含这组字段的容器（卡片、行或弹窗）。
function wireTrafficLimitUI(scope) {
  if (!scope || !scope.querySelector) return;
  const mode = scope.querySelector('[data-tl-mode]');
  const reset = scope.querySelector('[data-tl-reset]');
  const day = scope.querySelector('[data-tl-day]');
  const sync = () => {
    const monthly = mode && mode.value === 'monthly';
    if (reset) reset.disabled = !monthly;
    if (day) day.disabled = !(monthly && reset && reset.value === 'custom');
  };
  if (mode) mode.addEventListener('change', sync);
  if (reset) reset.addEventListener('change', sync);
  sync();
}
// 读一组限流字段：返回 {limit_mb, limit_mode, reset_mode, reset_day}。
// limit_mb 是 MB（GB×1024），0 = 不限流。
function readTrafficLimit(scope) {
  if (!scope || !scope.querySelector) return { limit_mb: 0, limit_mode: 'total', reset_mode: 'monthly', reset_day: 1 };
  const gb = Number((scope.querySelector('[data-tl-gb]') || {}).value) || 0;
  const mode = ((scope.querySelector('[data-tl-mode]') || {}).value) || 'total';
  const reset = ((scope.querySelector('[data-tl-reset]') || {}).value) || 'monthly';
  const day = Number((scope.querySelector('[data-tl-day]') || {}).value) || 1;
  return { limit_mb: Math.max(0, Math.round(gb * 1024)), limit_mode: mode, reset_mode: reset, reset_day: Math.min(31, Math.max(1, day)) };
}
// 限流表单 HTML（编辑弹窗用，动态生成；u 是列表行对象，预填用）。
function trafficLimitHTML(u) {
  const gb = Math.round((u.traffic_limit_mb || 0) / 1024);
  const mode = u.traffic_limit_mode || 'total';
  const reset = u.reset_mode || 'monthly';
  const day = u.reset_day || 1;
  return '<div class="form-row"><label class="small">流量上限（GB，0 = 不限流）：<input data-tl-gb type="number" min="0" step="1" value="' + gb + '" style="width:90px"></label>' +
    '<label class="small">算法：<select data-tl-mode style="width:130px"><option value="total"' + (mode !== 'monthly' ? ' selected' : '') + '>累计（整个生命周期）</option><option value="monthly"' + (mode === 'monthly' ? ' selected' : '') + '>按月（每月重置）</option></select></label>' +
    '<label class="small">重置：<select data-tl-reset style="width:130px"><option value="monthly"' + (reset !== 'anniversary' && reset !== 'custom' ? ' selected' : '') + '>每月 1 号</option><option value="anniversary"' + (reset === 'anniversary' ? ' selected' : '') + '>跟随生成日</option><option value="custom"' + (reset === 'custom' ? ' selected' : '') + '>自定义每月几号</option></select></label>' +
    '<label class="small">几号：<input data-tl-day type="number" min="1" max="31" value="' + day + '" style="width:60px"></label></div>' +
    '<p class="muted small">开了限流才给这条订阅建独立转发（独立端口 → 同一落地），不开限流的继续走共享端口；超量时 80% 发 Telegram 提醒、100% 自动停用。统计是上行 + 下行合计、按 L3 字节计，分钟级采样。</p>';
}
// 重置模式写成人话。
function resetModeLabel(m) {
  return m === 'anniversary' ? '跟随生成日' : m === 'custom' ? '自定义每月几号' : '每月 1 号';
}
// 列表行里的用量文本：用量/上限、重置日、超量标红、转发未建提醒。
function userTrafficText(u) {
  const mb = u.traffic_limit_mb || 0;
  // v1.4.3：停用徽标不限流用户也要显示（手动停用的用户往往没开限流），
  // 所以先算出来；原因空（老记录）fallback 到流量用完。
  const disabledTag = u.traffic_disabled ? ' <span class="badge red">已停用（' + esc(disabledReasonText(u.disabled_reason) || '流量用完') + '）</span>' : '';
  if (!mb) return '流量：不限流（走共享端口）' + disabledTag;
  const limit = mb * 1048576;
  const used = u.traffic_used || 0;
  const pct = limit > 0 ? Math.round(used / limit * 100) : 0;
  const mode = u.traffic_limit_mode === 'monthly' ? '按月' : '累计';
  const reset = u.traffic_limit_mode === 'monthly'
    ? '，重置：' + resetModeLabel(u.reset_mode) + (u.next_reset_at ? '（下次 ' + fmtDate(u.next_reset_at) + '）' : '')
    : '';
  const usedTxt = u.has_used ? fmtBytes(used) + ' / ' + fmtBytes(limit) + '（' + pct + '%）' : '上限 ' + fmtBytes(limit) + '（用量未同步）';
  let tag = disabledTag;
  // 原来是 if/else if 二选一（停用徽标与 80% 徽标不同时出现），保持。
  if (!u.traffic_disabled && pct >= 80) tag += ' <span class="badge">已用 ' + pct + '%</span>';
  if (u.forward_count === 0) tag += ' <span class="badge red">转发未建，重新保存一次</span>';
  // v1.3.27：限流版本警告与采样失败警告（有值时用量不可信，另起一行红字提示）。
  let warn = '';
  if (u.limit_warn) warn += '<br><span class="warn">' + esc(u.limit_warn) + '</span>';
  if (u.traffic_warn) warn += '<br><span class="warn">用量可能不准：' + esc(u.traffic_warn) + '</span>';
  return '流量：' + mode + ' ' + usedTxt + reset + tag + warn;
}
// 手动恢复一条停用的订阅（订阅用户或合并订阅共用）：
// kind 是 'users' 或 'merged'，恢复后刷新对应列表。
// v1.4.3：手动停用的恢复不看流量，确认文案按原因区分。
async function resumeSubTraffic(kind, id, reason) {
  const manual = reason === '手动停用' || reason === 'manual';
  const ok = await confirmModal('恢复订阅', manual ? '<p>确定恢复这条订阅吗？恢复后节点 60 秒内把这个人加回去。</p>' : '<p>流量回到上限以下才能恢复（已调高上限或到了下个周期）。确定恢复这条订阅的转发吗？</p>', '恢复', true);
  if (!ok) return;
  try {
    await apiPost('/api/v1/subscriptions/' + kind + '/' + encodeURIComponent(id) + '/resume', {});
    toast('已恢复，转发已加回去', 'ok');
    if (kind === 'merged') loadMergedSubsList(); else loadSubUsers();
  } catch (e) { toast('恢复失败：' + e.message, 'err'); }
}

// 手动停用一条订阅（v1.4.3，订阅用户或合并订阅共用）：停用后节点
// 60 秒内拿掉这个人、新连接被拒，订阅也取不到节点；恢复用行上的
// “恢复”按钮。kind 是 'users' 或 'merged'，停用后刷新对应列表。
async function disableSubTraffic(kind, id) {
  const ok = await confirmModal('停用订阅', '<p>停用后这个人的节点 60 秒内拿掉他，新连接被拒，订阅也取不到节点。确定停用吗？</p>', '停用', true);
  if (!ok) return;
  try {
    await apiPost('/api/v1/subscriptions/' + kind + '/' + encodeURIComponent(id) + '/disable', {});
    toast('已停用', 'ok');
    if (kind === 'merged') loadMergedSubsList(); else loadSubUsers();
  } catch (e) { toast('停用失败：' + e.message, 'err'); }
}

// 用户“操作”菜单（v1.4.4，切片 4）：用户行上只留一个“操作”按钮，
// 点开弹窗列出该用户的全部操作：编辑、复制、二维码、轮换链接、
// 发到节点、轮换 UUID、停用／恢复、撤销。弹窗里的按钮沿用原来行上
// 按钮的 data-* 属性（选择器不变，旧测试只改成“先点操作菜单再点
// 对应项”）。点某一项先关菜单弹窗，再走原来的操作流程。
function openUserOpsMenu(kind, u) {
  if (!u) return;
  const id = u.id;
  const name = u.name || u.summary || id;
  const disabled = !!u.traffic_disabled;
  const item = (attr, label, danger) => '<div style="margin-bottom:8px"><button class="btn ' + (danger ? 'danger' : 'secondary') + '" type="button" style="width:100%;text-align:left" ' + attr + '>' + label + '</button></div>';
  let html = '<h2>操作：' + esc(name) + '</h2>';
  if (kind === 'user') {
    html += item('data-sub-user-edit="' + esc(id) + '"', '编辑') +
      (disabled ? item('data-sub-user-resume="' + esc(id) + '"', '恢复') : item('data-sub-user-disable="' + esc(id) + '"', '停用')) +
      item('data-sub-user-copy="' + esc(id) + '"', '复制订阅链接') +
      item('data-sub-user-qr="' + esc(id) + '"', '二维码') +
      item('data-sub-user-rotate="' + esc(id) + '"', '轮换链接') +
      item('data-sub-user-nodes="' + esc(id) + '"', '发到节点') +
      item('data-sub-user-rotate-uuid="' + esc(id) + '"', '轮换 UUID') +
      item('data-sub-user-revoke="' + esc(id) + '"', '撤销', true);
  } else {
    html += item('data-sub-edit-merged="' + esc(id) + '"', '编辑') +
      (disabled ? item('data-sub-resume-merged="' + esc(id) + '"', '恢复') : item('data-sub-disable-merged="' + esc(id) + '"', '停用')) +
      item('data-sub-copy-merged="' + esc(id) + '" data-sub-mode-merged="' + esc(u.mode || '') + '" data-sub-name-merged="' + esc(u.name || '') + '"', '复制订阅链接') +
      item('data-sub-qr-merged="' + esc(id) + '" data-sub-mode-merged="' + esc(u.mode || '') + '" data-sub-name-merged="' + esc(u.name || '') + '"', '二维码') +
      item('data-sub-rotate-merged="' + esc(id) + '" data-sub-name-merged="' + esc(u.name || '') + '"', '轮换链接') +
      item('data-sub-nodes-merged="' + esc(id) + '"', '发到节点') +
      item('data-sub-rotate-uuid-merged="' + esc(id) + '" data-sub-name-merged="' + esc(u.name || '') + '"', '轮换 UUID') +
      item('data-sub-revoke-merged="' + esc(id) + '"', '撤销', true);
  }
  html += '<div class="btn-row"><button class="btn secondary" id="user-ops-close" type="button">关闭</button></div>';
  const m = openModal(html);
  if (!m) return;
  onIn(m, '#user-ops-close', 'click', closeModal);
  const act = (sel, fn) => { const b = m.querySelector(sel); if (b) b.addEventListener('click', () => { closeModal(); fn(); }); };
  if (kind === 'user') {
    act('[data-sub-user-edit]', () => openSubEditModal({ kind: 'user', id: u.id, name: u.name, note: u.note, selection: u.selection || [], mode: u.mode, expires_at: u.expires_at, traffic_limit_mb: u.traffic_limit_mb, traffic_limit_mode: u.traffic_limit_mode, reset_mode: u.reset_mode, reset_day: u.reset_day, group_id: u.group_id || '' }));
    if (disabled) act('[data-sub-user-resume]', () => resumeSubTraffic('users', id, u.disabled_reason));
    else act('[data-sub-user-disable]', () => disableSubTraffic('users', id));
    act('[data-sub-user-copy]', async () => {
      try {
        const r = await fetchSubLink('user', id);
        if (r.legacy) { await legacySubPrompt(() => rotateSubUser(id)); return; }
        $('#sub-user-url-value').textContent = r.url || '';
        const box = $('#sub-user-once');
        if (box) { box.classList.remove('hidden'); box.hidden = false; }
        copyText(subLinkWithMode(r.url || '', ''), null);
      } catch (e) { toast('复制失败：' + e.message, 'err'); }
    });
    act('[data-sub-user-qr]', async () => {
      try {
        const r = await fetchSubLink('user', id);
        if (r.legacy) { await legacySubPrompt(() => rotateSubUser(id)); return; }
        $('#sub-user-url-value').textContent = r.url || '';
        const box = $('#sub-user-once');
        if (box) { box.classList.remove('hidden'); box.hidden = false; }
        await showSubQrModal('user');
      } catch (e) { toast('二维码失败：' + e.message, 'err'); }
    });
    act('[data-sub-user-rotate]', async () => {
      const ok = await confirmModal('轮换订阅用户链接', '<p>轮换后这个人的旧订阅链接立即失效，客户端里要换成新链接。确定吗？</p>', '轮换', true);
      if (!ok) return;
      try { await rotateSubUser(id); }
      catch (e) { toast('轮换失败：' + e.message, 'err'); }
    });
    act('[data-sub-user-nodes]', () => openNodeAssignModal('subuser', id, u.name));
    act('[data-sub-user-rotate-uuid]', () => rotateNodeUUID('subuser', id, u.name));
    act('[data-sub-user-revoke]', async () => {
      const ok = await confirmModal('撤销订阅用户', '<p>撤销后这个人的订阅链接立即失效，其他人照常。确定吗？</p>', '撤销', true);
      if (!ok) return;
      try { await api('/api/v1/subscriptions/users/' + encodeURIComponent(id), { method: 'DELETE' }); toast('已撤销', 'ok'); loadSubUsers(); }
      catch (e) { toast('撤销失败：' + e.message, 'err'); }
    });
  } else {
    act('[data-sub-edit-merged]', () => openSubEditModal({ kind: 'merged', id: u.id, name: u.name, selection: u.selection || [], mode: u.mode, expires_at: u.expires_at, traffic_limit_mb: u.traffic_limit_mb, traffic_limit_mode: u.traffic_limit_mode, reset_mode: u.reset_mode, reset_day: u.reset_day, group_id: u.group_id || '' }));
    if (disabled) act('[data-sub-resume-merged]', () => resumeSubTraffic('merged', id, u.disabled_reason));
    else act('[data-sub-disable-merged]', () => disableSubTraffic('merged', id));
    act('[data-sub-copy-merged]', async () => {
      try {
        const r = await fetchSubLink('merged', id);
        if (r.legacy) { await legacySubPrompt(() => rotateMergedSub(id, u.name || undefined)); return; }
        openMergedSubModal(r.url || '', { title: '订阅链接', name: u.name || undefined, mergedId: id, mode: u.mode || undefined, expires_at: r.expires_at });
      } catch (e) { toast('复制失败：' + e.message, 'err'); }
    });
    act('[data-sub-qr-merged]', async () => {
      try {
        const r = await fetchSubLink('merged', id);
        if (r.legacy) { await legacySubPrompt(() => rotateMergedSub(id, u.name || undefined)); return; }
        const m2 = openMergedSubModal(r.url || '', { title: '订阅链接', name: u.name || undefined, mergedId: id, mode: u.mode || undefined, expires_at: r.expires_at });
        if (m2) { const qb = m2.querySelector('[data-sub-qr]'); if (qb) qb.click(); }
      } catch (e) { toast('二维码失败：' + e.message, 'err'); }
    });
    act('[data-sub-rotate-merged]', async () => {
      const ok = await confirmModal('轮换订阅链接', '<p>轮换后旧订阅链接立即失效，客户端里已导入的订阅要换成新链接。确定吗？</p>', '轮换订阅链接', true);
      if (!ok) return;
      try { await rotateMergedSub(id, u.name || undefined); }
      catch (e) { toast('轮换失败：' + e.message, 'err'); }
    });
    act('[data-sub-nodes-merged]', () => openNodeAssignModal('merged', id, u.name || u.summary));
    act('[data-sub-rotate-uuid-merged]', () => rotateNodeUUID('merged', id, u.name || undefined));
    act('[data-sub-revoke-merged]', async () => {
      const ok = await confirmModal('撤销合并订阅', '<p>撤销后这条订阅链接立即失效。确定吗？</p>', '撤销', true);
      if (!ok) return;
      try { await api('/api/v1/subscriptions/merged/' + encodeURIComponent(id), { method: 'DELETE' }); toast('已撤销', 'ok'); loadMergedSubsList(); }
      catch (e) { toast('撤销失败：' + e.message, 'err'); }
    });
  }
}

async function loadSubUsers() {
  const list = $('#sub-user-list');
  if (!list) return;
  try {
    const r = await api('/api/v1/subscriptions/users');
    const users = r.users || [];
    if (!users.length) { list.innerHTML = '<p class="muted small">还没有订阅用户：上面填名字点“新建订阅用户”，每人一条独立链接。</p>'; return; }
    const filtered = users.filter((u) => matchSubText(subUserSearch, [u.name, u.note, u.summary, u.kind]));
    if (!filtered.length) { list.innerHTML = '<p class="muted small">没有匹配的订阅用户，换个关键词试试。</p>'; return; }
    const pg = paginate(filtered, subUserPage);
    subUserPage = pg.cur;
    list.innerHTML = '<h3>订阅用户</h3>' + pg.slice.map((u) => '<div class="kv"><span>' + esc(u.name || '') + (u.group_name ? ' <span class="badge">分组：' + esc(u.group_name) + '</span>' : '') + (u.note ? ' <span class="small muted">（' + esc(u.note) + '）</span>' : '') + '<br><span class="small muted">' + esc(u.kind === 'merged' ? '合并订阅：' + (u.summary || '') : '单面板全量订阅') + ' · 内容' + esc(subModeLabel(u.mode)) + ' · ' + expiryBadge(u.expires_at) + ' · 生成于 ' + esc(fmtDate(u.created_at)) + '</span><br><span class="small">' + userTrafficText(u) + '</span></span><span><button class="btn secondary sm" type="button" data-sub-user-menu="' + esc(u.id) + '">操作</button></span></div>').join('') +
      pagerHtml(pg.total, pg.cur, pg.totalPages, 'subuser') +
      '<p class="muted small">编辑只改勾选与内容，链接不变；撤销只影响那个人，其他人的链接照常；轮换后旧链接立即失效。</p>';
    if (pg.totalPages > 1) wirePager(list, 'subuser', pg.cur, pg.totalPages, (p) => { subUserPage = p; loadSubUsers(); });
    $$('[data-sub-user-menu]', list).forEach((b) => b.addEventListener('click', () => {
      const u = users.find((x) => x.id === b.dataset.subUserMenu);
      if (!u) return;
      openUserOpsMenu('user', u);
    }));
  } catch (e) { list.innerHTML = '<div class="error-box">加载失败：' + esc(e.message) + '</div>'; }
}
// 轮换一个订阅用户的链接（不含确认框，确认由调用方做）：旧链接立即失效，
// 新链接显示在订阅用户卡片里，随时能复制。
async function rotateSubUser(id) {
  const r = await apiPost('/api/v1/subscriptions/users/' + encodeURIComponent(id) + '/rotate', {});
  $('#sub-user-url-value').textContent = r.url || '';
  const box = $('#sub-user-once');
  if (box) { box.classList.remove('hidden'); box.hidden = false; }
  toast('已轮换，旧链接已失效', 'ok'); loadSubUsers();
  return r;
}
// ---------- 节点用户指派与轮换 UUID（v1.4.2，切片 3；v1.4.4 重构） ----------
// “发到节点”弹窗：四级指派（全部／组／节点／入站）＋节点列表搜索。
// 级别存在用户记录上（BackendScope／BackendTargets，见 node_assign.go），
// 节点侧的成员门（UserIDs）仍按旧逻辑同步：勾上的节点把人加进去，
// 不勾的拿掉；nil（全部用户）节点不用动。
//   - 全部：所有后端节点，全部入站（scope=all）。
//   - 组：跟所在分组走，节点取分组的 BackendNodeIDs（scope=group，
//     targets=[分组id]；没分组或分组没配节点时提示，不让保存）。
//   - 节点：自选节点，全部入站（scope=node，targets=[节点id]）。
//   - 入站：自选节点＋入站，配置下发时 users[].inbounds 只含勾的 tag
//     （scope=inbound，targets=["节点id:入站tag"]；某节点一个入站
//     都没勾 = 这个人不去该节点）。
async function openNodeAssignModal(kind, id, name) {
  const key = kind + ':' + id;
  let nodes = [];
  try { const r = await api('/api/v1/nodes'); nodes = r.nodes || []; }
  catch (e) { toast('拉节点列表失败：' + e.message, 'err'); return; }
  if (!nodes.length) { toast('还没有节点，先在节点页建一个', 'err'); return; }
  let assign = { scope: 'all', targets: [], group_id: '', group_name: '', group_nodes: [] };
  try { assign = await api('/api/v1/users/' + encodeURIComponent(key) + '/backend-assignment'); } catch (e) { /* 老面板没这个接口时按全部算 */ }
  const states = [];
  for (const n of nodes) {
    try {
      const r = await api('/api/v1/nodes/' + encodeURIComponent(n.id) + '/users');
      const all = r.all_users === true;
      const ids = r.user_ids || null;
      const eff = (r.users || []).map((u) => u.user_id);
      let inbounds = [];
      try { const ri = await api('/api/v1/nodes/' + encodeURIComponent(n.id) + '/inbounds'); inbounds = (ri.inbounds || []).filter((x) => x.enabled).map((x) => x.inbound_tag); } catch (e) { inbounds = []; }
      states.push({ node: n, all, ids, eff, inbounds, checked: all || (ids && ids.indexOf(key) !== -1) });
    } catch (e) { states.push({ node: n, error: true, checked: false, inbounds: [] }); }
  }
  let level = assign.scope || 'all';
  if (['all', 'group', 'node', 'inbound'].indexOf(level) === -1) level = 'all';
  const selNodes = new Set();
  const selInbounds = {}; // nodeId -> Set(tag)
  states.forEach((st) => { if (st.checked) selNodes.add(st.node.id); });
  if (level === 'node') { (assign.targets || []).forEach((t) => { const nid = Number(t); if (nid) selNodes.add(nid); }); }
  if (level === 'inbound') {
    (assign.targets || []).forEach((t) => {
      const p = String(t).split(':');
      if (p.length !== 2) return;
      const nid = Number(p[0]);
      if (!nid) return;
      if (!selInbounds[nid]) selInbounds[nid] = new Set();
      selInbounds[nid].add(p[1]);
      selNodes.add(nid);
    });
  }
  let query = '';
  const groupNodes = assign.group_nodes || [];
  const m = openModal('<h2>发到节点：' + esc(name || id) + '</h2>' +
    '<div><label for="assign-level">指派级别</label><select id="assign-level">' +
    '<option value="all"' + (level === 'all' ? ' selected' : '') + '>全部——所有后端节点，全部入站</option>' +
    '<option value="group"' + (level === 'group' ? ' selected' : '') + '>组——跟所在分组走</option>' +
    '<option value="node"' + (level === 'node' ? ' selected' : '') + '>节点——只上勾选的节点</option>' +
    '<option value="inbound"' + (level === 'inbound' ? ' selected' : '') + '>入站——只上勾选的节点与入站</option>' +
    '</select></div>' +
    '<p class="muted" id="assign-level-hint"></p>' +
    '<div><input id="assign-search" placeholder="搜索节点（名字／地址）"></div>' +
    '<div id="assign-list" style="margin-top:8px"></div>' +
    '<div class="btn-row"><button class="btn" id="assign-ok" type="button">保存</button><button class="btn secondary" id="assign-cancel" type="button">取消</button></div>');
  if (!m) return;
  const hintEl = () => m.querySelector('#assign-level-hint');
  const renderHint = () => {
    const el = hintEl();
    if (!el) return;
    if (level === 'all') el.textContent = '全部：这个人上所有后端节点，全部入站。';
    else if (level === 'group') {
      if (!assign.group_id) el.textContent = '组：这个人还没分组，先在编辑里给他分组，再回来选组级指派。';
      else if (!groupNodes.length) el.textContent = '组：所在分组“' + (assign.group_name || assign.group_id) + '”还没配后端节点，先在分组编辑里勾后端节点。';
      else el.textContent = '组：跟分组“' + (assign.group_name || assign.group_id) + '”走，上 ' + groupNodes.length + ' 个后端节点。';
    }
    else if (level === 'node') el.textContent = '节点：只上下面勾选的节点，全部入站。';
    else el.textContent = '入站：只上下面勾选的节点与入站；某节点一个入站都没勾 = 这个人不去该节点。';
  };
  const renderList = () => {
    const box = m.querySelector('#assign-list');
    if (!box) return;
    const q = query.trim().toLowerCase();
    const showNodes = level === 'all' ? states : (level === 'group' ? states.filter((st) => groupNodes.indexOf(st.node.id) !== -1) : states);
    const rows = showNodes.filter((st) => !q || (st.node.name || '').toLowerCase().indexOf(q) !== -1 || (st.node.address || '').toLowerCase().indexOf(q) !== -1);
    if (!rows.length) { box.innerHTML = '<p class="muted small">没有匹配的节点。</p>'; return; }
    box.innerHTML = rows.map((st) => {
      const nid = st.node.id;
      const checked = level === 'all' || level === 'group' ? true : selNodes.has(nid);
      const dis = st.error || level === 'all' || level === 'group';
      let ibHtml = '';
      if (level === 'inbound' && checked && st.inbounds.length) {
        const sel = selInbounds[nid] || new Set();
        ibHtml = '<div style="margin:4px 0 4px 24px">' + st.inbounds.map((tag) => '<label class="checkbox-row"><input type="checkbox" data-assign-inbound="' + nid + ':' + esc(tag) + '"' + (sel.has(tag) ? ' checked' : '') + '><span>' + esc(tag) + '</span></label>').join('') + '</div>';
      }
      return '<label class="checkbox-row"><input type="checkbox" data-assign-node="' + nid + '"' + (checked ? ' checked' : '') + (dis ? ' disabled' : '') + '><span>' + esc(st.node.name || '') + '（' + esc(st.node.address || '') + '）' + (st.all ? ' · 全部用户' : '') + (st.error ? ' · 拉取失败' : '') + '</span></label>' + ibHtml;
    }).join('');
    box.querySelectorAll('[data-assign-node]').forEach((cb) => cb.addEventListener('change', () => {
      const nid = Number(cb.dataset.assignNode);
      if (cb.checked) selNodes.add(nid); else { selNodes.delete(nid); delete selInbounds[nid]; }
      renderList();
    }));
    box.querySelectorAll('[data-assign-inbound]').forEach((cb) => cb.addEventListener('change', () => {
      const p = String(cb.dataset.assignInbound).split(':');
      const nid = Number(p[0]);
      if (!selInbounds[nid]) selInbounds[nid] = new Set();
      if (cb.checked) selInbounds[nid].add(p[1]); else selInbounds[nid].delete(p[1]);
    }));
  };
  renderHint();
  renderList();
  onIn(m, '#assign-level', 'change', (e) => { level = e.target.value; renderHint(); renderList(); });
  onIn(m, '#assign-search', 'input', (e) => { query = e.target.value; renderList(); });
  onIn(m, '#assign-cancel', 'click', closeModal);
  onIn(m, '#assign-ok', 'click', async () => {
    try {
      // 期望的节点集合（按级别算）。
      let wantNodes;
      if (level === 'all') wantNodes = states.filter((st) => !st.error).map((st) => st.node.id);
      else if (level === 'group') {
        if (!assign.group_id) { toast('组级指派要先给这个用户分组', 'err'); return; }
        wantNodes = groupNodes.slice();
      }
      else if (level === 'node') wantNodes = Array.from(selNodes);
      else {
        wantNodes = Object.keys(selInbounds).map((x) => Number(x)).filter((nid) => selInbounds[nid] && selInbounds[nid].size > 0);
      }
      // 先同步节点侧的成员门（旧逻辑）：勾上的节点加人，不勾的拿掉。
      for (const st of states) {
        if (st.error) continue;
        const want = wantNodes.indexOf(st.node.id) !== -1;
        if (want === st.checked) continue;
        let list;
        if (want) {
          if (st.all) continue; // 全部用户模式本来就有他，不用改
          list = (st.ids || []).slice();
          if (list.indexOf(key) === -1) list.push(key);
        } else {
          const base = st.all ? st.eff.slice() : (st.ids || []).slice();
          list = base.filter((k) => k !== key);
        }
        await api('/api/v1/nodes/' + encodeURIComponent(st.node.id) + '/users', { method: 'PUT', body: JSON.stringify({ user_ids: list }) });
      }
      // 再写四级指派。
      let scope = level, targets = [];
      if (level === 'node') targets = wantNodes.map((x) => String(x));
      else if (level === 'inbound') {
        targets = [];
        wantNodes.forEach((nid) => { Array.from(selInbounds[nid] || []).forEach((tag) => targets.push(nid + ':' + tag)); });
      } else if (level === 'group') targets = [assign.group_id];
      await api('/api/v1/users/' + encodeURIComponent(key) + '/backend-assignment', { method: 'PUT', body: JSON.stringify({ scope, targets }) });
      closeModal();
      toast('已保存，节点 60 秒内应用', 'ok');
      if (kind === 'merged') loadMergedSubsList(); else loadSubUsers();
    } catch (e) { toast('保存失败：' + e.message, 'err'); }
  });
}
// 轮换一个用户在已指派节点上的 UUID（与“轮换”订阅链接是两回事：
// 这个换的是节点侧 VLESS 身份，订阅链接本身不动，下次取订阅即新 UUID）。
async function rotateNodeUUID(kind, id, name) {
  const key = kind + ':' + id;
  const ok = await confirmModal('轮换节点 UUID', '<p>确定轮换 <b>' + esc(name || id) + '</b> 在已指派节点上的 UUID 吗？</p><p>旧 UUID 在节点下次拉取（60 秒内）应用后失效，客户端须重新导入新 UUID。</p>', '轮换', true);
  if (!ok) return;
  try {
    const r = await apiPost('/api/v1/users/' + encodeURIComponent(key) + '/rotate-uuid', {});
    const uuids = r.uuids || {};
    const rows = Object.keys(uuids).map((nid) => '<div class="kv"><span>节点 ' + esc(nid) + '</span><b style="word-break:break-all">' + esc(uuids[nid]) + '</b></div>').join('');
    const m2 = openModal('<h2>UUID 已轮换</h2>' + (rows || '<p class="muted">该用户没有发到任何节点，无需轮换。</p>') + '<p class="muted">' + esc(r.note || '') + '</p><div class="btn-row"><button class="btn secondary" id="rot-uuid-close" type="button">知道了</button></div>');
    if (m2) onIn(m2, '#rot-uuid-close', 'click', closeModal);
  } catch (e) { toast('轮换失败：' + e.message, 'err'); }
}
async function subUserCreate() {
  const name = $('#sub-user-name').value.trim();
  if (!name) { toast('请先填名字（用来认人）', 'err'); return; }
  try {
    const scope = $('#sub-user-name').closest('.card');
    const tl = readTrafficLimit(scope);
    // 到期时间（v1.3.28）：勾永久 = 空串（永不过期）；不勾 = 日期框的值。
    const permEl = $('#sub-user-expiry-permanent');
    const dateEl = $('#sub-user-expiry-date');
    const expiresAt = (permEl && permEl.checked) ? '' : (dateEl ? dateEl.value : '');
    const r = await apiPost('/api/v1/subscriptions/users', { name, note: $('#sub-user-note').value.trim(), mode: subModeOf('sub-user-mode'), expires_at: expiresAt, timezone: browserTimeZone(), traffic_limit_mb: tl.limit_mb, traffic_limit_mode: tl.limit_mode, reset_mode: tl.reset_mode, reset_day: tl.reset_day, group_id: ($('#sub-user-group') || {}).value || '' });
    $('#sub-user-url-value').textContent = r.url || '';
    const box = $('#sub-user-once');
    if (box) { box.classList.remove('hidden'); box.hidden = false; }
    $('#sub-user-name').value = ''; $('#sub-user-note').value = '';
    if (dateEl) { dateEl.value = ''; dateEl.disabled = true; }
    if (permEl) permEl.checked = true;
    const gbEl = scope ? scope.querySelector('[data-tl-gb]') : null;
    if (gbEl) gbEl.value = 0;
    if (r.warning) toast(r.warning, 'err');
    else toast('订阅用户已新建，链接显示在下面，随时能复制', 'ok');
    loadSubUsers();
  } catch (err) { toast('新建失败：' + err.message, 'err'); }
}
// ---------- 分组（权限组，v1.3.26） ----------
// 一组 = 一组落地 + 一组用户，组里的人用组里的节点。建一次组、加用户就行，
// 不用每人重复勾落地；改组的落地，组里所有人的订阅下次更新立即同步。
let subGroupsCache = []; // 最近一次拉到的分组列表（下拉与弹窗共用）
let subGroupSearch = ''; // 分组列表搜索词
let subGroupPage = 1;
function populateSubGroupSelect(sel, selectedId) {
  if (!sel) return;
  const cur = selectedId !== undefined ? selectedId : sel.value;
  sel.innerHTML = '<option value="">不分组</option>' + (subGroupsCache || []).map((g) => '<option value="' + esc(g.id) + '"' + (g.id === cur ? ' selected' : '') + '>' + esc(g.name) + '</option>').join('');
  sel.value = cur || '';
}
async function loadSubGroups() {
  const list = $('#sub-group-list');
  populateSubGroupSelect($('#sub-user-group'));
  populateSubGroupSelect($('#central-sub-group'));
  if (!list) return;
  try {
    const r = await api('/api/v1/subscriptions/groups');
    subGroupsCache = r.groups || [];
    populateSubGroupSelect($('#sub-user-group'));
    populateSubGroupSelect($('#central-sub-group'));
    const filtered = subGroupsCache.filter((g) => matchSubText(subGroupSearch, [g.name, g.note, g.summary]));
    if (!subGroupsCache.length) { list.innerHTML = '<p class="muted small">还没有分组：点“新建分组”，先勾落地、再勾成员，一次建好。</p>'; return; }
    if (!filtered.length) { list.innerHTML = '<p class="muted small">没有匹配的分组，换个关键词试试。</p>'; return; }
    const pg = paginate(filtered, subGroupPage);
    subGroupPage = pg.cur;
    list.innerHTML = '<h3>分组</h3>' + pg.slice.map((g) => '<div class="kv"><span>' + esc(g.name || '') + (g.note ? ' <span class="small muted">（' + esc(g.note) + '）</span>' : '') + '<br><span class="small muted">' + esc(g.summary || '') + ' · ' + (g.members || []).length + ' 个成员 · 建于 ' + esc(fmtDate(g.created_at)) + '</span></span><span><button class="btn secondary sm" type="button" data-sub-group-edit="' + esc(g.id) + '">编辑</button> <button class="btn danger sm" type="button" data-sub-group-delete="' + esc(g.id) + '">删除</button></span></div>').join('') +
      pagerHtml(pg.total, pg.cur, pg.totalPages, 'subgroup') +
      '<p class="muted small">删组不删用户、不换令牌：成员恢复自己原来勾的落地，链接不变。</p>';
    if (pg.totalPages > 1) wirePager(list, 'subgroup', pg.cur, pg.totalPages, (p) => { subGroupPage = p; loadSubGroups(); });
    $$('[data-sub-group-edit]', list).forEach((b) => b.addEventListener('click', () => {
      const g = subGroupsCache.find((x) => x.id === b.dataset.subGroupEdit);
      if (g) openSubGroupModal(g);
    }));
    $$('[data-sub-group-delete]', list).forEach((b) => b.addEventListener('click', async () => {
      const g = subGroupsCache.find((x) => x.id === b.dataset.subGroupDelete);
      const ok = await confirmModal('删除分组', '<p>删掉分组「' + esc(g ? g.name : '') + '」？成员恢复自己原来勾的落地，不删用户、不换令牌、链接不变。确定吗？</p>', '删除', true);
      if (!ok) return;
      try { await api('/api/v1/subscriptions/groups/' + encodeURIComponent(b.dataset.subGroupDelete), { method: 'DELETE' }); toast('分组已删除，成员已恢复自己勾选', 'ok'); loadSubGroups(); loadSubUsers(); loadMergedSubsList(); }
      catch (e) { toast('删除失败：' + e.message, 'err'); }
    }));
  } catch (e) { list.innerHTML = '<div class="error-box">加载失败：' + esc(e.message) + '</div>'; }
}
// 新建/编辑分组弹窗：名字 + 落地勾选树（复用编辑弹窗那棵树，本机与远程
// 分层）+ 成员多选（订阅用户与合并订阅分两组列出）。
async function openSubGroupModal(group) {
  const isEdit = !!group;
  const m = openModal('<h2>' + esc(isEdit ? '编辑分组' : '新建分组') + '</h2>' +
    '<p class="muted small">一组 = 一组落地 + 一组用户，组里的人用组里的节点。改组的落地，组里所有人的订阅下次更新立即同步，链接不变。</p>' +
    '<label class="small">名字：<input type="text" data-group-name style="width:200px" value="' + esc(group ? group.name || '' : '') + '" placeholder="如 美国组"></label>' +
    '<label class="small" style="margin-left:8px">备注：<input type="text" data-group-note style="width:160px" value="' + esc(group ? group.note || '' : '') + '" placeholder="选填"></label>' +
    '<div data-group-tree style="margin-top:12px">加载中…</div>' +
    '<p class="muted small">至少勾一个落地。</p>' +
    '<div data-group-members style="margin-top:12px">加载中…</div>' +
    '<p class="muted small">成员一人只能进一个组：勾了这里的人，会自动从原来那个组里出来。</p>' +
    '<div data-group-backend-nodes style="margin-top:12px">加载中…</div>' +
    '<p class="muted small">后端节点（v1.4.4，组级指派用）：组级指派的成员上这里勾的后端节点；不勾 = 组级指派下成员上不了任何后端节点。</p>' +
    '<div class="btn-row" style="margin-top:12px"><button class="btn" type="button" data-group-save>保存</button>' +
    '<button class="btn secondary" type="button" data-group-cancel>取消</button></div>', true);
  if (!m) return;
  const checked = {};
  (group ? group.selection || [] : []).forEach((e) => { checked[subTreeKey(e.panel, e.landing)] = true; });
  const treeBox = m.querySelector('[data-group-tree]');
  try {
    const panels = await fetchSubTreePanels();
    renderSubEditTree(treeBox, panels, checked);
  } catch (e) {
    if (treeBox) treeBox.innerHTML = '<div class="error-box">勾选树加载失败：' + esc(e.message) + '</div>';
  }
  // 成员多选：订阅用户与合并订阅分两组列出，当前成员预选；已在别的组里
  // 的人会标注出来，勾过来即换组。
  const memBox = m.querySelector('[data-group-members]');
  let members = { users: [], merged: [] };
  try {
    const [ru, rm] = await Promise.all([api('/api/v1/subscriptions/users'), api('/api/v1/subscriptions/merged')]);
    members.users = ru.users || [];
    members.merged = rm.subs || [];
  } catch (e) {
    if (memBox) memBox.innerHTML = '<div class="error-box">成员列表加载失败：' + esc(e.message) + '</div>';
  }
  const renderMembers = () => {
    if (!memBox) return;
    const curUserIds = new Set((group ? group.members || [] : []).filter((x) => x.kind === 'user').map((x) => x.id));
    const curMergedIds = new Set((group ? group.members || [] : []).filter((x) => x.kind === 'merged').map((x) => x.id));
    let h = '<p class="small"><b>成员</b></p>';
    h += '<p class="small" style="margin-top:8px"><b>订阅用户</b></p>';
    h += members.users.length ? members.users.map((u) => '<label class="checkbox-row"><input type="checkbox" data-group-member-user="' + esc(u.id) + '"' + (curUserIds.has(u.id) ? ' checked' : '') + '><span>' + esc(u.name || '') + (u.group_id && u.group_id !== (group ? group.id : '') ? ' <span class="muted small">（在分组「' + esc(u.group_name || '') + '」里）</span>' : '') + '</span></label>').join('') : '<p class="muted small">还没有订阅用户。</p>';
    h += '<p class="small" style="margin-top:8px"><b>合并订阅</b></p>';
    h += members.merged.length ? members.merged.map((s) => '<label class="checkbox-row"><input type="checkbox" data-group-member-merged="' + esc(s.id) + '"' + (curMergedIds.has(s.id) ? ' checked' : '') + '><span>' + esc(s.name || '未命名合并订阅') + (s.group_id && s.group_id !== (group ? group.id : '') ? ' <span class="muted small">（在分组「' + esc(s.group_name || '') + '」里）</span>' : '') + '</span></label>').join('') : '<p class="muted small">还没有合并订阅。</p>';
    memBox.innerHTML = h;
  };
  renderMembers();
  // 后端节点多选（v1.4.4）：该组用的后端节点，组级指派时成员上这些节点。
  const bnBox = m.querySelector('[data-group-backend-nodes]');
  const curBackendNodes = new Set(group ? (group.backend_node_ids || []) : []);
  try {
    const rn = await api('/api/v1/nodes');
    const nodes = rn.nodes || [];
    if (bnBox) {
      bnBox.innerHTML = '<p class="small"><b>后端节点</b></p>' + (nodes.length ? nodes.map((n) => '<label class="checkbox-row"><input type="checkbox" data-group-backend-node="' + esc(String(n.id)) + '"' + (curBackendNodes.has(n.id) ? ' checked' : '') + '><span>' + esc(n.name || '') + '（' + esc(n.address || '') + '）</span></label>').join('') : '<p class="muted small">还没有后端节点。</p>');
    }
  } catch (e) {
    if (bnBox) bnBox.innerHTML = '<div class="error-box">后端节点列表加载失败：' + esc(e.message) + '</div>';
  }
  onIn(m, '[data-group-cancel]', 'click', closeModal);
  onIn(m, '[data-group-save]', 'click', async () => {
    const name = ((m.querySelector('[data-group-name]') || {}).value || '').trim();
    if (!name) { toast('请先填分组名字', 'err'); return; }
    const selection = subEditTreeSelection(checked);
    if (!selection.length) { toast('至少勾一个落地', 'err'); return; }
    const members = $$('[data-group-member-user]', m).filter((cb) => cb.checked).map((cb) => ({ kind: 'user', id: cb.dataset.groupMemberUser }))
      .concat($$('[data-group-member-merged]', m).filter((cb) => cb.checked).map((cb) => ({ kind: 'merged', id: cb.dataset.groupMemberMerged })));
    const backend_node_ids = $$('[data-group-backend-node]', m).filter((cb) => cb.checked).map((cb) => Number(cb.dataset.groupBackendNode));
    try {
      const body = { name, note: ((m.querySelector('[data-group-note]') || {}).value || '').trim(), selection, members, backend_node_ids };
      if (isEdit) await api('/api/v1/subscriptions/groups/' + encodeURIComponent(group.id), { method: 'PUT', body: JSON.stringify(body) });
      else await apiPost('/api/v1/subscriptions/groups', body);
      closeModal();
      toast(isEdit ? '分组已保存，成员订阅下次更新即同步' : '分组已新建', 'ok');
      loadSubGroups(); loadSubUsers(); loadMergedSubsList();
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
}
function initSubGroups() {
  on('#sub-group-create', 'click', () => openSubGroupModal(null));
  wireListSearch('#sub-group-search', (q) => { subGroupSearch = q; subGroupPage = 1; loadSubGroups(); });
}
// ---------- 编辑订阅用户 / 合并订阅（v1.3.19） ----------
// 把面板数据渲染成勾选树（面板框 + 落地行，半选态与全选框与集中管理页同一套）：
// container 是弹窗里的容器，panels 是 fetchSubTreePanels() 的返回，
// checked 是 "面板id:落地编号" -> true 的自有映射（改它不影响集中管理页的勾选）。
function renderSubEditTree(container, panels, checked) {
  if (!container) return;
  const total = panels.reduce((n, p) => n + p.landings.length, 0);
  let h = '<label class="checkbox-row"><input type="checkbox" data-edit-all><span>全选全部面板（共 ' + total + ' 个落地）</span></label>';
  panels.forEach((p) => {
    h += '<div style="margin-top:10px"><label class="checkbox-row"><input type="checkbox" data-edit-panel="' + esc(p.id) + '"><b>' + esc(p.name) + '</b><span class="muted small">（' + p.landings.length + ' 个落地' + (p.online === false ? '，离线' : '') + '）</span></label>';
    if (!p.landings.length) {
      h += '<p class="muted small" style="margin:4px 0 0 28px">没有落地。</p>';
    } else {
      p.landings.forEach((l) => {
        const key = subTreeKey(p.id, l.index);
        h += '<div style="margin:4px 0 0 28px"><label class="checkbox-row" style="margin:0"><input type="checkbox" data-edit-landing="' + esc(key) + '"' + (checked[key] ? ' checked' : '') + '><span>' + esc(l.title) + ' <span class="small">#' + esc(l.index) + '</span></span></label></div>';
      });
    }
    h += '</div>';
  });
  container.innerHTML = h;
  const refresh = () => {
    $$('[data-edit-panel]', container).forEach((cb) => {
      const pid = cb.dataset.editPanel;
      const panel = panels.find((p) => p.id === pid);
      if (!panel || !panel.landings.length) { cb.checked = false; cb.indeterminate = false; return; }
      const n = panel.landings.filter((l) => checked[subTreeKey(pid, l.index)]).length;
      cb.checked = n === panel.landings.length;
      cb.indeterminate = n > 0 && n < panel.landings.length;
    });
    const all = container.querySelector('[data-edit-all]');
    const n = Object.keys(checked).length;
    if (all) { all.checked = total > 0 && n === total; all.indeterminate = n > 0 && n < total; }
  };
  $$('[data-edit-all]', container).forEach((cb) => cb.addEventListener('change', () => {
    const on = cb.checked;
    panels.forEach((p) => p.landings.forEach((l) => {
      if (on) checked[subTreeKey(p.id, l.index)] = true;
      else delete checked[subTreeKey(p.id, l.index)];
    }));
    $$('[data-edit-panel]', container).forEach((c) => { c.checked = on; c.indeterminate = false; });
    $$('[data-edit-landing]', container).forEach((c) => { c.checked = on; });
    refresh();
  }));
  $$('[data-edit-panel]', container).forEach((cb) => cb.addEventListener('change', () => {
    const pid = cb.dataset.editPanel;
    const panel = panels.find((p) => p.id === pid);
    if (!panel) return;
    panel.landings.forEach((l) => {
      if (cb.checked) checked[subTreeKey(pid, l.index)] = true;
      else delete checked[subTreeKey(pid, l.index)];
    });
    $$('[data-edit-landing]', container).forEach((lc) => { if (lc.dataset.editLanding.startsWith(pid + ':')) lc.checked = cb.checked; });
    refresh();
  }));
  $$('[data-edit-landing]', container).forEach((cb) => cb.addEventListener('change', () => {
    if (cb.checked) checked[cb.dataset.editLanding] = true;
    else delete checked[cb.dataset.editLanding];
    refresh();
  }));
  refresh();
}
// 把勾选映射收成后端要的 selection 数组。
function subEditTreeSelection(checked) {
  const sel = [];
  Object.keys(checked).forEach((key) => {
    const idx = key.lastIndexOf(':');
    if (idx > 0) sel.push({ panel: key.slice(0, idx), landing: Number(key.slice(idx + 1)) });
  });
  return sel;
}
// 编辑订阅用户 / 合并订阅：改勾选集合与内容模式，不换令牌、不换链接——
// 保存后旧链接继续能用，下次取订阅即按新勾选出内容。
// opts: {kind:'user'|'merged', id, name, note, selection, mode, group_id}
// 分组（v1.3.26）：选了组后勾选树变只读、显示组的落地（写明“由分组决定”）；
// 保存时 group_id 一起发，进组不改自己的勾选，出组恢复自己勾选。
async function openSubEditModal(opts) {
  const isMerged = opts.kind === 'merged';
  const m = openModal('<h2>' + esc(isMerged ? '编辑合并订阅' : '编辑订阅用户') + '</h2>' +
    '<p class="muted small">改勾选、改内容，链接不变：保存后旧订阅链接继续能用，下次更新订阅即按新勾选同步。</p>' +
    '<label class="small">' + esc(isMerged ? '归属到人（选填）：' : '名字：') + '<input type="text" data-edit-name style="width:200px" value="' + esc(opts.name || '') + '"></label>' +
    (isMerged ? '' : '<label class="small" style="margin-left:8px">备注：<input type="text" data-edit-note style="width:160px" value="' + esc(opts.note || '') + '" placeholder="选填"></label>') +
    '<div style="margin-top:12px"><label class="small">所属分组：<select data-edit-group style="width:160px"><option value="">不分组</option></select></label>' +
    '<span class="muted small">进了组，订阅按组的落地出内容；自己的勾选保留着，出组后恢复。</span></div>' +
    '<div data-edit-tree style="margin-top:12px">加载中…</div>' +
    '<p class="muted small hidden" data-edit-group-note hidden></p>' +
    '<p class="muted small">' + esc(isMerged ? '合并订阅至少要勾一个落地。' : '全部不勾 = 全部落地（单面板全量订阅）。') + '</p>' +
    '<div style="margin-top:12px">' + subModeSelectHTML('sub-edit-mode', '') + '</div>' +
    '<div class="form-row"><label class="small">到期时间：<input type="date" data-edit-expiry value="' + esc(dateInputOfExpiry(opts.expires_at)) + '"' + (opts.expires_at ? '' : ' disabled') + '></label>' +
    '<label class="small check"><input type="checkbox" data-edit-permanent' + (opts.expires_at ? '' : ' checked') + '> 永久</label></div>' +
    '<p class="muted small">到期时间到了，这条订阅就打不开；改到期时间不换令牌，链接不变。勾永久 = 永不过期；填日期 = 当天 23:59:59 到期。</p>' +
    trafficLimitHTML(opts) +
    ((opts.traffic_limit_mb || 0) > 0 ? '<div style="margin-top:12px"><h3 class="small">中转端口（按人转发）</h3><div data-edit-forwards class="small muted">加载中…</div><p class="muted small">端口被封或想换个端口时，在这里按落地逐条换：换完订阅链接里的中转端口跟着变，客户端要重新导入一次订阅；换的瞬间正在用的连接会断一下。换端口立即生效，不用点下面的保存。</p></div>' : '') +
    '<div class="btn-row" style="margin-top:12px"><button class="btn" type="button" data-edit-save>保存</button>' +
    '<button class="btn secondary" type="button" data-edit-cancel>取消</button></div>', true);
  if (!m) return;
  wireTrafficLimitUI(m);
  // 更换中转端口（v1.3.34）：开了限流的订阅才有按人转发，逐条显示当前端口。
  if ((opts.traffic_limit_mb || 0) > 0) loadEditForwards(m, opts);
  // 永久勾选与日期框联动：勾永久时日期框置灰。
  onIn(m, '[data-edit-permanent]', 'change', () => {
    const d = m.querySelector('[data-edit-expiry]'), p = m.querySelector('[data-edit-permanent]');
    if (d && p) d.disabled = p.checked;
  });
  // 三选一下拉按当前值预选（只改弹窗里这一个下拉，不动页面上其他表单与上次选择）。
  syncSubModeSelect(m.querySelector('select[name="sub-edit-mode"]'), subModeValid(opts.mode || 'both'));
  // 分组（v1.3.26）：下拉按当前归属预选；切分组时重绘勾选树——进了组
  // 显示组的落地且只读（由分组决定），切回不分组恢复自己的勾选且可编辑。
  // checked 对象保持同一个引用（保存时 subEditTreeSelection 读它）。
  const checked = {};
  const treeBox = m.querySelector('[data-edit-tree]');
  const groupSel = m.querySelector('[data-edit-group]');
  populateSubGroupSelect(groupSel, opts.group_id || '');
  let editPanels = null;
  const applyEditGroup = (groupId) => {
    const note = m.querySelector('[data-edit-group-note]');
    if (!editPanels) return;
    for (const k in checked) delete checked[k];
    if (groupId) {
      const g = (subGroupsCache || []).find((x) => x.id === groupId);
      (g ? g.selection || [] : []).forEach((e) => { checked[subTreeKey(e.panel, e.landing)] = true; });
      renderSubEditTree(treeBox, editPanels, checked);
      if (treeBox) treeBox.querySelectorAll('input').forEach((cb) => { cb.disabled = true; });
      if (note) { note.textContent = '由分组决定：订阅按分组的落地出内容，自己的勾选保留着；改落地去分组里改。'; note.classList.remove('hidden'); note.hidden = false; }
    } else {
      (opts.selection || []).forEach((e) => { checked[subTreeKey(e.panel, e.landing)] = true; });
      renderSubEditTree(treeBox, editPanels, checked);
      if (note) { note.classList.add('hidden'); note.hidden = true; }
    }
  };
  try {
    editPanels = await fetchSubTreePanels();
    applyEditGroup(groupSel ? groupSel.value : '');
  } catch (e) {
    if (treeBox) treeBox.innerHTML = '<div class="error-box">勾选树加载失败：' + esc(e.message) + '</div>';
  }
  onIn(m, '[data-edit-group]', 'change', () => { applyEditGroup(groupSel ? groupSel.value : ''); });
  onIn(m, '[data-edit-cancel]', 'click', closeModal);
  onIn(m, '[data-edit-save]', 'click', async () => {
    const name = ((m.querySelector('[data-edit-name]') || {}).value || '').trim();
    const selection = subEditTreeSelection(checked);
    if (isMerged && !selection.length) { toast('合并订阅至少要勾一个落地', 'err'); return; }
    if (!isMerged && !name) { toast('请先填名字（用来认人）', 'err'); return; }
    const mode = subModeOf('sub-edit-mode');
    // 到期时间：勾永久 = 空串（永不过期）；不勾 = 日期框的值。
    const permEl = m.querySelector('[data-edit-permanent]');
    const expEl = m.querySelector('[data-edit-expiry]');
    const expiresAt = (permEl && permEl.checked) ? '' : (expEl ? expEl.value : '');
    const groupId = (m.querySelector('[data-edit-group]') || {}).value || '';
    try {
      const url = isMerged
        ? '/api/v1/subscriptions/merged/' + encodeURIComponent(opts.id)
        : '/api/v1/subscriptions/users/' + encodeURIComponent(opts.id);
      const tl = readTrafficLimit(m);
      const body = isMerged
        ? { selection, name, mode, expires_at: expiresAt, timezone: browserTimeZone(), group_id: groupId, traffic_limit_mb: tl.limit_mb, traffic_limit_mode: tl.limit_mode, reset_mode: tl.reset_mode, reset_day: tl.reset_day }
        : { name, note: ((m.querySelector('[data-edit-note]') || {}).value || '').trim(), selection, mode, expires_at: expiresAt, timezone: browserTimeZone(), group_id: groupId, traffic_limit_mb: tl.limit_mb, traffic_limit_mode: tl.limit_mode, reset_mode: tl.reset_mode, reset_day: tl.reset_day };
      const r = await api(url, { method: 'PUT', body: JSON.stringify(body) });
      closeModal();
      if (r && r.warning) toast(r.warning, 'err');
      else toast('已保存，订阅链接不变，下次更新即按新勾选同步', 'ok');
      if (isMerged) loadMergedSubsList(); else loadSubUsers();
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
  });
}
// 更换中转端口（v1.3.34）：编辑弹窗里按落地逐条显示一条订阅的按人转发，
// 每条可换端口。行内改（不当场开新弹窗，编辑弹窗保持打开、未保存的
// 改动不丢）：点“更换端口”后该行变成输入框，填自定义端口、或留空自动分配。
async function loadEditForwards(m, opts) {
  const box = m.querySelector('[data-edit-forwards]');
  if (!box) return;
  const kind = opts.kind === 'merged' ? 'merged' : 'users';
  const render = (list, portStart, portEnd) => {
    box.innerHTML = list.map((f, i) => '<div class="kv" data-fwd-row="' + i + '"><span>' + esc(f.panel_name || f.panel_id) + ' · ' + esc(f.landing_name || f.landing_addr) + (f.disabled ? ' <span class="badge red">已停用</span>' : '') + '<br><span class="muted">中转端口：<b>' + f.listen_port + '</b></span></span><span><button class="btn secondary sm" type="button" data-fwd-change="' + i + '">更换端口</button></span></div>').join('');
    box.querySelectorAll('[data-fwd-change]').forEach((b) => b.addEventListener('click', () => {
      const f = list[Number(b.dataset.fwdChange)];
      const row = box.querySelector('[data-fwd-row="' + b.dataset.fwdChange + '"]');
      if (!f || !row) return;
      row.innerHTML = '<span><label class="small">新端口（留空自动分配）：<input type="number" data-fwd-new-port style="width:100px" min="' + portStart + '" max="' + portEnd + '" placeholder="留空自动分配"></label><br><span class="muted small">端口段 ' + portStart + '–' + portEnd + '；撞了会报错、不应用。换完订阅链接跟着变，客户端要重新导入一次；换的瞬间正在用的连接会断一下。</span></span><span><button class="btn sm" type="button" data-fwd-confirm>确定更换</button> <button class="btn secondary sm" type="button" data-fwd-cancel>取消</button></span>';
      row.querySelector('[data-fwd-cancel]').addEventListener('click', () => render(list, portStart, portEnd));
      row.querySelector('[data-fwd-confirm]').addEventListener('click', async () => {
        const v = ((row.querySelector('[data-fwd-new-port]') || {}).value || '').trim();
        const newPort = v === '' ? 0 : Number(v);
        if (v !== '' && (!Number.isInteger(newPort) || newPort < 1 || newPort > 65535)) { toast('端口填 1–65535 的数字，或留空自动分配', 'err'); return; }
        try {
          const r = await apiPost('/api/v1/subscriptions/' + kind + '/' + encodeURIComponent(opts.id) + '/change-port', { panel_id: f.panel_id, landing_addr: f.landing_addr, new_port: newPort });
          let msg = '已换成新端口 ' + r.new_port + '，订阅链接已变、客户端要重新导入一次订阅';
          if (r.warning) msg += '；' + r.warning;
          toast(msg, 'ok');
          if (kind === 'merged') loadMergedSubsList(); else loadSubUsers();
          await loadEditForwards(m, opts);
        } catch (e) { toast('换端口失败：' + e.message, 'err'); }
      });
    }));
  };
  try {
    const r = await api('/api/v1/subscriptions/' + kind + '/' + encodeURIComponent(opts.id) + '/forwards');
    const list = r.forwards || [];
    if (!list.length) { box.innerHTML = '<span class="muted">这条订阅还没有按人转发（没开限流或转发还没建起来，重新保存一次订阅）。</span>'; return; }
    render(list, r.port_start || 30000, r.port_end || 39999);
  } catch (e) {
    box.innerHTML = '<span class="warn">转发列表加载失败：' + esc(e.message) + '</span>';
  }
}
// ---------- 订阅网关代码生成与检测（v1.3.50） ----------
// 部署方式三档：worker（Cloudflare Worker）/ caddy（Caddy 一键容器）/
// install（脚本直装）。点哪档就向后端要哪档的代码（值已按面板当前值填好）。
let gatewayType = '';
async function loadGatewayCode(type) {
  gatewayType = type;
  document.querySelectorAll('[data-gateway-type]').forEach((b) => {
    const on = b.dataset.gatewayType === type;
    b.classList.toggle('btn', on);
    b.classList.toggle('secondary', !on);
  });
  const box = $('#gateway-code-box'), pre = $('#gateway-code');
  if (!box || !pre) return;
  try {
    const r = await api('/api/v1/settings/gateway/code?type=' + encodeURIComponent(type));
    pre.textContent = r.content || '';
    // 每个部署文件一个下载按钮（用已取到的内容生成 Blob 下，不用新接口）：
    // Worker 是 worker.js＋wrangler.toml，Caddy 是 docker-compose.yml 与
    // Caddyfile 两个，脚本直装是 install.sh 一个。
    const dlBox = $('#gateway-downloads');
    if (dlBox) {
      dlBox.innerHTML = '';
      (r.files || []).forEach((f) => {
        if (!f || !f.name) return;
        const b = document.createElement('button');
        b.className = 'btn secondary sm'; b.type = 'button';
        b.textContent = '下载 ' + f.name;
        b.setAttribute('data-gateway-download', f.name);
        b.addEventListener('click', () => downloadTextFile(f.name, f.content || ''));
        dlBox.appendChild(b);
      });
    }
    const noteEl = $('#gateway-code-note');
    if (noteEl) {
      if (r.note) { noteEl.textContent = r.note; noteEl.classList.remove('hidden'); noteEl.hidden = false; }
      else { noteEl.textContent = ''; noteEl.classList.add('hidden'); noteEl.hidden = true; }
    }
    box.classList.remove('hidden'); box.hidden = false;
  } catch (err) { toast('生成失败：' + err.message, 'err'); }
}
// 检测网关三条路：服务端去请求订阅对外域名，逐条返回状态；
// 令牌只留在服务端，返回里只有状态与节点数。
async function checkGateway() {
  const btn = $('#gateway-check'), box = $('#gateway-check-results');
  if (!btn || !box) return;
  btn.disabled = true;
  const old = btn.textContent; btn.textContent = '检测中…';
  try {
    const r = await apiPost('/api/v1/settings/gateway/check', {});
    if (!r.checks || !r.checks.length) {
      box.innerHTML = '<span class="warn">' + esc(r.reason || '检测失败') + '</span>';
      return;
    }
    const label = { 'public-info': '公告', 'client-config': '远程配置', 'subscription': '订阅拉取' };
    box.innerHTML = r.checks.map((c) => {
      const st = c.ok ? '<b>通</b>' : '<span class="warn">不通</span>';
      const ms = (c.ms != null) ? '（' + c.ms + 'ms）' : '';
      if (c.name === 'subscription' && c.subs && c.subs.length) {
        // 订阅那一项下分三行：每类一行，写清类型、状态与数量（中转/直连/其它）；
        // 没有令牌的那类只报“无此类型订阅”，不算不通。通的那行直接显示理由，
        // 理由里已写明节点数与分类（通，2 个节点（中转 1，直连 1））。
        const rows = c.subs.map((s) => {
          const name = esc(s.label || s.kind || '');
          if (s.skipped) return '<div style="margin-left:16px">' + name + '：无此类型订阅</div>';
          if (s.ok) return '<div style="margin-left:16px">' + name + '：' + esc(s.reason || '通') + '</div>';
          return '<div style="margin-left:16px">' + name + '：<span class="warn">不通</span> —— ' + esc(s.reason || '') + '</div>';
        }).join('');
        return '<div>' + (label[c.name] || c.name) + '：' + st + ms + '</div>' + rows;
      }
      if (c.name === 'subscription' && c.ok) {
        // 旧形状（没有 subs）：订阅通时直接显示理由。
        return '<div>' + (label[c.name] || c.name) + '：' + st + ms + ' —— ' + esc(c.reason || '') + '</div>';
      }
      const why = c.ok ? '' : ' —— ' + esc(c.reason || '');
      return '<div>' + (label[c.name] || c.name) + '：' + st + ms + why + '</div>';
    }).join('');
  } catch (err) {
    box.innerHTML = '<span class="warn">检测失败：' + esc(err.message) + '</span>';
  } finally { btn.disabled = false; btn.textContent = old; }
}
// 合并订阅弹窗：显示一条刚生成/轮换的订阅链接，带复制、
// 二维码、模式选择，以及轮换/撤销按钮。opts: {title, rotated, name, mergedId, mode}
// mode 是这条订阅的内容范围（both|relay|direct），显示用；轮换时保持不变。
function openMergedSubModal(url, opts) {
  opts = opts || {};
  let curUrl = url;
  const m = openModal('<h2>' + esc(opts.title || '订阅链接') + '</h2>' +
    (opts.rotated ? '<div class="warn-box" data-merged-warn>这份选择之前生成过，已轮换：旧链接立即失效，请用下面这条新链接。</div>' : '<div class="warn-box" data-merged-warn>链接已加密存在面板里，关掉弹窗后在合并订阅列表里点“复制”随时能拿。</div>') +
    (opts.name ? '<div class="kv"><span>归属到人</span><b>' + esc(opts.name) + '</b></div>' : '') +
    '<div class="kv"><span>订阅链接</span><b class="mono" style="word-break:break-all" data-merged-url>' + esc(curUrl) + '</b></div>' +
    (opts.mode ? '<div class="kv"><span>订阅内容</span><b>' + esc(subModeLabel(opts.mode)) + '</b></div>' : '') +
    (opts.expires_at !== undefined ? '<div class="kv"><span>到期时间</span><b>' + esc(fmtExpiry(opts.expires_at)) + '</b></div>' : '') +
    '<p class="muted small">模式（可选）：<select data-sub-mode><option value="" selected>默认（不带参数，服务端按分流出）</option><option value="split">分流（TikTok/社交/流媒体走节点，其他直连）</option><option value="global">全局（全部走代理）</option></select>　客户端按 UA 自动识别格式（Clash 系拿 YAML，其他拿 base64 列表）；也可以手动加后缀：?format=clash / base64 / plain，?mode=split / global。默认复制的链接不带模式参数；只有规则模式下想全走代理、又保留分类分流时，才选全局。</p>' +
    '<div class="btn-row"><button class="btn sm" type="button" data-sub-copy-url>复制订阅链接</button>' +
    '<button class="btn secondary sm" type="button" data-sub-copy-clash>复制 Clash 链接</button>' +
    '<button class="btn secondary sm" type="button" data-sub-copy-plain>复制明文链接</button>' +
    '<button class="btn secondary sm" type="button" data-sub-qr>二维码</button>' +
    '<button class="btn secondary sm" type="button" data-sub-close>关闭</button></div>' +
    (opts.mergedId ? '<div class="btn-row" style="margin-top:8px"><button class="btn secondary sm" type="button" data-sub-rotate>轮换订阅链接</button>' +
    '<button class="btn danger sm" type="button" data-sub-revoke>撤销订阅链接</button></div>' : '') +
    '<div class="qr-wrap hidden" data-sub-qr-box><figure data-sub-qr-figure><img data-sub-qr-img alt="订阅链接二维码"><figcaption>订阅链接二维码</figcaption>' +
    '<div class="btn-row"><button class="btn secondary sm" type="button" data-copy-img>复制图片</button>' +
    '<button class="btn secondary sm" type="button" data-download-img="订阅链接-二维码.png">下载图片</button></div></figure></div>');
  if (!m) return null;
  const modeOf = () => { const s = m.querySelector('[data-sub-mode]'); return (s && s.value) || ''; };
  const withMode = (fmt) => { const q = []; if (fmt) q.push('format=' + fmt); const md = modeOf(); if (md) q.push('mode=' + md); return q.length ? curUrl + '?' + q.join('&') : curUrl; };
  onIn(m, '[data-sub-copy-url]', 'click', (e) => copyText(withMode(''), e.target));
  onIn(m, '[data-sub-copy-clash]', 'click', (e) => copyText(withMode('clash'), e.target));
  onIn(m, '[data-sub-copy-plain]', 'click', (e) => copyText(withMode('plain'), e.target));
  const loadModalQr = async () => {
    const box = m.querySelector('[data-sub-qr-box]'), img = m.querySelector('[data-sub-qr-img]'), fig = m.querySelector('[data-sub-qr-figure]');
    if (!box || !img) return;
    try {
      const r = await apiPost('/api/v1/subscription/qr', { url: withMode('') });
      if (!r.qr) throw new Error('后端没有返回二维码');
      img.src = r.qr;
      if (fig) fig.dataset.qr = r.qr;
      box.classList.remove('hidden');
    } catch (err) { toast('二维码生成失败：' + err.message, 'err'); }
  };
  onIn(m, '[data-sub-qr]', 'click', async () => {
    const box = m.querySelector('[data-sub-qr-box]');
    if (!box) return;
    if (!box.classList.contains('hidden')) { box.classList.add('hidden'); return; }
    await loadModalQr();
  });
  onIn(m, '[data-sub-mode]', 'change', async () => {
    const box = m.querySelector('[data-sub-qr-box]');
    if (box && !box.classList.contains('hidden')) await loadModalQr();
  });
  // 轮换（v1.3.2）：同一勾选集合与同一归属人换一个新令牌，弹窗里直接显示新链接。
  onIn(m, '[data-sub-rotate]', 'click', async () => {
    const ok = await confirmModal('轮换订阅链接', '<p>轮换后旧订阅链接立即失效，客户端里已导入的订阅要换成新链接。确定吗？</p>', '轮换订阅链接', true);
    if (!ok) return;
    try {
      const r = await apiPost('/api/v1/subscriptions/merged/' + encodeURIComponent(opts.mergedId) + '/rotate', {});
      curUrl = r.url || '';
      const urlEl = m.querySelector('[data-merged-url]');
      if (urlEl) urlEl.textContent = curUrl;
      const warn = m.querySelector('[data-merged-warn]');
      if (warn) warn.textContent = '已轮换：旧链接立即失效，请用下面这条新链接。';
      const box = m.querySelector('[data-sub-qr-box]');
      if (box) box.classList.add('hidden');
      toast('订阅链接已轮换，旧链接已失效', 'ok');
      loadMergedSubsList();
    } catch (err) { toast('轮换失败：' + err.message, 'err'); }
  });
  // 撤销（v1.3.2）：删掉这个令牌，弹窗关闭。
  onIn(m, '[data-sub-revoke]', 'click', async () => {
    const ok = await confirmModal('撤销订阅链接', '<p>撤销后订阅链接立即打不开，客户端里已导入的订阅会失效。确定吗？</p>', '撤销订阅链接', true);
    if (!ok) return;
    try {
      await api('/api/v1/subscriptions/merged/' + encodeURIComponent(opts.mergedId), { method: 'DELETE' });
      closeModal();
      toast('订阅链接已撤销', 'ok');
      loadMergedSubsList();
    } catch (err) { toast('撤销失败：' + err.message, 'err'); }
  });
  onIn(m, '[data-sub-close]', 'click', closeModal);
  return m;
}
// 给一份勾选（单个或多个落地）生成订阅链接，弹窗显示链接与复制按钮。
// selection: [{panel:'local'|远程面板 id, landing:编号}]；name 选填（归属到人，
// 只在合并订阅生成时传，填了就新起一条、不走轮换）；mode 是内容范围
// （both|relay|direct），不传默认全部；expiresAt 选填（日期，空 = 永久）。
async function showSubForSelection(selection, title, name, mode, expiresAt, traffic, groupId) {
  try {
    const tl = traffic || { limit_mb: 0, limit_mode: 'total', reset_mode: 'monthly', reset_day: 1 };
    const r = await apiPost('/api/v1/subscriptions/merged', { selection, name: name || undefined, mode: subModeValid(mode || 'both'), expires_at: expiresAt || '', timezone: browserTimeZone(), traffic_limit_mb: tl.limit_mb, traffic_limit_mode: tl.limit_mode, reset_mode: tl.reset_mode, reset_day: tl.reset_day, group_id: groupId || '' });
    openMergedSubModal(r.url || '', { title, rotated: r.rotated, name, mergedId: r.id, mode: r.mode || mode || 'both', expires_at: r.expires_at });
    if (r.warning) toast(r.warning, 'err');
    loadMergedSubsList();
  } catch (err) { toast('生成失败：' + err.message, 'err'); }
}
// 单落地“订阅”按钮：先弹小窗选订阅内容（全部/仅中转/仅直连），再生成。
// 选择记到这个小窗自己的上次选择里，下次打开还是它。
function askSubModeForSelection(selection, title) {
  const m = openModal('<h2>' + esc(title) + '</h2>' +
    '<p class="muted small">选这条订阅里要哪些节点：</p>' +
    '<div>' + subModeSelectHTML('single-sub-mode', 'relay-panel-sub-mode-single') + '</div>' +
    '<div class="btn-row"><button class="btn" type="button" data-single-sub-go>生成订阅链接</button>' +
    '<button class="btn secondary" type="button" data-single-sub-cancel>取消</button></div>');
  if (!m) return;
  const sel = m.querySelector('select[name="single-sub-mode"]');
  bindSubModeSelect(sel);
  syncSubModeSelect(sel, savedSubMode('relay-panel-sub-mode-single'));
  onIn(m, '[data-single-sub-go]', 'click', async () => {
    const mode = subModeOf('single-sub-mode');
    closeModal();
    await showSubForSelection(selection, title, '', mode);
  });
  onIn(m, '[data-single-sub-cancel]', 'click', closeModal);
}
// ---------- 集中管理卡片汇总（v1.3.4） ----------
// 远程落地流量与到期都来自集中管理快照（主面板每 2 分钟拉远程状态，
// 快照里的 traffic/days_left 与远程面板自己的 /api/v1/central/traffic
// 读的是同一份流量库，不用再为每张卡片单独拉一次；详情弹窗打开后
// 还会再从远程实时拉一次流量汇总，拉到了就更新成最新数）。
// 节点级流量做不到：子节点共用落地端口，中转机按端口计分不清节点，
// 只能显示所属落地的共享流量（见 loadCentralLandings 里的标注）。

// centralOnlineBadge 远程面板在线徽章。
function centralOnlineBadge(rm) {
  return '<span class="badge ' + (rm.online ? 'green' : 'red') + '">' + (rm.online ? '在线' : '离线') + '</span>';
}

// centralTrafficAgg 把一台远程面板全部落地的流量加总：今日/本月/累计
// 各自相加；已用是各落地 used_bytes 之和，上限是各落地 limit_bytes 之和
// （有限制的落地才计入上限）；pct = 已用/上限。
function centralTrafficAgg(landings) {
  let today = 0, month = 0, total = 0, used = 0, limit = 0, has = false;
  (landings || []).forEach((l) => {
    const t = l.traffic || {};
    if (t.today_up != null || t.today_down != null || t.total_up != null || t.total_down != null) has = true;
    today += (Number(t.today_up) || 0) + (Number(t.today_down) || 0);
    month += (Number(t.month_up) || 0) + (Number(t.month_down) || 0);
    total += (Number(t.total_up) || 0) + (Number(t.total_down) || 0);
    used += Number(t.used_bytes) || 0;
    if (t.limit_bytes) limit += Number(t.limit_bytes) || 0;
  });
  return { today, month, total, used, limit, pct: limit ? Math.min(100, Math.round(used / limit * 100)) : 0, has };
}

// centralTrafficMiniText 卡片上的流量汇总行，和本机小卡片的
// trafficMiniText 同一款式：有限制显示“已用 / 上限（%）”，不限只显示已用。
function centralTrafficMiniText(landings) {
  const a = centralTrafficAgg(landings);
  if (!a.has) return '暂无流量数据';
  if (!a.limit) return '不限 · 已用 ' + fmtBytes(a.used);
  return fmtBytes(a.used) + ' / ' + fmtBytes(a.limit) + '（' + a.pct + '%）';
}

// centralExpirySummary 一台远程面板的到期汇总：取全部落地里最近的到期
// （已到期的优先报已到期）；全部永久返回“全部永久”；没有落地返回“暂无落地”。
function centralExpirySummary(landings) {
  const list = (landings || []).filter((l) => l && l.days_left != null);
  if (!list.length) {
    return { text: (landings || []).length ? '全部永久' : '暂无落地', cls: 'grey' };
  }
  let nearest = list[0];
  list.forEach((l) => { if (l.days_left < nearest.days_left) nearest = l; });
  const name = centralLandingTitle(nearest);
  const d = nearest.days_left;
  if (d < 0) return { text: '已到期 ' + Math.abs(d) + ' 天（' + name + '）', cls: 'red' };
  if (d === 0) return { text: '今天到期（' + name + '）', cls: 'red' };
  return { text: '最早剩余 ' + d + ' 天（' + name + '）', cls: d <= 7 ? 'red' : d <= 30 ? 'yellow' : 'green' };
}

// centralDetailEl 集中管理详情弹窗里的落地容器（详情弹窗打开时才有）。
function centralDetailEl() { return $('#central-detail-modal'); }

// 集中管理页选项卡（v1.3.13）：三个大选项卡，顺序是已登记的远程面板（默认）、
// 登记远程面板、合并订阅。已登记的远程面板选项卡内部再按每个面板分子选项卡
// （一个远程面板一个子选项卡）。三层都复用设置页选项卡模式（原生 button＋
// aria-selected＋方向键，localStorage 记住上次；大选项卡、子选项卡、设置页
// 各用各的键）。
// 数据更新方式：loadCentral() 每次重拉 /api/v1/central/remotes 接口重绘全部
// 内容（隐藏的大选项卡、隐藏的子选项卡也一样画），切选项卡只是显隐、不额外
// 拉数据，所以切过去永远是最新数据；2 分钟轮询是后端 centralLoop，详情弹窗
// 照常工作（读 centralRemotes 缓存）。
const CENTRAL_TABS = ['list', 'add', 'sub'];
const CENTRAL_TAB_KEY = 'relay-panel-central-tab';
const CENTRAL_SUB_TAB_KEY = 'relay-panel-central-sub-tab';
function centralTabValid(id) { return CENTRAL_TABS.indexOf(id) >= 0; }
function showCentralTab(id) {
  if (!centralTabValid(id)) id = 'list';
  CENTRAL_TABS.forEach((t) => {
    const btn = document.getElementById('central-tab-' + t);
    if (btn) btn.setAttribute('aria-selected', t === id ? 'true' : 'false');
    setVisible(document.getElementById('central-panel-' + t), t === id);
  });
  try { localStorage.setItem(CENTRAL_TAB_KEY, id); } catch (e) { /* 隐私模式等存不了就每次回已登记 */ }
}
function initCentralTabs() {
  let saved = null;
  try { saved = localStorage.getItem(CENTRAL_TAB_KEY); } catch (e) { /* 读不到就用默认 */ }
  showCentralTab(centralTabValid(saved) ? saved : 'list');
  // 绑定只做一次：重复调用不能再叠加监听，否则一次按键会连跳多个选项卡。
  if (initCentralTabs.bound) return;
  initCentralTabs.bound = true;
  CENTRAL_TABS.forEach((t) => {
    on('#central-tab-' + t, 'click', () => showCentralTab(t));
  });
  const bar = document.querySelector('.central-tabs');
  if (bar) bar.addEventListener('keydown', (e) => {
    const cur = document.activeElement && document.activeElement.dataset
      ? document.activeElement.dataset.centralTab : null;
    if (!cur || !centralTabValid(cur)) return;
    let idx = CENTRAL_TABS.indexOf(cur);
    if (e.key === 'ArrowRight') idx = (idx + 1) % CENTRAL_TABS.length;
    else if (e.key === 'ArrowLeft') idx = (idx - 1 + CENTRAL_TABS.length) % CENTRAL_TABS.length;
    else if (e.key === 'Home') idx = 0;
    else if (e.key === 'End') idx = CENTRAL_TABS.length - 1;
    else return;
    e.preventDefault();
    showCentralTab(CENTRAL_TABS[idx]);
    const btn = document.getElementById('central-tab-' + CENTRAL_TABS[idx]);
    if (btn) btn.focus();
  });
}
// 子选项卡：一个远程面板一个，按钮与面板都由 loadCentral() 按当前登记列表重建
// （面板增删后跟着变）。正显示的面板被删了就回落到第一个子选项卡。
// 按钮是动态重建的，点按与方向键用事件委托（栏上只绑一次，不会叠加）。
function centralSubTabIds() { return (centralRemotes || []).map((rm) => rm.id); }
function showCentralSubTab(id) {
  const ids = centralSubTabIds();
  if (!ids.length) return;
  if (ids.indexOf(id) < 0) id = ids[0];
  ids.forEach((rid) => {
    const btn = document.getElementById('central-sub-tab-' + rid);
    if (btn) btn.setAttribute('aria-selected', rid === id ? 'true' : 'false');
    setVisible(document.getElementById('central-sub-panel-' + rid), rid === id);
  });
  try { localStorage.setItem(CENTRAL_SUB_TAB_KEY, id); } catch (e) { /* 存不了就每次回第一个 */ }
}
function initCentralSubTabs() {
  const bar = $('#central-sub-tabs');
  if (!bar || initCentralSubTabs.bound) return;
  initCentralSubTabs.bound = true;
  bar.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('[data-central-sub-tab]') : null;
    if (btn) showCentralSubTab(btn.dataset.centralSubTab);
  });
  bar.addEventListener('keydown', (e) => {
    const cur = document.activeElement && document.activeElement.dataset
      ? document.activeElement.dataset.centralSubTab : null;
    const ids = centralSubTabIds();
    if (!cur || ids.indexOf(cur) < 0) return;
    let idx = ids.indexOf(cur);
    if (e.key === 'ArrowRight') idx = (idx + 1) % ids.length;
    else if (e.key === 'ArrowLeft') idx = (idx - 1 + ids.length) % ids.length;
    else if (e.key === 'Home') idx = 0;
    else if (e.key === 'End') idx = ids.length - 1;
    else return;
    e.preventDefault();
    showCentralSubTab(ids[idx]);
    const btn = document.getElementById('central-sub-tab-' + ids[idx]);
    if (btn) btn.focus();
  });
}
function renderCentralSubTabs() {
  const bar = $('#central-sub-tabs');
  const holder = $('#central-sub-panels');
  if (!bar || !holder) return;
  const remotes = centralRemotes || [];
  if (!remotes.length) {
    bar.innerHTML = '';
    holder.innerHTML = '<div class="empty">还没登记远程面板。<br>到被管面板的设置页生成远程管理令牌，再到“登记远程面板”选项卡填地址和令牌登记。登记后这里会显示每台远程面板的在线状态、版本、落地数、流量汇总与最近到期时间。</div>';
    return;
  }
  bar.innerHTML = remotes.map((rm) =>
    '<button type="button" class="central-tab" role="tab" id="central-sub-tab-' + esc(rm.id) + '" aria-controls="central-sub-panel-' + esc(rm.id) + '" aria-selected="false" data-central-sub-tab="' + esc(rm.id) + '">' + esc(rm.name || rm.base_url) + '</button>').join('');
  holder.innerHTML = remotes.map((rm) =>
    '<div class="central-sub-panel" role="tabpanel" id="central-sub-panel-' + esc(rm.id) + '" aria-labelledby="central-sub-tab-' + esc(rm.id) + '" hidden>' + centralRemoteCardHtml(rm) + '</div>').join('');
  holder.querySelectorAll('[data-central-open]').forEach((b) => b.addEventListener('click', () => window.open(b.dataset.centralOpen, '_blank', 'noopener')));
  holder.querySelectorAll('[data-central-detail]').forEach((b) => b.addEventListener('click', () => openCentralDetail(b.dataset.centralDetail)));
  holder.querySelectorAll('[data-central-del]').forEach((b) => b.addEventListener('click', () => centralDeregister(b.dataset.centralDel)));
  holder.querySelectorAll('[data-central-token]').forEach((b) => b.addEventListener('click', () => centralChangeToken(b.dataset.centralToken)));
  holder.querySelectorAll('[data-central-edit]').forEach((b) => b.addEventListener('click', () => centralEditRemote(b.dataset.centralEdit)));
  let saved = null;
  try { saved = localStorage.getItem(CENTRAL_SUB_TAB_KEY); } catch (e) { /* 读不到就用第一个 */ }
  showCentralSubTab(saved);
}
// 一台远程面板的卡片（v1.3.4 的卡片内容，原样搬出 loadCentral 的 map 循环）。
function centralRemoteCardHtml(rm) {
  const landings = rm.landings || [];
  const onlineCount = landings.filter((l) => l.online === true).length;
  const offlineCount = landings.filter((l) => l.online === false).length;
  const unknownCount = landings.length - onlineCount - offlineCount;
  const exp = centralExpirySummary(landings);
  const t = centralTrafficAgg(landings);
  const trafficLine = t.has ? '今日 ' + fmtBytes(t.today) + ' · 本月 ' + fmtBytes(t.month) + ' · 累计 ' + fmtBytes(t.total) : '';
  const disk = (rm.disk_pct == null) ? '—' : Math.round(rm.disk_pct) + '%';
  const checked = rm.last_checked_at ? esc(fmtDate(rm.last_checked_at)) : '尚未拉取';
  const onlineLine = landings.length ? '（在线 ' + onlineCount + ' / 离线 ' + offlineCount + (unknownCount ? ' / 未知 ' + unknownCount : '') + '）' : '';
  return '<div class="mini-card static">' +
    '<div class="mini-top"><h3>' + esc(rm.name || rm.base_url) + '</h3><span>' + centralOnlineBadge(rm) +
    ' <span class="tag" title="这是登记在主面板上的另一台面板，数据由主面板每 2 分钟拉取；点“打开远程面板”跳到它自己的页面">远程面板</span></span></div>' +
    '<div class="mini-row"><span>地址</span><b class="mono" style="word-break:break-all">' + esc(rm.base_url) + '</b></div>' +
    '<div class="mini-row"><span>版本</span><b>' + esc(rm.version || '—') + '</b></div>' +
    '<div class="mini-row"><span>落地</span><b>' + landings.length + ' 个 ' + onlineLine + '</b></div>' +
    '<div class="mini-row"><span>流量</span><b>' + esc(centralTrafficMiniText(landings)) + '</b></div>' +
    (trafficLine ? '<div class="mini-row"><span></span><span class="small muted">' + esc(trafficLine) + '</span></div>' : '') +
    '<div class="mini-row"><span>到期</span><b><span class="badge ' + exp.cls + '">' + esc(exp.text) + '</span></b></div>' +
    '<div class="mini-row"><span>磁盘使用率</span><b>' + disk + '</b></div>' +
    '<div class="mini-row"><span>realm</span><b>' + (rm.realm_running ? '在跑' : '没在跑') + '</b></div>' +
    '<div class="mini-row"><span>上次检查</span><b>' + checked + '</b></div>' +
    (rm.online ? '' : '<div class="error-box">' + esc(rm.error || '远程面板连不上') + '</div>') +
    '<div class="btn-row">' +
    '<button class="btn sm" type="button" data-central-detail="' + esc(rm.id) + '">查看详情</button>' +
    '<button class="btn secondary sm" type="button" data-central-open="' + esc(rm.base_url) + '">打开远程面板 ↗</button>' +
    '<button class="btn secondary sm" type="button" data-central-edit="' + esc(rm.id) + '">编辑</button>' +
    '<button class="btn secondary sm" type="button" data-central-token="' + esc(rm.id) + '">更换令牌</button>' +
    '<button class="btn danger sm" type="button" data-central-del="' + esc(rm.id) + '">取消登记</button></div></div>';
}
async function loadCentral() {
  centralDetailId = null;
  loadCentralSubTree();
  loadSubGroups(); // v1.3.26：集中管理页生成表单的“所属分组”下拉也要有数据
  try {
    const r = await api('/api/v1/central/remotes');
    centralRemotes = r.remotes || [];
    centralNodeStats = r.node_stats || null;
    renderCentralNodeStats();
    renderCentralSubTabs();
  } catch (e) {
    centralNodeStats = null;
    renderCentralNodeStats();
    const holder = $('#central-sub-panels');
    if (holder) holder.innerHTML = '<div class="card"><div class="error-box">加载失败：' + esc(e.message) + '</div></div>';
  }
}

// ---------- 节点统计（v1.3.52） ----------
// 集中管理页顶部的节点统计：总数（中转/直连/其它）+ 按面板分行
// （本机一行、每台远程面板一行）。只计数，不标在线/离线——在线探测
// 只是 TCP 握手，标了会误导；远程失联时行上注明“离线，显示上次数据”，
// 数字是上次拉到的，不清零；从没拉成功过的标“尚未拉取”。
function centralNodeCountText(c) {
  c = c || {};
  const relay = Number(c.relay) || 0, direct = Number(c.direct) || 0, other = Number(c.other) || 0;
  return '共 ' + (relay + direct + other) + ' 个（中转 ' + relay + ' · 直连 ' + direct + ' · 其它 ' + other + '）';
}
function centralNodeStatsRowHtml(row) {
  const c = row.count || {};
  const relay = Number(c.relay) || 0, direct = Number(c.direct) || 0, other = Number(c.other) || 0;
  let note = '';
  if (row.never_polled) note = '（尚未拉取）';
  else if (row.offline) note = '（离线，显示上次数据）';
  return '<div class="mini-row"><span>' + esc(row.name || '—') + '</span><b>' + (relay + direct + other) + ' 个（中转 ' + relay + ' · 直连 ' + direct + ' · 其它 ' + other + '）' + esc(note) + '</b></div>';
}
function renderCentralNodeStats() {
  const box = $('#central-node-stats');
  if (!box) return;
  const st = centralNodeStats;
  if (!st) { box.innerHTML = ''; return; }
  let h = '<div class="card" style="margin-bottom:12px"><h3 style="margin:0 0 8px">节点统计</h3>' +
    '<div class="mini-row"><span>全部节点</span><b>' + esc(centralNodeCountText(st.total)) + '</b></div>';
  h += centralNodeStatsRowHtml(st.local || { name: '本机', count: {} });
  (st.remotes || []).forEach((row) => { h += centralNodeStatsRowHtml(row); });
  h += '<p class="small muted" style="margin:8px 0 0">只计数，不标在线/离线；数字跟着登记走，增删落地后刷新就变。</p></div>';
  box.innerHTML = h;
}

// ---------- 集中管理详情弹窗（v1.3.4） ----------
// 点卡片弹出该面板的详情：落地列表、每个落地的节点、状态、证书、流量。
// 落地列表按需加载（1.2.5 的懒加载思路：打开弹窗才拉，链接再点开才取）。
async function openCentralDetail(id) {
  const rm = centralRemotes.find((x) => x.id === id);
  if (!rm) { toast('找不到这台远程面板', 'err'); return; }
  const landings = rm.landings || [];
  const exp = centralExpirySummary(landings);
  const disk = (rm.disk_pct == null) ? '—' : Math.round(rm.disk_pct) + '%';
  const m = openModal(
    '<div style="display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap;align-items:center"><h2>' + esc(rm.name || rm.base_url) + '</h2><span>' + centralOnlineBadge(rm) +
    ' <span class="tag" title="这是登记在主面板上的另一台面板，数据由主面板每 2 分钟拉取；点“打开远程面板”跳到它自己的页面">远程面板</span></span></div>' +
    '<div class="kv"><span>地址</span><b class="mono" style="word-break:break-all">' + esc(rm.base_url) + '</b></div>' +
    '<div class="kv"><span>版本</span><b>' + esc(rm.version || '—') + ' · 磁盘 ' + disk + ' · ' + (rm.realm_running ? 'realm 在跑' : 'realm 没在跑') + '</b></div>' +
    '<div class="kv"><span>流量汇总</span><b id="central-detail-traffic">' + esc(centralTrafficMiniText(landings)) + '</b></div>' +
    '<div class="kv"><span>到期</span><b><span class="badge ' + exp.cls + '">' + esc(exp.text) + '</span></b></div>' +
    '<div class="btn-row">' +
    '<button class="btn secondary sm" type="button" data-cd-open="' + esc(rm.base_url) + '">打开远程面板 ↗</button>' +
    '<button class="btn secondary sm" type="button" data-cd-add="' + esc(id) + '">＋ 加落地</button>' +
    '<button class="btn secondary sm" type="button" data-cd-check="' + esc(id) + '">远程体检</button>' +
    '<button class="btn secondary sm" type="button" data-cd-realm="' + esc(id) + '">重启 realm</button>' +
    '<button class="btn secondary sm" type="button" data-cd-links="' + esc(id) + '">查看链接</button>' +
    '<button class="btn secondary sm" type="button" data-cd-token="' + esc(id) + '">更换令牌</button>' +
    '<button class="btn danger sm" type="button" data-cd-remove="' + esc(id) + '">取消登记</button></div>' +
    '<h3 style="margin-top:14px">落地（' + landings.length + ' 个）</h3>' +
    '<div id="central-detail-modal">加载中…</div>' +
    '<div class="btn-row"><button class="btn secondary" id="central-detail-close" type="button">关闭</button></div>', true);
  if (!m) return;
  onIn(m, '#central-detail-close', 'click', () => { centralDetailId = null; closeModal(); });
  $$('[data-cd-open]', m).forEach((b) => b.addEventListener('click', () => window.open(b.dataset.cdOpen, '_blank', 'noopener')));
  $$('[data-cd-add]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); centralAddLanding(b.dataset.cdAdd); }));
  $$('[data-cd-check]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); centralCheck(b.dataset.cdCheck); }));
  $$('[data-cd-realm]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); centralRestart(b.dataset.cdRealm); }));
  $$('[data-cd-links]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); centralLinks(b.dataset.cdLinks); }));
  $$('[data-cd-token]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); centralChangeToken(b.dataset.cdToken); }));
  $$('[data-cd-remove]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); centralDeregister(b.dataset.cdRemove); }));
  loadCentralLandings(id);
  // 流量汇总再从远程面板实时拉一次（central/traffic 代理接口，和快照
  // 读同一份流量库）：拉到了就更新成最新数，拉不到（远程离线/版本太旧）
  // 就留着快照里的数，不报错。
  try {
    const tr = await api('/api/v1/central/remotes/' + encodeURIComponent(id) + '/traffic');
    const el = $('#central-detail-traffic');
    if (el && tr && tr.summary) {
      const s2 = tr.summary;
      const used = Number(s2.used_bytes) || 0;
      const lim = Number(s2.limit_bytes) || 0;
      el.textContent = lim ? fmtBytes(used) + ' / ' + fmtBytes(lim) + '（' + Math.min(100, Number(s2.pct) || 0) + '%）' : '不限 · 已用 ' + fmtBytes(used);
      if (tr.note) el.title = tr.note;
    }
  } catch (e) { /* 快照里的数就是兜底 */ }
}
// 集中管理落地标题：有备注名称时“名称 地址”，没有时只显示地址；
// 名称与地址相同时也只显示一次（被管面板没设名称时 name 会回退成地址）。
function centralLandingTitle(l) {
  const addr = l.landing || l.domain || '';
  const name = (l.name || '').trim();
  if (name && name !== addr) return name + ' ' + addr;
  return addr;
}
// ---------- 合并订阅：按落地分层勾选 ----------
// 订阅的最小单位是单个落地：面板 → 下面列它的落地，每级都有勾选框；
// 勾面板即选它下面全部落地，部分选时面板框显示半选；顶上再放一个“全选全部面板”。
let centralSubTree = null; // [{id,name,online,landings:[{index,title}]}]
let centralSubChecked = {}; // "面板id:落地编号" -> true
function subTreeKey(panel, index) { return panel + ':' + index; }
function subTreeCheckedCount() {
  if (!centralSubTree) return 0;
  let n = 0;
  centralSubTree.forEach((p) => p.landings.forEach((l) => { if (centralSubChecked[subTreeKey(p.id, l.index)]) n++; }));
  return n;
}
// 取订阅勾选树的面板数据（本面板 + 已登记的远程面板，各带落地列表）：
// 集中管理页的勾选树与订阅编辑弹窗（v1.3.19）共用。管理面板模式没有本机
// 落地，订阅树里不列“本面板”，只列远程面板。
async function fetchSubTreePanels() {
  const r = await api('/api/v1/central/remotes');
  const remotes = r.remotes || [];
  const panels = [];
  if (!managerOnly) {
    if (!landingsCache.length) { try { await loadOverview(); } catch (e) { /* 总览读不到就只显示远程 */ } }
    panels.push({ id: 'local', name: '本面板', online: true, landings: (landingsCache || []).map((l) => ({ index: l.index, title: centralLandingTitle(l) })) });
  }
  for (const rm of remotes) {
    let landings = (rm.landings || []).map((l) => ({ index: l.index, title: centralLandingTitle(l) }));
    if (!landings.length && rm.online) {
      try {
        const d = await api('/api/v1/central/remotes/' + encodeURIComponent(rm.id) + '/landings');
        landings = (d.landings || []).map((l) => ({ index: l.index, title: centralLandingTitle(l) }));
      } catch (e) { /* 取不到就显示空 */ }
    }
    panels.push({ id: rm.id, name: rm.name, online: rm.online, landings });
  }
  return panels;
}
async function loadCentralSubTree() {
  const tree = $('#central-sub-tree');
  if (!tree) return;
  try {
    centralSubTree = await fetchSubTreePanels();
    renderCentralSubTree();
    await loadMergedSubsList();
  } catch (e) {
    tree.innerHTML = '<div class="error-box">加载失败：' + esc(e.message) + '</div>';
  }
}
function renderCentralSubTree() {
  const tree = $('#central-sub-tree');
  if (!tree || !centralSubTree) return;
  const total = centralSubTree.reduce((n, p) => n + p.landings.length, 0);
  let h = '<label class="checkbox-row"><input type="checkbox" id="central-sub-all"><span>全选全部面板（共 ' + total + ' 个落地）</span></label>';
  centralSubTree.forEach((p) => {
    h += '<div style="margin-top:10px"><label class="checkbox-row"><input type="checkbox" data-sub-panel="' + esc(p.id) + '"><b>' + esc(p.name) + '</b><span class="muted small">（' + p.landings.length + ' 个落地' + (p.online === false ? '，离线' : '') + '）</span></label>';
    if (!p.landings.length) {
      h += '<p class="muted small" style="margin:4px 0 0 28px">没有落地。</p>';
    } else {
      p.landings.forEach((l) => {
        const key = subTreeKey(p.id, l.index);
        h += '<div style="display:flex;align-items:center;gap:8px;margin:4px 0 0 28px"><label class="checkbox-row" style="margin:0;flex:1"><input type="checkbox" data-sub-landing="' + esc(key) + '"' + (centralSubChecked[key] ? ' checked' : '') + '><span>' + esc(l.title) + ' <span class="small">#' + esc(l.index) + '</span></span></label>' +
          '<button class="btn secondary sm" type="button" data-sub-copy-landing="' + esc(key) + '">复制订阅</button></div>';
      });
    }
    h += '</div>';
  });
  tree.innerHTML = h;
  on('#central-sub-all', 'change', (e) => {
    const on = e.target.checked;
    (centralSubTree || []).forEach((p) => p.landings.forEach((l) => {
      if (on) centralSubChecked[subTreeKey(p.id, l.index)] = true;
      else delete centralSubChecked[subTreeKey(p.id, l.index)];
    }));
    $$('[data-sub-panel]', tree).forEach((cb) => { cb.checked = on; cb.indeterminate = false; });
    $$('[data-sub-landing]', tree).forEach((cb) => { cb.checked = on; });
    refreshCentralSubTreeStates();
  });
  $$('[data-sub-panel]', tree).forEach((cb) => cb.addEventListener('change', () => {
    const pid = cb.dataset.subPanel;
    const panel = (centralSubTree || []).find((p) => p.id === pid);
    if (!panel) return;
    panel.landings.forEach((l) => {
      if (cb.checked) centralSubChecked[subTreeKey(pid, l.index)] = true;
      else delete centralSubChecked[subTreeKey(pid, l.index)];
    });
    $$('[data-sub-landing]', tree).forEach((lc) => { if (lc.dataset.subLanding.startsWith(pid + ':')) lc.checked = cb.checked; });
    refreshCentralSubTreeStates();
  }));
  $$('[data-sub-landing]', tree).forEach((cb) => cb.addEventListener('change', () => {
    if (cb.checked) centralSubChecked[cb.dataset.subLanding] = true;
    else delete centralSubChecked[cb.dataset.subLanding];
    refreshCentralSubTreeStates();
  }));
  $$('[data-sub-copy-landing]', tree).forEach((b) => b.addEventListener('click', () => {
    const key = b.dataset.subCopyLanding;
    const idx = key.lastIndexOf(':');
    const pid = key.slice(0, idx), li = Number(key.slice(idx + 1));
    const panel = (centralSubTree || []).find((p) => p.id === pid);
    const l = panel ? panel.landings.find((x) => x.index === li) : null;
    askSubModeForSelection([{ panel: pid, landing: li }], (l ? l.title : '落地') + ' 的订阅');
  }));
  refreshCentralSubTreeStates();
}
// 面板框半选态 + 全选框半选态 + 生成按钮可用状态
function refreshCentralSubTreeStates() {
  const tree = $('#central-sub-tree');
  if (!tree || !centralSubTree) return;
  $$('[data-sub-panel]', tree).forEach((cb) => {
    const pid = cb.dataset.subPanel;
    const panel = centralSubTree.find((p) => p.id === pid);
    if (!panel || !panel.landings.length) { cb.checked = false; cb.indeterminate = false; return; }
    const checked = panel.landings.filter((l) => centralSubChecked[subTreeKey(pid, l.index)]).length;
    cb.checked = checked === panel.landings.length;
    cb.indeterminate = checked > 0 && checked < panel.landings.length;
  });
  const all = $('#central-sub-all');
  const total = centralSubTree.reduce((n, p) => n + p.landings.length, 0);
  const checked = subTreeCheckedCount();
  if (all) {
    all.checked = total > 0 && checked === total;
    all.indeterminate = checked > 0 && checked < total;
  }
  const gen = $('#central-sub-generate');
  if (gen) gen.disabled = checked === 0;
}
// 已生成的合并订阅列表（只列勾选内容与生成时间，不含链接本身）。
// v1.3.20：合并订阅列表搜索（名字/勾选摘要）+ 分页（每页 20 条）+ 到期徽章。
let mergedSubPage = 1, mergedSubSearch = '';
async function loadMergedSubsList() {
  const list = $('#central-sub-list');
  if (!list) return;
  try {
    const r = await api('/api/v1/subscriptions/merged');
    const subs = r.subs || [];
    if (!subs.length) { list.innerHTML = '<p class="muted small">还没有合并订阅：上面勾选落地后点“生成合并订阅”。</p>'; return; }
    const filtered = subs.filter((s) => matchSubText(mergedSubSearch, [s.name, s.summary]));
    if (!filtered.length) { list.innerHTML = '<p class="muted small">没有匹配的合并订阅，换个关键词试试。</p>'; return; }
    const pg = paginate(filtered, mergedSubPage);
    mergedSubPage = pg.cur;
    list.innerHTML = '<h3>已生成的合并订阅</h3>' + pg.slice.map((s) => '<div class="kv"><span>' + (s.name ? esc(s.name) + ' — ' : '') + (s.group_name ? '<span class="badge">分组：' + esc(s.group_name) + '</span> ' : '') + esc(s.summary || '') + '<br><span class="small muted">内容' + esc(subModeLabel(s.mode)) + ' · ' + expiryBadge(s.expires_at) + ' · 生成于 ' + esc(fmtDate(s.created_at)) + '</span><br><span class="small">' + userTrafficText(s) + '</span></span><span><button class="btn secondary sm" type="button" data-sub-menu-merged="' + esc(s.id) + '">操作</button></span></div>').join('') +
      pagerHtml(pg.total, pg.cur, pg.totalPages, 'mergedsub') +
      '<p class="muted small">编辑只改勾选与内容，链接不变；每条订阅都有“复制”按钮，链接加密存在面板里，随时能重新复制；重新勾选生成（不填名称时同一选择重新生成会轮换；填了名称是新起一条，各归各人）。</p>';
    if (pg.totalPages > 1) wirePager(list, 'mergedsub', pg.cur, pg.totalPages, (p) => { mergedSubPage = p; loadMergedSubsList(); });
    $$('[data-sub-menu-merged]', list).forEach((b) => b.addEventListener('click', () => {
      const s = subs.find((x) => x.id === b.dataset.subMenuMerged);
      if (!s) return;
      openUserOpsMenu('merged', s);
    }));
  } catch (e) { list.innerHTML = '<div class="error-box">加载失败：' + esc(e.message) + '</div>'; }
}
// 轮换一条合并订阅（不含确认框，确认由调用方做）：旧链接立即失效，
// 新链接在弹窗里直接显示，之后随时能复制。
async function rotateMergedSub(id, name) {
  const r = await apiPost('/api/v1/subscriptions/merged/' + encodeURIComponent(id) + '/rotate', {});
  openMergedSubModal(r.url || '', { title: '订阅链接', rotated: true, name: name || undefined, mergedId: r.id, mode: r.mode });
  loadMergedSubsList();
  return r;
}
// 集中管理落地详情字段（v1.3.21）：行头与详情弹窗共用同一份计算，改一处两处都变。
function centralLandingFacts(l) {
  const ports = (l.ports || []).map((p) => esc(p.proto) + ' ' + esc(p.listen) + '→' + esc(p.remote)).join('、') || '—';
  const on = l.online === true ? '<span class="badge green">在线</span>' : l.online === false ? '<span class="badge red">离线</span>' : '<span class="badge">未知</span>';
  const lat = l.latency_ms == null ? '—' : esc(l.latency_ms) + ' ms';
  const cert = l.cert_days_left == null ? '—' : esc(l.cert_days_left) + ' 天' + (l.renew_mode ? '（' + esc(l.renew_mode) + '）' : '');
  const exp = l.days_left == null ? '永久' : (l.days_left < 0 ? '已到期 ' + esc(-l.days_left) + ' 天' : '剩余 ' + esc(l.days_left) + ' 天');
  const tr = l.traffic || {};
  // v1.3.32：agent 在线时流量是节点侧推来的总量，标来源。
  const src = l.metering_source === 'agent' ? ' · 节点侧' : '';
  const traffic = '今日 ' + esc(fmtBytes((tr.today_up || 0) + (tr.today_down || 0))) + ' / 本月 ' + esc(fmtBytes((tr.month_up || 0) + (tr.month_down || 0))) + ' / 累计 ' + esc(fmtBytes((tr.total_up || 0) + (tr.total_down || 0))) + (tr.limit_bytes ? '（上限 ' + esc(fmtBytes(tr.limit_bytes)) + '，已用 ' + esc(Math.round(tr.pct || 0)) + '%）' : '（不限）') + src;
  const subs = (l.subnodes || []).map((sn) =>
    '<div class="kv"><span>' + esc(sn.domain) + '</span><span><button class="btn danger sm" type="button" data-central-delsub="' + esc(l.index) + '|' + esc(sn.domain) + '">删除节点</button></span></div>'
  ).join('');
  // 行头也放到期与流量（和本机卡片同一款式）：不用点开就看得到。
  const expCls = l.days_left == null ? 'grey' : l.days_left < 0 ? 'red' : l.days_left <= 7 ? 'red' : l.days_left <= 30 ? 'yellow' : 'green';
  const trafficHead = '今日 ' + esc(fmtBytes((tr.today_up || 0) + (tr.today_down || 0))) + ' · 累计 ' + esc(fmtBytes((tr.total_up || 0) + (tr.total_down || 0))) + src;
  // 子节点没有独立流量：同一落地的节点共用一份流量（中转机按端口统计，
  // 分不清节点），下面正文里的流量就是该落地的共享流量，别当成单个节点的。
  const subShared = (l.subnodes && l.subnodes.length)
    ? '<div class="small muted">子节点流量：同一落地的节点共用一份流量（中转机按端口统计，分不清节点），下面是该落地的共享流量。</div>'
    : '';
  return { ports, on, lat, cert, exp, expCls, traffic, trafficHead, subs, subShared };
}
// 集中管理落地详情正文（v1.3.21）：以前是详情弹窗里的手风琴展开体，现在是
// 点行弹出的独立落地详情弹窗的正文（kv + 子节点 + 链接与二维码 + 操作按钮）。
function centralLandingBodyHtml(l) {
  const f = centralLandingFacts(l);
  return f.subShared +
    '<div class="kv"><span>落地</span><b>' + esc(centralLandingTitle(l)) + '</b></div>' +
    '<div class="kv"><span>监听端口</span><b>' + f.ports + '</b></div>' +
    '<div class="kv"><span>证书</span><b>' + f.cert + '</b></div>' +
    '<div class="kv"><span>到期</span><b>' + f.exp + '</b></div>' +
    '<div class="kv"><span>流量</span><b>' + f.traffic + '</b></div>' +
    (f.subs ? '<div class="kv"><span>子节点</span><b>' + esc((l.subnodes || []).length) + ' 个</b></div>' + f.subs : '') +
    '<div class="central-landing-links" data-central-links-box="' + esc(l.index) + '"></div>' +
    '<div class="btn-row"><button class="btn secondary sm" type="button" data-central-addnode="' + esc(l.index) + '">＋ 新增节点</button>' +
    '<button class="btn secondary sm" type="button" data-central-edit="' + esc(l.index) + '">编辑落地</button>' +
    '<button class="btn secondary sm" type="button" data-central-renew="' + esc(l.index) + '">续期</button>' +
    '<button class="btn secondary sm" type="button" data-central-traffic="' + esc(l.index) + '">流量设置</button>' +
    '<button class="btn secondary sm" type="button" data-central-addtraffic="' + esc(l.index) + '">续量</button>' +
    '<button class="btn secondary sm" type="button" data-central-origlink="' + esc(l.index) + '">补链接</button>' +
    '<button class="btn secondary sm" type="button" data-central-certs="' + esc(l.index) + '">证书</button>' +
    '<button class="btn secondary sm" type="button" data-central-sub-landing="' + esc(l.index) + '">订阅</button>' +
    '<button class="btn danger sm" type="button" data-central-del-landing="' + esc(l.index) + '">删除该落地</button></div>';
}
async function loadCentralLandings(id, page) {
  if (page) centralDetailPage = page;
  centralDetailId = id;
  if (centralDetailLinksRemote !== id) { centralDetailLinksGroups = null; centralDetailLinksRemote = null; }
  const rm = centralRemotes.find((x) => x.id === id);
  const detail = centralDetailEl();
  if (!detail) { // 详情弹窗没开（直接调了这个函数）：先开弹窗，弹窗里会再调一次拉取
    openCentralDetail(id);
    return;
  }
  detail.innerHTML = '加载中…';
  try {
    const r = await api('/api/v1/central/remotes/' + encodeURIComponent(id) + '/landings');
    centralDetailLandings = r.landings || [];
    centralDetailRelayAddr = (r.remote && r.remote.relay_addr) || '';
    const pg = paginate(centralDetailLandings, centralDetailPage);
    centralDetailPage = pg.cur;
    const cachedNote = r.note ? '<div class="warn-box">' + esc(r.note) + '</div>' : '';
    if (!centralDetailLandings.length) {
      detail.innerHTML = cachedNote + '<div class="empty">这台远程面板还没有落地。</div>';
      return;
    }
    detail.innerHTML = cachedNote +
      '<p class="muted small">点一行弹出它的详情（链接、二维码与操作）。实时日志不代理，要看日志点上面卡片里的“打开远程面板”跳过去。</p>' +
      pg.slice.map((l) => {
        const f = centralLandingFacts(l);
        return '<div class="central-landing">' +
          '<div class="central-landing-head" data-central-toggle="' + esc(l.index) + '" role="button" tabindex="0"><span>' + esc(centralLandingTitle(l)) + ' <span class="small">#' + esc(l.index) + '</span></span><span>' + f.on + ' ' + f.lat +
          ' <span class="small">到期 <span class="badge ' + f.expCls + '">' + f.exp + '</span></span>' +
          ' <span class="small muted">' + f.trafficHead + '</span>' +
          ' <span class="central-caret">▸</span></span></div></div>';
      }).join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'central');
    $$('[data-central-toggle]', detail).forEach((h) => {
      const open = () => showCentralLandingDetail(id, Number(h.dataset.centralToggle));
      h.addEventListener('click', open);
      h.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(); } });
    });
    if (pg.totalPages > 1) wirePager(detail, 'central', pg.cur, pg.totalPages, (p) => loadCentralLandings(id, p));
  } catch (e) {
    detail.innerHTML = '<div class="error-box">加载失败：' + esc(e.message) + '</div>';
  }
}
// 集中管理落地详情弹窗（v1.3.21）：以前是详情弹窗里的手风琴展开体，现在点行
// 弹独立的落地详情弹窗（与本机落地详情弹窗同一套样式）。返回按钮回到远程面板
// 详情弹窗（重新拉取，回到全部收起）。
function showCentralLandingDetail(remoteId, index) {
  const l = centralDetailLandings.find((x) => x.index === index);
  if (!l) return;
  const m = openModal('<h2>' + esc(centralLandingTitle(l)) + '</h2>' + centralLandingBodyHtml(l) +
    '<div class="btn-row" style="margin-top:12px"><button class="btn secondary" type="button" data-central-back>返回</button></div>', true);
  if (!m) return;
  onIn(m, '[data-central-back]', 'click', () => { closeModal(); openCentralDetail(remoteId); });
  $$('[data-central-addnode]', m).forEach((b) => b.addEventListener('click', () => {
    centralAddNodeWizard(remoteId, Number(b.dataset.centralAddnode), centralLandingTitle(l));
  }));
  $$('[data-central-edit]', m).forEach((b) => b.addEventListener('click', () => {
    centralEditLanding(remoteId, Number(b.dataset.centralEdit), l);
  }));
  $$('[data-central-renew]', m).forEach((b) => b.addEventListener('click', () => centralRenewLanding(remoteId, Number(b.dataset.centralRenew))));
  $$('[data-central-traffic]', m).forEach((b) => b.addEventListener('click', () => {
    centralTrafficSettings(remoteId, Number(b.dataset.centralTraffic), l);
  }));
  $$('[data-central-addtraffic]', m).forEach((b) => b.addEventListener('click', () => centralAddTraffic(remoteId, Number(b.dataset.centralAddtraffic))));
  $$('[data-central-origlink]', m).forEach((b) => b.addEventListener('click', () => {
    centralPasteOrigLink(remoteId, Number(b.dataset.centralOriglink), l);
  }));
  $$('[data-central-certs]', m).forEach((b) => b.addEventListener('click', () => centralCerts(remoteId)));
  $$('[data-central-delsub]', m).forEach((b) => b.addEventListener('click', () => {
    const parts = String(b.dataset.centralDelsub).split('|');
    centralDeleteSubnode(remoteId, Number(parts[0]), parts[1]);
  }));
  $$('[data-central-sub-landing]', m).forEach((b) => b.addEventListener('click', () => {
    askSubModeForSelection([{ panel: remoteId, landing: Number(b.dataset.centralSubLanding) }], centralLandingTitle(l) + ' 的订阅');
  }));
  $$('[data-central-del-landing]', m).forEach((b) => b.addEventListener('click', () => centralDeleteLanding(remoteId, Number(b.dataset.centralDelLanding))));
  centralLoadLandingLinks(remoteId, index, m);
}
async function centralLoadLandingLinks(remoteId, index, rootEl) {
  const detail = rootEl || centralDetailEl();
  const box = detail ? $('[data-central-links-box="' + index + '"]', detail) : null;
  if (!box || box.dataset.loaded) return;
  box.innerHTML = '<p class="muted">链接加载中…</p>';
  try {
    if (centralDetailLinksRemote !== remoteId || !centralDetailLinksGroups) {
      const r = await api('/api/v1/central/remotes/' + encodeURIComponent(remoteId) + '/links');
      centralDetailLinksGroups = r.groups || [];
      centralDetailLinksRemote = remoteId;
    }
    const l = centralDetailLandings.find((x) => x.index === index);
    const g = (centralDetailLinksGroups || []).find((x) => x.landing_index === index) ||
      (centralDetailLinksGroups || []).find((x) => l && x.landing === l.landing);
    const boxNow = detail ? $('[data-central-links-box="' + index + '"]', detail) : null;
    if (!boxNow) return; // 列表已重渲染（翻页等），不写回旧节点
    if (!g || !(g.links || []).length) { boxNow.innerHTML = '<p class="muted">暂无链接：先在远程面板的节点链接页生成，或给这个落地粘贴原始链接。</p>'; }
    else { boxNow.innerHTML = linksHtml(g.links, g.qr || {}); }
    boxNow.dataset.loaded = '1';
  } catch (e) {
    const boxNow = detail ? $('[data-central-links-box="' + index + '"]', detail) : null;
    if (boxNow) boxNow.innerHTML = '<div class="error-box">链接加载失败：' + esc(e.message) + '</div>';
  }
}
async function centralLinks(id) {
  const rm = centralRemotes.find((x) => x.id === id);
  const m = openModal('<h2>' + esc(rm ? rm.name : '') + ' 的节点链接</h2><div id="central-links-body">加载中…</div><div class="btn-row"><button class="btn secondary" id="central-links-close" type="button">关闭</button></div>');
  onIn(m, '#central-links-close', 'click', closeModal);
  try {
    const r = await api('/api/v1/central/remotes/' + encodeURIComponent(id) + '/links');
    const groups = r.groups || [];
    const body = $('#central-links-body', m);
    if (!groups.length) { body.innerHTML = '<div class="empty">这台远程面板还没有节点链接。</div>'; return; }
    // 默认全收起：只列落地名，点哪行展开哪行的链接与二维码（单开手风琴）
    let openGroup = null;
    body.innerHTML = '<p class="muted small">点一行展开看它的链接与二维码；一次只展开一个，再点一次收起。</p>' + groups.map((g, i) =>
      '<div class="central-link-group">' +
      '<div class="central-link-head" data-central-group-toggle="' + i + '" role="button" tabindex="0"><span>' + esc(centralLandingTitle(g)) + '</span><span>' + groupTagsHtml(g) + ' <span class="central-caret">▸</span></span></div>' +
      '<div class="central-link-body hidden" data-central-group-body="' + i + '">' + linksHtml(g.links || [], g.qr || {}) + '</div></div>'
    ).join('');
    $$('[data-central-group-toggle]', body).forEach((h) => {
      const toggle = () => {
        const i = Number(h.dataset.centralGroupToggle);
        const wasOpen = openGroup === i;
        $$('.central-link-body', body).forEach((b) => b.classList.add('hidden'));
        $$('.central-caret', body).forEach((c) => { c.textContent = '▸'; });
        if (wasOpen) { openGroup = null; return; }
        openGroup = i;
        const b = $('[data-central-group-body="' + i + '"]', body);
        if (b) b.classList.remove('hidden');
        const c = h.querySelector('.central-caret');
        if (c) c.textContent = '▾';
      };
      h.addEventListener('click', toggle);
      h.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(); } });
    });
  } catch (e) {
    const body = $('#central-links-body', m);
    if (body) body.innerHTML = '<div class="error-box">加载失败：' + esc(e.message) + '</div>';
  }
}
function centralAddLanding(id) {
  const rm = centralRemotes.find((x) => x.id === id);
  const m = openModal('<h2>给 ' + esc(rm ? rm.name : '') + ' 加落地</h2>' +
    '<p class="muted">提交后主面板会先校验，再用令牌调远程面板加落地；远程面板跑安装脚本要一会儿，请耐心等。字段与本地加落地一致。</p>' +
    '<label>落地 IP 或域名 <span class="hint">必填，如 203.0.113.1</span></label><input id="central-add-landing" placeholder="203.0.113.1">' +
    '<label>名称 / 备注 <span class="hint">选填，显示名，如 香港-01；空着显示落地地址</span></label><input id="central-add-name" placeholder="如 香港-01" maxlength="64">' +
    '<label>原始节点链接 <span class="hint">选填，落地机上生成的节点链接（vless:// 等开头）；填了卡片上直接出中转链接与二维码，不填以后也能在管理里补粘贴</span></label><input id="central-add-orig-link" placeholder="vless://11111111-2222-3333-4444-555555555555@203.0.113.1:443?security=tls&sni=example.com#示例">' +
    '<label>节点名称 <span class="hint">选填，生成链接时的备注名；空着用原始链接自己的备注名</span></label><input id="central-add-link-name" placeholder="如 香港-01" maxlength="64">' +
    '<label>预设 <span class="hint">落地机装的节点类型</span></label><select id="central-add-preset"><option value="1">预设 1：landing-node（Xray 核心）</option><option value="2">预设 2：landing-node（sing-box 核心）</option><option value="3">预设 3：自定义端口</option></select>' +
    '<div id="central-add-sb-wrap" class="hidden"><label>sing-box 模式</label><select id="central-add-sb-mode"><option value="1">TLS（443）</option><option value="2">明文（8080）</option></select></div>' +
    '<div id="central-add-custom-wrap" class="hidden"><label>自定义端口 <span class="hint">必填，逗号分隔，如 443,8388；可写 监听:落地，如 10443:443</span></label><input id="central-add-custom" placeholder="443,8388"></div>' +
    '<label>模式 <span class="hint">任意门：节点域名永远指落地，中转只做端口转发</span></label><select id="central-add-mode"><option value="standard" selected>标准模式</option><option value="door">任意门模式</option></select>' +
    '<div id="central-add-relay-wrap" class="hidden"><label>中转地址 <span class="hint">任意门链接用这个地址生成</span></label><input id="central-add-relay-addr" placeholder="留空 = 自动用中转机公网 IP"></div>' +
    '<label>证书域名 <span class="hint">用于签证书，如 example.com</span></label><input id="central-add-cert" placeholder="example.com">' +
    '<label>证书签发方式 <span class="hint">决定证书怎么签、怎么续签；要和落地机装节点时选的方式一致</span></label><select id="central-add-cert-mode"><option value="http01" selected>默认走 80（HTTP-01）</option><option value="dns01">DNS 验证 Cloudflare（DNS-01）</option></select>' +
    '<p class="small">默认走 80：落地机装节点时用 80 端口验证域名签证书，最常用、不用多配；代价是 80 必须直通落地，改了监听端口或域名指着中转时，续签要手动把域名改回指向落地。DNS 验证：落地机用 Cloudflare 令牌在 DNS 里加验证记录签证书，续签全自动，域名指哪都行、不用 80；前提是装节点时选了 DNS 验证并配好令牌，没配令牌的落地别选这个。</p>' +
    '<label>节点时长</label><select id="central-add-duration"><option value="0">永久（默认）</option><option value="30">30 天</option><option value="90">90 天</option><option value="180">180 天</option><option value="365">365 天</option><option value="custom">自定义天数</option></select>' +
    '<div id="central-add-duration-wrap" class="hidden"><label>自定义天数</label><input id="central-add-duration-custom" type="number" min="1" max="3650" placeholder="如 60"></div>' +
    '<label>流量上限（GB）<span class="hint">0 = 不限</span></label><input id="central-add-traffic" type="number" min="0" step="1" placeholder="0">' +
    '<label>流量上限算法</label><select id="central-add-traffic-mode"><option value="total">总量（整个生命周期）</option><option value="monthly">按月（每月重置）</option></select>' +
    '<label>流量倍率 <span class="hint">实际流量 × 倍率计入订阅用量，默认 1；贵的线路调高</span></label><input id="central-add-traffic-multiplier" type="number" min="0.1" max="100" step="0.1" placeholder="1">' +
    '<label>限速（Mbps）<span class="hint">0 = 不限速</span></label><input id="central-add-speed" type="number" min="0" step="1" placeholder="0">' +
    '<div class="checkbox-row"><input type="checkbox" id="central-add-want-udp"><label for="central-add-want-udp" style="margin:0;font-weight:400">同时转发 UDP（HY2 / TUIC 需要）</label></div>' +
    '<div class="btn-row"><button class="btn" id="central-add-ok" type="button">提交并添加落地</button><button class="btn secondary" id="central-add-cancel" type="button">取消</button></div>');
  onIn(m, '#central-add-cancel', 'click', closeModal);
  onIn(m, '#central-add-preset', 'change', () => {
    const v = $('#central-add-preset', m).value;
    const sb = $('#central-add-sb-wrap', m);
    const cw = $('#central-add-custom-wrap', m);
    if (sb) sb.classList.toggle('hidden', v !== '2');
    if (cw) cw.classList.toggle('hidden', v !== '3');
  });
  onIn(m, '#central-add-mode', 'change', () => {
    const w = $('#central-add-relay-wrap', m);
    if (w) w.classList.toggle('hidden', $('#central-add-mode', m).value !== 'door');
  });
  onIn(m, '#central-add-duration', 'change', () => {
    const w = $('#central-add-duration-wrap', m);
    if (w) w.classList.toggle('hidden', $('#central-add-duration', m).value !== 'custom');
  });
  onIn(m, '#central-add-ok', 'click', async () => {
    const payload = buildCentralAddPayload(m);
    if (!payload) return;
    closeModal();
    toast('正在远程加落地，请稍等…');
    try {
      const d = await centralSubmitAdd(id, payload, null);
      if (d.ok === false) {
        if (d.need_choice) {
          await showShareChoice(d, payload, async (choice) => {
            try {
              const d2 = await centralSubmitAdd(id, payload, choice);
              if (d2.ok === false) { toast(centralOpError(d2), 'err'); return; }
              toast('远程加落地成功', 'ok');
              showCentralAddResult(d2);
            } catch (err) { toast('远程加落地失败：' + err.message, 'err'); }
          });
          return;
        }
        toast(centralOpError(d), 'err'); return;
      }
      toast('远程加落地成功', 'ok');
      showCentralAddResult(d);
    } catch (err) { toast('远程加落地失败：' + err.message, 'err'); }
  });
}
// 集中管理加落地：字段与本地 buildAddPayload 一致（名称/原始链接/节点名称/
// 模式+中转地址/sing-box 模式/自定义端口/签发方式/时长/流量/限速/UDP）。
function buildCentralAddPayload(m) {
  const landing = $('#central-add-landing', m).value.trim();
  const certDomain = $('#central-add-cert', m).value.trim();
  if (!landing) { toast('请填写落地 IP 或域名', 'err'); return null; }
  if (!/^[A-Za-z0-9.-]+$/.test(landing)) { toast('落地 IP 或域名格式不正确', 'err'); return null; }
  if (certDomain && !/^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/.test(certDomain)) { toast('证书域名格式不正确，如 example.com', 'err'); return null; }
  const preset = $('#central-add-preset', m).value;
  let customPorts = '';
  if (preset === '3') {
    customPorts = $('#central-add-custom', m).value.trim();
    if (!customPorts) { toast('自定义端口必填，如 443,8388', 'err'); return null; }
    if (!/^[\d,: ]+$/.test(customPorts)) { toast('自定义端口格式不正确，只能是数字、逗号和冒号', 'err'); return null; }
  }
  let duration = Number($('#central-add-duration', m).value) || 0;
  if ($('#central-add-duration', m).value === 'custom') {
    duration = Number($('#central-add-duration-custom', m).value) || 0;
    if (duration < 1 || duration > 3650) { toast('自定义天数请填 1–3650', 'err'); return null; }
  }
  const trafficGb = Number($('#central-add-traffic', m).value) || 0;
  if (trafficGb < 0) { toast('流量上限不能为负', 'err'); return null; }
  const speed = Number($('#central-add-speed', m).value) || 0;
  if (speed < 0) { toast('限速不能为负', 'err'); return null; }
  const mult = parseTrafficMultiplier($('#central-add-traffic-multiplier', m));
  if (mult == null) return null;
  const mode = $('#central-add-mode', m).value;
  const relayAddr = (mode === 'door' && $('#central-add-relay-addr', m)) ? $('#central-add-relay-addr', m).value.trim() : '';
  const name = $('#central-add-name', m).value.trim();
  if (name && name.length > 64) { toast('名称不能超过 64 字', 'err'); return null; }
  const origLink = $('#central-add-orig-link', m).value.trim();
  if (origLink && !/^(vless|trojan|ss|vmess|hysteria2|hy2|tuic):\/\//.test(origLink)) { toast('原始节点链接格式不正确，需以 vless://、trojan://、ss://、vmess://、hysteria2:// 或 tuic:// 开头', 'err'); return null; }
  const linkName = $('#central-add-link-name', m).value.trim();
  if (linkName && linkName.length > 64) { toast('节点名称不能超过 64 字', 'err'); return null; }
  return {
    landing, preset, mode,
    name: name || undefined,
    orig_link: origLink || undefined,
    link_name: linkName || undefined,
    relay_addr: relayAddr || undefined,
    sb_mode: preset === '2' ? $('#central-add-sb-mode', m).value : undefined,
    custom_ports: customPorts || undefined,
    want_udp: $('#central-add-want-udp', m).checked,
    cert_domain: certDomain || undefined,
    cert_mode: $('#central-add-cert-mode', m).value,
    duration_days: duration,
    traffic_limit_bytes: Math.round(trafficGb * 1073741824),
    traffic_limit_mode: trafficGb > 0 ? $('#central-add-traffic-mode', m).value : undefined,
    traffic_multiplier: mult,
    speed_limit_mbps: speed,
  };
}
async function centralSubmitAdd(id, payload, shareChoice) {
  const body = Object.assign({}, payload);
  if (shareChoice) { body.share_mode = shareChoice.mode; if (shareChoice.overrides) body.listen_overrides = shareChoice.overrides; }
  return apiPostRaw('/api/v1/central/remotes/' + encodeURIComponent(id) + '/landings', body);
}
function showCentralAddResult(d) {
  openModal('<h2>✅ 远程落地添加成功</h2>' +
    (d.door_note ? '<div class="warn-box">' + esc(d.door_note) + '</div>' : '') +
    (d.manual ? '<div class="warn-box">' + esc(d.manual) + '</div>' : '') +
    ((d.qr && Object.keys(d.qr).length) ? '' : '<p class="muted">二维码生成失败，仅显示链接。</p>') +
    linksHtml(d.links, d.qr) +
    '<div class="btn-row"><button class="btn" id="central-add-done" type="button">完成</button></div>');
  on('#central-add-done', 'click', () => { closeModal(); loadCentral(); });
}
/* ---------- 远程给落地加节点：两步向导（与本地 addNodeWizard 同款） ---------- */
async function centralAddNodeWizard(remoteId, index, label) {
  const m = openModal('<h2>给远程落地加节点（第 1 步，共 2 步）</h2>' +
    '<p>面板在中转机上，够不着落地机，所以分两步走：先填新子域名，拿到落地机命令；到落地机跑完命令，再把新链接粘贴回来登记。</p>' +
    '<p class="small">远程落地：' + esc(label || index) + '</p>' +
    '<label>新子域名 <span class="hint">如 node2.example.com</span></label><input id="central-wizard-sub" placeholder="node2.example.com">' +
    '<div class="btn-row"><button class="btn" id="central-wz-next" type="button">生成落地机命令</button><button class="btn secondary" id="central-wz-cancel" type="button">取消</button></div>');
  onIn(m, '#central-wz-cancel', 'click', closeModal);
  onIn(m, '#central-wz-next', 'click', async () => {
    const sub = $('#central-wizard-sub', m).value.trim();
    if (!sub || !/^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/.test(sub)) { toast('请填写正确的子域名，如 node2.example.com', 'err'); return; }
    try {
      const r = await apiPostRaw('/api/v1/central/remotes/' + encodeURIComponent(remoteId) + '/landings/' + index + '/add-node', { subdomain: sub });
      if (r.ok === false) { toast(centralOpError(r), 'err'); return; }
      const cmd = (r.wizard && r.wizard.landing_cmd) || r.landing_cmd || '';
      const m2 = openModal('<h2>给远程落地加节点（第 2 步，共 2 步）</h2>' +
        '<p>到落地机上执行下面这条命令，跑完会生成新节点链接：</p>' +
        (cmd ? '<pre class="cmd">' + esc(cmd) + '</pre><div class="btn-row"><button class="btn secondary sm" type="button" data-copy="' + esc(cmd) + '">复制命令</button></div>' : '<p class="muted">后端未返回命令，请联系管理员确认命令模板。</p>') +
        '<label>粘贴新节点链接 <span class="hint">落地机跑完命令后生成的那条链接</span></label><input id="central-wz-link" placeholder="vless://…@' + esc(sub) + ':443?…">' +
        '<div class="btn-row"><button class="btn" id="central-wz-done" type="button">登记并生成 twin</button><button class="btn secondary" id="central-wz-back" type="button">上一步</button></div>');
      onIn(m2, '#central-wz-back', 'click', () => centralAddNodeWizard(remoteId, index, label));
      onIn(m2, '#central-wz-done', 'click', async () => {
        const link = $('#central-wz-link', m2).value.trim();
        if (!link || !/^(vless|trojan|ss|vmess|hysteria2|hy2|tuic):\/\//.test(link)) { toast('请粘贴正确的节点链接（vless:// 等开头）', 'err'); return; }
        try {
          const r2 = await apiPostRaw('/api/v1/central/remotes/' + encodeURIComponent(remoteId) + '/landings/' + index + '/add-node', { subdomain: sub, new_link: link });
          if (r2.ok === false) { toast(centralOpError(r2), 'err'); return; }
          closeModal();
          openModal('<h2>✅ 子节点已登记</h2>' + linksHtml(r2.links, r2.qr) +
            '<div class="btn-row"><button class="btn" id="central-wz-close" type="button">完成</button></div>');
          on('#central-wz-close', 'click', () => { closeModal(); if (centralDetailId === remoteId) loadCentralLandings(remoteId); });
        } catch (e) { toast('登记失败：' + e.message, 'err'); }
      });
    } catch (e) { toast('生成命令失败：' + e.message, 'err'); }
  });
}
async function centralDeleteLanding(id, index) {
  const ok = await confirmModal('删除远程落地', '<p>确定删除这台远程面板上的落地 <b>#' + index + '</b> 吗？删除是软删除：先进入远程面板的回收站保留 7 天。</p>', '删除（进回收站）', true);
  if (!ok) return;
  try {
    const d = await apiRaw('/api/v1/central/remotes/' + encodeURIComponent(id) + '/landings/' + index, { method: 'DELETE' });
    if (d.ok === false) { toast(centralOpError(d), 'err'); return; }
    toast('远程落地已删除（进回收站）', 'ok');
    loadCentral();
    if (centralDetailId === id) loadCentralLandings(id);
  } catch (err) { toast('删除失败：' + err.message, 'err'); }
}
// ---------- 远程编辑落地：表单与本地 editLanding 同款，提交走代理接口 ----------
// 预填值来自集中管理状态里的远程落地摘要（1.2.8 起含全部字段）；保存后重写
// 远程转发配置并重启远程 realm，正在用的节点会瞬断几秒。
function centralEditLanding(remoteId, index, l) {
  if (!l) { toast('找不到这个落地，请刷新页面再试', 'err'); return; }
  const t = l.traffic || {};
  const customPorts = l.custom_ports || '';
  const sbMode = l.sb_mode || '';
  const relayAddr = centralDetailRelayAddr || '';
  const m = openModal('<h2>编辑远程落地：' + esc(centralLandingTitle(l)) + '</h2>' +
    '<p class="muted">改地址、端口、模式后会重写远程转发配置并重启远程 realm，正在用的节点会瞬断几秒；到期与流量状态会跟着新地址走，不会丢。</p>' +
    '<label>落地 IP 或域名</label><input id="central-edit-landing" value="' + esc(l.landing) + '">' +
    '<label>名称 / 备注 <span class="hint">显示名，空着显示落地地址；改名只改显示，不动转发配置</span></label><input id="central-edit-name" value="' + esc(l.name && l.name !== l.landing ? l.name : '') + '" placeholder="' + esc(l.landing) + '" maxlength="64">' +
    '<label>预设</label><select id="central-edit-preset">' +
    '<option value="1"' + (l.preset === '1' ? ' selected' : '') + '>预设 1：landing-node（Xray 核心）</option>' +
    '<option value="2"' + (l.preset === '2' ? ' selected' : '') + '>预设 2：landing-node（sing-box 核心）</option>' +
    '<option value="3"' + (l.preset === '3' ? ' selected' : '') + '>预设 3：自定义端口</option></select>' +
    '<label>模式</label><select id="central-edit-mode">' +
    '<option value="standard"' + (l.mode !== 'door' ? ' selected' : '') + '>标准模式</option>' +
    '<option value="door"' + (l.mode === 'door' ? ' selected' : '') + '>任意门模式</option></select>' +
    '<div id="central-edit-relay-addr-wrap"' + (l.mode === 'door' ? '' : ' class="hidden"') + '><label>中转地址 <span class="hint">任意门链接用这个地址生成，空着用当前设置</span></label>' +
    '<input id="central-edit-relay-addr" value="' + esc(relayAddr) + '" placeholder="留空 = 自动用中转机公网 IP"></div>' +
    '<div id="central-edit-sb-mode-wrap"' + (l.preset === '2' ? '' : ' class="hidden"') + '><label>sing-box 模式</label><select id="central-edit-sb-mode">' +
    '<option value="1"' + (sbMode !== '2' ? ' selected' : '') + '>TLS（443）</option>' +
    '<option value="2"' + (sbMode === '2' ? ' selected' : '') + '>明文（8080）</option></select></div>' +
    '<div id="central-edit-custom-ports-wrap"' + (l.preset === '3' ? '' : ' class="hidden"') + '><label>自定义端口 <span class="hint">逗号分隔，如 443,8388；可写 监听:落地，如 10443:443</span></label>' +
    '<input id="central-edit-custom-ports" value="' + esc(customPorts) + '" placeholder="443,8388"></div>' +
    '<label>证书域名</label><input id="central-edit-cert-domain" value="' + esc(l.cert_domain || '') + '" placeholder="example.com">' +
    '<label>证书签发方式 <span class="hint">决定续签方式怎么判；DNS 验证的落地续签不走 80，判自动</span></label><select id="central-edit-cert-mode">' +
    '<option value=""' + (!l.cert_mode ? ' selected' : '') + '>未设置（按旧逻辑判断）</option>' +
    '<option value="http01"' + (l.cert_mode === 'http01' ? ' selected' : '') + '>默认走 80（HTTP-01）</option>' +
    '<option value="dns01"' + (l.cert_mode === 'dns01' ? ' selected' : '') + '>DNS 验证 Cloudflare（DNS-01）</option></select>' +
    '<p class="small">默认走 80：落地机用 80 端口验证域名签证书，80 改了监听端口时续签要手动。DNS 验证：落地机用 Cloudflare 令牌在 DNS 里加验证记录签证书，续签全自动，不用 80；前提是装节点时选了 DNS 验证并配好令牌，没配令牌的落地别选这个。</p>' +
    '<label>节点时长</label><select id="central-edit-duration">' +
    '<option value="-1" selected>保持不变</option><option value="0">改为永久</option><option value="30">从现在起 30 天</option>' +
    '<option value="90">从现在起 90 天</option><option value="180">从现在起 180 天</option>' +
    '<option value="365">从现在起 365 天</option>' +
    '<option value="custom">自定义天数</option></select>' +
    '<div id="central-edit-duration-custom-wrap" class="hidden"><label>自定义天数</label><input id="central-edit-duration-custom" type="number" min="1" max="3650" placeholder="如 60"></div>' +
    '<label>流量上限（GB，0 = 不限）</label><input id="central-edit-traffic" type="number" min="0" step="1" value="' + Math.round((t.limit_bytes || 0) / 1073741824) + '">' +
    '<label>流量上限算法</label><select id="central-edit-traffic-mode"><option value="total"' + (t.limit_mode !== 'monthly' ? ' selected' : '') + '>总量（整个生命周期）</option><option value="monthly"' + (t.limit_mode === 'monthly' ? ' selected' : '') + '>按月（每月重置）</option></select>' +
    '<label>流量倍率 <span class="hint">实际流量 × 倍率计入订阅用量，默认 1；贵的线路调高</span></label><input id="central-edit-traffic-multiplier" type="number" min="0.1" max="100" step="0.1" value="' + (l.traffic_multiplier || 1) + '">' +
    '<label>限速（Mbps，0 = 不限速）</label><input id="central-edit-speed" type="number" min="0" step="1" value="' + (l.speed_limit_mbps || 0) + '">' +
    '<label class="checkbox-row"><input type="checkbox" id="central-edit-want-udp"' + (l.want_udp ? ' checked' : '') + '><span style="font-weight:400">同时转发 UDP（HY2 / TUIC 需要）</span></label>' +
    '<label>原始节点链接 <span class="hint">选填；有它卡片上才出中转链接，改地址/端口后链接自动跟着重算</span></label><input id="central-edit-orig-link" value="' + esc(l.orig_link || '') + '" placeholder="vless://…">' +
    '<label>节点名称 <span class="hint">选填；直连备注“&lt;名称&gt;-直连”、中转备注“&lt;名称&gt;-中转”</span></label><input id="central-edit-link-name" value="' + esc(l.orig_link_name || '') + '" placeholder="如 香港-01" maxlength="64">' +
    '<div class="btn-row"><button class="btn" id="central-edit-ok" type="button">保存（重启远程 realm 生效）</button><button class="btn secondary" id="central-edit-cancel" type="button">取消</button></div>');
  onIn(m, '#central-edit-cancel', 'click', closeModal);
  const syncEdit = () => {
    const pv = $('#central-edit-preset', m).value;
    const sbw = $('#central-edit-sb-mode-wrap', m); if (sbw) sbw.classList.toggle('hidden', pv !== '2');
    const cpw = $('#central-edit-custom-ports-wrap', m); if (cpw) cpw.classList.toggle('hidden', pv !== '3');
    const raw = $('#central-edit-relay-addr-wrap', m); if (raw) raw.classList.toggle('hidden', $('#central-edit-mode', m).value !== 'door');
  };
  onIn(m, '#central-edit-preset', 'change', syncEdit);
  onIn(m, '#central-edit-mode', 'change', syncEdit);
  onIn(m, '#central-edit-duration', 'change', () => {
    const w = $('#central-edit-duration-custom-wrap', m);
    if (w) w.classList.toggle('hidden', $('#central-edit-duration', m).value !== 'custom');
  });
  onIn(m, '#central-edit-ok', 'click', async () => {
    const payload = buildCentralEditPayload(m);
    if (!payload) return;
    const ok = await confirmModal('保存编辑', '<p>保存后会重写远程转发配置并<b>重启远程 realm，正在用的节点会瞬断几秒</b>。确定保存吗？</p>', '保存并重启远程 realm');
    if (!ok) return;
    const btn = $('#central-edit-ok', m); if (btn) { btn.disabled = true; btn.textContent = '保存中…'; }
    try {
      const d = await centralSubmitEdit(remoteId, index, payload, null);
      if (d.ok === false) {
        if (btn) { btn.disabled = false; btn.textContent = '保存（重启远程 realm 生效）'; }
        if (d.need_choice) {
          await showShareChoice(d, payload, async (choice) => {
            try {
              const d2 = await centralSubmitEdit(remoteId, index, payload, choice);
              if (d2.ok === false) { toast(centralOpError(d2), 'err'); return; }
              closeModal();
              toast('远程落地已更新，realm 已重启', 'ok');
              if (centralDetailId === remoteId) loadCentralLandings(remoteId);
            } catch (err) { toast('保存失败：' + err.message, 'err'); }
          });
          return;
        }
        toast(centralOpError(d), 'err'); return;
      }
      closeModal();
      toast('远程落地已更新，realm 已重启', 'ok');
      if (centralDetailId === remoteId) loadCentralLandings(remoteId);
    } catch (e) {
      if (btn) { btn.disabled = false; btn.textContent = '保存（重启远程 realm 生效）'; }
      toast('保存失败：' + e.message, 'err');
    }
  });
}
async function centralSubmitEdit(id, index, payload, shareChoice) {
  const body = Object.assign({}, payload);
  if (shareChoice) { body.share_mode = shareChoice.mode; if (shareChoice.overrides) body.listen_overrides = shareChoice.overrides; }
  return apiRaw('/api/v1/central/remotes/' + encodeURIComponent(id) + '/landings/' + index, { method: 'PUT', body: JSON.stringify(body) });
}
// 远程编辑表单取值：与本地 buildEditPayload 同一套校验。
function buildCentralEditPayload(m) {
  const landing = $('#central-edit-landing', m).value.trim();
  const certDomain = $('#central-edit-cert-domain', m).value.trim();
  if (!landing) { toast('请填写落地 IP 或域名', 'err'); return null; }
  if (!/^[A-Za-z0-9.-]+$/.test(landing)) { toast('落地 IP 或域名格式不正确', 'err'); return null; }
  if (certDomain && !/^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/.test(certDomain)) { toast('证书域名格式不正确，如 example.com', 'err'); return null; }
  const preset = $('#central-edit-preset', m).value;
  let customPorts = '';
  if (preset === '3') {
    customPorts = $('#central-edit-custom-ports', m).value.trim();
    if (!customPorts) { toast('自定义端口必填，如 443,8388', 'err'); return null; }
    if (!/^[\d,: ]+$/.test(customPorts)) { toast('自定义端口格式不正确，只能是数字、逗号和冒号', 'err'); return null; }
  }
  let duration = Number($('#central-edit-duration', m).value);
  if ($('#central-edit-duration', m).value === 'custom') {
    duration = Number($('#central-edit-duration-custom', m).value) || 0;
    if (duration < 1 || duration > 3650) { toast('自定义天数请填 1–3650', 'err'); return null; }
  }
  const trafficGb = Number($('#central-edit-traffic', m).value) || 0;
  if (trafficGb < 0) { toast('流量上限不能为负', 'err'); return null; }
  const speed = Number($('#central-edit-speed', m).value) || 0;
  if (speed < 0) { toast('限速不能为负', 'err'); return null; }
  const mult = parseTrafficMultiplier($('#central-edit-traffic-multiplier', m));
  if (mult == null) return null;
  const mode = $('#central-edit-mode', m).value;
  const relayAddr = (mode === 'door' && $('#central-edit-relay-addr', m)) ? $('#central-edit-relay-addr', m).value.trim() : '';
  const name = $('#central-edit-name', m).value.trim();
  if (name && name.length > 64) { toast('名称不能超过 64 字', 'err'); return null; }
  const origLink = $('#central-edit-orig-link', m).value.trim();
  if (origLink && !/^(vless|trojan|ss|vmess|hysteria2|hy2|tuic):\/\//.test(origLink)) { toast('原始节点链接格式不正确，需以 vless://、trojan://、ss://、vmess://、hysteria2:// 或 tuic:// 开头', 'err'); return null; }
  const linkName = $('#central-edit-link-name', m).value.trim();
  if (linkName && linkName.length > 64) { toast('节点名称不能超过 64 字', 'err'); return null; }
  return {
    landing, preset, mode,
    name: name || undefined,
    orig_link: origLink || undefined,
    link_name: linkName || undefined,
    relay_addr: relayAddr || undefined,
    sb_mode: preset === '2' ? $('#central-edit-sb-mode', m).value : undefined,
    custom_ports: customPorts || undefined,
    want_udp: $('#central-edit-want-udp', m).checked,
    cert_domain: certDomain || undefined,
    cert_mode: $('#central-edit-cert-mode', m).value || undefined,
    duration_days: duration,
    traffic_limit_bytes: Math.round(trafficGb * 1073741824),
    traffic_limit_mode: trafficGb > 0 ? $('#central-edit-traffic-mode', m).value : undefined,
    traffic_multiplier: mult,
    speed_limit_mbps: speed,
  };
}
async function centralRenewLanding(remoteId, index) {
  const m = openModal('<h2>远程续期</h2><p>续期只延长时限；如果是因为流量用完被停用，请另外「续量」。</p>' +
    '<label>快捷天数</label><select id="central-renew-days"><option value="30">30 天</option><option value="90" selected>90 天</option><option value="180">180 天</option><option value="365">365 天</option></select>' +
    '<label>自定义天数 <span class="hint">填了就用自定义（1–3650 天），不填用上面的快捷天数</span></label><input id="central-renew-days-custom" type="number" min="1" max="3650" placeholder="如 45">' +
    '<div class="btn-row"><button class="btn" id="central-renew-ok" type="button">确认续期</button><button class="btn secondary" id="central-renew-cancel" type="button">取消</button></div>');
  onIn(m, '#central-renew-cancel', 'click', closeModal);
  onIn(m, '#central-renew-ok', 'click', async () => {
    const custom = $('#central-renew-days-custom', m).value.trim();
    let days;
    if (custom !== '') {
      days = Number(custom);
      if (!days || days < 1 || days > 3650) { toast('自定义天数请填 1–3650', 'err'); return; }
    } else days = Number($('#central-renew-days', m).value);
    closeModal();
    try {
      const d = await apiPostRaw('/api/v1/central/remotes/' + encodeURIComponent(remoteId) + '/landings/' + index + '/renew', { days });
      if (d.ok === false) { toast(centralOpError(d), 'err'); return; }
      toast('远程续期成功，已延长 ' + days + ' 天', 'ok');
      if (centralDetailId === remoteId) loadCentralLandings(remoteId);
    } catch (e) { toast('续期失败：' + e.message, 'err'); }
  });
}
async function centralTrafficSettings(remoteId, index, l) {
  const t = (l && l.traffic) || {};
  const m = openModal('<h2>远程流量限制</h2>' +
    '<label>流量上限（GB，0 = 不限）</label><input id="central-ts-gb" type="number" min="0" step="1" value="' + Math.round((t.limit_bytes || 0) / 1073741824) + '">' +
    '<label>算法</label><select id="central-ts-mode"><option value="total"' + (t.limit_mode !== 'monthly' ? ' selected' : '') + '>总量</option><option value="monthly"' + (t.limit_mode === 'monthly' ? ' selected' : '') + '>按月</option></select>' +
    '<label>限速（Mbps，0 = 不限速）</label><input id="central-ts-speed" type="number" min="0" step="1" value="' + (l ? (l.speed_limit_mbps || 0) : 0) + '">' +
    '<div class="btn-row"><button class="btn" id="central-ts-ok" type="button">保存</button><button class="btn secondary" id="central-ts-cancel" type="button">取消</button></div>');
  onIn(m, '#central-ts-cancel', 'click', closeModal);
  onIn(m, '#central-ts-ok', 'click', async () => {
    const gb = Number($('#central-ts-gb', m).value) || 0;
    if (gb < 0) { toast('流量上限不能为负', 'err'); return; }
    closeModal();
    try {
      const d = await apiPostRaw('/api/v1/central/remotes/' + encodeURIComponent(remoteId) + '/landings/' + index + '/traffic', {
        limit_bytes: Math.round(gb * 1073741824),
        limit_mode: gb > 0 ? $('#central-ts-mode', m).value : 'total',
        speed_limit_mbps: Number($('#central-ts-speed', m).value) || 0,
      });
      if (d.ok === false) { toast(centralOpError(d), 'err'); return; }
      toast('远程流量限制已保存', 'ok');
      if (centralDetailId === remoteId) loadCentralLandings(remoteId);
    } catch (e) { toast('保存失败：' + e.message, 'err'); }
  });
}
async function centralAddTraffic(remoteId, index) {
  const m = openModal('<h2>远程续量</h2><p>给流量上限增加额度。续完仍有余额且之前因流量停用的，会自动恢复转发。</p>' +
    '<label>增加多少 GB</label><input id="central-addtraffic-gb" type="number" min="1" step="1" value="50">' +
    '<div class="btn-row"><button class="btn" id="central-at-ok" type="button">确认续量</button><button class="btn secondary" id="central-at-cancel" type="button">取消</button></div>');
  onIn(m, '#central-at-cancel', 'click', closeModal);
  onIn(m, '#central-at-ok', 'click', async () => {
    const gb = Number($('#central-addtraffic-gb', m).value);
    if (!gb || gb < 1) { toast('请填大于 0 的 GB 数', 'err'); return; }
    closeModal();
    try {
      const d = await apiPostRaw('/api/v1/central/remotes/' + encodeURIComponent(remoteId) + '/landings/' + index + '/add-traffic', { bytes: Math.round(gb * 1073741824) });
      if (d.ok === false) { toast(centralOpError(d), 'err'); return; }
      toast('远程续量成功，已增加 ' + gb + ' GB', 'ok');
      if (centralDetailId === remoteId) loadCentralLandings(remoteId);
    } catch (e) { toast('续量失败：' + e.message, 'err'); }
  });
}
async function centralDeleteSubnode(remoteId, index, domain) {
  const ok = await confirmModal('删除远程子节点', '<p>确定删除子节点 <b>' + esc(domain) + '</b> 吗？</p><p>只删远程面板里的登记，不影响该落地的转发和其他节点。落地机上的对应节点请另外到落地机上删除。</p>', '删除该节点', true);
  if (!ok) return;
  try {
    const d = await apiRaw('/api/v1/central/remotes/' + encodeURIComponent(remoteId) + '/landings/' + index + '/subnodes/' + encodeURIComponent(domain), { method: 'DELETE' });
    if (d.ok === false) { toast(centralOpError(d), 'err'); return; }
    toast('远程子节点已删除', 'ok');
    if (centralDetailId === remoteId) loadCentralLandings(remoteId);
  } catch (e) { toast('删除失败：' + e.message, 'err'); }
}
async function centralPasteOrigLink(remoteId, index, l) {
  const m = openModal('<h2>给远程落地补粘贴原始链接' + (l ? '：' + esc(centralLandingTitle(l)) : '') + '</h2>' +
    '<p class="muted">把落地机上生成的节点链接粘贴进来，远程面板会算出中转 + 直连链接，卡片上直接显示，历史记录里也会记一笔。原始链接只存在远程面板自己的状态文件里，不会进日志与审计。</p>' +
    '<label>原始节点链接 <span class="hint">vless:// / trojan:// / ss:// / vmess:// 开头</span></label><input id="central-paste-orig-link" placeholder="vless://…">' +
    '<label>节点名称 <span class="hint">选填；直连备注“&lt;名称&gt;-直连”、中转备注“&lt;名称&gt;-中转”，空着用原链接备注名</span></label><input id="central-paste-link-name" placeholder="如 香港-01" maxlength="64">' +
    '<div class="btn-row"><button class="btn" id="central-paste-ok" type="button">生成中转链接</button><button class="btn secondary" id="central-paste-cancel" type="button">取消</button></div>');
  onIn(m, '#central-paste-cancel', 'click', closeModal);
  onIn(m, '#central-paste-ok', 'click', async () => {
    const origLink = $('#central-paste-orig-link', m).value.trim();
    const linkName = $('#central-paste-link-name', m).value.trim();
    if (!origLink || !/^(vless|trojan|ss|vmess|hysteria2|hy2|tuic):\/\//.test(origLink)) { toast('请粘贴正确的节点链接（vless:// 等开头）', 'err'); return; }
    if (linkName && linkName.length > 64) { toast('节点名称不能超过 64 字', 'err'); return; }
    closeModal();
    try {
      const r = await apiPostRaw('/api/v1/central/remotes/' + encodeURIComponent(remoteId) + '/landings/' + index + '/orig-link', { orig_link: origLink, link_name: linkName || undefined });
      if (r.ok === false) { toast(centralOpError(r), 'err'); return; }
      openModal('<h2>✅ 中转链接已生成</h2>' + linksHtml(r.links, r.qr) +
        '<div class="btn-row"><button class="btn" id="central-paste-done" type="button">完成</button></div>');
      on('#central-paste-done', 'click', () => { closeModal(); if (centralDetailId === remoteId) loadCentralLandings(remoteId); });
    } catch (e) { toast('生成失败：' + e.message, 'err'); }
  });
}
// 远程证书：弹窗列出该远程面板全部落地的证书剩余天数与续签方式；
// 手动续签仍按本地证书页的六步引导走（点“打开远程面板”跳过去按引导操作）。
async function centralCerts(remoteId) {
  const rm = centralRemotes.find((x) => x.id === remoteId);
  const m = openModal('<h2>' + esc(rm ? rm.name : '') + ' 的证书</h2><div id="central-certs-body">加载中…</div><div class="btn-row"><button class="btn secondary" id="central-certs-close" type="button">关闭</button></div>');
  onIn(m, '#central-certs-close', 'click', closeModal);
  try {
    const r = await api('/api/v1/central/remotes/' + encodeURIComponent(remoteId) + '/certs');
    const certs = r.certs || [];
    const body = $('#central-certs-body', m);
    if (!certs.length) { body.innerHTML = '<div class="empty">这台远程面板还没有落地证书信息。</div>'; return; }
    body.innerHTML = certs.map((c) => '<div class="kv"><span>' + esc(c.domain || c.landing || '') + '</span><b>' + (c.days_left == null ? '无证书' : esc(c.days_left) + ' 天') + (c.renew_mode ? '（' + esc(c.renew_mode) + '）' : '') + '</b></div>').join('') +
      '<p class="muted small">需要手动续签的落地：点“打开远程面板”跳过去，按证书页的六步引导操作；面板不申请、不续签证书，只探测显示。</p>';
  } catch (e) {
    const body = $('#central-certs-body', m);
    if (body) body.innerHTML = '<div class="error-box">加载失败：' + esc(e.message) + '</div>';
  }
}
// 远程体检：跑远程面板的 check.sh，结果与本地体检同款展示；一键修复同样走代理。
async function centralCheck(remoteId) {
  const rm = centralRemotes.find((x) => x.id === remoteId);
  const m = openModal('<h2>' + esc(rm ? rm.name : '') + ' 的体检结果</h2><div id="central-check-body">体检中，请稍候…</div><div class="btn-row"><button class="btn secondary" id="central-check-close" type="button">关闭</button></div>');
  onIn(m, '#central-check-close', 'click', closeModal);
  try {
    const c = await apiPostRaw('/api/v1/central/remotes/' + encodeURIComponent(remoteId) + '/check', {});
    if (c.ok === false) { const b = $('#central-check-body', m); if (b) b.innerHTML = '<div class="error-box">' + esc(centralOpError(c)) + '</div>'; return; }
    let h = '';
    (c.sections || []).forEach((sec) => {
      h += '<h3 style="margin-top:12px">' + esc(sec.name) + ' <span class="badge ' + (sec.status === 'pass' ? 'green' : sec.status === 'warn' ? 'yellow' : 'red') + '">' + esc(sec.status) + '</span></h3>';
      (sec.items || []).forEach((it) => {
        h += '<div class="kv"><span>' + (it.level === 'pass' ? '✅' : it.level === 'warn' ? '⚠️' : '❌') + ' ' + esc(it.message) + '</span></div>';
      });
    });
    const issues = c.issues || [];
    if (issues.length) {
      h += '<h3 style="margin-top:12px">发现的问题（一键修复）</h3>';
      issues.forEach((is, i) => {
        // v1.3.46：同款逻辑——只有可修复的才画“一键修复”，没有修复办法的画“查看原因”。
        const fixable = is.fix && is.fix !== 'manual';
        h += '<div class="kv"><span>❌ ' + esc(is.message || is.code) + '</span><span><button class="btn sm" type="button" data-central-fix="' + i + '">' + (fixable ? '一键修复' : '查看原因') + '</button></span></div>';
      });
      const body = $('#central-check-body', m);
      body.innerHTML = h;
      body.dataset.issues = JSON.stringify(issues);
      $$('[data-central-fix]', body).forEach((b) => b.addEventListener('click', () => {
        const is = JSON.parse(body.dataset.issues)[Number(b.dataset.centralFix)];
        if (is.fix && is.fix !== 'manual') centralFixIssue(remoteId, is); else viewIssue(is);
      }));
    } else {
      h += '<div class="ok-box" style="margin-top:12px">全部正常，没有发现问题。</div>';
      const body = $('#central-check-body', m);
      if (body) body.innerHTML = h;
    }
  } catch (e) {
    const body = $('#central-check-body', m);
    if (body) body.innerHTML = '<div class="error-box">体检失败：' + esc(e.message) + '</div>';
  }
}
async function centralFixIssue(remoteId, issue) {
  if (!issue) return;
  if (issue.fix === 'manual_renew') { toast('该问题需要手动续签：点“打开远程面板”跳过去，按证书页六步引导操作'); return; }
  if (issue.fix === 'choose_share') { toast('该问题需要选择分流方式：点“打开远程面板”跳过去，到添加落地重新提交'); return; }
  try {
    const r = await apiPostRaw('/api/v1/central/remotes/' + encodeURIComponent(remoteId) + '/fix', { issue_code: issue.code, landing: issue.landing });
    if (r.ok === false) { toast(centralOpError(r), 'err'); return; }
    if (r.need_choice) { toast('需要先选择分流方式：点“打开远程面板”跳过去，到添加落地重新提交'); return; }
    // v1.3.46：同款逻辑——后端回 need_manual 时展示原因，不再 toast“修复已执行”。
    if (r.need_manual) { await confirmModal('需要手动处理', '<p>' + esc(r.reason || issue.message || issue.code) + '</p><p class="muted">远程面板上的问题，点“打开远程面板”跳过去手动处理。</p>', '知道了'); return; }
    toast('远程修复已执行：' + (r.msg || issue.code), 'ok');
    loadCentral();
  } catch (e) { toast('修复失败：' + e.message, 'err'); }
}
async function centralRestart(id) {
  const rm = centralRemotes.find((x) => x.id === id);
  const ok = await confirmModal('重启远程 realm', '<p>确定重启远程面板 ' + esc(rm ? rm.name : '') + ' 上的 realm 吗？正在用的节点会短暂断开。</p>', '重启 realm');
  if (!ok) return;
  try {
    const d = await apiPostRaw('/api/v1/central/remotes/' + encodeURIComponent(id) + '/service/restart', {});
    if (d.ok === false) { toast(centralOpError(d), 'err'); return; }
    toast('远程 realm 已重启', 'ok');
    loadCentral();
  } catch (err) { toast('重启失败：' + err.message, 'err'); }
}
async function centralDeregister(id) {
  const rm = centralRemotes.find((x) => x.id === id);
  const ok = await confirmModal('取消登记', '<p>确定取消登记远程面板 <b>' + esc(rm ? rm.name : '') + '</b> 吗？只删主面板的登记，不影响远程面板本身。</p>', '取消登记', true);
  if (!ok) return;
  try {
    await api('/api/v1/central/remotes/' + encodeURIComponent(id), { method: 'DELETE' });
    toast('已取消登记', 'ok');
    loadCentral();
  } catch (e) { toast('取消失败：' + e.message, 'err'); }
}
// 更换一台已登记远程面板的令牌（v1.3.31）：弹窗只填新令牌，当前令牌
// 不回显原文、只显示“已设置”。提交后后端拿新令牌当场连一次远程面板，
// 验过才保存，验不过原令牌不动、错误显示在弹窗里不关闭。
async function centralChangeToken(id) {
  const rm = centralRemotes.find((x) => x.id === id);
  if (!rm) { toast('找不到这台远程面板', 'err'); return; }
  const m = openModal('<h2>更换令牌</h2>' +
    '<p class="muted">远程面板 <b>' + esc(rm.name || rm.base_url) + '</b>（<span class="mono">' + esc(rm.base_url) + '</span>）</p>' +
    '<p class="muted">当前令牌：已设置（原文不显示）。先到被管面板的设置页生成新令牌，填进来；提交后会拿新令牌当场连一次远程面板，验过才保存，验不过原令牌不动。</p>' +
    '<label>新令牌 <span class="hint">必填，以 rpt_ 开头，不回显</span></label><input id="central-token-new" type="password" placeholder="rpt_…" autocomplete="off">' +
    '<div class="error-box hidden" id="central-token-err"></div>' +
    '<div class="btn-row"><button class="btn" id="central-token-save" type="button">保存新令牌</button><button class="btn secondary" id="central-token-cancel" type="button">取消</button></div>');
  if (!m) return;
  onIn(m, '#central-token-cancel', 'click', () => closeModal());
  onIn(m, '#central-token-save', 'click', async () => {
    const input = $('#central-token-new', m);
    const token = input ? input.value.trim() : '';
    const errBox = $('#central-token-err', m);
    const showErr = (msg) => { if (errBox) { errBox.textContent = msg; errBox.classList.remove('hidden'); } };
    if (!token) { showErr('请填新令牌'); return; }
    try {
      const d = await apiRaw('/api/v1/central/remotes/' + encodeURIComponent(id) + '/token', { method: 'PUT', body: JSON.stringify({ token }) });
      if (d.ok === false) { showErr(centralOpError(d)); return; }
      closeModal();
      toast('令牌已更换', 'ok');
      loadCentral();
    } catch (e) { showErr(e.message); }
  });
}
// 编辑一台已登记远程面板的名字与地址（v1.3.49）：弹窗预填当前名字与
// 地址，只改这两项，ID 与令牌不动；保存后后端拿存的令牌当场连一次新
// 地址，验过才保存，验不过原值不动、错误显示在弹窗里不关闭。只改管理
// 面板去拉远程的地址，不动订阅链接本身，客户端不用重新导入。
async function centralEditRemote(id) {
  const rm = centralRemotes.find((x) => x.id === id);
  if (!rm) { toast('找不到这台远程面板', 'err'); return; }
  const m = openModal('<h2>编辑远程面板</h2>' +
    '<p class="muted">只改名字与地址，令牌不变。保存后会拿存的令牌当场连一次新地址，验过才保存，验不过原值不动。订阅链接本身不受影响，客户端不用重新导入。</p>' +
    '<label>名称 <span class="hint">必填，最多 64 个字</span></label><input id="central-edit-name" type="text" value="' + esc(rm.name || '') + '" maxlength="64">' +
    '<label>地址 <span class="hint">必填，写到端口为止，如 https://域名:11111</span></label><input id="central-edit-url" type="text" value="' + esc(rm.base_url || '') + '">' +
    '<div class="error-box hidden" id="central-edit-err"></div>' +
    '<div class="btn-row"><button class="btn" id="central-edit-save" type="button">保存</button><button class="btn secondary" id="central-edit-cancel" type="button">取消</button></div>');
  if (!m) return;
  onIn(m, '#central-edit-cancel', 'click', () => closeModal());
  onIn(m, '#central-edit-save', 'click', async () => {
    const nameEl = $('#central-edit-name', m);
    const urlEl = $('#central-edit-url', m);
    const name = nameEl ? nameEl.value.trim() : '';
    const baseUrl = urlEl ? urlEl.value.trim() : '';
    const errBox = $('#central-edit-err', m);
    const showErr = (msg) => { if (errBox) { errBox.textContent = msg; errBox.classList.remove('hidden'); } };
    if (!name) { showErr('请填名称'); return; }
    if (!baseUrl) { showErr('请填地址'); return; }
    try {
      const d = await apiRaw('/api/v1/central/remotes/' + encodeURIComponent(id), { method: 'POST', body: JSON.stringify({ name, base_url: baseUrl }) });
      if (d.ok === false) { showErr(centralOpError(d)); return; }
      closeModal();
      toast('远程面板已更新', 'ok');
      loadCentral();
    } catch (e) { showErr(e.message); }
  });
}
async function centralRefreshAll() {
  try {
    const r = await api('/api/v1/central/remotes');
    centralRemotes = r.remotes || [];
    for (const rm of centralRemotes) {
      try { await apiPost('/api/v1/central/remotes/' + encodeURIComponent(rm.id) + '/poll', {}); } catch (e) { /* 单台失败不影响其他 */ }
    }
    await loadCentral();
    toast('已刷新', 'ok');
  } catch (e) { toast('刷新失败：' + e.message, 'err'); }
}
function initCentral() {
  initCentralTabs(); // v1.3.13：大选项卡绑定 + 恢复上次大选项卡
  initCentralSubTabs(); // v1.3.13：子选项卡事件委托（栏只绑一次）+ 恢复上次子选项卡在重绘时做
  on('#central-refresh', 'click', centralRefreshAll);
  // 合并订阅创建表单“永久”勾选（v1.3.28）：与通用订阅同一模式，勾选时日期框置灰。
  on('#central-sub-permanent', 'change', () => { const d = $('#central-sub-expiry-date'); if (d) d.disabled = $('#central-sub-permanent').checked; });
  on('#central-sub-generate', 'click', async () => {
    const selection = [];
    (centralSubTree || []).forEach((p) => p.landings.forEach((l) => {
      if (centralSubChecked[subTreeKey(p.id, l.index)]) selection.push({ panel: p.id, landing: l.index });
    }));
    if (!selection.length) { toast('请先勾选至少一个落地', 'err'); return; }
    const nameEl = $('#central-sub-name');
    const expEl = $('#central-sub-expiry-date');
    const permEl = $('#central-sub-permanent');
    // 到期时间（v1.3.28）：勾永久 = 空串（永不过期）；不勾 = 日期框的值。
    const expiresAt = (permEl && permEl.checked) ? '' : (expEl ? expEl.value : '');
    const tl = readTrafficLimit($('#central-sub-traffic-row'));
    const groupEl = $('#central-sub-group');
    await showSubForSelection(selection, '合并订阅', nameEl ? nameEl.value.trim() : '', subModeOf('central-sub-mode'), expiresAt, tl, groupEl ? groupEl.value : '');
    if (expEl) { expEl.value = ''; expEl.disabled = true; }
    if (permEl) permEl.checked = true;
  });
  // 合并订阅列表搜索（v1.3.20）：输入即过滤，回到第 1 页。
  wireListSearch('#central-sub-search', (q) => { mergedSubSearch = q; mergedSubPage = 1; loadMergedSubsList(); });
  on('#central-add-form', 'submit', async (e) => {
    e.preventDefault();
    const name = $('#central-name').value.trim();
    const baseUrl = $('#central-url').value.trim();
    const token = $('#central-token').value.trim();
    if (!baseUrl || !token) { toast('请填远程面板地址和令牌', 'err'); return; }
    try {
      const d = await apiPostRaw('/api/v1/central/remotes', { name, base_url: baseUrl, token });
      if (d.ok === false) { toast(centralOpError(d), 'err'); return; }
      // v1.3.33：登记时顺带导入远程已有的按人转发记录（分配端口不再撞）。
      toast('远程面板已登记' + (d.imported ? '，已导入远程的 ' + d.imported + ' 条转发记录' : ''), 'ok');
      if (d.import_warning) { toast(d.import_warning, 'err'); }
      e.target.reset();
      loadCentral();
    } catch (err) { toast('登记失败：' + err.message, 'err'); }
  });
}

/* ---------- 启动 ---------- */
document.addEventListener('DOMContentLoaded', () => {
  // 启动成功标记：index.html 里的内联兜底脚本靠它判断 app.js 是否跑起来了
  window.__panelBooted = true;
  // 看门狗：10 秒后还在加载态（会话校验挂起、网络卡住），直接放登录页，页面不白板
  setTimeout(() => {
    const lv = $('#loading-view');
    if (lv && !lv.classList.contains('hidden')) showLogin();
  }, 10000);
  // 单个初始化失败不能拖垮后面的：逐个包 try/catch，checkMe 必须执行，
  // 否则登录页和后台都出不来，页面就是白板。
  const safeInit = (fn) => { try { fn(); } catch (e) { /* 忽略：单个初始化失败不影响其他 */ } };
  safeInit(initTheme);
  safeInit(initLogos); // v1.3.20：登录页/侧边栏 Logo 加载成败回退（登录前就要能显示）
  safeInit(buildNav);
  safeInit(initDrawer);
  safeInit(initLogin);
  safeInit(initCheck);
  safeInit(initAddForm);
  safeInit(initAddTabs); // v1.3.41：添加落地表单选项卡绑定 + 恢复上次选项卡
  safeInit(initServiceTabs); // v1.3.41：服务与日志页选项卡绑定 + 恢复上次选项卡
  safeInit(initFirewallTabs); // v1.3.41：端口管理页选项卡绑定 + 恢复上次选项卡
  safeInit(initTwin);
  safeInit(initSubMode); // v1.3.17：订阅内容三选一绑定 + 恢复上次选择
  safeInit(initService);
  safeInit(initSettings);
  safeInit(initSubGroups); // v1.3.26：分组卡片新建按钮 + 搜索框绑定
  safeInit(initMonitorSettings);
  safeInit(initLatencySettings);
  safeInit(initUpdatePoll);
  safeInit(initFirewall);
  safeInit(initTraffic);
  safeInit(initCentral);
  safeInit(initNodes); // v1.4.0：节点页建节点表单与复制按钮绑定
  safeInit(initNodeCerts); // v1.4.12：节点证书页签发／续签／删除／手动两步走绑定
  let refreshing = false;
  const refreshBtn = on('#refresh-btn', 'click', async () => {
    if (refreshing || !refreshBtn) return; // 正在刷时再点无效
    refreshing = true;
    refreshBtn.disabled = true; refreshBtn.classList.add('spinning');
    try {
      await loadPage(currentPage);
      toast('已刷新', 'ok');
    } catch (e) {
      toast('刷新失败：' + (e.message || e), 'err');
    } finally {
      refreshing = false;
      refreshBtn.disabled = false; refreshBtn.classList.remove('spinning');
    }
  });
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    if (ibHelpOpenKey) { ibHelpHide(); return; } // v1.4.12：提示框开着先关提示框，不关弹窗
    closeModal();
  });
  // checkMe 必须执行：不管前面哪个初始化失败，都要决定显示登录页还是后台
  try { checkMe(); } catch (e) { showLogin(); }
});
