package main

import (
	"fmt"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"math"
	"strings"
	"sumika-native/internal/chatui"
	"time"
)

type reading struct {
	List                                ui.ListState
	Area, Header, Composer, Input, Stop ui.Handle
	Height, Width, ViewW, ViewH         float32
	LastDraft                           string
	Rows                                int
}
type app struct {
	E                                  *engine
	Page                               string
	Selected                           map[string]string
	Reads                              map[string]*reading
	Group                              int
	Layer                              int
	Right, Scenes, Dark                bool
	Look                               chatui.Settings // 外观设置（字号、圆角、疏密、冷暖、壁纸、玻璃）；Dark 每帧同步进来
	T                                  chatui.Tokens
	lookUsed                           chatui.Settings
	Wall, PreviewWall                  chatui.Wallpaper
	Font                               string
	Mono                               string           // 等宽字（命令、路径、diff）：本机装了 Maple Mono 就用它，没有就空着走系统等宽
	LiveOpen                           bool             // 设置"进行中展开过程"（ADR-286，默认关）
	StepOpen                           map[string]*bool // 过程里每段的展开状态（按段第一步的 ID）
	MD                                 *chatui.MD       // 她的回复每条一份 markdown 状态（逐字出现、出完才显示时间）
	P                                  palette
	Theme                              ui.Theme
	ThemeReady                         bool
	ListWidth, SideWidth, ChatWidth    float32
	SearchField                        ui.Handle       // 搜索框；头像模式点放大镜后把焦点给它
	Opened, Drawn                      map[string]bool // 会话打开过；消息画过（打开时已有的、画过一次的都不再播出现动画）
	LayerShown                         int             // 窄窗口上一帧显示的是哪一层
	layerSeen                          bool
	drawerShown                        bool                   // 上一帧抽屉开着（打开的那一帧才从起点滑）
	LayerDir                           float32                // 最近一次换层的方向：1 往里走、-1 返回、0 还没换过
	SwitchX                            map[string]float32     // 这一帧各开关圆钮在轨道里的位置（测试读它量回弹）
	SegHandles                         map[string]*segHandles // 分段控件量位置用（选中块滑动）
	ListWant, SideShown                float32                // 拖出来的列表宽；三栏时实际给右栏的宽（放不下时比设定的 SideWidth 窄）
	ListDragged, ListCompact           bool                   // 会话列表被手动拖过（不再跟窗口按比例走）；收成头像模式
	Search                             string
	SettingsCat                        string       // 设置页选中的分类："" 是外观，"plugins" 是插件
	Plugins                            *pluginState // 插件页的假状态（规格-插件页）
	KeyboardConversation, KeyboardText string
	Scenario                           scenarioPanel
	Surfaces                           func(*ui.Context, ui.Element, surfaceInfo)
	Close                              func()
}
type surfaceInfo struct {
	ID, Kind      string
	Radius, Inset float32
}

func newApp() *app {
	a := &app{E: newEngine(1), Page: "local", Selected: map[string]string{"local": "A", "qq": "C"}, Reads: make(map[string]*reading), Right: true, SideWidth: sideDefault, Look: chatui.DefaultSettings()}
	a.Scenario = scenarioPanel{Target: "A", Actor: "r1", Delay: "0", Count: "1", Interval: "100", Seed: "42"}
	return a
}
func (a *app) cv() *conversation { return a.E.conv(a.Selected[a.Page]) }
func (a *app) read(id string) *reading {
	r := a.Reads[id]
	if r == nil {
		r = &reading{Height: composerMin + spacePanel}
		r.List.FollowEnd = true
		// 新消息从下面升上来（mygo 给聊天列表的做法，ListState.FollowAnimation）；位移用回弹（ADR-285 第 6 条）。
		// 消息行自己的进场动画在虚拟列表里不触发，所以工作台的"出现"靠这个；减少动态效果时 mygo 直接到底。
		r.List.FollowAnimation = &ui.Animation{Spring: chatui.Derive(chatui.DefaultSettings()).Bouncy}
		r.List.Key = func(i int) any { return a.E.conv(id).Messages[i].ID }
		a.Reads[id] = r
	}
	return r
}
func (a *app) surface(c *ui.Context, e ui.Element, id, kind string, radius, inset float32) ui.Element {
	if a.Surfaces != nil {
		a.Surfaces(c, e, surfaceInfo{id, kind, radius, inset})
	}
	return e
}
func (a *app) button(c *ui.Context, key, label string, primary, disabled bool, fn func()) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Label(label).Height(hitSmall).MinWidth(hitSmall).Padding(controlInset).Radius(radiusControl).Disabled(disabled).OnClick(fn)
	bg, ink := ui.Transparent, a.P.Ink
	if primary {
		bg, ink = a.P.Accent, a.P.OnAccent
	} else if b.Pressed() {
		bg = a.P.Press
	} else if b.Hovered() {
		bg = a.P.Hover
	}
	b.Background(b.AnimateColor("fill", bg, controlMotion)).TextColor(ink)
	if disabled {
		b.Opacity(disabledAlpha)
	}
	b.Children(func() { ui.Text(c, label).FontSize(labelSize).FixedLineHeight(metaLine).SingleLine() })
	return a.surface(c, b, key, "button", radiusControl, controlInset)
}

