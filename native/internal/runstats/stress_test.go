package runstats

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

// 验收③：把 model_runs 灌到 10 万、50 万行，量用量查询耗时（预算 10 万行 150 毫秒、50 万行 500 毫秒）。
// 平时跳过：要灌几十秒、占几百 MB。手动跑：
//
//	SUMIKA_RUNSTATS_STRESS=100000,500000 go test -count=1 -run TestUsageQueryBudget -v ./internal/runstats
//
// 行的宽度照真实库：sections_json、injected_json、context_json 三列大文本各几百字节，统计查询不碰它们。
// 范围分两种量：默认最近 7 天（走索引的窄范围）和全部（最坏）。
func TestUsageQueryBudget(t *testing.T) {
	spec := os.Getenv("SUMIKA_RUNSTATS_STRESS")
	if spec == "" || testing.Short() {
		t.Skip("设 SUMIKA_RUNSTATS_STRESS=100000,500000 才跑（灌大量行，慢）")
	}
	for _, f := range strings.Split(spec, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n <= 0 {
			t.Fatalf("SUMIKA_RUNSTATS_STRESS 写错了：%q", f)
		}
		t.Run(fmt.Sprintf("%d行", n), func(t *testing.T) { budgetCase(t, n) })
	}
}

func budgetCase(t *testing.T, n int) {
	db, file := tempTrace(t)
	syncRollup(t, db) // 和桌面程序启动时一样先建汇总表：灌的时候由触发器累加，灌的时间里就含了写入开销
	fill(t, db, n)
	svc := openService(t, file)
	t0 := time.Now()
	raw := reportVia(t, db, usageSQL, off8, dayMS, 0, int64(math.MaxInt64))
	t.Logf("%d 行，对照：不用汇总表、直接查原始行的全部范围 %v", n, time.Since(t0).Round(time.Millisecond))
	budget := 150 * time.Millisecond
	if n > 100000 {
		budget = 500 * time.Millisecond
	}
	for _, c := range []struct {
		name string
		args ctlproto.StatsArgs
	}{{"最近7天", ctlproto.StatsArgs{}}, {"最近30天", ctlproto.StatsArgs{Days: 30}}, {"全部", ctlproto.StatsArgs{All: true}}} {
		rng, err := svc.RangeOf(c.args)
		if err != nil {
			t.Fatal(err)
		}
		var best time.Duration
		var runs int64
		for i := 0; i < 5; i++ {
			start := time.Now()
			rep, err := svc.Usage(context.Background(), rng)
			d := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			runs = rep.Totals.Runs
			if i == 0 || d < best {
				best = d
			}
		}
		if c.args.All && rep0(t, svc, rng).Totals != raw.Totals {
			t.Error("汇总表和原始行的总计对不上")
		}
		t.Logf("%d 行，%s：命中 %d 行，最快 %v（预算 %v）", n, c.name, runs, best.Round(time.Millisecond), budget)
		if best > budget {
			t.Errorf("%d 行 %s 用了 %v，超过预算 %v：要做 usage_rollup（规格第二节第 5 条）", n, c.name, best, budget)
		}
	}
}

func rep0(t *testing.T, svc *Service, rng ctlproto.UsageRange) ctlproto.UsageReport {
	rep, err := svc.Usage(context.Background(), rng)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != SourceRollup {
		t.Errorf("压测应走汇总表，来源是 %q", rep.Source)
	}
	return rep
}

// fill 一次事务灌 n 行，时间均匀铺在 2026-11-09 之前的 400 天里，purpose、stage、model 按真实比例混
func fill(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	big := func(k int) string { return `[{"id":"` + strings.Repeat("x", k) + `"}]` }
	sections, injected, context := big(380), big(90), big(300)
	stages := [][2]string{{"foreground", "reply"}, {"", "vision"}, {"", "reply"}, {"memory", "memory"}, {"", "summarize"}, {"guard", "guard"}, {"compact", "compact"}, {"", "learn-expression"}}
	models := []string{"auto/deepseek-v4.1-flash", "auto/gpt-6-luna", "auto/claude-haiku-5-5", "agnes-ai/agnes-2.5-flash"}
	end := time.Date(2026, 11, 9, 0, 0, 0, 0, zone).UnixMilli()
	span := int64(400 * 24 * 3600 * 1000)
	rng := rand.New(rand.NewPCG(1, 2))
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, total_ms, input_tokens, output_tokens, cache_read_tokens,
 cache_write_tokens, reasoning_tokens, sections_json, injected_json, context_json, error, purpose, usage_seen, incomplete)
 VALUES (?, 'c', 'qq:group:1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < n; i++ {
		sg := stages[rng.IntN(len(stages))]
		var purpose, e, seen, inc any
		if sg[0] != "" {
			purpose, seen, inc = sg[0], 1, 0
		}
		if rng.IntN(20) == 0 {
			e = "Request aborted"
		}
		at := end - 1 - rng.Int64N(span)
		if _, err := stmt.Exec(fmt.Sprintf("s%09d", i), sg[1], models[rng.IntN(len(models))], at, 500+rng.IntN(20000),
			rng.IntN(3000), rng.IntN(800), rng.IntN(9000), rng.IntN(100), rng.IntN(500), sections, injected, context, e, purpose, seen, inc); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Logf("灌了 %d 行，用时 %v", n, time.Since(start).Round(time.Second))
}
