package control

import (
	"bytes"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/settings"
	"github.com/tsugumi42/MewTypeUI/theme"
	"image/color"
	"strings"
	"testing"
	"time"
)

func check(t *testing.T, id string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", id)
	}
}
func near(a, b float32) bool { return a-b <= 1 && b-a <= 1 }
func box(t *testing.T, tt *ui.Tester, label string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("missing %q: %v", label, tt.Texts())
	}
	return r
}
func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}
func rgba(c ui.Color) color.RGBA { return color.RGBA{R: c.R, G: c.G, B: c.B, A: c.A} }
func ink(tt *ui.Tester, r ui.Rect, c ui.Color) bool {
	im := tt.Image()
	for y := max(0, int(r.Y)); y < min(im.Bounds().Dy(), int(r.Y+r.H)); y++ {
		for x := max(0, int(r.X)); x < min(im.Bounds().Dx(), int(r.X+r.W)); x++ {
			if im.RGBAAt(x, y) == rgba(c) {
				return true
			}
		}
	}
	return false
}
func settle(tt *ui.Tester) { tt.Frame(); tt.Frame() }

func TestRoundLinkIconContracts(t *testing.T) {
	tr := theme.Light()
	calls := 0
	disabled := false
	suffix := NumberSuffix{Text: "8", Value: 8}
	var natural, fh float32
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		natural, fh = measure(c, "Action", tr.Typography().ContentTitle(), 0)
		ui.Column(c).FillWidth().AlignItems(ui.Start).Children(func() {
			RoundButton(c.Key("round"), "Action", RoundButtonOptions{Disabled: disabled, OnActivate: func() { calls++ }})
			RoundButton(c.Key("suffix"), "Count", RoundButtonOptions{Numbers: suffix}).Label("Counter")
			RoundButton(c.Key("light"), "Light", RoundButtonOptions{Tone: Light})
			LinkButton(c.Key("link"), "Link", LinkButtonOptions{OnActivate: func() { calls++ }})
			IconButton(c.Key("icon"), "Icon", backIcon, IconButtonOptions{OnActivate: func() { calls++ }})
			IconButton(c.Key("back"), "Back", backIcon, IconButtonOptions{Kind: BackIcon})
			IconButton(c.Key("close"), "Close", closeIcon, IconButtonOptions{Kind: CloseIcon})
		})
	}, 340, 400)
	settle(tt)
	r := box(t, tt, "Action")
	m := tr.Metrics().Button()
	// widgets.style:728-732; paint/round-button.md#几何与层次.
	check(t, "round-natural-frame", near(r.H, m.Height()) && near(r.W, natural+m.HorizontalExtra()))
	check(t, "round-primary-background", tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+2)) == rgba(tr.Colors().Control().PrimaryBackground()))
	check(t, "round-foreground-and-font", ink(tt, ui.Rect{X: r.X, Y: r.Y + m.TextTop(), W: r.W, H: fh}, tr.Colors().Control().PrimaryForeground()))
	tt.Move(r.X+2, r.Y+2)
	check(t, "round-immediate-hover", tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+2)) == rgba(tr.Colors().Control().PrimaryBackgroundHover()))
	tt.Press(r.X+2, r.Y+2)
	tt.Move(330, 390)
	tt.Release(330, 390)
	check(t, "round-drag-out-cancels", calls == 0)
	click(t, tt, "Action")
	tt.Key(0, ui.KeyEnter)
	check(t, "round-pointer-and-enter", calls == 2)
	disabled = true
	tt.Frame()
	click(t, tt, "Action")
	tt.Key(0, ui.KeySpace)
	check(t, "round-disabled-rejects", calls == 2)
	link := box(t, tt, "Link")
	var lw, lh float32
	measureTest := ui.NewTester(func(c *ui.Context) { theme.Apply(c, tr); lw, lh = measure(c, "Link", tr.Typography().Body(), 0) }, 20, 20)
	measureTest.Frame()
	check(t, "link-natural-frame", near(link.W, lw) && near(link.H, lh))
	click(t, tt, "Link")
	tt.Key(0, ui.KeySpace)
	check(t, "link-pointer-and-space", calls == 4)
	icon, back, close := box(t, tt, "Icon"), box(t, tt, "Back"), box(t, tt, "Close")
	// widgets.style:1487-1497; info.style:138-166.
	check(t, "icon-independent-frame", near(icon.W, tr.Metrics().IconButton().Width()) && near(icon.H, tr.Metrics().IconButton().Height()))
	check(t, "icon-back-close-slots", near(back.W, tr.Metrics().PageTitle().BackWidth()) && near(close.W, tr.Metrics().PageTitle().CloseWidth()) && near(back.H, tr.Metrics().PageTitle().Height()))
	click(t, tt, "Icon")
	check(t, "icon-activation", calls == 5)
	before := box(t, tt, "Counter").W
	suffix = NumberSuffix{Text: "123", Value: 123}
	tt.Frame()
	tt.Advance(tr.Motion().ButtonNumbers().Duration() / 2)
	tt.Frame()
	mid := box(t, tt, "Counter").W
	check(t, "numbers-interpolate-width", mid > before)
	tt.Advance(tr.Motion().ButtonNumbers().Duration() / 2)
	tt.Frame()
	end := box(t, tt, "Counter").W
	check(t, "numbers-circ-width-midpoint", mid < end && mid > before+(end-before)/2)
	suffix = NumberSuffix{Text: "4567", Value: 4567}
	tt.Frame()
	tt.Advance(50 * time.Millisecond)
	suffix = NumberSuffix{Text: "1", Value: 1}
	tt.Frame()
	tt.Advance(100 * time.Millisecond)
	tt.Frame()
	check(t, "numbers-latest-target-queued", box(t, tt, "Counter").W >= end)
	tt.Advance(tr.Motion().ButtonNumbers().Duration())
	tt.Frame()
	check(t, "numbers-finish-latest-target", near(box(t, tt, "Counter").W, before))
	tt.Advance(tr.Motion().RippleShow().Duration())
	tt.Frame()
	check(t, "buttons-eventually-idle", !tt.FramePending())
}

