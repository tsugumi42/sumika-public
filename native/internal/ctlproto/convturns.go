package ctlproto

// 右栏上下文标签的数据（规格-右栏上下文）：一个会话最近的模型请求，按轮分组。只有数字、模型名和失败类别，没有正文。

// ConvTurnsMaxRuns 是最多读多少次请求。
const ConvTurnsMaxRuns = 400

// ConvRequest 是一轮里的一次模型请求。
type ConvRequest struct {
	ID        string `json:"id"` // model_runs 的 id：点开请求详情时按它取
	Model     string `json:"model"`
	AtMS      int64  `json:"atMs"`
	TotalMs   int64  `json:"totalMs"`
	TTFTMs    int64  `json:"ttftMs"`
	Input     int64  `json:"input"` // 总输入：没命中缓存的 + 命中的 + 写缓存的
	CacheRead int64  `json:"cacheRead"`
	Output    int64  `json:"output"`
	Reasoning int64  `json:"reasoning"`
	Failed    string `json:"failed,omitempty"` // 失败类别；成功是空
	// Window 是这次请求记下的上下文窗口上限（context_json 里的）。只有整份报告里最近一次成功、有输入的请求填，
	// 标题栏头像的上下文环用它（ADR-351）；其余是 0，要看就点开请求读详情。
	Window int64 `json:"window,omitempty"`
}

// ConvTurn 是一轮：同一个 turn_id 的请求，按时间排。
type ConvTurn struct {
	ID        string        `json:"id"`
	Character string        `json:"character"`
	StartedAt int64         `json:"startedAt"`
	EndedAt   int64         `json:"endedAt"` // 还没结束是 0
	Outcome   string        `json:"outcome"`
	Requests  []ConvRequest `json:"requests"`
}

// ConvTurnsReport 是一个会话最近的几轮，新的在前。Truncated：读满上限了，更早的没列。
type ConvTurnsReport struct {
	Turns     []ConvTurn `json:"turns"`
	Truncated bool       `json:"truncated,omitempty"`
}

// RequestSection 是一次请求里的一段提示词（系统提示词或注入的上下文）。
// Text 是全文；Missing 为真说明正文表里找不到这个哈希（不编，界面写明）。
type RequestSection struct {
	ID      string `json:"id"`
	Source  string `json:"source,omitempty"`
	Chars   int    `json:"chars"`
	Text    string `json:"text,omitempty"`
	Missing bool   `json:"missing,omitempty"`
}

// RequestTool 是工具表里的一项：名字和估算的 token。
type RequestTool struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
}

// RequestDetail 是点开一次请求才读的详情（规格-右栏上下文 第二刀）。
// 解析不了的列不丢：原文放进对应的 Raw，ParseErr 写哪一列出了什么问题，界面原样显示原文，不崩。
type RequestDetail struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	Stage      string `json:"stage"`
	Route      string `json:"route,omitempty"`
	TotalMs    int64  `json:"totalMs"`
	TTFTMs     int64  `json:"ttftMs"`
	Input      int64  `json:"input"` // 没命中缓存的输入
	CacheRead  int64  `json:"cacheRead"`
	CacheWrite int64  `json:"cacheWrite"`
	Output     int64  `json:"output"`
	Reasoning  int64  `json:"reasoning"`
	Error      string `json:"error,omitempty"`

	System     []RequestSection `json:"system,omitempty"`   // 系统提示词：除注入以外的段
	Injected   []RequestSection `json:"injected,omitempty"` // 注入的上下文：memory 段，加 injected_json 里的条目
	Tools      []RequestTool    `json:"tools,omitempty"`
	ToolTokens int              `json:"toolTokens,omitempty"`
	Window     int              `json:"window,omitempty"` // context_json 里的窗口上限，没有是 0
	Estimate   int              `json:"estimate,omitempty"`
	// 请求正文（request_json）目前后台不存，列是空的；有就原样放这里。
	RequestRaw string `json:"requestRaw,omitempty"`

	SectionsRaw string   `json:"sectionsRaw,omitempty"` // sections_json 解析失败时的原文
	InjectedRaw string   `json:"injectedRaw,omitempty"` // injected_json 里解析不了的原文
	ContextRaw  string   `json:"contextRaw,omitempty"`  // context_json 解析失败时的原文
	ParseErr    []string `json:"parseErr,omitempty"`
}
