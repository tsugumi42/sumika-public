package chatui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 2026-11-15 气泡两边的立体感统一（主人："对方的没有边框，我们的有边框，一边突出，另一边突出还多一圈框"）：
// 对方的、自己的、过程气泡用同一组投影，都不描边；自己的只靠底色和对方分开。有壁纸、没壁纸、浅色、深色都一样。
func TestBubbleSidesLiftTheSame(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, wall := range []bool{true, false} {
			s := DefaultSettings()
			s.Dark, s.Wallpaper = dark, wall
			tk := Derive(s)
			bg := tk.C.ChatBg
			host := Host{T: tk, SelfChar: CharColors[0], Column: 600, NoEnter: true}
			msgs := map[string]*Msg{
				"other": {ID: "other", Sender: "r1", Char: CharColors[0], Text: "好呀"},
				"self":  {ID: "self", Sender: "self", Self: true, Text: "好呀"},
				"proc":  procMsg("proc", 0, &Step{ID: "r", Kind: StepRead, State: StepDone, Target: "a.md"}),
			}
			below := map[string][3]uint8{}
			for id, m := range msgs {
				view := func(c *ui.Context) {
					c.Root().Background(bg)
					ui.Column(c.Key("list")).Padding(20).Children(func() { host.Message(c, m, nil, nil) })
				}
				tt := ui.NewTester(view, 700, 200)
				tt.Frame()
				tt.Frame()
				b := tree(tt).path(id, "bubble")
				img := tt.Image()
				p := img.RGBAAt(int(b.Box.X+b.Box.W/2), int(b.Box.Y+b.Box.H)+1)
				below[id] = [3]uint8{p.R, p.G, p.B}
				darker := p.R < bg.R || p.G < bg.G || p.B < bg.B
				if darker != wall {
					t.Errorf("深=%v 壁纸=%v %s 气泡下沿投影有无 = %v，应 = %v", dark, wall, id, darker, wall)
				}
				// 不描边：最左一列像素一像素就是气泡底色，没有一圈线（有壁纸的深浅和浅色的无壁纸下，边线色和底色是分得开的）
				if id == "self" {
					want := tk.AccentOf(CharColors[0]).Soft
					e := img.RGBAAt(int(b.Box.X), int(b.Box.Y+b.Box.H/2))
					if d := int(e.R) - int(want.R); d > 2 || d < -2 || int(e.G)-int(want.G) > 2 || int(want.G)-int(e.G) > 2 || int(e.B)-int(want.B) > 2 || int(want.B)-int(e.B) > 2 {
						t.Errorf("深=%v 壁纸=%v 自己的气泡最左一列像素 %v，应是底色 %v（不描边）", dark, wall, e, want)
					}
				}
			}
			if below["other"] != below["self"] || below["other"] != below["proc"] {
				t.Errorf("深=%v 壁纸=%v 三种气泡下沿的投影不一样：%v", dark, wall, below)
			}
		}
	}
}

// 宿主给了语义色（Colors 不空）时两边也一样：浅色无壁纸时都浮起来，深色无壁纸时都不画阴影；自己的同样不描边。
func TestBubbleSidesLiftTheSameWithHostColors(t *testing.T) {
	for _, dark := range []bool{false, true} {
		s := DefaultSettings()
		s.Dark, s.Wallpaper = dark, false
		tk := Derive(s)
		bg := tk.C.ChatBg
		in, out := ui.RGB(240, 240, 240), ui.RGB(200, 230, 255)
		host := Host{T: tk, SelfChar: CharColors[0], Column: 600, NoEnter: true, Colors: &MessageColors{
			InBackground: in, InText: tk.C.Text, InTime: tk.C.Text3, InLink: tk.C.Text, OutBackground: out, OutText: tk.C.Text, OutTime: tk.C.Text3, OutLink: tk.C.Text}}
		below := map[string][3]uint8{}
		for id, m := range map[string]*Msg{
			"other": {ID: "other", Sender: "r1", Char: CharColors[0], Text: "好呀"},
			"self":  {ID: "self", Sender: "self", Self: true, Text: "好呀"},
		} {
			tt := ui.NewTester(func(c *ui.Context) {
				c.Root().Background(bg)
				ui.Column(c.Key("list")).Padding(20).Children(func() { host.Message(c, m, nil, nil) })
			}, 700, 200)
			tt.Frame()
			tt.Frame()
			b := tree(tt).path(id, "bubble")
			img := tt.Image()
			p := img.RGBAAt(int(b.Box.X+b.Box.W/2), int(b.Box.Y+b.Box.H)+1)
			below[id] = [3]uint8{p.R, p.G, p.B}
			if darker := p.R < bg.R || p.G < bg.G || p.B < bg.B; darker == dark {
				t.Errorf("深=%v %s 气泡下沿投影有无 = %v，应 = %v", dark, id, darker, !dark)
			}
			want := in
			if id == "self" {
				want = out
			}
			if e := img.RGBAAt(int(b.Box.X), int(b.Box.Y+b.Box.H/2)); e.R != want.R || e.G != want.G || e.B != want.B {
				t.Errorf("深=%v %s 最左一列像素 %v，应是底色 %v（不描边）", dark, id, e, want)
			}
		}
		if below["other"] != below["self"] {
			t.Errorf("深=%v 两边下沿投影不一样：%v", dark, below)
		}
	}
}
