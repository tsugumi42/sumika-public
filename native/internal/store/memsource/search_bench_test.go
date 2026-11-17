package memsource

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"sumika-native/internal/memory"
)

// TestRawSearchPerformance only opens a caller-supplied copy under an explicit temporary directory.
func TestRawSearchPerformance(t *testing.T) {
	input, dir := os.Getenv("RAW_SEARCH_BENCH_DB"), os.Getenv("RAW_SEARCH_BENCH_DIR")
	if input == "" && dir == "" {
		t.Skip("set RAW_SEARCH_BENCH_DB and RAW_SEARCH_BENCH_DIR")
	}
	if input == "" || dir == "" {
		t.Fatal("both RAW_SEARCH_BENCH_DB and RAW_SEARCH_BENCH_DIR are required")
	}
	temp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	temp, err = filepath.Abs(temp)
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	input, err = filepath.EvalSymlinks(input)
	if err != nil {
		t.Fatal(err)
	}
	input, err = filepath.Abs(input)
	if err != nil {
		t.Fatal(err)
	}
	inside := func(root, p string) bool {
		rel, e := filepath.Rel(root, p)
		return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	if !inside(temp, dir) || !inside(dir, input) {
		t.Fatal("benchmark directory must be under os.TempDir and the input copy must be inside it")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(input + suffix); err == nil {
			t.Fatalf("input has %s; provide a checkpointed standalone copy", suffix)
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name     string
		patterns []string
	}{
		{"one-rune", []string{"%的%"}}, {"two-rune", []string{"%什么%"}}, {"three-rune", []string{"%机器人%"}},
		{"and-model", []string{"%模型%", "%的%"}}, {"and-mixed", []string{"%AI%", "%模型%"}},
	}
	for _, factor := range []int{1, 10} {
		t.Run(fmt.Sprintf("%dx", factor), func(t *testing.T) {
			work, err := os.MkdirTemp(dir, "raw-search-")
			if err != nil {
				t.Fatal(err)
			}
			if os.Getenv("RAW_SEARCH_BENCH_KEEP") == "1" {
				t.Logf("retained benchmark work directory: %s", work)
			} else {
				defer os.RemoveAll(work)
			}
			dest := filepath.Join(work, "qq.db")
			src, err := os.Open(input)
			if err != nil {
				t.Fatal(err)
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				src.Close()
				t.Fatal(err)
			}
			_, copyErr := io.Copy(out, src)
			closeErr := out.Close()
			src.Close()
			if copyErr != nil {
				t.Fatal(copyErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			db, err := sql.Open("sqlite", dest)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			var indexed int
			if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('events_trigram','events_short')`).Scan(&indexed); err != nil {
				t.Fatal(err)
			}
			if indexed != 0 {
				t.Fatal("input must be a pre-search-migration copy for backfill measurement")
			}
			var plans int
			if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='outbound_plans'`).Scan(&plans); err != nil {
				t.Fatal(err)
			}
			var maxSeq int64
			if err = db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&maxSeq); err != nil {
				t.Fatal(err)
			}
			if maxSeq <= 0 {
				t.Fatal("empty events database")
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			for n := 1; n < factor; n++ {
				prefix := fmt.Sprintf("rawbench-%d-", n)
				offset := int64(n) * maxSeq
				_, err = tx.Exec(`INSERT INTO events(seq,id,channel,conversation,actor_kind,actor_id,kind,text,segments_json,reply_to,created_at,origin) SELECT seq+?, ?||id,channel,conversation,actor_kind,actor_id,kind,text,segments_json,CASE WHEN reply_to IS NULL THEN NULL ELSE ?||reply_to END,created_at,origin FROM events WHERE seq<=?`, offset, prefix, prefix, maxSeq)
				if err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				_, err = tx.Exec(`INSERT INTO observations(character_id,event_seq) SELECT character_id,event_seq+? FROM observations WHERE event_seq<=?`, offset, maxSeq)
				if err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				if plans != 0 {
					_, err = tx.Exec(`INSERT INTO outbound_plans(id,character_id,conversation,event_seq,account,origin,kind,reason,state,created_at,done_at) SELECT ?||id,character_id,conversation,event_seq+?,account,origin,kind,reason,state,created_at,done_at FROM outbound_plans WHERE event_seq IS NOT NULL AND event_seq<=?`, prefix, offset, maxSeq)
					if err != nil {
						tx.Rollback()
						t.Fatal(err)
					}
				}
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var events, observations int64
			if err = db.QueryRow(`SELECT count(*) FROM events`).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT count(*) FROM observations`).Scan(&observations); err != nil {
				t.Fatal(err)
			}
			var bytesBefore, bytesAfter int64
			if err = db.QueryRow(`SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&bytesBefore); err != nil {
				t.Fatal(err)
			}
			provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS(filepath.Join("..", "migrations", "chat")))
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			migrations, err := provider.Up(context.Background())
			elapsed := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("factor=%d events=%d observations=%d migration_backfill=%s migrations=%v; copied event-linked outbound plans, not unrelated link/part tables", factor, events, observations, elapsed, migrations)
			if err = db.QueryRow(`SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&bytesAfter); err != nil {
				t.Fatal(err)
			}
			t.Logf("factor=%d database_bytes_before=%d database_bytes_after=%d index_growth_bytes=%d", factor, bytesBefore, bytesAfter, bytesAfter-bytesBefore)
			var character, exclude string
			var until int64
			err = db.QueryRow(`SELECT character_id FROM observations GROUP BY character_id ORDER BY count(*) DESC LIMIT 1`).Scan(&character)
			if err != nil {
				t.Fatal(err)
			}
			err = db.QueryRow(`SELECT e.conversation FROM events e JOIN observations o ON o.event_seq=e.seq WHERE o.character_id=? ORDER BY e.created_at DESC,e.seq DESC LIMIT 1`, character).Scan(&exclude)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT MAX(created_at)+1 FROM events`).Scan(&until); err != nil {
				t.Fatal(err)
			}
			since := until - int64(6*time.Hour/time.Millisecond)
			s, err := OpenDB(db, character)
			if err != nil {
				t.Fatal(err)
			}
			// Select only from public candidate terms, so logs never expose copied message text.
			selectedCases := append([]struct {
				name     string
				patterns []string
			}(nil), cases...)
			selected := false
			for _, pair := range [][]string{{"%模型%", "%的%"}, {"%可以%", "%的%"}, {"%什么%", "%的%"}, {"%测试%", "%的%"}, {"%中文%", "%的%"}, {"%AI%", "%的%"}} {
				hits, err := oldSearch(ctx, s, findScope{excludeConversation: exclude, window: true, sinceAt: since, untilAt: until}, pair, 1)
				if err != nil {
					t.Fatal(err)
				}
				if len(hits) > 0 {
					selectedCases = append(selectedCases, struct {
						name     string
						patterns []string
					}{"and-selected", pair})
					selected = true
					t.Logf("factor=%d selected public keywords=%q since=%d until=%d", factor, pair, since, until)
					break
				}
			}
			if !selected {
				t.Fatal("no public AND keyword pair matched within the six-hour window after excluding the newest conversation")
			}
			for _, c := range selectedCases {
				for _, window := range []bool{false, true} {
					const limit = 20
					scope := findScope{window: window}
					if window {
						scope.excludeConversation = exclude
						scope.sinceAt = since
						scope.untilAt = until
					}
					query, args := s.findQuery(scope, c.patterns)
					args = append(args, int64(1)<<62, int64(1)<<62, findPage)
					oldQuery, oldArgs := oldSearchQuery(s, scope, c.patterns)
					oldArgs = append(oldArgs, int64(1)<<62, int64(1)<<62, int64(1)<<62, 50)
					for _, planCase := range []struct {
						name, query string
						args        []any
					}{{"old", oldQuery, oldArgs}, {"new", query, args}} {
						plan, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+planCase.query, planCase.args...)
						if err != nil {
							t.Fatal(err)
						}
						for plan.Next() {
							var id, parent, unused int
							var detail string
							if err = plan.Scan(&id, &parent, &unused, &detail); err != nil {
								t.Fatal(err)
							}
							t.Logf("EXPLAIN factor=%d case=%s window=%v variant=%s %d %d %d %s", factor, c.name, window, planCase.name, id, parent, unused, detail)
						}
						if err = plan.Err(); err != nil {
							plan.Close()
							t.Fatal(err)
						}
						plan.Close()
					}
					old := func() ([]memory.Event, error) { return oldSearch(ctx, s, scope, c.patterns, limit) }
					fresh := func() ([]memory.Event, error) {
						if window {
							return s.FindWindow(ctx, exclude, c.patterns, since, until, limit)
						}
						return s.Find(ctx, "", c.patterns, limit)
					}
					want, err := old()
					if err != nil {
						t.Fatal(err)
					}
					got, err := fresh()
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("old/new mismatch case=%s window=%v", c.name, window)
					}
					for _, variant := range []struct {
						name string
						fn   func() ([]memory.Event, error)
					}{{"old", old}, {"new", fresh}} {
						durations := make([]time.Duration, 50)
						for i := range durations {
							start := time.Now()
							result, err := variant.fn()
							durations[i] = time.Since(start)
							if err != nil {
								t.Fatal(err)
							}
							if !reflect.DeepEqual(result, want) {
								t.Fatalf("changed result %s", variant.name)
							}
						}
						sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
						lengths := make([]int, len(c.patterns))
						for i, p := range c.patterns {
							lengths[i] = len([]rune(p)) - 2
						}
						t.Logf("factor=%d case=%s keyword_lengths=%v variant=%s window=%v since=%d until=%d limit=%d hits=%d runs=%d p50=%s p95=%s", factor, c.name, lengths, variant.name, window, since, until, limit, len(want), len(durations), durations[24], durations[47])
					}
				}
			}
		})
	}
}

