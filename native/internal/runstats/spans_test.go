package runstats

import (
	"context"
	"testing"

	"sumika-native/internal/ctlproto"
)

func TestSpansAggregateAndTurn(t *testing.T) {
	db, file := tempTrace(t)
	ins := func(turn string, seq int, kind, name string, at, ms int64, outcome string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO turn_spans (turn_id, seq, kind, name, started_at, ms, outcome) VALUES (?, ?, ?, ?, ?, ?, ?)`, turn, seq, kind, name, at, ms, outcome); err != nil {
			t.Fatal(err)
		}
	}
	for i := int64(1); i <= 100; i++ {
		ins("t1", int(i), "tool.exec", "ls", day7+i, i, "ok")
	}
	ins("t2", 0, "model.wait", "m", day7+200, 50000, "failed")
	ins("t2", 1, "turn.total", "", day7+200, 60000, "ok")
	svc := openService(t, file)
	rng, err := svc.RangeOf(ctlproto.StatsArgs{All: true})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Spans(context.Background(), rng, ctlproto.StatsArgs{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Turns != 2 || len(rep.Stats) != 3 || rep.Stats[0].Kind != "turn.total" {
		t.Fatalf("应按合计从大到小：%+v", rep)
	}
	var exec ctlproto.SpanStat
	for _, s := range rep.Stats {
		if s.Kind == "tool.exec" {
			exec = s
		}
		if s.Kind == "model.wait" && s.Failed != 1 {
			t.Fatalf("失败要数出来：%+v", s)
		}
	}
	if exec.Count != 100 || exec.TotalMs != 5050 || exec.P50Ms != 50 || exec.P95Ms != 95 || exec.MaxMs != 100 || exec.Name != "ls" {
		t.Fatalf("汇总不对：%+v", exec)
	}
	last, err := svc.Spans(context.Background(), ctlproto.UsageRange{}, ctlproto.StatsArgs{Turn: "last"})
	if err != nil {
		t.Fatal(err)
	}
	if last.Turn != "t2" || len(last.Rows) != 2 {
		t.Fatalf("last 应是最近一轮：%+v", last)
	}
	none, err := svc.Spans(context.Background(), ctlproto.UsageRange{}, ctlproto.StatsArgs{Turn: "不存在"})
	if err != nil || len(none.Rows) != 0 {
		t.Fatalf("不存在的回合应是空：%+v %v", none, err)
	}
}

func TestSpansArgs(t *testing.T) {
	for _, bad := range []ctlproto.StatsArgs{{Turn: "x", All: true}, {Turn: "x", Days: 3}, {Year: 2026}, {By: "day"}, {Full: true}, {Task: "send"}, {Failures: 3}} {
		if bad.ValidateSpans() == nil {
			t.Errorf("应拒绝：%+v", bad)
		}
	}
	for _, ok := range []ctlproto.StatsArgs{{}, {Turn: "last"}, {Days: 7}, {All: true}} {
		if err := ok.ValidateSpans(); err != nil {
			t.Errorf("应接受 %+v：%v", ok, err)
		}
	}
}
