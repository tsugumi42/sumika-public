package app

// 主动联系的导演接进后台（规格-主动联系 第十五节第 1 步）：
//   - 只在配置里 proactive.enabled 且列了角色时建；默认关，关着不跑、不记日志；
//   - 导演随后台起停：Run 里起（用 autoCtx，关机最先取消），写 trace.db 的 proactive_state、proactive_decisions；
//   - 第 2 步起到点叫模型：Waker 是话题线的宿主（proactivethread.go），线里的工具在 proactivetools.go，本地私聊认领在 claimLocal；
//     proactive.send 为 think 时走完整流程但不发；
//   - 主人活跃信号取各平台库里主人最后一次发言，窗口前台取 Options.WindowForeground；上次接触、今天次数、开着的线从线表取。

import (
	"context"
	"math/rand/v2"
	"sumika-native/internal/applog"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/proactive"
)

func (a *App) openProactive() {
	p := a.cfg.Proactive
	if !p.Enabled || len(p.Characters) == 0 {
		return
	}
	mgr := proactive.NewThreads(a.set.Trace(), func() config.Proactive { return a.cfg.Proactive }, a.now)
	host := newProactiveHost(a, mgr)
	d, err := proactive.New(proactive.Options{
		Store:        proactive.DBStore{DB: a.set.Trace()},
		Config:       func() config.Proactive { return a.cfg.Proactive },
		Signals:      a.proactiveSignals,
		Waker:        host,
		Housekeeping: host.sweep,
		Now:          a.now,
		Rand:         rand.Float64,
		Logf:         a.logfFor(applog.Proactive),
		Warnf:        a.warnFor(applog.Proactive),
		Errorf:       a.errorFor(applog.Proactive),
	})
	if err != nil {
		a.logf("主动联系没起来：%v", err)
		return
	}
	host.dir = d
	// 上个进程没收尾的线：发送中的记 unknown、想着的回到等回复或关成 error、等着的认领丢掉（规格第四节）
	if rep, err := mgr.Recover(); err != nil {
		a.logf("主动联系：恢复上个进程留下的线失败：%v", err)
	} else {
		host.recovered = rep
		if rep.Unknown+rep.Lost+len(rep.Threads) > 0 {
			a.logf("主动联系：恢复了上个进程留下的线（发送中记成不明 %d 条、线不再接的认领 %d 条、状态有变的线 %d 条）", rep.Unknown, rep.Lost, len(rep.Threads))
		}
	}
	if err := a.hub.Use(proactiveTools{h: host}); err != nil {
		a.logf("主动联系的线上工具没装上，不开主动联系：%v", err)
		return
	}
	a.proactive, a.pthreads = d, host
	a.logf("主动联系已打开（send=%s）：角色 %v", p.Send, p.Characters)
}

// runProactive 在 Run 里起导演；返回时已经交给 autoWG。
func (a *App) runProactive() {
	if a.proactive == nil {
		return
	}
	if a.pthreads != nil {
		a.pthreads.afterRecover()
	}
	a.autoWG.Add(1)
	go func() {
		defer a.autoWG.Done()
		a.proactive.Run(a.autoCtx)
	}()
}

// proactiveSignals 取一个角色这一刻的信号。第 1 步没有线，上次接触、今天次数都是空；
// 主人活跃只查 presenceWindowHours 以内（更早的当"不知道"），每个平台库一句带时间下界的查询。
// 本地库那一条同时当作"主人在本地窗口最后一次输入"（没有更细的输入事件可用）
func (a *App) proactiveSignals(ctx context.Context, char string, now time.Time) (proactive.Snapshot, error) {
	var sg proactive.Snapshot
	if h := a.pthreads; h != nil {
		if err := h.threadSignals(char, now, &sg); err != nil {
			return sg, err
		}
	}
	since := now.Add(-time.Duration(a.cfg.Proactive.PresenceWindowHours) * time.Hour).UnixMilli()
	for _, n := range a.set.Each() {
		if err := ctx.Err(); err != nil {
			return sg, err
		}
		at, ok, err := n.Store.OwnerLastSpoke(since)
		if err != nil {
			return sg, err
		}
		if !ok {
			continue
		}
		t := time.UnixMilli(at)
		if t.After(sg.OwnerLastActive) {
			sg.OwnerLastActive = t
		}
		if n.Platform == "local" && t.After(sg.LocalLastInput) {
			sg.LocalLastInput = t
		}
	}
	if f := a.opt.WindowForeground; f != nil {
		sg.WindowForeground = f()
	}
	return sg, nil
}

// threadSignals 把线表里的信息填进导演的信号：上次接触（最近一条真发过话的线最后一次发话）、上一条线的热度、
// 今天开线并真发过话几次（本机时区的"今天"）、有没有开着的线、开着的线到没到续醒时间
func (h *proactiveHost) threadSignals(char string, now time.Time, sg *proactive.Snapshot) error {
	p := h.cfg()
	if last, ok, err := h.mgr.LastContacted(char); err != nil {
		return err
	} else if ok {
		sg.LastContact = last.LastSendAt
		if !last.Open() {
			_, sg.LastHeat = proactive.HeatOf(last)
		}
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	n, err := h.mgr.SentToday(char, dayStart)
	if err != nil {
		return err
	}
	sg.ThreadsToday = n
	th, ok, err := h.mgr.OpenOf(char)
	if err != nil {
		return err
	}
	if ok {
		sg.OpenThread = true
		sg.FollowUpDue = proactive.FollowUpDue(p, th, now)
	}
	return nil
}
