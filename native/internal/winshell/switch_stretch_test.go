package winshell

import (
	"image"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// thumbSize 量圆钮（纯白，其余都不是）在 r 里占的宽和高；找不到返回 0。
func thumbSize(img *image.RGBA, r ui.Rect) (w, h int) {
	x0, y0, x1, y1 := 1<<30, 1<<30, -1, -1
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			if p := img.RGBAAt(x, y); p.R >= 250 && p.G >= 250 && p.B >= 250 {
				x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
			}
		}
	}
	if x1 < 0 {
		return 0, 0
	}
	return x1 - x0 + 1, y1 - y0 + 1
}

// 开关滑钮：滑动中途按速度被横向拉长、纵向压扁，停下后恢复正圆；减少动态效果时不挤。
func TestSwitchThumbStretch(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		on := false
		s := chatui.DefaultSettings()
		s.Dark = true // 深色底：白色圆钮是唯一的纯白，好量
		k := NewKit(s, "")
		tt := motionTester(func(c *ui.Context) {
			ui.Box(c.Key("bg")).Fill().Background(k.P.Canvas).Padding(40).Children(func() {
				k.Switch(c, "sw", "开机自启", &on)
			})
		}, 200, 120, reduce)
		tt.Frame()
		a, ok := tt.AccessByRole(ui.RoleSwitch, "开机自启")
		if !ok {
			t.Fatal("找不到开关")
		}
		box := a.Bounds
		if w, h := thumbSize(tt.Image(), box); w == 0 || w != h {
			t.Fatalf("静止的圆钮应是正圆：%d×%d", w, h)
		}
		tt.Press(box.X+box.W/2, box.Y+box.H/2)
		tt.Release(box.X+box.W/2, box.Y+box.H/2)
		var ws, hs []int
		for i := 0; i < 90; i++ {
			step(tt, 16*time.Millisecond)
			w, h := thumbSize(tt.Image(), box)
			ws, hs = append(ws, w), append(hs, h)
		}
		w1, h1 := ws[len(ws)-1], hs[len(hs)-1] // 停下后的圆钮是基准（量法按白色像素数，抗锯齿的边会少一两像素）
		maxW, minH := w1, h1
		for i := 1; i < len(ws); i++ { // 第一帧是点下去那一帧的过渡，不算
			maxW, minH = max(maxW, ws[i]), min(minH, hs[i])
		}
		if w1 != h1 {
			t.Errorf("停下后应恢复正圆：%d×%d", w1, h1)
		}
		if reduce {
			if maxW != w1 || minH != h1 {
				t.Errorf("减少动态效果：圆钮不该挤，最宽 %d、最矮 %d（停下 %d×%d）", maxW, minH, w1, h1)
			}
		} else {
			if maxW < w1+2 {
				t.Errorf("滑动中途圆钮应被横向拉长：最宽 %d（停下 %d）", maxW, w1)
			}
			if minH >= h1 {
				t.Errorf("滑动中途圆钮应被纵向压扁：最矮 %d（停下 %d）", minH, h1)
			}
		}
		t.Logf("减少动态=%v：最宽 %d，最矮 %d，停下 %d×%d", reduce, maxW, minH, w1, h1)
	}
}

func TestThumbStretch(t *testing.T) {
	for _, c := range []struct{ v, want float32 }{{0, 0}, {250, .4}, {-250, .4}, {1000, .45}} {
		if got := thumbStretch(c.v); math.Abs(float64(got-c.want)) > 1e-6 {
			t.Errorf("thumbStretch(%v)=%v，应为 %v", c.v, got, c.want)
		}
	}
}
