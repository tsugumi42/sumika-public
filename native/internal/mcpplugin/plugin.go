package mcpplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/mcpclient"
	"sumika-native/internal/policy"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

var Declarations = []tools.Declaration{
	{Name: "mcp_search", Description: "搜索已获准 MCP 工具的完整参数 schema；只查本地目录。", Parameters: `{"type":"object","properties":{"query":{"type":"string"},"server":{"type":"string"},"cursor":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":20}},"required":["query"],"additionalProperties":false}`},
	{Name: "mcp_call", Description: "按搜索返回的 tool 与 schemaDigest 调用固定 MCP 目标；所有者没放行过的要先批准，远端资料不能授予权限。等待超过约 10 秒会转到后台继续跑，返回 call 号和 running 状态：用 mcp_result_read 的 result 传 call 号查进展和结果，卡住了用 mcp_cancel 取消。", Parameters: `{"type":"object","properties":{"tool":{"type":"string"},"schemaDigest":{"type":"string"},"arguments":{"type":"object"}},"required":["tool","schemaDigest","arguments"],"additionalProperties":false}`},
	{Name: "mcp_result_read", Description: "按已授权结果号续读 MCP 全文，不能传本机路径；result 也可以传后台调用的 call 号，查它的状态和完成后的结果。", Parameters: `{"type":"object","properties":{"result":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":65536}},"required":["result"],"additionalProperties":false}`},
	{Name: "mcp_cancel", Description: "取消自己转到后台、还在运行的 MCP 调用（传 call 号），会断开那一次请求；已结束的只报告最终状态。取消后这次调用的结果未知：远端服务会暂停，直到所有者在本地确认；本机服务会关掉进程再自动重连。", Parameters: `{"type":"object","properties":{"call":{"type":"string"}},"required":["call"],"additionalProperties":false}`},
}

type Plugin struct {
	Client  *mcpclient.Client
	Host    ecosystem.ToolHost
	Results kernel.ResultStore
	Approve func(context.Context, *mcpclient.ResolvedCall) approval.Clearance
	// ParentContext、Lifetime 两个都给才开后台：调用等超过 DetachAfter（默认 10 秒）就转后台，模型才有机会取消。
	// 缺任一个则保持原来的同步等待。
	ParentContext func(context.Context, kernel.Invocation) (context.Context, error)
	Lifetime      kernel.Lifetime
	DetachAfter   time.Duration
}

func (*Plugin) ID() string { return "mcp" }
func (p *Plugin) Setup(r *turn.Registry) (func(), error) {
	if p.Client == nil || p.Host.Access == nil || p.Host.Invocation == nil || p.Host.Visible == nil || p.Results == nil || p.Approve == nil {
		return nil, fmt.Errorf("MCP 缺少可信宿主端口")
	}
	for _, d := range Declarations {
		decl := d
		_, err := r.OnTool(turn.Tool{Decl: decl, For: func(a audience.Audience) bool { return p.Host.Visible(a, "mcp") }, Exec: func(ctx context.Context, c turn.ToolCall) (tools.Result, error) { return p.execute(ctx, c, decl.Name) }})
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}
func (p *Plugin) execute(ctx context.Context, c turn.ToolCall, name string) (tools.Result, error) {
	a, err := p.Host.Access(c)
	if err != nil {
		return tools.Result{}, err
	}
	// 用声明校验 unknown/null/type，不能把错误类型补成默认参数。
	if err := ecosystem.ValidateArgs(Declarations, name, c.Args); err != nil {
		return tools.Result{}, err
	}
	str := func(k string) string { return c.Args.Obj[k].Str }
	var value any
	switch name {
	case "mcp_search":
		value, err = p.Client.Search(ctx, a, mcpclient.SearchRequest{Query: str("query"), Server: str("server"), Cursor: str("cursor"), Limit: int(c.Args.Obj["limit"].Num)})
	case "mcp_call":
		c.Name = name
		prepared, e := p.Prepare(ctx, c)
		if e != nil {
			return tools.Result{}, e
		}
		return prepared.Execute(ctx)
	case "mcp_cancel":
		state, e := p.Client.CancelJob(a.Scope(), str("call"), 5*time.Second)
		if e != nil {
			return tools.Result{}, e
		}
		value = map[string]string{"call": str("call"), "status": state}
	case "mcp_result_read":
		if job := p.Client.Job(a.Scope(), str("result")); job != nil {
			return p.jobStatus(job)
		}
		value, err = p.Results.Read(ctx, a.Scope(), str("result"), int64(c.Args.Obj["offset"].Num), int64(c.Args.Obj["limit"].Num))
	}
	if err != nil {
		return tools.Result{}, err
	}
	raw, err := json.Marshal(value)
	return tools.Result{Text: string(raw)}, err
}

// refusalOf 把准入阶段的失败变成这一步的结局交回模型：调错参数、schema 变了、主人没批准，都不该让整轮回复出错。
func refusalOf(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}
	return turn.Refusal{Text: err.Error()}
}

