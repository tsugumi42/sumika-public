// Package whisperlog 是右侧栏“对话日志”的投影（规格-群聊引用和路由 第十五节）：对某个来源会话，按时间列出主人指过的悄悄话、
// 每位被指角色的安排、她处理时的思考和工具和记忆写入、她叫起的子任务和返回、处理记录。
//
// 全是现有记录的投影，只读，不写不改任何来源表；唯一新存的是 trace 库里的 whisper_links（哪条悄悄话引起了哪一轮）。
// 列表只带摘要，全文用 Detail 点开才读。聊天区读的是来源会话的事件，悄悄话不在里面，这里也不往里写任何东西。
package whisperlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/audience"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/history"
	"sumika-native/internal/store"
)

// 摘要的字数上限；被引原文在列表里最多三行。
const (
	summaryRunes = 100
	quoteRunes   = 160
	quoteLines   = 3
	// batchGroup 是一次读几批：读到条数够一页为止，批次不切开。
	batchGroup = 6
)

var (
	// ErrBadRequest 说请求不合法（会话为空、页大小越界）。
	ErrBadRequest = errors.New("日志请求不合法")
	// ErrNoEntry 说没有这条（或它不属于这个来源会话：不区分，不泄露别的会话有没有）。
	ErrNoEntry = errors.New("没有这条日志")
)

// Reader 读日志。Position 说排队中的某一份前面还有几个（可为 nil）。
type Reader struct {
	St       *store.Store
	Position func(batch, character string) (int, bool)
}

// Request 是读一页：来源会话、游标、页大小、可选只看一批。
type Request struct {
	Source string
	Cursor string
	Limit  int
	Batch  string
}

type group struct {
	batch string
	links []store.WhisperLink
}

// Read 读一页。条目按时间从早到晚；Next 是更早一页的游标。
func (r *Reader) Read(ctx context.Context, req Request) (ctlproto.LogPage, error) {
	if r.St == nil || req.Source == "" || audience.IsWhisper(req.Source) {
		return ctlproto.LogPage{}, ErrBadRequest
	}
	limit := req.Limit
	switch {
	case limit == 0:
		limit = ctlproto.LogPageDefault
	case limit < 0 || limit > ctlproto.LogPageMax:
		return ctlproto.LogPage{}, fmt.Errorf("%w：每页条数要在 1 到 %d 之间", ErrBadRequest, ctlproto.LogPageMax)
	}
	cur, err := store.ParseLogCursor(req.Cursor, req.Source)
	if err != nil {
		return ctlproto.LogPage{}, err
	}
	var page ctlproto.LogPage
	var chunks [][]ctlproto.LogEntry
	total := 0
	for total < limit {
		links, err := r.St.WhisperLinkBatches(ctx, req.Source, cur, batchGroup)
		if err != nil {
			return ctlproto.LogPage{}, err
		}
		if len(links) == 0 {
			break
		}
		groups := groupLinks(links)
		// 一个批次一个批次地并进页里：够一页就停，批次不切开
		for _, g := range groups {
			if req.Batch != "" && g.batch != req.Batch {
				cur = store.LogCursor{Source: req.Source, At: g.links[0].CreatedAt, Batch: g.batch}
				continue
			}
			es, running, err := r.project(ctx, g)
			if err != nil {
				return ctlproto.LogPage{}, err
			}
			chunks = append(chunks, es)
			total += len(es)
			page.Running = page.Running || running
			cur = store.LogCursor{Source: req.Source, At: g.links[0].CreatedAt, Batch: g.batch}
			if total >= limit {
				break
			}
		}
		if len(groups) < batchGroup && total < limit {
			cur = store.LogCursor{} // 取尽了
			break
		}
	}
	for i := len(chunks) - 1; i >= 0; i-- { // 页内从早到晚：最旧的批在前
		page.Entries = append(page.Entries, chunks[i]...)
	}
	if cur.Batch != "" {
		more, err := r.St.WhisperLinkBatches(ctx, req.Source, cur, 1)
		if err != nil {
			return ctlproto.LogPage{}, err
		}
		if len(more) > 0 {
			page.Next = cur.Encode()
		}
	}
	// 外来文字（被引的话、模型写的字、工具结果）出去前过一遍控制字符和方向覆盖符：界面和 ctl 拿到的都是干净的
	return ctlproto.CleanLogPage(page), nil
}

