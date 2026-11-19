package chatui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/assets/icons"
)

// 老表里的 82 个短名必须全部还能取到，两种样式都要有（alert 只有实心、grip 只有线性是自己画的，另算）。
func TestLegacyIconNamesResolve(t *testing.T) {
	if len(legacyIconBodies) != 82 {
		t.Fatalf("老表应有 82 个短名，现在列了 %d 个", len(legacyIconBodies))
	}
	for name := range legacyIconBodies {
		for _, fill := range []bool{false, true} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("Icon(%q, fill=%v) panic：%v", name, fill, r)
					}
				}()
				if Icon(name, fill) == nil {
					t.Errorf("Icon(%q, fill=%v) 是 nil", name, fill)
				}
			}()
		}
	}
}

// 别名都要指到 icons 里真有、两种样式齐全的图标；同名的不该写别名。
func TestIconAliasTargets(t *testing.T) {
	for short, mc := range iconAlias {
		if short == mc {
			t.Errorf("别名 %q 和目标同名，不用写", short)
		}
		for _, fill := range []bool{false, true} {
			if _, err := icons.Body(mc, fill); err != nil {
				t.Errorf("别名 %q → %q fill=%v：%v", short, mc, fill, err)
			}
		}
	}
}

// 全套 MingCute 名字能直接用；没有的名字 panic，信息里带上名字。
func TestIconMingCuteNamesAndUnknown(t *testing.T) {
	if Icon("chat_3", false) == nil || Icon("heart", true) == nil {
		t.Fatal("MingCute 名字取不到")
	}
	if Icon("alert", true) == nil || Icon("grip", false) == nil {
		t.Fatal("自己画的图标取不到")
	}
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "no_such_icon") {
			t.Fatalf("没有的名字应当 panic 并带上名字，得到 %v", r)
		}
	}()
	Icon("no_such_icon", false)
}

func rasterGray(t *testing.T, svg *ui.SVG) []uint8 {
	t.Helper()
	view := func(c *ui.Context) {
		ui.Box(c.Key("r")).Size(24, 24).Background(ui.RGB(255, 255, 255)).Children(func() {
			ui.Icon(c, svg).Size(24, 24).TextColor(ui.RGB(0, 0, 0))
		})
	}
	tt := ui.NewTester(view, 24, 24)
	tt.SetScale(2)
	tt.Frame()
	tt.Frame()
	img := tt.Image()
	b := img.Bounds()
	out := make([]uint8, 0, b.Dx()*b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			out = append(out, uint8(r>>8))
		}
	}
	return out
}

func meanAbsDiff(a, b []uint8) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 1e9
	}
	var s float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		if d < 0 {
			d = -d
		}
		s += d
	}
	return s / float64(len(a))
}

// 图标长相不变：老表（icons_legacy_test.go）里 82 个图标的线性、实心各画成 48×48 灰度图，和现在 Icon 取到的比，
// 平均每个像素差不超过 2.5（满分 255；不同的图标差得远多于此，见对照组）。差得多的几个已经固定成老版本（pinnedLegacyIcons），这里量出来是 0。
func TestIconLookUnchanged(t *testing.T) {
	const limit = 2.5
	if len(legacyIconBodies) != 82 {
		t.Fatalf("老表应有 82 个图标，现在 %d 个", len(legacyIconBodies))
	}
	for name, bodies := range legacyIconBodies {
		for i, fill := range []bool{false, true} {
			if bodies[i] == "" {
				continue
			}
			old := ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">` + bodies[i] + `</svg>`))
			d := meanAbsDiff(rasterGray(t, old), rasterGray(t, Icon(name, fill)))
			if d > limit {
				t.Errorf("%s fill=%v：和老版本平均差 %.2f，超过 %v，长相变了（要么固定成老版本，要么由用户看过再换）", name, fill, d, limit)
			}
		}
	}
}

// 对照组：相近但不同的图标，量出来的差要超过门槛，证明这个量分得出“换了图标”（以前用 add 对 close 太容易，门槛 12 时 info 和 warning 也算一样）。
func TestIconLookMetricSeparatesSimilarIcons(t *testing.T) {
	const limit = 2.5
	for _, p := range [][2]string{{"information", "warning"}, {"refresh_2", "refresh_1"}, {"archive", "unarchive"}, {"bell", "bellOff"}, {"volume", "volumeOff"}, {"zoomIn", "zoomOut"}} {
		for _, fill := range []bool{false, true} {
			d := meanAbsDiff(rasterGray(t, Icon(p[0], fill)), rasterGray(t, Icon(p[1], fill)))
			if d <= limit {
				t.Errorf("%s 和 %s fill=%v 只差 %.2f，这个量分不出它们", p[0], p[1], fill, d)
			}
		}
	}
}

// 固定成老版本的几个：ownSVG 里就是老表的原文，线性、实心都在；MingCute 里同名的新画法确实不一样（所以才要固定）。
func TestPinnedLegacyIconsAreOldBodies(t *testing.T) {
	for _, name := range pinnedLegacyIcons {
		own, ok := ownSVG[name]
		if !ok {
			t.Errorf("%s 该固定在 ownSVG 里", name)
			continue
		}
		if own != legacyIconBodies[name] {
			t.Errorf("%s 固定的不是老表的原文", name)
		}
		if own[0] == "" || own[1] == "" {
			t.Errorf("%s 线性、实心都要固定", name)
		}
		mc := name // 对应的 MingCute 名；短名和它不同的只有 minus
		if name == "minus" {
			mc = "minimize"
		}
		d := meanAbsDiff(rasterGray(t, Icon(name, false)), rasterGray(t, icons.Must(mc, false)))
		d2 := meanAbsDiff(rasterGray(t, Icon(name, true)), rasterGray(t, icons.Must(mc, true)))
		if d <= 2.5 && d2 <= 2.5 {
			t.Errorf("%s 和新画法其实差不多（%.2f、%.2f），不用固定", name, d, d2)
		}
	}
	// 不在固定名单里的，不该在 ownSVG 里（grip、alert 是自己画的，MingCute 里没有）
	for name := range ownSVG {
		if name == "grip" || name == "alert" {
			continue
		}
		found := false
		for _, p := range pinnedLegacyIcons {
			found = found || p == name
		}
		if !found {
			t.Errorf("ownSVG 里多了 %s：要么加进 pinnedLegacyIcons 说明为什么固定，要么删掉", name)
		}
	}
}
