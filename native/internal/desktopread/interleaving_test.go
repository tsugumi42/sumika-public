package desktopread

import (
	"context"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

func TestSubscriptionRegistrationInterleavings(t *testing.T) {
	// 这条不加顶层 t.Parallel()：它的子测试彼此并行，但要独占机器。单页读取仍有 ReadMessages 的 5 秒时限，
	// 和包里别的测试一起在 -race 下抢 CPU 时可能超时；所以子测试里的读取不再套 watch 的总时限（见那里的注释）。
	// 顶层不并行时，它的子测试在别的测试开跑之前就全部跑完。
	newFixture := fixtureFactory(t)
	// 同时跑的子测试最多 4 个：每个都要读一千条的页，服务里单次查询有 5 秒时限，十几个一起在 -race 下抢 CPU 会把时限吃光
	slots := make(chan struct{}, 4)
	for _, stage := range []string{"before-register", "during-read", "after-initial"} {
		for _, size := range []int{65, 1000} {
			for repeat := range 20 {
				t.Run(fmt.Sprintf("%s/%d/%d", stage, size, repeat), func(t *testing.T) {
					t.Parallel()
					slots <- struct{}{}
					defer func() { <-slots }()
					s, set := newFixture(t)
					qq, _ := set.Platform("qq")
					conv := "qq:group:90002"
					req := api.ConversationRequest{Key: keyOf(conv), Character: "a", Limit: 1000}
					original := add(t, qq, conv, "message", "human", 1, []string{"a"})
					in := store.NewInbound{Event: store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "90001", Kind: "message", CreatedAt: 2}, PlatformID: "90005", Observers: []string{"b"}}
					hidden, err := qq.AppendInbound(in)
					if err != nil {
						t.Fatal(err)
					}
					plan := store.NewPlan{ID: "plan", CharacterID: "a", Conversation: conv, Account: "90001", Origin: "character", Kind: store.PlanKindSend, At: 1, Event: &store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "character", ActorID: "a", Kind: "message", CreatedAt: 3, Text: "编造回复"}, Observers: []string{"a"}, Parts: []store.NewPart{{Kind: "text", Text: "编造回复"}}}
					planSeq, err := qq.AppendPlan(plan)
					if err != nil {
						t.Fatal(err)
					}
					var planID string
					if err = qq.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, planSeq).Scan(&planID); err != nil {
						t.Fatal(err)
					}
					burstIDs := prepareBurst(t, qq, conv, size)
					entered, release := make(chan struct{}), make(chan struct{})
					var calls atomic.Int64
					// 读取用自己的上下文，只由 cancelBase 取消。watch 给每次 read 套的 5 秒时限会盖住下面整条多页读取；
					// 生产里 watch 每次只读一页，每页的 5 秒时限由 ReadMessages 自己负责。这里一千条要读两页，
					// 机器被别的包抢满时第一页就能吃掉大半个 5 秒，第二页随即失败，和交错无关。
					base, cancelBase := context.WithCancel(context.Background())
					read := func(context.Context) (api.MessagePage, error) {
						n := calls.Add(1)
						var page api.MessagePage
						var cursor api.Cursor
						for {
							result, err := s.ReadMessages(base, req, cursor)
							if err != nil {
								return page, err
							}
							page.Items = append(page.Items, result.Value.Items...)
							page.HighWater = result.Value.HighWater
							if !result.Value.HasMore {
								break
							}
							cursor = result.Value.NextCursor
						}
						if n == 1 && stage == "during-read" {
							close(entered)
							select {
							case <-release:
							case <-base.Done():
								return api.MessagePage{}, base.Err()
							}
						}
						return page, nil
					}
					expected := map[string]bool{original: true, planID: true, hidden.EventID: true}
					for _, id := range burstIDs {
						expected[id] = true
					}
					commit := func() {
						if ok, err := qq.EndPart(plan.ID, 0, []string{store.PartPending}, store.PartAborted, store.PartEnd{}, 2); err != nil || !ok {
							t.Fatalf("批量提交和旧发送状态修正: %v %v", ok, err)
						}
						in.Observers = []string{"a"}
						if _, err := qq.AppendInbound(in); err != nil {
							t.Fatal(err)
						}
						seq, err := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, Kind: "notice", ActorKind: "human", ActorID: "90001", CreatedAt: int64(size + 5), ReplyTo: &original, Segments: map[string]any{"notice": "recall"}}, []string{"a"})
						if err != nil {
							t.Fatal(err)
						}
						var recallID string
						if err = qq.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&recallID); err != nil {
							t.Fatal(err)
						}
						expected[recallID] = true
					}
					if stage == "before-register" {
						commit()
					}
					sub := watch(context.Background(), []store.Named{{Platform: "qq", Store: qq}}, nil, read)
					defer sub.Close()
					defer cancelBase()
					if stage == "during-read" {
						<-entered
						commit()
						close(release)
					}
					v := next(t, sub)
					if stage == "after-initial" {
						commit()
					}
					complete := func(page api.MessagePage) bool {
						if len(page.Items) < len(expected) {
							return false
						}
						recalled, aborted := false, false
						for _, m := range page.Items {
							if m.Ref.Event.EventID == original {
								recalled = m.Recalled
							}
							if m.Ref.Event.EventID == planID {
								aborted = m.Aborted
							}
						}
						return recalled && aborted
					}
					for !complete(v.Value) {
						v = next(t, sub)
					}
					got := map[string]bool{}
					for _, m := range v.Value.Items {
						got[m.Ref.Event.EventID] = true
						if m.Ref.Event.EventID == original && !m.Recalled {
							t.Fatal("交错丢失旧撤回")
						}
						if m.Ref.Event.EventID == planID && !m.Aborted {
							t.Fatal("交错丢失旧发送状态")
						}
					}
					if !reflect.DeepEqual(got, expected) {
						t.Fatalf("交错集合不同 got=%d want=%d", len(got), len(expected))
					}
					rows, err := qq.Events.Query(`SELECT e.id FROM events e WHERE e.conversation=? AND EXISTS (SELECT 1 FROM observations o WHERE o.event_seq=e.seq AND o.character_id='a')`, conv)
					if err != nil {
						t.Fatal(err)
					}
					sqlIDs := map[string]bool{}
					for rows.Next() {
						var id string
						if err = rows.Scan(&id); err != nil {
							t.Fatal(err)
						}
						sqlIDs[id] = true
					}
					if err = rows.Err(); err != nil {
						t.Fatal(err)
					}
					rows.Close()
					if !reflect.DeepEqual(got, sqlIDs) {
						t.Fatal("交错与独立 SQL 集合不同")
					}
					sub.Close()
					before := calls.Load()
					add(t, qq, conv, "message", "human", 2, []string{"a"})
					if calls.Load() != before {
						t.Fatal("Close 返回后仍查库")
					}
				})
			}
		}
	}
}

