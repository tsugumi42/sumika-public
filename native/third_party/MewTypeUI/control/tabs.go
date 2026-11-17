package control

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"math"
	"slices"
	"time"
)

type TabItem struct {
	ID, Label string
	Disabled  bool
}
type TabsVariant uint8

const (
	UnderlineTabs TabsVariant = iota
	ProfileTabs
)

type TabsOptions struct {
	Variant             TabsVariant
	Disabled, Instant   bool
	OnSelect, OnContext func(string)
	// Set by the host when the surrounding page last handled vertical scrolling.
	LastVerticalScroll time.Time
}
type SegmentsOptions struct {
	Settings, Disabled, Instant bool
	StrictSkip                  float32
	OnSelect                    func(string)
}
type stripSection struct{ x, w, text float32 }
type stripLocal struct {
	items                       []TabItem
	sections                    []stripSection
	width, total, scale         float32
	style                       theme.TextStyle
	font                        string
	gap, padding, strict        float32
	tabPadding                  float32
	outline                     ui.Path
	outlineStops                [3]ui.GradientStop
	kind                        uint8
	left, span                  timeline
	scroll                      ui.ScrollState
	active, preview, pending    string
	deadline                    time.Time
	down, dragged               bool
	startX, startY, startScroll float32
	handle                      ui.Handle
}

// SettingsSlider is a discrete label strip, distinct from settings.Slider's
// numeric seek track. Fast pointer releases commit at the selection deadline.
func SettingsSlider(c *ui.Context, items []TabItem, active string, o SegmentsOptions) ui.Element {
	return strip(c, items, active, 0, o, TabsOptions{})
}