func groupLinks(links []store.WhisperLink) []group {
	var out []group
	for _, l := range links {
		if n := len(out); n > 0 && out[n-1].batch == l.Batch {
			out[n-1].links = append(out[n-1].links, l)
			continue
		}
		out = append(out, group{batch: l.Batch, links: []store.WhisperLink{l}})
	}
	return out
}

type turnRow struct {
	ID, Character, Conversation, Outcome string
	StartedAt, EndedAt                   int64
}

// project 把一批翻成条目（含这一批所有角色的过程）。
func (r *Reader) project(ctx context.Context, g group) (es []ctlproto.LogEntry, running bool, err error) {
	say := "say:" + g.batch
	first := g.links[0]
	we, err := r.whisperEvents(ctx, g.links)
	if err != nil {
		return nil, false, err
	}
	// 悄悄话本身：引用卡去重（同一条被引的话指给几位只出现一次）、对象、主人的话
	entry := ctlproto.LogEntry{ID: say, Batch: g.batch, Kind: ctlproto.LogWhisper, At: first.CreatedAt}
	seenQuote := map[string]bool{}
	var says []string
	for _, l := range g.links {
		entry.Targets = append(entry.Targets, l.Character)
		for _, p := range we[l.EventSeq] {
			if p.Quote != nil && !seenQuote[p.Quote.ID] {
				seenQuote[p.Quote.ID] = true
				entry.Quotes = append(entry.Quotes, ctlproto.LogQuote{Name: oneLine(p.Quote.Name, 40), At: p.Quote.At, Text: clipLines(p.Quote.Text, quoteLines, quoteRunes)})
			}
			if t := strings.TrimSpace(p.Text); t != "" && !slices.Contains(says, t) {
				says = append(says, t)
			}
		}
	}
	entry.Summary = clip(oneLine(strings.Join(says, " / "), 0), summaryRunes)
	es = append(es, entry)

	turns, err := r.turnsOf(ctx, g.links)
	if err != nil {
		return nil, false, err
	}
	var turnIDs []string
	for _, t := range turns {
		turnIDs = append(turnIDs, t.ID)
	}
	steps, err := r.steps(ctx, turnIDs)
	if err != nil {
		return nil, false, err
	}
	tasks, err := r.tasks(ctx, turnIDs)
	if err != nil {
		return nil, false, err
	}
	notes, err := r.notes(ctx, g)
	if err != nil {
		return nil, false, err
	}
	for _, l := range g.links {
		if !terminalState(l.State) {
			running = true
		}
		arrange := "arr:" + l.Batch + ":" + l.Character
		a := ctlproto.LogEntry{ID: arrange, Batch: l.Batch, Kind: ctlproto.LogArrange, Character: l.Character, Parent: say, At: l.CreatedAt, State: l.State,
			Summary: clip(oneLine(l.Detail, 0), summaryRunes), Busy: l.SourceBusy}
		if l.State == ctlproto.WhisperQueued && r.Position != nil {
			if p, ok := r.Position(l.Batch, l.Character); ok {
				a.Position = p
			}
		}
		if t, ok := turns[l.Character+"|"+l.Batch]; ok {
			a.Turn = t.ID
		}
		es = append(es, a)
		t, ok := turns[l.Character+"|"+l.Batch]
		if ok {
			stepOf := map[string]string{}
			for _, s := range steps[t.ID] {
				e := stepEntry(l, arrange, t.ID, s)
				stepOf[s.ID] = e.ID
				es = append(es, e)
			}
			for _, k := range tasks[t.ID] {
				parent := arrange
				if id, ok := stepOf[k.ToolID]; ok {
					parent = id
				}
				at := k.At
				if at == 0 {
					at = t.StartedAt
				}
				es = append(es, ctlproto.LogEntry{ID: "task:" + k.ID, Batch: l.Batch, Kind: ctlproto.LogSpawn, Character: l.Character, Parent: parent, At: at, State: k.State, Turn: t.ID,
					Summary: clip(oneLine(k.Payload, 0), summaryRunes)})
				if k.Result != "" || terminalTask(k.State) {
					es = append(es, ctlproto.LogEntry{ID: "ret:" + k.ID, Batch: l.Batch, Kind: ctlproto.LogReturn, Character: l.Character, Parent: "task:" + k.ID, At: max(at, k.EndAt), State: k.State, Turn: t.ID,
						Err: k.State != "done" && k.State != "completed" && k.State != "settled", Summary: clip(oneLine(k.Result, 0), summaryRunes)})
				}
			}
		}
		if n, ok := notes[l.Character]; ok {
			es = append(es, ctlproto.LogEntry{ID: "note:" + l.Batch + ":" + l.Character, Batch: l.Batch, Kind: ctlproto.LogNote, Character: l.Character, Parent: arrange, At: n.CreatedAt,
				Summary: clip(oneLine(n.Text, 0), summaryRunes)})
		}
	}
	sort.SliceStable(es, func(i, j int) bool { return es[i].At < es[j].At })
	// 悄悄话条目和每个安排要排在自己的过程前面：时间相同时稳定排序保留了上面追加的先后
	return es, running, nil
}

