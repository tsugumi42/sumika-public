package overlay

import (
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type ViewerPhase uint8

const (
	ViewerReady ViewerPhase = iota
	ViewerLoading
	ViewerFailed
)

// ViewerItem contains only prepared presentation resources. Ready requires an
// image; loading and failure never start I/O. ID remains stable across reorders.
type ViewerItem struct {
	ID, Title, Information, Caption string
	Image                           *ui.Bitmap
	// IntrinsicWidth/Height are a pair of logical dimensions, independent of
	// thumbnail resolution. Zero uses the ready bitmap's dimensions.
	IntrinsicWidth, IntrinsicHeight float32
	Phase                           ViewerPhase
	Progress                        float32
	Error                           string
}
type ViewerState struct {
	Open bool
	ID   string
	// Zoom is Telegram's discrete level: negative levels divide natural size
	// by 1-Zoom; nonnegative levels multiply it by 1+Zoom. Fit inserts the
	// window-fit scale as a separate stage, even at an equal visible scale.
	// Entering it can exceed the ordinary level limits, as in the source viewer.
	Zoom     int
	Fit      bool
	Pan      ui.Point
	serial   uint64
	active   string
	dragging bool
	drag     ui.Point
	pinch    float32
}

func (s *ViewerState) Show(id string) { s.ID = id; s.Open = true; s.active = ""; s.serial++ }
func (s *ViewerState) Close()         { s.Open = false; s.dragging = false }

type ViewerOptions struct {
	PreviousLabel, NextLabel, CloseLabel, ZoomInLabel, ZoomOutLabel, ResetLabel, RetryLabel string
	LoadingLabel                                                                            string
	OnChange                                                                                func(id string)
	OnClose                                                                                 func()
	OnRetry                                                                                 func(id string)
}
type viewerLocal struct {
	controls visibility
	activity time.Time
	pointer  ui.Point
	wasOpen  bool
	opened   time.Time
	serial   uint64
	phase    ViewerPhase
	loading  viewerLoadingVisual
}
type viewerKey struct{ owner uint64 }

type viewerLoadingVisual struct {
	shown               visibility
	started, changed    time.Time
	from, target, value float32
	path                ui.Path
	radius              float32
}

// viewerContent renders media in the viewer window and in headless tests.
func viewerContent(c *ui.Context, state *ViewerState, items []ViewerItem, o ViewerOptions) ui.Element {
	root := ui.Box(c).Size(0, 0)
	local := ui.Local(root, "viewer", func() viewerLocal { return viewerLocal{} })
	if !state.Open {
		local.wasOpen = false
		return root
	}
	index := slices.IndexFunc(items, func(it ViewerItem) bool { return it.ID == state.ID })
	if index < 0 {
		state.Close()
		if o.OnClose != nil {
			o.OnClose()
		}
		return root
	}
	item := items[index]
	t := theme.T(c)
	m := t.Metrics().Viewer()
	vw, vh := c.Size()
	fit := float32(1)
	naturalW, naturalH := float32(0), float32(0)
	// paint/viewer.md#图片矩形: replacing a thumbnail must not change scale.
	if item.IntrinsicWidth > 0 && item.IntrinsicHeight > 0 {
		naturalW, naturalH = item.IntrinsicWidth, item.IntrinsicHeight
	} else if item.Phase == ViewerReady {
		iw, ih := item.Image.Size()
		naturalW, naturalH = float32(iw), float32(ih)
	}
	if naturalW > 0 && naturalH > 0 {
		fit = min(vw/naturalW, vh/naturalH)
	}
	changed := state.active != item.ID || !local.wasOpen
	if !local.wasOpen || local.serial != state.serial {
		local.opened, local.serial = c.Now(), state.serial
	}
	if changed {
		local.loading.shown = visibility{}
		local.loading.started, local.loading.changed = c.Now(), c.Now()
		local.loading.from, local.loading.target, local.loading.value = 0, 0, 0
	}
	if changed || local.phase != ViewerReady && item.Phase == ViewerReady {
		state.active = item.ID
		state.Zoom = 0
		state.Fit = fit < 1
		state.Pan = ui.Point{}
		state.pinch = 0
		local.activity = c.Now()
		local.controls = visibility{initialized: true, target: true, from: 1, value: 1, start: c.Now()}
	}
	local.wasOpen = true
	local.phase = item.Phase
	closeViewer := func() {
		state.Close()
		if o.OnClose != nil {
			o.OnClose()
		}
		c.Invalidate()
	}
	// paint/viewer.md#打开保护: fixed input threshold required by the host.
	closeByClick := func() {
		if c.Now().Sub(local.opened) >= 500*time.Millisecond {
			closeViewer()
		}
	}
	turn := func(to int) {
		if to >= 0 && to < len(items) {
			state.ID = items[to].ID
			if o.OnChange != nil {
				o.OnChange(state.ID)
			}
			c.Invalidate()
		}
	}
	zoom := func(direction int, anchor ui.Point) {
		viewerZoom(state, direction, fit, int(m.MaxZoomLevel()), anchor, vw, vh, naturalW, naturalH)
		c.Invalidate()
	}
	reset := func() {
		state.Fit = !state.Fit && state.Zoom == 0
		state.Zoom = 0
		state.Pan = ui.Point{}
		c.Invalidate()
	}
	scale := viewerScale(state, fit)
	iw, ih := naturalW*scale, naturalH*scale
	clampPan(state, iw, ih, vw, vh)
	ui.Overlay(c, func() {
		back := ui.Box(c.Key(viewerKey{root.ID()})).Absolute().Left(0).Top(0).Right(0).Bottom(0).Modal().Clip().Role(ui.RoleDialog).Label(item.Title).Background(t.Colors().Viewer().Background())
		x, y, _ := back.PointerPosition()
		pointer := ui.Point{X: x, Y: y}
		if pointer != local.pointer {
			local.pointer = pointer
			local.activity = c.Now()
		}
		showControls := c.Now().Sub(local.activity) < t.Motion().ViewerWait().Duration() || state.dragging
		if showControls {
			c.After(t.Motion().ViewerWait().Duration() - min(c.Now().Sub(local.activity), t.Motion().ViewerWait().Duration()))
		}
		alpha := local.controls.update(c, showControls, t.Motion().ViewerShow(), t.Motion().ViewerHide())
		gesture := func(ev ui.InputEvent) bool {
			if ev.Kind == ui.InputScroll {
				local.activity = ev.Time
				if ev.Mods&ui.Ctrl != 0 {
					if ev.DY < 0 {
						zoom(1, ui.Point{X: ev.X, Y: ev.Y})
					} else if ev.DY > 0 {
						zoom(-1, ui.Point{X: ev.X, Y: ev.Y})
					}
					return true
				}
				if ev.DY > 0 {
					turn(index + 1)
				} else if ev.DY < 0 {
					turn(index - 1)
				}
				return true
			}
			if ev.Kind == ui.InputZoom {
				local.activity = ev.Time
				state.pinch += ev.Scale - 1
				for state.pinch >= m.PinchStep() {
					state.pinch -= m.PinchStep()
					zoom(1, ui.Point{X: ev.X, Y: ev.Y})
				}
				for state.pinch <= -m.PinchStep() {
					state.pinch += m.PinchStep()
					zoom(-1, ui.Point{X: ev.X, Y: ev.Y})
				}
				return true
			}
			return false
		}
		back.HandleInput(gesture)
		back.Children(func() {
			if item.Phase == ViewerReady {
				image := ui.Image(c.Key(item.ID), item.Image).Label("Viewer image").Size(iw, ih).Attach(ui.AnchorCenter, ui.AnchorCenter).Translate(state.Pan.X, state.Pan.Y).Focusable()
				imageX, imageY := (vw-iw)/2+state.Pan.X, (vh-ih)/2+state.Pan.Y
				image.HandleInput(func(ev ui.InputEvent) bool {
					// Mygo sends pinches only to the innermost handler; preserve its window anchor.
					if ev.Kind == ui.InputZoom {
						ev.X += imageX
						ev.Y += imageY
						return gesture(ev)
					}
					switch ev.Kind {
					case ui.InputPointerDown:
						if ev.Button == 2 {
							reset()
							local.activity = ev.Time
							return true
						}
						if ev.Button != 0 {
							return false
						}
						// paint/viewer.md#双击: the second press toggles once.
						if ev.Clicks == 2 {
							state.dragging = false
							reset()
							return true
						}
						local.activity = ev.Time
						state.dragging = true
						state.drag = ui.Point{X: ev.X, Y: ev.Y}
						return true
					case ui.InputPointerMove:
						if !state.dragging {
							return false
						}
						state.Pan.X += ev.X - state.drag.X
						state.Pan.Y += ev.Y - state.drag.Y
						clampPan(state, iw, ih, vw, vh)
						local.activity = ev.Time
						return true
					case ui.InputPointerUp:
						state.dragging = false
						return true
					}
					return false
				})
			} else if item.Phase == ViewerFailed {
				ui.Column(c).Attach(ui.AnchorCenter, ui.AnchorCenter).AlignItems(ui.Center).Gap(m.InfoGap()).Children(func() {
					text(c, item.Error, t.Typography().Body(), t.Colors().Viewer().ControlForeground())
					if o.OnRetry != nil {
						label := o.RetryLabel
						if label == "" {
							label = "Retry"
						}
						ui.ButtonBase(c).Label(label).Padding(m.InfoGap()).Children(func() { text(c, label, t.Typography().Body(), t.Colors().Viewer().TextLinkForeground()) }).OnClick(func() { o.OnRetry(item.ID) })
					}
				})
			}
			viewerLoading(c, &local.loading, item.Phase == ViewerLoading, item.Progress, o.LoadingLabel)
			// The requested embedded viewer puts information at the top; dimensions
			// use the desktop viewer's information offsets (see exceptions.md).
			ui.Column(c).Absolute().Left(m.InfoLeft()).Top(m.TextOffset()).Right(m.ActionWidth()).Opacity(alpha).IgnoreInput(true).Children(func() {
				text(c, item.Title, t.Typography().ConversationTitle(), t.Colors().Viewer().ControlForeground()).SingleLine()
				text(c, item.Information, t.Typography().Body(), t.Colors().Viewer().ControlForeground()).SingleLine().Absolute().Left(0).Right(0).Top(m.HeaderOffset() - m.TextOffset())
			})
			previous, next := o.PreviousLabel, o.NextLabel
			if previous == "" {
				previous = "Previous"
			}
			if next == "" {
				next = "Next"
			}
			left, right := leftIcon, rightIcon
			if c.Direction() == ui.RightToLeft {
				left, right = right, left
				previous, next = next, previous
			}
			for _, nav := range []struct {
				id, label string
				icon      *ui.SVG
				to        int
				right     bool
			}{{"previous", previous, left, index - 1, false}, {"next", next, right, index + 1, true}} {
				b := ui.ButtonBase(c.Key(nav.id)).Label(nav.label).Absolute().Top(m.HeaderOffset()).Bottom(m.HeaderOffset()).Width(m.ControlWidth()).Disabled(nav.to < 0 || nav.to >= len(items)).Opacity(alpha)
				if nav.right {
					b.Right(0)
				} else {
					b.Left(0)
				}
				if nav.to < 0 || nav.to >= len(items) {
					b.Invisible()
				}
				viewerButton(c, b, nav.icon)
				b.OnClick(func() { local.activity = c.Now(); turn(nav.to) })
			}
			closeLabel := o.CloseLabel
			if closeLabel == "" {
				closeLabel = "Close viewer"
			}
			b := ui.ButtonBase(c).Label(closeLabel).Absolute().Right(0).Top(0).Size(m.ActionWidth(), m.ActionHeight()).Opacity(alpha)
			viewerButton(c, b, closeIcon)
			b.OnClick(closeByClick)
			inLabel, outLabel, resetLabel := o.ZoomInLabel, o.ZoomOutLabel, o.ResetLabel
			if inLabel == "" {
				inLabel = "Zoom in"
			}
			if outLabel == "" {
				outLabel = "Zoom out"
			}
			if resetLabel == "" {
				resetLabel = "Actual size / fit"
			}
			ui.Row(c).Absolute().Right(0).Bottom(0).Opacity(alpha).Children(func() {
				for _, action := range []struct {
					label string
					icon  *ui.SVG
					fn    func()
				}{{outLabel, zoomOutIcon, func() { zoom(-1, ui.Point{X: vw / 2, Y: vh / 2}) }}, {inLabel, zoomInIcon, func() { zoom(1, ui.Point{X: vw / 2, Y: vh / 2}) }}} {
					b := ui.ButtonBase(c.Key(action.label)).Label(action.label).Size(m.ActionWidth(), m.ActionHeight()).Disabled(item.Phase != ViewerReady)
					viewerButton(c, b, action.icon)
					b.OnClick(action.fn)
				}
				ui.ButtonBase(c).Label(resetLabel).Height(m.ActionHeight()).Padding(0, m.InfoLeft()).Disabled(item.Phase != ViewerReady).Children(func() { text(c, percent(scale), t.Typography().Body(), t.Colors().Viewer().ControlForeground()) }).OnClick(reset)
			})
			if item.Caption != "" {
				ui.Box(c).Absolute().Left(m.CaptionMargin()).Bottom(m.ActionHeight()+m.CaptionMargin()).MaxWidth(vw-m.CaptionMargin()*2).Padding(m.CaptionPaddingY(), m.CaptionPaddingX()).Radius(m.CaptionRadius()).Background(t.Colors().Viewer().CaptionBackground()).Opacity(alpha).IgnoreInput(true).Children(func() { text(c, item.Caption, t.Typography().Body(), t.Colors().Viewer().CaptionForeground()) })
			}
		})
		if back.Clicked() {
			closeByClick()
		}
		backKey, nextKey := ui.KeyLeft, ui.KeyRight
		if c.Direction() == ui.RightToLeft {
			backKey, nextKey = nextKey, backKey
		}
		switch {
		case back.OverlayShortcut(0, ui.KeyEscape):
			closeViewer()
		case back.OverlayShortcut(0, backKey):
			turn(index - 1)
		case back.OverlayShortcut(0, nextKey):
			turn(index + 1)
		case back.OverlayShortcut(0, ui.KeyHome):
			turn(0)
		case back.OverlayShortcut(0, ui.KeyEnd):
			turn(len(items) - 1)
		case back.OverlayShortcut(ui.Ctrl, ui.KeyEqual), back.OverlayShortcut(ui.Ctrl|ui.Shift, ui.KeyEqual):
			if item.Phase == ViewerReady {
				zoom(1, ui.Point{X: vw / 2, Y: vh / 2})
			}
		case back.OverlayShortcut(ui.Ctrl, ui.KeyMinus):
			if item.Phase == ViewerReady {
				zoom(-1, ui.Point{X: vw / 2, Y: vh / 2})
			}
		case back.OverlayShortcut(ui.Ctrl, ui.Key0):
			if item.Phase == ViewerReady {
				reset()
			}
		}
	})
	return root
}

func viewerLoading(c *ui.Context, s *viewerLoadingVisual, loading bool, target float32, label string) {
	t := theme.T(c)
	m, motion := t.Metrics().Viewer(), t.Motion()
	if loading && !s.shown.target {
		s.started, s.changed = c.Now(), c.Now()
		s.from, s.target, s.value = target, target, target
	}
	alpha := s.shown.update(c, loading, motion.ViewerLoading(), motion.ViewerLoading())
	if !loading && alpha == 0 {
		return
	}
	if label == "" {
		label = "Loading…"
	}
	if !loading {
		target = 1
	}
	if target != s.target {
		s.from, s.target, s.changed = s.value, target, c.Now()
	}
	elapsed, duration := c.Now().Sub(s.changed), motion.ViewerLoading().Duration()
	p := float32(1)
	if duration > 0 && !c.Preferences().ReduceMotion {
		if loading {
			p = 1 - float32(duration)/float32(duration+elapsed)
		} else {
			p = min(float32(elapsed)/float32(duration), 1)
		}
		p = motion.ViewerLoading().Easing().Curve()(p)
	}
	s.value = s.from + (s.target-s.from)*p
	size, stroke := m.LoadingSize(), m.LoadingStroke()
	radius := size/2 - stroke
	if s.radius != radius {
		s.path.Reset().Circle(0, 0, radius)
		s.radius = radius
	}
	length := (m.LoadingMinArc() + (360-m.LoadingMinArc())*max(s.value, m.LoadingMinProgress())) / 360
	s.path.Trim(0, length)
	turn := float32(0)
	if period := motion.ViewerLoadingRotation().Duration(); period > 0 && !c.Preferences().ReduceMotion {
		turn = motion.ViewerLoadingRotation().Easing().Curve()(float32(c.Now().Sub(s.started)%period) / float32(period))
	}
	if loading && !c.Preferences().ReduceMotion {
		c.AnimationFrame()
	}
	rtl := c.Direction() == ui.RightToLeft
	ui.Box(c.Key("viewer-loading")).Attach(ui.AnchorCenter, ui.AnchorCenter).Size(size, size).Label(label).Role(ui.RoleProgress).Range(0, 1, float64(target)).Opacity(alpha).IgnoreInput(true).Draw(func(p *ui.Painter, r ui.Rect) {
		draw.Radial(p, r, &s.path, stroke, -90+360*(turn-length), t.Colors().Control().RadialBackground(), t.Colors().Control().RadialForeground(), rtl)
	})
}
func viewerButton(c *ui.Context, b ui.Element, icon *ui.SVG) {
	t := theme.T(c)
	m := t.Metrics().Viewer()
	tr := t.Motion().ViewerHover()
	hover := b.AnimateWith("hover", unit(b.Hovered()), tr.Duration(), tr.Easing().Curve())
	b.Children(func() {
		ui.Box(c).Absolute().Attach(ui.AnchorCenter, ui.AnchorCenter).Size(m.HoverSize(), m.HoverSize()).Radius(m.HoverSize() / 2).Background(t.Colors().Viewer().ControlBackground().Alpha(hover)).IgnoreInput(true)
		ui.Icon(c, icon).Size(t.Metrics().Menu().IconSize(), t.Metrics().Menu().IconSize()).TextColor(t.Colors().Viewer().ControlForeground()).IgnoreInput(true)
	})
}
func viewerScale(s *ViewerState, fit float32) float32 {
	if s.Fit {
		return fit
	}
	return levelScale(s.Zoom)
}
func levelScale(level int) float32 {
	if level < 0 {
		return 1 / float32(1-level)
	}
	return float32(1 + level)
}
func viewerZoom(s *ViewerState, direction int, fit float32, limit int, anchor ui.Point, vw, vh, nw, nh float32) {
	old := viewerScale(s, fit)
	fitLevel := fit - 1
	if fit < 1 {
		fitLevel = 1 - 1/fit
	}
	if s.Fit {
		candidate := int(math.Ceil(float64(fitLevel)))
		allowed := candidate <= limit
		if direction < 0 {
			candidate = int(math.Floor(float64(fitLevel)))
			allowed = candidate >= -limit
		}
		if allowed {
			s.Zoom, s.Fit = candidate, false
		}
	} else {
		candidate := s.Zoom + direction
		towardFit, crossesFit, atLimit := float32(s.Zoom) < fitLevel, float32(candidate) > fitLevel, s.Zoom == limit
		canStep := s.Zoom < limit
		if direction < 0 {
			towardFit, crossesFit, atLimit = float32(s.Zoom) > fitLevel, float32(candidate) < fitLevel, s.Zoom == -limit
			canStep = s.Zoom > -limit
		}
		if towardFit && (crossesFit || atLimit) {
			s.Fit = true
		} else if canStep {
			s.Zoom = candidate
		}
	}
	scale := viewerScale(s, fit)
	// Keep the image point under the cursor fixed across the scale change.
	s.Pan.X = (s.Pan.X+vw/2-anchor.X)*(scale/old) + anchor.X - vw/2
	s.Pan.Y = (s.Pan.Y+vh/2-anchor.Y)*(scale/old) + anchor.Y - vh/2
	clampPan(s, nw*scale, nh*scale, vw, vh)
}
func clampPan(s *ViewerState, iw, ih, vw, vh float32) {
	mx, my := max((iw-vw)/2, 0), max((ih-vh)/2, 0)
	s.Pan.X = max(-mx, min(s.Pan.X, mx))
	s.Pan.Y = max(-my, min(s.Pan.Y, my))
}
func percent(scale float32) string { return strconv.Itoa(int(math.Round(float64(scale*100)))) + "%" }
