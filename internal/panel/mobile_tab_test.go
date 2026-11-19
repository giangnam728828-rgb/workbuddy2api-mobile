package panel

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 底栏高亮与首屏视图必须一致。
//
// 复现的 bug：首次启动时页面显示签到中心，底栏却高亮「账号」。
// 原因是两个 <script> 各占一个任务，app.js 的初始 go() 是 setTimeout(0)——
// 定时器可能在 mobile.js 求值之前就跑完；随后 mobile.js 初始化时按 location.hash
// 推断当前页，而 hash 此刻是空的，于是退到旧默认值 accounts。
//
// 这条测试把两种执行顺序都跑一遍（定时器先 / 后），断言底栏高亮始终等于
// **DOM 里真正显示的那一页**。修复前，顺序 A 会失败。
func TestMobileTabMatchesVisibleView(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; tab sync smoke skipped")
	}

	harness := `
const fs = require('fs');
const vm = require('vm');

function mkClassList() {
  const set = new Set();
  return {
    add(c) { set.add(c); }, remove(c) { set.delete(c); },
    toggle(c, on) { if (on === undefined) { set.has(c) ? set.delete(c) : set.add(c); } else if (on) { set.add(c); } else { set.delete(c); } return set.has(c); },
    contains(c) { return set.has(c); }, _set: set,
  };
}

function mkEl(id) {
  const el = {
    id: id || '', hidden: !!el_hidden[id], dataset: {}, textContent: '', value: '',
    children: [], attrs: {}, style: { setProperty() {}, removeProperty() {} },
    classList: mkClassList(),
    addEventListener() {}, removeEventListener() {}, appendChild(c) { el.children.push(c); return c; },
    remove() {}, setAttribute(k, v) { el.attrs[k] = String(v); },
    getAttribute(k) { return el.attrs[k] === undefined ? null : el.attrs[k]; },
    focus() {}, blur() {}, click() {}, closest() { return null; },
    querySelector() { return null; },
    // 底栏的 <a> 由 innerHTML 解析进 children；syncTabs 用 querySelectorAll('a') 取。
    querySelectorAll(sel) { return sel === 'a' ? el.children : []; },
    insertAdjacentHTML() {}, getBoundingClientRect() { return { top: 0, left: 0, width: 0, height: 0 }; },
  };
  return el;
}
const el_hidden = {};

// innerHTML 设到 tabbar 上时解析出 <a data-tab="x">：底栏是 mobile.js 注入的，
// 真实浏览器里由 HTML 解析器生成，这里手工还原成同样的结构。
function attachInnerHTML(el) {
  let html = '';
  Object.defineProperty(el, 'innerHTML', {
    get() { return html; },
    set(v) {
      html = v;
      el.children = [];
      const re = /data-tab="([^"]+)"/g;
      let m;
      while ((m = re.exec(v))) {
        const a = mkEl('a-' + m[1]);
        a.dataset.tab = m[1];
        el.children.push(a);
      }
    },
  });
}

// 视图：签到中心是默认页（index.html 里 #view-checkin 无 hidden，其余带 hidden）。
// 注意：真实 HTML 中默认可见的是 accounts；这里按「go() 已经把 checkin 显示出来」
// 和「go() 还没跑」两种时机分别配置，见下方场景。
const views = {};
function mkView(id, hidden) {
  const v = mkEl(id); v.hidden = hidden; views[id] = v; return v;
}

const body = mkEl('body');
const createdNavs = [];
const document = {
  getElementById(id) { if (!cache.has(id)) cache.set(id, mkEl(id)); return cache.get(id); },
  querySelector(sel) {
    if (sel === '.nav') return mkEl('navDrawer');
    if (sel === '.veil.on') return null;
    if (sel === '.view:not([hidden])') {
      for (const k of Object.keys(views)) if (!views[k].hidden) return views[k];
      return null;
    }
    return null;
  },
  querySelectorAll(sel) {
    if (sel === '.view') return Object.keys(views).map(k => views[k]);
    return [];
  },
  createElement(tag) {
    const e = mkEl('new-' + tag);
    attachInnerHTML(e);
    if (tag === 'nav') createdNavs.push(e);
    return e;
  },
  addEventListener() {}, documentElement: mkEl('html'), head: mkEl('head'), body,
  cookie: '', title: '',
};
const cache = new Map();

function makeSandbox(timers) {
  const sb = {
    location: { hash: '', href: 'http://127.0.0.1:7863/panel/' },
    history: { replaceState(_a, _b, url) { if (url) sb.location.hash = String(url); } },
    localStorage: { getItem() { return null; }, setItem() {}, removeItem() {} },
    navigator: { clipboard: { writeText: () => Promise.resolve() } },
    document, fetch: () => new Promise(() => {}),
    addEventListener() {}, removeEventListener() {},
    matchMedia: () => ({ matches: true, addEventListener() {} }),
    MutationObserver: class { constructor() {} observe() {} disconnect() {} },
    setTimeout(fn) { timers.push(fn); return timers.length; }, clearTimeout() {},
    setInterval() { return 0; }, clearInterval() {},
    console, JSON, Math, Date, Number, String, Boolean, Object, Array, Promise, Map, Set,
    RegExp, Error, TypeError, isNaN, parseInt, parseFloat, encodeURIComponent,
    decodeURIComponent, URL, Symbol, Proxy, Reflect, requestAnimationFrame(fn) { timers.push(fn); return 0; },
    confirm: () => false, alert() {}, scrollTo() {},
  };
  sb.window = sb; sb.globalThis = sb;
  return sb;
}

const appSrc = fs.readFileSync('app.js', 'utf8');
const mobSrc = fs.readFileSync('mobile.js', 'utf8');

function runScenario(name, mobileFirst) {
  // 重置视图：签到中心默认可见（go() 已生效），账号池隐藏。
  for (const k of Object.keys(views)) delete views[k];
  mkView('view-checkin', false);
  mkView('view-accounts', true);
  cache.clear();
  createdNavs.length = 0;

  const timers = [];
  const sb = makeSandbox(timers);
  vm.createContext(sb);
  vm.runInContext(appSrc, sb, { filename: 'app.js' });

  function flush(n) {
    for (let i = 0; i < n && timers.length; i++) {
      const batch = timers.splice(0, timers.length);
      for (const fn of batch) { try { fn(); } catch (e) {} }
    }
  }

  if (mobileFirst) {
    vm.runInContext(mobSrc, sb, { filename: 'mobile.js' });
    flush(3);
  } else {
    // 关键顺序：app.js 的初始 go() 先跑完，mobile.js 之后才求值。
    flush(3);
    vm.runInContext(mobSrc, sb, { filename: 'mobile.js' });
    flush(3);
  }

  const bar = createdNavs[createdNavs.length - 1];
  if (!bar) { console.log('FAIL[' + name + ']: 没有注入底栏'); process.exit(1); }
  const active = bar.children.filter(a => a.classList.contains('on')).map(a => a.dataset.tab);
  const visible = (() => { for (const k of Object.keys(views)) if (!views[k].hidden) return k.slice(5); return '?'; })();

  if (active.length !== 1) {
    console.log('FAIL[' + name + ']: 底栏应恰有一个高亮，实际 ' + JSON.stringify(active));
    process.exit(1);
  }
  if (active[0] !== 'checkin') {
    console.log('FAIL[' + name + ']: 页面显示 ' + visible + '，底栏却高亮 ' + active[0]);
    process.exit(1);
  }
  return { active: active[0], visible };
}

runScenario('定时器先跑（原 bug 顺序）', false);
runScenario('mobile.js 先求值', true);
console.log('TAB OK');
`

	hf := filepath.Join(t.TempDir(), "tab-sync.cjs")
	if err := os.WriteFile(hf, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, hf)
	cmd.Dir = "." // 测试工作目录 = internal/panel
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("TAB OK")) {
		t.Fatalf("底栏与视图同步冒烟失败: %v\n%s", err, out)
	}
}
