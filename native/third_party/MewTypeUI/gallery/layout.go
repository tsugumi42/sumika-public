package gallery

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/compose"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/layout"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func init() {
	Register(Page{ID: "layout/window", Area: "layout", Title: "窗口布局", View: windowPage})
	Register(Page{ID: "layout/divider", Area: "layout", Title: "可拖分隔线", View: dividerPage})
	Register(Page{ID: "layout/third", Area: "layout", Title: "资料与侧栏动效", View: thirdPage})
	Register(Page{ID: "layout/chat", Area: "composition", Title: "完整聊天", View: chatPage})
}

type layoutDemo struct {
	shell       layout.State
	host        *layout.WindowHost
	folders     bool
	wideList    bool
	width       float64
	query       string
	models      [3]*dialogs.Model
	folderLists [3]dialogs.ListState
	list        dialogs.ListState
	rail        dialogs.RailState
	draft       string
	capture     chat.DraftCapture
	field       ui.Handle
	messages    []demoMessage
	next        int
	scroll      ui.ScrollState
	reference   *compose.Reference
	picker      compose.PickerState
	pickerOpen  bool
	viewer      overlay.ViewerState
	media       []overlay.ViewerItem
	toast       overlay.ToastState
	event       string
}

type layoutPreviewKey string

func newLayoutDemo() layoutDemo {
	s := layoutDemo{
		shell: layout.State{Active: layout.Chat}, folders: true,
		width: float64(theme.Light().Metrics().Layout().BigWidth()),
		list:  dialogs.ListState{SelectedID: "studio"}, rail: dialogs.RailState{SelectedID: "all"},
		messages: []demoMessage{
			{id: "arrival", name: "灯", text: "**今天把聊天界面拼起来了。** 试试选择消息、回复和表情回应。"},
			{id: "reply", name: "你", text: "缩窄窗口，列表和聊天会切成单栏。", outgoing: true, reply: &content.QuoteData{Name: "灯", Summary: "今天把聊天界面拼起来了。"}},
			{id: "details", name: "澪", text: "资料栏可以开合，边界拖动会保留宽度。"},
			{id: "media", name: "灯", text: "工作室的色彩练习，点开查看。", photo: true},
		},
		media: []overlay.ViewerItem{{ID: "room", Title: "工作室色彩练习", Information: "灯 · 今天", Caption: "画廊生成素材", Image: contentBitmap()}},
	}
	s.filterConversations()
	return s
}

func chatPage(c *ui.Context) {
	s := ui.Local(c.Root(), "layout-complete-chat", newLayoutDemo)
	s.host = *ui.Local(c.Root(), "gallery-layout-host", func() *layout.WindowHost { return nil })
	demoShell(c, s)
}

