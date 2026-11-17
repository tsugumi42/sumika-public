package app

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
)

// 第二个渠道（规格-多平台装配第 3 步）：用假适配器当一个虚构平台，和 QQ 一起装上。它收到的一条消息落进它自己的
// 平台库、QQ 库里没有，分流走它自己的规矩；关机时两个适配器都停了。QQ 的行为由其余测试原样钉着。
func TestSecondChannelLandsInItsOwnPlatform(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fa := fake.New("fakeplat", types.Caps{})
	var dispatched atomic.Int32
	opts := e.options()
	opts.ExtraChannels = []ChannelSpec{{
		Channel: "fakeplat", Platform: "fakeplat", Account: "9001", Character: "example", Adapter: fa,
		Admit:       func(in types.Inbound) channel.Admission { return channel.Admission{Drop: in.Conv.ID == "78"} },
		AdmitNotice: func(types.Notice) channel.Admission { return channel.Admission{Drop: true} },
		Dispatch:    func(channel.Dispatch) { dispatched.Add(1) },
	}}
	a, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatalf("Open：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	stopped := false
	defer func() {
		if !stopped {
			cancel()
			<-done
		}
	}()
	select {
	case <-fa.Started():
	case <-time.After(20 * time.Second):
		t.Fatal("虚构渠道的适配器没起来")
	}
	// 先塞一条它自己的准入说丢的（会话 78），再塞一条收的（77）：按顺序处理，77 分流出去时 78 早就过了准入
	dropped := types.Conversation{Channel: "fakeplat", Scene: types.SceneGroup, ID: "78"}
	conv := types.Conversation{Channel: "fakeplat", Scene: types.SceneGroup, ID: "77"}
	for i, c := range []types.Conversation{dropped, conv} {
		if err := fa.PushInbound(context.Background(), types.Inbound{
			Kind: types.InboundMessage, Account: "9001", Conv: c, PlatformID: fmt.Sprintf("m%d", i),
			From: types.User{ID: "55", Nick: "编的甲"}, Time: e.now, Segs: []types.Seg{{Kind: types.SegText, Text: "编的话"}},
		}); err != nil {
			t.Fatalf("往虚构渠道塞消息：%v", err)
		}
	}
	waitFor(t, "虚构渠道那一批走到它自己的分流", func() bool { return dispatched.Load() == 1 })
	count := func(platform string, conv types.Conversation) int {
		t.Helper()
		st, ok := a.set.Platform(platform)
		if !ok {
			t.Fatalf("没打开平台 %s 的库", platform)
		}
		var n int
		if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ?`, conv.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got, qq := count("fakeplat", conv), count("qq", conv); got != 1 || qq != 0 {
		t.Fatalf("虚构渠道的消息应该只在它自己的库里：fakeplat %d 条、qq %d 条", got, qq)
	}
	if n := count("fakeplat", dropped); n != 0 {
		t.Fatalf("它自己的准入说丢的消息入了库：%d 条", n)
	}
	// 还没有自己发送规矩的渠道一律只记录、不发：私聊和群都是（QQ 的档位由其余测试钉着）
	for _, c := range []types.Conversation{conv, {Channel: "fakeplat", Scene: types.ScenePrivate, Self: "9001", ID: "55"}} {
		if m := a.modeOf(c); m != channel.ModeRecord {
			t.Fatalf("虚构渠道 %s 的档位是 %q，应该只记录", c.Scene, m)
		}
	}
	cancel()
	stopped = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回：%v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("关机一分钟没关完")
	}
	if fa.Running() {
		t.Fatal("关机以后虚构渠道的适配器还在跑")
	}
}

// 多装的渠道写错了（缺函数、渠道名撞上 QQ、适配器名对不上）Open 就报错，不替它补放行的默认；报错以后数据锁放掉了，照常能开
func TestExtraChannelSpecRejected(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ok := func(name string) ChannelSpec {
		return ChannelSpec{Channel: name, Platform: name, Account: "9001", Character: "example", Adapter: fake.New(name, types.Caps{}),
			Admit:       func(types.Inbound) channel.Admission { return channel.Admission{} },
			AdmitNotice: func(types.Notice) channel.Admission { return channel.Admission{} },
			Dispatch:    func(channel.Dispatch) {}}
	}
	noAdmit := ok("fakeplat")
	noAdmit.Admit = nil
	asQQ := ok("qq")
	asQQ.Platform = "fakeplat"
	misnamed := ok("fakeplat")
	misnamed.Adapter = fake.New("other", types.Caps{})
	for name, spec := range map[string]ChannelSpec{"缺准入": noAdmit, "渠道名是 qq": asQQ, "适配器名对不上": misnamed} {
		opts := e.options()
		opts.ExtraChannels = []ChannelSpec{spec}
		if a, err := Open(context.Background(), opts); err == nil {
			a.Close()
			t.Fatalf("%s：Open 应该报错", name)
		}
	}
	e.open().Close()
}

// stuckAdapter 不认取消：Run 一直等到测试放它走
type stuckAdapter struct {
	*fake.Adapter
	release chan struct{}
}

func (s *stuckAdapter) Run(context.Context, types.Sink) error { <-s.release; return nil }

// 有个适配器不认取消时，关机第 1 步等到时限就往下走，后面照样关库（不然 QQ 的发送锁不交还、库不关）
func TestShutdownDoesNotHangOnStuckAdapter(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	stuck := &stuckAdapter{Adapter: fake.New("stuckplat", types.Caps{}), release: make(chan struct{})}
	t.Cleanup(func() { close(stuck.release) })
	opts := e.options()
	opts.ExtraChannels = []ChannelSpec{{Channel: "stuckplat", Platform: "stuckplat", Account: "9002", Character: "example", Adapter: stuck,
		Admit:       func(types.Inbound) channel.Admission { return channel.Admission{} },
		AdmitNotice: func(types.Notice) channel.Admission { return channel.Admission{} },
		Dispatch:    func(channel.Dispatch) {}}}
	a, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatalf("Open：%v", err)
	}
	a.shutdownWait = 2 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run 在关机时限以后还没返回")
	}
	waitFor(t, "关机走完、库关掉", func() bool {
		_, err := a.set.For("qq")
		return err != nil && strings.Contains(err.Error(), "已经关了")
	})
}
