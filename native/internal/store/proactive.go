package store

import (
	"database/sql"
	"errors"
)

// OwnerLastSpoke 是这个平台库里主人最后一次发言的时间（毫秒），只看 sinceMS 以后的；没有 ok 为假。
// 给主动联系的导演判断"主人现在在不在活跃"用（规格-主动联系 第七节输入、第八节）。
// 带时间下界、按时间倒着取第一条：主人很久没说话时只扫下界以后的这段，不扫全库
func (s *Store) OwnerLastSpoke(sinceMS int64) (at int64, ok bool, err error) {
	err = s.Events.QueryRow(`SELECT created_at FROM events WHERE actor_kind = 'owner' AND kind = 'message' AND created_at > ?
		ORDER BY created_at DESC, seq DESC LIMIT 1`, sinceMS).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return at, err == nil, err
}

// OwnerLastSpokeIn 是主人在某个会话里最后一次发言的时间（毫秒），只看 sinceMS 以后的；没有 ok 为假。
// 主动联系的 owner_presence 用：靠 ix_events_conv_time 在这一个会话里往回找
func (s *Store) OwnerLastSpokeIn(conversation string, sinceMS int64) (at int64, ok bool, err error) {
	err = s.Events.QueryRow(`SELECT created_at FROM events WHERE conversation = ? AND actor_kind = 'owner' AND kind = 'message' AND created_at > ?
		ORDER BY created_at DESC, seq DESC LIMIT 1`, conversation, sinceMS).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return at, err == nil, err
}

// LastMessageAt 是某个会话最近一条消息（任何人说的）的时间（毫秒）；没有 ok 为假
func (s *Store) LastMessageAt(conversation string) (at int64, ok bool, err error) {
	err = s.Events.QueryRow(`SELECT created_at FROM events WHERE conversation = ? AND kind = 'message' ORDER BY created_at DESC, seq DESC LIMIT 1`, conversation).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return at, err == nil, err
}

// LatestCharacterSeq 是某个会话里这个角色最近一条说出口的话（origin 为 character）的 seq；没有 ok 为假。
// 主动联系判断"线发的那条仍是该会话最近一条角色发言"用
func (s *Store) LatestCharacterSeq(conversation, character string) (seq int64, ok bool, err error) {
	err = s.Events.QueryRow(`SELECT seq FROM events WHERE conversation = ? AND actor_kind = 'character' AND actor_id = ? AND origin = 'character' AND kind = 'message'
		ORDER BY seq DESC LIMIT 1`, conversation, character).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return seq, err == nil, err
}

// EventTextID 按 seq 取某个会话里一条事件的文字和 id（交回主人的话、写发送记录用）；没有 ok 为假
func (s *Store) EventTextID(conversation string, seq int64) (text, id string, ok bool, err error) {
	err = s.Events.QueryRow(`SELECT text, id FROM events WHERE conversation = ? AND seq = ?`, conversation, seq).Scan(&text, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return text, id, err == nil, err
}
