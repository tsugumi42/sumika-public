package store

// 渠道中间件收到的一条消息怎么入库（规格-渠道中间件 第六节第 3、5 步，第十四节"入库和映射"一行）。
// 只在 Go 自己的库上用（OpenFresh 打开的，有 0002 建的 channel_msgs）；Bun 管着的真实库照旧只读

import (
	"database/sql"
	"errors"
	"strings"
)

// HasPlatformID 说这个平台消息号能不能用来去重：空串、全是 0 的（协议端没给号，或给了个 0）都不行。
// 不行的消息按单条入库，不进 channel_msgs——不然不同的消息会因为"都没有号"被当成同一条（清单⑯，规格 第六节第 3 步）
func HasPlatformID(id string) bool {
	return strings.TrimLeft(id, "0") != ""
}

// NewInbound 是要入库的一条收到的消息
type NewInbound struct {
	Event NewEvent
	// PlatformID 是平台消息号，原始数字串（不转数字：超过 2^53 的号往返要不变）。去重的键是（Event.Channel、Event.Conversation、PlatformID），
	// 不是内容：同一条消息在两个号上的内容可能不同（语音转写、图片链接）
	PlatformID string
	// ReplyPlatformID 是这条消息引用的那条的平台消息号。库里有那一条就把 Event.ReplyTo 连上它；没有就留空，由别处后补（不挡入库，规格 第六节第 7 步）。
	// 已经给了 Event.ReplyTo 的不覆盖
	ReplyPlatformID string
	// Observers 是看到了这条消息的角色。重复的消息（键已经在库里）也要记：两个号看到同一条，各记一份"看到了"
	Observers []string
}

// InboundResult 是入库的结果。Seq 起到 Origin 说的都是库里那一条事件——重复到达时是已有的那条，不是这一次交来的那份
type InboundResult struct {
	Seq       int64
	EventID   string
	ActorKind string
	ActorID   string
	Origin    string
	// Duplicate 为真：键已经在库里，这一份没有新增事件，只补了 Observers 的"看到了"
	Duplicate bool
	// NewObservations 是这次新记了几条"看到了"（已经记过的不算）。重复到达时用它知道这个角色是不是第一次看到这条
	NewObservations int
	// DupCreatedAt、DupBackfill 只在重复到达时有值：库里已有的那条的时间（毫秒），和它当初是不是补录来的存进去的。
	// 一个角色第一次看到一条已有的事件时，"这个角色看到的第一条实时事件"可能因此变早，调用方（接管时刻的缓存）要精确更新而不是整个作废
	DupCreatedAt int64
	DupBackfill  bool
}

// backfillSQL 是"events 这一行（别名 e）是补录来的"的判断：tags 里有 backfill；segments_json 不是合法 JSON 的行（别的写法留下的）当作不是补录。
// FirstLiveAt 和 AppendInbound 读已有那条用同一段，"是不是补录"只有一个口径
const backfillSQL = `(CASE WHEN json_valid(e.segments_json)
		THEN EXISTS (SELECT 1 FROM json_each(e.segments_json, '$.tags') WHERE value = 'backfill') ELSE 0 END)`

// AppendInbound 把一条收到的消息入库：查重、事件、平台消息号映射、"看到了"，在一个事务里，要么全在要么全不在
// （Bun 里事件和映射是两次写入，中间崩了补录会再存一份：qq.ts 的 appendEvent 和 INSERT INTO qq_msgs，盘点 第五节）。
//
// 一个库只有一个连接（open 里 SetMaxOpenConns(1)），事务里的所有查询都用 tx，不能再碰 s.Events，否则自己等自己
func (s *Store) AppendInbound(in NewInbound) (InboundResult, error) {
	if in.Event.Channel == "" || in.Event.Conversation == "" {
		return InboundResult{}, errors.New("入库的消息没有渠道或会话")
	}
	tx, err := s.Events.Begin()
	if err != nil {
		return InboundResult{}, err
	}
	defer tx.Rollback()

	keyed := HasPlatformID(in.PlatformID)
	if keyed {
		var r InboundResult
		err := tx.QueryRow(`SELECT e.seq, e.id, e.actor_kind, e.actor_id, e.origin, e.created_at, `+backfillSQL+`
			FROM channel_msgs m JOIN events e ON e.seq = m.event_seq
			WHERE m.channel = ? AND m.conversation = ? AND m.platform_id = ?`, in.Event.Channel, in.Event.Conversation, in.PlatformID).
			Scan(&r.Seq, &r.EventID, &r.ActorKind, &r.ActorID, &r.Origin, &r.DupCreatedAt, &r.DupBackfill)
		switch {
		case err == nil:
			r.Duplicate = true
			if r.NewObservations, err = observe(tx, r.Seq, in.Observers); err != nil {
				return InboundResult{}, err
			}
			return r, s.commitChanged(tx, in.Event.Conversation)
		case !errors.Is(err, sql.ErrNoRows):
			return InboundResult{}, err
		}
	}

	e := in.Event
	if e.ReplyTo == nil && HasPlatformID(in.ReplyPlatformID) {
		var id string
		err := tx.QueryRow(`SELECT e.id FROM channel_msgs m JOIN events e ON e.seq = m.event_seq
			WHERE m.channel = ? AND m.conversation = ? AND m.platform_id = ?`, e.Channel, e.Conversation, in.ReplyPlatformID).Scan(&id)
		switch {
		case err == nil:
			e.ReplyTo = &id
		case !errors.Is(err, sql.ErrNoRows):
			return InboundResult{}, err
		}
	}
	seq, id, err := insertEvent(tx, e)
	if err != nil {
		return InboundResult{}, err
	}
	if keyed {
		if _, err := tx.Exec(`INSERT INTO channel_msgs (channel, conversation, platform_id, event_seq) VALUES (?, ?, ?, ?)`,
			e.Channel, e.Conversation, in.PlatformID, seq); err != nil {
			return InboundResult{}, err
		}
	}
	added, err := observe(tx, seq, in.Observers)
	if err != nil {
		return InboundResult{}, err
	}
	return InboundResult{Seq: seq, EventID: id, ActorKind: e.ActorKind, ActorID: e.ActorID, Origin: e.Origin, NewObservations: added}, s.commitChanged(tx, e.Conversation)
}

// FirstLiveAt 是这个角色在这个渠道里看到的第一条"不是补录来的"事件的时间（毫秒，没有返回 false）。
// Sumika 实时接管这个角色的号的时刻就按它算（Bun 的 takeoverAt，规格 第十八节"takeoverAt 启发式"：照搬，没有验证）。
// 补录的标记在 segments_json 的 tags 里（中间件写的）；segments_json 不是合法 JSON 的行（别的写法留下的）当作不是补录
func (s *Store) FirstLiveAt(channel, characterID string) (int64, bool, error) {
	var t sql.NullInt64
	err := s.Events.QueryRow(`SELECT min(e.created_at) FROM events e JOIN observations o ON o.event_seq = e.seq
		WHERE e.channel = ? AND o.character_id = ?
		AND NOT `+backfillSQL, channel, characterID).Scan(&t)
	if err != nil {
		return 0, false, err
	}
	return t.Int64, t.Valid, nil
}