func TestInputEditingBoundsAndIME(t *testing.T) {
	tr := theme.Light()
	draft := ""
	multi := "line\nline\nline\nline\nline\nline\nline\nline"
	readonly, disabled := false, false
	submitted, cancelled := 0, 0
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		services = c.Services()
		ui.Column(c).FillWidth().Children(func() {
			InputField(c.Key("one"), &draft, "Single", InputOptions{Placeholder: "Floating", ReadOnly: readonly, Disabled: disabled, OnSubmit: func(string) { submitted++ }, OnCancel: func() { cancelled++ }}).Label("Single frame")
			InputField(c.Key("multi"), &multi, "Multi", InputOptions{Placeholder: "Multiline", Multiline: true}).Label("Multi frame")
		})
	}, 260, 330)
	settle(tt)
	// widgets.style:1033,1058-1060; paint/input-field.md.
	check(t, "input-single-min-height", near(box(t, tt, "Single frame").H, tr.Metrics().Input().HeightMin()))
	check(t, "input-multiline-max-height", near(box(t, tt, "Multi frame").H, tr.Metrics().Input().HeightMax()))
	click(t, tt, "Single")
	tt.Compose("中文", 2)
	tt.Key(0, ui.KeyEnter)
	check(t, "input-ime-confirm-not-submit", draft == "" && submitted == 0)
	tt.Type("中文")
	tt.Key(0, ui.KeyEnter)
	check(t, "input-commit-submit-once", draft == "中文" && submitted == 1)
	services.WriteClipboard("a\r\nb\rc\nd")
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Key(ui.Ctrl, ui.KeyV)
	check(t, "input-single-paste-normalized", draft == "a b c d")
	tt.Key(0, ui.KeyEscape)
	check(t, "input-escape-intent", cancelled == 1)
	readonly = true
	tt.Frame()
	tt.Type("blocked")
	check(t, "input-readonly-retains-text", draft == "a b c d")
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Key(ui.Ctrl, ui.KeyC)
	check(t, "input-readonly-copy", services.ReadClipboard() == "a b c d")
	disabled = true
	tt.Frame()
	click(t, tt, "Single")
	tt.Type("blocked")
	tt.Key(0, ui.KeyEnter)
	check(t, "input-disabled-no-submit", draft == "a b c d" && submitted == 1)
	multi = "short"
	services.Invalidate()
	settle(tt)
	check(t, "input-deletion-shrinks", near(box(t, tt, "Multi frame").H, tr.Metrics().Input().HeightMin()))
	multi = strings.Repeat("wrapped words ", 25)
	services.Invalidate()
	settle(tt)
	narrow := box(t, tt, "Multi frame").H
	tt.SetSize(1400, 330)
	settle(tt)
	check(t, "input-width-reflow-shrinks", box(t, tt, "Multi frame").H < narrow)
}

