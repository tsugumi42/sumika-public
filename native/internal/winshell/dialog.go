package winshell

import (
	"time"

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

// dialogMorph 是一个对话框的展开收起（主人 2026-11-15“对话框”这批，照 Fluid MorphDialog）：
// 打开时从打开它的那颗按钮长出来（一秒内按下过的 Kit 按钮；没有就是从面板中间），关掉时缩回去。
// 关掉以后对话框还多画一小段（closing），这段里整块不收点击、焦点和按键，缩完才真的拿掉。
type dialogMorph struct {
	from    ui.Rect   // 打开它的按钮（窗口坐标）；W 为 0 时从面板中间长出来
	at      time.Time // 打开的时刻
	closing bool
	done    bool // 缩完了：下一帧不再画
}

// dialogFrom 是面板长出来的起点：按钮的框；没有按钮时是面板中间九成大小的一块（只是轻轻放大，不飞）。
func dialogFrom(from, r ui.Rect) ui.Rect {
	if from.W > 0 && from.H > 0 {
		return from
	}
	return ui.Rect{X: r.X + r.W*.05, Y: r.Y + r.H*.05, W: r.W * .9, H: r.H * .9}
}

func lerp(a, b, t float32) float32 { return a + (b-a)*t }

func (k *Kit) dialogShell(c *ui.Context, open *bool, locked bool, title, closeLabel string, width float32, body func()) {
	g, p := k.G, k.P
	if k.dialogs == nil {
		k.dialogs = map[*bool]*dialogMorph{}
	}
	st := k.dialogs[open]
	switch {
	case *open && (st == nil || st.done):
		st = &dialogMorph{at: c.Now()}
		k.dialogs[open] = st
	case *open:
		st.closing = false // 收到一半又打开：从当时的样子接着长回去
	case st == nil || st.done:
		delete(k.dialogs, open)
		return
	default:
		st.closing = true
	}
	// 起点：打开前一秒内按下、点到的那颗 Kit 按钮。对话框可能比按钮先建（同一帧里按钮还没记下），所以打开后 0.1 秒内还没起点就再取一次
	if st.from.W == 0 && !k.lastPressAt.IsZero() && k.lastPressAt.After(st.at.Add(-time.Second)) && c.Now().Sub(st.at) < 100*time.Millisecond {
		st.from, k.lastPressAt = k.lastPress, time.Time{}
	}
	show := true
	ui.DialogBase(c, &show, func(backdrop ui.Element, panel ui.Element) {
		panel.Width(width).Padding(g.SpacePanel).Gap(g.SpacePanel).Radius(g.RadiusPanel)
		k.FloatPanel(panel)
		// 两根通道：morph 是位置和露出来的大小（“柔和”弹簧，减少动态效果时直接到位），fade 是遮罩和面板的透明度（时长淡变，减少动态照常）
		seen := ui.Local(panel, "morph-seen", func() bool { return false })
		if !*seen {
			*seen = true
			panel.Jump("morph", 0)
			panel.Jump("fade", 0)
		}
		target, fadeFor, spring := float32(1), slowDur(k.T.D.Motion.MotionDialogIn), k.soft()
		if st.closing {
			target, fadeFor, spring = 0, slowDur(k.T.D.Motion.MotionDialogOut), k.firm() // 退场用“利落”：不回弹、快一点
		}
		reduce := c.Preferences().ReduceMotion
		v := panel.AnimateTo("morph", target, ui.Animation{Spring: spring})
		f := max(0, min(1, panel.AnimateTo("fade", target, ui.Animation{Duration: fadeFor, Ease: ui.EaseOut, NonMotion: true})))
		scrim, op := f, f
		if st.closing && !reduce {
			// 收起时跟着收的进度走：遮罩一路变亮，面板缩到快贴着按钮才淡掉。单用 160 毫秒的淡出，人还没缩回去就看不见了，像飘走
			scrim, op = max(0, min(1, v)), max(0, min(1, v*4))
			f = op
		}
		backdrop.Background(p.Ink.Alpha(ScrimAlpha * scrim))
		if op < 1 {
			panel.Opacity(op)
		}
		// 面板在布局里一直是全尺寸、在正中；露出来的那块从按钮的框走到面板的框：整块平移到那块的中心，四边按大小裁，里面的字不缩放
		if r := panel.Bounds(); v != 1 && r.W > 0 && !reduce {
			s := dialogFrom(st.from, r)
			w, h := lerp(s.W, r.W, v), lerp(s.H, r.H, v)
			cx, cy := lerp(s.X+s.W/2, r.X+r.W/2, v), lerp(s.Y+s.H/2, r.Y+r.H/2, v)
			ix, iy := max(0, (r.W-w)/2), max(0, (r.H-h)/2)
			radius := lerp(min(s.W, s.H)/2, g.RadiusPanel, max(0, min(v, 1)))
			panel.Translate(cx-(r.X+r.W/2), cy-(r.Y+r.H/2)).ClipInset(iy, ix, iy, ix, radius)
		}
		if st.closing {
			panel.Disabled(true).DimDisabled(false) // 缩回去的这段不收点击、焦点和按键（Esc、回车）
			if v <= .001 && f <= .001 {
				st.done = true
				c.After(time.Millisecond) // 要下一帧把它拿掉
			}
		}

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
	// DialogBase 收到 Esc 或点了遮罩：照常关，下一帧开始缩回去。锁住时（恢复码这种只显示一次的）不理，只有按钮关得掉
	if !show && *open && !locked {
		*open = false
	}
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
