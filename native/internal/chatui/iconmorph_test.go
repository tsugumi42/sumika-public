package chatui

import (
	"image"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/assets/icons"
)

// 图标变形（规格-过渡动画 第十一节后的“数字和图标”）：路径解析对得上 mygo 自己画的；撒点和配对不丢形状；
// 变形中间帧既不是起点也不是终点，终点和直接画目标图标逐像素一样；减少动态效果直接换；变到一半再换不跳。

// 画廊和代码块里用到的几对，加上有洞的（复制）、带圆弧的（关闭）、多块的（暂停）。
var morphIcons = []string{"copy", "check", "send", "stop", "menu", "close", "play", "pause"}

const morphBox = 96 // 测试里图标画多大（像素）

func bodyOf(t *testing.T, name string) string {
	t.Helper()
	b, ok := iconBody(name, false)
	if !ok {
		t.Fatalf("取不到图标 %s 的路径", name)
	}
	return b
}

// paintPolys 在白底上把若干多边形填黑，规则按图标自己的（evenodd 或 nonzero），画 morphBox×morphBox。
func paintPolys(t *testing.T, polys [][]mpt, evenOdd bool) *image.RGBA {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(ui.RGB(255, 255, 255))
		ui.Box(c.Key("b")).Size(morphBox, morphBox).Draw(func(pt *ui.Painter, r ui.Rect) {
			var path ui.Path
			for _, poly := range polys {
				for i, q := range poly {
					if i == 0 {
						path.MoveTo(q.x, q.y)
					} else {
						path.LineTo(q.x, q.y)
					}
				}
				path.Close()
			}
			if evenOdd {
				path.FillRule(ui.EvenOdd)
			}
			s := float32(morphBox) / iconGrid
			pt.Transform(ui.Scaling(s, s), r.X, r.Y, func() { pt.FillPath(&path, ui.RGB(0, 0, 0)) })
		})
	}, morphBox, morphBox)
	tt.Frame()
	return tt.Image()
}

// paintIcon 在白底上画 name 的真图标。
func paintIcon(name string) *image.RGBA {
	tt := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(ui.RGB(255, 255, 255))
		ui.Icon(c.Key("i"), Icon(name, false)).Size(morphBox, morphBox).TextColor(ui.RGB(0, 0, 0))
	}, morphBox, morphBox)
	tt.Frame()
	return tt.Image()
}

// diffFrac 是两张图里亮度差超过 128 的像素占整张图的比例。
func diffFrac(a, b *image.RGBA) float64 {
	n := 0
	for i := 0; i+3 < len(a.Pix) && i+3 < len(b.Pix); i += 4 {
		d := int(a.Pix[i]) - int(b.Pix[i])
		if d < 0 {
			d = -d
		}
		if d > 128 {
			n++
		}
	}
	return float64(n) / float64(len(a.Pix)/4)
}

func inkFrac(a *image.RGBA) float64 {
	n := 0
	for i := 0; i+3 < len(a.Pix); i += 4 {
		if a.Pix[i] < 128 {
			n++
		}
	}
	return float64(n) / float64(len(a.Pix)/4)
}

// 路径解析：按 d 切成折线（不撒点）填出来，和 mygo 自己画的图标几乎逐像素一致；撒 96 点以后轮廓差得稍多一点但仍然很像。
func TestShapeParseMatchesMygo(t *testing.T) {
	for _, name := range morphIcons {
		body := bodyOf(t, name)
		want := paintIcon(name)
		var polys [][]mpt
		for _, m := range rePathD.FindAllStringSubmatch(body, -1) {
			subs, err := flattenPath(m[1])
			if err != nil {
				t.Fatalf("%s 的路径解析失败：%v", name, err)
			}
			polys = append(polys, subs...)
		}
		eo := strings.Contains(body, `fill-rule="evenodd"`)
		if f := diffFrac(want, paintPolys(t, polys, eo)); f > 0.012 { // 边缘抗锯齿的取整不同，实测最大约 0.7%；路径读错了会差到几个百分点以上
			t.Errorf("%s：整条路径切成折线后和真图标差了 %.2f%% 的像素（应小于 1.2%%）", name, f*100)
		}
		sh, ok := ParseShape(body)
		if !ok {
			t.Fatalf("%s 取不到轮廓", name)
		}
		if f := diffFrac(want, paintPolys(t, sh.Points(), eo)); f > 0.03 {
			t.Errorf("%s：撒 %d 点后和真图标差了 %.2f%% 的像素（应小于 3%%）", name, morphPoints, f*100)
		}
	}
}

