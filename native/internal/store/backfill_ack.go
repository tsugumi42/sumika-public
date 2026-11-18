package store

import (
	"context"
	"database/sql"
	"errors"
)

// SetGapAcknowledged 只更新原区间的提醒状态；不动覆盖、自动次数或两类位置。
func (s *Store) SetGapAcknowledged(ctx context.Context, g ChannelGap, acknowledged bool, now int64) (int64, error) {
	var at int64
	err := s.Events.QueryRowContext(ctx, `UPDATE channel_gaps SET acknowledged_ms=CASE WHEN ? THEN CASE WHEN acknowledged_ms=0 THEN ? ELSE acknowledged_ms END ELSE 0 END WHERE id=? AND channel=? AND conversation=? AND from_ms=? AND to_ms=? AND state='blocked' RETURNING acknowledged_ms`, acknowledged, now, g.ID, g.Channel, g.Conversation, g.FromMS, g.ToMS).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrStaleGap
	}
	if err == nil {
		s.changed(g.Conversation)
	}
	return at, err
}
