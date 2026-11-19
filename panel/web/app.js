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
  return r === 'expired' ? '已到期' : r === 'traffic' ? '流量用完' : r === 'both' ? '流量用完且已到期' : '';
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
  try { document.execCommand('copy'); done(); } catch (e) { toast('复制失败，请手动复制', 'err'); }
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

/* ---------- 弹窗 ---------- */
function openModal(html, wide) {
  const root = $('#modal-root');
  if (!root) return null; // 弹窗容器缺失时直接放弃，绝不抛错
  root.innerHTML = '<div class="modal-backdrop"><div class="modal' + (wide ? ' wide' : '') + '" role="dialog" aria-modal="true">' + html + '</div></div>';
  onIn(root, '.modal-backdrop', 'click', (e) => { if (e.target.classList.contains('modal-backdrop')) closeModal(); });
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
const MANAGER_PAGES = ['central', 'settings'];
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
}
function showApp() {
  hideLoading();
  const app = $('#app'), login = $('#login-view');
  if (login) { login.classList.add('hidden'); login.hidden = true; }
  if (app) { app.classList.remove('hidden'); app.hidden = false; }
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
      await checkMe();
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
  if (!t.limit_bytes) return '<div class="bar-row"><span>流量：不限</span><span>' + fmtBytes(t.used_bytes || 0) + ' 已用</span></div>';
  const pct = Math.min(100, Number(t.pct) || 0);
  const cls = pct >= 100 ? 'bad' : pct >= 80 ? 'warn' : '';
  const mode = t.limit_mode === 'monthly' ? '按月' : '总量';
  return '<div class="bar-row"><span>流量（' + mode + '）</span><span>' + pct + '% · ' + fmtBytes(t.used_bytes || 0) + ' / ' + fmtBytes(t.limit_bytes) + '</span></div>' +
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
    const box = x.closest('.card, .central-link-body, .central-landing-body');
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
  if (!t.limit_bytes) return '不限 · 已用 ' + fmtBytes(t.used_bytes || 0);
  return fmtBytes(t.used_bytes || 0) + ' / ' + fmtBytes(t.limit_bytes) + '（' + (Math.min(100, Number(t.pct) || 0)) + '%）';
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
  $$('[data-sub-landing]', m).forEach((b) => b.addEventListener('click', () => { closeModal(); const l = landingsCache.find((x) => x.index === Number(b.dataset.subLanding)); showSubForSelection([{ panel: 'local', landing: Number(b.dataset.subLanding) }], (l ? displayNameOf(l) : '落地') + ' 的订阅'); }));
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
    showSubForSelection([{ panel: 'local', landing: Number(b.dataset.miniSub) }], (l ? displayNameOf(l) : '落地') + ' 的订阅');
  }));
}
let overviewPage = 1;
function modeText(l, relayAddr) {
  if (l.mode === 'door') return '任意门模式' + (relayAddr ? '（中转地址：' + esc(relayAddr) + '）' : '');
  return '标准模式';
}
function renderLandingCards(page) {
  if (page) overviewPage = page;
  const wrap = $('#landing-cards');
  $('#overview-empty').classList.toggle('hidden', landingsCache.length > 0);
  if (!landingsCache.length) { wrap.innerHTML = ''; return; }
  const pg = paginateCards(landingsCache, overviewPage);
  overviewPage = pg.cur;
  const relayAddr = (statusCache && statusCache.relay_addr) || '';
  wrap.innerHTML = pg.slice.map((l) => landingMiniCard(l, relayAddr)).join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'ovcards');
  wireMiniCards(wrap);
  if (pg.totalPages > 1) wirePager(wrap, 'ovcards', pg.cur, pg.totalPages, (p) => renderLandingCards(p));
}