// 圆弧、省略写法、相对命令：边角用例，和 mygo 的 ParsePath 画出来对照。
func TestFlattenPathForms(t *testing.T) {
	for _, d := range []string{
		"M4 4h16v16H4z",
		"M4 12a8 8 0 1 0 16 0a8 8 0 1 0-16 0z",
		"M4 12a8 8 0 1016 0a8 8 0 10-16 0z",
		"m4 4 16 0 0 16-16 0z",
		"M3 3C3 9 9 3 12 12S21 21 21 3Q12 0 3 3z",
		"M3 3q9 0 9 9t9 9L3 21z",
		"M4 4.5.5 8l.5-3.5zM10 10h4v4h-4z",
	} {
		subs, err := flattenPath(d)
		if err != nil {
			t.Errorf("%q 解析失败：%v", d, err)
			continue
		}
		tt := func(polys [][]mpt) *image.RGBA { return paintPolys(t, polys, false) }
		ref := ui.NewTester(func(c *ui.Context) {
			c.Root().Background(ui.RGB(255, 255, 255))
			ui.Box(c.Key("b")).Size(morphBox, morphBox).Draw(func(pt *ui.Painter, r ui.Rect) {
				s := float32(morphBox) / iconGrid
				pt.Transform(ui.Scaling(s, s), r.X, r.Y, func() { pt.FillPath(ui.MustParsePath(d), ui.RGB(0, 0, 0)) })
			})
		}, morphBox, morphBox)
		ref.Frame()
		if f := diffFrac(ref.Image(), tt(subs)); f > 0.004 {
			t.Errorf("%q：和 mygo 的 ParsePath 差了 %.2f%% 的像素", d, f*100)
		}
	}
	for _, bad := range []string{"M4 4L", "M4", "X1 2", "M4 4 a1 1 0 2 0 1 1"} {
		if _, err := flattenPath(bad); err == nil {
			t.Errorf("%q 本该报错", bad)
		}
	}
}

// 描边写法的图标不变形（Fluid：只变形填充形状）。
func TestStrokeIconHasNoShape(t *testing.T) {
	if _, ok := ParseShape(bodyOf(t, "add")); ok {
		t.Error("描边写法的 add 不该有轮廓")
	}
	if _, ok := ParseShape(bodyOf(t, "checks")); ok {
		t.Error("描边写法的 checks 不该有轮廓")
	}
	if g := MorphGlyph("add", false); g.Shape != nil || g.SVG == nil {
		t.Error("add 应该有 SVG、没有轮廓")
	}
}

// 起点帧是起点的形状、终点帧和目标的形状逐像素一样（只是点的起点转过）；配对是一对一、多出来的从一个点长出来。
func TestMorphEndpoints(t *testing.T) {
	for _, pr := range [][2]string{{"copy", "check"}, {"send", "stop"}, {"menu", "close"}, {"play", "pause"}} {
		a, _ := ParseShape(bodyOf(t, pr[0]))
		b, _ := ParseShape(bodyOf(t, pr[1]))
		m := NewMorph(a, b)
		if want := max(len(a.cs), len(b.cs)); len(m.pairs) != want {
			t.Errorf("%v：配出 %d 对，应是 %d", pr, len(m.pairs), want)
		}
		for _, tc := range []struct {
			t    float32
			src  *Shape
			name string
		}{{0, a, "起点"}, {1, b, "终点"}} {
			got := paintPolys(t, m.Frame(tc.t).Points(), true)
			want := paintPolys(t, tc.src.Points(), true)
			if f := diffFrac(got, want); f > 0.001 {
				t.Errorf("%v %s帧和原形状差了 %.2f%% 的像素", pr, tc.name, f*100)
			}
		}
		mid := paintPolys(t, m.Frame(.5).Points(), true)
		if diffFrac(mid, paintPolys(t, a.Points(), true)) < 0.01 || diffFrac(mid, paintPolys(t, b.Points(), true)) < 0.01 {
			t.Errorf("%v 一半时的形状应既不是起点也不是终点", pr)
		}
	}
}

// 变形这个位置的小界面：name 是现在要的图标。
func morphView(m *IconMorph, name *string, tk Tokens) func(c *ui.Context) {
	return func(c *ui.Context) {
		c.Root().Background(ui.RGB(255, 255, 255))
		m.Show(c, "m", MorphGlyph(*name, false), morphBox, ui.RGB(0, 0, 0), tk.Spring.Snappy)
	}
}

