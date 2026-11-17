// Package memsource 是记忆包读事件的接口（memory.EventSource，internal/memory/ports.go）在事件库上的实现（规格-记忆-v2 13.2.2、
// 13.2.12 的 W0）。每个角色一个视图：Seen 按这个角色的 observations 算。
// 聊天记录按平台分库以后（规格-聊天记录按平台分库），Source 是一个平台库的视图，Multi 按会话选库、跨库合并（multi.go）。
//
// 事件库可能是 Bun 写的真实库，也可能是 Go 渠道线写的新库，下面几样各有两种写法，两种都认：
//   - 发言时的名字：Go 存在 segments.from（{nick, card}）；Bun 没存，按 qq_people 现在的名字（这个群的群名片，没有用昵称）。取不到给空，不退回号码
//   - 补录、自检：Go 在 segments.tags 里；Bun 是 segments 顶层的 backfill、selftest
//   - 撤回：一条 kind = notice 的事件，reply_to 指向被撤回那条的 id；segments.notice 在 Go 是 recall，在 Bun 是 friend_recall、group_recall
//   - 没发出去：Go 看发送计划（outbound_plans，只有 Go 库有）是试运行、失败、中止；Bun 是 segments.unsent 有值且没有 partial
//     （发了一半、不知道发没发的算说过）。试运行、她看完决定不说（origin 是 automation:dry-run、automation:self-skip）两种库都按
//     origin 认（Bun agent.ts 的 NOT_SAID、Go 的 history.NotSaid）。主窗口说到一半被停下的（segments 只有 aborted）主人在屏幕上看到了，算说过
//   - 引用：被引用的那条撤回了，引用整条不给（撤回的话不进整理，开头几个字也不带出来）
//   - 图片：Go 的段是 kind = image / emoji、她发的在 parts 里；Bun 的段是 type = image（sticker 也算，商城表情不算）。
//     带 import_meta.legacy_segments 的是导入前的旧段（规格-从Bun导入历史 第五节）：按旧段用 Bun 的规则取槽，
//     缺 hash 时只从对应的中立 image 补；商城图、mface 转成 emoji 后仍不占槽
//
// 只给事件、不替记忆过滤（B8）：Events 里被丢的行也照样返回（游标要越过它们）；Find 才按约定去掉撤回的、没发出去的。
// 一页里要补的（谁看见过、撤回、引用、名字、图片描述、没发出去）各用一条 IN 查询批量补，不一行一查
package memsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"sumika-native/internal/history"
	"sumika-native/internal/memory"
	"sumika-native/internal/store"
)

// quoteRunes 是被引用那条给多少字（记忆取前 20 个码点，多给一点不碍事）
const quoteRunes = 60

// findPage 是 Find 一次从库里取多少条候选（撤回的、没发出去的在这之后才去掉，取不满就再取下一页）
const findPage = 50

// Source 是一个角色看事件库的视图
type Source struct {
	db        *sql.DB
	character string
	plans     bool // 库里有 outbound_plans（Go 渠道线的发送计划；Bun 的真实库没有）
	people    bool // 库里有 qq_people（分库以后只有 QQ 库有，本地库查名字要跳过这张表）
}

var _ memory.EventSource = (*Source)(nil)

// New 建这个角色的视图。建的时候查一次库里有哪些可选表，之后按它选读法
func New(st *store.Store, characterID string) (*Source, error) {
	return OpenDB(st.Events, characterID)
}

// OpenDB 用一个已经打开的聊天库建视图。导入的 stage 库不是 store.Store 那一套多平台连接，测试和导入对拍走这里。
func OpenDB(db *sql.DB, characterID string) (*Source, error) {
	return newSource(db, characterID)
}

