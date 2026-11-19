package uitest

import (
	"image"
	"runtime"

	"github.com/egoist/mygo/ui"
)

// skipper 是 *testing.T 里 RequireGlyphs 用到的两个方法。不直接写 testing.TB：这个包也被 cmd/sumika-gallery 这类
// 非测试程序导入，不该把 testing 包链进去
type skipper interface {
	Helper()
	Skip(args ...any)
}

// RequireGlyphs 在这台机器画不出中文字时跳过当前测试，并写明缺什么。
//
// 像素和布局类的测试要把中文字真画出来再量（字色对比、字行高、字间距）。公开仓库不带字体文件（native/fonts/*.ttf 被忽略），
// Linux 的 CI 机器又没有中文字体：字一个都画不出来，对比度恒为 1.00、行高量不到，红的原因是缺字体、不是行为错。
// Windows 一格有微软雅黑，覆盖这些测试。
//
// 判断办法：用默认字体画两个不同的汉字，要求两张图都有墨迹、且彼此不同。只看有没有墨迹会把“缺字显示成同一个方块”误判成有字；
// 两个不同的字画出一样的图，说明画的是同一个缺字符号。
func RequireGlyphs(t skipper) {
	t.Helper()
	a, b := glyphImage("汉"), glyphImage("永")
	if hasInk(a) && hasInk(b) && !sameImage(a, b) {
		return
	}
	t.Skip("原因：这台机器画不出中文字（公开仓库不带字体文件，系统里也没有中文字体），像素和布局的量不出来；Windows 一格有微软雅黑，覆盖这条测试")
}

func glyphImage(text string) *image.RGBA {
	tt := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(ui.Hex("#ffffff"))
		ui.Text(c.Key("glyph"), text).FontSize(48).TextColor(ui.Hex("#000000"))
	}, 120, 90)
	tt.Frame()
	tt.Frame()
	return tt.Image()
}

func hasInk(img *image.RGBA) bool {
	for i := 0; i+3 < len(img.Pix); i += 4 {
		if img.Pix[i] < 128 {
			return true
		}
	}
	return false
}

func sameImage(a, b *image.RGBA) bool {
	if a.Bounds() != b.Bounds() || len(a.Pix) != len(b.Pix) {
		return false
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return false
		}
	}
	return true
}

// RequireWindowsFontMetrics 在非 Windows 上跳过依赖字体度量的用例，并写明原因。
// 这些用例量的是像素位置、行高、字宽（量出 29.7 的地方 Noto 给 33.5），数是对着 Windows 的字体（文渊圆体、微软雅黑）调的；
// Linux 的 CI 机器上装的是 Noto CJK，行距和字宽本来就不同，量出来不一样不等于布局错。
// 这条跳过是“度量不同不算错”的判断，不是证明 Noto 下布局没问题；真要在 Linux 上测，得把文渊圆体带进 CI（docs/待办.md 第八节 Linux 那条）。
func RequireWindowsFontMetrics(t skipper, what string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("原因：" + what + "依赖字体度量，数是对着 Windows 的字体调的，Linux 的 CI 机器用 Noto CJK，度量不同不算错")
	}
}
