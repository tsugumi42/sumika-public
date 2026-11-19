package compose

import (
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/locales"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type PickerTab uint8

const (
	EmojiTab PickerTab = iota
	StickerTab
	GIFTab
)

type PickerItem struct {
	ID, Label, Keywords string
	// Emoji is the Unicode sequence inserted by the host. Image may supply prepared artwork.
	Emoji string
	Image ui.ImageSource
	// AspectRatio controls GIF tile width. Zero uses the prepared image's dimensions.
	AspectRatio float32
	Disabled    bool
}

type Category struct {
	ID, Label  string
	HideHeader bool
	Icon       ui.ImageSource
	Items      []PickerItem
}

type PickerPageState uint8

const (
	PageReady PickerPageState = iota
	PageLoading
	PageFailed
	PageRestricted
)

type PickerPage struct {
	Categories []Category
	State      PickerPageState
	// Status is a host-rendered loading/error/restriction message. Empty shows the grid.
	Status string
}

type PickerData [3]PickerPage

type PickerState struct {
	Tab      PickerTab
	Query    [3]string
	Category [3]string
	Scroll   [3]ui.ScrollState
	targets  [3]map[string]*ui.Handle
}

type PickerOptions struct {
	// Open optionally controls animated visibility. Keep calling Picker while it closes.
	Open            *bool
	AvailableHeight float32
	// AvailableWidth is the host slot width when narrower than the window.
	AvailableWidth                                                  float32
	Disabled                                                        bool
	Tabs                                                            [3]string
	SearchLabel, SearchPlaceholder, EmptyText, ClearLabel, AllLabel string
	OnSelect                                                        func(PickerTab, PickerItem)
	OnSearch                                                        func(PickerTab, string)
	OnCategory                                                      func(PickerTab, string)
	OnClose                                                         func()
}

type pickerVisual struct {
	current, previous PickerTab
	revision          int
}

// Picker displays the emoji, sticker and GIF catalog prepared by the host.
// Callers keep PickerState across hiding/reopening or moving the panel.
func Picker(c *ui.Context, state *PickerState, data PickerData, o PickerOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Picker()
	windowWidth, _ := c.Size()
	width := min(m.Width(), windowWidth)
	if o.AvailableWidth > 0 {
		width = min(width, o.AvailableWidth)
	}
	height := m.MinHeight()
	if o.AvailableHeight > 0 {
		height = min(o.AvailableHeight, max(m.MinHeight(), min(m.MaxHeight(), o.AvailableHeight*m.HeightRatio())))
	}
	bodyLine := fontLineHeight(c, t, t.Typography().Compose())
	tabHeight := max(m.TabHeight(), fontLineHeight(c, t, t.Typography().PickerTitle())+m.TabLine())
	searchHeight, footerHeight := max(m.SearchHeight(), bodyLine), max(m.FooterHeight(), bodyLine)
	root := ui.Box(c).Width(width).MaxWidthPercent(100).Height(height).Disabled(o.Disabled)
	reveal, widthFactor, heightFactor := float32(1), float32(1), float32(1)
	showing := o.Open == nil || *o.Open
	if o.Open != nil {
		target := float32(0)
		if showing {
			target = 1
		}
		reveal = root.AnimateTo("reveal", target, ui.Animation{Duration: t.Motion().Picker().Duration(), Ease: t.Motion().Picker().Easing().Curve()})
		root.IgnoreInput(!showing || reveal < 1).Hidden(!showing && reveal == 0)
		if showing {
			widthFactor = m.ShowStartWidth() + (1-m.ShowStartWidth())*circularStage(reveal, m.ShowWidthFraction())
			heightFactor = m.ShowStartHeight() + (1-m.ShowStartHeight())*circularStage(reveal, m.ShowHeightFraction())
			root.Opacity(circularStage(reveal, m.ShowOpacityFraction()))
		} else {
			root.Opacity(reveal)
		}
		root.OnShortcut(0, ui.KeyEscape, func() {
			*o.Open = false
			if o.OnClose != nil {
				o.OnClose()
			}
		})
	}
	v := ui.Local(root, "slide", func() pickerVisual { return pickerVisual{current: state.Tab, previous: state.Tab} })
	viewport := ui.Local(root, "viewport", func() ui.Handle { return ui.Handle{} })
	gridWidth := width
	if committed := viewport.Bounds(c).W; committed > 0 && o.AvailableWidth == 0 {
		gridWidth = min(width, committed)
	}
	if v.current != state.Tab {
		v.previous, v.current = v.current, state.Tab
		v.revision++
	}
	motion := t.Motion().PickerSlide()
	slide := root.AnimateTo("slide", float32(v.revision), ui.Animation{Duration: motion.Duration(), Ease: motion.Easing().Curve()})
	p := max(float32(0), 1-(float32(v.revision)-slide))
	root.Children(func() {
		pane := ui.Box(c).Absolute().Right(0).Bottom(0).WidthPercent(100*widthFactor).Height(height*heightFactor).Radius(m.Radius()).Clip().Background(t.Colors().Emoji().PanBackground()).Shadow(0, m.ShadowOffsetY(), m.ShadowBlur(), 0, t.Colors().Window().ShadowForeground().Alpha(m.ShadowOpacity()))
		if showing && reveal < 1 && m.ShowFadeHeight() > 0 {
			pane.DrawOver(func(p *ui.Painter, r ui.Rect) {
				band := height * m.ShowFadeHeight()
				distance := height * (m.ShowFadeStart() + (1-m.ShowFadeStart())*circularStage(reveal, 1))
				end := max(float32(0), r.H-distance)
				start := max(float32(0), end-band)
				bg := t.Colors().Emoji().PanBackground().Alpha(m.ShowFadeOpacity())
				if start > 0 {
					p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: start}, bg, 0)
				}
				if end > start {
					p.FillGradient(ui.Rect{X: r.X, Y: r.Y + start, W: r.W, H: end - start}, ui.LinearGradient{From: bg.Alpha(min(float32(1), end/band)), To: bg.Alpha(0), Angle: 180}, 0)
				}
			})
		}
		contentWidth := float32(100)
		if widthFactor > 0 {
			contentWidth /= widthFactor
		}
		pane.Children(func() {
			ui.Column(c).Absolute().Right(0).Bottom(0).WidthPercent(contentWidth).Height(height).Gap(0).Children(func() {
				ui.Row(c).FillWidth().Height(tabHeight).Gap(0).Children(func() {
					defaults := [3]string{"emoji", "stickers", "gifs"}
					for tab := EmojiTab; tab <= GIFTab; tab++ {
						c.Key(tab)
						b := ui.ButtonBase(c).Grow(1).MinWidth(0).Height(tabHeight).Center().Label(label(o.Tabs[tab], locales.Text(c.Locale(), defaults[tab]))).OnClick(func() { state.Tab = tab })
						fg := t.Colors().Emoji().PanHeaderForeground()
						if state.Tab == tab {
							fg = t.Colors().Control().SurfaceForeground()
						}
						b.Children(func() {
							text(c, label(o.Tabs[tab], locales.Text(c.Locale(), defaults[tab])), t.Typography().PickerTitle(), fg)
						})
						if state.Tab == tab {
							b.DrawOver(func(p *ui.Painter, r ui.Rect) {
								p.Fill(ui.Rect{X: r.X, Y: r.Y + r.H - m.TabLine(), W: r.W, H: m.TabLine()}, fg, m.TabLine()/2)
							})
						}
					}
				})
				tab := state.Tab
				ui.Row(c).FillWidth().Shrink(0).Gap(0).Padding(m.SearchTop(), m.SearchRight(), m.SearchBottom(), m.SearchLeft()).Children(func() {
					ui.TextInputBase(c, &state.Query[tab]).Grow(1).MinWidth(0).Height(searchHeight).Padding(0, m.SearchTextLeft()).Radius(searchHeight / 2).
						Background(t.Colors().Window().BackgroundHover()).TextColor(t.Colors().Window().Text()).
						Font(t.Typography().Font()).FontSize(t.Typography().Compose().Size() * c.Preferences().TextScale).
						Placeholder(label(o.SearchPlaceholder, locales.Text(c.Locale(), "search"))).PlaceholderColor(t.Colors().Emoji().PanHeaderForeground()).Label(label(o.SearchLabel, locales.Text(c.Locale(), "searchMedia"))).
						OnChange(func() {
							state.Scroll[tab].Y = 0
							if o.OnSearch != nil {
								o.OnSearch(tab, state.Query[tab])
							}
						})
					if state.Query[tab] != "" {
						ui.ButtonBase(c).Size(searchHeight, searchHeight).Center().Label(label(o.ClearLabel, locales.Text(c.Locale(), "clearSearch"))).OnClick(func() {
							state.Query[tab] = ""
							state.Scroll[tab].Y = 0
							if o.OnSearch != nil {
								o.OnSearch(tab, "")
							}
						}).Children(func() {
							draw.Element(c, closeIcon, t.Metrics().Compose().IconSize(), t.Metrics().Compose().IconSize()).TextColor(t.Colors().Emoji().IconForeground())
						})
					}
				})
				ui.Box(c).FillWidth().Grow(1).MinHeight(0).Clip().Bind(viewport).Children(func() {
					if p < 1 {
						width := viewport.Bounds(c).W
						direction := float32(1)
						if v.current < v.previous {
							direction = -1
						}
						c.Key(v.previous)
						pickerGrid(c, state, v.previous, data[v.previous], gridWidth, o).Absolute().Left(0).Top(0).Fill().Translate(-direction*width*(1-float32(math.Sqrt(float64(1-p*p)))), 0).Opacity(1 - float32(math.Sqrt(float64(1-(p-1)*(p-1))))).IgnoreInput(true)
						c.Key(v.current)
						pickerGrid(c, state, v.current, data[v.current], gridWidth, o).Absolute().Left(0).Top(0).Fill().Translate(direction*width*(1-float32(math.Sqrt(float64(1-(p-1)*(p-1))))), 0).Opacity(1 - float32(math.Sqrt(float64(1-p*p))))
					} else {
						c.Key(v.current)
						pickerGrid(c, state, v.current, data[v.current], gridWidth, o).Fill()
					}
				})
				ui.ScrollHorizontal(c).FillWidth().Height(footerHeight).Shrink(0).Gap(0).Padding(0, m.CategoryGap()).Background(t.Colors().Emoji().PanCategories()).Children(func() {
					c.Key("all")
					categoryButton(c, label(o.AllLabel, locales.Text(c.Locale(), "all")), pickerAllIcon, state.Category[tab] == "", footerHeight, func() {
						state.Category[tab] = ""
						state.Scroll[tab].Y = 0
						if o.OnCategory != nil {
							o.OnCategory(tab, "")
						}
					})
					for _, category := range data[tab].Categories {
						c.Key(category.ID)
						categoryButton(c, category.Label, category.Icon, state.Category[tab] == category.ID, footerHeight, func() {
							state.Category[tab] = category.ID
							if data[tab].Status == "" && data[tab].State == PageReady {
								viewport := state.Scroll[tab].Bounds(c)
								header := state.targets[tab][category.ID].Bounds(c)
								state.Scroll[tab].ScrollTo(0, state.Scroll[tab].Y+header.Y-viewport.Y, ui.Animation{Duration: t.Motion().PickerSlide().Duration(), Ease: t.Motion().PickerSlide().Easing().Curve()})
							}
							if o.OnCategory != nil {
								o.OnCategory(tab, category.ID)
							}
						})
					}
				})
			})
		})
	})
	return root
}

