package desktopread

import (
	"encoding/json"
	"reflect"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

// 自动召回的过程步骤：重启后从 trace 读回；旧消息没有这一行、存坏了的不报错、不显示。
func TestProcessOfRecallStep(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	st, _ := set.Platform("local")
	k := api.ConversationKey{Platform: "local", Conversation: "c1"}
	reply := func(turn string, steps func()) store.DisplayRow {
		t.Helper()
		steps()
		seq, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: k.Conversation, ActorKind: "character", ActorID: "a", Origin: "character", Kind: "message", Text: "编造回复", CreatedAt: 10}, []string{"a"})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AttachSteps(turn, seq); err != nil {
			t.Fatal(err)
		}
		return store.DisplayRow{Origin: "character", Seq: seq}
	}
	step := func(turn, id, kind, name, preview string, args *string, at int64) {
		t.Helper()
		if err := st.StepStart(store.Step{ID: id, CharacterID: "a", Conversation: k.Conversation, TurnID: turn, Kind: kind, Name: name, Args: args, Preview: preview, Status: "done", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	items := []api.RecallItem{
		{Text: "编造的固定要点", Kind: "boundary", Pinned: true, Source: "主人确认", CreatedAt: 5},
		{Text: "编造的记忆", Kind: "fact", Source: "原话", CreatedAt: 6, Score: 0.03, Rerank: 0.8, Sim: 0.7, Vector: 2, Lexical: 1, Previous: []api.RecallPrior{{Text: "编造的旧记忆", CreatedAt: 4}}},
	}
	raw, _ := json.Marshal(items)
	args := string(raw)

	// 有召回：排在思考前面，字段原样读回
	got := s.processOf(k, reply("t1", func() {
		step("t1", "recall:t1:1", "recall", "memory", "2 条", &args, 1)
		step("t1", "think:t1", "thinking", "", "想了想", nil, 2)
	}))
	if len(got) != 2 || got[0].Kind != "recall" || got[0].Tool != "" || got[1].Kind != "think" {
		t.Fatalf("应是召回在前、思考在后：%+v", got)
	}
	if len(got[0].Recall) != 2 || !reflect.DeepEqual(got[0].Recall, items) {
		t.Fatalf("明细读回来不一致：%+v", got[0].Recall)
	}

	// 旧消息：没有 recall 行，照常，不报错
	got = s.processOf(k, reply("t2", func() { step("t2", "think:t2", "thinking", "", "想了想", nil, 3) }))
	if len(got) != 1 || got[0].Kind != "think" {
		t.Fatalf("旧消息只有思考：%+v", got)
	}

	// 存坏了（不是 JSON、是空数组、没有参数）：这一行不显示，别的照常
	for i, bad := range []*string{ptr("{坏了"), ptr("[]"), nil} {
		turn := "bad" + string(rune('a'+i))
		got = s.processOf(k, reply(turn, func() {
			step(turn, "recall:"+turn, "recall", "memory", "x", bad, 1)
			step(turn, "think:"+turn, "thinking", "", "想了想", nil, 2)
		}))
		if len(got) != 1 || got[0].Kind != "think" {
			t.Errorf("坏数据 %d：应只剩思考：%+v", i, got)
		}
	}
}

func ptr(s string) *string { return &s }
