package main

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// gifApp 造一个带两帧 GIF 的消息，列表缩略图和看大图用的缓存记录都放好。
func gifApp(t *testing.T) (*app, *ui.Tester) {
	t.Helper()
	s := seg{DisplaySegment: api.DisplaySegment{Kind: "image", Animated: true, Width: 120, Height: 80,
		Media: &api.MediaRef{Hash: "gif1", Source: api.EventRef{Event: api.EventKey{Platform: "qq"}}}}}
	m := mediaMsg("g", s)
	a := &app{sel: "c", msgs: []*msg{m}, gen: 1, srcGen: 1}
	a.list = a.newList()
	st := a.is()
	st.scale = 1
	mk := func(w, h int) *animation {
		f1, f2 := image.NewRGBA(image.Rect(0, 0, w, h)), image.NewRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < len(f1.Pix); i += 4 {
			f1.Pix[i], f1.Pix[i+3] = 255, 255
			f2.Pix[i+2], f2.Pix[i+3] = 255, 255
		}
		return newAnimation([]*image.RGBA{f1, f2}, []int{50, 50}, 0)
	}
	for _, w := range []float32{240, 480} {
		an := mk(120, 80)
		st.m[keyOf(m.segs[0], w, false, 1)] = &imgEntry{state: imgOK, bmp: ui.NewBitmap(an.frames[0]), anim: an}
	}
	tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchChat(c); a.chatImageWindow(c) }, 900, 600)
	tt.Frame()
	tt.Frame()
	return a, tt
}

func play(tt *ui.Tester, n int) {
	for i := 0; i < n; i++ {
		tt.Advance(60 * time.Millisecond)
		tt.Frame()
	}
}

// 点开 GIF 大图以后一直开着：动图播放、消息行被重建（新对象、版本变了）、新消息进来，都不关；
// 点图本身不关，点背景才关，Esc 也关。
func TestGIFViewerStaysOpen(t *testing.T) {
	a, tt := gifApp(t)
	if err := tt.Click("查看图片"); err != nil {
		t.Fatal(err)
	}
	play(tt, 3)
	if !a.chatLightbox.open {
		t.Fatal("点开以后没有打开看大图")
	}
	tt.Advance(600 * time.Millisecond) // 过了刚打开的保护期
	play(tt, 40)
	if !a.chatLightbox.open {
		t.Fatal("GIF 播放几十帧后查看器关了")
	}
	// 消息行被重建：同一个消息 ID，新对象、版本号变了
	old := a.msgs[0]
	fresh := *old
	fresh.version++
	a.msgs[0] = &fresh
	play(tt, 5)
	if !a.chatLightbox.open {
		t.Fatal("消息行被重建（版本变了、对象换了）后查看器关了")
	}
	// 新消息进来
	a.msgs = append(a.msgs, &msg{id: "n", text: "新消息", version: 1, at: time.Now().UnixMilli()})
	play(tt, 5)
	if !a.chatLightbox.open {
		t.Fatal("新消息进来后查看器关了")
	}
	// 点图本身不关
	r, ok := tt.Find("图片·大图")
	if !ok {
		t.Fatalf("没找到大图：%v", tt.Texts())
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	play(tt, 3)
	if !a.chatLightbox.open {
		t.Fatal("点图片本身把查看器关了")
	}
	// 点背景才关
	tt.ClickAt(3, 300)
	play(tt, 3)
	if a.chatLightbox.open {
		t.Fatal("点背景没有关")
	}
	// 重开，Esc 才关
	a.chatLightbox.open = true
	play(tt, 3)
	if !a.chatLightbox.open {
		t.Fatal("重开失败")
	}
	tt.Key(0, ui.KeyEscape)
	play(tt, 3)
	if a.chatLightbox.open {
		t.Fatal("Esc 没有关")
	}
}

// 换会话、换数据源才该关。
func TestGIFViewerClosesOnSessionChange(t *testing.T) {
	a, tt := gifApp(t)
	if err := tt.Click("查看图片"); err != nil {
		t.Fatal(err)
	}
	play(tt, 3)
	a.gen++
	play(tt, 3)
	if a.chatLightbox.open {
		t.Fatal("换了会话还开着")
	}
}
