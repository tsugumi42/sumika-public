package winshell

import (
	"fmt"
	"image"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/assets/fonts"
	"sumika-native/internal/chatui"
)

// 数字逐位滚动（规格-过渡动画 第五节铁律 7 的新规则）：变了的位在滚、没变的位不动，中间帧有新旧两个数字，终点正确；
// 减少动态效果时只交叉淡变不位移；步进器没在编辑时才滚，编辑时是输入框自己的字。

// colDiff 是 a、b 两张图在 r 这块里亮度差超过 24 的像素数。
func colDiff(a, b *image.RGBA, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			pa, pb := a.RGBAAt(x, y), b.RGBAAt(x, y)
			if dist(pa.R, pb.R) > 24 || dist(pa.G, pb.G) > 24 || dist(pa.B, pb.B) > 24 {
				n++
			}
		}
	}
	return n
}

// 步进器：2000 → 2001，前三位不动，最后一位在滚；终点和直接画 2001 逐像素一样；减少动态效果下不位移。
func TestStepperRollsChangedDigits(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		r := newStepperRig(2000, StepperSpec{Min: 1, Max: 9999, Step: 1})
		r.tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		r.tt.Frame()
		rt, ok := r.tt.Find("2000")
		if !ok {
			t.Fatalf("reduce=%v 找不到数字 2000：%v", reduce, r.tt.Texts())
		}
		field := image.Rect(int(rt.X), int(rt.Y), int(rt.X+rt.W), int(rt.Y+rt.H))
		ink := inkBox(r.tt.Image(), field)
		if ink.Dx() < 10 {
			t.Fatalf("reduce=%v 没量到数字的墨迹：%v", reduce, ink)
		}
		// 等宽数字：四位一样宽，前三位在左、最后一位在右，中间留一条缝
		dw := ink.Dx() / 4
		left := image.Rect(ink.Min.X-2, field.Min.Y, ink.Min.X+3*dw-2, field.Max.Y)
		right := image.Rect(ink.Min.X+3*dw+1, field.Min.Y, ink.Max.X+3, field.Max.Y)
		box := field
		start := cloneImg(r.tt.Image())
		if err := r.tt.Click("窗口 加"); err != nil {
			t.Fatal(err)
		}
		r.tt.Frame()
		r.tt.Advance(60 * time.Millisecond)
		r.tt.Frame()
		mid := cloneImg(r.tt.Image())
		if reduce {
			// 只交叉淡变、不位移：中间帧的墨迹不超出起点墨迹的上下范围
			if mi := inkBox(mid, field); mi.Min.Y < ink.Min.Y-1 || mi.Max.Y > ink.Max.Y+1 {
				t.Errorf("reduce=%v 数字在位移：起点墨迹纵向 %d～%d，中间帧 %d～%d", reduce, ink.Min.Y, ink.Max.Y, mi.Min.Y, mi.Max.Y)
			}
		} else {
			if n := colDiff(start, mid, left); n != 0 {
				t.Errorf("前三位没变，不该动，却有 %d 个像素不同", n)
			}
			if n := colDiff(start, mid, right); n < 5 {
				t.Errorf("最后一位应在滚，右边只有 %d 个像素变了", n)
			}
		}
		settle(r.tt)
		end := cloneImg(r.tt.Image())
		fresh := newStepperRig(2001, StepperSpec{Min: 1, Max: 9999, Step: 1})
		fresh.tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		fresh.tt.Frame()
		if n := colDiff(end, fresh.tt.Image(), box); n != 0 {
			t.Errorf("reduce=%v 终点和直接画 2001 有 %d 个像素不同", reduce, n)
		}
		if r.tt.FramePending() {
			t.Errorf("reduce=%v 滚完还在要帧（空转）", reduce)
		}
		if !reduce {
			// 变大从下面升上来、变小从上面落下来：同一时刻的中间帧不同
			dn := newStepperRig(2001, StepperSpec{Min: 1, Max: 9999, Step: 1})
			dn.tt.Frame()
			if err := dn.tt.Click("窗口 减"); err != nil {
				t.Fatal(err)
			}
			dn.tt.Frame()
			dn.tt.Advance(60 * time.Millisecond)
			dn.tt.Frame()
			if n := colDiff(mid, dn.tt.Image(), right); n < 5 {
				t.Errorf("变大和变小的中间帧一样（只差 %d 个像素），方向没分开", n)
			}
		}
	}
}

// inkBox 是 r 里和背景（左上角那个像素）差得明显的像素的外框。
func inkBox(a *image.RGBA, r image.Rectangle) image.Rectangle {
	bg := a.RGBAAt(r.Min.X, r.Min.Y)
	out := image.Rectangle{Min: r.Max, Max: r.Min}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p := a.RGBAAt(x, y)
			if dist(p.R, bg.R)+dist(p.G, bg.G)+dist(p.B, bg.B) > 150 {
				out.Min.X, out.Min.Y = min(out.Min.X, x), min(out.Min.Y, y)
				out.Max.X, out.Max.Y = max(out.Max.X, x+1), max(out.Max.Y, y+1)
			}
		}
	}
	return out
}

func cloneImg(a *image.RGBA) *image.RGBA {
	b := image.NewRGBA(a.Bounds())
	copy(b.Pix, a.Pix)
	return b
}

