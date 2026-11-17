package overlay

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestNativeMenuContent(t *testing.T) {
	for _, icons := range []bool{false, true} {
		name := "plain"
		if icons {
			name = "icons"
		}
		t.Run(name, func(t *testing.T) {
			panel := menuPanel{open: true, focus: true}
			chosen, child := "", ""
			items := []MenuItem{{ID: "copy", Label: "Copy", Checkable: true}, {ID: "disabled", Label: "Disabled", Disabled: true}, {ID: "sep", Separator: true}, {ID: "more", Label: "More", Children: []MenuItem{{ID: "nested", Label: "Nested"}}}, {ID: "delete", Label: "Delete", Danger: true}}
			var line float32
			actions := menuActions{
				selectID:    func(id string) { chosen = id; panel.open = false },
				close:       func() { panel.open = false },
				child:       func(id string, _ ui.Rect, _ bool) { child = id },
				childBounds: func() ui.Rect { return ui.Rect{} },
				done:        func() {},
			}
			tester := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, theme.Light())
				_, line = measure(c, "Mg", theme.T(c).Typography().Menu(), 0)
				panel.render(c, items, MenuOptions{Label: "Actions", Icons: icons}, actions)
			}, 300, 300)
			tester.Advance(theme.Light().Motion().MenuShow().Duration())
			tester.Frame()
			m := theme.Light().Metrics().Menu()
			bottom, pad := m.PaddingBottom(), m.ScrollPadding()
			if icons {
				bottom, pad = m.IconPaddingBottom(), m.IconScrollPadding()
			}
			menu, copyRow, disabled, more := rect(t, tester, "Actions"), rect(t, tester, "Copy"), rect(t, tester, "Disabled"), rect(t, tester, "More")
			// lib_ui widgets.style:969,990,1697,1710; menu_action.cpp:62.
			check(t, "native menu row height", near(copyRow.H, line+m.PaddingTop()+bottom))
			check(t, "native menu padding", near(copyRow.Y-menu.Y, pad))
			check(t, "native menu separator", near(more.Y-disabled.Y-disabled.H, m.SeparatorWidth()+2*m.SeparatorPadding()))
			check(t, "native menu initial focus", tester.Focused("Copy"))
			click(t, tester, "Disabled")
			check(t, "native disabled ignores press", chosen == "" && panel.open)
			tester.Key(0, ui.KeyDown)
			check(t, "native arrows skip disabled", tester.Focused("More"))
			tester.Key(0, ui.KeyRight)
			check(t, "native right requests selected child", child == "more")
			tester.Key(0, ui.KeyEnd)
			tester.Key(0, ui.KeyEnter)
			check(t, "native enter selects leaf ID", chosen == "delete" && !panel.open)
			tester.Advance(theme.Light().Motion().Menu().Duration())
			tester.Frame()
			check(t, "native menu completes exit", !tester.HasText("Copy"))
		})
	}
}

func TestNativeMenuReorderTypeAndRTL(t *testing.T) {
	panel := menuPanel{open: true, focus: true}
	chosen, child := "", ""
	items := []MenuItem{{ID: "alpha", Label: "Alpha"}, {ID: "beta", Label: "Beta"}, {ID: "more", Label: "More", Children: []MenuItem{{ID: "child", Label: "Child"}}}}
	actions := menuActions{selectID: func(id string) { chosen = id }, close: func() { panel.open = false }, child: func(id string, _ ui.Rect, _ bool) { child = id }, childBounds: func() ui.Rect { return ui.Rect{} }, done: func() {}}
	tester := ui.NewTester(func(c *ui.Context) { theme.Apply(c, theme.Light()); panel.render(c, items, MenuOptions{}, actions) }, 300, 300)
	tester.SetDirection(ui.RightToLeft)
	tester.Advance(theme.Light().Motion().MenuShow().Duration())
	tester.Frame()
	tester.Type("b")
	check(t, "native type prefix selects item", tester.Focused("Beta"))
	items[0], items[1] = items[1], items[0]
	tester.Frame()
	tester.Key(0, ui.KeyEnter)
	check(t, "native reorder preserves selected identity", chosen == "beta")
	tester.Key(0, ui.KeyEnd)
	tester.Key(0, ui.KeyLeft)
	check(t, "native RTL opens with left", child == "more")
	tester.Key(0, ui.KeyRight)
	check(t, "native RTL back closes level", !panel.open)
}

