/* mobile.js — 手机端（≤760px）联动逻辑：抽屉开合 + 宽表卡片化的字段名标签。
 *
 * 本文件是本仓库自有文件，上游不维护（同步上游时不参与冲突）。
 * 必须在 app.js 之后加载：依赖 app.js 的全局（$、go、esc、各 render*）与已渲染的 DOM。
 *
 * 分工：样式在 mobile.css；CSS 表达不了的联动（抽屉开关、th 文本 → td[data-l]）放这里。
 */

/* ── 移动端适配（≤760px）──────────────────────────────────────────────
   分工：所有手机端**样式**都在 mobile.css；这里只做 CSS 表达不了的联动：

   1) 侧栏抽屉开合（汉堡 / 遮罩 / 导航项 / ESC / 转回宽屏复位）。
      「转回宽屏复位」不是可选项：遮罩是 fixed 全屏层，窗口拉宽后若不摘掉 .on，
      它会永远盖在面板上，整个页面点不动。

   2) 表格 → 卡片的**字段名**。卡片模式隐藏了表头，用户就看不出哪个值是哪一列。
      做法不是把字段名写死在 CSS 里（表头一改名就漂移），而是把每个 <th> 的纯文本
      抄进同列 <td> 的 data-l，CSS 用 content: attr(data-l) 渲染。
      行全是 JS 动态 innerHTML 出来的（含弹层里的任务表、积分构成里每账号一张表），
      故用 MutationObserver 监听 body 子树，任何一次重渲染后自动补标签。

   两件事在 >760px 时都空转：matchMedia 不匹配；data-l 只是多一个桌面端不参与
   渲染的属性。桌面端表现与改动前完全一致。 */
const MOBILE_MQ = matchMedia('(max-width: 760px)');

const navDrawer = document.querySelector('.nav');
const navBtn = $('navBtn'), navVeil = $('navVeil');

/* drawerSet 幂等设置抽屉三态载体：侧栏 .open / 遮罩 .on / 按钮 aria-expanded。 */
function drawerSet(open) {
  if (navDrawer) navDrawer.classList.toggle('open', open);
  if (navVeil) navVeil.classList.toggle('on', open);
  if (navBtn) navBtn.setAttribute('aria-expanded', open ? 'true' : 'false');
}
function drawerClose() { drawerSet(false); }
function drawerToggle() { drawerSet(!(navDrawer && navDrawer.classList.contains('open'))); }

if (navBtn) navBtn.addEventListener('click', drawerToggle);          // 汉堡：开/关
if (navVeil) navVeil.addEventListener('click', drawerClose);         // 点遮罩关
// 点导航项关：事件委托，不去改上方既有的 go() 绑定
if (navDrawer) navDrawer.addEventListener('click', ev => { if (ev.target.closest('a')) drawerClose(); });
addEventListener('keydown', ev => {
  if (ev.key !== 'Escape') return;
  // 先关弹层再关抽屉：桌面键盘用户开着签到弹层按 ESC 原先毫无反应。
  // 复用 __wbBack 的既有链路（它已经知道每个 veil 该调哪个 closer）。
  if (typeof window.__wbBack === 'function' && window.__wbBack()) return;
  drawerClose();
}); // ESC：弹层 → 抽屉
// 转回 >760px 自动复位：matchMedia 的 change 在跨过断点时触发，resize 兜底
function drawerResetIfWide() { if (!MOBILE_MQ.matches) drawerClose(); }
if (MOBILE_MQ.addEventListener) MOBILE_MQ.addEventListener('change', drawerResetIfWide);
else if (MOBILE_MQ.addListener) MOBILE_MQ.addListener(drawerResetIfWide); // 旧 Safari
addEventListener('resize', drawerResetIfWide);

/* ── 表格字段名 → data-l ─────────────────────────────────────────────── */
/* thLabel 只取表头里的**纯文本节点**：表头里可能还有排序箭头之类的装饰 <span>，
   innerText 会把它们的字符一并算成字段名。 */
