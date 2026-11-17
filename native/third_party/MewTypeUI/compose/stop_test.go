package compose

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
)

func TestGenerationStopKeepsDraftAndActionSlot(t *testing.T) {
	tr := theme.Light()
	draft := "preserved draft"
	generation := Generation{RunID: "run-a", Phase: Idle}
	stops := []string{}
	submitted := 0
	readOnly := false
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		Editor(c, &draft, EditorOptions{ReadOnly: readOnly, Generation: &generation, OnSubmit: func(string) { submitted++ }, OnStop: func(id string) { stops = append(stops, id); generation.Phase = Stopping }})
	}, 500, 200)
	fieldBefore := bounds(t, tt, "Message")
	sendBefore := bounds(t, tt, "Send")
	generation.Phase = Generating
	tt.Frame()
	tt.Advance(tr.Motion().ComposeButton().Duration())
	tt.Frame()
	stop := bounds(t, tt, "Stop generating")
	fieldAfter := bounds(t, tt, "Message")
	// paint/stop-generation.md#样式与图形: fixed 44x46 slot, 14 square.
	expect(t, "stop-submit-slot-stable", near(stop.X, sendBefore.X) && near(stop.W, tr.Metrics().Compose().SendWidth()) && near(stop.H, tr.Metrics().Compose().SendHeight()) && fieldBefore == fieldAfter)
	painted := actionInkBounds(tt, stop, tr.Colors().Compose().SendIconForeground())
	expect(t, "stop-square-visible", near(painted.W, tr.Metrics().Compose().StopSize()) && near(painted.H, tr.Metrics().Compose().StopSize()) && near(painted.X, stop.X+(stop.W-painted.W)/2) && near(painted.Y, stop.Y+(stop.H-painted.H)/2))
	click(t, tt, "Message")
	tt.Key(0, ui.KeyEnter)
	expect(t, "stop-generating-enter-does-not-send", submitted == 0 && draft == "preserved draft")
	readOnly = true
	tt.Frame()
	click(t, tt, "Stop generating")
	tt.Key(0, ui.KeyEnter)
	expect(t, "stop-independent-of-readonly", len(stops) == 1 && stops[0] == "run-a" && draft == "preserved draft")
	click(t, tt, "Stopping…")
	expect(t, "stop-stopping-blocks-repeat", len(stops) == 1)
	generation.Phase = StopFailed
	tt.Frame()
	click(t, tt, "Stop generating")
	expect(t, "stop-failure-allows-explicit-retry", len(stops) == 2)
	generation.Phase = Completed
	readOnly = false
	tt.Frame()
	tt.Advance(tr.Motion().ComposeButton().Duration())
	tt.Frame()
	expect(t, "stop-completion-restores-send", tt.HasText("Send") && !tt.HasText("Stop generating") && draft == "preserved draft")
}

func TestActionSwapInterruptsVisibleSnapshot(t *testing.T) {
	tr := theme.Light()
	action := Send
	var visual *actionVisual
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		e := ActionButton(c, action, ActionOptions{})
		visual = ui.Local(e, "visual", func() actionVisual { return actionVisual{} })
	}, 60, 60)
	action = Stop
	tt.Frame()
	tt.Advance(tr.Motion().ComposeButton().Duration() / 2)
	tt.Frame()
	midpoint := append([]byte(nil), tt.Image().Pix...)
	action = Send
	tt.Frame()
	expect(t, "stop-swap-interruption-continuity", string(midpoint) == string(tt.Image().Pix))
	tt.Advance(tr.Motion().ComposeButton().Duration())
	tt.Frame()
	expect(t, "stop-swap-releases-old-snapshot", visual.from == nil && visual.started.IsZero() && !tt.FramePending())
}

func actionInkBounds(tt *ui.Tester, box ui.Rect, ink ui.Color) ui.Rect {
	left, top, right, bottom := int(box.X+box.W), int(box.Y+box.H), int(box.X), int(box.Y)
	im := tt.Image()
	for y := int(box.Y); y < int(box.Y+box.H); y++ {
		for x := int(box.X); x < int(box.X+box.W); x++ {
			p := im.RGBAAt(x, y)
			if p.R == ink.R && p.G == ink.G && p.B == ink.B && p.A == ink.A {
				left = min(left, x)
				top = min(top, y)
				right = max(right, x+1)
				bottom = max(bottom, y+1)
			}
		}
	}
	return ui.Rect{X: float32(left), Y: float32(top), W: float32(right - left), H: float32(bottom - top)}
}