func circularStage(progress, fraction float32) float32 {
	if fraction == 0 {
		return 1
	}
	v := min(progress/fraction, 1)
	return float32(math.Sqrt(float64(1 - (v-1)*(v-1))))
}

func categoryButton(c *ui.Context, name string, image ui.ImageSource, selected bool, height float32, click func()) {
	t := theme.T(c)
	m := t.Metrics().Picker()
	b := ui.ButtonBase(c).Height(height).Shrink(0).Center().Label(name).Tooltip(name).OnClick(click)
	if image != nil {
		b.Width(m.CategoryWidth())
	} else {
		b.MinWidth(m.CategoryWidth()).Padding(0, m.CategoryGap())
	}
	b.Children(func() {
		area := ui.Box(c).Height(m.CategoryImageSize()).Radius(m.Radius()).Center()
		if selected || b.Hovered() {
			area.Background(t.Colors().Window().BackgroundPressed())
		}
		if image != nil {
			area.Width(m.CategoryImageSize()).Children(func() {
				if svg, ok := image.(*ui.SVG); ok && !draw.IsHost(svg) {
					draw.Resolve(c, svg, m.CategoryImageSize(), m.CategoryImageSize()).Element(c).TextColor(t.Colors().Emoji().IconForeground())
					return
				}
				ui.Image(c, image).Size(m.CategoryImageSize(), m.CategoryImageSize()).Fit(ui.Contain).TextColor(t.Colors().Emoji().IconForeground())
			})
		} else {
			area.Children(func() { text(c, name, t.Typography().Compose(), t.Colors().Emoji().IconForeground()).SingleLine() })
		}
	})
}

