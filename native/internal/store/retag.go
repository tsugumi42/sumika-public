package store

// RetagOrigin 把事件的来源从 from 改成 to，只改 seq 在 (lo, hi] 里的（hi 为 0 不设上限）、
// actor_kind 是 actorKind 的（空串不限）；conv 为空是所有会话。返回改了几行，改了就通知界面刷新。
// 定期任务的一轮先用不显示的来源存，分了类再改成可见或永久隐藏，只改这一个字段，不动原文。
func (s *Store) RetagOrigin(conv string, lo, hi int64, from, to, actorKind string) (int64, error) {
	tx, err := s.Events.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := `SELECT DISTINCT conversation FROM events WHERE origin = ? AND seq > ?`
	args := []any{from, lo}
	if hi > 0 {
		q += ` AND seq <= ?`
		args = append(args, hi)
	}
	if conv != "" {
		q += ` AND conversation = ?`
		args = append(args, conv)
	}
	if actorKind != "" {
		q += ` AND actor_kind = ?`
		args = append(args, actorKind)
	}
	rows, err := tx.Query(q, args...)
	if err != nil {
		return 0, err
	}
	var convs []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return 0, err
		}
		convs = append(convs, c)
	}
	rows.Close()
	if len(convs) == 0 {
		return 0, nil
	}
	up := `UPDATE events SET origin = ? WHERE origin = ? AND seq > ?` + q[len(`SELECT DISTINCT conversation FROM events WHERE origin = ? AND seq > ?`):]
	res, err := tx.Exec(up, append([]any{to}, args...)...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, s.commitChanged(tx, convs...)
}