func TestCollapseFocusTimingAndDynamicHeight(t *testing.T) {
	tr := theme.Light()
	expanded := true
	var trigger ui.Handle
	value := "kept"
	height := float32(100)
	done := 0
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ui.Column(c).FillWidth().Children(func() {
			LinkButton(c, "Toggle", LinkButtonOptions{OnActivate: func() { expanded = !expanded }}).Bind(&trigger)
			Collapse(c, CollapseOptions{Expanded: expanded, Trigger: &trigger, OnComplete: func(bool) { done++ }}, func() {
				ui.Box(c).FillWidth().Height(height).Children(func() { InputField(c, &value, "Child", InputOptions{}) })
			}).Label("Collapse")
		})
	}, 300, 300)
	settle(tt)
	check(t, "collapse-initial-expanded", near(box(t, tt, "Collapse").H, height))
	click(t, tt, "Child")
	expanded = false
	tt.Frame()
	check(t, "collapse-transfers-focus-at-start", tt.Focused("Toggle"))
	tt.Advance(tr.Motion().Collapse().Duration() / 2)
	tt.Frame()
	check(t, "collapse-linear-midpoint", near(box(t, tt, "Collapse").H, 50))
	expanded = true
	tt.Frame()
	height = 200
	tt.Frame()
	tt.Advance(tr.Motion().Collapse().Duration() / 2)
	settle(tt)
	check(t, "collapse-reverse-dynamic-measure", near(box(t, tt, "Collapse").H, 150))
	tt.Advance(tr.Motion().Collapse().Duration() / 2)
	tt.Frame()
	check(t, "collapse-completion-once", near(box(t, tt, "Collapse").H, 200) && done == 1)
	expanded = false
	tt.Frame()
	tt.Advance(tr.Motion().Collapse().Duration())
	tt.Frame()
	tt.Type("bad")
	check(t, "collapse-hidden-not-editable", value == "kept" && near(box(t, tt, "Collapse").H, 0))
	check(t, "collapse-end-no-frame-request", !tt.FramePending() && done == 2)
	expanded = true
	tt.SetPreferences(ui.Preferences{TextScale: 1, ReduceMotion: true})
	settle(tt)
	check(t, "collapse-reduced-motion-terminal", near(box(t, tt, "Collapse").H, 200))
}

func TestBadgesTitleAndCategories(t *testing.T) {
	tr := theme.Light()
	selected := "a"
	calls := 0
	hidden := false
	items := []Category{{ID: "a", Label: "Category A", Icon: backIcon}, {ID: "disabled", Label: "Blocked", Disabled: true}, {ID: "b", Label: "Category B"}}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ui.Column(c).Fill().Children(func() {
			StatusBadge(c, "Ready", BadgeOptions{Tone: Success})
			CountBadge(c, "88", CountBadgeOptions{Muted: true})
			PageTitle(c, "Heading", PageTitleOptions{Subtitle: "Subtitle", Hidden: hidden, OnBack: func() { calls++ }, BackLabel: "Back", OnClose: func() { calls++ }, CloseLabel: "Close"}).Label("Title")
			ui.Box(c).FillWidth().Height(180).Children(func() {
				SideCategories(c, items, selected, CategoriesOptions{Label: "Categories", OnSelect: func(id string) { selected = id; calls++ }})
			})
		})
	}, 400, 400)
	settle(tt)
	var tw, fh float32
	mt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		tw, _ = measure(c, "Ready", tr.Typography().StatusBadge(), 0)
		fm := (ui.Font{Family: tr.Typography().Font(), Size: tr.Typography().StatusBadge().Size(), Weight: tr.Typography().StatusBadge().Weight()}).Metrics()
		fh = fm.Ascent + fm.Descent + fm.LineGap
	}, 20, 20)
	mt.Frame()
	badge := box(t, tt, "Ready")
	// settings.style:156,161 and paint/badge.md; dimensions follow real font metrics.
	check(t, "status-badge-natural-padding", near(badge.W, tw+tr.Metrics().Badge().TextPaddingLeft()+tr.Metrics().Badge().TextPaddingRight()) && near(badge.H, fh+2))
	check(t, "status-badge-semantic-ink", ink(tt, badge, tr.Colors().Semantic().Success()))
	check(t, "count-badge-separate-height", near(box(t, tt, "88").H, tr.Metrics().Conversation().BadgeHeight()))
	title := box(t, tt, "Title")
	heading := box(t, tt, "Heading")
	subtitle := box(t, tt, "Subtitle")
	check(t, "title-frame-and-origin", near(title.H, tr.Metrics().PageTitle().Height()) && near(heading.X-title.X, tr.Metrics().PageTitle().BackWidth()) && near(heading.Y-title.Y, tr.Metrics().PageTitle().TitleWithSubtitleTop()))
	check(t, "title-subtitle-top", near(subtitle.Y-title.Y, tr.Metrics().PageTitle().SubtitleTop()))
	click(t, tt, "Back")
	click(t, tt, "Close")
	check(t, "title-independent-actions", calls == 2)
	click(t, tt, "Category A")
	tt.Key(0, ui.KeyDown)
	check(t, "categories-focus-skips-disabled", tt.Focused("Category B") && selected == "a")
	tt.Key(0, ui.KeyEnter)
	check(t, "categories-host-select-id", selected == "b" && calls == 4)
	items[0], items[2] = items[2], items[0]
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	check(t, "categories-reorder-retains-id", selected == "b" && calls == 5)
	hidden = true
	tt.Frame()
	tt.Advance(tr.Motion().PageTitle().Duration())
	tt.Frame()
	check(t, "title-mode-hidden-at-duration", !tt.HasText("Heading") && tt.HasText("Close"))
}