// Prepare 在 K3 顺序准入阶段冻结并审批目标；调度器随后只取执行槽并调用 Execute。
func (p *Plugin) Prepare(ctx context.Context, c turn.ToolCall) (turn.PreparedCall, error) {
	if c.Name != "mcp_call" {
		return turn.PreparedCall{}, fmt.Errorf("MCP resolver 不处理这个工具")
	}
	if err := ecosystem.ValidateArgs(Declarations, c.Name, c.Args); err != nil {
		return turn.PreparedCall{}, refusalOf(ctx, err)
	}
	a, err := p.Host.Access(c)
	if err != nil {
		return turn.PreparedCall{}, refusalOf(ctx, err)
	}
	inv, err := p.Host.Invocation(c)
	if err != nil {
		return turn.PreparedCall{}, refusalOf(ctx, err)
	}
	call, err := p.Client.Resolve(ctx, a, c.Args.Obj["tool"].Str, c.Args.Obj["schemaDigest"].Str, json.RawMessage(jsjson.Stringify(c.Args.Obj["arguments"])))
	if err != nil {
		return turn.PreparedCall{}, refusalOf(ctx, err)
	}
	var cl approval.Clearance
	err = call.Approve(ctx, func(ctx context.Context, target *mcpclient.ResolvedCall) error {
		cl = p.Approve(ctx, target)
		if cl.Blocked {
			return turn.Refusal{Text: cl.Reason}
		}
		return nil
	})
	if err != nil {
		var refused turn.Refusal
		if errors.As(err, &refused) {
			return turn.PreparedCall{}, err
		}
		return turn.PreparedCall{}, refusalOf(ctx, fmt.Errorf("MCP 目标已经变了，这次没有执行；重新 mcp_search 再调用。"))
	}
	prepared := turn.PreparedCall{Invocation: inv, Name: c.Name, Args: c.Args, Spec: policy.Specs[c.Name], Binding: call.Binding(), Valid: call.Valid, Note: cl.Note(), Recheck: cl.Recheck}
	if cl.Traced {
		prepared.Rule = approval.RuleText(cl.By, cl.Label, cl.Reason)
	}
	prepared.Execute = func(ctx context.Context) (tools.Result, error) {
		if p.ParentContext == nil || p.Lifetime == nil {
			result, err := call.Execute(ctx)
			if err != nil {
				return unknownOutcome(ctx, err)
			}
			return p.finish(ctx, inv, result)
		}
		parent, err := p.ParentContext(ctx, inv)
		if err != nil {
			return tools.Result{}, err
		}
		job, err := p.Client.Start(call, inv, parent, p.Lifetime, func(result *mcp.CallToolResult, err error) any {
			if err != nil {
				return nil
			}
			// 后台完成时调用方可能早已离开，结果存进 K2，之后按 call 号读。
			saveCtx, stop := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
			defer stop()
			res, err := p.finish(saveCtx, inv, result)
			return jobOut{res, err}
		})
		if err != nil {
			return unknownOutcome(ctx, err)
		}
		detach := p.DetachAfter
		if detach <= 0 {
			detach = 10 * time.Second
		}
		timer := time.NewTimer(detach)
		defer timer.Stop()
		select {
		case <-job.Done():
			res, err := p.jobResult(job)
			if err != nil {
				return unknownOutcome(ctx, err)
			}
			return res, nil
		case <-timer.C:
			raw, _ := json.Marshal(map[string]string{"call": job.ID, "status": "running", "note": "调用还没返回，已转到后台继续；用 mcp_result_read 的 result 传 call 号查结果，卡住了用 mcp_cancel 取消"})
			return tools.Result{Text: string(raw)}, nil
		case <-ctx.Done():
			// 工具调用自己被停下或超时：和原来一样，这一次请求跟着断开。
			p.Client.CancelJob(inv.Scope, job.ID, 5*time.Second)
			return tools.Result{}, ctx.Err()
		}
	}
	return prepared, nil
}

