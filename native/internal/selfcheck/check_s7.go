package selfcheck

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 失败原因类别（internal/llm 的 Class* 词表）。selfcheck 不能 import llm，值在这里抄一份，class_test.go 在包外核对和 llm 一致。
const (
	classCancelled       = "cancelled"
	classModelUnknown    = "model_unknown"
	classBudgetExhausted = "budget_exhausted"
	classParseFailed     = "parse_failed"
)

// stageWindow 是 S6、S7 查最近几次时往前看多久：只看最近一天，查询按 started_at 索引收窄，不加索引（规格第八节）
const stageWindow = 24 * time.Hour

// benignModelError 说这一行 model_runs 的失败是不是"不算模型坏了"：
//   - cancelled：主人点了停止、关机打断；
//   - model_unknown：模型列表里没有这个模型（没配或配错），是配置问题，设置页会说，不是模型在失败；
//   - budget_exhausted：这一轮的预算用完了，是限额不是故障；
//   - 错误原文里有"都在冷却"：故障切换里全部候选都在冷却、被跳过，还没真的请求（llm/failover.go 的 Err：err 为空时才拼这句）。
//     注意只认"都在冷却"：同一个 Err 在有底层错误时拼的是"试过 a、b；冷却跳过 c：<底层错误>"，那是候选真的请求失败了，要算失败。
//
// 规格写的 not_configured、cooldown 在 llm 的词表里没有这两个类别，实际落库的是上面这几种，测试逐个钉住。
func benignModelError(class, errText string) bool {
	switch class {
	case classCancelled, classModelUnknown, classBudgetExhausted:
		return true
	}
	return strings.Contains(errText, "都在冷却")
}

// failedRun 是 model_runs 的一行里和失败有关的两列
type failedRun struct {
	err   string // 空 = 成功
	class string // error_class；旧行是空（NULL）
}

func (r failedRun) failed() bool { return r.err != "" }

// modelStageFailing 是 S7 model.stage_failing：同一个后台模型阶段（守卫、总结、识图、链接翻译……）最近 K 次全部失败。
// 只读 trace 库的 model_runs，按 stage 取最近 K 条；不算 compact（那是 S6 的事）；排除没配、冷却、取消、预算用完这些不是模型本身的失败。
// 最近一天里不足 K 次的阶段不判（样本不够，不是 ok 也不是 bad，只是没有证据）。
type modelStageFailing struct{ k int }

func (modelStageFailing) ID() string   { return "model.stage_failing" }
func (modelStageFailing) Name() string { return "后台模型阶段连续失败" }

func (c modelStageFailing) Eval(ctx context.Context, e *Env) Result {
	r := Result{Value: 0, Threshold: int64(c.k)}
	if e.Trace == nil {
		r.State, r.Note = Unknown, "没有运行记录库的只读连接"
		return r
	}
	// 窗口函数按 stage 分组取最近 K 条；started_at 有索引（ix_runs_started），取的列都不带大字段
	rows, err := e.Trace.QueryContext(ctx, `SELECT stage, error, error_class FROM (
		SELECT stage, error, error_class, ROW_NUMBER() OVER (PARTITION BY stage ORDER BY started_at DESC) AS rn
		FROM model_runs WHERE started_at >= ? AND stage <> 'compact'
	) WHERE rn <= ? ORDER BY stage, rn`, e.Now.Add(-stageWindow).UnixMilli(), c.k)
	if err != nil {
		r.State, r.Note = Unknown, "查询失败："+errClassOf(err)
		return r
	}
	defer rows.Close()
	byStage := map[string][]failedRun{}
	for rows.Next() {
		var stage string
		var errText, class *string
		if err := rows.Scan(&stage, &errText, &class); err != nil {
			r.State, r.Note = Unknown, "读取失败："+errClassOf(err)
			return r
		}
		fr := failedRun{}
		if errText != nil && *errText != "" {
			fr.err = *errText
			if class != nil {
				fr.class = *class
			}
			if fr.class == "" && e.Classify != nil {
				fr.class = e.Classify(fr.err) // 没有类别的旧行：按原文现算，取消、超时不会被当成模型坏了
			}
			if benignModelError(fr.class, fr.err) {
				fr.err = "" // 不算失败：这一次当成"没出错"，连续失败就断了
			}
		}
		byStage[stage] = append(byStage[stage], fr)
	}
	if err := rows.Err(); err != nil {
		r.State, r.Note = Unknown, "读取失败："+errClassOf(err)
		return r
	}
	var bad []string
	for stage, runs := range byStage {
		if len(runs) < c.k {
			continue
		}
		all := true
		for _, fr := range runs {
			if !fr.failed() {
				all = false
				break
			}
		}
		if all {
			bad = append(bad, stage)
		}
	}
	sort.Strings(bad)
	r.Value = int64(len(bad))
	if len(bad) > 0 {
		r.State = Bad
		r.Example = map[string]string{"stages": strings.Join(bad[:min(len(bad), 5)], ","), "streak": strconv.Itoa(c.k)}
		return r
	}
	r.State = Ok
	return r
}

// errClassOf 把查询错误缩成一句不带内容的说明（只留"超时"或"出错"），写进 note
func errClassOf(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "超时"
	}
	if errors.Is(err, context.Canceled) {
		return "被打断"
	}
	return "出错"
}
