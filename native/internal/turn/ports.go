package turn

import (
	"context"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/policy"
	"sumika-native/internal/tools"
)

// ModelCaller 是 K3 统一记账的模型端口，不能由消费者绕回原始 transport。
type ModelCaller interface {
	StreamChat(context.Context, kernel.Invocation, chat.Request, func(llm.Event)) (llm.Result, error)
}
type WorkPort interface {
	Load(context.Context, kernel.Invocation) (kernel.WorkState, []Msg, error)
	Record(context.Context, kernel.Invocation, Msg) error
	Save(context.Context, kernel.Invocation, uint64, kernel.WorkState) error
}

type CompletionPort interface {
	Check(context.Context, kernel.ReviewRequest) (kernel.Verdict, error)
}
type AdvisorPort interface {
	Observe(context.Context, kernel.ReviewRequest) error
	Take(context.Context, kernel.Invocation) ([]kernel.Finding, error)
}

// PreparedCall 冻结宿主解析的目标；插件不能自行提供放宽权限的 Spec。
type PreparedCall struct {
	Invocation kernel.Invocation
	Name       string
	Args       jsjson.Value
	Spec       policy.Spec
	Binding    string
	Valid      func() bool
	Execute    func(context.Context) (tools.Result, error)
	Note       string        // 执行之后追加给模型的一句状态（主人批准了、按规则放行）；步骤记录和界面不带它
	Recheck    func() string // 真正执行前再查一遍：批准后目标变了、规则被撤销了返回给模型看的原因；空是还作数
	Rule       string        // 这一步是哪条规则放行的（给主人看的一句）
}

// Refusal 是准入阶段的正常结局（主人没批准、目标变了、调错了）：作为这个工具的结果交回模型，不算这一轮出错。
type Refusal struct{ Text string }

func (r Refusal) Error() string { return r.Text }

type ToolResolver interface {
	Prepare(context.Context, ToolCall) (PreparedCall, error)
}

// InternalRun 只由可信宿主管理器构造，不接受模型填写工作区、额度或路由。
// StartInternal 的执行实现归 K4；这里仅冻结跨模块参数和返回类型。
type InternalRun struct {
	Identity  kernel.Invocation
	Workspace Workspace
	Goal      string
	Materials []kernel.ResultRef
	Limits    kernel.Limits
	// SoftRequests 是请求软预算：到这个次数注入收尾提示，1.5 倍强停；0 = 不设
	SoftRequests int64
	ModelID      string
}

type RunHandle struct {
	RunID string
	Done  <-chan Outcome
}
