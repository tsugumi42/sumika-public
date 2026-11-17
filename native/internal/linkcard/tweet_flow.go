package linkcard

// 不封顶的长推文（linkReader.expand.tweetCard.maxHeight 设成 0，用户 2026-11-13）按“长截图”的样子排：
// 段落之间留半行的空隙（不是一整行空行），长文里的小标题、配图、分隔线按原来的顺序夹在段落中间，
// 引用框、时间、互动排仍照 X 网页。封顶时照旧（正文一整块、图在文字下面）。

import (
	"regexp"
	"strings"
)

// FlowKind 是长文里一块内容的种类
type FlowKind string

const (
	FlowPara    FlowKind = "para"    // 正文的一段
	FlowHeading FlowKind = "heading" // 小标题（X 文章的标题）
	FlowImage   FlowKind = "image"   // 配图（视频、动图画缩略图加播放钮）
	FlowRule    FlowKind = "rule"    // 分隔线
)

// FlowBlock 是按顺序排的一块内容。Tweet.Flow 非空且不封顶时，用它代替 Text 加 Media。
type FlowBlock struct {
	Kind  FlowKind
	Text  string
	Media TweetMedia
}

const (
	paraGap     float32 = 12 // 段落之间（半行）
	headingSize float32 = 24
	headingLine float32 = 32
	headingMT   float32 = 20 // 小标题前后留得多一点
	headingMB   float32 = 8
	flowMediaMY float32 = 12
	flowRuleMY  float32 = 16
	// flowImageMax 是长文里的图最高排到宽的多少倍：竖长的图不整张铺开，免得一张图占掉大半张卡
	flowImageMax float32 = 1.5
)

var paraSplit = regexp.MustCompile(`\n[ \t]*\n+`)

// flowBlocks 是不封顶时正文区按顺序要画的内容：有 Flow 就用它（文字块再按空行拆成段），没有就把 Text 按空行拆成段。
func (t Tweet) flowBlocks() []FlowBlock {
	var out []FlowBlock
	addText := func(kind FlowKind, s string) {
		for _, p := range paraSplit.Split(strings.TrimSpace(s), -1) {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, FlowBlock{Kind: kind, Text: p})
			}
		}
	}
	if len(t.Flow) == 0 {
		addText(FlowPara, t.Text)
		return out
	}
	for _, b := range t.Flow {
		switch b.Kind {
		case FlowPara:
			addText(FlowPara, b.Text)
		case FlowHeading:
			if s := strings.TrimSpace(b.Text); s != "" {
				out = append(out, FlowBlock{Kind: FlowHeading, Text: s})
			}
		default:
			out = append(out, b)
		}
	}
	return out
}

// flowBox 是排好的一块：box 是它的位置，lines 是文字块画几行，m 是图块的内容。
type flowBox struct {
	kind  FlowKind
	text  string
	box   box
	lines int
	m     TweetMedia
}

// flowMargins 是一块内容前后留的空：相邻两块之间取两个里大的那个（外边距折叠）。
func flowMargins(k FlowKind) (before, after float32) {
	switch k {
	case FlowHeading:
		return headingMT, headingMB
	case FlowImage:
		return flowMediaMY, flowMediaMY
	case FlowRule:
		return flowRuleMY, flowRuleMY
	}
	return paraGap, paraGap
}

// layoutFlow 把长文的各块从 y 起往下排，返回排好的块和结束的 y。
func layoutFlow(blocks []FlowBlock, m measurer, left, cw, y float32) ([]flowBox, float32) {
	var out []flowBox
	var prevAfter float32
	for i, b := range blocks {
		before, after := flowMargins(b.Kind)
		if i > 0 {
			y += max(before, prevAfter)
		}
		fb := flowBox{kind: b.Kind, text: b.Text, m: b.Media}
		switch b.Kind {
		case FlowPara:
			n, h, _ := m.block(b.Text, bodySize, bodyLine, 400, cw, 0)
			fb.lines, fb.box = n, box{left, y, cw, h}
		case FlowHeading:
			n, h, _ := m.block(b.Text, headingSize, headingLine, 700, cw, 0)
			fb.lines, fb.box = n, box{left, y, cw, h}
		case FlowImage:
			h := (cw - 2*cardBorder) * min(max(mediaRatio(b.Media), 0.3), flowImageMax)
			fb.box = box{left, y, cw, h + 2*cardBorder}
		case FlowRule:
			fb.box = box{left, y, cw, cardBorder}
		}
		out = append(out, fb)
		y += fb.box.H
		prevAfter = after
	}
	return out, y
}
