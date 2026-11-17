package winshell

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/remotepick"
)

// 搜索下拉（异步候选）：设置页里“字不好打对”的项通用（所在地、以后的模型、会话）。
// 底座是 mygo 的 ComboboxBase（输入框、方向键上下、回车、鼠标点选、Esc 关闭都是它的），取候选的状态机在 remotepick。
// 这个文件只管画：当前值一行、搜索框、候选面板（正在查、没有匹配、失败文字加重试）。

// RemotePicker 是一个搜索下拉的描述。State 要先填好 Fetch、Post；每帧都用同一个 State。
type RemotePicker struct {
	Key         string
	State       *remotepick.State
	Value       string // 当前已选的规范结果；空 = 没设
	Empty       string // 没设时显示的字，比如“没有设置”
	Placeholder string // 搜索框里的提示，比如“搜索城市或地区”
	Busy        bool   // 保存中：不让再选、不让清空
	OnPick      func(remotepick.Candidate)
	OnClear     func() // nil = 不能清空
}

// 下拉里最多露几行候选，再多就滚动
const remoteRows = 6

// RemotePicker 画一个搜索下拉：上面一行是当前规范结果（带清空），下面是搜索框，候选在面板里。
func (k *Kit) RemotePicker(c *ui.Context, o RemotePicker) {
	g, p := k.G, k.P
	st := o.State
	now := c.Now()
	st.Edit(now, st.Text)
	if wait := st.Step(now); wait > 0 {
		c.After(wait + time.Millisecond)
	}
	ui.Column(c.Key(o.Key)).Width(g.ControlWide).Shrink(1).MinWidth(0).Gap(g.SpaceTight).Children(func() {
		// 当前值：选中后显示规范结果，不是用户打的原文
		ui.Row(c.Key(o.Key + "-value")).FillWidth().Gap(g.SpaceSmall).AlignItems(ui.Center).MinHeight(g.HitSmall).Children(func() {
			text, ink := o.Value, p.Ink
			if text == "" {
				text, ink = o.Empty, p.Muted
			}
			ui.Text(c, text).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(ink).SingleLine().Grow(1).MinWidth(0)
			if o.Value != "" && o.OnClear != nil {
				k.Button(c, o.Key+"-clear", "清空", false, o.Busy, o.OnClear)
			}
		})
		// 输入框自己就是外框（下拉面板跟它同宽、贴着它的左边）
		cb := ui.ComboboxBase(c.Key(o.Key+"-input"), &st.Text)
		cb.Input.Label(o.Placeholder).Placeholder(o.Placeholder).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).FillWidth().MinWidth(0).
			Height(g.RowHeight).Padding(0, g.FieldInset).Radius(g.RadiusControl).Background(p.Bubble).Disabled(o.Busy)
		k.surface(c, cb.Input, o.Key, "input", g.RadiusControl, g.FieldInset)
		st.Edit(c.Now(), st.Text)
		k.remotePopup(c, o, cb)
		if id, ok := cb.Chosen(); ok {
			for _, cand := range st.Items {
				if cand.ID == id {
					st.Reset() // 清掉搜索字；显示的是选中的规范结果
					if o.OnPick != nil && !o.Busy {
						o.OnPick(cand)
					}
					break
				}
			}
		}
	})
}

func (k *Kit) remotePopup(c *ui.Context, o RemotePicker, cb ui.ComboboxParts) {
	g, p := k.G, k.P
	st := o.State
	cb.Popup(func(panel ui.Element) {
		panel.Padding(g.SpaceTight).Gap(g.SpaceTight).Radius(g.RadiusControl).MaxHeight(g.HitSmall * remoteRows)
		k.FloatMenu(panel)
		k.surface(c, panel, o.Key+"-popup", "choice-popup", g.RadiusControl, g.SpaceTight)
		note := func(key, s string, ink ui.Color) {
			ui.Text(c.Key(o.Key+"-"+key), s).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(ink).Wrap().Padding(g.ControlInset)
		}
		for i, cand := range st.Items {
			item := cb.Item(cand.ID).Label(cand.Label).MinHeight(g.HitSmall).Padding(g.ControlInset).Gap(g.SpaceSmall).AlignItems(ui.Center).Radius(g.RadiusSegment).TextColor(p.Ink)
			if item.Highlighted() {
				item.Background(p.Selected)
			}
			item.Children(func() {
				ui.Text(c, cand.Label).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine().Grow(1).MinWidth(0)
				if cand.Detail != "" {
					ui.Text(c, cand.Detail).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Meta).SingleLine().Shrink(1)
				}
			})
			k.surface(c, item, o.Key+"-item-"+strconv.Itoa(i), "menu-item", g.RadiusSegment, g.ControlInset)
		}
		switch {
		case st.Err != "":
			note("error", st.Err, k.T.C.DangerText)
			k.Button(c, o.Key+"-retry", "重试", false, false, st.Retry)
		case st.Loading && len(st.Items) == 0:
			note("loading", "正在查…", p.Muted)
		case st.Empty():
			note("empty", "没有匹配的结果", p.Muted)
		}
	})
}
