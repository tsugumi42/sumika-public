package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"sumika-native/internal/store"
)

// TestRealCopyPage 对着一份只读副本把最长的会话翻完。
// 没给 SUMIKA_S7_DB 就跳过，日常短测不碰真实库。
func TestRealCopyPage(t *testing.T) {
	t.Parallel()
	path := os.Getenv("SUMIKA_S7_DB")
	if path == "" {
		t.Skip("SUMIKA_S7_DB 没设")
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenReadOnlyDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE s7_probe (n)`); err == nil {
		t.Fatal("只读副本还能建表")
	}
	var character, conversation string
	var n int
	err = db.QueryRow(`SELECT o.character_id, e.conversation, count(*)
		FROM observations o JOIN events e ON e.seq = o.event_seq
		GROUP BY o.character_id, e.conversation
		ORDER BY count(*) DESC LIMIT 1`).Scan(&character, &conversation, &n)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{Events: db}
	platform := strings.SplitN(conversation, ":", 2)[0]
	scope := store.DisplayScope{Platform: platform, Conversation: conversation, Character: character, Characters: []string{character}}
	ctx := context.Background()
	seen := map[string]bool{}
	var prevAt, prevSeq int64
	pages, rows := 0, 0
	cursor := ""
	for {
		page, err := st.ReadDisplay(ctx, scope, cursor, 200)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(page.Items) == 0 {
			t.Fatalf("第 %d 页是空的", pages)
		}
		for i, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("重复 %s", item.ID)
			}
			seen[item.ID] = true
			if i == 0 && pages > 1 && (item.CreatedAt > prevAt || (item.CreatedAt == prevAt && item.Seq >= prevSeq)) {
				t.Fatalf("翻页没有往更早走 %d %d", item.CreatedAt, item.Seq)
			}
		}
		last := page.Items[len(page.Items)-1]
		prevAt, prevSeq = last.CreatedAt, last.Seq
		rows += len(page.Items)
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			t.Fatal("还有更多，游标却没往前")
		}
		cursor = page.NextCursor
		if pages > 200 {
			t.Fatal("页数超过 200")
		}
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("副本被改了 %s %d -> %s %d", before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
	t.Logf("conversation=%s character=%s observed=%d pages=%d rows=%d", conversation, character, n, pages, rows)

}
