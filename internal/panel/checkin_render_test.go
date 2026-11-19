package panel

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 签到中心的渲染冒烟：用最小 DOM 桩跑完整 app.js，然后把一份真实形状的快照
// 喂给 renderCheckin()，断言卡片 HTML 生成正确、且用户数据被转义。
//
// 为什么值得单独做：renderCheckin / ckCardHtml 是纯字符串拼接，语法检查抓不到
// 「拼出来是 undefined」「忘了 esc」这类错误；而它又是用户进 App 第一眼看到的页面。
func TestCheckinRenderSmoke(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; render smoke skipped")
	}

	harness := `
const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');

// 最小元素桩：只实现 app.js 顶层与 renderCheckin 真正碰到的方法。
function el(id) {
  return {
    id, innerHTML: '', textContent: '', value: '', checked: false, disabled: false,
    hidden: false, placeholder: '', title: '', className: '', dataset: {},
    style: { setProperty(k, v) { this[k] = v; }, removeProperty() {} },
    classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    addEventListener() {}, removeEventListener() {}, appendChild() {}, remove() {},
    setAttribute() {}, getAttribute() { return null; }, focus() {}, blur() {},
    querySelector() { return null; }, querySelectorAll() { return []; },
    closest() { return null; }, insertAdjacentHTML() {}, click() {},
    getBoundingClientRect() { return { top: 0, left: 0, width: 0, height: 0 }; },
  };
}
const cache = new Map();
const document = {
  getElementById(id) { if (!cache.has(id)) cache.set(id, el(id)); return cache.get(id); },
  querySelector() { return null; },
  querySelectorAll() { return []; },
  createElement() { return el('new'); },
  addEventListener() {},
  documentElement: el('html'),
  head: el('head'), body: el('body'),
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
  setInterval: () => 0, clearInterval() {}, setTimeout: () => 0, clearTimeout() {},
  console, JSON, Math, Date, Number, String, Boolean, Object, Array, Promise, Map, Set,
  RegExp, Error, TypeError, isNaN, parseInt, parseFloat, encodeURIComponent,
  decodeURIComponent, URL, Symbol, Proxy, Reflect, requestAnimationFrame: () => 0,
  confirm: () => false, alert() {}, scrollTo() {},
};
sandbox.window = sandbox; sandbox.globalThis = sandbox;
vm.createContext(sandbox);
vm.runInContext(src, sandbox, { filename: 'app.js' });

// 一份「真实形状」的快照：包含失败站点、HTML 注入尝试、连签奖励与历史。
const snap = {
  settings: { enabled: true, run_at: '08:05', timeout_sec: 20, concurrency: 2 },
  next_run_at: new Date(Date.now() + 3600e3).toISOString(),
  running: false,
  summary: { total: 2, enabled: 2, ok_today: 1, fail_today: 1, pending: 0 },
  sites: [
    { id: 'c_ok', name: '好的站点', url: 'https://ok.example.com', host: 'ok.example.com',
      type: 'auto', enabled: true, cookie_set: true, cookie_hint: '…a1b2', token_set: false,
      has_custom: false, today: 'ok',
      last: { at: new Date().toISOString(), ok: true, message: '签到成功，获得 5 积分',
              duration_ms: 321, days: 7, reward: '+5', status: 200 },
      history: [{ at: new Date().toISOString(), ok: true, message: '签到成功' }] },
    { id: 'c_bad', name: '<img src=x onerror=alert(1)>', url: 'https://bad.example.com',
      host: 'bad.example.com', type: 'template', enabled: true, cookie_set: true,
      cookie_hint: '…c3d4', token_set: true, has_custom: true,
      custom: { method: 'POST', path: '/sign', ok_kw: '成功' }, today: 'fail',
      last: { at: new Date().toISOString(), ok: false,
              message: 'HTTP 401：Cookie/Token 无效或已过期' + '<script>x</script>',
              duration_ms: 12, status: 401 },
      history: [] },
  ],
};

vm.runInContext('ckData = ' + JSON.stringify(snap) + '; renderCheckin()', sandbox);

const html = document.getElementById('ckList').innerHTML;
if (!html || html.length < 200) { console.log('FAIL: 卡片 HTML 过短: ' + html); process.exit(1); }
if (!html.includes('好的站点')) { console.log('FAIL: 缺少站点名'); process.exit(1); }
if (html.includes('<img src=x onerror')) { console.log('FAIL: 站点名未转义'); process.exit(1); }
if (html.includes('<script>x')) { console.log('FAIL: 失败原因未转义'); process.exit(1); }
if (!html.includes('&lt;img src=x')) { console.log('FAIL: 应有转义后的站点名'); process.exit(1); }
if (!html.includes('今日已签') || !html.includes('今日失败')) {
  console.log('FAIL: 状态徽标缺失'); process.exit(1);
}
if (!html.includes('连签 7 天')) { console.log('FAIL: 连签天数未渲染'); process.exit(1); }
if (!html.includes('data-act="run"')) { console.log('FAIL: 操作按钮缺失'); process.exit(1); }

// 顶部汇总：圆环百分比 = ok/enabled，副标题给出待签提示。
const ring = document.getElementById('ckRing').style['--p'];
if (String(ring) !== '50') { console.log('FAIL: 圆环进度应为 50，实际 ' + ring); process.exit(1); }
const okEl = document.getElementById('ckRingOk').textContent;
if (String(okEl) !== '1') { console.log('FAIL: 已签数应为 1，实际 ' + okEl); process.exit(1); }
if (document.getElementById('ckEmpty').hidden !== true) {
  console.log('FAIL: 有站点时空态应隐藏'); process.exit(1);
}

// 空库：空态显示、一键签到禁用。
vm.runInContext("ckData = { settings: { enabled: true, run_at: '08:05' }, next_run_at: '', running: false, summary: {}, sites: [] }; renderCheckin()", sandbox);
if (document.getElementById('ckEmpty').hidden !== false) {
  console.log('FAIL: 空库时应显示空态'); process.exit(1);
}
if (document.getElementById('btnCkRunAll').disabled !== true) {
  console.log('FAIL: 空库时一键签到应禁用'); process.exit(1);
}
console.log('RENDER OK');
`

	hf := filepath.Join(t.TempDir(), "render-smoke.cjs")
	if err := os.WriteFile(hf, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, hf, "app.js")
	cmd.Dir = "." // 测试工作目录 = internal/panel
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("RENDER OK")) {
		t.Fatalf("renderCheckin 渲染冒烟失败: %v\n%s", err, out)
	}
}
