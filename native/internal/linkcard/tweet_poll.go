package linkcard

// 推特卡片上的投票（复核 #39）：每个选项一条横条，条里是选项名和百分比，领先的选项加粗、条用蓝色；
// 条下面一行小字写总票数和是否结束。版式照 X 网页的投票卡（react-tweet 不含投票，数值是照网页量的近似）。

import (
	"fmt"
	"math"
	"strings"
)

// PollChoice 是投票的一个选项；OK 为假说明取数没给票数比例，只画选项名（不画成 0%）
type PollChoice struct {
	Label   string
	Percent float64
	OK      bool
}

// TweetPoll 是卡片上的投票；Footer 是条下面那行小字（共 N 票 · 已结束），没有就空
type TweetPoll struct {
	Choices []PollChoice
	Footer  string
}

const (
	pollMT     float32 = 12
	pollRowH   float32 = 36
	pollRowGap float32 = 8
	pollPadX   float32 = 12
	pollFooter float32 = 20
	pollFootMT float32 = 8
	pollSize   float32 = 15
	pollRadius float32 = 6
)

type pollRow struct {
	box        box
	fill       float32 // 填充条的宽（0～box.W）
	label, pct string
	lead       bool
}

// layoutPoll 把投票从 y 起往下排，返回结束的 y。
func (l *tweetLayout) layoutPoll(p *TweetPoll, m measurer, left, cw, y float32) float32 {
	if p == nil || len(p.Choices) == 0 {
		return y
	}
	y += pollMT
	top := -1.0
	for _, c := range p.Choices {
		if c.OK && c.Percent > top {
			top = c.Percent
		}
	}
	y0 := y
	for i, c := range p.Choices {
		r := pollRow{box: box{left, y, cw, pollRowH}, label: strings.TrimSpace(c.Label)}
		if c.OK {
			r.pct = fmtPercent(c.Percent)
			r.fill = float32(math.Min(math.Max(c.Percent, 0), 100)) / 100 * cw
			r.lead = c.Percent == top && top > 0
		}
		l.pollRows = append(l.pollRows, r)
		y += pollRowH
		if i < len(p.Choices)-1 {
			y += pollRowGap
		}
	}
	if p.Footer != "" {
		y += pollFootMT
		l.pollFoot = box{left, y, cw, pollFooter}
		l.pollFootText = p.Footer
		y += pollFooter
	}
	l.poll = box{left, y0, cw, y - y0}
	return y
}

func fmtPercent(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f%%", v)
	}
	return fmt.Sprintf("%.1f%%", v)
}
