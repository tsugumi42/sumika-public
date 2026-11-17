package winshell

import (
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/chatui"
)

// IconCheck 是勾选框里的对勾（MingCute）。
var IconCheck = chatui.Icon("check", false)

// DialogAction 是对话框底部的一个按钮。Default 是主按钮，也接回车；Left 排到左边；点了默认关窗，KeepOpen 不关（等校验）。
type DialogAction struct {
	ID, Label                         string
	Default, Disabled, Left, KeepOpen bool
}

// DialogSpec 是带按钮行的对话框。Wide 用宽版；BodyHeight 大于 0 时正文定高、不带内距，滚动归内容自己（列表那种）。
// Locked 锁住：右上角关闭钮不画，点遮罩、按 Esc 也关不掉，只能点按钮（恢复码这种只显示一次的用）。
type DialogSpec struct {
	Title      string
	Wide       bool
	BodyHeight float32
	Locked     bool
	Actions    []DialogAction
	OnAction   func(id string)
}

// dialogWidth、dialogWideWidth 是对话框的宽度，窗口更窄时跟着窗口收。
const (
	dialogWidth     = 420
	dialogWideWidth = 600
)

// Dialog 是居中的对话框（设计语言第二节第 4 层浮层）。原型没有对话框，照它的弹出面板（choice-popup）推：
// 面板用 float 底色加一圈细边，圆角 = 里面控件的圆角 + 面板内距（和控件同心，原型 radiusPanel），内距 spacePanel；
// 背后是 20% 的内容色遮罩；点遮罩、按 Esc 都关（DialogBase 自带），右上角有关闭钮。
// 内容归调用方；对话框只管外框和标题行。
func (k *Kit) Dialog(c *ui.Context, open *bool, title, closeLabel string, body func()) {
	k.dialogShell(c, open, false, title, closeLabel, dialogWidth, body)
}

// ActionDialog 是带按钮行的对话框：外框、标题和 Dialog 同一份，正文放不下时滚动，按钮行固定在底部。
func (k *Kit) ActionDialog(c *ui.Context, key string, open *bool, sp DialogSpec, body func()) {
	g := k.G
	width := float32(dialogWidth)
	if sp.Wide {
		width = dialogWideWidth
	}
	vw, vh := c.Size()
	width = min(width, vw-2*g.SpacePanel)
	maxBody := max(vh-6*g.RowHeight, g.RowHeight)
	k.dialogShell(c.Key(key), open, sp.Locked, sp.Title, "关闭", width, func() {
		if sp.BodyHeight > 0 {
			ui.Column(c.Key("dialog-body")).FillWidth().Height(min(sp.BodyHeight, maxBody)).Shrink(0).Gap(g.SpaceSmall).Children(body)
		} else {
			ui.Scroll(c.Key("dialog-body")).FillWidth().MaxHeight(maxBody).Gap(g.SpaceSmall).Children(body)
		}
		if len(sp.Actions) == 0 {
			return
		}
		fire := func(a DialogAction) {
			if !a.KeepOpen {
				*open = false
			}
			if sp.OnAction != nil {
				sp.OnAction(a.ID)
			}
		}
		foot := ui.Row(c.Key("dialog-actions")).FillWidth().Shrink(0).Gap(g.SpaceSmall).AlignItems(ui.Center)
		foot.Children(func() {
			for _, a := range sp.Actions {
				if a.Left {
					k.Button(c, "action-"+a.ID, a.Label, a.Default, a.Disabled, func() { fire(a) })
				}
			}
			ui.Box(c).Grow(1)
			// 主按钮在最右：写在前面的排在右边，和旧对话框的顺序一样
			for i := len(sp.Actions) - 1; i >= 0; i-- {
				if a := sp.Actions[i]; !a.Left {
					k.Button(c, "action-"+a.ID, a.Label, a.Default, a.Disabled, func() { fire(a) })
				}
			}
		})
		for _, a := range sp.Actions {
			if a.Default && !a.Disabled && foot.Shortcut(0, ui.KeyEnter) {
				fire(a)
				break
			}
		}
	})
}

