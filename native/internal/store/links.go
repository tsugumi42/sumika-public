package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// LinkResult 由链接解析层校验，存储层不解释平台载荷。
type LinkResult struct {
	CleanURL  string
	FetchedAt time.Time
	JSON      []byte
}

func (s *Store) LinkResult(ctx context.Context, clean string) (LinkResult, bool, error) {
	r := LinkResult{CleanURL: clean}
	var stamp string
	err := s.Trace.QueryRowContext(ctx, `SELECT fetched_at,result_json FROM link_results WHERE clean_url=?`, clean).Scan(&stamp, &r.JSON)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.FetchedAt, err = time.Parse(time.RFC3339Nano, stamp)
	return r, err == nil, err
}
func (s *Store) PutLinkResult(ctx context.Context, r LinkResult) error {
	_, err := s.Trace.ExecContext(ctx, `INSERT INTO link_results(clean_url,fetched_at,result_json) VALUES(?,?,?) ON CONFLICT(clean_url) DO UPDATE SET fetched_at=excluded.fetched_at,result_json=excluded.result_json`, r.CleanURL, r.FetchedAt.UTC().Format(time.RFC3339Nano), r.JSON)
	return err
}
func (s *Store) LinkAlias(ctx context.Context, clean string) (string, time.Time, bool, error) {
	var target, stamp string
	err := s.Trace.QueryRowContext(ctx, `SELECT target_url,resolved_at FROM link_aliases WHERE clean_url=?`, clean).Scan(&target, &stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	return target, at, err == nil, err
}
func (s *Store) PutLinkAlias(ctx context.Context, clean, target string, at time.Time) error {
	_, err := s.Trace.ExecContext(ctx, `INSERT INTO link_aliases VALUES(?,?,?) ON CONFLICT(clean_url) DO UPDATE SET target_url=excluded.target_url,resolved_at=excluded.resolved_at`, clean, target, at.UTC().Format(time.RFC3339Nano))
	return err
}

// RecordLink 返回首次处理时的历史次数；重放保持原值，失败后可补齐详情。
// 详情按内容哈希存进 link_snapshots，同一份结果被关联多少次都只存一份（1 万次长推文从 256 MB 降下来）。
func (s *Store) RecordLink(ctx context.Context, conversation string, seq int64, clean string, result []byte) (int, error) {
	if seq <= 0 || clean == "" {
		return 0, fmt.Errorf("链接关联需要消息 seq 和干净链接")
	}
	var hash any
	if result != nil {
		sum := sha256.Sum256(result)
		hash = hex.EncodeToString(sum[:])
	}
	tx, err := s.Events.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO message_links(conversation,message_seq,clean_url,previous_count,snapshot) SELECT ?,?,?,count(*),? FROM message_links WHERE conversation=? AND clean_url=? ON CONFLICT(conversation,message_seq,clean_url) DO UPDATE SET snapshot=CASE WHEN message_links.result_json IS NULL THEN coalesce(message_links.snapshot,excluded.snapshot) END`, conversation, seq, clean, hash, conversation, clean)
	if err != nil {
		return 0, err
	}
	// 只在这一行确实指向这个哈希时才存详情：重放带来的新结果不会留下没人引用的快照。
	if hash != nil {
		if _, err = tx.ExecContext(ctx, `INSERT INTO link_snapshots(hash,result_json) SELECT ?,? WHERE EXISTS(SELECT 1 FROM message_links WHERE conversation=? AND message_seq=? AND clean_url=? AND snapshot=? AND result_json IS NULL) ON CONFLICT(hash) DO NOTHING`, hash, result, conversation, seq, clean, hash); err != nil {
			return 0, err
		}
	}
	var previous int
	if err = tx.QueryRowContext(ctx, `SELECT previous_count FROM message_links WHERE conversation=? AND message_seq=? AND clean_url=?`, conversation, seq, clean).Scan(&previous); err != nil {
		return 0, err
	}
	return previous, tx.Commit()
}

// MessageLink 先读 0004 时期存在行里的旧详情，再读共享快照。
func (s *Store) MessageLink(ctx context.Context, conversation string, seq int64, clean string) ([]byte, bool, error) {
	var b []byte
	err := s.Events.QueryRowContext(ctx, `SELECT coalesce(m.result_json,k.result_json) FROM message_links m LEFT JOIN link_snapshots k ON k.hash=m.snapshot WHERE m.conversation=? AND m.message_seq=? AND m.clean_url=?`, conversation, seq, clean).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return b, err == nil, err
}

// LinkNote 是消息关联的链接，携带真实事件原文、身份及可选段信封，给“她看”和详情工具读。
// Result 为空表示取过但没取到（失败也记次数）；还没取完的根本没有这一行
type LinkNote struct {
	Seq       int64
	CreatedAt int64 // 毫秒
	Segments  sql.NullString
	Text      string
	ActorKind string
	CleanURL  string
	Previous  int // 这条以前本会话发过几次
	Result    []byte
}

// QuotedLinkNotes 是被引用的那条消息（同一个会话的 seq）关联的链接，给引用那一行带摘要用：引用本身就把那条消息
// 摆在了会话里所有人面前，所以不按角色筛（详情工具要按角色筛，用 LinkNotes）
func (s *Store) QuotedLinkNotes(ctx context.Context, conversation string, seq int64) ([]LinkNote, error) {
	rows, err := s.Events.QueryContext(ctx, `SELECT m.message_seq, e.created_at, e.segments_json, coalesce(e.text, ''), e.actor_kind, m.clean_url, m.previous_count, coalesce(m.result_json, k.result_json)
FROM message_links m
JOIN events e ON e.seq = m.message_seq AND e.conversation = m.conversation
LEFT JOIN link_snapshots k ON k.hash = m.snapshot
WHERE m.conversation = ? AND m.message_seq = ? ORDER BY m.rowid`, conversation, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkNote
	for rows.Next() {
		var n LinkNote
		if err := rows.Scan(&n.Seq, &n.CreatedAt, &n.Segments, &n.Text, &n.ActorKind, &n.CleanURL, &n.Previous, &n.Result); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// LinkNotes 读 conversation 里 character 看到过（observations 有）的消息关联的链接：seq > 0 时只要那一条，否则取最近 limit 条消息的；
// 同一条消息里按处理的先后。别的角色没看到的、别的会话的读不到——详情工具的越权检查靠这里
func (s *Store) LinkNotes(ctx context.Context, conversation, character string, seq int64, limit int) ([]LinkNote, error) {
	if conversation == "" || character == "" {
		return nil, fmt.Errorf("读链接资料要会话和角色")
	}
	q := `SELECT m.message_seq, e.created_at, e.segments_json, coalesce(e.text, ''), e.actor_kind, m.clean_url, m.previous_count, coalesce(m.result_json, k.result_json)
FROM message_links m
JOIN events e ON e.seq = m.message_seq AND e.conversation = m.conversation
JOIN observations o ON o.event_seq = m.message_seq AND o.character_id = ?
LEFT JOIN link_snapshots k ON k.hash = m.snapshot
WHERE m.conversation = ?`
	args := []any{character, conversation}
	if seq > 0 {
		q += ` AND m.message_seq = ?`
		args = append(args, seq)
	} else {
		if limit <= 0 {
			limit = 20
		}
		q += ` AND m.message_seq IN (SELECT DISTINCT message_seq FROM message_links WHERE conversation = ? ORDER BY message_seq DESC LIMIT ?)`
		args = append(args, conversation, limit)
	}
	q += ` ORDER BY m.message_seq, m.rowid`
	rows, err := s.Events.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkNote
	for rows.Next() {
		var n LinkNote
		if err := rows.Scan(&n.Seq, &n.CreatedAt, &n.Segments, &n.Text, &n.ActorKind, &n.CleanURL, &n.Previous, &n.Result); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
