package runstats

// 规格-模型配置 M2：model_runs.model 新记录是规范引用（magpie/xxx），M2 之前的历史行是裸 ID。
// 读的时候统一：裸 ID 按 defaultProvider 补成规范引用，同一个模型统计成一行；不改历史行。

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/modelref"
)

func openRefService(t *testing.T, file string, refs modelref.Resolver) *Service {
	t.Helper()
	s, err := OpenFile(file, Options{Loc: zone, Refs: refs, Now: func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, zone) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// 同一个模型（Magpie 的 ID 自己带斜杠）新旧两种写法各两行：用量统计合成一行 4 次；
// 不传解析器（旧行为）仍是两行，证明是读时合并而不是库里被改
func TestUsageMergesBareAndCanonicalModel(t *testing.T) {
	refs := modelref.New("magpie", "magpie")
	for _, mode := range []string{"raw", "rebuild"} {
		t.Run(mode, func(t *testing.T) {
			db, file := tempTrace(t)
			at := timestamps[5]
			insertRows(t, db, []fx{
				{purpose: "foreground", stage: "reply", model: "group/auto-x", at: at, input: 10, output: 1, ms: i64(100)},
				{purpose: "foreground", stage: "reply", model: "group/auto-x", at: at + 1, input: 10, output: 1, ms: i64(100)},
				{purpose: "foreground", stage: "reply", model: "magpie/group/auto-x", at: at + 2, input: 10, output: 1, ms: i64(100)},
				{purpose: "memory", stage: "memory", model: "magpie/group/auto-x", at: at + 3, input: 10, output: 1, ms: i64(100)},
			})
			if mode == "rebuild" {
				syncRollup(t, db)
			}
			rep := func(s *Service) ctlproto.UsageReport {
				rng, err := s.RangeOf(ctlproto.StatsArgs{Days: 1})
				if err != nil {
					t.Fatal(err)
				}
				r, err := s.Usage(context.Background(), rng)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			merged := rep(openRefService(t, file, refs))
			if len(merged.Models) != 1 || merged.Models[0].Model != "magpie/group/auto-x" || merged.Models[0].Runs != 4 {
				t.Fatalf("新旧写法该合成一行 magpie/group/auto-x 共 4 次：%+v", merged.Models)
			}
			for _, task := range merged.Tasks {
				for _, m := range task.Models {
					if m.Model != "magpie/group/auto-x" {
						t.Errorf("任务 %s 下的模型 %q 没统一成规范引用", task.Key, m.Model)
					}
				}
			}
			plain := rep(openRefService(t, file, modelref.Resolver{}))
			if len(plain.Models) != 2 {
				t.Fatalf("不带解析器时库里仍是两种写法（读时合并，不改历史行）：%+v", plain.Models)
			}
			var bare int
			if err := db.QueryRow(`SELECT count(*) FROM model_runs WHERE model = 'group/auto-x'`).Scan(&bare); err != nil || bare != 2 {
				t.Fatalf("历史行不该被改：%d %v", bare, err)
			}
		})
	}
}

// 请求详情和会话回合里显示的模型也统一成规范引用
func TestRequestDetailAndConvTurnsShowCanonicalModel(t *testing.T) {
	db, file := tempTrace(t)
	for _, r := range []struct{ id, model string }{{"old", "group/auto-x"}, {"new", "magpie/group/auto-x"}} {
		if _, err := db.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, total_ms, input_tokens, turn_id, sections_json, injected_json)
			VALUES (?, 'c1', 'console:c1', 'reply', ?, 1000, 5, 1, ?, '[]', '[]')`, r.id, r.model, "t-"+r.id); err != nil {
			t.Fatal(err)
		}
	}
	svc := openRefService(t, file, modelref.New("magpie", "magpie"))
	for _, id := range []string{"old", "new"} {
		d, err := svc.RequestDetail(context.Background(), id)
		if err != nil || d.Model != "magpie/group/auto-x" {
			t.Errorf("%s 的详情模型 = %q（%v）", id, d.Model, err)
		}
	}
	rep, err := svc.ConvTurns(context.Background(), "console:c1", 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, turn := range rep.Turns {
		for _, rq := range turn.Requests {
			seen++
			if rq.Model != "magpie/group/auto-x" {
				t.Errorf("会话回合里的模型 = %q", rq.Model)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("两个请求都该在回合里：%d", seen)
	}
}

// 质量标签里的失败行和慢请求也用统一的模型名
func TestQualityShowsCanonicalModel(t *testing.T) {
	db, file := tempTrace(t)
	at := timestamps[5]
	insertRows(t, db, []fx{
		{purpose: "foreground", stage: "reply", model: "group/auto-x", at: at, input: 1, output: 1, ms: i64(100), err: "boom"},
		{purpose: "foreground", stage: "reply", model: "magpie/group/auto-x", at: at + 1, input: 1, output: 1, ms: i64(100), err: "boom"},
	})
	svc := openRefService(t, file, modelref.New("magpie", "magpie"))
	rng, err := svc.RangeOf(ctlproto.StatsArgs{Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Quality(context.Background(), rng, ctlproto.StatsArgs{Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, sl := range rep.Slowest {
		if sl.Model != "" && sl.Model != "magpie/group/auto-x" {
			t.Errorf("慢请求的模型 = %q", sl.Model)
		}
	}
	for _, f := range rep.Recent {
		n++
		if f.Model != "magpie/group/auto-x" {
			t.Errorf("失败行的模型 = %q", f.Model)
		}
	}
	if n != 2 {
		t.Fatalf("该读到两条失败行：%+v", rep)
	}
}
