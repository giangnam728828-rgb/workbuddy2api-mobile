package checkin

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/netca"
)

// maxBody 响应体读取上限。签到接口的响应都是几百字节的小 JSON；
// 256 KiB 足够容错，也能防止误填一个视频地址把内存吃满。
const maxBody = 256 << 10

// checkinUA 固定一个常见桌面 Chrome UA：不少站点会按 UA 直接拦掉 curl/Go-http-client
// （返回 403 或挑战页），用默认 UA 会让「Cookie 明明是对的」也签不上。
const checkinUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// autoCandidates auto 模式探测的标准端点。顺序按普及度排：new-api / one-api 系
// 绝大多数是第一个（POST /api/user/checkin），后面是社区分支与旧版的写法。
// 探测只接受「非 404/405」的响应作为结果——所以探测本身不会把一次签到的状态
// 弄乱（签到接口都是幂等的，重复调用最多回「今日已签到」）。
// 端点清单严格按规格给的五条：多探测一条就多一次「误判成成功」的机会
// （有的站把未命中的路径统统 200 回 SPA 首页），少探测则漏掉社区分支。
var autoCandidates = []struct{ Method, Path string }{
	{"POST", "api/user/checkin"},
	{"GET", "api/user/checkin"},
	{"POST", "api/user/sign_in"},
	{"GET", "api/user/sign_in"},
	{"POST", "api/checkin"},
}

// Runner 真正发请求并判定结果。无状态，可并发使用。
type Runner struct {
	client *http.Client
}

// NewRunner 默认 Runner：不设 Client.Timeout（单次超时由 ctx 控制，
// 这样连 Redirect 也受同一个 deadline 约束）。
func NewRunner() *Runner {
	return &Runner{client: &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			MaxIdleConnsPerHost:   2,
			// 根证书池显式指定：Android 上 Go 的 SystemCertPool 是空的，
			// 不设这一项，手机上给「其他网站」签到会全部失败于 unknown authority。
			TLSClientConfig: &tls.Config{RootCAs: netca.Pool(), MinVersion: tls.VersionTLS12},
		},
		// 最多 5 跳；跳到非 http(s) 直接停，避免 file:// 之类被当成重定向目标。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("重定向次数过多")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("重定向到了非 HTTP 地址")
			}
			return nil
		},
	}}
}

// Run 执行一个站点的签到，返回可直接展示的结果（永远不返回 error：
// 网络层失败也是一次「失败的结果」，要进历史）。
func (r *Runner) Run(ctx context.Context, s Site, st Settings) Result {
	st = st.Normalize()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(st.TimeoutSec)*time.Second)
	defer cancel()

	start := time.Now()
	res := Result{At: start}
	finish := func(ok bool, msg string, status int) Result {
		res.OK = ok
		res.Message = trimMessage(msg)
		res.Status = status
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}

	switch s.Type {
	case TypeTemplate:
		return r.runTemplate(ctx, s, finish)
	default:
		return r.runAuto(ctx, s, finish)
	}
}

// runAuto auto 模式：按候选端点探测，第一个「不像 404」的响应即为结果。
func (r *Runner) runAuto(ctx context.Context, s Site, finish func(bool, string, int) Result) Result {
	base := strings.TrimRight(s.URL, "/")
	var lastStatus int
	for _, cand := range autoCandidates {
		target := base + "/" + cand.Path
		req, err := r.newRequest(ctx, s, cand.Method, target, "")
		if err != nil {
			return finish(false, "请求构造失败："+err.Error(), 0)
		}
		resp, err := r.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return finish(false, timeoutMessage(ctx, s, 0), 0)
			}
			// 网络层失败是终局：换端点也一样连不上，直接报错，不把 6 个端点试一遍。
			return finish(false, netErrorMessage(err), 0)
		}
		body, _ := readBody(resp.Body)
		_ = resp.Body.Close()
		lastStatus = resp.StatusCode

		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			continue // 这个站点没有这个端点，换下一个
		}
		if !isJSONResponse(resp, body) {
			return finish(false, "自动识别失败：响应不是 JSON（HTTP "+
				strconv.Itoa(resp.StatusCode)+"），该站可能不是 new-api 系，请改用自定义模板", resp.StatusCode)
		}
		return finishJSON(body, resp.StatusCode, finish)
	}
	if lastStatus == 0 {
		return finish(false, "连接失败", 0)
	}
	return finish(false, "没找到签到接口（自动识别失败）：该站可能不是 new-api 系，请改用自定义模板", lastStatus)
}

