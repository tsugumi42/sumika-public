package store

import "slices"

// ForwardMediaMetaCandidates 用完整主键翻页，缺文件的旧行不会卡住后续补扫。
func (s *Store) ForwardMediaMetaCandidates(afterID string, afterNode, afterIdx, limit int) ([]ForwardMedia, error) {
	rows, err := s.Events.Query(`SELECT forward_id,node,idx,hash FROM forward_media_fetch
		WHERE state='done' AND kind IN ('image','emoji','card') AND (width=0 OR height=0)
		AND (forward_id,node,idx) > (?,?,?) ORDER BY forward_id,node,idx LIMIT ?`, afterID, afterNode, afterIdx, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ForwardMedia
	for rows.Next() {
		var m ForwardMedia
		if err := rows.Scan(&m.ID, &m.Node, &m.Idx, &m.Hash); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateForwardMediaMeta 每页一次提交、每个会话一次通知；只补仍指向同一文件的 done 行。
func (s *Store) UpdateForwardMediaMeta(ms []ForwardMedia) error {
	tx, err := s.Events.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var convs []string
	for _, m := range ms {
		res, err := tx.Exec(`UPDATE forward_media_fetch SET width=?,height=?,animated=?
			WHERE forward_id=? AND node=? AND idx=? AND hash=? AND state='done' AND (width=0 OR height=0)`,
			m.Width, m.Height, m.Animated, m.ID, m.Node, m.Idx, m.Hash)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		var conv string
		if err := tx.QueryRow(`SELECT conversation FROM forward_media_fetch WHERE forward_id=? AND node=? AND idx=?`, m.ID, m.Node, m.Idx).Scan(&conv); err != nil {
			return err
		}
		if !slices.Contains(convs, conv) {
			convs = append(convs, conv)
		}
	}
	return s.commitChanged(tx, convs...)
}