func TestNativeMenuMotionAndTextScale(t *testing.T) {
	panel := menuPanel{open: true}
	var line float32
	actions := menuActions{selectID: func(string) {}, close: func() { panel.open = false }, child: func(string, ui.Rect, bool) {}, childBounds: func() ui.Rect { return ui.Rect{} }, done: func() {}}
	tester := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		c.Root().Background(theme.T(c).Colors().Conversation().BackgroundActive())
		_, line = measure(c, "Mg", theme.T(c).Typography().Menu(), 0)
		panel.render(c, []MenuItem{{ID: "a", Label: "Readable"}}, MenuOptions{}, actions)
	}, 300, 300)
	before := inkAt(tester, 10, 10)
	tester.Advance(60 * time.Millisecond)
	tester.Frame()
	check(t, "native menu sourced opacity fraction", before != inkAt(tester, 10, 10))
	tester.Advance(140 * time.Millisecond)
	tester.Frame()
	panel.open = false
	tester.Frame()
	tester.Advance(75 * time.Millisecond)
	tester.Frame()
	check(t, "native menu exit midpoint retained", tester.HasText("Readable"))
	tester.Advance(75 * time.Millisecond)
	tester.Frame()
	check(t, "native menu exit source deadline", !tester.HasText("Readable"))
	panel.open = true
	tester.SetPreferences(ui.Preferences{TextScale: 2, ReduceMotion: true})
	tester.Frame()
	m := theme.Light().Metrics().Menu()
	check(t, "native text scale preserves padding", near(rect(t, tester, "Readable").H, line+m.PaddingTop()+m.PaddingBottom()))
	panel.open = false
	tester.Frame()
	check(t, "native reduced motion closes immediately", !tester.HasText("Readable") && !tester.FramePending())
}

func TestNativeTooltipContent(t *testing.T) {
	tip := nativeTooltip{label: "Details"}
	var tw, line, width, height float32
	tester := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		tw, line = measure(c, tip.label, theme.T(c).Typography().Body(), 0)
		width, height = tooltipSize(c, tip.label)
		ui.Box(c).Size(width, height).Children(func() { tip.render(c) })
	}, 800, 300)
	start := inkAt(tester, 4, 4)
	tester.Advance(theme.Light().Motion().Fade().Duration() / 2)
	tester.Frame()
	middle := inkAt(tester, 4, 4)
	check(t, "native tooltip fade midpoint", middle != start)
	tester.Advance(theme.Light().Motion().Fade().Duration() / 2)
	tester.Frame()
	m := theme.Light().Metrics().Tooltip()
	border := theme.Light().Metrics().Control().BorderWidth()
	// lib_ui widgets.style:1281-1287; tooltip.cpp:99-111.
	check(t, "native tooltip source width", near(width, tw+2*(m.PaddingX()+border)))
	check(t, "native tooltip source height", near(height, line+2*(m.PaddingY()+border)))
	check(t, "native tooltip fade completion", inkAt(tester, 4, 4) != middle)
	check(t, "native tooltip rounded border", inkAt(tester, 0, 0) != inkAt(tester, 4, 4))
	tester.Advance(time.Second)
	tester.Frame()
	check(t, "native tooltip stops animation", !tester.FramePending())
}

func TestNativeMenuEnterWithPanelFocus(t *testing.T) {
	panel := menuPanel{open: true}
	chosen := ""
	items := []MenuItem{{ID: "first", Label: "First"}, {ID: "last", Label: "Last"}}
	actions := menuActions{selectID: func(id string) { chosen = id }, close: func() {}, child: func(string, ui.Rect, bool) {}, childBounds: func() ui.Rect { return ui.Rect{} }, done: func() {}}
	tester := ui.NewTester(func(c *ui.Context) { theme.Apply(c, theme.Light()); panel.render(c, items, MenuOptions{}, actions) }, 300, 300)
	tester.Advance(theme.Light().Motion().MenuShow().Duration())
	tester.Frame()
	tester.Key(0, ui.KeyEnd)
	tester.ClickAt(1, 1)
	tester.Key(0, ui.KeyEnter)
	check(t, "native Enter selects highlight with panel focus", chosen == "last")
}

func TestNativeMenuKeyboardPointerHandoff(t *testing.T) {
	panel := menuPanel{open: true, focus: true}
	chosen := ""
	items := []MenuItem{{ID: "first", Label: "First"}, {ID: "last", Label: "Last"}}
	actions := menuActions{selectID: func(id string) { chosen = id }, close: func() {}, child: func(string, ui.Rect, bool) {}, childBounds: func() ui.Rect { return ui.Rect{} }, done: func() {}}
	tester := ui.NewTester(func(c *ui.Context) { theme.Apply(c, theme.Light()); panel.render(c, items, MenuOptions{}, actions) }, 300, 300)
	tester.Advance(theme.Light().Motion().MenuShow().Duration())
	tester.Frame()
	tester.Move(50, 20)
	tester.Key(0, ui.KeyEnd)
	tester.Move(50, 20)
	tester.Key(0, ui.KeyEnter)
	check(t, "stationary pointer preserves keyboard choice", chosen == "last")
	tester.Move(60, 20)
	tester.Key(0, ui.KeyEnter)
	check(t, "moving pointer resumes hovered choice", chosen == "first")
}