// segment 是分段控件。选中块是轨道里单独一块，按上一帧量到的选中格位置滑过去：位移回弹、宽度平滑
// （ADR-285 第 6 条；减少动态效果时直接到位）。第一帧还没量到位置时，选中格自己上底色。
func (a *app) segment(c *ui.Context, key string, value *int, names []string) {
	if a.SegHandles == nil {
		a.SegHandles = map[string]*segHandles{}
	}
	hs := a.SegHandles[key]
	if hs == nil {
		hs = &segHandles{}
		a.SegHandles[key] = hs
	}
	fill := a.P.Composer
	if a.Dark {
		fill = a.P.Selected
	}
	parts := ui.SegmentedBase(c.Key(key), value, len(names))
	parts.Track.Bind(&hs.Track)
	track, cell := hs.Track.Bounds(c), ui.Rect{}
	if *value >= 0 && *value < len(hs.Cells) {
		cell = hs.Cells[*value].Bounds(c)
	}
	measured := track.W > 0 && cell.W > 0
	if measured {
		// 选中块画在轨道背景上（Draw 在背景之后、子元素之前），不做成单独的元素，免得盖住格子里的字
		// 位置回弹、宽度平滑；减少动态效果时 mygo 的弹簧直接给终点（ui/motion.go:300）
		x := parts.Track.Spring("ind-x", cell.X-track.X, a.T.Bouncy)
		w := parts.Track.Spring("ind-w", cell.W, a.T.Smooth)
		hs.IndX, hs.IndW = x, w
		parts.Track.Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X + x, Y: r.Y + spaceTight, W: w, H: hitSmall}, fill, radiusSegment)
		})
	}
	parts.Track.Padding(spaceTight).Gap(spaceTight).Background(a.P.Bubble).Radius(radiusControl).Children(func() {
		for i, name := range names {
			b := parts.Segment(i).Grow(1).Height(hitSmall).Center().Radius(radiusSegment).TextColor(a.P.Muted)
			if i < len(hs.Cells) {
				b.Bind(&hs.Cells[i])
			}
			if i == *value {
				if !measured {
					b.Background(fill)
				}
				b.TextColor(a.P.Ink)
			} else if b.Pressed() {
				b.Background(a.P.Press)
			} else if b.Hovered() {
				b.Background(a.P.Hover)
			}
			b.Children(func() { ui.Text(c, name).FontSize(labelSize).FixedLineHeight(metaLine) })
			a.surface(c, b, key+fmt.Sprint(i), "segment", radiusSegment, 0)
		}
	})
	a.surface(c, parts.Track, key, "segment-track", radiusControl, spaceTight)
}
func (a *app) view(c *ui.Context) {
	a.E.tick(c.Now())
	if a.KeyboardConversation != "" {
		id, text := a.KeyboardConversation, a.KeyboardText
		a.KeyboardConversation, a.KeyboardText = "", ""
		a.E.submit(a.E.conv(id), text)
	}
	if !a.E.Paused {
		wait := a.E.nextRedraw()
		if len(a.E.Events) > 0 {
			if d := max(time.Nanosecond, a.E.Events[0].At-a.E.Now); wait == 0 || d < wait {
				wait = d
			}
		}
		if wait > 0 {
			c.After(wait)
		}
	}
	a.Look.Dark = a.Dark
	if !a.ThemeReady || a.Look != a.lookUsed {
		a.T = chatui.Derive(a.Look)
		applyLook(a.T) // 字号、圆角、疏密铺满整窗（审查 B1）
		a.P, a.Theme = makeTheme(a.T, a.Font)
		a.lookUsed, a.ThemeReady = a.Look, true
	}
	c.SetTheme(&a.Theme)
	w, h := c.Size()
	c.Root().Background(a.P.Canvas)
	a.surface(c, ui.Column(c.Key("prototype")).Fill().Background(a.P.Canvas), "canvas", "canvas", 0, 0).Children(func() {
		ui.Box(c.Key("workspace")).Grow(1).MinHeight(0).Children(func() {
			if w < breakpointNarrow {
				// 窄窗口三层（列表 → 聊天 → 资料）：换层时新的一层滑进来并淡入，往里走从右边、返回从左边（审查 B8、R065）。
				// 每层按层号起 key，换层就是新元素，mygo 给它播进场；刚启动那一帧不播。
				if !a.layerSeen { // 第一帧只记下当前层，不当成换层
					a.LayerShown, a.layerSeen = a.Layer, true
				}
				if a.Layer != a.LayerShown {
					a.LayerDir = float32(1)
					if a.Layer < a.LayerShown {
						a.LayerDir = -1
					}
					a.LayerShown = a.Layer
				}
				layer := ui.Box(c.Key(fmt.Sprint("layer-", a.Layer))).Fill()
				if a.LayerDir != 0 {
					layer.Transition(ui.ElementTransition{Animation: &routeMotion, Enter: &ui.Motion{X: a.LayerDir * spaceLarge, Opacity: 0}})
				}
				layer.Children(func() {
					switch a.Layer {
					case 0:
						a.listLayer(c, w)
					case 1:
						if a.Page == "settings" {
							a.settings(c)
						} else {
							a.chat(c)
						}
					case 2:
						a.side(c, true)
					}
				})
			} else {
				ui.Row(c.Key("columns")).Fill().AlignItems(ui.Stretch).Children(func() {
					a.nav(c)
					a.layoutList(w)
					split := ui.Split(c.Key("list-split"), &a.ListWidth, func() { a.sidebar(c) }, func() {
						if a.Page == "settings" {
							a.settings(c)
							return
						}
						if w >= breakpointWide && a.Right {
							available := w - navWidth - a.ListWidth - splitSize
							a.ChatWidth = available - a.SideShown - splitSize
							s := ui.Split(c.Key("side-split"), &a.ChatWidth, func() { a.chat(c) }, func() { a.side(c, false) }).Fill()
							if s.Changed() {
								a.SideWidth = min(max(available-a.ChatWidth-splitSize, sideMin), sideMax, available-chatMin-splitSize)
								c.AnimationFrame()
							}
						} else {
							a.chat(c)
						}
					}).Grow(1).MinWidth(0).FillHeight()
					if split.Changed() {
						a.dragList(w)
						c.AnimationFrame()
					}
				})
				if w < breakpointWide && a.Right && a.Layer == 2 {
					a.drawer(c)
				} else {
					a.drawerShown = false
				}
			}
		})
		if a.Scenes {
			ui.Overlay(c, func() { a.sceneView(c, min(sceneHeight, h-headerHeight-spacePanel*2)) })
		}
	})
}
func (a *app) nav(c *ui.Context) {
	// 顶上一排和别的栏标题行同高、放 logo（主人 11-06：几栏顶行同高对齐），下面才是导航按钮
	e := ui.Column(c.Key("navigation")).Width(navWidth).Shrink(0).FillHeight().Background(a.P.Rail)
	e.Children(func() {
		ui.Row(c.Key("nav-header")).FillWidth().Height(headerHeight).Shrink(0).Padding(paneInset).Justify(ui.Center).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Image(c.Key("app-logo"), appLogo).Size(hitSmall, hitSmall).Label("Sumika")
		})
		ui.Column(c.Key("nav-body")).FillWidth().Grow(1).MinHeight(0).Padding(navInset).Gap(navInset).Children(func() { a.navItems(c) })
	})
	a.surface(c, e, "navigation", "pane", 0, 0)
}