func newSource(db *sql.DB, characterID string) (*Source, error) {
	if characterID == "" {
		return nil, fmt.Errorf("memsource：没给角色")
	}
	has, err := tables(db, "outbound_plans", "qq_people")
	if err != nil {
		return nil, err
	}
	return &Source{db: db, character: characterID, plans: has["outbound_plans"], people: has["qq_people"]}, nil
}

// tables 查这个库里有哪些表（只认给出的这几个名字）
func tables(db *sql.DB, names ...string) (map[string]bool, error) {
	rs, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name IN (`+placeholders(len(names))+`)`, anys(names)...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := map[string]bool{}
	for rs.Next() {
		var name string
		if err := rs.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rs.Err()
}

const cols = `e.seq, e.id, e.conversation, e.actor_kind, e.actor_id, COALESCE(e.origin, ''), e.kind, e.text, e.segments_json, COALESCE(e.reply_to, ''), e.created_at`

// row 是读出来、还没补齐的一条
type row struct {
	ev      memory.Event
	id      string
	replyTo string
	seg     segments
}

// segments 是 segments_json 里这里要用的几样（两种写法都在）
type segments struct {
	Backfill json.RawMessage `json:"backfill"` // Bun
	Selftest json.RawMessage `json:"selftest"` // Bun
	Unsent   json.RawMessage `json:"unsent"`   // Bun：没发出去的原因
	Partial  json.RawMessage `json:"partial"`  // Bun：发了一半或不知道发没发
	Tags     []string        `json:"tags"`     // Go：backfill、selftest、stranger、fromSelf
	From     *struct {
		Nick string `json:"nick"`
		Card string `json:"card"`
	} `json:"from"` // Go：发言当时的名字
	Segs  []map[string]json.RawMessage `json:"segs"` // Bun 的段带 type，Go 的带 kind
	Parts []struct {
		Kind string `json:"kind"`
		Ref  string `json:"ref"`
	} `json:"parts"` // Go 出口：她发的段，图片的 ref 是哈希
	// ImportMeta.Legacy 是导入时留下的转换前旧段。键在才按旧规则取槽；没有这字段的新消息照下面的 kind 收
	ImportMeta *struct {
		Legacy []map[string]json.RawMessage `json:"legacy_segments"`
	} `json:"import_meta"`
}

func truthy(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "false" && s != `""` && s != "0"
}

func (s segments) tagged(tag string) bool {
	for _, t := range s.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func str(m map[string]json.RawMessage, k string) string {
	var v string
	json.Unmarshal(m[k], &v)
	return v
}

// images 是这一条里每个占位（[图片]、[表情包]）对应的哈希，按先后；空串是没存下来、没有哈希的图。
// 带 import_meta.legacy_segments 的事件（从 Bun 导入的历史）按旧段用 Bun 同一规则取槽：type=image 且非 market，
// skipped 置空，mface 不收——商城表情和 mface 转成 emoji 后正文里仍只有"[商城表情：…]"，不是占位，收进去会让
// 后面第一个"[图片]"配到商城哈希上，没有识图行时整句一直等图（规格-从Bun导入历史 第五节）
func (s segments) images() []string {
	if s.ImportMeta != nil && s.ImportMeta.Legacy != nil {
		out := bunImageSlots(s.ImportMeta.Legacy)
		neutralCount := 0
		for _, g := range s.Segs {
			if str(g, "kind") == "image" {
				neutralCount++
			}
		}
		// 旧段第 n 个占槽的 image 对中立段第 n 个 kind=image；数目不符就不猜位置。
		if len(out) != neutralCount {
			return out
		}
		n, i := 0, 0
		for _, g := range s.ImportMeta.Legacy {
			if str(g, "type") != "image" || truthy(g["market"]) {
				continue
			}
			for str(s.Segs[i], "kind") != "image" {
				i++
			}
			if out[n] == "" && !truthy(g["skipped"]) {
				out[n] = str(s.Segs[i], "hash")
			}
			n++
			i++
		}
		return out
	}
	var out []string
	for _, g := range s.Segs {
		switch {
		case str(g, "kind") == "image" || str(g, "kind") == "emoji": // Go
			out = append(out, str(g, "hash"))
		case str(g, "type") == "image" && !truthy(g["market"]): // Bun：商城表情写成"[商城表情：…]"，不是占位
			h := str(g, "hash")
			if truthy(g["skipped"]) {
				h = ""
			}
			out = append(out, h)
		}
	}
	for _, p := range s.Parts {
		if p.Kind == "image" || p.Kind == "emoji" {
			out = append(out, p.Ref)
		}
	}
	return out
}

// bunImageSlots 是 Bun 取识图槽的规则：type=image 且非 market；skipped 置空；mface 不是占位
func bunImageSlots(segs []map[string]json.RawMessage) []string {
	var out []string
	for _, g := range segs {
		if str(g, "type") != "image" || truthy(g["market"]) {
			continue
		}
		h := str(g, "hash")
		if truthy(g["skipped"]) {
			h = ""
		}
		out = append(out, h)
	}
	return out
}

func scan(rs *sql.Rows) ([]*row, error) {
	defer rs.Close()
	var out []*row
	for rs.Next() {
		r := &row{}
		var seg sql.NullString
		if err := rs.Scan(&r.ev.Seq, &r.id, &r.ev.Conversation, &r.ev.ActorKind, &r.ev.ActorID, &r.ev.Origin, &r.ev.Kind, &r.ev.Text, &seg, &r.replyTo, &r.ev.At); err != nil {
			return nil, err
		}
		if seg.Valid {
			json.Unmarshal([]byte(seg.String), &r.seg) // 坏的 JSON 当没有（Go 的 backfillSQL 也这样）
		}
		r.ev.Backfill = truthy(r.seg.Backfill) || r.seg.tagged("backfill")
		r.ev.Selftest = truthy(r.seg.Selftest) || r.seg.tagged("selftest")
		r.ev.Unsent = truthy(r.seg.Unsent) && !truthy(r.seg.Partial) || r.ev.Origin == "automation:dry-run" || r.ev.Origin == "automation:self-skip" || r.ev.Origin == history.OriginLoopPending || r.ev.Origin == history.OriginLoopQuiet || r.ev.Origin == history.OriginRemoteApproval
		out = append(out, r)
	}
	return out, rs.Err()
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anys[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// fill 给一页补齐 Seen、Revoked、Unsent（Go 库）、Quote（被引用的撤回了就不给）、ActorName、Images
func (s *Source) fill(ctx context.Context, rows []*row) error {
	if len(rows) == 0 {
		return nil
	}
	seqs := make([]int64, len(rows))
	for i, r := range rows {
		seqs[i] = r.ev.Seq
	}
	seen, err := s.seqSet(ctx, `SELECT event_seq FROM observations WHERE character_id = ? AND event_seq IN (`+placeholders(len(seqs))+`)`, append([]any{s.character}, anys(seqs)...)...)
	if err != nil {
		return err
	}
	revoked, err := s.revokedIDs(ctx, rows)
	if err != nil {
		return err
	}
	unsent := map[int64]bool{}
	if s.plans {
		if unsent, err = s.seqSet(ctx, `SELECT event_seq FROM outbound_plans WHERE event_seq IN (`+placeholders(len(seqs))+`) AND (kind = 'dry-run' OR state IN ('failed', 'aborted'))`, anys(seqs)...); err != nil {
			return err
		}
	}
	quotes, err := s.quotes(ctx, rows)
	if err != nil {
		return err
	}
	quoted := make([]*row, 0, len(quotes))
	for _, q := range quotes {
		quoted = append(quoted, q)
	}
	quoteRevoked, err := s.revokedIDs(ctx, quoted)
	if err != nil {
		return err
	}
	for _, r := range rows {
		r.ev.Seen, r.ev.Revoked = seen[r.ev.Seq], revoked[r.id]
		r.ev.Unsent = r.ev.Unsent || unsent[r.ev.Seq]
		if q, ok := quotes[r.replyTo]; ok && r.ev.Kind == "message" && !quoteRevoked[q.id] {
			r.ev.Quote = q.quote()
		}
	}
	if err := s.names(ctx, append(rows[:len(rows):len(rows)], quoted...)); err != nil {
		return err
	}
	for _, r := range rows {
		if q, ok := quotes[r.replyTo]; ok && r.ev.Quote != nil {
			r.ev.Quote.ActorName = q.ev.ActorName
		}
	}
	return s.images(ctx, rows)
}

func (s *Source) seqSet(ctx context.Context, q string, args ...any) (map[int64]bool, error) {
	rs, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := map[int64]bool{}
	for rs.Next() {
		var n int64
		if err := rs.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rs.Err()
}

// revokedIDs：这几条里哪些被撤回了（按会话各查一次：撤回通知在被撤回那条之后、同一个会话里，按 id 连）
func (s *Source) revokedIDs(ctx context.Context, rows []*row) (map[string]bool, error) {
	byConv := map[string][]*row{}
	for _, r := range rows {
		byConv[r.ev.Conversation] = append(byConv[r.ev.Conversation], r)
	}
	out := map[string]bool{}
	for conv, rs := range byConv {
		from := rs[0].ev.Seq
		ids := make([]any, len(rs))
		for i, r := range rs {
			from = min(from, r.ev.Seq)
			ids[i] = r.id
		}
		q, err := s.db.QueryContext(ctx, `SELECT DISTINCT reply_to FROM events WHERE conversation = ? AND seq > ? AND kind = 'notice' AND reply_to IN (`+placeholders(len(ids))+`)
			AND json_valid(segments_json) AND json_extract(segments_json, '$.notice') IN ('recall', 'friend_recall', 'group_recall')`, append([]any{conv, from}, ids...)...)
		if err != nil {
			return nil, err
		}
		for q.Next() {
			var id string
			if err := q.Scan(&id); err != nil {
				q.Close()
				return nil, err
			}
			out[id] = true
		}
		if err := q.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// quotes 是这一页引用到的那几条（按 events.id）
func (s *Source) quotes(ctx context.Context, rows []*row) (map[string]*row, error) {
	var ids []any
	for _, r := range rows {
		if r.replyTo != "" && r.ev.Kind == "message" {
			ids = append(ids, r.replyTo)
		}
	}
	out := map[string]*row{}
	if len(ids) == 0 {
		return out, nil
	}
	rs, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM events e WHERE e.id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, err
	}
	got, err := scan(rs)
	if err != nil {
		return nil, err
	}
	for _, q := range got {
		out[q.id] = q
	}
	return out, nil
}

func (r *row) quote() *memory.Quote {
	text := r.ev.Text
	if rs := []rune(text); len(rs) > quoteRunes {
		text = string(rs[:quoteRunes])
	}
	return &memory.Quote{ActorKind: r.ev.ActorKind, ActorID: r.ev.ActorID, Text: text}
}

// names 补发言时的名字：只给人（主人写"主人"、她写"你"，记忆不看名字）。Go 写的事件用当时存下的群名片、昵称；
// Bun 写的按 qq_people 现在的名字（这个群的群名片，没有用昵称；私聊只看昵称）。取不到给空。
// 分库以后本地库没有 qq_people（规格-聊天记录按平台分库 第三节）：那时只认 Go 存下的名字，别的留空，不报错
func (s *Source) names(ctx context.Context, rows []*row) error {
	var uids []any
	need := map[string]bool{}
	for _, r := range rows {
		if r.ev.ActorKind != "human" {
			continue
		}
		if f := r.seg.From; f != nil && (f.Card != "" || f.Nick != "") {
			r.ev.ActorName = f.Card
			if r.ev.ActorName == "" {
				r.ev.ActorName = f.Nick
			}
			continue
		}
		if uid, ok := strings.CutPrefix(r.ev.ActorID, "qq:"); ok && uid != "" && !need[uid] {
			need[uid] = true
			uids = append(uids, uid)
		}
	}
	if len(uids) == 0 || !s.people {
		return nil
	}
	rs, err := s.db.QueryContext(ctx, `SELECT uid, COALESCE(nickname, ''), cards_json FROM qq_people WHERE uid IN (`+placeholders(len(uids))+`)`, uids...)
	if err != nil {
		return err
	}
	type person struct {
		nick  string
		cards map[string]string
	}
	people := map[string]person{}
	for rs.Next() {
		var uid, nick, cards string
		if err := rs.Scan(&uid, &nick, &cards); err != nil {
			rs.Close()
			return err
		}
		p := person{nick: nick}
		json.Unmarshal([]byte(cards), &p.cards)
		people[uid] = p
	}
	if err := rs.Close(); err != nil {
		return err
	}
	for _, r := range rows {
		if r.ev.ActorKind != "human" || r.ev.ActorName != "" {
			continue
		}
		uid, _ := strings.CutPrefix(r.ev.ActorID, "qq:")
		p, ok := people[uid]
		if !ok {
			continue
		}
		if _, gid, isGroup := strings.Cut(r.ev.Conversation, ":group:"); isGroup && p.cards[gid] != "" {
			r.ev.ActorName = p.cards[gid]
		} else {
			r.ev.ActorName = p.nick
		}
	}
	return nil
}

// images 补每张图的识图结果。Go 现在没有识图，media_descriptions 只有 Bun 在写：不照 Bun 算"当前版本"（模型、提示词哈希、
// 预处理版本、注解，和它的识图绑死），取这张图最新一行看成功的描述（最新的一次看失败、更早有成功的，用成功那行）；
// 有任何一行（看完或看失败）就算就绪。主人改了识图提示词以后，Bun 重看之前用的是旧描述。等 Go 有了自己的识图再按它的版本取
// （和记忆线对过，2026-10-27）。没有哈希的图（没存下来）当就绪、没有描述，免得整批卡在它前面
func (s *Source) images(ctx context.Context, rows []*row) error {
	var hashes []any
	seen := map[string]bool{}
	per := make([][]string, len(rows))
	for i, r := range rows {
		per[i] = r.seg.images()
		for _, h := range per[i] {
			if h != "" && !seen[h] {
				seen[h] = true
				hashes = append(hashes, h)
			}
		}
	}
	type desc struct {
		brief string
		at    int64
	}
	got := map[string]*desc{} // 有这张图的任何一行（看完或看失败）就在这里：就绪
	if len(hashes) > 0 {
		rs, err := s.db.QueryContext(ctx, `SELECT hash, ok, COALESCE(brief, ''), at FROM media_descriptions WHERE hash IN (`+placeholders(len(hashes))+`)`, hashes...)
		if err != nil {
			return err
		}
		for rs.Next() {
			var h, brief string
			var ok, at int64
			if err := rs.Scan(&h, &ok, &brief, &at); err != nil {
				rs.Close()
				return err
			}
			d := got[h]
			if d == nil {
				d = &desc{at: -1}
				got[h] = d
			}
			if ok == 1 && at > d.at {
				d.brief, d.at = brief, at
			}
		}
		if err := rs.Close(); err != nil {
			return err
		}
	}
	for i, r := range rows {
		for _, h := range per[i] {
			img := memory.Image{Hash: h, Ready: h == ""}
			if d := got[h]; d != nil {
				img.Ready, img.Brief = true, d.brief
			}
			r.ev.Images = append(r.ev.Images, img)
		}
	}
	return nil
}

func events(rows []*row) []memory.Event {
	out := make([]memory.Event, len(rows))
	for i, r := range rows {
		out[i] = r.ev
	}
	return out
}

// Events 取这个会话 afterSeq 之后最早的 limit 条，按 seq 升序（memory.EventSource）。不过滤
func (s *Source) Events(ctx context.Context, conv string, afterSeq int64, limit int) ([]memory.Event, error) {
	rs, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM events e WHERE e.conversation = ? AND e.seq > ? ORDER BY e.seq LIMIT ?`, conv, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	rows, err := scan(rs)
	if err != nil {
		return nil, err
	}
	if err := s.fill(ctx, rows); err != nil {
		return nil, err
	}
	return events(rows), nil
}

