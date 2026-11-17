package whisperlog

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/history"
	"sumika-native/internal/store"
)

// seedScale 造规模数据：十个角色、batches 个悄悄话批次，每批每位角色各带思考、工具、处理记录。全是编的，库在 t.TempDir() 里。
func seedScale(t *testing.T, batches int) (*store.Store, *Reader) {
	t.Helper()
	st := openStore(t)
	chars := make([]string, 10)
	for i := range chars {
		chars[i] = fmt.Sprintf("char%02d", i)
	}
	tx, err := st.Trace.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for b := 0; b < batches; b++ {
		batch := fmt.Sprintf("B%06d", b)
		at := int64(1_000_000 + b*1000)
		var evs []store.NewEvent
		var obs [][]string
		for _, c := range chars {
			conv := audience.WhisperConversation(c, src)
			parts := []history.Part{{Quote: &history.Quote{ID: "q" + batch, Name: "编造群友", At: at, Text: "编的被引原文\n第二行\n第三行", Whole: true}, Text: "帮我查一下 " + batch, Mentions: []string{c}}}
			evs = append(evs,
				store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "x", CreatedAt: at,
					Segments: map[string]any{"local_parts": parts, "whisper": history.Whisper{Batch: batch, Source: src}}},
				store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "character", ActorID: c, Origin: "whisper", Kind: store.KindWhisperNote, Text: "编的处理记录 " + batch, CreatedAt: at + 500,
					Segments: map[string]any{"whisper_note": map[string]any{"batch": batch, "source": src, "say": "查"}}})
			obs = append(obs, []string{c}, nil)
		}
		seqs, _, err := st.AppendWhisperEvents(evs, obs)
		if err != nil {
			t.Fatal(err)
		}
		for i, c := range chars {
			conv := audience.WhisperConversation(c, src)
			turn := fmt.Sprintf("T%s-%s", batch, c)
			rows := []struct {
				q string
				a []any
			}{
				{`INSERT INTO whisper_links(batch, character_id, source_conversation, whisper_conversation, event_seq, note_seq, state, root_run_id, created_at, started_at, ended_at) VALUES(?,?,?,?,?,?, 'done', ?, ?, ?, ?)`, []any{batch, c, src, conv, seqs[2*i], seqs[2*i+1], turn, at, at + 5, at + 500}},
				{`INSERT INTO turns(id, character_id, conversation, started_at, ended_at, outcome) VALUES(?,?,?,?,?, 'done')`, []any{turn, c, conv, at + 10, at + 500}},
				{`INSERT INTO steps(id, character_id, conversation, turn_id, kind, preview, status, started_at) VALUES(?,?,?,?, 'thinking', '编的思考内容，比较长的一段话，用来模拟真实的思考摘要', 'done', ?)`, []any{"th-" + turn, c, conv, turn, at + 20}},
				{`INSERT INTO steps(id, character_id, conversation, turn_id, kind, name, preview, status, started_at) VALUES(?,?,?,?, 'tool', 'web_search', '搜到三条结果', 'done', ?)`, []any{"to-" + turn, c, conv, turn, at + 30}},
			}
			for _, r := range rows {
				if _, err := tx.Exec(r.q, r.a...); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return st, &Reader{St: st}
}

// 读页规模（规格 15.4）：十个角色、2000 个悄悄话批次，每批每位角色各带思考、工具、处理记录。
// 一页 200 条读取 P95 ≤ 50 ms，并且不随历史总量增长。合并前的快测用 300 批，整套里跑 2000 批。
func TestReadLogScaleTenCharacters(t *testing.T) {
	batches := 2000
	if testing.Short() {
		batches = 300
	}
	_, r := seedScale(t, batches)
	var ds []time.Duration
	cursor, entries, pages := "", 0, 0
	for i := 0; i < 40; i++ {
		start := time.Now()
		p, err := r.Read(context.Background(), Request{Source: src, Cursor: cursor, Limit: 200})
		ds = append(ds, time.Since(start))
		if err != nil {
			t.Fatal(err)
		}
		entries += len(p.Entries)
		pages++
		if p.Next == "" {
			break
		}
		cursor = p.Next
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	p95 := ds[len(ds)*95/100]
	t.Logf("十个角色、%d 批次，从最新往前读 %d 页（共 %d 条，每页约 %d 条）：P95 %v，最慢 %v", batches, pages, entries, entries/max(pages, 1), p95, ds[len(ds)-1])
	limit := 50 * time.Millisecond // 规格 15.4；快测在负载下跑（合并队列），只拦数量级的退化
	if testing.Short() {
		limit = 150 * time.Millisecond
	}
	if p95 > limit {
		t.Errorf("一页 200 条读取 P95 %v 超过 %v", p95, limit)
	}
	d := time.Now()
	if _, err := r.Detail(context.Background(), src, "note:B000001:char03", false); err != nil {
		t.Fatal(err)
	}
	t.Logf("读一条全文：%v", time.Since(d))
}