func tabsFixture() []TabItem {
	return []TabItem{{ID: "a", Label: "Alpha"}, {ID: "blocked", Label: "Blocked", Disabled: true}, {ID: "b", Label: "Beta"}, {ID: "c", Label: "Longer category"}}
}
func TestSegmentsPreviewAndTabsManualActivation(t *testing.T) {
	tr := theme.Light()
	selected := "a"
	calls := 0
	var state *stripLocal
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		e := SettingsSlider(c, tabsFixture(), selected, SegmentsOptions{OnSelect: func(id string) { selected = id; calls++ }})
		state = ui.Local(e, "strip", func() stripLocal { return stripLocal{} })
	}, 420, 70)
	settle(tt)
	a, b := box(t, tt, "Alpha"), box(t, tt, "Beta")
	check(t, "segments-sourced-height-gap", near(a.H, tr.Metrics().Segments().Height()) && near(b.X-state.sections[1].x-state.sections[1].w, tr.Metrics().Segments().Gap()))
	tt.Press(a.X+a.W/2, a.Y+a.H/2)
	tt.Move(b.X+b.W/2, b.Y+b.H/2)
	tt.Release(b.X+b.W/2, b.Y+b.H/2)
	check(t, "segments-fast-release-delays", calls == 0 && selected == "a")
	tt.Advance(tr.Motion().Segments().Duration() - time.Millisecond)
	tt.Frame()
	check(t, "segments-no-early-commit", calls == 0)
	tt.Advance(time.Millisecond)
	tt.Frame()
	check(t, "segments-commits-dragged-id", calls == 1 && selected == "b")
	tt.Key(0, ui.KeyHome)
	tt.Key(0, ui.KeySpace)
	tt.Advance(tr.Motion().Segments().Duration())
	tt.Frame()
	check(t, "segments-keyboard-commit", selected == "a" && calls == 2)
	for _, variant := range []TabsVariant{UnderlineTabs, ProfileTabs} {
		t.Run(string(rune('a'+variant)), func(t *testing.T) {
			active := "a"
			events := 0
			context := ""
			var visual *stripLocal
			test := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, tr)
				e := Tabs(c, tabsFixture(), active, TabsOptions{Variant: variant, OnSelect: func(id string) { active = id; events++ }, OnContext: func(id string) { context = id }})
				visual = ui.Local(e, "strip", func() stripLocal { return stripLocal{} })
			}, 420, 80)
			settle(test)
			click(t, test, "Alpha")
			test.Key(0, ui.KeyRight)
			check(t, "tabs-manual-arrow-focus", test.Focused("Beta") && active == "a" && events == 1)
			test.Key(0, ui.KeyEnter)
			check(t, "tabs-enter-active-id", active == "b" && events == 2)
			duration := tr.Motion().Tabs().Duration()
			if variant == ProfileTabs {
				duration = tr.Motion().ProfileTabs().Duration()
			}
			test.Advance(duration / 2)
			test.Frame()
			check(t, "tabs-moving-indicator", visual.left.value > visual.sections[0].x && visual.left.value < visual.sections[2].x)
			test.Advance(duration / 2)
			test.Frame()
			check(t, "tabs-indicator-endpoint", near(visual.left.value, visual.sections[2].x+func() float32 {
				if variant == UnderlineTabs {
					return (visual.sections[2].w - visual.sections[2].text) / 2
				}
				return 0
			}()))
			r := box(t, test, "Beta")
			test.RightClick("Beta")
			check(t, "tabs-right-click-stable-id", context == "b")
			if variant == ProfileTabs {
				test.Press(r.X+r.W/2, r.Y+r.H/2)
				test.Move(r.X+r.W/2-30, r.Y+r.H/2)
				test.Release(r.X+r.W/2-30, r.Y+r.H/2)
				check(t, "profile-drag-not-activate", events == 2)
			}
		})
	}
}

