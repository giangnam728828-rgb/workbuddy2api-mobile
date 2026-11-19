// Package checkin 多站点每日签到：把用户在其他站点（中转运维站、积分站等）的签到
// 收进面板，一键全签 + 到点自动签。
//
// 设计约束：
//   - 站点配置独立落盘（checkin-sites.json），不进 config.json：签到站点数量可变、
//     且含 Cookie/Token 这类机密，独立文件好备份也好整体删掉；
//   - 只支持「HTTP 请求 + 响应判定」这一种模型：不做站点专属逆向、不执行脚本。
//     auto 模式覆盖 new-api / one-api 系（多数中转站）的标准签到端点；
//     其余站点用 template 模式自己填请求模板；
//   - 机密只存在服务端：列表接口只回「是否已配置 + 末 4 位」，日志与错误信息
//     一律不打印 Cookie/Token 原文。
package checkin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// SiteType 签到站点的适配类型。
const (
	// TypeAuto 自动识别：按 new-api / one-api 系的标准端点顺序探测。
	// 绝大多数中转站都是这套接口（POST /api/user/checkin），用户只需要填网址 + Cookie。
	TypeAuto = "auto"
	// TypeTemplate 自定义模板：请求方式/路径/请求头/请求体/成功判定全部自己填。
	// 给「不是 new-api 系」或者接口改过的站点用。
	TypeTemplate = "template"
)

// Result 一次签到执行的结果。既写进站点 Last，也进 History。
type Result struct {
	At         time.Time `json:"at"`
	OK         bool      `json:"ok"`
	Message    string    `json:"message"`
	DurationMS int64     `json:"duration_ms"`
	Days       int       `json:"days,omitempty"`   // 连签天数（响应里解析得到才填）
	Reward     string    `json:"reward,omitempty"` // 奖励文案（解析得到才填）
	Status     int       `json:"status,omitempty"` // HTTP 状态码（网络层失败时为 0）
}

// Site 一个待签到的外部站点。Cookie/Token 是机密，只在服务端保存。
type Site struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`

	// 凭证二选一或同时给：Cookie 原样放进 Cookie 头；Token 作为 Bearer。
	// 很多中转站两种都认，同时带上兼容性最好。
	Cookie string `json:"cookie,omitempty"`
	Token  string `json:"token,omitempty"`

	// template 专用字段（auto 模式下留空）。
	Method          string            `json:"method,omitempty"`
	Path            string            `json:"path,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Body            string            `json:"body,omitempty"`
	SuccessKeyword  string            `json:"success_keyword,omitempty"`
	FailureKeyword  string            `json:"failure_keyword,omitempty"`
	SuccessJSONPath string            `json:"success_json_path,omitempty"`

	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Last      *Result   `json:"last,omitempty"`
	History   []Result  `json:"history,omitempty"` // 最多 HistoryLimit 条，新的在前
}

// HistoryLimit 每个站点保留的历史条数。够看最近一周多，又不会把文件撑大。
const HistoryLimit = 50

// Settings 全局设置（签到站点共用）。
type Settings struct {
	Enabled     bool   `json:"enabled"`
	RunAt       string `json:"run_at"`      // 本地时间 "HH:MM"
	TimeoutSec  int    `json:"timeout_sec"` // 单站超时
	Concurrency int    `json:"concurrency"` // 全量签到的并发上限
}

// DefaultSettings 首次使用的推荐值：早上 8:05 自动签，20 秒超时，并发 2。
func DefaultSettings() Settings {
	return Settings{Enabled: true, RunAt: "08:05", TimeoutSec: 20, Concurrency: 2}
}

// Normalize 把缺失/越界的字段拉回合法范围（老版本文件、手改文件都可能缺字段）。
func (s Settings) Normalize() Settings {
	d := DefaultSettings()
	if strings.TrimSpace(s.RunAt) == "" {
		s.RunAt = d.RunAt
	}
	if s.TimeoutSec < 5 || s.TimeoutSec > 120 {
		s.TimeoutSec = d.TimeoutSec
	}
	if s.Concurrency < 1 || s.Concurrency > 5 {
		s.Concurrency = d.Concurrency
	}
	return s
}

// ValidateSettings 校验用户提交的设置；RunAt 必须是 24 小时制 HH:MM。
func ValidateSettings(s Settings) error {
	if _, _, err := parseClock(s.RunAt); err != nil {
		return err
	}
	if s.TimeoutSec < 5 || s.TimeoutSec > 120 {
		return errors.New("超时时间要在 5–120 秒之间")
	}
	if s.Concurrency < 1 || s.Concurrency > 5 {
		return errors.New("并发数要在 1–5 之间")
	}
	return nil
}

// parseClock 解析 "HH:MM"；同时接受 "H:MM"。
func parseClock(v string) (int, int, error) {
	v = strings.TrimSpace(v)
	var h, m int
	if _, err := fmt.Sscanf(v, "%d:%d", &h, &m); err != nil {
		return 0, 0, errors.New("时间格式要写成 08:05 这样（24 小时制）")
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, errors.New("时间要在 00:00–23:59 之间")
	}
	return h, m, nil
}

