package checkin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ── Store：持久化与并发 ─────────────────────────────────────────────────

func TestStoreAddUpdateDeletePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkin-sites.json")
	st := OpenStore(path)

	in := Input{Name: "示例站", URL: "https://a.example.com/", Type: TypeAuto,
		Cookie: strPtr("session=abc123456")}
	site, err := st.add(mustSite(t, in))
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if site.ID == "" {
		t.Fatal("站点 ID 为空")
	}

	// 重开：数据必须还在（落盘成功）。
	st2 := OpenStore(path)
	got, ok := st2.get(site.ID)
	if !ok {
		t.Fatal("重启后站点丢失")
	}
	if got.Cookie != "session=abc123456" {
		t.Fatalf("Cookie 未持久化: %q", got.Cookie)
	}

	// 更新时 Input.Cookie == nil → 保留原值；显式 "" → 清空。
	newCookie := ""
	if _, err := st2.update(site.ID, func(s *Site) error {
		return applyInput(s, Input{Name: "改名", URL: site.URL, Type: TypeAuto}, false)
	}); err != nil {
		t.Fatalf("update(无凭证字段): %v", err)
	}
	if got, _ = st2.get(site.ID); got.Cookie != "session=abc123456" {
		t.Fatalf("nil Cookie 应保留原值，实际 %q", got.Cookie)
	}
	if _, err := st2.update(site.ID, func(s *Site) error {
		return applyInput(s, Input{Name: "改名", URL: site.URL, Type: TypeAuto, Cookie: &newCookie, Token: strPtr("t")}, false)
	}); err != nil {
		t.Fatalf("update(清 Cookie): %v", err)
	}
	if got, _ = st2.get(site.ID); got.Cookie != "" || got.Token != "t" {
		t.Fatalf("空串应清空 Cookie，实际 cookie=%q token=%q", got.Cookie, got.Token)
	}

	if err := st2.remove(site.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := st2.get(site.ID); ok {
		t.Fatal("删除后仍能取到")
	}
	if err := st2.remove(site.ID); err != ErrNotFound {
		t.Fatalf("重复删除应返回 ErrNotFound，实际 %v", err)
	}
}

func TestStoreCorruptFileKeepsRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkin-sites.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := OpenStore(path) // 不能 panic
	if n := len(st.snapshotSites()); n != 0 {
		t.Fatalf("损坏文件应按空库处理，实际 %d 个站点", n)
	}
}

func TestStoreRecordPrependsHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkin-sites.json")
	st := OpenStore(path)
	site, _ := st.add(mustSite(t, Input{Name: "A", URL: "https://a.com", Type: TypeAuto, Cookie: strPtr("c")}))
	for i := 0; i < HistoryLimit+5; i++ {
		st.record(site.ID, Result{At: time.Now(), OK: true, Message: "m"})
	}
	got, _ := st.get(site.ID)
	if len(got.History) != HistoryLimit {
		t.Fatalf("历史应截断到 %d，实际 %d", HistoryLimit, len(got.History))
	}
	if got.Last == nil {
		t.Fatal("Last 未写入")
	}
}

// snapshotSites 测试辅助：拿站点列表。
func (s *Store) snapshotSites() []Site {
	_, sites, _ := s.snapshot()
	return sites
}

// ── Runner：请求与判定 ─────────────────────────────────────────────────

func TestRunnerAutoCheckinSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/checkin" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Cookie") != "session=xyz" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "未登录"})
			return
		}
		if r.Header.Get("User-Agent") == "" || r.Header.Get("Authorization") != "Bearer tk" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "message": "签到成功",
			"data": map[string]any{"days": 3, "reward": "+5 积分"},
		})
	}))
	defer srv.Close()

	r := NewRunner()
	res := r.Run(context.Background(), Site{
		Name: "T", URL: srv.URL, Type: TypeAuto, Enabled: true,
		Cookie: "session=xyz", Token: "tk",
	}, DefaultSettings())
	if !res.OK {
		t.Fatalf("应成功，实际 %s（status=%d）", res.Message, res.Status)
	}
	if res.Days != 3 || res.Reward != "+5 积分" {
		t.Fatalf("days=%d reward=%q", res.Days, res.Reward)
	}
}