func (k *Kit) dialogShell(c *ui.Context, open *bool, locked bool, title, closeLabel string, width float32, body func()) {
	g, p := k.G, k.P
	if !*open {
		k.forgetPop(open)
	}
	// DialogBase 点遮罩、按 Esc 就写 *open=false；锁住时给它一份拷贝，这两下只改拷贝，
	// 下一帧仍按调用方真正的开关画出来，只有按钮（写 *open）关得掉。
	dismiss := open
	if locked {
		v := *open
		dismiss = &v
	}
	ui.DialogBase(c, dismiss, func(backdrop ui.Element, panel ui.Element) {
		// 遮罩（连同里面的面板）220 进 / 160 出，退场拷贝保留最后的内容、不收点击和 Esc；面板另加上移 10、缩放 .985
		backdrop.Background(p.Ink.Alpha(ScrimAlpha)).Transition(DialogFade)
		panel.Width(width).Padding(g.SpacePanel).Gap(g.SpacePanel).Radius(g.RadiusPanel)
		k.FloatPanel(panel)
		k.popIn(panel, open, float32(k.T.D.Num.TravelDialog), float32(k.T.D.Num.ScaleDialog))

		k.surface(c, panel, "dialog", "dialog", g.RadiusPanel, g.SpacePanel)
		panel.Children(func() {
			ui.Row(c.Key("dialog-title")).FillWidth().AlignItems(ui.Center).Gap(g.SpaceSmall).Children(func() {
				ui.Text(c, title).Grow(1).FontSize(g.BodySize).FontWeight(g.BoldWeight).FixedLineHeight(g.BodyLine).SingleLine()
				if !locked {
					k.IconButton(c, "dialog-close", closeLabel, IconClose, func() { *open = false })
				}
			})
			body()
		})
	})
}

// Check 是一行勾选：左边方框（圆角取分段选中块那一档），右边文字，整行都是点击区。
func (k *Kit) Check(c *ui.Context, key, label string, v *bool, disabled bool) {
	k.checkLook(c, key, label, v, disabled)
}

// CheckValue 是值由外面管的勾选（要提交到后台的）：点了调 onChange，值等后台确认后再变。
func (k *Kit) CheckValue(c *ui.Context, key, label string, value, disabled bool, onChange func(bool)) {
	v := value
	e := k.checkLook(c, key, label, &v, disabled)
	if !disabled {
		e.OnClick(func() { onChange(v) })
	}
}

func (k *Kit) checkLook(c *ui.Context, key, label string, v *bool, disabled bool) ui.Element {
	g, p := k.G, k.P
	e := ui.CheckboxBase(c.Key(key), v).Label(label).FillWidth().MinHeight(g.HitSmall).Padding(0, g.ControlInset).Gap(g.SpaceSmall).AlignItems(ui.Center).Radius(g.RadiusSegment).Disabled(disabled)
	bg := ui.Transparent
	switch {
	case e.Pressed():
		bg = p.Press
	case e.Hovered():
		bg = p.Hover
	}
	e.Background(e.AnimateColor("fill", bg, ControlFade))
	if disabled {
		e.Opacity(DisabledAlpha)
	}
	box := g.IconSize
	on := *v
	e.Children(func() {
		// 只有勾上、取消才动：底色 140 毫秒淡变，对勾淡入；进页面时已勾的，通道第一次出现就是终点，静止
		b := ui.Box(c.Key("box")).Size(box, box).Shrink(0).Center().Radius(g.RadiusSegment)
		fillTo, edgeTo, tickTo := ui.Transparent, p.Meta, float32(0)
		if on {
			fillTo, edgeTo, tickTo = p.Accent, p.Accent, 1
		}
		tick := b.AnimateTo("tick", tickTo, ControlFade)
		b.Background(b.AnimateColor("fill", fillTo, ControlFade)).Border(1.5, b.AnimateColor("edge", edgeTo, ControlFade)).TextColor(p.OnAccent)
		if tick > 0 {
			b.Children(func() { ui.Icon(c, IconCheck).Size(g.IconSmall, g.IconSmall).Opacity(tick) })
		}
		ui.Text(c, label).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine().Grow(1).MinWidth(0)
	})
	k.surface(c, e, key, "check", g.RadiusSegment, g.ControlInset)
	return e
}