// Tabs separates host active ID, keyboard focus, and pointer feedback. Profile
// uses natural-width islands; Underline uses the discrete width negotiation.
func Tabs(c *ui.Context, items []TabItem, active string, o TabsOptions) ui.Element {
	kind := uint8(1)
	if o.Variant == ProfileTabs {
		kind = 2
	}
	return strip(c, items, active, kind, SegmentsOptions{}, o)
}
func (s *stripLocal) layout(c *ui.Context, items []TabItem, width, gap, padding, strict float32, kind uint8, style theme.TextStyle, tabPadding float32) bool {
	font := theme.T(c).Typography().Font()
	scale := c.Preferences().TextScale
	if slices.Equal(s.items, items) && s.width == width && s.style == style && s.font == font && s.scale == scale && s.kind == kind && s.gap == gap && s.padding == padding && s.strict == strict && s.tabPadding == tabPadding {
		return false
	}
	s.items = append(s.items[:0], items...)
	s.sections = slices.Grow(s.sections[:0], len(items))
	s.sections = s.sections[:len(items)]
	s.width = width
	s.style = style
	s.font = font
	s.scale = scale
	s.kind = kind
	s.gap, s.padding, s.strict = gap, padding, strict
	s.tabPadding = tabPadding
	sum := float32(0)
	for i, v := range items {
		w, _ := measure(c, v.Label, style, 0)
		s.sections[i].text = w
		sum += w
	}
	n := float32(len(items))
	space := max(0, width-2*padding-float32(max(0, len(items)-1))*gap)
	equal := space / max(n, 1)
	proportional := false
	for _, v := range s.sections {
		if v.text >= equal && sum <= space {
			proportional = true
		}
	}
	pos := padding
	for i := range items {
		w := equal
		if proportional {
			w = s.sections[i].text + (space-sum)/max(n, 1)
		}
		if sum > space {
			w = s.sections[i].text + 2*gap
		}
		if strict > 0 {
			w = s.sections[i].text + strict
		}
		if kind == 2 {
			w = s.sections[i].text + 2*tabPadding
		}
		left := floor(pos) + float32(i)*gap
		s.sections[i].x = left
		s.sections[i].w = round(pos+w) - (left - float32(i)*gap)
		pos += w
	}
	s.total = max(width, round(pos)+float32(max(0, len(items)-1))*gap+padding)
	return true
}
func strip(c *ui.Context, items []TabItem, active string, kind uint8, so SegmentsOptions, to TabsOptions) ui.Element {
	t := theme.T(c)
	m, tm, pm := t.Metrics().Segments(), t.Metrics().Tabs(), t.Metrics().ProfileTabs()
	height, gap, padding, labelTop := m.Height(), m.Gap(), m.Padding(), m.LabelTop()
	disabled, instant := so.Disabled, so.Instant
	tr := t.Motion().Segments()
	style := t.Typography().Body()
	onSelect := so.OnSelect
	pl, pt, pr, pb := float32(0), float32(0), float32(0), float32(0)
	if so.Settings {
		pl, pt, pr, pb = m.OuterLeft(), m.OuterTop(), m.OuterRight(), m.OuterBottom()
	}
	if kind > 0 {
		height = tm.Height()
		gap = tm.Gap()
		padding = 0
		labelTop = tm.LabelTop()
		tr = t.Motion().Tabs()
		style = t.Typography().ContentTitle()
		disabled = to.Disabled
		instant = to.Instant
		onSelect = to.OnSelect
	}
	if kind == 2 {
		height = pm.Height() - pm.MarginTop() - pm.MarginBottom()
		pl, pt, pr, pb = pm.MarginLeft(), pm.MarginTop(), pm.MarginRight(), pm.MarginBottom()
		tr = t.Motion().ProfileTabs()
	}
	if len(items) == 0 {
		return ui.Box(c).FillWidth().Height(0)
	}
	_, fh := measure(c, "M", style, 0)
	height = max(height, labelTop+fh)
	if kind == 2 {
		height = max(pm.Height()-pt-pb, fh+2*pm.ActiveInset())
		labelTop = floor((height - fh) / 2)
	}
	root := ui.Box(c).FillWidth().Height(height + pt + pb).Shrink(0).Role(ui.RoleTabList).FocusGroup(ui.Horizontal).Disabled(disabled)
	s := ui.Local(root, "strip", func() stripLocal { return stripLocal{} })
	width := max(0, root.LayoutBounds().W-pl-pr)
	strict, tabPadding := so.StrictSkip, float32(0)
	if strict == 0 {
		strict = m.StrictSkip()
	}
	if kind == 2 {
		tabPadding = pm.TabPadding()
	}
	changed := s.layout(c, items, width, gap, padding, strict, kind, style, tabPadding)
	services := c.Services()
	root.DrawOver(func(_ *ui.Painter, r ui.Rect) {
		if r.W-pl-pr != s.width {
			services.Invalidate()
		}
	})
	// paint/settings-slider.md#状态动效与提交时机: invalidation cancels a pending ID.
	if s.down || s.pending != "" {
		available := false
		if !disabled {
			for _, v := range items {
				if v.ID == s.preview && !v.Disabled {
					available = true
					break
				}
			}
		}
		if !available {
			s.down = false
			s.pending = ""
			s.preview = active
		}
	}
	selected := active
	if s.down || s.pending != "" {
		selected = s.preview
	}
	if s.pending != "" && !c.Now().Before(s.deadline) {
		id := s.pending
		s.pending = ""
		root.SetChanged().OnChange(func() {
			if onSelect != nil {
				onSelect(id)
			}
		})
	} else if s.pending != "" {
		c.After(s.deadline.Sub(c.Now()))
	}
	x, w := float32(0), float32(0)
	for i, v := range items {
		if v.ID == selected {
			x = s.sections[i].x
			w = s.sections[i].w
			if kind == 1 {
				lw := min(w, s.sections[i].text)
				x += floor((w - lw) / 2)
				w = lw
			}
			break
		}
	}
	fast := instant || changed
	ax := s.left.update(c, x, tr, fast)
	aw := s.span.update(c, w, tr, fast)
	if kind == 2 && active != s.active {
		for i, v := range items {
			if v.ID == active {
				r := s.sections[i]
				if r.x < s.scroll.X || r.x+r.w > s.scroll.X+width {
					target := max(0, min(s.total-width, r.x+(r.w-width)/2))
					s.scroll.ScrollTo(target, 0, ui.Animation{Duration: t.Motion().ProfileTabsScroll().Duration(), Ease: t.Motion().ProfileTabsScroll().Easing().Curve()})
				}
				break
			}
		}
	}
	s.active = active
	rtl := c.Direction() == ui.RightToLeft
	if kind == 2 {
		// paint/tabs.md#info岛状变体: shadow, island, then clipped content.
		root.Draw(func(p *ui.Painter, r ui.Rect) {
			island := ui.Rect{X: r.X + pl, Y: r.Y + pt, W: width, H: height}
			p.Shadow(island, height/2, 0, pm.ShadowY(), pm.ShadowBlur(), 0, t.Colors().Window().ShadowForeground().Alpha(pm.ShadowOpacity()))
			p.Fill(island, t.Colors().Window().Background(), height/2)
		})
	}
	root.Children(func() {
		scroll := ui.ScrollHorizontal(c.Key("viewport")).Absolute().Left(pl).Right(pr).Top(pt).Height(height).Scrollbars(false).TrackScroll(&s.scroll).Clip()
		if kind == 2 {
			scroll.Radius(height / 2)
		}
		scroll.Children(func() {
			canvas := ui.Box(c.Key("canvas")).Width(s.total).Height(height).Shrink(0).Bind(&s.handle)
			// paint/tabs.md#下划线变体 and #info岛状变体: sourced active geometry.
			canvas.Draw(func(p *ui.Painter, r ui.Rect) {
				if aw <= 0 {
					return
				}
				if kind == 1 {
					bar := mirrorRect(ui.Rect{X: max(0, ax-tm.BarStroke()/2), Y: tm.BarTop(), W: min(s.total, ax+aw+tm.BarStroke()/2) - max(0, ax-tm.BarStroke()/2), H: tm.BarStroke()}, s.total, rtl)
					bar.X += r.X
					bar.Y += r.Y
					p.Fill(bar, t.Colors().Control().SliderBackgroundActive(), tm.Radius())
				}
				if kind == 2 {
					bar := mirrorRect(ui.Rect{X: ax + pm.ActiveInset(), Y: pm.ActiveInset(), W: max(0, aw-2*pm.ActiveInset()), H: height - 2*pm.ActiveInset()}, s.total, rtl)
					bar.X += r.X
					bar.Y += r.Y
					p.Fill(bar, t.Colors().Control().SurfaceBackgroundHover(), bar.H/2)
				}
			})
			canvas.Children(func() {
				for i, v := range items {
					section := s.sections[i]
					// The strip supplies whole-control disabling and its single opacity multiplier.
					b := ui.ButtonBase(c.Key(v.ID)).Label(v.Label).Role(ui.RoleTab).Checked(v.ID == active).Disabled(v.Disabled && !disabled).Height(height).Shrink(0)
					logical(c, b, section.x, section.w).Top(0)
					weight := 1 - min(1, float32(math.Abs(float64(ax-section.x)))/max(1, min(section.w, aw)))
					normal, chosen := t.Colors().Window().ActiveTextForeground(), t.Colors().Window().ActiveTextForeground()
					if so.Settings || kind > 0 {
						normal = t.Colors().Window().MutedText()
					}
					if kind > 0 {
						chosen = t.Colors().Control().SurfaceForeground()
					}
					ink := normal.Mix(chosen, weight)
					if kind == 0 {
						// paint/settings-slider.md#几何: active intersection, not a round knob.
						b.Draw(func(p *ui.Painter, r ui.Rect) {
							inactive := t.Colors().Control().SliderBackgroundInactive()
							if so.Settings {
								inactive = t.Colors().Window().BackgroundHover()
							}
							p.Fill(ui.Rect{X: r.X, Y: r.Y + m.BarTop(), W: r.W, H: m.BarHeight()}, inactive, m.Radius())
							l, rr := max(section.x, ax), min(section.x+section.w, ax+aw)
							if rr > l {
								local := mirrorRect(ui.Rect{X: l - section.x, Y: m.BarTop(), W: rr - l, H: m.BarHeight()}, section.w, rtl)
								local.X += r.X
								local.Y += r.Y
								p.Fill(local, t.Colors().Control().SliderBackgroundActive(), m.Radius())
							}
						})
					}
					wave := ui.Local(b, "wave", func() pulse { return pulse{} })
					if kind > 0 {
						wave.update(c, b, !disabled && !v.Disabled && !s.dragged, nil)
						b.DrawOver(func(p *ui.Painter, r ui.Rect) {
							clip := r
							radius := float32(0)
							if kind == 1 {
								clip.H -= tm.RippleBottomSkip()
							} else {
								clip.X += pm.ActiveInset()
								clip.Y += pm.ActiveInset()
								clip.W -= 2 * pm.ActiveInset()
								clip.H -= 2 * pm.ActiveInset()
								radius = clip.H / 2
							}
							p.Clip(clip, radius, func() { wave.paint(p, r, t.Colors().Control().SurfaceBackgroundPressed()) })
						})
					}
					b.Children(func() {
						text(c, v.Label, style, ink).Absolute().Left(max(0, (section.w-section.text)/2)).Top(labelTop).Width(section.text).SingleLine().IgnoreInput(true)
					})
					chooseAt := func(localX float32, at time.Time) {
						px := section.x + localX
						if rtl {
							px = section.x + section.w - localX
						}
						for j, item := range items {
							part := s.sections[j]
							if px >= part.x && px < part.x+part.w && !item.Disabled {
								if s.preview != item.ID {
									s.preview = item.ID
									s.deadline = at.Add(tr.Duration())
								}
								return
							}
						}
					}
					hovered := b.Hovered()
					b.HandleInput(func(ev ui.InputEvent) bool {
						if disabled || v.Disabled {
							return false
						}
						switch ev.Kind {
						case ui.InputPointerDown:
							if ev.Button != 0 {
								return false
							}
							s.startX = ev.X
							s.startY = ev.Y
							s.startScroll = s.scroll.X
							s.dragged = false
							if kind == 0 {
								s.down = true
								s.preview = active
								chooseAt(ev.X, ev.Time)
								return true
							}
						case ui.InputPointerMove:
							if kind == 0 && s.down {
								chooseAt(ev.X, ev.Time)
								return true
							}
							if kind == 2 && ev.Button == 0 {
								dx, dy := ev.X-s.startX, ev.Y-s.startY
								threshold := t.Metrics().Choose().ClearInset()
								if !s.dragged && float32(math.Abs(float64(dx))) > threshold && float32(math.Abs(float64(dx))) > float32(math.Abs(float64(dy))) {
									s.dragged = true
								}
								if s.dragged {
									if rtl {
										dx = -dx
									}
									s.scroll.X = min(max(0, s.startScroll-dx), max(0, s.total-width))
									return true
								}
							}
						case ui.InputPointerUp:
							if ev.Button != 0 {
								return false
							}
							if kind == 0 && s.down {
								s.down = false
								chooseAt(ev.X, ev.Time)
								s.pending = s.preview
								if !ev.Time.Before(s.deadline) {
									s.pending = ""
									if onSelect != nil {
										onSelect(s.preview)
									}
								}
								return true
							}
							if kind == 2 && s.dragged {
								return true
							}
						case ui.InputScroll:
							if kind == 2 {
								if ev.Precise && ev.DX == 0 {
									return false
								}
								delta := ev.DX
								if delta == 0 {
									grace := t.Motion().ProfileTabsWheelGrace().Duration()
									if !hovered {
										grace = t.Motion().ProfileTabsWheelRest().Duration()
									}
									if !to.LastVerticalScroll.IsZero() && ev.Time.Sub(to.LastVerticalScroll) < grace {
										return false
									}
									delta = ev.DY
								}
								if rtl {
									delta = -delta
								}
								s.scroll.X = min(max(0, s.scroll.X+delta), max(0, s.total-width))
								return true
							}
						}
						return false
					})
					if kind == 0 {
						b.OnClick(func() { s.preview = v.ID; s.pending = v.ID; s.deadline = c.Now().Add(tr.Duration()) })
					} else {
						b.OnClick(func() {
							if !s.dragged && onSelect != nil {
								onSelect(v.ID)
							}
						})
					}
					if b.RightClicked() && to.OnContext != nil {
						root.SetChanged().OnChange(func() { to.OnContext(v.ID) })
					}
				}
			})
		})
	})
	if kind == 2 {
		// paint/tabs.md#info岛状变体; CTL-TABS procedural outline/fade substitution.
		root.DrawOver(func(p *ui.Painter, r ui.Rect) {
			island := ui.Rect{X: r.X + pl, Y: r.Y + pt, W: width, H: height}
			bg := t.Colors().Window().Background()
			lightness := (float32(max(bg.R, bg.G, bg.B)) + float32(min(bg.R, bg.G, bg.B))) / 2
			line := t.Metrics().Control().BorderWidth()
			if lightness >= pm.LightnessThreshold() {
				p.Stroke(island, t.Colors().ProfileTabs().OutlineLight().Alpha(pm.LightStrokeAlpha()), height/2, line*pm.LightStrokeRatio())
			} else {
				ink := t.Colors().ProfileTabs().OutlineDark()
				inner := ui.Rect{X: island.X + line/2, Y: island.Y + line/2, W: island.W - line, H: island.H - line}
				capsulePath(&s.outline, inner)
				s.outlineStops = [3]ui.GradientStop{{Color: ink.Alpha(pm.DarkTopAlpha()), At: 0}, {Color: ink.Alpha(pm.DarkMidAlpha()), At: .5}, {Color: ink.Alpha(pm.DarkBottomAlpha()), At: 1}}
				p.StrokePathGradient(&s.outline, line, ui.LinearGradient{Stops: s.outlineStops[:], Angle: 180})
			}
			fade := pm.TabPadding()
			p.Clip(island, height/2, func() {
				left, right := s.scroll.X, max(0, s.total-width-s.scroll.X)
				if rtl {
					left, right = right, left
				}
				if left > 0 {
					a := min(1, left/(fade*pm.FadeFactor()))
					p.FillGradient(ui.Rect{X: island.X, Y: island.Y, W: fade, H: height}, ui.LinearGradient{From: bg.Alpha(a), To: bg.Alpha(0), Angle: 90}, 0)
				}
				if right > 0 {
					a := min(1, right/(fade*pm.FadeFactor()))
					p.FillGradient(ui.Rect{X: island.X + island.W - fade, Y: island.Y, W: fade, H: height}, ui.LinearGradient{From: bg.Alpha(0), To: bg.Alpha(a), Angle: 90}, 0)
				}
			})
		})
	}
	return root
}

// paint/tabs.md#info岛状变体: four cubic quarter-circles form the inset capsule.
func capsulePath(path *ui.Path, r ui.Rect) {
	a := r.H / 2
	k := a * float32(4*(math.Sqrt2-1)/3)
	l, t, x, b := r.X, r.Y, r.X+r.W, r.Y+r.H
	path.Reset().MoveTo(l+a, t).LineTo(x-a, t).
		CubeTo(x-a+k, t, x, t+a-k, x, t+a).
		CubeTo(x, t+a+k, x-a+k, b, x-a, b).LineTo(l+a, b).
		CubeTo(l+a-k, b, l, b-a+k, l, b-a).
		CubeTo(l, t+a-k, l+a-k, t, l+a, t).Close()
}
