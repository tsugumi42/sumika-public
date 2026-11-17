package main

// 外壳、导航和顶栏使用 MewTypeUI；会话数据与页面路由由 Sumika 持有。
import (
	"strings"

	"github.com/egoist/mygo/ui"
	mewchat "github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/control"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/layout"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/theme"
	"sumika-native/internal/desksettings"
)

var railNav = []dialogs.Folder{{ID: "local", Name: "本地", Icon: navIcon("house")}, {ID: "qq", Name: "QQ", Icon: navIcon("message-circle")}, {ID: "memory", Name: "记忆", Icon: navIcon("brain")}, {ID: "runtime", Name: "运行", Icon: navIcon("activity")}, {ID: "settings", Name: "设置", Icon: navIcon("settings")}}

var sideTabs = []control.TabItem{{ID: "session", Label: "这个会话"}, {ID: "thinking", Label: "她在想什么"}, {ID: "stats", Label: "程序数据"}}

// fade 换页：淡出淡入，时长交给 mygo 默认（和 motion.CrossFade 一样）
var fade = ui.ElementTransition{Enter: &ui.Motion{}, Exit: &ui.Motion{}}

func (a *app) view(c *ui.Context) {
	a.bodies.begin()
	defer a.bodies.end()
	a.downloads.beginFrame()
	defer a.downloads.endFrame()
	a.applyLook(c)
	a.secretsFrame() // 恢复码的清理由这一帧管，不靠设置页停在哪个标签
	a.publishViewing()
	a.memSync() // 记忆页的订阅只在它在前台时活着
	a.runSync() // 运行页的订阅同理：用量、存储标签不在前台就没有定时器
	// Tester 没有原生窗口；真实窗口的独立浮层统一绑定到同一个宿主。
	if a.win != nil {
		overlay.Native(c, a.win, func() { a.windowView(c) })
	} else {
		a.windowView(c)
	}
}

func (a *app) windowView(c *ui.Context) {
	if a.rail != 4 {
		a.models.leave()
		a.linkSettings.leave()
	}
	if a.rail <= 1 && !a.forward.visible() && !a.workbenchEnabled() {
		a.paneKeys(c)
	}
	if !a.pn.ready {
		a.pn.load(desksettings.DefaultPanes())
	}
	a.nav.SelectedID = railNav[a.rail].ID
	railOptions := dialogs.RailOptions{OnSelect: func(id string) {
		for i := range railNav {
			if railNav[i].ID == id {
				a.rail = i
				return
			}
		}
	}}
	switch {
	case a.workbenchEnabled(): // 新界面：五页都在新外壳里（记忆、运行、设置的内容区先装旧页，见 legacyPage）
		a.workbenchView(c, a.look.dark)
	case a.rail <= 1:
		a.legacyWindowBar(c, func() {
			layout.Window(c, &a.pn.layout, layout.Options{Folders: true}, func() {
				layout.Pane(c, &a.pn.layout, layout.Title, func() { a.titleView(c) })
				layout.Pane(c, &a.pn.layout, layout.Folders, func() { dialogs.Rail(c, railNav, &a.nav, railOptions).Grow(1) })
				layout.Pane(c, &a.pn.layout, layout.FolderTabs, func() { a.folderTabs(c, railOptions) })
				a.panesView(c)
			}).Background(windowBackground(c))
		})
	default:
		a.legacyWindowBar(c, func() { a.pageShell(c, railOptions) })
	}
	a.persistPanes(c)
	if !a.workbenchEnabled() { // 新界面的合并转发在右栏（workbench_forward.go）
		a.forwardWindow(c)
	}
	a.chatImageWindow(c)
}

// legacyWindowBar 给旧界面顶上补一条带子放窗口按钮：系统的三个键已经隐藏（shell.go），旧界面自己不画。
// 临时：旧界面下线时和它一起删（规格-新界面主线）。
func (a *app) legacyWindowBar(c *ui.Context, body func()) {
	if a.winControls == nil {
		body()
		return
	}
	kit := a.wbKit(a.look.dark, c.Theme().Font)
	ui.Column(c.Key("legacy-window")).Fill().Children(func() {
		ui.Row(c.Key("legacy-window-bar")).FillWidth().Height(kit.G.HeaderHeight).Shrink(0).Padding(0, kit.G.SpaceSmall).Justify(ui.End).AlignItems(ui.Center).Background(windowBackground(c)).DragWindow().Children(func() {
			kit.WindowButtons(c)
		})
		ui.Box(c.Key("legacy-window-body")).Grow(1).MinHeight(0).FillWidth().Children(body)
	})
}

