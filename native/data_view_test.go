package main

import (
	"testing"

	"sumika-native/internal/api"
)

// 发送者在哪一边只看 actor_kind / origin：名字叫"主人"的群友仍在左边，未发送写明
func TestMsgFromViewSides(t *testing.T) {
	t.Parallel()
	qq := api.ConversationKey{Platform: "qq", Scene: "group", Conversation: "qq:group:1"}
	local := api.ConversationKey{Platform: "local", Scene: "private", Conversation: "console:aoi"}
	charOf := func(id string) (character, bool) { return character{ID: id, Name: "小葵"}, id == "aoi" }
	if m := msgFromView(qq, api.MessageView{ActorKind: "human", DisplayName: "主人"}, charOf); m.mine || m.isChar || m.who != "主人" {
		t.Fatalf("群友：%+v", m)
	}
	if m := msgFromView(qq, api.MessageView{ActorKind: "character", ActorID: "aoi", Origin: "character", Unsent: true}, charOf); !m.mine || !m.isChar || m.who != "小葵" || m.unsent == "" {
		t.Fatalf("她的号：%+v", m)
	}
	if m := msgFromView(local, api.MessageView{Origin: "character"}, charOf); m.mine || m.who != "小葵" || !m.md {
		t.Fatalf("本地角色：%+v", m)
	}
	if m := msgFromView(local, api.MessageView{Origin: "owner"}, charOf); !m.mine {
		t.Fatalf("本地主人：%+v", m)
	}
}