func TestIconMorphFrames(t *testing.T) {
	tk := Derive(DefaultSettings())
	for _, reduce := range []bool{false, true} {
		for _, pr := range [][2]string{{"copy", "check"}, {"send", "stop"}, {"menu", "close"}, {"play", "pause"}} {
			name := pr[0]
			m := &IconMorph{}
			tt := ui.NewTester(morphView(m, &name, tk), morphBox, morphBox)
			tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
			tt.Frame()
			start := tt.Image()
			if f := diffFrac(start, paintIcon(pr[0])); f != 0 {
				t.Errorf("reduce=%v %v：没变形时和直接画图标不一样（%.3f%%）", reduce, pr, f*100)
			}
			name = pr[1]
			tt.Frame()
			tt.Advance(60 * time.Millisecond)
			tt.Frame()
			mid := tt.Image()
			end := paintIcon(pr[1])
			if reduce {
				if diffFrac(mid, end) != 0 {
					t.Errorf("reduce=%v %v：减少动态效果时应直接换成目标图标", reduce, pr)
				}
				if tt.FramePending() {
					t.Errorf("reduce=%v %v：换完还在要帧", reduce, pr)
				}
				continue
			}
			if diffFrac(mid, start) < 0.005 || diffFrac(mid, end) < 0.005 {
				t.Errorf("%v：60 毫秒时应已离开起点、还没到终点（离起点 %.2f%%、离终点 %.2f%%）", pr, diffFrac(mid, start)*100, diffFrac(mid, end)*100)
			}
			if ink := inkFrac(mid); ink < 0.02 {
				t.Errorf("%v：中间帧几乎是空白（墨迹只占 %.2f%%），变形没画出来", pr, ink*100)
			}
			if !tt.FramePending() {
				t.Errorf("%v：变形中间不要帧了", pr)
			}
			tt.Advance(time.Second) // 放完（不按固定毫秒等：等到不再要帧为止）
			tt.Frame()
			for i := 0; tt.FramePending() && i < 100; i++ {
				tt.Advance(50 * time.Millisecond)
				tt.Frame()
			}
			if f := diffFrac(tt.Image(), end); f != 0 {
				t.Errorf("%v：终点和直接画目标图标差了 %.3f%% 的像素（应逐像素一致）", pr, f*100)
			}
			if tt.FramePending() {
				t.Errorf("%v：放完了还在要帧（空转）", pr)
			}
		}
	}
}

// 变到一半又换：从当时的形状接着流，下一帧和上一帧差不多，不跳回起点或跳到终点。
func TestIconMorphInterrupt(t *testing.T) {
	tk := Derive(DefaultSettings())
	name := "copy"
	m := &IconMorph{}
	tt := ui.NewTester(morphView(m, &name, tk), morphBox, morphBox)
	tt.Frame()
	name = "check"
	tt.Frame()
	tt.Advance(70 * time.Millisecond)
	tt.Frame()
	before := tt.Image()
	name = "copy" // 又点了一下，换回去
	tt.Frame()
	after := tt.Image()
	if f := diffFrac(before, after); f > 0.01 {
		t.Errorf("被打断那一帧跳了：和上一帧差 %.2f%% 的像素", f*100)
	}
	if f := diffFrac(after, paintIcon("copy")); f < 0.005 {
		t.Errorf("被打断后还没动就已经是起点了（差 %.2f%%）", f*100)
	}
	for i := 0; tt.FramePending() && i < 200; i++ {
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	if f := diffFrac(tt.Image(), paintIcon("copy")); f != 0 {
		t.Errorf("打断后终点和直接画 copy 差了 %.3f%%", f*100)
	}
}

// 没有轮廓的图标（描边写法）退回 mygo 的替换，也走完、不空转。
func TestIconMorphFallback(t *testing.T) {
	tk := Derive(DefaultSettings())
	name := "add"
	m := &IconMorph{}
	tt := ui.NewTester(morphView(m, &name, tk), morphBox, morphBox)
	tt.Frame()
	name = "check"
	tt.Frame()
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	if !tt.FramePending() {
		t.Error("退回替换时中间应在要帧")
	}
	for i := 0; tt.FramePending() && i < 100; i++ {
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	if f := diffFrac(tt.Image(), paintIcon("check")); f != 0 {
		t.Errorf("退回替换的终点和直接画 check 差了 %.3f%%", f*100)
	}
}

// 数据里没有的名字不在这里测；这里只确认 icons 包给的路径文本形状没变（单个 path、没有描边）。
func TestIconBodiesAreSinglePathFills(t *testing.T) {
	for _, n := range []string{"copy_2", "check", "send_plane", "stop_circle", "menu", "close", "play", "pause"} {
		b, err := icons.Body(n, false)
		if err != nil || strings.Contains(b, "stroke=") {
			t.Errorf("%s：%v / 有描边", n, err)
		}
	}
}
