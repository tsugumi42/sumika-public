package chat

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestFloatingDateIgnoresHoverOutsideCapsule(t *testing.T) {
	source := theme.Light()
	var state DateState
	offset := float32(0)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		ui.Column(c).Fill().Children(func() {
			FloatingDate(c, "Today", &state, FloatingDateOptions{ScrollOffset: offset})
		})
	}, 400, 240)
	offset = 100
	tt.Frame()
	tt.Advance(source.Motion().DateFade().Duration())
	tt.Move(1, 1)
	tt.Advance(source.Motion().DateHold().Duration() + source.Motion().DateFade().Duration())
	r := box(tt, "Today")
	verify(t, "date hover only keeps the capsule visible", pixel(tt, r.X+r.W/2, r.Y+1) == rgba(source.Colors().Window().Background()))
}
