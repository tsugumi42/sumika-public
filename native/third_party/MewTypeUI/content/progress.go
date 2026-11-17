package content

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
	"time"
)

type progressKey struct{}
type progressGeometry struct {
	size, line           float32
	path                 ui.Path
	initialized          bool
	start, changed, last time.Time
	from, target, shown  float32
}

func progressArc(c *ui.Context, progress float32) ui.Element {
	t := theme.T(c)
	line := t.Metrics().Media().ProgressWidth()
	e := ui.Box(c).Absolute().Fill()
	state := ui.Local(e, progressKey{}, func() progressGeometry { return progressGeometry{start: c.Now()} })
	if !state.initialized {
		state.from, state.target, state.shown = progress, progress, progress
		state.changed, state.last, state.initialized = c.Now(), c.Now(), true
	} else if progress != state.target {
		state.from, state.target, state.changed = state.shown, progress, state.last
	}
	period := t.Motion().MediaTransferRotate().Duration()
	animated := period > 0 && !c.Preferences().ReduceMotion && !c.Instant()
	catchup := t.Motion().MediaTransferCatchup().Duration()
	reduced := c.Preferences().ReduceMotion || c.Instant()
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		value := state.target
		if !reduced && catchup > 0 {
			elapsed := max(time.Duration(0), p.Now().Sub(state.changed))
			value = state.from + (state.target-state.from)*(1-float32(catchup)/float32(catchup+elapsed))
			if value != state.target {
				p.AnimationFrame()
			}
		}
		state.shown, state.last = value, p.Now()
		size := min(r.W, r.H)
		if state.size != size || state.line != line {
			state.path.Reset().Circle(0, 0, size/2-line)
			state.size, state.line = size, line
		}
		// arc_angles.h:15-16 reserves one degree before progress arrives.
		state.path.Trim(0, 1.0/360+value*(359.0/360))
		rotation := float32(-90)
		if animated {
			rotation += 360 * float32(p.Now().Sub(state.start)%period) / float32(period)
			p.AnimationFrame()
		}
		draw.Center(p, r, func() {
			p.Transform(ui.Rotation(rotation), 0, 0, func() {
				p.StrokePath(&state.path, line, t.Colors().Control().RadialForeground())
			})
		})
	})
	return e
}
