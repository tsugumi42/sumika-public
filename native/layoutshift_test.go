package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 切换时不相关的元素一帧也不能动（设计语言：切换不抖）。这是可复用的测试工具：
//
//	assertNoLayoutShift(t, tt, []string{"look-radius", "look-density"}, func() { click(t, tt, "夜间") }, 20)
//
// 先记下 keys 这些元素的框作基线，执行 act，然后逐帧（每帧推进 16 毫秒）把它们的位置和大小和基线比；
// 任何一帧、任何一个元素的 x、y、宽、高偏了超过半个像素就算红，并说明是哪一帧、哪个元素、偏了多少。
// 元素按 c.Key 起的名字找；找不到基线里没有的元素算调用写错。
func assertNoLayoutShift(t *testing.T, tt *ui.Tester, keys []string, act func(), frames int) {
	t.Helper()
	assertNoShift(t, tt, keys, act, frames, true)
}

// assertNoHorizontalShift 同上，但不看纵向位置：字号变了行高跟着变，下面的元素纵向挪是应该的，横向和大小不能动。
func assertNoHorizontalShift(t *testing.T, tt *ui.Tester, keys []string, act func(), frames int) {
	t.Helper()
	assertNoShift(t, tt, keys, act, frames, false)
}

func assertNoShift(t *testing.T, tt *ui.Tester, keys []string, act func(), frames int, vertical bool) {
	t.Helper()
	rect := func() map[string]uiBox {
		out := map[string]uiBox{}
		for _, b := range uiBoxes(tt) {
			if _, dup := out[b.Key]; !dup && b.Key != "" {
				out[b.Key] = b
			}
		}
		return out
	}
	base := rect()
	for _, k := range keys {
		if _, ok := base[k]; !ok {
			t.Fatalf("基线里没有元素 %q（测试写错了，或者页面没画出它）", k)
		}
	}
	act()
	for f := 1; f <= frames; f++ {
		now := rect()
		for _, k := range keys {
			b, ok := now[k]
			if !ok {
				t.Fatalf("第 %d 帧：元素 %q 不见了", f, k)
			}
			o := base[k]
			if d := maxDelta(b.Box, o.Box, vertical); d > 0.5 {
				t.Fatalf("第 %d 帧：元素 %q 动了 %.1f 像素：基线 %+v → 这一帧 %+v", f, k, d, o.Box, b.Box)
			}
		}
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
}

func maxDelta(a, b ui.Rect, vertical bool) float32 {
	m := float32(0)
	ds := []float32{a.X - b.X, a.W - b.W, a.H - b.H}
	if vertical {
		ds = append(ds, a.Y-b.Y)
	}
	for _, d := range ds {
		if d < 0 {
			d = -d
		}
		m = max(m, d)
	}
	return m
}

func TestAssertNoLayoutShiftCatchesAShift(t *testing.T) {
	moved := false
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(10).Children(func() {
			if moved {
				ui.Box(c.Key("pad")).Height(24)
			}
			ui.Text(c.Key("below"), "下面的一行")
		})
	}, 300, 200)
	tt.Frame()
	ft := &fakeT{}
	func() {
		defer func() { recover() }()
		assertNoLayoutShiftOn(ft, tt, []string{"below"}, func() { moved = true; tt.Frame() }, 3)
	}()
	if !ft.failed {
		t.Fatal("下面一行被推下去了，工具却没报")
	}
}

// 用于测试这个工具自己：记录有没有失败，失败时 panic 跳出来。
type fakeT struct{ failed bool }

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(string, ...any) {
	f.failed = true
	panic("fail")
}

// assertNoLayoutShiftOn 和 assertNoLayoutShift 一样，只是把 *testing.T 换成接口，工具自己的测试要用。
func assertNoLayoutShiftOn(t interface {
	Helper()
	Fatalf(string, ...any)
}, tt *ui.Tester, keys []string, act func(), frames int) {
	t.Helper()
	rect := func() map[string]uiBox {
		out := map[string]uiBox{}
		for _, b := range uiBoxes(tt) {
			if _, dup := out[b.Key]; !dup && b.Key != "" {
				out[b.Key] = b
			}
		}
		return out
	}
	base := rect()
	act()
	for f := 1; f <= frames; f++ {
		now := rect()
		for _, k := range keys {
			if d := maxDelta(now[k].Box, base[k].Box, true); d > 0.5 {
				t.Fatalf("第 %d 帧：元素 %q 动了 %.1f 像素", f, k, d)
			}
		}
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
}

var _ = fmt.Sprint

// 外观页：点分段、拨开关、选下拉的时候，下面各行一帧也不动。
func TestShellAppearanceSwitchesDoNotShiftRows(t *testing.T) {
	a := shellApp()
	a.rail = 4
	a.initLook()
	tt := shellTester(a, 2048, 1030)
	below := []string{"look-radius", "look-density", "look-neutral-row", "look-wallpaper-row", "look-material-row", "look-ligatures-row", "look-preview"}
	for _, step := range []struct {
		name string
		act  func()
	}{
		{"外观模式切夜间", func() { click(t, tt, "夜间") }},
		{"外观模式切回跟随系统", func() { click(t, tt, "跟随系统") }},
		{"底色切偏暖", func() { click(t, tt, "偏暖") }},
		{"面板材质切磨砂", func() { click(t, tt, "磨砂") }},
		{"面板材质切实色", func() { click(t, tt, "实色") }},
	} {
		keys := below
		if step.name == "外观模式切夜间" || step.name == "外观模式切回跟随系统" {
			keys = below[:] // 切深浅是整页换色，布局也不许动
		}
		t.Run(step.name, func(t *testing.T) { assertNoLayoutShift(t, tt, keys, step.act, 20) })
	}
	t.Run("字体下拉", func(t *testing.T) {
		assertNoLayoutShift(t, tt, below, func() { click(t, tt, "文渊圆体（默认）") }, 10)
	})
}

// 设置分类之间切换：左栏、标题行一帧也不动。
func TestShellSettingsCategorySwitchDoesNotShiftFrame(t *testing.T) {
	a := shellApp()
	a.rail = 4
	a.initLook()
	tt := shellTester(a, 2048, 1030)
	stable := []string{"navigation", "sidebar", "settings-header", "search-row", categoryKey(tabLook), categoryKey(tabNotify)}
	assertNoLayoutShift(t, tt, stable, func() { click(t, tt, "通知") }, 15)
	assertNoLayoutShift(t, tt, stable, func() { click(t, tt, "外观") }, 15)
}

// 会话列表切私聊 / 群聊：导航、搜索行、分段本身、聊天标题栏一帧也不动。
func TestShellConversationSegmentDoesNotShiftFrame(t *testing.T) {
	a := shellApp()
	tt := shellTester(a, 2048, 1030)
	stable := []string{"navigation", "search-row", "venues", "chat-header", "composer-frame"}
	assertNoLayoutShift(t, tt, stable, func() { click(t, tt, "群聊") }, 15)
	assertNoLayoutShift(t, tt, stable, func() { click(t, tt, "私聊") }, 15)
}