function thLabel(th) {
  let t = '';
  for (const n of th.childNodes) if (n.nodeType === 3) t += n.nodeValue;
  return t.replace(/\s+/g, ' ').trim();
}
/* labelTable 按列把表头文字抄进每一行：td[data-l] = 同列 th 的纯文本。
   跨列单元格（colspan：空态提示、包分组汇总）不参与列对齐，也不打标签。 */
/* SHORT_LABEL 手机卡片用的短字段名：一行要并排放好几个指标，表头原名太长会挤成
   两行（「成功 / 失败」「支持的思考档位」「上下文长度」）。未列出的沿用表头纯文本。 */
const SHORT_LABEL = {
  '成功 / 失败': '成功',
  '支持的思考档位': '档位',
  '上下文长度': '上下文',
  '最大输出': '输出',
  '积分倍率': '倍率',
  '均延迟': '延迟',
  '均速率': '速率',
  '最近成功': '最近',
};
function labelTable(table) {
  const head = table.tHead;
  if (!head || !head.rows.length) return;
  const labels = [];
  for (const th of head.rows[head.rows.length - 1].cells) labels.push(thLabel(th));
  for (const body of table.tBodies) {
    for (const tr of body.rows) {
      let col = 0;
      for (const td of tr.cells) {
        const span = td.colSpan > 1 ? td.colSpan : 1;
        const full = span === 1 ? (labels[col] || '') : '';
        const label = SHORT_LABEL[full] || full;
        if (label) td.setAttribute('data-l', label);
        else td.removeAttribute('data-l');
        col += span;
      }
    }
  }
}
function labelTables() {
  document.querySelectorAll('table').forEach(labelTable);
}
/* 账号卡底部的操作按钮要在一行里平分宽度（设计要求：全部按钮单行）。上游按钮文案会变长
   （v1.12.0 起多了「暂停选号」）：320px 下 6 个按钮一行每个只有 ~40px，4 个字放不下、
   文字被硬裁。这里把超过 3 个字的文案压到 2 个字，完整文案挂到 title，保持单行不裁字。 */
const ACT_SHORT = { '暂停选号': '暂停', '恢复选号': '恢复' };
function shortenActLabels() {
  if (!MOBILE_MQ.matches) return;
  for (const b of document.querySelectorAll('.acc .acts button')) {
    const t = (b.textContent || '').trim();
    if (t.length <= 3) continue;
    if (!b.title) b.title = t;
    b.textContent = ACT_SHORT[t] || t.slice(0, 2);
  }
}
/* 一次重渲染会连发多条 mutation（整段 tbody 换掉），合并成一次批处理。 */
let labelPending = false;
function queueLabels() {
  if (labelPending) return;
  labelPending = true;
  setTimeout(() => { labelPending = false; labelTables(); shortenActLabels(); }, 0);
}
labelTables(); // 首屏：把已存在的行先标好
shortenActLabels();
new MutationObserver(queueLabels).observe(document.body, { childList: true, subtree: true });

/* ── 用量「Token 时序」：窄屏另画一张 ─────────────────────────────────
   上游那张图是固定 viewBox 1200×200 的 SVG，靠 width:100% 整体缩放：360px 宽的手机上
   缩放比只有 ~0.28，内部 10px 刻度实际只剩 ~2.8px（看着就是一排小点），柱宽也只剩 5px。
   窄屏改用 440×230 的 viewBox（缩放 ~0.77）并抽稀刻度，字号/柱宽按 CSS 像素算，
   不需要横向拖动。>760px 仍走上游实现，桌面端不受影响。
   实现放在本文件而不是改 app.js：上游常改前端，独立文件才不会每次同步都冲突。 */
const deskUsageChart = renderUsageChart;

function mobileUsageChart(series) {
  if (!MOBILE_MQ.matches) { deskUsageChart(series); return; }
  try {
    drawMobileUsageChart(series);
  } catch (e) {
    deskUsageChart(series); // 自绘出错不能让整页用量视图空掉
  }
}