func oldSearchQuery(s *Source, scope findScope, like []string) (string, []any) {
	where := []string{"1 = 1"}
	args := []any{s.character}
	if scope.conversation != "" {
		where = append(where, "e.conversation = ?")
		args = append(args, scope.conversation)
	}
	if scope.excludeConversation != "" {
		where = append(where, "e.conversation != ?")
		args = append(args, scope.excludeConversation)
	}
	if scope.window {
		where = append(where, "e.created_at >= ?", "e.created_at < ?")
		args = append(args, scope.sinceAt, scope.untilAt)
	}
	for _, p := range like {
		where = append(where, `e.text LIKE ? ESCAPE '\'`)
		args = append(args, p)
	}
	return `SELECT ` + cols + ` FROM events e JOIN observations o ON o.event_seq=e.seq AND o.character_id=? WHERE ` + strings.Join(where, " AND ") + ` AND (e.created_at < ? OR (e.created_at = ? AND e.seq < ?)) ORDER BY e.created_at DESC,e.seq DESC LIMIT ?`, args
}

func oldSearch(ctx context.Context, s *Source, scope findScope, like []string, limit int) ([]memory.Event, error) {
	if len(like) == 0 || limit <= 0 || scope.window && scope.sinceAt >= scope.untilAt {
		return nil, nil
	}
	query, args := oldSearchQuery(s, scope, like)
	var out []memory.Event
	at, seq := int64(1)<<62, int64(1)<<62
	for len(out) < limit {
		rs, err := s.db.QueryContext(ctx, query, append(append([]any(nil), args...), at, at, seq, 50)...)
		if err != nil {
			return nil, err
		}
		rows, err := scan(rs)
		if err != nil {
			return nil, err
		}
		if err = s.fill(ctx, rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if !r.ev.Revoked && !r.ev.Unsent && len(out) < limit {
				out = append(out, r.ev)
			}
			at, seq = r.ev.At, r.ev.Seq
		}
		if len(rows) < 50 {
			break
		}
	}
	return out, nil
}