// Revoked 按 seq 问这个会话里这几条现在撤回了没有；库里没有的 seq 不在返回里。
// seq 只在一个库里唯一，所以调用方按会话选好了库、会话也带过来（规格-聊天记录按平台分库 第四节）
func (s *Source) Revoked(ctx context.Context, conv string, seqs []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(seqs) == 0 {
		return out, nil
	}
	rs, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM events e WHERE e.conversation = ? AND e.seq IN (`+placeholders(len(seqs))+`)`, append([]any{conv}, anys(seqs)...)...)
	if err != nil {
		return nil, err
	}
	rows, err := scan(rs)
	if err != nil {
		return nil, err
	}
	revoked, err := s.revokedIDs(ctx, rows)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ev.Seq] = revoked[r.id]
	}
	return out, nil
}

// LastSaid 取这个会话里她看到的最近一句人说的话（kind message、origin owner 或 peer；Bun apply 里的同一条查询，不滤补录、自检）
func (s *Source) LastSaid(ctx context.Context, conv string) (memory.Event, bool, error) {
	rs, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM events e JOIN observations o ON o.event_seq = e.seq AND o.character_id = ?
		WHERE e.conversation = ? AND e.kind = 'message' AND e.origin IN ('owner', 'peer') ORDER BY e.seq DESC LIMIT 1`, s.character, conv)
	if err != nil {
		return memory.Event{}, false, err
	}
	rows, err := scan(rs)
	if err != nil || len(rows) == 0 {
		return memory.Event{}, false, err
	}
	if err := s.fill(ctx, rows); err != nil {
		return memory.Event{}, false, err
	}
	return rows[0].ev, true, nil
}

