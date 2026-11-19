package checkin

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// fileData checkin-sites.json 的磁盘结构。版本号留给以后迁移用。
type fileData struct {
	Version     int      `json:"version"`
	Settings    Settings `json:"settings"`
	Sites       []Site   `json:"sites"`
	LastAutoDay string   `json:"last_auto_day,omitempty"` // 自动签到最后执行的自然日（YYYY-MM-DD，本地时区）
}

var (
	// ErrNotFound 站点不存在（删除/更新的竞态、用户开了两个页面）。
	ErrNotFound = errors.New("站点不存在（可能已被删除）")
	// ErrBusy 已经有一轮全量签到在跑。
	ErrBusy = errors.New("正在签到中，请稍候")
)

// Store 站点配置的内存副本 + 落盘。所有读写走同一把锁，保证面板列表与
// 后台自动签到看到的是同一份数据。
type Store struct {
	mu   sync.RWMutex
	path string
	data fileData
}

// OpenStore 读取站点配置；文件不存在 = 空库（首次使用），
// 文件损坏 = 记日志后按空库继续（不要让一份坏 JSON 把整个网关拖得起不来）。
func OpenStore(path string) *Store {
	s := &Store{path: path, data: fileData{Version: 1, Settings: DefaultSettings()}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[checkin] 读取 %s 失败（按空库继续）：%v", path, err)
		}
		return s
	}
	var d fileData
	if err := json.Unmarshal(raw, &d); err != nil {
		log.Printf("[checkin] %s 不是合法 JSON（按空库继续，原文件保留）：%v", path, err)
		return s
	}
	d.Settings = d.Settings.Normalize()
	if d.Sites == nil {
		d.Sites = []Site{}
	}
	s.data = d
	return s
}

// Path 配置文件路径（日志/测试用）。
func (s *Store) Path() string { return s.path }

// snapshot 返回设置 + 站点深拷贝 + 自动签到最后执行日。
func (s *Store) snapshot() (Settings, []Site, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sites := make([]Site, 0, len(s.data.Sites))
	for _, v := range s.data.Sites {
		sites = append(sites, cloneSite(v))
	}
	return s.data.Settings, sites, s.data.LastAutoDay
}

// get 取单个站点的深拷贝。
func (s *Store) get(id string) (Site, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.data.Sites {
		if v.ID == id {
			return cloneSite(v), true
		}
	}
	return Site{}, false
}

// add 追加站点并落盘。落盘失败会回滚内存，避免「面板显示成功、重启后消失」。
func (s *Store) add(site Site) (Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.data.Sites
	s.data.Sites = append(append([]Site(nil), prev...), site)
	if err := s.flushLocked(); err != nil {
		s.data.Sites = prev
		return Site{}, err
	}
	return cloneSite(site), nil
}

// update 对站点执行 fn，通过后落盘；失败回滚。
func (s *Store) update(id string, fn func(*Site) error) (Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, v := range s.data.Sites {
		if v.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Site{}, ErrNotFound
	}
	old := s.data.Sites[idx]
	next := cloneSite(old)
	if err := fn(&next); err != nil {
		return Site{}, err
	}
	s.data.Sites[idx] = next
	if err := s.flushLocked(); err != nil {
		s.data.Sites[idx] = old
		return Site{}, err
	}
	return cloneSite(next), nil
}

// remove 删除站点；返回是否真的删掉了。
func (s *Store) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, v := range s.data.Sites {
		if v.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	prev := s.data.Sites
	next := make([]Site, 0, len(prev)-1)
	next = append(next, prev[:idx]...)
	next = append(next, prev[idx+1:]...)
	s.data.Sites = next
	if err := s.flushLocked(); err != nil {
		s.data.Sites = prev
		return err
	}
	return nil
}

// setSettings 保存全局设置。
func (s *Store) setSettings(st Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.data.Settings
	s.data.Settings = st
	if err := s.flushLocked(); err != nil {
		s.data.Settings = prev
		return err
	}
	return nil
}

// record 把一次结果写进站点（Last + History 头插 + 截断）。
// 站点在执行期间被删了不算错误：结果没地方放而已。
func (s *Store) record(id string, r Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.data.Sites {
		if s.data.Sites[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	site := &s.data.Sites[idx]
	rr := r
	site.Last = &rr
	hist := make([]Result, 0, len(site.History)+1)
	hist = append(hist, r)
	hist = append(hist, site.History...)
	if len(hist) > HistoryLimit {
		hist = hist[:HistoryLimit]
	}
	site.History = hist
	if err := s.flushLocked(); err != nil {
		// 结果写盘失败只影响历史展示，不影响本次签到本身；内存里仍保留结果。
		log.Printf("[checkin] 写历史失败（内存已更新）：%v", err)
	}
}

// setAutoDay 记录自动签到最后执行日（失败也记：避免每 20 秒重试轰炸站点）。
func (s *Store) setAutoDay(day string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.LastAutoDay = day
	if err := s.flushLocked(); err != nil {
		log.Printf("[checkin] 记录自动签到日期失败：%v", err)
	}
}

// flushLocked 原子写盘：同目录 .tmp + rename。调用方必须持写锁。
// 文件权限 0600：里面是 Cookie/Token，不能给同机其它应用读。
func (s *Store) flushLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// nextRunAt 计算下一次自动签到的时刻（本地时区）。
// settings.Enabled 为 false 时仍返回下一个时点，面板可以显示「已暂停 · 若启用将于 …」。
func nextRunAt(st Settings, lastAutoDay string, now time.Time) time.Time {
	h, m, err := parseClock(st.RunAt)
	if err != nil {
		h, m = 8, 5
	}
	loc := now.Location()
	at := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)
	// 今天该时点已过（或今天已经自动跑过）→ 顺延到明天。用 lastAutoDay 判断而不是
	// 只看时间：用户在 08:06 手动改设置后，面板应该显示明天 08:05 而不是「刚才」。
	if !at.After(now) || lastAutoDay == dayKey(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// dayKey 本地自然日 "YYYY-MM-DD"。
func dayKey(t time.Time) string { return t.Format("2006-01-02") }
