package compose

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/locales"
	"github.com/tsugumi42/MewTypeUI/theme"
	"math"
	"time"
)

type Action uint8

const (
	Automatic Action = iota
	Attach
	Emoji
	Send
	Voice
	Save
	Cancel
	Stop
)

type ActionOptions struct {
	Label            string
	Disabled, Active bool
	OnClick          func()
}

type actionVisual struct {
	current, previous Action
	generation        int
	started           time.Time
	image, from       *ui.Bitmap
}

type rippleVisual struct {
	pressed           bool
	started, released time.Time
	x, y              float32
}

// ActionButton exposes the same accessible button for mouse and keyboard activation.
func ActionButton(c *ui.Context, action Action, o ActionOptions) ui.Element {
	t := theme.T(c)
	m, colors := t.Metrics().Compose(), t.Colors().Compose()
	b := ui.ButtonBase(c).Size(m.SendWidth(), m.SendHeight()).Shrink(0).Center().Clip().
		Label(label(o.Label, locales.Text(c.Locale(), actionLabel(action)))).Tooltip(label(o.Label, locales.Text(c.Locale(), actionLabel(action)))).Disabled(o.Disabled).OnClick(o.OnClick)
	v := ui.Local(b, "visual", func() actionVisual { return actionVisual{current: action, previous: action} })
	if v.current != action {
		// paint/stop-generation.md#send与stop互换: rotating the cached layer
		// freezes the currently visible composite, including an interrupted swap.
		v.previous, v.current = v.current, action
		v.from, v.image = v.image, nil
		v.generation++
		v.started = c.Now()
	}
	motion := t.Motion().ComposeButton()
	p := float32(1)
	if !v.started.IsZero() && motion.Duration() > 0 && !c.Instant() && !c.Preferences().ReduceMotion {
		p = min(1, float32(c.Now().Sub(v.started))/float32(motion.Duration()))
		if p < 1 {
			c.AnimationFrame()
		}
	}
	if p == 1 {
		v.from = nil
		v.image = nil
		v.started = time.Time{}
	}
	fg := colors.IconForeground()
	if b.Hovered() || b.Pressed() {
		fg = colors.IconForegroundHover()
	}
	if action == Send || action == Save || action == Stop || o.Active {
		fg = colors.SendIconForeground()
		if b.Hovered() || b.Pressed() {
			fg = colors.SendIconForegroundHover()
		}
	}
	r := ui.Local(b, "ripple", func() rippleVisual { return rippleVisual{} })
	pressed := b.Pressed()
	if pressed && !r.pressed {
		r.started, r.released = c.Now(), time.Time{}
		r.x, r.y, _ = b.PointerPosition()
	}
	if !pressed && r.pressed {
		r.released = c.Now()
	}
	r.pressed = pressed
	reduced := c.Preferences().ReduceMotion
	show, hide := t.Motion().RippleShow(), t.Motion().RippleHide()
	b.Draw(func(painter *ui.Painter, box ui.Rect) {
		if r.started.IsZero() {
			return
		}
		alpha := float32(1)
		if !r.released.IsZero() {
			if reduced || hide.Duration() == 0 {
				return
			}
			alpha = max(float32(0), 1-float32(painter.Now().Sub(r.released))/float32(hide.Duration()))
			if alpha == 0 {
				return
			}
		}
		progress := float32(1)
		if !reduced && show.Duration() > 0 {
			progress = min(float32(1), float32(painter.Now().Sub(r.started))/float32(show.Duration()))
		}
		progress = show.Easing().Curve()(progress)
		d := m.RippleSize()
		circle := ui.Rect{X: -d / 2, Y: -d / 2, W: d, H: d}
		x, y := r.x-box.W/2, r.y-box.H/2
		radius := (d/2 + float32(math.Sqrt(float64(x*x+y*y)))) * progress
		draw.Center(painter, box, func() {
			painter.Clip(circle, d/2, func() {
				painter.Fill(ui.Rect{X: x - radius, Y: y - radius, W: 2 * radius, H: 2 * radius}, colors.ButtonBackgroundPressed().Alpha(alpha), radius)
			})
		})
		if progress < 1 || !r.released.IsZero() {
			painter.AnimationFrame()
		}
	})
	// paint/stop-generation.md#样式与图形 and #send与stop互换.
	// Snapshot expansion k=5 yields visible 1→1/k and 1/k→1 scales.
	oldScale := 1 - (1-1/m.SwapScale())*p
	newScale := 1/m.SwapScale() + (1-1/m.SwapScale())*p
	// paint/icon-glyph.md#图形限框: prepare geometry before the paint callback.
	prepare := func(a Action) draw.Glyph {
		if a == Stop {
			return draw.Glyph{}
		}
		size := m.IconSize()
		if a == Voice {
			size = m.VoiceIconSize()
		}
		return draw.Resolve(c, actionIcon(a), size, size)
	}
	currentIcon := prepare(action)
	var previousIcon draw.Glyph
	if p < 1 && v.from == nil {
		previousIcon = prepare(v.previous)
	}
	b.TextColor(fg).Children(func() {
		if p == 1 {
			// paint/stop-generation.md#样式与图形; centering.md: a settled
			// vector keeps its floating center without an intermediate bitmap grid.
			ui.Box(c.Key("settled")).Absolute().Fill().IgnoreInput(true).Draw(func(painter *ui.Painter, box ui.Rect) { paintAction(painter, box, action, currentIcon, t, fg) })
			return
		}
		ui.Box(c.Key(v.generation)).Absolute().Fill().IgnoreInput(true).Cache().Effect(actionCapture{v, v.generation}).Children(func() {
			if p < 1 {
				ui.Box(c.Key("departing")).Absolute().Fill().Opacity(1-p).Scale(oldScale, oldScale).Draw(func(painter *ui.Painter, box ui.Rect) {
					if v.from != nil {
						painter.ImageRegion(v.from, ui.Rect{}, box, ui.ImageOptions{})
					} else {
						paintAction(painter, box, v.previous, previousIcon, t, fg)
					}
				})
			}
			ui.Box(c.Key("arriving")).Absolute().Fill().Opacity(p).Scale(newScale, newScale).Draw(func(painter *ui.Painter, box ui.Rect) { paintAction(painter, box, action, currentIcon, t, fg) })
		})
	})
	return b
}

