package chatui

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// DigitSlotWidth 是一段会变的数字文字该占的固定宽：每个数字都按这款字体里最宽的那个数字量，其余字符照原样量。
// 正式界面的文渊圆体不支持等宽数字特性（Tabular 开了也不起作用，0 宽 9.20、7 宽 8.46，字号 14），
// 不留固定宽的话，数字每变一次宽度就变，旁边的图标跟着左右跳（主人 2026-11-15 看审批卡倒计时指出）。
// sp 给字体、字号、特性，Text 不用填。
func DigitSlotWidth(c *ui.Context, text string, sp ui.Span) float32 {
	widest, wd := '0', float32(0)
	for d := '0'; d <= '9'; d++ {
		s := sp
		s.Text = string(d)
		if w, _ := c.MeasureText(1e6, s); w > wd {
			widest, wd = d, w
		}
	}
	r := []rune(text)
	for i := range r {
		if r[i] >= '0' && r[i] <= '9' {
			r[i] = widest
		}
	}
	sp.Text = string(r)
	w, _ := c.MeasureText(1e6, sp)
	return float32(math.Ceil(float64(w)))
}