func pickerGrid(c *ui.Context, state *PickerState, tab PickerTab, page PickerPage, width float32, o PickerOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Picker()
	grid := pickerTiles(t, width, tab)
	root := ui.Scroll(c).MinWidth(0).MinHeight(0).Gap(0).Padding(grid.top, grid.right, grid.bottom, grid.left).TrackScroll(&state.Scroll[tab])
	root.Children(func() {
		status := page.Status
		if status == "" && page.State != PageReady {
			status = locales.Text(c.Locale(), [...]string{"", "loading", "searchFailed", "restricted"}[page.State])
		}
		if status != "" {
			text(c, status, t.Typography().Compose(), t.Colors().Emoji().PanHeaderForeground())
			return
		}
		query := strings.ToLower(strings.TrimSpace(state.Query[tab]))
		if state.targets[tab] == nil {
			state.targets[tab] = make(map[string]*ui.Handle)
		}
		count := 0
		for _, category := range page.Categories {
			c.Key(category.ID)
			if state.targets[tab][category.ID] == nil {
				state.targets[tab][category.ID] = new(ui.Handle)
			}
			ui.Column(c).FillWidth().MinWidth(0).Gap(0).Bind(state.targets[tab][category.ID]).Children(func() {
				if !category.HideHeader {
					ui.Box(c).FillWidth().Height(max(m.HeaderHeight(), m.HeaderTop()+fontLineHeight(c, t, t.Typography().PickerTitle()))).Children(func() {
						text(c, category.Label, t.Typography().PickerTitle(), t.Colors().Emoji().PanHeaderForeground()).Absolute().Left(m.Radius() + m.HeaderLeft() - grid.left).Right(0).Top(m.HeaderTop()).SingleLine()
					})
				} else if tab == StickerTab {
					ui.Box(c).Height(m.StickerPadding())
				}
				gap := float32(0)
				if tab == GIFTab {
					gap = m.GifGap()
				}
				ui.Row(c).FillWidth().Wrap().Gap(gap).Children(func() {
					for _, item := range category.Items {
						if query != "" && !strings.Contains(strings.ToLower(item.Label), query) && !strings.Contains(strings.ToLower(item.Keywords), query) {
							continue
						}
						count++
						c.Key(item.ID)
						w, h, imageSize := grid.cell, grid.height, grid.image
						if tab == GIFTab {
							ratio := item.AspectRatio
							if ratio == 0 {
								iw, ih := ui.ImageSize(item.Image)
								ratio = iw / ih
							}
							w, h = max(m.GifMinWidth(), min(width-grid.left-grid.right, m.GifHeight()*ratio)), m.GifHeight()
						}
						b := ui.ButtonBase(c).Size(w, h).MaxWidthPercent(100).Shrink(0).Center().Label(item.Label).Disabled(item.Disabled).OnClick(func() {
							if o.OnSelect != nil {
								o.OnSelect(tab, item)
							}
						})
						if b.Hovered() || b.Focused() {
							b.Background(t.Colors().Window().BackgroundHover()).Radius(t.Metrics().Control().RadiusSmall())
						}
						b.Children(func() {
							if tab == GIFTab {
								ui.Image(c, item.Image).Fill().Fit(ui.Cover)
							} else if tab == EmojiTab && item.Image == nil {
								ui.Text(c, item.Emoji).FontSize(imageSize).SingleLine()
							} else {
								ui.Image(c, item.Image).Size(imageSize, imageSize).Fit(ui.Contain)
							}
						})
					}
				})
			})
		}
		if count == 0 {
			text(c, label(o.EmptyText, locales.Text(c.Locale(), "noResults")), t.Typography().Compose(), t.Colors().Emoji().PanHeaderForeground())
		}
	})
	return root
}

