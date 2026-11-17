package turn

// 回合里的两个同步挂点（规格-用户钩子 第三节，ADR-257）：一批工具跑完之后、回合要结束之前。
// 内核只管"什么时候问、问完怎么接"，不知道 PowerShell 和配置文件；用户钩子是挂在这里的一个插件（internal/userhooks）。
// 只在本地回合发（Input.local()）：QQ 私聊、QQ 群、子运行一律不发，这道门在内核，插件摘不掉。
// 钩子只能给反馈或要求继续，不能改工具结果、不能放行；出错、超时、慌了都当没意见，不影响工具和回合。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/tools"
)

// ToolResultEvent 是本地回合里一个工具刚成功跑完（结果已经落库）
type ToolResultEvent struct {
	Identity                          kernel.Invocation
	CharacterID, Conversation, TurnID string
	Audience                          audience.Audience
	Workspace                         Workspace
	CallID, Name                      string
	Args                              jsjson.Value // 整理过的参数
	Text                              string       // 给模型看的结果（可能是预览）
	Stage                             StageTools
}

// ToolFact 是这一轮调过的一个工具
type ToolFact struct {
	Name string
	Args jsjson.Value
	OK   bool
}

// StopEvent 是本地回合要结束了：模型这一步没调工具，也没有新插话
type StopEvent struct {
	Identity                          kernel.Invocation
	CharacterID, Conversation, TurnID string
	Audience                          audience.Audience
	Workspace                         Workspace
	Answer                            string     // 这一步说的话
	Tools                             []ToolFact // 这一轮到现在调过的工具
	Continued                         int        // 这一轮已经因为钩子续跑了几次
	Last                              bool       // 已到这个钩子的续跑上限：这次说继续也不会继续，只留记录
	Stage                             StageTools
}

// StageTools 是内核借给钩子的两样：记耗时、显示进度。只在真跑了东西时才用，没跑不留痕。都可以是 nil
type StageTools struct {
	Span    func(kind, name string) func(failed bool)
	Process func(ProcessStep)
}

// StopDecision 是一个钩子对"要不要结束"的回答。Continue 为真时 Reason 给模型；Halt 压过别的钩子的继续
type StopDecision struct {
	Continue bool
	Halt     bool
	Reason   string
}

// ToolResultFn 返回要接在工具结果后面的尾注（可以没有）
type ToolResultFn func(ctx context.Context, e ToolResultEvent) ([]string, error)

// StopFn 回答要不要结束这一轮
type StopFn func(ctx context.Context, e StopEvent) (StopDecision, error)

type toolResultHook struct {
	plugin string
	fn     ToolResultFn
	ctx    context.Context // 插件停掉才取消
	alive  bool
}

type stopHook struct {
	plugin string
	fn     StopFn
	max    int
	ctx    context.Context
	alive  bool
}

