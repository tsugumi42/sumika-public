package desktopread

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

// 库元数据覆盖新段，旧段优先；文件打开计数器先用 ReadMedia 校验，避免断言恒为零。
func TestMessageImageMetadataWithoutFileIO(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	hash := putMedia(t, root, "qq", pngBytes)
	raw := fmt.Sprintf(`{"segs":[{"kind":"text","text":"编的"},{"kind":"image","hash":%q},{"kind":"image","hash":%q},{"kind":"image","hash":%q},{"kind":"image","hash":%q}],"import_meta":{"version":1,"legacy_segments":[{"type":"text","text":"编的"},{"type":"image","hash":%q},{"type":"image","hash":%q,"width":90,"height":40,"animated":false},{"type":"image","hash":%q},{"type":"image","hash":%q}]}}`, hash, hash, hash, hash, hash, hash, hash, hash)
	// 无 legacy 的新 Go 段单独一条，另测同一个事件不同下标与 hash 对不上。
	id := addSegs(t, qq, "qq", conv, image(hash), "", []string{"a"})
	legacyID := addSegs(t, qq, "qq", conv, json.RawMessage(raw), "", []string{"a"})
	var seq, legacySeq int64
	if err := qq.Events.QueryRow(`SELECT seq FROM events WHERE id=?`, id).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err := qq.Events.QueryRow(`SELECT seq FROM events WHERE id=?`, legacyID).Scan(&legacySeq); err != nil {
		t.Fatal(err)
	}
	for _, row := range []store.MediaFetch{
		{EventSeq: seq, Idx: 0, Hash: hash, State: store.MediaDone, Width: 30, Height: 80, Animated: true},
		{EventSeq: legacySeq, Idx: 1, Hash: hash, State: store.MediaDone, Width: 30, Height: 80, Animated: true},
		{EventSeq: legacySeq, Idx: 2, Hash: hash, State: store.MediaDone, Width: 30, Height: 80, Animated: true},
		{EventSeq: legacySeq, Idx: 3, Hash: strings.Repeat("b", 64), State: store.MediaDone, Width: 60, Height: 70, Animated: true},
	} {
		if err := qq.PutMediaFetch(row); err != nil {
			t.Fatal(err)
		}
	}
	opens := 0
	s.openMedia = func(data, platform, hash string) (*os.File, error) {
		opens++
		return mediastore.OpenMedia(data, platform, hash)
	}
	if _, err := s.ReadMedia(context.Background(), mediaRef(conv, id, hash), 0); err != nil || opens != 1 {
		t.Fatalf("计数器未覆盖实际媒体打开：opens=%d err=%v", opens, err)
	}
	opens = 0
	page, err := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 200}, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range page.Value.Items {
		seen[m.Ref.Event.EventID] = true
		if m.Ref.Event.EventID == id {
			d := m.Segments[0]
			if d.Width != 30 || d.Height != 80 || !d.Animated {
				t.Fatalf("新 Go 段：%+v", d)
			}
		} else {
			if d := m.Segments[1]; d.Width != 30 || d.Height != 80 || !d.Animated {
				t.Fatalf("旧段缺字段时应取库元数据：%+v", d)
			}
			d := m.Segments[2]
			if d.Width != 90 || d.Height != 40 || d.Animated {
				t.Fatalf("旧段应优先：%+v", d)
			}
			for _, i := range []int{3, 4} {
				d := m.Segments[i]
				if d.Width != 0 || d.Height != 0 || d.Animated {
					t.Fatalf("缺元数据或 hash 不符仍应未知：%+v", d)
				}
			}
		}
	}
	if !seen[id] || !seen[legacyID] {
		t.Fatalf("缺少预期消息：%v", seen)
	}
	if opens != 0 {
		t.Fatalf("读消息打开媒体 %d 次，要 0", opens)
	}
}
