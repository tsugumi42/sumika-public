package snowluma

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 大图上传慢（用户 2026-11-13，规格-链接发送改版 B12 第 4 条）：十几 MB 的帧写到协议端、等回包要超过 10 秒。
// 以前整次调用只有 10 秒：到点取消 Write，coder/websocket 会在写入被取消时关掉整条连接，所有在途调用一起变成“写出以后连接断了”，
// 链接插件再把它判成被拒、转 180° 重发。现在帧越大时限越长（types.UploadAllowance），写入不再被 10 秒的计时器掐断。
// 小帧照旧 10 秒。

type callOut struct {
	frame []byte
	wrote bool
	err   error
}

func TestBigFrameSlowWriteIsNotCancelledAtTenSeconds(t *testing.T) {
	a, clk, c, l, sid := reviewAdapter()
	a.markReady(sid)
	big := strings.Repeat("A", 12<<20) // 12 MiB 的帧，按每 MiB 多等 2 秒（前 256 KiB 不算），总时限约 10 + 23.5 秒
	started, release := make(chan struct{}), make(chan struct{})
	var cause atomic.Value
	c.write = func(ctx context.Context, b []byte) error {
		close(started)
		select {
		case <-release:
			a.takeReply(withEcho(`{"retcode":0,"data":{"message_id":7}}`, parseAction(b).Echo))
			return nil
		case <-ctx.Done():
			cause.Store(context.Cause(ctx))
			return ctx.Err()
		}
	}
	done := make(chan callOut, 1)
	go func() {
		f, w, e := a.call(context.Background(), l, "send_group_msg", map[string]any{"message": big})
		done <- callOut{f, w, e}
	}()
	<-started
	clk.Advance(callTimeout + time.Second) // 第 11 秒：旧代码在这里取消写入、断连
	select {
	case r := <-done:
		t.Fatalf("11 秒就结束了（旧的 10 秒时限）：%+v 原因 %v", r.err, cause.Load())
	case <-time.After(50 * time.Millisecond):
	}
	if v := cause.Load(); v != nil {
		t.Fatalf("写入被取消了：%v", v)
	}
	clk.Advance(18 * time.Second) // 第 29 秒，还在约 33.5 秒的时限里
	close(release)                // 上传完，回包到
	r := <-done
	if r.err != nil || !r.wrote || len(r.frame) == 0 {
		t.Fatalf("慢上传后回包到了，该成功：%+v", r)
	}
}

// 放宽有上限：大帧也不是无限等。回包一直不来，到 10 秒加放宽量就按“没有回包”结束。
func TestBigFrameStillTimesOutEventually(t *testing.T) {
	a, clk, c, l, sid := reviewAdapter()
	a.markReady(sid)
	big := strings.Repeat("A", 12<<20)
	started := make(chan struct{})
	c.write = func(ctx context.Context, b []byte) error { close(started); return nil } // 写出去了，回包不来
	done := make(chan callOut, 1)
	go func() {
		f, w, e := a.call(context.Background(), l, "send_group_msg", map[string]any{"message": big})
		done <- callOut{f, w, e}
	}()
	<-started
	clk.Advance(callTimeout + 22*time.Second) // 32 秒：还没到约 33.5 秒
	select {
	case r := <-done:
		t.Fatalf("32 秒不该结束：%+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	clk.Advance(3 * time.Second)
	r := <-done
	if !errors.Is(r.err, errCallTimeout) || !r.wrote {
		t.Fatalf("到点该是没有回包：%+v", r)
	}
}

// 小帧不放宽：回包不来，10 秒就结束。
func TestSmallFrameKeepsTenSecondLimit(t *testing.T) {
	a, clk, c, l, sid := reviewAdapter()
	a.markReady(sid)
	started := make(chan struct{})
	c.write = func(context.Context, []byte) error { close(started); return nil }
	done := make(chan callOut, 1)
	go func() {
		f, w, e := a.call(context.Background(), l, "send_group_msg", map[string]any{"message": "小帧"})
		done <- callOut{f, w, e}
	}()
	<-started
	clk.Advance(callTimeout + time.Millisecond)
	r := <-done
	if !errors.Is(r.err, errCallTimeout) {
		t.Fatalf("小帧 10 秒到点：%+v", r)
	}
}