// runTemplate 自定义模板：完全按用户填的方法/路径/请求体发一次，再按判定规则下结论。
func (r *Runner) runTemplate(ctx context.Context, s Site, finish func(bool, string, int) Result) Result {
	method := strings.ToUpper(strings.TrimSpace(s.Method))
	if method == "" {
		method = "POST"
	}
	target := resolveTarget(s.URL, s.Path)
	body := expandPlaceholders(s.Body, s)
	req, err := r.newRequest(ctx, s, method, target, body)
	if err != nil {
		return finish(false, "请求构造失败："+err.Error(), 0)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return finish(false, timeoutMessage(ctx, s, 0), 0)
		}
		return finish(false, netErrorMessage(err), 0)
	}
	raw, _ := readBody(resp.Body)
	_ = resp.Body.Close()
	text := string(raw)

	// 判定顺序是「先用用户给的规则，再退回通用规则」：用户明确写了失败关键字，
	// 就不能因为 HTTP 200 就判成成功（很多站用 200 + {"success":false} 表示失败）。
	if s.FailureKeyword != "" && strings.Contains(text, s.FailureKeyword) {
		return finish(false, firstLine(text, s.FailureKeyword), resp.StatusCode)
	}
	if s.SuccessKeyword != "" && strings.Contains(text, s.SuccessKeyword) {
		return finish(true, jsonMessageOr(raw, "签到成功"), resp.StatusCode)
	}
	if s.SuccessJSONPath != "" {
		ok, msg := judgeJSONPath(raw, s.SuccessJSONPath)
		return finish(ok, msg, resp.StatusCode)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return finish(false, httpStatusMessage(resp.StatusCode), resp.StatusCode)
	}
	if !isJSONResponse(resp, raw) {
		return finish(false, "返回的是网页而不是数据（HTTP "+strconv.Itoa(resp.StatusCode)+
			"）：Cookie 可能已失效，或这个地址不是签到接口", resp.StatusCode)
	}
	return finishJSON(raw, resp.StatusCode, finish)
}

// newRequest 组装请求：固定 UA + Accept，按站点配置补 Cookie / Bearer / 自定义头。
func (r *Runner) newRequest(ctx context.Context, s Site, method, target, body string) (*http.Request, error) {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("网址不合法")
	}
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", checkinUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	if s.Cookie != "" {
		req.Header.Set("Cookie", s.Cookie)
	}
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	if body != "" {
		if req.Header.Get("Content-Type") == "" {
			if strings.HasPrefix(strings.TrimSpace(body), "{") || strings.HasPrefix(strings.TrimSpace(body), "[") {
				req.Header.Set("Content-Type", "application/json")
			} else {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
		}
	}
	return req, nil
}

// resolveTarget 拼最终 URL：
//   - path 为空 → 用 URL 原样；
//   - path 是完整地址 → 用 path；
//   - 否则把 path 接在 base 后面（保留 base 里的子路径，兼容部署在 /newapi/ 的站）。
func resolveTarget(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	path = strings.TrimSpace(path)
	if path == "" {
		return base
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return base + "/" + strings.TrimLeft(path, "/")
}

// expandPlaceholders 模板占位符替换。只做字面替换，不执行任何表达式：
// 模板来自用户输入，任何「求值」都会变成在网关进程里执行用户代码。
func expandPlaceholders(body string, s Site) string {
	if body == "" {
		return ""
	}
	now := time.Now()
	repl := strings.NewReplacer(
		"{{cookie}}", s.Cookie,
		"{{token}}", s.Token,
		"{{date}}", now.Format("2006-01-02"),
		"{{ts}}", strconv.FormatInt(now.Unix(), 10),
	)
	return repl.Replace(body)
}

// isJSONResponse 判断响应是不是 JSON 数据。以 Content-Type 为主、正文首字符兜底：
// 有些站会返回 text/plain 但内容是 JSON，也接受。
func isJSONResponse(resp *http.Response, body []byte) bool {
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "json") {
		return true
	}
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

// finishJSON 解析 JSON 响应并给出成功/失败结论（auto 与未配置规则的 template 共用）。
func finishJSON(raw []byte, status int, finish func(bool, string, int) Result) Result {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// 数组形态（少数站返回 [{...}]）取第一个对象再试一次。
		var arr []map[string]any
		if err2 := json.Unmarshal(raw, &arr); err2 == nil && len(arr) > 0 {
			m = arr[0]
		} else {
			return finish(false, "响应不是对象 JSON，无法判断签到结果", status)
		}
	}
	has, ok := jsonSuccess(m)
	msg := jsonMessage(m)
	days, reward := jsonExtras(m)
	// 401/403 是「凭证无效」的明确信号：无论正文说什么都按失败处理，并给出动作建议。
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return finish(false, prefixStatus(httpStatusMessage(status), msg), status)
	}
	if !has {
		// 没有显式成功标记：只有正文里带「成功/已签到」这类词才当成功，
		// 否则宁可按失败报——让用户看到原文，比误报「已签到」好。
		if msg != "" && containsAny(msg, "成功", "已签到", "已签", "success", "already") {
			r := finish(true, msg, status)
			r.Days, r.Reward = days, reward
			return r
		}
		return finish(false, emptyAs(msg, "接口没有返回成功标记（检查 Cookie 是否有效）"), status)
	}
	if status < 200 || status >= 300 {
		return finish(false, httpStatusMessage(status)+"："+emptyAs(msg, "签到失败"), status)
	}
	r := finish(ok, emptyAs(msg, ternary(ok, "签到成功", "签到失败")), status)
	r.Days, r.Reward = days, reward
	return r
}

