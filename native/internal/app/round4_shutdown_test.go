package app

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 路由超过关机预算才返回，判断必须先收尾且迟到回填不能再写一次。
func TestShutdownDeadlineSealsDecisions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	a.responderWait = 40 * time.Millisecond
	st := a.set.TraceStore()
	if _, err := st.Trace.Exec(`CREATE TABLE outcome_writes (id INTEGER); CREATE TRIGGER count_outcomes AFTER UPDATE OF outcome ON gate_decisions BEGIN INSERT INTO outcome_writes VALUES (NEW.id); END`); err != nil {
		t.Fatal(err)
	}
	k := &shutdownKernel{Kernel: a.kernel.Kernel, stopped: make(chan struct{}), route: make(chan struct{}), outcome: make(chan turn.Outcome, 1)}
	a.kernel.Kernel = k
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var routeOnce sync.Once
	unblock := func() { routeOnce.Do(func() { close(k.route) }) }
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		unblock()
		k.outcome <- turn.Outcome{}
		a.responder.Close()
		a.kernel.idle(context.Background())
	}()
	c := e.peer.accept(t)
	e.peer.send(t, c, groupAtFrame(e.now, testAccount, "731", testOwner, "编的第一批"))
	waitFor(t, "首批内核已启动", func() bool { return a.kernel.calls.Load() == 1 })
	e.peer.send(t, c, groupAtFrame(e.now.Add(time.Minute), testAccount, "732", testOwner, "编的第二批"))
	select {
	case <-k.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("未进入 StopIn")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超过缩短后的关机预算")
	}
	db, err := sql.Open("sqlite", a.paths.GoTraceDB())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check := func(stage string) {
		t.Helper()
		var total, interrupted, writes int
		if err := db.QueryRow(`SELECT count(*), count(CASE WHEN outcome='interrupted' THEN 1 END) FROM gate_decisions`).Scan(&total, &interrupted); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM outcome_writes`).Scan(&writes); err != nil {
			t.Fatal(err)
		}
		if total != 2 || interrupted != 2 || writes != 2 {
			t.Errorf("%s：判断=%d interrupted=%d 写入=%d，要各 2", stage, total, interrupted, writes)
		}
	}
	check("关机返回")
	// 绕过路由直接模拟内核迟到的 TurnEnd，也不能碰已关闭连接。
	if err := st.TurnEnd("编的回合", "done", e.now.UnixMilli()); err != nil {
		t.Errorf("迟到 TurnEnd 未静默丢弃：%v", err)
	}
	// 先放回路由，再等 Close，检查迟到判断回填没有二次 UPDATE。
	unblock()
	a.responder.Close()
	check("迟到路由返回")
	b := e.open()
	b.cleanup()
	check("重开库")
	for _, line := range e.logSnapshot() {
		if containsClosedDB(line) {
			t.Errorf("迟到日志：%s", line)
		}
	}
}

func containsClosedDB(s string) bool {
	return strings.Contains(s, "database is closed") || strings.Contains(s, "库已经关")
}

// 被杀进程没有走 shutdown，启动扫描也要补放行判断，已有结局及未放行判断保持原样。
func TestStartupSweepsUnfinishedDecisions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	st := a.set.TraceStore()
	for _, v := range []string{"forced", "enter", "skip"} {
		if _, err := st.AddGateDecision(store.GateDecisionRow{CharacterID: testChar, Conversation: "qq:group:" + testGroup, Verdict: v, Reason: "编的原因"}); err != nil {
			t.Fatal(err)
		}
	}
	finished, err := st.AddGateDecision(store.GateDecisionRow{CharacterID: testChar, Conversation: "qq:group:" + testGroup, Seq: 1, Verdict: "forced", Reason: "编的已完成判断"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGateOutcome(finished, store.GateOutcomeDry, "编的已完成结局"); err != nil {
		t.Fatal(err)
	}
	a.cleanup()
	b := e.open()
	defer b.cleanup()
	rows, err := b.set.TraceStore().GateDecisions("qq:group:"+testGroup, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		want := ""
		if r.Verdict != "skip" {
			want = store.GateOutcomeInterrupted
		}
		if r.Seq == 1 {
			want = store.GateOutcomeDry
		}
		if r.Outcome != want {
			t.Errorf("启动补判断 %s：结局=%q，要 %q", r.Verdict, r.Outcome, want)
		}
	}
}
