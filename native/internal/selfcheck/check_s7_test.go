package selfcheck_test

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/llm"
	"sumika-native/internal/selfcheck"
)

func s7(t *testing.T, q selfcheck.Querier) selfcheck.Result {
	t.Helper()
	c := findCheck(t, "model.stage_failing", selfcheck.DefaultThresholds())
	e := envAt(t0, selfcheck.NewCounters())
	e.Trace = q
	return c.Eval(context.Background(), e)
}

func failing(class, text string) func(int) run {
	return func(int) run { return run{err: text, class: class} }
}

// B5：同一阶段最近 5 次全失败 → bad；差一次成功就不是
func TestS7FiveFailuresInARow(t *testing.T) {
	rw, ro := traceDB(t)
	runs(t, rw, "vision", 4, failing("network", "连不上"))
	if r := s7(t, ro); r.State != selfcheck.Ok {
		t.Fatalf("只有 4 次，样本不够不判：%+v", r)
	}
	runs(t, rw, "guard", 5, failing("server_error", "502"))
	r := s7(t, ro)
	if r.State != selfcheck.Bad || r.Value != 1 || r.Example["stages"] != "guard" {
		t.Fatalf("guard 连续 5 次失败应 bad，vision 不够 5 次不算：%+v", r)
	}
	// 再给 vision 一次失败，两个阶段都 bad
	insertRun(t, rw, run{id: "v-last", stage: "vision", at: t0.Add(-10 * time.Minute), err: "x", class: "network"})
	if r := s7(t, ro); r.Value != 2 || r.Example["stages"] != "guard,vision" {
		t.Fatalf("两个阶段：%+v", r)
	}
}

func TestS7OneSuccessBreaksTheStreak(t *testing.T) {
	rw, ro := traceDB(t)
	runs(t, rw, "summary", 5, func(i int) run {
		if i == 2 {
			return run{} // 成功
		}
		return run{err: "出错了", class: "timeout"}
	})
	if r := s7(t, ro); r.State != selfcheck.Ok {
		t.Fatalf("中间有一次成功就断了：%+v", r)
	}
}

// B9：不算模型坏的失败：没配（model_unknown）、冷却跳过（类别空、原文有“冷却”）、取消、预算用完
func TestS7BenignErrorsDoNotCount(t *testing.T) {
	for name, mk := range map[string]func(int) run{
		"没配":   failing(llm.ClassModelUnknown, "模型列表里没有「x」"),
		"冷却":   failing("", "模型故障切换：试过 a；冷却跳过 b：都在冷却，没有可试的"),
		"取消":   failing(llm.ClassCancelled, "context canceled"),
		"预算用完": failing(llm.ClassBudgetExhausted, "budget_exhausted"),
	} {
		rw, ro := traceDB(t)
		runs(t, rw, "link", 5, mk)
		if r := s7(t, ro); r.State != selfcheck.Ok {
			t.Errorf("%s 不该算失败：%+v", name, r)
		}
	}
}

// 旧行没有 error_class（NULL），认不出是不是无害的，按失败算
func TestS7OldRowsWithoutClassCountAsFailures(t *testing.T) {
	rw, ro := traceDB(t)
	runs(t, rw, "vision", 5, failing("", "上游 500"))
	if r := s7(t, ro); r.State != selfcheck.Bad {
		t.Fatalf("没有类别的旧失败行应算失败：%+v", r)
	}
}

// compact 是 S6 的事，S7 不看；一天以前的不看
func TestS7IgnoresCompactAndOldRuns(t *testing.T) {
	rw, ro := traceDB(t)
	runs(t, rw, "compact", 5, failing("network", "x"))
	for i := 0; i < 5; i++ {
		insertRun(t, rw, run{id: "old" + string(rune('a'+i)), stage: "vision", at: t0.Add(-30 * time.Hour), err: "x", class: "network"})
	}
	if r := s7(t, ro); r.State != selfcheck.Ok {
		t.Fatalf("compact 和一天前的都不算：%+v", r)
	}
}

// 拿不到库、查询出错都是 unknown，不是 ok
func TestS7UnknownWhenUnreadable(t *testing.T) {
	c := findCheck(t, "model.stage_failing", selfcheck.DefaultThresholds())
	e := envAt(t0, selfcheck.NewCounters())
	if r := c.Eval(context.Background(), e); r.State != selfcheck.Unknown {
		t.Fatalf("没有库：%+v", r)
	}
	rw, ro := traceDB(t)
	rw.Close()
	if db, ok := ro.(interface{ Close() error }); ok {
		db.Close()
	}
	e.Trace = ro
	if r := c.Eval(context.Background(), e); r.State != selfcheck.Unknown {
		t.Fatalf("库关了查不动：%+v", r)
	}
}

// 本包抄的类别值和 llm 的词表一致（selfcheck 不能 import llm，所以在包外核对）
func TestClassConstantsMatchLLM(t *testing.T) {
	got := selfcheck.ClassConsts()
	want := map[string]string{"cancelled": llm.ClassCancelled, "model_unknown": llm.ClassModelUnknown, "budget_exhausted": llm.ClassBudgetExhausted, "parse_failed": llm.ClassParseFailed}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s：selfcheck 里是 %q，llm 里是 %q", k, got[k], v)
		}
	}
}

// 旧行没有类别、只有原文：app 接 llm.ClassifyText 现算，取消不会被当成模型坏了；不接就按失败算
func TestS7OldRowsAreClassifiedFromText(t *testing.T) {
	rw, ro := traceDB(t)
	runs(t, rw, "vision", 5, failing("", "context canceled"))
	c := findCheck(t, "model.stage_failing", selfcheck.DefaultThresholds())
	e := envAt(t0, selfcheck.NewCounters())
	e.Trace = ro
	if r := c.Eval(context.Background(), e); r.State != selfcheck.Bad {
		t.Fatalf("没有分类函数：没类别的行按失败：%+v", r)
	}
	e.Classify = llm.ClassifyText
	if r := c.Eval(context.Background(), e); r.State != selfcheck.Ok {
		t.Fatalf("按原文现算出 cancelled，不算失败：%+v", r)
	}
}

// 故障切换的错误原文：候选全在冷却（没真的请求）不算失败；但“试过 a、b；冷却跳过 c：<底层错误>”是候选真的失败了，要算
func TestS7FailoverWrapperWithRealErrorStillCounts(t *testing.T) {
	rw, ro := traceDB(t)
	runs(t, rw, "reply", 5, failing("server_error", "模型故障切换：试过 a、b；冷却跳过 c：502 bad gateway"))
	if r := s7(t, ro); r.State != selfcheck.Bad || r.Example["stages"] != "reply" {
		t.Fatalf("候选真失败了（带底层错误）不能当冷却跳过：%+v", r)
	}
	rw2, ro2 := traceDB(t)
	runs(t, rw2, "reply", 5, failing("", "模型故障切换：试过 无；冷却跳过 a、b：都在冷却，没有可试的"))
	if r := s7(t, ro2); r.State != selfcheck.Ok {
		t.Fatalf("全在冷却、没真的请求不算失败：%+v", r)
	}
}
