package winshell

import "github.com/egoist/mygo/ui"

// Tip 给 e 挂悬停提示，代替 Element.Tooltip：框的样子照 mygo 自带的（反色小框、字号小一号、小阴影，在 e 上方居中），
// 多的是进出场：150 毫秒淡入加从下方上移 2，100 毫秒淡出（TipTransition，规格 A17）。
// 暖机（刚看过一个提示，400 毫秒内下一个直接出来）、悬停 600 毫秒才出、按下或 Esc 收起，都是 ui.TooltipBase 自带的。
// 读屏说明照 Tooltip 的规矩：e 还没有说明才用这句。
func (k *Kit) Tip(c *ui.Context, e ui.Element, s string) ui.Element {
	if s == "" {
		return e
	}
	e.Description(s)
	t := c.Theme()
	fill, text := t.Inverse, t.InverseText
	if fill == ui.Transparent {
		fill = t.Text
	}
	if text == ui.Transparent {
		text = t.Background
	}
	ui.TooltipBase(c, e, func(tip ui.Element) {
		tip.MaxWidth(t.Space(80)).Padding(t.Space(1.25), t.Space(2)).Radius(t.Space(1.25)).Background(fill).TextColor(text).FontSize(t.FontSize - 1)
		tip.Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, 0.2))
		tip.AttachTo(e, ui.AnchorTop, ui.AnchorBottom).Margin(0, 0, t.Space(1.5), 0)
		tip.Transition(TipTransition)
		tip.Children(func() { ui.Text(c, s) })
	})
	return e
}
