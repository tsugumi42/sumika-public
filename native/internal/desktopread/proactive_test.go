package desktopread

import (
	"context"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

// 主动联系的角标（规格-主动联系 第十节第 1 条）：本地私聊里她主动发的话带标记，别的话、别的发言人、QQ 里的都不带。名字、话都是编的。
func TestProactiveMarkProjectedOnlyForCharacterOwnLocalMessages(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	local, _ := set.Platform("local")
	conv := "console:a"
	mark := map[string]any{"proactive": map[string]any{"thread": "th1", "reason": "想接着问昨天的事"}}
	add := func(actor, id, origin, text string, seg any) {
		t.Helper()
		if _, err := local.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, Kind: "message", ActorKind: actor, ActorID: id, Origin: origin, Text: text, Segments: seg, CreatedAt: 1000}, []string{"a"}); err != nil {
			t.Fatal(err)
		}
	}
	add("character", "a", "character", "主动的话", mark)
	add("character", "a", "character", "普通的话", nil)
	add("owner", "owner", "owner", "主人伪造的标记", mark) // 别的发言人的 segments 里带这个键也不算
	add("character", "a", "automation:link", "自动发的带标记", mark)
	res, err := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Character: "a", Limit: 50}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*api.ProactiveMark{}
	for _, m := range res.Value.Items {
		got[m.Text] = m.Proactive
	}
	if p := got["主动的话"]; p == nil || p.Reason != "想接着问昨天的事" || p.Thread != "th1" {
		t.Fatalf("她主动发的话要带标记：%+v", p)
	}
	for _, text := range []string{"普通的话", "主人伪造的标记", "自动发的带标记"} {
		if got[text] != nil {
			t.Errorf("%q 不该带主动联系标记：%+v", text, got[text])
		}
	}
	// QQ 平台的事件即使带这个键也不显示
	qq, _ := set.Platform("qq")
	if _, err := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: "qq:group:90002", Kind: "message", ActorKind: "character", ActorID: "a", Origin: "character", Text: "QQ 里的话", Segments: mark, CreatedAt: 2000}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	res, err = s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf("qq:group:90002"), Character: "a", Limit: 50}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Value.Items) != 1 {
		t.Fatalf("QQ 那条应当读得到：%d", len(res.Value.Items))
	}
	for _, m := range res.Value.Items {
		if m.Proactive != nil {
			t.Fatalf("QQ 的消息不显示主动联系标记：%+v", m)
		}
	}
}
