package chat

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
)

func TestMultilineServiceContourAndThemeResize(t *testing.T) {
	source := theme.Light()
	var line float32
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		_, line = c.MeasureText(0, span(c, "M", source.Typography().Service(), source.Colors().Service().Text()))
		ServiceMessage(c, "A considerably wider first line\nShort", ServiceOptions{}).Label("notice")
	}, 500, 240)
	r := box(tt, "A considerably wider first line\nShort")
	bg := rgba(source.Colors().Window().Background())
	verify(t, "service contour connects both text lines", pixel(tt, r.X+r.W/2, r.Y+line+source.Metrics().Service().PaddingTop()) != bg)
	verify(t, "service contour follows narrower final line", pixel(tt, r.X+2, r.Y+line+source.Metrics().Service().PaddingTop()+line/2) == bg)
	definition := source.Definition()
	definition.Metrics["service.paddingTop"] = 12
	var err error
	source, err = theme.Compile(definition)
	if err != nil {
		t.Fatal(err)
	}
	old := r.H
	tt.Frame()
	verify(t, "service geometry responds to padding edits", box(tt, "A considerably wider first line\nShort").H > old)
}
