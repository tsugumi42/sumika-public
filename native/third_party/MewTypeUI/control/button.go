package control

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
	"strings"
	"time"
)

type ButtonTone uint8

const (
	Primary ButtonTone = iota
	Light
)

type NumberSuffix struct {
	Text  string
	Value int64
}
type RoundButtonOptions struct {
	Tone                                       ButtonTone
	Disabled, FullRadius, ToUpper, Transparent bool
	// FullWidth >0 fixes the width; <0 adds its magnitude to natural content.
	FullWidth    float32
	Corners      *[4]float32
	Icon         *ui.SVG
	TrailingIcon bool // CTL-SELECT: header.iconSize plus menu.rightSkip.
	IconPosition *ui.Point
	Numbers      NumberSuffix
	OnActivate   func()
}
type numberAnimation struct {
	shown, previous NumberSuffix
	ready, running  bool
	at              time.Time
	q, slot         float32
	old, new        []rune
	style           theme.TextStyle
	font            string
	scale           float32
}

func (s *numberAnimation) update(c *ui.Context, target NumberSuffix) float32 {
	t := theme.T(c)
	style := t.Typography().ContentTitle()
	tr := t.Motion().ButtonNumbers()
	if s.slot == 0 || s.style != style || s.font != t.Typography().Font() || s.scale != c.Preferences().TextScale {
		s.style = style
		s.font = t.Typography().Font()
		s.scale = c.Preferences().TextScale
		s.slot = 0
		for _, r := range "012345678" {
			w, _ := measure(c, string(r), style, 0)
			s.slot = max(s.slot, w)
		}
	}
	if !s.ready || target.Text == "" || c.Preferences().ReduceMotion || tr.Duration() == 0 {
		s.ready = true
		s.running = false
		s.shown = target
		s.q = 1
		s.new = []rune(target.Text)
		s.old = nil
	} else {
		if s.running {
			s.q = tr.Easing().Curve()(min(float32(c.Now().Sub(s.at))/float32(tr.Duration()), 1))
			if s.q >= 1 {
				s.running = false
			}
		}
		if !s.running && s.shown != target {
			s.previous = s.shown
			s.shown = target
			s.old = s.new
			s.new = []rune(target.Text)
			s.at = c.Now()
			s.q = 0
			s.running = true
		}
	}
	if s.running {
		c.AnimationFrame()
	}
	if !s.running {
		return float32(len(s.new)) * s.slot
	}
	return (float32(len(s.old)) + (float32(len(s.new))-float32(len(s.old)))*s.q) * s.slot
}

