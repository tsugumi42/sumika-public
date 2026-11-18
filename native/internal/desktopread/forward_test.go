package desktopread

import (
	"bytes"
	"context"
	"errors"
	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"testing"
)

func TestForwardReadAndMediaAuthorization(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	st, _ := set.Platform("qq")
	ctx := context.Background()
	conv := "qq:group:90002"
	hash := putMedia(t, root, "qq", pngBytes)
	inner := types.ForwardTree{ID: "inner", Nodes: []types.ForwardMessageNode{{UID: "200", Name: "编的乙", Time: 100, Segs: []types.Seg{{Kind: "image", Hash: hash}, {Kind: "text", Text: "编的里层"}}}}}
	outer := types.ForwardTree{ID: "outer", Nodes: []types.ForwardMessageNode{{UID: "100", Name: "编的甲", Time: 100, Segs: []types.Seg{{Kind: "text", Text: "编的外层"}, {Kind: "forward", Ref: "inner"}}}}}
	for _, tree := range []types.ForwardTree{inner, outer} {
		if err := st.PutForward(ctx, tree, 1000, conv); err != nil {
			t.Fatal(err)
		}
	}
	id := addSegs(t, st, "qq", conv, map[string]any{"segs": []types.Seg{{Kind: "forward", Ref: "outer"}}}, "", []string{"a"})
	source := mediaRef(conv, id, hash).Source
	req := api.ForwardRequest{Source: source, ID: "inner", Via: []string{"outer"}}
	got, err := s.ReadForward(ctx, req)
	if err != nil || got.Value.State != "ready" || len(got.Value.Nodes) != 1 || got.Value.Nodes[0].At != 100000 || got.Value.Nodes[0].Segments[1].Text != "编的里层" {
		t.Fatalf("读树：%+v %v", got, err)
	}
	ref := got.Value.Nodes[0].Segments[0].Media
	media, err := s.ReadMedia(ctx, *ref, 128)
	if err != nil || !bytes.Equal(media.Value.Bytes, pngBytes) {
		t.Fatalf("转发图：%v", err)
	}
	for _, bad := range []api.ForwardRequest{{Source: source, ID: "inner"}, {Source: source, ID: "inner", Via: []string{"other"}}, {Source: source, ID: "outer", Via: []string{"outer"}}} {
		if _, err := s.ReadForward(ctx, bad); !errors.Is(err, ErrForwardRef) {
			t.Fatalf("越权读树：%+v %v", bad, err)
		}
	}
	badSource := source
	badSource.Conversation = keyOf("qq:group:90003")
	if _, err := s.ReadForward(ctx, api.ForwardRequest{Source: badSource, ID: "outer"}); !errors.Is(err, ErrForwardRef) {
		t.Fatalf("跨会话读树：%v", err)
	}
	forged := *ref
	forged.Forward = []string{"inner"}
	if _, err := s.ReadMedia(ctx, forged, 128); !errors.Is(err, ErrMediaRef) {
		t.Fatalf("跳过外层读图：%v", err)
	}
	page, err := s.ReadMessages(ctx, api.ConversationRequest{Key: keyOf(conv), Limit: 10}, "")
	if err != nil {
		t.Fatal(err)
	}
	brief := page.Value.Items[0].Segments[0].Forward
	if brief == nil || brief.Title != "编的甲的聊天记录" || brief.Count != 1 || brief.Lines[0] != "编的甲：编的外层[合并转发]" {
		t.Fatalf("摘要：%+v", brief)
	}
	if _, err := st.Events.Exec(`DROP TABLE forward_media_fetch`); err != nil {
		t.Fatal(err)
	}
	archived, err := s.ReadForward(ctx, req)
	if err != nil || archived.Value.Nodes[0].Segments[0].Media.Hash != hash {
		t.Fatalf("旧 archive 树和图不可读：%+v %v", archived, err)
	}
}
