package gallery

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func init() {
	Register(Page{ID: "chat/identity", Area: "chat", Title: "首字头像与发送人颜色", View: chatIdentity})
}

type chatIdentityState struct {
	resource int
	selected bool
	custom   bool
	groupTop float32
	scroll   ui.ScrollState
}

// paint/chat-identity.md: all names and media here are invented gallery data.
func chatIdentity(c *ui.Context) {
	s := ui.Local(c.Root(), "chat.identity", func() chatIdentityState { return chatIdentityState{custom: true} })
	t := theme.T(c)
	m := t.Metrics()
	statuses := [...]struct{ id, label string }{{"missing", "缺图"}, {"loading", "加载中"}, {"failed", "失败"}, {"ready", "就绪"}}
	bitmap := contentBitmap()
	ui.Column(c).Fill().MinWidth(0).Children(func() {
		ui.Row(c).FillWidth().Wrap().Gap(m.Control().Spacing()).Children(func() {
			ui.Button(c, "切换头像状态").OnClick(func() { s.resource = (s.resource + 1) % len(statuses) })
			ui.Checkbox(c, &s.selected, "选中消息")
			ui.Checkbox(c, &s.custom, "自定名字颜色")
			ui.Text(c, statuses[s.resource].label).Label("当前头像状态")
		})
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Gap(m.Control().Spacing()).Children(func() {
				message := chat.Message{Text: "头像资源状态变化，首字、可访问名字和消息布局保持独立。", SenderName: "林遥", AvatarInitials: "林", AvatarLabel: "林遥的头像", AvatarColor: t.Colors().Avatar().Peer1UserpicBackground()}
				if s.resource == 3 {
					message.Avatar = bitmap
				}
				if s.custom {
					message.SenderNameColor = t.Colors().Avatar().Peer1NameForeground()
				}
				chat.Row(c.Key("resource-preview"), message, chat.RowOptions{ReserveAvatar: true, BubbleOptions: chat.BubbleOptions{Selected: s.selected}}, nil).FillWidth().Label("状态预览消息")
				for i, status := range statuses {
					ui.Text(c.Key("status-"+status.id), status.label)
					item := chat.Message{Text: "这是一条用于比较头像的编造消息。", SenderName: "青木", AvatarInitials: "青", AvatarLabel: status.label + "示例头像"}
					if i == 3 {
						item.Avatar = bitmap
					}
					chat.Row(c.Key("resource-"+status.id), item, chat.RowOptions{ReserveAvatar: true, BubbleOptions: chat.BubbleOptions{Selected: s.selected}}, nil).FillWidth()
				}
				chat.Bubble(c.Key("private"), chat.Message{Text: "没有发送人名字的私聊不增加名字行。", SenderNameColor: t.Colors().Avatar().Peer1NameForeground()}, chat.BubbleOptions{Side: chat.Outgoing, Selected: s.selected}, nil)
				ui.Text(c, "滚动下方分组：首字头像跟随可见区底部。")
				ui.Scroll(c.Key("identity-group")).FillWidth().Height(m.Header().Height() * 4).TrackScroll(&s.scroll).Children(func() {
					ui.Column(c).FillWidth().Draw(func(_ *ui.Painter, r ui.Rect) { s.groupTop = r.Y + m.Message().MarginTop() }).Children(func() {
						for i := range 6 {
							chat.Row(c.Key(fmt.Sprintf("identity-group-%d", i)), chat.Message{Text: strings.Repeat("分组中的编造长消息。", 15), SenderName: "林遥", AvatarInitials: "林", AvatarLabel: fmt.Sprintf("分组发送人%d", i), AvatarColor: t.Colors().Avatar().Peer1UserpicBackground(), SenderNameColor: message.SenderNameColor}, chat.RowOptions{ReserveAvatar: true, AvatarGroupTop: &s.groupTop, BubbleOptions: chat.BubbleOptions{Selected: s.selected, Group: chat.Group{Previous: i > 0, Next: i < 5}}}, nil).FillWidth()
						}
					})
				}).Label("首字头像分组视口")
			})
		})
	})
}
