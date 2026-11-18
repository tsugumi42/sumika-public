package winshell

import (
	"math"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/chatui"
	"sumika-native/internal/design/colorx"
)

// “选中”按用途分两种长相（主人 2026-11-15 定的分工，2026-11-17 在画廊对比小样里挑定 B1）：
//
//	模式切换和位置（分段、导航、会话行、右栏页签、设置分类）：浮起的一块（Raised 的底 + 内容档影子），夜间亮一档；
//	标签（筛选片、下拉当前项）：强调色浅底，不带影子。
//
// 选中都从这里上色，不在各控件里自己挑颜色。
const (
	selMode = iota
	selTag
)

// darkSoftL、darkSoftC 是夜间强调色浅底的明度和彩度上限：比夜间凹块（约 0.30）亮一档，字仍用正文色。
// 以前直接用 Palette.Self，夜间那片浅底比没选中的灰底还暗，看起来像“没选中”。
const darkSoftL, darkSoftC = .40, .07

// selectFill 是选中的底。
func (k *Kit) selectFill(kind int) ui.Color {
	if kind == selMode {
		return k.raisedFill()
	}
	return k.softSelect()
}

// softSelect 是标签类选中的强调色浅底：亮色是 Palette.Self，夜间提亮到比周围亮。
func (k *Kit) softSelect() ui.Color {
	if k.S.Dark {
		ch := chatui.DefaultChar.Design()
		return colorx.New(darkSoftL, math.Min(ch.C, darkSoftC), ch.H).Color()
	}
	return k.P.Self
}

// selectBg 给自己画底的选中项上底色（模式类再加影子）。
func (k *Kit) selectBg(e ui.Element, kind int) ui.Element {
	e.Background(k.selectFill(kind))
	if kind == selMode {
		k.RaisedShadow(e)
	}
	return e
}

// selectPaint 在画笔上画选中块（滑动的那一块）。
func (k *Kit) selectPaint(pt *ui.Painter, r ui.Rect, radius float32, kind int) {
	if kind == selMode {
		k.RaisedPaint(pt, r, radius)
	}
	pt.Fill(r, k.selectFill(kind), radius)
}