func (a *app) navItems(c *ui.Context) {
	for _, p := range []struct {
		ID, Label string
		Icon      *ui.SVG
	}{{"local", "本地", iconHome}, {"qq", "QQ", iconChat}, {"settings", "设置", iconSettings}} {
		if p.ID == "settings" {
			ui.Spacer(c)
			a.sceneEntry(c).FillWidth().Height(navItemHeight).Radius(radiusNavItem)
		}
		b := ui.ButtonBase(c.Key("nav-" + p.ID)).Label(p.Label).FillWidth().Height(navItemHeight).Radius(radiusNavItem).Selected(a.Page == p.ID).TextColor(a.P.Ink).OnClick(func() { a.Page = p.ID; a.Group = 0; a.Layer = 0 })
		bg := ui.Transparent
		if a.Page == p.ID {
			bg = a.P.Selected
		} else if b.Pressed() {
			bg = a.P.Press
		} else if b.Hovered() {
			bg = a.P.Hover
		}
		b.Background(b.AnimateColor("fill", bg, controlMotion)).Children(func() {
			ui.Column(c).Center().Gap(spaceTight).Children(func() {
				ui.Icon(c, p.Icon).Size(iconSize, iconSize)
				ui.Text(c, p.Label).FontSize(metaSize).FixedLineHeight(metaLine).SingleLine()
			})
		})
		a.surface(c, b, "nav-"+p.ID, "navigation-item", radiusNavItem, 0)
	}
}

