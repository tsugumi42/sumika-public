package linkcard

// 卡片上的表情用 Twemoji 的 SVG 画（CC-BY 4.0，jdecked/twemoji，和界面同一份图，ADR-150），不用系统的 Segoe UI Emoji，
// 也不让文渊圆体、Maple 把带 VS16 的符号画成单色字形（㊗️、❤️）。做法：正文切成普通字和表情序列，有图的表情换成一个
// U+FFFC 内嵌对象（ui.Span.ObjectWidth），字照常排，对象的位置上画图；量和画用同一份 Span，行高、折行对得上。
// 切序列、按序列找图都在公共的表情服务（assets/emoji）里，和界面是同一份；找目录用 assets.Twemoji。
// 没有 Twemoji 目录、或这个序列没有图时退回原来的做法：交字体画（缺字兜底链最后是系统的彩色表情字体）。

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/assets/emoji"
)

// 照 X 网页的表情图：高宽 1.2em、左右各留 0.075em、vertical-align -20%（相对行高）
const (
	emojiImage  float32 = 1.2
	emojiMargin float32 = 0.075
	emojiDrop   float32 = 0.2
)

// emojiSpans 把字排成 Span：有图的表情换成 U+FFFC 对象，objs 按对象在整段字里的字序号（rune）记图。
// line 是这段字的行高（vertical-align -20% 相对行高）。一个表情都没有时就是一个 Span，和直接排字一样。
func emojiSpans(set *emoji.Set, s string, size, line float32) (sp []ui.Span, objs map[int]*ui.SVG) {
	n := 0
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			sp = append(sp, ui.Span{Text: text.String()})
			text.Reset()
		}
	}
	for _, p := range emoji.Split(s) {
		var svg *ui.SVG
		if p.Emoji {
			svg = set.SVG(p.Text)
		}
		if svg == nil {
			text.WriteString(p.Text)
			n += len([]rune(p.Text))
			continue
		}
		flush()
		drop := emojiDrop * line
		sp = append(sp, ui.Span{Text: "￼", ObjectWidth: size * (emojiImage + 2*emojiMargin), ObjectHeight: size*emojiImage - drop, ObjectDescent: drop})
		if objs == nil {
			objs = map[int]*ui.SVG{}
		}
		objs[n] = svg
		n++
	}
	flush()
	if len(sp) == 0 {
		sp = []ui.Span{{Text: s}}
	}
	return sp, objs
}

// drawEmoji 在内嵌对象的框里画表情图：图 1.2em 见方，左右各留 0.075em
func drawEmoji(objs map[int]*ui.SVG, size float32) func(p *ui.Painter, at int, r ui.Rect) {
	return func(p *ui.Painter, at int, r ui.Rect) {
		svg := objs[at]
		if svg == nil {
			return
		}
		d := size * emojiImage
		p.Image(svg, ui.Rect{X: r.X + (r.W-d)/2, Y: r.Y + (r.H-d)/2, W: d, H: d}, ui.Contain)
	}
}
