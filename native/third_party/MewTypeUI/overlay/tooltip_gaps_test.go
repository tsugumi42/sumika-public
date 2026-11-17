package overlay

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestTooltipKeyboardTriggerEscapeAndRefocus(t *testing.T) {
	state := tooltipLocal{}
	shown := false
	trigger := TooltipTrigger(0)
	disabled := false
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		ui.Column(c).Children(func() {
			b := ui.Button(c, "Target").Disabled(disabled)
			shown = state.show(c, b, "Details", TooltipOptions{Trigger: trigger})
			ui.Button(c, "Next")
		})
	}, 400, 200)
	tt.Move(390, 190)
	tt.Key(0, ui.KeyTab)
	check(t, "keyboard focus shows tooltip immediately", shown && tt.Focused("Target"))
	tt.Key(0, ui.KeyEscape)
	check(t, "Escape dismisses without moving focus", !shown && tt.Focused("Target"))
	tt.Advance(2 * time.Second)
	tt.Frame()
	check(t, "Escape suppression survives same focus", !shown)
	tt.Key(0, ui.KeyTab)
	check(t, "Tab moves forward without focus recovery theft", !shown && tt.Focused("Next"))
	tt.Key(ui.Shift, ui.KeyTab)
	check(t, "refocusing reopens tooltip", shown && tt.Focused("Target"))
	trigger = TooltipHover
	tt.Frame()
	check(t, "hover only excludes keyboard trigger", !shown)
	trigger = TooltipFocus
	tt.Frame()
	check(t, "focus only accepts keyboard trigger", shown)
	disabled = true
	tt.Frame()
	check(t, "disabled anchor dismisses tooltip", !shown)
}

func TestTooltipHoverDelayAndEscapeWithoutAnchorFocus(t *testing.T) {
	state := tooltipLocal{}
	shown := false
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		ui.Column(c).Children(func() {
			b := ui.Button(c, "Target")
			shown = state.show(c, b, "Details", TooltipOptions{Trigger: TooltipHover})
			ui.Button(c, "Next")
		})
	}, 400, 200)
	r := rect(t, tt, "Target")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(theme.Light().Motion().TooltipDelay().Duration() - time.Millisecond)
	check(t, "hover waits for source delay", !shown)
	tt.Advance(time.Millisecond)
	check(t, "hover shows at source delay", shown)
	tt.Key(0, ui.KeyEscape)
	check(t, "hover Escape works without anchor focus", !shown)
	tt.Move(390, 190)
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(theme.Light().Motion().TooltipDelay().Duration())
	check(t, "leaving resets dismissal", shown)
}

func TestTooltipExitReversalAndReducedMotion(t *testing.T) {
	tip := nativeTooltip{label: "Details"}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		tip.render(c)
	}, 160, 60)
	duration := theme.Light().Motion().Fade().Duration()
	tt.Advance(duration)
	tt.Frame()
	tip.hide()
	tt.Frame()
	tt.Advance(duration / 2)
	tt.Frame()
	alpha := tip.visible.value
	check(t, "tooltip retains content halfway through exit", tt.HasText("Details") && alpha > 0 && alpha < 1)
	tip.hidden = false
	tt.Frame()
	check(t, "tooltip reverses from current opacity", tip.visible.value == alpha)
	tt.Advance(duration)
	tt.Frame()
	check(t, "tooltip reversal reaches visible endpoint", tip.visible.value == 1)
	tip.hide()
	tt.Frame()
	tt.Advance(duration)
	tt.Frame()
	check(t, "tooltip exit reaches transparent endpoint", tip.visible.value == 0 && !tt.FramePending())
	tip.hidden = false
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	check(t, "reduced motion tooltip enters immediately", tip.visible.value == 1)
	tip.hide()
	tt.Frame()
	check(t, "reduced motion tooltip exits immediately", tip.visible.value == 0 && !tt.FramePending())
}
