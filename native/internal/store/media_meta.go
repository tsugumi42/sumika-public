package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
)

// MediaMetaCandidates 用主键游标前进，缺文件、认不出尺寸的行不会卡住后面的图片。
func (s *Store) MediaMetaCandidates(afterSeq int64, afterIdx, limit int) ([]MediaFetch, error) {
	rows, err := s.Events.Query(`SELECT event_seq, idx, hash FROM media_fetch
		WHERE state='done' AND kind IN ('image','emoji') AND (width=0 OR height=0) AND (event_seq,idx) > (?,?)
		ORDER BY event_seq,idx LIMIT ?`, afterSeq, afterIdx, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaFetch
	for rows.Next() {
		var m MediaFetch
		if err := rows.Scan(&m.EventSeq, &m.Idx, &m.Hash); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateMediaMeta 只补同一文件的元数据，保留下载状态。一批（补扫的一页）放进一个事务、提交后每个会话只通知一次：
// 一张一提交的话，补几百张图时打开着的会话会跟着重读几百遍
func (s *Store) UpdateMediaMeta(ms []MediaFetch) error {
	tx, err := s.Events.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var convs []string
	for _, m := range ms {
		res, err := tx.Exec(`UPDATE media_fetch SET width=?,height=?,animated=?
			WHERE event_seq=? AND idx=? AND hash=? AND state='done' AND (width=0 OR height=0)`,
			m.Width, m.Height, m.Animated, m.EventSeq, m.Idx, m.Hash)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		var conv string
		if err := tx.QueryRow(`SELECT conversation FROM events WHERE seq=?`, m.EventSeq).Scan(&conv); err != nil {
			return err
		}
		if !slices.Contains(convs, conv) {
			convs = append(convs, conv)
		}
	}
	return s.commitChanged(tx, convs...)
}

// 展示查询只读这一页的库行，不接触媒体文件；主键前缀 IN 一次取齐每条消息的全部图片段。
func enrichDisplayMedia(ctx context.Context, tx *sql.Tx, items []DisplayRow) error {
	marks := make([]string, len(items))
	args := make([]any, len(items))
	bySeq := make(map[int64]int, len(items))
	for i, r := range items {
		marks[i], args[i], bySeq[r.Seq] = "?", r.Seq, i
	}
	// archive 只读旧库；没新增列时返回未知类型，不为了展示迁移数据。
	var hasFormat int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('media_fetch') WHERE name='format'`).Scan(&hasFormat); err != nil {
		return err
	}
	format := `''`
	if hasFormat == 1 {
		format = `format`
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_seq,idx,coalesce(hash,''),width,height,animated,kind,state,cover_hash,duration_ms,size,`+format+` FROM media_fetch
		WHERE event_seq IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var m MediaFetch
		if err := rows.Scan(&m.EventSeq, &m.Idx, &m.Hash, &m.Width, &m.Height, &m.Animated, &m.Kind, &m.State, &m.CoverHash, &m.DurationMS, &m.Size, &m.Format); err != nil {
			return err
		}
		r := &items[bySeq[m.EventSeq]]
		if r.MediaMeta == nil {
			r.MediaMeta = make(map[int]MediaFetch)
		}
		r.MediaMeta[m.Idx] = m
	}
	return rows.Err()
}
