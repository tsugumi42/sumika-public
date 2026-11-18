package compose

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/locales"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type actionCapture struct {
	state      *actionVisual
	generation int
}

func (actionCapture) EffectMargin() ui.Point { return ui.Point{} }
func (v actionCapture) PaintEffect(p *ui.Painter, r ui.Rect, image *ui.Bitmap, _ ui.Point) {
	if v.state.generation == v.generation {
		v.state.image = image
	}
	p.ImageRegion(image, ui.Rect{}, r, ui.ImageOptions{})
}
func paintAction(p *ui.Painter, r ui.Rect, a Action, icon draw.Glyph, t *theme.Theme, ink ui.Color) {
	m := t.Metrics().Compose()
	if a == Stop {
		// paint/stop-generation.md#样式与图形: a true 14 DIP rounded square,
		// sharing the ripple's floating center (centering.md).
		draw.Center(p, r, func() { d := m.StopSize(); p.Fill(ui.Rect{X: -d / 2, Y: -d / 2, W: d, H: d}, ink, m.StopRadius()) })
		return
	}
	size := m.IconSize()
	if a == Voice {
		size = m.VoiceIconSize()
	}
	draw.Icon(p, r, icon, size, ink)
}

type GenerationPhase uint8

const (
	Idle GenerationPhase = iota
	Generating
	Stopping
	StopFailed
	Stopped
	Completed
)

type Generation struct {
	RunID string
	Phase GenerationPhase
}

func (g Generation) active() bool {
	return g.Phase == Generating || g.Phase == Stopping || g.Phase == StopFailed
}

type StopOptions struct {
	Label    string
	Disabled bool
	OnStop   func(string)
}

// StopGeneration is the standalone form. Editor's Generation option keeps the
// same submit root and field width when transitioning from Send to Stop.
func StopGeneration(c *ui.Context, g Generation, o StopOptions) ui.Element {
	name := o.Label
	if name == "" && g.Phase == Stopping {
		name = locales.Text(c.Locale(), "stopping")
	}
	return ActionButton(c, Stop, ActionOptions{Label: name, Disabled: o.Disabled || g.Phase == Stopping || !g.active(), OnClick: func() {
		if o.OnStop != nil {
			o.OnStop(g.RunID)
		}
	}})
}
