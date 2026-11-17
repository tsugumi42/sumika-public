package alert

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func recv(t *testing.T, s *Sub) Change {
	t.Helper()
	select {
	case c, ok := <-s.C():
		if !ok {
			t.Fatal("订阅已关闭")
		}
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("2 秒内没收到变化")
	}
	return Change{}
}

func none(t *testing.T, s *Sub) {
	t.Helper()
	select {
	case c := <-s.C():
		t.Fatalf("不该有输出，收到 %+v", c)
	case <-time.After(60 * time.Millisecond):
	}
}

func conn(title string) Alert {
	return Alert{Key: "qq:甲:conn", Class: "账号掉线", Title: title, Body: "连接断了"}
}

func TestSameKeyTwiceOnlyOneRaised(t *testing.T) {
	h := New()
	s := h.Subscribe()
	defer s.Close()
	h.Raise(conn("掉线"))
	h.Raise(Alert{Key: "qq:甲:conn", Title: "掉线", Body: "断了 1 分钟"})
	if c := recv(t, s); c.Kind != Raised {
		t.Fatalf("第一次应为 Raised，得到 %v", c.Kind)
	}
	c := recv(t, s)
	if c.Kind != Updated || c.Alert.Body != "断了 1 分钟" {
		t.Fatalf("第二次应为 Updated 且正文更新，得到 %+v", c)
	}
	if got := h.Open(); len(got) != 1 || got[0].Body != "断了 1 分钟" {
		t.Fatalf("Open 应只有一条最新的，得到 %+v", got)
	}
}

func TestTitleChangeRaisesAgain(t *testing.T) {
	h := New()
	s := h.Subscribe()
	defer s.Close()
	h.Raise(conn("掉线"))
	h.Raise(conn("被踢下线"))
	recv(t, s)
	if c := recv(t, s); c.Kind != Raised || c.Alert.Title != "被踢下线" {
		t.Fatalf("换标题应为 Raised，得到 %+v", c)
	}
	if n := len(h.Open()); n != 1 {
		t.Fatalf("Open 应为 1 条，得到 %d", n)
	}
}

func TestClearNotOpenIsSilent(t *testing.T) {
	h := New()
	s := h.Subscribe()
	defer s.Close()
	h.Clear("qq:甲:conn", Note{Title: "恢复了"})
	none(t, s)
	h.Raise(conn("掉线"))
	recv(t, s)
	h.Clear("qq:甲:conn", Note{Title: "恢复了", Body: "断了 2 分钟"})
	c := recv(t, s)
	if c.Kind != Cleared || c.Note.Body != "断了 2 分钟" || c.Alert.Title != "掉线" {
		t.Fatalf("得到 %+v", c)
	}
	h.Clear("qq:甲:conn", Note{Title: "恢复了"}) // 已关，再清无输出
	none(t, s)
	if len(h.Open()) != 0 {
		t.Fatal("Open 应为空")
	}
}

func TestRaiseClearRaiseAgain(t *testing.T) {
	h := New()
	s := h.Subscribe()
	defer s.Close()
	h.Raise(conn("掉线"))
	h.Clear("qq:甲:conn", Note{Title: "恢复了"})
	h.Raise(conn("掉线"))
	for i, want := range []Kind{Raised, Cleared, Raised} {
		if c := recv(t, s); c.Kind != want {
			t.Fatalf("第 %d 条应为 %v，得到 %v", i, want, c.Kind)
		}
	}
}

func TestOpenOrder(t *testing.T) {
	h := New()
	h.Raise(Alert{Key: "a", Title: "1"})
	h.Raise(Alert{Key: "b", Title: "1"})
	h.Raise(Alert{Key: "c", Title: "1"})
	h.Raise(Alert{Key: "a", Title: "2"}) // 换标题不挪位置
	h.Clear("b", Note{})
	h.Raise(Alert{Key: "b", Title: "1"}) // 再开排最后
	var keys string
	for _, a := range h.Open() {
		keys += a.Key
	}
	if keys != "acb" {
		t.Fatalf("顺序应为 acb，得到 %s", keys)
	}
}

func TestStuckSubscriberDoesNotBlockAndNothingLost(t *testing.T) {
	h := New()
	s := h.Subscribe()
	defer s.Close()
	const n = 5000
	done := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ { // 订阅者一条都不读
			h.Raise(Alert{Key: "k", Title: fmt.Sprint(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("订阅者不读时 Raise 被阻塞")
	}
	for i := 0; i < n; i++ {
		c := recv(t, s)
		if c.Alert.Title != fmt.Sprint(i) {
			t.Fatalf("第 %d 条应为 %d，得到 %s（丢了或乱序）", i, i, c.Alert.Title)
		}
	}
}

func TestSubscribeWithOpenSnapshotThenChanges(t *testing.T) {
	h := New()
	h.Raise(Alert{Key: "a", Title: "旧"})
	s, snap := h.SubscribeWithOpen()
	defer s.Close()
	if len(snap) != 1 || snap[0].Key != "a" {
		t.Fatalf("快照 %+v", snap)
	}
	none(t, s) // 快照里的不会再作为变化送一遍
	h.Clear("a", Note{})
	if c := recv(t, s); c.Kind != Cleared {
		t.Fatalf("得到 %+v", c)
	}
}

func TestCloseStopsAndIsRepeatable(t *testing.T) {
	h := New()
	s := h.Subscribe()
	s.Close()
	s.Close()
	h.Raise(Alert{Key: "a", Title: "x"})
	select {
	case _, ok := <-s.C():
		if ok {
			t.Fatal("关闭后不该再收到")
		}
	case <-time.After(time.Second):
		t.Fatal("C 应已关闭")
	}
}

func TestEachSubscriberGetsAll(t *testing.T) {
	h := New()
	s1, s2 := h.Subscribe(), h.Subscribe()
	defer s1.Close()
	defer s2.Close()
	h.Raise(Alert{Key: "a", Title: "x"})
	if recv(t, s1).Kind != Raised || recv(t, s2).Kind != Raised {
		t.Fatal("两个订阅者都应收到")
	}
}

func TestConcurrentRaiseClear(t *testing.T) {
	h := New()
	s := h.Subscribe()
	defer s.Close()
	var raised, cleared int
	finished := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case c := <-s.C():
				switch c.Kind {
				case Raised:
					raised++
				case Cleared:
					cleared++
				}
			case <-stop:
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			key := fmt.Sprintf("k%d", g%3)
			for i := 0; i < 500; i++ {
				h.Raise(Alert{Key: key, Title: "t"})
				_ = h.Open()
				h.Clear(key, Note{})
			}
		}(g)
	}
	wg.Wait()
	// 等队列送完再停读
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		s.mu.Lock()
		empty := len(s.q) == 0
		s.mu.Unlock()
		if empty {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	<-finished
	if len(h.Open()) != 0 {
		t.Fatalf("最后应全部关掉，Open=%+v", h.Open())
	}
	// 同键并发时 Raise 可能落在已开着的键上（Updated），所以 Raised 不会多于 Cleared，
	// 且每个 Cleared 之前必有一个 Raised。
	if raised != cleared {
		t.Fatalf("开关次数应相等：Raised %d，Cleared %d", raised, cleared)
	}
}
