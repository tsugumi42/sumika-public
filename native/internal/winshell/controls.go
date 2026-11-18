package winshell

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// 设置页、记忆页、运行页共用的标准控件（盘点-组件复用-2026-11-09）：页面不再各自拿旧库的。

// SearchField 是标准搜索框：左边放大镜，框的底和圆角在外层，输入本身透明。bind 可以是 nil。
func (k *Kit) SearchField(c *ui.Context, key string, value *string, hint string, bind *ui.Handle) ui.Element {
	g := k.G
	return k.searchShaped(c, key, value, hint, bind, g.RowHeight, g.FieldInset).FillWidth().Shrink(0)
}

// searchShaped 给顶行那种更高的搜索框用：样子一样，只是尺寸不同。单行，所以圆角是高度的一半；“凹下去”（Sunken）。
func (k *Kit) searchShaped(c *ui.Context, key string, value *string, hint string, bind *ui.Handle, height, inset float32) ui.Element {
	g, p := k.G, k.P
	radius := g.Pill(height)
	box := ui.Row(c.Key(key + "-box")).MinWidth(0).Height(height).Gap(g.SpaceSmall).AlignItems(ui.Center).Radius(radius)
	k.pillPadding(box, inset)
	k.SunkenField(box, box.FocusWithin())
	k.surface(c, box, key, "input", radius, inset).Children(func() {
		ui.Icon(c, IconSearch).Size(g.IconSmall, g.IconSmall).Shrink(0).TextColor(p.Meta)
		in := ui.TextInputBase(c.Key(key), value).Label(hint).Placeholder(hint).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).Grow(1).MinWidth(0).FocusRing(false)
		if bind != nil {
			in.Bind(bind)
		}
	})
	return box
}

// searchWait 是防抖搜索一份的状态。
type searchWait struct {
	seen string
	due  time.Time
}

// DebouncedSearch 是停手一会儿才报告的搜索框：*query 跟着打字变，*reported 停够 delay 才变。
func (k *Kit) DebouncedSearch(c *ui.Context, key string, query, reported *string, hint string, delay time.Duration) {
	k.SearchField(c, key, query, hint, nil)
	if k.waits == nil {
		k.waits = map[string]*searchWait{}
	}
	w := k.waits[key]
	if w == nil {
		w = &searchWait{seen: *query}
		k.waits[key] = w
	}
	now := c.Now()
	if *query != w.seen {
		w.seen, w.due = *query, now.Add(delay)
	}
	if *reported == *query || w.due.IsZero() {
		return
	}
	if left := w.due.Sub(now); left > 0 {
		c.After(left)
		return
	}
	*reported = *query
}

// NumberText 把输入过滤成数字：去掉非数字；空或（不允许 0 时的）0 变成空；超过上限不收（ok=false，退回上一次的）。
func NumberText(s string, limit int, allowZero bool) (string, bool) {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	next := b.String()
	if next == "" {
		return "", true
	}
	n, err := strconv.ParseInt(next, 10, 32)
	if err != nil {
		return "", false
	}
	if n == 0 && !allowZero {
		return "", true
	}
	if limit > 0 && n > int64(limit) {
		return "", false
	}
	return next, true
}

// NumberField 是只收数字的输入框。limit 为 0 不限上限；回车调 onSubmit。
func (k *Kit) NumberField(c *ui.Context, key string, value *string, label string, limit int, allowZero bool, onSubmit func()) ui.Element {
	e := k.TextField(c, key, value, label).FillWidth().AutoFocus()
	accepted := ui.Local(e, "accepted", func() string { return *value })
	e.OnChange(func() {
		next, ok := NumberText(*value, limit, allowZero)
		if !ok {
			next = *accepted
		}
		*value, *accepted = next, next
	})
	if onSubmit != nil {
		e.OnSubmit(onSubmit)
	}
	return e
}

// TextArea 是多行输入框：多行所以不是胶囊，用控件圆角；和 TextField 同样“凹下去”（Sunken），随内容在 minLines 到 maxLines 之间长。
func (k *Kit) TextArea(c *ui.Context, key string, value *string, label string, minLines, maxLines int) ui.Element {
	g := k.G
	e := ui.TextAreaBase(c.Key(key), value).Label(label).Lines(minLines, maxLines).FillWidth().Padding(g.FieldInset).Radius(g.RadiusControl).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).FocusRing(false)
	k.SunkenField(e, e.Focused())
	return k.surface(c, e, key, "input", g.RadiusControl, g.FieldInset)
}

