package settings

import (
	"slices"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/theme"
	"golang.org/x/text/unicode/norm"
)

// ChoiceItem identity is independent of its title, order and filter position.
// Groups are ordered by first occurrence; items keep their order within a group.
type ChoiceItem struct {
	ID, Title, Subtitle, GroupID, GroupTitle string
	Disabled                                 bool
}
type ChooseState struct {
	Open        bool
	Query       string
	scroll      ui.ScrollState
	input, rows ui.Handle
	cache       chooserCache
	high        string
	reveal      bool
	wasOpen     bool
	rowsFocused bool
	maxHeight   float32
}

func (s *ChooseState) Show() {
	s.Open = true
	s.Query = ""
	s.high = ""
	s.scroll.Y = 0
	s.maxHeight = 0
}

type ChooseOptions struct {
	SearchLabel, SearchPlaceholder, ClearLabel, EmptyLabel, CloseLabel string
	Wide, WideText, Locked                                             bool
	OnSelect                                                           func(string)
	OnClose                                                            func()
}
type choiceLayout struct {
	item              int
	y, h, titleHeight float32
	kind              uint8
	group             string
}
type chooserCache struct {
	items                            []ChoiceItem
	words                            [][]string
	matches                          []int
	layout                           []choiceLayout
	query                            string
	width, scale, total, rowEstimate float32
	source                           *theme.Theme
	wideText                         bool
	valid                            bool
}

// paint/choose-box.md#过滤与空结果. Prefix-match every query word against
// normalized title/subtitle keywords, preserving original item identity.
func keywords(value string) []string {
	value = norm.NFD.String(strings.ToLower(value))
	value = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, value)
	return strings.FieldsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("@-+()[]{}<>,.:!_;\"'\x00", r) })
}
func (s *chooserCache) prepare(c *ui.Context, items []ChoiceItem, query string, width float32, wideText bool) bool {
	changed := !slices.Equal(s.items, items)
	if changed {
		s.items = append(s.items[:0], items...)
		s.words = make([][]string, len(items))
		for i, v := range items {
			s.words[i] = keywords(v.Title + " " + v.Subtitle)
		}
	}
	t := theme.T(c)
	if s.valid && !changed && s.query == query && s.width == width && s.scale == c.Preferences().TextScale && s.source == t && s.wideText == wideText {
		return false
	}
	s.valid = true
	s.query = query
	s.width = width
	s.scale = c.Preferences().TextScale
	s.source = t
	s.wideText = wideText
	terms := keywords(query)
	s.matches = s.matches[:0]
	for i := range items {
		match := true
		for _, q := range terms {
			found := false
			for _, word := range s.words[i] {
				if strings.HasPrefix(word, q) {
					found = true
					break
				}
			}
			if !found {
				match = false
				break
			}
		}
		if match {
			s.matches = append(s.matches, i)
		}
	}
	m, sm := t.Metrics().Choose(), t.Metrics().Settings()
	_, fs := measure(c, "M", t.Typography().ContentTitle(), 0)
	_, fn := measure(c, "M", t.Typography().Body(), 0)
	s.rowEstimate = m.RowPaddingTop() + fs + m.RowTextGap() + fn + m.RowPaddingBottom()
	// paint/choose-box.md#列表行: preserve the language menu slot by default.
	reserve := t.Metrics().Scrollbar().Width() + m.ScrollInsetX() + float32(int((s.rowEstimate-t.Metrics().Header().ActionWidth())/2)) + t.Metrics().Header().ActionWidth()
	if wideText {
		reserve = 0
	}
	textWidth := max(1, width-3*m.RowPaddingLeft()-m.RowIconGap()-reserve)
	s.layout = s.layout[:0]
	s.total = 0
	// No map or temporary groups on unchanged frames; group order follows IDs.
	groups := make([]string, 0)
	for _, i := range s.matches {
		id := items[i].GroupID
		if !slices.Contains(groups, id) {
			groups = append(groups, id)
		}
	}
	for gi, g := range groups {
		if gi > 0 {
			s.layout = append(s.layout, choiceLayout{kind: 2, y: s.total, h: sm.DividerHeight(), group: g})
			s.total += sm.DividerHeight()
		}
		s.total += m.GroupPaddingTop()
		for _, i := range s.matches {
			if items[i].GroupID == g {
				if items[i].GroupTitle != "" {
					_, h := measure(c, items[i].GroupTitle, t.Typography().SettingsSectionTitle(), max(1, width-sm.SectionTitlePaddingLeft()-sm.SectionTitlePaddingRight()))
					h += sm.SectionTitlePaddingTop() + sm.SectionTitlePaddingBottom()
					s.layout = append(s.layout, choiceLayout{kind: 1, y: s.total, h: h, group: items[i].GroupTitle, item: i})
					s.total += h
				}
				break
			}
		}
		for _, i := range s.matches {
			v := items[i]
			if v.GroupID != g {
				continue
			}
			_, hs := measure(c, v.Title, t.Typography().ContentTitle(), textWidth)
			_, hn := measure(c, v.Subtitle, t.Typography().Body(), textWidth)
			h := m.RowPaddingTop() + hs + m.RowTextGap() + hn + m.RowPaddingBottom()
			s.layout = append(s.layout, choiceLayout{item: i, y: s.total, h: h, titleHeight: hs})
			s.total += h
		}
		s.total += m.GroupPaddingBottom()
	}
	if len(s.matches) == 0 {
		_, h := measure(c, "M", t.Typography().DialogBody(), 0)
		s.total = m.EmptyFontHeights() * h
	}
	return true
}

