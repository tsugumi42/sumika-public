package control

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type ApprovalStatus uint8

const (
	Awaiting ApprovalStatus = iota
	Submitting
	ApprovalFailed
	Resolved
	Expired
)

type ApprovalAction uint8

const (
	AllowOnce ApprovalAction = iota
	Reject
)

type Approval struct {
	ID, Title, Summary, StatusText, Error string
	Status                                ApprovalStatus
	Expanded                              bool
}
type ApprovalOptions struct {
	DetailsLabel, AllowLabel, RejectLabel             string
	Disabled, CanRetry, AllowDisabled, RejectDisabled bool
	OnExpand                                          func(bool)
	OnAction                                          func(string, ApprovalAction)
}
type approvalLocal struct {
	entry, details, allow, reject ui.Handle
	status                        ApprovalStatus
	sent                          bool
	width                         float32
}

// ApprovalCard is a host-driven request. It never executes a tool, assumes
// approval, or removes itself after reporting an action.
func ApprovalCard(c *ui.Context, data Approval, o ApprovalOptions, details func()) ui.Element {
	t := theme.T(c)
	m, am := t.Metrics().Dialog(), t.Metrics().Approval()
	root := ui.Column(c.Key(data.ID)).FillWidth().MaxWidth(m.WideWidth()).Shrink(0).Radius(m.Radius()).Background(t.Colors().Dialog().Background()).Padding(m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft())
	s := ui.Local(root, "approval", func() approvalLocal { return approvalLocal{} })
	root.Bind(&s.entry).Label(data.Title)
	if s.status != data.Status {
		s.sent = false
		s.status = data.Status
	}
	enabled := !o.Disabled && (data.Status == Awaiting || data.Status == ApprovalFailed && o.CanRetry)
	if !enabled && (s.allow.Focused(c) || s.reject.Focused(c)) {
		if details != nil {
			s.details.Focus(c)
		} else {
			root.Focusable()
			s.entry.Focus(c)
		}
	}
	width := max(0, root.LayoutBounds().W-m.PaddingLeft()-m.PaddingRight())
	services := c.Services()
	root.DrawOver(func(_ *ui.Painter, r ui.Rect) {
		if s.width != r.W {
			s.width = r.W
			services.Invalidate()
		}
	})
	gap := t.Metrics().Settings().SectionSkip()
	// paint/approval-card.md#外壳标题与详情我定的: full natural text, no safety truncation.
	root.Children(func() {
		text(c, data.Title, t.Typography().DialogTitle(), t.Colors().Dialog().TitleForeground()).FillWidth().Role(ui.RoleHeading)
		if data.Summary != "" {
			text(c, data.Summary, t.Typography().DialogBody(), t.Colors().Dialog().TextForeground()).FillWidth().Margin(gap, 0, 0, 0)
		}
		if details != nil {
			LinkButton(c.Key("details-entry"), o.DetailsLabel, LinkButtonOptions{OnActivate: func() {
				if o.OnExpand != nil {
					o.OnExpand(!data.Expanded)
				}
			}}).Bind(&s.details).Role(ui.RoleDisclosure).Expanded(data.Expanded).Margin(gap, 0, 0, 0)
			Collapse(c.Key("details"), CollapseOptions{Expanded: data.Expanded, Trigger: &s.details, Padding: [4]float32{0, gap, 0, 0}}, details)
		}
		if data.StatusText != "" {
			text(c, data.StatusText, t.Typography().DialogBody(), t.Colors().Dialog().TextForeground()).FillWidth().Role(ui.RoleStatus).Margin(gap, 0, 0, 0)
		}
		if data.Error != "" {
			text(c, data.Error, t.Typography().DialogBody(), t.Colors().Dialog().TextForegroundError()).FillWidth().Role(ui.RoleStatus).Margin(gap, 0, 0, 0)
		}
		lw, _ := measure(c, o.AllowLabel, t.Typography().Service(), 0)
		rw, _ := measure(c, o.RejectLabel, t.Typography().Service(), 0)
		stacked := max(lw, rw)+2*am.ActionPadding() > (width-am.ActionGap())/2
		actions := ui.Row(c.Key("actions")).FillWidth().Gap(am.ActionGap()).Margin(am.ActionGap(), 0, 0, 0)
		if stacked {
			actions.Column()
		}
		actions.Children(func() {
			action := func(id, label string, which ApprovalAction, off bool, handle *ui.Handle) {
				targetWidth := (width - am.ActionGap()) / 2
				if stacked {
					targetWidth = width
				}
				_, th := measure(c, label, t.Typography().Service(), max(1, targetWidth-2*am.ActionPadding()))
				h := am.ActionHeight()
				if th+2*am.ActionTextTop() > h {
					h = th + 2*am.ActionTextTop()
				}
				b := ui.ButtonBase(c.Key(id)).Label(label).Height(h).MinWidth(0).Shrink(0).Disabled(!enabled || off).Radius(t.Metrics().Message().RadiusSmall()).Clip().Bind(handle)
				if stacked {
					b.FillWidth()
				} else {
					b.Grow(1)
				}
				bg := t.Colors().Keyboard().InlinePrimaryBackground()
				if which == Reject {
					bg = t.Colors().Keyboard().InlineDangerBackground()
				}
				b.Background(bg)
				// paint/approval-card.md#两个动作我定的: sourced bot-keyboard colors and ripple.
				wave := ui.Local(b, "wave", func() pulse { return pulse{} })
				wave.update(c, b, enabled && !off, nil)
				over := b.Hovered() || b.Pressed()
				b.Draw(func(p *ui.Painter, r ui.Rect) {
					if over {
						p.Fill(r, t.Colors().Message().BotKbHoverBackgroundAdd(), t.Metrics().Message().RadiusSmall())
					}
					wave.paint(p, r, t.Colors().Message().BotKbPressedBackground())
				})
				b.Children(func() {
					text(c, label, t.Typography().Service(), t.Colors().Window().ForegroundActive()).FillWidth().Padding(0, am.ActionPadding()).TextAlign(ui.Center).Absolute().Top(am.ActionTextTop()).Left(0).Right(0).IgnoreInput(true)
				})
				b.OnClick(func() {
					if s.sent {
						return
					}
					s.sent = true
					if o.OnAction != nil {
						o.OnAction(data.ID, which)
					}
				})
			}
			action("allow", o.AllowLabel, AllowOnce, o.AllowDisabled, &s.allow)
			action("reject", o.RejectLabel, Reject, o.RejectDisabled, &s.reject)
		})
	})
	return root
}
