package dialogs

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// SearchScope is one host-provided scope: topic, chat, community, archive,
// own messages or public posts. IDs are stable, labels are already localized.
type SearchScope struct {
	ID, Label string
	Image     ui.ImageSource
}

type SearchScopeOptions struct {
	Heading, CancelScopeLabel, CancelSenderLabel string
	Sender                                       *SearchScope
	Disabled                                     bool
	OnSelect                                     func(string)
	OnCancelScope, OnSender, OnCancelSender      func()
}

// SearchScopes renders the upstream search-in header and independent sender row.
// The host owns the selected ID and which scopes are currently available.
func SearchScopes(c *ui.Context, scopes []SearchScope, selected string, o SearchScopeOptions) ui.Element {
	root := ui.Column(c).FillWidth().Gap(0).Disabled(o.Disabled)
	root.Children(func() {
		GroupHeading(c, o.Heading)
		for _, scope := range scopes {
			if scope.ID != selected {
				continue
			}
			c.Key("scope")
			searchScopeRow(c, scope, o.CancelScopeLabel, o.OnCancelScope, func(menu *ui.Menu) {
				for _, s := range scopes {
					if menu.Item(s.Label).Checked(s.ID == selected).Chosen() && o.OnSelect != nil {
						o.OnSelect(s.ID)
					}
				}
			}, nil)
		}
		if o.Sender != nil {
			c.Key("sender")
			searchScopeRow(c, *o.Sender, o.CancelSenderLabel, o.OnCancelSender, nil, o.OnSender)
		}
	})
	return root
}

func searchScopeRow(c *ui.Context, scope SearchScope, cancelLabel string, cancel func(), menu func(*ui.Menu), activate func()) {
	t := theme.T(c)
	m := t.Metrics().Search()
	height := max(m.ScopeHeight(), m.ScopeTop()+fontHeight(c, t.Typography().ConversationTitle()))
	ui.Row(c).FillWidth().Height(height).Gap(0).Background(t.Colors().Conversation().Background()).Children(func() {
		button := ui.ButtonBase(c).Height(height).Grow(1).MinWidth(0).Label(scope.Label).OnClick(activate)
		if menu != nil {
			button.Menu(menu)
		}
		if button.Hovered() {
			button.Background(t.Colors().Conversation().BackgroundHover())
		}
		ripple(c, button, t.Colors().Conversation().PressedBackground(), nil)
		button.Children(func() {
			ui.Box(c).Absolute().Left(m.ScopeLeft()).Top((height-m.ScopePhoto())/2).Size(m.ScopePhoto(), m.ScopePhoto()).Radius(m.ScopePhoto() / 2).Background(t.Colors().Avatar().SavedMessagesBackground()).Clip().Center().Children(func() {
				if scope.Image != nil {
					ui.Image(c, scope.Image).Fill().Fit(ui.Cover)
				} else {
					icon(c, folderIcon, m.ScopePhoto()-2*m.ScopeArrowGap(), t.Colors().Avatar().UserpicForeground())
				}
			})
			ui.Row(c).Absolute().Left(m.ScopeLeft() + m.ScopePhoto() + m.ScopeGap()).Right(m.ScopeGap()).Top(m.ScopeTop()).Gap(m.ScopeArrowGap()).AlignItems(ui.Center).Children(func() {
				text(c, scope.Label, t.Typography().ConversationTitle(), t.Colors().Window().BoldForeground()).SingleLine().MinWidth(0).Shrink(1)
				text(c, "⌄", t.Typography().Body(), t.Colors().Window().BoldForeground()).Shrink(0)
			})
		})
		ui.ButtonBase(c).Size(m.ClearSize(), height).Center().Label(cancelLabel).OnClick(cancel).Children(func() {
			icon(c, clearIcon, m.ClearSize()-2*m.ClearInset(), t.Colors().Conversation().MenuIconForeground())
		})
	})
	ui.Box(c).FillWidth().Height(t.Metrics().Control().BorderWidth()).Background(t.Colors().Window().ShadowForegroundFallback())
}

func fontHeight(c *ui.Context, style theme.TextStyle) float32 {
	_, h := measure(c, "M", style)
	return h
}
