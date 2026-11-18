package turn

// Register（Hub.Use 经 use）和 reconcile 并发：reconcile 遍历 p.order 要在 p.mu 之内拷一份，
// 不然和 register 追加的写入是数据竞争。放回旧代码：go test -race 报 DATA RACE（plugin_reconcile.go 的 order 读和 register 的写）。
// 不带 -race 时这里只检查两边都跑完、不死锁。

import (
	"fmt"
	"sync"
	"testing"
)

func TestRegisterAndReconcileConcurrent(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			h.plugins.reconcile()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = h.plugins.register(testPlugin{id: fmt.Sprintf("late%d", i), setup: func(*Registry) (func(), error) { return nil, nil }})
		}
	}()
	wg.Wait()
	if n := len(h.PluginStatuses()); n != 200 {
		t.Fatalf("应该登记 200 个插件，实际 %d", n)
	}
}
