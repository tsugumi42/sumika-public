package chatui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

const posText = "这是一段足够长的话，用来在气泡里折成好几行：第一行在这里，第二行在那里，第三行收个尾，再多写几个字好让它一定超过两行的宽度，继续继续，再来一些字，凑够三行为止吧。"

type posRig struct {
	bar SelectionBar
	tt  *ui.Tester
	lh  float32
}

func newPosRig(t *testing.T, md bool, top float32) *posRig {
	t.Helper()
	r := &posRig{}
	tokens := Derive(DefaultSettings())
	r.lh = tokens.BodyLine
	host := Host{T: tokens, Column: 600, NoEnter: true, Selection: &r.bar, MD: NewMD(),
		OnReply:  func(Selected, bool) {},
		ViewRect: func() ui.Rect { return ui.Rect{X: 0, Y: 0, W: 720, H: 480} }}
	m := &Msg{ID: "m1", Sender: "她", Name: "她", Text: posText, Markdown: md, Blocks: segview.TextBlocks(posText), At: time.UnixMilli(1000)}
	r.tt = ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Size(720, 480).Padding(top, 24, 24, 24).Children(func() { host.Message(c, m, nil, nil) })
	}, 720, 480)
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	r.tt.Frame()
	r.tt.Frame()
	return r
}

// drag 在正文第 line1 行的 x1 处按下，拖到第 line2 行的 x2 处松开（x 是占正文宽度的比例，行从 0 数）。
// 返回选中那几行的"字所在的带"：鼠标在行里，所以带取 [最小 y − 半行 + 1, 最大 y + 半行 − 1]。
func (r *posRig) drag(t *testing.T, line1 int, x1 float32, line2 int, x2 float32) ui.Rect {
	t.Helper()
	rect, ok := findText(r.tt, "这是一段足够长的话")
	if !ok {
		t.Fatalf("找不到正文：%v", r.tt.Texts())
	}
	ya := rect.Y + (float32(line1)+0.5)*r.lh
	yb := rect.Y + (float32(line2)+0.5)*r.lh
	xa, xb := rect.X+rect.W*x1, rect.X+rect.W*x2
	r.tt.Press(xa, ya)
	r.tt.Move((xa+xb)/2, (ya+yb)/2)
	r.tt.Move(xb, yb)
	r.tt.Release(xb, yb)
	r.tt.Frame()
	r.tt.Advance(selectionSettle + 10*time.Millisecond)
	r.tt.Frame()
	r.tt.Frame()
	y0, y1 := min(ya, yb)-r.lh/2+1, max(ya, yb)+r.lh/2-1
	x0, x1p := min(xa, xb), max(xa, xb)
	if line1 != line2 {
		x0, x1p = rect.X, rect.X+rect.W
	}
	return ui.Rect{X: x0, Y: y0, W: x1p - x0, H: y1 - y0}
}

func intersects(a, b ui.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func (r *posRig) checkBar(t *testing.T, name string, band ui.Rect, wantAbove bool) ui.Rect {
	t.Helper()
	bar, ok := r.tt.Find("选区操作")
	if !ok {
		t.Fatalf("%s：没有选区条：%v", name, r.tt.Texts())
	}
	if intersects(bar, band) {
		t.Fatalf("%s：条 %+v 压住了选中的文字 %+v", name, bar, band)
	}
	if bar.X < 0 || bar.Y < 0 || bar.X+bar.W > 720 || bar.Y+bar.H > 480 {
		t.Fatalf("%s：条 %+v 超出可见范围", name, bar)
	}
	if above := bar.Y+bar.H <= band.Y; above != wantAbove {
		t.Fatalf("%s：条应在选区%s，实际 %+v 对 %+v", name, map[bool]string{true: "上方", false: "下方"}[wantAbove], bar, band)
	}
	return bar
}

// 不管从哪个方向拖、跨几行，条都不能压住选中的文字；上方放不下放到下方。纯文字和 Markdown 两条路径都测。
func TestSelectionBarNeverCoversSelectedText(t *testing.T) {
	for _, md := range []bool{false, true} {
		kind := map[bool]string{false: "纯文字", true: "Markdown"}[md]
		t.Run(kind, func(t *testing.T) {
			cases := []struct {
				name      string
				top       float32
				l1        int
				x1        float32
				l2        int
				x2        float32
				wantAbove bool
			}{
				{"从左往右", 150, 0, 0.2, 0, 0.5, true},
				{"从右往左", 150, 0, 0.6, 0, 0.2, true},
				{"跨多行", 150, 0, 0.4, 2, 0.5, true},
				{"跨多行从下往上", 150, 2, 0.5, 0, 0.4, true},
				{"选区贴近顶部", 4, 0, 0.6, 0, 0.2, false},
			}
			for _, tc := range cases {
				r := newPosRig(t, md, tc.top)
				band := r.drag(t, tc.l1, tc.x1, tc.l2, tc.x2)
				r.checkBar(t, tc.name, band, tc.wantAbove)
			}
		})
	}
}

func TestShotSelectionBarRightToLeft(t *testing.T) {
	r := newPosRig(t, false, 150)
	r.tt.SetScale(1.25)
	r.tt.Frame()
	r.drag(t, 0, 0.6, 0, 0.2)
	newuiShot(t, r.tt, "5-selection-bar-right-to-left.png")
}
