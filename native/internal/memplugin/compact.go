package memplugin

// 压缩前强制整理（规格-记忆-v2 13.2.7，W7）：内核要调模型压缩一个会话之前，先问记忆"整理到哪儿了"，切点不越过它。
// 钩子里一批接一批跑 SummarizeBatch(force)，到内核的等待时限（WaitUntil）前留一点余量就交回已经推到的游标；
// 整理本身在自己的协程里，用插件的生命周期：钩子交回以后它照常跑完、写库（原文在 events 里不丢），只有插件停掉才丢结果。

import (
	"context"
	"errors"
	"sync"
	"time"

	"sumika-native/internal/memory"
	"sumika-native/internal/turn"
)

// compactMargin：离 WaitUntil 还有这么久就不等了，交回现有的游标（内核到点不等晚到的返回值，留一点余量好让交回的游标算数）
const compactMargin = 2 * time.Second

// compactJob 是一次钩子调用和它的整理协程之间的交接。cursor 是已经推到的游标；finished 以后 upTo / limited 是最终的交回值
type compactJob struct {
	mu       sync.Mutex
	cursor   int64
	finished bool
	upTo     int64
	limited  bool
	done     chan struct{}
}

func (j *compactJob) advance(cursor int64) {
	j.mu.Lock()
	j.cursor = max(j.cursor, cursor)
	j.mu.Unlock()
}

func (j *compactJob) finish(upTo int64, limited bool) {
	j.mu.Lock()
	j.finished, j.upTo, j.limited = true, upTo, limited
	j.mu.Unlock()
	close(j.done)
}

// partial：到点了整理还没完，交回已经推到的游标
func (j *compactJob) partial() (int64, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished {
		return j.upTo, j.limited
	}
	return j.cursor, true
}

// beforeCompact 是 OnBeforeCompact 的钩子。没开写入、没有记忆、没有游标：什么都不限制
func (l *life) beforeCompact(ctx context.Context, e turn.BeforeCompact) (int64, bool, error) {
	if !l.enter() {
		return 0, false, nil
	}
	s := l.s
	k := skey{e.CharacterID, e.Conversation}
	m := s.memoryFor(k.char, k.conv)
	if m == nil || !s.ensureCursor(m, k) {
		l.wg.Done()
		return 0, false, nil
	}
	st, ok, err := m.SummaryState(l.ctx, k.conv)
	if err != nil || !ok {
		l.wg.Done()
		if err != nil && l.ctx.Err() == nil {
			s.opt().Logf("记忆[%s]：压缩前读不到游标（原因 %s），这次不限制切点", k.char, memory.FailureCode(err))
		}
		return 0, false, nil
	}
	var deadline time.Time
	var timeout <-chan time.Time
	if !e.WaitUntil.IsZero() {
		deadline = e.WaitUntil.Add(-compactMargin)
		t := time.NewTimer(time.Until(deadline))
		defer t.Stop()
		timeout = t.C
	}
	j := &compactJob{cursor: st.LastSeq, done: make(chan struct{})}
	s.mu.Lock()
	sl := s.slotLocked(k)
	s.mu.Unlock()
	go func() {
		defer l.wg.Done()
		s.compactWork(sl, m, j, deadline)
	}()
	select {
	case <-j.done:
	case <-timeout:
		s.opt().Logf("记忆[%s]：压缩前的整理到点还没做完，交回已经整理到的位置，剩下的在后台接着做（compact_hook_timeout）", k.char)
	case <-ctx.Done():
		return 0, false, nil
	case <-l.ctx.Done():
		return 0, false, nil
	}
	upTo, limited := j.partial()
	return upTo, limited, nil
}

// compactWork 在整理协程里一批接一批跑：同一个会话和排程的整理串行（slot.run）；到了 deadline 不再开新的一批。
// 最后一批的结局交给排程的 settled：后面还有的接着排，等识图的到点再来
func (s *sched) compactWork(sl *slot, m *memory.Memory, j *compactJob, deadline time.Time) {
	l := s.l
	sl.run.Lock()
	defer sl.run.Unlock()
	var res memory.BatchResult
	prev := j.cursor
	for {
		var err error
		res, err = m.SummarizeBatch(l.ctx, sl.k.conv, true)
		if err != nil {
			if l.ctx.Err() == nil && !errors.Is(err, memory.ErrClosed) {
				s.failed(sl, m, err)
			}
			j.finish(0, false)
			return
		}
		progressed := res.Cursor > prev
		if progressed {
			prev = res.Cursor
			j.advance(res.Cursor)
		}
		more := res.More && progressed && (res.Skipped == "" || res.Skipped == memory.SkipDone || res.Skipped == memory.SkipNothing)
		if !more || !deadline.IsZero() && !time.Now().Before(deadline) {
			break
		}
	}
	upTo, limited := int64(0), false
	switch res.Skipped {
	case "", memory.SkipDone, memory.SkipCursorMoved, memory.SkipWaitImages, memory.SkipNothing:
		// 重读游标：别的批可能挪过它
		if st, ok, err := m.SummaryState(l.ctx, sl.k.conv); err == nil && ok {
			upTo, limited = st.LastSeq, true
		}
	}
	if l.ctx.Err() != nil {
		upTo, limited = 0, false
	}
	j.finish(upTo, limited)
	s.settled(sl, m, res, false)
	s.mu.Lock()
	s.kickLocked(sl)
	s.mu.Unlock()
}
