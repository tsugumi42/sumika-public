package modelmeta

import (
	"sumika-native/internal/llm"
)

// Override 是用户对一个模型写的覆盖（配置 modelMeta.overrides.<模型ID>），0 = 这一项没覆盖
type Override struct {
	ContextWindow   int `yaml:"contextWindow"`
	MaxOutputTokens int `yaml:"maxOutputTokens"`
}

// Resolve 把一个模型的窗口和单次输出按取值顺序定下来，字段单独走一遍，并记每个字段的来源：
//
//	用户覆盖 > 服务商（网关）返回 > models.dev 缓存（只补空，绝不盖服务商已报的值）> 保守默认（窗口 128000、输出 32000，标"未知"）
//
// 服务商必须排在 models.dev 前面：同一个模型，网关知道请求实际落到哪条渠道，报的窗口常比 models.dev 小
// （实测 gpt-6.1-sol：网关 50 万，models.dev 105 万），照 models.dev 发会超窗口被拒。连 Magpie 这类网关时它已经合过一遍
// models.dev，所以它报了的字段这里不再叠一层。
// 返回的是副本，原模型不动；已填的 Limits 会按新结果重填，所以可以对同一个列表反复调用（改了覆盖、刷新了缓存之后）。
func Resolve(m llm.Model, ov Override, cat *Catalog) llm.Model {
	src := llm.LimitSources{}
	// 服务商报的是什么（先于任何改写读出来；重复调用时用 Limits 里记的原值）
	providerWindow, providerOutput := m.Limits.ProviderWindow, m.Limits.ProviderOutput
	if m.Limits.Window == "" && m.Limits.Output == "" {
		providerWindow, providerOutput = rawWindow(m), m.MaxOutputTokens
	}
	src.ProviderWindow, src.ProviderOutput = providerWindow, providerOutput
	src.UserWindow, src.UserOutput = max(ov.ContextWindow, 0), max(ov.MaxOutputTokens, 0)
	found, hasDev := cat.Lookup(m.ID)
	if hasDev {
		src.DevWindow, src.DevOutput, src.DevMixed = found.Context, found.Output, found.Mixed
	}
	// 先清掉上一次填进去的值，从服务商原值重新开始
	m.ContextLength, m.ContextWindow, m.MaxInputTokens, m.MaxOutputTokens = 0, 0, 0, 0
	switch {
	case src.UserWindow > 0:
		m.ContextLength, src.Window = src.UserWindow, llm.SourceUser
	case providerWindow > 0:
		m.ContextLength, src.Window = providerWindow, llm.SourceProvider
	case src.DevWindow > 0:
		m.ContextLength, src.Window = src.DevWindow, llm.SourceModelsDev
	default:
		src.Window = llm.SourceDefault // Window() 会按 128000
	}
	switch {
	case src.UserOutput > 0:
		m.MaxOutputTokens, src.Output = src.UserOutput, llm.SourceUser
	case providerOutput > 0:
		m.MaxOutputTokens, src.Output = providerOutput, llm.SourceProvider
	case src.DevOutput > 0:
		m.MaxOutputTokens, src.Output = src.DevOutput, llm.SourceModelsDev
	default:
		src.Output = llm.SourceDefault // OutputLimit() 会按 32000
	}
	m.Limits = src
	return m
}

// rawWindow 是服务商报的窗口：和 llm.Model.Window 一样，几个字段谁有用谁，都没有是 0（不套 128000）
func rawWindow(m llm.Model) int {
	for _, n := range []int{m.ContextLength, m.ContextWindow, m.MaxInputTokens} {
		if n > 0 {
			return n
		}
	}
	return 0
}

// ResolveAll 对整张列表做 Resolve
func ResolveAll(models []llm.Model, overrides map[string]Override, cat *Catalog) []llm.Model {
	out := make([]llm.Model, len(models))
	for i, m := range models {
		out[i] = Resolve(m, overrides[m.ID], cat)
	}
	return out
}
