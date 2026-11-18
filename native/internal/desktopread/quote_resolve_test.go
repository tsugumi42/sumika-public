package desktopread

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

// ResolveQuote 的范围和可见性：只能引用同一会话里她看得到的聊天消息，几种拒绝用同一个错误。
func TestResolveQuoteScopeAndVisibility(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	st, _ := set.Platform("local")
	add := func(conv, kind, origin, actor, text string, at int64) string {
		t.Helper()
		seq, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: actor, ActorID: actor, Origin: origin, Kind: kind, Text: text, CreatedAt: at}, []string{"a", "b"})
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err := st.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	conv, otherConv := "console:a", "console:b"
	ok := add(conv, "message", "owner", "owner", "第一句。第二句。", 1)
	other := add(otherConv, "message", "owner", "owner", "别的会话里的话", 2)
	dry := add(conv, "message", "automation:dry-run", "character", "没说出口", 3)
	notice := add(conv, "notice", "owner", "owner", "通知", 4)
	empty := add(conv, "message", "owner", "owner", "", 5)
	key, otherKey := keyOf(conv), keyOf(otherConv)
	ref := func(k api.ConversationKey, id string) api.EventRef {
		return api.EventRef{Conversation: k, Event: api.EventKey{Platform: k.Platform, EventID: id}}
	}
	ctx := context.Background()

	q, err := s.ResolveQuote(ctx, key, api.LocalQuote{Ref: ref(key, ok), Text: "第二句"})
	if err != nil || q.ID != ok || q.Text != "第二句" || q.Start != 4 || q.End != 7 || q.Name != "主人" || q.At != 1 {
		t.Fatalf("正常引用：%+v %v", q, err)
	}
	for name, c := range map[string]struct {
		k   api.ConversationKey
		ref api.EventRef
	}{
		"别的会话的事件（写本会话）": {key, ref(key, other)},
		"别的会话的事件（写别处）":  {key, ref(otherKey, other)},
		"没说出口的":         {key, ref(key, dry)},
		"通知":            {key, ref(key, notice)},
		"没有文字":          {key, ref(key, empty)},
		"不存在":           {key, ref(key, "invented")},
		"空 id":          {key, ref(key, "")},
		"平台不对":          {key, api.EventRef{Conversation: key, Event: api.EventKey{Platform: "qq", EventID: ok}}},
	} {
		if _, err := s.ResolveQuote(ctx, c.k, api.LocalQuote{Ref: c.ref}); !errors.Is(err, ErrQuoteTarget) {
			t.Errorf("%s：应拒绝为 ErrQuoteTarget，得到 %v", name, err)
		}
	}
	// 会话键本身不合法，或指到观察范围外的角色会话，先被范围校验拦下
	if _, err := s.ResolveQuote(ctx, api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:nobody"}, api.LocalQuote{Ref: api.EventRef{Conversation: api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:nobody"}, Event: api.EventKey{Platform: "local", EventID: ok}}}); err == nil {
		t.Error("未知角色的会话放行了")
	}
}

// ResolveParts：块的核对和归一化。只有一个没有引用没有附件的普通块 → parts 为 nil（走旧路径）；其余按顺序保留；任何一块不合格整条拒绝并写明第几块。
func TestResolveParts(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	st, _ := set.Platform("local")
	seq, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: "console:a", ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "原文一句", CreatedAt: 1}, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := st.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&id); err != nil {
		t.Fatal(err)
	}
	key := keyOf("console:a")
	ref := api.EventRef{Conversation: key, Event: api.EventKey{Platform: key.Platform, EventID: id}}
	ctx := context.Background()

	parts, text, err := s.ResolveParts(ctx, key, []api.LocalPart{{Text: "普通话"}})
	if err != nil || parts != nil || text != "普通话" {
		t.Fatalf("单个普通块应归一成旧路径：%+v %q %v", parts, text, err)
	}
	parts, text, err = s.ResolveParts(ctx, key, []api.LocalPart{
		{Quote: &api.LocalQuote{Ref: ref, Text: "一句"}, Text: "回", Attachments: []api.LocalAttachment{{Kind: "image", Path: "D:/a.png"}}},
		{Text: "另说"},
	})
	if err != nil || len(parts) != 2 || text != "回\n另说" || parts[0].Quote == nil || parts[0].Quote.Text != "一句" || parts[0].Quote.Clock == "" ||
		parts[0].Attachments[0].Path != "D:/a.png" || parts[1].Quote != nil {
		t.Fatalf("两块：%+v %q %v", parts, text, err)
	}
	if parts, _, err = s.ResolveParts(ctx, key, []api.LocalPart{{Text: "图", Attachments: []api.LocalAttachment{{Kind: "image", Path: "D:/a.png"}}}}); err != nil || len(parts) != 1 {
		t.Fatalf("单个普通块带附件要保留：%+v %v", parts, err)
	}
	for name, in := range map[string][]api.LocalPart{
		"空块":        {{Text: "好"}, {}},
		"引用目标不存在":   {{Text: "好"}, {Quote: &api.LocalQuote{Ref: api.EventRef{Conversation: key, Event: api.EventKey{Platform: key.Platform, EventID: "x"}}}, Text: "坏"}},
		"附件种类不对":    {{Text: "好"}, {Text: "坏", Attachments: []api.LocalAttachment{{Kind: "audio", Path: "D:/a"}}}},
		"附件没有来源":    {{Text: "好"}, {Text: "坏", Attachments: []api.LocalAttachment{{Kind: "file"}}}},
		"文件不许给媒体凭据": {{Text: "好"}, {Text: "坏", Attachments: []api.LocalAttachment{{Kind: "file", Media: &api.MediaRef{Source: ref, Hash: "a"}}}}},
	} {
		if _, _, err := s.ResolveParts(ctx, key, in); err == nil || !strings.Contains(err.Error(), "第 2 块") {
			t.Errorf("%s：应拒绝并写明第 2 块，得到 %v", name, err)
		}
	}
	if _, _, err := s.ResolveParts(ctx, key, make([]api.LocalPart, MaxParts+1)); !errors.Is(err, ErrPart) {
		t.Errorf("块太多：%v", err)
	}
}
