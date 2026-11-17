package history_test

import (
	"fmt"
	"testing"

	"sumika-native/internal/history"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func TestHistoryPastCapNoGap(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const conversation = "console:made-a"
	var high int64
	for i := range 2501 {
		high, err = st.AppendEvent(store.NewEvent{Conversation: conversation, Channel: "console", ActorKind: "owner", ActorID: "made-owner", Origin: "human", Kind: "message", Text: fmt.Sprint("目标与来源-", i), CreatedAt: int64(i + 1)}, []string{"made-a"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddEpoch(store.Epoch{ID: "made-epoch", CharacterID: "made-a", Conversation: conversation, FromSeq: 1, Summary: "旧摘要", Kind: "chat", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	snapshot, msgs, _, err := history.For(st.Events, "made-a", conversation, 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || len(msgs) != 2501 || msgs[500].Text != "目标与来源-500" {
		t.Fatalf("Cap 中段缺失：%d %+v", len(msgs), snapshot)
	}
	var seqs []int64
	pages := 0
	err = history.WalkEvents(st.Events, conversation, 1, high, 200, func(page []history.Event) error {
		pages++
		for _, e := range page {
			seqs = append(seqs, e.Seq)
		}
		if pages == 1 {
			_, err := st.AppendEvent(store.NewEvent{Conversation: conversation, Channel: "console", ActorKind: "owner", ActorID: "made-owner", Origin: "human", Kind: "message", Text: "水位之后的新输入", CreatedAt: 9999}, nil)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seqs) != 2501 || seqs[2500] != high || pages != 13 {
		t.Fatalf("分页水位/缺口：events=%d pages=%d high=%d", len(seqs), pages, high)
	}
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("中段不连续：%d=%d", i, seq)
		}
	}
	t.Logf("K2-T5 events=%d pages=%d high=%d late_excluded=1", len(seqs), pages, high)
}
