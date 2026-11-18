package chatui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 设了 SUMIKA_SHOT_DIR 才截图（给主人看的组件区域，像素比 1.25）；平时跳过。
func TestWhisperPolishShots(t *testing.T) {
	dir := os.Getenv("SUMIKA_SHOT_DIR")
	if dir == "" {
		t.Skip("没设 SUMIKA_SHOT_DIR")
	}
	save := func(tt *ui.Tester, name string, r image.Rectangle) {
		img := tt.Image()
		sub := img.SubImage(r.Intersect(img.Bounds()))
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, sub); err != nil {
			t.Fatal(err)
		}
	}
	const sc = 1.25
	px := func(x, y, w, h float32) image.Rectangle {
		return image.Rect(int(x*sc), int(y*sc), int((x+w)*sc)+1, int((y+h)*sc)+1)
	}

	// 一、引用卡：卡头时间，对象列表连勾后仍开着
	r := newComposerRig(t)
	r.tester.SetScale(sc)
	r.p.Whisper = true
	r.host.Targets = threeTargets
	b := r.p.AddWhisper(Quoted{MessageID: "m1", Name: "小B", Text: "第一句在这里，很长很长的一句话。", At: time.Date(2026, 11, 9, 21, 5, 0, 0, time.Local)}, []string{"a"}, false, false)
	r.tester.Frame()
	r.tester.Frame()
	card, _ := r.tester.Find("引用卡")
	save(r.tester, "card-head-time.png", px(card.X, card.Y, card.W, 120))
	_ = r.tester.Click("引用 1 的对象：小A")
	r.tester.Frame()
	_ = r.tester.Click("小B")
	r.tester.Frame()
	r.tester.Frame()
	_ = r.tester.Click("小C")
	r.tester.Frame()
	r.tester.Frame()
	if len(b.To) != 3 {
		t.Fatalf("对象 %v", b.To)
	}
	save(r.tester, "target-list-stays-open.png", px(card.X, card.Y-170, card.W, 330))

	// 二、消息：Tab 取到焦点，Shift+F10 开菜单
	w := newWhisperRig(t, false, threeTargets)
	w.tt.SetScale(sc)
	w.tt.Key(0, ui.KeyTab)
	w.tt.Frame()
	w.tt.Key(ui.Shift, ui.KeyF10)
	w.tt.Frame()
	w.tt.Frame()
	save(w.tt, "message-focus-menu.png", px(0, 60, 420, 260))
}
