package turn

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"sumika-native/internal/artifact"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

type resultPlugin struct{ hub *Hub }

func (p resultPlugin) ID() string { return "kernel-results" }
func (p resultPlugin) Setup(r *Registry) (func(), error) {
	_, err := r.OnTool(Tool{Decl: tools.Declaration{Name: "result_read", Description: "按 UTF-8 字节续读本会话、本角色自己的工具结果。id 只能用工具返回的引用；offset 从 0 起，limit 含包装且不超过读取页上限。", Parameters: `{"type":"object","properties":{"id":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["id","offset","limit"],"additionalProperties":false}`}, Exec: func(ctx context.Context, c ToolCall) (tools.Result, error) {
		return readResult(ctx, p.hub.Cfg.Results, c.Identity.Scope, c.Args)
	}})
	return nil, err
}
func readResult(ctx context.Context, results kernel.ResultStore, scope kernel.Scope, args jsjson.Value) (tools.Result, error) {
	fail := func(err error) (tools.Result, error) { return tools.Result{Text: err.Error(), IsError: true}, nil }
	if args.Kind != jsjson.Object || args.Obj["id"].Kind != jsjson.String {
		return fail(fmt.Errorf("id 必须是工具返回的结果编号"))
	}
	num := func(name string) (int64, error) {
		v, ok := args.Obj[name]
		if !ok || v.Kind != jsjson.Number || math.IsNaN(v.Num) || math.IsInf(v.Num, 0) || v.Num != math.Trunc(v.Num) || v.Num < 0 || v.Num > float64(1<<53) {
			return 0, fmt.Errorf("%s 必须是非负整数字节数", name)
		}
		return int64(v.Num), nil
	}
	offset, err := num("offset")
	if err != nil {
		return fail(err)
	}
	limit, err := num("limit")
	if err != nil {
		return fail(err)
	}
	page, err := results.Read(ctx, scope, args.Obj["id"].Str, offset, limit)
	if err != nil {
		return fail(err)
	}
	text := fmt.Sprintf("[result %s bytes %d:%d next=%d eof=%t complete=%t]\n%s", page.Ref.ID, page.Start, page.End, page.Next, page.EOF, page.Ref.Complete, page.Text)
	if int64(len(text)) > limit {
		return fail(fmt.Errorf("结果包装超过 limit，请增大读取页"))
	}
	return tools.Result{Text: text, Partial: !page.Ref.Complete}, nil
}
func invocationFor(cfg Config, run string, c Call) kernel.Invocation {
	inv := cfg.invocation
	inv.RunID = run
	inv.ToolID = c.ToolID
	if inv.ToolID == "" {
		inv.ToolID = c.ID
	}
	inv.ProviderCallID = c.ID
	return inv
}
func toolStepID(c Call) string {
	if c.ToolID != "" {
		return c.ToolID
	}
	return c.ID
}

// finalizeTool 是执行事实→完整结果→steps→工作投影的唯一入口；失败不能补执行。
func finalizeTool(ctx context.Context, cfg Config, st *store.Store, c Call, r tools.Result) (Msg, error) {
	retained := false
	defer func() {
		if !retained && r.Disposition != nil {
			r.Disposition.Discard()
		}
	}()
	id := toolStepID(c)
	inv := invocationFor(cfg, cfg.invocation.RunID, c)
	if workEnabled(cfg, inv) && c.Name != "result_read" && !r.Controlled && r.Disposition == nil {
		if r.FullResult == nil {
			ref, err := cfg.Results.Put(ctx, inv, "text/plain; charset=utf-8", strings.NewReader(r.Text), !r.Partial)
			if err != nil {
				return Msg{}, fmt.Errorf("执行可能已完成，证据保存失败；不要重跑工具：%w", err)
			}
			r.FullResult = &ref
		}
		if len(r.Text) > int(cfg.WorkSettings.InlineBytes) || r.Partial {
			r.Text = artifact.Preview(r.Text, *r.FullResult, int(cfg.WorkSettings.InlineBytes))
		}
	}
	if err := st.StepEnd(id, r.Text, r.IsError, cfg.Now().UnixMilli(), r.Details); err != nil {
		return Msg{}, err
	}
	if r.FullResult != nil {
		if err := st.StepResult(id, *r.FullResult); err != nil {
			return Msg{}, err
		}
	}
	m := toolMessage(c.ID, r)
	m.Kind, m.Scope, m.ToolName, m.IsError = KindTool, inv.Scope.ScopeID, c.Name, r.IsError
	m.WorkStepID, m.ToolID = inv.StepID, inv.ToolID
	if c.Name == "todo_write" {
		m.TodoArgs = jsjson.Stringify(c.Args)
	}
	m.GroupEnd = c.GroupEnd
	if err := recordWork(context.WithoutCancel(ctx), cfg, m); err != nil {
		return Msg{}, err
	}
	retained = true
	return m, nil
}

type captureSink struct {
	capture *artifact.Capture
	cancel  context.CancelFunc
	used    bool
}

func (s *captureSink) Write(p []byte) (int, error) {
	s.used = true
	n, err := s.capture.Write(p)
	if err != nil {
		s.cancel()
	}
	return n, err
}
func captureTool(ctx context.Context, cfg Config, c Call, run func(context.Context) (tools.Result, error)) (tools.Result, error) {
	inv := invocationFor(cfg, cfg.invocation.RunID, c)
	results, ok := cfg.Results.(*artifact.Store)
	if !ok || !workEnabled(cfg, inv) || c.Name == "result_read" {
		return run(ctx)
	}
	capture, err := results.Begin(ctx, inv, "text/plain; charset=utf-8")
	if err != nil {
		return tools.Result{}, err
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	sink := &captureSink{capture: capture, cancel: cancel}
	r, runErr := run(tools.WithOutputSink(child, sink))
	if !sink.used {
		_, err = capture.Write([]byte(r.Text))
		if err != nil && !errors.Is(err, artifact.ErrLimit) {
			runErr = errors.Join(runErr, err)
		}
	}
	complete := ctx.Err() == nil && child.Err() == nil && runErr == nil && !r.Partial
	ref, err := capture.Finish(context.WithoutCancel(ctx), "text/plain; charset=utf-8", complete)
	if err != nil {
		return r, fmt.Errorf("执行可能已完成，证据保存失败；不要重跑工具：%w", err)
	}
	r.FullResult, r.Partial = &ref, !ref.Complete
	return r, runErr
}