func (a *app) listLayer(c *ui.Context, w float32) {
	ui.Row(c.Key("list-layer")).Fill().AlignItems(ui.Stretch).Children(func() { a.nav(c); ui.Box(c).Grow(1).MinWidth(0).FillHeight().Children(func() { a.sidebar(c) }) })
}
func (a *app) sidebar(c *ui.Context) {
	if a.ListCompact {
		a.compactSidebar(c)
		return
	}
	a.surface(c, ui.Column(c.Key("sidebar")).Fill().Background(a.P.Pane), "sidebar", "pane", 0, spacePanel).Children(func() {
		ui.Row(c.Key("search-row")).Height(headerHeight).Shrink(0).Padding(listInset).Gap(spaceSmall).Children(func() {
			hint := "搜索会话"
			if a.Page == "settings" {
				hint = "搜索设置"
			}
			a.textField(c, "search", &a.Search, hint).Bind(&a.SearchField).Placeholder(hint).Height(searchHeight).Padding(searchInset).Grow(1).MinWidth(0)
			w, _ := c.Size()
			if w < breakpointNarrow {
				a.closeControl(c)
			}
		})
		// 会话行铺满列表整宽（ADR-284），所以内容栏自己不留内距，分段控件那一排单独留
		ui.Column(c.Key("sidebar-content")).Grow(1).MinHeight(0).Children(func() {
			if a.Page == "settings" {
				// 设置分类照 Codex / Claude 桌面版：灰色组名 + 左对齐的图标文字项，选中浅底圆角
				ui.Column(c.Key("sidebar-tools")).FillWidth().Padding(listInset).Gap(spaceTight).Children(func() {
					ui.Text(c, "通用").FontSize(metaSize).FixedLineHeight(metaLine).TextColor(a.P.Meta).Padding(0, spaceSmall)
					for _, cat := range []struct{ ID, Key, Label, Icon string }{{"", "appearance", "外观", "palette"}, {"plugins", "plugins", "插件", "layers"}} {
						selected := a.SettingsCat == cat.ID
						b := ui.ButtonBase(c.Key(cat.Key)).Label(cat.Label).FillWidth().Height(hitRegular).Padding(spaceSmall).Gap(spaceSmall).
							Justify(ui.Start).AlignItems(ui.Center).Radius(radiusControl).Selected(selected).TextColor(a.P.Ink).OnClick(func() { a.SettingsCat = cat.ID; a.Layer = 1 })
						bg := ui.Transparent
						if selected {
							bg = a.P.Selected
						} else if b.Hovered() {
							bg = a.P.Hover
						}
						b.Background(bg)
						b.Children(func() {
							ui.Icon(c, chatui.Icon(cat.Icon, false)).Size(iconSize, iconSize)
							ui.Text(c, cat.Label).FontSize(labelSize).FixedLineHeight(metaLine)
						})
						a.surface(c, b, cat.Key, "button", radiusControl, spaceSmall)
					}
				})
				return
			}
			ui.Column(c.Key("sidebar-tools")).FillWidth().Padding(listInset).Children(func() {
				a.segment(c, "venues", &a.Group, []string{"私聊", "群聊"})
			})
			ids := []string{"A", "B"}
			if a.Page == "qq" {
				ids = []string{"C", "D"}
			}
			id := ids[a.Group]
			cv := a.E.conv(id)
			ui.List(c.Key("conversation-list"), nil, 1, func(i int) {
				if !strings.Contains(cv.Name, a.Search) {
					return
				}
				selected := a.Selected[a.Page] == id
				b := ui.ButtonBase(c.Key("conversation-" + id)).FillWidth().MinHeight(conversationAvatar + conversationInset*2).Padding(conversationInset).Gap(spaceMedium).AlignItems(ui.Center).Selected(selected).OnClick(func() { a.Selected[a.Page] = id; a.Layer = 1 })
				// 选中整块填强调色、字换成强调色上的颜色；悬停只给一层浅底（照 Telegram、QQ，ADR-284）
				// 未读角标照 Telegram、Bun 用强调色（这里用强调深档配白字，和选中行同一对，对比 ≥ 4.5）；选中行上反过来
				// 名字 600、选中 700，预览用第三级灰（照 Bun chat.css:354-357；审查 B3）；按下那一刻底更深，预览换第二级灰保住 4.5
				bg, ink, muted, badgeBg, badgeInk, weight := ui.Transparent, a.P.Ink, a.P.Meta, a.P.Strong, a.P.OnStrong, strongWeight
				if selected {
					bg, ink, muted, badgeBg, badgeInk, weight = a.P.Strong, a.P.OnStrong, a.P.OnStrongMuted, a.P.OnStrong, a.P.Strong, boldWeight
				} else if b.Pressed() {
					bg, muted = a.P.Press, a.P.Muted
				} else if b.Hovered() {
					bg = a.P.Hover
				}
				b.Background(bg).TextColor(ink).Children(func() {
					a.avatarSized(c, "conversation-avatar-"+id, cv.Name, cv.Actors[0], conversationAvatar)
					ui.Column(c).Grow(1).MinWidth(0).Gap(0).Children(func() {
						ui.Text(c, cv.Name).FontSize(labelSize).FontWeight(weight).FixedLineHeight(conversationLine).TextColor(ink).SingleLine()
						ui.Text(c, a.E.status(cv)).FontSize(metaSize).FixedLineHeight(conversationLine).TextColor(muted).SingleLine()
					})
					if cv.Unread > 0 {
						ui.Box(c).MinWidth(conversationSlot).Height(conversationSlot).Shrink(0).Center().Children(func() {
							ui.Text(c.Key("unread-"+id), fmt.Sprint(cv.Unread)).FontSize(metaSize).FontFeatures(chatui.Tabular).FixedLineHeight(metaLine).Padding(badgeInset).MinWidth(max(avatarSize, metaLine+2*badgeInset)).Radius((metaLine + 2*badgeInset) / 2).Background(badgeBg).TextColor(badgeInk)
						})
					}
				})
				a.surface(c, b, "conversation-"+id, "conversation", 0, conversationInset)
			}).Grow(1).MinHeight(0)
			if a.Search != "" && !strings.Contains(cv.Name, a.Search) {
				ui.Text(c, "没有找到会话，换个词试试").TextColor(a.P.Muted).Padding(listInset)
			}
		})
	})
}
func (a *app) chat(c *ui.Context) {
	cv := a.cv()
	a.markOpened(cv)
	r := a.read(cv.ID)
	r.Rows = 0
	e := ui.Box(c.Key("chat-" + cv.ID)).Fill().Clip().Bind(&r.Area).Background(a.T.C.ChatBg)
	if a.Look.Wallpaper {
		glow := actorChar(cv.Actors[0])
		e.Draw(func(p *ui.Painter, rect ui.Rect) { a.Wall.Paint(p, rect, a.T, glow) })
	}
	width := r.Area.Bounds(c).W
	if width <= 0 {
		width, _ = c.Size()
	}
	viewportWidth, _ := c.Size()
	inset := spacePanel
	if width < narrowContentWidth || viewportWidth < breakpointWide || !a.Right {
		inset = spaceMedium
	}
	column := min(contentWidth, width-inset*2)
	horizontal := max(inset, (width-column)/2)
	e.Children(func() {
		ui.List(c.Key("messages-"+cv.ID), &r.List, len(cv.Messages), func(i int) {
			r.Rows++
			var previous, next *message
			if i > 0 {
				previous = cv.Messages[i-1]
			}
			if i+1 < len(cv.Messages) {
				next = cv.Messages[i+1]
			}
			a.messageView(c, cv, cv.Messages[i], previous, next, column)
		}).Fill().Padding(headerHeight+spacePanel, horizontal, r.Height+inset, horizontal).ScrollbarInsets(headerHeight, 0, r.Height, 0).Label("聊天记录")
		a.header(c, cv, r, horizontal)
		ui.Column(c.Key("composer-frame")).Absolute().Left(horizontal).Right(horizontal).Bottom(inset).Bind(&r.Composer).Children(func() {
			a.composer(c, cv, r)
		})
		if !r.List.AtEnd() {
			a.button(c, "latest", "回到最新", false, false, func() { r.List.ScrollToEnd(); cv.Unread = 0 }).Absolute().Right(inset).Bottom(r.Height + spacePanel)
		} else {
			cv.Unread = 0
		}
	})
	vw, vh := c.Size()
	if r.LastDraft != cv.Draft || r.Width != width || r.ViewW != vw || r.ViewH != vh {
		r.LastDraft, r.Width, r.ViewW, r.ViewH = cv.Draft, width, vw, vh
		c.AnimationFrame()
	}
	if height := r.Composer.Bounds(c).H + spacePanel; height > spacePanel && height != r.Height {
		r.Height = height
		c.AnimationFrame()
	}
}
func (a *app) header(c *ui.Context, cv *conversation, r *reading, horizontal float32) {
	fill := ui.Box(c.Key("chat-header-fill")).Absolute().Left(0).Right(0).Top(0).Height(headerHeight)
	a.glassOr(fill, glassHeader, a.P.Canvas.Alpha(overlayAlpha))
	e := ui.Row(c.Key("chat-header")).Absolute().Left(horizontal - paneInset).Right(horizontal - paneInset).Top(0).Height(headerHeight).Padding(paneInset).Gap(spaceSmall).Bind(&r.Header).DragWindow()
	e.Children(func() {
		w, _ := c.Size()
		if w < breakpointNarrow {
			a.button(c, "back-list", "返回", false, false, func() { a.Layer = 0 })
		}
		a.avatarSized(c, "header-avatar", cv.Name, cv.Actors[0], headerAvatar)
		ui.Text(c.Key("chat-title"), cv.Name).FontSize(bodySize).FontWeight(boldWeight).FixedLineHeight(metaLine).SingleLine()
		ui.Text(c.Key("chat-status"), a.E.status(cv)).FontSize(metaSize).FixedLineHeight(metaLine).TextColor(a.P.Muted).Grow(1).MinWidth(0).SingleLine()
		b := ui.ButtonBase(c.Key("side-open")).Label("资料与活动").Tooltip("资料与活动").Size(hitSmall, hitSmall).Center().Radius(hitSmall / 2).TextColor(a.P.Ink).OnClick(func() {
			a.Right = !a.Right
			if w < breakpointWide {
				a.Right = true
				a.Layer = 2
			}
		})
		bg := ui.Transparent
		if b.Pressed() {
			bg = a.P.Selected
		} else if b.Hovered() {
			bg = a.P.Bubble
		}
		b.Background(b.AnimateColor("fill", bg, controlMotion)).Children(func() { ui.Icon(c, iconSide).Size(iconSize, iconSize) })
		a.surface(c, b, "side-open", "icon-button", hitSmall/2, 0)
		if w < breakpointNarrow {
			a.sceneEntry(c)
		}
		if w < breakpointWide || !a.Right {
			a.closeControl(c)
		}
	})
	a.surface(c, e, "chat-header", "header", 0, paneInset)
}

// messageView 用 chatui 的气泡族画一条消息；这里只把工作台的假后端消息转成 chatui.Msg。
func (a *app) messageView(c *ui.Context, cv *conversation, m, previous, next *message, column float32) {
	host := chatui.Host{T: a.T, Font: a.Font, SelfChar: actorChar(cv.Actors[0]), Group: strings.HasSuffix(cv.Venue, "Group"), Column: column,
		OnRetry: func(string) { a.E.retry(cv, m) }, Appear: func(id string) bool { return a.firstFrameOfNew(cv, id) },
		Mono: a.Mono, LiveOpen: a.LiveOpen, StepOpen: a.stepOpen, MD: a.md(), DenyReason: true,
		OnApprove: func(id string, scope chatui.ApprovalScope) { a.E.approve(id, scope) },
		OnDeny:    func(id, why string) { a.E.deny(id) }} // 工作台不弹写原因的框，"拒绝并说明原因"也直接拒绝
	host.Message(c, a.chatMsg(cv, m), a.chatMsg(cv, previous), a.chatMsg(cv, next))
}

