package checkin

import (
	"context"
	"log"
	"time"
)

// autoCheckInterval 自动签到循环的检查周期。20 秒足够准时（最多晚 20 秒），
// 又不会为「等到点」空转。
const autoCheckInterval = 20 * time.Second

// lateGrace 签到时点过去多久内仍算「今天该签没签」。App 在后台被冻结、
// 手机重启后，循环醒来时如果还在这个窗口里就补跑一轮；超窗则今天不再补
// （中午才解锁手机的人不会希望 App 一睁眼就给所有站点发请求）。
const lateGrace = 6 * time.Hour

// Start 每日自动签到循环：ctx 取消即停。阻塞调用，请放 goroutine 里。
//
// 失败也记 last_auto_day：自动签到不是「重试到成功为止」的语义——站点挂了
// 应该让人在面板上看到红卡，而不是后台每 20 秒重试一次把站点打进风控。
func (s *Service) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(autoCheckInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				s.maybeRunAuto(now)
			}
		}
	}()
}

// maybeRunAuto 判断并执行一轮自动签到。
func (s *Service) maybeRunAuto(now time.Time) {
	st, _, lastDay := s.store.snapshot()
	if !st.Enabled {
		return
	}
	today := dayKey(now)
	if lastDay == today {
		return
	}
	h, m, err := parseClock(st.RunAt)
	if err != nil {
		return
	}
	due := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if now.Before(due) {
		return // 还没到点
	}
	if now.Sub(due) > lateGrace {
		return // 错过太多，今天不补（明天照常）
	}
	s.store.setAutoDay(today)
	log.Printf("[checkin] 自动签到开始（每日 %s）", st.RunAt)
	results, _, err := s.RunAll(context.Background())
	if err != nil {
		if err == ErrBusy {
			return // 手动全量正在跑：今天的手动轮次覆盖了自动任务，日期已记，不再补
		}
		log.Printf("[checkin] 自动签到异常：%v", err)
		return
	}
	okCnt := 0
	for _, r := range results {
		if r.OK {
			okCnt++
		}
	}
	log.Printf("[checkin] 自动签到完成：%d/%d 成功", okCnt, len(results))
}
