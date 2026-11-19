package chatui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 2026-11-15 气泡玻璃：宿主给了 BubbleGlass，对方的、自己的、过程气泡各调用一次并带上实色时的底；
// 撤回的、宿主给了语义色的不调用（仍是实色）；不给就一次也不调用，气泡还是实色。
func TestBubbleGlassCallback(t *testing.T) {
	tk := Derive(DefaultSettings())
	acc := tk.AccentOf(CharColors[0])
	proc := procMsg("proc", 0, &Step{ID: "r", Kind: StepRead, State: StepDone, Target: "a.md"})
	cases := []struct {
		name   string
		msg    *Msg
		colors *MessageColors
		glass  bool
		bg     ui.Color
	}{
		{"对方的", &Msg{ID: "o", Sender: "r1", Char: CharColors[0], Text: "好呀"}, nil, true, tk.C.Bubble},
		{"自己的", &Msg{ID: "s", Sender: "self", Self: true, Text: "好呀"}, nil, true, acc.Soft},
		{"过程气泡", proc, nil, true, tk.C.Bubble},
		{"撤回的", &Msg{ID: "r", Sender: "r1", Char: CharColors[0], Text: "好呀", State: Recalled}, nil, false, ui.Color{}},
		{"宿主给了语义色", &Msg{ID: "c", Sender: "r1", Char: CharColors[0], Text: "好呀"}, &MessageColors{InBackground: tk.C.Bubble, InText: tk.C.Text}, false, ui.Color{}},
	}
	for _, c := range cases {
		for _, set := range []bool{true, false} {
			var got []ui.Color
			host := Host{T: tk, SelfChar: CharColors[0], Column: 600, NoEnter: true, Colors: c.colors}
			if set {
				host.BubbleGlass = func(e ui.Element, bg ui.Color, _ [4]float32) { got = append(got, bg); e.Background(bg.Alpha(.5)) }
			}
			tt := ui.NewTester(func(ctx *ui.Context) {
				ui.Column(ctx.Key("list")).Padding(20).Children(func() { host.Message(ctx, c.msg, nil, nil) })
			}, 700, 200)
			tt.Frame()
			tt.Frame()
			switch {
			case set && c.glass:
				// 每次构建界面调用一次，Tester 两帧里构建了好几次；每次的底色都该是实色时的底
				if len(got) == 0 {
					t.Errorf("%s：该调用 BubbleGlass 却没调用", c.name)
				}
				for _, bg := range got {
					if bg != c.bg {
						t.Errorf("%s：BubbleGlass 收到的底 %v，应是实色时的底 %v", c.name, bg, c.bg)
						break
					}
				}
			default:
				if len(got) != 0 {
					t.Errorf("%s（给了回调=%v）：不该调用 BubbleGlass，实际 %v", c.name, set, got)
				}
			}
		}
	}
}

// 玻璃气泡的上沿有一道高光、不描边：气泡最上面一行像素比中间亮（浅色）或更亮（深色），最左一列没有一圈线。
func TestBubbleGlassRim(t *testing.T) {
	for _, dark := range []bool{false, true} {
		s := DefaultSettings()
		s.Dark = dark
		tk := Derive(s)
		host := Host{T: tk, SelfChar: CharColors[0], Column: 600, NoEnter: true,
			BubbleGlass: func(e ui.Element, bg ui.Color, _ [4]float32) { e.Background(bg) }} // 不透明的底，只看高光
		m := &Msg{ID: "o", Sender: "r1", Char: CharColors[0], Text: "好呀好呀好呀"}
		tt := ui.NewTester(func(c *ui.Context) {
			c.Root().Background(tk.C.ChatBg)
			ui.Column(c.Key("list")).Padding(20).Children(func() { host.Message(c, m, nil, nil) })
		}, 700, 200)
		tt.Frame()
		tt.Frame()
		b := tree(tt).path("o", "bubble")
		img := tt.Image()
		x := int(b.Box.X + b.Box.W - 4) // 右边内距里，没有字
		top, mid := img.RGBAAt(x, int(b.Box.Y)), img.RGBAAt(x, int(b.Box.Y+b.Box.H/2))
		lum := func(c interface{ RGBA() (r, g, b, a uint32) }) int { r, g, b, _ := c.RGBA(); return int(r + g + b) }
		if lum(top) == lum(mid) {
			t.Errorf("深=%v 玻璃气泡上沿没有高光：上沿 %v，中间 %v", dark, top, mid)
		}
		left := img.RGBAAt(int(b.Box.X), int(b.Box.Y+b.Box.H/2))
		if d := int(left.R) - int(mid.R); d > 2 || d < -2 { // 最左一列和右边内距里的像素同色（容许抗锯齿差一两级）：没有描边
			t.Errorf("深=%v 玻璃气泡最左一列 %v 和中间 %v 不同：不该描边", dark, left, mid)
		}
	}
}