// Input 面板提交的站点字段。Cookie/Token 用指针区分两种语义：
//   - nil   → 编辑时保留库里的原值（前端不回显机密，用户没改就不该被清空）
//   - ""    → 显式清空
//   - 非空  → 覆盖
type Input struct {
	ID              string            `json:"id,omitempty"`
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	Type            string            `json:"type"`
	Enabled         *bool             `json:"enabled"`
	Cookie          *string           `json:"cookie"`
	Token           *string           `json:"token"`
	Method          string            `json:"method"`
	Path            string            `json:"path"`
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
	SuccessKeyword  string            `json:"success_keyword"`
	FailureKeyword  string            `json:"failure_keyword"`
	SuccessJSONPath string            `json:"success_json_path"`
	Note            string            `json:"note"`
}

// applyInput 把 Input 落到站点上（创建与更新共用），并做完整校验。
// 校验放在落库前：宁可让用户改表单，也不要存进一个永远签到失败的配置。
//
// needSecret 区分「要不要强制有凭证」：落库场景必须有一个可用凭证；而试运行
// （Test）时用户可能压根没重填 Cookie（编辑弹层不回显机密），此时允许无凭证跑一次，
// 拿站点回的 401 当提示，比直接甩一句「至少填一个」有用。
func applyInput(s *Site, in Input, creating bool) error {
	return applyInputWith(s, in, creating, true)
}

// applyInputWith applyInput 的可控凭证校验版本。needSecret=false 时跳过
// 「Cookie/Token 至少填一个」，其余校验一字不差。
func applyInputWith(s *Site, in Input, creating, needSecret bool) error {
	// 仅开关（PATCH 语义）：列表里的启用开关只翻 Enabled，其余字段全部保留。
	// Name/URL/Type 全空 + Enabled 有值 = 开关请求；全量表单提交不会长这样
	// （校验会拦下空名称/空网址），两种语义不会撞。
	if !creating && in.Enabled != nil && in.Name == "" && in.URL == "" && in.Type == "" {
		s.Enabled = *in.Enabled
		return nil
	}
	if creating {
		s.ID = newID()
		s.CreatedAt = time.Now()
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = hostOf(in.URL)
	}
	if name == "" {
		return errors.New("站点名称不能为空")
	}
	if rs := []rune(name); len(rs) > 40 {
		name = string(rs[:40])
	}
	s.Name = name

	rawURL := strings.TrimSpace(in.URL)
	if rawURL == "" {
		return errors.New("站点网址不能为空")
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("网址要以 http:// 或 https:// 开头")
	}
	s.URL = strings.TrimRight(rawURL, "/")

	typ := strings.TrimSpace(in.Type)
	if typ == "" {
		typ = TypeAuto
	}
	if typ != TypeAuto && typ != TypeTemplate {
		return errors.New("站点类型只能是自动识别或自定义模板")
	}
	s.Type = typ

	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	} else if creating {
		s.Enabled = true
	}

	// 只有模板模式才保留自定义字段：auto 模式切回来后残留的模板字段会让判定顺序
	// 出乎用户意料（比如上一次填的失败关键字还在拦截）。
	if typ == TypeTemplate {
		method := strings.ToUpper(strings.TrimSpace(in.Method))
		if method == "" {
			method = "POST"
		}
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
			s.Method = method
		default:
			return errors.New("请求方式只能是 GET/POST/PUT/PATCH/DELETE")
		}
		s.Path = strings.TrimSpace(in.Path)
		s.Body = in.Body
		s.Headers = cleanHeaders(in.Headers)
		s.SuccessKeyword = strings.TrimSpace(in.SuccessKeyword)
		s.FailureKeyword = strings.TrimSpace(in.FailureKeyword)
		s.SuccessJSONPath = strings.TrimSpace(in.SuccessJSONPath)
	} else {
		s.Method, s.Path, s.Body = "", "", ""
		s.Headers, s.SuccessKeyword, s.FailureKeyword, s.SuccessJSONPath = nil, "", "", ""
	}

	if in.Cookie != nil {
		s.Cookie = strings.TrimSpace(*in.Cookie)
	}
	if in.Token != nil {
		s.Token = strings.TrimSpace(*in.Token)
	}
	if needSecret && s.Cookie == "" && s.Token == "" {
		return errors.New("Cookie 和 Token 至少填一个（多数站点填 Cookie 就行）")
	}

	note := strings.TrimSpace(in.Note)
	if rs := []rune(note); len(rs) > 120 {
		note = string(rs[:120])
	}
	s.Note = note
	return nil
}

// cleanHeaders 丢掉空键值与非法的头（比如用户从别处粘进来的 "Cookie: xxx"，
// 那种应该走专门的 Cookie 字段，否则会和自动拼的 Cookie 头打架）。
func cleanHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		if strings.EqualFold(k, "cookie") || strings.EqualFold(k, "authorization") {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// newID 生成 "c_" + 8 位 hex；碰撞概率对本地几十个站点可以忽略。
func newID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("c_%08x", time.Now().UnixNano()&0xffffffff)
	}
	return "c_" + hex.EncodeToString(b[:])
}

// hostOf 取域名（没有就返回空串），用于默认站点名与列表展示。
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Host
}

// cloneSite 深拷贝（map/slice 都要拷），避免读快照时与后台写盘并发读写同一份内存。
func cloneSite(s Site) Site {
	out := s
	if s.Headers != nil {
		out.Headers = make(map[string]string, len(s.Headers))
		for k, v := range s.Headers {
			out.Headers[k] = v
		}
	}
	if s.Last != nil {
		last := *s.Last
		out.Last = &last
	}
	if s.History != nil {
		out.History = append([]Result(nil), s.History...)
	}
	return out
}
