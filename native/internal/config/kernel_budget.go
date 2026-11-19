package config

import (
	"fmt"
	"github.com/goccy/go-yaml"
	"sumika-native/internal/kernel"
	"time"
)

type Scheduler struct {
	MaxToolsPerRun  int `yaml:"maxToolsPerRun"`
	MaxToolsGlobal  int `yaml:"maxToolsGlobal"`
	MaxModelsGlobal int `yaml:"maxModelsGlobal"`
}

func DefaultScheduler() Scheduler { return Scheduler{4, 8, 4} }
func (s *Scheduler) UnmarshalYAML(b []byte) error {
	type plain Scheduler
	v := plain(DefaultScheduler())
	if err := strictKernelNumbers(b); err != nil {
		return err
	}
	if err := yaml.UnmarshalWithOptions(b, &v, yaml.Strict()); err != nil {
		return err
	}
	if v.MaxToolsPerRun < 1 || v.MaxToolsPerRun > 16 || v.MaxToolsGlobal < 1 || v.MaxToolsGlobal > 32 || v.MaxModelsGlobal < 1 || v.MaxModelsGlobal > 16 {
		return fmt.Errorf("agent.scheduler: capacity outside supported range")
	}
	*s = Scheduler(v)
	return nil
}

// Budget 是一个根（一次受理的活）的额度。顶层这组数管 QQ、群聊、本地群聊和后台任务（无人值守，没有主人逐次看着），保持保守；
// local 管主人在本地主窗口私聊里干活：别家的主窗口（Codex、opencode、omp、pi）都没有请求次数、累计令牌、时限上限，
// 防跑飞靠重复调用检测和用户打断，所以 local 那组默认全关（写 0 就是不设），要设再写。
// 单次输出不在这里定：默认跟模型自己的输出上限走（requestOutputTokens 写 0）；写正数只是可选的封顶。
type Budget struct {
	MaxRequests         int64       `yaml:"maxRequests"`
	MaxTools            int64       `yaml:"maxTools"`
	MaxTokens           int64       `yaml:"maxTokens"`
	MaxOutputTokens     int64       `yaml:"maxOutputTokens"`
	MaxDurationMs       int64       `yaml:"maxDurationMs"`
	MaxCostMicros       *int64      `yaml:"maxCostMicros"`
	RequestOutputTokens int64       `yaml:"requestOutputTokens"`
	Local               BudgetLocal `yaml:"local"`
}

// BudgetLocal 是本地主窗口私聊的额度（agent.budget.local），每一项 0 = 不设。金额门不在这里：它对所有场合是同一个开关
type BudgetLocal struct {
	MaxRequests         int64 `yaml:"maxRequests"`
	MaxTools            int64 `yaml:"maxTools"`
	MaxTokens           int64 `yaml:"maxTokens"`
	MaxOutputTokens     int64 `yaml:"maxOutputTokens"`
	MaxDurationMs       int64 `yaml:"maxDurationMs"`
	RequestOutputTokens int64 `yaml:"requestOutputTokens"`
	// loaded 只有 DefaultBudgetLocal 会置位：整块没按配置加载（手拼的 Budget、测试里的小额度）时它是 false，
	// 所有场合都用顶层那组。默认值全是 0，不能再拿"整块是零值"判断有没有加载
	loaded bool
}

// DefaultBudget 的顶层数是无人值守的兜底（有 Hermes 的依据：网关和定时任务默认硬停）。
// 累计输出默认等于累计令牌：单次输出不再被固定值压住后，一次回复写满一个小累计额，后面的请求就全被拒了，
// 累计令牌本来就盖住了输出，所以这里不再另设更小的数。
func DefaultBudget() Budget {
	return Budget{MaxRequests: 24, MaxTools: 60, MaxTokens: 524288, MaxOutputTokens: 524288, MaxDurationMs: 900000, RequestOutputTokens: 0, Local: DefaultBudgetLocal()}
}

// DefaultBudgetLocal：主窗口私聊全关
func DefaultBudgetLocal() BudgetLocal { return BudgetLocal{loaded: true} }

func (s *Budget) UnmarshalYAML(b []byte) error {
	type plain Budget
	v := plain(DefaultBudget())
	if err := strictKernelNumbers(b); err != nil {
		return err
	}
	if err := yaml.UnmarshalWithOptions(b, &v, yaml.Strict()); err != nil {
		return err
	}
	if err := checkBudgetRanges("agent.budget", false, v.MaxRequests, v.MaxTools, v.MaxTokens, v.MaxOutputTokens, v.RequestOutputTokens, v.MaxDurationMs); err != nil {
		return err
	}
	if v.MaxCostMicros != nil && *v.MaxCostMicros < 0 {
		return fmt.Errorf("agent.budget.maxCostMicros: invalid limit，不能是负数")
	}
	// 金额门现在设不了：模型调用端拿不到供应商可证明的价格上界，设了以后账本会把每个请求都拒掉（turnbudget.Reserve）。
	// 在这里拒绝保存，免得用户设了金额、等到运行时才发现什么都发不出去
	if v.MaxCostMicros != nil {
		return fmt.Errorf("agent.budget.maxCostMicros: 暂不支持设金额上限（供应商没有可证明的价格上界，设了会让所有模型请求被拒绝）；请留空，用请求数、令牌数、时限限额")
	}
	*s = Budget(v)
	return nil
}