function drawMobileUsageChart(series) {
  const host = $('usChart');
  if (!host) return;
  const pts = [];
  for (const p of series || []) {
    const t = parsePointTime(p);
    if (t === null) continue;
    const pt = Number(p.prompt_tokens || 0), ct = Number(p.completion_tokens || 0);
    pts.push({ t: t, raw: p.t, scope: p.scope, pt: pt, ct: ct,
               tt: Number(p.total_tokens || 0) || (pt + ct), req: p.requests || 0 });
  }
  if (!pts.length) {
    host.innerHTML = '<div class="us-empty">暂无用量数据。发起一次对话后再刷新。</div>';
    $('usChartNote').textContent = '—';
    return;
  }
  const W = 440, H = 230, PL = 56, PR = 12, PT = 18, PB = 30;
  const iw = W - PL - PR, ih = H - PT - PB, yBase = PT + ih;
  const t0 = pts[0].t, span = Math.max(1, pts[pts.length - 1].t - t0);
  const max = Math.max(1, ...pts.map(p => p.tt));
  const peak = pts.reduce((a, b) => (b.tt > a.tt ? b : a), pts[0]);
  const avg = pts.reduce((s, p) => s + p.tt, 0) / pts.length;
  $('usChartNote').textContent = pts.length + ' 个点 · 峰值 ' + fmtTok(peak.tt) + ' @ ' +
    fmtTokTimeLabel(peak) + ' · 均值 ' + fmtTok(avg);

  let minGap = Infinity;
  for (let i = 1; i < pts.length; i++) minGap = Math.min(minGap, pts[i].t - pts[i - 1].t);
  if (!isFinite(minGap) || minGap <= 0) minGap = span;
  const bw = Math.max(3, Math.min(22, iw * (minGap / span) * 0.7));
  const xOf = t => PL + bw / 2 + (t - t0) / span * Math.max(1, iw - bw);
  const yOf = v => PT + ih - ih * (v / max);

  let out = '<svg viewBox="0 0 ' + W + ' ' + H + '" role="img" preserveAspectRatio="xMidYMid meet">';
  // 只画 4 条网格：窄屏塞 5 条时刻度文字比柱子还挤
  for (let i = 0; i <= 3; i++) {
    const y = PT + ih - (ih * i / 3);
    out += '<line class="gl" x1="' + PL + '" y1="' + y.toFixed(1) + '" x2="' + (W - PR) + '" y2="' + y.toFixed(1) + '"/>' +
      '<text class="tk" x="' + (PL - 6) + '" y="' + (y + 4).toFixed(1) + '" text-anchor="end">' + fmtTok(max * i / 3) + '</text>';
  }
  if (avg > 0 && avg < max) {
    const y = yOf(avg);
    out += '<line class="avg" x1="' + PL + '" y1="' + y.toFixed(1) + '" x2="' + (W - PR) + '" y2="' + y.toFixed(1) + '"/>' +
      '<text class="tk-avg" x="' + (PL + 4) + '" y="' + (y - 5).toFixed(1) + '" text-anchor="start">均值 ' + fmtTok(avg) + '</text>';
  }
  for (const p of pts) {
    const x = xOf(p.t) - bw / 2;
    const hTot = ih * (p.tt / max);
    const hP = p.tt ? hTot * (p.pt / p.tt) : 0;
    const hC = Math.max(p.tt && p.ct ? 1 : 0, hTot - hP);
    // 每根柱子包一个 <g>：SVG 里 <title> 描述的是父元素，平铺在根下会全图共用一个
    // tooltip（悬停任何柱子都是同一份数据，issue #128）。
    out += '<g><title>' + esc(p.raw) + '  ' + fmtTok(p.pt) + ' prompt / ' + fmtTok(p.ct) + ' completion / ' + p.req + ' 次</title>';
    if (hP > 0) out += '<rect class="usbar usbar-p" x="' + x.toFixed(2) + '" y="' + (yBase - hP).toFixed(2) +
      '" width="' + bw.toFixed(2) + '" height="' + hP.toFixed(2) + '"' + (hC > 0 ? '' : ' rx="1.5"') + '/>';
    if (hC > 0) out += '<rect class="usbar usbar-c" x="' + x.toFixed(2) + '" y="' + (yBase - hP - hC).toFixed(2) +
      '" width="' + bw.toFixed(2) + '" height="' + hC.toFixed(2) + '" rx="1.5"/>';
    out += '</g>';
  }
  {
    const px = xOf(peak.t), py = yOf(peak.tt);
    const anchor = px > W - PR - 84 ? 'end' : 'middle';
    out += '<text class="tk-peak" x="' + Math.max(PL, Math.min(W - PR, px)).toFixed(1) + '" y="' +
      Math.max(12, py - 6).toFixed(1) + '" text-anchor="' + anchor + '">峰值 ' + fmtTok(peak.tt) + '</text>';
  }
  out += '<line class="ax" x1="' + PL + '" y1="' + yBase + '" x2="' + (W - PR) + '" y2="' + yBase + '"/>';
  // x 刻度抽到 4 个（上游 6 个），且取"离目标最近的真实柱子"，标签永远落在有数据的点上
  const TICKS = Math.min(4, pts.length);
  const used = new Set();
  for (let k = 0; k < TICKS; k++) {
    const target = t0 + span * (TICKS === 1 ? 0.5 : k / (TICKS - 1));
    let bi = 0, best = Infinity;
    for (let i = 0; i < pts.length; i++) {
      const d = Math.abs(pts[i].t - target);
      if (d < best) { best = d; bi = i; }
    }
    if (used.has(bi)) continue;
    used.add(bi);
    const p = pts[bi], cx = xOf(p.t);
    const anchor = cx < PL + 16 ? 'start' : (cx > W - PR - 16 ? 'end' : 'middle');
    out += '<text class="tk" x="' + Math.max(PL, Math.min(W - PR, cx)).toFixed(1) + '" y="' + (yBase + 15).toFixed(1) +
      '" text-anchor="' + anchor + '">' + esc(fmtTokTimeLabel(p)) + '</text>';
  }
  let prevDay = null; // 跨天分隔线：长窗口里能看出日界
  for (const p of pts) {
    const d = new Date(p.t).getDate();
    if (prevDay !== null && d !== prevDay) {
      const x = xOf(p.t).toFixed(1);
      out += '<line class="gl" x1="' + x + '" y1="' + PT + '" x2="' + x + '" y2="' + yBase + '" style="opacity:.45"/>';
    }
    prevDay = d;
  }
  out += '</svg>';
  host.innerHTML = out;
}