// 消息出现的动画只给真正新到的（R068；审查 C4）：打开会话时已经在的消息记成旧的；之后新到的，只在它第一次被画的那一帧
// 告诉 chatui"从起点开始"。翻回去重新出现的历史、切页回来重新挂上的行都不再播。
func (a *app) markOpened(cv *conversation) {
	if a.Opened == nil {
		a.Opened, a.Drawn = map[string]bool{}, map[string]bool{}
	}
	if a.Opened[cv.ID] {
		return
	}
	a.Opened[cv.ID] = true
	for _, m := range cv.Messages {
		a.Drawn[cv.ID+"/"+m.ID] = true
	}
}

// md 是 markdown 状态仓库（第一次用时建）。
func (a *app) md() *chatui.MD {
	if a.MD == nil {
		a.MD = chatui.NewMD()
	}
	return a.MD
}

// stepOpen 给过程里每段一个展开状态（宿主持有，切会话、翻历史都还在）。
func (a *app) stepOpen(id string) *bool {
	if a.StepOpen == nil {
		a.StepOpen = map[string]*bool{}
	}
	if a.StepOpen[id] == nil {
		a.StepOpen[id] = new(bool)
	}
	return a.StepOpen[id]
}

func (a *app) firstFrameOfNew(cv *conversation, id string) bool {
	k := cv.ID + "/" + id
	if a.Drawn[k] {
		return false
	}
	a.Drawn[k] = true
	return true
}

// chatMsg 把假后端的消息转成 chatui.Msg；时间按消息在会话里的位置排开（假后端没有时间戳，每条隔 20 秒，合组只看发送人）。
func (a *app) chatMsg(cv *conversation, m *message) *chatui.Msg {
	if m == nil {
		return nil
	}
	index := 0
	for i, x := range cv.Messages {
		if x == m {
			index = i
			break
		}
	}
	state := chatui.Sent
	switch m.State {
	case "sending":
		state = chatui.Sending
	case "failed":
		state = chatui.Failed
	case "streaming":
		state = chatui.Streaming
	case "interrupted":
		state = chatui.Interrupted
	}
	msg := &chatui.Msg{ID: m.ID, Sender: m.Actor, Self: m.Actor == "self", Name: actorNames[m.Actor], Char: actorChar(m.Actor),
		At: protoEpoch.Add(time.Duration(index) * 20 * time.Second), Text: m.Text, Markdown: m.Actor != "self", State: state, Steps: m.Steps}
	if m.QuoteText != "" {
		msg.Quote = &chatui.Quote{Name: m.QuoteName, Text: m.QuoteText}
	}
	return msg
}

// protoEpoch 是假后端消息的起始时间（只用来显示右下角时间和合组）。
var protoEpoch = time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)

// actorChar 是角色的角色色：r1、r2… 依次取 chatui.CharColors；主人自己用第一个。
func actorChar(actor string) chatui.OKLCH {
	n := 0
	fmt.Sscanf(actor, "r%d", &n)
	if n <= 0 {
		n = 1
	}
	return chatui.CharColors[(n-1)%len(chatui.CharColors)]
}
func (a *app) composer(c *ui.Context, cv *conversation, r *reading) {
	w, _ := c.Size()
	_ = w
	radius, inset := radiusComposer, spaceMedium
	if cv.Account != "local" {
		readonlyRadius := radiusControl
		a.surface(c, ui.Text(c.Key("platform-readonly"), "只读演练").FontSize(metaSize).FixedLineHeight(metaLine).Padding(spaceMedium).Background(a.P.Composer.Alpha(overlayAlpha)).Radius(readonlyRadius), "platform-readonly", "multiline", readonlyRadius, spaceMedium)
		return
	}
	box := ui.Column(c.Key("composer")).MinHeight(composerMin).Padding(inset).Gap(inset).Radius(radius)
	a.glassOr(box, glassComposer, a.P.Composer.Alpha(overlayAlpha))
	box.Children(func() {
		if len(cv.Queued) > 0 {
			ui.Row(c).Gap(spaceSmall).Children(func() {
				ui.Text(c, fmt.Sprintf("有 %d 条消息在排队", len(cv.Queued))).FontSize(metaSize)
				if cv.QueuePaused {
					a.button(c, "resume-queue", "继续发送", false, false, func() { a.E.resume(cv) })
				}
			})
		}
		input := ui.TextAreaBase(c.Key("draft-"+cv.ID), &cv.Draft).Label("消息").Placeholder("写一条消息…").FontSize(bodySize).FixedLineHeight(bodyLine).Lines(1, 8).MinHeight(0).Grow(1).FillWidth().Bind(&r.Input)
		// 键盘文本可能还没发布到Draft；先同步编辑器，再由下一次构建受理，避免慢帧里的确认先于绑定清空草稿。
		composing := input.Composing()
		input.OnTextKey(func(k ui.TextKey) bool {
			if composing {
				return k.Key == ui.KeyEnter
			}
			if k.Key == ui.KeyEnter && k.Mods == 0 {
				text := k.Text()
				if strings.TrimSpace(text) != "" && cv.PendingDraft == "" && a.KeyboardConversation == "" {
					if a.E.active(cv.ID) {
						if a.E.submit(cv, text) {
							k.SetText("")
						}
					} else {
						if cv.Draft != text {
							k.SetText(text)
						}
						a.KeyboardConversation, a.KeyboardText = cv.ID, text
					}
				}
				return true
			}
			return false
		})
		ui.Row(c.Key("composer-actions")).Gap(inset).AlignItems(ui.End).Justify(ui.End).Children(func() {
			if a.E.active(cv.ID) {
				a.button(c, "stop", "停止", false, false, func() { a.E.stop(cv.ID); r.Input.Focus(c) }).Bind(&r.Stop)
			}
			// 发送按钮直径固定 32，离输入框右、下各一个内距；正圆不和输入框圆角同心（ADR-284）
			disabled := strings.TrimSpace(cv.Draft) == "" || cv.PendingDraft != ""
			bg, ink := a.P.Accent, a.P.OnAccent
			if disabled {
				bg, ink = a.P.Selected, a.P.Muted
			}
			b := ui.ButtonBase(c.Key("send")).Label("发送").Size(sendSize, sendSize).Center().Radius(radiusSend).Background(bg).TextColor(ink).Disabled(disabled).OnClick(func() { a.E.submit(cv, cv.Draft) })
			b.Children(func() { ui.Icon(c, chatui.Icon("send", false)).Size(iconSmall, iconSmall) }) // 照 Bun composer.tsx:259 的 send 图标（审查 D1）
			a.surface(c, b, "send", "send", radiusSend, 0)
		})
	})
	a.surface(c, box, "composer", "composer", radius, inset)
}
func (a *app) side(c *ui.Context, back bool) {
	cv := a.cv()
	a.surface(c, ui.Column(c.Key("side")).Fill().Background(a.P.Pane), "side", "pane", 0, spacePanel).Children(func() {
		ui.Row(c.Key("side-header")).Height(headerHeight).Shrink(0).Padding(paneInset).Gap(spaceSmall).DragWindow().Children(func() {
			ui.Text(c.Key("side-title"), "资料与活动").Grow(1).FontSize(labelSize).FixedLineHeight(metaLine).FontWeight(strongWeight)
			if back {
				a.button(c, "back-chat", "返回聊天", false, false, func() { a.Layer = 1; a.Right = false })
			}
			w, _ := c.Size()
			if w < breakpointNarrow {
				a.sceneEntry(c)
			}
			a.closeControl(c)
		})
		ui.Scroll(c.Key("side-content")).Grow(1).MinHeight(0).Padding(paneInset).Gap(paneInset).Children(func() {
			ui.Column(c.Key("activity-section")).Gap(spaceSmall).Children(func() {
				ui.Text(c, "活动").FontSize(metaSize).FixedLineHeight(metaLine).FontWeight(mediumWeight).TextColor(a.P.Muted)
				if a.E.ReadCount[cv.ID] == 0 && a.E.SearchCount[cv.ID] == 0 {
					ui.Text(c.Key("activity-empty"), "还没有活动，她查资料时会列在这里").FontSize(labelSize).FixedLineHeight(labelLine).TextColor(a.P.Muted)
				} else {
					a.sideRow(c, "activity-row", func() {
						ui.Box(c).Size(avatarSize, avatarSize).Shrink(0).Center().Children(func() { ui.Icon(c, iconSide).Size(iconSize, iconSize).TextColor(a.P.Muted) })
						ui.Text(c.Key("activity-summary"), a.E.activity(cv.ID)).FontSize(labelSize).FixedLineHeight(labelLine)
					})
				}
			})
			ui.Column(c.Key("members-section")).Gap(spaceSmall).Children(func() {
				ui.Text(c, "成员").FontSize(metaSize).FixedLineHeight(metaLine).FontWeight(mediumWeight).TextColor(a.P.Muted)
				for _, id := range cv.Actors {
					a.sideRow(c, "member-row-"+id, func() {
						a.avatar(c, "member-"+id, actorNames[id], id)
						ui.Text(c, actorNames[id]).FontSize(labelSize).FixedLineHeight(labelLine)
					})
				}
			})
		})
	})
}

