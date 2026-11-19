package store

import (
	"context"
	"database/sql"
)

// ReadDisplaySince 按新增水位追到固定上界；补录的旧时间不影响追赶。
func (s *Store) ReadDisplaySince(ctx context.Context, scope DisplayScope, after, upper int64) (DisplayPage, error) {
	tx, err := s.Events.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DisplayPage{}, err
	}
	defer tx.Rollback()
	if err = validateDisplay(ctx, tx, scope); err != nil {
		return DisplayPage{}, err
	}
	filter, args := displayFilter(scope, "e")
	args = append([]any{scope.Conversation, after, upper}, args...)
	rows, err := tx.QueryContext(ctx, `SELECT `+displayColumns+` FROM events e WHERE e.conversation=? AND e.seq>? AND e.seq<=? AND `+filter+` ORDER BY e.seq LIMIT 200`, args...)
	if err != nil {
		return DisplayPage{}, err
	}
	items, err := scanDisplay(rows)
	rows.Close()
	if err != nil {
		return DisplayPage{}, err
	}
	page := DisplayPage{Items: items, HighWater: upper}
	if len(items) == 200 {
		page.HighWater = items[len(items)-1].Seq
		page.HasMore = page.HighWater < upper
	}
	if err = enrichDisplay(ctx, tx, scope, items); err != nil {
		return DisplayPage{}, err
	}
	return page, tx.Commit()
}

// ReadDisplayLoaded 重查已加载区间；补观察可能让原先不可见的旧身份进入区间。
func (s *Store) ReadDisplayLoaded(ctx context.Context, scope DisplayScope, ids []string) ([]DisplayRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tx, err := s.Events.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = validateDisplay(ctx, tx, scope); err != nil {
		return nil, err
	}
	var oldestAt, oldestSeq int64
	found := false
	for start := 0; start < len(ids); start += 200 {
		end := min(start+200, len(ids))
		marks := ""
		args := []any{scope.Conversation}
		for _, id := range ids[start:end] {
			if marks != "" {
				marks += ","
			}
			marks += "?"
			args = append(args, id)
		}
		var at, seq int64
		err := tx.QueryRowContext(ctx, `SELECT created_at,seq FROM events WHERE conversation=? AND id IN (`+marks+`) ORDER BY created_at,seq LIMIT 1`, args...).Scan(&at, &seq)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !found || at < oldestAt || (at == oldestAt && seq < oldestSeq) {
			oldestAt, oldestSeq, found = at, seq, true
		}
	}
	if !found {
		return nil, tx.Commit()
	}
	filter, args := displayFilter(scope, "e")
	args = append([]any{scope.Conversation, oldestAt, oldestAt, oldestSeq}, args...)
	rows, err := tx.QueryContext(ctx, `SELECT `+displayColumns+` FROM events e WHERE e.conversation=? AND (e.created_at>? OR (e.created_at=? AND e.seq>=?)) AND `+filter+` ORDER BY e.created_at,e.seq`, args...)
	if err != nil {
		return nil, err
	}
	out, err := scanDisplay(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(out); start += 200 {
		if err = enrichDisplay(ctx, tx, scope, out[start:min(start+200, len(out))]); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

// ReadDisplayUnread 不建立已读行；缺行时由服务保留本进程基线。
// 数的是 read_at 之后的真实条数，不封顶（主人 2026-11-02："实际多少就是多少"；原来数到 100 为止，界面显示 99+）。
// 走 ix_events_conv_time 范围扫描，每行再查一次 ix_obs_seq，开销跟这个会话的未读条数成正比；量过的数见 docs/archive/notes/量测-未读不封顶-2026-11-02.md。
func (s *Store) ReadDisplayUnread(ctx context.Context, scope DisplayScope, baseline *int64) (int, int64, error) {
	tx, err := s.Events.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	if err = validateDisplay(ctx, tx, scope); err != nil {
		return 0, 0, err
	}
	var at int64
	err = tx.QueryRowContext(ctx, `SELECT read_at FROM qq_read WHERE conversation=?`, scope.Conversation).Scan(&at)
	filter, args := displayFilter(scope, "e")
	if err == sql.ErrNoRows {
		if baseline != nil {
			at = *baseline
		} else {
			if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(e.created_at),0) FROM events e WHERE e.conversation=? AND `+filter, append([]any{scope.Conversation}, args...)...).Scan(&at); err != nil {
				return 0, 0, err
			}
		}
	} else if err != nil {
		return 0, 0, err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events e WHERE e.conversation=? AND e.created_at>? AND e.kind='message' AND e.actor_kind!='character' AND `+filter, append([]any{scope.Conversation, at}, args...)...).Scan(&count)
	if err != nil {
		return 0, 0, err
	}
	return count, at, tx.Commit()
}

// ReadDisplayEvent 按 ID 读一条可见事件（带引用目标），媒体凭据校验用；
// 不可见或不在这个会话里时 ok=false，不报原文。
func (s *Store) ReadDisplayEvent(ctx context.Context, scope DisplayScope, id string) (DisplayRow, bool, error) {
	tx, err := s.Events.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DisplayRow{}, false, err
	}
	defer tx.Rollback()
	if err = validateDisplay(ctx, tx, scope); err != nil {
		return DisplayRow{}, false, err
	}
	filter, args := displayFilter(scope, "e")
	args = append([]any{scope.Conversation, id}, args...)
	rows, err := tx.QueryContext(ctx, `SELECT `+displayColumns+` FROM events e WHERE e.conversation=? AND e.id=? AND `+filter, args...)
	if err != nil {
		return DisplayRow{}, false, err
	}
	out, err := scanDisplay(rows)
	rows.Close()
	if err != nil || len(out) == 0 {
		return DisplayRow{}, false, err
	}
	if err = enrichDisplay(ctx, tx, scope, out); err != nil {
		return DisplayRow{}, false, err
	}
	return out[0], true, tx.Commit()
}
