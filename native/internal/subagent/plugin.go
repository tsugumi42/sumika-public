package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sumika-native/internal/audience"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
	"time"
)

var Declarations = []tools.Declaration{
	{Name: "task", Description: "创建一层子任务，立即返回任务号；子任务看不到这段对话，prompt 要写全背景和要交回的内容，也不额外取得工具权限。agent 三选一：reader 只读本地文件（read、ls、grep、find，不能跑命令）；worker 读写文件并跑命令（每一步仍按所有者的审批规则走）；research 只联网查公开资料（网页功能没开时用不了）。任务在后台跑，用 task_result 查状态，完成后结果就在里面；不想要了用 task_cancel。", Parameters: `{"type":"object","properties":{"agent":{"type":"string","enum":["reader","research","worker"]},"description":{"type":"string","minLength":1,"maxLength":120},"prompt":{"type":"string","minLength":1}},"required":["agent","description","prompt"],"additionalProperties":false}`},
	{Name: "task_result", Description: "读取当前父任务所属子任务的状态和结果；任务号不是授权。子任务没结束时可用 wait_seconds（默认 0，最多 300）最多等这么久，结束了立刻返回，不必反复轮询。", Parameters: `{"type":"object","properties":{"task_id":{"type":"string"},"cursor":{"type":"string"},"wait_seconds":{"type":"integer","minimum":0,"maximum":300}},"required":["task_id"],"additionalProperties":false}`},
	{Name: "task_list", Description: "列出本会话自己派出的子任务（编号、类型、状态、开始时间、任务摘要），新的在前；只列自己的。this_run 为真的才能用 task_result、task_message、task_cancel 操作。", Parameters: `{"type":"object","properties":{},"additionalProperties":false}`},
	{Name: "task_message", Description: "给自己派出的、还在运行的子任务追加一句指示，它在下一步之前会看到；不扩大它的授权。任务已结束会报已结束，需要继续请用 task 再派一个。", Parameters: `{"type":"object","properties":{"task_id":{"type":"string"},"message":{"type":"string","minLength":1,"maxLength":16384}},"required":["task_id","message"],"additionalProperties":false}`},
	{Name: "task_cancel", Description: "取消当前父任务所属子任务；实际执行退出前仍占容量。", Parameters: `{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"],"additionalProperties":false}`},
}

type Plugin struct {
	Manager *Manager
	Host    ecosystem.ToolHost
}

func (*Plugin) ID() string { return "subagent" }
func (p *Plugin) Setup(r *turn.Registry) (func(), error) {
	if p.Manager == nil || p.Host.Access == nil || p.Host.Invocation == nil || p.Host.Visible == nil {
		return nil, fmt.Errorf("task 缺可信宿主端口")
	}
	for _, d := range Declarations {
		decl := d
		_, err := r.OnTool(turn.Tool{Decl: decl, For: func(a audience.Audience) bool { return p.Host.Visible(a, decl.Name) }, Exec: func(ctx context.Context, c turn.ToolCall) (tools.Result, error) { return p.execute(ctx, c, decl.Name) }})
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}
func (p *Plugin) execute(ctx context.Context, c turn.ToolCall, name string) (tools.Result, error) {
	if err := ecosystem.ValidateArgs(Declarations, name, c.Args); err != nil {
		return tools.Result{}, err
	}
	a, err := p.Host.Access(c)
	if err != nil {
		return tools.Result{}, err
	}
	inv, err := p.Host.Invocation(c)
	if err != nil {
		return tools.Result{}, err
	}
	var value any
	switch name {
	case "task":
		value, err = p.Manager.Start(ctx, a, inv, Request{c.Args.Obj["agent"].Str, c.Args.Obj["description"].Str, c.Args.Obj["prompt"].Str})
	case "task_result":
		if w := int(c.Args.Obj["wait_seconds"].Num); w > 0 {
			if err = p.Manager.Wait(ctx, a, inv, c.Args.Obj["task_id"].Str, time.Duration(w)*time.Second); err != nil {
				return tools.Result{}, err
			}
		}
		value, err = p.Manager.ReadPage(ctx, a, inv, c.Args.Obj["task_id"].Str, c.Args.Obj["cursor"].Str)
	case "task_list":
		value, err = p.Manager.List(ctx, a, inv)
	case "task_message":
		value, err = p.Manager.Message(ctx, a, inv, c.Args.Obj["task_id"].Str, c.Args.Obj["message"].Str)
	case "task_cancel":
		err = p.Manager.Cancel(ctx, a, inv, c.Args.Obj["task_id"].Str)
		value = map[string]string{"status": "stopping"}
	}
	if err != nil {
		return tools.Result{}, err
	}
	raw, err := json.Marshal(value)
	return tools.Result{Text: string(raw)}, err
}
