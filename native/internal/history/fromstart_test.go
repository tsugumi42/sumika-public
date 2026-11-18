package history_test

// 从上下文起点重建的单测（规格-私聊从上下文起点重建 第二、六、七节）：起点、快照切点、参考块下限、
// 只有通知/说明分隔之后没有消息/skip 命中起点/噪声通知这些边界，以及 LastBreak 的 SQL 过滤要和旧写法判得一样。

import (
	"database/sql"
	"fmt"
	"testing"

	"sumika-native/internal/history"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func fromStartStore(t *testing.T) *store.Store {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func appendFromStartMsg(t *testing.T, st *store.Store, conv, actor, text string) int64 {
	t.Helper()
	kind := "owner"
	if actor != "owner" {
		kind = "character"
	}
	seq, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: kind, ActorID: actor,
		Origin: kind, Kind: "message", Text: text, CreatedAt: 1000}, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func appendFromStartNotice(t *testing.T, st *store.Store, conv string, seg any) int64 {
	t.Helper()
	seq, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "system", ActorID: "system",
		Origin: "system", Kind: "notice", Text: "n", Segments: seg, CreatedAt: 1000}, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func insertRawNotice(t *testing.T, st *store.Store, conv, id string, segments any) int64 {
	t.Helper()
	res, err := st.Events.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, origin, kind, text, segments_json, created_at)
		VALUES (?, 'console', ?, 'system', 'system', 'system', 'notice', 'n', ?, 1000)`, id, conv, segments)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func checkForFromStart(t *testing.T, st *store.Store, char, conv string, skip []int64, wantSnapshot bool, wantEpoch string, want ...string) {
	t.Helper()
	snapshot, msgs, epochID, err := history.ForFromStart(st.Events, char, conv, skip)
	if err != nil {
		t.Fatal(err)
	}
	if (snapshot != nil) != wantSnapshot || epochID != wantEpoch {
		t.Fatalf("快照不对：snapshot=%v epoch=%q，应该 %v %q", snapshot != nil, epochID, wantSnapshot, wantEpoch)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.Text)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("历史不对：%v，应该 %v", got, want)
	}
}

func TestFromStartEdges(t *testing.T) {
	t.Parallel()
	const conv = "console:a"
	brk := map[string]any{"notice": history.ContextBreak}

	t.Run("只有通知", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartNotice(t, st, conv, brk)
		checkForFromStart(t, st, "a", conv, nil, false, "")
	})
	t.Run("分隔后面没有消息", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartMsg(t, st, conv, "owner", "m1")
		appendFromStartMsg(t, st, conv, "a", "m2")
		appendFromStartNotice(t, st, conv, brk)
		checkForFromStart(t, st, "a", conv, nil, false, "")
	})
	t.Run("skip 命中起点", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartMsg(t, st, conv, "owner", "m1")
		appendFromStartMsg(t, st, conv, "a", "m2")
		appendFromStartNotice(t, st, conv, brk)
		start := appendFromStartMsg(t, st, conv, "owner", "m3")
		appendFromStartMsg(t, st, conv, "a", "m4")
		checkForFromStart(t, st, "a", conv, []int64{start}, false, "", "m4")
	})
	t.Run("切点等于起点不用快照", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartMsg(t, st, conv, "owner", "m1")
		appendFromStartNotice(t, st, conv, brk)
		start := appendFromStartMsg(t, st, conv, "owner", "m3")
		appendFromStartMsg(t, st, conv, "a", "m4")
		epoch(t, st, conv, "e1", "a", start, "摘要", 2000)
		checkForFromStart(t, st, "a", conv, nil, false, "", "m3", "m4")
	})
	t.Run("切点大于起点用快照", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartMsg(t, st, conv, "owner", "m1")
		appendFromStartNotice(t, st, conv, brk)
		appendFromStartMsg(t, st, conv, "owner", "m3")
		cut := appendFromStartMsg(t, st, conv, "a", "m4")
		epoch(t, st, conv, "e2", "a", cut, "摘要二", 3000)
		checkForFromStart(t, st, "a", conv, nil, true, "e2", "m4")
	})
	t.Run("快照之后又有分隔不用快照", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartMsg(t, st, conv, "owner", "m1")
		cut := appendFromStartMsg(t, st, conv, "a", "m2")
		epoch(t, st, conv, "e3", "a", cut, "摘要三", 2000)
		appendFromStartNotice(t, st, conv, brk)
		appendFromStartMsg(t, st, conv, "owner", "m3")
		checkForFromStart(t, st, "a", conv, nil, false, "", "m3")
	})
	t.Run("噪声通知不算分隔", func(t *testing.T) {
		t.Parallel()
		for _, seg := range []any{
			nil,
			map[string]any{"notice": 1},
			map[string]any{"notice": []string{history.ContextBreak}},
			map[string]any{"notice": nil},
			map[string]any{},
			"坏 JSON",
			`{"notice":"context-break "}`,
		} {
			st := fromStartStore(t)
			appendFromStartMsg(t, st, conv, "owner", "m1")
			appendFromStartMsg(t, st, conv, "a", "m2")
			if s, ok := seg.(string); ok && s == "坏 JSON" {
				insertRawNotice(t, st, conv, "bad", s)
			} else {
				appendFromStartNotice(t, st, conv, seg)
			}
			appendFromStartMsg(t, st, conv, "owner", "m3")
			if brk, err := history.LastBreak(st.Events, conv); err != nil || brk != 0 {
				t.Fatalf("噪声 %v 不该算分隔：%d %v", seg, brk, err)
			}
			checkForFromStart(t, st, "a", conv, nil, false, "", "m1", "m2", "m3")
		}
	})
	t.Run("另一个角色没有快照", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartNotice(t, st, conv, brk)
		appendFromStartMsg(t, st, conv, "owner", "m3")
		cut := appendFromStartMsg(t, st, conv, "owner", "m4")
		epoch(t, st, conv, "e4", "a", cut, "只属于 a", 3000)
		checkForFromStart(t, st, "b", conv, nil, false, "", "m3", "m4")
	})
	t.Run("没有分隔从第一条", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartMsg(t, st, conv, "owner", "m1")
		appendFromStartMsg(t, st, conv, "a", "m2")
		checkForFromStart(t, st, "a", conv, nil, false, "", "m1", "m2")
	})
	t.Run("没有算数消息", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		checkForFromStart(t, st, "a", conv, nil, false, "")
	})
}

func epoch(t *testing.T, st *store.Store, conv, id, char string, from int64, summary string, at int64) {
	t.Helper()
	if err := st.AddEpoch(store.Epoch{ID: id, Conversation: conv, CharacterID: char, FromSeq: from, Summary: summary,
		Kind: "chat", Trigger: "manual", Model: "made", Status: "ok", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
}

// startSeq 由 ForFromStart 露出：空会话、没有分隔从第一条、分隔后第一条、分隔后没有消息、噪声通知不改起点
func TestStartSeq(t *testing.T) {
	t.Parallel()
	const conv = "console:a"
	brk := map[string]any{"notice": history.ContextBreak}
	st := fromStartStore(t)
	checkForFromStart(t, st, "a", conv, nil, false, "")
	appendFromStartMsg(t, st, conv, "owner", "m1")
	appendFromStartMsg(t, st, conv, "a", "m2")
	checkForFromStart(t, st, "a", conv, nil, false, "", "m1", "m2")
	appendFromStartNotice(t, st, conv, brk)
	appendFromStartNotice(t, st, conv, map[string]any{"notice": "other"})
	checkForFromStart(t, st, "a", conv, nil, false, "")
	appendFromStartMsg(t, st, conv, "owner", "m3")
	appendFromStartMsg(t, st, conv, "a", "m4")
	checkForFromStart(t, st, "a", conv, nil, false, "", "m3", "m4")
	appendFromStartNotice(t, st, conv, brk)
	checkForFromStart(t, st, "a", conv, nil, false, "")
}

func TestWindowFloorFromStart(t *testing.T) {
	t.Parallel()
	const conv = "console:a"
	brk := map[string]any{"notice": history.ContextBreak}
	t.Run("没有快照取起点", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartMsg(t, st, conv, "owner", "m1")
		appendFromStartNotice(t, st, conv, brk)
		m3 := appendFromStartMsg(t, st, conv, "owner", "m3")
		appendFromStartMsg(t, st, conv, "a", "m4")
		got, err := history.WindowFloorFromStart(st.Events, "a", conv)
		if err != nil || got != m3 {
			t.Fatalf("下限应是起点 %d：%d %v", m3, got, err)
		}
	})
	t.Run("切点比起点新取切点", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartMsg(t, st, conv, "owner", "m1")
		appendFromStartNotice(t, st, conv, brk)
		appendFromStartMsg(t, st, conv, "owner", "m3")
		cut := appendFromStartMsg(t, st, conv, "a", "m4")
		epoch(t, st, conv, "e1", "a", cut, "摘要", 2000)
		got, err := history.WindowFloorFromStart(st.Events, "a", conv)
		if err != nil || got != cut {
			t.Fatalf("下限应是快照切点 %d：%d %v", cut, got, err)
		}
	})
	t.Run("切点等于起点取起点", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartMsg(t, st, conv, "owner", "m1")
		appendFromStartNotice(t, st, conv, brk)
		start := appendFromStartMsg(t, st, conv, "owner", "m3")
		epoch(t, st, conv, "e1", "a", start, "摘要", 2000)
		got, err := history.WindowFloorFromStart(st.Events, "a", conv)
		if err != nil || got != start {
			t.Fatalf("切点等于起点，下限应是起点 %d：%d %v", start, got, err)
		}
	})
	t.Run("切点比起点旧取起点", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartMsg(t, st, conv, "owner", "m1")
		cut := appendFromStartMsg(t, st, conv, "a", "m2")
		appendFromStartNotice(t, st, conv, brk)
		start := appendFromStartMsg(t, st, conv, "owner", "m3")
		epoch(t, st, conv, "e1", "a", cut, "摘要", 2000)
		got, err := history.WindowFloorFromStart(st.Events, "a", conv)
		if err != nil || got != start {
			t.Fatalf("切点比起点旧，下限应是起点 %d：%d %v", start, got, err)
		}
	})
	t.Run("另一个角色的快照不算", func(t *testing.T) {
		t.Parallel()
		st := fromStartStore(t)
		appendFromStartNotice(t, st, conv, brk)
		start := appendFromStartMsg(t, st, conv, "owner", "m3")
		cut := appendFromStartMsg(t, st, conv, "a", "m4")
		epoch(t, st, conv, "e1", "b", cut, "摘要", 2000)
		got, err := history.WindowFloorFromStart(st.Events, "a", conv)
		if err != nil || got != start {
			t.Fatalf("别的角色的快照不算，下限应是起点 %d：%d %v", start, got, err)
		}
	})
}

// lastBreakOld 是 LastBreak 改 SQL 过滤之前的写法，留在这里当对照。
func lastBreakOld(t *testing.T, db *sql.DB, conversation string) int64 {
	t.Helper()
	rows, err := db.Query(`SELECT seq, segments_json FROM events WHERE conversation = ? AND kind = 'notice' ORDER BY seq DESC`, conversation)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var seg sql.NullString
		if err := rows.Scan(&seq, &seg); err != nil {
			t.Fatal(err)
		}
		if history.IsContextBreak(history.Event{Kind: "notice", Segments: seg}) {
			return seq
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return 0
}

func TestLastBreakMatchesOldFiltering(t *testing.T) {
	t.Parallel()
	const conv = "console:a"
	cases := []struct {
		name string
		seg  any
		want bool // 这条本身算不算分隔
	}{
		{"合法分隔", `{"notice":"context-break"}`, true},
		{"多一个字段", `{"notice":"context-break","x":1}`, true},
		{"键名大写", `{"NOTICE":"context-break"}`, true},
		{"重键最后一个合法", `{"notice":1,"notice":"context-break"}`, true},
		{"notice 是数字", `{"notice":1}`, false},
		{"notice 是数组", `{"notice":["context-break"]}`, false},
		{"notice 是 null", `{"notice":null}`, false},
		{"没有 notice", `{}`, false},
		{"别的 notice", `{"notice":"other"}`, false},
		{"大小写不同", `{"notice":"Context-Break"}`, false},
		{"末尾多空格", `{"notice":"context-break "}`, false},
		{"重键最后一个不是", `{"notice":"context-break","notice":1}`, false},
		{"嵌套不算", `{"x":{"notice":"context-break"}}`, false},
		{"坏 JSON", `not json`, false},
		{"空串", ``, false},
		{"NULL", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := fromStartStore(t)
			leading := appendFromStartNotice(t, st, conv, map[string]any{"notice": history.ContextBreak})
			got := insertRawNotice(t, st, conv, "case", tc.seg)
			want := leading
			if tc.want {
				want = got
			}
			old := lastBreakOld(t, st.Events, conv)
			now, err := history.LastBreak(st.Events, conv)
			if err != nil {
				t.Fatal(err)
			}
			if old != want || now != want || old != now {
				t.Fatalf("旧写法 %d、新写法 %d，应该 %d（这条算分隔=%v）", old, now, want, tc.want)
			}
		})
	}
}
