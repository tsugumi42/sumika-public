package store

import (
	"fmt"
	"testing"
	"time"
)

// 主动联系用到的库（迁移 0017、OwnerLastSpoke）。会话、号、话都是编的。

func TestProactiveTablesExistAfterMigration(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	for _, table := range []string{"proactive_state", "proactive_decisions"} {
		var n int
		if err := s.Trace.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("trace.db 里没有 %s：%d %v", table, n, err)
		}
	}
	// 列：日志要有合并计数，状态要有下次醒的时间
	for table, cols := range map[string][]string{
		"proactive_state":     {"character_id", "factor", "quiet_until", "cold_streak", "next_wake_at", "last_wake_at", "updated_at"},
		"proactive_decisions": {"id", "at", "character_id", "kind", "reason", "inputs", "next_at", "thread", "model_note", "count"},
	} {
		have := map[string]bool{}
		rows, err := s.Trace.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name string
			rows.Scan(&name)
			have[name] = true
		}
		rows.Close()
		for _, c := range cols {
			if !have[c] {
				t.Errorf("%s 缺列 %s", table, c)
			}
		}
	}
}

func TestOwnerLastSpoke(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	const conv = "console:ca"
	base := int64(1_790_000_000_000)
	add := func(kind, at string, ms int64, k string) {
		t.Helper()
		if _, err := s.AppendEvent(NewEvent{Channel: "console", Conversation: conv, ActorKind: kind, ActorID: kind, Kind: k, Text: at, CreatedAt: ms}, []string{"ca"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := s.OwnerLastSpoke(0); ok || err != nil {
		t.Fatalf("空库：%v %v", ok, err)
	}
	add("owner", "早一点", base, "message")
	add("owner", "后一点", base+60_000, "message")
	add("character", "她说的", base+120_000, "message")
	add("human", "别人说的", base+180_000, "message")
	add("owner", "通知不算", base+240_000, "notice")
	at, ok, err := s.OwnerLastSpoke(0)
	if err != nil || !ok || at != base+60_000 {
		t.Fatalf("应取主人最后一条消息：%d %v %v", at, ok, err)
	}
	// 下界：只看 since 以后的；since 等于那条的时间也不算（严格大于）
	if _, ok, _ := s.OwnerLastSpoke(base + 60_000); ok {
		t.Fatal("下界之内没有主人发言")
	}
	if at, ok, _ := s.OwnerLastSpoke(base + 59_999); !ok || at != base+60_000 {
		t.Fatalf("刚过下界：%d %v", at, ok)
	}
}

// 量一下：两万条别人的话里主人只在最近说过一句，查询耗时（只记录，不当断言，机器忙时会抖）
func TestOwnerLastSpokeTiming(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("-short 不量")
	}
	s, _ := fresh(t)
	base := time.Now().Add(-48 * time.Hour).UnixMilli()
	tx, err := s.Events.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20000; i++ {
		kind := "human"
		if i == 19990 {
			kind = "owner"
		}
		id := fmt.Sprintf("m%06d", i)
		if _, err := tx.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, created_at) VALUES (?, 'qq', 'qq:group:1', ?, 'x', 'message', 't', ?)`,
			id, kind, base+int64(i)*8); err != nil {
			t.Skipf("events 表的列和这里假设的不一样，不量了：%v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	since := base - 1
	start := time.Now()
	for i := 0; i < 20; i++ {
		if _, ok, err := s.OwnerLastSpoke(since); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	t.Logf("两万条事件、主人最后发言在倒数第 10 条：单次查询平均 %v", time.Since(start)/20)
	start = time.Now()
	for i := 0; i < 20; i++ {
		if _, ok, _ := s.OwnerLastSpoke(base + 20000*8); ok {
			t.Fatal("下界之后没有主人发言")
		}
	}
	t.Logf("主人在窗口里没说话（下界之后没有行）：单次查询平均 %v", time.Since(start)/20)
}