// checkBudgetRanges 查一组额度；出错写清是哪个键、现在是多少。
// unset：这一组允许 0（不设）。顶层那组（无人值守）不允许，它的作用就是兜底
func checkBudgetRanges(prefix string, unset bool, requests, tools, tokens, outputTokens, requestOutput, durationMs int64) error {
	floor := int64(1)
	rng := "至少 1"
	if unset {
		floor, rng = 0, "0（不设）或更大"
	}
	for _, c := range []struct {
		key string
		got int64
	}{{"maxRequests", requests}, {"maxTools", tools}, {"maxTokens", tokens}, {"maxOutputTokens", outputTokens}, {"maxDurationMs", durationMs}} {
		if c.got < floor {
			return fmt.Errorf("%s.%s: invalid limit，现在是 %d，允许 %s", prefix, c.key, c.got, rng)
		}
	}
	// 单次输出两组都允许 0：0 就是跟模型
	if requestOutput < 0 {
		return fmt.Errorf("%s.requestOutputTokens: invalid limit，现在是 %d，允许 0（跟模型）或更大", prefix, requestOutput)
	}
	return nil
}

func (s *BudgetLocal) UnmarshalYAML(b []byte) error {
	type plain BudgetLocal
	v := plain(DefaultBudgetLocal())
	if err := strictKernelNumbers(b); err != nil {
		return err
	}
	if err := yaml.UnmarshalWithOptions(b, &v, yaml.Strict()); err != nil {
		return err
	}
	if err := checkBudgetRanges("agent.budget.local", true, v.MaxRequests, v.MaxTools, v.MaxTokens, v.MaxOutputTokens, v.RequestOutputTokens, v.MaxDurationMs); err != nil {
		return err
	}
	*s = BudgetLocal(v)
	return nil
}

// Limits 是 QQ、群聊、后台任务这类场合的额度（顶层那组）
func (s Budget) Limits(accepted time.Time) kernel.Limits {
	return kernel.Limits{MaxRequests: s.MaxRequests, MaxTools: s.MaxTools, MaxTokens: s.MaxTokens, MaxOutputTokens: s.MaxOutputTokens, Deadline: accepted.Add(time.Duration(s.MaxDurationMs) * time.Millisecond), MaxCostMicros: s.MaxCostMicros, RequestOutputTokens: s.RequestOutputTokens}
}

// LimitsFor 按场合挑额度：主人在本地主窗口私聊用 local，别的都用顶层那组。
// 受理时定一次，之后同一根不变（账本里存的是定下来的数）
func (s Budget) LimitsFor(scope kernel.Scope, accepted time.Time) kernel.Limits {
	return s.LimitsForInput(scope, false, accepted)
}

// LimitsForInput 同 LimitsFor，另外认"宿主自动发的轮"（目标续轮、定期任务）：没有主人在场逐次看着，用顶层那组，不用本地的宽额度
func (s Budget) LimitsForInput(scope kernel.Scope, auto bool, accepted time.Time) kernel.Limits {
	if auto {
		return s.Limits(accepted)
	}
	// Local 没按配置加载（手拼的 Budget、测试里的小额度）：所有场合都用顶层那组，不拿零额度开根
	if !scope.LocalWork() || !s.Local.loaded {
		return s.Limits(accepted)
	}
	l := s.Local
	// 账本只认正数：0（不设）换成 kernel.NoLimit，时限换成一百年后
	open := func(v int64) int64 {
		if v <= 0 {
			return kernel.NoLimit
		}
		return v
	}
	deadline := accepted.Add(kernel.NoDeadlineAfter)
	if l.MaxDurationMs > 0 {
		deadline = accepted.Add(time.Duration(l.MaxDurationMs) * time.Millisecond)
	}
	return kernel.Limits{MaxRequests: open(l.MaxRequests), MaxTools: open(l.MaxTools), MaxTokens: open(l.MaxTokens), MaxOutputTokens: open(l.MaxOutputTokens), Deadline: deadline, MaxCostMicros: s.MaxCostMicros, RequestOutputTokens: l.RequestOutputTokens}
}

// YAML 的小数、字符串及 null 不当作默认或无限；金额门单独允许 null。
func strictKernelNumbers(b []byte) error {
	var fields map[string]any
	if err := yaml.Unmarshal(b, &fields); err != nil {
		return err
	}
	for key, value := range fields {
		if key == "maxCostMicros" && value == nil {
			continue
		}
		if key == "local" {
			continue // 本地额度是下一层的块，它自己的 UnmarshalYAML 再查
		}
		switch value.(type) {
		case int, int64, uint64:
		default:
			return fmt.Errorf("%s: expected integer", key)
		}
	}
	return nil
}
