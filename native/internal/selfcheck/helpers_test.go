package selfcheck_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)

// findCheck 按 id 取默认阈值下的一条检查（有状态的检查要在一个测试里始终用同一个实例）
func findCheck(t *testing.T, id string, th selfcheck.Thresholds) selfcheck.Check {
	t.Helper()
	for _, c := range selfcheck.Checks(th) {
		if c.ID() == id {
			return c
		}
	}
	t.Fatalf("没有这条检查：%s", id)
	return nil
}

func envAt(now time.Time, live *selfcheck.Counters) *selfcheck.Env {
	return &selfcheck.Env{Now: now, Live: live, Day: selfcheck.DayFromLive(live), KV: &selfcheck.KV{}}
}

// traceDB 在临时目录建一个 trace 库：返回读写句柄（造数据用）和自检会拿到的只读 Querier
func traceDB(t *testing.T) (*sql.DB, selfcheck.Querier) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "trace.db")
	rw, err := store.OpenTraceFile(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rw.Close() })
	ro, err := store.OpenReadOnlyFile(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	return rw, ro
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

// run 是 model_runs 的一行里自检关心的列
type run struct {
	id, char, conv, stage string
	at                    time.Time
	err, class            string // 空 = NULL
}

func insertRun(t *testing.T, db *sql.DB, r run) {
	t.Helper()
	var e, c any
	if r.err != "" {
		e = r.err
	}
	if r.class != "" {
		c = r.class
	}
	if r.char == "" {
		r.char = "c1"
	}
	if r.conv == "" {
		r.conv = "qq:group:1"
	}
	mustExec(t, db, `INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, sections_json, injected_json, error, error_class)
		VALUES (?, ?, ?, ?, 'm', ?, '[]', '[]', ?, ?)`, r.id, r.char, r.conv, r.stage, r.at.UnixMilli(), e, c)
}

// runs 造 n 条同一阶段的 model_runs，时间从 now 往前每条隔一分钟（第 0 条最新）
func runs(t *testing.T, db *sql.DB, stage string, n int, mk func(i int) run) {
	t.Helper()
	for i := 0; i < n; i++ {
		r := mk(i)
		r.stage = stage
		r.id = fmt.Sprintf("%s-%s-%d", stage, r.conv, i)
		r.at = t0.Add(-time.Duration(i+1) * time.Minute)
		insertRun(t, db, r)
	}
}
