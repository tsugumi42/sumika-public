// Package goal 实现 /goal：给会话设一个目标，每轮结束由后台模型（裁判）判断是否达成，
// 没达成就给出下一轮的提示，直到达成、预算到顶或遇到兜底条件而暂停。
//
// 这个包不接本地发送链路：宿主在每轮结束时调 Manager.AfterTurn，按返回的 Decision 决定
// 发下一轮、显示达成还是显示暂停。设计原则是引导加少数兜底，不替模型加额外限制。
package goal

import "time"

// Status 是目标的状态。
type Status string

const (
	StatusActive  Status = "active"  // 进行中
	StatusPaused  Status = "paused"  // 暂停
	StatusDone    Status = "done"    // 达成（达成后目标即被清除，只出现在 Decision 带回的快照里）
	StatusCleared Status = "cleared" // 清除（同上）
)

// 默认预算：轮数 30，token 不限。
const (
	DefaultMaxRounds = 30
	DefaultMaxFails  = 3
)

// Budget 是预算，0 表示不限（轮数默认 30 由 Manager 填）。
type Budget struct {
	MaxRounds int   `json:"maxRounds"`
	MaxTokens int64 `json:"maxTokens"`
}

// Judgement 是裁判的一次判断。
type Judgement struct {
	Achieved bool      `json:"achieved"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
	// Failed 为真表示这次裁判出错，没有给出判断，Reason 是错误说明。
	Failed bool `json:"failed,omitempty"`
}

// Goal 是一个会话的目标状态。
type Goal struct {
	Session     string     `json:"session"`
	Text        string     `json:"text"`
	StartedAt   time.Time  `json:"startedAt"`
	Rounds      int        `json:"rounds"`
	TokensUsed  int64      `json:"tokensUsed"`
	Status      Status     `json:"status"`
	PauseReason string     `json:"pauseReason,omitempty"`
	Last        *Judgement `json:"last,omitempty"`
	JudgeFails  int        `json:"judgeFails"` // 连续裁判出错次数
	Budget      Budget     `json:"budget"`
	// Step 是预算的步长：暂停后继续时，到顶的那一项按步长再续一段。
	Step Budget `json:"step"`

	// 以下是兜底用的内部状态。
	Wrapping     bool   `json:"wrapping,omitempty"`     // 已发过收尾提示，下一次就暂停
	StuckStreak  int    `json:"stuckStreak,omitempty"`  // 连续说卡住的轮数
	LastAnswer   string `json:"lastAnswer,omitempty"`   // 上一轮回答（截断），用来判断空转
	LastToolCnt  int    `json:"lastToolCnt,omitempty"`  // 上一轮工具调用数
	HasLastRound bool   `json:"hasLastRound,omitempty"` // LastAnswer、LastToolCnt 有效
}

// ToolSummary 是一次工具调用的摘要。
type ToolSummary struct {
	Name   string
	OK     bool
	Result string // 简短结果，裁判提示里会再截断
}

// TurnResult 是一轮的结果，由宿主在回合结束时填。
type TurnResult struct {
	Answer    string        // 她这一轮的最终回答
	Tools     []ToolSummary // 这一轮的工具调用
	Tokens    int64         // 这一轮用掉的 token（输入加输出，宿主自己定口径）
	NeedsUser bool          // 她在等主人回答（审批卡、提问）：目标暂停
}
