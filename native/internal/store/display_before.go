package store

import (
	"context"
	"database/sql"
)

// readBefore 取 (at, seq) 之前最近的一条可见消息；界面用它判断最老那一行前面是不是同一个人、同一天（见 DisplayPage.Before）。
// 口径和这一页一样：同一个会话、观察范围内，只认 kind=message（通知不成气泡，不算前一行）。
func readBefore(ctx context.Context, tx *sql.Tx, scope DisplayScope, at, seq int64) (*DisplayRow, error) {
	filter, fa := displayFilter(scope, "e")
	args := append([]any{scope.Conversation, at, at, seq}, fa...)
	rows, err := tx.QueryContext(ctx, `SELECT `+displayColumns+` FROM events e WHERE e.conversation=? AND e.kind='message' AND (e.created_at<? OR (e.created_at=? AND e.seq<?)) AND `+filter+` ORDER BY e.created_at DESC,e.seq DESC LIMIT 1`, args...)
	if err != nil {
		return nil, err
	}
	out, err := scanDisplay(rows)
	rows.Close()
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return &out[0], nil
}