// drawer 是窄窗里盖在聊天上的右栏：Esc、点遮罩、"返回聊天"三种方式都能关（R059；关的效果和"返回聊天"一样）。
func (a *app) drawer(c *ui.Context) {
	closeDrawer := func() { a.Layer = 1; a.Right = false }
	// 浮层和里面的东西是同一帧新出现的，mygo 的 Transition 进场不播（父元素上一帧要在），所以自己做：
	// 打开的第一帧右栏在右边外面、遮罩透明，之后滑回原位、淡入（审查 B8）
	first := !a.drawerShown
	a.drawerShown = true
	ui.Overlay(c, func() {
		row := ui.Row(c.Key("side-overlay")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Modal().Background(a.P.Ink.Alpha(scrimAlpha))
		if row.OverlayShortcut(0, ui.KeyEscape) {
			closeDrawer()
		}
		op, x := float32(1), float32(0)
		if first {
			op, x = 0, sideMin
		}
		row.Opacity(row.AnimateTo("drawer-o", op, routeMotion))
		a.surface(c, row, "scrim", "scrim", 0, 0).Children(func() {
			ui.ButtonBase(c.Key("scrim-close")).Label("关闭资料").Grow(1).FillHeight().OnClick(closeDrawer)
			// 右栏从右边整块滑出来（审查 B8）
			panel := ui.Box(c.Key("drawer-panel")).Width(sideMin).FillHeight()
			panel.Translate(panel.AnimateTo("drawer-x", x, routeMotion), 0).Children(func() { a.side(c, true) })
		})
	})
}
func (a *app) settings(c *ui.Context) {
	ui.Column(c.Key("settings")).Fill().Children(func() {
		ui.Row(c.Key("settings-header")).Height(headerHeight).Shrink(0).Padding(paneInset).Gap(spaceSmall).DragWindow().Children(func() {
			w, _ := c.Size()
			if w < breakpointNarrow {
				a.button(c, "settings-back", "返回分类", false, false, func() { a.Layer = 0 })
			}
			title := "外观"
			if a.SettingsCat == "plugins" {
				title = "插件"
			}
			ui.Text(c, title).FontSize(labelSize).FixedLineHeight(metaLine).FontWeight(strongWeight).Grow(1)
			if w < breakpointNarrow {
				a.sceneEntry(c)
			}
			a.closeControl(c)
		})
		ui.Scroll(c.Key("settings-content")).Grow(1).MinHeight(0).Padding(paneInset).Children(func() {
			if a.SettingsCat == "plugins" {
				ui.Column(c.Key("plugins-page")).FillWidth().MaxWidth(settingsWidth).Margin(0, ui.Auto).Gap(spaceSmall).Children(func() { a.plugins(c) })
				return
			}
			ui.Column(c).FillWidth().MaxWidth(settingsWidth).Margin(0, ui.Auto).Gap(paneInset).Children(func() {
				ui.Row(c).Gap(spacePanel).Children(func() { ui.Text(c, "深色外观").Grow(1); a.themeSwitch(c) })
				// 照 Bun 外观设置的四个滑块（appearance.ts），拖动时整窗实时重算 chatui 令牌
				a.lookSlider(c, "look-text", "字号", &a.Look.Text, chatui.TextMin, chatui.TextMax, 1)
				a.lookSlider(c, "look-radius", "圆角", &a.Look.Radius, chatui.RadiusMin, chatui.RadiusMax, 1)
				a.lookSlider(c, "look-density", "疏密", &a.Look.Density, chatui.DensityMin, chatui.DensityMax, 0.5)
				ui.Row(c).Gap(spacePanel).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "底色").Grow(1)
					n := int(a.Look.Neutral)
					// 和滑块一样宽，三段均分（段是 Grow，外层不给宽会被挤到最小）
					ui.Column(c.Key("look-neutral-box")).Width(controlWide).Shrink(1).MinWidth(0).Children(func() { a.segment(c, "look-neutral", &n, []string{"偏冷", "中性", "偏暖"}) })
					a.Look.Neutral = chatui.Neutral(n)
				})
				ui.Row(c).Gap(spacePanel).Children(func() {
					ui.Text(c, "聊天背景图案").Grow(1)
					a.switchControl(c, "look-wallpaper", "聊天背景图案", &a.Look.Wallpaper)
				})
				ui.Row(c).Gap(spacePanel).Children(func() { ui.Text(c, "玻璃").Grow(1); a.switchControl(c, "look-glass", "玻璃", &a.Look.Glass) })
				ui.Row(c).Gap(spacePanel).Children(func() {
					ui.Text(c, "等宽字连字").Grow(1)
					a.switchControl(c, "look-ligatures", "等宽字连字", &a.Look.Ligatures)
				})
				ui.Row(c).Gap(spacePanel).Children(func() {
					ui.Text(c, "进行中展开过程").Grow(1)
					a.switchControl(c, "live-open", "进行中展开过程", &a.LiveOpen)
				})
				ui.Text(c, "预览").FontSize(labelSize).FontWeight(strongWeight)
				a.lookPreview(c)
			})
		})
	})
}

