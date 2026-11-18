package content

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// QuoteData is already resolved by the host. Status supplies loading/deleted text.
type QuoteData struct {
	Name, Summary, Status string
	Preview               *ui.Bitmap
	Multiline             bool
}
type QuoteState struct{ Expanded bool }
type QuoteOptions struct {
	Appearance
	OnActivate func()
	Label      string
}

// Quote renders a reply. Multiline replies expand before requesting navigation.
func Quote(c *ui.Context, data QuoteData, state *QuoteState, o QuoteOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Quote()
	palette := colors(t, o.Appearance)
	summary := data.Summary
	if data.Status != "" {
		summary = data.Status
	}
	onActivate := o.OnActivate
	if data.Multiline && !state.Expanded {
		onActivate = func() { state.Expanded = true }
	}
	root := action(c, o.Label, o.Disabled, onActivate).Row().AlignItems(ui.Start).Justify(ui.Start).FillWidth().MinWidth(0).
		Padding(m.ReplyPaddingTop(), m.ReplyPaddingRight(), m.ReplyPaddingBottom(), m.ReplyPaddingLeft()).Radius(m.Radius()).Clip()
	bg := t.Colors().Quote().In().Background()
	if o.Outgoing {
		bg = t.Colors().Quote().Out().Background()
	}
	if o.Selected {
		bg = palette.bar
	}
	name, summaryColor := t.Colors().Quote().In().Name(), t.Colors().Quote().In().Summary()
	if o.Outgoing {
		name, summaryColor = t.Colors().Quote().Out().Name(), t.Colors().Quote().Out().Summary()
	}
	if o.Selected {
		name, summaryColor = palette.service, palette.text
	}
	root.Background(bg.Alpha(m.BackgroundOpacity()))
	quoteBar(root, m.OutlineWidth(), m.Radius(), palette.bar.Alpha(m.OutlineOpacity()))
	root.Children(func() {
		if data.Preview != nil {
			ui.Image(c, data.Preview).Size(m.PreviewSize(), m.PreviewSize()).Fit(ui.Cover).Radius(m.Radius()).Margin(0, m.PreviewGap(), 0, 0).Shrink(0)
		}
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			name := text(c, data.Name, t.Typography().ContentTitle(), name)
			if data.Multiline {
				name.MaxLines(2)
			} else {
				name.SingleLine()
			}
			body := text(c, summary, t.Typography().Quote(), summaryColor)
			if !data.Multiline {
				body.SingleLine()
			} else if !state.Expanded {
				body.MaxLines(int(m.CollapsedLines()))
			}
		})
	})
	if onActivate != nil {
		ripple(c, root, palette.bar, m.Radius(), m.BackgroundOpacity())
	}
	return root
}

// ForwardData keeps localization and hidden-author labels with the host.
type ForwardData struct {
	Label, Source, Via string
	Hidden             bool
}
type ForwardOptions struct {
	Appearance
	OnOpen func()
}

func Forward(c *ui.Context, data ForwardData, o ForwardOptions) ui.Element {
	t := theme.T(c)
	palette := colors(t, o.Appearance)
	fn := o.OnOpen
	if data.Hidden {
		fn = nil
	}
	root := action(c, data.Label+" "+data.Source, o.Disabled, fn).Column().AlignItems(ui.Start).Justify(ui.Start).FillWidth().MinWidth(0)
	root.Children(func() {
		source := data.Label + " " + data.Source
		if data.Via != "" {
			source += " " + data.Via
		}
		text(c, source, t.Typography().ContentTitle(), palette.service).MaxLines(2)
	})
	if fn != nil {
		ripple(c, root, palette.service, t.Metrics().Forward().Radius(), t.Metrics().Quote().BackgroundOpacity())
	}
	return root
}
