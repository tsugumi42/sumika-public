package memory

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 后台整理的游标和退避（规格-记忆-v2 13.2.5，W5）。游标、失败次数、下次时间都在 summary_state 同一行里（B3，Go 版落库）：
// 重启不清零，到期了自动再试，不等新消息。什么时候排整理、几个会话同时跑，是记忆插件的事（internal/memplugin），这里只管库里的状态

// MaxFailures：连着失败这么多次就不再自动重试，要等下一条新通知或"整理"按钮才再开（6 次是我定的，起步值）
const MaxFailures = 6

// BackoffDelay：第 n 次连续失败以后等多久再试，2、4、8…分钟，最长 60（B3）
func BackoffDelay(failures int) time.Duration {
	if failures < 1 {
		return 0
	}
	if failures >= 6 { // 2^6 = 64 > 60；先挡住，免得位移溢出
		return time.Hour
	}
	return time.Duration(1<<failures) * time.Minute
}

// Writable：这个角色的记忆开了写入（Options.Writable）。没开的，整理、退避都不会动它
func (m *Memory) Writable() bool { return m.w != nil }

// SummarizeEnabled：后台整理开着（Options.SummarizeEnabled 且 Writable）。排程只对它为真的角色做整理
func (m *Memory) SummarizeEnabled() bool { return m.w != nil && m.summaryState.Load()&1 != 0 }

// AutoConfirming：待确认的事实攒够证据会真的提升（Options.AutoConfirm 且 Writable）。装配时 AutoConfirm 已经和整理开着挂过钩（memwrite 的 settings），
// 这里再要求 Writable，是页面据此告诉你"攒够就会自己提升"，说的要和写入侧实际做的一致
func (m *Memory) AutoConfirming() bool { return m.w != nil && m.summaryState.Load()&2 != 0 }

// SetSummaryEnabled 只发布已经通过 memwrite 前提的状态；不改变写入权限或连接。
func (m *Memory) SetSummaryEnabled(enabled, autoConfirm bool) {
	var bits uint32
	if enabled {
		bits |= 1
	}
	if autoConfirm {
		bits |= 2
	}
	m.summaryState.Store(bits)
}

// PrepareSummary 在排程仍关闭时完成 catchUp 和元数据，再由调用方打开整理。
func (m *Memory) PrepareSummary(ctx context.Context, latest bool, now int64) error {
	if latest {
		if _, err := m.catchUpToLatest(ctx, false); err != nil {
			return err
		}
	}
	_, err := m.SetMetaIfAbsent(ctx, MetaSummarizeEnabledAt, now)
	return err
}

// Conversations 列出事件来源里这个角色的会话和各自最新的 seq（启动扫描用）。没配事件来源返回 ErrNoEvents
func (m *Memory) Conversations(ctx context.Context) ([]ConvHead, error) {
	es, err := m.events()
	if err != nil {
		return nil, err
	}
	return es.Conversations(ctx)
}

// Pending 数这个会话游标之后还有几条要整理的：和 SummarizeBatch 第 1、2 步用同一个过滤（补录、自检、撤回、陌生人的不数），
// 最多数到一批（200 条）。计时用它判断攒够 40 条没有。没有游标行（新会话）ok 为假
func (m *Memory) Pending(ctx context.Context, conv string) (n int, ok bool, err error) {
	es, err := m.events()
	if err != nil {
		return 0, false, err
	}
	st, ok, err := m.SummaryState(ctx, conv)
	if err != nil || !ok {
		return 0, false, err
	}
	evs, err := es.Events(ctx, conv, st.LastSeq, maxBatchEvents)
	if err != nil {
		return 0, true, &reasoned{"读事件", "events_error", err}
	}
	for _, e := range evs {
		if e.Seq > st.LastSeq && dropReason(e) == "" {
			n++
		}
	}
	return n, true, nil
}

// NoteFailure 记一次整理失败：失败次数加一，下次时间 = 现在 + BackoffDelay；到了 MaxFailures 就不留下次时间（停了）。
// 和游标是同一行；拿角色库的写锁，和 remember、别的整理的写互斥。游标不动。没有这一行（没立游标的会话）什么都不写
func (m *Memory) NoteFailure(ctx context.Context, conv string) (SummaryState, error) {
	w, err := m.writer()
	if err != nil {
		return SummaryState{}, err
	}
	unlock, err := w.lock(ctx, 0)
	if err != nil {
		return SummaryState{}, err
	}
	defer unlock()
	var failures int
	err = w.db.QueryRowContext(ctx, `SELECT failures FROM summary_state WHERE conversation = ?`, conv).Scan(&failures)
	if errors.Is(err, sql.ErrNoRows) {
		return SummaryState{}, nil
	}
	if err != nil {
		return SummaryState{}, err
	}
	failures = min(failures+1, MaxFailures)
	var retry any // NULL = 没有下次（停了）
	if failures < MaxFailures {
		retry = w.now().Add(BackoffDelay(failures)).UnixMilli()
	}
	if _, err := w.db.ExecContext(ctx, `UPDATE summary_state SET failures = ?, retry_at = ? WHERE conversation = ?`, failures, retry, conv); err != nil {
		return SummaryState{}, err
	}
	st, _, err := m.SummaryState(ctx, conv)
	return st, err
}

// ReopenSummary：停了的会话（连着失败 MaxFailures 次）有了新通知，再给一次机会：失败次数退到差一次就又停，
// 下次时间清掉。这一次再失败就直接又停，不会因为新消息不断而每次都白白试满 6 次。没停的不动。返回有没有重新开
func (m *Memory) ReopenSummary(ctx context.Context, conv string) (bool, error) {
	w, err := m.writer()
	if err != nil {
		return false, err
	}
	unlock, err := w.lock(ctx, 0)
	if err != nil {
		return false, err
	}
	defer unlock()
	r, err := w.db.ExecContext(ctx, `UPDATE summary_state SET failures = ?, retry_at = NULL WHERE conversation = ? AND failures >= ?`, MaxFailures-1, conv, MaxFailures)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// FailureCode 把整理失败的错误归成一个原因码，日志只写它：错误本身可能带服务商的错误体、SQL 里的会话 id（B16、边界 ㉑）
func FailureCode(err error) string {
	var r *reasoned
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrBadOutput):
		return "bad_output"
	case errors.As(err, &r):
		return r.code
	case errors.Is(err, ErrBusy):
		return "busy"
	case errors.Is(err, ErrNoGenerator):
		return "no_generator"
	case errors.Is(err, ErrNoEvents):
		return "no_events"
	case errors.Is(err, ErrNotWritable):
		return "not_writable"
	case errors.Is(err, ErrSummarizeOff):
		return "summarize_off"
	}
	return "other"
}
