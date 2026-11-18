package store

import (
	"context"
	"database/sql"
	"errors"
)

// QQContinuity 只描述 [FromMS, ThroughMS] 的连续证明；ThroughMS < FromMS 表示尚未证明任何消息。
type QQContinuity struct{ FromMS, ThroughMS int64 }

func (s *Store) QQContinuousRange(ctx context.Context, account, conversation string) (QQContinuity, error) {
	var c QQContinuity
	err := s.Events.QueryRowContext(ctx, `SELECT from_ms,through_ms FROM qq_backfill_continuity WHERE account=? AND conversation=?`, account, conversation).Scan(&c.FromMS, &c.ThroughMS)
	return c, err
}

// QQBackfillResult 的边界仅使用已提交的消息证明；检查位置不能填入 CoveredToMS。
type QQBackfillResult struct {
	Conversation               string
	FromMS                     int64
	Covered                    bool
	CoveredFromMS, CoveredToMS int64
	Updates, Gaps              []ChannelGap
}

// SettleQQBackfill 把整轮缺口、连续边界和账号检查位置一起提交；失败时重连仍读旧检查位置。
func (s *Store) SettleQQBackfill(ctx context.Context, account string, checkedMS int64, results []QQBackfillResult) error {
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := range results {
		r := &results[i]
		for _, g := range r.Updates {
			if err := updateAutomaticGap(ctx, tx, g); err != nil {
				return err
			}
		}
		for _, g := range r.Gaps {
			if err := recordChannelGap(ctx, tx, g); err != nil {
				return err
			}
		}
		var c QQContinuity
		err := tx.QueryRowContext(ctx, `SELECT from_ms,through_ms FROM qq_backfill_continuity WHERE account=? AND conversation=?`, account, r.Conversation).Scan(&c.FromMS, &c.ThroughMS)
		if errors.Is(err, sql.ErrNoRows) {
			c = QQContinuity{r.FromMS, r.FromMS - 1}
		} else if err != nil {
			return err
		}
		var pending sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT min(from_ms) FROM channel_gaps WHERE channel='qq' AND conversation=? AND state IN ('open','blocked','abandoned')`, r.Conversation).Scan(&pending); err != nil {
			return err
		}
		if pending.Valid && pending.Int64 < c.FromMS {
			// 新发现更早的洞时，旧证明没有覆盖这个新起点，不能把两段拼成连续。
			c.FromMS, c.ThroughMS = pending.Int64, pending.Int64-1
		}
		if r.Covered && r.CoveredFromMS <= c.ThroughMS+1 && r.CoveredToMS >= c.FromMS {
			c.ThroughMS = max(c.ThroughMS, r.CoveredToMS)
		}
		if pending.Valid {
			c.ThroughMS = min(c.ThroughMS, pending.Int64-1)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO qq_backfill_continuity(account,conversation,from_ms,through_ms) VALUES(?,?,?,?) ON CONFLICT(account,conversation) DO UPDATE SET from_ms=excluded.from_ms,through_ms=excluded.through_ms`, account, r.Conversation, c.FromMS, c.ThroughMS); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO qq_sync(account,through) VALUES(?,?) ON CONFLICT(account) DO UPDATE SET through=max(qq_sync.through,excluded.through)`, account, checkedMS); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for i := range results {
		s.changed(results[i].Conversation)
	}
	return nil
}
