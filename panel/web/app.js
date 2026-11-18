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
function openModal(html) {
  const root = $('#modal-root');
  if (!root) return null; // 弹窗容器缺失时直接放弃，绝不抛错
  root.innerHTML = '<div class="modal-backdrop"><div class="modal" role="dialog" aria-modal="true">' + html + '</div></div>';
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

function buildNav() {
  const side = $('#side-nav');
  if (!side) return; // 导航容器缺失时直接跳过
  side.innerHTML = '';
  PAGES.forEach((p) => {
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
function showLogin() {
  // class 与 hidden 属性一起切：只靠 class 的话，style.css 加载失败时两边会堆在一起
  const app = $('#app'), login = $('#login-view');
  if (app) { app.classList.add('hidden'); app.hidden = true; }
  if (login) { login.classList.remove('hidden'); login.hidden = false; }
}
function showApp() {
  const app = $('#app'), login = $('#login-view');
  if (login) { login.classList.add('hidden'); login.hidden = true; }
  if (app) { app.classList.remove('hidden'); app.hidden = false; }
}
async function checkMe() {
  try {
    const me = await api('/api/v1/me');
    const userEl = $('#side-user');
    if (userEl) userEl.textContent = me.username || 'admin';
    if (me.version) { const verEl = $('#side-version'); if (verEl) verEl.textContent = me.version; }
    showApp();
    const hash = (location.hash || '').replace('#', '');
    goto(PAGES.some((p) => p.id === hash) ? hash : 'overview');
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
    if (!username || !password) { $('#login-error').textContent = '请输入用户名和密码'; $('#login-error').classList.remove('hidden'); return; }
    const btn = $('#login-btn'); btn.disabled = true; btn.textContent = '登录中…';
    $('#login-error').classList.add('hidden'); $('#login-locked').classList.add('hidden');
    try {
      await apiPost('/api/v1/login', { username, password }, true);
      $('#login-pass').value = '';
      await checkMe();
    } catch (err) {
      const msg = String(err.message || '');
      if (msg.indexOf('429') >= 0 || msg.indexOf('锁定') >= 0 || msg.indexOf('locked') >= 0) $('#login-locked').classList.remove('hidden');
      const box = $('#login-error');
      box.textContent = msg.indexOf('401') >= 0 || msg.indexOf('账号或密码') >= 0 || msg.indexOf('用户名或密码') >= 0 ? '用户名或密码错误，请重试' : msg;
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
      if (qr[k]) h += '<figure><img src="' + esc(qr[k]) + '" alt="' + esc(k) + ' 二维码"><figcaption>' + esc(k) + ' 二维码</figcaption></figure>';
    });
    h += '</div>';
  }
  return h;
}
// 事件委托：所有 data-copy 按钮
document.addEventListener('click', (e) => {
  const b = e.target.closest('[data-copy]');
  if (b) copyText(b.dataset.copy, b);
  const g = e.target.closest('[data-goto]');
  if (g) goto(g.dataset.goto);
  const x = e.target.closest('[data-expand-links]');
  if (x) {
    const card = x.closest('.card');
    const box = card ? card.querySelector('.extra-links') : null;
    if (!box) return;
    const nowHidden = box.classList.toggle('hidden');
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
  const pg = paginate(landingsCache, overviewPage);
  overviewPage = pg.cur;
  const relayAddr = (statusCache && statusCache.relay_addr) || '';
  wrap.innerHTML = pg.slice.map((l) => {
    const exp = daysLeftText(l.expiry);
    const ports = (l.ports || []).map((p) => esc(p.proto) + ' ' + esc(p.listen) + '→' + esc(p.remote)).join('、') || '—';
    return '<div class="card' + (l.online === false ? ' offline' : '') + '"><div style="display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap">' +
      '<h2>' + esc(l.cert_domain || l.domain || l.landing) + '</h2>' + statusBadge(l) + '</div>' +
      '<div class="kv"><span>落地</span><b>' + esc(l.landing) + '</b></div>' +
      '<div class="kv"><span>监听端口</span><b>' + ports + '</b></div>' +
      '<div class="kv"><span>类型</span><b>' + esc(l.mode_note || '—') + '</b></div>' +
      '<div class="kv"><span>模式</span><b>' + modeText(l, relayAddr) + '</b></div>' +
      '<div class="kv"><span>延迟</span><b>' + latencyHtml(l) + '</b></div>' +
      '<div class="kv"><span>证书</span><b>' + certBadge(l.cert_days_left) + ' · ' + esc(l.renew_mode || '') + '</b></div>' +
      '<div class="kv"><span>到期</span><b><span class="badge ' + exp.cls + '">' + esc(exp.text) + '</span></b></div>' +
      '<div style="margin-top:10px">' + trafficBarHtml(l) + '</div>' +
      ((l.subnodes && l.subnodes.length) ? '<div class="small">子节点：' + l.subnodes.map((s) => esc(s.domain)).join('、') + '</div>' : '') +
      '<div class="btn-row"><button class="btn secondary sm" type="button" data-manage="' + l.index + '">管理</button>' +
      '<button class="btn secondary sm" type="button" data-goto="links">查看链接</button>' +
      '<button class="btn secondary sm" type="button" data-goto="certs">证书</button></div></div>';
  }).join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'overview');
  $$('[data-manage]', wrap).forEach((b) => b.addEventListener('click', () => goto('manage')));
  if (pg.totalPages > 1) wirePager(wrap, 'overview', pg.cur, pg.totalPages, (p) => renderLandingCards(p));
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
  return {
    landing, preset, mode,
    relay_addr: relayAddr || undefined,
    sb_mode: preset === '2' ? $('#add-sb-mode').value : undefined,
    custom_ports: customPorts || undefined,
    want_udp: $('#add-want-udp').checked,
    cert_domain: certDomain || undefined,
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
      (r.qr ? '' : '<p class="muted">二维码不可用（服务器未装 qrencode），仅显示链接。</p>') +
      linksHtml(r.links, r.qr) + '</div>';
    toast('落地添加成功', 'ok');
    loadOverview();
  } catch (e) {
    const msg = String(e.message || '');
    // 后端也可能把 need_choice 放在错误体里
    toast('添加失败：' + msg, 'err');
  } finally { btn.disabled = false; btn.textContent = '提交并添加落地'; }
}
async function showShareChoice(resp, payload) {
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
    await submitAdd(payload, { mode: sel.value, overrides: Object.keys(overrides).length ? overrides : undefined });
  });
}

/* ---------- 落地管理 ---------- */
let managePage = 1;
async function loadManage(page) {
  if (page) managePage = page;
  const wrap = $('#manage-list');
  try {
    const s = await api('/api/v1/status');
    landingsCache = s.landings || [];
    if (!landingsCache.length) { wrap.innerHTML = '<div class="empty">还没有落地节点。</div>'; }
    else {
      const pg = paginate(landingsCache, managePage);
      managePage = pg.cur;
      const relayAddr = s.relay_addr || '';
      wrap.innerHTML = pg.slice.map((l) => {
        const exp = daysLeftText(l.expiry);
        const ports = (l.ports || []).map((p) => esc(p.proto) + ' ' + esc(p.listen) + '→' + esc(p.remote)).join('、') || '—';
        const subs = (l.subnodes || []).map((sn) =>
          '<div class="kv"><span>🔹 ' + esc(sn.domain) + '</span><span><button class="btn ghost sm" type="button" data-del-sub="' + esc(sn.domain) + '" data-idx="' + l.index + '">删除该节点</button></span></div>').join('');
        return '<div class="card" style="margin-bottom:16px"><div style="display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap">' +
          '<h2>' + esc(l.cert_domain || l.domain || l.landing) + ' <span class="small">#' + l.index + '</span></h2>' + statusBadge(l) + '</div>' +
          '<div class="kv"><span>落地</span><b>' + esc(l.landing) + '</b></div>' +
          '<div class="kv"><span>监听端口</span><b>' + ports + '</b></div>' +
          '<div class="kv"><span>类型</span><b>' + esc(l.mode_note || '—') + '</b></div>' +
          '<div class="kv"><span>模式</span><b>' + modeText(l, relayAddr) + '</b></div>' +
          '<div class="kv"><span>证书</span><b>' + certBadge(l.cert_days_left) + ' · ' + esc(l.renew_mode || '') + '</b></div>' +
          '<div class="kv"><span>到期</span><b><span class="badge ' + exp.cls + '">' + esc(exp.text) + '</span>' + (l.expiry ? '（' + esc(fmtDate(l.expiry)) + '）' : '') + '</b></div>' +
          '<div style="margin-top:8px">' + trafficBarHtml(l) + '</div>' +
          (subs ? '<h3 style="margin-top:12px">子节点（同一落地机上的多个节点，共用一条转发）</h3>' + subs : '<p class="small">暂无子节点。</p>') +
          '<div class="btn-row">' +
          '<button class="btn secondary sm" type="button" data-addnode="' + l.index + '">＋ 给落地加节点</button>' +
          '<button class="btn secondary sm" type="button" data-renew="' + l.index + '">续期</button>' +
          '<button class="btn secondary sm" type="button" data-addtraffic="' + l.index + '">续量</button>' +
          '<button class="btn secondary sm" type="button" data-trafficset="' + l.index + '">流量限制</button>' +
          '<button class="btn danger sm" type="button" data-del="' + l.index + '">删除（进回收站）</button></div></div>';
      }).join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'manage');
      $$('[data-del]', wrap).forEach((b) => b.addEventListener('click', () => deleteLanding(Number(b.dataset.del))));
      $$('[data-renew]', wrap).forEach((b) => b.addEventListener('click', () => renewLanding(Number(b.dataset.renew))));
      $$('[data-addtraffic]', wrap).forEach((b) => b.addEventListener('click', () => addTraffic(Number(b.dataset.addtraffic))));
      $$('[data-trafficset]', wrap).forEach((b) => b.addEventListener('click', () => trafficSettings(Number(b.dataset.trafficset))));
      $$('[data-addnode]', wrap).forEach((b) => b.addEventListener('click', () => addNodeWizard(Number(b.dataset.addnode))));
      $$('[data-del-sub]', wrap).forEach((b) => b.addEventListener('click', () => deleteSubnode(Number(b.dataset.idx), b.dataset.delSub)));
      if (pg.totalPages > 1) wirePager(wrap, 'manage', pg.cur, pg.totalPages, (p) => loadManage(p));
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
  const ok = await confirmModal('删除落地', '<p>确定删除落地 <b>' + esc(l ? (l.cert_domain || l.landing) : index) + '</b> 吗？</p><p>删除是软删除：先进入回收站保留 7 天，期间可以一键恢复；7 天后才真正清除。转发规则会立即撤掉。</p>', '删除（进回收站）', true);
  if (!ok) return;
  try { await api('/api/v1/landings/' + index, { method: 'DELETE' }); toast('已移入回收站', 'ok'); loadManage(); }
  catch (e) { toast('删除失败：' + e.message, 'err'); }
}
async function renewLanding(index) {
  const m = openModal('<h2>续期</h2><p>续期只延长时限；如果是因为流量用完被停用，请另外「续量」。</p>' +
    '<label>续期天数</label><select id="renew-days"><option value="30">30 天</option><option value="90" selected>90 天</option><option value="180">180 天</option><option value="365">365 天</option></select>' +
    '<div class="btn-row"><button class="btn" id="renew-ok" type="button">确认续期</button><button class="btn secondary" id="renew-cancel" type="button">取消</button></div>');
  onIn(m, '#renew-cancel', 'click', closeModal);
  onIn(m, '#renew-ok', 'click', async () => {
    const days = Number($('#renew-days', m).value);
    closeModal();
    try { await apiPost('/api/v1/landings/' + index + '/renew', { days }); toast('续期成功，已延长 ' + days + ' 天', 'ok'); loadManage(); }
    catch (e) { toast('续期失败：' + e.message, 'err'); }
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
    try { await apiPost('/api/v1/landings/' + index + '/add-traffic', { bytes: Math.round(gb * 1073741824) }); toast('续量成功，已增加 ' + gb + ' GB', 'ok'); loadManage(); }
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
      toast('流量限制已保存', 'ok'); loadManage();
    } catch (e) { toast('保存失败：' + e.message, 'err'); }
  });
}
async function deleteSubnode(index, domain) {
  const ok = await confirmModal('删除子节点', '<p>确定删除子节点 <b>' + esc(domain) + '</b> 吗？</p><p>只删面板里的登记，不影响该落地的转发和其他节点。落地机上的对应节点请另外到落地机上删除。</p>', '删除该节点', true);
  if (!ok) return;
  try {
    await api('/api/v1/landings/' + index + '/subnodes/' + encodeURIComponent(domain), { method: 'DELETE' });
    toast('子节点已删除', 'ok'); loadManage();
  } catch (e) { toast('删除失败：' + e.message + '（若接口未提供，请到落地机手动处理）', 'err'); }
}

/* ---------- 给落地加节点：两步向导 ---------- */
async function addNodeWizard(index) {
  const l = landingsCache.find((x) => x.index === index);
  const m = openModal('<h2>给落地加节点（第 1 步，共 2 步）</h2>' +
    '<p>面板在中转机上，够不着落地机，所以分两步走：先填新子域名，拿到落地机命令；到落地机跑完命令，再把新链接粘贴回来登记。</p>' +
    '<p class="small">落地：' + esc(l ? (l.cert_domain || l.landing) : index) + '</p>' +
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
          on('#wz-close', 'click', () => { closeModal(); loadManage(); });
        } catch (e) { toast('登记失败：' + e.message, 'err'); }
      });
    } catch (e) { toast('生成命令失败：' + e.message, 'err'); }
  });
}