func TestApprovalStateAndResponsiveActions(t *testing.T) {
	tr := theme.Light()
	data := Approval{ID: "approval", Title: "Approval", Summary: "Prepared summary", Status: Awaiting}
	calls := 0
	options := ApprovalOptions{DetailsLabel: "Details", AllowLabel: "Allow once", RejectLabel: "Reject", CanRetry: true, OnExpand: func(v bool) { data.Expanded = v }, OnAction: func(string, ApprovalAction) { calls++; data.Status = Submitting }}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ApprovalCard(c, data, options, func() { ui.Text(c, "Prepared details") })
	}, 500, 500)
	settle(tt)
	allow, reject := box(t, tt, "Allow once"), box(t, tt, "Reject")
	// chat.style:282-285; layers.style:87,118-119; paint/approval-card.md.
	check(t, "approval-action-geometry", near(allow.H, tr.Metrics().Approval().ActionHeight()) && near(reject.X-allow.X-allow.W, tr.Metrics().Approval().ActionGap()))
	check(t, "approval-shell-width-padding", near(allow.X, tr.Metrics().Dialog().PaddingLeft()) && near(allow.W+reject.W+tr.Metrics().Approval().ActionGap(), tr.Metrics().Dialog().WideWidth()-2*tr.Metrics().Dialog().PaddingLeft()))
	click(t, tt, "Allow once")
	click(t, tt, "Reject")
	tt.Key(0, ui.KeyEnter)
	check(t, "approval-only-one-in-flight", calls == 1 && data.Status == Submitting)
	click(t, tt, "Details")
	tt.Advance(tr.Motion().Collapse().Duration())
	settle(tt)
	check(t, "approval-details-available-while-submitting", data.Expanded && tt.HasText("Prepared details"))
	data.Status = ApprovalFailed
	data.Error = "Failed"
	tt.Frame()
	click(t, tt, "Reject")
	check(t, "approval-explicit-failure-retry", calls == 2)
	data.Status = Resolved
	tt.Frame()
	click(t, tt, "Allow once")
	check(t, "approval-resolved-no-action", calls == 2)
	data.Status = Awaiting
	tt.SetSize(170, 500)
	settle(tt)
	allow, reject = box(t, tt, "Allow once"), box(t, tt, "Reject")
	check(t, "approval-narrow-stacks-actions", near(allow.X, reject.X) && near(reject.Y-allow.Y-allow.H, tr.Metrics().Approval().ActionGap()))
}

func TestSearchDropdownStableIDs(t *testing.T) {
	tr := theme.Light()
	s := DropdownState{}
	selected := "a"
	calls := 0
	items := []settings.ChoiceItem{{ID: "a", Title: "Same"}, {ID: "b", Title: "Same"}, {ID: "disabled", Title: "Blocked", Disabled: true}}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		Dropdown(c, &s, items, selected, "Dropdown", DropdownOptions{Mode: SearchBox, Placeholder: "Select", Choose: settings.ChooseOptions{SearchLabel: "Search", SearchPlaceholder: "Find", CloseLabel: "Cancel", EmptyLabel: "Empty"}, OnSelect: func(id string) { selected = id; calls++ }})
	}, 600, 600)
	settle(tt)
	click(t, tt, "Dropdown")
	tt.Advance(tr.Motion().Dialog().Duration())
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	check(t, "dropdown-duplicate-label-stable-id", selected == "b" && calls == 1 && !s.chooser.Open)
	check(t, "dropdown-restores-entry-focus", tt.Focused("Dropdown"))
	tt.Advance(tr.Motion().Dialog().Duration())
	tt.Frame()
	click(t, tt, "Dropdown")
	tt.Advance(tr.Motion().Dialog().Duration())
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	check(t, "dropdown-escape-never-submits", selected == "b" && calls == 1 && !s.chooser.Open)
}

