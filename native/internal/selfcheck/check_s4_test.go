package selfcheck_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

func turn(t *testing.T, db *sql.DB, id string, started time.Time, ended bool) {
	t.Helper()
	if ended {
		mustExec(t, db, `INSERT INTO turns (id, character_id, conversation, started_at, ended_at, outcome) VALUES (?, 'c1', 'qq:group:1', ?, ?, 'done')`, id, started.UnixMilli(), started.Add(time.Minute).UnixMilli())
		return
	}
	mustExec(t, db, `INSERT INTO turns (id, character_id, conversation, started_at) VALUES (?, 'c1', 'qq:group:1', ?)`, id, started.UnixMilli())
}

func s4(t *testing.T, q selfcheck.Querier, running func() []string) selfcheck.Result {
	t.Helper()
	c := findCheck(t, "turn.stuck", selfcheck.DefaultThresholds())
	e := envAt(t0, selfcheck.NewCounters())
	e.Trace, e.Running = q, running
	return c.Eval(context.Background(), e)
}

func none() []string { return nil }

// B5：没结局、不在跑、不在等审批、超过 30 分钟的回合 → bad；放进 Running → 不报
func TestS4StuckTurn(t *testing.T) {
	rw, ro := traceDB(t)
	turn(t, rw, "t-stuck", t0.Add(-31*time.Minute), false)
	r := s4(t, ro, none)
	if r.State != selfcheck.Bad || r.Value != 1 || r.Example["turn"] != "t-stuck" {
		t.Fatalf("超过 30 分钟没结局应 bad：%+v", r)
	}
	if r := s4(t, ro, func() []string { return []string{"t-stuck"} }); r.State != selfcheck.Ok {
		t.Fatalf("还在进程内跑着的不算卡住（长工具调用）：%+v", r)
	}
}

func TestS4DoesNotCountRecentEndedOrWaiting(t *testing.T) {
	rw, ro := traceDB(t)
	turn(t, rw, "t-recent", t0.Add(-10*time.Minute), false) // 才 10 分钟
	turn(t, rw, "t-done", t0.Add(-3*time.Hour), true)       // 有结局
	turn(t, rw, "t-wait", t0.Add(-2*time.Hour), false)      // 停在审批卡上
	mustExec(t, rw, `INSERT INTO steps (id, turn_id, kind, name, status) VALUES ('s1', 't-wait', 'tool', 'bash', 'waiting')`)
	if r := s4(t, ro, none); r.State != selfcheck.Ok {
		t.Fatalf("这三个都不是卡住：%+v", r)
	}
	// 等审批的步骤结束了（running 的工具不算在等审批）：同一个回合再没结局就是卡住
	mustExec(t, rw, `UPDATE steps SET status = 'running' WHERE id = 's1'`)
	if r := s4(t, ro, none); r.State != selfcheck.Bad || r.Example["turn"] != "t-wait" {
		t.Fatalf("不在等审批以后该算卡住：%+v", r)
	}
}

// 不知道进程内在跑什么就不能判：unknown，不是 ok；没有库也是
func TestS4UnknownWithoutRunningOrTrace(t *testing.T) {
	_, ro := traceDB(t)
	if r := s4(t, ro, nil); r.State != selfcheck.Unknown {
		t.Fatalf("没接 Running：%+v", r)
	}
	if r := s4(t, nil, none); r.State != selfcheck.Unknown {
		t.Fatalf("没有库：%+v", r)
	}
}
