package main

// 后台说"有件事要主人看"（目标达成或暂停、定期任务有结果、审批卡出现，见 internal/app/attention.go）时，
// 弹一条系统通知，点通知把主窗口叫出来并切到那个会话。哪一类弹由后台按设置页“通知”的开关先筛过，这里只管弹不弹得出来：
// 只有"窗口在前台、而且正看着这个会话"才不弹（主人已经看见了）；窗口在后台、最小化、没有键盘焦点，
// 或在前台但看的是别的会话（也包括停在设置页），都弹。做法同 Telegram Desktop：当前打开的就是这个会话才不提醒。
// 前台状态用标已读同一个原子量（readmark.go 的 fg：显示着、没最小化、有键盘焦点），正看着哪个会话见 readMark.viewing。

import (
	"strings"

	"github.com/egoist/mygo"

	backend "sumika-native/internal/app"
)

// attentionIDPrefix 是这类通知的 ID 前缀：同一会话同一类的新通知顶替旧的，点击时据它认出是自己的
const attentionIDPrefix = "attention|"

func (s *shell) attention(e backend.Attention) {
	if s.a == nil || s.a.rm.watching(e.Conversation) {
		return
	}
	show := s.showNotice
	if show == nil {
		show = func(id, title, body string) error {
			return mygo.NewNotification(mygo.NotificationOptions{ID: id, Title: title, Body: body, Image: windowIconPNG}).Show()
		}
	}
	if err := show(attentionIDPrefix+e.Kind+"|"+e.Conversation, e.Title, e.Body); err != nil {
		s.logf("系统通知没弹出来：%v", err)
	}
}

// attentionConversation 从通知 ID（attention|种类|会话）里取出会话；不是我们的通知、没带会话就返回 false
func attentionConversation(id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, attentionIDPrefix)
	if !ok {
		return "", false
	}
	_, conv, _ := strings.Cut(rest, "|")
	return conv, true
}

// notificationClicked 点了通知：是我们弹的就把主窗口叫出来（最小化的还原、藏起来的显示、聚焦），
// 再切到通知说的那个会话并滚到最新一条。会话已经不在列表里（删了、重启后换了数据）就只叫出窗口。
// 通知上没有带具体哪条消息（后台只说了哪个会话），所以落点是会话末尾，不是某一条。
func (s *shell) notificationClicked(id string) {
	conv, ok := attentionConversation(id)
	if !ok {
		return
	}
	s.a.post(func() {
		s.showMain()
		s.a.gotoConversation(conv)
	})
}

// gotoConversation 切到一个会话看：换到它所在的导航页（本地或 QQ），选中并打开，滚到最新一条。
// 已经选中它的话不重新订阅，只滚到底。会话不在列表里就什么也不动。
func (a *app) gotoConversation(conv string) {
	cv := a.convByID(conv)
	if cv == nil {
		return
	}
	if cv.Key.Platform == "local" {
		a.rail = 0
	} else {
		a.rail = 1
	}
	if a.workbenchEnabled() {
		a.workbench.shell.st.OpenChat() // 窄窗口时聊天区是叠在列表上的，要翻出来
	}
	if a.sel == conv {
		a.list.ScrollToEnd()
		return
	}
	a.open(conv)
}
