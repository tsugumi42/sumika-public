package api

import (
	"context"
	"errors"
)

var (
	ErrLocalChatUnsupported = errors.New("只看历史：请先启动后台，再发送本地消息")
	ErrLocalChatBusy        = errors.New("这个会话正在生成，请先停止")
	ErrApprovalGone         = errors.New("审批已结束或不属于这个会话")
	ErrQuestionGone         = errors.New("这个问题已经结束了，或者不属于这个会话")
)

// 本地私聊交给 Bun 以后（配置 backend.local 是 bun，规格-本地私聊切到Bun）的两种拒绝：
//   - ErrLocalOwnedByBun：Go 后台这边的本地写方法一律拒绝，写由 Bun 管；
//   - ErrLocalBackendDown：该走 Bun 的方法，但 Bun 后台现在不在（没起来、崩了、连不上），不会退回 Go 去写。
var (
	ErrLocalOwnedByBun  = errors.New("本地私聊现在由 Bun 管，这里不能改")
	ErrLocalBackendDown = errors.New("本地私聊的后台不在")
)

// LocalDraft 是一条发送请求当前步骤的完整草稿；Text 为空表示该步骤已落库。
// 回调只在该请求内有序，不依赖聊天库的失效通知。
type LocalDraft struct {
	TurnID string
	Step   int
	Text   string
	// CharacterID 只有本地群聊填：这段话是哪位成员的。
	CharacterID string
	// Reset 撤回这一步已经流出的字：模型中途失败、换备选重来。和“这一步存好了”（Text 为空）不是一回事。
	Reset bool
}

// LocalApproval 的身份全由后台绑定，显示文字不能用来判断权限。
type LocalApproval struct {
	ID, CharacterID                             string
	Key                                         ConversationKey
	Title, Tool, Subject, Why, Purpose, Preview string
	Workdir                                     string // 这张卡在哪个目录里弹的（会话选的项目目录，或全局默认）；审批卡上显示
	Notes                                       []string
	Options                                     []ApprovalChoice // "这一次"以外还能选哪几档（这个对话、记住）；空就只有这一次和拒绝
	KindLabel                                   string           // 这几档管的是哪一类，比如「派 reader 子任务」
	ExpiresAt                                   int64
	// Whisper 不空：这张卡来自悄悄话处理那一轮（Key 是她的悄悄话会话，主窗口里没有这个会话）。
	// 界面把卡放在来源会话 Source 的窗口里（规格-群聊引用和路由 8.3），卡上 Notes 第一行写明来历。
	Whisper *ApprovalWhisper
	// Egress 不空：这次调用参数里会把数据带出去的地址（悄悄话那一轮才有）。只是信息，不改放行；编号和对话日志里同一个号。
	Egress *ApprovalEgress
}

// ApprovalEgress 是审批卡上的外发地址：编号、每一处地址、危险的有几处（规格-群聊引用和路由 8.2）。
type ApprovalEgress struct {
	ID      string
	Targets []EgressTarget
	Danger  int
	// Unchecked：参数太多或嵌套太深，后面的没检查；卡上要说，免得“没有外发块”被当成检查过了
	Unchecked bool
}

// EgressTarget 是一处外发目标。Value 已压成一行；Danger 为真时 Reason 是给主人看的原因。
type EgressTarget struct {
	Kind, Value, Reason string
	Danger              bool
}

// ApprovalWhisper 是悄悄话审批卡的来历：来源会话、批号、被指的角色。
type ApprovalWhisper struct {
	Source           ConversationKey
	Batch, Character string
}

// ApprovalChoice 是主人在审批卡上的回答。
type ApprovalChoice string

const (
	ApproveOnce         ApprovalChoice = "once"
	ApproveConversation ApprovalChoice = "conversation"
	ApproveAlways       ApprovalChoice = "always" // 界面上叫"记住"
	ApproveDeny         ApprovalChoice = "deny"
)

