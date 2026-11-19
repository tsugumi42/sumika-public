package store

import (
	"context"
	"database/sql"
	"errors"
)

// ChannelGap 是未覆盖或未证实的闭区间；放弃不代表填平，不能拿最新实时消息替代它。
type ChannelGap struct {
	ID                                            int64
	Channel, Conversation                         string
	FromMS, ToMS                                  int64
	Reason, State                                 string
	Tries                                         int
	LastTryMS, LastManualMS, CreatedMS, UpdatedMS int64
	AcknowledgedMS                                int64
}

// QQCheckedThrough 复用导入的 qq_sync，仅表示检查位置；它不是连续覆盖证明。
func (s *Store) QQCheckedThrough(ctx context.Context, account string, fallback int64) (int64, error) {
	var at int64
	err := s.Events.QueryRowContext(ctx, `SELECT through FROM qq_sync WHERE account=?`, account).Scan(&at)
	if !errors.Is(err, sql.ErrNoRows) {
		return at, err
	}
	err = s.Events.QueryRowContext(ctx, `SELECT coalesce(min(created_at), ?) FROM events WHERE channel='qq' AND
 (conversation LIKE ? OR conversation LIKE 'qq:group:%')`, fallback, "qq:private:"+account+":%").Scan(&at)
	return at, err
}

func (s *Store) SetQQCheckedThrough(ctx context.Context, account string, at int64) error {
	_, err := s.Events.ExecContext(ctx, `INSERT INTO qq_sync(account,through) VALUES(?,?)
 ON CONFLICT(account) DO UPDATE SET through=max(qq_sync.through,excluded.through)`, account, at)
	return err
}

// QQActivity 按平台时间取会话活动顺序；不能按 seq 排，后补的老消息 seq 会更大。
type QQActivity struct {
	Conversation string
	LastMS       int64
}

func (s *Store) QQActivities(ctx context.Context, account string) ([]QQActivity, error) {
	rows, err := s.Events.QueryContext(ctx, `SELECT conversation,max(created_at) FROM events WHERE channel='qq'
 AND (conversation LIKE ? OR conversation LIKE 'qq:group:%') GROUP BY conversation ORDER BY max(created_at) DESC,conversation`, "qq:private:"+account+":%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QQActivity
	for rows.Next() {
		var a QQActivity
		if err := rows.Scan(&a.Conversation, &a.LastMS); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) ChannelGaps(ctx context.Context, channel string) ([]ChannelGap, error) {
	columns, err := gapReadColumns(ctx, s.Events)
	if err != nil {
		return nil, err
	}
	rows, err := s.Events.QueryContext(ctx, `SELECT `+columns+` FROM channel_gaps WHERE channel=? ORDER BY from_ms,id`, channel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChannelGap
	for rows.Next() {
		g, err := scanGap(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RecordChannelGap 只合并尚待自动扫描的区间；已受阻旧段保留身份和范围。
func (s *Store) RecordChannelGap(ctx context.Context, g ChannelGap) error {
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recordChannelGap(ctx, tx, g); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed(g.Conversation)
	return nil
}

func recordChannelGap(ctx context.Context, tx *sql.Tx, g ChannelGap) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,from_ms,to_ms,tries,last_try_ms,last_manual_ms,created_ms FROM channel_gaps WHERE channel=? AND conversation=? AND (state='open' OR (state='blocked' AND last_try_ms=0 AND acknowledged_ms=0)) ORDER BY from_ms`, g.Channel, g.Conversation)
	if err != nil {
		return err
	}
	var open []ChannelGap
	for rows.Next() {
		var old ChannelGap
		if err := rows.Scan(&old.ID, &old.FromMS, &old.ToMS, &old.Tries, &old.LastTryMS, &old.LastManualMS, &old.CreatedMS); err != nil {
			rows.Close()
			return err
		}
		open = append(open, old)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if g.State == "" {
		g.State = "open"
	}
	g.CreatedMS = g.LastTryMS
	var ids []int64
	for changed := true; changed; {
		changed = false
		for i := range open {
			old := &open[i]
			if old.ID == 0 || old.ToMS+1 < g.FromMS || old.FromMS > g.ToMS+1 {
				continue
			}
			g.FromMS, g.ToMS = min(g.FromMS, old.FromMS), max(g.ToMS, old.ToMS)
			g.Tries = max(g.Tries, old.Tries)
			g.LastTryMS = max(g.LastTryMS, old.LastTryMS)
			g.LastManualMS = max(g.LastManualMS, old.LastManualMS)
			g.CreatedMS = min(g.CreatedMS, old.CreatedMS)
			ids = append(ids, old.ID)
			if g.ID == 0 || old.ID < g.ID {
				g.ID = old.ID
			}
			old.ID = 0
			changed = true
		}
	}
	// 合并若撞上已有终局区间，保持参与行原样；不能先删再靠局部回滚救回。
	var terminal bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM channel_gaps WHERE channel=? AND conversation=? AND from_ms=? AND to_ms=? AND (state IN ('filled','abandoned') OR (state='blocked' AND (last_try_ms>0 OR acknowledged_ms>0))))`, g.Channel, g.Conversation, g.FromMS, g.ToMS).Scan(&terminal); err != nil {
		return err
	}
	if terminal {
		return nil
	}
	// 先移除参与合并的行，再写回最早的 id；任一步失败整个事务回滚。
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM channel_gaps WHERE id=?`, id); err != nil {
			return err
		}
	}
	var id any
	if g.ID != 0 {
		id = g.ID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO channel_gaps(id,channel,conversation,from_ms,to_ms,reason,state,tries,last_try_ms,last_manual_ms,created_ms,updated_ms)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, g.Channel, g.Conversation, g.FromMS, g.ToMS, g.Reason, g.State, g.Tries, g.LastTryMS, g.LastManualMS, g.CreatedMS, g.LastTryMS)
	return err
}

// ErrChannelGapSettled 说这行缺口已经是终局（填平或放弃）或不存在，不能再改。
var ErrChannelGapSettled = errors.New("缺口已经结算或不存在，不再改动")

// UpdateChannelGap 改一行还没结算（open、blocked）的缺口。已填平、已放弃的行和不存在的 id 不动、返回 ErrChannelGapSettled：
// 拿旧副本写回会把已经结算的缺口改回去，让补录重查一遍已确认的区间。
// 正式代码的结算走 updateAutomaticGap；目前只有测试拿它预置缺口状态。
func (s *Store) UpdateChannelGap(ctx context.Context, g ChannelGap) error {
	res, err := s.Events.ExecContext(ctx, `UPDATE channel_gaps SET reason=?,state=?,tries=?,last_try_ms=?,updated_ms=? WHERE id=? AND state IN ('open','blocked')`, g.Reason, g.State, g.Tries, g.LastTryMS, g.UpdatedMS, g.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrChannelGapSettled
	}
	s.changed(g.Conversation)
	return nil
}