/* ---------- 节点链接 ---------- */
let linksPage = 1;
async function loadLinks(page) {
  if (page) linksPage = page;
  const wrap = $('#links-history');
  try {
    const r = await api('/api/v1/links');
    const groups = r.groups || [];
    if (!groups.length) { wrap.innerHTML = '<div class="empty">暂无历史记录（nodes.txt 为空）。</div>'; return; }
    const pg = paginate(groups, linksPage);
    linksPage = pg.cur;
    wrap.innerHTML = pg.slice.map((g) =>
      '<div class="card" style="margin-bottom:16px"><h2>落地 #' + esc(g.landing_index) + ' · ' + esc(g.landing || '') + (g.mode === 'door' ? ' <span class="badge blue">任意门模式</span>' : '') + '</h2>' + linksHtml(g.links, g.qr) + '</div>').join('') +
      pagerHtml(pg.total, pg.cur, pg.totalPages, 'links');
    if (pg.totalPages > 1) wirePager(wrap, 'links', pg.cur, pg.totalPages, (p) => loadLinks(p));
  } catch (e) {
    wrap.innerHTML = '<div class="card"><div class="error-box">加载失败：' + esc(e.message) + '</div></div>';
  }
}
function initTwin() {
  on('#twin-form', 'submit', async (e) => {
    e.preventDefault();
    const link = $('#twin-link').value.trim();
    const landingIp = $('#twin-landing').value.trim();
    const sni = $('#twin-sni').value.trim();
    if (!/^(vless|trojan|ss|vmess):\/\//.test(link)) { toast('链接格式不正确，需以 vless://、trojan://、ss:// 或 vmess:// 开头', 'err'); return; }
    if (!landingIp) { toast('请填写落地 IP', 'err'); return; }
    try {
      const r = await apiPost('/api/v1/links/twin', { link, landing_ip: landingIp, sni });
      const relayLabel = r.relay_label || '中转';
      const links = [];
      if (r.relay_link) links.push({ label: relayLabel, link: r.relay_link });
      if (r.direct_link) links.push({ label: '直连', link: r.direct_link });
      $('#twin-result').innerHTML = '<div class="card" style="margin-top:16px"><h2>生成结果</h2>' +
        (r.manual ? '<div class="warn-box">' + esc(r.manual) + '</div>' : '') +
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
    if (!certs.length) { wrap.innerHTML = '<div class="empty">暂无证书信息。</div>'; }
    else {
      const pg = paginate(certs, certsPage);
      certsPage = pg.cur;
      wrap.innerHTML = pg.slice.map((c) => {
        const days = c.days_left;
        const pct = days == null ? 0 : Math.max(0, Math.min(100, Math.round(days / 90 * 100)));
        const cls = days != null && days < 7 ? 'bad' : days != null && days < 30 ? 'warn' : '';
        return '<div class="card" style="margin-bottom:16px"><div style="display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap">' +
          '<h2>' + esc(c.domain || '') + '</h2>' + certBadge(days) + '</div>' +
          '<div class="kv"><span>落地</span><b>' + esc(c.landing || '') + '</b></div>' +
          '<div class="kv"><span>到期日</span><b>' + esc(c.expiry || '—') + '</b></div>' +
          '<div class="kv"><span>续签方式</span><b>' + esc(c.renew_mode || '') + (c.need_manual ? '（需手动续签，见下方六步引导）' : '（自动）') + '</b></div>' +
          '<div class="bar-row"><span>剩余天数进度（按 90 天有效期）</span><span>' + (days == null ? '未知' : days + ' 天') + '</span></div>' +
          '<div class="bar"><i class="' + cls + '" style="width:' + pct + '%"></i></div></div>';
      }).join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'certs');
      if (pg.totalPages > 1) wirePager(wrap, 'certs', pg.cur, pg.totalPages, (p) => loadCerts(p));
    }
    // 续签引导的落地选择
    const sel = $('#renew-landing');
    const opts = (landingsCache.length ? landingsCache : certs.map((c) => ({ index: c.landing_index, cert_domain: c.domain, landing: c.landing })));
    sel.innerHTML = opts.map((l) => '<option value="' + l.index + '">' + esc(l.cert_domain || l.domain || l.landing) + '</option>').join('');
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
    const lines = r.lines || [];
    box.textContent = lines.length ? lines.join('\n') : '暂无日志（日志文件不存在或为空）。';
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
async function loadSettingsPage() {
  try {
    const st = await api('/api/v1/settings');
    settingsCache = st;
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
    $('#update-box').innerHTML = '<div class="kv"><span>当前版本</span><b>' + esc(st.version || '—') + '</b></div>' +
      '<div class="kv"><span>最新版本</span><b>' + esc(st.latest_version || '—') + '</b></div>' +
      '<div class="kv"><span>状态</span><b>' + (st.update_available ? '<span class="badge yellow">有更新</span>' : '<span class="badge green">已是最新</span>') + '</b></div>' +
      '<div class="kv"><span>仓库</span><b><a href="https://github.com/nbbk/relay-node" target="_blank" rel="noopener">relay-node</a></b></div>';
  } catch (e) { toast('设置加载失败：' + e.message, 'err'); }
  loadBackups(); loadAudit();
}
function initSettings() {
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
    sel.innerHTML = landingsCache.map((l) => '<option value="' + l.index + '">' + esc(l.cert_domain || l.domain || l.landing) + '</option>').join('');
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
async function loadCentral() {
  const wrap = $('#central-list');
  const detail = $('#central-detail');
  if (detail) detail.innerHTML = '';
  centralDetailId = null;
  try {
    const r = await api('/api/v1/central/remotes');
    centralRemotes = r.remotes || [];
    if (!centralRemotes.length) {
      wrap.innerHTML = '<div class="empty">还没登记远程面板。<br>到被管面板的设置页生成远程管理令牌，再在上面填地址和令牌登记。登记后这里会显示每台远程面板的在线状态、版本、落地数与磁盘使用率。</div>';
      return;
    }
    wrap.innerHTML = centralRemotes.map((rm) => {
      const disk = (rm.disk_pct == null) ? '—' : Math.round(rm.disk_pct) + '%';
      const badge = rm.online ? '<span class="badge green">在线</span>' : '<span class="badge red">离线</span>';
      const checked = rm.last_checked_at ? esc(fmtDate(rm.last_checked_at)) : '尚未拉取';
      return '<div class="card" style="margin-bottom:16px"><div style="display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap">' +
        '<h2>' + esc(rm.name) + '</h2>' + badge + '</div>' +
        '<div class="kv"><span>地址</span><b class="mono">' + esc(rm.base_url) + '</b></div>' +
        '<div class="kv"><span>版本</span><b>' + esc(rm.version || '—') + '</b></div>' +
        '<div class="kv"><span>落地数</span><b>' + esc(rm.landing_count == null ? '—' : rm.landing_count) + '</b></div>' +
        '<div class="kv"><span>磁盘使用率</span><b>' + disk + '</b></div>' +
        '<div class="kv"><span>realm</span><b>' + (rm.realm_running ? '在跑' : '没在跑') + '</b></div>' +
        '<div class="kv"><span>上次检查</span><b>' + checked + '</b></div>' +
        (rm.online ? '' : '<div class="error-box">' + esc(rm.error || '远程面板连不上') + '</div>') +
        '<div class="btn-row">' +
        '<button class="btn secondary sm" type="button" data-central-open="' + esc(rm.base_url) + '">打开远程面板 ↗</button>' +
        '<button class="btn secondary sm" type="button" data-central-landings="' + esc(rm.id) + '">查看落地</button>' +
        '<button class="btn secondary sm" type="button" data-central-links="' + esc(rm.id) + '">查看链接</button>' +
        '<button class="btn secondary sm" type="button" data-central-add="' + esc(rm.id) + '">＋ 加落地</button>' +
        '<button class="btn secondary sm" type="button" data-central-restart="' + esc(rm.id) + '">重启 realm</button>' +
        '<button class="btn danger sm" type="button" data-central-del="' + esc(rm.id) + '">取消登记</button></div></div>';
    }).join('');
    $$('[data-central-open]', wrap).forEach((b) => b.addEventListener('click', () => window.open(b.dataset.centralOpen, '_blank', 'noopener')));
    $$('[data-central-landings]', wrap).forEach((b) => b.addEventListener('click', () => loadCentralLandings(b.dataset.centralLandings)));
    $$('[data-central-links]', wrap).forEach((b) => b.addEventListener('click', () => centralLinks(b.dataset.centralLinks)));
    $$('[data-central-add]', wrap).forEach((b) => b.addEventListener('click', () => centralAddLanding(b.dataset.centralAdd)));
    $$('[data-central-restart]', wrap).forEach((b) => b.addEventListener('click', () => centralRestart(b.dataset.centralRestart)));
    $$('[data-central-del]', wrap).forEach((b) => b.addEventListener('click', () => centralDeregister(b.dataset.centralDel)));
  } catch (e) {
    wrap.innerHTML = '<div class="card"><div class="error-box">加载失败：' + esc(e.message) + '</div></div>';
  }
}
async function loadCentralLandings(id, page) {
  if (page) centralDetailPage = page;
  centralDetailId = id;
  const rm = centralRemotes.find((x) => x.id === id);
  const detail = $('#central-detail');
  detail.innerHTML = '<div class="card">加载中…</div>';
  try {
    const r = await api('/api/v1/central/remotes/' + encodeURIComponent(id) + '/landings');
    centralDetailLandings = r.landings || [];
    const pg = paginate(centralDetailLandings, centralDetailPage);
    centralDetailPage = pg.cur;
    const cachedNote = r.note ? '<div class="warn-box">' + esc(r.note) + '</div>' : '';
    if (!centralDetailLandings.length) {
      detail.innerHTML = '<div class="card"><h2>' + esc(rm ? rm.name : id) + ' 的落地</h2>' + cachedNote + '<div class="empty">这台远程面板还没有落地。</div></div>';
      return;
    }
    detail.innerHTML = '<div class="card"><h2>' + esc(rm ? rm.name : id) + ' 的落地</h2>' + cachedNote +
      pg.slice.map((l) => {
        const ports = (l.ports || []).map((p) => esc(p.proto) + ' ' + esc(p.listen) + '→' + esc(p.remote)).join('、') || '—';
        const on = l.online === true ? '<span class="badge green">在线</span>' : l.online === false ? '<span class="badge red">离线</span>' : '<span class="badge">未知</span>';
        const lat = l.latency_ms == null ? '—' : esc(l.latency_ms) + ' ms';
        return '<div class="kv"><span>' + esc(l.domain || l.landing) + ' <span class="small">#' + esc(l.index) + '</span></span><span>' + on + ' ' + lat + '</span></div>' +
          '<div class="kv"><span>落地</span><b>' + esc(l.landing) + '</b></div>' +
          '<div class="kv"><span>监听端口</span><b>' + ports + '</b></div>' +
          '<div class="btn-row"><button class="btn danger sm" type="button" data-central-del-landing="' + esc(l.index) + '">删除该落地</button></div>';
      }).join('') + pagerHtml(pg.total, pg.cur, pg.totalPages, 'central') + '</div>';
    $$('[data-central-del-landing]', detail).forEach((b) => b.addEventListener('click', () => centralDeleteLanding(id, Number(b.dataset.centralDelLanding))));
    if (pg.totalPages > 1) wirePager(detail, 'central', pg.cur, pg.totalPages, (p) => loadCentralLandings(id, p));
  } catch (e) {
    detail.innerHTML = '<div class="card"><div class="error-box">加载失败：' + esc(e.message) + '</div></div>';
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
    body.innerHTML = groups.map((g) => '<h3>' + esc(g.landing || '') + '</h3>' + linksHtml(g.links || [], g.qr || {})).join('');
  } catch (e) {
    const body = $('#central-links-body', m);
    if (body) body.innerHTML = '<div class="error-box">加载失败：' + esc(e.message) + '</div>';
  }
}
function centralAddLanding(id) {
  const rm = centralRemotes.find((x) => x.id === id);
  const m = openModal('<h2>给 ' + esc(rm ? rm.name : '') + ' 加落地</h2>' +
    '<p class="muted">提交后主面板会先校验，再用令牌调远程面板加落地；远程面板跑安装脚本要一会儿，请耐心等。</p>' +
    '<label>落地 IP 或域名</label><input id="central-add-landing" placeholder="203.0.113.1">' +
    '<label>预设</label><select id="central-add-preset"><option value="1">预设 1：tiktok-node（Xray）</option><option value="2">预设 2：sing-box-node</option><option value="3">预设 3：自定义端口</option></select>' +
    '<div id="central-add-custom-wrap" class="hidden"><label>自定义端口</label><input id="central-add-custom" placeholder="443,8388"></div>' +
    '<label>证书域名</label><input id="central-add-cert" placeholder="example.com">' +
    '<div class="btn-row"><button class="btn" id="central-add-ok" type="button">提交并添加落地</button><button class="btn secondary" id="central-add-cancel" type="button">取消</button></div>');
  onIn(m, '#central-add-cancel', 'click', closeModal);
  onIn(m, '#central-add-preset', 'change', () => {
    const w = $('#central-add-custom-wrap', m);
    if (w) w.classList.toggle('hidden', $('#central-add-preset', m).value !== '3');
  });
  onIn(m, '#central-add-ok', 'click', async () => {
    const landing = $('#central-add-landing', m).value.trim();
    const preset = $('#central-add-preset', m).value;
    const customPorts = $('#central-add-custom', m).value.trim();
    const certDomain = $('#central-add-cert', m).value.trim();
    if (!landing) { toast('请先填落地 IP 或域名', 'err'); return; }
    if (preset === '3' && !customPorts) { toast('自定义端口必填', 'err'); return; }
    closeModal();
    toast('正在远程加落地，请稍等…');
    try {
      const d = await apiPostRaw('/api/v1/central/remotes/' + encodeURIComponent(id) + '/landings', { landing, preset, custom_ports: customPorts, cert_domain: certDomain });
      if (d.ok === false) { toast(centralOpError(d), 'err'); return; }
      toast('远程加落地成功', 'ok');
      loadCentral();
    } catch (err) { toast('远程加落地失败：' + err.message, 'err'); }
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