func TestRunnerAutoSkips404And405(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/api/user/checkin" && r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "已签到"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	r := NewRunner()
	res := r.Run(context.Background(), Site{Name: "T", URL: srv.URL, Type: TypeAuto, Cookie: "c"},
		DefaultSettings())
	// 第一候选 POST /api/user/checkin 命中，只应有一次请求。
	if len(hits) != 1 {
		t.Fatalf("应只探测一个端点，实际 %v", hits)
	}
	if !res.OK || res.Message != "已签到" {
		t.Fatalf("res=%+v", res)
	}
}

func TestRunnerAutoFallsThrough404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/sign_in" {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	r := NewRunner()
	res := r.Run(context.Background(), Site{Name: "T", URL: srv.URL, Type: TypeAuto, Cookie: "c"},
		DefaultSettings())
	if !res.OK {
		t.Fatalf("404 后应继续探测并成功：%s", res.Message)
	}
}

func TestRunnerAutoNoEndpointFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	r := NewRunner()
	res := r.Run(context.Background(), Site{Name: "T", URL: srv.URL, Type: TypeAuto, Cookie: "c"},
		DefaultSettings())
	if res.OK {
		t.Fatal("全是 404 应判失败")
	}
	if res.Message == "" {
		t.Fatal("要有可读的失败原因")
	}
}

func TestRunnerAuto401MeansCookieInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "unauthorized"})
	}))
	defer srv.Close()
	r := NewRunner()
	res := r.Run(context.Background(), Site{Name: "T", URL: srv.URL, Type: TypeAuto, Cookie: "bad"},
		DefaultSettings())
	if res.OK {
		t.Fatal("401 应判失败")
	}
	if !contains(res.Message, "Cookie") {
		t.Fatalf("失败原因应提示 Cookie：%s", res.Message)
	}
}

func TestRunnerAutoNonJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>login page</body></html>"))
	}))
	defer srv.Close()
	r := NewRunner()
	res := r.Run(context.Background(), Site{Name: "T", URL: srv.URL, Type: TypeAuto, Cookie: "c"},
		DefaultSettings())
	if res.OK {
		t.Fatal("HTML 响应应判失败")
	}
}

func TestRunnerTemplateSuccessKeyword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sign.do" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("表单体应自动补 form Content-Type，实际 %q", r.Header.Get("Content-Type"))
		}
		_, _ = w.Write([]byte(" result=ok , msg=签到成功,今日获得5积分 "))
	}))
	defer srv.Close()

	r := NewRunner()
	res := r.Run(context.Background(), Site{
		Name: "T", URL: srv.URL, Type: TypeTemplate, Method: "POST", Path: "/sign.do",
		Body: "u={{date}}&t={{ts}}", SuccessKeyword: "签到成功", Cookie: "c",
	}, DefaultSettings())
	if !res.OK {
		t.Fatalf("关键字命中应成功：%s", res.Message)
	}
}

func TestRunnerTemplateFailureKeywordBeats200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"message":"今日已签到"}`))
	}))
	defer srv.Close()

	r := NewRunner()
	// 用户明确写了失败关键字：HTTP 200 也要判失败。
	res := r.Run(context.Background(), Site{
		Name: "T", URL: srv.URL, Type: TypeTemplate, Method: "GET",
		FailureKeyword: "已签到", Cookie: "c",
	}, DefaultSettings())
	if res.OK {
		t.Fatal("失败关键字命中应判失败（即使 HTTP 200）")
	}
}

func TestRunnerTemplateSuccessJSONPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"checked":true},"message":"好的"}`))
	}))
	defer srv.Close()

	r := NewRunner()
	res := r.Run(context.Background(), Site{
		Name: "T", URL: srv.URL, Type: TypeTemplate, Method: "GET",
		SuccessJSONPath: "data.checked", Cookie: "c",
	}, DefaultSettings())
	if !res.OK || res.Message != "好的" {
		t.Fatalf("res=%+v", res)
	}
}