/* ---------- 一键体检 + 一键修复 ---------- */
function initCheck() {
  on('#check-btn', 'click', async () => {
    const box = $('#check-result');
    box.innerHTML = '<div class="card">体检中，请稍候…</div>';
    try {
      const c = await apiPost('/api/v1/check', {});
      let h = '<div class="card"><h2>体检结果</h2>';
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
          h += '<div class="kv"><span>❌ ' + esc(is.message || is.code) + '</span><span><button class="btn sm" type="button" data-fix="' + i + '">一键修复</button></span></div>';
        });
        box.dataset.issues = JSON.stringify(issues);
      } else h += '<div class="ok-box" style="margin-top:12px">全部正常，没有发现问题。</div>';
      h += '</div>';
      box.innerHTML = h;
      $$('[data-fix]', box).forEach((b) => b.addEventListener('click', () => fixIssue(JSON.parse(box.dataset.issues)[Number(b.dataset.fix)])));
    } catch (e) {
      box.innerHTML = '<div class="card"><div class="error-box">体检失败：' + esc(e.message) + '</div></div>';
    }
  });
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
  const mode = $('#add-mode') ? $('#add-mode').value : 'standard';
  const relayAddr = (mode === 'door' && $('#add-relay-addr')) ? $('#add-relay-addr').value.trim() : '';
  const name = $('#add-name') ? $('#add-name').value.trim() : '';
  if (name && name.length > 64) { toast('名称不能超过 64 字', 'err'); return null; }
  const origLink = $('#add-orig-link') ? $('#add-orig-link').value.trim() : '';
  if (origLink && !/^(vless|trojan|ss|vmess):\/\//.test(origLink)) { toast('原始节点链接格式不正确，需以 vless://、trojan://、ss:// 或 vmess:// 开头', 'err'); return null; }
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
    $('#add-result').innerHTML = '<div class="card"><h2>✅ 落地添加成功</h2>' +
      (r.door_note ? '<div class="warn-box">' + esc(r.door_note) + '</div>' : '') +
      ((r.qr && Object.keys(r.qr).length) ? '' : '<p class="muted">二维码生成失败，仅显示链接。</p>') +
      linksHtml(r.links, r.qr) + '</div>';
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
    '<option value="1"' + (l.preset === '1' ? ' selected' : '') + '>预设 1：tiktok-node（Xray）</option>' +
    '<option value="2"' + (l.preset === '2' ? ' selected' : '') + '>预设 2：sing-box-node</option>' +
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
  const mode = $('#edit-mode', m).value;
  const relayAddr = (mode === 'door' && $('#edit-relay-addr', m)) ? $('#edit-relay-addr', m).value.trim() : '';
  const name = $('#edit-name', m).value.trim();
  if (name && name.length > 64) { toast('名称不能超过 64 字', 'err'); return null; }
  const origLink = $('#edit-orig-link', m).value.trim();
  if (origLink && !/^(vless|trojan|ss|vmess):\/\//.test(origLink)) { toast('原始节点链接格式不正确，需以 vless://、trojan://、ss:// 或 vmess:// 开头', 'err'); return null; }
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
    if (!origLink || !/^(vless|trojan|ss|vmess):\/\//.test(origLink)) { toast('请粘贴正确的节点链接（vless:// 等开头）', 'err'); return; }
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
        if (!link || !/^(vless|trojan|ss|vmess):\/\//.test(link)) { toast('请粘贴正确的节点链接（vless:// 等开头）', 'err'); return; }
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
        '<button class="btn secondary sm" type="button" data-group-toggle="' + ord + '">展开 ▸</button></div>' +
        '<div class="group-body hidden" data-group-body="' + ord + '">' + linksHtml(g.links, g.qr) + '</div></div>';
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
    // 点行（除勾选框与按钮外）展开/收起
    $$('[data-group-head]', wrap).forEach((head) => head.addEventListener('click', (e) => {
      if (e.target.closest('input[type="checkbox"]') || e.target.closest('button')) return;
      toggleLinksGroup(Number(head.dataset.groupHead));
    }));
    $$('[data-group-toggle]', wrap).forEach((b) => b.addEventListener('click', (e) => { e.stopPropagation(); toggleLinksGroup(Number(b.dataset.groupToggle)); }));
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
function toggleLinksGroup(ord) {
  const body = $('[data-group-body="' + ord + '"]');
  const btn = $('[data-group-toggle="' + ord + '"]');
  if (!body) return;
  const nowHidden = body.classList.toggle('hidden');
  if (btn) btn.textContent = nowHidden ? '展开 ▸' : '收起 ▾';
}
function initTwin() {
  on('#twin-form', 'submit', async (e) => {
    e.preventDefault();
    const link = $('#twin-link').value.trim();
    const landingIp = $('#twin-landing').value.trim();
    const sni = $('#twin-sni').value.trim();
    const name = $('#twin-name') ? $('#twin-name').value.trim() : '';
    if (name && name.length > 64) { toast('节点名称不能超过 64 字', 'err'); return; }
    if (!/^(vless|trojan|ss|vmess):\/\//.test(link)) { toast('链接格式不正确，需以 vless://、trojan://、ss:// 或 vmess:// 开头', 'err'); return; }
    if (!landingIp) { toast('请填写落地 IP', 'err'); return; }
    try {
      const r = await apiPost('/api/v1/links/twin', { link, landing_ip: landingIp, sni, name: name || undefined });
      const relayLabel = r.relay_label || '中转';
      const links = [];
      if (r.relay_link) links.push({ label: relayLabel, link: r.relay_link });
      if (r.direct_link) links.push({ label: '直连', link: r.direct_link });
      $('#twin-result').innerHTML = '<div class="card" style="margin-top:16px"><h2>生成结果</h2>' +
        (r.manual ? '<div class="warn-box">' + esc(r.manual) + '</div>' : '') +
        (r.persist_error ? '<div class="warn-box">' + esc(r.persist_error) + '</div>' : '') +
        ((r.qr && Object.keys(r.qr).length) ? '' : '<p class="muted">二维码生成失败，仅显示链接。</p>') +
        linksHtml(links, r.qr) + '</div>';
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
async function loadSettingsPage() {
  try {
    const st = await api('/api/v1/settings');
    settingsCache = st;
    // 面板模式（v1.2.8）：管理面板模式下隐藏本机专属设置卡
    //（Cloudflare Token、任意门地址、机器监控、延迟探测都是本机中转的配置）
    const modeOn = !!st.manager_only;
    const modeStatus = $('#mode-status');
    if (modeStatus) modeStatus.textContent = modeOn ? '当前：管理面板（只做集中管理，本地页已隐藏）' : '当前：完整面板';
    ['card-cloudflare', 'card-door', 'card-monitor', 'card-latency'].forEach((cid) => {
      const c = document.getElementById(cid);
      if (c) c.classList.toggle('hidden', modeOn);
    });
    const toMgr = $('#mode-to-manager'), toFull = $('#mode-to-full');
    if (toMgr) toMgr.disabled = modeOn;
    if (toFull) toFull.disabled = !modeOn;
    $('#panel-port').value = st.port || 11111;
    $('#whitelist-input').value = (st.ip_whitelist || []).join('\n');
    $('#https-domain').value = st.https_domain || '';
    $('#https-status').textContent = st.https_enabled ? 'HTTPS 已开启' + (st.https_domain ? '（域名：' + st.https_domain + '）' : '（自签证书）') : '当前为 HTTP，未开启 HTTPS。';
    const cf = st.cloudflare || {};
    $('#cf-status').textContent = cf.has_token ? '已保存 Cloudflare Token（绑域名开 HTTPS 可用）' : '未保存 Token（绑域名开 HTTPS 前要先填）';
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
      $('#sub-status').textContent = '已生成订阅链接' + (sub.created_at ? '（生成于 ' + fmtDate(sub.created_at) + '）' : '') + '：链接已加密存在面板里，随时能点复制重新拿。';
      loadGenericSubLink();
    }
    const subDomain = $('#sub-public-domain');
    if (subDomain) subDomain.value = sub.public_domain || '';
    // Worker 源站地址（v1.3.5）：存过就回填到输入框；留空就是默认用法。
    const wo = $('#worker-origin');
    if (wo) { wo.value = sub.worker_origin || ''; wo.placeholder = '留空 = ' + location.origin; }
    // 自动定时备份（v1.3.6）：设置接口下发的排程填进表单。
    const bk = st.backup || {};
    const bke = $('#bk-enabled'); if (bke) bke.checked = bk.enabled !== false;
    const bkt = $('#bk-time'); if (bkt) bkt.value = bk.time || '03:00';
    const bkk = $('#bk-keep'); if (bkk) bkk.value = bk.keep || 7;
    // 面板名称（v1.3.4）：设置接口下发的当前名填进输入框。
    const pn = $('#panel-name');
    if (pn) pn.value = st.panel_name || 'CP管理';
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
    updateWorkerExampleLink(sub.public_domain || '');
    $('#update-box').innerHTML = '<div class="kv"><span>当前版本</span><b>' + esc(st.version || '—') + '</b></div>' +
      '<div class="kv"><span>最新版本</span><b>' + esc(st.latest_version || '—') + '</b></div>' +
      '<div class="kv"><span>状态</span><b>' + (st.update_available ? '<span class="badge yellow">有更新</span>' : '<span class="badge green">已是最新</span>') + '</b></div>' +
      '<div class="kv"><span>仓库</span><b><a href="https://github.com/nbbk/relay-node" target="_blank" rel="noopener">relay-node</a></b></div>';
  } catch (e) { toast('设置加载失败：' + e.message, 'err'); }
  loadBackups(); loadAudit(); loadSubUsers(); loadSchedBackups();
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
function initSettings() {
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
    try {
      const r = await apiPost('/api/v1/settings/https', { enable: true, domain });
      toast('HTTPS 已开启' + (r.msg ? '：' + r.msg : ''), 'ok'); loadSettingsPage();
    } catch (err) { toast('开启失败：' + err.message + '（已保留原有方式，面板未中断）', 'err'); }
  });
  on('#https-disable', 'click', async () => {
    const ok = await confirmModal('关闭 HTTPS', '<p>关闭后回到 HTTP 明文访问。确定吗？</p>', '关闭 HTTPS');
    if (!ok) return;
    try { await apiPost('/api/v1/settings/https', { enable: false, domain: '' }); toast('已关闭 HTTPS', 'ok'); loadSettingsPage(); }
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
  on('#cf-form', 'submit', async (e) => {
    e.preventDefault();
    const token = $('#cf-token').value.trim();
    if (!token) { toast('请先填写 Token', 'err'); return; }
    try {
      const r = await apiPost('/api/v1/settings/cloudflare', { token });
      toast('Cloudflare Token 已保存' + (r.warning ? '：' + r.warning : ''), 'ok'); $('#cf-token').value = ''; loadSettingsPage();
    } catch (err) { toast('保存失败：' + err.message, 'err'); }
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
  on('#sub-copy', 'click', (e) => { copyText(subLinkWithMode($('#sub-url-value').textContent, ''), e.target); });
  on('#sub-copy-clash', 'click', (e) => { copyText(subLinkWithMode($('#sub-url-value').textContent, 'clash'), e.target); });
  on('#sub-copy-plain', 'click', (e) => { copyText(subLinkWithMode($('#sub-url-value').textContent, 'plain'), e.target); });
  on('#sub-qr', 'click', async () => {
    const box = $('#sub-qr-box');
    if (!box) return;
    if (!box.classList.contains('hidden')) { box.classList.add('hidden'); box.hidden = true; return; }
    await loadSubQr();
  });
  on('#sub-mode', 'change', async () => { const box = $('#sub-qr-box'); if (box && !box.classList.contains('hidden')) await loadSubQr(); });
  on('#sub-user-create', 'click', async () => { await subUserCreate(); });
  on('#sub-user-copy', 'click', (e) => { copyText(subLinkWithMode($('#sub-user-url-value').textContent, ''), e.target); });
  on('#sub-user-copy-clash', 'click', (e) => { copyText(subLinkWithMode($('#sub-user-url-value').textContent, 'clash'), e.target); });
  on('#sub-user-copy-plain', 'click', (e) => { copyText(subLinkWithMode($('#sub-user-url-value').textContent, 'plain'), e.target); });
  on('#sub-user-qr', 'click', async () => {
    const box = $('#sub-user-qr-box');
    if (!box) return;
    if (!box.classList.contains('hidden')) { box.classList.add('hidden'); box.hidden = true; return; }
    await loadSubUserQr();
  });
  on('#worker-generate', 'click', async () => { await loadWorkerCode(); });
  on('#worker-copy', 'click', (e) => { copyText($('#worker-code').textContent, e.target); });
  on('#update-check', 'click', async () => {
    try {
      const r = await api('/api/v1/update/check');
      await loadSettingsPage();
      toast(r.update_available ? '发现新版本 ' + (r.latest_version || '') : '已是最新版本', r.update_available ? undefined : 'ok');
    } catch (err) { toast('检查失败：' + err.message, 'err'); }
  });
  on('#backup-create', 'click', async () => {
    try { await apiPost('/api/v1/backups', { label: '手动备份' }); toast('备份已创建', 'ok'); loadBackups(); }
    catch (err) { toast('备份失败：' + err.message, 'err'); }
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
    const ok = await confirmModal('导入配置', '<p>导入会覆盖当前全部落地与设置（导入前会自动备份当前配置；密码与 Telegram Token 保持本机原值，不随导入走）。确定导入 <b>' + esc(file.name) + '</b> 吗？</p>', '确认导入', true);
    if (!ok) { e.target.value = ''; return; }
    try {
      await apiPost('/api/v1/import', data);
      $('#import-result').innerHTML = '<div class="ok-box">导入成功，realm 已重启并验过端口。</div>';
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
let backupsPage = 1;
async function loadBackups(page) {
  if (page) backupsPage = page;
  const wrap = $('#backup-list');
  try {
    const r = await api('/api/v1/backups');
    const list = r.backups || [];
    if (!list.length) { wrap.innerHTML = '<div class="empty">暂无备份。</div>'; return; }
    const pg = paginate(list, backupsPage);
    backupsPage = pg.cur;
    wrap.innerHTML = pg.slice.map((b) =>
      '<div class="kv"><span>' + esc(b.label || b.id) + '<br><span class="small">' + esc(fmtDate(b.ts)) + ' · ' + esc(b.scope || '') + '</span></span>' +
      '<span><button class="btn secondary sm" type="button" data-restore-bk="' + esc(b.id) + '">恢复</button></span></div>').join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'backups');
    $$('[data-restore-bk]', wrap).forEach((btn) => btn.addEventListener('click', async () => {
      const ok = await confirmModal('恢复备份', '<p>恢复会用这份备份覆盖当前配置并重启 realm。确定恢复吗？</p>', '恢复备份', true);
      if (!ok) return;
      try { await apiPost('/api/v1/backups/' + encodeURIComponent(btn.dataset.restoreBk) + '/restore', {}); toast('备份已恢复', 'ok'); loadOverview(); }
      catch (e) { toast('恢复失败：' + e.message, 'err'); }
    }));
    if (pg.totalPages > 1) wirePager(wrap, 'backups', pg.cur, pg.totalPages, (p) => loadBackups(p));
  } catch (e) { wrap.innerHTML = '<div class="empty">备份列表不可用：' + esc(e.message) + '</div>'; }
}
// 自动定时备份列表（v1.3.6）：恢复/删除，恢复前弹确认（会覆盖当前配置并重启 realm）。
async function loadSchedBackups() {
  const wrap = $('#sched-backup-list');
  if (!wrap) return;
  try {
    const r = await api('/api/v1/scheduled-backups');
    const list = r.backups || [];
    const last = $('#bk-last');
    if (last) last.textContent = r.last_backup ? '上次备份：' + fmtDate(r.last_backup.ts) + '（' + (r.last_backup.label || '') + '）' : '还没有定时备份。';
    if (!list.length) { wrap.innerHTML = '<div class="empty">暂无定时备份。</div>'; return; }
    wrap.innerHTML = list.map((b) =>
      '<div class="kv"><span>' + esc(fmtDate(b.ts)) + '<br><span class="small">' + esc(b.label || '') + ' · ' + (b.size != null ? (b.size / 1024).toFixed(1) + ' KB' : '') + '</span></span>' +
      '<span><button class="btn secondary sm" type="button" data-restore-sbk="' + esc(b.id) + '">恢复</button> ' +
      '<button class="btn secondary sm" type="button" data-del-sbk="' + esc(b.id) + '">删除</button></span></div>').join('');
    $$('[data-restore-sbk]', wrap).forEach((btn) => btn.addEventListener('click', async () => {
      const ok = await confirmModal('恢复定时备份', '<p>恢复会用这份备份覆盖当前配置并重启 realm；恢复后不在备份里的订阅用户会被移除，新用户的订阅令牌要重新生成。确定恢复吗？</p>', '恢复备份', true);
      if (!ok) return;
      try { await apiPost('/api/v1/scheduled-backups/' + encodeURIComponent(btn.dataset.restoreSbk) + '/restore', {}); toast('备份已恢复', 'ok'); loadOverview(); }
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
const AUDIT_PAGE_SIZE = 20;
async function loadAudit(page) {
  if (page) auditPage = page;
  const wrap = $('#audit-list');
  try {
    const r = await api('/api/v1/audit?page=' + auditPage + '&page_size=' + AUDIT_PAGE_SIZE);
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
let centralDetailId = null;
let centralDetailPage = 1;
let centralDetailLandings = [];
let centralDetailRelayAddr = ''; // 当前展开的远程面板的任意门地址（编辑任意门落地时预填）
let centralOpenLanding = null; // 集中管理落地列表当前展开的落地编号（单开手风琴，null=全收起）
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
// 订阅链接二维码：点“二维码”按钮按当前链接（默认不带模式参数）向后端要一张图；
// 模式切换时如果二维码正显示着，跟着重新生成。复用节点链接同一套后端
// 生成（Go 原生 qrcode），图片的复制/下载走页面已有的 data-copy-img /
// data-download-img 事件委托，不用另写。
async function loadSubQr() {
  const box = $('#sub-qr-box'), img = $('#sub-qr-img'), fig = $('#sub-qr-figure');
  if (!box || !img) return;
  const url = subLinkWithMode($('#sub-url-value').textContent, '');
  try {
    const r = await apiPost('/api/v1/subscription/qr', { url });
    if (!r.qr) throw new Error('后端没有返回二维码');
    img.src = r.qr;
    if (fig) fig.dataset.qr = r.qr;
    box.classList.remove('hidden'); box.hidden = false;
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
async function subTokenGenerate(rotated) {
  try {
    const r = await apiPost('/api/v1/settings/subscription/generate', {});
    $('#sub-url-value').textContent = r.url || '';
    const box = $('#sub-once');
    if (box) { box.classList.remove('hidden'); box.hidden = false; }
    toast(rotated ? '订阅链接已轮换，旧链接已失效' : '订阅链接已生成，随时能复制', 'ok');
    loadSettingsPage();
  } catch (err) { toast('生成失败：' + err.message, 'err'); }
}
// ---------- 订阅对外域名（v1.3.2） ----------
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
    const q1 = $('#sub-qr-box'), q2 = $('#sub-user-qr-box');
    if (q1) { q1.classList.add('hidden'); q1.hidden = true; }
    if (q2) { q2.classList.add('hidden'); q2.hidden = true; }
    updateWorkerExampleLink(r.domain || '');
    toast(r.domain ? '订阅对外域名已保存，订阅链接改用 https://' + r.domain + '/sub/…' : '订阅对外域名已清除，订阅链接恢复成面板自己的地址', 'ok');
    loadSettingsPage();
  } catch (err) { toast('保存失败：' + err.message, 'err'); }
}
// 把页面上正在显示的一次性订阅链接换成新域名（只换地址部分）：
// 有域名 → https://域名 + 原路径；没域名 → 当前打开面板的地址 + 原路径。
function rebaseShownSubUrls(domain) {
  const base = domain ? 'https://' + domain : location.origin;
  ['#sub-url-value', '#sub-user-url-value'].forEach((sel) => {
    const el = $(sel);
    if (!el || !el.textContent) return;
    try {
      const u = new URL(el.textContent.trim());
      if (!u.pathname.startsWith('/sub/')) return;
      el.textContent = base + u.pathname + u.search + u.hash;
    } catch (e) { /* 不是合法 URL 就不动 */ }
  });
}
// Worker 卡片里的示例订阅链接：填了订阅对外域名就用它，没填时用面板自己的地址。
function updateWorkerExampleLink(domain) {
  const el = $('#worker-example-link');
  if (!el) return;
  el.textContent = domain ? 'https://' + domain + '/sub/<令牌>' : location.origin + '/sub/<令牌>';
}
// ---------- 订阅用户：一人一条（v1.3.0） ----------
// 列表只含名字与生成时间，不含令牌；每行都有“复制”按钮，随时能重新拿链接。
async function loadSubUsers() {
  const list = $('#sub-user-list');
  if (!list) return;
  try {
    const r = await api('/api/v1/subscriptions/users');
    const users = r.users || [];
    if (!users.length) { list.innerHTML = '<p class="muted small">还没有订阅用户：上面填名字点“新建订阅用户”，每人一条独立链接。</p>'; return; }
    list.innerHTML = '<h3>订阅用户</h3>' + users.map((u) => '<div class="kv"><span>' + esc(u.name || '') + (u.note ? ' <span class="small muted">（' + esc(u.note) + '）</span>' : '') + '<br><span class="small muted">' + esc(u.kind === 'merged' ? '合并订阅：' + (u.summary || '') : '单面板全量订阅') + ' · 生成于 ' + esc(fmtDate(u.created_at)) + '</span></span><span><button class="btn secondary sm" type="button" data-sub-user-copy="' + esc(u.id) + '">复制</button> <button class="btn secondary sm" type="button" data-sub-user-rotate="' + esc(u.id) + '">轮换</button> <button class="btn danger sm" type="button" data-sub-user-revoke="' + esc(u.id) + '">撤销</button></span></div>').join('') +
      '<p class="muted small">撤销只影响那个人，其他人的链接照常；轮换后旧链接立即失效。</p>';
    $$('[data-sub-user-copy]', list).forEach((b) => b.addEventListener('click', async () => {
      try {
        const r = await fetchSubLink('user', b.dataset.subUserCopy);
        if (r.legacy) { await legacySubPrompt(() => rotateSubUser(b.dataset.subUserCopy)); return; }
        $('#sub-user-url-value').textContent = r.url || '';
        const box = $('#sub-user-once');
        if (box) { box.classList.remove('hidden'); box.hidden = false; }
        copyText(subLinkWithMode(r.url || '', ''), b);
      } catch (e) { toast('复制失败：' + e.message, 'err'); }
    }));
    $$('[data-sub-user-rotate]', list).forEach((b) => b.addEventListener('click', async () => {
      const ok = await confirmModal('轮换订阅用户链接', '<p>轮换后这个人的旧订阅链接立即失效，客户端里要换成新链接。确定吗？</p>', '轮换', true);
      if (!ok) return;
      try { await rotateSubUser(b.dataset.subUserRotate); }
      catch (e) { toast('轮换失败：' + e.message, 'err'); }
    }));
    $$('[data-sub-user-revoke]', list).forEach((b) => b.addEventListener('click', async () => {
      const ok = await confirmModal('撤销订阅用户', '<p>撤销后这个人的订阅链接立即失效，其他人照常。确定吗？</p>', '撤销', true);
      if (!ok) return;
      try { await api('/api/v1/subscriptions/users/' + encodeURIComponent(b.dataset.subUserRevoke), { method: 'DELETE' }); toast('已撤销', 'ok'); loadSubUsers(); }
      catch (e) { toast('撤销失败：' + e.message, 'err'); }
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
async function subUserCreate() {
  const name = $('#sub-user-name').value.trim();
  if (!name) { toast('请先填名字（用来认人）', 'err'); return; }
  try {
    const r = await apiPost('/api/v1/subscriptions/users', { name, note: $('#sub-user-note').value.trim() });
    $('#sub-user-url-value').textContent = r.url || '';
    const box = $('#sub-user-once');
    if (box) { box.classList.remove('hidden'); box.hidden = false; }
    $('#sub-user-name').value = ''; $('#sub-user-note').value = '';
    toast('订阅用户已新建，链接显示在下面，随时能复制', 'ok');
    loadSubUsers();
  } catch (err) { toast('新建失败：' + err.message, 'err'); }
}
// 订阅用户链接的二维码：与通用订阅链接同一套后端生成，默认不带模式参数。
async function loadSubUserQr() {
  const box = $('#sub-user-qr-box'), img = $('#sub-user-qr-img'), fig = $('#sub-user-qr-figure');
  if (!box || !img) return;
  const url = subLinkWithMode($('#sub-user-url-value').textContent, '');
  try {
    const r = await apiPost('/api/v1/subscription/qr', { url });
    if (!r.qr) throw new Error('后端没有返回二维码');
    img.src = r.qr;
    if (fig) fig.dataset.qr = r.qr;
    box.classList.remove('hidden'); box.hidden = false;
  } catch (err) { toast('二维码生成失败：' + err.message, 'err'); }
}
// ---------- Worker 反代代码生成（v1.3.0） ----------
async function loadWorkerCode() {
  const origin = $('#worker-origin').value.trim();
  const box = $('#worker-code-box'), pre = $('#worker-code');
  if (!box || !pre) return;
  try {
    const r = await api('/api/v1/subscriptions/worker?origin=' + encodeURIComponent(origin));
    pre.textContent = r.code || '';
    const warn = $('#worker-code-warn');
    if (warn) {
      if (r.warning) { warn.textContent = r.warning; warn.classList.remove('hidden'); warn.hidden = false; }
      else { warn.textContent = ''; warn.classList.add('hidden'); warn.hidden = true; }
    }
    box.classList.remove('hidden'); box.hidden = false;
    toast('Worker 代码已生成，复制后去 Cloudflare 部署', 'ok');
  } catch (err) { toast('生成失败：' + err.message, 'err'); }
}
// 合并订阅弹窗：显示一条刚生成/轮换的订阅链接，带复制、
// 二维码、模式选择，以及轮换/撤销按钮。opts: {title, rotated, name, mergedId}
// mergedId 为空时不显示轮换/撤销（老调用方只传链接时用不到）。
function openMergedSubModal(url, opts) {
  opts = opts || {};
  let curUrl = url;
  const m = openModal('<h2>' + esc(opts.title || '订阅链接') + '</h2>' +
    (opts.rotated ? '<div class="warn-box" data-merged-warn>这份选择之前生成过，已轮换：旧链接立即失效，请用下面这条新链接。</div>' : '<div class="warn-box" data-merged-warn>链接已加密存在面板里，关掉弹窗后在合并订阅列表里点“复制”随时能拿。</div>') +
    (opts.name ? '<div class="kv"><span>归属到人</span><b>' + esc(opts.name) + '</b></div>' : '') +
    '<div class="kv"><span>订阅链接</span><b class="mono" style="word-break:break-all" data-merged-url>' + esc(curUrl) + '</b></div>' +
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
// 只在合并订阅生成时传，填了就新起一条、不走轮换）。
async function showSubForSelection(selection, title, name) {
  try {
    const r = await apiPost('/api/v1/subscriptions/merged', { selection, name: name || undefined });
    openMergedSubModal(r.url || '', { title, rotated: r.rotated, name, mergedId: r.id });
    loadMergedSubsList();
  } catch (err) { toast('生成失败：' + err.message, 'err'); }
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

async function loadCentral() {
  const wrap = $('#central-list');
  centralDetailId = null;
  loadCentralSubTree();
  try {
    const r = await api('/api/v1/central/remotes');
    centralRemotes = r.remotes || [];
    if (!centralRemotes.length) {
      wrap.innerHTML = '<div class="empty">还没登记远程面板。<br>到被管面板的设置页生成远程管理令牌，再在上面填地址和令牌登记。登记后这里会显示每台远程面板的在线状态、版本、落地数、流量汇总与最近到期时间。</div>';
      return;
    }
    wrap.innerHTML = '<div class="grid cols-3">' + centralRemotes.map((rm) => {
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
        '<button class="btn danger sm" type="button" data-central-del="' + esc(rm.id) + '">取消登记</button></div></div>';
    }).join('') + '</div>';
    $$('[data-central-open]', wrap).forEach((b) => b.addEventListener('click', () => window.open(b.dataset.centralOpen, '_blank', 'noopener')));
    $$('[data-central-detail]', wrap).forEach((b) => b.addEventListener('click', () => openCentralDetail(b.dataset.centralDetail)));
    $$('[data-central-del]', wrap).forEach((b) => b.addEventListener('click', () => centralDeregister(b.dataset.centralDel)));
  } catch (e) {
    wrap.innerHTML = '<div class="card"><div class="error-box">加载失败：' + esc(e.message) + '</div></div>';
  }
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
async function loadCentralSubTree() {
  const tree = $('#central-sub-tree');
  if (!tree) return;
  try {
    const r = await api('/api/v1/central/remotes');
    const remotes = r.remotes || [];
    // 管理面板模式没有本机落地：订阅树里不列“本面板”，只列远程面板
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
    centralSubTree = panels;
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
    showSubForSelection([{ panel: pid, landing: li }], (l ? l.title : '落地') + ' 的订阅');
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
async function loadMergedSubsList() {
  const list = $('#central-sub-list');
  if (!list) return;
  try {
    const r = await api('/api/v1/subscriptions/merged');
    const subs = r.subs || [];
    if (!subs.length) { list.innerHTML = '<p class="muted small">还没有合并订阅：上面勾选落地后点“生成合并订阅”。</p>'; return; }
    list.innerHTML = '<h3>已生成的合并订阅</h3>' + subs.map((s) => '<div class="kv"><span>' + (s.name ? esc(s.name) + ' — ' : '') + esc(s.summary || '') + '<br><span class="small muted">生成于 ' + esc(fmtDate(s.created_at)) + '</span></span><span><button class="btn secondary sm" type="button" data-sub-copy-merged="' + esc(s.id) + '" data-sub-name-merged="' + esc(s.name || '') + '">复制</button> <button class="btn secondary sm" type="button" data-sub-rotate-merged="' + esc(s.id) + '" data-sub-name-merged="' + esc(s.name || '') + '">轮换</button> <button class="btn danger sm" type="button" data-sub-revoke-merged="' + esc(s.id) + '">撤销</button></span></div>').join('') +
      '<p class="muted small">每条订阅都有“复制”按钮，链接加密存在面板里，随时能重新复制；旧链接（1.3.7 之前生成的）没有存密文，点轮换生成一条新的（旧链接立即失效），或重新勾选生成（不填名称时同一选择重新生成会轮换；填了名称是新起一条，各归各人）。</p>';
    $$('[data-sub-copy-merged]', list).forEach((b) => b.addEventListener('click', async () => {
      try {
        const r = await fetchSubLink('merged', b.dataset.subCopyMerged);
        if (r.legacy) { await legacySubPrompt(() => rotateMergedSub(b.dataset.subCopyMerged, b.dataset.subNameMerged || undefined)); return; }
        openMergedSubModal(r.url || '', { title: '订阅链接', name: b.dataset.subNameMerged || undefined, mergedId: b.dataset.subCopyMerged });
      } catch (e) { toast('复制失败：' + e.message, 'err'); }
    }));
    $$('[data-sub-rotate-merged]', list).forEach((b) => b.addEventListener('click', async () => {
      const ok = await confirmModal('轮换订阅链接', '<p>轮换后旧订阅链接立即失效，客户端里已导入的订阅要换成新链接。确定吗？</p>', '轮换订阅链接', true);
      if (!ok) return;
      try { await rotateMergedSub(b.dataset.subRotateMerged, b.dataset.subNameMerged || undefined); }
      catch (e) { toast('轮换失败：' + e.message, 'err'); }
    }));
    $$('[data-sub-revoke-merged]', list).forEach((b) => b.addEventListener('click', async () => {
      const ok = await confirmModal('撤销合并订阅', '<p>撤销后这条订阅链接立即失效。确定吗？</p>', '撤销', true);
      if (!ok) return;
      try { await api('/api/v1/subscriptions/merged/' + encodeURIComponent(b.dataset.subRevokeMerged), { method: 'DELETE' }); toast('已撤销', 'ok'); loadMergedSubsList(); }
      catch (e) { toast('撤销失败：' + e.message, 'err'); }
    }));
  } catch (e) { list.innerHTML = '<div class="error-box">加载失败：' + esc(e.message) + '</div>'; }
}
// 轮换一条合并订阅（不含确认框，确认由调用方做）：旧链接立即失效，
// 新链接在弹窗里直接显示，之后随时能复制。
async function rotateMergedSub(id, name) {
  const r = await apiPost('/api/v1/subscriptions/merged/' + encodeURIComponent(id) + '/rotate', {});
  openMergedSubModal(r.url || '', { title: '订阅链接', rotated: true, name: name || undefined, mergedId: r.id });
  loadMergedSubsList();
  return r;
}
async function loadCentralLandings(id, page) {
  if (page) centralDetailPage = page;
  centralDetailId = id;
  centralOpenLanding = null; // 默认全收起：翻页/重进都回到折叠态
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
      '<p class="muted small">点一行展开看它的链接、二维码与操作；一次只展开一个，再点一次收起。实时日志不代理，要看日志点上面卡片里的“打开远程面板”跳过去。</p>' +
      pg.slice.map((l) => {
        const ports = (l.ports || []).map((p) => esc(p.proto) + ' ' + esc(p.listen) + '→' + esc(p.remote)).join('、') || '—';
        const on = l.online === true ? '<span class="badge green">在线</span>' : l.online === false ? '<span class="badge red">离线</span>' : '<span class="badge">未知</span>';
        const lat = l.latency_ms == null ? '—' : esc(l.latency_ms) + ' ms';
        const cert = l.cert_days_left == null ? '—' : esc(l.cert_days_left) + ' 天' + (l.renew_mode ? '（' + esc(l.renew_mode) + '）' : '');
        const exp = l.days_left == null ? '永久' : (l.days_left < 0 ? '已到期 ' + esc(-l.days_left) + ' 天' : '剩余 ' + esc(l.days_left) + ' 天');
        const tr = l.traffic || {};
        const traffic = '今日 ' + esc(fmtBytes((tr.today_up || 0) + (tr.today_down || 0))) + ' / 本月 ' + esc(fmtBytes((tr.month_up || 0) + (tr.month_down || 0))) + ' / 累计 ' + esc(fmtBytes((tr.total_up || 0) + (tr.total_down || 0))) + (tr.limit_bytes ? '（上限 ' + esc(fmtBytes(tr.limit_bytes)) + '，已用 ' + esc(Math.round(tr.pct || 0)) + '%）' : '（不限）');
        const subs = (l.subnodes || []).map((sn) =>
          '<div class="kv"><span>' + esc(sn.domain) + '</span><span><button class="btn danger sm" type="button" data-central-delsub="' + esc(l.index) + '|' + esc(sn.domain) + '">删除节点</button></span></div>'
        ).join('');
        // 行头也放到期与流量（和本机卡片同一款式）：不用点开就看得到。
        const expCls = l.days_left == null ? 'grey' : l.days_left < 0 ? 'red' : l.days_left <= 7 ? 'red' : l.days_left <= 30 ? 'yellow' : 'green';
        const trafficHead = '今日 ' + esc(fmtBytes((tr.today_up || 0) + (tr.today_down || 0))) + ' · 累计 ' + esc(fmtBytes((tr.total_up || 0) + (tr.total_down || 0)));
        // 子节点没有独立流量：同一落地的节点共用一份流量（中转机按端口统计，
        // 分不清节点），下面展开体里的流量就是该落地的共享流量，别当成单个节点的。
        const subShared = (l.subnodes && l.subnodes.length)
          ? '<div class="small muted">子节点流量：同一落地的节点共用一份流量（中转机按端口统计，分不清节点），下面是该落地的共享流量。</div>'
          : '';
        return '<div class="central-landing">' +
          '<div class="central-landing-head" data-central-toggle="' + esc(l.index) + '" role="button" tabindex="0"><span>' + esc(centralLandingTitle(l)) + ' <span class="small">#' + esc(l.index) + '</span></span><span>' + on + ' ' + lat +
          ' <span class="small">到期 <span class="badge ' + expCls + '">' + exp + '</span></span>' +
          ' <span class="small muted">' + trafficHead + '</span>' +
          ' <span class="central-caret">▸</span></span></div>' +
          subShared +
          '<div class="central-landing-body hidden" data-central-body="' + esc(l.index) + '">' +
          '<div class="kv"><span>落地</span><b>' + esc(centralLandingTitle(l)) + '</b></div>' +
          '<div class="kv"><span>监听端口</span><b>' + ports + '</b></div>' +
          '<div class="kv"><span>证书</span><b>' + cert + '</b></div>' +
          '<div class="kv"><span>到期</span><b>' + exp + '</b></div>' +
          '<div class="kv"><span>流量</span><b>' + traffic + '</b></div>' +
          (subs ? '<div class="kv"><span>子节点</span><b>' + esc((l.subnodes || []).length) + ' 个</b></div>' + subs : '') +
          '<div class="central-landing-links" data-central-links-box="' + esc(l.index) + '"></div>' +
          '<div class="btn-row"><button class="btn secondary sm" type="button" data-central-addnode="' + esc(l.index) + '">＋ 新增节点</button>' +
          '<button class="btn secondary sm" type="button" data-central-edit="' + esc(l.index) + '">编辑落地</button>' +
          '<button class="btn secondary sm" type="button" data-central-renew="' + esc(l.index) + '">续期</button>' +
          '<button class="btn secondary sm" type="button" data-central-traffic="' + esc(l.index) + '">流量设置</button>' +
          '<button class="btn secondary sm" type="button" data-central-addtraffic="' + esc(l.index) + '">续量</button>' +
          '<button class="btn secondary sm" type="button" data-central-origlink="' + esc(l.index) + '">补链接</button>' +
          '<button class="btn secondary sm" type="button" data-central-certs="' + esc(l.index) + '">证书</button>' +
          '<button class="btn secondary sm" type="button" data-central-sub-landing="' + esc(l.index) + '">订阅</button>' +
          '<button class="btn danger sm" type="button" data-central-del-landing="' + esc(l.index) + '">删除该落地</button></div></div></div>';
      }).join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'central');
    $$('[data-central-toggle]', detail).forEach((h) => {
      const toggle = () => centralToggleLanding(Number(h.dataset.centralToggle));
      h.addEventListener('click', toggle);
      h.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(); } });
    });
    $$('[data-central-addnode]', detail).forEach((b) => b.addEventListener('click', () => {
      const l = centralDetailLandings.find((x) => x.index === Number(b.dataset.centralAddnode));
      centralAddNodeWizard(id, Number(b.dataset.centralAddnode), l ? centralLandingTitle(l) : ('#' + b.dataset.centralAddnode));
    }));
    $$('[data-central-edit]', detail).forEach((b) => b.addEventListener('click', () => {
      const l = centralDetailLandings.find((x) => x.index === Number(b.dataset.centralEdit));
      centralEditLanding(id, Number(b.dataset.centralEdit), l);
    }));
    $$('[data-central-renew]', detail).forEach((b) => b.addEventListener('click', () => centralRenewLanding(id, Number(b.dataset.centralRenew))));
    $$('[data-central-traffic]', detail).forEach((b) => b.addEventListener('click', () => {
      const l = centralDetailLandings.find((x) => x.index === Number(b.dataset.centralTraffic));
      centralTrafficSettings(id, Number(b.dataset.centralTraffic), l);
    }));
    $$('[data-central-addtraffic]', detail).forEach((b) => b.addEventListener('click', () => centralAddTraffic(id, Number(b.dataset.centralAddtraffic))));
    $$('[data-central-origlink]', detail).forEach((b) => b.addEventListener('click', () => {
      const l = centralDetailLandings.find((x) => x.index === Number(b.dataset.centralOriglink));
      centralPasteOrigLink(id, Number(b.dataset.centralOriglink), l);
    }));
    $$('[data-central-certs]', detail).forEach((b) => b.addEventListener('click', () => centralCerts(id)));
    $$('[data-central-delsub]', detail).forEach((b) => b.addEventListener('click', () => {
      const parts = String(b.dataset.centralDelsub).split('|');
      centralDeleteSubnode(id, Number(parts[0]), parts[1]);
    }));
    $$('[data-central-sub-landing]', detail).forEach((b) => b.addEventListener('click', () => {
      const l = centralDetailLandings.find((x) => x.index === Number(b.dataset.centralSubLanding));
      showSubForSelection([{ panel: id, landing: Number(b.dataset.centralSubLanding) }], (l ? centralLandingTitle(l) : '落地') + ' 的订阅');
    }));
    $$('[data-central-del-landing]', detail).forEach((b) => b.addEventListener('click', () => centralDeleteLanding(id, Number(b.dataset.centralDelLanding))));
    if (pg.totalPages > 1) wirePager(detail, 'central', pg.cur, pg.totalPages, (p) => loadCentralLandings(id, p));
  } catch (e) {
    detail.innerHTML = '<div class="error-box">加载失败：' + esc(e.message) + '</div>';
  }
}
// 手风琴开关：点开一个、收起其他；再点已开的就收起。链接只在第一次展开时取。
function centralToggleLanding(index) {
  const detail = centralDetailEl();
  if (!detail) return;
  const wasOpen = centralOpenLanding === index;
  $$('.central-landing-body', detail).forEach((b) => b.classList.add('hidden'));
  $$('.central-caret', detail).forEach((c) => { c.textContent = '▸'; });
  if (wasOpen) { centralOpenLanding = null; return; }
  centralOpenLanding = index;
  const body = $('[data-central-body="' + index + '"]', detail);
  const head = $('[data-central-toggle="' + index + '"]', detail);
  if (body) body.classList.remove('hidden');
  if (head) { const c = head.querySelector('.central-caret'); if (c) c.textContent = '▾'; }
  centralLoadLandingLinks(centralDetailId, index);
}
async function centralLoadLandingLinks(remoteId, index) {
  const detail = centralDetailEl();
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
    '<label>预设 <span class="hint">落地机装的节点类型</span></label><select id="central-add-preset"><option value="1">预设 1：tiktok-node（Xray）</option><option value="2">预设 2：sing-box-node</option><option value="3">预设 3：自定义端口</option></select>' +
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
  const mode = $('#central-add-mode', m).value;
  const relayAddr = (mode === 'door' && $('#central-add-relay-addr', m)) ? $('#central-add-relay-addr', m).value.trim() : '';
  const name = $('#central-add-name', m).value.trim();
  if (name && name.length > 64) { toast('名称不能超过 64 字', 'err'); return null; }
  const origLink = $('#central-add-orig-link', m).value.trim();
  if (origLink && !/^(vless|trojan|ss|vmess):\/\//.test(origLink)) { toast('原始节点链接格式不正确，需以 vless://、trojan://、ss:// 或 vmess:// 开头', 'err'); return null; }
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
        if (!link || !/^(vless|trojan|ss|vmess):\/\//.test(link)) { toast('请粘贴正确的节点链接（vless:// 等开头）', 'err'); return; }
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
    '<option value="1"' + (l.preset === '1' ? ' selected' : '') + '>预设 1：tiktok-node（Xray）</option>' +
    '<option value="2"' + (l.preset === '2' ? ' selected' : '') + '>预设 2：sing-box-node</option>' +
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
  const mode = $('#central-edit-mode', m).value;
  const relayAddr = (mode === 'door' && $('#central-edit-relay-addr', m)) ? $('#central-edit-relay-addr', m).value.trim() : '';
  const name = $('#central-edit-name', m).value.trim();
  if (name && name.length > 64) { toast('名称不能超过 64 字', 'err'); return null; }
  const origLink = $('#central-edit-orig-link', m).value.trim();
  if (origLink && !/^(vless|trojan|ss|vmess):\/\//.test(origLink)) { toast('原始节点链接格式不正确，需以 vless://、trojan://、ss:// 或 vmess:// 开头', 'err'); return null; }
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
    if (!origLink || !/^(vless|trojan|ss|vmess):\/\//.test(origLink)) { toast('请粘贴正确的节点链接（vless:// 等开头）', 'err'); return; }
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
        h += '<div class="kv"><span>❌ ' + esc(is.message || is.code) + '</span><span><button class="btn sm" type="button" data-central-fix="' + i + '">一键修复</button></span></div>';
      });
      const body = $('#central-check-body', m);
      body.innerHTML = h;
      body.dataset.issues = JSON.stringify(issues);
      $$('[data-central-fix]', body).forEach((b) => b.addEventListener('click', () => centralFixIssue(remoteId, JSON.parse(body.dataset.issues)[Number(b.dataset.centralFix)])));
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
  on('#central-refresh', 'click', centralRefreshAll);
  on('#central-sub-generate', 'click', async () => {
    const selection = [];
    (centralSubTree || []).forEach((p) => p.landings.forEach((l) => {
      if (centralSubChecked[subTreeKey(p.id, l.index)]) selection.push({ panel: p.id, landing: l.index });
    }));
    if (!selection.length) { toast('请先勾选至少一个落地', 'err'); return; }
    const nameEl = $('#central-sub-name');
    await showSubForSelection(selection, '合并订阅', nameEl ? nameEl.value.trim() : '');
  });
  on('#central-add-form', 'submit', async (e) => {
    e.preventDefault();
    const name = $('#central-name').value.trim();
    const baseUrl = $('#central-url').value.trim();
    const token = $('#central-token').value.trim();
    if (!baseUrl || !token) { toast('请填远程面板地址和令牌', 'err'); return; }
    try {
      const d = await apiPostRaw('/api/v1/central/remotes', { name, base_url: baseUrl, token });
      if (d.ok === false) { toast(centralOpError(d), 'err'); return; }
      toast('远程面板已登记', 'ok');
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
  safeInit(buildNav);
  safeInit(initDrawer);
  safeInit(initLogin);
  safeInit(initCheck);
  safeInit(initAddForm);
  safeInit(initTwin);
  safeInit(initService);
  safeInit(initSettings);
  safeInit(initMonitorSettings);
  safeInit(initLatencySettings);
  safeInit(initUpdatePoll);
  safeInit(initFirewall);
  safeInit(initTraffic);
  safeInit(initCentral);
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
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeModal(); });
  // checkMe 必须执行：不管前面哪个初始化失败，都要决定显示登录页还是后台
  try { checkMe(); } catch (e) { showLogin(); }
});
