package forwardfetch

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 工人先启动不代表协议端已连接；积压转发只在连接状态通知后的补扫取回。
func TestForwardSweepWaitsForConnection(t *testing.T) {
	st := forwardStore(t)
	reader := &treeReader{trees: map[string]types.ForwardTree{}}
	add := func(id string) {
		t.Helper()
		reader.mu.Lock()
		reader.trees[id] = types.ForwardTree{ID: id, Nodes: []types.ForwardMessageNode{{Name: "编的转发者"}}}
		reader.mu.Unlock()
		_, err := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: "qq:group:90002", ActorKind: "human", ActorID: "qq:10001", Kind: "message", CreatedAt: time.Now().UnixMilli(), Segments: struct {
			Segs []types.Seg `json:"segs"`
		}{[]types.Seg{{Kind: types.SegForward, Ref: id}}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	add("first")
	f := New(Options{Store: st, Reader: reader, Account: "100", Logf: t.Logf})
	f.Start()
	defer f.Close()
	// 留出工人执行机会，实际观察协议调用；旧 Start 会在这里取树。
	time.Sleep(150 * time.Millisecond)
	reader.mu.Lock()
	calls := append([]string(nil), reader.calls...)
	reader.mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("尚未连接就取了转发：%v", calls)
	}
	for _, id := range []string{"first", "reconnected"} {
		if id == "reconnected" {
			add(id)
		}
		f.Resweep()
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, have, err := st.ReadForward(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if have {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("连接后没有补回 %s", id)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if len(reader.calls) != 2 || reader.calls[0] != "first" || reader.calls[1] != "reconnected" {
		t.Fatalf("首次连接和重连调用：%v", reader.calls)
	}
}
