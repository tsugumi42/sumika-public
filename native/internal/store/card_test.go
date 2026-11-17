package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/paths"
)

// 旧库按仓库已有的第 1 版迁移建，升级后旧预览不冒充载荷，新卡片经过关闭重开仍逐字节相同。
func TestCardPayloadOldDatabase(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p.ChatDB("qq")), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := open(p.ChatDB("qq"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate(db, "migrations/chat", 1); err != nil {
		t.Fatal(err)
	}
	old := `{"segs":[{"kind":"card","raw":"编的旧预览"}]}`
	if _, err := db.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, segments_json, created_at) VALUES ('old-card', 'qq', 'qq:group:400', 'human', 'qq:200', 'message', '[卡片]', ?, 1)`, old); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	set, err := OpenGo(p, "qq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	st, _ := set.For("qq")
	payload := " \r\n{ \"meta\": {\"news\": {\"title\": \"编的标题😀\", \"url\": \"https:\\/\\/example.invalid/?a=1&b=%252F\", \"n\": 9007199254740993, \"escaped\": \"\\u4e2d\", \"literal\": \"\\\\u2028\", \"line\": \"\u2028\u2029\"}}, \"padding\": \"" + strings.Repeat("字", 250) + "\" }\n "
	want := []types.Seg{
		{Kind: types.SegAt, Target: "100"},
		{Kind: types.SegCard, CardType: "json", CardJSON: payload, Raw: "编的新预览"},
		{Kind: types.SegCard, CardType: "json", CardIssue: "card_too_large", Raw: "编的超限预览"},
		{Kind: types.SegText, Text: "卡片后面的文字"},
	}
	res, err := st.AppendInbound(NewInbound{
		Event: NewEvent{Channel: "qq", Conversation: "qq:group:400", ActorKind: "human", ActorID: "qq:200", Kind: "message", Text: "[卡片]", Segments: struct {
			Segs []types.Seg `json:"segs"`
		}{want}, CreatedAt: 2},
		PlatformID: "300",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenGo(p, "qq")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	st, _ = reopened.For("qq")
	for _, c := range []struct {
		name string
		seq  int64
		want []types.Seg
	}{
		{"旧行", 1, []types.Seg{{Kind: types.SegCard, Raw: "编的旧预览"}}},
		{"新载荷和标记", res.Seq, want},
	} {
		t.Run(c.name, func(t *testing.T) {
			row, ok, err := st.EventRow("qq:group:400", c.seq)
			if err != nil || !ok {
				t.Fatalf("重开后取不到卡片：%v，存在 %v", err, ok)
			}
			var body struct {
				Segs []types.Seg `json:"segs"`
			}
			if err := json.Unmarshal([]byte(row.Segments.String), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Segs) != len(c.want) {
				t.Fatalf("段数 %d，应该 %d", len(body.Segs), len(c.want))
			}
			for i, got := range body.Segs {
				if got != c.want[i] {
					t.Fatalf("第 %d 段往返变了：载荷 %d 字节，应该 %d；类型 %q、标记 %q、预览 %q", i, len(got.CardJSON), len(c.want[i].CardJSON), got.CardType, got.CardIssue, got.Raw)
				}
			}
		})
	}
}
