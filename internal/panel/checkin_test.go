package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/checkin"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// newCheckinPanel 起一个带签到中心的测试面板，API_KEY 固定，方便带 Bearer 调端点。
func newCheckinPanel(t *testing.T) (*Panel, http.Handler) {
	t.Helper()
	svc := checkin.NewService(t.TempDir() + "/checkin-sites.json")
	p := New(Config{Version: "test", APIKey: "k", Pool: pool.New(""), Checkin: svc})
	return p, p.mux
}

// callAPI 调面板端点并解析 JSON。
func callCheckinAPI(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer k")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestCheckinAPILifecycle(t *testing.T) {
	// 上游「站点」：一个永远成功的 new-api 风格签到接口。
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/checkin" && r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": "签到成功"})
			return
		}
		http.NotFound(w, r)
	}))
	defer upstreamSrv.Close()

	p, h := newCheckinPanel(t)
	_ = p

	// 1) 空库快照。
	code, out := callCheckinAPI(t, h, "GET", "/panel/api/checkin", "")
	if code != 200 {
		t.Fatalf("snapshot code=%d out=%v", code, out)
	}
	if out["running"] != false {
		t.Fatalf("初始不应在跑: %v", out["running"])
	}

	// 2) 创建站点。
	cookie := "session=abcd1234"
	enabled := true
	body, _ := json.Marshal(map[string]any{
		"name": "测试站", "url": upstreamSrv.URL, "type": "auto",
		"cookie": &cookie, "enabled": &enabled,
	})
	code, out = callCheckinAPI(t, h, "POST", "/panel/api/checkin/sites", string(body))
	if code != 200 {
		t.Fatalf("create code=%d out=%v", code, out)
	}
	site := out["site"].(map[string]any)
	id := site["id"].(string)
	if site["cookie_set"] != true || site["cookie_hint"] == "" {
		t.Fatalf("site=%v", site)
	}
	// 机密不出库：响应里不能有 cookie/token 字段。
	if _, ok := site["cookie"]; ok {
		t.Fatal("site 视图不应包含 cookie 原文")
	}
	if _, ok := site["token"]; ok {
		t.Fatal("site 视图不应包含 token 原文")
	}

	// 3) 快照应包含站点，today=pending。
	code, out = callCheckinAPI(t, h, "GET", "/panel/api/checkin", "")
	if code != 200 {
		t.Fatalf("snapshot2 code=%d", code)
	}
	sites := out["sites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("应有 1 个站点: %v", out)
	}
	if sites[0].(map[string]any)["today"] != "pending" {
		t.Fatalf("today 应为 pending: %v", sites[0])
	}
	sum := out["summary"].(map[string]any)
	if sum["total"].(float64) != 1 || sum["pending"].(float64) != 1 {
		t.Fatalf("summary=%v", sum)
	}

	// 4) 单站签到。
	code, out = callCheckinAPI(t, h, "POST", "/panel/api/checkin/sites/"+id+"/run", "")
	if code != 200 {
		t.Fatalf("run code=%d out=%v", code, out)
	}
	res := out["result"].(map[string]any)
	if res["ok"] != true || res["message"] != "签到成功" {
		t.Fatalf("result=%v", res)
	}

	// 5) 一键全签。
	code, out = callCheckinAPI(t, h, "POST", "/panel/api/checkin/run_all", "")
	if code != 200 {
		t.Fatalf("run_all code=%d out=%v", code, out)
	}
	results := out["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results=%v", results)
	}

	// 6) 快照 today=ok，pending=0。
	code, out = callCheckinAPI(t, h, "GET", "/panel/api/checkin", "")
	sum = out["summary"].(map[string]any)
	if sum["ok_today"].(float64) != 1 || sum["pending"].(float64) != 0 {
		t.Fatalf("summary after run=%v", sum)
	}
	site0 := out["sites"].([]any)[0].(map[string]any)
	if site0["today"] != "ok" {
		t.Fatalf("today 应为 ok: %v", site0)
	}

	// 7) 设置保存。
	code, out = callCheckinAPI(t, h, "POST", "/panel/api/checkin/settings",
		`{"enabled":false,"run_at":"07:30","timeout_sec":30,"concurrency":3}`)
	if code != 200 {
		t.Fatalf("settings code=%d out=%v", code, out)
	}
	st := out["settings"].(map[string]any)
	if st["run_at"] != "07:30" || st["enabled"] != false {
		t.Fatalf("settings=%v", st)
	}
	// 非法时间要 400。
	if code, _ = callCheckinAPI(t, h, "POST", "/panel/api/checkin/settings",
		`{"run_at":"25:99"}`); code != 400 {
		t.Fatalf("非法时间应 400，实际 %d", code)
	}

	// 8) 历史端点。
	code, out = callCheckinAPI(t, h, "GET", "/panel/api/checkin/history?site="+id, "")
	if code != 200 {
		t.Fatalf("history code=%d", code)
	}
	items := out["items"].([]any)
	if len(items) < 2 {
		t.Fatalf("至少两条历史（单站 + 全量）：%v", out)
	}

	// 9) 测试端点（不落库）。
	testBody, _ := json.Marshal(map[string]any{"name": "临时", "url": upstreamSrv.URL,
		"type": "auto", "cookie": "x=1"})
	code, out = callCheckinAPI(t, h, "POST", "/panel/api/checkin/test", string(testBody))
	if code != 200 {
		t.Fatalf("test code=%d out=%v", code, out)
	}
	code, out = callCheckinAPI(t, h, "GET", "/panel/api/checkin", "")
	if len(out["sites"].([]any)) != 1 {
		t.Fatal("test 不应新增站点")
	}

	// 10) 删除。
	code, _ = callCheckinAPI(t, h, "POST", "/panel/api/checkin/sites/"+id+"/delete", "")
	if code != 200 {
		t.Fatalf("delete code=%d", code)
	}
	if code, _ = callCheckinAPI(t, h, "POST", "/panel/api/checkin/sites/"+id+"/delete", ""); code != 404 {
		t.Fatalf("重复删除应 404，实际 %d", code)
	}
}

