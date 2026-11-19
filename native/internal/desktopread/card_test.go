package desktopread

import (
	"bytes"
	"context"
	"sumika-native/internal/api"
	"testing"
)

func TestReadCardAndAuthorizeCover(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	hash := putMedia(t, root, "qq", pngBytes)
	id := addSegs(t, qq, "qq", conv, map[string]any{"segs": []any{map[string]any{"kind": "card", "card_title": "编的标题", "card_description": "编的简介", "card_source": "编的来源", "card_url": "https://example.com/page", "hash": hash}}}, "", []string{"a"})
	page, err := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 10}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Value.Items {
		if m.Ref.Event.EventID != id {
			continue
		}
		d := m.Segments[0]
		if d.Card == nil || d.Card.Title != "编的标题" || d.Card.Description != "编的简介" || d.Card.Source != "编的来源" || d.Card.URL != "https://example.com/page" || d.Media == nil || d.Media.Hash != hash {
			t.Fatalf("卡片投影：%+v", d)
		}
		cover, err := s.ReadMedia(context.Background(), *d.Media, 128)
		if err != nil || !bytes.Equal(cover.Value.Bytes, pngBytes) {
			t.Fatalf("封面读取：%v", err)
		}
		return
	}
	t.Fatal("未读到卡片")
}
