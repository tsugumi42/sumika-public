package backup

import (
	"context"
	"errors"
	"time"
)

// 调度（规格-数据目录与备份 10.3“时机”）：启动 2 分钟后第一次看，之后每小时看一次；
// 最近 20 小时没有校验通过的每天备份、并且没有角色正在回复，就做一份。判断依据是备份目录里的清单，不是进程内的计时，所以不跟着每次重启都做。
const (
	DefaultStartDelay = 2 * time.Minute
	DefaultEvery      = time.Hour
	// FreshFor：这么久以内有校验通过的每天备份就不再做
	FreshFor = 20 * time.Hour
	// BusyCap：上一份好的每天备份已经超过这么久，就不再等“没有角色正在回复”（长时间的目标模式、定期任务可能一直有回合在跑，
	// 不能饿死备份；快照用独立连接，不挡回复）
	BusyCap = 36 * time.Hour
	// RetryAfter：每天备份失败以后至少隔这么久才再试（盘满、库打不开时每小时重试一次只会堆东西、刷日志）
	RetryAfter = 6 * time.Hour
)

// Due 是纯判断：现在要不要做一份每天备份。haveGood 为假（一份校验通过的都没有）时不等“没有角色正在回复”：什么备份都没有是最危险的状态。
// lastGood 是时间不晚于 now 的最新一份；时钟往回拨过时 Scan 出来的“未来的备份”不参与，所以这里不会因为它们以为自己刚做过。
func Due(now, lastGood time.Time, haveGood, busy bool) bool {
	if !haveGood {
		return true
	}
	age := now.Sub(lastGood)
	if age < FreshFor {
		return false
	}
	if busy && age < BusyCap {
		return false
	}
	return true
}

// refresh 扫一遍备份目录，更新“最近一份校验通过的每天备份”。目录不存在、读不了都当没有
func (s *Service) refresh() {
	now := s.opt.Now()
	var good time.Time
	have, haveAny := false, false // have：有校验通过的每天备份；haveAny：有可以抽验的备份（任何原因）
	skipped := 0
	if dir, err := s.Dir(); err == nil {
		s.loadDrills(dir)
		if entries, err := Scan(dir); err == nil {
			for _, e := range s.ours(entries) { // 新的在前
				m := e.Manifest
				if e.Partial || m.Status != StatusOK || m.Time.After(now) {
					continue
				}
				haveAny = true
				if m.Reason != ReasonDaily || have {
					continue
				}
				good, have, skipped = m.Time, true, m.SkippedMedia
			}
		}
	}
	s.mu.Lock()
	s.state.Drill.HaveBackup = haveAny
	s.state.Checked = true
	s.state.HaveGood, s.state.LastGood, s.state.LastSkipped = have, good, skipped
	s.mu.Unlock()
}

// Tick 看一眼要不要做每天备份，要就做一份并等它做完。测试直接调它，不起定时器、不真等。返回有没有做
func (s *Service) Tick(ctx context.Context) (ran bool, err error) {
	ran, err = s.tickDaily(ctx)
	s.tickDrill(ctx, ran) // 每周抽验排在每天备份后面；这一次刚做过每天备份就先跳过，错开
	return ran, err
}

func (s *Service) tickDaily(ctx context.Context) (ran bool, err error) {
	s.refresh()
	if !s.opt.Daily {
		return false, nil
	}
	st := s.Status()
	now := s.opt.Now()
	if st.LastFailure != "" && !st.FailedAt.IsZero() && now.Sub(st.FailedAt) >= 0 && now.Sub(st.FailedAt) < RetryAfter {
		return false, nil // 刚失败过，退避：不每小时重试
	}
	busy := s.opt.Busy != nil && s.opt.Busy()
	due := Due(now, st.LastGood, st.HaveGood, busy)
	s.mu.Lock()
	s.state.Deferred = busy && !due && Due(now, st.LastGood, st.HaveGood, false)
	s.mu.Unlock()
	if !due {
		return false, nil
	}
	s.opt.Logf("每天备份：开始（媒体档 %s）", s.opt.Media)
	res, err := s.Run(ctx, Request{Reason: ReasonDaily})
	switch {
	case errors.Is(err, ErrBusy):
		s.opt.Logf("每天备份：已经有备份、恢复或抽验在做，这次不重复做，下个小时再看")
		return false, nil
	case err == nil:
		s.opt.Logf("每天备份：做完 %s（%d 个文件，%d 个和上一份共用，%.1f MB）", res.Name, res.Files, res.Linked, float64(res.Bytes)/(1<<20))
	case ctx.Err() != nil:
		s.opt.Logf("每天备份：被打断，没做完的已经删掉")
	default:
		s.opt.Warnf("每天备份：失败，下次再试：%v", err)
	}
	return true, err
}

// Start 开调度：关着每日备份就只记一行日志，不起协程
func (s *Service) Start() {
	if !s.opt.Daily {
		s.opt.Logf("每天自动备份已关闭（backup.daily: false）；sumika-ctl backup now 仍可手动备份")
		s.refresh()
		return
	}
	s.life.Lock()
	defer s.life.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	done := make(chan struct{})
	s.done = done
	delay, every := s.opt.StartDelay, s.opt.Every
	if delay == 0 {
		delay = DefaultStartDelay
	}
	if every <= 0 {
		every = DefaultEvery
	}
	go func() {
		defer close(done)
		wait := func(d time.Duration) bool {
			if d <= 0 {
				return ctx.Err() == nil
			}
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
				return true
			}
		}
		if !wait(delay) {
			return
		}
		for {
			s.safeTick(ctx)
			if !wait(every) {
				return
			}
		}
	}()
}

// safeTick：一次检查里出了意外（panic）只记日志，不带垮调度循环和整个后台
func (s *Service) safeTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.opt.Warnf("每天备份：检查时出了意外：%v", r)
		}
	}()
	_, _ = s.Tick(ctx)
}

// Stop 停调度并取消正在做的那份（没做完的整个删掉），等到收完尾
func (s *Service) Stop() { s.stopWithin(s.opt.CloseWait) }

// stopWithin 停调度并取消正在做的那份，最多等 wait：调度驱动的备份卡在一次不看 ctx 的读上时，等它的 goroutine 退出也不能无限等
// （真卡在一次读上取消不了，只剩自检 S9 的“做太久”报警（runMaxHours + 1 小时，最多 26 小时）；规格 10.3）
func (s *Service) stopWithin(wait time.Duration) {
	s.life.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	rc := s.runCtx
	s.life.Unlock()
	if cancel != nil {
		cancel()
	}
	if rc != nil {
		rc()
	}
	if done != nil {
		if wait < 0 {
			wait = 0
		}
		if s.onWait != nil {
			s.onWait(wait)
		}
		select {
		case <-done:
		case <-time.After(wait):
			s.opt.Warnf("每天备份的调度没能在 %v 内停下（可能卡在读不动的文件上），不再等它", s.opt.CloseWait)
		}
	}
}
