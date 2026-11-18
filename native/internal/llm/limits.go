package llm

// LimitSource 说一个限额字段的值是谁给的，取值顺序见 ADR-334：用户覆盖 > 服务商（网关）返回 > models.dev 本地缓存 > 保守默认
type LimitSource string

const (
	SourceUser      LimitSource = "user"      // 用户在配置里对这个模型写的覆盖
	SourceProvider  LimitSource = "provider"  // 服务商或网关的 /models 返回的
	SourceModelsDev LimitSource = "modelsdev" // models.dev 本地缓存补的空
	SourceDefault   LimitSource = "default"   // 谁都没给，保守默认（窗口 128000、单次输出 32000），界面标"未知"
)

// LimitSources 记窗口和单次输出各自的来源，以及别的来源说了多少（悬停提示"服务商说多少、资料库说多少"用）
type LimitSources struct {
	Window, Output LimitSource
	// 各来源报的值（0 = 没报）。Used 的那一项就是最后用的
	ProviderWindow, ProviderOutput int
	DevWindow, DevOutput           int
	UserWindow, UserOutput         int
	// DevMixed：models.dev 里各家对这个模型说的不一样，取了保守的最小值
	DevMixed bool
}

// WindowUncertain 说窗口是不是不可靠：来源是保守默认，或 models.dev 里各家说法不一致。压缩硬线要更保守（0.75）
func (m Model) WindowUncertain() bool {
	return m.Limits.Window == SourceDefault || m.Limits.Window == SourceModelsDev && m.Limits.DevMixed
}

// OutputSource 是单次输出上限的来源；没填过来源的模型按报没报读
func (m Model) OutputSource() LimitSource {
	if m.Limits.Output != "" {
		return m.Limits.Output
	}
	if m.MaxOutputTokens > 0 {
		return SourceProvider
	}
	return SourceDefault
}

// WindowSource 同上，窗口的来源
func (m Model) WindowSource() LimitSource {
	if m.Limits.Window != "" {
		return m.Limits.Window
	}
	if m.ContextLength > 0 || m.ContextWindow > 0 || m.MaxInputTokens > 0 {
		return SourceProvider
	}
	return SourceDefault
}

// OutputLimit 是单次输出上限：有值用值，没有按 FallbackMaxOutput
func (m Model) OutputLimit() int {
	if m.MaxOutputTokens > 0 {
		return m.MaxOutputTokens
	}
	return FallbackMaxOutput
}

// ClampOutput 把单次输出压到窗口放得下：窗口 - 输入估算 - 4096 余量，至少 1（和主回合 turn.requestFor 同算法）。
// 后台请求（记忆整理、目标判断、复核）自己没有消息估算，传输入的字节数，按每 2 字节 1 令牌保守估
// （中文每字 3 字节约 1 到 2 令牌，英文每令牌约 4 字节，宁可压多一点也不让整档输出把请求顶出窗口）。
// output 小于等于 0 时先取 OutputLimit
func (m Model) ClampOutput(inputBytes int, output int64) int64 {
	if output <= 0 {
		output = int64(m.OutputLimit())
	}
	room := int64(m.Window()) - int64(inputBytes)/2 - 4096
	return min(output, max(1, room))
}
