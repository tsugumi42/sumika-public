package desktopread

// 未读不封顶（主人 2026-11-02："实际多少就是多少"）的测试和量测。
// 大数的真实值用批量 SQL 造（一条一条 AppendEvent 太慢）；量测默认不跑，SUMIKA_SCALE=1 才跑，结果写进 docs/archive/notes/量测-未读不封顶-2026-11-02.md。

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

// bulkMessages 一条 SQL 插 n 条事件（created_at 从 from 起每条加 1），observed 的再补观察。返回下一个可用的时间
func bulkMessages(t testing.TB, st *store.Store, conv, kind, actor string, from int64, n int, observed bool) int64 {
	t.Helper()
	tx, err := st.Events.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	tag := fmt.Sprintf("%s|%s|%s|%d", conv, kind, actor, from)
	if _, err = tx.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<?)
		INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,created_at)
		SELECT ?||'#'||i,'qq',?,?,'90001',?,'编造正文',?+i FROM n`, n-1, tag, conv, actor, kind, from); err != nil {
		t.Fatal(err)
	}
	if observed {
		if _, err = tx.Exec(`INSERT INTO observations(character_id,event_seq) SELECT 'a',seq FROM events WHERE id LIKE ?||'#%'`, tag); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return from + int64(n)
}

// 真实值：几千条未读就是几千，不是 100；角色自己的话、通知、没人观察的陌生人不算
func TestUnreadBigCountsAreReal(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	if _, e := qq.Events.Exec(`INSERT INTO qq_read (conversation, read_at) VALUES (?, 1000)`, conv); e != nil {
		t.Fatal(e)
	}
	at := bulkMessages(t, qq, conv, "message", "human", 1001, 3333, true)
	at = bulkMessages(t, qq, conv, "message", "character", at, 500, true) // 她自己的话
	at = bulkMessages(t, qq, conv, "notice", "human", at, 200, true)      // 通知
	at = bulkMessages(t, qq, conv, "message", "human", at, 300, false)    // 没人观察
	bulkMessages(t, qq, conv, "message", "human", 1, 700, true)           // 读到的时间以前的老消息
	if got := unreadOf(t, s, conv); got != 3333 {
		t.Fatalf("应有 3333 条未读，实际 %d", got)
	}
	if testing.Short() {
		t.Log("五位数那一段放在每日整套：快测只数到 3333 条（-race 下再灌一万多条，服务里 5 秒的查询时限会被满载的机器吃掉）")
		return
	}
	// 数到五位数也照实
	bulkMessages(t, qq, conv, "message", "human", at, 12000, true)
	if got := unreadOf(t, s, conv); got != 3333+12000 {
		t.Fatalf("应有 %d 条未读，实际 %d", 3333+12000, got)
	}
}

// 量测：一个会话的一次计数，和整份概览（每个 QQ 会话各算一次）；对比原来封顶 100 的查询。SUMIKA_SCALE=1 才跑
func TestUnreadScaleMeasure(t *testing.T) {
	t.Parallel()
	if os.Getenv("SUMIKA_SCALE") == "" {
		t.Skip("量测：SUMIKA_SCALE=1 才跑")
	}
	const oldQuery = `SELECT COUNT(*) FROM (SELECT 1 FROM events e WHERE e.conversation=? AND e.created_at>? AND e.kind='message' AND e.actor_kind!='character' AND EXISTS (SELECT 1 FROM observations o WHERE o.event_seq = e.seq AND o.character_id IN (?,?)) LIMIT 100)`
	// 七次取中位数；reps > 1 时每次连调 reps 遍再除，免得很快的查询落在计时器的最小刻度以下（Windows 上量到过 0s）
	measure := func(reps int, f func()) time.Duration {
		var ds []time.Duration
		for range 7 {
			t0 := time.Now()
			for range reps {
				f()
			}
			ds = append(ds, time.Since(t0)/time.Duration(reps))
		}
		for i := range ds {
			for j := i + 1; j < len(ds); j++ {
				if ds[j] < ds[i] {
					ds[i], ds[j] = ds[j], ds[i]
				}
			}
		}
		return ds[len(ds)/2]
	}
	// 1. 一个会话 N 条未读，一次计数（新：不封顶；旧：封顶 100）
	for _, n := range []int{100, 1000, 10000, 20000, 100000} {
		s, set := fixture(t)
		_ = s
		qq, _ := set.Platform("qq")
		conv := "qq:group:90002"
		qq.Events.Exec(`INSERT INTO qq_read (conversation, read_at) VALUES (?, 0)`, conv)
		bulkMessages(t, qq, conv, "message", "human", 1, n, true)
		scope := store.DisplayScope{Platform: "qq", Conversation: conv, Characters: []string{"a", "b"}}
		var got int
		newT := measure(20, func() { got, _, _ = qq.ReadDisplayUnread(context.Background(), scope, nil) })
		oldT := measure(20, func() {
			var c int
			qq.Events.QueryRow(oldQuery, conv, 0, "a", "b").Scan(&c)
		})
		t.Logf("一个会话 %6d 条未读：不封顶 %v（数到 %d），原来封顶 100 %v", n, newT, got, oldT)
	}
	// 2. 整份概览：groups 个 QQ 群各 per 条未读，取一份快照要多久（每个会话各计一次数）
	for _, c := range []struct{ groups, per int }{{50, 5000}, {200, 1000}, {1000, 100}} {
		s, set := fixture(t)
		qq, _ := set.Platform("qq")
		for g := range c.groups {
			conv := fmt.Sprintf("qq:group:%d", 90000+g)
			qq.Events.Exec(`INSERT INTO qq_read (conversation, read_at) VALUES (?, 0)`, conv)
			bulkMessages(t, qq, conv, "message", "human", 1, c.per, true)
		}
		var total int
		snapshot := func() time.Duration {
			return measure(1, func() {
				sub, e := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 1000})
				if e != nil {
					t.Fatal(e)
				}
				v := next(t, sub)
				sub.Close()
				total = 0
				for _, cv := range v.Value.Conversations.Items {
					total += cv.Unread
				}
			})
		}
		d := snapshot()
		withTotal := total
		// 底噪：全部读完（未读 0）时同一份快照要多久，差值就是计数的开销
		qq.Events.Exec(`UPDATE qq_read SET read_at = 1 << 50`)
		quiet := snapshot()
		t.Logf("概览：%4d 个群各 %5d 条未读（共 %d 条），一份快照 %v；全部读完时 %v（底噪，其余是计数的开销）", c.groups, c.per, withTotal, d, quiet)
	}
}
