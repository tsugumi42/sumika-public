package chatui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

type stickyRig struct {
	st    ui.ListState
	sa    StickyAvatars
	msgs  []*Msg
	tt    *ui.Tester
	tok   Tokens
	viewH float32
}

func tallText(n int) string {
	var parts []string
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("第 %d 段：这是一段用来撑高消息的话。", i+1))
	}
	return strings.Join(parts, "\n\n")
}

// n 条同一个人连续发的消息，每条 paras 段文字；再跟一条别人的短消息收尾（让组底不是列表的底）。
func newStickyRig(t *testing.T, n, paras int) *stickyRig {
	t.Helper()
	r := &stickyRig{viewH: 600, st: ui.ListState{}}
	r.tok = Derive(DefaultSettings())
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	for i := 0; i < n; i++ {
		text := tallText(paras)
		r.msgs = append(r.msgs, &Msg{ID: fmt.Sprintf("a%d", i), Sender: "甲", Name: "甲", Text: text, Blocks: segview.TextBlocks(text), At: at.Add(time.Duration(i) * time.Second)})
	}
	r.msgs = append(r.msgs, &Msg{ID: "z", Sender: "乙", Name: "乙", Text: "后面别人说的一句", Blocks: segview.TextBlocks("后面别人说的一句"), At: at.Add(time.Hour)})
	host := Host{T: r.tok, Group: true, Column: 560, NoEnter: true, Sticky: &r.sa}
	r.st.Key = func(i int) any { return r.msgs[i].ID }
	r.tt = ui.NewTester(func(c *ui.Context) {
		ui.Box(c.Key("stack")).Size(640, r.viewH).Children(func() {
			ui.List(c.Key("list"), &r.st, len(r.msgs), func(i int) {
				var prev, next *Msg
				if i > 0 {
					prev = r.msgs[i-1]
				}
				if i+1 < len(r.msgs) {
					next = r.msgs[i+1]
				}
				host.Message(c, r.msgs[i], prev, next)
			}).Fill().Padding(16, 24)
			r.sa.Overlay(c)
		})
	}, 640, int(r.viewH))
	r.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	r.tt.Frame()
	r.tt.Frame()
	return r
}

func (r *stickyRig) frames() { r.tt.Frame(); r.tt.Frame(); r.tt.Frame() }

// 头像的 y 在可见区域内，并且在组的上下界内。
func (r *stickyRig) check(t *testing.T, name string) AvatarPlacement {
	t.Helper()
	var got *AvatarPlacement
	for i, p := range r.sa.Placed() {
		if p.MessageID[0] == 'a' {
			got = &r.sa.Placed()[i]
		}
	}
	if got == nil {
		t.Fatalf("%s：甲这一组没有头像：%+v", name, r.sa.Placed())
	}
	if got.Y < 0 || got.Y+got.Size > r.viewH {
		t.Fatalf("%s：头像 y=%.1f 不在可见区域 0..%.0f 内", name, got.Y, r.viewH)
	}
	if got.Y < got.GroupTop || got.Y+got.Size > got.GroupBottom+0.5 {
		t.Fatalf("%s：头像 y=%.1f 越出组 [%.1f, %.1f]", name, got.Y, got.GroupTop, got.GroupBottom)
	}
	t.Logf("%s：头像 y=%.1f 组 [%.1f, %.1f]", name, got.Y, got.GroupTop, got.GroupBottom)
	return *got
}

// 一组 6 条高消息（总高约 3 倍窗口）：滚到上段、中段、下段，头像都在可见区域里、在组的范围内，并且钉在可见区域底边。
func TestStickyAvatarLongGroup(t *testing.T) {
	r := newStickyRig(t, 6, 7)
	for _, c := range []struct {
		name   string
		to     int
		pinned bool
	}{{"上段", 0, true}, {"中段", 2, true}, {"中段偏下", 4, true}} {
		r.st.ScrollTo(c.to, ui.Start)
		r.frames()
		p := r.check(t, c.name)
		if c.pinned {
			want := r.viewH - r.tok.S2 - p.Size
			if p.Y < want-1 && p.Y+p.Size < p.GroupBottom-1 {
				t.Fatalf("%s：头像 y=%.1f，应钉在可见区域底边 %.1f", c.name, p.Y, want)
			}
		}
	}
	r.st.ScrollToEnd()
	r.frames()
	r.check(t, "滚到底")
}

// 扫一遍各行、各种对齐的滚动位置：头像永远在可见区域里、在组的范围内。
// 头像的位置是这一帧画行时登记的框算出来的（见 sticky.go），不读上一帧，所以滚动时不会差一帧。
func TestStickyAvatarSweep(t *testing.T) {
	r := newStickyRig(t, 6, 7)
	for row := 0; row < 6; row++ {
		for _, al := range []ui.Align{ui.Start, ui.Center, ui.End} {
			r.st.ScrollTo(row, al)
			r.frames()
			r.check(t, fmt.Sprintf("行%d 对齐%v", row, al))
		}
	}
}

