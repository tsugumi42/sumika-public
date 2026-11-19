package desktopread

import (
	"context"
	"testing"

	"sumika-native/internal/api"
)

// 一页最老一条再往前的那条消息带在页上（界面靠它判断最老那一行是不是一组的第一条）：有更早的消息就给，到头就不给
func TestPageCarriesMessageBefore(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	first := add(t, qq, conv, "message", "human", 100, []string{"a"})
	add(t, qq, conv, "message", "human", 110, []string{"a"})
	add(t, qq, conv, "message", "human", 120, []string{"a"})
	p, err := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 2}, "")
	if err != nil {
		t.Fatal(err)
	}
	b := p.Value.Before
	if len(p.Value.Items) != 2 || !p.Value.HasMore || b == nil || b.Ref.Event.EventID != first || b.CreatedAt != 100 || b.Kind != "message" {
		t.Fatalf("应带最早那条作前一条：%d 条 hasMore=%v before=%+v", len(p.Value.Items), p.Value.HasMore, b)
	}
	all, err := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 3}, "")
	if err != nil {
		t.Fatal(err)
	}
	if all.Value.HasMore || all.Value.Before != nil {
		t.Fatalf("到头了不该有前一条：%+v", all.Value.Before)
	}
}
