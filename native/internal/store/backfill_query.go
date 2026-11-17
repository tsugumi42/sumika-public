package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

const gapBaseColumns = `id,channel,conversation,from_ms,to_ms,reason,state,tries,last_try_ms,last_manual_ms,created_ms,updated_ms`
const gapColumns = gapBaseColumns + `,acknowledged_ms`

// archive 和迁移夹具不升级旧库，缺少的派生状态按未确认投影。
func gapReadColumns(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (string, error) {
	var manual, acknowledged int
	err := db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE name='last_manual_ms'),count(*) FILTER (WHERE name='acknowledged_ms') FROM pragma_table_info('channel_gaps')`).Scan(&manual, &acknowledged)
	if err != nil {
		return "", err
	}
	columns := gapBaseColumns
	if manual == 0 {
		columns = strings.Replace(columns, "last_manual_ms", "0", 1)
	}
	if acknowledged == 0 {
		return columns + ",0", nil
	}
	return columns + ",acknowledged_ms", nil
}

type GapWindow struct {
	Scope        DisplayScope
	FromMS, ToMS int64
	Cursor       string
	Limit        int
}
type GapPage struct {
	Items      []ChannelGap
	NextCursor string
	HasMore    bool
}
type gapCursor struct {
	Conversation, Character string
	FromMS, ToMS, At, ID    int64
}

func scanGap(row interface{ Scan(...any) error }) (g ChannelGap, err error) {
	err = row.Scan(&g.ID, &g.Channel, &g.Conversation, &g.FromMS, &g.ToMS, &g.Reason, &g.State, &g.Tries, &g.LastTryMS, &g.LastManualMS, &g.CreatedMS, &g.UpdatedMS, &g.AcknowledgedMS)
	return
}

// ReadGapWindow 与消息读取共用范围校验；Character 只缩小到观察过的会话，不借缺口制造观察关系。
func (s *Store) ReadGapWindow(ctx context.Context, q GapWindow) (GapPage, error) {
	if q.Limit < 1 || q.Limit > 200 || q.FromMS < 0 || q.ToMS < 0 || (q.FromMS > q.ToMS && q.ToMS != 0) || (q.FromMS == 0) != (q.ToMS == 0) {
		return GapPage{}, ErrDisplayParameter
	}
	var c gapCursor
	if q.Cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Conversation != q.Scope.Conversation || c.Character != q.Scope.Character || c.FromMS != q.FromMS || c.ToMS != q.ToMS || c.ID <= 0 {
			return GapPage{}, ErrDisplayParameter
		}
	}
	tx, err := s.Events.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GapPage{}, err
	}
	defer tx.Rollback()
	if err = validateDisplay(ctx, tx, q.Scope); err != nil {
		return GapPage{}, err
	}
	if q.Scope.Platform != "qq" {
		return GapPage{}, tx.Commit()
	}
	// archive 不迁移旧库；旧版本没有缺口或手动时间时，明确只投影已有数据。
	var has int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='channel_gaps'`).Scan(&has); err != nil {
		return GapPage{}, err
	}
	if has == 0 {
		return GapPage{}, tx.Commit()
	}
	columns, err := gapReadColumns(ctx, tx)
	if err != nil {
		return GapPage{}, err
	}
	query := `SELECT ` + columns + ` FROM channel_gaps WHERE channel='qq' AND conversation=? AND state IN ('open','blocked','abandoned')`
	args := []any{q.Scope.Conversation}
	if q.Scope.Character != "" {
		query += ` AND EXISTS (SELECT 1 FROM events e JOIN observations o ON o.event_seq=e.seq WHERE e.conversation=channel_gaps.conversation AND o.character_id=?)`
		args = append(args, q.Scope.Character)
	}
	if q.ToMS != 0 {
		query += ` AND from_ms<=? AND to_ms>=?`
		args = append(args, q.ToMS, q.FromMS)
	}
	if q.Cursor != "" {
		query += ` AND (from_ms,id)>(?,?)`
		args = append(args, c.At, c.ID)
	}
	query += ` ORDER BY from_ms,id LIMIT ?`
	args = append(args, q.Limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return GapPage{}, err
	}
	out := GapPage{Items: []ChannelGap{}}
	for rows.Next() {
		g, e := scanGap(rows)
		if e != nil {
			rows.Close()
			return GapPage{}, e
		}
		out.Items = append(out.Items, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return GapPage{}, err
	}
	out.HasMore = len(out.Items) > q.Limit
	if out.HasMore {
		out.Items = out.Items[:q.Limit]
		last := out.Items[len(out.Items)-1]
		b, _ := json.Marshal(gapCursor{q.Scope.Conversation, q.Scope.Character, q.FromMS, q.ToMS, last.FromMS, last.ID})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, tx.Commit()
}

func (s *Store) ChannelGap(ctx context.Context, id int64) (ChannelGap, error) {
	return scanGap(s.Events.QueryRowContext(ctx, `SELECT `+gapColumns+` FROM channel_gaps WHERE id=?`, id))
}

var ErrStaleGap = errors.New("stale-gap")
var ErrGapCooldown = errors.New("cooldown")
var ErrGapFilled = errors.New("filled")

// StartManualGap 以区间作比较更新；合并或竞争点击不能扩大目标或清空冷却。
func (s *Store) StartManualGap(ctx context.Context, g ChannelGap, now, interval int64) error {
	res, err := s.Events.ExecContext(ctx, `UPDATE channel_gaps SET last_manual_ms=?,updated_ms=? WHERE id=? AND channel=? AND conversation=? AND from_ms=? AND to_ms=? AND state IN ('open','blocked','abandoned') AND (last_manual_ms=0 OR last_manual_ms<=?)`, now, now, g.ID, g.Channel, g.Conversation, g.FromMS, g.ToMS, now-interval)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		current, readErr := s.ChannelGap(ctx, g.ID)
		if errors.Is(readErr, sql.ErrNoRows) {
			return ErrStaleGap
		}
		if readErr != nil {
			return readErr
		}
		if current.Channel != g.Channel || current.Conversation != g.Conversation || current.FromMS != g.FromMS || current.ToMS != g.ToMS {
			return ErrStaleGap
		}
		if current.State == "filled" {
			return ErrGapFilled
		}
		return ErrGapCooldown
	}
	s.changed(g.Conversation)
	return nil
}

