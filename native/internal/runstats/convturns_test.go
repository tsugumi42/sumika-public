package runstats

import (
	"context"
	"testing"
)

func TestConvTurnsGroupsNewestFirst(t *testing.T) {
	db, file := tempTrace(t)
	ins := func(id, turn, conv, model string, at, in, read, write, out int64, failure, class string) {
		t.Helper()
		var e, c any
		if failure != "" {
			e = failure
		}
		if class != "" {
			c = class
		}
		var tid any
		if turn != "" {
			tid = turn
		}
		if _, err := db.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, total_ms, input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, error, error_class, turn_id, request_json, sections_json, injected_json, response_json)
			VALUES (?, 'c1', ?, 'reply', ?, ?, 100, ?, ?, ?, ?, ?, ?, ?, '{"secret":"编造的提示词"}', '[]', '[]', '{}')`, id, conv, model, at, in, read, write, out, e, c, tid); err != nil {
			t.Fatal(err)
		}
	}
	ins("r1", "t1", "console:c1", "编造模型", 1000, 10, 80, 10, 5, "", "")
	ins("r2", "t1", "console:c1", "编造模型", 2000, 20, 0, 0, 6, "", "")
	ins("r3", "t2", "console:c1", "编造模型二", 3000, 0, 0, 0, 0, "编造的错误", "timeout")
	ins("r4", "t3", "console:别的会话", "编造模型", 4000, 1, 0, 0, 1, "", "")
	ins("r5", "", "console:c1", "后台模型", 5000, 1, 0, 0, 1, "", "")
	if _, err := db.Exec(`INSERT INTO turns (id, character_id, conversation, channel, started_at, ended_at, outcome) VALUES ('t1','c1','console:c1','console',900,2500,'done')`); err != nil {
		t.Fatal(err)
	}
	svc := openService(t, file)
	rep, err := svc.ConvTurns(context.Background(), "console:c1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Turns) != 2 || rep.Turns[0].ID != "t2" || rep.Turns[1].ID != "t1" {
		t.Fatalf("新的在前，别的会话和后台任务不算：%+v", rep.Turns)
	}
	t2, t1 := rep.Turns[0], rep.Turns[1]
	if t2.EndedAt != 0 || t2.Requests[0].Failed != "timeout" {
		t.Fatalf("进行中不编结束时间，失败写类别：%+v", t2)
	}
	if t1.StartedAt != 900 || t1.EndedAt != 2500 || t1.Outcome != "done" {
		t.Fatalf("结局从 turns 表补：%+v", t1)
	}
	if len(t1.Requests) != 2 || t1.Requests[0].AtMS != 1000 {
		t.Fatalf("一轮里按先后：%+v", t1.Requests)
	}
	if got := t1.Requests[0].Input; got != 100 {
		t.Fatalf("总输入要加上缓存：%d", got)
	}
	short, err := svc.ConvTurns(context.Background(), "console:c1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !short.Truncated {
		t.Fatalf("读满上限要说：%+v", short)
	}
}

// 头像上下文环（ADR-351）：最近一次成功、有输入的请求带上它记下的窗口；失败的、更早的不带；context_json 坏了当 0，不报错。
func TestConvTurnsLatestWindow(t *testing.T) {
	db, file := tempTrace(t)
	ins := func(id string, at, in int64, failure, ctx any) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, total_ms, input_tokens, error, error_class, turn_id, context_json, request_json, sections_json, injected_json, response_json)
			VALUES (?, 'c1', 'console:c1', 'reply', '编造模型', ?, 100, ?, ?, CASE WHEN ? IS NULL THEN NULL ELSE 'timeout' END, 't1', ?, '{}', '[]', '[]', '{}')`, id, at, in, failure, failure, ctx); err != nil {
			t.Fatal(err)
		}
	}
	ins("r1", 1000, 10, nil, `{"breakdown":{"window":64000}}`)
	ins("r2", 2000, 20, nil, `{"breakdown":{"window":131000}}`)
	ins("r3", 3000, 30, "编造的错误", `{"breakdown":{"window":999}}`)
	svc := openService(t, file)
	rep, err := svc.ConvTurns(context.Background(), "console:c1", 0)
	if err != nil {
		t.Fatal(err)
	}
	reqs := rep.Turns[0].Requests
	if reqs[1].Window != 131000 || reqs[0].Window != 0 || reqs[2].Window != 0 {
		t.Fatalf("只有最近一次成功的 r2 带窗口：%+v", reqs)
	}
	if _, err := db.Exec(`UPDATE model_runs SET context_json = '不是 JSON' WHERE id = 'r2'`); err != nil {
		t.Fatal(err)
	}
	rep, err = svc.ConvTurns(context.Background(), "console:c1", 0)
	if err != nil {
		t.Fatalf("context_json 坏了不该报错：%v", err)
	}
	if w := rep.Turns[0].Requests[1].Window; w != 0 {
		t.Fatalf("坏的 context_json 当 0：%d", w)
	}
}