func TestSubscriptionCancellationWaitsForQuery(t *testing.T) {
	t.Parallel()
	_, set := fixture(t)
	qq, _ := set.Platform("qq")
	entered, exited := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	sub := watch(context.Background(), []store.Named{{Platform: "qq", Store: qq}}, nil, func(ctx context.Context) (int, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(exited)
		return 0, ctx.Err()
	})
	<-entered
	sub.Close()
	<-exited
	sub.Close()
	if calls.Load() != 1 {
		t.Fatalf("取消后查询次数=%d", calls.Load())
	}
	if _, ok := <-sub.Updates(); ok {
		t.Fatal("取消仍有发布")
	}
}

func TestOldRowsObservationRecallAndStatus(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	original := add(t, qq, conv, "message", "human", 1, []string{"a"})
	in := store.NewInbound{Event: store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "90001", Kind: "message", CreatedAt: 2}, PlatformID: "90005", Observers: []string{"b"}}
	hidden, e := qq.AppendInbound(in)
	if e != nil {
		t.Fatal(e)
	}
	req := api.ConversationRequest{Key: keyOf(conv), Character: "a", Limit: 200}
	sub, e := s.WatchConversation(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	next(t, sub)
	in.Observers = []string{"a"}
	if _, e = qq.AppendInbound(in); e != nil {
		t.Fatal(e)
	}
	v := next(t, sub)
	ids := map[string]bool{}
	for _, m := range v.Value.Page.Items {
		ids[m.Ref.Event.EventID] = true
	}
	if !reflect.DeepEqual(ids, map[string]bool{original: true, hidden.EventID: true}) {
		t.Fatalf("补观察未更新: %v", ids)
	}
	if _, e = qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, Kind: "notice", ActorKind: "human", ActorID: "90001", CreatedAt: 3, ReplyTo: &original, Segments: map[string]any{"notice": "recall"}}, []string{"a"}); e != nil {
		t.Fatal(e)
	}
	for {
		v = next(t, sub)
		recalled := false
		for _, m := range v.Value.Changed {
			if m.Ref.Event.EventID == original && m.Recalled {
				recalled = true
			}
		}
		if recalled {
			break
		}
	}
}
