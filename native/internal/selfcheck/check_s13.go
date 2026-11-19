package selfcheck

import (
	"context"
	"strings"
)

// EmbedState 是本机嵌入现在的状态（app 从 embed.Local 取一份交给自检；自检包不 import embed）。
type EmbedState struct {
	Enabled bool
	Backend string // sumika.yml 的 embed.backend（空 = 默认 go-models）
	Status  string // off / starting / ready / down
	Reason  string // 没就绪的原因；缺模型时带完整路径
}

// embedUnavailable 是 S13 embed.unavailable：嵌入配了却起不来，或者 backend 还是已删的 legacy。
// 嵌入起不来时记忆检索退回全文检索、发送前检查只剩原文比较；缺模型时这里报出它在找的完整路径（Local 的原因里有），
// 让人知道该把模型放到哪。embed.enabled: false 是故意的，不算异常；启动中记 unknown，不急着报。
type embedUnavailable struct{}

func (embedUnavailable) ID() string   { return "embed.unavailable" }
func (embedUnavailable) Name() string { return "嵌入起不来或后端已移除" }

func (embedUnavailable) Eval(_ context.Context, e *Env) Result {
	r := Result{Threshold: 1}
	if e.Embed == nil {
		r.State, r.Note = Unknown, "没有接嵌入状态"
		return r
	}
	s := e.Embed()
	if !s.Enabled {
		r.State, r.Note = Ok, "嵌入关着（embed.enabled: false）"
		return r
	}
	if s.Backend == "legacy" {
		r.State, r.Value = Bad, 1
		r.Example = map[string]string{"backend": "legacy"}
		r.Note = "embed.backend: legacy 已移除（不再借外部的 Python 嵌入服务）"
		r.Next = "改成 go-models，并在 embed.modelsRoot 或 <数据目录>/models 里放好模型，然后重启接收"
		return r
	}
	switch s.Status {
	case "ready":
		r.State = Ok
		return r
	case "starting":
		r.State, r.Note = Unknown, "嵌入还在启动"
		return r
	}
	first, _, _ := strings.Cut(strings.TrimSpace(s.Reason), "\n")
	if first == "" {
		first = "嵌入没就绪（状态 " + s.Status + "）"
	}
	r.State, r.Value = Bad, 1
	r.Example = map[string]string{"status": s.Status, "reason": first}
	r.Note = first + "；记忆检索先只用全文检索"
	r.Next = "把模型放到它在找的目录里，修好以后重启接收"
	return r
}
