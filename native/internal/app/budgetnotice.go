package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/turn"
)

// 一条系统行的来源名（和 noticeGoal 一类）
const noticeBudget = "budget"

// budgetNoticePlugin 订阅"回合定了"：主人在本地主窗口私聊里，这一轮因为额度到顶被收住、或者最后一条回复被单次输出上限截断时，
// 在对话里存一行灰色系统行，说清楚是什么到顶了、用了多少、怎么接着做。不再让回复一声不吭地停下（规格-内核补强的整轮耗尽：
// "用宿主状态反馈"，不多花一次模型请求解释；QQ、群聊走原有出口，不在这里发）
type budgetNoticePlugin struct{ app *App }

func (budgetNoticePlugin) ID() string { return "budget-notice" }
func (p budgetNoticePlugin) Setup(r *turn.Registry) (func(), error) {
	return r.OnSettled(func(ctx context.Context, s turn.Settled) { p.app.budgetSettled(ctx, s) }), nil
}

func (a *App) budgetSettled(ctx context.Context, s turn.Settled) {
	if a.closing.Load() || !s.Identity.Scope.LocalWork() {
		return
	}
	// 自动轮（目标续轮、定期任务）用顶层那组额度，配置键和"继续"的说法都不同
	prefix := "agent.budget.local"
	if s.Auto {
		prefix = "agent.budget"
	}
	var text string
	switch {
	case s.Outcome == string(kernel.OutcomeBudgetExhausted):
		snap, err := a.hub.Cfg.Budget.Snapshot(context.WithoutCancel(ctx), s.Identity.RootID)
		if err != nil {
			text = "这一轮到额度上限被收住了（取不到具体用量：" + err.Error() + "）。" + budgetRetryHint(s.Auto) + "上限在配置文件（sumika.yml）里的 " + prefix + " 一组，改完重启 Sumika 才生效。"
		} else {
			text = budgetExhaustedText(snap, a.now(), s.Auto)
		}
	case s.Outcome == turn.OutcomeDone && s.Truncated:
		text = truncatedText(a.budgetSnapshot(ctx, s), s.Auto)
	case s.Outcome == turn.OutcomeError && reasoningFilledOutput(s.Err):
		text = reasoningFilledText(a.budgetSnapshot(ctx, s), s.Auto)
	default:
		return
	}
	a.noteLogf(a.systemNotice(s.Conversation, noticeBudget, text))
}

// budgetSnapshot 取这一轮根的额度和用量；取不到给零值（提示里就不说具体数）
func (a *App) budgetSnapshot(ctx context.Context, s turn.Settled) kernel.BudgetSnapshot {
	snap, err := a.hub.Cfg.Budget.Snapshot(context.WithoutCancel(ctx), s.Identity.RootID)
	if err != nil {
		return kernel.BudgetSnapshot{}
	}
	return snap
}

// budgetRetryHint 说怎么接着做：主人的轮回「继续」；自动轮没人能回（主窗口在自动轮运行时不收新一句），目标会自己续轮、定期任务到点再跑
func budgetRetryHint(auto bool) string {
	if auto {
		return "这是自动发起的一轮：目标会自己接着续轮，定期任务到点会再跑；要立刻处理，先点停止。"
	}
	return "回一句「继续」会新开一轮接着做。"
}

// reasoningFilledOutput 认"单次输出被思考占满、一个字没写出来"这种出错：网关（Magpie 上游）回 502 加一句
// "an empty reply ... cut at its length, the whole output spent on reasoning"，不是 stop=length，走不到截断提示。
// 临时：llm 层还没有明确的错误类别，先匹配网关消息特征；llm 给出类别（比如 llm.ErrOutputFilledByReasoning）后改成判类别并删掉这个函数
func reasoningFilledOutput(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "spent on reasoning") || strings.Contains(m, "cut at its length")
}

