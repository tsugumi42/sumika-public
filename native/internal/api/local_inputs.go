package api

import "context"

// LocalInputSubmission 不接受调用者、权限、工作区；桌面宿主核对会话后绑定。
type LocalInputSubmission struct {
	Text, Mode, ClientKey, ExpectedRunID string
	// Parts 非空时是这条消息的有序部分（引用回复块 / 普通文字块，各自可带附件），此时 Text 必须为空；
	// 只填 Text 等价于只有一个普通文字块（规格-引用）。
	Parts []LocalPart
}
type InputReceipt struct {
	InputID, RunID, State string
	Sequence, Revision    uint64
}
type StopReceipt struct {
	RunID    string
	Revision uint64
	Drained  bool
}

// LocalProcessStep 是她这一轮的一步：思考或一次工具调用。Kind：think | tool；State：running | done | failed | stopped。
type LocalProcessStep struct {
	ID, Kind, Tool, Target, Thought, Output, State string
	Rule                                           string // 这一步是哪条规则放行的（给主人看的一句）；空是不用放行或没记录
	Step, Seconds                                  int
	Started                                        int64        // Unix 毫秒
	Recall                                         []RecallItem // Kind 为 recall：这一轮自动召回、真正注入给模型的记忆（不是工具调用）
}

// RecallItem 是自动召回里实际注入给模型的一条，只在用户本机界面显示。分数没有的是 0，界面不写；字段名和 trace 里存的 JSON 一致。
type RecallItem struct {
	Text, Kind, Source string
	CreatedAt          int64 // 记下的时间，Unix 毫秒
	Pinned, Hearsay    bool  // 固定要点；待确认
	Score, Rerank, Sim float64
	Lexical, Vector    int           // 词法、向量那一路的名次，0 没中
	Previous           []RecallPrior // 被这条取代的旧版本，新的在前
}

// RecallPrior 是命中带着的一条被取代的旧记录。
type RecallPrior struct {
	Text      string
	CreatedAt int64 // 记下的时间，Unix 毫秒
}

type LocalRunSnapshot struct {
	Process []LocalProcessStep // 这一轮的思考和工具，按开始的先后

	InputID, RootID, RunID, State, Outcome, Text string
	Revision                                     uint64
	Step                                         int
	SealedStep                                   int // 已经存库的步数（步号从 0 起）：比它小的步，界面等真消息接替草稿
	Drained                                      bool
	// Reason* 是结局的具体原因：Class 是稳定的键（budget_exhausted、rate_limited、timeout、network、model_unknown、exit……），
	// Detail 是截短的原始错误（只给折叠区），Model 是出错那一步的模型。正常结束三个都空。
	ReasonClass, ReasonDetail, ReasonModel string
	// Requeued 是这一轮收尾时才到的插话，后台把它们改排成了新的几轮；界面要把它们放进排队、跟着跑。
	Requeued []LocalRequeued
}

// LocalRequeued 是一句被改排成新一轮的插话。
type LocalRequeued struct {
	InputID, RunID, Text string
	Seq                  uint64
}

// LocalInputs 是显式 start/steer/queue 的可选入口，原 SendLocal 的 Busy 行为保持。
type LocalInputs interface {
	SubmitLocal(context.Context, ConversationKey, LocalInputSubmission) (InputReceipt, error)
	StopLocalRun(context.Context, ConversationKey, string, uint64) (StopReceipt, error)
	WatchLocalRun(context.Context, ConversationKey, string) (Subscription[LocalRunSnapshot], error)
}

// HeldInput 是重启时还在排队、没来得及处理的一句话（内核状态 held）。
type HeldInput struct {
	Key        ConversationKey
	InputID    string
	Text       string
	AcceptedAt int64 // Unix 毫秒
}

// LocalHeld 是主窗口对“没处理的话”和“排队中的话”的操作；和 LocalInputs 分开，旧的假后台不用改。
type LocalHeld interface {
	// HeldLocal 列出所有本地私聊里重启后没处理的话（任何会话，不只是前台这个）。
	HeldLocal(context.Context) ([]HeldInput, error)
	// ReleaseHeldLocal 放行一句：按排队处理，回执里的 RunID 就是它的那一轮。
	ReleaseHeldLocal(context.Context, ConversationKey, string) (InputReceipt, error)
	// DiscardHeldLocal 放弃一句：只标成取消，原文仍在聊天记录里。
	DiscardHeldLocal(context.Context, ConversationKey, string) error
	// WithdrawQueuedLocal 撤回一句还在排队的话；已经开始处理的返回错误。
	WithdrawQueuedLocal(context.Context, ConversationKey, string) error
}
