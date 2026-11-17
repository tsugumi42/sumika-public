package main

import (
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/segview"
)

// 本地私聊里宿主写的系统行（目标、定期任务、命令）：编造的会话和文字，不碰真实数据。
func systemRowView(id, source, text string, at int64) api.MessageView {
	return api.MessageView{
		Ref:       api.EventRef{Event: api.EventKey{EventID: id}},
		ActorKind: "system", ActorID: source, Origin: "system:" + source, Kind: "notice",
		Text: text, CreatedAt: at,
	}
}

func systemRowApp() *app {
	a := shellApp()
	k := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "local:r1"}
	charOf := func(string) (character, bool) { return character{Name: "白羽"}, true }
	say := func(id, text string, at int64) *msg {
		return &msg{id: id, who: "白羽", isChar: true, actor: "r1", md: true, text: text, blocks: segview.TextBlocks(text), at: at}
	}
	cont := api.MessageView{Ref: api.EventRef{Event: api.EventKey{EventID: "g2"}}, ActorKind: "owner", ActorID: "owner", DisplayName: "目标续轮", Origin: "goal", Kind: "message", Text: "目标还没达成（理由：还差一步）。接着做", CreatedAt: 63000}
	a.msgs = []*msg{
		say("m1", "第一句", 60000),
		msgFromView(k, systemRowView("s1", "goal", "目标达成：文件已写好", 61000), charOf),
		say("m2", "第二句", 62000),
		msgFromView(k, cont, charOf),
		msgFromView(k, systemRowView("s2", "loop", "定期任务 #1 已停止：手动停止", 64000), charOf),
		msgFromView(k, systemRowView("s3", "slash", "没有这个命令", 65000), charOf),
	}
	a.list = a.newList()
	return a
}

// 系统行投影成通知条：文字、来源图标对得上；不属于任何人，前后的气泡不会隔着它连成一组。
func TestSystemRowsProjectAsServiceRows(t *testing.T) {
	t.Parallel()
	a := systemRowApp()
	want := []struct {
		idx        int
		text, icon string
	}{
		{1, "目标达成：文件已写好", "checks"},
		{3, "目标还没达成（理由：还差一步）。接着做", "checks"},
		{4, "定期任务 #1 已停止：手动停止", "refresh"},
		{5, "没有这个命令", "command"},
	}
	for _, w := range want {
		v := chatMessage(a.msgs[w.idx])
		if v.Service != w.text || v.Icon != w.icon {
			t.Errorf("第 %d 行：文字 %q 图标 %q，应为 %q %q", w.idx, v.Service, v.Icon, w.text, w.icon)
		}
	}
	// 普通消息、QQ 群通知条不变：普通消息不是通知条，群通知条没有图标
	if v := chatMessage(a.msgs[0]); v.Service != "" || v.Icon != "" {
		t.Errorf("普通消息被当成了通知条：%+v", v)
	}
	g := msgFromView(api.ConversationKey{Platform: "qq", Scene: "group", Conversation: "qq:group:90002"}, api.MessageView{Kind: "notice", Service: "虚构乙 加入了群聊", ActorKind: "human"}, func(string) (character, bool) { return character{}, false })
	if v := chatMessage(g); v.Service != "虚构乙 加入了群聊" || v.Icon != "" {
		t.Errorf("群通知条：%+v", v)
	}
	// 连着两条系统行、系统行夹在同一个角色的两句话之间，都不合组
	win := 15 * time.Minute
	for i := 1; i < len(a.msgs); i++ {
		prev, cur := chatMessage(a.msgs[i-1]), chatMessage(a.msgs[i])
		if (i == 1 || i == 2 || i == 3 || i == 4 || i == 5) && chatui.Joined(&prev, &cur, win) {
			t.Errorf("第 %d、%d 行连成了一组", i-1, i)
		}
	}
	m1, m2 := chatMessage(a.msgs[0]), chatMessage(a.msgs[2])
	if chatui.Joined(&m1, &m2, win) && chatMessage(a.msgs[1]).Sender == m1.Sender {
		t.Error("系统行的发送人不该和角色相同")
	}
}

// 画出来：居中的一行，文字前有来源小图标，没有气泡（不挂“消息：”的行），行距按组间距而不是组内。
func TestSystemRowsDrawCenteredWithIconNoBubble(t *testing.T) {
	a := systemRowApp()
	tt := shellTester(a, 2048, 1030)
	row, ok := tt.Find("通知：目标达成：文件已写好")
	if !ok {
		t.Fatalf("没有系统行：%q", tt.Texts())
	}
	text, ok := tt.Find("目标达成：文件已写好")
	if !ok {
		t.Fatal("系统行里没有文字")
	}
	if !hasKey(tt, "service-icon") {
		t.Fatal("系统行前面没有来源图标")
	}
	// 图标在文字左边、整颗胶囊（图标加文字）在消息列里居中
	icon := boxOf(t, tt, "service-icon")
	pill := boxOf(t, tt, "service-pill")
	if icon.Box.X+icon.Box.W > text.X+1 {
		t.Fatalf("图标 %+v 压到了文字 %+v", icon.Box, text)
	}
	if d := (pill.Box.X + pill.Box.W/2) - (row.X + row.W/2); d < -1 || d > 1 {
		t.Fatalf("系统行没居中：胶囊 %+v，行 %+v", pill.Box, row)
	}
	// 系统行本身不是气泡：没有以“消息：s1”为名的行
	if _, ok := tt.Find("消息：s1"); ok {
		t.Fatal("系统行被画成了消息气泡")
	}
	// 前后两句角色的话隔着系统行，按组间距分开
	first, _ := tt.Find("消息：m1")
	second, _ := tt.Find("消息：m2")
	if second.Y-first.Y-first.H < 2*a.chatTokens().GapGroups {
		t.Fatalf("前后两句不该隔着系统行连在一起：%+v %+v", first, second)
	}
	// 续轮的那句输入也是灰字系统行，不是主人的气泡
	if _, ok := tt.Find("通知：目标还没达成（理由：还差一步）。接着做"); !ok {
		t.Fatalf("目标续轮没有画成系统行：%q", tt.Texts())
	}
}