func TestRunnerTemplateHTMLWith2xxFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>ok</html>"))
	}))
	defer srv.Close()
	r := NewRunner()
	res := r.Run(context.Background(), Site{
		Name: "T", URL: srv.URL, Type: TypeTemplate, Method: "GET", Cookie: "c",
	}, DefaultSettings())
	if res.OK {
		t.Fatal("2xx + HTML 无规则应判失败（Cookie 失效场景）")
	}
}

func TestRunnerTemplateFullURLPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/other/endpoint" {
			t.Errorf("应使用 Path 里的完整地址，实际 %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	r := NewRunner()
	res := r.Run(context.Background(), Site{
		Name: "T", URL: srv.URL + "/base", Type: TypeTemplate, Method: "GET",
		Path: srv.URL + "/other/endpoint", Cookie: "c",
	}, DefaultSettings())
	if !res.OK {
		t.Fatalf("res=%+v", res)
	}
}

func TestRunnerSubPathBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/newapi/api/user/checkin" {
			t.Errorf("应保留 base 子路径，实际 %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	r := NewRunner()
	res := r.Run(context.Background(), Site{
		Name: "T", URL: srv.URL + "/newapi", Type: TypeAuto, Cookie: "c",
	}, DefaultSettings())
	if !res.OK {
		t.Fatalf("res=%+v", res)
	}
}

func TestRunnerTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(6 * time.Second)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	r := NewRunner()
	st := DefaultSettings()
	st.TimeoutSec = 5
	start := time.Now()
	res := r.Run(context.Background(), Site{Name: "T", URL: srv.URL, Type: TypeTemplate,
		Method: "GET", Cookie: "c", SuccessKeyword: "ok"}, st)
	if res.OK {
		t.Fatal("超时应失败")
	}
	if elapsed := time.Since(start); elapsed > 7*time.Second {
		t.Fatalf("超时未生效，耗时 %v", elapsed)
	}
	if !contains(res.Message, "超时") {
		t.Fatalf("应提示超时：%s", res.Message)
	}
}

func TestRunnerCustomHeadersAndBodyJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "abc" {
			t.Errorf("自定义头丢失")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("JSON body 应补 application/json，实际 %q", r.Header.Get("Content-Type"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["d"] == "" {
			t.Errorf("占位符未替换: %v", body)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	r := NewRunner()
	res := r.Run(context.Background(), Site{
		Name: "T", URL: srv.URL, Type: TypeTemplate, Method: "POST",
		Headers: map[string]string{"X-Token": "abc", "Cookie": "should-be-dropped"},
		Body:    `{"d":"{{ts}}"}`, Cookie: "c",
	}, DefaultSettings())
	if !res.OK {
		t.Fatalf("res=%+v", res)
	}
}

func TestRunnerNetworkErrorMessages(t *testing.T) {
	r := NewRunner()
	// 不存在的域名：解析失败要有中文说明。
	res := r.Run(context.Background(), Site{Name: "T", URL: "https://no-such-host-checkin.invalid",
		Type: TypeAuto, Cookie: "c"}, DefaultSettings())
	if res.OK || res.Message == "" {
		t.Fatalf("res=%+v", res)
	}
	// URL 不合法：直接失败。
	res2 := r.Run(context.Background(), Site{Name: "T", URL: "ftp://x", Type: TypeTemplate,
		Method: "GET", Cookie: "c"}, DefaultSettings())
	if res2.OK {
		t.Fatal("非 http URL 应失败")
	}
}

// ── applyInput 校验 ─────────────────────────────────────────────────────

func TestApplyInputValidation(t *testing.T) {
	cases := []struct {
		name string
		in   Input
	}{
		{"没有凭证", Input{Name: "A", URL: "https://a.com", Type: TypeAuto}},
		{"没有网址", Input{Name: "A", Type: TypeAuto, Cookie: strPtr("c")}},
		{"坏网址", Input{Name: "A", URL: "notaurl", Type: TypeAuto, Cookie: strPtr("c")}},
		{"坏类型", Input{Name: "A", URL: "https://a.com", Type: "x", Cookie: strPtr("c")}},
		{"坏方法", Input{Name: "A", URL: "https://a.com", Type: TypeTemplate, Method: "BREW", Cookie: strPtr("c")}},
	}
	for _, c := range cases {
		var s Site
		if err := applyInput(&s, c.in, true); err == nil {
			t.Errorf("%s: 应校验失败", c.name)
		}
	}
	// 名称缺省 → 取域名；超长 → 截断。
	var s Site
	if err := applyInput(&s, Input{URL: "https://long.example.com", Type: TypeAuto, Cookie: strPtr("c")}, true); err != nil {
		t.Fatalf("默认名: %v", err)
	}
	if s.Name != "long.example.com" {
		t.Fatalf("默认名应取域名，实际 %q", s.Name)
	}
	long := make([]rune, 100)
	for i := range long {
		long[i] = '长'
	}
	var s2 Site
	_ = applyInput(&s2, Input{Name: string(long), URL: "https://a.com", Type: TypeAuto, Cookie: strPtr("c")}, true)
	if len([]rune(s2.Name)) > 40 {
		t.Fatalf("名称应截断到 40，实际 %d", len([]rune(s2.Name)))
	}
	// auto 模式不应保留模板字段。
	var s3 Site
	s3.SuccessKeyword = "旧值"
	_ = applyInput(&s3, Input{URL: "https://a.com", Type: TypeAuto, Cookie: strPtr("c")}, false)
	if s3.SuccessKeyword != "" {
		t.Fatal("auto 模式应清空模板字段")
	}
}

func TestValidateSettings(t *testing.T) {
	if err := ValidateSettings(Settings{RunAt: "25:00", TimeoutSec: 20, Concurrency: 2}); err == nil {
		t.Fatal("25:00 应无效")
	}
	if err := ValidateSettings(Settings{RunAt: "08:05", TimeoutSec: 3, Concurrency: 2}); err == nil {
		t.Fatal("3 秒超时应无效")
	}
	if err := ValidateSettings(Settings{RunAt: "08:05", TimeoutSec: 20, Concurrency: 9}); err == nil {
		t.Fatal("并发 9 应无效")
	}
	if err := ValidateSettings(Settings{RunAt: "23:59", TimeoutSec: 120, Concurrency: 5}); err != nil {
		t.Fatalf("合法设置不应报错：%v", err)
	}
}

// ── Service：互斥、快照、测试通道 ────────────────────────────────────────

func newTestService(t *testing.T, handler http.HandlerFunc) *Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	svc := NewService(filepath.Join(t.TempDir(), "checkin-sites.json"))
	cookie := strPtr("k=v")
	enabled := true
	if _, err := svc.Create(Input{Name: "T站", URL: srv.URL, Type: TypeAuto,
		Cookie: cookie, Enabled: &enabled}); err != nil {
		t.Fatalf("create: %v", err)
	}
	return svc
}

func TestServiceRunAllAndSnapshot(t *testing.T) {
	svc := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": "签到成功"})
	})
	res, sum, err := svc.RunAll(context.Background())
	if err != nil {
		t.Fatalf("runAll: %v", err)
	}
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("res=%+v", res)
	}
	if sum.OKToday != 1 || sum.Pending != 0 {
		t.Fatalf("sum=%+v", sum)
	}
	snap := svc.Snapshot()
	if snap.Sites[0].Today != "ok" || snap.Sites[0].CookieSet != true {
		t.Fatalf("snap=%+v", snap.Sites[0])
	}
	// 按字符数而不是字节数断言：提示以「…」开头，它是 3 字节的多字节字符，
	// 按 len() 数会永远超限（"…****" 是 7 字节 5 字符），属于断言的度量写错了。
	if snap.Sites[0].CookieHint == "" || len([]rune(snap.Sites[0].CookieHint)) > 6 {
		t.Fatalf("Cookie 提示应只有末 4 位: %q", snap.Sites[0].CookieHint)
	}
	if len(snap.Sites[0].History) != 1 {
		t.Fatalf("历史应有一条")
	}
}

func TestServiceRunAllBusyMutex(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	svc := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	go svc.RunAll(context.Background())
	<-started
	if _, _, err := svc.RunAll(context.Background()); err != ErrBusy {
		t.Fatalf("并发全量应返回 ErrBusy，实际 %v", err)
	}
	close(release)
}

