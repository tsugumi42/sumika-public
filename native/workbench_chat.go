package main

// 新界面聊天区的接线：外壳（internal/winshell）给标题栏和几何，这里画消息列、把输入区放进外壳留的位置。
// 消息气泡和输入框内部归 internal/chatui 和 workbench_composer.go，不在这里改。

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/winshell"
)

// workbenchChat 只画聊天区（消息列加输入区，没有导航和列表），占满给它的空间。测试和性能量测从这里进。
func (a *app) workbenchChat(c *ui.Context) {
	kit := a.wbKit(a.look.dark, c.Theme().Font)
	c.SetTheme(&kit.Theme)
	kit.ChatOnly(c, &a.workbench.shell.st, a.wbChatSpec(c, kit))
}

func (a *app) wbChatSpec(c *ui.Context, kit *winshell.Kit) winshell.ChatSpec {
	cs := winshell.ChatSpec{Empty: "选择一个会话", Key: a.sel, WidthPct: a.wbWidthPct()}
	if a.sel == "" {
		return cs
	}
	cv := a.convByID(a.sel)
	title := a.title
	cs.Header = winshell.HeaderSpec{
		Title: title, Subtitle: a.wbSubtitle(), Notice: a.status, AvatarIndex: avatarIndexOf(title),
	}
	if cv != nil {
		cs.Header.Title = convName(*cv)
		cs.Header.AvatarIndex = avatarIndexOf(cs.Header.Title)
		cs.Header.Avatar = a.avs().peek(avatarKeyOfConv(cv.Key))
	}
	cs.Header.Glow = a.wbCharColor(cv, cs.Header.Title)
	cs.Header.SideLabel = "资料与活动（Ctrl+\\）"
	a.wbHeaderUsage(c, &cs.Header) // 头像外圈的上下文环（ADR-351）
	cs.OnBack = func() { a.workbench.shell.st.ShowList() }
	var dropped []string
	var droppedText string
	dropHot := false
	cs.OnArea = func(root ui.Element) {
		dropped, dropHot = root.DroppedFiles(), root.FileDragOver() // 整个聊天列都是投放区
		// 浏览器拖来的链接、选中的文字走数据拖放；其中带的本地文件也当文件挂
		if d, ok := ui.DropData(root, dropDataOptions); ok {
			paths, text := dropContent(d.Data)
			dropped, droppedText = append(dropped, paths...), text
		}
		if _, over := ui.DataDragOver(root, dropDataOptions); over {
			dropHot = true
		}
	}
	cs.Body = func(l winshell.ChatLayout) {
		a.list.Key = func(i int) any { return a.msgs[i].rowKey() }
		if a.stepOpen == nil {
			a.stepOpen = map[string]*bool{}
		}
		a.list.FollowEnd = true
		// 采样实际像素比例要占一个元素的 Draw；聊天区自己的 Draw 画背景图案，所以另挂一个看不见的小探针
		a.is().observeScale(c, ui.Box(c.Key("scale-probe")).Absolute().Size(1, 1))
		list := ui.List(c.Key("workbench-messages"), &a.list, len(a.msgs), func(i int) {
			m := a.msgs[i]
			a.rowAt = i
			row := ui.Box(c.Key("message-row")).FillWidth()
			row.Children(func() {
				a.workbenchAnchor(c, i, row)
				h := a.chatHostFor(c, m, i)
				h.Column = l.Column
				if m.liveKind == liveTyping {
					h.Typing(c, "live-typing", chatui.OKLCH{})
					return
				}
				cur := chatMessage(m)
				var prev, next *chatui.Msg
				if i > 0 {
					v := chatMessage(a.msgs[i-1])
					prev = &v
				}
				if i+1 < len(a.msgs) {
					v := chatMessage(a.msgs[i+1])
					next = &v
				}
				h.Message(c, &cur, prev, next)
			})
		}).Fill().Padding(l.Top, l.Horizontal, l.Bottom, l.Horizontal).ScrollbarInsets(kit.G.HeaderHeight, 0, l.Bottom, 0).Label("聊天记录")
		areaH := a.workbench.shell.st.Area.Bounds(c).H
		kit.MessageFade(list, areaH)
		// 群聊头像钉在“可见区域”底边：可见区 = 消息列去掉上下两个浮层（标题栏、输入区），用 StickyAvatars.Visible 换算
		a.stickyAv.Visible = func(view ui.Rect) ui.Rect {
			top, bottom := a.wbOverlayInsets()
			view.Y += top
			view.H -= top + bottom
			return view
		}
		kit.MessageFade(a.stickyAv.Overlay(c), areaH)
		kit.ScrollEdges(c, l) // 标题栏和输入区下面的滚动边缘（渐进模糊）
		a.markIfRead()
		a.loadOlderNearTop()
		kit.ComposerSlot(c, &a.workbench.shell.st, l, func() {
			a.workbenchComposer(c, l.Column, dropped, droppedText, dropHot)
		})
	}
	// 图缓存：这一帧用到的图下一帧不会被扔
	a.is().beginFrame()
	return cs
}

// wbWidthPct 是当前会话的聊天区宽度百分比：群聊（本地群聊、QQ 群）用群聊那一项，私聊用私聊那一项。
// 都是没设就 100（拉满）：私聊 2026-11-09 主人改的默认，“聊天区也是默认全宽好一点”。
func (a *app) wbWidthPct() float32 {
	w := &a.workbench.shell
	if a.group {
		if w.widthPct[1] == 0 {
			return desksettings.DefaultLook().GroupWidth
		}
		return w.widthPct[1]
	}
	if w.widthPct[0] == 0 {
		return 100
	}
	return w.widthPct[0]
}
