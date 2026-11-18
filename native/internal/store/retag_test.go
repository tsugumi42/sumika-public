package store

import (
	"context"
	"testing"

	"sumika-native/internal/history"
)

// 待分类和 [无事] 的来源：显示、历史、她的自动发言补读都看不见；改回可见来源后立刻出现。
func TestRetagOriginHidesAndReveals(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	local, _ := set.Platform("local")
	ctx := context.Background()
	conv := "console:a"
	add := func(actor, origin, text string) int64 {
		t.Helper()
		seq, err := local.AppendEvent(NewEvent{Channel: "console", Conversation: conv, ActorKind: actor, ActorID: actor, Origin: origin, Kind: "message", Text: text, CreatedAt: 100}, []string{"a"})
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	add("owner", "owner", "普通话")
	p := add("owner", history.OriginLoopPending, "任务")
	add("character", history.OriginLoopPending, "[无事]")
	scope := DisplayScope{Platform: "local", Conversation: conv, Characters: []string{"a"}}
	texts := func() []string {
		page, err := local.ReadDisplay(ctx, scope, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range page.Items {
			out = append(out, r.Text)
		}
		return out
	}
	if got := texts(); len(got) != 1 || got[0] != "普通话" {
		t.Fatalf("待分类的行不该显示：%q", got)
	}
	snapshot, msgs, _, err := history.For(local.Events, "a", conv, 10, nil)
	if err != nil || snapshot != nil || len(msgs) != 1 || msgs[0].Text != "普通话" {
		t.Fatalf("待分类的行不该进她的历史：%+v %v", msgs, err)
	}
	// 改成永久隐藏
	n, err := local.RetagOrigin(conv, p-1, 0, history.OriginLoopPending, history.OriginLoopQuiet, "")
	if err != nil || n != 2 {
		t.Fatalf("改了 %d 行：%v", n, err)
	}
	if got := texts(); len(got) != 1 {
		t.Fatalf("[无事] 不该显示：%q", got)
	}
	if n, _ := local.RetagOrigin(conv, p-1, 0, history.OriginLoopPending, "loop", "owner"); n != 0 {
		t.Fatalf("来源已经不是待分类，不该再改：%d", n)
	}
	// 另一路：有内容，owner 改成 loop、character 改成 character
	q := add("owner", history.OriginLoopPending, "任务2")
	add("character", history.OriginLoopPending, "有内容")
	if _, err := local.RetagOrigin(conv, q-1, 0, history.OriginLoopPending, "loop", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := local.RetagOrigin(conv, q-1, 0, history.OriginLoopPending, "character", "character"); err != nil {
		t.Fatal(err)
	}
	if got := texts(); len(got) != 3 {
		t.Fatalf("有内容的该显示：%q", got)
	}
	// 会话和范围限制：别的会话的待分类不动
	add2 := func() {
		_, err := local.AppendEvent(NewEvent{Channel: "console", Conversation: "console:b", ActorKind: "owner", ActorID: "owner", Origin: history.OriginLoopPending, Kind: "message", Text: "别处", CreatedAt: 100}, []string{"b"})
		if err != nil {
			t.Fatal(err)
		}
	}
	add2()
	if n, _ := local.RetagOrigin(conv, 0, 0, history.OriginLoopPending, "loop", ""); n != 0 {
		t.Fatalf("不该动别的会话：%d", n)
	}
	if n, _ := local.RetagOrigin("", 0, 0, history.OriginLoopPending, "loop", ""); n != 1 {
		t.Fatalf("会话留空是所有会话：%d", n)
	}
}