// RoundButton separates the hit frame, background and naturally measured content.
func RoundButton(c *ui.Context, label string, o RoundButtonOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Button()
	style := t.Typography().ContentTitle()
	caption := label
	if o.ToUpper {
		caption = strings.ToUpper(caption)
	}
	tw, fh := measure(c, caption, style, 0)
	b := ui.ButtonBase(c).Label(label).Disabled(o.Disabled).Shrink(0).Clip().OnClick(o.OnActivate)
	nums := ui.Local(b, "numbers", func() numberAnimation { return numberAnimation{} })
	nw := nums.update(c, o.Numbers)
	iw := float32(0)
	ix, iy := -t.Metrics().Menu().RightSkip(), float32(0)
	if o.IconPosition != nil {
		ix = o.IconPosition.X
		iy = o.IconPosition.Y
	}
	extra := nw
	if o.Icon != nil {
		iw = t.Metrics().Header().IconSize()
		if ix < 0 {
			extra += iw - ix
		}
	}
	width := tw + extra + m.HorizontalExtra() + m.PaddingLeft() + m.PaddingRight()
	if o.FullWidth > 0 {
		width = o.FullWidth
		if o.FullRadius {
			width += m.PaddingLeft() + m.PaddingRight()
		}
	} else if o.FullWidth < 0 {
		width = tw + extra - o.FullWidth
	}
	height := max(m.Height(), m.TextTop()+fh) + m.PaddingTop() + m.PaddingBottom()
	b.Size(width, height).MaxWidth(width)
	radius := t.Metrics().Dialog().ButtonRadius()
	if o.FullRadius {
		radius = floor((height - m.PaddingTop() - m.PaddingBottom()) / 2)
	}
	corners := [4]float32{radius, radius, radius, radius}
	if o.Corners != nil {
		corners = *o.Corners
	}
	if c.Direction() == ui.RightToLeft {
		corners = [4]float32{corners[1], corners[0], corners[3], corners[2]}
	}
	b.Radius(corners[:]...)
	colors := t.Colors().Control()
	bg, fg, secondary, ripple := colors.PrimaryBackground(), colors.PrimaryForeground(), colors.PrimarySecondaryForeground(), colors.PrimaryBackgroundPressed()
	over := !o.Disabled && (b.Hovered() || b.Pressed())
	if over {
		bg = colors.PrimaryBackgroundHover()
		fg = colors.PrimaryForegroundHover()
		secondary = colors.PrimarySecondaryForegroundHover()
	}
	if o.Tone == Light {
		bg = colors.SurfaceBackground()
		fg = colors.SurfaceForeground()
		ripple = colors.SurfaceBackgroundPressed()
		if over {
			bg = colors.SurfaceBackgroundHover()
			fg = colors.SurfaceForegroundHover()
		}
		secondary = fg
	}
	if o.Transparent && !over {
		bg = bg.Alpha(0)
	}
	wave := ui.Local(b, "ripple", func() pulse { return pulse{} })
	wave.update(c, b, !o.Disabled, nil)
	// paint/round-button.md#几何与层次: one rounded background clips its ripple.
	b.Children(func() {
		ui.Box(c).Absolute().Left(m.PaddingLeft()).Right(m.PaddingRight()).Top(m.PaddingTop()).Bottom(m.PaddingBottom()).Radius(corners[:]...).Clip().Background(bg).IgnoreInput(true).Draw(func(p *ui.Painter, r ui.Rect) { wave.paint(p, r, ripple) })
		available := max(0, width-m.PaddingLeft()-m.PaddingRight())
		content := min(tw+extra, max(0, available-(m.Height()-fh)))
		x := m.PaddingLeft() + floor((available-content)/2)
		textX := x
		if o.Icon != nil && ix < 0 && !o.TrailingIcon {
			textX += iw - ix
		}
		titleWidth := max(0, content-extra)
		logical(c, text(c, caption, style, fg).SingleLine().IgnoreInput(true), textX, titleWidth).Top(m.PaddingTop() + m.TextTop())
		if o.Icon != nil {
			pos := ix
			if ix < 0 {
				pos = textX + ix - iw
				if o.TrailingIcon {
					pos = textX + titleWidth - ix
				}
			}
			y := iy
			if iy < 0 {
				y = m.PaddingTop() + m.TextTop() + iy
			} else if o.IconPosition == nil {
				y = (height - iw) / 2
			}
			logical(c, draw.Element(c, o.Icon, iw, iw).TextColor(fg).IgnoreInput(true), pos, iw).Top(y).Height(iw)
		}
		// paint/round-button.md#数字后缀与文字变换: right-aligned character slots.
		if nw > 0 {
			start := textX + titleWidth
			if caption != "" {
				start += m.NumbersGap()
			}
			slots := max(len(nums.old), len(nums.new))
			direction := float32(1)
			if nums.shown.Value < nums.previous.Value {
				direction = -1
			}
			for i := range slots {
				ni := i - (slots - len(nums.new))
				oi := i - (slots - len(nums.old))
				nr, or := rune(0), rune(0)
				if ni >= 0 {
					nr = nums.new[ni]
				}
				if oi >= 0 {
					or = nums.old[oi]
				}
				slotX := start + nw - float32(slots-i)*nums.slot
				paintChar := func(key string, r rune, alpha, dy float32) {
					if r == 0 || alpha == 0 {
						return
					}
					w, _ := measure(c, string(r), style, 0)
					logical(c, text(c.Key(struct {
						Slot  int
						Phase string
					}{i, key}), string(r), style, secondary).IgnoreInput(true).Opacity(alpha), slotX+(nums.slot-w)/2, w).Top(m.PaddingTop() + m.TextTop() + dy)
				}
				if !nums.running || nr == or {
					paintChar("digit", nr, 1, 0)
				} else {
					paintChar("old", or, 1-nums.q, direction*nums.q*fh)
					paintChar("new", nr, nums.q, -direction*(1-nums.q)*fh)
				}
			}
		}
	})
	return b
}