func windowPage(c *ui.Context) {
	t := theme.T(c)
	m := t.Metrics()
	s := ui.Local(c.Root(), "layout-window-page", newLayoutDemo)
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(m.Conversation().PaddingLeft()).Gap(m.Control().Spacing()).Children(func() {
			ui.Text(c, "拖动预览宽度，检查单栏 / 双栏 / 三栏；边界线可拖动，也可聚焦后用方向键调整。")
			ui.Slider(c, &s.width, float64(m.Layout().MinWidth()), float64(m.Layout().BigWidth()+m.Layout().ThirdMaxWidth())).Label("预览宽度")
			ui.Row(c).Gap(m.Header().ActionGap()).Children(func() {
				ui.Checkbox(c, &s.folders, "文件夹栏")
				ui.Checkbox(c, &s.shell.Compact, "头像列表")
				ui.Checkbox(c, &s.wideList, "搜索时展开")
				ui.Checkbox(c, &s.shell.ThirdOpen, "第三栏")
			})
		})
		ui.ScrollHorizontal(c).Grow(1).MinHeight(0).Children(func() {
			ui.Row(c).FillHeight().Gap(m.Header().ActionGap()).Children(func() {
				ui.Column(c).Width(float32(s.width)).FillHeight().Children(func() {
					ui.Text(c, "交互预览").Height(m.Header().Height())
					ui.Box(c).Grow(1).MinHeight(0).Children(func() { demoShell(c, s) })
				})
				for _, example := range []struct {
					id, title      string
					width          float32
					active         layout.Region
					compact, third bool
				}{
					{"single-list", "单栏 · 会话", m.Layout().MinWidth(), layout.Conversations, false, false},
					{"single-chat", "单栏 · 聊天", m.Layout().MinWidth(), layout.Chat, false, false},
					{"single-details", "单栏 · 资料", m.Layout().MinWidth(), layout.Chat, false, true},
					{"rail", "单栏 · 保留竖栏", m.Layout().MinWidth() + m.Sidebar().Width(), layout.Chat, false, false},
					{"double", "双栏", m.Layout().ListMinWidth() + m.Layout().ChatMinWidth() + m.Sidebar().Width(), layout.Chat, false, false},
					{"double-details", "双栏 · 资料占主区域", m.Layout().ListMinWidth() + m.Layout().ChatMinWidth() + m.Sidebar().Width(), layout.Chat, false, true},
					{"compact", "头像列表", m.Layout().ListMinWidth() + m.Layout().ChatMinWidth() + m.Sidebar().Width(), layout.Chat, true, false},
					{"triple", "三栏", m.Layout().ListMinWidth() + m.Layout().ChatMinWidth() + m.Layout().ThirdMinWidth() + m.Sidebar().Width(), layout.Chat, false, true},
				} {
					ui.Column(c.Key(example.id)).Width(example.width).FillHeight().Children(func() {
						demo := ui.Local(c.Root(), layoutPreviewKey(example.id), func() layoutDemo {
							v := newLayoutDemo()
							v.shell.Active, v.shell.Compact, v.shell.ThirdOpen = example.active, example.compact, example.third
							return v
						})
						ui.Text(c, example.title).Height(m.Header().Height())
						ui.Box(c).Grow(1).MinHeight(0).Children(func() { demoShell(c, demo) })
					})
				}
			})
		})
		info := s.shell.Info()
		ui.Textf(c, "%.0f DIP · 主体 %.0f · 列表 %.0f · 聊天 %.0f · 第三栏 %.0f", s.width, info.BodyWidth, info.ListWidth, info.ChatWidth, info.ThirdWidth)
	})
}

func thirdPage(c *ui.Context) {
	t := theme.T(c)
	s := ui.Local(c.Root(), "layout-third-page", newLayoutDemo)
	s.host = *ui.Local(c.Root(), "gallery-layout-host", func() *layout.WindowHost { return nil })
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Padding(t.Metrics().Conversation().PaddingLeft()).Gap(t.Metrics().Header().ActionGap()).Children(func() {
			ui.Button(c, "开合资料").OnClick(func() { s.shell.SetThird(c, !s.shell.ThirdOpen, s.host) })
			motion := t.Motion().ThirdPanel()
			ui.Textf(c, "%d ms · %s · 窄窗页面滑动；宿主允许时扩窗", motion.Duration().Milliseconds(), motion.Easing()).Grow(1)
		})
		ui.Box(c).Grow(1).MinHeight(0).Children(func() { demoShell(c, s) })
	})
}