// glassOr 给浮在聊天内容上的控制层（标题栏、输入框）上玻璃（Bun"内容实、控制浮"，mygo plugins/glass 的 Liquid Glass）：
// 玻璃上叠一层内容色（标题栏 20%、输入框 35%）、通透度 0.6：mygo 的玻璃默认是 macOS 的磨砂，面板越大越磨，
// 照 Bun 的 50% / 70% 叠上去真窗口里成了一条实心白，看不到下面的壁纸（11-06 真窗口截图）。外观里关了玻璃就用实色 solid。
// 临时：系统"降低透明度"mygo 不自动处理（plugins/glass 文档），以后读系统设置一起退回实色，见待办"界面资源打包"同条。
func (a *app) glassOr(e ui.Element, tint float32, solid ui.Color) {
	if a.Look.Glass {
		e.Material(glass.Glass{Tint: a.T.C.Surface.Alpha(tint), Clarity: 0.6})
		return
	}
	e.Background(solid)
}

// lookSlider 是外观页的一行滑块：名字、当前值、滑块；值存在 Look 里（float32），滑块要 float64，来回转。
func (a *app) lookSlider(c *ui.Context, key, label string, v *float32, lo, hi, step float32) {
	val := float64(*v)
	ui.Row(c.Key(key)).Gap(spacePanel).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).Grow(1)
		ui.Text(c, fmt.Sprintf("%g", *v)).FontFeatures(chatui.Tabular).TextColor(a.P.Muted).MinWidth(valueWidth)
		a.slider(c, key+"-slider", &val, lo, hi, step, label)
	})
	if float32(val) != *v {
		*v = float32(val)
		c.AnimationFrame()
	}
}

// lookPreview 是外观页的气泡预览：她的两条（合组）、你的一条带表情回应、你的一条发送失败，跟着滑块实时变。
func (a *app) lookPreview(c *ui.Context) {
	a1 := actorChar("r1")
	at := protoEpoch
	msgs := []*chatui.Msg{
		{ID: "pv1", Sender: "r1", Name: actorNames["r1"], Char: a1, At: at, Text: "窗外的风停了，纸灯还亮着。"},
		{ID: "pv2", Sender: "r1", Name: actorNames["r1"], Char: a1, At: at.Add(10 * time.Second), Text: "要不要一起把今天的事慢慢说一遍？说到哪里都可以。"},
		{ID: "pv3", Sender: "self", Self: true, At: at.Add(30 * time.Second), Text: "好呀。", Reactions: []chatui.Reaction{{Emoji: "❤", Count: 1, By: []chatui.Reactor{{Name: actorNames["r1"], Char: a1}}}}},
		{ID: "pv4", Sender: "self", Self: true, At: at.Add(40 * time.Second), Text: "这是一条发送失败的消息。", State: chatui.Failed},
	}
	// 消息列宽按预览框实际可用宽：窄窗口下设置页比 settingsWidth 窄，写死会让气泡冲出去
	w, _ := c.Size()
	// 预览卡片的内距等于圆角（都取浮层面板那一档）：气泡离卡片边至少一个圆角半径，永远进不了圆角区，
	// 单行胶囊、多行圆角、相贴小角怎么排都不会和卡片的圆角打架（原来按单行胶囊推圆角，字号拉大后多行气泡掉进角里就不同心了）
	inset := radiusPanel
	column := min(settingsWidth, w-2*paneInset) - 2*inset
	host := chatui.Host{T: a.T, Font: a.Font, SelfChar: a1, Column: column, NoEnter: true}
	box := ui.Column(c.Key("look-preview")).FillWidth().Padding(inset).Radius(inset).Clip().Background(a.T.C.ChatBg) // 壁纸是整张贴的，要按圆角裁掉
	if a.Look.Wallpaper {
		// 壁纸是整张贴的，元素的 Clip 只裁子元素、不裁自己的 Draw，所以画之前按框的圆角裁一次；
		// 放进绝对定位的子元素会画在消息上面，把消息盖住（11-06 真窗口截图）
		box.Draw(func(p *ui.Painter, r ui.Rect) {
			p.Clip(r, inset, func() { a.PreviewWall.Paint(p, r, a.T, a1) })
		})
	}
	box.Children(func() {
		for i, m := range msgs {
			var prev, next *chatui.Msg
			if i > 0 {
				prev = msgs[i-1]
			}
			if i+1 < len(msgs) {
				next = msgs[i+1]
			}
			host.Message(c, m, prev, next)
		}
	})
}

