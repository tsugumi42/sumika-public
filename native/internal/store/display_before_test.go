package store

// DisplayPage.Before（这一页最老一条再往前的那条消息）的测试：跳过通知、守观察范围和会话、同一毫秒按 seq、到头为空。数据全是编的。

import (
	"context"
	"testing"
)

func TestDisplayPageBefore(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	qq, _ := set.Platform("qq")
	ctx := context.Background()
	conv := "qq:group:90002"
	add := func(c, kind string, at int64, obs []string, extra map[string]any) string {
		t.Helper()
		ev := NewEvent{Channel: "qq", Conversation: c, ActorKind: "human", ActorID: "90001", Kind: kind, Text: kind, CreatedAt: at}
		if extra != nil {
			ev.Segments = extra
		}
		seq, err := qq.AppendEvent(ev, obs)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err := qq.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	m1 := add(conv, "message", 100, []string{"a"}, nil)
	hidden := add(conv, "message", 105, []string{"b"}, nil) // 只有 b 观察到，a 的范围里看不见
	m2 := add(conv, "message", 110, []string{"a"}, nil)
	add(conv, "notice", 115, []string{"a"}, map[string]any{"notice": "poke"}) // 通知不算前一行
	m3 := add(conv, "message", 120, []string{"a"}, nil)
	add("qq:group:90009", "message", 125, []string{"a"}, nil) // 别的会话的不算
	m4 := add(conv, "message", 130, []string{"a"}, nil)
	m5 := add(conv, "message", 130, []string{"a"}, nil) // 和 m4 同一毫秒，seq 更大
	scope := DisplayScope{Platform: "qq", Conversation: conv, Characters: []string{"a"}}

	read := func(scope DisplayScope, cursor string, limit int) DisplayPage {
		t.Helper()
		p, err := qq.ReadDisplay(ctx, scope, cursor, limit)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	idOf := func(r *DisplayRow) string {
		if r == nil {
			return ""
		}
		return r.ID
	}
	// 每页 2 条（时间倒序）：[m5 m4]、[m3 通知]、[m2 m1]；最老一条再往前的那条消息依次是 m3、m2（跳过通知）、空（hidden 在 a 的范围外）
	p1 := read(scope, "", 2)
	if got := idOf(p1.Before); got != m3 || !p1.HasMore {
		t.Fatalf("第一页的前一条应是 m3(%s)：%s hasMore=%v", m3, got, p1.HasMore)
	}
	p2 := read(scope, p1.NextCursor, 2)
	if got := idOf(p2.Before); got != m2 {
		t.Fatalf("第二页最老是通知，前一条消息应是 m2(%s)：%s", m2, got)
	}
	p3 := read(scope, p2.NextCursor, 2)
	if p3.HasMore || p3.Before != nil {
		t.Fatalf("第三页到头了，不该有前一条：hasMore=%v before=%v", p3.HasMore, idOf(p3.Before))
	}
	// 同一毫秒：每页 1 条，m5 的前一条是 m4（同一毫秒按 seq 排在前面的）
	one := read(scope, "", 1)
	if one.Items[0].ID != m5 || idOf(one.Before) != m4 {
		t.Fatalf("同一毫秒按 seq：本页 %s 前一条 %s，应是 %s 前一条 %s", one.Items[0].ID, idOf(one.Before), m5, m4)
	}
	// hidden 在 a 的范围里不存在：a 的最老一页 [m1] 前面不会冒出它
	for cur, n := "", 0; ; n++ {
		p := read(scope, cur, 1)
		if idOf(p.Before) == hidden {
			t.Fatalf("a 的范围看不到 hidden，却作为前一条给了出来")
		}
		if !p.HasMore || n > 20 {
			break
		}
		cur = p.NextCursor
	}
	// 范围换成 b：看得见 hidden 和只有 b 观察到的，m1 不在 b 的范围
	b := scope
	b.Characters = []string{"b"}
	pb := read(b, "", 1)
	if len(pb.Items) != 1 || pb.Items[0].ID != hidden || pb.HasMore || pb.Before != nil {
		t.Fatalf("b 只看得到 hidden 一条：%+v before=%v", pb.Items, idOf(pb.Before))
	}
	_ = m1
}
