package winshell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// IconTab 是图标页签里的一个：图标加名字。
type IconTab struct {
	Label string
	Icon  *ui.SVG
}

// IconTabs 是会伸缩的图标页签：一排图标，选中的那个展开成“图标 + 文字”的强调色浅底胶囊，没选中的是只有图标的圆钮。
// 换选中项时新的展开、旧的收回，两边的宽度变化同时进行，其余的跟着挪位；文字随宽度淡入淡出——
// 宽度装不下整行字之前文字是透明的，不会出现被裁掉一半还清晰可见的字。
// 键盘左右键切换、读屏名是页签名（ui.SegmentedBase 给的单选组行为），没选中的悬停显示名字。返回选中项是否变了。
//
// 动效：每个页签一个“展开程度”通道（0 圆钮、1 胶囊），用不回弹的“利落”弹簧（宽度变化永远不过冲）；宽 = 圆钮宽到自然宽的插值，
// 自然宽用 MeasureText 算（不靠上一帧量）；底色、字色、文字透明度都从同一个展开程度推出，所以和宽度同步。减少动态效果时直接到位。
// 整排占的宽度固定为“任何一个选中时最宽的那一种”，所以展开收回不会把旁边的东西挤动。
func (k *Kit) IconTabs(c *ui.Context, key string, tabs []IconTab, value *int) (changed bool) {
	g, p := k.G, k.P
	before := *value
	h := g.HitSmall
	icon := g.IconSize
	padL := (h - icon) / 2              // 圆钮里图标居中的左边距；展开后图标原地不动
	gap := g.SpaceSmall                 // 图标和字之间
	x0 := padL + icon + gap             // 字从这里开始
	textW := make([]float32, len(tabs)) // 每个名字的宽
	full := make([]float32, len(tabs))  // 展开后的自然宽
	maxRow := float32(0)
	for i, t := range tabs {
		w, _ := c.MeasureText(1e6, ui.Span{Text: t.Label, Size: g.LabelSize, Font: c.Theme().Font})
		textW[i] = w
		full[i] = x0 + w + padL + g.SpaceSmall
	}
	for sel := range tabs {
		row := float32(len(tabs)-1) * g.SpaceTight
		for i := range tabs {
			if i == sel {
				row += full[i]
			} else {
				row += h
			}
		}
		maxRow = max(maxRow, row)
	}
	parts := ui.SegmentedBase(c.Key(key), value, len(tabs))
	parts.Track.Width(maxRow).Shrink(0).Gap(g.SpaceTight).AlignItems(ui.Center).Justify(ui.Start)
	k.surface(c, parts.Track, key, "icon-tabs", h/2, 0)
	parts.Track.Children(func() {
		for i, t := range tabs {
			selected := i == *value
			b := parts.Segment(i).Label(t.Label).Height(h).Shrink(0).Radius(h/2).Clip().Justify(ui.Start).AlignItems(ui.Center).Padding(0, 0, 0, padL).Gap(gap)
			open := float32(0)
			if selected {
				open = 1
			}
			// 展开程度：0 是圆钮，1 是胶囊；不回弹，所以宽度不过冲
			t01 := min(max(b.Spring("open", open, k.firm()), 0), 1)
			w := h + t01*(full[i]-h)
			b.Width(w)
			// 底：选中的是强调色浅底（随展开程度淡入）；没选中的悬停、按下深一层
			if t01 > 0 {
				b.Background(k.selectFill(selMode).Alpha(t01))
			} else if b.Pressed() {
				b.Background(p.Press)
			} else if b.Hovered() {
				b.Background(p.Hover)
			}
			b.TextColor(p.Muted.Mix(p.Ink, t01))
			if !selected {
				k.Tip(c, b, t.Label) // 圆钮没有字：悬停说名字
			}
			// 字的透明度：字露出来的比例（宽度超过字起点的那一段 ÷ 字宽）的平方，只露出一半时已经几乎看不见
			reveal := min(max((w-x0)/max(textW[i], 1), 0), 1)
			b.Children(func() {
				ui.Icon(c, t.Icon).Size(icon, icon).Shrink(0)
				if reveal > 0 {
					ui.Text(c, t.Label).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine().Shrink(0).Opacity(reveal * reveal)
				}
			})
			k.surface(c, b, fmt.Sprint(key, "-", i), "icon-tab", h/2, padL)
		}
	})
	return *value != before
}