// 编辑时不滚：聚焦输入框以后显示它自己的字，外面又改了值也不盖一个旧的文字。
func TestStepperNoRollWhileEditing(t *testing.T) {
	r := newStepperRig(2000, StepperSpec{Min: 1, Max: 9999, Step: 1})
	r.click(t, "窗口")
	r.tt.Key(ui.Cmd, ui.KeyA)
	r.tt.Type("77")
	r.tt.Frame()
	if _, ok := r.tt.Find("2000"); ok {
		t.Error("编辑中还显示着旧数字的叠层")
	}
}

// 数字留位：正式字体（文渊圆体）的数字不等宽，Tabular 开了也不起作用（主人 2026-11-15 看审批卡倒计时，时钟图标跟着跳）。
// 所以滚动数字旁边有别的东西时按 chatui.DigitSlotWidth 留死宽：0:00 到 9:59 每一种留的宽都一样，而且装得下实际的字。
// 同时记一句：这款字体开不开 Tabular 数字宽度都不一样，证明靠字体特性不够。
func TestDigitSlotSteady(t *testing.T) {
	data, err := os.ReadFile(fonts.WenYuanPath())
	if err != nil {
		t.Skipf("本机没有文渊圆体（%v），量不了正式字体；默认字体的数字本来就等宽，量了也说明不了什么", err)
	}
	if err := ui.RegisterFont(data, "WY Rounded"); err != nil {
		t.Fatal(err)
	}
	k := NewKit(chatui.DefaultSettings(), "WY Rounded")
	sp := ui.Span{Size: k.G.LabelSize, Font: k.Font, Features: chatui.Tabular}
	var slots, actual []float32
	var w0, w7 float32
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		slots, actual = slots[:0], actual[:0]
		for sec := 0; sec < 600; sec += 7 {
			s := fmt.Sprintf("%d:%02d", sec/60, sec%60)
			slots = append(slots, chatui.DigitSlotWidth(c, s, sp))
			a := sp
			a.Text = s
			w, _ := c.MeasureText(1e6, a)
			actual = append(actual, w)
		}
		a := sp
		a.Text = "0"
		w0, _ = c.MeasureText(1e6, a)
		a.Text = "7"
		w7, _ = c.MeasureText(1e6, a)
	}, 200, 100)
	tt.Frame()
	t.Logf("这款字体开着 Tabular：0 宽 %.2f、7 宽 %.2f", w0, w7)
	for i := range slots {
		if slots[i] != slots[0] {
			t.Fatalf("留的宽不稳：第 %d 个 %.2f，第 0 个 %.2f", i, slots[i], slots[0])
		}
		if actual[i] > slots[i] {
			t.Fatalf("留的宽 %.2f 装不下实际的字 %.2f", slots[i], actual[i])
		}
	}
}

// SettingNumber：只读数值变化时逐位滚，终点和直接画一样。
func TestSettingNumberRolls(t *testing.T) {
	k := testKit()
	v := 41.0
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Row(c.Key("r")).Padding(10).Children(func() { k.SettingNumber(c, "n", v, func(f float64) string { return fmtInt(f) }) })
	}, 200, 80)
	tt.Frame()
	tt.Frame()
	start := cloneImg(tt.Image())
	rt, _ := tt.Find("41")
	box := image.Rect(int(rt.X)-2, int(rt.Y)-2, int(rt.X+rt.W)+2, int(rt.Y+rt.H)+2)
	v = 42
	tt.Frame()
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	if n := colDiff(start, tt.Image(), box); n < 5 {
		t.Errorf("数字变了但中间帧几乎没变（%d 个像素）", n)
	}
	settle(tt)
	if _, ok := tt.Find("42"); !ok {
		t.Errorf("终点没有 42：%v", tt.Texts())
	}
	if tt.FramePending() {
		t.Error("滚完还在要帧")
	}
}

func fmtInt(v float64) string {
	s := ""
	for n := int(v); n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

// 变得比一秒一次更勤的数字先压到一秒一次再滚；停下后补到最新值。
func TestRollNumberThrottlesToOncePerSecond(t *testing.T) {
	k := testKit()
	v := 10.0
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Row(c.Key("r")).Padding(10).Children(func() {
			k.RollNumber(c, "n", v, func(f float64) string { return fmtInt(f) })
		})
	}, 200, 80)
	tt.Frame()
	shown := func() string {
		for _, s := range tt.Texts() {
			if len(s) >= 2 && s[0] >= '0' && s[0] <= '9' {
				return s
			}
		}
		return ""
	}
	last, changes := shown(), 0
	for i := 0; i < 25; i++ { // 2.5 秒里每 100 毫秒变一次
		v++
		tt.Advance(100 * time.Millisecond)
		tt.Frame()
		if s := shown(); s != last {
			changes++
			last = s
		}
	}
	if changes > 3 {
		t.Errorf("2.5 秒里每 0.1 秒变一次，显示的数字换了 %d 次，应不多于 3 次（一秒一次）", changes)
	}
	tt.Advance(1200 * time.Millisecond)
	tt.Frame()
	settle(tt)
	if got := shown(); got != fmtInt(v) {
		t.Errorf("停下后应补到最新值 %s，显示的是 %s", fmtInt(v), got)
	}
}
