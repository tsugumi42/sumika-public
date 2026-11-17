package settings

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
)

func TestRowRippleClockAndKeyboard(t *testing.T) {
	tr := theme.Light()
	count := 0
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		Button(c, "Ripple", ButtonOptions{OnActivate: func() { count++ }})
	}, 320, 80)
	r := rect(t, test, "Ripple")
	test.Press(1, r.Y+1)
	// paint/settings-button.md#悬停按下禁用与动效: quint expansion 650ms,
	// then a held wave; release has an independent 200ms linear fade.
	test.Advance(tr.Motion().RippleShow().Duration() / 2)
	test.Frame()
	check(t, "ripple-expands-from-pointer", pixel(test, 150, r.Y+2) == rgba(tr.Colors().Window().BackgroundPressed()) && pixel(test, 318, r.Y+2) == rgba(tr.Colors().Window().BackgroundHover()))
	test.Advance(tr.Motion().RippleShow().Duration() / 2)
	test.Frame()
	check(t, "ripple-reaches-farthest-corner", pixel(test, 318, r.Y+2) == rgba(tr.Colors().Window().BackgroundPressed()))
	test.Release(1, r.Y+1)
	test.Advance(tr.Motion().RippleHide().Duration() / 2)
	test.Frame()
	expected := tr.Colors().Window().BackgroundHover().Mix(tr.Colors().Window().BackgroundPressed(), .5)
	actual := pixel(test, 150, r.Y+2)
	check(t, "ripple-linear-release", abs(float32(actual.R)-float32(expected.R)) <= 1 && abs(float32(actual.G)-float32(expected.G)) <= 1 && abs(float32(actual.B)-float32(expected.B)) <= 1)
	test.Advance(tr.Motion().RippleHide().Duration() / 2)
	test.Frame()
	check(t, "ripple-stops-at-hide-duration", pixel(test, 150, r.Y+2) == rgba(tr.Colors().Window().BackgroundHover()) && !test.FramePending())
	test.Key(0, ui.KeyEnter)
	test.Frame()
	check(t, "keyboard-without-pointer-ripple", count == 2 && pixel(test, 150, r.Y+2) == rgba(tr.Colors().Window().BackgroundHover()))
}
func TestSettingsScaleDirectionAndTypography(t *testing.T) {
	for _, tr := range []*theme.Theme{theme.Light(), theme.Dark()} {
		for _, scale := range []float32{1, 1.25, 1.5} {
			t.Run(fmt.Sprintf("dark=%v/scale=%g", tr.Dark(), scale), func(t *testing.T) {
				var titleHeight float32
				test := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c, tr)
					_, titleHeight = measure(c, "Heading", tr.Typography().SettingsSectionTitle(), 0)
					ui.Column(c).FillWidth().Children(func() {
						SectionTitle(c, "Heading").Label("Heading area")
						ToggleRow(c, "On", true, ToggleOptions{})
						Radio(c, "Radio", true, ChoiceOptions{})
						Check(c, "Check", true, ChoiceOptions{})
						Divider(c, "", DividerOptions{HideTop: true, HideBottom: true}).Label("Band")
					})
				}, 320, 250)
				test.SetScale(scale)
				test.SetDirection(ui.RightToLeft)
				test.Frame()
				m := tr.Metrics().Settings()
				view := rect(t, test, "On switch")
				mark := rect(t, test, "Radio mark")
				band := rect(t, test, "Band")
				// paint/settings-section.md#坐标来源与边界 and each component's RTL rule.
				check(t, "scaled-dip-slot", near(view.W, 2*m.ToggleBorder()+m.ToggleDiameter()+m.ToggleTravel()) && near(view.X, m.ToggleRight()))
				check(t, "scaled-rtl-radio", near(320-mark.X-mark.W, m.CheckPaddingLeft()))
				check(t, "scaled-theme-band", containsInk(test, ui.Rect{X: band.X * scale, Y: band.Y * scale, W: band.W * scale, H: band.H * scale}, tr.Colors().Dialog().DividerBackground()))
				test.SetPreferences(ui.Preferences{TextScale: 1.5, ReduceMotion: true})
				test.Frame()
				check(t, "text-scale-applied-once", near(rect(t, test, "Heading area").H, m.SectionTitlePaddingTop()+titleHeight+m.SectionTitlePaddingBottom()))
			})
		}
	}
	custom, err := theme.Compile(theme.Definition{Version: theme.Version, Base: theme.LightBase, Typography: map[string]theme.TextDefinition{"settingsSectionTitle": {Size: new(float32(24)), Weight: new(400)}, "dialogBody": {Size: new(float32(24))}}, Colors: map[string]string{"window.activeTextForeground": "#946311"}})
	if err != nil {
		t.Fatal(err)
	}
	var line float32
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, custom)
		_, line = measure(c, "Custom", custom.Typography().SettingsSectionTitle(), 0)
		ui.Column(c).FillWidth().Children(func() { SectionTitle(c, "Custom").Label("custom heading"); Button(c, "Large row", ButtonOptions{}) })
	}, 320, 180)
	r := rect(t, test, "custom heading")
	m := custom.Metrics().Settings()
	check(t, "custom-heading-font-and-padding", near(r.H, line+m.SectionTitlePaddingTop()+m.SectionTitlePaddingBottom()) && containsInk(test, r, custom.Colors().Window().ActiveTextForeground()))
	check(t, "large-row-does-not-clip-font", rect(t, test, "Large row").H > m.ButtonNoIconPaddingTop()+m.ButtonContentHeight()+m.ButtonNoIconPaddingBottom())
}
func TestNumberCaretCorrection(t *testing.T) {
	s := NumberState{Text: "123"}
	caret := 0
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		NumberInput(c, &s, "Number", NumberOptions{Limit: 65535})
		_, caret = c.Resolve(s.input).TextSelection()
	}, 240, 80)
	click(t, test, "Number")
	test.Key(0, ui.KeyHome)
	test.Key(0, ui.KeyRight)
	test.Type("a4")
	test.Frame()
	// paint/numeric-input.md#数字编辑与提交边界: strip at the caret, not at end.
	check(t, "number-middle-edit-caret", s.Text == "1423" && caret == 2)
	test.Key(0, ui.KeyHome)
	test.Type("9")
	test.Frame()
	check(t, "number-rejected-edit-restores-caret", s.Text == "1423" && caret == 0)
}
