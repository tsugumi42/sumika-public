package desktopread

import (
	"context"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

func TestOutboundStateRefreshWithoutNewSequence(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.PartAborted, store.PartConfirmed} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s, set := fixture(t)
			qq, _ := set.Platform("qq")
			conv := "qq:group:90002"
			plan := store.NewPlan{ID: "plan", CharacterID: "a", Conversation: conv, Account: "90001", Origin: "character", Kind: store.PlanKindSend, At: 1, Event: &store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "character", ActorID: "a", Kind: "message", CreatedAt: 1, Text: "编造回复", Segments: map[string]any{"unsent": true}}, Observers: []string{"a"}, Parts: []store.NewPart{{Kind: "text", Text: "编造回复"}}}
			seq, e := qq.AppendPlan(plan)
			if e != nil {
				t.Fatal(e)
			}
			sub, e := s.WatchConversation(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 200})
			if e != nil {
				t.Fatal(e)
			}
			defer sub.Close()
			first := next(t, sub)
			if len(first.Value.Page.Items) != 1 || !first.Value.Page.Items[0].Unsent {
				t.Fatalf("待发冒充已发: %+v", first)
			}
			if ok, e := qq.EndPart(plan.ID, 0, []string{store.PartPending}, state, store.PartEnd{PlatformID: "90005"}, 2); e != nil || !ok {
				t.Fatalf("终态: %v %v", ok, e)
			}
			nextState := next(t, sub)
			if nextState.Value.Page.HighWater != seq {
				t.Fatal("状态更新增加了事件水位")
			}
			found := false
			for _, m := range nextState.Value.Changed {
				if m.Seq == seq {
					found = true
					aborted := state == store.PartAborted
					if m.Aborted != aborted || m.Unsent != aborted || m.Version <= first.Value.Page.Items[0].Version {
						t.Fatalf("旧出站未刷新: %+v", m)
					}
				}
			}
			if !found {
				t.Fatal("已加载状态没有重查")
			}
		})
	}
}
