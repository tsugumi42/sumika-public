package main

// 概览订阅的更新要一直送到界面（规格-界面接Go后台 4.2）。
// 用一个自己能推快照的假数据源：第一份概览到了以后界面会自动打开第一个本地会话，之后的概览快照仍要并进会话列表。

import (
	"context"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/api"
)

type scriptedReader struct {
	demoReader
	overview chan api.Snapshot[api.Overview]
}

type idleSub[T any] struct{ ch chan api.Snapshot[T] }

func (s idleSub[T]) Updates() <-chan api.Snapshot[T] { return s.ch }
func (s idleSub[T]) Close()                          {}

func (r *scriptedReader) WatchOverview(context.Context, api.OverviewRequest) (api.Subscription[api.Overview], error) {
	return idleSub[api.Overview]{r.overview}, nil
}
func (r *scriptedReader) WatchConversation(context.Context, api.ConversationRequest) (api.Subscription[api.ConversationSnapshot], error) {
	return idleSub[api.ConversationSnapshot]{make(chan api.Snapshot[api.ConversationSnapshot])}, nil
}

func overviewWith(lastText string) api.Snapshot[api.Overview] {
	local := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:example"}
	group := api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:300"}
	last := &api.MessageView{Text: lastText, CreatedAt: 1}
	return api.Snapshot[api.Overview]{Version: 1, Result: api.Result[api.Overview]{State: api.Ready, Value: api.Overview{
		Characters:    api.List[api.Result[api.CharacterView]]{Items: []api.Result[api.CharacterView]{{State: api.Ready, Value: api.CharacterView{ID: "example", Name: "编的角色"}}}},
		Conversations: api.List[api.ConversationView]{Items: []api.ConversationView{{Key: local, Title: "日常"}, {Key: group, Title: "编的群", Last: last}}},
	}}}
}

// 第一份概览让界面自己打开了本地会话；接着来的第二份（群里来了新消息）不能被丢掉
func TestOverviewUpdatesKeepArrivingAfterFirstSnapshot(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &app{ctx: ctx, cancel: cancel, recalled: map[string]bool{}, speakers: map[string]bool{}, keys: map[string]api.ConversationKey{}}
	a.postFn = func(f func()) { mu.Lock(); defer mu.Unlock(); f() }
	r := &scriptedReader{overview: make(chan api.Snapshot[api.Overview])}
	mu.Lock()
	a.attach(r)
	mu.Unlock()

	groupLast := func() string {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range a.convs {
			if c.Key.Conversation == "qq:group:300" {
				return c.Last.Text
			}
		}
		return ""
	}
	for _, text := range []string{"旧消息", "新消息"} {
		select {
		case r.overview <- overviewWith(text):
		case <-time.After(uiWaitLimit):
			t.Fatalf("概览订阅没人收：%q", text)
		}
		deadline := time.Now().Add(5 * time.Second)
		for groupLast() != text && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := groupLast(); got != text {
			t.Fatalf("会话列表里群的最后一条应是 %q，实际 %q", text, got)
		}
	}
}
