package main

import (
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// 合并转发详情放在右栏（待办“合并转发详情进右栏”；主人 2026-11-09：整块白盖住窗口，三个键也点不到）。
// 不再是模态窗：聊天还能看，窗口按钮还在。右栏原来关着的，看完关掉时恢复原样。
// 旧界面（mainView=legacy）还走 forwardWindow。

// forwardSideState 记右栏是不是因为看转发才打开的。
type forwardSideState struct {
	shown      bool
	sideBefore bool
}

// wbForwardSide 在画外壳之前调：有转发要看就把右栏打开、内容换成它。右栏被用户关掉（按钮、抽屉遮罩、窄窗口返回）就算看完了。
func (a *app) wbForwardSide(c *ui.Context, kit *winshell.Kit, sp *winshell.SideSpec) {
	f := &a.forward
	w := &a.workbench.shell
	st := &w.st
	open := f.open && len(f.stack) > 0
	if open && f.stack[0].request.Source.Conversation.Conversation != a.sel {
		a.stopForward() // 换了会话
		open = false
	}
	vw, _ := c.Size()
	mode := kit.Mode(vw)
	visible := func() bool {
		switch mode {
		case winshell.Narrow:
			return st.Layer == winshell.LayerSide
		}
		return st.Panes.SideOpen
	}
	if !open {
		if w.forwardSide.shown {
			w.forwardSide.shown = false
			st.Panes.SideOpen = w.forwardSide.sideBefore
			if st.Layer == winshell.LayerSide {
				st.Layer = winshell.LayerChat
			}
		}
		return
	}
	switch {
	case !w.forwardSide.shown:
		w.forwardSide = forwardSideState{shown: true, sideBefore: st.Panes.SideOpen}
		st.Panes.SideOpen = true
		if mode != winshell.Full {
			st.Layer = winshell.LayerSide
		}
	case !visible():
		w.forwardSide.shown = false
		a.stopForward()
		return
	}
	layer := f.stack[len(f.stack)-1]
	back := func() { a.forwardBack() }
	title := "聊天记录"
	if len(layer.msgs) > 0 {
		title = forwardTitle(layer.view)
	}
	g := kit.G
	sp.Tabs = func() {
		ui.Row(c.Key("forward-head")).Grow(1).MinWidth(0).Gap(g.SpaceMicro).AlignItems(ui.Center).Children(func() {
			if len(f.stack) > 1 {
				kit.IconButton(c, "forward-back", "返回上一层", winshell.IconBack, back)
			}
			ui.Text(c.Key("forward-title"), title).Grow(1).MinWidth(0).Padding(0, g.SpaceSmall).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine().Tooltip(title)
			if mode == winshell.Full { // 浮动时点外面、窄窗口用返回键，不再摆一个 X
				kit.IconButton(c, "forward-close", "关闭聊天记录", winshell.IconClose, a.stopForward)
			}
		})
	}
	sp.Recessed = true
	sp.KeepOpen = func() bool { return a.chatLightbox.open } // 看转发里的图时点大图不收起右栏
	sp.Escape = func() bool {                                // 浮动右栏里 Esc 先退一层，最外层才关
		if len(f.stack) > 1 {
			back()
			return true
		}
		return false
	}
	sp.Body = func() {
		c.OnWindowShortcut(0, ui.KeyEscape, back)
		c.OnWindowShortcut(ui.Alt, ui.KeyLeft, back)
		a.wbForwardBody(c, kit, layer)
	}
}

// forwardBack 退一层；已经是最外层就关掉。
func (a *app) forwardBack() {
	f := &a.forward
	if len(f.stack) <= 1 {
		a.stopForward()
		return
	}
	f.cancelRead()
	last := len(f.stack) - 1
	f.stack[last] = nil
	f.stack = f.stack[:last]
	f.warning = ""
	if parent := f.stack[len(f.stack)-1]; parent.dirty {
		a.loadForward(parent, false)
	}
}

// wbForwardBody 画这一层的消息：和聊天区同一种气泡，一个节点一行，虚拟列表。
func (a *app) wbForwardBody(c *ui.Context, kit *winshell.Kit, layer *forwardLayer) {
	f := &a.forward
	g, p := kit.G, kit.P
	f.downloads.beginFrame()
	defer f.downloads.endFrame()
	f.rendering = layer // 媒体缓存归这个查看器，看完一起收掉（a.is、bindChatMedia 认它）
	defer func() { f.rendering = nil }()
	if f.imgs != nil {
		f.imgs.beginFrame()
	}
	column := layer.list.Bounds(c).W - 2*g.PaneInset
	if column <= 0 {
		column = winshell.SideDefault - 2*g.PaneInset
	}
	body := ui.Column(c.Key("forward-body")).Fill()
	a.is().observeScale(c, body)
	body.Children(func() {
		ui.List(c.Key(layer), &layer.list, len(layer.msgs), func(i int) {
			m := layer.msgs[i]
			a.forwardMessage(c, kit, layer, m, i, column)
		}).Grow(1).MinHeight(0).Padding(g.PaneInset).Label("聊天记录的消息")
		state := forwardStateText(layer.view)
		if layer.view.State == "ready" && layer.view.Missing == 0 {
			state = "" // 正常就不写，少占一行
		}
		note := func(text string) {
			ui.Text(c, text).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap().Padding(g.SpaceSmall, g.PaneInset)
		}
		if state != "" {
			note(state)
		}
		if f.warning != "" {
			note(f.warning)
		}
		if layer.view.State != "ready" && layer.view.State != "fetching" && layer.view.State != "not_receiving" && layer.view.State != "refused" {
			if _, ok := a.reader.(api.ForwardActions); ok {
				ui.Box(c.Key("forward-retry")).Padding(0, g.PaneInset, g.SpaceSmall, g.PaneInset).Children(func() {
					kit.Button(c, "forward-retry-button", "重试", false, false, func() { a.loadForward(layer, true) })
				})
			}
		}
	})
}

// forwardMessage 画一个节点。聊天区才有的东西（钉住的头像、回复、跳转、进场动画）这里都没有。
func (a *app) forwardMessage(c *ui.Context, kit *winshell.Kit, layer *forwardLayer, m *msg, i int, column float32) {
	a.forward.rowAt = i // 动图判断可见靠它（rowVisible）
	h := a.chatHostFor(c, m, i)
	h.Group, h.Column, h.SquareAvatar = true, column, false
	h.Sticky, h.OnReply, h.OnQuote, h.OnQuotePart = nil, nil, nil, nil
	h.Appear = nil
	h.ViewRect = func() ui.Rect { return layer.list.Bounds(c) }
	cur := chatMessage(m)
	var prev, next *chatui.Msg
	if i > 0 {
		v := chatMessage(layer.msgs[i-1])
		prev = &v
	}
	if i+1 < len(layer.msgs) {
		v := chatMessage(layer.msgs[i+1])
		next = &v
	}
	ui.Box(c.Key("forward-row")).FillWidth().Children(func() { h.Message(c, &cur, prev, next) })
}
