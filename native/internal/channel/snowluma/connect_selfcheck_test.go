package snowluma

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/selfcheck"
)

// S5（规格-运行时自检 B4）：一次断线里的多次退避只数 1 次断开事件；连上又断了才数第 2 次。
// 数的是"连接真的断了"和"从在线变成连不上"，不是重试次数。
func TestSelfcheckDisconnectCountedOncePerOutage(t *testing.T) {
	clk := fake.NewClock(time.Unix(1000, 0))
	p := newPeer(t, testToken)
	alerts := newTestAlerts()
	a := newWS(t, p, clk, alerts)
	sc := selfcheck.NewCounters()
	a.cfg.Selfcheck = sc
	sink, cancel, done := runWS(t, a)

	c1 := p.accept(t)
	recvStatus(t, sink, types.StatusConnected)
	if n := sc.Get(selfcheck.Reconnects); n != 0 {
		t.Fatalf("连着的时候不该有断开事件：%d", n)
	}
	p.fail.Store(true) // 断线以后协议端一直不在线
	c1.CloseNow()
	recvStatus(t, sink, types.StatusDisconnected)
	// 连不上：退避 1 秒、2 秒、4 秒各重试一次，都失败
	for _, d := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		clk.WaitTimer(t, d)
		clk.Advance(d)
		recvStatus(t, sink, types.StatusReconnecting)
	}
	if n := sc.Get(selfcheck.Reconnects); n != 1 {
		t.Fatalf("同一次断线里的 3 次重试只算 1 次断开事件：%d", n)
	}

	p.fail.Store(false)
	clk.WaitTimer(t, 8*time.Second)
	clk.Advance(8 * time.Second)
	c2 := p.accept(t)
	recvStatus(t, sink, types.StatusConnected)
	c2.CloseNow() // 连上又断：第二次断开事件
	recvStatus(t, sink, types.StatusDisconnected)
	if n := sc.Get(selfcheck.Reconnects); n != 2 {
		t.Fatalf("连上又断了应数第 2 次：%d", n)
	}
	cancel()
	waitRun(t, done)
	if n := sc.Get(selfcheck.Reconnects); n != 2 {
		t.Fatalf("关机（ctx 取消）不算断开事件：%d", n)
	}
}

// S1（规格-运行时自检 B3，形状 × 入口：解码失败、不是消息、空消息体）：
// 每一帧恰好落进一个去向，收到 = 解码失败 + 不是消息 + 交给入库；删掉任何一行埋点这里都会红。
func TestSelfcheckFramesEachLandInExactlyOneBucket(t *testing.T) {
	sc := selfcheck.NewCounters()
	a := &Adapter{cfg: Config{Selfcheck: sc}}
	rec := &fake.Recorder{}
	ctx := context.Background()
	counts := func() [4]int64 {
		return [4]int64{sc.Get(selfcheck.FrameReceived), sc.Get(selfcheck.FrameDecodeFailed), sc.Get(selfcheck.FrameNonMessage), sc.Get(selfcheck.FrameToIntake)}
	}
	check := func(name string, want [4]int64) {
		t.Helper()
		if got := counts(); got != want {
			t.Fatalf("%s：收到、解码失败、不是消息、交给入库 = %v，要 %v", name, got, want)
		}
		if got := sc.Get(selfcheck.FrameReceived) - (sc.Get(selfcheck.FrameDecodeFailed) + sc.Get(selfcheck.FrameNonMessage) + sc.Get(selfcheck.FrameToIntake)); got != 0 {
			t.Fatalf("%s：A 式差额 %d，要 0", name, got)
		}
	}

	a.dispatch(ctx, rec, []byte(`{坏掉的 json`))
	check("坏 JSON", [4]int64{1, 1, 0, 0})
	a.dispatch(ctx, rec, []byte(`{"post_type":"meta_event","meta_event_type":"heartbeat","time":100,"self_id":100,"interval":30000}`))
	check("心跳", [4]int64{2, 1, 1, 0})
	a.dispatch(ctx, rec, []byte(`{"post_type":"notice","notice_type":"bot_offline","time":100,"self_id":100,"message":"编的原因"}`))
	check("通知", [4]int64{3, 1, 2, 0})
	a.dispatch(ctx, rec, []byte(`{"post_type":"message","message_type":"private","time":100,"self_id":100,"user_id":200,"message_id":300,"sender":{"nickname":"虚构甲"},"message":[{"type":"text","data":{"text":"你好"}}]}`))
	check("消息", [4]int64{4, 1, 2, 1})
	if n := len(rec.Inbounds()); n != 1 {
		t.Fatalf("消息帧应交给接收端 1 条：%d", n)
	}
	// 空消息体：不管适配器翻成消息还是当作不是消息，都只落进一个桶
	a.dispatch(ctx, rec, []byte(`{"post_type":"message","message_type":"private","time":100,"self_id":100,"user_id":200,"message_id":301,"message":[]}`))
	if got := sc.Get(selfcheck.FrameReceived) - (sc.Get(selfcheck.FrameDecodeFailed) + sc.Get(selfcheck.FrameNonMessage) + sc.Get(selfcheck.FrameToIntake)); got != 0 || sc.Get(selfcheck.FrameReceived) != 5 {
		t.Fatalf("空消息体：收到 %d，A 式差额 %d", sc.Get(selfcheck.FrameReceived), got)
	}
}