func TestInlineTitleKeepsTextAndActionsVisible(t *testing.T) {
	tr := theme.Light()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ui.Column(c).FillWidth().Children(func() {
			PageTitle(c, "Heading", PageTitleOptions{Inline: true, Subtitle: "More", OnClose: func() {}, CloseLabel: "Close inline"}).Label("Inline")
		})
	}, 260, 700)
	settle(tt)
	root, heading, description, action := box(t, tt, "Inline"), box(t, tt, "Heading"), box(t, tt, "More"), box(t, tt, "Close inline")
	// paint/page-title.md#内嵌页面标题我定的: 24/17/6/9 and natural wrapping.
	check(t, "inline-title-natural-height", root.H >= heading.H+description.H+tr.Metrics().PageTitle().Top()+tr.Metrics().Settings().SectionSkip()+tr.Metrics().Settings().SectionTitlePaddingBottom()-1)
	check(t, "inline-title-action-beside-text", action.X >= heading.X+heading.W-1 && near(action.Y, heading.Y))
	tt.SetSize(70, 700)
	settle(tt)
	description, action = box(t, tt, "More"), box(t, tt, "Close inline")
	check(t, "inline-title-narrow-action-below", action.Y >= description.Y+description.H+tr.Metrics().Settings().SectionSkip()-1)
}

func TestTitleFreezesContentUntilModeTransitionEnds(t *testing.T) {
	tr := theme.Light()
	hidden := false
	title := "Original heading"
	var visual *titleVisual
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		e := PageTitle(c, title, PageTitleOptions{Hidden: hidden})
		visual = ui.Local(e, "mode", func() titleVisual { return titleVisual{} })
	}, 320, 80)
	settle(tt)
	hidden = true
	tt.Frame()
	tt.Advance(tr.Motion().PageTitle().Duration() / 2)
	tt.Frame()
	before := string(tt.Image().Pix)
	title = "Changed heading"
	tt.Frame()
	check(t, "title-mid-fade-freezes-content", before == string(tt.Image().Pix))
	hidden = false
	tt.Frame()
	tt.Advance(tr.Motion().PageTitle().Duration())
	tt.Frame()
	check(t, "title-reversal-restores-live-content", tt.HasText(title) && visual.snapshot == nil && !tt.FramePending())
}

func TestProfileTabsProtectPageVerticalScrolling(t *testing.T) {
	tr := theme.Light()
	var recent, now time.Time
	var visual *stripLocal
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		now = c.Now()
		e := Tabs(c, []TabItem{{ID: "a", Label: "A sufficiently long first label"}, {ID: "b", Label: "A sufficiently long second label"}}, "", TabsOptions{Variant: ProfileTabs, LastVerticalScroll: recent})
		visual = ui.Local(e, "strip", func() stripLocal { return stripLocal{} })
	}, 180, 80)
	settle(tt)
	r := box(t, tt, "A sufficiently long first label")
	x, y := r.X+min(r.W/2, 40), r.Y+r.H/2
	tt.Move(x, y)
	recent = now
	tt.Frame()
	tt.Wheel(x, y, 0, 40)
	check(t, "profile-recent-page-wheel-protected", visual.scroll.X == 0)
	tt.Advance(tr.Motion().ProfileTabsWheelGrace().Duration())
	tt.Wheel(x, y, 0, 40)
	check(t, "profile-aimed-wheel-after-grace", visual.scroll.X > 0)
	before := visual.scroll.X
	tt.Scroll(x, y, 0, 40)
	check(t, "profile-vertical-touchpad-keeps-page-axis", visual.scroll.X == before)
	tt.Scroll(x, y, 30, 0)
	check(t, "profile-horizontal-touchpad-scrolls", visual.scroll.X > before)
}

func TestPendingSegmentsCancelWhenUnavailable(t *testing.T) {
	for _, mode := range []string{"control-disabled", "item-disabled", "removed", "reordered"} {
		t.Run(mode, func(t *testing.T) {
			items := []TabItem{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Beta"}}
			disabled := false
			calls, last := 0, ""
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, theme.Light())
				SettingsSlider(c, items, "a", SegmentsOptions{Disabled: disabled, OnSelect: func(id string) { calls++; last = id }})
			}, 320, 80)
			settle(tt)
			click(t, tt, "Beta")
			switch mode {
			case "control-disabled":
				disabled = true
			case "item-disabled":
				items[1].Disabled = true
			case "removed":
				items = items[:1]
			case "reordered":
				items[0], items[1] = items[1], items[0]
			}
			tt.Frame()
			tt.Advance(theme.Light().Motion().Segments().Duration())
			tt.Frame()
			if mode == "reordered" {
				check(t, "segments-pending-follows-id-on-reorder", calls == 1 && last == "b")
			} else {
				check(t, "segments-invalidated-pending-cancels", calls == 0)
			}
		})
	}
}

