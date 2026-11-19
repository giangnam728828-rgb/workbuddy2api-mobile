package checkin

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

// Service 面板与自动调度共用的入口：组合 Store（配置）与 Runner（执行），
// 并对「全量签到」加一把互斥（一键按钮连点、自动签到与手动全量撞车都挡在这里）。
type Service struct {
	store  *Store
	runner *Runner

	// runMu 全量签到互斥：TryLock 语义，同一时刻只允许一轮全量。
	// 单站签到不占这把锁（补签一个站点不该被别人的全量卡死）。
	runMu sync.Mutex

	// autoRunning 防止自动循环重入（循环本身串行，这里只是兜底）。
	autoRunning bool
}

// NewService 打开（或创建）站点库。path 一般是 data/checkin-sites.json。
func NewService(path string) *Service {
	return &Service{store: OpenStore(path), runner: NewRunner()}
}

// SiteResult 一轮全量签到里单站的结果（给面板的 run_all 返回值用）。
type SiteResult struct {
	SiteID     string `json:"site_id"`
	SiteName   string `json:"site_name"`
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	DurationMS int64  `json:"duration_ms"`
}

// Snapshot 面板列表视图（机密不出库）。
type Snapshot struct {
	Settings  Settings   `json:"settings"`
	NextRunAt time.Time  `json:"next_run_at"`
	Running   bool       `json:"running"`
	Summary   Summary    `json:"summary"`
	Sites     []SiteView `json:"sites"`
}

// Summary 今日汇总。
type Summary struct {
	Total     int `json:"total"`
	Enabled   int `json:"enabled"`
	OKToday   int `json:"ok_today"`
	FailToday int `json:"fail_today"`
	Pending   int `json:"pending"`
}

