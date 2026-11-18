package mediastore

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"testing"

	"sumika-native/internal/store"
)

func TestStickerHashes(t *testing.T) {
	db, err := store.OpenChatFile(filepath.Join(t.TempDir(), "qq.db"), "qq")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ins := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s：%v", q, err)
		}
	}
	const (
		hEmoji  = "aa11111111111111111111111111111111111111111111111111111111111111"
		hAvatar = "bb22222222222222222222222222222222222222222222222222222222222222"
		hBlob   = "cc33333333333333333333333333333333333333333333333333333333333333"
		hImage  = "dd44444444444444444444444444444444444444444444444444444444444444"
		hOther  = "ee55555555555555555555555555555555555555555555555555555555555555"
	)
	seg := func(seq int, body string) {
		ins(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, segments_json, created_at) VALUES (?, 'qq', 'qq:group:1', 'user', 'u', 'message', '', ?, 1)`, "e"+string(rune('a'+seq)), body)
	}
	seg(1, `{"segs":[{"kind":"emoji","hash":"`+hEmoji+`"},{"kind":"image","hash":"`+hImage+`"}]}`)
	seg(2, `{"segs":[{"kind":"image","hash":"`+hOther+`"}]}`)
	seg(3, `这不是 JSON "emoji"`)
	// 导入来的旧段：中立段没有 hash，从 import_meta 里对应的旧段补；下标对不上的不补
	const hLegacy = "ff66666666666666666666666666666666666666666666666666666666666666"
	seg(4, `{"segs":[{"kind":"emoji"}],"import_meta":{"version":1,"legacy_segments":[{"type":"image","sticker":true,"hash":"`+hLegacy+`"}]}}`)
	seg(5, `{"segs":[{"kind":"emoji"}],"import_meta":{"version":1,"legacy_segments":[]}}`)
	ins(`INSERT INTO qq_avatars (key, hash, fetched_at) VALUES ('u1', ?, 1), ('u2', NULL, 1), ('u3', '', 1)`, hAvatar)
	ins(`INSERT INTO blobs (hash, size, mime, kind, created_at, last_used_at) VALUES (?, 1, 'image/png', 'sticker', 1, 1), (?, 1, 'image/png', 'image', 1, 1)`, hBlob, hOther)
	got, err := StickerHashes(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{hEmoji, hAvatar, hBlob, hLegacy}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("想要 %v，实际 %v", want, got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("想要 %v，实际 %v", want, got)
		}
	}
}

// 没有这些表的库（别的平台、很旧的库）：当没有，不报错
func TestStickerHashesMissingTables(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	got, err := StickerHashes(context.Background(), db)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}
