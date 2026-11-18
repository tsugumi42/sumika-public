package control

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/settings"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type Category struct {
	ID, Label, GroupID, GroupTitle string
	Icon                           *ui.SVG
	Disabled                       bool
}
type CategoriesOptions struct {
	Label    string
	Arrow    bool
	OnSelect func(string)
}
type categoriesLocal struct {
	root    ui.Handle
	rows    map[string]*ui.Handle
	focused string
}

// SideCategories owns only navigation feedback. Page state and narrow-layout
// routing remain with the host; compose it with layout's existing page stack.
func SideCategories(c *ui.Context, items []Category, selected string, o CategoriesOptions) ui.Element {
	root := ui.Scroll(c).MinWidth(theme.T(c).Metrics().Layout().ListMinWidth()).FillHeight().FocusGroup(ui.Vertical).Role(ui.RoleGroup).Label(o.Label)
	s := ui.Local(root, "categories", func() categoriesLocal { return categoriesLocal{rows: map[string]*ui.Handle{}} })
	root.Bind(&s.root)
	seenFocus := false
	root.Children(func() {
		group := ""
		for _, v := range items {
			if v.GroupID != group {
				if group != "" {
					settings.Divider(c.Key(struct{ Group string }{v.GroupID}), "", settings.DividerOptions{})
				}
				if v.GroupTitle != "" {
					settings.SectionTitle(c.Key(struct{ Heading string }{v.GroupID}), v.GroupTitle)
				}
				group = v.GroupID
			}
			h := s.rows[v.ID]
			if h == nil {
				h = &ui.Handle{}
				s.rows[v.ID] = h
			}
			// paint/side-categories.md#复用的行外观 and #常驻分类组合我定的.
			settings.Button(c.Key(v.ID), v.Label, settings.ButtonOptions{Icon: v.Icon, Disabled: v.Disabled, Selected: v.ID == selected, Arrow: o.Arrow, OnActivate: func() {
				if o.OnSelect != nil {
					o.OnSelect(v.ID)
				}
			}}).Bind(h).Role(ui.RoleTab).Checked(v.ID == selected)
			if h.Focused(c) {
				s.focused = v.ID
				seenFocus = true
			}
		}
	})
	if s.focused != "" && !seenFocus {
		exists := false
		for _, v := range items {
			if v.ID == s.focused && !v.Disabled {
				exists = true
				break
			}
		}
		if !exists {
			root.Focusable()
			s.root.Focus(c)
			s.focused = ""
		}
	}
	for id, h := range s.rows {
		found := false
		for _, v := range items {
			if v.ID == id {
				found = true
				break
			}
		}
		if !found {
			h.CancelFocus(c)
			delete(s.rows, id)
		}
	}
	return root
}

type TitleAction struct {
	ID, Label  string
	Icon       *ui.SVG
	Disabled   bool
	OnActivate func()
}
type PageTitleOptions struct {
	Subtitle, Badge                string
	BackLabel, CloseLabel          string
	OnBack, OnClose                func()
	Actions                        []TitleAction
	Layer, Inline, Hidden, Instant bool
}

type titleVisual struct {
	motion          timeline
	image, snapshot *ui.Bitmap
	generation      int
	hidden          bool
}
type titleCapture struct {
	state      *titleVisual
	generation int
}

func (titleCapture) EffectMargin() ui.Point { return ui.Point{} }
func (v titleCapture) PaintEffect(p *ui.Painter, r ui.Rect, image *ui.Bitmap, _ ui.Point) {
	if v.state.generation == v.generation {
		v.state.image = image
		if v.state.motion.value != v.state.motion.target && v.state.snapshot == nil {
			v.state.snapshot = image
		}
	}
	p.ImageRegion(image, ui.Rect{}, r, ui.ImageOptions{})
}