func TestDisabledControlsApplyOpacityOnce(t *testing.T) {
	for kind, name := range []string{"input", "segments", "underline", "profile"} {
		t.Run(name, func(t *testing.T) {
			render := func(disabled bool) *ui.Tester {
				draft := "MMMM"
				items := []TabItem{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Beta"}}
				tt := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c, theme.Light())
					var e ui.Element
					switch kind {
					case 0:
						e = InputField(c, &draft, "Field", InputOptions{Disabled: disabled})
					case 1:
						e = SettingsSlider(c, items, "a", SegmentsOptions{Disabled: disabled})
					default:
						e = Tabs(c, items, "a", TabsOptions{Variant: TabsVariant(kind - 2), Disabled: disabled})
					}
					if !disabled {
						e.Opacity(.5)
					}
				}, 320, 80)
				settle(tt)
				return tt
			}
			actual, reference := render(true), render(false)
			// SET-DISABLED: exactly one inherited mygo 0.5, without nested multiplication.
			check(t, "disabled-opacity-only-once", bytes.Equal(actual.Image().Pix, reference.Image().Pix))
		})
	}
}

func TestOpenDropdownWithdrawnWhenUnavailable(t *testing.T) {
	for _, mode := range []string{"disabled", "empty"} {
		t.Run(mode, func(t *testing.T) {
			s := DropdownState{}
			items := []settings.ChoiceItem{{ID: "a", Title: "Alpha"}, {ID: "b", Title: "Beta"}}
			disabled, calls := false, 0
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, theme.Light())
				Dropdown(c, &s, items, "a", "Dropdown", DropdownOptions{Mode: SearchBox, Disabled: disabled, Choose: settings.ChooseOptions{SearchLabel: "Search", CloseLabel: "Cancel"}, OnSelect: func(string) { calls++ }})
			}, 600, 600)
			click(t, tt, "Dropdown")
			tt.Advance(theme.Light().Motion().Dialog().Duration())
			if mode == "disabled" {
				disabled = true
			} else {
				items = nil
			}
			tt.Frame()
			// paint/dropdown.md#入口: disabling or removing all choices withdraws the picker.
			check(t, "dropdown-unavailable-withdraws-open-picker", !s.chooser.Open)
			tt.Key(0, ui.KeyDown)
			tt.Key(0, ui.KeyEnter)
			check(t, "dropdown-withdrawn-picker-no-late-submit", calls == 0)
		})
	}
}

func TestPaintContoursAndTabGeometry(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(map[bool]string{false: "light", true: "dark"}[dark], func(t *testing.T) {
			tr := theme.Light()
			if dark {
				tr = theme.Dark()
			}
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, tr)
				// Keep the profile island's cast shadow outside the preceding contour samples.
				ui.Column(c).Fill().AlignItems(ui.Start).Gap(2 * tr.Metrics().ProfileTabs().ShadowBlur()).Background(tr.Colors().Window().Background()).Children(func() {
					RoundButton(c.Key("rectangle"), "Rectangle", RoundButtonOptions{FullWidth: 120})
					RoundButton(c.Key("pill"), "Pill", RoundButtonOptions{FullWidth: 120, FullRadius: true})
					Tabs(c.Key("underline"), []TabItem{{ID: "a", Label: "Line A"}, {ID: "b", Label: "Line B"}}, "a", TabsOptions{}).Label("Underline frame")
					Tabs(c.Key("profile"), []TabItem{{ID: "a", Label: "Profile A"}, {ID: "b", Label: "Profile B"}}, "a", TabsOptions{Variant: ProfileTabs}).Label("Profile frame")
				})
			}, 360, 260)
			settle(tt)
			pixel := func(r ui.Rect, x, y float32) color.RGBA { return tt.Image().RGBAAt(int(r.X+x), int(r.Y+y)) }
			background := rgba(tr.Colors().Window().Background())
			primary := rgba(tr.Colors().Control().PrimaryBackground())
			rect, pill := box(t, tt, "Rectangle"), box(t, tt, "Pill")
			// paint/round-button.md#几何与层次; widgets.style:716,728,736.
			radius := tr.Metrics().Dialog().ButtonRadius()
			check(t, "round-default-corner-four-dip", pixel(rect, 0, 0) == background && pixel(rect, radius, 1) == primary)
			check(t, "round-fullradius-contour", pixel(pill, radius, 1) == background && pixel(pill, pill.H/2, 1) == primary)
			line, first := box(t, tt, "Underline frame"), box(t, tt, "Line A")
			lm := tr.Metrics().Tabs()
			// paint/tabs.md#下划线变体; widgets.style:1193-1204. Six-DIP stroke clips to three visible DIP.
			check(t, "tabs-underline-sourced-frame", near(line.H, lm.Height()))
			check(t, "tabs-underline-visible-three-dip", pixel(line, first.W/2, lm.BarTop()-1) == background && pixel(line, first.W/2, lm.Height()-1) == rgba(tr.Colors().Control().SliderBackgroundActive()))
			profile, item := box(t, tt, "Profile frame"), box(t, tt, "Profile A")
			pm := tr.Metrics().ProfileTabs()
			var width float32
			measureTest := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, tr)
				width, _ = measure(c, "Profile A", tr.Typography().ContentTitle(), 0)
			}, 20, 20)
			measureTest.Frame()
			// paint/tabs.md#info岛状变体; info.style:719-723. Natural text+32, island36, active28.
			check(t, "profile-natural-width-and-insets", near(profile.H, pm.Height()) && near(item.W, width+2*pm.TabPadding()) && near(item.X-profile.X, pm.MarginLeft()) && near(item.Y-profile.Y, pm.MarginTop()) && near(item.H, pm.Height()-pm.MarginTop()-pm.MarginBottom()))
			check(t, "profile-active-inset-paint", pixel(item, item.W/2, pm.ActiveInset()-1) == background && pixel(item, item.W/2, pm.ActiveInset()+1) == rgba(tr.Colors().Control().SurfaceBackgroundHover()))
		})
	}
}

