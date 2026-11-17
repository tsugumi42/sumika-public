package desktopread

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 文件根本不存在时仍能取得尺寸，证明读取转发没有同步探测文件头。
func TestForwardDisplayMetadata(t *testing.T) {
	t.Parallel()
	s, set, _ := mediaFixture(t)
	st, _ := set.Platform("qq")
	ctx := context.Background()
	for _, conv := range []string{"qq:group:90002", "qq:private:90000:90001"} {
		t.Run(conv, func(t *testing.T) {
			hash := strings.Repeat("b", 64)
			tree := types.ForwardTree{ID: conv, Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{
				{Kind: "text", Text: "编的"}, {Kind: "image", Hash: hash}, {Kind: "emoji", Hash: hash}, {Kind: "card", Hash: hash}, {Kind: "image", Hash: hash}, {Kind: "image"},
			}}}}
			if err := st.PutForward(ctx, tree, 1000, conv); err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 5; i++ {
				m := store.MediaFetch{Idx: i, Kind: tree.Nodes[0].Segs[i].Kind, State: store.MediaDone, Hash: hash, Width: 30, Height: 80, Animated: i == 2}
				if i == 3 {
					m.Width, m.Height = 80, 30
				}
				if i == 4 {
					m.Hash = strings.Repeat("c", 64)
				}
				if i == 5 {
					m.State, m.Hash, m.Width, m.Height = store.MediaFailed, "", 0, 0
				}
				if err := st.PutForwardMedia(tree.ID, 0, conv, m); err != nil {
					t.Fatal(err)
				}
			}
			id := addSegs(t, st, "qq", conv, map[string]any{"segs": []types.Seg{{Kind: "forward", Ref: tree.ID}}}, "", []string{"a"})
			req := api.ForwardRequest{Source: mediaRef(conv, id, hash).Source, ID: tree.ID}
			got, err := s.ReadForward(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			segs := got.Value.Nodes[0].Segments
			for i := 1; i <= 3; i++ {
				w, h := 30, 80
				if i == 3 {
					w, h = 80, 30
				}
				if d := segs[i]; d.Width != w || d.Height != h || d.Animated != (i == 2) || d.MediaState != store.MediaDone || len(d.Media.Forward) != 1 {
					t.Fatalf("段 %d：%+v", i, d)
				}
			}
			if segs[4].Width != 0 || segs[5].Width != 0 || segs[5].MediaState != store.MediaFailed {
				t.Fatalf("错 hash 或失败图带尺寸：%+v", segs)
			}
			// archive 停在 0004 时仍能读下载状态，尺寸为未知。
			for _, col := range []string{"width", "height", "animated"} {
				if _, err := st.Events.Exec(`ALTER TABLE forward_media_fetch DROP COLUMN ` + col); err != nil {
					t.Fatal(err)
				}
			}
			old, err := s.ReadForward(ctx, req)
			if err != nil || old.Value.Nodes[0].Segments[1].Width != 0 || old.Value.Nodes[0].Segments[1].MediaState != store.MediaDone {
				t.Fatalf("旧 archive：%+v %v", old, err)
			}
			for _, col := range []string{"width", "height", "animated"} {
				if _, err := st.Events.Exec(`ALTER TABLE forward_media_fetch ADD COLUMN ` + col + ` INTEGER NOT NULL DEFAULT 0`); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
