package main

// 远端后台（规格-Bun后台第1批 第五节）：界面接上协议客户端，看得到真实读服务里的会话；
// 客户端没有写接口，界面的写操作全按"只读"降级。

import (
	"context"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/paths"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcserve"
	"sumika-native/internal/store"
)

func TestRemoteReaderFeedsConversationList(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	qq, _ := set.Platform("qq")
	if _, err := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: "qq:group:90002", Kind: "message", ActorKind: "human", ActorID: "90001", Text: "远端来的编造消息", CreatedAt: time.Now().UnixMilli()}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	svc := desktopread.New(set, []api.CharacterView{{ID: "a", Name: "编造甲"}}, nil)
	defer svc.Close()

	st, ct := rpcserve.Pipe()
	go rpcserve.New(&desktopread.Reader{Service: svc}, "test").Serve(context.Background(), st)
	cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ccancel()
	c, err := rpcclient.New(cctx, ct, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	remote := &rpcclient.Reader{C: c}
	if _, ok := api.Reader(remote).(api.LocalChat); ok {
		t.Fatal("远端客户端不该有本地发送接口")
	}

	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &app{ctx: ctx, cancel: cancel, recalled: map[string]bool{}, speakers: map[string]bool{}, keys: map[string]api.ConversationKey{}}
	a.postFn = func(f func()) { mu.Lock(); defer mu.Unlock(); f() }
	mu.Lock()
	a.attach(remote)
	mu.Unlock()
	deadline := time.Now().Add(uiWaitLimit)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, cv := range a.convs {
			if cv.Key.Conversation == "qq:group:90002" && cv.Last.Text == "远端来的编造消息" {
				mu.Unlock()
				return
			}
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("经协议连的界面没看到会话")
}