func TestServiceTestDoesNotPersist(t *testing.T) {
	svc := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "nope"})
	})
	before := svc.Snapshot()
	res, err := svc.Test(context.Background(), Input{Name: "临时", URL: before.Sites[0].URL,
		Type: TypeAuto, Cookie: strPtr("x")})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.OK {
		t.Fatal("应失败")
	}
	after := svc.Snapshot()
	if len(after.Sites) != len(before.Sites) || len(after.Sites[0].History) != len(before.Sites[0].History) {
		t.Fatal("Test 不应写库/写历史")
	}
}

func TestServiceSettingsRoundTrip(t *testing.T) {
	svc := newTestService(t, func(w http.ResponseWriter, r *http.Request) {})
	st, err := svc.SetSettings(Settings{Enabled: false, RunAt: "07:30", TimeoutSec: 30, Concurrency: 3})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if st.RunAt != "07:30" || st.Enabled {
		t.Fatalf("st=%+v", st)
	}
	if svc.Settings().RunAt != "07:30" {
		t.Fatal("设置未生效")
	}
	if _, err := svc.SetSettings(Settings{RunAt: "bad"}); err == nil {
		t.Fatal("坏时间应报错")
	}
}

// ── 调度：时点判断 ───────────────────────────────────────────────────────

func TestNextRunAt(t *testing.T) {
	now := time.Date(2026, 11, 19, 7, 0, 0, 0, time.Local)
	st := Settings{RunAt: "08:05", TimeoutSec: 20, Concurrency: 2}
	if got := nextRunAt(st, "", now); !sameDay(got, now) || got.Hour() != 8 {
		t.Fatalf("今天未到点应返回今天 08:05，实际 %s", got)
	}
	after := time.Date(2026, 11, 19, 9, 0, 0, 0, time.Local)
	if got := nextRunAt(st, "", after); sameDay(got, after) {
		t.Fatalf("已过点应顺延明天，实际 %s", got)
	}
	// 今天已跑过 → 明天。
	if got := nextRunAt(st, dayKey(now), now); sameDay(got, now) {
		t.Fatalf("今天已跑应显示明天，实际 %s", got)
	}
}

func TestMaybeRunAutoWindows(t *testing.T) {
	var hits int
	var mu sync.Mutex
	svc := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	st := svc.Settings()
	st.Enabled = true
	st.RunAt = "08:00"
	if _, err := svc.SetSettings(st); err != nil {
		t.Fatal(err)
	}

	before := time.Date(2026, 11, 19, 7, 0, 0, 0, time.Local)
	svc.maybeRunAuto(before)
	if n := currentHits(&mu, &hits); n != 0 {
		t.Fatalf("未到点不应执行，实际 %d", n)
	}

	due := time.Date(2026, 11, 19, 8, 0, 30, 0, time.Local)
	svc.maybeRunAuto(due)
	if n := currentHits(&mu, &hits); n != 1 {
		t.Fatalf("到点应执行一轮，实际 %d", n)
	}
	// 同日再触发不重复执行。
	svc.maybeRunAuto(due.Add(time.Minute))
	if n := currentHits(&mu, &hits); n != 1 {
		t.Fatalf("同日不应重复执行，实际 %d", n)
	}

	// 超过补跑窗口：不执行。
	svc.store.setAutoDay("")
	tooLate := time.Date(2026, 11, 19, 20, 0, 0, 0, time.Local)
	svc.maybeRunAuto(tooLate)
	if n := currentHits(&mu, &hits); n != 1 {
		t.Fatalf("超窗不应补跑，实际 %d", n)
	}
}

