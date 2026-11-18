package runstats

import (
	"context"
	"database/sql"
	"math"
	"reflect"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// reportVia 用一条 SQL 直接出报告，绕开 Usage 的"选哪条路"：拿汇总表和原始行互相对拍用
func reportVia(t *testing.T, db *sql.DB, query string, args ...any) ctlproto.UsageReport {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	g := newGrouper()
	for rows.Next() {
		var purpose, stage, model string
		var day int64
		var a acc
		if err := rows.Scan(&purpose, &stage, &model, &day, &a.runs, &a.failed, &a.noUsage, &a.uncached, &a.cacheRead, &a.cacheWrite, &a.output, &a.reasoning, &a.okRuns, &a.okMs); err != nil {
			t.Fatal(err)
		}
		g.add(purpose, stage, model, day, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return g.report()
}

const off8 = int64(8 * 3600 * 1000)

// assertRollupMatchesRaw 汇总表整张读出来的报告必须等于原始行整张算出来的
func assertRollupMatchesRaw(t *testing.T, db *sql.DB, what string) {
	t.Helper()
	fromRollup := reportVia(t, db, rollupSQL, int64(-1)<<62, int64(1)<<62)
	fromRaw := reportVia(t, db, usageSQL, off8, dayMS, 0, int64(math.MaxInt64))
	if !reflect.DeepEqual(fromRollup, fromRaw) {
		t.Errorf("%s：汇总表和原始行对不上\n汇总表 %+v\n原始行 %+v", what, fromRollup.Totals, fromRaw.Totals)
		for i := range fromRaw.Tasks {
			if i < len(fromRollup.Tasks) && !reflect.DeepEqual(fromRollup.Tasks[i], fromRaw.Tasks[i]) {
				t.Errorf("  类别 %s：汇总表 %+v 原始行 %+v", fromRaw.Tasks[i].Key, fromRollup.Tasks[i].UsageStats, fromRaw.Tasks[i].UsageStats)
			}
		}
	}
	var n int64
	if err := db.QueryRow(`SELECT count(*) FROM usage_rollup WHERE runs <= 0`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%s：汇总表里留着 %d 行空的", what, n)
	}
}

// 触发器：插入、改（含 AddRun 的 upsert）、删，汇总表始终等于原始行
func TestRollupTriggersStayInSync(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	db := st.Trace
	syncRollup(t, db)
	insertRows(t, db, buildFixture())
	assertRollupMatchesRaw(t, db, "插入后")

	// 直接改：出错、换 stage、换时间（挪到别的天）、清掉用量
	for _, q := range []string{
		`UPDATE model_runs SET error = 'x', total_ms = 77 WHERE rowid % 5 = 0`,
		`UPDATE model_runs SET stage = 'vision', purpose = NULL WHERE rowid % 11 = 0`,
		`UPDATE model_runs SET started_at = started_at + 86400000 * 3 WHERE rowid % 13 = 0`,
		`UPDATE model_runs SET usage_seen = 0, input_tokens = 0 WHERE rowid % 17 = 0`,
		`UPDATE model_runs SET model = 'm/gamma' WHERE rowid % 19 = 0`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
		assertRollupMatchesRaw(t, db, q)
	}
	// 没碰会进汇总的列的更新（只改回合号）不该动汇总表
	if _, err := db.Exec(`UPDATE model_runs SET turn_id = 't' WHERE rowid % 2 = 0`); err != nil {
		t.Fatal(err)
	}
	assertRollupMatchesRaw(t, db, "只改 turn_id")

	// 真写入口：AddRun 先记一行、同一个 attempt 再记一次（upsert 改 stage、耗时、出错），不能记成两次
	id := kernel.Invocation{RootID: "root", RunID: "run", StepID: "step", AttemptID: "attempt-1", Kind: "foreground"}
	at := ms(2026, 11, 8, 10, 0, 0, 0)
	run := store.Run{ID: "run-1", CharacterID: "c", Conversation: "console:c", Stage: "reply", Model: "m/alpha", StartedAt: at, TotalMs: 100,
		Input: 10, Output: 5, CacheRead: 50, UsageSeen: ptr(true), Identity: id, Now: at}
	if err := st.AddRun(run); err != nil {
		t.Fatal(err)
	}
	assertRollupMatchesRaw(t, db, "AddRun 第一次")
	e := "Request aborted"
	run.Stage, run.TotalMs, run.Error = "reply-retry", 4000, &e
	if err := st.AddRun(run); err != nil {
		t.Fatal(err)
	}
	assertRollupMatchesRaw(t, db, "AddRun upsert")
	var runs int
	if err := db.QueryRow(`SELECT count(*) FROM model_runs WHERE attempt_id = 'attempt-1'`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("upsert 应只有一行：%d %v", runs, err)
	}

	for _, q := range []string{
		`DELETE FROM model_runs WHERE rowid % 3 = 0`,
		`DELETE FROM model_runs WHERE stage = 'vision'`,
		`DELETE FROM model_runs`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
		assertRollupMatchesRaw(t, db, q)
	}
	var left int
	if err := db.QueryRow(`SELECT count(*) FROM usage_rollup`).Scan(&left); err != nil || left != 0 {
		t.Errorf("model_runs 清空以后汇总表该是空的：%d %v", left, err)
	}
}

func ptr[T any](v T) *T { return &v }

// 汇总表没做过 SyncRollup（没有 meta 行）：触发器什么都不做，写入不受影响，读的人走原始行
func TestRollupInactiveWithoutMeta(t *testing.T) {
	db, file := tempTrace(t)
	insertRows(t, db, buildFixture())
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM usage_rollup`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("没有 meta 时触发器不该写汇总表：%d %v", n, err)
	}
	svc := openService(t, file)
	rng, _ := svc.RangeOf(ctlproto.StatsArgs{All: true})
	rep, err := svc.Usage(context.Background(), rng)
	if err != nil || rep.Source != SourceRaw || rep.Totals.Runs != int64(len(buildFixture())) {
		t.Errorf("应走原始行并拿到全部数据：%v %+v", err, rep.Totals)
	}
}

// 汇总表只在"偏移一致、范围正好是整天"时用；别的情形走原始行，两边结果一样
func TestRollupFallsBackToRaw(t *testing.T) {
	db, file := tempTrace(t)
	insertRows(t, db, buildFixture())
	syncRollup(t, db)

	// 时区变了（用另一个时区的服务读）：偏移对不上，走原始行
	utc, err := OpenFile(file, Options{Loc: time.UTC, Now: func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	defer utc.Close()
	rng, _ := utc.RangeOf(ctlproto.StatsArgs{Days: 3})
	if rep, err := utc.Usage(context.Background(), rng); err != nil || rep.Source != SourceRaw {
		t.Errorf("偏移不一致应走原始行：%v %q", err, rep.Source)
	}

	svc := openService(t, file)
	// 自定义的不整天范围：走原始行，数目对得上
	odd := ctlproto.UsageRange{Kind: "custom", FromMS: ms(2026, 11, 7, 6, 30, 0, 0), ToMS: ms(2026, 11, 8, 6, 30, 0, 0)}
	rep, err := svc.Usage(context.Background(), odd)
	if err != nil || rep.Source != SourceRaw {
		t.Fatalf("不整天的范围应走原始行：%v %q", err, rep.Source)
	}
	var want int64
	for _, r := range buildFixture() {
		if r.at >= odd.FromMS && r.at < odd.ToMS {
			want++
		}
	}
	if rep.Totals.Runs != want {
		t.Errorf("不整天范围得到 %d 行，预期 %d", rep.Totals.Runs, want)
	}
	// 整天范围：走汇总表
	rng, _ = svc.RangeOf(ctlproto.StatsArgs{Days: 3})
	if rep, err := svc.Usage(context.Background(), rng); err != nil || rep.Source != SourceRollup {
		t.Errorf("整天范围应走汇总表：%v %q", err, rep.Source)
	}
	// 年范围的终点是下一年零点，同样整天
	rng, _ = svc.RangeOf(ctlproto.StatsArgs{Year: 2026})
	if rep, err := svc.Usage(context.Background(), rng); err != nil || rep.Source != SourceRollup {
		t.Errorf("年范围应走汇总表：%v %q", err, rep.Source)
	}
}

// 没有汇总表的老库（只读打开、不跑迁移的那种）：直接走原始行，不报错
func TestUsageOnDatabaseWithoutRollupTables(t *testing.T) {
	db, file := tempTrace(t)
	insertRows(t, db, buildFixture())
	for _, q := range []string{`DROP TRIGGER model_runs_rollup_ai`, `DROP TRIGGER model_runs_rollup_ad`, `DROP TRIGGER model_runs_rollup_au`, `DROP TABLE usage_rollup`, `DROP TABLE usage_rollup_meta`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	svc := openService(t, file)
	rng, _ := svc.RangeOf(ctlproto.StatsArgs{All: true})
	rep, err := svc.Usage(context.Background(), rng)
	if err != nil || rep.Source != SourceRaw || rep.Totals.Runs == 0 {
		t.Errorf("老库应走原始行：%v %q %d", err, rep.Source, rep.Totals.Runs)
	}
}

// SyncRollup：偏移没变不重建；时区变了重建，结果仍和原始行一致；重复调用安全
func TestSyncRollup(t *testing.T) {
	db, _ := tempTrace(t)
	insertRows(t, db, buildFixture())
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, zone)
	if rebuilt, err := SyncRollup(context.Background(), db, zone, now); err != nil || !rebuilt {
		t.Fatalf("第一次应重建：%v %v", rebuilt, err)
	}
	assertRollupMatchesRaw(t, db, "第一次重建")
	if rebuilt, err := SyncRollup(context.Background(), db, zone, now); err != nil || rebuilt {
		t.Fatalf("偏移没变不该重建：%v %v", rebuilt, err)
	}
	// 换成 UTC：重建，天序号变了
	if rebuilt, err := SyncRollup(context.Background(), db, time.UTC, now); err != nil || !rebuilt {
		t.Fatalf("时区变了应重建：%v %v", rebuilt, err)
	}
	a := reportVia(t, db, rollupSQL, int64(-1)<<62, int64(1)<<62)
	b := reportVia(t, db, usageSQL, 0, dayMS, 0, int64(math.MaxInt64))
	if !reflect.DeepEqual(a, b) {
		t.Error("换时区重建以后汇总表和原始行对不上")
	}
	// 再换回来，也对得上
	if _, err := SyncRollup(context.Background(), db, zone, now); err != nil {
		t.Fatal(err)
	}
	assertRollupMatchesRaw(t, db, "换回来")
}
