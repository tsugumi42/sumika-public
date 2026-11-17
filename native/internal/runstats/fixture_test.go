package runstats

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func init() { store.UseFastSyncForTests() }

// 固定的时区：UTC+8，没有夏令时。测试里的"天""周"都按它算
var zone = time.FixedZone("UTC+8", 8*3600)

// fx 是一行要灌进 model_runs 的数据。指针字段为 nil 就写 NULL，用来还原 Bun 时代行里的各种 NULL
type fx struct {
	purpose, stage, model string // purpose 为空写 NULL
	at                    int64  // 毫秒
	input, cacheRead      int64
	cacheWrite, reasoning *int64
	output                int64
	ms                    *int64
	err                   string // 空 = NULL（成功）
	seen, incomplete      *int64 // nil = NULL
}

var nextID atomic.Int64

func i64(v int64) *int64 { return &v }

func ms(y int, mo time.Month, d, h, mi, s, milli int) int64 {
	return time.Date(y, mo, d, h, mi, s, milli*1e6, zone).UnixMilli()
}

// tempTrace 在临时目录里建一个 Go 迁移过的 trace.db，返回可写连接（灌数据用）和文件路径。测试结束自动关
func tempTrace(t *testing.T) (*sql.DB, string) {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st.Trace, p.BunTraceDB()
}

const insertSQL = `INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, total_ms, input_tokens, output_tokens, cache_read_tokens,
 cache_write_tokens, reasoning_tokens, sections_json, injected_json, error, purpose, usage_seen, incomplete)
 VALUES (?, 'c', 'console:c', ?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', '[]', ?, ?, ?, ?)`

func insertRows(t *testing.T, db *sql.DB, rows []fx) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		var purpose, e any
		if r.purpose != "" {
			purpose = r.purpose
		}
		if r.err != "" {
			e = r.err
		}
		var seen, inc, msv, cw, rs any
		if r.seen != nil {
			seen = *r.seen
		}
		if r.incomplete != nil {
			inc = *r.incomplete
		}
		if r.ms != nil {
			msv = *r.ms
		}
		if r.cacheWrite != nil {
			cw = *r.cacheWrite
		}
		if r.reasoning != nil {
			rs = *r.reasoning
		}
		if _, err := stmt.Exec(fmt.Sprintf("r%09d", nextID.Add(1)), r.stage, r.model, r.at, msv, r.input, r.output, r.cacheRead, cw, rs, e, purpose, seen, inc); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// openService 只读打开同一个文件建服务，固定"现在"为 2026-11-08（周日）12:00
func openService(t *testing.T, file string) *Service {
	t.Helper()
	s, err := OpenFile(file, Options{Loc: zone, Now: func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, zone) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// syncRollup 按测试的固定时区（UTC+8）建汇总表，"现在"同 openService
func syncRollup(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := SyncRollup(context.Background(), db, zone, time.Date(2026, 11, 8, 12, 0, 0, 0, zone)); err != nil {
		t.Fatal(err)
	}
}