type LinkButtonOptions struct {
	Disabled   bool
	Foreground *ui.Color
	OnActivate func()
}

// LinkButton has a natural text hit box and an immediate hover underline.
func LinkButton(c *ui.Context, label string, o LinkButtonOptions) ui.Element {
	t := theme.T(c)
	w, h := measure(c, label, t.Typography().Body(), 0)
	b := ui.ButtonBase(c).Label(label).Role(ui.RoleLink).Width(w).MaxWidth(w).Height(h).Disabled(o.Disabled).OnClick(o.OnActivate)
	ink := t.Colors().Window().ActiveTextForeground()
	if o.Foreground != nil {
		ink = *o.Foreground
	}
	// paint/link-button.md#几何与画法.
	b.Children(func() {
		e := text(c, label, t.Typography().Body(), ink).SingleLine().FillWidth().IgnoreInput(true)
		if b.Hovered() && !o.Disabled {
			e.Underline()
		}
	})
	return b
}

type IconButtonKind uint8

const (
	StandaloneIcon IconButtonKind = iota
	BackIcon
	CloseIcon
)

type IconButtonOptions struct {
	Kind                   IconButtonKind
	Disabled, ForceRippled bool
	Foreground             *ui.Color
	OnActivate             func()
}

// IconButton requires the host's accessible label and prepared artwork.
func IconButton(c *ui.Context, label string, icon *ui.SVG, o IconButtonOptions) ui.Element {
	t := theme.T(c)
	m, pm := t.Metrics().IconButton(), t.Metrics().PageTitle()
	w, h, d, rx, ry := m.Width(), m.Height(), m.RippleSize(), m.RippleLeft(), m.RippleTop()
	size := t.Metrics().Header().IconSize()
	x := floor((w - size) / 2)
	if o.Kind == BackIcon {
		w = pm.BackWidth()
		h = pm.Height()
		d = pm.BackRippleSize()
		rx = pm.BackRippleLeft()
		ry = pm.BackRippleTop()
		x = pm.BackIconLeft()
	}
	if o.Kind == CloseIcon {
		w = pm.CloseWidth()
		h = pm.Height()
		d = pm.BackRippleSize()
		rx = pm.CloseRippleLeft()
		ry = pm.BackRippleTop()
		x = pm.CloseIconLeft()
	}
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Size(w, h).Shrink(0).Disabled(o.Disabled).OnClick(o.OnActivate)
	ink := t.Colors().Menu().IconForeground()
	if !o.Disabled && (b.Hovered() || b.Pressed() || o.ForceRippled) {
		ink = t.Colors().Menu().IconForegroundHover()
	}
	if o.Foreground != nil {
		ink = *o.Foreground
	}
	wave := ui.Local(b, "ripple", func() pulse { return pulse{} })
	rtl := c.Direction() == ui.RightToLeft
	if rtl {
		rx = w - rx - d
	}
	wave.update(c, b, !o.Disabled, func(x, y float32) bool { return x >= rx && x < rx+d && y >= ry && y < ry+d })
	// paint/icon-button.md#样式与几何: hit frame and circular ripple are distinct.
	b.Draw(func(p *ui.Painter, r ui.Rect) {
		circle := ui.Rect{X: r.X + rx, Y: r.Y + ry, W: d, H: d}
		p.Clip(circle, d/2, func() { wave.paint(p, r, t.Colors().Window().BackgroundHover()) })
	})
	b.Children(func() {
		logical(c, draw.Element(c, icon, size, size).TextColor(ink).IgnoreInput(true), x, size).Top(floor((h - size) / 2)).Height(size)
	})
	return b
}