// FinishManualGap 只结算原行，自动 tries / last_try_ms 不动，abandoned 失败不会复活。
func (s *Store) FinishManualGap(ctx context.Context, g ChannelGap, filled bool, reason string, now int64) error {
	res, err := s.Events.ExecContext(ctx, `UPDATE channel_gaps SET state=CASE WHEN ? THEN 'filled' WHEN state='abandoned' THEN state ELSE 'blocked' END,reason=?,updated_ms=? WHERE id=? AND conversation=? AND from_ms=? AND to_ms=?`, filled, reason, now, g.ID, g.Conversation, g.FromMS, g.ToMS)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStaleGap
	}
	s.changed(g.Conversation)
	return nil
}

// GapRightAnchor 只取已存、合法且位于右端或之后的平台消息；它只作分页衔接，不代表首页最新。
func (s *Store) GapRightAnchor(ctx context.Context, conv string, to int64) (string, int64, error) {
	var id string
	var at int64
	err := s.Events.QueryRowContext(ctx, `SELECT m.platform_id,e.created_at FROM events e INDEXED BY ix_events_conv_time JOIN channel_msgs m ON m.event_seq=e.seq AND m.channel='qq' AND m.conversation=e.conversation WHERE e.conversation=? AND e.kind='message' AND e.created_at>=? AND m.platform_id<>'' AND trim(m.platform_id,'0')<>'' ORDER BY e.created_at,e.seq LIMIT 1`, conv, to).Scan(&id, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	return id, at, err
}

// 只结算轮次读取到的可重试原区间；已填平或合并后，旧副本失去写权。
func updateAutomaticGap(ctx context.Context, tx *sql.Tx, g ChannelGap) error {
	_, err := tx.ExecContext(ctx, `UPDATE channel_gaps SET reason=?,state=?,tries=?,last_try_ms=?,updated_ms=? WHERE id=? AND channel=? AND conversation=? AND from_ms=? AND to_ms=? AND state IN ('open','blocked')`, g.Reason, g.State, g.Tries, g.LastTryMS, g.UpdatedMS, g.ID, g.Channel, g.Conversation, g.FromMS, g.ToMS)
	return err
}