func actionLabel(a Action) string {
	switch a {
	case Attach:
		return "attach"
	case Emoji:
		return "emoji"
	case Send:
		return "send"
	case Voice:
		return "voice"
	case Save:
		return "save"
	case Cancel:
		return "cancel"
	case Stop:
		return "stopGeneration"
	}
	panic("compose: Automatic is only valid in EditorOptions")
}

func actionIcon(a Action) *ui.SVG {
	switch a {
	case Attach:
		return clipIcon
	case Emoji:
		return smileIcon
	case Send:
		return sendIcon
	case Voice:
		return micIcon
	case Save:
		return checkIcon
	case Cancel:
		return closeIcon
	}
	panic("compose: action has no icon")
}

// Lucide 1.58.0 paths, ISC/MIT; see THIRD_PARTY_NOTICES.md.
func icon(paths string, x, y, w, h float32) *ui.SVG {
	return draw.Lucide(paths, ui.Rect{X: x, Y: y, W: w, H: h})
}

var (
	clipIcon  = icon(`<path d="m16 6-8.414 8.586a2 2 0 0 0 2.829 2.829l8.414-8.586a4 4 0 1 0-5.657-5.657l-8.379 8.551a6 6 0 1 0 8.485 8.485l8.379-8.551"/>`, 3, 2, 19, 20)
	sendIcon  = icon(`<path d="M14.536 21.686a.5.5 0 0 0 .937-.024l6.5-19a.496.496 0 0 0-.635-.635l-19 6.5a.5.5 0 0 0-.024.937l7.93 3.18a2 2 0 0 1 1.112 1.11z"/><path d="m21.854 2.147-10.94 10.939"/>`, 2, 2, 20, 20)
	smileIcon = icon(`<path d="M15 10V9"/><path d="M16.472 15a6 6 0 01-8.943 0"/><path d="M9 10V9"/><circle cx="12" cy="12" r="10"/>`, 2, 2, 20, 20)
	micIcon   = icon(`<path d="M12 19v3"/><path d="M19 10v2a7 7 0 0 1-14 0v-2"/><rect x="9" y="2" width="6" height="13" rx="3"/>`, 5, 2, 14, 20)
	closeIcon = icon(`<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`, 6, 6, 12, 12)
	checkIcon = icon(`<path d="M20 6 9 17l-5-5"/>`, 4, 6, 16, 11)
	fileIcon  = icon(`<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z"/><path d="M14 2v5a1 1 0 0 0 1 1h5"/>`, 4, 2, 16, 20)
	replyIcon = icon(`<path d="M20 18v-2a4 4 0 0 0-4-4H4"/><path d="m9 17-5-5 5-5"/>`, 4, 7, 16, 11)
	editIcon  = icon(`<path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z"/><path d="m15 5 4 4"/>`, 2, 2, 20, 20)
)

func init() {
	draw.Configure(clipIcon, draw.Attach, 24, 24)
	draw.Configure(sendIcon, draw.Send, 24, 24)
	draw.Configure(smileIcon, draw.Emoji, 24, 24)
	draw.Configure(closeIcon, draw.Close, 24, 24)
	draw.Configure(checkIcon, draw.Save, 24, 24)
	draw.Configure(fileIcon, draw.File, 24, 24)
	draw.Configure(replyIcon, draw.Reply, 35, 35)
	draw.Configure(editIcon, draw.Edit, 24, 24)
}