func TestStripGeometryTracksThemeEdits(t *testing.T) {
	tr := theme.Light()
	items := []TabItem{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Beta"}}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ui.Column(c).FillWidth().Children(func() {
			Tabs(c.Key("profile"), items, "a", TabsOptions{Variant: ProfileTabs})
			SettingsSlider(c.Key("segments"), []TabItem{{ID: "one", Label: "One"}}, "one", SegmentsOptions{})
		})
	}, 360, 180)
	settle(tt)
	before := box(t, tt, "Alpha").W
	padding := tr.Metrics().ProfileTabs().TabPadding()
	var err error
	tr, err = theme.Compile(theme.Definition{Version: theme.Version, Base: theme.LightBase, Metrics: map[string]float32{"profileTabs.tabPadding": padding * 2}})
	if err != nil {
		t.Fatal(err)
	}
	settle(tt)
	// paint/tabs.md#info岛状变体; info.style:722. Geometry must invalidate on token edits without text/width changes.
	check(t, "profile-padding-theme-edit-reflows", near(box(t, tt, "Alpha").W, before+2*padding))
	tr, err = theme.Compile(theme.Definition{Version: theme.Version, Base: theme.LightBase, Metrics: map[string]float32{"profileTabs.tabPadding": padding * 2, "segments.strictSkip": 12}})
	if err != nil {
		t.Fatal(err)
	}
	settle(tt)
	var width float32
	measureTest := ui.NewTester(func(c *ui.Context) { theme.Apply(c, tr); width, _ = measure(c, "One", tr.Typography().Body(), 0) }, 20, 20)
	measureTest.Frame()
	// paint/settings-slider.md#几何; the explicit theme override is a test fixture.
	check(t, "segments-strict-skip-theme-override", near(box(t, tt, "One").W, width+tr.Metrics().Segments().StrictSkip()))
}

func TestSiblingDropdownsKeepIndependentSelection(t *testing.T) {
	var states [2]DropdownState
	selected := [2]string{"a", "a"}
	calls := [2]int{}
	items := []settings.ChoiceItem{{ID: "a", Title: "Alpha"}, {ID: "b", Title: "Beta"}}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		ui.Column(c).FillWidth().Children(func() {
			for i, label := range []string{"First", "Second"} {
				Dropdown(c.Key(label), &states[i], items, selected[i], label, DropdownOptions{SettingsRow: i == 1, Mode: SearchBox, Choose: settings.ChooseOptions{SearchLabel: "Search", CloseLabel: "Cancel"}, OnSelect: func(id string) { selected[i] = id; calls[i]++ }})
			}
		})
	}, 600, 600)
	click(t, tt, "Second")
	tt.Advance(theme.Light().Motion().Dialog().Duration())
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	// paint/dropdown.md#入口: distinct caller identities own independent choice state.
	check(t, "dropdown-siblings-select-own-id", selected == [2]string{"a", "b"} && calls == [2]int{0, 1} && !states[0].chooser.Open && !states[1].chooser.Open)
}