func currentHits(mu *sync.Mutex, hits *int) int {
	mu.Lock()
	defer mu.Unlock()
	return *hits
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// ── 工具 ────────────────────────────────────────────────────────────────

func strPtr(s string) *string { return &s }

func mustSite(t *testing.T, in Input) Site {
	t.Helper()
	var s Site
	if err := applyInput(&s, in, true); err != nil {
		t.Fatalf("applyInput: %v", err)
	}
	return s
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// ── 回归：试运行与响应截断的缺陷 ─────────────────────────────────────────

func TestServiceTestReusesSavedSecret(t *testing.T) {
	// 假站点只认这一个 Cookie：编辑态「测试」不带凭证时必须复用库里的旧值，
	// 否则真实站点回 401，用户会以为是站点坏了。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "k=v" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "未登录"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": "签到成功"})
	}))
	defer srv.Close()

	svc := NewService(filepath.Join(t.TempDir(), "checkin-sites.json"))
	created, err := svc.Create(Input{Name: "T站", URL: srv.URL, Type: TypeAuto, Cookie: strPtr("k=v")})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 编辑弹层不回显机密：前端只带 id 和站点基本信息，Cookie 为 nil（不修改）。
	// 修复前 applyInput 会因 Cookie/Token 都为空直接返回校验错误，根本跑不到请求。
	res, err := svc.Test(context.Background(), Input{
		ID: created.ID, Name: created.Name, URL: created.URL, Type: TypeAuto,
	})
	if err != nil {
		t.Fatalf("带 id 的测试应复用已存凭证，却返回错误：%v", err)
	}
	if !res.OK {
		t.Fatalf("复用已存 Cookie 后应签到成功，实际 %s（status=%d）", res.Message, res.Status)
	}
}

func TestServiceTestAllowsEmptySecretForNewSite(t *testing.T) {
	// 新建弹层里用户还没填凭证就点「测试」：应真的发一次请求，把站点的 401
	// 当提示回给用户，而不是被本地「至少填一个」的校验挡住（那样看不到站点通不通）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "unauthorized"})
	}))
	defer srv.Close()

	svc := NewService(filepath.Join(t.TempDir(), "checkin-sites.json"))
	res, err := svc.Test(context.Background(), Input{Name: "新站", URL: srv.URL, Type: TypeAuto})
	if err != nil {
		t.Fatalf("无凭证的新建测试不应在本地校验失败：%v", err)
	}
	if res.OK {
		t.Fatal("站点回 401，结果应为失败")
	}
	if !contains(res.Message, "401") && !contains(res.Message, "Cookie") {
		t.Fatalf("失败原因应提示 401/Cookie：%s", res.Message)
	}
}

func TestServiceTestUnknownIDFallsBackToEmptySecret(t *testing.T) {
	// 站点被删掉后，编辑器里还留着旧 id：点「测试」不能报「站点不存在」，
	// 应退回成「无凭证跑一次」，让用户看到站点自己的 401。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "unauthorized"})
	}))
	defer srv.Close()

	svc := NewService(filepath.Join(t.TempDir(), "checkin-sites.json"))
	res, err := svc.Test(context.Background(), Input{
		ID: "c_deadbeef", Name: "旧站", URL: srv.URL, Type: TypeAuto,
	})
	if err != nil {
		t.Fatalf("不存在的 id 应退回无凭证执行，却返回错误：%v", err)
	}
	if res.OK {
		t.Fatal("站点回 401，结果应为失败")
	}
	if !contains(res.Message, "401") && !contains(res.Message, "Cookie") {
		t.Fatalf("失败原因应提示 401/Cookie：%s", res.Message)
	}
}

// countingReader 记录真正被读走的字节数，用来断言 reader 是否已读到底。
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestReadBodyDrainsAfterTruncation(t *testing.T) {
	// 响应超过上限时：raw 只留 maxBody，但剩余字节必须继续读进 Discard。
	// 否则 http.Response.Body 没读到底就 Close，连接被丢弃无法复用——
	// auto 模式一次要探测多个端点，每条都重新握手会让一键签到明显变慢。
	const extra = 5000
	total := maxBody + extra
	cr := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte("a"), total))}

	raw, truncated := readBody(io.NopCloser(cr))
	if len(raw) != maxBody {
		t.Fatalf("截断后长度应为 %d，实际 %d", maxBody, len(raw))
	}
	if !truncated {
		t.Fatal("超上限应标记为已截断")
	}
	// 修复前这里只读到 maxBody+1 就停（剩余 4999 字节没人读）。
	if cr.n != total {
		t.Fatalf("截断后应把剩余字节排空：已读 %d，总长 %d", cr.n, total)
	}
}
