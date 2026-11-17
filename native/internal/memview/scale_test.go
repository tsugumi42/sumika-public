package memview

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/memory"
)

// 放大十倍（规格-记忆页 第十节第 9 条的查询一半）：十个角色，每个 3000 条事实（1200 条待确认、600 条已撤销）、4000 个片段、
// 3 万条证据、2 万条修订，会话几十个。量第一页、搜索、计数、详情字段、证据页、单条出处的 P50 / P95，再让订阅活一小段时间量 CPU 和内存。
// 数字写进测试输出，提交说明从输出里抄。数据全是编的；-short 跳过

func fillScale(t *testing.T, d *db, n int) {
	t.Helper()
	tx, err := d.conn.Begin()
	if err != nil {
		t.Fatal(err)
	}
	must := func(_ sql.Result, err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	base := now.UnixMilli()
	fs, _ := tx.Prepare(`INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, created_at, origin, conversation, scope, speech_act, confirmed_by) VALUES (?, ?, ?, ?, 'owner', ?, ?, ?, 'test', ?, ?, 'assert', ?)`)
	is, _ := tx.Prepare(`INSERT INTO item_scopes (ref, scope) VALUES (?, ?)`)
	kinds := []string{"fact", "promise", "preference", "boundary"}
	for i := 0; i < n; i++ {
		status, by := "active", any(nil)
		switch {
		case i%5 < 2: // 40% 待确认 = 1200 / 3000
			status = "candidate"
		case i%5 == 2: // 20% 已撤销 = 600 / 3000
			status = "revoked"
		case i%3 == 0:
			by = "owner"
		case i%3 == 1:
			by = "ai"
		}
		scope := []string{"owner", "public", fmt.Sprintf("conv:qq:group:%d", 30000+i%40), "person:qq:" + fmt.Sprint(10000+i%50)}[i%4]
		must(fs.Exec(fmt.Sprintf("s%05d", i), kinds[i%4], fmt.Sprintf("编造的记忆第 %d 条：喜欢草莓蛋糕、周末去海边、下周考试，关键词%d", i, i%97), []string{"owner", "self", fmt.Sprintf("qq:%d", 10000+i%50)}[i%3],
			fmt.Sprintf("[%d,%d,%d]", i*3+1, i*3+2, i*3+3), status, base-int64(i)*1000, fmt.Sprintf("qq:group:%d", 30000+i%40), scope, by))
		must(is.Exec("f:"+fmt.Sprintf("s%05d", i), scope))
	}
	es, _ := tx.Prepare(`INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at, scope, origin, status) VALUES (?, ?, ?, 'v', ?, ?, ?, 'owner', 't', ?)`)
	for i := 0; i < n*4/3; i++ {
		must(es.Exec(fmt.Sprintf("e%05d", i), fmt.Sprintf("qq:group:%d", 30000+i%40), fmt.Sprintf("编造的片段第 %d 段", i), i*30, i*30+29, base-int64(i)*1500, []string{"active", "revoked"}[i%8/7]))
		must(tx.Exec(`INSERT INTO item_scopes (ref, scope) VALUES (?, 'owner')`, fmt.Sprintf("e:e%05d", i)))
	}
	ev, _ := tx.Prepare(`INSERT INTO fact_evidence (fact_id, day, conversation, seq, speaker, by_subject, verdict, at) VALUES (?, ?, ?, ?, ?, ?, 'ok', ?)`)
	for i := 0; i < n*10; i++ {
		must(ev.Exec(fmt.Sprintf("s%05d", i%n), fmt.Sprintf("2026-10-%02d", 1+(i/n)%28), fmt.Sprintf("qq:group:%d", 30000+i%7), i, fmt.Sprintf("qq:%d", 10000+i%50), i%2, base))
	}
	rv, _ := tx.Prepare(`INSERT INTO fact_revisions (fact_id, op, at, by, reason, before_json, after_json) VALUES (?, ?, ?, 'summary', '攒够', '{"status":"candidate"}', '{"status":"active","confirmed_by":"ai"}')`)
	for i := 0; i < n*20/3; i++ {
		must(rv.Exec(fmt.Sprintf("s%05d", i%n), []string{"add", "confirm", "scopes"}[i%3], base-int64(i)))
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func pct(ds []time.Duration, p int) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)*p/100]
}

