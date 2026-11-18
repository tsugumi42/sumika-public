package channel

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 大图上传慢时出口的兜底时限也要跟着放宽（用户 2026-11-13，规格-链接发送改版 B12 第 4 条）：
// 适配器放宽到 10 秒加按帧长算的量，出口的 30 秒兜底不放宽就会先到点、取消适配器的调用。
// 6 MiB 的图：段里媒体的字节数乘 3/2（base64 和 JSON 的膨胀）= 9 MiB，每 MiB 多等 2 秒（前 256 KiB 不算），兜底约 47.5 秒。
func TestSendTimeoutGrowsWithMediaBytes(t *testing.T) {
	t.Parallel()
	h := newOH(t, mediaCaps, unguardedLinks)
	h.run(nil)
	cancelled := make(chan struct{})
	h.ad.OnSend(func(ctx context.Context, c fake.SendCall) (types.Receipt, error) {
		<-ctx.Done()
		close(cancelled)
		return types.Receipt{}, nil
	})
	m := types.MemoryMedia("big", "image/png", 4000, 3000, make([]byte, 6<<20))
	body := []types.BodySegment{{Kind: types.BodyText, Text: "大图"}, {Kind: types.BodyImage, Media: m}}
	id, err := h.out.SendOwned(context.Background(), "ca", "automation:link-reader", group("g1"), []types.Part{{Kind: types.PartMessage, Body: body}}, []*types.MediaResource{m})
	if err != nil {
		t.Fatal(err)
	}
	h.waitSent(1)
	limit := 30*time.Second + types.UploadAllowance(m.Size*3/2)
	if limit < 45*time.Second {
		t.Fatalf("编的大图该让兜底时限明显变长：%v", limit)
	}
	h.clk.WaitTimer(t, limit)
	h.clk.Advance(30 * time.Second) // 旧的 30 秒兜底在这里取消
	select {
	case <-cancelled:
		t.Fatalf("30 秒就取消了大图的发送")
	case <-time.After(50 * time.Millisecond):
	}
	if st := h.plan(id).Parts[0].State; st != store.PartSending {
		t.Fatalf("30 秒时还在发：%s", st)
	}
	h.clk.Advance(limit - 30*time.Second)
	h.waitPlan(id, store.PlanUnknown)
	<-cancelled
}

// 视频不算：本机文件地址交给协议端，不在帧里，放宽它的时限会拖慢视频的退路
func TestPartMediaBytesSkipsVideo(t *testing.T) {
	img := types.MemoryMedia("i", "image/png", 1, 1, make([]byte, 1000))
	vid := types.MemoryMedia("v", "video/mp4", 1, 1, make([]byte, 5000))
	p := types.Part{Kind: types.PartForward, Body: []types.BodySegment{{Kind: types.BodyImage, Media: img}},
		Nodes: []types.ForwardNode{{Body: []types.BodySegment{{Kind: types.BodyGIF, Media: img}, {Kind: types.BodyVideo, Media: vid}}}}}
	if got := types.PartMediaBytes(p); got != 2000 {
		t.Fatalf("只算图片和动图：%d", got)
	}
}

func TestUploadAllowance(t *testing.T) {
	for n, want := range map[int64]time.Duration{
		-1: 0, 0: 0, 3: 0, 256 << 10: 0, 1 << 20: 1500 * time.Millisecond, 6 << 20: 11500 * time.Millisecond, 9 << 20: 17500 * time.Millisecond,
		1 << 40: types.MaxUploadAllowance,
	} {
		if got := types.UploadAllowance(n); got != want {
			t.Errorf("UploadAllowance(%d) = %v，要 %v", n, got, want)
		}
	}
	if got := types.UploadAllowance(512 << 10); got != 500*time.Millisecond {
		t.Errorf("512 KiB 的图只多等半秒：%v", got)
	}
}
