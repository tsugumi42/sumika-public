package fake

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

var (
	groupConv = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "g1"}
	text1     = types.Part{ID: "p1", Conv: groupConv, Kind: types.PartMessage, Body: types.TextBody("你好")}
)

// 跑起来一个假适配器，返回它和停掉它的函数（停掉并等 Run 返回）
func run(t *testing.T, a *Adapter, sink types.Sink) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, sink) }()
	select {
	case <-a.Started():
	case <-time.After(2 * time.Second):
		t.Fatal("适配器 2 秒内没起来")
	}
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run 返回了 %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("取消后 Run 2 秒内没返回")
		}
	}
	t.Cleanup(stop)
	return stop
}

// 推进去的消息、通知、状态原样到 sink，按推的先后
func TestPushReachesSinkInOrder(t *testing.T) {
	a, rec := New("qq", types.Caps{}), &Recorder{}
	run(t, a, rec)
	ctx := context.Background()
	for i, id := range []string{"m1", "m2", "m3"} {
		if err := a.PushInbound(ctx, types.Inbound{Kind: types.InboundMessage, Account: "a", Conv: groupConv, PlatformID: id,
			Segs: []types.Seg{{Kind: types.SegText, Text: "第 " + string(rune('1'+i)) + " 条"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.PushNotice(ctx, types.Notice{Kind: types.NoticePoke, Actor: "u1", Target: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := a.PushStatus(ctx, types.Status{Kind: types.StatusConnected, Account: "a"}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, in := range rec.Inbounds() {
		got = append(got, in.PlatformID)
	}
	if len(got) != 3 || got[0] != "m1" || got[1] != "m2" || got[2] != "m3" {
		t.Errorf("消息顺序不对：%v", got)
	}
	if n := rec.Notices(); len(n) != 1 || n[0].Kind != types.NoticePoke {
		t.Errorf("通知 = %+v", n)
	}
	if s := rec.Statuses(); len(s) != 1 || s[0].Kind != types.StatusConnected {
		t.Errorf("状态 = %+v", s)
	}
}

// 关了以后推不进去，也不会到 sink；还没起来的同理（清单④：关掉的渠道还在回来的事件不能再进库）
func TestPushAfterStopDoesNotReachSink(t *testing.T) {
	a, rec := New("qq", types.Caps{}), &Recorder{}
	if err := a.PushInbound(context.Background(), types.Inbound{}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("还没起来就推：%v", err)
	}
	stop := run(t, a, rec)
	stop()
	for name, err := range map[string]error{
		"消息": a.PushInbound(context.Background(), types.Inbound{}),
		"通知": a.PushNotice(context.Background(), types.Notice{}),
		"状态": a.PushStatus(context.Background(), types.Status{}),
	} {
		if !errors.Is(err, ErrNotRunning) {
			t.Errorf("停了以后推%s：%v，要 ErrNotRunning", name, err)
		}
	}
	if len(rec.Inbounds())+len(rec.Notices())+len(rec.Statuses()) != 0 {
		t.Error("停了以后推的东西到了 sink")
	}
}

// 正在交给 sink 的事件，Run 要等它交完才返回：Run 返回的那一刻起，sink 不会再被调用
type slowSink struct {
	Recorder
	enter, release chan struct{}
	after          atomic.Bool // Run 返回以后又被调用
	runReturned    atomic.Bool
}

func (s *slowSink) Inbound(ctx context.Context, in types.Inbound) error {
	close(s.enter)
	<-s.release
	if s.runReturned.Load() {
		s.after.Store(true)
	}
	return s.Recorder.Inbound(ctx, in)
}

func TestRunWaitsForInflightEvents(t *testing.T) {
	a := New("qq", types.Caps{})
	sink := &slowSink{enter: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.Run(ctx, sink)
		sink.runReturned.Store(true)
		close(done)
	}()
	<-a.Started()
	pushed := make(chan error, 1)
	go func() { pushed <- a.PushInbound(context.Background(), types.Inbound{PlatformID: "m1"}) }()
	<-sink.enter // 事件已经在 sink 里了
	cancel()
	select {
	case <-done:
		t.Fatal("事件还在 sink 里，Run 就返回了")
	case <-time.After(50 * time.Millisecond):
	}
	close(sink.release)
	if err := <-pushed; err != nil {
		t.Fatal(err)
	}
	<-done
	if sink.after.Load() {
		t.Error("Run 返回以后 sink 又被调用了")
	}
}

func TestRunTwiceAtOnce(t *testing.T) {
	a := New("qq", types.Caps{})
	run(t, a, &Recorder{})
	if err := a.Run(context.Background(), &Recorder{}); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("同时第二个 Run：%v", err)
	}
	if err := New("qq", types.Caps{}).Run(context.Background(), nil); err == nil {
		t.Error("sink 是空的却跑起来了")
	}
}

// 普通文字始终可发，媒体逐场合查能力；戳一戳不走 Send。
func TestSendByKindAndCaps(t *testing.T) {
	for _, c := range []struct {
		media bool
		caps  types.Caps
		want  string // confirmed / unsupported
	}{
		{false, types.Caps{}, "confirmed"},
		{false, types.Caps{Poke: true}, "confirmed"},
		{true, types.Caps{}, "unsupported"},
		{true, types.Caps{MediaGroup: types.MediaCaps{Image: true, ImageBase64: true}}, "confirmed"},
	} {
		a := New("qq", c.caps)
		run(t, a, &Recorder{})
		p := text1
		if c.media {
			resource := types.MemoryMedia("编的图片", "image/png", 1, 1, []byte("fixture"))
			t.Cleanup(func() {
				if err := resource.Release(); err != nil {
					t.Error(err)
				}
			})
			p.Body = []types.BodySegment{{Kind: types.BodyImage, Media: resource}}
		}
		r, err := a.Send(context.Background(), "a", p)
		switch c.want {
		case "confirmed":
			if err != nil || r.Status != types.ReceiptConfirmed || r.PartID != p.ID || r.PlatformID == "" || r.Validate() != nil {
				t.Errorf("media=%v %+v：回执 %+v，error %v", c.media, c.caps, r, err)
			}
		case "unsupported":
			if !errors.Is(err, types.ErrUnsupported) {
				t.Errorf("media=%v %+v：error %v，要 ErrUnsupported", c.media, c.caps, err)
			}
		}
		if len(a.Sent()) != 1 {
			t.Errorf("media=%v %+v：调用记了 %d 次，要 1 次", c.media, c.caps, len(a.Sent()))
		}
	}
}

// 没起来、已经停了：Send、Poke 是明确失败（没连上），不是确认
func TestSendWhenNotRunningFails(t *testing.T) {
	a := New("qq", types.Caps{Poke: true})
	for name, call := range map[string]func() (types.Receipt, error){
		"Send": func() (types.Receipt, error) { return a.Send(context.Background(), "a", text1) },
		"Poke": func() (types.Receipt, error) { return a.Poke(context.Background(), "a", groupConv, "u1") },
	} {
		r, err := call()
		if err != nil || r.Status != types.ReceiptFailed || r.Validate() != nil {
			t.Errorf("没起来就 %s：回执 %+v，error %v", name, r, err)
		}
	}
}

// OnSend 规定的回执原样给，包括不合规的（零值）：假适配器不替中间件检查回执；调用记录里有账号、段、次序
func TestOnSendScriptsReceipts(t *testing.T) {
	a := New("qq", types.Caps{})
	fixed := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return fixed }
	run(t, a, &Recorder{})
	replies := []func(c SendCall) (types.Receipt, error){
		func(c SendCall) (types.Receipt, error) { return Unknown(c.Part.ID, "超时", c.At), nil },
		func(c SendCall) (types.Receipt, error) { return Failed(c.Part.ID, "协议端拒绝", c.At), nil },
		func(c SendCall) (types.Receipt, error) { return types.Receipt{}, nil },
	}
	a.OnSend(func(_ context.Context, c SendCall) (types.Receipt, error) { return replies[c.N-1](c) })
	want := []string{types.ReceiptUnknown, types.ReceiptFailed, ""}
	for i := range want {
		p := text1
		p.ID = "p" + string(rune('1'+i))
		r, err := a.Send(context.Background(), "acct-"+string(rune('a'+i)), p)
		if err != nil || r.Status != want[i] {
			t.Errorf("第 %d 次：回执 %+v，error %v，要状态 %q", i+1, r, err, want[i])
		}
	}
	sent := a.Sent()
	if len(sent) != 3 || sent[1].N != 2 || sent[1].Account != "acct-b" || sent[1].Part.ID != "p2" || !sent[1].At.Equal(fixed) {
		t.Errorf("调用记录 = %+v", sent)
	}
	// 记录是拷贝：改它不影响假适配器里的
	sent[0].Account = "改了"
	if a.Sent()[0].Account == "改了" {
		t.Error("Sent() 返回的不是拷贝")
	}
}

// OnSend 里阻塞的调用能被 ctx 取消（"回执延迟 10 秒"这类测试靠它不挂死）
func TestOnSendBlocksUntilCtxCancelled(t *testing.T) {
	a := New("qq", types.Caps{})
	run(t, a, &Recorder{})
	a.OnSend(func(ctx context.Context, c SendCall) (types.Receipt, error) {
		<-ctx.Done()
		return Unknown(c.Part.ID, "ctx 到期", c.At), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	r, err := a.Send(ctx, "a", text1)
	if err != nil || r.Status != types.ReceiptUnknown {
		t.Errorf("回执 %+v，error %v", r, err)
	}
}

func TestOnPokeAndPokeRecord(t *testing.T) {
	a := New("qq", types.Caps{Poke: true})
	run(t, a, &Recorder{})
	r, err := a.Poke(context.Background(), "a", groupConv, "u1")
	if err != nil || r.Status != types.ReceiptConfirmed || r.Validate() != nil {
		t.Fatalf("默认的戳：回执 %+v，error %v", r, err)
	}
	a.OnPoke(func(_ context.Context, c PokeCall) (types.Receipt, error) { return Failed("", "被限流", c.At), nil })
	if r, _ := a.Poke(context.Background(), "a", groupConv, "u2"); r.Status != types.ReceiptFailed {
		t.Errorf("OnPoke 没生效：%+v", r)
	}
	pokes := a.Pokes()
	if len(pokes) != 2 || pokes[1].N != 2 || pokes[1].Target != "u2" || pokes[1].Conv != groupConv {
		t.Errorf("戳的记录 = %+v", pokes)
	}
	if len(a.Sent()) != 0 {
		t.Error("戳一戳不该记进 Send 的调用")
	}
	// 不支持戳的适配器
	if _, err := New("qq", types.Caps{}).Poke(context.Background(), "a", groupConv, "u1"); !errors.Is(err, types.ErrUnsupported) {
		t.Errorf("Caps.Poke 为假：%v", err)
	}
}
