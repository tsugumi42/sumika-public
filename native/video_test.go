package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/api"
	"sumika-native/internal/segview"
	"sumika-native/internal/store"
)

type videoTestReader struct {
	demoReader
	prepare func(context.Context, api.MediaRef) (string, error)
}

func (r videoTestReader) PrepareVideo(ctx context.Context, ref api.MediaRef) (string, error) {
	return r.prepare(ctx, ref)
}

func TestVideoBubbleAndSystemOpen(t *testing.T) {
	key := demoKey("qq:group:90002")
	ref := &api.MediaRef{Source: api.EventRef{Conversation: key, Event: api.EventKey{Platform: "qq", EventID: "v"}}, Hash: "video"}
	old := openVideoFile
	defer func() { openVideoFile = old }()
	opened := make(chan string, 1)
	openVideoFile = func(path string) error { opened <- path; return nil }
	for _, width := range []int{480, 900, 2048} {
		for _, withCover := range []bool{false, true} {
			d := api.DisplaySegment{Kind: "video", Media: ref, DurationMS: 12500}
			if withCover {
				d.Cover = &api.MediaRef{Source: ref.Source, Hash: "poster"}
				d.Width, d.Height = 160, 90
			}
			m := msgFromView(key, api.MessageView{Ref: ref.Source, Kind: "message", ActorKind: "human", Segments: []api.DisplaySegment{d, d}}, charOf)
			n := &imgNet{files: map[string][]byte{"poster": facePNG(160, 90, red)}}
			a, q := imgApp(t, n, m)
			a.reader = videoTestReader{prepare: func(_ context.Context, got api.MediaRef) (string, error) {
				if !reflect.DeepEqual(got, *ref) {
					return "", errors.New("视频凭据不对")
				}
				return "compiled-sample.mp4", nil
			}}
			tt := testerOf(width, 700, func(c *ui.Context) { a.chatView(c) })
			before, ok := tt.Find("播放视频")
			if !ok || !approx(before.W/before.H, 16.0/9, 0.001) || before.W < 24 || before.H < 24 || before.X < 0 || before.X+before.W > float32(width) || !tt.HasText("0:13") {
				t.Fatalf("视频气泡：%+v texts=%v", before, tt.Texts())
			}
			videoCount := 0
			for _, label := range tt.Texts() {
				if label == "播放视频" {
					videoCount++
				}
			}
			if videoCount != 2 {
				t.Fatalf("同一条消息的两个视频都应可见，得到 %d", videoCount)
			}
			settleImgs(a, q, tt)
			after, _ := tt.Find("播放视频")
			if after != before {
				t.Fatalf("封面到达改变尺寸：%+v -> %+v", before, after)
			}
			frame := tt.Image()
			pixel := frame.RGBAAt(int(after.X+after.W/4), int(after.Y+after.H/2))
			if withCover && !near(pixel, opaqueRed, 40) {
				t.Fatalf("封面未画：%v", pixel)
			}
			if err := tt.Click("播放视频"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			select {
			case path := <-opened:
				if path != "compiled-sample.mp4" {
					t.Fatal(path)
				}
			case <-time.After(uiWaitLimit):
				t.Fatal("点视频没有调用系统打开函数")
			}
			t.Logf("窗口=%d cover=%v，气泡 %.3f×%.3f，封面到达跳变 0，点击交给替换的系统打开函数", width, withCover, after.W, after.H)
		}
	}
	// 缺少下载凭据或后台能力时只读状态，未下载不提供播放入口。
	a, _ := imgApp(t, &imgNet{})
	tt := testerOf(480, 560, func(c *ui.Context) {
		a.videoBubble(c, &msg{}, segFromDisplay(api.DisplaySegment{FetchState: store.MediaTooBig, Size: 60 << 20}), 0)
	})
	if !tt.HasText("视频较大（60.0 MB），尚未下载") {
		t.Fatal(tt.Texts())
	}
	if _, ok := tt.Find("播放视频"); ok {
		t.Fatal("未下载不能播放")
	}
}

func TestVideoNoteFallsBackToMediaState(t *testing.T) {
	t.Parallel()
	s := segFromDisplay(api.DisplaySegment{MediaState: store.MediaTooBig, Size: 60 << 20})
	if got := videoNote(s); got != "视频较大（60.0 MB），尚未下载" {
		t.Fatalf("媒体状态回退：%q", got)
	}
	s.FetchState = store.MediaBlocked
	if got := videoNote(s); got != "视频地址不在允许的主机里，尚未下载" {
		t.Fatalf("下载状态应优先：%q", got)
	}
}

func TestVideoUnknownDurationHasPlayControl(t *testing.T) {
	ref := &api.MediaRef{Hash: "invented-video"}
	a, _ := imgApp(t, &imgNet{})
	var playSize float32
	tt := testerOf(480, 400, func(c *ui.Context) {
		playSize = theme.T(c).Metrics().Media().ActionSize()
		a.videoBubble(c, &msg{id: "invented-message"}, segFromDisplay(api.DisplaySegment{Media: ref}), 0)
	})
	r, ok := tt.Find("播放视频")
	if !ok || tt.HasText("0:00") {
		t.Fatalf("未知时长没有播放入口或伪造时长：%v", tt.Texts())
	}
	// 播放标记可以是描边或实心；无封面时中央区域须有图形对比，不能是空白点击区。
	frame := tt.Image()
	x, y := int(r.X+r.W/2), int(r.Y+r.H/2)
	low, high := uint8(255), uint8(0)
	for dy := -int(playSize / 3); dy <= int(playSize/3); dy++ {
		for dx := -int(playSize / 3); dx <= int(playSize/3); dx++ {
			pixel := frame.RGBAAt(x+dx, y+dy)
			low, high = min(low, pixel.R, pixel.G, pixel.B), max(high, pixel.R, pixel.G, pixel.B)
		}
	}
	if int(high)-int(low) <= 30 {
		t.Fatalf("未知时长没有可见播放图标：min=%d max=%d", low, high)
	}
	t.Logf("未知时长视频=%.1f×%.1f 播放标记范围=%d..%d", r.W, r.H, low, high)
}

func TestForwardVideoWindow(t *testing.T) {
	key := demoKey("qq:group:90002")
	source := api.EventRef{Conversation: key, Event: api.EventKey{Platform: "qq", EventID: "forward-source"}}
	ref := &api.MediaRef{Source: source, Hash: "video", Forward: []string{"outer", "inner"}}
	cover := &api.MediaRef{Source: source, Hash: "poster", Forward: ref.Forward}
	req := api.ForwardRequest{Source: source, ID: "inner", Via: []string{"outer"}}
	for _, width := range []int{480, 900, 2048} {
		nodes := []api.ForwardNode{
			{Name: "编的甲", Segments: []api.DisplaySegment{{Kind: "video", Media: ref, Cover: cover, DurationMS: 12500, Width: 160, Height: 90}}},
			{Name: "编的乙", Segments: []api.DisplaySegment{{Kind: "video"}}},
			{Name: "编的丙", Segments: []api.DisplaySegment{{Kind: "image", Media: &api.MediaRef{Source: source, Hash: "poster", Forward: ref.Forward}, Width: 160, Height: 90}}},
		}
		layer := &forwardLayer{request: req, view: api.ForwardView{ID: "inner", State: "ready", Nodes: nodes}}
		for i, n := range nodes {
			layer.msgs = append(layer.msgs, forwardNodeMsg(req, n, i))
		}
		layer.rows = forwardRows(layer.msgs)
		a, q := imgApp(t, &imgNet{files: map[string][]byte{"poster": facePNG(160, 90, red)}})
		a.forward.open, a.forward.stack = true, []*forwardLayer{layer}
		prepared := make(chan api.MediaRef, 1)
		a.reader = videoTestReader{prepare: func(_ context.Context, got api.MediaRef) (string, error) {
			prepared <- got
			return "", errors.New("编的文件不可用")
		}}
		tt := testerOf(width, 800, func(c *ui.Context) { a.forwardWindow(c) })
		before, ok := tt.Find("播放视频")
		if !ok || !tt.HasText("0:13") || !tt.HasText("视频还没下载") {
			t.Fatalf("视频节点缺失：%v", tt.Texts())
		}
		settleImgs(a, q, tt)
		after, _ := tt.Find("播放视频")
		if before != after || after.W < 24 || after.H < 24 || after.X < 0 || after.X+after.W > float32(width) {
			t.Fatalf("视频几何：%+v -> %+v", before, after)
		}
		pixel := tt.Image().RGBAAt(int(after.X+after.W/4), int(after.Y+after.H/2))
		if !near(pixel, opaqueRed, 40) {
			t.Fatalf("转发封面未画：%v", pixel)
		}
		posts := make(chan func(), 1)
		a.postFn = func(fn func()) { posts <- fn }
		click(t, tt, "播放视频")
		select {
		case got := <-prepared:
			if !reflect.DeepEqual(got, *ref) {
				t.Fatalf("播放丢失转发路径：%+v", got)
			}
		case <-time.After(uiWaitLimit):
			t.Fatal("未准备转发视频")
		}
		select {
		case fn := <-posts:
			fn()
		case <-time.After(uiWaitLimit):
			t.Fatal("播放失败未返回")
		}
		tt.Frame()
		if !tt.HasText("视频打不开，点一下再试") {
			t.Fatal(tt.Texts())
		}
		t.Logf("窗口=%d 视频=%.1fx%.1f 封面到达跳变=0，未下载占位、时长、完整路径点击、失败原因均可见", width, after.W, after.H)
	}
}

func TestVideoRootInitialGeometry(t *testing.T) {
	a := panesApp()
	a.firstDone, a.olderOff = true, true
	s := segFromDisplay(api.DisplaySegment{Kind: "video", Media: &api.MediaRef{Hash: "compiled-video"}, Width: 320, Height: 180})
	m := &msg{id: "initial-video", actor: "compiled-peer", who: "编造发送者", at: t0, segs: []seg{s}}
	m.blocks = segview.BlocksOf(segmentViews(m.segs))
	a.msgs = []*msg{m}
	tt := ui.NewTester(a.view, 2048, 1030)
	tt.Frame()
	r, ok := tt.Find("播放视频")
	if !ok || r.W < 24 || r.H < 24 || !approx(r.W/r.H, 16.0/9, .001) || r.X < 0 || r.X+r.W > 2048 {
		t.Fatalf("根视图首帧视频几何：%+v found=%v", r, ok)
	}
}
