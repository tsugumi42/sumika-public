// Package kernel 定义内核各模块共用的身份和端口；不依赖具体执行器、存储或权限实现。
package kernel

import "strings"

// Scope 由可信宿主或已落库的渠道事件构造，正文和模型参数不能生成授权。
type Scope struct {
	CharacterID, Conversation, ScopeID  string
	Platform, Channel, AccountID, Scene string
	ActorKind, ActorID                  string
	AuthorityID                         string
	AuthorityRevision                   uint64
}

// LocalWork 是主人在本地主窗口私聊里干活：只有这个场合有逐次审批和主人在场，额度可以比 QQ、群聊、后台宽（agent.budget.local）。
// 本地群聊、QQ、后台任务都不算。目标续轮、定期任务虽然身份是主人，但没人看着：Scope 分不出来，由 turn.Input.Auto 另行排除（config.LimitsForInput）
func (s Scope) LocalWork() bool {
	return s.Platform == "local" && s.Channel == "console" && s.Scene == "private" && s.ActorKind == "owner" && !strings.Contains(s.Conversation, ":group:")
}

// Invocation 区分工作根、一次激活、逻辑步骤和实际派发，避免服务商重复 call ID 覆盖宿主记录。
type Invocation struct {
	Scope                                     Scope
	InputID, RootID, RunID, ParentRunID       string
	StepID, AttemptID, ToolID, ProviderCallID string
	Generation                                uint64
	Kind                                      string
}

type InputMode string
type InputState string
type RunState string
type Outcome string

const (
	InputStart InputMode = "start"
	InputSteer InputMode = "steer"
	InputQueue InputMode = "queue"
)

const (
	InputAccepted  InputState = "accepted"
	InputAdmitted  InputState = "admitted"
	InputHeld      InputState = "held"
	InputCancelled InputState = "cancelled"
	InputRejected  InputState = "rejected"
)

const (
	RunQueued   RunState = "queued"
	RunRunning  RunState = "running"
	RunStopping RunState = "stopping"
	RunDraining RunState = "draining"
	RunSettled  RunState = "settled"
)

const (
	OutcomeDone            Outcome = "done"
	OutcomeStopped         Outcome = "stopped"
	OutcomeError           Outcome = "error"
	OutcomeInterrupted     Outcome = "interrupted"
	OutcomeBudgetExhausted Outcome = "budget_exhausted"
)

const (
	KindForeground = "foreground"
	KindChild      = "child"
	KindAdvisor    = "advisor"
	KindVerify     = "verify"
	KindCompact    = "compact"
	KindMemory     = "memory"
	KindGuard      = "guard"
	// KindGoalJudge 是 /goal 的裁判调用，模型调用记录的 stage 写它，用量统计才分得出来
	KindGoalJudge = "goal_judge"
	// KindLinkTranslate 是链接展开的翻译调用（规格-链接翻译），用量统计单列“链接翻译”
	KindLinkTranslate = "link_translate"
)

// Receipt 只证明输入事务已提交，不证明模型已读取或渠道已交付。
type Receipt struct {
	InputID, RunID     string
	Sequence, Revision uint64
	State              InputState
}

type StopRequest struct {
	Scope           Scope
	RunID           string
	ThroughSequence uint64
}

type StopReceipt struct {
	RunID    string
	Revision uint64
	Drained  bool
}