// SiteCustom 模板站点的自定义配置（编辑表单回显用）。
// Cookie/Token 不在这里：机密不出库，表单里留空表示「不修改」。
type SiteCustom struct {
	Method   string            `json:"method,omitempty"`
	Path     string            `json:"path,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     string            `json:"body,omitempty"`
	OKKw     string            `json:"ok_kw,omitempty"`
	BadKw    string            `json:"bad_kw,omitempty"`
	JSONPath string            `json:"json_path,omitempty"`
}

// SiteView 单站视图。
type SiteView struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	URL        string      `json:"url"`
	Host       string      `json:"host"`
	Type       string      `json:"type"`
	Enabled    bool        `json:"enabled"`
	CookieSet  bool        `json:"cookie_set"`
	CookieHint string      `json:"cookie_hint,omitempty"`
	TokenSet   bool        `json:"token_set"`
	HasCustom  bool        `json:"has_custom"`
	Custom     *SiteCustom `json:"custom,omitempty"`
	Note       string      `json:"note,omitempty"`
	Today      string      `json:"today"` // ok | fail | pending | off
	Last       *Result     `json:"last,omitempty"`
	History    []Result    `json:"history,omitempty"`
}

// Snapshot 生成当前状态快照。
func (s *Service) Snapshot() Snapshot {
	st, sites, lastDay := s.store.snapshot()
	now := time.Now()
	today := dayKey(now)

	out := Snapshot{
		Settings:  st,
		NextRunAt: nextRunAt(st, lastDay, now),
		Running:   s.isBusy(),
	}
	out.Sites = make([]SiteView, 0, len(sites))
	for _, site := range sites {
		view := buildSiteView(site, true)
		switch {
		case !site.Enabled:
			view.Today = "off"
		case site.Last == nil:
			view.Today = "pending"
		case dayKey(site.Last.At) == today:
			if site.Last.OK {
				view.Today = "ok"
			} else {
				view.Today = "fail"
			}
		default:
			view.Today = "pending"
		}
		out.Sites = append(out.Sites, view)
	}
	out.Summary = summarize(out.Sites)
	return out
}

// summarize 由视图推导今日汇总（Enabled 且非 off 且今天没结果 = 待签）。
func summarize(views []SiteView) Summary {
	var sm Summary
	sm.Total = len(views)
	for _, v := range views {
		if v.Enabled {
			sm.Enabled++
		}
		switch v.Today {
		case "ok":
			sm.OKToday++
		case "fail":
			sm.FailToday++
		case "pending":
			sm.Pending++
		}
	}
	return sm
}

// tailRunes 取字符串末尾 n 个 rune（Cookie 提示用，只露尾不露头）。
func tailRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[len(rs)-n:])
}

// Create 新增站点。
func (s *Service) Create(in Input) (SiteView, error) {
	var site Site
	if err := applyInput(&site, in, true); err != nil {
		return SiteView{}, err
	}
	saved, err := s.store.add(site)
	if err != nil {
		return SiteView{}, errors.New("保存失败：" + err.Error())
	}
	return s.viewOf(saved.ID)
}

// Update 编辑站点。Cookie/Token 为 nil 时保留原值（前端不回显机密）。
func (s *Service) Update(id string, in Input) (SiteView, error) {
	_, err := s.store.update(id, func(site *Site) error {
		return applyInput(site, in, false)
	})
	if err != nil {
		return SiteView{}, err
	}
	return s.viewOf(id)
}

// Delete 删除站点。
func (s *Service) Delete(id string) error { return s.store.remove(id) }

// viewOf 取站点视图（创建/更新后回显用）。
func (s *Service) viewOf(id string) (SiteView, error) {
	site, ok := s.store.get(id)
	if !ok {
		return SiteView{}, ErrNotFound
	}
	return buildSiteView(site, false), nil
}

// buildSiteView 站点 → 视图（机密不出库）。trimHistory 决定是否把历史截到 10 条
// （列表要截；创建/更新回显不用）。
func buildSiteView(site Site, trimHistory bool) SiteView {
	v := SiteView{
		ID:        site.ID,
		Name:      site.Name,
		URL:       site.URL,
		Host:      hostOf(site.URL),
		Type:      site.Type,
		Enabled:   site.Enabled,
		CookieSet: site.Cookie != "",
		TokenSet:  site.Token != "",
		Note:      site.Note,
		HasCustom: site.Type == TypeTemplate &&
			(site.Path != "" || site.Body != "" || site.SuccessKeyword != "" ||
				site.FailureKeyword != "" || site.SuccessJSONPath != "" || len(site.Headers) > 0),
		History: site.History,
		Last:    site.Last,
	}
	if v.HasCustom {
		v.Custom = &SiteCustom{
			Method:   site.Method,
			Path:     site.Path,
			Headers:  site.Headers,
			Body:     site.Body,
			OKKw:     site.SuccessKeyword,
			BadKw:    site.FailureKeyword,
			JSONPath: site.SuccessJSONPath,
		}
	}
	if len(v.History) > 10 && trimHistory {
		v.History = v.History[:10]
	}
	if site.Cookie != "" {
		v.CookieHint = "…" + cookieTail(site.Cookie)
	}
	if !v.Enabled {
		v.Today = "off"
	} else if site.Last == nil {
		v.Today = "pending"
	} else if dayKey(site.Last.At) == dayKey(time.Now()) {
		if site.Last.OK {
			v.Today = "ok"
		} else {
			v.Today = "fail"
		}
	} else {
		v.Today = "pending"
	}
	return v
}

// cookieTail Cookie 提示只露末 4 位。取最后一个 name=value 的 value 尾部：
// 直接切原串会带上 "=" / ";"（"session=abcd" → "=abcd"），对辨认站点没帮助。
func cookieTail(cookie string) string {
	last := cookie
	if i := strings.LastIndex(last, ";"); i >= 0 {
		last = last[i+1:]
	}
	last = strings.TrimSpace(last)
	// 末段里还有 "=" 就取等号后的值；值太短则退回末段本身（"a=b" → "b"）。
	if i := strings.Index(last, "="); i >= 0 && i+1 < len(last) {
		last = last[i+1:]
	}
	if last == "" {
		last = cookie
	}
	return tailRunes(last, 4)
}

// Run 立即签到单个站点，结果写进历史。
func (s *Service) Run(ctx context.Context, id string) (Result, error) {
	st, sites, _ := s.store.snapshot()
	var site *Site
	for i := range sites {
		if sites[i].ID == id {
			site = &sites[i]
			break
		}
	}
	if site == nil {
		return Result{}, ErrNotFound
	}
	res := s.runner.Run(ctx, *site, st)
	s.store.record(id, res)
	logResult(*site, res)
	return res, nil
}

// RunAll 全量签到：并发跑所有启用的站点。已在跑时返回 ErrBusy。
func (s *Service) RunAll(ctx context.Context) ([]SiteResult, Summary, error) {
	if !s.tryBeginRun() {
		return nil, Summary{}, ErrBusy
	}
	defer s.endRun()

	st, sites, _ := s.store.snapshot()
	var enabled []Site
	for _, site := range sites {
		if site.Enabled {
			enabled = append(enabled, site)
		}
	}
	if len(enabled) == 0 {
		return []SiteResult{}, s.Snapshot().Summary, nil
	}

	sem := make(chan struct{}, st.Concurrency)
	results := make([]SiteResult, len(enabled))
	var wg sync.WaitGroup
	for i, site := range enabled {
		wg.Add(1)
		go func(i int, site Site) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res := s.runner.Run(ctx, site, st)
			s.store.record(site.ID, res)
			logResult(site, res)
			results[i] = SiteResult{
				SiteID:     site.ID,
				SiteName:   site.Name,
				OK:         res.OK,
				Message:    res.Message,
				DurationMS: res.DurationMS,
			}
		}(i, site)
	}
	wg.Wait()
	return results, s.Snapshot().Summary, nil
}

// snapshotAll 取快照（RunAll 结束后算汇总用）。
func (s *Service) snapshotAll() Snapshot { return s.Snapshot() }

// Test 试运行一份配置：不落库、不写历史。表单里「测试」按钮用。
func (s *Service) Test(ctx context.Context, in Input) (Result, error) {
	var site Site
	if err := applyInput(&site, in, true); err != nil {
		return Result{}, err
	}
	// 测试时 Cookie/Token 可能为空（用户还没填就想先看看接口通不通），
	// 允许跑：多数站会回 401，正好当作「凭证没填」的提示。
	st, _, _ := s.store.snapshot()
	res := s.runner.Run(ctx, site, st)
	// 测试结果不入库，但日志里留一行（不带任何机密）。
	log.Printf("[checkin] 测试 %s (%s): %s", site.Name, hostOf(site.URL), resultLine(res))
	return res, nil
}

// Settings 当前设置。
func (s *Service) Settings() Settings {
	st, _, _ := s.store.snapshot()
	return st
}

// SetSettings 保存设置（校验后落盘）。
func (s *Service) SetSettings(st Settings) (Settings, error) {
	st = st.Normalize()
	if err := ValidateSettings(st); err != nil {
		return Settings{}, err
	}
	if err := s.store.setSettings(st); err != nil {
		return Settings{}, errors.New("保存失败：" + err.Error())
	}
	return st, nil
}

// History 单站历史（历史弹层用）。
func (s *Service) History(id string, limit int) []Result {
	_, sites, _ := s.store.snapshot()
	for _, site := range sites {
		if site.ID == id {
			out := append([]Result(nil), site.History...)
			if limit > 0 && len(out) > limit {
				out = out[:limit]
			}
			return out
		}
	}
	return nil
}

// isBusy 是否有全量签到在跑（TryLock 试一下立刻还回去）。
func (s *Service) isBusy() bool {
	if s.runMu.TryLock() {
		s.runMu.Unlock()
		return false
	}
	return true
}

// tryBeginRun/endRun 全量签到互斥的进入/退出。
func (s *Service) tryBeginRun() bool { return s.runMu.TryLock() }
func (s *Service) endRun()           { s.runMu.Unlock() }

// logResult 单次结果落日志（不带机密）。
func logResult(site Site, res Result) {
	log.Printf("[checkin] %s %s: %s", site.Name, resultTag(res), resultLine(res))
}

func resultTag(r Result) string {
	if r.OK {
		return "成功"
	}
	return "失败"
}

func resultLine(r Result) string {
	line := r.Message
	if r.Days > 0 {
		line += "（连签 " + itoa(r.Days) + " 天）"
	}
	if r.Reward != "" {
		line += " " + r.Reward
	}
	if r.DurationMS > 0 {
		line += " · " + itoa(int(r.DurationMS)) + "ms"
	}
	return line
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
