package chatui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// 设 NEWUI_SHOT=目录 时把组件区域存成 PNG（像素比 1.25）；平时跳过。
func newuiShot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := os.Getenv("NEWUI_SHOT")
	if dir == "" {
		t.Skip("设 NEWUI_SHOT 才出图")
	}
	img := tt.Image()
	if img == nil {
		t.Fatal("没有图")
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestShotBubbleFit(t *testing.T) {
	if os.Getenv("NEWUI_SHOT") == "" {
		t.Skip("设 NEWUI_SHOT 才出图")
	}
	h := fitHost()
	h.Column = 560
	at := time.Date(2026, 11, 8, 11, 36, 0, 0, time.Local)
	mk := func(id string, self bool, text string) *Msg {
		return &Msg{ID: id, Self: self, Text: text, At: at, Blocks: segview.TextBlocks(text)}
	}
	msgs := []*Msg{
		mk("a", false, "哼，早什么早，都快十二点了哦。"),
		mk("b", true, "早"),
		mk("c", false, "算了，看在你记得跟我打招呼的份上，早上好，主人。中午吃了吗？还是又打算一边饿着一边使唤我干活，这可不行哦。"),
		mk("d", true, "中午好"),
		mk("e", true, "饿了么"),
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("root")).Size(600, 330).Padding(16).Background(h.T.C.ChatBg).Children(func() {
			for i, m := range msgs {
				var prev, next *Msg
				if i > 0 {
					prev = msgs[i-1]
				}
				if i+1 < len(msgs) {
					next = msgs[i+1]
				}
				h.Message(c, m, prev, next)
			}
		})
	}, 600, 330)
	tt.SetScale(1.25)
	tt.Frame()
	tt.Frame()
	newuiShot(t, tt, "2-6-bubble-fit.png")
}

// newuiShotRect 只存 r 这块区域（逻辑坐标，像素比 1.25）。
func newuiShotRect(t *testing.T, tt *ui.Tester, name string, r ui.Rect) {
	t.Helper()
	dir := os.Getenv("NEWUI_SHOT")
	if dir == "" {
		t.Skip("设 NEWUI_SHOT 才出图")
	}
	img := tt.Image()
	if img == nil {
		t.Fatal("没有图")
	}
	const sc = 1.25
	box := image.Rect(int(r.X*sc), int(r.Y*sc), int((r.X+r.W)*sc), int((r.Y+r.H)*sc)).Intersect(img.Bounds())
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img.SubImage(box)); err != nil {
		t.Fatal(err)
	}
}

func TestShotMessageMenu(t *testing.T) {
	if os.Getenv("NEWUI_SHOT") == "" {
		t.Skip("设 NEWUI_SHOT 才出图")
	}
	r := newMenuRig(t, false, true, false)
	r.tt.SetScale(1.25)
	r.tt.Frame()
	rect, _ := findText(r.tt, "第一句在这")
	r.tt.RightClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	r.tt.Frame()
	r.tt.Frame()
	newuiShotRect(t, r.tt, "7-menu-no-selection.png", ui.Rect{X: 0, Y: 60, W: 520, H: 200})
	r2 := newMenuRig(t, false, true, false)
	r2.tt.SetScale(1.25)
	r2.tt.Frame()
	x, y := r2.selectMiddle(t)
	r2.tt.RightClickAt(x, y)
	r2.tt.Frame()
	r2.tt.Frame()
	newuiShotRect(t, r2.tt, "7-menu-with-selection.png", ui.Rect{X: 0, Y: 60, W: 520, H: 200})
}