// prefixStatus 状态提示 + 正文说明（正文为空时不留悬挂冒号）。
func prefixStatus(status string, msg string) string {
	if strings.TrimSpace(msg) == "" {
		return status
	}
	return status + "：" + msg
}

// jsonSuccess 识别常见的成功字段。返回 has=是否找到显式标记。
func jsonSuccess(m map[string]any) (has, ok bool) {
	if v, found := m["success"]; found {
		if b, isBool := v.(bool); isBool {
			return true, b
		}
		if n, isNum := toFloat(v); isNum {
			return true, n != 0
		}
	}
	if v, found := m["ok"]; found {
		if b, isBool := v.(bool); isBool {
			return true, b
		}
	}
	if v, found := m["ret"]; found {
		if n, isNum := toFloat(v); isNum {
			return true, n == 1
		}
	}
	if v, found := m["code"]; found {
		if n, isNum := toFloat(v); isNum {
			return true, n == 0 || n == 200
		}
	}
	return false, false
}

// jsonMessage 取人类可读文案。data 为字符串时当奖励文案。
func jsonMessage(m map[string]any) string {
	for _, k := range []string{"message", "msg", "error", "err", "detail"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// jsonMessageOr 解析不到 message 时的兜底文案。
func jsonMessageOr(raw []byte, fallback string) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if s := jsonMessage(m); s != "" {
			return s
		}
	}
	return fallback
}

// jsonExtras 连签天数与奖励：不同站字段名不一样，按常见顺序取。
func jsonExtras(m map[string]any) (days int, reward string) {
	get := func(mm map[string]any, keys ...string) (any, bool) {
		for _, k := range keys {
			if v, ok := mm[k]; ok {
				return v, true
			}
		}
		return nil, false
	}
	dayNum := func(mm map[string]any) int {
		v, ok := get(mm, "days", "continuous_days", "continuousDays", "checkin_days", "checkinDays", "streak")
		if !ok {
			return 0
		}
		if n, isNum := toFloat(v); isNum {
			return int(n)
		}
		if s, isStr := v.(string); isStr {
			if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				return n
			}
		}
		return 0
	}
	if data, ok := m["data"].(map[string]any); ok {
		days = dayNum(data)
		if v, found := get(data, "reward", "quota", "credits", "amount"); found {
			reward = anyText(v)
		}
	} else if s, ok := m["data"].(string); ok {
		// data 是字符串：多数站直接放「+5 积分」这类奖励文案。
		reward = strings.TrimSpace(s)
	}
	if days == 0 {
		days = dayNum(m)
	}
	if reward == "" {
		if v, found := get(m, "reward", "quota", "credits"); found {
			reward = anyText(v)
		}
	}
	return days, reward
}

