package store

import "database/sql"

// 提醒（规格-提醒）用的两个小查询。提醒表本身不在库里（data/go/reminders.json）。

// HasReminderKey 这个会话里有没有写过带这个幂等键的提醒（since 以后的）：重启后接着响之前查，查到就不再写
func (s *Store) HasReminderKey(conversation, key string, since int64) (bool, error) {
	var one int
	err := s.Events.QueryRow(`SELECT 1 FROM events WHERE conversation = ? AND created_at >= ? AND json_extract(segments_json, '$.reminder.key') = ? LIMIT 1`, conversation, since, key).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// ChatLine 是一句话：谁说的（owner、character）和原文
type ChatLine struct{ Actor, Text string }

// RecentChatLines 是这个会话 since 以后最近几句主人和她说的话，旧的在前。不带隐藏的、试运行的
func (s *Store) RecentChatLines(conversation string, since int64, limit int) ([]ChatLine, error) {
	rows, err := s.Events.Query(`SELECT actor_kind, text FROM events WHERE conversation = ? AND created_at >= ? AND kind = 'message'
		AND ((actor_kind = 'owner' AND origin = 'owner') OR (actor_kind = 'character' AND origin = 'character')) AND text <> ''
		ORDER BY created_at DESC, seq DESC LIMIT ?`, conversation, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatLine
	for rows.Next() {
		var l ChatLine
		if err := rows.Scan(&l.Actor, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
