package selfcheck

import (
	"context"
	"sort"
	"strconv"
)

// memoryCompactFailed 是 S6 memory.compact_failed：同一个角色在同一个会话里，上下文压缩连续失败。
//
// 数据来源：trace 库 model_runs 里 stage='compact' 的行。压缩每次（成功、失败都）写一行，带 error 和 error_class（turn/compact.go：
// run.Stage="compact"，errClassOf 填类别）。不读聊天库的 context_epochs：它的 error 是自由文本、没有类别列，而 model_runs 有，
// 规格第十一节“原因列要核实”的结论就是用这里的 error_class。
//
// 排除：parse_failed（摘要太短、太长、没压小、模型要调工具——这是“摘要被校验挡下”的正常拒收，不是压缩链路坏了），
// 再加 S7 的 benignModelError（cancelled 被停止或关机打断、model_unknown 没配模型、budget_exhausted 预算用完、全部候选都在冷却）：
// 两条检查对“不算失败”的口径一致，压缩阶段没配模型、预算用完不会报成 memory.compact_failed。
// 别的失败（网络、超时、限流、服务端错误、认证……）连续 K 次才报。按 (角色, 会话) 分组：压缩是一个会话一条链，换了会话就是另一回事。
type memoryCompactFailed struct{ k int }

func (memoryCompactFailed) ID() string   { return "memory.compact_failed" }
func (memoryCompactFailed) Name() string { return "上下文压缩连续失败" }

func (c memoryCompactFailed) Eval(ctx context.Context, e *Env) Result {
	r := Result{Threshold: int64(c.k)}
	if e.Trace == nil {
		r.State, r.Note = Unknown, "没有运行记录库的只读连接"
		return r
	}
	rows, err := e.Trace.QueryContext(ctx, `SELECT character_id, conversation, error, error_class FROM (
		SELECT character_id, conversation, error, error_class,
			ROW_NUMBER() OVER (PARTITION BY character_id, conversation ORDER BY started_at DESC) AS rn
		FROM model_runs WHERE started_at >= ? AND stage = 'compact'
	) WHERE rn <= ? ORDER BY character_id, conversation, rn`, e.Now.Add(-stageWindow).UnixMilli(), c.k)
	if err != nil {
		r.State, r.Note = Unknown, "查询失败："+errClassOf(err)
		return r
	}
	defer rows.Close()
	type key struct{ char, conv string }
	groups := map[key][]failedRun{}
	for rows.Next() {
		var k key
		var errText, class *string
		if err := rows.Scan(&k.char, &k.conv, &errText, &class); err != nil {
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
			if fr.class == classParseFailed || benignModelError(fr.class, fr.err) {
				fr.err = "" // 校验拒收、被打断、没配模型、预算用完、全员冷却都不算压缩链路坏：连续失败在这里断开（和 S7 同一个口径）
			}
		}
		groups[k] = append(groups[k], fr)
	}
	if err := rows.Err(); err != nil {
		r.State, r.Note = Unknown, "读取失败："+errClassOf(err)
		return r
	}
	var bad []string
	for k, runs := range groups {
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
			bad = append(bad, k.char+"@"+k.conv)
		}
	}
	sort.Strings(bad)
	r.Value = int64(len(bad))
	if len(bad) > 0 {
		r.State = Bad
		// 例子里放“角色@会话”：会话号带群号，由 Runner 的打码函数处理
		r.Example = map[string]string{"first": bad[0], "streak": strconv.Itoa(c.k)}
		if len(bad) > 1 {
			r.Example["more"] = strconv.Itoa(len(bad) - 1)
		}
		return r
	}
	r.State = Ok
	return r
}
