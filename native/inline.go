package main

// 行内块里的小黄脸（规格-原生界面第二版 3.1、3.4，ADR-188）：
//   - 一个行内块交给 chat.RetainedBody，脸是其中的行内框（Span.ObjectWidth）；
//   - 同一份 TextLayout 同时供正文绘制、命中和对象框使用，不再另排一份正文；
//   - BodySelection 的 CopyData 把 U+FFFC 换回脸的复制文字，时间尾槽不参与复制。

import (
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"sumika-native/internal/segview"
)

const (
	faceEm   = 1.375 // 脸的边长 = 字号 × 1.375（网页 media.css:178 的 1.375em）
	faceDrop = 0.3   // 网页 vertical-align: -0.3em：脸往基线下面沉 0.3em
)

// faceSize 脸画出来的边长（DIP）
func faceSize(fontSize float32) float32 { return fontSize * faceEm }

// faceBox 行内框的宽高。高度比画出来的脸矮 faceDrop 个字：框坐在基线上，脸的下沿画到基线下面，
// 和网页的 vertical-align 一样，含脸的行就不会被撑高（规格 3.1 第 133 行：框 19 DIP 高时含脸的行多 8.3 DIP）
func faceBox(fontSize float32) (w, h float32) {
	return faceSize(fontSize), fontSize * (faceEm - faceDrop)
}

// ───────────── 行内块 → span ─────────────

type inlineDoc struct {
	spans  []ui.Span
	imgs   []string // 行内框里要画图的脸的 id，按框的先后
	copies []string // 和 imgs 一一对应：复制出来的字
}

// inlineOf 行内块 → 富文本的 span。脸按 resolveFace 分：图是行内框，字符和文字排进字里。
// state 用于确认缺图时将对象换成文字。
func inlineOf(b segview.Block, fontSize float32, table map[string]faceInfo, state func(id string) int) inlineDoc {
	var d inlineDoc
	add := func(text string, bold bool) {
		if text == "" {
			return
		}
		if n := len(d.spans); n > 0 && d.spans[n-1].ObjectWidth == 0 && (d.spans[n-1].Weight == 600) == bold {
			d.spans[n-1].Text += text // 连着的同样式文字并成一段
			return
		}
		sp := ui.Span{Text: text}
		if bold {
			sp.Weight = 600
		}
		d.spans = append(d.spans, sp)
	}
	bw, bh := faceBox(fontSize)
	for _, r := range b.Runs {
		if r.Face == nil {
			add(r.Text, r.Bold)
			continue
		}
		res := resolveFace(*r.Face, table, state)
		if res.Kind != faceAsImage {
			add(res.Text, r.Bold)
			continue
		}
		d.spans = append(d.spans, ui.Span{Text: "￼", ObjectWidth: bw, ObjectHeight: bh})
		d.imgs = append(d.imgs, r.Face.ID)
		d.copies = append(d.copies, res.Copy)
	}
	return d
}

// layoutEnv 只描述当前正文的字体与宽度，不持有跨线程预排状态。
type layoutEnv struct {
	size, bodyW float32
	font        ui.Font
}

// envFor 按当前主题和列表几何取得正文条件。
func (a *app) envFor(c *ui.Context) layoutEnv {
	t := theme.T(c)
	m := t.Metrics().Message()
	width := a.list.Bounds(c).W
	if layer := a.forward.rendering; layer != nil {
		width = layer.list.Bounds(c).W
	}
	if width == 0 {
		width, _ = c.Size()
	}
	size := t.Typography().Message().Size() * c.Preferences().TextScale
	font := ui.Font{Family: t.Typography().Font(), Size: size, Weight: t.Typography().Message().Weight()}
	// 列表首帧尚未量出时使用主题上限；不能把嵌套内容区再扣一次消息边距。
	outer := m.MaxWidth()
	if width > 0 {
		outer = min(outer, width-m.MarginLeft()-m.MarginRight())
	}
	bodyW := max(1, outer-m.PaddingLeft()-m.PaddingRight())
	return layoutEnv{size: size, font: font, bodyW: bodyW}
}

// drawCheck 测试用：不为 nil 时，Draw 把这一次用的 span 和框位置交给它核对
var drawCheck func(rc ui.Rect, spans []ui.Span, objs []ui.TextObject)

// faceAt 将 DrawObjects 的 rune 下标映射到图片序号；尾部时间槽不属于脸。
func (d inlineDoc) faceAt(at int) (int, bool) {
	offset, index := 0, 0
	for _, span := range d.spans {
		count := utf8.RuneCountInString(span.Text)
		if span.ObjectWidth > 0 {
			if at >= offset && at < offset+count {
				i := index + at - offset
				return i, i < len(d.imgs)
			}
			index += count
		}
		offset += count
	}
	return 0, false
}

// 图没到时保留框；绘制阶段只读图片和真实对象位置。
func (a *app) faceObjectPainter(doc inlineDoc, size float32) func(*ui.Painter, int, ui.Rect) {
	f := a.fs()
	side := faceSize(size)
	return func(p *ui.Painter, at int, r ui.Rect) {
		if i, ok := doc.faceAt(at); ok {
			if bitmap := f.peek(doc.imgs[i]); bitmap != nil {
				p.Image(bitmap, ui.Rect{X: r.X, Y: r.Y, W: side, H: side}, ui.Contain)
			}
		}
	}
}

// ───────────── 复制还原（规格 3.4 的做法 E）─────────────

// copyMap 一个气泡里每个带脸的富文本段落（按元素 id）要还原的复制文字。气泡每次构建都新建
type copyMap struct{ faces map[uint64][]string }

func (m *copyMap) add(id uint64, copies []string) {
	if m.faces == nil {
		m.faces = map[uint64][]string{}
	}
	m.faces[id] = copies
}

// restore 是 CopyData 的回调：每段选中的字里，第 n 个 U+FFFC 换成这个段落第 n 个脸的复制文字；
// 同段的双向文字碎片直接连接；不同段之间空一行。
func (m *copyMap) restore(parts []ui.SelectedText) transfer.Data {
	var out strings.Builder
	for i, p := range parts {
		if i > 0 && parts[i-1].ID != p.ID {
			out.WriteString("\n\n")
		}
		if copies, ok := m.faces[p.ID]; ok && len(copies) > 0 {
			out.WriteString(restoreFaces(p.Text, p.Start, p.End, copies))
		} else {
			rs := []rune(p.Text)
			out.WriteString(segview.CleanText(string(rs[max(0, p.Start):min(len(rs), p.End)])))
		}
	}
	return transfer.TextData(out.String())
}

// restoreFaces text 是整个段落，[start, end) 是选中的字（按字）。选区前面的脸要先数过去，
// 才知道选区里的第一个 U+FFFC 是第几个脸；copies 不够用时写 [表情]，不 panic
func restoreFaces(text string, start, end int, copies []string) string {
	rs := []rune(text)
	start = max(0, min(start, len(rs)))
	end = max(start, min(end, len(rs)))
	n := 0
	for _, r := range rs[:start] {
		if r == '￼' {
			n++
		}
	}
	var sb strings.Builder
	for _, r := range rs[start:end] {
		if r != '￼' {
			sb.WriteRune(r)
			continue
		}
		if n < len(copies) {
			sb.WriteString(copies[n])
		} else {
			sb.WriteString("[表情]")
		}
		n++
	}
	return sb.String()
}