func stepEntry(l store.WhisperLink, parent, turn string, s stepRow) ctlproto.LogEntry {
	e := ctlproto.LogEntry{ID: "step:" + s.ID, Batch: l.Batch, Character: l.Character, Parent: parent, At: s.StartedAt, Turn: turn}
	switch {
	case s.Kind == "thinking":
		e.Kind, e.Runes = ctlproto.LogThink, s.Len
		e.Summary = clip(oneLine(firstLine(s.Preview), 0), summaryRunes)
	case isMemoryTool(s.Name):
		e.Kind = ctlproto.LogMemory
		e.Summary = s.Name
		if p := oneLine(s.Preview, 0); p != "" {
			e.Summary += "：" + clip(p, summaryRunes)
		}
	default:
		e.Kind = ctlproto.LogTool
		e.Summary = s.Name
		if p := oneLine(s.Preview, 0); p != "" {
			e.Summary += "：" + clip(p, summaryRunes)
		}
	}
	if s.Kind != "thinking" {
		e.State, e.Err, e.Waiting = s.Status, s.IsError, s.Status == "waiting"
		if e.Kind == ctlproto.LogTool {
			e.Egress, e.EgressN, e.EgressDanger = s.Egress, s.EgressN, s.EgressDanger
		}
	}
	return e
}

func terminalState(s string) bool {
	switch s {
	case ctlproto.WhisperDone, ctlproto.WhisperFailed, ctlproto.WhisperCancelled, ctlproto.WhisperTimedOut, ctlproto.WhisperInterrupted:
		return true
	}
	return false
}

func isMemoryTool(name string) bool { return name == "remember" || name == "memory_forget" }

func terminalTask(s string) bool {
	switch s {
	case "done", "completed", "settled", "failed", "cancelled", "interrupted", "timed_out":
		return true
	}
	return false
}