func TestCheckinAPIValidationAnd404(t *testing.T) {
	_, h := newCheckinPanel(t)

	// 缺凭证 → 400。
	code, out := callCheckinAPI(t, h, "POST", "/panel/api/checkin/sites",
		`{"name":"A","url":"https://a.com","type":"auto"}`)
	if code != 400 {
		t.Fatalf("缺凭证应 400，实际 %d out=%v", code, out)
	}
	if msg, _ := out["error"].(string); msg == "" {
		t.Fatal("错误信息不能为空")
	}

	// 更新不存在的站点 → 404。
	code, _ = callCheckinAPI(t, h, "POST", "/panel/api/checkin/sites/c_nope",
		`{"name":"A","url":"https://a.com","type":"auto","cookie":"c"}`)
	if code != 404 {
		t.Fatalf("更新不存在站点应 404，实际 %d", code)
	}

	// 非法 JSON → 400。
	code, _ = callCheckinAPI(t, h, "POST", "/panel/api/checkin/sites", "{bad")
	if code != 400 {
		t.Fatalf("坏 JSON 应 400，实际 %d", code)
	}
}

func TestCheckinAPIWithoutServiceReturns501(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "k", Pool: pool.New("")})
	req := httptest.NewRequest("GET", "/panel/api/checkin", nil)
	req.Header.Set("Authorization", "Bearer k")
	rec := httptest.NewRecorder()
	p.mux.ServeHTTP(rec, req)
	if rec.Code != 501 {
		t.Fatalf("未接线应 501，实际 %d", rec.Code)
	}
}

func TestCheckinAPIRequiresAuth(t *testing.T) {
	_, h := newCheckinPanel(t)
	req := httptest.NewRequest("GET", "/panel/api/checkin", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("无密钥应 401，实际 %d", rec.Code)
	}
}