func TestScaleTenfold(t *testing.T) {
	if testing.Short() {
		t.Skip("放大十倍的量数很慢，-short 跳过")
	}
	const chars, n = 10, 3000
	var cs []Character
	ev := &events{}
	for i := 0; i < chars; i++ {
		d := newDB(t, true)
		fillScale(t, d, n)
		d.conn.Close()
		ev.evs = append(ev.evs, memory.Event{Seq: int64(i + 1), Conversation: "qq:group:30001", Kind: "message", Text: "编的原话", Seen: true})
		cs = append(cs, Character{ID: fmt.Sprintf("c%d", i), Name: fmt.Sprintf("角色%d", i), DB: d.file, Browse: opts(), Events: ev})
	}
	s := newService(t, cs, Options{Poll: 2 * time.Second})
	bgc := context.Background()
	measure := func(name string, runs int, f func(i int) error) {
		t.Helper()
		var ds []time.Duration
		for i := 0; i < runs; i++ {
			st := time.Now()
			if err := f(i); err != nil {
				t.Fatalf("%s：%v", name, err)
			}
			ds = append(ds, time.Since(st))
		}
		t.Logf("%-28s P50 %-10v P95 %-10v 最大 %v（%d 次）", name, pct(ds, 50).Round(10*time.Microsecond), pct(ds, 95).Round(10*time.Microsecond), pct(ds, 100-1).Round(10*time.Microsecond), runs)
	}
	char := func(i int) string { return fmt.Sprintf("c%d", i%chars) }
	measure("第一页（全部事实）", 60, func(i int) error {
		r, err := s.ListMemory(bgc, api.MemoryListRequest{Character: char(i)})
		if err == nil && len(r.Value.Items) != 50 {
			err = fmt.Errorf("%d 行", len(r.Value.Items))
		}
		return err
	})
	measure("第一页（待确认）", 60, func(i int) error {
		_, err := s.ListMemory(bgc, api.MemoryListRequest{Character: char(i), Groups: []string{"candidate"}})
		return err
	})
	measure("翻到第 10 页", 40, func(i int) error {
		cursor := api.Cursor("")
		for p := 0; p < 10; p++ {
			r, err := s.ListMemory(bgc, api.MemoryListRequest{Character: char(i), Cursor: cursor})
			if err != nil {
				return err
			}
			cursor = r.Value.NextCursor
		}
		return nil
	})
	measure("搜索（命中约 31 条）", 60, func(i int) error {
		r, err := s.ListMemory(bgc, api.MemoryListRequest{Character: char(i), Query: fmt.Sprintf("关键词%d", i%97)})
		if err == nil && len(r.Value.Items) < 20 {
			err = fmt.Errorf("只命中 %d 条", len(r.Value.Items))
		}
		return err
	})
	measure("搜索（没有命中，扫全表）", 40, func(i int) error {
		_, err := s.ListMemory(bgc, api.MemoryListRequest{Character: char(i), Query: "这个词绝对没有"})
		return err
	})
	measure("计数（概览一个角色）", 40, func(i int) error {
		_, err := s.character(bgc, cs[i%chars], 0)
		return err
	})
	measure("详情字段", 60, func(i int) error {
		_, err := s.ReadMemoryDetail(bgc, char(i), fmt.Sprintf("f:s%05d", i*37%n))
		return err
	})
	measure("证据第一页（每条 10 行）", 60, func(i int) error {
		_, err := s.ReadMemoryEvidence(bgc, char(i), fmt.Sprintf("f:s%05d", i*37%n), "")
		return err
	})
	measure("修订第一页（每条约 7 行）", 60, func(i int) error {
		_, err := s.ReadMemoryRevisions(bgc, char(i), fmt.Sprintf("f:s%05d", i*37%n), "")
		return err
	})
	measure("最近变动 50 行", 40, func(i int) error {
		_, err := s.ReadMemoryRecent(bgc, char(i))
		return err
	})
	measure("单条出处原话", 60, func(i int) error {
		_, err := s.ReadMemoryQuote(bgc, char(i), api.MemorySource{Conversation: "qq:group:30001", Seq: int64(i%chars + 1)})
		return err
	})
	// 订阅活着：首份快照（十个角色全算一遍）的时间，再安静地轮询一段时间量 CPU 和内存
	st := time.Now()
	sub, err := s.WatchMemory(bgc)
	if err != nil {
		t.Fatal(err)
	}
	first := <-sub.Updates()
	t.Logf("订阅的首份快照（10 个角色的计数和下拉）：%v，%d 个角色", time.Since(st).Round(time.Millisecond), len(first.Value.Characters))
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	c0 := cpuSeconds()
	idle := 20 * time.Second
	if v := os.Getenv("MEMVIEW_IDLE_SECS"); v != "" { // 规格要量十分钟：手动量时设成 600
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			idle = time.Duration(n) * time.Second
		}
	}
	time.Sleep(idle)
	c1 := cpuSeconds()
	runtime.ReadMemStats(&m1)
	select {
	case snap := <-sub.Updates():
		t.Errorf("没有人写却来了新快照 %d", snap.Version)
	default:
	}
	t.Logf("订阅活着 %v（轮询 2 秒一次，10 个库）：CPU %.3f 秒（%.2f%% 单核），堆 %.1f MB → %.1f MB",
		idle, c1-c0, (c1-c0)/idle.Seconds()*100, float64(m0.HeapAlloc)/(1<<20), float64(m1.HeapAlloc)/(1<<20))
	sub.Close()
}
