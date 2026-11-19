package chatui

import (
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// 分块消息里的图片（规格-引用 第十节）：和普通图片消息是同一套排法——albumLayout 按宽高比占位、albumGroup 画格子、
// 点一下走 OnViewImage（同一条消息的图可以左右切换）。气泡宽度也和普通消息一样贴合内容，不再一律拉满。

// split 把一块的附件分成"画成真图的"和"画成标签的"。
func (p Part) split() (imgs []segview.Segment, files []PartAtt) {
	for _, a := range p.Atts {
		if a.Image && a.Show {
			imgs = append(imgs, segview.Segment{Kind: "image", Position: a.Pos, Width: a.Width, Height: a.Height, Name: a.Name})
		} else {
			files = append(files, a)
		}
	}
	return imgs, files
}

// partsMediaMode 是分块消息用哪种排法：第一块有图时，图铺满气泡（块里的引用卡在图上面、自己缩进）；
// 有没有表情回应、注释都一样。后面几块里的图缩在内边距里。
func partsMediaMode(m *Msg) mediaMode {
	if imgs, _ := m.Parts[0].split(); len(imgs) > 0 {
		return mediaFlush
	}
	return mediaNone
}

// partsEndWithImage 报告消息最后是不是只剩图（后面没有文字和标签）：是的话时间叠在图上，不另起一行。
func partsEndWithImage(m *Msg) bool {
	if len(m.Parts) != 1 {
		return false
	}
	p := m.Parts[0]
	imgs, files := p.split()
	return len(imgs) > 0 && len(files) == 0 && strings.TrimSpace(p.Text) == ""
}

// partAlbum 画一块的图：铺满时直角、由气泡按自己的圆角裁；否则自带圆角（在气泡最上面时和气泡同心，其余用最小内层圆角）。
func (h Host) partAlbum(c *ui.Context, m *Msg, imgs []segview.Segment, l layout, flush, top bool) {
	if flush {
		h.albumGroup(c, m, imgs, mediaFlush, l.MaxW)
		return
	}
	t := h.T
	r := Corners{t.RInnerMin, t.RInnerMin, t.RInnerMin, t.RInnerMin}
	if top {
		r = QuoteCorners(t, l.Corners, false)
	}
	ui.Column(c.Key("pics")).Label("图片").Shrink(0).Radius(r[:]...).Clip().Children(func() {
		h.albumGroup(c, m, imgs, mediaFlush, l.TextW+2*(t.BubblePadH-t.BubblePadV))
	})
}

// partsMeasure 量分块消息的自然宽度和估高：每一块文字按真实排版量最长一行，引用块、标签、图各按自己画出来的宽度。
// width 是文字区宽度（气泡最大宽减两侧内边距），maxW 是气泡最大宽（铺满的相册按它排）；Fit 是内容宽（不含两边的 side），和普通消息的 blockMeasure 同一个口径。
type partsMetrics struct {
	Fit float32
	H   float32
}

func (h Host) partsMeasure(m *Msg, width, maxW float32) partsMetrics {
	t := h.T
	side := t.BubblePadH - t.BubblePadV
	var pm partsMetrics
	for i, p := range m.Parts {
		if i > 0 {
			pm.H += t.BubblePadV
		}
		var parts int // 这一块里已经量过几样东西：之间隔 S1
		add := func(fit, height float32) {
			pm.Fit = max(pm.Fit, fit)
			if parts > 0 {
				pm.H += t.S1
			}
			pm.H += height
			parts++
		}
		if q := p.Quote; q != nil {
			add(h.quoteFit(q, width, side), t.quoteHeight())
		}
		imgs, files := p.split()
		if len(imgs) > 0 {
			a := albumLayout(imgs, width+2*side)
			if i == 0 && partsMediaMode(m) == mediaFlush {
				a = albumLayout(imgs, maxW) // 铺满的相册按气泡最大宽排，宽度不参与收紧
				add(0, a.H)
			} else {
				add(a.W-2*side, a.H)
			}
		}
		if text := strings.TrimSpace(p.Text); text != "" {
			var w float32
			var lines int
			for _, para := range paragraphs(p.Text) {
				ls := shapeLines(para, h.Font, t.Body, 1.6, width)
				lines += len(ls)
				for _, ln := range ls {
					w = max(w, ln)
				}
			}
			add(w, float32(max(lines, 1))*t.BodyLine)
		}
		if len(files) > 0 {
			var sum float32
			for j, a := range files {
				if j > 0 {
					sum += t.S1
				}
				sum += h.attChipW(a)
			}
			add(min(sum, width), t.HSm)
		}
	}
	return pm
}

// attChipW 是附件小标签的宽：内距 + 图标 + 间隔 + 名字（最宽 attName）。
func (h Host) attChipW(a PartAtt) float32 {
	t := h.T
	name := float32(0)
	for _, w := range shapeLines(a.Name, h.Font, t.FS[2], 1.6, 1e6) {
		name = max(name, w)
	}
	return float32(math.Ceil(float64(t.S2*2 + t.FS[2] + t.S1 + min(name, attName))))
}

// quoteFit 是引用块的自然宽度（去掉气泡两侧的 side）：名字一行、片段最多三行里最长的一行，加上竖线和内距。
func (h Host) quoteFit(q *PartQuote, width, side float32) float32 {
	t := h.T
	if q.Gone {
		w := float32(0)
		for _, ln := range shapeLines("引用的消息已经看不到了", h.Font, t.FS[1], 1.6, 1e6) {
			w = max(w, ln)
		}
		return w
	}
	name := q.Name
	if name == "" {
		name = "对方"
	}
	overhead := side + quoteBarBase*t.TS + 2*t.S2 // 左右内距 + 竖线 + 竖线两侧的间隔（和 partQuote 的排法一致）
	avail := max(1, width+2*side-overhead)
	text := float32(0)
	for _, ln := range shapeLines(name, h.Font, t.FS[2], 1.6, 1e6) {
		text = max(text, ln)
	}
	for _, ln := range shapeLines(q.Snippet, h.Font, t.FS[2], 1.45, avail) {
		text = max(text, ln)
	}
	return min(width, float32(math.Ceil(float64(text)))+overhead-2*side)
}
