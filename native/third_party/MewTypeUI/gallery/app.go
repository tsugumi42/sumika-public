package gallery

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/layout"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// App owns the native gallery's selection and appearance. A host may supply a
// prepared screenshot for side-by-side comparison; the gallery performs no I/O.
type App struct {
	Window       *mygo.Window
	Selected     string
	Dark         bool
	Reference    *ui.Bitmap
	Compare      bool
	Focus        bool
	OnAppearance func(bool)
	OnReference  func()
	Overlay      bool
	Comparison   Comparison
	WindowHost   *layout.WindowHost
	pages        []Page
	pageMenu     overlay.MenuState
	pageItems    []overlay.MenuItem
}

func NewApp() *App {
	pages := Pages()
	items := make([]overlay.MenuItem, len(pages))
	for i, p := range pages {
		items[i] = overlay.MenuItem{ID: p.ID, Label: p.Title}
	}
	return &App{Selected: "layout/chat", pages: pages, pageItems: items, Comparison: NewComparison()}
}

func (a *App) View(c *ui.Context) {
	*ui.Local(c.Root(), "gallery-layout-host", func() *layout.WindowHost { return nil }) = a.WindowHost
	t := theme.Light()
	if a.Dark {
		t = theme.Dark()
	}
	theme.Apply(c, t)
	bar := c.TitleBar()
	w, _ := c.Size()
	m := t.Metrics()
	wide := w >= m.Layout().MinWidth()+m.Layout().ListMinWidth()
	overlay.Native(c, a.Window, func() {
		ui.Column(c).Fill().MinWidth(0).MinHeight(0).Children(func() {
			ui.Row(c.Key("gallery-chrome")).FillWidth().MinWidth(0).Height(max(bar.Height, m.Header().Height())).
				Padding(0, bar.Right+m.Conversation().PaddingRight(), 0, bar.Left+m.Conversation().PaddingLeft()).
				Gap(m.Header().ActionGap()).Background(t.Colors().Titlebar().BackgroundActive()).DragWindow().Children(func() {
				button := ui.Button(c, "页面")
				overlay.PopupMenu(c, button, a.pageItems, &a.pageMenu, overlay.MenuOptions{Label: "Gallery pages", OnSelect: func(id string) { a.Selected = id }})
				ui.Text(c, "MewTypeUI").Grow(1).Basis(0).MinWidth(0).SingleLine().FontSize(t.Typography().HeaderTitle().Size() * c.Preferences().TextScale).
					FontWeight(t.Typography().HeaderTitle().Weight())
				ui.Toggle(c, &a.Dark, "暗色").Label("暗色主题").OnChange(func() {
					if a.OnAppearance != nil {
						a.OnAppearance(a.Dark)
					}
				})
				if a.OnReference != nil {
					ui.Button(c, "选择参考图").OnClick(a.OnReference)
				}
			})
			if a.Reference != nil {
				a.comparisonControls(c)
			}
			ui.Row(c).Grow(1).MinWidth(0).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
				if wide && !a.Focus {
					ui.Sidebar(c.Key("gallery-pages"), &a.Selected, func() {
						for _, p := range a.pages {
							ui.SidebarItem(c.Key(p.ID), p.ID, nil, p.Title)
						}
					}).Width(m.Layout().ListMinWidth()).Shrink(0).FillHeight()
				}
				page := ui.Box(c.Key(a.Selected)).Grow(1).Basis(0).MinWidth(0).Clip()
				if a.Overlay && !a.Compare && a.Reference != nil {
					page.Effect(a.Comparison.prepare(a.Reference))
				}
				page.Children(func() {
					for _, p := range a.pages {
						if p.ID == a.Selected {
							p.View(c)
							break
						}
					}
				})
				if a.Compare && a.Reference != nil {
					ui.Image(c, a.Reference).Grow(1).Basis(0).MinWidth(0).Fit(ui.Contain)
				}
			})
		})
	})
}
