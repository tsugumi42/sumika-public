package content

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/control"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type SummaryNode struct{ ID, Text string }
type SummaryStatus uint8

const (
	SummaryReady SummaryStatus = iota
	SummaryLoading
	SummaryFailed
)

type ForwardSummaryData struct {
	ID, Title, CountLabel, StatusText, Error string
	Nodes                                    []SummaryNode
	Status                                   SummaryStatus
}
type ForwardSummaryOptions struct {
	Appearance
	RetryLabel      string
	CanRetry        bool
	OnOpen, OnRetry func(string)
}

// ForwardSummary keeps the host's known nodes through loading and failure. It
// owns neither node fetching nor the host's forward browsing stack.
func ForwardSummary(c *ui.Context, data ForwardSummaryData, o ForwardSummaryOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Preview()
	q := t.Metrics().Quote()
	palette := colors(t, o.Appearance)
	var open func()
	if data.Status == SummaryReady && !o.Disabled && o.OnOpen != nil {
		open = func() { o.OnOpen(data.ID) }
	}
	root := action(c.Key(data.ID), data.Title, o.Disabled, open).Column().AlignItems(ui.Start).Justify(ui.Start).FillWidth().MinWidth(0).MaxWidth(t.Metrics().Message().MaxWidth()).Radius(q.Radius()).Clip().Padding(m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft())
	bg := t.Colors().Quote().In().Background()
	if o.Outgoing {
		bg = t.Colors().Quote().Out().Background()
	}
	if o.Selected {
		bg = palette.bar
	}
	// paint/forward-summary.md#数值形状与配色: existing quote contour and role mapping.
	root.Background(bg.Alpha(q.BackgroundOpacity()))
	quoteBar(root, q.OutlineWidth(), q.Radius(), palette.bar.Alpha(q.OutlineOpacity()))
	root.Children(func() {
		have := false
		if data.Title != "" {
			text(c, data.Title, t.Typography().ContentTitle(), palette.service).FillWidth().MaxLines(int(m.TitleLines()))
			have = true
		}
		count := 0
		for _, node := range data.Nodes {
			if node.Text != "" {
				count++
			}
		}
		if count > 0 {
			// paint/forward-summary.md#内容排版我定的: a single shaped total line
			// budget, with stable node identities, rather than three lines per node.
			body := ui.RichText(c.Key("summary")).FillWidth().MaxLines(int(t.Metrics().ForwardSummary().MaxSummaryLines())).Font(t.Typography().Font()).FontSize(t.Typography().Body().Size() * c.Preferences().TextScale).FontWeight(t.Typography().Body().Weight()).TextColor(palette.text)
			if have {
				body.Margin(m.MediaGap(), 0, 0, 0)
			}
			body.Children(func() {
				left := count
				for _, node := range data.Nodes {
					if node.Text == "" {
						continue
					}
					left--
					value := node.Text
					if left > 0 {
						value += "\n"
					}
					ui.Text(c.Key(node.ID), value)
				}
			})
			have = true
		}
		if data.CountLabel != "" {
			e := text(c, data.CountLabel, t.Typography().Body(), palette.muted).FillWidth().SingleLine()
			if have {
				e.Margin(m.MediaGap(), 0, 0, 0)
			}
			have = true
		}
		if data.Status == SummaryLoading && data.StatusText != "" {
			e := text(c, data.StatusText, t.Typography().Body(), palette.muted).FillWidth().Role(ui.RoleStatus)
			if have {
				e.Margin(m.MediaGap(), 0, 0, 0)
			}
		}
		if data.Status == SummaryFailed {
			if data.Error != "" {
				text(c, data.Error, t.Typography().Body(), t.Colors().Semantic().Danger()).FillWidth().Margin(m.MediaGap(), 0, 0, 0).Role(ui.RoleStatus)
			}
			if o.CanRetry && o.OnRetry != nil {
				// The card root owns disabled input and opacity for the retry as well.
				control.LinkButton(c.Key("retry"), o.RetryLabel, control.LinkButtonOptions{OnActivate: func() { o.OnRetry(data.ID) }}).Margin(m.MediaGap(), 0, 0, 0)
			}
		}
	})
	if open != nil {
		ripple(c, root, palette.bar, q.Radius(), q.BackgroundOpacity())
	}
	return root
}