// 整组都在可见区域里（组很短）：头像在组的最后一条旁边，和原来一样对齐组底。
func TestStickyAvatarShortGroupAtBottomOfGroup(t *testing.T) {
	r := newStickyRig(t, 2, 1)
	r.frames()
	p := r.check(t, "短组")
	if abs32(p.Y+p.Size-p.GroupBottom) > 1 {
		t.Fatalf("短组头像应对齐组底：y+size=%.1f 组底=%.1f", p.Y+p.Size, p.GroupBottom)
	}
}

func TestShotStickyAvatarMiddle(t *testing.T) {
	r := newStickyRig(t, 6, 7)
	r.tt.SetScale(1.25)
	r.st.ScrollTo(2, ui.Center)
	r.frames()
	newuiShotRect(t, r.tt, "10-sticky-avatar-middle.png", ui.Rect{X: 0, Y: 0, W: 640, H: 600})
}

// 底部有提示栏（栏在消息列下面）或玻璃输入框（叠在消息列上面）：滚过每个位置，
// 头像的可见部分都在视口里、不越过栏的上沿，被视口底边裁掉的消息，头像一起被裁。
func TestStickyAvatarClippedByViewport(t *testing.T) {
	const barH, viewH = 44, 360
	barColor := ui.Color{R: 255, G: 0, B: 255, A: 255}
	for _, glass := range []bool{false, true} {
		sa := &StickyAvatars{}
		tok := Derive(DefaultSettings())
		at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
		var msgs []*Msg
		for i := 0; i < 12; i++ { // 两人轮流说话：每条自成一组，每组都有头像
			who := []string{"甲", "乙"}[i%2]
			txt := fmt.Sprintf("第 %d 句：一句不长不短的话。", i)
			msgs = append(msgs, &Msg{ID: fmt.Sprintf("m%d", i), Sender: who, Name: who, Text: txt, Blocks: segview.TextBlocks(txt), At: at.Add(time.Duration(i) * time.Hour)})
		}
		host := Host{T: tok, Group: true, Column: 560, NoEnter: true, Sticky: sa}
		var st ui.ListState
		st.Key = func(i int) any { return msgs[i].ID }
		var stack ui.Element
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c.Key("root")).Size(640, viewH+barH).Children(func() {
				ui.Box(c.Key("stack")).Grow(1).MinHeight(0).Children(func() {
					ui.List(c.Key("list"), &st, len(msgs), func(i int) {
						var prev, next *Msg
						if i > 0 {
							prev = msgs[i-1]
						}
						if i+1 < len(msgs) {
							next = msgs[i+1]
						}
						host.Message(c, msgs[i], prev, next)
					}).Fill().Padding(16, 24).Label("列表")
					sa.Overlay(c)
					if glass {
						ui.Box(c.Key("glass")).Label("输入框").Absolute().Left(0).Right(0).Bottom(0).Height(barH).Background(barColor)
					}
				})
				if !glass {
					ui.Box(c.Key("bar")).Label("提示栏").FillWidth().Height(barH).Shrink(0).Background(barColor)
				}
			})
			_ = stack
		}, 640, viewH+barH)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		if glass {
			sa.Visible = func(v ui.Rect) ui.Rect { v.H -= barH; return v }
		}
		tt.Frame()
		tt.Frame()
		list, ok := tt.Find("列表")
		if !ok {
			t.Fatal("没找到列表")
		}
		limit := float32(viewH)
		if !glass {
			limit = list.Y + list.H
		}
		for step := 0; step < 40; step++ {
			tt.Wheel(300, 100, 0, 9)
			tt.Frame()
			tt.Frame()
			img := tt.Image()
			for _, pl := range sa.Placed() {
				vis := ui.Rect{X: pl.X, Y: pl.Y, W: pl.Size, H: pl.Size}
				if vis.Y+vis.H > limit {
					vis.H = limit - vis.Y // 超出的被裁掉
				}
				if vis.H > 0 && vis.Y+vis.H > limit+0.5 {
					t.Fatalf("glass=%v 步%d：头像可见部分越过下界 %.1f", glass, step, limit)
				}
			}
			// 栏上的像素必须是栏色：头像没有画进去
			for x := 20; x < 620; x += 3 {
				for _, y := range []int{int(limit) + 3, int(limit) + barH/2, int(limit) + barH - 3} {
					px := img.RGBAAt(x, y)
					if px.R != 255 || px.G != 0 || px.B != 255 {
						t.Fatalf("glass=%v 步%d：栏上 (%d,%d) 的像素 %v 不是栏色，头像画进了栏里", glass, step, x, y, px)
					}
				}
			}
		}
	}
}
