package store

// 登记和更新下载结果分开：重复引用同一棵树不能把下载结果改回 pending。
func initForwardMedia(db execer, id string, node int, conv string, m MediaFetch) error {
	_, err := db.Exec(`INSERT INTO forward_media_fetch(forward_id,node,idx,conversation,kind,url_sig,state,tries,next_at,updated_at,hash,last_error)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(forward_id,node,idx) DO UPDATE SET
 conversation=excluded.conversation,kind=excluded.kind,url_sig=excluded.url_sig,state=excluded.state,
 tries=excluded.tries,next_at=excluded.next_at,updated_at=excluded.updated_at,hash=excluded.hash,last_error=excluded.last_error,
 width=0,height=0,animated=0
 WHERE forward_media_fetch.url_sig<>excluded.url_sig`, id, node, m.Idx, conv, m.Kind, m.URLSig, m.State, m.Tries, m.NextAt, m.UpdatedAt, nullable(m.Hash), nullable(m.LastError))
	return err
}

// 相同 URL 复用原状态和次数；调用方拿持久化结果决定是否入队。
func (s *Store) RegisterForwardMedia(id string, node int, conv string, m MediaFetch) (MediaFetch, error) {
	if err := initForwardMedia(s.Events, id, node, conv, m); err != nil {
		return MediaFetch{}, err
	}
	var out MediaFetch
	err := s.Events.QueryRow(`SELECT idx,kind,url_sig,state,tries,next_at,updated_at,coalesce(hash,''),coalesce(last_error,''),width,height,animated
 FROM forward_media_fetch WHERE forward_id=? AND node=? AND idx=?`, id, node, m.Idx).Scan(
		&out.Idx, &out.Kind, &out.URLSig, &out.State, &out.Tries, &out.NextAt, &out.UpdatedAt, &out.Hash, &out.LastError, &out.Width, &out.Height, &out.Animated)
	return out, err
}