// PageTitle is a flat Info top bar; Inline opts into the documented flowing
// composition. Only explicit action slots navigate.
func PageTitle(c *ui.Context, title string, o PageTitleOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().PageTitle()
	style := t.Typography().PageTitle()
	x, y, h := m.Left(), m.Top(), m.Height()
	sy := m.SubtitleTop()
	if o.Subtitle != "" {
		style = t.Typography().ContentTitle()
		x = m.SubtitleLeft()
		y = m.TitleWithSubtitleTop()
	}
	if o.Layer {
		h = m.LayerHeight()
		style = t.Typography().DialogTitle()
		if o.Subtitle != "" {
			y = m.LayerTitleTop()
			sy = m.LayerSubtitleTop()
		}
	}
	if o.OnBack != nil {
		x = m.BackWidth()
	}
	if o.Inline {
		style = t.Typography().PageTitle()
	}
	_, fh := measure(c, "M", style, 0)
	_, sh := measure(c, "M", t.Typography().Body(), 0)
	if o.Subtitle != "" {
		sy = max(sy, y+fh)
		h = max(h, sy+sh+(m.Height()-m.SubtitleTop()-m.TextMaxHeight()))
	} else {
		h = max(h, y+fh+(m.Height()-m.Top()-m.TextMaxHeight()))
	}
	// paint/page-title.md#几何和层次: flat full-width surface, layer top corners only.
	root := ui.Box(c).FillWidth().Shrink(0).Background(t.Colors().Window().Background())
	if !o.Inline {
		root.Height(h)
	}
	if o.Layer {
		r := t.Metrics().Dialog().Radius()
		root.Radius(r, r, 0, 0)
	}
	visual := ui.Local(root, "mode", func() titleVisual { return titleVisual{hidden: o.Hidden} })
	if visual.hidden != o.Hidden {
		visual.hidden = o.Hidden
		visual.snapshot = visual.image
		if !o.Hidden {
			visual.snapshot = nil
		}
		visual.generation++
	}
	a := visual.motion.update(c, unit(!o.Hidden), t.Motion().PageTitle(), o.Instant)
	if a == visual.motion.target {
		visual.snapshot = nil
	}
	// paint/page-title.md#状态与动效: 5x transparent margin changes visible scale.
	scale := 1 - (1-m.HiddenScale())*(1-1/t.Metrics().Compose().SwapScale())*(1-a)
	right := float32(len(o.Actions)) * t.Metrics().IconButton().Width()
	if o.OnClose != nil {
		right += m.CloseWidth()
	}
	badgeWidth := float32(0)
	if o.Badge != "" {
		badgeWidth, _ = measure(c, o.Badge, t.Typography().Body(), 0)
		right += badgeWidth + 2*m.BadgeGap()
	}
	width := root.LayoutBounds().W
	services := c.Services()
	root.DrawOver(func(_ *ui.Painter, r ui.Rect) {
		if r.W != width {
			services.Invalidate()
		}
	})
	// paint/page-title.md#内嵌页面标题我定的: real flow for wrapping content.
	if o.Inline {
		root.Column().Padding(m.Top(), m.Left(), t.Metrics().Settings().SectionTitlePaddingBottom(), m.Left())
		actionsWidth := float32(len(o.Actions)) * t.Metrics().IconButton().Width()
		if o.OnClose != nil {
			actionsWidth += t.Metrics().IconButton().Width()
		}
		below := width-2*m.Left()-actionsWidth <= 0
		texts := func() {
			text(c, title, style, t.Colors().Window().BoldForeground()).FillWidth().Role(ui.RoleHeading)
			if o.Subtitle != "" {
				text(c, o.Subtitle, t.Typography().Body(), t.Colors().Window().MutedText()).FillWidth().Margin(t.Metrics().Settings().SectionSkip(), 0, 0, 0)
			}
		}
		actions := func() {
			for _, v := range o.Actions {
				IconButton(c.Key(v.ID), v.Label, v.Icon, IconButtonOptions{Disabled: v.Disabled, OnActivate: v.OnActivate})
			}
			if o.OnClose != nil {
				IconButton(c.Key("close"), o.CloseLabel, closeIcon, IconButtonOptions{OnActivate: o.OnClose})
			}
		}
		root.Children(func() {
			if below {
				ui.Column(c.Key("text")).FillWidth().Children(texts)
				if actionsWidth > 0 {
					ui.Row(c.Key("actions")).FillWidth().Margin(t.Metrics().Settings().SectionSkip(), 0, 0, 0).Children(actions)
				}
			} else {
				ui.Row(c).FillWidth().Children(func() {
					ui.Column(c.Key("text")).Grow(1).MinWidth(0).Children(texts)
					if actionsWidth > 0 {
						ui.Row(c.Key("actions")).Width(actionsWidth).Shrink(0).Children(actions)
					}
				})
			}
		})
		return root
	}
	root.Children(func() {
		titleLayer := ui.Box(c.Key("titles")).Absolute().Fill().Opacity(a).Scale(scale, scale).Hidden(a == 0)
		titleLayer.Children(func() {
			capture := ui.Box(c.Key(visual.generation)).Absolute().Fill().Cache().Effect(titleCapture{visual, visual.generation})
			if visual.snapshot != nil {
				capture.Draw(func(p *ui.Painter, r ui.Rect) { p.ImageRegion(visual.snapshot, ui.Rect{}, r, ui.ImageOptions{}) })
				return
			}
			capture.Children(func() {
				logical(c, text(c, title, style, t.Colors().Window().BoldForeground()).SingleLine().Role(ui.RoleHeading), x, max(0, width-right-x)).Top(y)
				if o.Subtitle != "" {
					logical(c, text(c, o.Subtitle, t.Typography().Body(), t.Colors().Window().MutedText()).SingleLine(), x, max(0, width-right-x)).Top(sy)
				}
				if o.Badge != "" {
					tail := right - badgeWidth - m.BadgeGap()
					logical(c, text(c, o.Badge, t.Typography().Body(), t.Colors().Window().MutedText()).SingleLine(), max(0, width-tail-badgeWidth), badgeWidth).Top(y)
				}
			})
		})
		if o.OnBack != nil {
			icon := backIcon
			b := IconButton(c.Key("back"), o.BackLabel, icon, IconButtonOptions{Kind: BackIcon, OnActivate: o.OnBack}).Absolute().Height(h)
			if c.Direction() == ui.RightToLeft {
				b.Right(0)
			} else {
				b.Left(0)
			}
		}
		offset := float32(0)
		if o.OnClose != nil {
			b := IconButton(c.Key("close"), o.CloseLabel, closeIcon, IconButtonOptions{Kind: CloseIcon, OnActivate: o.OnClose}).Absolute().Top(0).Height(h)
			if c.Direction() == ui.RightToLeft {
				b.Left(offset)
			} else {
				b.Right(offset)
			}
			offset += m.CloseWidth()
		}
		for _, v := range o.Actions {
			b := IconButton(c.Key(v.ID), v.Label, v.Icon, IconButtonOptions{Disabled: v.Disabled, OnActivate: v.OnActivate}).Absolute().Top((h - t.Metrics().IconButton().Height()) / 2)
			if c.Direction() == ui.RightToLeft {
				b.Left(offset)
			} else {
				b.Right(offset)
			}
			offset += t.Metrics().IconButton().Width()
		}
	})
	return root
}
