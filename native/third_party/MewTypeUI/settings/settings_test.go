package settings

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func check(t *testing.T, id string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", id)
	}
}
func near(a, b float32) bool { return abs(a-b) <= 1 }
func abs(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
func rect(t *testing.T, test *ui.Tester, label string) ui.Rect {
	t.Helper()
	r, ok := test.Find(label)
	if !ok {
		t.Fatalf("missing %q: %v", label, test.Texts())
	}
	return r
}
func click(t *testing.T, test *ui.Tester, label string) {
	t.Helper()
	if err := test.Click(label); err != nil {
		t.Fatal(err)
	}
}
func pixel(test *ui.Tester, x, y float32) color.RGBA { return test.Image().RGBAAt(int(x), int(y)) }
func rgba(c ui.Color) color.RGBA                     { return color.RGBA{R: c.R, G: c.G, B: c.B, A: c.A} }
func containsInk(test *ui.Tester, r ui.Rect, ink ui.Color) bool {
	im := test.Image()
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			if im.RGBAAt(x, y) == rgba(ink) {
				return true
			}
		}
	}
	return false
}

func TestSectionsAndButtonGeometry(t *testing.T) {
	tr := theme.Light()
	m := tr.Metrics().Settings()
	calls := 0
	var titleLine float32
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		_, titleLine = measure(c, "Section", tr.Typography().SettingsSectionTitle(), 0)
		ui.Column(c).FillWidth().Children(func() {
			SectionTitle(c, "Section").Label("heading")
			Skip(c).Label("skip1")
			Skip(c).Label("skip2")
			Button(c, "Plain", ButtonOptions{Value: "Value", OnActivate: func() { calls++ }})
			Button(c, "Icon", ButtonOptions{Icon: checkIcon, Arrow: true, Value: "Current"})
			Divider(c, "", DividerOptions{HideTop: true, HideBottom: true}).Label("band")
			Divider(c, "A description that wraps naturally inside the full width of this grey divider.", DividerOptions{}).Label("description")
		})
	}, 320, 400)
	test.Frame()
	heading := rect(t, test, "heading")
	skip1, skip2 := rect(t, test, "skip1"), rect(t, test, "skip2")
	plain, icon := rect(t, test, "Plain"), rect(t, test, "Icon")
	band := rect(t, test, "band")
	// paint/settings-section.md#小标题/#skip与页面内边距/#分隔带.
	check(t, "section-height", near(heading.H, m.SectionTitlePaddingTop()+titleLine+m.SectionTitlePaddingBottom()))
	check(t, "adjacent-skips", near(skip2.Y-skip1.Y, m.SectionSkip()) && near(plain.Y-skip1.Y, 2*m.SectionSkip()))
	check(t, "band-height-width", near(band.H, m.DividerHeight()) && near(band.W, 320) && near(band.X, 0))
	check(t, "band-color", pixel(test, 160, band.Y+band.H/2) == rgba(tr.Colors().Dialog().DividerBackground()))
	// paint/settings-button.md#行图标标题.
	check(t, "plain-row-height", near(plain.H, m.ButtonNoIconPaddingTop()+m.ButtonContentHeight()+m.ButtonNoIconPaddingBottom()))
	check(t, "icon-row-height", near(icon.H, m.ButtonPaddingTop()+m.ButtonContentHeight()+m.ButtonPaddingBottom()))
	value := rect(t, test, "Value")
	check(t, "value-right-inset", near(plain.X+plain.W-value.X-value.W, m.ButtonValueRight()))
	check(t, "value-active-color", containsInk(test, value, tr.Colors().Window().ActiveTextForeground()))
	check(t, "button-title-font", containsInk(test, ui.Rect{X: m.ButtonNoIconPaddingLeft(), Y: plain.Y + m.ButtonNoIconPaddingTop(), W: 50, H: tr.Typography().DialogBody().Size() * 2}, tr.Colors().Window().BoldForeground()))
	test.Move(plain.X+1, plain.Y+1)
	check(t, "hover-immediate", pixel(test, 1, plain.Y+1) == rgba(tr.Colors().Window().BackgroundHover()))
	click(t, test, "Plain")
	check(t, "button-click", calls == 1)
	test.Key(0, ui.KeyEnter)
	check(t, "button-keyboard", calls == 2)
	test.SetSize(70, 400)
	test.Frame()
	check(t, "overflow-hides-value", !test.HasText("Value"))
}
func TestToggleGeometryAnimationAndDisabled(t *testing.T) {
	tr := theme.Light()
	m := tr.Metrics().Settings()
	checked, disabled, locked := true, false, false
	calls := 0
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ToggleRow(c, "Toggle", checked, ToggleOptions{Disabled: disabled, Locked: locked, LockIcon: checkIcon, OnChange: func(v bool) {
			calls++
			if !locked {
				checked = v
			}
		}})
	}, 320, 70)
	view := rect(t, test, "Toggle switch")
	// paint/settings-toggle.md#开关与行的位置/#轨道和圆钮.
	check(t, "toggle-logical-slot", near(view.W, 2*m.ToggleBorder()+m.ToggleDiameter()+m.ToggleTravel()) && near(view.H, 2*m.ToggleBorder()+m.ToggleDiameter()))
	check(t, "toggle-right-and-top", near(320-view.X-view.W, m.ToggleRight()) && near(view.Y, 9))
	on := tr.Colors().Window().BackgroundActive()
	off := tr.Colors().Menu().IconForeground()
	trackX, trackY := view.X+m.ToggleBorder()+m.ToggleTrackInset()+m.ToggleDiameter()/2, view.Y+view.H/2
	check(t, "initial-on-no-animation", pixel(test, trackX, trackY) == rgba(on) && !test.FramePending())
	test.Move(0, 60)
	checked = false
	test.Frame()
	test.Advance(tr.Motion().SettingsToggle().Duration() / 2)
	test.Frame()
	check(t, "toggle-linear-midpoint", pixel(test, view.X+2*m.ToggleBorder()+m.ToggleTrackInset(), view.Y+view.H/2) == rgba(off.Mix(on, .5)))
	checked = true
	test.Frame()
	test.Advance(tr.Motion().SettingsToggle().Duration() / 2)
	test.Frame()
	check(t, "toggle-reverse-from-current", pixel(test, view.X+2*m.ToggleBorder()+m.ToggleTrackInset(), view.Y+view.H/2) == rgba(off.Mix(on, .75)))
	test.Advance(tr.Motion().SettingsToggle().Duration() / 2)
	test.Frame()
	check(t, "toggle-exact-duration", !test.FramePending())
	locked = true
	test.Frame()
	click(t, test, "Toggle")
	check(t, "locked-reports-intent", calls == 1 && checked)
	disabled = true
	test.Frame()
	click(t, test, "Toggle")
	check(t, "disabled-blocks-intent", calls == 1)
	test.SetDirection(ui.RightToLeft)
	test.Frame()
	view = rect(t, test, "Toggle switch")
	check(t, "toggle-rtl-slot", near(view.X, m.ToggleRight()))
}
func TestCheckAndRadioInteraction(t *testing.T) {
	tr := theme.Light()
	m := tr.Metrics().Choice()
	sm := tr.Metrics().Settings()
	checked := false
	selected := "a"
	radioCalls := 0
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ui.Column(c).FillWidth().Children(func() {
			Check(c, "Check", checked, ChoiceOptions{Error: !checked, OnChange: func(v bool) { checked = v }})
			Radio(c.Key("a"), "Radio A", selected == "a", ChoiceOptions{OnChange: func(bool) { radioCalls++; selected = "a" }})
			Radio(c.Key("b"), "Radio B", selected == "b", ChoiceOptions{OnChange: func(bool) { radioCalls++; selected = "b" }})
			Check(c, "Disabled", true, ChoiceOptions{Disabled: true, OnChange: func(bool) { checked = false }})
		})
	}, 320, 230)
	test.Frame()
	r := rect(t, test, "Check")
	mark := rect(t, test, "Check mark")
	// paint/radio-check.md#在行中的位置; lib_ui/widgets/widgets.style:845-846.
	check(t, "check-row-height", near(r.H, sm.CheckPaddingTop()+m.CheckDiameter()+sm.CheckPaddingBottom()))
	check(t, "check-slot-position", near(mark.X, sm.CheckPaddingLeft()) && near(mark.Y, sm.CheckPaddingTop()) && near(mark.W, m.CheckDiameter()))
	click(t, test, "Check")
	test.Advance(tr.Motion().Choice().Duration())
	test.Frame()
	check(t, "check-label-click", checked)
	check(t, "check-selected-fill", containsInk(test, mark, tr.Colors().Window().BackgroundActive()))
	click(t, test, "Radio A")
	check(t, "selected-radio-stays-selected", selected == "a" && radioCalls == 0)
	click(t, test, "Radio B")
	test.Advance(tr.Motion().Choice().Duration())
	test.Frame()
	check(t, "radio-selects-host-id", selected == "b" && radioCalls == 1)
	r = rect(t, test, "Radio B mark")
	check(t, "radio-dot-diameter", pixel(test, r.X+r.W/2, r.Y+r.H/2) == rgba(tr.Colors().Window().BackgroundActive()) && pixel(test, r.X+r.W/2-6, r.Y+r.H/2) != rgba(tr.Colors().Window().BackgroundActive()))
	click(t, test, "Disabled")
	check(t, "disabled-check-stable", checked)
}
func chooserFixture() []ChoiceItem {
	return []ChoiceItem{{ID: "a", Title: "Café Aurora", Subtitle: "Vision · 128K", GroupID: "one", GroupTitle: "One"}, {ID: "disabled", Title: "Blocked", Subtitle: "Vision", GroupID: "one", GroupTitle: "One", Disabled: true}, {ID: "b", Title: "Beta", Subtitle: "Audio · 64K", GroupID: "two", GroupTitle: "Two"}, {ID: "c", Title: "Gamma", Subtitle: "Vision · 256K", GroupID: "two", GroupTitle: "Two"}}
}
func TestChooserFilteringNavigationAndDismissal(t *testing.T) {
	tr := theme.Light()
	s := ChooseState{}
	items := chooserFixture()
	selected := "b"
	chosen := ""
	closes := 0
	options := ChooseOptions{SearchLabel: "Search", SearchPlaceholder: "Find", ClearLabel: "Clear", EmptyLabel: "Empty", CloseLabel: "Close", OnSelect: func(id string) { chosen = id; selected = id }, OnClose: func() { closes++ }}
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ui.Button(c, "Open").OnClick(s.Show)
		ChooseBox(c, &s, items, selected, "Chooser", options)
	}, 640, 650)
	click(t, test, "Open")
	test.Advance(tr.Motion().Dialog().Duration())
	test.Frame()
	panel, search := rect(t, test, "Chooser"), rect(t, test, "Chooser search area")
	// paint/choose-box.md#外壳矩形与绘制层次/#搜索输入区.
	check(t, "chooser-width", near(panel.W, tr.Metrics().Dialog().Width()))
	check(t, "chooser-search-height", near(search.H, tr.Metrics().Choose().SearchPaddingTop()+tr.Metrics().Choose().SearchFieldMinHeight()+tr.Metrics().Choose().SearchPaddingBottom()))
	check(t, "chooser-initial-search-focus", test.Focused("Search"))
	test.Key(0, ui.KeyDown)
	test.Key(0, ui.KeyDown)
	check(t, "chooser-skips-disabled-crosses-group", s.high == "b" && selected == "b" && chosen == "")
	test.Key(0, ui.KeyUp)
	test.Key(0, ui.KeyUp)
	check(t, "chooser-up-clears-first", s.high == "")
	test.Type("CAFE vis")
	check(t, "chooser-accent-prefix-multiple-terms", len(s.cache.matches) == 1 && s.cache.items[s.cache.matches[0]].ID == "a" && selected == "b")
	check(t, "chooser-filter-keeps-shell", near(rect(t, test, "Chooser").H, panel.H))
	test.Key(0, ui.KeyEnter)
	check(t, "chooser-enter-first-result", chosen == "a" && selected == "a" && s.Open)
	click(t, test, "Clear")
	check(t, "chooser-clear-restores-focus", s.Query == "" && test.Focused("Search") && len(s.cache.matches) == len(items))
	test.Type("urora")
	check(t, "chooser-rejects-substring", len(s.cache.matches) == 0 && test.HasText("Empty"))
	chosen = ""
	test.Key(0, ui.KeyEnter)
	check(t, "chooser-empty-does-not-submit", chosen == "")
	test.Key(0, ui.KeyEscape)
	check(t, "chooser-escape-once", !s.Open && closes == 1)
	test.Advance(tr.Motion().Dialog().Duration())
	test.Frame()
	click(t, test, "Open")
	test.Advance(tr.Motion().Dialog().Duration())
	test.Frame()
	test.ClickAt(0, 0)
	check(t, "chooser-scrim-closes", !s.Open && closes == 2)
	test.Advance(tr.Motion().Dialog().Duration())
	test.Frame()
	click(t, test, "Open")
	test.Advance(tr.Motion().Dialog().Duration())
	test.Frame()
	click(t, test, "Café Aurora")
	check(t, "chooser-pointer-reports-id", chosen == "a")
	test.SetSize(220, 300)
	test.Frame()
	check(t, "chooser-clamps-parent", rect(t, test, "Chooser").W <= 220 && rect(t, test, "Close").Y < 300)
}
func TestChooserLongListIdentityAndScroll(t *testing.T) {
	tr := theme.Light()
	s := ChooseState{}
	s.Show()
	items := make([]ChoiceItem, 200)
	selected := "model-199"
	chosen := ""
	for i := range items {
		items[i] = ChoiceItem{ID: fmt.Sprintf("model-%d", i), Title: fmt.Sprintf("Model %03d", i), Subtitle: "Vision 128K", GroupID: fmt.Sprint(i / 40), GroupTitle: fmt.Sprintf("Provider %d", i/40)}
	}
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ChooseBox(c, &s, items, selected, "Models", ChooseOptions{SearchLabel: "Search", ClearLabel: "Clear", EmptyLabel: "Empty", CloseLabel: "Close", OnSelect: func(id string) { chosen = id }})
	}, 640, 700)
	test.Advance(tr.Motion().Dialog().Duration())
	test.Frame()
	search := rect(t, test, "Models search area")
	for range 199 {
		test.Key(0, ui.KeyDown)
	}
	check(t, "long-chooser-navigation-scrolls", s.scroll.Y > 0 && s.high == "model-198" && test.HasText("Model 198"))
	check(t, "search-stays-fixed", rect(t, test, "Models search area") == search)
	test.Key(0, ui.KeyEnter)
	check(t, "long-chooser-commits-visible-id", chosen == "model-198" && selected == "model-199")
	s.Query = "Model 199"
	test.Frame()
	test.Key(0, ui.KeyEnter)
	check(t, "filtered-id-not-index", chosen == "model-199")
	items[199].Title = "Renamed"
	s.Query = "Ren"
	test.Frame()
	test.Key(0, ui.KeyEnter)
	check(t, "catalog-edit-invalidates-cache", chosen == "model-199" && len(s.cache.matches) == 1)
}
func TestNumberEditingAndTransitions(t *testing.T) {
	tr := theme.Light()
	m := tr.Metrics().Input()
	s := NumberState{}
	zero := false
	errorState := false
	submitted := ""
	cancelled := 0
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ui.Column(c).FillWidth().Children(func() {
			NumberInput(c, &s, "Number", NumberOptions{Placeholder: "Port", Limit: 65535, AllowZero: zero, Error: errorState, OnSubmit: func(v string) { submitted = v }, OnCancel: func() { cancelled++ }}).Label("Number area")
			ui.Button(c, "Outside")
		})
	}, 240, 130)
	field := rect(t, test, "Number area")
	label := rect(t, test, "Port")
	// paint/numeric-input.md#字段矩形与绘制: 55 high, 28 top, 20 rise.
	check(t, "number-field-height", near(field.H, m.HeightMin()))
	check(t, "number-placeholder-rest", near(label.Y, m.TextTop()))
	click(t, test, "Number")
	test.Advance(tr.Motion().InputField().Duration() / 2)
	test.Frame()
	check(t, "number-placeholder-half-rise", near(rect(t, test, "Port").Y, m.TextTop()-m.PlaceholderRise()/2))
	test.Advance(tr.Motion().InputField().Duration() / 2)
	test.Frame()
	check(t, "number-placeholder-full-rise", near(rect(t, test, "Port").Y, m.TextTop()-m.PlaceholderRise()))
	test.Type("12a3")
	check(t, "number-strips-nondigits", s.Text == "123")
	test.Key(ui.Ctrl, ui.KeyA)
	test.Type("65535")
	test.Type("9")
	check(t, "number-rejects-over-limit", s.Text == "65535")
	test.Key(ui.Ctrl, ui.KeyA)
	test.Type("0")
	check(t, "number-default-zero-clears", s.Text == "")
	zero = true
	test.Frame()
	test.Type("0")
	check(t, "number-explicit-zero-retained", s.Text == "0")
	test.Key(0, ui.KeyEnter)
	test.Key(0, ui.KeyEscape)
	check(t, "number-submit-cancel", submitted == "0" && cancelled == 1)
	test.Key(ui.Ctrl, ui.KeyA)
	test.Type(strings.Repeat("9", 40))
	check(t, "number-overflow-clears", s.Text == "")
	errorState = true
	test.Frame()
	test.Advance(tr.Motion().InputField().Duration())
	test.Frame()
	check(t, "number-error-line-color", pixel(test, 120, field.Y+field.H-2) == rgba(tr.Colors().Control().ActiveLineForegroundError()))
	click(t, test, "Outside")
	test.Advance(tr.Motion().InputField().Duration())
	test.Frame()
	check(t, "number-empty-unfocused-placeholder-rest", near(rect(t, test, "Port").Y, m.TextTop()))
}
func TestDiscreteSliderInteraction(t *testing.T) {
	tr := theme.Light()
	value := 2
	commits := 0
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ui.Column(c).FillWidth().Children(func() {
			LimitValue(c, "Limit", "50 MiB")
			LimitSlider(c, "Slider", value, 5, SliderOptions{OnChange: func(v int) { value = v }, OnCommit: func(int) { commits++ }})
		})
	}, 320, 90)
	// paint/numeric-input.md#自动下载大小右值行加离散滑块.
	r := rect(t, test, "Slider")
	m := tr.Metrics().Settings()
	check(t, "slider-layout-padding", near(r.X, m.SliderPaddingLeft()) && near(r.Y, m.LimitPaddingTop()+m.ButtonContentHeight()+m.SliderPaddingTop()) && near(r.H, tr.Metrics().Slider().MarkerHeight()))
	test.ClickAt(r.X+r.W-1, r.Y+r.H/2)
	check(t, "slider-pointer-last-stop", value == 4)
	test.Key(0, ui.KeyHome)
	check(t, "slider-home-first-stop", value == 0)
	test.Key(0, ui.KeyRight)
	check(t, "slider-key-next-distinct-stop", value == 1)
	test.Key(0, ui.KeyEnd)
	check(t, "slider-end-and-commit", value == 4 && commits >= 3)
}
