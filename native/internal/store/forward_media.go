package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"sumika-native/internal/channel/types"
)

func putForwardMedia(db execer, id string, node int, conv string, m MediaFetch) error {
	_, err := db.Exec(`INSERT INTO forward_media_fetch(forward_id,node,idx,conversation,kind,url_sig,state,tries,next_at,updated_at,hash,last_error,width,height,animated,cover_hash,duration_ms,size,format)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(forward_id,node,idx) DO UPDATE SET conversation=excluded.conversation,kind=excluded.kind,url_sig=excluded.url_sig,state=excluded.state,tries=excluded.tries,next_at=excluded.next_at,updated_at=excluded.updated_at,hash=excluded.hash,last_error=excluded.last_error,width=excluded.width,height=excluded.height,animated=excluded.animated,cover_hash=excluded.cover_hash,duration_ms=excluded.duration_ms,size=excluded.size,format=excluded.format`, id, node, m.Idx, conv, m.Kind, m.URLSig, m.State, m.Tries, m.NextAt, m.UpdatedAt, nullable(m.Hash), nullable(m.LastError), m.Width, m.Height, m.Animated, m.CoverHash, m.DurationMS, m.Size, m.Format)
	return err
}
func (s *Store) PutForwardMedia(id string, node int, conv string, m MediaFetch) error {
	return putForwardMedia(s.Events, id, node, conv, m)
}

func (s *Store) FinishForwardSegHash(id string, node int, conv string, m MediaFetch) (bool, error) {
	if !validSHA256(m.Hash) || node < 0 || m.Idx < 0 {
		return false, fmt.Errorf("非法转发媒体凭据")
	}
	tx, err := s.Events.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	path := fmt.Sprintf("$.nodes[%d].segs[%d]", node, m.Idx)
	var raw string
	err = tx.QueryRow(`SELECT coalesce(json_extract(tree_json,?),'{}') FROM qq_forwards WHERE id=?`, path, id).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	applied := false
	var seg types.Seg
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &seg); err != nil {
			return false, err
		}
	}
	if err == nil && (seg.Kind == "image" || seg.Kind == "emoji" || seg.Kind == "card" || seg.Kind == "video" || seg.Kind == "file") && seg.Kind == m.Kind && (seg.URL != "" || seg.Kind == "file") && MediaSegSig(seg) == m.URLSig && seg.Hash == "" {
		_, err = tx.Exec(`UPDATE qq_forwards SET tree_json=json_set(tree_json,?||'.hash',?) WHERE id=?`, path, m.Hash, id)
		if err != nil {
			return false, err
		}
		applied = true
	}
	if err = putForwardMedia(tx, id, node, conv, m); err != nil {
		return false, err
	}
	if applied {
		return true, s.commitChanged(tx, conv)
	}
	return false, tx.Commit()
}

type ForwardMedia struct {
	ID, Conversation, URL string
	Node                  int
	MediaFetch
}

func (s *Store) ForwardMediaPending(ctx context.Context, since, now int64) ([]ForwardMedia, error) {
	rows, err := s.Events.QueryContext(ctx, `SELECT m.forward_id,m.node,m.idx,m.conversation,m.kind,m.url_sig,m.state,m.tries,m.next_at,m.updated_at,
 coalesce(json_extract(f.tree_json,'$.nodes['||m.node||'].segs['||m.idx||'].url'),'')
 FROM forward_media_fetch m JOIN qq_forwards f ON f.id=m.forward_id WHERE m.updated_at>=? AND (m.state='pending' OR (m.state='failed' AND m.tries=3 AND f.fetched_at>=?)) LIMIT 500`, since, now-24*60*60*1000)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ForwardMedia
	for rows.Next() {
		var m ForwardMedia
		if err = rows.Scan(&m.ID, &m.Node, &m.Idx, &m.Conversation, &m.Kind, &m.URLSig, &m.State, &m.Tries, &m.NextAt, &m.UpdatedAt, &m.URL); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ForwardMediaRows 一次取齐这一层的下载状态和展示元数据；archive 不升级旧库。
func (s *Store) ForwardMediaRows(ctx context.Context, id string) (map[[2]int]MediaFetch, error) {
	var columns, metaColumns, fileColumns int
	if err := s.Events.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(name IN ('width','height','animated')),0),coalesce(sum(name IN ('cover_hash','duration_ms','size','format')),0) FROM pragma_table_info('forward_media_fetch')`).Scan(&columns, &metaColumns, &fileColumns); err != nil {
		return nil, err
	}
	if columns == 0 {
		return nil, nil
	}
	meta := `0,0,0`
	if metaColumns == 3 {
		meta = `width,height,animated`
	}
	extra := `'',0,0,''`
	if fileColumns == 4 {
		extra = `cover_hash,duration_ms,size,format`
	}
	rows, err := s.Events.QueryContext(ctx, `SELECT node,idx,kind,url_sig,state,coalesce(hash,''),coalesce(last_error,''),`+meta+`,`+extra+` FROM forward_media_fetch WHERE forward_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]int]MediaFetch{}
	for rows.Next() {
		var node int
		var m MediaFetch
		if err = rows.Scan(&node, &m.Idx, &m.Kind, &m.URLSig, &m.State, &m.Hash, &m.LastError, &m.Width, &m.Height, &m.Animated, &m.CoverHash, &m.DurationMS, &m.Size, &m.Format); err != nil {
			return nil, err
		}
		out[[2]int{node, m.Idx}] = m
	}
	return out, rows.Err()
}
