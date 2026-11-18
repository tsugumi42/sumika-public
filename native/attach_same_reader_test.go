package main

// 宿主把同一个后台的只读接口再发布一次（比如"不能开始接收"只改了提示文字）时，界面换数据源要认出它没变，
// 不能在比较两个接口值时崩掉，也不能把正在看的会话和订阅清掉。

import (
	"context"
	"sync"
	"testing"

	"sumika-native/internal/api"
)

func TestAttachSameRealReaderTwiceKeepsSelection(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	defer h.shutdown()
	r := log.last().Reader
	if r == nil {
		t.Fatalf("宿主没给只读接口：%+v", log.last())
	}
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &app{ctx: ctx, cancel: cancel, recalled: map[string]bool{}, speakers: map[string]bool{}, keys: map[string]api.ConversationKey{}}
	a.postFn = func(f func()) { mu.Lock(); defer mu.Unlock(); f() }
	mu.Lock()
	defer mu.Unlock()
	a.attach(r)
	a.sel = "console:example"
	gen := a.gen
	a.attach(log.last().Reader) // 同一个后台，再发布一次
	if a.sel != "console:example" || a.gen != gen {
		t.Fatalf("同一个数据源再发布一次，不该清掉选中的会话：sel=%q gen %d→%d", a.sel, gen, a.gen)
	}
}
