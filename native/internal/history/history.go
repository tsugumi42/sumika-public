// Package history 是重建对话时给她看的历史，照 daemon/plugins/agent.ts 的 historyFor / contextEvents / toMessage
// 和 daemon/plugins/store.ts 的 messageWindow / eventsSince（只读）。
// 有压缩快照就是"快照 + 快照之后的原文"，没有就是最近 n 条算数的聊天消息；"从这里开始新话题"的分隔比快照新时按分隔截
package history

import (
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"sumika-native/internal/audience"
	"sumika-native/internal/textclean"
)

// Event 是 events 表的一行
type Event struct {
	Seq                                                   int64
	ID, Channel, Conversation, ActorKind, ActorID, Origin string
	Kind, Text                                            string
	Segments, ReplyTo                                     sql.NullString
	CreatedAt                                             int64
}

const eventCols = `seq, id, channel, conversation, actor_kind, actor_id, origin, kind, text, segments_json, reply_to, created_at`

func scanEvents(rows *sql.Rows) ([]Event, error) {
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.ID, &e.Channel, &e.Conversation, &e.ActorKind, &e.ActorID, &e.Origin, &e.Kind, &e.Text, &e.Segments, &e.ReplyTo, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// eventsPageSize 只限制单次查询，恢复区间由冻结水位决定。
const eventsPageSize = 200

// WalkEvents 在调用方冻结的水位内按 keyset 翻页；visit 执行前关闭 rows，不占用写连接。
func WalkEvents(db *sql.DB, conversation string, from, through int64, pageSize int, visit func([]Event) error) error {
	pageSize = min(max(pageSize, 1), 1000)
	for after := from - 1; after < through; {
		rows, err := db.Query(`SELECT `+eventCols+` FROM events WHERE conversation=? AND seq>? AND seq<=? ORDER BY seq LIMIT ?`, conversation, after, through, pageSize)
		if err != nil {
			return err
		}
		page, err := scanEvents(rows)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		after = page[len(page)-1].Seq
		if err := visit(page); err != nil {
			return err
		}
	}
	return nil
}

// EventsSince 读固定高水位内的全部事件，pageSize 仅限制每次查询的行数。
func EventsSince(db *sql.DB, conversation string, fromSeq int64, pageSize int) ([]Event, error) {
	through, err := MaxSeq(db, conversation)
	if err != nil {
		return nil, err
	}
	return eventsThrough(db, conversation, fromSeq, through, pageSize)
}
func eventsThrough(db *sql.DB, conversation string, from, through int64, pageSize int) ([]Event, error) {
	var events []Event
	err := WalkEvents(db, conversation, from, through, pageSize, func(page []Event) error { events = append(events, page...); return nil })
	return events, err
}

// MessageWindow 是重建时带的那一段最近的原文：至少 n 条、不到 2n 条算数的聊天消息（通知、没说出口的、skip 里的不算数），
// 返回从起点那一条到最新的全部事件。
//
// 起点锚定（规格-提示词前缀稳定第五节）：算数的消息按 seq 从 0 编号，起点取 n 的整数倍里还能留下至少 n 条的最大那个。
// 新消息进来时起点不动，攒够 n 条才整体往后跳 n 条；同一会话两次重建、重启前后，只要中间没跨过跳点，带的历史一样，
// 服务商的缓存前缀接得上（以前每次取最近 n 条，起点跟着新消息滑，重建以后历史一条都对不上）。
// 只由库里的状态算，不另记起点。skip（这一批）都在最新那头，不影响前面的编号
func MessageWindow(db *sql.DB, conversation string, n int, skip []int64) ([]Event, error) {
	from, err := windowFrom(db, conversation, n, skip, 0)
	if err != nil || from == 0 {
		return nil, err
	}
	return EventsSince(db, conversation, from, eventsPageSize)
}

// countableSQL 是“算数的消息”的查询条件（参数：会话、skip 的 JSON 数组）：聊天消息，没说出口的和这一批不算。窗口起点和重建留痕数条数都用它
const countableSQL = `FROM events WHERE conversation = ? AND kind = 'message' AND origin NOT IN (` + NotSaidSQL + `) AND seq NOT IN (SELECT value FROM json_each(?))`

// CountBefore 数这个会话里算数的消息（口径和窗口起点一样）seq 小于 before 的有几条；before <= 0 是 0 条。
// 重建留痕用它数“窗口外丢了几条”（规格-提示词前缀稳定第八节）。skip 是这一批
func CountBefore(db *sql.DB, conversation string, before int64, skip []int64) (int, error) {
	return CountBetween(db, conversation, 0, before, skip)
}

// CountBetween 数这个会话里算数的消息里 seq 落在 [from, before) 的有几条；before <= from 是 0 条。
// 从上下文起点重建时用它把分隔之前的条数排除在外（那些是主人主动结束的，不是丢的）
func CountBetween(db *sql.DB, conversation string, from, before int64, skip []int64) (int, error) {
	if before <= from {
		return 0, nil
	}
	if skip == nil {
		skip = []int64{}
	}
	sk, _ := json.Marshal(skip)
	var n int
	err := db.QueryRow(`SELECT count(*) `+countableSQL+` AND seq >= ? AND seq < ?`, conversation, string(sk), from, before).Scan(&n)
	return n, err
}

// EpochFromSeq 是这份快照从第几条事件起往后读（压缩时的切点）；找不到报 sql.ErrNoRows
func EpochFromSeq(db *sql.DB, characterID, conversation, epochID string) (int64, error) {
	var from int64
	err := db.QueryRow(`SELECT from_seq FROM context_epochs WHERE id = ? AND conversation = ? AND character_id = ?`, epochID, conversation, characterID).Scan(&from)
	return from, err
}

// windowFrom 是 MessageWindow 的起点（带的第一条事件的 seq）；没有可带的是 0。起点怎么算只有这一份，WindowFloor 也走它。
// slack 把总数当成少了这么多条来算起点（只给 WindowFloor 留余量用，MessageWindow 传 0）
func windowFrom(db *sql.DB, conversation string, n int, skip []int64, slack int) (int64, error) {
	if n <= 0 {
		return 0, nil
	}
	if skip == nil {
		skip = []int64{}
	}
	sk, _ := json.Marshal(skip)
	const countable = countableSQL
	var total int
	if err := db.QueryRow(`SELECT count(*) `+countable, conversation, string(sk)).Scan(&total); err != nil || total == 0 {
		return 0, err
	}
	var from int64
	// 从最新往回数（最多扫 2n 行），不从最旧的往后数（大群要扫整个会话）
	if err := db.QueryRow(`SELECT seq `+countable+` ORDER BY seq DESC LIMIT 1 OFFSET ?`, conversation, string(sk), total-AnchoredStart(max(total-slack, 0), n)-1).Scan(&from); err != nil {
		return 0, err
	}
	return from, nil
}

// startSeq 是这个会话的上下文起点：最后一个“从这里开始新话题”分隔之后的第一条算数消息；没有分隔就是第一条（0 = 没有）。
// 本地私聊和主人的 QQ 私聊从它读到现在（规格-私聊从上下文起点重建 第二节），其余场合仍走锚定窗口。
func startSeq(db *sql.DB, conversation string) (int64, error) {
	brk, err := LastBreak(db, conversation)
	if err != nil {
		return 0, err
	}
	var from int64
	err = db.QueryRow(`SELECT coalesce(min(seq), 0) FROM events WHERE conversation = ? AND seq > ? AND kind = 'message' AND origin NOT IN (`+NotSaidSQL+`)`, conversation, brk).Scan(&from)
	return from, err
}

// StartSeq 是 startSeq 的只读入口：压缩任务开始和写库之前各读一次，起点变了（开了新话题）就不写这份快照
func StartSeq(db *sql.DB, conversation string) (int64, error) { return startSeq(db, conversation) }

// WindowFloorFromStart 是从起点重建的会话清理参考信息块的下限：有成功快照、而且切点比起点新时取切点，
// 否则取起点。切点之前的块已经被摘要覆盖、重建不会再插回，留着只会让库越攒越大（规格-私聊从上下文起点重建 第六节）。
func WindowFloorFromStart(db *sql.DB, characterID, conversation string) (int64, error) {
	start, err := startSeq(db, conversation)
	if err != nil || start == 0 {
		return 0, err
	}
	ep, err := LatestEpoch(db, characterID, conversation)
	if err != nil {
		return 0, err
	}
	if ep != nil && ep.FromSeq > start {
		return ep.FromSeq, nil
	}
	return start, nil
}

// WindowFloor 是这个会话重建时最早可能带到的一条事件的 seq：比它更早的事件，之后任何一次重建都不会再带（0 = 没有下限）。
// 锚定窗口的起点只往后走；有压缩快照时重建从快照的起点往后读，可能比窗口起点更早，取更早的那个。
// 参考信息块清理用它：起点之前的块不会再插回（规格-提示词前缀稳定 7.3）
func WindowFloor(db *sql.DB, characterID, conversation string, n int) (int64, error) {
	// 留一个跳点的余量：下一轮重建会把它那一批排除在计数之外，总数少几条，起点可能比现在落在上一个跳点
	from, err := windowFrom(db, conversation, n, nil, n)
	if err != nil || from == 0 {
		return 0, err
	}
	ep, err := LatestEpoch(db, characterID, conversation)
	if err != nil {
		return 0, err
	}
	if ep != nil && ep.FromSeq < from {
		from = ep.FromSeq
	}
	return from, nil
}

// AnchoredStart 是一共 total 条算数的消息、至少带 n 条时，从第几条（从 0 数）带起：n 的整数倍，留下的条数在 [n, 2n) 里；
// 不够 n 条从 0 起
func AnchoredStart(total, n int) int {
	if n <= 0 || total <= n {
		return 0
	}
	return (total - n) / n * n
}

// Epoch 是一份压缩快照（context_epochs 的一行，只取用得到的几列）
type Epoch struct {
	ID        string
	FromSeq   int64
	Summary   string
	CreatedAt int64
}

// LatestEpoch 只取当前角色在该会话的成功快照；同群其他角色的摘要不能当成自己的上下文。
// 同毫秒按 ID 取后生成的一份（规格第七节 A17）。
func LatestEpoch(db *sql.DB, characterID, conversation string) (*Epoch, error) {
	var e Epoch
	err := db.QueryRow(`SELECT id, from_seq, summary, created_at FROM context_epochs WHERE conversation = ? AND character_id = ? AND status = 'ok' ORDER BY created_at DESC, id DESC LIMIT 1`, conversation, characterID).
		Scan(&e.ID, &e.FromSeq, &e.Summary, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// ContextBreak 是"从这里开始新话题"那条通知的标记（api/event.ts CONTEXT_BREAK）
const ContextBreak = "context-break"

// LastBreak 是这个会话最后一个"从这里开始新话题"分隔的 seq（没有是 0）。
// 先让 SQLite 按 segments 过滤（坏 JSON 用 json_valid 挡住，取最后出现的 notice 键，和 encoding/json 认重键一样），
// 只读最后几条，再按 IsContextBreak 核一遍：数目、数组、空串、坏 JSON 这些形状和它判得一样，不能只靠 SQL 的类型比较
func LastBreak(db *sql.DB, conversation string) (int64, error) {
	rows, err := db.Query(`SELECT `+eventCols+` FROM events WHERE conversation = ? AND kind = 'notice'
		AND CASE WHEN json_valid(segments_json) THEN (
			SELECT j.value FROM json_each(events.segments_json) j
			WHERE j.key COLLATE NOCASE = 'notice' ORDER BY j.id DESC LIMIT 1
		) END = ?
		ORDER BY seq DESC`, conversation, ContextBreak)
	if err != nil {
		return 0, err
	}
	notices, err := scanEvents(rows)
	if err != nil {
		return 0, err
	}
	for _, e := range notices {
		if IsContextBreak(e) {
			return e.Seq, nil
		}
	}
	return 0, nil
}

// IsContextBreak：kind 是 notice、segments 里 notice 是 context-break
func IsContextBreak(e Event) bool {
	if e.Kind != "notice" || !e.Segments.Valid || e.Segments.String == "" {
		return false
	}
	var seg struct {
		Notice any `json:"notice"`
	}
	if json.Unmarshal([]byte(e.Segments.String), &seg) != nil {
		return false
	}
	return seg.Notice == ContextBreak
}

// SummaryHeader 是快照那条消息的开头（util/compact.ts）
const SummaryHeader = `（前面的聊天已经整理成下面的摘要，原话不再给你。接着往下聊，不要提到"摘要""压缩"这些词。）`

// Message 是给她看的一条：Role 是 user 或 assistant；Seq 是它来自哪个事件（快照那条是 0）
type Message struct {
	Role      string
	Text      string
	Timestamp int64
	Seq       int64
}

// 定期任务那一轮的话的两个不显示的来源：待分类（这一轮还没结束，不知道要不要显示）和 [无事]（永久隐藏）。
// 它们和"没说出口的"一样不进历史、不进记忆、不显示；分了类、有内容的会改回可见来源
const (
	OriginLoopPending = "automation:loop-pending"
	OriginLoopQuiet   = "automation:loop-quiet"
)

// OriginRemoteApproval 是主人 QQ 私聊审批（规格-主人QQ私聊审批）的卡片、结局，以及被截走的“同意”“拒绝”回复行的来源：
// 界面照常显示，模型历史、补给下一批的自动行、压缩、记忆证据都看不到它们。
const OriginRemoteApproval = "automation:remote-approval"

// 话题线里宿主写的两种 system 事件的来源（规格-主动联系 第六节）：导演醒来行、主人回复转述
const (
	OriginProactiveWake  = "proactive:wake"
	OriginProactiveRelay = "proactive:relay"
)

// NotSaidSQL 是"没说出口的"各来源写成 SQL 的 IN 列表，查询里统一用它，不各写一份
const NotSaidSQL = `'automation:dry-run', 'automation:self-skip', 'automation:loop-pending', 'automation:loop-quiet', 'automation:remote-approval'`

// 她没说出口的（试运行、看完不说、定期任务还没分类或回了 [无事]）：不进历史
func notSaid(e Event) bool {
	switch e.Origin {
	case "automation:dry-run", "automation:self-skip", OriginLoopPending, OriginLoopQuiet, OriginRemoteApproval:
		return true
	}
	return false
}

// NotSaid 是她没说出口的那一行（试运行、看完不说）：不进历史，压缩找切点、数原文时也不算
func NotSaid(e Event) bool { return notSaid(e) }

// breakSince：这个会话 fromSeq 起有没有"从这里开始新话题"的分隔
func breakSince(db *sql.DB, conversation string, fromSeq int64) (bool, error) {
	rows, err := db.Query(`SELECT `+eventCols+` FROM events WHERE conversation = ? AND seq >= ? AND kind = 'notice'`, conversation, fromSeq)
	if err != nil {
		return false, err
	}
	notices, err := scanEvents(rows)
	if err != nil {
		return false, err
	}
	for _, e := range notices {
		if IsContextBreak(e) {
			return true, nil
		}
	}
	return false, nil
}

// For 是重建实例时给她看的历史（historyFor）：snapshot 是快照那条（没有就是 nil），skip 是这一轮单独追加的那几条
func For(db *sql.DB, characterID, conversation string, recent int, skip []int64) (snapshot *Message, msgs []Message, epochID string, err error) {
	win, err := MessageWindow(db, conversation, recent, skip)
	if err != nil {
		return nil, nil, "", err
	}
	brk := -1
	for i := len(win) - 1; i >= 0; i-- {
		if IsContextBreak(win[i]) {
			brk = i
			break
		}
	}
	ep, err := LatestEpoch(db, characterID, conversation)
	if err != nil {
		return nil, nil, "", err
	}
	// 快照之后有过"新话题"分隔：快照不用，只带分隔之后的。分隔不在最近这几条里也要认（规格第七节 A4：Bun 只在这几条里找，
	// 分隔更早、却比快照新时照样用快照，分隔之前的原文全带进来；Bun 冻结不改）
	if ep != nil {
		if brk >= 0 && win[brk].Seq >= ep.FromSeq {
			ep = nil
		} else if brk < 0 {
			later, err := breakSince(db, conversation, ep.FromSeq)
			if err != nil {
				return nil, nil, "", err
			}
			if later {
				ep = nil
			}
		}
	}
	events := win[brk+1:]
	if ep != nil {
		if events, err = EventsSince(db, conversation, ep.FromSeq, eventsPageSize); err != nil {
			return nil, nil, "", err
		}
		snapshot = &Message{Role: "user", Text: SummaryHeader + "\n" + textclean.Trim(ep.Summary), Timestamp: ep.CreatedAt}
		epochID = ep.ID
	}
	return snapshot, messagesOf(events, characterID, skip), epochID, nil
}

// ForFromStart 是从上下文起点重建的历史：起点由 StartSeq 派生，读到最新；有比起点新的成功快照时照快照分支
// （摘要 + 切点之后的全部）。没有算数消息时返回空。
func ForFromStart(db *sql.DB, characterID, conversation string, skip []int64) (snapshot *Message, msgs []Message, epochID string, err error) {
	start, err := startSeq(db, conversation)
	if err != nil || start == 0 {
		return nil, nil, "", err
	}
	ep, err := LatestEpoch(db, characterID, conversation)
	if err != nil {
		return nil, nil, "", err
	}
	from := start
	if ep != nil && ep.FromSeq > start {
		from = ep.FromSeq
		snapshot = &Message{Role: "user", Text: SummaryHeader + "\n" + textclean.Trim(ep.Summary), Timestamp: ep.CreatedAt}
		epochID = ep.ID
	}
	events, err := EventsSince(db, conversation, from, eventsPageSize)
	if err != nil {
		return nil, nil, "", err
	}
	return snapshot, messagesOf(events, characterID, skip), epochID, nil
}

// messagesOf 把事件转成给她看的那几条（skip 是这一轮单独追加的、不说出口的不算）
func messagesOf(events []Event, characterID string, skip []int64) []Message {
	skipped := map[int64]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	var msgs []Message
	for _, e := range events {
		if skipped[e.Seq] || notSaid(e) {
			continue
		}
		if m, ok := toMessage(e, characterID); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs
}

// After 是这个会话 afterSeq 之后给她看的消息（按 seq 正序；没说出口的、不给她看的不算），实例追上别的档说过的话用（规格-Go后台 8.1）
func After(db *sql.DB, characterID, conversation string, afterSeq int64) ([]Message, error) {
	events, err := EventsSince(db, conversation, afterSeq+1, eventsPageSize)
	if err != nil {
		return nil, err
	}
	var msgs []Message
	for _, e := range events {
		if notSaid(e) {
			continue
		}
		if m, ok := toMessage(e, characterID); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs, nil
}

// Of 是这个会话里这几条事件给她看的样子（按 seq 正序；没说出口的、不给她看的不算）：
// 渠道回合交回写库结果以后，实例里那一段照库重写用（规格-渠道中间件第九节第 1 条：以库为准）
func Of(db *sql.DB, characterID, conversation string, seqs []int64) ([]Message, error) {
	if len(seqs) == 0 {
		return nil, nil
	}
	js, _ := json.Marshal(seqs)
	rows, err := db.Query(`SELECT `+eventCols+` FROM events WHERE conversation = ? AND seq IN (SELECT value FROM json_each(?)) ORDER BY seq`, conversation, string(js))
	if err != nil {
		return nil, err
	}
	events, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}
	var msgs []Message
	for _, e := range events {
		if notSaid(e) {
			continue
		}
		if m, ok := toMessage(e, characterID); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs, nil
}

// MaxSeq 是这个会话现在最新的事件（没有是 0）
func MaxSeq(db *sql.DB, conversation string) (int64, error) {
	var n sql.NullInt64
	err := db.QueryRow(`SELECT max(seq) FROM events WHERE conversation = ?`, conversation).Scan(&n)
	return n.Int64, err
}

// 一行事件给她看的样子；本地群中其他角色也是有独立身份的说话者。
func toMessage(e Event, characterID string) (Message, bool) {
	if e.Kind != "message" {
		return Message{}, false
	}
	if e.Channel == "console" && strings.HasPrefix(e.Conversation, "console:group:") {
		if e.ActorKind == "owner" {
			return Message{Role: "user", Text: "[所有者] " + EscapeUserText(e.Text), Timestamp: e.CreatedAt, Seq: e.Seq}, true
		}
		if e.ActorKind == "character" && e.ActorID != characterID {
			return Message{Role: "user", Text: "[角色 " + EscapeUserText(e.ActorID) + "] " + EscapeUserText(e.Text), Timestamp: e.CreatedAt, Seq: e.Seq}, true
		}
	}
	if e.ActorKind == "owner" || e.ActorKind == "human" {
		return Message{Role: "user", Text: SaidBy(e), Timestamp: e.CreatedAt, Seq: e.Seq}, true
	}
	// 话题线里的醒来行和主人回复转述是 system 事件（规格-主动联系 第六节）：只在线里、只认这两个来源，渲染成用户消息；
	// 别处的 system 行（通知条等）照旧不进模型历史。这样线的实例重建后历史里还有这些行
	if e.ActorKind == "system" && e.ActorID == audience.ThreadActor && audience.IsThread(e.Conversation) && (e.Origin == OriginProactiveWake || e.Origin == OriginProactiveRelay) {
		return Message{Role: "user", Text: e.Text, Timestamp: e.CreatedAt, Seq: e.Seq}, true
	}
	if e.ActorKind != "character" || e.ActorID != characterID {
		return Message{}, false
	}
	if e.Origin == "human" || e.Origin == "external" {
		return Message{Role: "user", Text: "（这条是从你的号发出去的，但不是你说的——可能是所有者登录你的号手动发的，也可能是以前别的程序用这个号说的）" + SaidBy(e), Timestamp: e.CreatedAt, Seq: e.Seq}, true
	}
	if auto, ok := strings.CutPrefix(e.Origin, "automation:"); ok {
		return Message{Role: "user", Text: "（这条是系统替你自动发的（" + auto + "），不是你说的）" + e.Text, Timestamp: e.CreatedAt, Seq: e.Seq}, true
	}
	return Message{Role: "assistant", Text: e.Text, Timestamp: e.CreatedAt, Seq: e.Seq}, true
}

// SaidBy 是别人说的话给她看的样子（util/event.ts saidBy）：主窗口给原文；平台来的用排好、转义过的那一行
func SaidBy(e Event) string {
	if e.Channel == "console" {
		if _, parts := WhisperOf(e); parts != nil {
			return SaidWhisper(parts)
		}
		return Said(partsOf(e), e.Text)
	}
	var meta struct {
		Line any `json:"line"`
		Seen any `json:"seen"`
	}
	if e.Segments.Valid && e.Segments.String != "" {
		json.Unmarshal([]byte(e.Segments.String), &meta)
	}
	line := meta.Line
	if s, ok := meta.Seen.(string); ok && s != "" {
		line = s
	}
	if s, ok := line.(string); ok && s != "" {
		return s
	}
	return EscapeUserText(e.Text)
}

// partsOf 取主人消息的分块存档（segments.local_parts）。含引用块时 reply_to 必须等于第一个引用块的 id，
// 对不上、片段为空、整份没有内容都当没有，不凭残缺的存档拼结构。
func partsOf(e Event) []Part {
	if !e.Segments.Valid || e.Segments.String == "" {
		return nil
	}
	var seg struct {
		Parts []Part `json:"local_parts"`
	}
	if json.Unmarshal([]byte(e.Segments.String), &seg) != nil || len(seg.Parts) == 0 {
		return nil
	}
	first := ""
	for _, p := range seg.Parts {
		if p.Quote == nil {
			continue
		}
		if p.Quote.ID == "" || p.Quote.Text == "" {
			return nil
		}
		if first == "" {
			first = p.Quote.ID
		}
	}
	if first != "" && (!e.ReplyTo.Valid || e.ReplyTo.String != first) {
		return nil
	}
	return seg.Parts
}

var newlineRE = regexp.MustCompile(`\r?\n`)

// EscapeUserText：别人的原文里的方括号换全角、"↪"换掉、换行后缩进（util/escape.ts），伪造不出框架头和别人的话
func EscapeUserText(s string) string {
	s = strings.NewReplacer("[", "［", "]", "］", "↪", "→").Replace(s)
	return newlineRE.ReplaceAllString(s, "\n  ")
}
