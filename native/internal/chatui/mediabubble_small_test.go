package chatui

import (
	"testing"

	"sumika-native/internal/segview"
)

// 小图不放大：设计语言“单张按原比例缩进 320 × 360 以内，小图不放大”。
func TestSingleMediaSmallNotUpscaled(t *testing.T) {
	cases := []struct {
		iw, ih int
		w, h   float32
	}{
		{200, 150, 200, 150}, // 小于上限，原样
		{100, 100, 120, 100}, // 比最窄还窄，只把宽兜到 120
		{640, 480, 320, 240}, // 大图照旧缩进 320
		{300, 900, 120, 360}, // 窄竖图：按高缩到 360，宽兜到 120
	}
	for _, c := range cases {
		w, h := singleMediaSize(segview.Segment{Width: c.iw, Height: c.ih}, 600)
		if w != c.w || h != c.h {
			t.Errorf("%d×%d → %.0f×%.0f，要 %.0f×%.0f", c.iw, c.ih, w, h, c.w, c.h)
		}
	}
}
