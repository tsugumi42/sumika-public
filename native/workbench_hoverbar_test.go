package main

import (
	"testing"

	"sumika-native/internal/api"
)

// 悬停操作条接进新界面：四种场合都有复制，回复只在本地私聊；悬停前后整行高度不变；撤回的消息没有复制。
func TestWorkbenchHoverBarScenes(t *testing.T) {
	const text = "第一句在这。第二句在那。"
	scenes := []struct {
		name  string
		key   api.ConversationKey
		group bool
		reply bool
	}{
		{"本地私聊", api.ConversationKey{Platform: "local", Channel: "console", Scene: "private"}, false, true},
		{"本地群聊", api.ConversationKey{Platform: "local", Channel: "console", Scene: "group"}, true, false},
		{"QQ 私聊", api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private"}, false, false},
		{"QQ 群", api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group"}, true, false},
	}
	for _, sc := range scenes {
		r := newWbRig(t, &fakeInputs{})
		sc.key.Conversation = r.a.sel
		r.a.keys[r.a.sel] = sc.key
		r.a.group = sc.group
		r.settle()
		row, ok := r.tt.Find("消息：m1")
		if !ok {
			t.Fatalf("%s：找不到消息行：%v", sc.name, r.tt.Texts())
		}
		body, ok := r.tt.Find(text)
		if !ok {
			t.Fatalf("%s：找不到正文：%v", sc.name, r.tt.Texts())
		}
		r.tt.Move(body.X+body.W/2, body.Y+body.H/2)
		r.settle()
		after, _ := r.tt.Find("消息：m1")
		if after.H != row.H {
			t.Fatalf("%s：悬停后行高 %v → %v", sc.name, row.H, after.H)
		}
		if _, ok := r.tt.Find("复制消息"); !ok {
			t.Fatalf("%s：没有复制：%v", sc.name, r.tt.Texts())
		}
		if _, ok := r.tt.Find("回复消息"); ok != sc.reply {
			t.Fatalf("%s：回复入口 %v，应为 %v", sc.name, ok, sc.reply)
		}
		if err := r.tt.Click("复制消息"); err != nil {
			t.Fatalf("%s：%v", sc.name, err)
		}
		if got := r.tt.Clipboard(); got != text {
			t.Fatalf("%s：复制到 %q", sc.name, got)
		}
		if sc.reply {
			r.settle()
			r.tt.Move(body.X+body.W/2, body.Y+body.H/2)
			r.settle()
			if err := r.tt.Click("回复消息"); err != nil {
				t.Fatalf("%s：%v", sc.name, err)
			}
			r.settle()
			if len(r.p.Cards) != 1 || r.p.Cards[0].Quote.MessageID != "m1" || r.p.Cards[0].Quote.Text != text {
				t.Fatalf("%s：点回复该在输入区加一张引用整条的卡：%+v", sc.name, r.p.Cards)
			}
		}
	}
	r := newWbRig(t, &fakeInputs{})
	r.a.msgs[0].recalled = true
	r.settle()
	if _, ok := r.tt.Find("复制消息"); ok {
		t.Fatalf("撤回的消息不该有复制：%v", r.tt.Texts())
	}
}