func (a *app) folderTabs(c *ui.Context, o dialogs.RailOptions) {
	// 照 MewTypeUI gallery/layout.go 的窄窗导航组合。
	ui.Row(c).Fill().Children(func() {
		for _, folder := range railNav {
			ui.Button(c.Key(folder.ID), folder.Name).Grow(1).OnClick(func() { o.OnSelect(folder.ID); a.pn.layout.Active = layout.Conversations })
		}
	})
}

// 记忆和设置占满导航右侧，不套聊天的列表/资料分栏。
func (a *app) pageShell(c *ui.Context, o dialogs.RailOptions) {
	m := theme.T(c).Metrics()
	w, _ := c.Size()
	ui.Column(c).Fill().Background(windowBackground(c)).Children(func() {
		bar := c.TitleBar()
		ui.Box(c.Key("page-title")).FillWidth().Height(bar.Height).Shrink(0).Padding(0, bar.Right, 0, bar.Left).DragWindow().Children(func() { a.titleView(c) })
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			if w >= m.Layout().MinWidth()+m.Sidebar().Width() {
				dialogs.Rail(c, railNav, &a.nav, o).FillHeight()
			}
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				if w < m.Layout().MinWidth()+m.Sidebar().Width() {
					ui.Box(c).FillWidth().Height(m.Layout().FolderTabsHeight()).Children(func() { a.folderTabs(c, o) })
				}
				ui.Box(c.Key(a.rail)).Grow(1).MinHeight(0).Children(func() { a.legacyPageBody(c) })
			})
		})
	})
}

// legacyPageBody 是记忆、运行、设置三页的内容（旧组件库画的）；旧界面的页壳和新界面外壳里的内容区共用它。
func (a *app) legacyPageBody(c *ui.Context) {
	switch {
	case a.rail == 4 && a.demoMode && a.set != nil:
		a.settingsView(c)
	case a.rail == 4:
		a.realSettingsView(c)
	case a.rail == 3:
		a.runPageView(c)
	default:
		a.memoryView(c)
	}
}

// legacyPage 把这三页放进新界面外壳的内容区。页里还是旧组件库画的，所以先重新应用旧主题、铺旧窗口底，
// 免得它们读到外壳的令牌而花掉。临时：只有这个桥允许新界面的内容区碰旧主题，记忆、运行、设置三页按原型的令牌重做后删掉
// （规格-新界面主线“原型外壳迁移”，H 设置页外壳那一步先换设置页）。
func (a *app) legacyPage(c *ui.Context) {
	a.applyLook(c)
	ui.Column(c.Key("legacy-page")).Fill().Background(windowBackground(c)).Children(func() { a.legacyPageBody(c) })
}

// legacySettingsBody 是新界面设置页里“外观”以外各分类的内容：旧组件库画的，所以先重新应用旧主题、铺旧窗口底。
// 临时：设置页各分类按原型的令牌逐页重做，做完一页换一页，全换完删掉这个桥。
func (a *app) legacySettingsBody(c *ui.Context) {
	a.applyLook(c)
	ui.Column(c.Key("legacy-settings")).FillWidth().Background(windowBackground(c)).Children(func() {
		if a.demoMode && a.set != nil {
			a.settingsView(c)
			return
		}
		a.settingsTabContent(c)
	})
}

// ───────────── 会话列表 ─────────────

// capReason 某个能力没接上的原因（后台给的）；没给就用默认说法
func (a *app) capReason(name, fallback string) string {
	for _, cp := range a.caps {
		if cp.Name == name && cp.Reason != "" {
			return cp.Reason
		}
	}
	return fallback
}

func convName(cv conv) string {
	if strings.TrimSpace(cv.Title) == "" {
		return unnamedConv
	}
	return cv.Title
}

// ───────────── 聊天 ─────────────

func (a *app) chatView(c *ui.Context) {
	t := c.Theme()
	a.list.Key = func(i int) any { return a.msgs[i].id }
	a.list.FollowEnd = true
	st := a.is()
	st.beginFrame() // 图缓存：这一帧用到的图下一帧不会被扔。
	a.arr.span = theme.T(c).Motion().MessageReveal().Duration()
	a.arr.sweep(c.Now()) // 滚出屏幕的行不再构建，它在途表里的项只有这里清得到
	root := ui.Column(c).Fill().Label("聊天窗口")
	st.observeScale(c, root) // 在不参与消息入场的根上采样实际比例。
	root.Children(func() {
		a.chatHeader(c)
		if a.status != "" {
			localCaption(c, a.status).TextColor(theme.T(c).Colors().Semantic().Danger()).Padding(t.Space(2), t.Space(5))
		}
		ui.List(c, &a.list, len(a.msgs), func(i int) { a.message(c, i) }).Grow(1).Padding(t.Space(4), t.Space(5)).Justify(ui.End)
		a.markIfRead()       // 看到了最新一条就标已读（readmark.go）
		a.loadOlderNearTop() // 滚到接近顶部就取更早的一页（scrolltop.go）
		ui.Column(c).FillWidth().Shrink(0).Children(func() {
			a.localComposer(c)
		})
	})
}

