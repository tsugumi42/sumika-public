// Package layout provides the adaptive desktop chat shell. The host owns pane
// content and navigation; State belongs to one window and may be persisted.
package layout

import (
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// Region identifies a content slot. Keys are stable across responsive changes.
type Region uint8

const (
	Conversations Region = iota
	Chat
	Third
	Title
	Folders
	FolderTabs
	regionCount
)

type Mode uint8

const (
	Single Mode = iota
	Double
	Triple
)

// State contains host-owned preferences. Zero values show the conversation list
// with the platform's default list ratio. Active is Conversations or Chat.
// ThirdOpen presents the third pane in the main slot when three columns cannot fit.
type State struct {
	ListRatio  float32
	Compact    bool
	ThirdWidth float32
	ThirdOpen  bool
	Active     Region
	extended   float32

	frame frame
}

type Options struct {
	Folders       bool
	ForceWideList bool   // search / choosing a conversation temporarily expands the list
	OnResize      func() // persist preferences after pointer release or a keyboard move
	Host          *WindowHost
}

// Info describes the current visible layout, including animation progress.
type Info struct {
	Mode            Mode
	BodyWidth       float32
	ListWidth       float32
	ChatWidth       float32
	ThirdWidth      float32
	Compact         bool
	VerticalFolders bool
	ThirdInMain     bool
	WideChat        bool
}

type frame struct {
	Info
	theme  *theme.Theme
	rects  [regionCount]ui.Rect
	shown  [regionCount]bool
	panes  *[regionCount]ui.Handle
	slides *pageSlide
}

type windowView struct {
	ready  bool
	slides pageSlide
	focus  ui.Handle
	panes  [regionCount]ui.Handle
}

func (s *State) Info() Info { return s.frame.Info }

// Window fills its assigned container. Build its slots with Pane inside content.
// Its own measured bounds determine breakpoints, so previews and nested shells
// behave like windows. Only the portion intersecting native title controls is
// reserved; a shell below an existing title bar reserves no additional band.
func Window(c *ui.Context, s *State, o Options, content func()) ui.Element {
	t := theme.T(c)
	root := ui.Box(c).Fill().MinWidth(0).MinHeight(0).Clip()
	bounds := root.LayoutBounds()
	root.Draw(func(_ *ui.Painter, r ui.Rect) {
		if r != bounds {
			c.Invalidate()
		}
	})
	m := t.Metrics().Layout()
	bar := c.TitleBar()
	top := min(bounds.H, max(0, bar.Height-bounds.Y))
	f := solve(t, s, o, bounds.W, max(0, bounds.H-top))
	visual := ui.Local(root, "view", func() windowView { return windowView{} })
	root.Bind(&visual.focus)
	first := !visual.ready
	if visual.focus.Focused(c) {
		root.Focus()
	}
	if bounds.W > 0 {
		visual.ready = true
	}
	// A drag retains its raw pointer position while the displayed list snaps to
	// the normal minimum. Otherwise a series of small moves could never reach 130.
	collapse := t.Motion().ListCollapse()
	list := animate(c, root, "list", f.ListWidth, collapse, first || !s.Compact || o.ForceWideList || f.Mode == Single)
	if f.Mode != Single && list != f.ListWidth {
		list = min(list, max(0, f.BodyWidth-f.ThirdWidth-m.ChatMinWidth()))
		f.ListWidth = list
		f.ChatWidth = f.BodyWidth - list - f.ThirdWidth
		f.rects[Conversations].W = list
		f.rects[Chat].X, f.rects[Chat].W = f.rects[Conversations].X+list, f.ChatWidth
	}
	f.WideChat = f.ChatWidth >= m.WideChatWidth()
	for i := range f.rects {
		f.rects[i].Y += top
	}
	f.rects[Title] = ui.Rect{W: bounds.W, H: top}
	f.shown[Title] = top > 0
	visual.slides.update(c, t, s.frame, f)
	f.slides = &visual.slides
	root.DrawOver(func(p *ui.Painter, _ ui.Rect) { visual.slides.paint(p, t) })
	// Keep focus in the shell when its owner disappears; visible editors keep
	// their focus through resizing and opening an adjacent third column.
	for region := range f.shown {
		leaving := !f.shown[region] || Region(region) == Third && !s.ThirdOpen
		if leaving && s.frame.shown[region] && visual.panes[region].FocusWithin(c) {
			root.Focus()
		}
	}
	f.panes = &visual.panes
	s.frame = f
	bodyWidth, thirdWidth, single := f.BodyWidth, f.ThirdWidth, f.Mode == Single
	root.Children(func() {
		if top > 0 {
			ui.Box(c.Key("native-drag-band")).Absolute().Top(0).Left(0).Right(0).Height(top).
				Background(t.Colors().Titlebar().BackgroundActive()).DragWindow()
		}
		content()
		if f.Mode != Single {
			x := f.rects[Conversations].X + f.ListWidth
			Divider(c.Key("list-divider"), f.ListWidth, func(raw float32) {
				s.Compact = raw < m.ListMinWidth()/2
				if !s.Compact {
					s.ListRatio = clamp(raw, m.ListMinWidth(), min(m.ListMaxWidth(), bodyWidth-thirdWidth-m.ChatMinWidth())) / bodyWidth
				}
			}, DividerOptions{Label: "Resize conversations", Min: 0, Max: f.BodyWidth - f.ThirdWidth - m.ChatMinWidth(), OnCommit: o.OnResize}).
				Absolute().Left(x).Top(top).Height(bounds.H - top)
		}
		if f.Mode == Triple && f.ThirdWidth > 0 {
			Divider(c.Key("third-divider"), f.ThirdWidth, func(raw float32) {
				s.ThirdWidth = clamp(raw, m.ThirdMinWidth(), m.ThirdMaxWidth())
			}, DividerOptions{Label: "Resize third pane", Min: m.ThirdMinWidth(), Max: min(m.ThirdMaxWidth(), f.BodyWidth-m.ListMinWidth()-m.ChatMinWidth()), Reverse: true, OnCommit: o.OnResize}).
				Absolute().Left(bounds.W - f.ThirdWidth).Top(top).Height(bounds.H - top)
		}
	})
	root.OnShortcut(0, ui.KeyEscape, func() {
		if s.ThirdOpen {
			s.SetThird(c, false, o.Host)
		} else if single {
			s.Active = Conversations
		}
	})
	return root
}

// Pane builds a keyed, clipped region of Window. Hidden panes retain their UI
// identity and local state. Use Info().Compact to choose avatar-only content.
func Pane(c *ui.Context, s *State, region Region, content func()) ui.Element {
	f := &s.frame
	r := f.rects[region]
	p := ui.Column(c.Key(region)).Absolute().Left(r.X).Top(r.Y).Size(r.W, r.H).
		Hidden(!f.shown[region]).Clip().MinWidth(0).MinHeight(0).Bind(&f.panes[region])
	if region <= Third {
		p.Cache().Effect(&f.slides.images[region])
	}
	if region == Title {
		bar := c.TitleBar()
		p.Padding(0, bar.Right, 0, bar.Left).DragWindow()
	}
	return p.Children(content)
}

func solve(t *theme.Theme, s *State, o Options, width, height float32) frame {
	m, sidebar := t.Metrics().Layout(), t.Metrics().Sidebar()
	f := frame{theme: t}
	f.VerticalFolders = o.Folders && width >= m.MinWidth()+sidebar.Width()
	rail := float32(0)
	if f.VerticalFolders {
		rail = sidebar.Width()
		f.shown[Folders] = true
		f.rects[Folders] = ui.Rect{W: rail, H: height}
	}
	body := max(0, width-rail)
	f.BodyWidth = body
	f.Mode = Single
	f.ThirdInMain = s.ThirdOpen && body < m.ListMinWidth()+m.ChatMinWidth()+m.ThirdMinWidth()
	if body < m.ListMinWidth()+m.ChatMinWidth() {
		visible := Conversations
		if s.Active == Chat {
			visible = Chat
		}
		if s.ThirdOpen {
			visible = Third
		}
		f.rects[visible] = ui.Rect{X: rail, W: body, H: height}
		f.shown[visible] = true
		if visible == Conversations {
			f.ListWidth = body
			if o.Folders && !f.VerticalFolders {
				f.shown[FolderTabs] = true
				tabs := t.Metrics().Layout().FolderTabsHeight()
				f.rects[FolderTabs] = ui.Rect{W: body, H: tabs}
				f.rects[Conversations].Y, f.rects[Conversations].H = tabs, max(0, height-tabs)
			}
		} else {
			f.ChatWidth = body
		}
		return f
	}
	f.Mode = Double
	ratio := s.ListRatio
	if ratio == 0 {
		ratio = m.ListRatio()
	}
	list := clamp(float32(math.Round(float64(body*ratio))), m.ListMinWidth(), m.ListMaxWidth())
	third := float32(0)
	if s.ThirdOpen && !f.ThirdInMain {
		f.Mode = Triple
		third = clamp(s.ThirdWidth, m.ThirdMinWidth(), m.ThirdMaxWidth())
		room := body - m.ChatMinWidth()
		if list+third > room {
			share := float32(math.Floor(float64(room * third / (list + third))))
			third = clamp(share, m.ThirdMinWidth(), room-m.ListMinWidth())
			list = room - third
		}
	}
	list = min(list, body-third-m.ChatMinWidth())
	f.Compact = s.Compact && !o.ForceWideList
	if f.Compact {
		list = t.Metrics().Conversation().PaddingLeft()*2 + t.Metrics().Conversation().AvatarSize()
	}
	f.ListWidth, f.ThirdWidth = list, third
	f.ChatWidth = body - list - third
	f.shown[Conversations] = true
	f.rects[Conversations] = ui.Rect{X: rail, W: list, H: height}
	f.shown[Chat] = !f.ThirdInMain
	f.rects[Chat] = ui.Rect{X: rail + list, W: f.ChatWidth, H: height}
	if f.ThirdInMain {
		f.shown[Third] = true
		f.rects[Third] = f.rects[Chat]
	} else if third > 0 {
		f.shown[Third] = true
		f.rects[Third] = ui.Rect{X: width - third, W: third, H: height}
	}
	return f
}

func clamp(v, lo, hi float32) float32 { return max(lo, min(v, hi)) }

func animate(c *ui.Context, e ui.Element, key string, target float32, motion theme.Transition, immediate bool) float32 {
	if immediate || motion.Duration() == 0 || c.Instant() || c.Preferences().ReduceMotion {
		return e.Jump(key, target)
	}
	return e.AnimateTo(key, target, ui.Animation{Duration: motion.Duration(), Ease: motion.Easing().Curve()})
}
