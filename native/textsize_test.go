package main

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// 字号两份主题一起改，其他字号按同一比例缩放，间距跟着，圆角不动。
func TestTextSizeScalesBothThemes(t *testing.T) {
	t.Parallel()
	a := &app{}
	a.initLook()
	base := theme.Light().Typography()
	radius := a.look.light.Metrics().Control().RadiusLarge() // 默认是原型预设的圆角，字号改完应一模一样
	a.look.current = a.look.light
	a.setTextSize(18)
	for _, th := range []*theme.Theme{a.look.light, a.look.night} {
		if got := th.Typography().Body().Size(); got != 18 {
			t.Fatalf("正文字号 %v", got)
		}
		want := base.Sidebar().Size() * 18 / 13
		if math.Abs(float64(th.Typography().Sidebar().Size()-want)) > 0.01 {
			t.Fatalf("侧栏字号没有同比例：%v 想要 %v", th.Typography().Sidebar().Size(), want)
		}
		if th.Metrics().Control().RadiusLarge() != radius {
			t.Fatal("圆角不应跟字号变")
		}
	}
	if sp := theme.Light().Metrics().Control().Spacing(); math.Abs(float64(a.look.light.Metrics().Control().Spacing()-sp*18/13)) > 0.01 {
		t.Fatal("控件间距没有跟字号")
	}
	// 来回切不累积误差
	a.setTextSize(13)
	if got := a.look.night.Typography().Sidebar().Size(); math.Abs(float64(got-base.Sidebar().Size())) > 0.01 {
		t.Fatalf("切回 13 有漂移：%v", got)
	}
	// 档外的值显示最近一档
	if textSizeIndex(20) != len(textSizes)-1 || textSizeIndex(11) != 0 {
		t.Fatal("档外取最近")
	}
}

// 16 和 18 下各页面的文字高度变大，且设置页的文字不出界。
func TestTextSizePagesFit(t *testing.T) {
	for _, rail := range []int{0, 1, 2, 3, 4} {
		var heights [2]float32
		for i, size := range []float32{16, 18} {
			a := &app{rail: rail}
			a.initLook()
			a.setTextSize(size)
			a.look.current = nil
			var fs float32
			tt := ui.NewTester(func(c *ui.Context) { a.view(c); fs = c.Theme().FontSize }, 1300, 1030)
			tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			tt.Frame()
			if fs != size {
				t.Fatalf("rail %d: ui 字号 %v", rail, fs)
			}
			heights[i] = fs
			if rail == 4 {
				for _, label := range []string{"字号", "深浅模式"} {
					r, ok := tt.Find(label)
					if !ok || r.X < 0 || r.X+r.W > 1300 || r.Y < 0 || r.Y+r.H > 1030 {
						t.Fatalf("设置页 %s 在 %v 下出界：%v %v", label, size, r, ok)
					}
				}
			}
		}
		if heights[1] <= heights[0] {
			t.Fatalf("rail %d 字号没变大", rail)
		}
	}
}