// ApprovalRule 是一条放行规则（规格-审批分级第七节）。Scope 是 always（记住）或 conversation（这个对话）。
type ApprovalRule struct {
	Scope, Key, Label, Tool string
	Effect, Pattern         string // 主人自己写的规则才有：allow / ask / deny 和原文；点"记住"记下的是空
	Project, Session        string // 主人自己写的 allow、ask 规则的作用范围：只对这个项目目录、只对这个会话；都空是全局
	Conversation            string // 这个对话的规则属于哪个会话；记住的是空
	Dirs                    []string
	At, LastUsed            int64 // Unix 毫秒；LastUsed 为 0 是还没用过
}

// ApprovalRules 是"工具与审批"设置页的放行规则：看、撤销。撤销后下一次判定就要问。
type ApprovalRules interface {
	ListApprovalRules(context.Context) ([]ApprovalRule, error)
	RevokeApprovalRule(context.Context, ApprovalRule) error
}

// ApprovalRuleEditor 是放行规则页的"添加"：主人自己写 allow / ask / deny 规则（写法对齐 Claude Code，
// 如 Bash(git status:*)）。和"记住"共用同一份存储，列表、撤销走 ApprovalRules。写错了说清楚，不写进去。
// project 是项目目录（绝对路径）、session 是会话号，限定 allow、ask 规则只对它们生效，都空是全局；拒绝带范围会报错。
type ApprovalRuleEditor interface {
	AddApprovalRule(ctx context.Context, raw, effect, project, session string) error
}

// LocalChat 是桌面可选的本地私聊入口。SendLocal 等整轮收尾，错误照实返回；
// ctx 取消同时取消尚未开始的回合。调用方在 worker 使用，不能阻塞界面线程。
// 不接受 actor、audience、权限或工作目录；这些由本机宿主绑定。
type LocalChat interface {
	SendLocal(context.Context, ConversationKey, string, func(LocalDraft)) error
	StopLocal(context.Context, ConversationKey) error
	WatchLocalApprovals(context.Context) (Subscription[[]LocalApproval], error)
	DecideLocal(context.Context, ConversationKey, string, ApprovalChoice) error
}

// LocalQuestionOption 是问题卡上的一个选项。
type LocalQuestionOption struct{ Label, Detail string }

// LocalQuestion 是她在一轮中间问主人的问题（规格-向主人提问）。身份由后台绑定；回答不是批准。
type LocalQuestion struct {
	ID, CharacterID string
	Key             ConversationKey
	Text            string
	Options         []LocalQuestionOption
	ExpiresAt       int64
}

// LocalAnswerSkip 是 AnswerLocal 的“先不答”。
const LocalAnswerSkip = -1

// LocalQuestions 是桌面可选的问题卡入口。AnswerLocal 的 option 从 0 数；
// 问题已结束、不属于这个会话、下标越界都返回 ErrQuestionGone。打字回答走 LocalInputs.SubmitLocal。
type LocalQuestions interface {
	WatchLocalQuestions(context.Context) (Subscription[[]LocalQuestion], error)
	AnswerLocal(context.Context, ConversationKey, string, int) error
}

// LocalImageChat 接收本机上传的图片字节，不接受路径、URL 或已有媒体 hash。
// 每消息最多 16 张、每张 20 MiB、合计 80 MiB；与 LocalChat 共用身份和生命周期。
type LocalImageChat interface {
	SendLocalImages(context.Context, ConversationKey, string, [][]byte, func(LocalDraft)) error
}

// ConversationApprovalMode 是一个会话现在的审批档（manual / auto / yolo）。Override 为真是这个会话自己切过的，
// 为假是跟着全局默认 Default。
type ConversationApprovalMode struct {
	Mode, Default string
	Override      bool
}

// ConversationApproval 是输入框药丸的审批档：按会话切，当场生效，不动全局配置。
// 只有本地私聊能切；mode 传空是去掉这个会话的覆盖、回到默认。
type ConversationApproval interface {
	ConversationApprovalMode(context.Context, ConversationKey) (ConversationApprovalMode, error)
	SetConversationApprovalMode(ctx context.Context, k ConversationKey, mode string) error
}