// ChooseBox shares overlay.Box's modal shell. Host acceptance controls closing;
// OnSelect alone never mutates the selected ID or starts a model request.
func ChooseBox(c *ui.Context, s *ChooseState, items []ChoiceItem, selected, title string, o ChooseOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Choose()
	dm := t.Metrics().Dialog()
	vw, vh := c.Size()
	width := dm.Width()
	if o.Wide {
		width = dm.WideWidth()
	}
	width = min(width, vw)
	opening := s.Open && !s.wasOpen
	if opening {
		s.input.Focus(c)
	}
	s.wasOpen = s.Open
	previousQuery := s.cache.query
	rebuilt := s.cache.prepare(c, items, s.Query, width, o.WideText)
	if rebuilt && !s.visible(s.high) {
		s.high = ""
	}
	if previousQuery != s.Query {
		s.high = ""
		s.scroll.Y = 0
		s.reveal = false
	}
	searchH := max(m.SearchFieldMinHeight(), fontHeight(c, t.Typography().Body())+m.SearchTextTop()+m.SearchTextBottom()) + m.SearchPaddingTop() + m.SearchPaddingBottom()
	s.maxHeight = max(s.maxHeight, min(dm.MaxBodyHeight(), searchH+s.cache.total))
	contentH := min(s.maxHeight, max(0, vh-2*dm.Margin()-dm.TitleHeight()-dm.ButtonTop()-dm.ButtonHeight()-dm.ButtonBottom()))
	viewH := max(0, contentH-searchH)
	closeBox := func() {
		s.Open = false
		if o.OnClose != nil {
			o.OnClose()
		}
	}
	activate := func() {
		id := s.high
		if id == "" {
			for _, i := range s.cache.matches {
				if !s.cache.items[i].Disabled {
					id = s.cache.items[i].ID
					break
				}
			}
		}
		if id != "" && o.OnSelect != nil {
			o.OnSelect(id)
		}
	}
	nav := func(key ui.Key) bool {
		step := 1
		if key == ui.KeyPageDown || key == ui.KeyPageUp {
			step = max(1, int(contentH/s.cache.rowEstimate))
		}
		switch key {
		case ui.KeyDown, ui.KeyPageDown:
			s.move(step)
		case ui.KeyUp, ui.KeyPageUp:
			s.move(-step)
		case ui.KeyHome:
			s.high = ""
			s.move(1)
		case ui.KeyEnd:
			s.high = ""
			s.move(len(s.cache.matches))
		case ui.KeyEnter, ui.KeySpace:
			activate()
		default:
			return false
		}
		s.reveal = true
		return true
	}
	boxState := overlay.BoxState{Open: s.Open}
	root := overlay.Box(c, &boxState, title, overlay.BoxOptions{Wide: o.Wide, Locked: o.Locked, FullBody: true, BodyHeight: contentH, Actions: []overlay.BoxAction{{ID: "close", Label: o.CloseLabel, KeepOpen: true}}, OnAction: func(string) { closeBox() }, OnClose: closeBox}, func() {
		// paint/choose-box.md#搜索输入区: fixed 44 DIP at normal text scale.
		search := ui.Box(c).FillWidth().Height(searchH).Shrink(0).Background(t.Colors().Dialog().SearchBackground()).Label(title + " search area")
		search.Children(func() {
			field := ui.TextInputBase(c.Key("search-field"), &s.Query).Bind(&s.input).Absolute().Top(m.SearchPaddingTop()).Left(m.SearchPaddingLeft()+m.SearchIconSkip()).Right(m.SearchPaddingRight()).Height(searchH-m.SearchPaddingTop()-m.SearchPaddingBottom()).Label(o.SearchLabel).Font(t.Typography().Font()).FontSize(t.Typography().Body().Size()*c.Preferences().TextScale).FontWeight(t.Typography().Body().Weight()).Padding(m.SearchTextTop(), m.SearchTextRight(), m.SearchTextBottom(), m.SearchTextLeft()).TextColor(t.Colors().Window().Text())
			// The base editor vertically centers its content. Compensate below
			// the line so the source text top remains exactly 6+7 DIP.
			field.Padding(m.SearchTextTop(), m.SearchTextRight(), max(m.SearchTextBottom(), searchH-m.SearchPaddingTop()-m.SearchPaddingBottom()-m.SearchTextTop()-fontHeight(c, t.Typography().Body())), m.SearchTextLeft())
			if opening {
				field.Focus()
			}
			hasQuery := strings.TrimSpace(s.Query) != ""
			if hasQuery {
				field.Right(m.SearchPaddingRight() + m.SearchCancelSkip())
			}
			field.OnTextKey(func(k ui.TextKey) bool {
				switch k.Key {
				case ui.KeyUp, ui.KeyDown, ui.KeyPageUp, ui.KeyPageDown, ui.KeyEnter:
					return nav(k.Key)
				}
				return false
			})
			placeholder := ui.Local(search, "placeholder-fade", func() timeline { return timeline{ready: true, value: 1, target: 1} }).update(c, unit(s.Query == ""), t.Motion().ChooseSearch(), false)
			if placeholder > 0 {
				ink := t.Colors().Compose().Placeholder()
				if s.input.Focused(c) {
					ink = t.Colors().Compose().PlaceholderFocused()
				}
				text(c, o.SearchPlaceholder, t.Typography().Body(), ink).Absolute().Left(m.SearchPaddingLeft() + m.SearchIconSkip() + m.SearchPlaceholderLeft()).Top(m.SearchPaddingTop() + m.SearchTextTop()).Right(m.SearchPaddingRight() + m.SearchPlaceholderRight()).SingleLine().Opacity(placeholder).IgnoreInput(true)
			}
			draw.Element(c, searchIcon, m.SearchIconSize(), m.SearchIconSize()).Absolute().Left(m.SearchPaddingLeft() + m.SearchIconLeft()).Top(m.SearchPaddingTop() + m.SearchIconTop()).TextColor(t.Colors().Menu().IconForeground()).IgnoreInput(true)
			clearA := ui.Local(search, "clear-fade", func() timeline { return timeline{ready: true} }).update(c, unit(hasQuery), t.Motion().ChooseClear(), false)
			clear := ui.ButtonBase(c.Key("clear")).Absolute().Right(0).Top(0).Size(m.ClearWidth(), m.ClearHeight()).Label(o.ClearLabel).Disabled(!hasQuery).Opacity(clearA)
			// paint/choose-box.md#搜索输入区: rotating, flat-cap X, independent of Lucide.
			path := ui.Local(clear, "cross-path", func() ui.Path { return ui.Path{} })
			half := m.ClearInset() * (m.ClearMinScale() + (1-m.ClearMinScale())*clearA) / 2
			path.Reset().Cap(ui.ButtCap).MoveTo(-half, -half).LineTo(half, half).MoveTo(-half, half).LineTo(half, -half)
			clear.Draw(func(p *ui.Painter, r ui.Rect) {
				p.Transform(ui.Translation(r.X+m.ClearPositionX()+m.ClearCanvas()/2, r.Y+m.ClearPositionY()+m.ClearCanvas()/2), 0, 0, func() {
					p.Transform(ui.Rotation(90*(1-clearA)), 0, 0, func() { p.StrokePath(path, m.ClearStroke(), t.Colors().Control().CancelIconForeground()) })
				})
			})
			clear.OnClick(func() { s.Query = ""; s.input.Focus(c) })
		})
		rowsFocused := s.rows.Focused(c)
		if rowsFocused && !s.rowsFocused && s.visible(selected) {
			s.high = selected
			s.reveal = true
		}
		s.rowsFocused = rowsFocused
		if s.reveal {
			for _, r := range s.cache.layout {
				if r.kind == 0 && s.cache.items[r.item].ID == s.high {
					if r.y < s.scroll.Y {
						s.scroll.Y = r.y
					} else if r.y+r.h > s.scroll.Y+viewH {
						s.scroll.Y = r.y + r.h - viewH
					}
					break
				}
			}
			s.reveal = false
		}
		viewport := ui.Box(c).FillWidth().Height(viewH).Shrink(0).Clip().Label(title + " results")
		viewport.Children(func() {
			scroll := ui.Scroll(c).Fill().TrackScroll(&s.scroll).Scrollbars(false).Bind(&s.rows).Focusable().Role(ui.RoleRadioGroup).Label(title + " list")
			scroll.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind == ui.InputKeyDown {
					if ev.Repeat && (ev.Key == ui.KeyEnter || ev.Key == ui.KeySpace) {
						return true
					}
					return nav(ev.Key)
				}
				return false
			})
			scroll.Children(func() {
				ui.Box(c).FillWidth().Height(s.cache.total).Shrink(0).Children(func() {
					if len(s.cache.matches) == 0 {
						text(c, o.EmptyLabel, t.Typography().DialogBody(), t.Colors().Control().MembersAboutLimitForeground()).Absolute().Left(0).Right(0).Top((s.cache.total - fontHeight(c, t.Typography().DialogBody())) / 2).TextAlign(ui.Center)
					}
					// Construct only visible entries. Geometry and identity survive filtering;
					// navigation scrolls by cached full row bounds, not a guessed fixed height.
					for _, r := range s.cache.layout {
						if r.y+r.h < s.scroll.Y || r.y > s.scroll.Y+viewH {
							continue
						}
						switch r.kind {
						case 1:
							SectionTitle(c.Key(struct{ Group string }{s.cache.items[r.item].GroupID}), r.group).Absolute().Top(r.y).Left(0).Right(0)
						case 2:
							Divider(c.Key(struct{ Divider string }{r.group}), "", DividerOptions{}).Absolute().Top(r.y).Left(0).Right(0)
						default:
							choiceRow(c.Key(s.cache.items[r.item].ID), s, r, selected, width, o, activate).Absolute().Top(r.y).Left(0).Right(0)
						}
					}
				})
			})
			chooseScrollbar(c, s, viewH)
		})
		// paint/choose-box.md#滚动区: fixed 1 DIP boundaries; bottom fades away
		// at the end, while the nonzero search skip keeps the top visible.
		edge := t.Metrics().Control().BorderWidth()
		bottom := ui.Local(viewport, "bottom-shadow", func() timeline { return timeline{} }).update(c, unit(s.scroll.Y < s.scroll.MaxY), t.Motion().Fade(), opening)
		viewport.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: edge}, t.Colors().Control().ShadowForeground(), 0)
			p.Fill(ui.Rect{X: r.X, Y: r.Y + r.H - edge, W: r.W, H: edge}, t.Colors().Control().ShadowForeground().Alpha(bottom), 0)
		})
	})
	return root
}
func fontHeight(c *ui.Context, style theme.TextStyle) float32 {
	_, h := measure(c, "M", style, 0)
	return h
}
func (s *ChooseState) move(delta int) {
	current := -1
	for n, i := range s.cache.matches {
		if s.cache.items[i].ID == s.high {
			current = n
			break
		}
	}
	direction := 1
	if delta < 0 {
		direction = -1
		delta = -delta
	}
	for delta > 0 {
		next := current + direction
		for next >= 0 && next < len(s.cache.matches) && s.cache.items[s.cache.matches[next]].Disabled {
			next += direction
		}
		if next < 0 {
			s.high = ""
			return
		}
		if next >= len(s.cache.matches) {
			return
		}
		current = next
		s.high = s.cache.items[s.cache.matches[current]].ID
		delta--
	}
}
func choiceRow(c *ui.Context, s *ChooseState, r choiceLayout, selected string, width float32, o ChooseOptions, activate func()) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Choose()
	item := s.cache.items[r.item]
	b := ui.ButtonBase(c).FillWidth().Height(r.h).Label(item.Title).Checked(item.ID == selected).Disabled(item.Disabled).Role(ui.RoleRadio)
	// A pointer move, not a stationary hover after navigation, takes highlight.
	b.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputPointerMove {
			s.high = item.ID
		}
		return false
	})
	over := s.high == item.ID || b.Pressed()
	bg := t.Colors().Dialog().Background()
	sub := t.Colors().Window().MutedText()
	if over {
		bg = t.Colors().Window().BackgroundHover()
		sub = t.Colors().Window().SubTextForegroundHover()
	}
	wave := ui.Local(b, "row-ripple", func() pulse { return pulse{} })
	wave.update(c, b, !item.Disabled, nil)
	b.Background(bg).Draw(func(p *ui.Painter, box ui.Rect) { wave.paint(p, box, t.Colors().Window().BackgroundPressed(), false) })
	left := 2*m.RowPaddingLeft() + t.Metrics().Choice().RadioDiameter()
	reserve := t.Metrics().Scrollbar().Width() + m.ScrollInsetX() + float32(int((s.cache.rowEstimate-t.Metrics().Header().ActionWidth())/2)) + t.Metrics().Header().ActionWidth()
	if o.WideText {
		reserve = 0
	}
	available := max(1, width-3*m.RowPaddingLeft()-m.RowIconGap()-reserve)
	b.Children(func() {
		a := ui.Local(b, "checked", func() timeline { return timeline{} }).update(c, unit(item.ID == selected), t.Motion().Choice(), item.Disabled)
		mirror(c, mark(c, item.ID == selected, true, a, 0, true), m.RowPaddingLeft(), t.Metrics().Choice().RadioDiameter()).Top(float32(int((r.h - t.Metrics().Choice().RadioDiameter()) / 2)))
		mirror(c, text(c, item.Title, t.Typography().ContentTitle(), t.Colors().Window().Text()), left, available).Top(m.RowPaddingTop())
		mirror(c, text(c, item.Subtitle, t.Typography().Body(), sub), left, available).Top(m.RowPaddingTop() + r.titleHeight + m.RowTextGap())
	})
	return b.OnClick(func() { s.high = item.ID; activate() })
}

func (s *ChooseState) visible(id string) bool {
	for _, i := range s.cache.matches {
		if item := s.cache.items[i]; item.ID == id && !item.Disabled {
			return true
		}
	}
	return false
}
