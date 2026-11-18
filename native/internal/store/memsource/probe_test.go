package memsource

// 真实库探针（平时跳过）：Bun 格式的行 Go 没有写入代码，造的数据只能证明"照我理解的格式读得出"，合并前要在真实库上量一次。
// MEMSOURCE_PROBE_SRC 给真实 events.db 的路径：只读打开、VACUUM INTO 拷一份到临时目录，在副本上跑，原库不动。
// 只打印计数和耗时，不打印原文和会话 id（会话 id 里有群号）

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pressly/goose/v3"
	"sumika-native/internal/memory"
)

func TestProbeRealCopy(t *testing.T) {
	src := os.Getenv("MEMSOURCE_PROBE_SRC")
	if src == "" {
		t.Skip("没给 MEMSOURCE_PROBE_SRC")
	}
	ro, err := sql.Open("sqlite", "file:"+filepath.ToSlash(src)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "events-copy.db")
	if _, err := ro.Exec(`VACUUM INTO ?`, cp); err != nil {
		t.Fatal(err)
	}
	ro.Close()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(cp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Find 现在要求全文索引。只给副本加派生索引，不升级原 Bun 库或它的其他表。
	ddl, err := os.ReadFile("../migrations/events/0005_raw_search.sql")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db,
		fstest.MapFS{"0005_raw_search.sql": &fstest.MapFile{Data: ddl}},
		goose.WithTableName("raw_search_probe_version"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var chars []string
	rs, err := db.Query(`SELECT DISTINCT character_id FROM observations`)
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		var c string
		rs.Scan(&c)
		chars = append(chars, c)
	}
	rs.Close()
	for _, ch := range chars {
		s, err := newSource(db, ch)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		heads, err := s.Conversations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("角色 %s：会话 %d 个（列会话 %v），库里有发送计划的表：%v", ch, len(heads), time.Since(start).Round(time.Millisecond), s.plans)
		for i, h := range heads {
			if i >= 8 {
				break
			}
			kind := "其他"
			switch {
			case strings.Contains(h.Conversation, ":group:"):
				kind = "群"
			case strings.Contains(h.Conversation, ":private:"):
				kind = "私聊"
			case strings.HasPrefix(h.Conversation, "console:"):
				kind = "主窗口"
			}
			start := time.Now()
			evs, err := s.Events(ctx, h.Conversation, max(0, h.LastSeq-2000), 200)
			if err != nil {
				t.Fatal(err)
			}
			took := time.Since(start)
			c := map[string]int{}
			for _, e := range evs {
				c["条"]++
				if e.Kind == "message" {
					c["消息"]++
				}
				if e.ActorKind == "human" {
					c["人"]++
					if e.ActorName != "" {
						c["有名字"]++
					}
				}
				if e.Seen {
					c["看见"]++
				}
				if e.Quote != nil {
					c["引用"]++
					if e.Quote.ActorKind == "human" {
						c["引用的是人"]++
						if e.Quote.ActorName != "" {
							c["引用有名字"]++
						}
					}
				}
				for _, im := range e.Images {
					c["图"]++
					if im.Ready {
						c["图就绪"]++
					}
					if im.Brief != "" {
						c["图有描述"]++
					}
				}
				for k, v := range map[string]bool{"撤回": e.Revoked, "没发出去": e.Unsent, "补录": e.Backfill, "自检": e.Selftest} {
					if v {
						c[k]++
					}
				}
				if n := strings.Count(e.Text, "[图片]") + strings.Count(e.Text, "[表情包]"); n != len(e.Images) && e.Kind == "message" {
					c["占位和图数对不上"]++
				}
			}
			_, said, err := s.LastSaid(ctx, h.Conversation)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("  第 %d 个（%s）：%s；最近一句人说的话：%v；读 200 条 %v", i+1, kind, fmtCounts(c), said, took.Round(time.Millisecond))
		}
		start = time.Now()
		hits, err := s.Find(ctx, "", []string{"%的%"}, 10)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("  Find（所有会话、含\"的\"）：%d 条，%v", len(hits), time.Since(start).Round(time.Millisecond))
		// 按标记抽查：Bun 的写法在最近这几百条里不一定有，直接挑带这些标记的行，看认出来的对不对
		for _, c := range []struct {
			what, where string
			got         func(e eventFlags) bool
			want        func(seg string) bool
		}{
			{"没发出去", `e.segments_json LIKE '%"unsent"%'`, func(e eventFlags) bool { return e.unsent }, func(seg string) bool { return !strings.Contains(seg, `"partial":true`) }},
			{"补录", `e.segments_json LIKE '%"backfill":true%'`, func(e eventFlags) bool { return e.backfill }, func(string) bool { return true }},
			{"自检", `e.segments_json LIKE '%"selftest":true%'`, func(e eventFlags) bool { return e.selftest }, func(string) bool { return true }},
			{"被撤回", `EXISTS (SELECT 1 FROM events r WHERE r.reply_to = e.id AND r.kind = 'notice' AND r.segments_json LIKE '%_recall"%')`, func(e eventFlags) bool { return e.revoked }, func(string) bool { return true }},
		} {
			rs, err := db.Query(`SELECT e.seq, e.conversation, COALESCE(e.segments_json, '') FROM events e JOIN observations o ON o.event_seq = e.seq AND o.character_id = ? WHERE `+c.where+` ORDER BY e.seq DESC LIMIT 30`, ch)
			if err != nil {
				t.Fatal(err)
			}
			type pick struct {
				seq       int64
				conv, seg string
			}
			var picks []pick
			for rs.Next() {
				var p pick
				rs.Scan(&p.seq, &p.conv, &p.seg)
				picks = append(picks, p)
			}
			rs.Close()
			agree := 0
			for _, p := range picks {
				evs, err := s.Events(ctx, p.conv, p.seq-1, 1)
				if err != nil {
					t.Fatal(err)
				}
				if len(evs) == 1 && evs[0].Seq == p.seq && c.got(flagsOf(evs[0])) == c.want(p.seg) {
					agree++
				}
			}
			t.Logf("  抽查%s：%d 行，认得对 %d 行", c.what, len(picks), agree)
		}
	}
}

type eventFlags struct{ unsent, backfill, selftest, revoked bool }

func flagsOf(e memory.Event) eventFlags {
	return eventFlags{e.Unsent, e.Backfill, e.Selftest, e.Revoked}
}

func fmtCounts(c map[string]int) string {
	var out []string
	for _, k := range []string{"条", "消息", "人", "有名字", "看见", "引用", "引用的是人", "引用有名字", "图", "图就绪", "图有描述", "撤回", "没发出去", "补录", "自检", "占位和图数对不上"} {
		out = append(out, fmt.Sprintf("%s %d", k, c[k]))
	}
	return strings.Join(out, "、")
}