renderUsageChart = mobileUsageChart;

/* ══ App 壳层（底部标签栏 / 下拉刷新 / 返回键 / 主题同步）══════════════════
   这一节把「网页」变成「App」：手机上不再靠左上角汉堡找入口，而是常驻底栏；
   下拉即刷新；系统返回键按 弹层 → 抽屉 → 首页 → 退出 的顺序逐层后退；
   页面明暗切换同步给原生状态栏，避免浅色页面配深色状态栏的割裂感。

   全部逻辑都在 ≤760px 生效（native 桥接除外），桌面端零影响。
   依赖 app.js 的 go() / $() / 各 closeXxx()；本文件在其后加载，故都能拿到。 */
(function appShell() {
  /* 单测会在没有 DOM 的 node vm 里截取本文件后半段跑图表断言：无 window 直接退出，
     不要在这里碰任何全局对象。 */
  if (typeof window === 'undefined' || typeof document === 'undefined') return;
  const IS_APP = !!window.AndroidBridge;   // 原生 WebView 注入的桥
  if (IS_APP) document.documentElement.classList.add('in-app');

  /* ── 1. 底部标签栏 ────────────────────────────────────────────────
     5 个入口是"每天都会点"的：签到 / 账号池 / 任务 / 用量 / 更多。
     积分构成、模型、配置、运行日志属于低频项，留在抽屉里（点"更多"打开）。 */
  const TABS = [
    { v: 'checkin', t: '签到', d: '<rect x="2.2" y="3" width="11.6" height="10.8" rx="2"/><path d="M2.2 6.4h11.6M5.4 1.6v2.6M10.6 1.6v2.6"/><path d="m5.6 10.3 1.7 1.7 3.1-3.3"/>' },
    { v: 'accounts', t: '账号', d: '<circle cx="8" cy="5.5" r="2.6"/><path d="M2.8 13.5c.6-2.6 2.7-4 5.2-4s4.6 1.4 5.2 4"/>' },
    { v: 'taskscenter', t: '任务', d: '<path d="M3 2.5h10v9H8l-3 3v-3H3z"/><path d="M6 6.5h4M6 9h2.5"/>' },
    { v: 'usage', t: '用量', d: '<path d="M2 13.5h12"/><path d="M4.5 13.5V8.2M8 13.5V3.5M11.5 13.5v-3"/>' },
    { v: 'more', t: '更多', d: '<circle cx="3" cy="8" r="1.3" fill="currentColor" stroke="none"/><circle cx="8" cy="8" r="1.3" fill="currentColor" stroke="none"/><circle cx="13" cy="8" r="1.3" fill="currentColor" stroke="none"/>' }
  ];
  /* 抽屉里的低频视图归属"更多"：进这些页面时底栏高亮"更多"而不是全灭。
     模型页从底栏移进抽屉（签到中心顶掉了它的位置），映射也归"更多"。 */
  const MORE_VIEWS = { packages: 1, config: 1, logs: 1, models: 1 };

  const bar = document.createElement('nav');
  bar.className = 'tabbar';
  bar.id = 'tabBar';
  bar.setAttribute('aria-label', '主导航');
  bar.innerHTML = TABS.map(x =>
    '<a href="#' + x.v + '" data-tab="' + x.v + '">' +
    '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + x.d + '</svg>' +
    '<span>' + x.t + '</span></a>'
  ).join('');
  document.body.appendChild(bar);

  /* syncTabs 把当前视图映射回 5 个入口之一（低频页 → 更多）。 */
  function syncTabs(v) {
    const cur = v || (location.hash || '#accounts').slice(1);
    const key = MORE_VIEWS[cur] ? 'more' : cur;
    bar.querySelectorAll('a').forEach(a => a.classList.toggle('on', a.dataset.tab === key));
  }

  /* 包一层 go()：app.js 内部（导航点击、深链、外部调用）都走同一条路径，
     包在 go 上比给每个入口单独绑事件更不容易漏。 */
  const baseGo = window.go;
  window.go = function (v) {
    baseGo(v);
    syncTabs(v);
    window.scrollTo(0, 0);
    document.title = 'WorkBuddy2API · ' + ((document.getElementById('ttl') || {}).textContent || '');
  };
  syncTabs();

  bar.addEventListener('click', ev => {
    const a = ev.target.closest('a');
    if (!a) return;
    ev.preventDefault();
    if (a.dataset.tab === 'more') { drawerSet(true); return; }
    window.go(a.dataset.tab);
    history.replaceState(null, '', '#' + a.dataset.tab);
  });

  /* ── 2. 下拉刷新 ──────────────────────────────────────────────────
     只有"已经在页面最顶端 + 单指 + 纵向下拉"才接管，否则一律不拦（否则列表
     滑不动）。超过 64px 松手即触发顶栏的「刷新」，同时指示器转圈。 */
  const ptr = document.createElement('div');
  ptr.className = 'ptr';
  ptr.innerHTML = '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M8 2.5v8M4.8 7.4 8 10.6l3.2-3.2"/></svg>';
  document.body.appendChild(ptr);

  const PTR_TRIGGER = 64;
  let ptrStartY = 0, ptrStartX = 0, ptrDy = 0, ptrActive = false, ptrBusy = false;

  addEventListener('touchstart', ev => {
    if (ptrBusy || ev.touches.length !== 1) return;
    if (window.scrollY > 0) return;
    if (document.querySelector('.veil.on')) return;
    ptrStartY = ev.touches[0].clientY;
    ptrStartX = ev.touches[0].clientX;
    ptrActive = true; ptrDy = 0;
  }, { passive: true });

  addEventListener('touchmove', ev => {
    if (!ptrActive) return;
    const dy = ev.touches[0].clientY - ptrStartY;
    const dx = ev.touches[0].clientX - ptrStartX;
    if (dy <= 0 || Math.abs(dx) > Math.abs(dy) || window.scrollY > 0) { ptrActive = false; ptr.classList.remove('on'); return; }
    if (dy > 6) {
      ev.preventDefault();              // 接管手势：不让 WebView 自己弹橡皮筋
      ptrDy = dy;
      ptr.classList.add('on');
      ptr.style.transform = 'translateY(' + Math.min(18, dy / 4) + 'px) scale(' + Math.min(1, 0.85 + dy / 400) + ')';
    }
  }, { passive: false });

  addEventListener('touchend', () => {
    if (!ptrActive) return;
    ptrActive = false;
    ptr.style.transform = '';
    if (ptrDy >= PTR_TRIGGER) {
      ptrBusy = true;
      ptr.classList.add('on', 'busy');
      const btn = document.getElementById('btnRefresh');
      if (btn) btn.click();
      setTimeout(() => { ptrBusy = false; ptr.classList.remove('on', 'busy'); }, 900);
    } else {
      ptr.classList.remove('on');
    }
    ptrDy = 0;
  });
  addEventListener('touchcancel', () => { ptrActive = false; ptrDy = 0; ptr.classList.remove('on'); ptr.style.transform = ''; });

  /* ── 3. 系统返回键 ────────────────────────────────────────────────
     原生侧 evaluateJavascript 取返回值：true = 已消费（关弹层/关抽屉/回首页），
     false = 交给系统（App 退到后台，不杀进程，服务继续跑）。 */
  const VEIL_CLOSER = {
    addVeil: () => typeof closeAdd === 'function' && closeAdd(),
    taskVeil: () => typeof closeTasks === 'function' && closeTasks(),
    remarkVeil: () => typeof closeRemarks === 'function' && closeRemarks(),
    ckVeil: () => typeof closeCheckinEdit === 'function' && closeCheckinEdit(),
    vcVeil: () => { const v = document.getElementById('vcVeil'); if (v) v.classList.remove('on'); }
  };
  window.__wbBack = function () {
    const drawer = document.getElementById('navDrawer');
    if (drawer && drawer.classList.contains('open')) { drawerSet(false); return true; }
    const veil = document.querySelector('.veil.on');
    if (veil) {
      if (veil.id === 'keyVeil') return false;   // 鉴权门不能靠返回键绕过
      const closer = VEIL_CLOSER[veil.id];
      if (closer) closer(); else veil.classList.remove('on');
      return true;
    }
    const cur = (location.hash || '#checkin').slice(1);
    if (cur && cur !== 'checkin') {
      window.go('checkin');
      history.replaceState(null, '', '#checkin');
      return true;
    }
    return false;
  };

  /* ── 4. 主题同步（状态栏 / 导航栏取色）──────────────────────────────
     app.js 只改 <html data-theme>，原生层无从得知；这里把生效主题推给原生，
     让状态栏图标与背景跟着面板一起明暗。 */
  function pushTheme() {
    if (!window.AndroidBridge || !window.AndroidBridge.setTheme) return;
    const t = document.documentElement.getAttribute('data-theme') || 'dark';
    try { window.AndroidBridge.setTheme(t); } catch (e) {}
  }
  pushTheme();
  new MutationObserver(pushTheme).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });

  /* 原生侧"回到前台"时通知页面：手机上后台久了，数据可能已经过期，
     回前台自动刷一次比让用户自己点刷新更符合 App 直觉。 */
  window.__wbOnResume = function () {
    const btn = document.getElementById('btnRefresh');
    if (btn) btn.click();
  };
})();
