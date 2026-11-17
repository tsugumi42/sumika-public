package chat

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// RevealState follows the message's stable ID in host state, not its mounted
// element. Historical rows therefore never replay when scrolled back into view.
type RevealState struct {
	started time.Time
	active  bool
}

// Start is called by the host for a new-message event, using c.Now().
func (s *RevealState) Start(at time.Time) { s.started, s.active = at, true }
func (s *RevealState) Finish()            { s.active = false }

type RevealOptions struct {
	// Height is the measured full row height, including its outside spacing.
	Height float32
	// ListPath chooses the alternate Telegram list's quintic curve.
	ListPath bool
	// SendingProgress drives the reveal linearly alongside a host-owned flight.
	SendingProgress *float32
}

// Reveal opens the row's layout space from zero; it does not scale glyphs or
// fade the message. Offscreen remounts keep elapsed progress in explicit state.
func Reveal(c *ui.Context, state *RevealState, o RevealOptions, content func()) ui.Element {
	transition := theme.T(c).Motion().MessageReveal()
	if o.ListPath {
		transition = theme.T(c).Motion().MessageRevealList()
	}
	progress := float32(1)
	if state.active {
		elapsed := c.Now().Sub(state.started)
		if c.Instant() || c.Preferences().ReduceMotion || elapsed >= transition.Duration() {
			state.active = false
		} else {
			progress = transition.Easing().Curve()(float32(elapsed) / float32(transition.Duration()))
			c.AnimationFrame()
		}
	}
	if o.SendingProgress != nil && !c.Preferences().ReduceMotion && !c.Instant() {
		progress = *o.SendingProgress
	}
	root := ui.Column(c).Height(o.Height * progress).Shrink(0).ClipY()
	return root.Children(func() { ui.Column(c).Height(o.Height).Shrink(0).Children(content) })
}
