package selfcheck_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

// B10：评估的耗时（规格第八节：单次总耗时 ≤ 50 ms，不含 S8 的一次性历史基线；查询不靠新索引）。
// 数据量是编的、按真实规模的数量级放大：model_runs 1 万行、turns 6 千行（其中 6 个没结局）、聊天库 3 万行事件（聊天库有全文检索触发器，写入慢，行数不能再多）。
// 不断言墙钟（并行跑别的测试时会抖、每日整套负载下会误红），只断言“在大表上没有变成 unknown（超时或出错）”；实测耗时写进日志，提交说明引用。
// 评估的超时本身就是断言：每条检查有 2 秒（S8 第一次数历史基线 5 秒）的上限，超了记 unknown，这里会红。
func TestEvaluationStaysUnderBudgetOnLargeTables(t *testing.T) {
	if testing.Short() {
		t.Skip("造大表要几秒，-short 时跳过；每日整套里跑")
	}
	rw, ro := traceDB(t)
	bulk(t, rw, func(tx *sql.Tx) {
		runStmt, _ := tx.Prepare(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, sections_json, injected_json, error, error_class) VALUES (?, 'c1', ?, ?, 'm', ?, '[]', '[]', ?, ?)`)
		stages := []string{"guard", "summary", "vision", "link", "compact", "expression"}
		for i := 0; i < 10000; i++ {
			var errText, class any
			if i%7 == 0 {
				errText, class = "出错了", "timeout"
			}
			runStmt.Exec(fmt.Sprintf("r%d", i), fmt.Sprintf("qq:group:%d", i%40), stages[i%len(stages)], t0.Add(-time.Duration(i)*20*time.Second).UnixMilli(), errText, class)
		}
		turnStmt, _ := tx.Prepare(`INSERT INTO turns (id, character_id, conversation, started_at, ended_at, outcome) VALUES (?, 'c1', ?, ?, ?, 'done')`)
		for i := 0; i < 6000; i++ {
			at := t0.Add(-time.Duration(i) * 30 * time.Second)
			var ended any = at.Add(time.Minute).UnixMilli()
			if i%1000 == 999 {
				ended = nil // 没结局的
			}
			turnStmt.Exec(fmt.Sprintf("t%d", i), fmt.Sprintf("qq:group:%d", i%40), at.UnixMilli(), ended)
		}
	})
	cw, cro := chatDB(t, "qq")
	bulk(t, cw, func(tx *sql.Tx) {
		st, _ := tx.Prepare(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, created_at) VALUES (?, 'qq', ?, 'user', 'u', 'message', 'x', 1)`)
		for i := 0; i < 30000; i++ {
			st.Exec(fmt.Sprintf("e%d", i), fmt.Sprintf("qq:group:%d", i%40))
		}
	})

	th := selfcheck.DefaultThresholds()
	kv := &selfcheck.KV{}
	running := func() []string { return nil }
	for _, id := range []string{"turn.stuck", "memory.compact_failed", "model.stage_failing", "events.seq_gap"} {
		c := findCheck(t, id, th)
		e := envAt(t0, selfcheck.NewCounters())
		e.Trace, e.Running, e.KV = ro, running, kv
		e.Events = func() map[string]selfcheck.Querier { return map[string]selfcheck.Querier{"qq": cro} }
		first := time.Now()
		c.Eval(context.Background(), e) // S8 的第一次是历史基线，只记不比
		firstDur := time.Since(first)
		start := time.Now()
		r := c.Eval(context.Background(), e)
		d := time.Since(start)
		t.Logf("%s：第一次 %v，之后 %v，状态 %s", id, firstDur, d, r.State)
		if r.State == selfcheck.Unknown {
			t.Errorf("%s 在大表上变成 unknown（超时或出错）：%+v", id, r)
		}
	}
}

func bulk(t *testing.T, db *sql.DB, f func(tx *sql.Tx)) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	f(tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
