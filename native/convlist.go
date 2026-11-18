package main

// 会话列表共用不可变快照；宽/窄形态和虚拟行都交给 dialogs.List。
import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/layout"
	"github.com/tsugumi42/MewTypeUI/theme"
	"sumika-native/internal/api"
	"sumika-native/internal/loadui"
)

type conversationList struct {
	model             *dialogs.Model
	entries           []dialogs.Entry
	keys              map[string]api.ConversationKey
	state             dialogs.ListState
	revision, avatars uint64
	query             string
	selection         api.ConversationKey
	singleList        bool
}

func dialogID(k api.ConversationKey) string {
	return k.Platform + "\x00" + k.Channel + "\x00" + k.Scene + "\x00" + k.Conversation
}

const unnamedConv = "未命名的会话"

func (a *app) convList(c *ui.Context) {
	t := theme.T(c)
	info := a.pn.layout.Info()
	compact := info.Compact
	singleList := info.Mode == layout.Single && a.pn.layout.Active == layout.Conversations
	s := a.conversationModel(a.rail)
	selected := a.keyOf(a.sel)
	if s.selection != selected || s.singleList != singleList {
		s.selection, s.singleList = selected, singleList
		if !singleList {
			s.state.SelectedID = dialogID(selected)
		}
	}
	// 单栏列表没有并排聊天的高亮；清控件选择后，回点当前会话也能触发 OnSelect。
	// a.sel 仍保留聊天身份，回到宽栏再恢复完整 ID 的高亮。
	if a.sel == "" || singleList {
		s.state.SelectedID = ""
	}
	ui.Column(c).Fill().Background(t.Materials().Conversation().Background()).Label("会话列表").Children(func() {
		paneHeader(c, "会话列表").Padding(0, t.Metrics().Control().Spacing()).Children(func() {
			if !compact {
				c.Key("conversation-search")
				dialogs.Search(c, &a.search[a.rail], dialogs.SearchOptions{Label: "搜索会话", Placeholder: "搜索会话", ClearLabel: "清空会话搜索"}).Grow(1).MinWidth(0)
			}
			c.Key("list-toggle")
			label, icon := labelListFold, "panel-left-close"
			if compact {
				label, icon = labelListUnfold, "panel-left-open"
			}
			m := t.Metrics().Header()
			ui.ButtonBase(c).Size(m.ActionWidth(), m.Height()).Shrink(0).Label(label).Tooltip(label).OnClick(a.pn.toggleList).Center().Children(func() {
				ui.Icon(c, navIcon(icon)).Size(m.IconSize(), m.IconSize()).TextColor(t.Colors().Menu().IconForeground())
			})
		})
		if a.rail == 0 && a.canCreateGroup() && !compact {
			ui.Button(c, "新建群聊").Margin(t.Metrics().Control().Spacing()).OnClick(a.beginGroup)
		}
		selectConversation := func(id string) { a.open(s.keys[id].Conversation) }
		dialogs.List(c, s.model, &s.state, dialogs.ListOptions{Compact: compact, Label: "会话条目", OnSelect: selectConversation, OnActivate: selectConversation}).Grow(1).MinHeight(0)
		// 第一份概览还没到：什么都别写（原来这几行"还没有…"会在到之前闪一下），读得慢了才写"取数据中…"
		ov := a.ovLoad.View(c.Now())
		loadui.Wake(c, ov)
		waiting := a.ovLoad.Loading()
		if waiting && (len(s.keys) == 0 || a.rail == 0 && len(a.homes) == 0) {
			loadui.Line(ov, "取数据中…", func(s string) ui.Element { return ui.Text(c, s).TextColor(t.Colors().Window().MutedText()).Wrap() })
		}
		if len(s.keys) == 0 && !waiting {
			text := "没有匹配的会话"
			if strings.TrimSpace(a.search[a.rail]) == "" {
				if a.rail == 0 {
					text = "还没有本地会话"
				} else {
					text = "还没有 QQ 会话"
				}
			}
			ui.Text(c, text).TextColor(t.Colors().Window().MutedText()).Wrap()
		}
		if a.rail == 0 && len(a.homes) == 0 && !compact && !waiting && strings.TrimSpace(a.search[0]) == "" {
			text := "还没有群聊"
			if !a.canCreateGroup() {
				text = "群聊：" + a.capReason("local-group", "当前数据源不能建群")
			}
			ui.Text(c, text).TextColor(t.Colors().Window().MutedText()).Wrap()
		}
		if a.moreConv && !compact {
			ui.Text(c, "还有更多会话没列出来（一次最多列 1000 个）").TextColor(t.Colors().Window().MutedText()).Wrap()
		}
	})
	// List 先确定实际构建范围，再请求对应头像，不为1000项同时建立任务。
	first, last := s.state.View.Visible()
	for i := max(0, first); i <= min(last, len(s.entries)-1); i++ {
		if k, ok := s.keys[s.entries[i].Conversation.ID]; ok {
			a.avs().want(avatarKeyOfConv(k))
		}
	}
	a.groupDialog(c)
}