// whisperEvents 取各行的悄悄话事件里的各块（引用和主人的话）：按 events 的 seq。
func (r *Reader) whisperEvents(ctx context.Context, links []store.WhisperLink) (map[int64][]history.Part, error) {
	out := map[int64][]history.Part{}
	var seqs []any
	for _, l := range links {
		if l.EventSeq > 0 {
			seqs = append(seqs, l.EventSeq)
		}
	}
	if len(seqs) == 0 {
		return out, nil
	}
	rows, err := r.St.Events.QueryContext(ctx, `SELECT seq, conversation, COALESCE(segments_json,'') FROM events WHERE kind = 'message' AND seq IN (`+placeholders(len(seqs))+`)`, seqs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := map[int64]string{}
	for _, l := range links {
		want[l.EventSeq] = l.WhisperConversation
	}
	for rows.Next() {
		var seq int64
		var conv, seg string
		if err := rows.Scan(&seq, &conv, &seg); err != nil {
			return nil, err
		}
		if want[seq] != conv { // 关联指到的事件不在她的悄悄话会话里：不信
			continue
		}
		var parts []history.Part
		if store.WhisperSegment(seg, "local_parts", &parts) {
			out[seq] = parts
		}
	}
	return out, rows.Err()
}

// turnsOf 找每一行对应的那一轮：有 root_run_id 按它；还没有（处理中）按悄悄话会话里开始时间之后最早的一轮。
// 键是“角色|批号”。
func (r *Reader) turnsOf(ctx context.Context, links []store.WhisperLink) (map[string]turnRow, error) {
	out := map[string]turnRow{}
	var roots []any
	byRoot := map[string]store.WhisperLink{}
	var loose []store.WhisperLink
	for _, l := range links {
		switch {
		case l.RootRun != "":
			roots = append(roots, l.RootRun)
			byRoot[l.RootRun] = l
		case l.StartedAt > 0:
			loose = append(loose, l)
		}
	}
	if len(roots) > 0 {
		rows, err := r.St.Trace.QueryContext(ctx, `SELECT id, character_id, conversation, COALESCE(started_at,0), COALESCE(ended_at,0), COALESCE(outcome,'') FROM turns WHERE id IN (`+placeholders(len(roots))+`)`, roots...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var t turnRow
			if err := rows.Scan(&t.ID, &t.Character, &t.Conversation, &t.StartedAt, &t.EndedAt, &t.Outcome); err != nil {
				rows.Close()
				return nil, err
			}
			l := byRoot[t.ID]
			if t.Character == l.Character && t.Conversation == l.WhisperConversation { // 对不上的回合 id 不信
				out[l.Character+"|"+l.Batch] = t
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	for _, l := range loose {
		var t turnRow
		err := r.St.Trace.QueryRowContext(ctx, `SELECT id, character_id, conversation, COALESCE(started_at,0), COALESCE(ended_at,0), COALESCE(outcome,'') FROM turns
			WHERE character_id = ? AND conversation = ? AND started_at >= ? ORDER BY started_at LIMIT 1`, l.Character, l.WhisperConversation, l.StartedAt).Scan(&t.ID, &t.Character, &t.Conversation, &t.StartedAt, &t.EndedAt, &t.Outcome)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[l.Character+"|"+l.Batch] = t
	}
	return out, nil
}

type stepRow struct {
	ID, Kind, Name, Status, Preview string
	IsError                         bool
	StartedAt                       int64
	Len                             int // 预览全文的字数（列表只取前一小段，字数另算）
	// 外发检查：审批留在步骤记录里的编号、几处、几处可疑（没有就空）
	Egress                string
	EgressN, EgressDanger int
}

func (r *Reader) steps(ctx context.Context, turnIDs []string) (map[string][]stepRow, error) {
	out := map[string][]stepRow{}
	if len(turnIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(turnIDs))
	for i, id := range turnIDs {
		args[i] = id
	}
	// 列表只要预览的前一小段：大文本点开才读
	rows, err := r.St.Trace.QueryContext(ctx, `SELECT turn_id, id, kind, COALESCE(name,''), COALESCE(status,''), substr(COALESCE(preview,''),1,`+fmt.Sprint(thinkingPeek)+`), COALESCE(is_error,0), COALESCE(started_at,0), length(COALESCE(preview,'')),
		CASE WHEN json_valid(approval_json) THEN COALESCE(json_extract(approval_json,'$.egress.id'),'') ELSE '' END,
		CASE WHEN json_valid(approval_json) THEN COALESCE(json_extract(approval_json,'$.egress.n'),0) ELSE 0 END,
		CASE WHEN json_valid(approval_json) THEN COALESCE(json_extract(approval_json,'$.egress.danger'),0) ELSE 0 END
		FROM steps WHERE turn_id IN (`+placeholders(len(args))+`) AND kind IN ('thinking','tool') ORDER BY started_at, rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var turn string
		var s stepRow
		var isErr int
		if err := rows.Scan(&turn, &s.ID, &s.Kind, &s.Name, &s.Status, &s.Preview, &isErr, &s.StartedAt, &s.Len, &s.Egress, &s.EgressN, &s.EgressDanger); err != nil {
			return nil, err
		}
		s.IsError = isErr != 0
		out[turn] = append(out[turn], s)
	}
	return out, rows.Err()
}

// thinkingPeek 是列表里思考预览取的字节数；字数统计用全文，所以单独查（见 thinkingRunes）。
const thinkingPeek = 400

type taskRow struct {
	ID, ToolID, State, Payload, Result string
	At, EndAt                          int64
}

// tasks 取这些回合直接派出的子任务（kernel_tasks，按派它的回合）。没有时间列：时间取派它的那一步（steps.id = tool_id）的开始和结束。
func (r *Reader) tasks(ctx context.Context, turnIDs []string) (map[string][]taskRow, error) {
	out := map[string][]taskRow{}
	if len(turnIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(turnIDs))
	for i, id := range turnIDs {
		args[i] = id
	}
	rows, err := r.St.Trace.QueryContext(ctx, `SELECT k.parent_run, k.id, k.tool_id, k.state, substr(k.payload,1,400), substr(k.result,1,400), COALESCE(s.started_at,0), COALESCE(s.ended_at,0)
		FROM kernel_tasks k LEFT JOIN steps s ON s.id = k.tool_id WHERE k.parent_run IN (`+placeholders(len(args))+`) ORDER BY k.rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var parent string
		var k taskRow
		if err := rows.Scan(&parent, &k.ID, &k.ToolID, &k.State, &k.Payload, &k.Result, &k.At, &k.EndAt); err != nil {
			return nil, err
		}
		out[parent] = append(out[parent], k)
	}
	return out, rows.Err()
}

type noteRow struct {
	Text      string
	CreatedAt int64
}

// notes 取这一批里各角色的处理记录（悄悄话会话里的 whisper_note 事件），键是角色。按关联行里记的 note_seq 取：
// 主键查找，不去事件表里翻 JSON；seq 对不上这位角色的悄悄话会话就不信。
func (r *Reader) notes(ctx context.Context, g group) (map[string]noteRow, error) {
	out := map[string]noteRow{}
	var seqs []any
	want := map[int64]store.WhisperLink{}
	for _, l := range g.links {
		if l.State == ctlproto.WhisperDone && l.NoteSeq > 0 {
			seqs = append(seqs, l.NoteSeq)
			want[l.NoteSeq] = l
		}
	}
	if len(seqs) == 0 {
		return out, nil
	}
	rows, err := r.St.Events.QueryContext(ctx, `SELECT seq, conversation, actor_id, substr(text,1,400), created_at FROM events WHERE kind = ? AND seq IN (`+placeholders(len(seqs))+`)`, append([]any{store.KindWhisperNote}, seqs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq, at int64
		var conv, actor, text string
		if err := rows.Scan(&seq, &conv, &actor, &text, &at); err != nil {
			return nil, err
		}
		if l := want[seq]; l.WhisperConversation == conv && l.Character == actor {
			out[actor] = noteRow{Text: text, CreatedAt: at}
		}
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// oneLine 把空白压成一个空格；n>0 时再截到 n 字。
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if n > 0 {
		return clip(s, n)
	}
	return s
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// clipLines 取前几行、每行不超过 n 字（被引原文在列表里最多三行）。
func clipLines(s string, lines, n int) string {
	var all []string
	for _, ln := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(ln) != "" {
			all = append(all, clip(strings.TrimSpace(ln), n))
		}
	}
	if len(all) > lines {
		all = append(all[:lines:lines], "…")
	}
	return strings.Join(all, "\n")
}