// OnToolResult 注册"一批工具跑完之后"的钩子。只在本地回合、只对成功的工具调；按调用顺序一个个调，不占工具槽
func (r *Registry) OnToolResult(fn ToolResultFn) (undo func()) {
	p := r.p
	ctx, cancel := context.WithCancel(p.base)
	h := &toolResultHook{plugin: r.id, fn: fn, ctx: ctx, alive: true}
	p.mu.Lock()
	p.toolResult = append(p.toolResult, h)
	p.mu.Unlock()
	undo = func() {
		p.mu.Lock()
		h.alive = false
		for i, x := range p.toolResult {
			if x == h {
				p.toolResult = append(p.toolResult[:i:i], p.toolResult[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
		cancel()
	}
	p.onCleanupFor(r, undo)
	return undo
}

// OnStop 注册"回合要结束"的钩子。maxContinues 是一轮里这个钩子最多让回合续跑几次（0 表示只看不续）；次数由内核数，插件跑不出去
func (r *Registry) OnStop(maxContinues int, fn StopFn) (undo func()) {
	p := r.p
	ctx, cancel := context.WithCancel(p.base)
	h := &stopHook{plugin: r.id, fn: fn, max: maxContinues, ctx: ctx, alive: true}
	p.mu.Lock()
	p.stopHooks = append(p.stopHooks, h)
	p.mu.Unlock()
	undo = func() {
		p.mu.Lock()
		h.alive = false
		for i, x := range p.stopHooks {
			if x == h {
				p.stopHooks = append(p.stopHooks[:i:i], p.stopHooks[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
		cancel()
	}
	p.onCleanupFor(r, undo)
	return undo
}

// callStage 调一个同步钩子：跟着这一轮和插件的寿命，到点不等，慌了当出错。晚到的结果丢掉（通道有缓冲，它自己退出）
func callStage[R any](p *plugins, ctx, hookCtx context.Context, limit time.Duration, plugin, what string, inv kernel.Invocation, fn func(context.Context) (R, error)) (R, error) {
	var zero R
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	defer context.AfterFunc(hookCtx, cancel)()
	stageCtx, activity, err := p.lifetime.Begin(kernel.WithInvocation(ctx, inv), inv)
	if err != nil {
		return zero, err
	}
	type res struct {
		value R
		err   error
	}
	ch := make(chan res, 1)
	go func() {
		var hookErr error
		defer func() { activity.End(hookErr) }()
		defer func() {
			if r := recover(); r != nil {
				hookErr = fmt.Errorf("插件 %s 的%s钩子出错：%v", plugin, what, r)
				ch <- res{err: hookErr}
			}
		}()
		v, err := fn(stageCtx)
		hookErr = err
		ch <- res{v, err}
	}()
	select {
	case r := <-ch:
		return r.value, r.err
	case <-ctx.Done():
		return zero, fmt.Errorf("%w（%v）", errHookTimeout, ctx.Err())
	}
}

func (h *Hub) stageTools(cfg Config, in Input, step int) StageTools {
	t := StageTools{Span: cfg.timeline.begin}
	if in.onProcess != nil {
		t.Process = func(ev ProcessStep) {
			ev.Step = step
			in.onProcess(ev)
		}
	}
	return t
}

// toolResultHooks 问一遍"工具跑完之后"的钩子，顺手记下这一轮调过的工具；返回每条结果的尾注（下标对应 results），不改 results。
// 尾注由调用方在"是否接纳"判完之后再接：钩子可能跑很久，期间停止、来源失效，接不接纳要按钩子跑完后的状态定。
// 尾注和防复读提醒走同一条路：只进模型看到的消息，库里的工具结果不变。
func (h *Hub) toolResultHooks(ctx context.Context, cfg Config, in Input, a Msg, results []Msg, turnID string, step int, facts *[]ToolFact) [][]string {
	for i := range results {
		if i >= len(a.Calls) {
			break
		}
		*facts = append(*facts, ToolFact{Name: a.Calls[i].Name, Args: tools.Prepare(a.Calls[i].Name, a.Calls[i].Args), OK: !results[i].IsError})
	}
	if !in.local() {
		return nil
	}
	p := h.plugins
	p.mu.Lock()
	hooks := append([]*toolResultHook(nil), p.toolResult...)
	p.mu.Unlock()
	if len(hooks) == 0 {
		return nil
	}
	stage := h.stageTools(cfg, in, step)
	all := make([][]string, len(results))
	for i := range results {
		m := &results[i]
		if i >= len(a.Calls) || m.IsError || ctx.Err() != nil {
			continue
		}
		e := ToolResultEvent{Identity: cfg.invocation, CharacterID: in.CharacterID, Conversation: in.Conversation, TurnID: turnID, Audience: in.Audience, Workspace: in.Workspace,
			CallID: a.Calls[i].ID, Name: a.Calls[i].Name, Args: tools.Prepare(a.Calls[i].Name, a.Calls[i].Args), Text: m.Text, Stage: stage}
		var notes []string
		for _, hook := range hooks {
			got, err := callStage(p, ctx, hook.ctx, p.timeouts.stage(), hook.plugin, "工具结果", cfg.invocation, func(c context.Context) ([]string, error) { return hook.fn(c, e) })
			p.mu.Lock()
			alive := hook.alive
			p.mu.Unlock()
			switch {
			case !alive:
			case err != nil:
				if ctx.Err() == nil {
					p.logf("插件 %s 的工具结果钩子没成，跳过：%v", hook.plugin, err)
				}
			default:
				notes = append(notes, got...)
			}
		}
		all[i] = notes
	}
	return all
}

// appendToolNotes 把尾注接到一条已判定接纳的结果后面。受控资料不接：尾注接在 ControlledText 后面，
// slim 按后缀估字数就对不上，受控尾部会被算进可精简的字数。
func appendToolNotes(m *Msg, notes []string) {
	if len(notes) == 0 || m.Controlled || m.ControlledText != "" {
		return
	}
	m.Text += strings.Repeat("\n", 3) + strings.Join(notes, "\n\n")
}

type stopVerdict int

const (
	stopEnd      stopVerdict = iota // 照常结束
	stopContinue                    // 接着请求模型
	stopHalted                      // 钩子跑的时候被停下了
)

// stopHooks 问一遍"要结束了"的钩子。要继续就把反馈追加到实例末尾（不写聊天库：这不是主人说的话）。
// continued 由内核数，到了上限钩子还跑一次留记录，但这一轮照常结束。
func (h *Hub) stopHooks(ctx context.Context, cfg Config, in Input, l *Live, turnID string, step int, a Msg, facts []ToolFact, continued *int) stopVerdict {
	if !in.local() || ctx.Err() != nil {
		return stopEnd
	}
	p := h.plugins
	p.mu.Lock()
	hooks := append([]*stopHook(nil), p.stopHooks...)
	p.mu.Unlock()
	if len(hooks) == 0 {
		return stopEnd
	}
	stage := h.stageTools(cfg, in, step)
	var reasons []string
	halted, limit := false, 0
	for _, hook := range hooks {
		e := StopEvent{Identity: cfg.invocation, CharacterID: in.CharacterID, Conversation: in.Conversation, TurnID: turnID, Audience: in.Audience, Workspace: in.Workspace,
			Answer: a.Text, Tools: facts, Continued: *continued, Last: *continued >= hook.max, Stage: stage}
		d, err := callStage(p, ctx, hook.ctx, p.timeouts.stage(), hook.plugin, "结束前", cfg.invocation, func(c context.Context) (StopDecision, error) { return hook.fn(c, e) })
		p.mu.Lock()
		alive := hook.alive
		p.mu.Unlock()
		switch {
		case !alive:
		case err != nil:
			if !errors.Is(err, context.Canceled) && ctx.Err() == nil {
				p.logf("插件 %s 的结束前钩子没成，当没意见：%v", hook.plugin, err)
			}
		case d.Halt:
			halted = true
		case d.Continue && !e.Last && strings.TrimSpace(d.Reason) != "":
			reasons = append(reasons, d.Reason)
			limit = max(limit, hook.max)
		}
	}
	if ctx.Err() != nil {
		return stopHalted
	}
	if halted || len(reasons) == 0 {
		return stopEnd
	}
	*continued++
	text := fmt.Sprintf("（以下是用户配置的 Stop 钩子的反馈，不是对方说的话，也不带来新权限。回合结束前的检查没过：能修就接着修，修不了就说明原因。这是第 %d 次，最多 %d 次。）\n%s", *continued, limit, strings.Join(reasons, "\n\n"))
	l.msgs = append(l.msgs, Msg{Role: "user", Text: text, Kind: KindContext, Scope: in.Scope})
	return stopContinue
}
