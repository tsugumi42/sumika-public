package content

import (
	"bytes"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
)

func TestForwardSummaryTotalBudgetAndHostStates(t *testing.T) {
	tr := theme.Light()
	data := ForwardSummaryData{ID: "summary", Title: "Forward title", CountLabel: "24 items", Nodes: []SummaryNode{{ID: "a", Text: "first\nsecond"}, {ID: "b", Text: "third"}, {ID: "c", Text: "fourth"}}}
	opened, retried := 0, 0
	disabled := false
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			ForwardSummary(c, data, ForwardSummaryOptions{Appearance: Appearance{Disabled: disabled}, CanRetry: true, RetryLabel: "Retry", OnOpen: func(string) { opened++ }, OnRetry: func(string) { retried++ }}).Label("Summary")
		})
	}, 300, 500)
	root, title, count := box(t, tt, "Summary"), box(t, tt, "Forward title"), box(t, tt, "24 items")
	// paint/forward-summary.md#内容排版我定的: independent title and total 3 summary lines.
	var fh float32
	mt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		_, fh = c.MeasureText(0, ui.Span{Text: "M", Size: tr.Typography().Body().Size(), Weight: tr.Typography().Body().Weight()})
	}, 20, 20)
	mt.Frame()
	expected := tr.Metrics().Preview().PaddingTop() + fh + tr.Metrics().Preview().MediaGap() + tr.Metrics().ForwardSummary().MaxSummaryLines()*fh + tr.Metrics().Preview().MediaGap() + fh + tr.Metrics().Preview().PaddingBottom()
	expect(t, "forward-summary-total-line-budget", closeTo(root.H, expected))
	expect(t, "forward-summary-count-separate-position", count.Y >= title.Y+title.H+3*fh+2*tr.Metrics().Preview().MediaGap()-1)
	click(t, tt, "Forward title")
	tt.Key(0, ui.KeyEnter)
	expect(t, "forward-summary-open-pointer-keyboard", opened == 2)
	data.Status = SummaryLoading
	data.StatusText = "Loading"
	tt.Frame()
	click(t, tt, "Forward title")
	expect(t, "forward-summary-loading-retains-data", opened == 2 && tt.HasText("24 items") && tt.HasText("Loading"))
	data.Status = SummaryFailed
	data.Error = "Failed"
	tt.Frame()
	click(t, tt, "Retry")
	expect(t, "forward-summary-retry-is-independent", retried == 1 && opened == 2 && tt.HasText("24 items"))
	disabled = true
	tt.Frame()
	click(t, tt, "Retry")
	expect(t, "forward-summary-disabled-retry", retried == 1)
	data.Nodes = nil
	data.Status = SummaryReady
	disabled = false
	tt.Frame()
	expect(t, "forward-summary-empty-has-no-three-line-hole", closeTo(box(t, tt, "Summary").H, tr.Metrics().Preview().PaddingTop()+2*fh+tr.Metrics().Preview().MediaGap()+tr.Metrics().Preview().PaddingBottom()))
}

func TestForwardSummaryDisabledRetryOpacity(t *testing.T) {
	render := func(disabled bool) *ui.Tester {
		tt := ui.NewTester(func(c *ui.Context) {
			surface(c, func() {
				build := func(off bool) {
					ForwardSummary(c, ForwardSummaryData{ID: "s", Title: "Forward", Status: SummaryFailed, Error: "Failed"}, ForwardSummaryOptions{Appearance: Appearance{Disabled: off}, CanRetry: true, RetryLabel: "Retry", OnRetry: func(string) {}})
				}
				// mygo 0.8.0 起 Opacity 是整组淡出，和禁用继承的 0.5 不再等价；
				// 参照改成：启用的卡片放进禁用的父元素，只吃 mygo 的那一次 0.5。
				if disabled {
					build(true)
				} else {
					ui.Column(c).FillWidth().Disabled(true).Children(func() { build(false) })
				}
			})
		}, 300, 160)
		tt.Frame()
		tt.Frame()
		return tt
	}
	actual, reference := render(true), render(false)
	expect(t, "forward-summary-retry-single-disabled-opacity", bytes.Equal(actual.Image().Pix, reference.Image().Pix))
}
