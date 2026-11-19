package panel

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// 签到中心的前端接线断言：index.html / app.js / mobile.js 必须三处一致。
// 这些文件都是 go:embed 进二进制的静态资源，Go 编译器不检查内容——
// 靠这组测试钉住「页面有入口、路由认得视图、底栏有签到」这三条最小闭环。
func TestCheckinFrontendWiring(t *testing.T) {
	html := string(indexHTML)
	app := string(appJS)
	mob := string(mobileJS)

	// 1) 页面骨架：视图 section + 导航项 + 弹层 + 关键控件。
	for _, frag := range []string{
		`id="view-checkin"`,
		`href="#checkin" data-view="checkin"`,
		`id="ckVeil"`,
		`id="ckRing"`,
		`id="btnCkRunAll"`,
		`id="ckEnabled"`, `id="ckRunAt"`, `id="btnCkSaveSet"`,
		`id="ckList"`, `id="ckEmpty"`, `id="btnCkAdd"`,
		`id="ckName"`, `id="ckURL"`, `id="ckCookie"`, `id="ckToken"`,
		`id="ckType"`, `id="ckTplGroup"`,
		`id="btnCkLogin"`, `id="btnCkTest"`, `id="btnCkSave"`, `id="btnCkClose"`,
	} {
		if !strings.Contains(html, frag) {
			t.Errorf("index.html 缺少 %s", frag)
		}
	}

	// 2) CSP 硬约束：签到弹层不许带内联事件处理器（全仓库统一走 addEventListener）。
	//    只查 #ckVeil 区块：上游 keyVeil 里遗留一个无害的 onsubmit="return false"，
	//    不在本功能范围内整改。
	if i := strings.Index(html, `id="ckVeil"`); i >= 0 {
		ckPart := html[i:]
		for _, bad := range []string{`onclick=`, `onsubmit=`, `onchange=`, `oninput=`, `onload=`} {
			if strings.Contains(ckPart, bad) {
				t.Errorf("签到弹层出现内联事件 %s（CSP script-src 'self' 会拦掉）", bad)
			}
		}
	} else {
		t.Error("index.html 找不到 ckVeil")
	}

	// 3) 路由与数据面。
	for _, frag := range []string{
		"checkin: '签到中心'",
		"v === 'checkin') loadCheckin()",
		"async function loadCheckin",
		"function renderCheckin",
		"api('checkin')",
		"api('checkin/run_all'",
		"api('checkin/settings'",
		"api('checkin/test'",
		"window.__ckCookie",
		"AndroidBridge.openLogin",
	} {
		if !strings.Contains(app, frag) {
			t.Errorf("app.js 缺少 %s", frag)
		}
	}

	// 4) 手机壳层：底栏 5 个 tab，签到顶掉模型的位置；返回键认得签到弹层。
	if !strings.Contains(mob, "{ v: 'checkin', t: '签到'") {
		t.Error("mobile.js 底栏没有签到入口")
	}
	if strings.Contains(mob, "{ v: 'models'") {
		t.Error("mobile.js 底栏不应再有模型入口（已移入「更多」抽屉）")
	}
	if !strings.Contains(mob, "ckVeil: () => typeof closeCheckinEdit") {
		t.Error("mobile.js 返回键关闭链没有接签到弹层")
	}
	if !strings.Contains(mob, "cur !== 'checkin'") {
		t.Error("mobile.js 返回键的首页应是签到中心")
	}

	// 5) 样式：手机端适配必须落在 mobile.css 的 @media 块内。
	css := string(mobileCSS)
	if !strings.Contains(css, "#ckList { grid-template-columns: minmax(0, 1fr)") {
		t.Error("mobile.css 缺少签到中心的单列布局适配")
	}
}

// 签到中心的 JS 与其余前端共用语法闸门：node --check 必须过。
func TestCheckinFrontendSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; JS syntax skipped")
	}
	for _, name := range []string{"app.js", "mobile.js"} {
		out, err := exec.Command(node, "--check", name).CombinedOutput()
		if err != nil {
			t.Fatalf("%s 语法错误: %v\n%s", name, err, out)
		}
	}
}

// 默认视图 = 签到中心：无 hash 打开时进 checkin（与 mobile.js 底栏第一项一致）。
func TestCheckinDefaultView(t *testing.T) {
	app := string(appJS)
	// 默认视图是签到中心。断言拆成两段而不是整句：这一行改过（现在先算 v 再 go，
	// 顺便把 hash 写回去——底栏与返回键都按 hash 判断当前页），整句断言会跟着代码
	// 写法漂移，而「默认 checkin」这个行为本身才是要钉住的。
	if !strings.Contains(app, "hash in TITLES ? hash : 'checkin'") {
		t.Error("app.js 默认视图应为签到中心")
	}
	if !strings.Contains(app, "if (location.hash !== '#' + v) history.replaceState") {
		t.Error("首次进入应把视图写回 hash（底栏高亮与返回键都依赖它）")
	}
	if os.Getenv("CI") == "" {
		t.Log("提示：完整冒烟由 TestAppJSTopLevelSmoke 覆盖（含 #checkin hash）")
	}
}