func dividerPage(c *ui.Context) {
	t := theme.T(c)
	m := t.Metrics()
	s := ui.Local(c.Root(), "layout-divider-page", func() struct {
		width   float32
		commits int
	} {
		return struct {
			width   float32
			commits int
		}{width: m.Layout().ListMinWidth()}
	})
	ui.Column(c).Fill().Padding(m.Conversation().PaddingLeft()).Gap(m.Header().ActionGap()).Children(func() {
		ui.Textf(c, "线 %.0f DIP / 命中区 %.0f DIP · 已提交 %d 次", m.Control().BorderWidth(), m.Layout().ResizeHitWidth(), s.commits)
		ui.Text(c, "普通、悬停、拖动和键盘焦点状态：拖动中使用强调色。Tab 聚焦；← / → 调整；Home / End 到边界。")
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Box(c).Width(s.width).Shrink(0).Background(t.Colors().Conversation().BackgroundHover()).Center().Children(func() {
				ui.Textf(c, "左栏 %.0f", s.width)
			})
			layout.Divider(c, s.width, func(v float32) { s.width = v }, layout.DividerOptions{
				Label: "画廊分隔线", Min: m.Layout().ListMinWidth(), Max: m.Layout().ListMaxWidth(), OnCommit: func() { s.commits++ },
			})
			ui.Box(c).Grow(1).MinWidth(0).Background(t.Colors().Header().Background()).Center().Children(func() { ui.Text(c, "右栏") })
		})
	})
}

var demoConversations = [...]struct{ id, initial, name, preview string }{
	{"studio", "星", "星见工作室", "灯：新布局可以拖动分隔线了"},
	{"weekend", "周", "周末计划", "澪：周六下午见"},
	{"notes", "笔", "随手记", "参考图和尺寸记录"},
	{"garden", "庭", "小小庭院", "花开了，等会儿发照片"},
	{"reading", "书", "夜读会", "下一章从这里开始"},
}

type demoMessage struct {
	id, name, text                     string
	outgoing, photo, selected, reacted bool
	reply                              *content.QuoteData
	markdown                           content.MarkdownState
	quote                              content.QuoteState
	menu                               overlay.MenuState
	sending                            chat.SendingState
}

var demoFolders = []dialogs.Folder{{ID: "all", Name: "全部"}, {ID: "work", Name: "工作"}, {ID: "friends", Name: "朋友"}}

var demoFolderIDs = []string{"all", "work", "friends"}

func (s *layoutDemo) filterConversations() {
	query := strings.ToLower(s.query)
	for folder := range s.models {
		entries := make([]dialogs.Entry, 0, len(demoConversations))
		for i, item := range demoConversations {
			if (folder == 0 || i%2 == folder-1) && strings.Contains(strings.ToLower(item.name+" "+item.preview), query) {
				entries = append(entries, dialogs.Entry{Conversation: dialogs.Conversation{ID: item.id, Name: item.name, Initials: item.initial, Preview: item.preview, Time: "18:42", Kind: dialogs.Group}})
			}
		}
		s.models[folder] = dialogs.NewModel(entries)
	}
}

func demoMessageRow(c *ui.Context, s *layoutDemo, msg *demoMessage) {
	a := content.Appearance{Outgoing: msg.outgoing, Selected: msg.selected}
	side := chat.Incoming
	delivery := chat.NoReceipt
	if msg.outgoing {
		side = chat.Outgoing
		delivery = chat.Read
	}
	c.Key(msg.id)
	row := chat.SendingTarget(c, &msg.sending, func() {
		chat.Row(c, chat.Message{SenderName: msg.name, Avatar: contentBitmap(), Info: chat.Info{Label: "18:42", Delivery: delivery}}, chat.RowOptions{
			BubbleOptions: chat.BubbleOptions{Side: side, Selected: msg.selected, MediaBottom: msg.photo, Footer: func(tail ui.Span) {
				count := 2
				if msg.reacted {
					count++
				}
				content.Reactions(c, []content.Reaction{{ID: "like", Emoji: "👍", Count: count, Own: msg.reacted}}, content.ReactionOptions{Appearance: a, InBubble: !msg.photo, Outside: msg.photo, Trailing: tail, OnActivate: func(string) { msg.reacted = !msg.reacted }})
			}, Body: func(tail ui.Span) {
				content.Markdown(c, msg.text, &msg.markdown, content.MarkdownOptions{Appearance: a, Trailing: tail})
				if msg.photo {
					content.Photo(c, demoMedia("工作室色彩练习"), content.MediaOptions{Appearance: a, InBubble: true, Caption: msg.text, Preview: true, BottomInfo: func() {
						chat.ImageInfo(c, chat.Info{Label: "18:42", Delivery: delivery}, chat.BodyOptions{Side: side, Selected: msg.selected})
					}, OnOpen: func() { s.viewer.Show("room") }})
				}
			}}, ReserveAvatar: !msg.outgoing,
		}, func() {
			if msg.reply != nil {
				content.Quote(c, *msg.reply, &msg.quote, content.QuoteOptions{Appearance: a})
			}
		})
	}).Focusable().Role(ui.RoleGroup).Label("Message " + msg.id)
	overlay.ContextMenu(c, row, []overlay.MenuItem{{ID: "reply", Label: "回复"}, {ID: "select", Label: "选择消息", Checkable: true, Checked: msg.selected}}, &msg.menu, overlay.MenuOptions{Label: "消息菜单", OnSelect: func(id string) {
		if id == "reply" {
			s.reference = &compose.Reference{ID: msg.id, Name: msg.name, Summary: msg.text}
		} else {
			msg.selected = !msg.selected
		}
	}})
}

