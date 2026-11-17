package chat

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestServiceEqualWidthContinuation(t *testing.T) {
	source := theme.Light()
	m := source.Metrics().Service()
	const label = "A considerably wider first line\nShort\nShort"
	var line, shortWidth float32
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		shortWidth, line = c.MeasureText(0, span(c, "Short", source.Typography().Service(), source.Colors().Service().Text()))
		ServiceMessage(c, label, ServiceOptions{})
	}, 500, 220)
	r := box(tt, label)
	x := r.X + r.W/2 - (shortWidth+m.PaddingLeft()+m.PaddingRight())/2 + 1
	y := r.Y + 2*line + m.PaddingTop() + line/2
	// service_message.cpp:366-379: equal-width lines retain a preceding inward join.
	verify(t, "equal service lines preserve contour inset", pixel(tt, x, y) == rgba(source.Colors().Window().Background()))
}
