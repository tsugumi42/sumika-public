package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/bench"
)

// B3 停下的那一步带着"中止了"的错（Go 壳是 The operation was aborted.），不算壳没跑成；没停下的步出错照算
func TestStepErrorsSkipsStopped(t *testing.T) {
	res := bench.Result{Steps: []bench.StepResult{
		{Error: "The operation was aborted.", Stopped: true},
		{Error: "连不上网关"},
	}}
	if got := stepErrors(res); got != "第 2 步：连不上网关" {
		t.Fatalf("%q", got)
	}
}

func TestSummaryB3Stops(t *testing.T) {
	yes, no := true, false
	record := func(id string, fired *bool, stopped, timedOut, pass bool) *runRecord {
		return &runRecord{Task: id, Group: "b3", Shell: "go", Rep: 1, Ran: true, Verdict: bench.Verdict{Pass: pass},
			Result: bench.Result{Steps: []bench.StepResult{{StopTriggered: fired, Stopped: stopped, TimedOut: timedOut}}}}
	}
	recs := []*runRecord{
		record("触发且通过", &yes, true, false, true), record("触发但失败", &yes, true, false, false),
		record("没到点但做完", &no, false, false, true), record("只超时", &no, true, true, true),
		record("旧结果", nil, true, false, true),
		{Task: "普通任务", Group: "b2", Shell: "go", Ran: true, Verdict: bench.Verdict{Pass: true}},
	}
	interrupted := record("执行器被打断", &yes, true, false, true)
	interrupted.Interrupted = true
	recs = append(recs, interrupted)
	summary := summarize(recs, nil)
	for _, want := range []string{
		"| go | 2 | 2 | 1 | 1 / 2 |", // B3 只将触发且跑成的两次放进续做分母。
		"| 没到点但做完 | go | 1 | 过 | 未触发 |", "| 只超时 | go | 1 | 过 | 未触发 |",
		"| 旧结果 | go | 1 | 过 | 未记录 |", "| 普通任务 | go | 0 | 过 | 不适用 |",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("汇总缺少 %q：\n%s", want, summary)
		}
	}
	t.Log(summary)
}

// 固定题集：按文件里的题号挑、保持文件里的先后；题库里没有的题号报错，不悄悄少跑
func TestPickSet(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "scripts", "bench-t2", "sets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "demo.txt"), []byte("# 注释\nb-2\n\na-1 # 行尾注释\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "bad.txt"), []byte("a-1\nzzz\n"), 0o644)
	bank := []bench.BankTask{{ID: "a-1"}, {ID: "b-2"}, {ID: "c-3"}}
	got, err := pickSet(bank, root, "demo")
	if err != nil || len(got) != 2 || got[0].ID != "b-2" || got[1].ID != "a-1" {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := pickSet(bank, root, "bad"); err == nil || !strings.Contains(err.Error(), "zzz") {
		t.Fatalf("缺的题号要报错：%v", err)
	}
	if _, err := pickSet(bank, root, "../demo"); err == nil {
		t.Fatal("题集名不能带路径")
	}
}

// 额度账：累计 token 的口径和预算账本一样（输入、缓存读、缓存写、输出都算），结束原因和回合结局都列出来
func TestRunTable(t *testing.T) {
	r := &runRecord{Task: "x", Shell: "go", Rep: 1, WallMs: 12_000, Verdict: bench.Verdict{Pass: false},
		Gateway: []bench.GatewayCall{{Input: 100, CacheRead: 900, CacheWrite: 10, Output: 40, Stop: "tool_calls"}, {Input: 5, CacheRead: 1000, Output: 4096, Stop: "length"}},
		Result:  bench.Result{Steps: []bench.StepResult{{Tools: make([]bench.ToolUse, 3), Outcome: "budget_exhausted"}}}}
	got := runTable([]*runRecord{r})
	if !strings.Contains(got, "| x | go | 1 | 2 | 3 | 6151 | 1900 | 4136 | 12 | tool_calls length | budget_exhausted | 没过 |") {
		t.Fatal(got)
	}
}