// judgeJSONPath 按点路径取成功标记，如 "success"、"data.checked"。
func judgeJSONPath(raw []byte, path string) (bool, string) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false, "响应不是 JSON，无法按字段判定"
	}
	cur := any(m)
	for _, seg := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return false, "响应里没有字段 " + path
		}
		v, found := obj[seg]
		if !found {
			return false, "响应里没有字段 " + path
		}
		cur = v
	}
	msg := jsonMessage(m)
	switch v := cur.(type) {
	case bool:
		if v {
			return true, emptyAs(msg, "签到成功")
		}
		return false, emptyAs(msg, "字段 "+path+" 为 false")
	case float64:
		if v != 0 {
			return true, emptyAs(msg, "签到成功")
		}
		return false, emptyAs(msg, "字段 "+path+" 为 0")
	case string:
		if containsAny(strings.ToLower(v), "true", "ok", "success", "1", "已签", "成功") {
			return true, emptyAs(msg, "签到成功")
		}
		return false, emptyAs(msg, "字段 "+path+" = "+v)
	}
	return false, emptyAs(msg, "字段 "+path+" 的值无法判断")
}

// drainLimit 截断后最多再排空多少字节。加大这个数只为让连接能被复用，
// 不值得为一个恶意大响应无限读下去。
const drainLimit = 1 << 20

// readBody 读取响应体，超上限截断（不报错：签到响应不会那么大，截断只为兜底）。
//
// 截断时顺手把剩余部分读进 Discard：http.Response.Body 没读到底就 Close，
// 连接会被直接丢弃而进不了空闲池——auto 模式一次要连试多个端点，
// 每条都重新 TCP+TLS 握手的话，弱网手机上「一键签到」会明显变慢。
func readBody(body io.ReadCloser) ([]byte, bool) {
	if body == nil {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxBody+1))
	if err != nil {
		return raw, false
	}
	if len(raw) > maxBody {
		_, _ = io.Copy(io.Discard, io.LimitReader(body, drainLimit))
		return raw[:maxBody], true
	}
	return raw, false
}

// httpStatusMessage 把状态码翻译成用户能懂的动作建议。
func httpStatusMessage(code int) string {
	switch {
	case code == 401 || code == 403:
		// 用「可能已失效」而不是「无效」：403 也可能是站点风控/接口权限，先让用户
		// 重新取一次 Cookie，确实是权限问题的话站点正文里会说。
		return "Cookie 可能已失效（HTTP " + strconv.Itoa(code) + "），重新登录取一次"
	case code == 404:
		return "HTTP 404：签到接口不存在"
	case code == 429:
		return "HTTP 429：请求太频繁，稍后再试"
	case code >= 500:
		return "HTTP " + strconv.Itoa(code) + "：站点自己出错了，稍后再试"
	default:
		return "HTTP " + strconv.Itoa(code) + "：签到请求被拒绝"
	}
}

// timeoutMessage 超时的中文说明（带上配置的值，用户才知道去哪儿改）。
func timeoutMessage(ctx context.Context, s Site, _ int) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "请求超时：站点没在设定时间内响应（可在设置里调大超时）"
	}
	return "请求被取消"
}

// netErrorMessage 网络层错误 → 中文短句。不把完整 error 抛给用户看：
// 里面常带 IP、端口和 Go 的内部分层信息，对「我该怎么办」没有帮助。
func netErrorMessage(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "域名解析失败：" + dnsErr.Name + "（检查网址是否写对）"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		msg := opErr.Err.Error()
		if strings.Contains(msg, "refused") {
			return "连接被拒绝：站点没开或端口不对"
		}
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "x509") || strings.Contains(low, "tls") || strings.Contains(low, "certificate"):
		return "TLS 证书校验失败：站点证书有问题，或系统时间不对"
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline"):
		return "请求超时：站点没响应"
	case strings.Contains(low, "no such host"):
		return "域名解析失败：检查网址是否写对"
	case strings.Contains(low, "connection reset"):
		return "连接被站点重置（可能有风控）"
	}
	if len(msg) > 120 {
		msg = msg[:120] + "…"
	}
	return "请求失败：" + msg
}

// trimMessage 结果文案截断：历史列表要显示，太长会把卡片撑爆。
func trimMessage(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	rs := []rune(s)
	if len(rs) > 200 {
		return string(rs[:200]) + "…"
	}
	return s
}

// firstLine 失败时优先回显「离关键字最近的一句」，比整页 HTML 有用。
func firstLine(text, keyword string) string {
	idx := strings.Index(text, keyword)
	if idx < 0 {
		return trimMessage(text)
	}
	start := idx - 80
	if start < 0 {
		start = 0
	}
	end := idx + len(keyword) + 80
	if end > len(text) {
		end = len(text)
	}
	return trimMessage(text[start:end])
}

// anyText 把任意 JSON 标量转成短文本。
func anyText(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return ""
}

// toFloat JSON 数字统一是 float64；字符串数字也认。
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case int:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func emptyAs(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