type tileLayout struct{ left, right, top, bottom, cell, height, image float32 }

func pickerTiles(t *theme.Theme, width float32, tab PickerTab) tileLayout {
	m := t.Metrics().Picker()
	g := tileLayout{bottom: m.PaddingBottom(), image: m.ImageSize()}
	if tab == GIFTab {
		g.left, g.right, g.top, g.bottom = m.Radius()+m.GifPaddingLeft(), m.ScrollbarWidth()+m.GifPaddingRight(), m.GifPaddingTop(), m.GifPaddingBottom()
		return g
	}
	inner := max(float32(0), width-m.Radius()-m.ScrollbarWidth())
	usable, minimum := inner+m.MarginLeft()+m.MarginRight()-m.PaddingLeft()-m.PaddingRight(), m.CellWidth()
	if tab == StickerTab {
		usable, minimum = inner-m.StickerPadding()+m.MarginLeft(), m.StickerSize()
	}
	columns := max(1, int(usable/minimum))
	g.cell = float32(math.Floor(float64(usable / float32(columns))))
	if tab == EmojiTab {
		g.left = m.Radius() + m.PaddingLeft() - m.MarginLeft() + float32(math.Floor(float64((usable-float32(columns)*g.cell)/2)))
		g.height = g.cell - (m.CellWidth() - m.CellHeight())
	} else {
		free := m.MarginLeft() + inner + m.ScrollbarWidth() - float32(columns)*g.cell
		g.left = m.Radius() + free - max(m.ScrollbarWidth(), float32(math.Floor(float64(free/2)))) - m.MarginLeft()
		g.height, g.image = g.cell, max(float32(0), g.cell-2*t.Metrics().Control().RadiusSmall())
	}
	g.right = max(float32(0), width-g.left-float32(columns)*g.cell)
	return g
}

var pickerAllIcon = icon(`<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>`, 2, 2, 20, 20)

func init() { draw.Configure(pickerAllIcon, draw.Recent, 28, 28) }