// Find 在她看到过的事件里找原话：conv 空是所有会话，like 各项按 LIKE ? ESCAPE '\' 全部匹配。
// 全文索引缩小候选，原 LIKE 复核转义和大小写；按 (created_at, seq) 倒序，Go 过滤撤回及未发出。
func (s *Source) Find(ctx context.Context, conv string, like []string, limit int) ([]memory.Event, error) {
	return s.find(ctx, findScope{conversation: conv}, like, limit)
}

// Conversations 列出这个角色看到过事件的会话，各带现在最新的一条事件的 seq 和时间（会话里全部的，不只她看见的），最近有动静的在前。
// LastAt 取 LastSeq 那条的 created_at（不是 MAX(created_at)：补录的行可以更晚进库、时间更早），跨库合并时按它排
func (s *Source) Conversations(ctx context.Context) ([]memory.ConvHead, error) {
	rs, err := s.db.QueryContext(ctx, `SELECT e.conversation, MAX(e.seq),
		(SELECT x.created_at FROM events x WHERE x.conversation = e.conversation ORDER BY x.seq DESC LIMIT 1)
		FROM events e
		WHERE e.conversation IN (SELECT DISTINCT x.conversation FROM observations o JOIN events x ON x.seq = o.event_seq WHERE o.character_id = ?)
		GROUP BY e.conversation ORDER BY MAX(e.seq) DESC`, s.character)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []memory.ConvHead
	for rs.Next() {
		var h memory.ConvHead
		if err := rs.Scan(&h.Conversation, &h.LastSeq, &h.LastAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rs.Err()
}
