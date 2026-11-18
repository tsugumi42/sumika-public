package chatui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 壁纸只在尺寸、主题、角色色变化时重画；同一尺寸连续几帧只画一次（规格六）。
func TestWallpaperCache(t *testing.T) {
	tk := Derive(DefaultSettings())
	var wp Wallpaper
	var w float32 = 1200
	view := func(c *ui.Context) {
		ui.Box(c.Key("wall")).Size(w, 800).Draw(func(p *ui.Painter, r ui.Rect) { wp.Paint(p, r, tk, CharColors[0]) })
	}
	tt := ui.NewTester(view, 1300, 900)
	tt.SetScale(1.25)
	start := time.Now()
	tt.Frame()
	first := time.Since(start)
	base := wp.Hits // NewTester 先按 1 倍画过一次，SetScale 后按 1.25 倍再画；从这里起算
	tt.Frame()
	tt.Frame()
	if wp.Hits != base {
		t.Fatalf("同一尺寸又重画了 %d 次", wp.Hits-base)
	}
	w = 1000
	tt.Frame()
	if wp.Hits != base+1 {
		t.Fatalf("改尺寸后应重画一次，实际 %d 次", wp.Hits-base)
	}
	t.Logf("1200×800 DIP @1.25 首帧含重画 %v", first)
}

// 2048×1030 @1.25（主人的屏）整张壁纸画一次的耗时，记进日志看是否需要再优化。
func BenchmarkWallpaper(b *testing.B) {
	tk := Derive(DefaultSettings())
	for i := 0; i < b.N; i++ {
		renderWallpaper(2560, 1288, 1.25, tk.C.ChatBg, tk.C.DotInk, tk.AccentOf(CharColors[0]).Base)
	}
}

// 光晕照 Bun 在椭圆半径的 72% 处淡到透明（审查 B9）：半径 80% 处是纯底色，50% 处还带角色色。
func TestWallpaperGlowFalloff(t *testing.T) {
	w, h := 400, 300
	base, dot, accent := ui.RGB(240, 240, 240), ui.RGB(240, 240, 240), ui.RGB(0, 160, 160) // 波点和底色同色，只看光晕
	img := renderWallpaper(w, h, 1, base, dot, accent)
	g := glows[0] // 中心 (0.70, 0.18)，往左量，别的两团够不着
	at := func(d float64) ui.Color {
		x := int((g.cx - d*g.rx) * float64(w))
		p := img.RGBAAt(x, int(g.cy*float64(h)))
		return ui.Color{R: p.R, G: p.G, B: p.B, A: 255}
	}
	if c := at(0.8); c != base {
		t.Errorf("半径 80%% 处还有光晕：%v，期望底色 %v", c, base)
	}
	if c := at(0.5); c == base {
		t.Errorf("半径 50%% 处没有光晕")
	}
}