func (a *app) chatHeader(c *ui.Context) {
	state := &a.pn.layout
	actions := []mewchat.Action{{ID: "history", Label: "加载更早的消息", Icon: navIcon("history"), Disabled: a.loading || !a.hasMore, OnActivate: func() { a.olderOff = false; a.olderPage(200) }}}
	if a.demoMode {
		actions = append(actions, mewchat.Action{ID: "stress", Label: "压测：加载到 1 万条", Icon: navIcon("gauge"), Disabled: a.loading, OnActivate: func() { a.stress(10000) }})
	}
	label := labelSideUnfold
	if state.ThirdOpen {
		label = labelSideFold
	}
	actions = append(actions, mewchat.Action{ID: "third", Label: label, Icon: navIcon("panel-right-open"), Active: state.ThirdOpen, OnActivate: func() { state.SetThird(c, !state.ThirdOpen, nil) }})
	o := mewchat.HeaderOptions{Actions: actions}
	if state.Info().Mode == layout.Single {
		o.BackLabel = "返回会话列表"
		o.OnBack = func() { state.Active = layout.Conversations }
	}
	mewchat.Header(c, mewchat.HeaderInfo{Title: a.title, Subtitle: a.subtitle(c)}, o).Label("聊天顶栏")
}

// ───────────── 右侧栏 ─────────────

// sideView 的关闭入口在本栏，窄窗第三栏占主槽时仍可返回聊天。
func (a *app) sideView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Background(theme.T(c).Materials().Sidebar().Background()).Label("右侧栏内容").Children(func() {
		mewchat.Header(c, mewchat.HeaderInfo{Title: "会话资料"}, mewchat.HeaderOptions{BackLabel: labelSideFold, OnBack: func() { a.pn.layout.SetThird(c, false, nil) }}).Label("右侧栏顶栏")
		control.Tabs(c.Key("side-tabs"), sideTabs, sideTabs[a.tab].ID, control.TabsOptions{OnSelect: func(id string) {
			for i := range sideTabs {
				if sideTabs[i].ID == id {
					a.tab = i
					return
				}
			}
		}}).Label("右侧栏")
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Box(c).Children(func() {
				c.Key(a.tab)
				ui.Column(c).Padding(t.Space(4)).Gap(t.Space(3)).Transition(fade).Children(func() {
					switch a.tab {
					case 0:
						a.sessionInfo(c)
					case 1:
						localCaption(c, "她看到了哪些消息、打不打算接话、注入了什么：还没接到 Go 后台，排在后面。")
					case 2:
						a.stats(c)
					}
				})
			})
		})
	})
}

// sessionInfo 右侧栏第一页：只写当前会话的身份、看到它的角色、数据从哪来（规格 4.2 最后一行）；没有假审批和编的注入
func (a *app) sessionInfo(c *ui.Context) {
	t := c.Theme()
	if a.sel == "" {
		localCaption(c, "还没打开会话")
		return
	}
	k := a.keyOf(a.sel)
	var observers []string
	for _, list := range [][]conv{a.local, a.homes, a.convs} {
		for _, cv := range list {
			if cv.Key.Conversation == a.sel {
				for _, id := range cv.Observers {
					name := id
					if ch, ok := a.charOf(id); ok && ch.Name != "" {
						name = ch.Name
					}
					observers = append(observers, name)
				}
			}
		}
	}
	src := "Go 后台 · " + phaseText(a.host.Phase)
	if a.demoMode {
		src = "演示数据（全是编的）"
	}
	scene := map[string]string{"private": "私聊", "group": "群聊"}[k.Scene]
	place := map[string]string{"local": "本地", "qq": "QQ"}[k.Platform]
	rows := [][2]string{{"场合", place + scene}, {"会话", k.Conversation}, {"数据来源", src}}
	if len(observers) > 0 {
		rows = append(rows, [2]string{"看到它的角色", strings.Join(observers, "、")})
	}
	for _, r := range rows {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, r[0]).TextColor(t.TextMuted).Width(96)
			ui.Text(c, r[1]).Selectable().Grow(1).Wrap()
		})
	}
	localCaption(c, "本地审批在聊天输入框上方处理；注入明细和工具卡尚未接入。")
}
