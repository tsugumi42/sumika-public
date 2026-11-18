package store

import (
	"context"
	"testing"
)

// 会话列表的预览是聊天区最后一条能看到的：之后的进群、撤回这些通知不抢预览，也不把会话顶到前面。
func TestConversationPreviewSkipsHiddenNotices(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	qq, _ := set.Platform("qq")
	conv, onlyNotices := "qq:group:90002", "qq:group:90003"
	add := func(c, actorKind, kind, text string, at int64) {
		t.Helper()
		if _, err := qq.AppendEvent(NewEvent{Channel: "qq", Conversation: c, ActorKind: actorKind, ActorID: "90001", Kind: kind, Text: text, CreatedAt: at}, []string{"a"}); err != nil {
			t.Fatal(err)
		}
	}
	add(conv, "human", "message", "带图的那句话", 1000)
	add(conv, "human", "notice", "（一条没认出来的通知）", 2000)
	add(onlyNotices, "human", "notice", "（一条没认出来的通知）", 3000)
	read := func() map[string]DisplayConversation {
		t.Helper()
		page, err := set.ReadDisplayConversations(context.Background(), []string{"a"}, "", "", 100)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]DisplayConversation{}
		for _, item := range page.Items {
			out[item.Conversation] = item
		}
		return out
	}
	got := read()
	if v := got[conv]; v.Last == nil || v.Last.Text != "带图的那句话" || v.LastAt != 1000 {
		t.Fatalf("预览应是最后一条能看到的消息：%+v", v.Last)
	}
	if v, ok := got[onlyNotices]; ok && v.Last != nil {
		t.Fatalf("只有通知的会话没有预览：%+v", v.Last)
	}
	// 宿主写的系统行（命令回复、目标达成）聊天区显示，预览也算
	add(conv, "system", "notice", "目标达成", 4000)
	if v := read()[conv]; v.Last == nil || v.Last.Text != "目标达成" {
		t.Fatalf("系统行应算进预览：%+v", v.Last)
	}
	// 翻聊天记录不受影响：通知还在库里，界面自己决定显示什么
	page, err := qq.ReadDisplay(context.Background(), DisplayScope{Platform: "qq", Conversation: conv, Characters: []string{"a"}}, "", 10)
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("普通读取应还有 3 行：%d %v", len(page.Items), err)
	}
}