// EmptyState 是“这里没东西”：标题、一句原因，可以带一个按钮（action 为空不画）。返回按钮这一帧有没有被点。
func (k *Kit) EmptyState(c *ui.Context, key, title, reason, action string) (clicked bool) {
	g, p := k.G, k.P
	ui.Column(c.Key(key)).FillWidth().AlignItems(ui.Center).Gap(g.SpaceSmall).Padding(g.SpacePanel).Children(func() {
		ui.Text(c, title).FontSize(g.BodySize).FontWeight(g.BoldWeight).FixedLineHeight(g.BodyLine).TextColor(p.Ink)
		if reason != "" {
			ui.Text(c, reason).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted).Wrap()
		}
		if action != "" {
			clicked = k.Button(c, key+"-action", action, false, false, func() {}).Clicked()
		}
	})
	return clicked
}

// ErrorState 是“出错了”：和 EmptyState 同一个排法，原因用危险色的文字，通常带重试。
func (k *Kit) ErrorState(c *ui.Context, key, title, reason, retry string) (clicked bool) {
	g, p := k.G, k.P
	ui.Column(c.Key(key)).FillWidth().AlignItems(ui.Center).Gap(g.SpaceSmall).Padding(g.SpacePanel).Children(func() {
		ui.Text(c, title).FontSize(g.BodySize).FontWeight(g.BoldWeight).FixedLineHeight(g.BodyLine).TextColor(p.Ink)
		if reason != "" {
			ui.Text(c, reason).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(k.T.C.DangerText).Wrap().Selectable()
		}
		if retry != "" {
			clicked = k.Button(c, key+"-retry", retry, false, false, func() {}).Clicked()
		}
	})
	return clicked
}

// BadgeTone 是徽标的语气。
type BadgeTone int

const (
	BadgeNeutral BadgeTone = iota
	BadgeGood
	BadgeWarn
)

// Badge 是一小块状态字：整颗圆角、四边内距相等；语气只改字的颜色，底都是凹块。
func (k *Kit) Badge(c *ui.Context, key, text string, tone BadgeTone) {
	g, p := k.G, k.P
	ink := p.Muted
	switch tone {
	case BadgeGood:
		ink = p.Accent
	case BadgeWarn:
		ink = k.T.C.DangerText
	}
	h := g.MetaLine + 2*g.SpaceTight
	b := ui.Box(c.Key(key)).Shrink(0).Height(h).Padding(g.SpaceTight).Radius(h / 2).Background(p.Track).Center()
	k.surface(c, b, key, "badge", h/2, g.SpaceTight).Children(func() {
		ui.Text(c, text).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(ink).SingleLine()
	})
}

// KeyValue 是 KeyValues 的一行。
type KeyValue struct{ Key, Value string }

// KeyValues 是详情里的“名字 + 值”列表：名字一栏定宽、弱一档，值能换行、能选中复制。
func (k *Kit) KeyValues(c *ui.Context, key string, items []KeyValue) {
	g, p := k.G, k.P
	ui.Column(c.Key(key)).FillWidth().Gap(g.SpaceSmall).Children(func() {
		for _, it := range items {
			ui.Row(c.Key(it.Key)).FillWidth().Gap(g.SpaceSmall).Children(func() {
				ui.Text(c, it.Key).Width(g.ControlWide / 2).Shrink(0).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted)
				ui.Text(c, it.Value).Grow(1).MinWidth(0).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Ink).Wrap().Selectable()
			})
		}
	})
}

// Note 是设置里的一行说明：和行的字左边对齐，弱一档；failure 时用危险色的文字。
func (k *Kit) Note(c *ui.Context, text string, failure bool) {
	g, p := k.G, k.P
	ink := p.Muted
	if failure {
		ink = k.T.C.DangerText
	}
	ui.Text(c, text).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(ink).Wrap().Margin(g.SpaceTight, 0)
}

// Break 是设置里两组之间的分隔：一条线，可以带一行说明。
func (k *Kit) Break(c *ui.Context, text string) {
	g, p := k.G, k.P
	ui.Box(c).FillWidth().Height(1).Shrink(0).Margin(g.SpaceSmall, 0).Background(p.Ink.Alpha(.1))
	if text != "" {
		k.Note(c, text, false)
	}
}
