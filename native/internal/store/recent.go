package store

// 拦截插件和"叫到她"的判断要从库里读的几样（规格-渠道中间件 第七节、第四节 Addressed、第九节）：
// 按平台消息号找一条事件（引用的是不是她的话、撤回指的是哪一条），某个会话里这一批之前的最近几条消息（复读在重启后靠它重建），
// 和插件自己发的话（复读等，回合定了以后补进她的实例）。只读，Go 自己的库（有 channel_msgs）上用

import (
	"database/sql"
	"errors"

	"sumika-native/internal/history"
)

// EventRef 是库里一条事件的几个要紧字段
type EventRef struct {
	Seq       int64
	ID        string
	ActorKind string
	ActorID   string
	Origin    string
}

// EventByPlatformID 按（渠道、会话、平台消息号）找事件，找不到 ok 为假。号为空或全是 0 的找不到（本来就没进映射）
func (s *Store) EventByPlatformID(channel, conversation, platformID string) (ref EventRef, ok bool, err error) {
	if !HasPlatformID(platformID) {
		return EventRef{}, false, nil
	}
	err = s.Events.QueryRow(`SELECT e.seq, e.id, e.actor_kind, e.actor_id, COALESCE(e.origin, '') FROM channel_msgs m JOIN events e ON e.seq = m.event_seq
		WHERE m.channel = ? AND m.conversation = ? AND m.platform_id = ?`, channel, conversation, platformID).
		Scan(&ref.Seq, &ref.ID, &ref.ActorKind, &ref.ActorID, &ref.Origin)
	if errors.Is(err, sql.ErrNoRows) {
		return EventRef{}, false, nil
	}
	return ref, err == nil, err
}

// EventIDBySeq 按会话里的 seq 找事件 id；找不到 ok 为假。
func (s *Store) EventIDBySeq(conversation string, seq int64) (id string, ok bool, err error) {
	err = s.Events.QueryRow(`SELECT id FROM events WHERE conversation = ? AND seq = ?`, conversation, seq).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// RecentRow 是 RecentMessages 的一行：消息事件的几个字段，段原样（JSON 文本）交给调用方解
type RecentRow struct {
	Seq       int64
	ActorKind string
	ActorID   string
	Origin    string
	CreatedAt int64          // 毫秒；平台时间，补录来的是当初发出的时间
	Segments  sql.NullString // segments_json
}

// RecentMessages 是这个会话里 seq 小于 beforeSeq 的最近 n 条消息事件（kind = message），按时间正序（旧的在前；同一毫秒按 seq）。
// 被撤回的（有一条 kind = notice、notice = recall 的通知指向它）不在里面；通知不是消息，也不在。
//
// "最近"按平台时间排，不按 seq：补录把更早的话晚存进来，seq 靠后、时间靠前，排在 seq 后面的话会把真正最近的挤出去。
// 查撤回只认同会话中更晚且指向该消息的通知；Go 库由 chat/0012 的通知目标索引定位，旧库仍可走会话索引。
// INDEXED BY ix_events_conv_time 不能省：不指定的话 SQLite 选 ix_events_conv，把这个会话的每一条都先过一遍"有没有被撤回"再排序取前 n 条
// （3 万条事件分在 10 个会话里，每次 5.5 秒）；按这个索引走，已经是 (created_at, seq) 的倒序，查够 n 条就停，只有最近的几十条要查撤回。
// 这个索引在 0001 基线里，Bun 管着的真实库里也有
func (s *Store) RecentMessages(conversation string, beforeSeq int64, n int) ([]RecentRow, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := s.Events.Query(`SELECT e.seq, e.actor_kind, e.actor_id, COALESCE(e.origin, ''), e.created_at, e.segments_json FROM events e INDEXED BY ix_events_conv_time
		WHERE e.conversation = ? AND e.kind = 'message' AND e.seq < ?
		AND NOT EXISTS (SELECT 1 FROM events r WHERE r.conversation = e.conversation AND r.seq > e.seq AND r.kind = 'notice' AND r.reply_to = e.id
			AND json_valid(r.segments_json) AND json_extract(r.segments_json, '$.notice') = 'recall')
		ORDER BY e.created_at DESC, e.seq DESC LIMIT ?`, conversation, beforeSeq, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecentRow
	for rows.Next() {
		var r RecentRow
		if err := rows.Scan(&r.Seq, &r.ActorKind, &r.ActorID, &r.Origin, &r.CreatedAt, &r.Segments); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// AutomationLine 是插件自己发的一条话（origin 是 automation:*）
type AutomationLine struct {
	Seq    int64
	Origin string
	Text   string
}

// AutomationLines 是这个会话里 after 之后、before 之前插件自己发的话（origin 是 automation:*，不含试运行和 self-skip），按 seq 正序。
// 这些不是她说的，但确实是她的号说出去的：回合定了以后补进她的实例，下一批才知道自己复读过（规格-渠道中间件 第九节，写法和 history.toMessage 一样）。
// 按会话取库由调用方做（seq 只在一个平台的库里唯一）
func (s *Store) AutomationLines(conversation, characterID string, after, before int64) ([]AutomationLine, error) {
	rows, err := s.Events.Query(`SELECT seq, origin, text FROM events
		WHERE conversation = ? AND kind = 'message' AND actor_kind = 'character' AND actor_id = ? AND origin LIKE 'automation:%'
		AND origin NOT IN (`+history.NotSaidSQL+`) AND seq > ? AND seq < ? ORDER BY seq`,
		conversation, characterID, after, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AutomationLine
	for rows.Next() {
		var l AutomationLine
		if err := rows.Scan(&l.Seq, &l.Origin, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
