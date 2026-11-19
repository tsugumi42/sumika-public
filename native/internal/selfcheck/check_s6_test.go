package selfcheck_test

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/llm"
	"sumika-native/internal/selfcheck"
)

func s6(t *testing.T, q selfcheck.Querier) selfcheck.Result {
	t.Helper()
	c := findCheck(t, "memory.compact_failed", selfcheck.DefaultThresholds())
	e := envAt(t0, selfcheck.NewCounters())
	e.Trace = q
	return c.Eval(context.Background(), e)
}

// B5：同一角色同一会话压缩连续 3 次失败（非校验拒收）→ bad，例子里是“角色@会话”
func TestS6ThreeCompactFailuresInARow(t *testing.T) {
	rw, ro := traceDB(t)
	runs(t, rw, "compact", 2, failing("timeout", "摘要请求没完成"))
	if r := s6(t, ro); r.State != selfcheck.Ok {
		t.Fatalf("只有 2 次不够：%+v", r)
	}
	insertRun(t, rw, run{id: "c3", stage: "compact", at: t0.Add(-10 * time.Minute), err: "摘要请求没完成", class: "timeout"})
	r := s6(t, ro)
	if r.State != selfcheck.Bad || r.Value != 1 || r.Example["first"] != "c1@qq:group:1" {
		t.Fatalf("连续 3 次应 bad：%+v", r)
	}
}

// 摘要被校验挡下（parse_failed）是正常拒收，取消也不算；一次成功就断了
func TestS6ValidationRejectionsAndSuccessDoNotCount(t *testing.T) {
	for name, mk := range map[string]func(int) run{
		"校验拒收": failing("parse_failed", "摘要太短"),
		"取消":   failing("cancelled", "被停下"),
		"夹一次成功": func(i int) run {
			if i == 1 {
				return run{}
			}
			return run{err: "x", class: "network"}
		},
	} {
		rw, ro := traceDB(t)
		runs(t, rw, "compact", 3, mk)
		if r := s6(t, ro); r.State != selfcheck.Ok {
			t.Errorf("%s 不该报：%+v", name, r)
		}
	}
}

// 按（角色，会话）分组：两个会话各失败 2 次不是同一条链连续 3 次
func TestS6GroupsByCharacterAndConversation(t *testing.T) {
	rw, ro := traceDB(t)
	runs(t, rw, "compact", 2, func(int) run { return run{conv: "qq:group:1", err: "x", class: "network"} })
	runs(t, rw, "compact", 2, func(int) run { return run{conv: "qq:group:2", err: "x", class: "network"} })
	if r := s6(t, ro); r.State != selfcheck.Ok {
		t.Fatalf("不同会话不合并：%+v", r)
	}
}

func TestS6UnknownWithoutTrace(t *testing.T) {
	c := findCheck(t, "memory.compact_failed", selfcheck.DefaultThresholds())
	if r := c.Eval(context.Background(), envAt(t0, selfcheck.NewCounters())); r.State != selfcheck.Unknown {
		t.Fatalf("%+v", r)
	}
}

// 旧行没有类别：按原文现算，被停下的压缩不算链路坏
func TestS6OldRowsAreClassifiedFromText(t *testing.T) {
	rw, ro := traceDB(t)
	runs(t, rw, "compact", 3, failing("", "context canceled"))
	c := findCheck(t, "memory.compact_failed", selfcheck.DefaultThresholds())
	e := envAt(t0, selfcheck.NewCounters())
	e.Trace = ro
	if r := c.Eval(context.Background(), e); r.State != selfcheck.Bad {
		t.Fatalf("没有分类函数：%+v", r)
	}
	e.Classify = llm.ClassifyText
	if r := c.Eval(context.Background(), e); r.State != selfcheck.Ok {
		t.Fatalf("现算出 cancelled：%+v", r)
	}
}

// 和 S7 同一个口径：没配模型、预算用完、全部候选都在冷却不是压缩链路坏，连续出现也不报；真的请求失败仍然报
func TestS6BenignModelErrorsDoNotCountAsCompactFailures(t *testing.T) {
	for name, mk := range map[string]func(int) run{
		"没配模型": failing("model_unknown", "模型不在列表里"),
		"预算用完": failing("budget_exhausted", "本轮预算用完"),
		"都在冷却": failing("", "候选 a、b 都在冷却"),
	} {
		rw, ro := traceDB(t)
		runs(t, rw, "compact", 3, mk)
		if r := s6(t, ro); r.State != selfcheck.Ok {
			t.Errorf("%s 不该报：%+v", name, r)
		}
	}
	// 对照：有底层错误的冷却跳过是候选真的请求失败了，照报
	rw, ro := traceDB(t)
	runs(t, rw, "compact", 3, failing("network", "试过 a；冷却跳过 b：连接被重置"))
	if r := s6(t, ro); r.State != selfcheck.Bad {
		t.Errorf("真的请求失败要报：%+v", r)
	}
}
