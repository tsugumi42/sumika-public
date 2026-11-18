package settings

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
)

func TestChooserTabAndCatalogRemoval(t *testing.T) {
	s := ChooseState{}
	s.Show()
	items := chooserFixture()
	chosen := ""
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		ChooseBox(c, &s, items, "c", "Chooser", ChooseOptions{SearchLabel: "Search", ClearLabel: "Clear", CloseLabel: "Close", OnSelect: func(id string) { chosen = id }})
	}, 640, 650)
	test.Advance(theme.Light().Motion().Dialog().Duration())
	test.Frame()
	test.Key(0, ui.KeyTab)
	// paint/choose-box.md#键盘焦点选择提交: Tab enters the checked ID,
	// while arrows/Home/End only change highlight and reveal the whole row.
	check(t, "chooser-tab-prefers-checked-id", s.high == "c" && test.Focused("Chooser list"))
	test.Key(0, ui.KeyHome)
	check(t, "chooser-list-home", s.high == "a")
	test.Key(0, ui.KeyEnd)
	test.Key(0, ui.KeySpace)
	check(t, "chooser-list-end-space", s.high == "c" && chosen == "c")
	items = items[:3]
	test.Frame()
	chosen = ""
	test.Key(0, ui.KeyEnter)
	check(t, "chooser-removal-clears-stale-highlight", chosen == "a")
}
func TestNumericWholeFieldAndInterruptedFocus(t *testing.T) {
	s := NumberState{}
	tr := theme.Light()
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		ui.Column(c).FillWidth().Children(func() { NumberInput(c, &s, "Number", NumberOptions{Placeholder: "Label"}); ui.Button(c, "Outside") })
	}, 240, 110)
	// paint/numeric-input.md#字段矩形与绘制: the whole field accepts focus;
	// regaining focus during fade restores opacity without shrinking the line.
	test.ClickAt(30, 5)
	test.Advance(tr.Motion().InputField().Duration())
	test.Frame()
	check(t, "number-whole-field-focus", test.Focused("Number"))
	click(t, test, "Outside")
	test.Advance(tr.Motion().InputField().Duration() / 2)
	test.Frame()
	test.ClickAt(30, 5)
	test.Frame()
	check(t, "number-refocus-keeps-full-line", pixel(test, 230, tr.Metrics().Input().HeightMin()-2) != rgba(tr.Colors().Control().Border()))
	test.Advance(tr.Motion().InputField().Duration())
	test.Frame()
	check(t, "number-refocus-completes", pixel(test, 230, tr.Metrics().Input().HeightMin()-2) == rgba(tr.Colors().Control().Focus()))
}

func TestCheckingClearsErrorImmediately(t *testing.T) {
	checked := false
	tr := theme.Light()
	test := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		Check(c, "Error check", checked, ChoiceOptions{Error: true, OnChange: func(v bool) { checked = v }})
	}, 320, 70)
	click(t, test, "Error check")
	r := rect(t, test, "Error check mark")
	// paint/radio-check.md#交互状态和动效: checking clears the override
	// immediately, while the checked fill still follows its 120ms timeline.
	check(t, "checked-clears-error-immediately", pixel(test, r.X+1, r.Y+r.H/2) == rgba(tr.Colors().Control().CheckboxForeground()))
}
