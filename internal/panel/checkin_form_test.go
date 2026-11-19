package panel

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 签到表单的状态机冒烟：用可派发事件的 DOM 桩跑 app.js，钉住两条真实缺陷——
//
//	1) 手动粘贴 Cookie/Token 必须能提交（脏标记只在原生回调里置位时，桌面/普通
//	   浏览器路径完全建不了站：cookie 提交 null → 后端校验「至少填一个」）；
//	2) 用户改了自动签到设置还没保存时，5 秒轮询不能把控件回写成服务端旧值。
//
// 这两条都是「看起来能用、实际悄悄丢数据」的类型，字符串断言抓不到。
func TestCheckinFormStateSmoke(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; form smoke skipped")
	}

	harness := `
const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');

// 带监听表的元素桩：addEventListener 真的存下来，dispatch 真的调。
function el(id) {
  const listeners = {};
  const node = {
    id, innerHTML: '', textContent: '', value: '', checked: false, disabled: false,
    hidden: false, placeholder: '', title: '', className: '', dataset: {}, attrs: {},
    style: { setProperty(k, v) { this[k] = v; }, removeProperty() {} },
    classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
    removeEventListener() {},
    dispatch(type) { (listeners[type] || []).forEach(fn => fn({ target: node, preventDefault() {} })); },
    appendChild() {}, remove() {},
    setAttribute(k, v) { node.attrs[k] = String(v); },
    getAttribute(k) { return node.attrs[k] === undefined ? null : node.attrs[k]; },
    focus() {}, blur() {}, querySelector() { return null; }, querySelectorAll() { return []; },
    closest() { return null; }, insertAdjacentHTML() {}, click() {},
  };
  return node;
}
const cache = new Map();
const document = {
  getElementById(id) { if (!cache.has(id)) cache.set(id, el(id)); return cache.get(id); },
  querySelector() { return null; }, querySelectorAll() { return []; },
  createElement() { return el('new'); }, addEventListener() {},
  documentElement: el('html'), head: el('head'), body: el('body'),
  cookie: '', title: '',
};
const sandbox = {
  location: { hash: '#checkin', href: 'http://x/panel/', replace() {}, reload() {} },
  history: { replaceState() {}, pushState() {} },
  localStorage: { getItem() { return null; }, setItem() {}, removeItem() {} },
  navigator: { clipboard: { writeText: () => Promise.resolve() } },
  document, fetch: () => new Promise(() => {}),
  addEventListener() {}, removeEventListener() {},
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  MutationObserver: class { constructor() {} observe() {} disconnect() {} },
  setInterval: () => 0, clearInterval() {}, setTimeout: (fn) => 0, clearTimeout() {},
  console, JSON, Math, Date, Number, String, Boolean, Object, Array, Promise, Map, Set,
  RegExp, Error, TypeError, isNaN, parseInt, parseFloat, encodeURIComponent,
  decodeURIComponent, URL, Symbol, Proxy, Reflect, requestAnimationFrame: () => 0,
  confirm: () => false, alert() {}, scrollTo() {}, CSS: { escape: (s) => s },
};
sandbox.window = sandbox; sandbox.globalThis = sandbox;
vm.createContext(sandbox);
vm.runInContext(src, sandbox, { filename: 'app.js' });

function fail(msg) { console.log('FAIL: ' + msg); process.exit(1); }
function run(code) { return vm.runInContext(code, sandbox); }

// 站点数据：一个已存在的站点（id 固定），供「编辑态」用。
const saved = {
  id: 'c_aaaaaaaa', name: '老站', url: 'https://a.example.com', host: 'a.example.com',
  type: 'auto', enabled: true, cookie_set: true, cookie_hint: '…a1b2', token_set: false,
  has_custom: false, today: 'pending', last: null, history: [],
};

// ── 用例 1：新建站点，手动粘贴 Cookie（不经过原生回调）必须能提交 ────────
run("openCheckinEdit(null)");
run("document.getElementById('ckURL').value = 'https://new.example.com'");
run("document.getElementById('ckCookie').value = 'session=manual123'");
run("document.getElementById('ckCookie').dispatch('input')");
let input = run("ckFormInput()");
if (input.cookie !== 'session=manual123') {
  fail('新建站点手动粘贴的 Cookie 没进提交体（后端会报「至少填一个」）：' + JSON.stringify(input.cookie));
}
if (input.id !== '') fail('新建站点不应带 id，实际 ' + JSON.stringify(input.id));

// ── 用例 2：编辑站点、不动机密 → 提交 null（后端保留原值）─────────────────
run("openCheckinEdit(" + JSON.stringify(saved) + ")");
input = run("ckFormInput()");
if (input.cookie !== null) fail('编辑站点没改 Cookie 时应提交 null，实际 ' + JSON.stringify(input.cookie));
if (input.token !== null) fail('编辑站点没改 Token 时应提交 null，实际 ' + JSON.stringify(input.token));
if (input.id !== 'c_aaaaaaaa') fail('编辑站点必须带 id（测试端点据此复用已存凭证）');

// ── 用例 3：编辑站点、手动换 Cookie → 提交新值（不能静默保留旧值）────────
run("document.getElementById('ckCookie').value = 'session=rotated'");
run("document.getElementById('ckCookie').dispatch('input')");
input = run("ckFormInput()");
if (input.cookie !== 'session=rotated') {
  fail('编辑站点手动换 Cookie 后应提交新值（否则「已保存」但凭证没换）：' + JSON.stringify(input.cookie));
}

// ── 用例 4：5s 轮询不能回写用户改过但没保存的设置 ────────────────────────
run("ckSetDirty = false");
run("renderCheckinSkipped = 0");
const snap = {
  settings: { enabled: true, run_at: '08:05', timeout_sec: 20, concurrency: 2 },
  next_run_at: new Date(Date.now() + 3600e3).toISOString(), running: false,
  summary: { total: 1, enabled: 1, ok_today: 0, fail_today: 0, pending: 1 },
  sites: [saved],
};
run("ckData = " + JSON.stringify(snap) + "; renderCheckin()");
// 用户拨开关 + 改时间（等价于触发 change/input）
run("document.getElementById('ckEnabled').checked = false; document.getElementById('ckEnabled').dispatch('change')");
run("document.getElementById('ckRunAt').value = '06:30'; document.getElementById('ckRunAt').dispatch('change')");
const dirty = run("ckSetDirty");
if (dirty !== true) fail('改动设置后 ckSetDirty 应为 true，实际 ' + dirty);
// 再来一轮渲染（模拟 5 秒后的轮询）
run("ckData = " + JSON.stringify(snap) + "; renderCheckin()");
const keepEnabled = run("document.getElementById('ckEnabled').checked");
const keepRunAt = run("document.getElementById('ckRunAt').value");
if (keepEnabled !== false) fail('轮询把用户拨掉的开关弹回了 true');
if (keepRunAt !== '06:30') fail('轮询把用户改的时间还原成 ' + keepRunAt);

// ── 用例 5：圆环 aria-label 要带上数字（role=img 会吞子节点）─────────────
const label = run("document.getElementById('ckRing').getAttribute('aria-label')");
if (!label || label.indexOf('0') < 0) {
  fail('圆环 aria-label 缺少数字：' + JSON.stringify(label));
}

console.log('FORM OK');
`

	hf := filepath.Join(t.TempDir(), "form-smoke.cjs")
	if err := os.WriteFile(hf, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, hf, "app.js")
	cmd.Dir = "." // 测试工作目录 = internal/panel
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("FORM OK")) {
		t.Fatalf("签到表单状态机冒烟失败: %v\n%s", err, out)
	}
}