// slider 是自己画的滑块（mygo SliderBase）：高 = 小控件高，圆钮直径 = 高 − 2×嵌套内边距，
// 四边内边距都是半个圆钮宽——上下左右相等，几何检查不用开口子；mygo 自带的 Slider 只在左右留半个圆钮。
// 轨道左段强调色、右段凹块色，圆钮是内容色加轻阴影。
func (a *app) slider(c *ui.Context, key string, val *float64, lo, hi, step float32, label string) {
	t := a.T
	h := t.HSm
	kw := h - 2*t.InsetCtl
	s := ui.SliderBase(c.Key(key), val, float64(lo), float64(hi)).Step(float64(step)).Label(label).Width(controlWide).Shrink(1).MinWidth(hitRegular * 2) // 窄窗口放不下时缩窄.Height(h).Padding(kw / 2).Radius(h / 2) // 聚焦框跟着圆角走
	acc := t.AccentOf(chatui.DefaultChar)
	s.Draw(func(p *ui.Painter, r ui.Rect) {
		frac := float32((*val - float64(lo)) / float64(hi-lo))
		track := 6 * t.S.Text / chatui.TextDefault
		x0, x1 := r.X+kw/2, r.X+r.W-kw/2
		cy := r.Y + r.H/2
		x := x0 + (x1-x0)*frac
		p.Fill(ui.Rect{X: x0 - track/2, Y: cy - track/2, W: x1 - x0 + track, H: track}, t.C.Sunken, track/2)
		p.Fill(ui.Rect{X: x0 - track/2, Y: cy - track/2, W: x - x0 + track, H: track}, acc.Base, track/2)
		p.Shadow(ui.Rect{X: x - kw/2, Y: cy - kw/2, W: kw, H: kw}, kw/2, 0, 1, 3, 0, t.C.ShadowA)
		p.Fill(ui.Rect{X: x - kw/2, Y: cy - kw/2, W: kw, H: kw}, ui.RGB(252, 252, 252), kw/2) // 圆钮深浅都白（Bun --thumb）
		p.Stroke(ui.Rect{X: x - kw/2, Y: cy - kw/2, W: kw, H: kw}, t.C.LineStrong, kw/2, 1)   // 深色下圆钮和底色接近，加一圈细边
	})
}

// layoutList 定会话列表和右栏宽，照 Telegram（window_session_controller.cpp:2766-2858；ADR-285 第 1、2 条）：
// 列表 = 主体宽 × 5/14，不封顶，至少 260（拖过就用拖出来的宽）；两栏时给聊天留够 380。
// 三栏时右栏取设定宽（默认 320，夹在 292～392）；列表 + 右栏放不下就按两者现在的比例一起收，
// 右栏收到 292 或列表收到 260 就停在那，剩下的都给聊天。
func (a *app) layoutList(w float32) {
	body := w - navWidth
	if a.Page == "settings" && !a.ListCompact { // 设置页左栏只放分类，固定宽（照 Codex 侧栏默认 275）
		a.ListWidth = min(settingsListWidth, body-chatMin-splitSize)
		return
	}
	list := float32(math.Round(float64(body * 5 / 14)))
	if a.ListDragged {
		list = a.ListWant
	}
	list = max(list, listMin)
	if a.ListCompact {
		list = listCompact
	}
	side := min(max(a.SideWidth, sideMin), sideMax)
	if w >= breakpointWide && a.Right {
		room := body - chatMin - 2*splitSize
		if list+side > room {
			l := float32(math.Floor(float64(room * list / (list + side))))
			s := float32(math.Floor(float64(room * side / (list + side))))
			switch {
			case s < sideMin:
				s, l = sideMin, room-sideMin
			case l < listMin && !a.ListCompact:
				l, s = listMin, room-listMin
			}
			list, side = l, s
		}
		a.ListWidth, a.SideShown = list, side
		return
	}
	if !a.ListCompact {
		list = max(listMin, min(list, body-chatMin-splitSize))
	}
	a.ListWidth, a.SideShown = list, side
}

// dragList 处理拖分隔条：拖过就不再跟窗口比例走；拖到比 130 窄收成头像模式，从头像模式拖宽过 130 就恢复。
func (a *app) dragList(w float32) {
	a.ListDragged, a.ListWant = true, min(a.ListWidth, listMax)
	switch {
	case a.ListWidth < listCompactDrag:
		a.ListCompact = true
	case a.ListCompact && a.ListWidth > listCompactDrag:
		a.ListCompact = false
	}
	a.layoutList(w)
}

// compactSidebar 是会话列表的头像模式（Telegram 拖到 130 以下）：顶上一排是放大镜，点了展开列表、光标进搜索框；
// 每个会话一格铺满 62 宽（8 + 46 + 8），直角，选中填强调色（ADR-284）。
func (a *app) compactSidebar(c *ui.Context) {
	all := []string{"A", "B"}
	if a.Page == "qq" {
		all = []string{"C", "D"}
	}
	ids := all[a.Group : a.Group+1] // 和展开时同一份：当前分段（私聊或群聊）的会话
	a.surface(c, ui.Column(c.Key("sidebar")).Fill().Background(a.P.Pane), "sidebar", "pane", 0, 0).Children(func() {
		ui.Row(c.Key("search-row")).FillWidth().Height(headerHeight).Shrink(0).Padding(listInset).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			a.iconButton(c, "search-expand", "搜索会话", chatui.Icon("search", false), func() {
				a.ListCompact, a.ListDragged = false, false
				a.SearchField.Focus(c)
			})
		})
		for _, id := range ids {
			cv := a.E.conv(id)
			selected := a.Selected[a.Page] == id
			b := ui.ButtonBase(c.Key("conversation-" + id)).Label(cv.Name).FillWidth().Height(conversationAvatar + 2*conversationInset).Center().
				Selected(selected).OnClick(func() { a.Selected[a.Page] = id; a.Layer = 1 })
			if selected {
				b.Background(a.P.Strong)
			} else if b.Pressed() {
				b.Background(a.P.Press)
			} else if b.Hovered() {
				b.Background(a.P.Hover)
			}
			b.Children(func() {
				a.avatarSized(c, "conversation-avatar-"+id, cv.Name, cv.Actors[0], conversationAvatar)
				if cv.Unread > 0 { // 照 Telegram 小列（dialogs_layout.cpp:304 PaintNarrowCounter）：未读数贴在头像右上
					badgeBg, badgeInk := a.P.Strong, a.P.OnStrong
					if selected {
						badgeBg, badgeInk = a.P.OnStrong, a.P.Strong
					}
					ui.Text(c.Key("unread-"+id), fmt.Sprint(cv.Unread)).FontSize(metaSize).FontFeatures(chatui.Tabular).FixedLineHeight(compactBadge).Padding(0, spaceTight).
						MinWidth(compactBadge).Radius(compactBadge / 2).Background(badgeBg).TextColor(badgeInk).Absolute().Top(spaceTight).Right(spaceTight)
				}
			})
			a.surface(c, b, "conversation-"+id, "conversation", 0, conversationInset)
		}
	})
}

// segHandles 记分段控件轨道和各格的位置，下一帧让选中块滑过去。
type segHandles struct {
	Track      ui.Handle
	Cells      [8]ui.Handle
	IndX, IndW float32 // 这一帧选中块画在轨道里的位置（测试读它量回弹）
}