// 概览、搜索或头像完成才换模型，普通构建和拖栏复用整份快照。
func (a *app) conversationModel(rail int) *conversationList {
	s := &a.dialogs[rail]
	avatars := a.avs()
	if s.model != nil && s.revision == a.dialogRevision && s.avatars == avatars.revision && s.query == a.search[rail] {
		return s
	}
	query := strings.ToLower(strings.TrimSpace(a.search[rail]))
	capacity := len(a.convs)
	if rail == 0 {
		capacity = len(a.local) + len(a.homes) + 2
	}
	entries := make([]dialogs.Entry, 0, capacity)
	keys := make(map[string]api.ConversationKey, capacity)
	add := func(heading string, cs []conv) {
		start := len(entries)
		for i := range cs {
			cv := &cs[i]
			if query != "" && !strings.Contains(strings.ToLower(convName(*cv)), query) && !strings.Contains(strings.ToLower(cv.Last.Text), query) {
				continue
			}
			item, visible := conversationItem(cv)
			if !visible {
				continue
			}
			if heading != "" && len(entries) == start {
				entries = append(entries, dialogs.Entry{HeadingID: "heading:" + heading, Heading: heading})
			}
			item.Avatar = avatars.peek(avatarKeyOfConv(cv.Key))
			entries = append(entries, dialogs.Entry{Conversation: item})
			keys[item.ID] = cv.Key
		}
	}
	if rail == 0 {
		add("私聊", a.local)
		add("群聊", a.homes)
	} else {
		add("", a.convs)
	}
	s.entries, s.keys, s.model = entries, keys, dialogs.NewModel(entries)
	s.revision, s.avatars, s.query = a.dialogRevision, avatars.revision, a.search[rail]
	return s
}

// 本地空私聊仍是入口；空群使用真实创建时间，其他空历史不伪造时间。
func conversationItem(cv *conv) (dialogs.Conversation, bool) {
	at, last := cv.Last.CreatedAt, strings.ReplaceAll(cv.Last.Text, "\n", " ")
	if at == 0 && cv.Kind == "home" {
		at, last = cv.CreatedAt, "还没有消息"
	}
	if at == 0 && cv.Key.Platform != "local" {
		return dialogs.Conversation{}, false
	}
	if at == 0 && last == "" {
		last = "还没有消息"
	}
	stamp := ""
	if at != 0 {
		stamp = time.UnixMilli(at).Format("15:04")
	}
	kind := dialogs.Person
	if cv.Key.Scene == "group" || cv.Kind == "home" || cv.Kind == "group" {
		kind = dialogs.Group
	}
	name := convName(*cv)
	return dialogs.Conversation{ID: dialogID(cv.Key), Name: name, Initials: initialOf(name), Preview: last, Time: stamp, Unread: max(cv.Unread, 0), Kind: kind}, true
}
