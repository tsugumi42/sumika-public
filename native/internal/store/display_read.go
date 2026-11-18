package store

// 界面标已读的写入口（主人 2026-11-02："现在未读也还不能自动刷新消除"）。
// 之前 qq_read 只有导入在写、展示只读（ReadDisplayUnread 不建行）；这是展示这一侧第一个写库的方法，所以做得很窄：
//   - 只写 qq_read 一张表一行（会话 → 读到哪个时间，毫秒），不碰事件、观察；
//   - 只能往前：已有的 read_at 比这次大或一样就不动，不通知；
//   - 不能超过这个会话里最新一条可见消息的时间：调用方传大了（界面 bug、伪造）也只记到真实有的最新一条，
//     不会把将来才到的消息提前算成已读；
//   - 会话必须在可见范围里（和读路径同一个 validateDisplay），本地库没有 qq_read 表，直接报错不假装成功。
// 计数口径见 ReadDisplayUnread：created_at 严格大于 read_at 的才算未读，所以和 read_at 同一毫秒后到的消息会被当成已读（毫秒内的并发，接受）。

import (
	"context"
	"database/sql"
	"errors"
)

// ErrNoReadTracking 是这个库没有已读记录（不是 QQ 平台的库）
var ErrNoReadTracking = errors.New("这个库没有已读记录：已读只在 QQ 平台的库里（qq_read）")

// MarkDisplayRead 把 scope.Conversation 标成读到 upTo（毫秒）。返回实际写进库的 read_at；
// 没有可标的（会话里没有可见消息、upTo 不大于 0）或没有往前走（已经读到这里了）返回 0, nil，不通知也不写。
func (s *Store) MarkDisplayRead(ctx context.Context, scope DisplayScope, upTo int64) (int64, error) {
	ok, err := tableExists(s.Events, "qq_read")
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNoReadTracking
	}
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = validateDisplay(ctx, tx, scope); err != nil {
		return 0, err
	}
	if upTo <= 0 {
		return 0, nil
	}
	filter, args := displayFilter(scope, "e")
	var newest sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT MAX(e.created_at) FROM events e WHERE e.conversation=? AND e.kind='message' AND `+filter, append([]any{scope.Conversation}, args...)...).Scan(&newest); err != nil {
		return 0, err
	}
	if !newest.Valid {
		return 0, nil
	}
	at := min(upTo, newest.Int64)
	res, err := tx.ExecContext(ctx, `INSERT INTO qq_read (conversation, read_at) VALUES (?, ?)
		ON CONFLICT(conversation) DO UPDATE SET read_at=excluded.read_at WHERE excluded.read_at > qq_read.read_at`, scope.Conversation, at)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	s.changed(scope.Conversation)
	return at, nil
}