func demoShell(c *ui.Context, s *layoutDemo) {
	t := theme.T(c)
	m, colors := t.Metrics(), t.Colors()
	layout.Window(c, &s.shell, layout.Options{Folders: s.folders, ForceWideList: s.wideList, Host: s.host}, func() {
		layout.Pane(c, &s.shell, layout.Title, func() { demoText(c, "MewTypeUI") })
		layout.Pane(c, &s.shell, layout.Folders, func() {
			dialogs.Rail(c, demoFolders, &s.rail, dialogs.RailOptions{OnSelect: func(string) { s.shell.Active = layout.Conversations }})
		})
		layout.Pane(c, &s.shell, layout.FolderTabs, func() {
			ui.Row(c).Fill().Background(colors.Conversation().Background()).Children(func() {
				for _, folder := range demoFolders {
					ui.Button(c.Key(folder.ID), folder.Name).Grow(1).OnClick(func() { s.rail.SelectedID = folder.ID; s.shell.Active = layout.Conversations })
				}
			})
		})
		layout.Pane(c, &s.shell, layout.Conversations, func() {
			ui.Column(c).Fill().Background(colors.Conversation().Background()).Children(func() {
				if !s.shell.Info().Compact {
					dialogs.Search(c, &s.query, dialogs.SearchOptions{Label: "搜索会话", OnChange: func(string) { s.filterConversations() }})
				} else {
					ui.Button(c, "展开").OnClick(func() { s.shell.Compact = false })
				}
				dialogs.FolderPages(c, demoFolderIDs, s.rail.SelectedID, func(id string) {
					i := 0
					for demoFolderIDs[i] != id {
						i++
					}
					dialogs.List(c, s.models[i], &s.folderLists[i], dialogs.ListOptions{Compact: s.shell.Info().Compact, Label: "会话列表", OnSelect: func(id string) { s.list.SelectedID = id; s.shell.Active = layout.Chat }}).Fill()
				})
			})
		})
		layout.Pane(c, &s.shell, layout.Chat, func() {
			ui.Column(c).Fill().Background(colors.Window().BackgroundHover()).Children(func() {
				title := "星见工作室"
				for _, item := range demoConversations {
					if item.id == s.list.SelectedID {
						title = item.name
						break
					}
				}
				var back func()
				if s.shell.Info().Mode == layout.Single {
					back = func() { s.shell.Active = layout.Conversations }
				}
				chat.Header(c, chat.HeaderInfo{Title: title, Subtitle: "5 位成员 · 2 位在线", Status: chat.Online}, chat.HeaderOptions{OnBack: back, BackLabel: "返回", OnOpen: func() { s.shell.SetThird(c, !s.shell.ThirdOpen, s.host) }, Actions: []chat.Action{{ID: "details", Label: "资料", Kind: chat.MenuAction, OnActivate: func() { s.shell.SetThird(c, !s.shell.ThirdOpen, s.host) }}}})
				ui.Scroll(c.Key("history")).Grow(1).MinHeight(0).TrackScroll(&s.scroll).Children(func() {
					chat.DateMarker(c, "今天", chat.DateOptions{})
					messageBottomSamples(c, false)
					for i := range s.messages {
						demoMessageRow(c, s, &s.messages[i])
					}
				})
				compose.Picker(c.Key("picker"), &s.picker, composeCatalog(c), compose.PickerOptions{Open: &s.pickerOpen, OnSelect: func(tab compose.PickerTab, item compose.PickerItem) {
					if tab == compose.EmojiTab {
						s.draft += item.Emoji
					}
				}})
				chat.CaptureDraft(c.Key("editor-capture"), &s.capture, func() {
					compose.Editor(c.Key("editor"), &s.draft, compose.EditorOptions{Field: compose.FieldOptions{Handle: &s.field}, Reference: s.reference, ReferenceOptions: compose.ReferenceOptions{OnCancel: func() { s.reference = nil }}, EmojiOpen: s.pickerOpen,
						OnSubmit: func(value string) {
							s.next++
							message := demoMessage{id: fmt.Sprintf("sent-%d", s.next), name: "你", text: value, outgoing: true}
							message.sending.Start(s.capture.Take(c, &s.field))
							if s.reference != nil {
								message.reply = &content.QuoteData{Name: s.reference.Name, Summary: s.reference.Summary}
							}
							s.messages = append(s.messages, message)
							s.draft, s.reference = "", nil
							// The layout clamps this against the new content extent.
							s.scroll.Y = math.MaxFloat32
						},
						OnEmoji:  func() { s.pickerOpen = !s.pickerOpen },
						OnAttach: func() { s.event = "宿主收到附件选择请求"; s.toast.Show() },
						OnVoice:  func() { s.event = "宿主收到录音请求"; s.toast.Show() },
					})
				})
			})
		})
		layout.Pane(c, &s.shell, layout.Third, func() {
			ui.Column(c).Fill().Background(colors.Window().Background()).Children(func() {
				ui.Row(c).FillWidth().Height(m.Header().Height()).Padding(0, m.Conversation().PaddingLeft()).Children(func() {
					demoText(c, "群组资料").Grow(1).FontWeight(t.Typography().HeaderTitle().Weight())
					ui.Button(c, "关闭资料").OnClick(func() { s.shell.SetThird(c, false, s.host) })
				})
				ui.Scroll(c).Grow(1).MinHeight(0).Padding(m.Message().MarginLeft()).Gap(m.Header().ActionGap()).Children(func() {
					demoText(c, "星见工作室")
					demoText(c, "成员与共享媒体").TextColor(colors.Window().MutedText())
					for _, id := range []string{"灯", "澪", "千咲", "春", "你"} {
						ui.Box(c.Key(id)).FillWidth().Height(m.Conversation().RowHeight()).Background(colors.Conversation().BackgroundHover()).
							Padding(m.Conversation().PaddingLeft()).Children(func() { demoText(c, id) })
					}
				})
			})
		})
	})
	for i := range s.messages {
		chat.SendingFlight(c.Key(s.messages[i].id), &s.messages[i].sending)
	}
	overlay.Viewer(c.Key("media-viewer"), &s.viewer, s.media, overlay.ViewerOptions{})
	overlay.Toast(c.Key("notification"), &s.toast, s.event, overlay.ToastOptions{})
}

func demoText(c *ui.Context, text string) ui.Element {
	s := theme.T(c).Typography().Body()
	return ui.Text(c, text).FontSize(s.Size() * c.Preferences().TextScale).FontWeight(s.Weight())
}
