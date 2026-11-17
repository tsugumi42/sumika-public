package overlay

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type BoxState struct{ Open bool }
type BoxAction struct {
	ID, Label                                 string
	Danger, Disabled, Default, Left, KeepOpen bool
}
type BoxOptions struct {
	Wide, CloseButton, Locked   bool
	AdditionalTitle, CloseLabel string
	Actions                     []BoxAction
	OnAction                    func(id string)
	OnClose                     func()
	// FullBody supplies an unpadded fixed body; its content owns any scrolling.
	FullBody   bool
	BodyHeight float32
}
type boxLocal struct{ panel, scrim visibility }
type boxKey struct{ owner uint64 }

// Box supplies the title, scrolling body and action row. Body content inherits
// the dialog font, text color and line height. Locked suppresses backdrop and
// Escape dismissal. Actions close unless KeepOpen is set.
func Box(c *ui.Context, state *BoxState, title string, o BoxOptions, content func()) ui.Element {
	root := ui.Box(c).Size(0, 0)
	local := ui.Local(root, "box", func() boxLocal { return boxLocal{} })
	t := theme.T(c)
	m := t.Metrics().Dialog()
	vw, vh := c.Size()
	panelAlpha := local.panel.update(c, state.Open, t.Motion().Dialog(), t.Motion().Dialog())
	scrimAlpha := local.scrim.update(c, state.Open, t.Motion().DialogScrim(), t.Motion().DialogScrim())
	if !state.Open && panelAlpha == 0 && scrimAlpha == 0 {
		return root
	}
	closeBox := func() {
		state.Open = false
		if o.OnClose != nil {
			o.OnClose()
		}
	}
	activate := func(action BoxAction) {
		if !action.KeepOpen {
			state.Open = false
		}
		if o.OnAction != nil {
			o.OnAction(action.ID)
		}
	}
	ui.Overlay(c, func() {
		back := ui.Box(c).Key(boxKey{root.ID()}).Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().IgnoreInput(!state.Open)
		// paint/dropdown.md#交互与提交: a closing visual no longer traps input or focus.
		if state.Open {
			back.Modal()
		}
		materialOpacity(back, t.Materials().Overlay(), scrimAlpha)
		back.Children(func() {
			width := m.Width()
			if o.Wide {
				width = m.WideWidth()
			}
			width = min(width, vw)
			panel := ui.Column(c).Width(width).MaxHeight(vh - m.Margin()*2).Radius(m.Radius()).Background(t.Colors().Dialog().Background()).Opacity(panelAlpha).Role(ui.RoleDialog).Label(title)
			panel.Pressed()
			menuShadow(panel, t)
			panel.Children(func() {
				titleHeight := float32(0)
				if title != "" || o.CloseButton || o.AdditionalTitle != "" {
					_, line := measure(c, title, t.Typography().DialogTitle(), 0)
					titleHeight = max(m.TitleHeight(), line+m.TitleTop()*2)
					ui.Row(c).FillWidth().Height(titleHeight).Shrink(0).Padding(m.TitleTop(), m.TitleLeft(), 0, m.TitleLeft()).Children(func() {
						text(c, title, t.Typography().DialogTitle(), t.Colors().Dialog().TitleForeground()).SingleLine().MinWidth(0)
						if o.AdditionalTitle != "" {
							text(c, o.AdditionalTitle, t.Typography().Body(), t.Colors().Dialog().TitleAdditionalForeground()).SingleLine().Margin(0, 0, 0, m.TitleGap())
						}
						if o.CloseButton {
							label := o.CloseLabel
							if label == "" {
								label = "Close"
							}
							b := ui.ButtonBase(c).Label(label).Absolute().Top(0).Right(0).Size(titleHeight, titleHeight)
							fg := t.Colors().Dialog().TitleCloseForeground()
							if b.Hovered() {
								fg = t.Colors().Dialog().TitleCloseForegroundHover()
							}
							b.Children(func() {
								draw.Element(c, closeIcon, t.Metrics().Menu().IconSize(), t.Metrics().Menu().IconSize()).TextColor(fg)
							})
							b.OnClick(closeBox)
						}
					})
				}
				footerHeight := float32(0)
				if len(o.Actions) > 0 {
					footerHeight = m.ButtonTop() + m.ButtonHeight() + m.ButtonBottom()
				}
				// paint/choose-box.md#现有overlaybox接入差异: fixed search and
				// full-width rows share the ordinary shell, title and action area.
				bodyMax := min(m.MaxBodyHeight(), max(0, vh-m.Margin()*2-titleHeight-footerHeight))
				if o.FullBody {
					ui.Column(c).FillWidth().Height(min(o.BodyHeight, bodyMax)).Shrink(0).Children(content)
				} else {
					ui.Scroll(c).FillWidth().MaxHeight(bodyMax).Padding(m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft()).
						Font(t.Typography().Font()).FontSize(t.Typography().DialogBody().Size() * c.Preferences().TextScale).FontWeight(t.Typography().DialogBody().Weight()).
						FixedLineHeight(m.LineHeight() * c.Preferences().TextScale).TextColor(t.Colors().Dialog().TextForeground()).Children(content)
				}
				if len(o.Actions) > 0 {
					ui.Row(c).FillWidth().Shrink(0).Padding(m.ButtonTop(), m.ButtonRight(), m.ButtonBottom(), m.ButtonRight()).Gap(m.ButtonGap()).Children(func() {
						for _, action := range o.Actions {
							if action.Left {
								boxButton(c, action, func() { activate(action) })
							}
						}
						ui.Box(c).Grow(1)
						// Telegram's first supplied action is rightmost.
						for i := len(o.Actions) - 1; i >= 0; i-- {
							action := o.Actions[i]
							if !action.Left {
								boxButton(c, action, func() { activate(action) })
							}
						}
					})
				}
			})
			if state.Open && !o.Locked && (back.OverlayShortcut(0, ui.KeyEscape) || back.Clicked()) {
				closeBox()
			}
			if state.Open {
				for _, action := range o.Actions {
					if action.Default && !action.Disabled && panel.Shortcut(0, ui.KeyEnter) {
						activate(action)
						break
					}
				}
			}
		})
	})
	return root
}
func boxButton(c *ui.Context, action BoxAction, activate func()) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Dialog()
	// paint/choose-box.md#外壳矩形与绘制层次: buttonRadius is 4, not 3.
	b := ui.ButtonBase(c).Key(action.ID).Label(action.Label).Height(m.ButtonHeight()).Padding(0, m.ButtonPadding()).Radius(m.ButtonRadius()).Disabled(action.Disabled)
	fg, bg, rippleColor := t.Colors().Control().SurfaceForeground(), t.Colors().Dialog().Background(), t.Colors().Control().SurfaceBackgroundPressed()
	if b.Hovered() {
		bg = t.Colors().Control().SurfaceBackgroundHover()
		fg = t.Colors().Control().SurfaceForegroundHover()
	}
	if action.Danger {
		fg = t.Colors().Control().DangerForeground()
		rippleColor = t.Colors().Control().DangerBackgroundPressed()
		if b.Hovered() {
			fg = t.Colors().Control().DangerForegroundHover()
			bg = t.Colors().Control().DangerBackgroundHover()
		}
	}
	if action.Disabled {
		fg = t.Colors().Menu().ForegroundDisabled()
	}
	w, _ := measure(c, action.Label, t.Typography().DialogButton(), 0)
	b.Width(w + 2*m.ButtonPadding()).Background(bg).Children(func() {
		text(c, action.Label, t.Typography().DialogButton(), fg).SingleLine().Absolute().Left(m.ButtonPadding()).Top(m.ButtonTextTop())
	})
	ripple(c, b, rippleColor)
	return b.OnClick(activate)
}