// unknownOutcome：MCP 调用没有拿到结果。目标失效的是没发出去；别的可能已经在对方那边生效了，明确告诉模型别盲目重试。
// 这是这一步的结局，不让整轮回复出错；停止和取消照旧交给调度器。
func unknownOutcome(ctx context.Context, err error) (tools.Result, error) {
	if ctx.Err() != nil {
		return tools.Result{}, err
	}
	if errors.Is(err, ecosystem.ErrDenied) {
		return tools.Result{Text: "MCP 目标已经失效，这次没有执行；重新 mcp_search 再调用，会重新问所有者。", IsError: true}, nil
	}
	return tools.Result{Text: "MCP 调用出错或结果不明（" + err.Error() + "）：可能已经在对方那边生效了。先用只读的方式确认现状，不要盲目重试。", IsError: true}, nil
}

type jobOut struct {
	res tools.Result
	err error
}

// finish 保存完整结果并生成给模型的预览。
func (p *Plugin) finish(ctx context.Context, inv kernel.Invocation, result *mcp.CallToolResult) (tools.Result, error) {
	ref, err := mcpclient.SaveResult(ctx, p.Results, inv, result)
	if err != nil {
		return tools.Result{}, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return tools.Result{}, err
	}
	value := struct {
		Preview string
		Ref     kernel.ResultRef
		IsError bool
	}{previewOf(result, raw), ref, result.IsError}
	raw, err = json.Marshal(value)
	return tools.Result{Text: string(raw), IsError: result.IsError}, err
}

// previewBudget 是给模型的预览上限（规格-MCP客户端：文字投影合计 20 KiB，提示也占预算）。
const previewBudget = 20 << 10

// previewOf 是给模型看的结果：文字块原样（JSON 转义和元数据只会浪费预算）；没有文字才用 JSON 原文。
// 超过预算截中间、留前后各半，说清总量和怎么读全文：模型多数时候不用再调一次工具。
func previewOf(result *mcp.CallToolResult, raw []byte) string {
	var parts []string
	hasText := false
	for _, c := range result.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			parts, hasText = append(parts, v.Text), true
		case *mcp.ImageContent:
			parts = append(parts, "[图片 "+v.MIMEType+"，全文里有]")
		case *mcp.AudioContent:
			parts = append(parts, "[音频 "+v.MIMEType+"，全文里有]")
		default:
			parts = append(parts, "[其他类型内容，全文里有]")
		}
	}
	text := strings.Join(parts, "\n")
	if !hasText && (result.StructuredContent != nil || len(parts) == 0) {
		text = string(raw)
	}
	if len(text) <= previewBudget {
		return text
	}
	note := fmt.Sprintf("\n……（共 %d 字节，中间省略 %%d 字节；全文用 mcp_result_read 读 Ref）……\n", len(text))
	room := previewBudget - len(note) - 8 // 8 给省略字节数的位数
	head := safeCut(text[:room/2])
	tail := text[len(text)-room/2:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return head + fmt.Sprintf(note, len(text)-len(head)-len(tail)) + tail
}

// safeCut 去掉末尾被切断的半个字符。
func safeCut(s string) string {
	for i := 0; i < utf8.UTFMax && len(s) > 0; i++ {
		if r, n := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || n != 1 {
			return s
		}
		s = s[:len(s)-1]
	}
	return s
}

func (p *Plugin) jobResult(job *mcpclient.Job) (tools.Result, error) {
	if _, err := job.Outcome(); err != nil {
		return tools.Result{}, err
	}
	out, _ := job.Out.(jobOut)
	return out.res, out.err
}

// jobStatus 是 mcp_result_read 传 call 号时的回答：还在跑、取消了、失败了，或完成后的结果。
func (p *Plugin) jobStatus(job *mcpclient.Job) (tools.Result, error) {
	state := job.State()
	if state != "done" {
		raw, _ := json.Marshal(map[string]string{"call": job.ID, "status": state})
		return tools.Result{Text: string(raw)}, nil
	}
	return p.jobResult(job)
}