// budgetExhaustedText 说哪一项到顶了：取各项用量占上限比例最高的那一项（时限到点优先）
func budgetExhaustedText(snap kernel.BudgetSnapshot, now time.Time, auto bool) string {
	l := snap.Limits
	type dim struct {
		name, key string
		used, max int64
		text      string
	}
	dims := []dim{
		{"请求次数", "maxRequests", snap.Requests, l.MaxRequests, fmt.Sprintf("%d 次模型请求（上限 %d）", snap.Requests, l.MaxRequests)},
		{"工具调用", "maxTools", snap.Tools, l.MaxTools, fmt.Sprintf("%d 次工具调用（上限 %d）", snap.Tools, l.MaxTools)},
		{"累计令牌", "maxTokens", snap.Tokens, l.MaxTokens, fmt.Sprintf("约 %s 令牌（上限 %s）", compactCount(snap.Tokens), compactCount(l.MaxTokens))},
		{"输出令牌", "maxOutputTokens", snap.OutputTokens, l.MaxOutputTokens, fmt.Sprintf("输出 %s 令牌（上限 %s）", compactCount(snap.OutputTokens), compactCount(l.MaxOutputTokens))},
	}
	best, ratio := dims[0], -1.0
	for _, d := range dims {
		if d.max > 0 && !kernel.Unlimited(d.max) {
			if r := float64(d.used) / float64(d.max); r > ratio {
				best, ratio = d, r
			}
		}
	}
	timeUp := !l.Deadline.IsZero() && !now.Before(l.Deadline)
	prefix := "agent.budget.local"
	if auto {
		prefix = "agent.budget"
	}
	used := fmt.Sprintf("模型请求 %s 次、工具调用 %s 次、令牌约 %s", usedOf(snap.Requests, l.MaxRequests), usedOf(snap.Tools, l.MaxTools), usedOfCount(snap.Tokens, l.MaxTokens))
	if timeUp {
		return fmt.Sprintf("这一轮到「时限」的上限被收住了，不是做完：用了 %s。已经改好的文件和做过的事都还在。%s时限在配置文件（sumika.yml）里的 %s.maxDurationMs，改完重启 Sumika 才生效。", used, budgetRetryHint(auto), prefix)
	}
	return fmt.Sprintf("这一轮到「%s」的上限被收住了，不是做完：用了 %s。已经改好的文件和做过的事都还在。"+
		"%s常做大活可以在配置文件（sumika.yml）里调高 %s.%s，改完重启 Sumika 才生效。", best.name, used, budgetRetryHint(auto), prefix, best.key)
}

// usedOf 把"已用/上限"写出来；上限不设时只写已用
func usedOf(used, max int64) string {
	if kernel.Unlimited(max) {
		return fmt.Sprintf("%d", used)
	}
	return fmt.Sprintf("%d/%d", used, max)
}

func usedOfCount(used, max int64) string {
	if kernel.Unlimited(max) {
		return compactCount(used)
	}
	return compactCount(used) + "/" + compactCount(max)
}

// outputLimitCause 说这一次单次输出为什么到顶：配置里的可选封顶、输出额度快用完，还是模型自己的上限或上下文快满。
// 单次输出默认不再被配置压住，所以多数时候是最后一种
func outputLimitCause(snap kernel.BudgetSnapshot, auto bool) (cause string, configurable bool) {
	prefix := "agent.budget.local"
	if auto {
		prefix = "agent.budget"
	}
	switch {
	case snap.Limits.RequestOutputTokens > 0:
		return fmt.Sprintf("到了配置里设的单次输出封顶（%s 令牌，%s.requestOutputTokens；写 0 就跟模型走）", compactCount(snap.Limits.RequestOutputTokens), prefix), true
	case snap.Limits.MaxOutputTokens > 0 && !kernel.Unlimited(snap.Limits.MaxOutputTokens) && snap.Limits.MaxOutputTokens-snap.OutputTokens-snap.ReservedOutputTokens <= 0:
		return fmt.Sprintf("这一轮的输出额度快用完了（已输出 %s，上限 %s，%s.maxOutputTokens）", compactCount(snap.OutputTokens), compactCount(snap.Limits.MaxOutputTokens), prefix), true
	}
	return "写满了模型单次输出的上限，或者上下文快满了，由模型和窗口决定、不是配置压的", false
}

func truncatedText(snap kernel.BudgetSnapshot, auto bool) string {
	cause, _ := outputLimitCause(snap, auto)
	return fmt.Sprintf("上一条回复被截断了：%s。%s", cause, truncatedRetryHint(auto))
}

func truncatedRetryHint(auto bool) string {
	if auto {
		return "这是自动发起的一轮，没人能回「继续」；下一轮会接着做。"
	}
	return "回一句「继续」让她接着写；长文件可以让她分几段写。"
}

// reasoningFilledText：单次输出被思考占满，这一步一个字没写出来，回合出错收场
func reasoningFilledText(snap kernel.BudgetSnapshot, auto bool) string {
	cause, _ := outputLimitCause(snap, auto)
	return fmt.Sprintf("这一轮出错了：模型把单次输出全花在思考上，一个字没写出来（%s）。回一句「继续」可以重试，或者把任务拆小一点。", cause)
}

// compactCount 把令牌数写成容易读的样子（一万以上写成"52.4 万"）
func compactCount(n int64) string {
	if n >= 10_000 {
		return fmt.Sprintf("%.1f 万", float64(n)/1e4)
	}
	return fmt.Sprintf("%d", n)
}
