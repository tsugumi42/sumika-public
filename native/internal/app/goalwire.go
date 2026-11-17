package app

// /goal 接进本地私聊（规格-目标和循环 第二节、第四节第 2 步）。
//
//   - 命令：/goal 设目标（并把目标原文当这一轮的输入发给她）、/goal 看状态、edit 改、clear 清除、pause、resume；
//   - 每轮结束：订阅内核的"回合定了"，只在本地私聊主人的回合上排一个后台任务（不在钩子里等模型），
//     任务里调 goal.Manager.AfterTurn，按 Decision 续轮、显示达成或暂停；
//   - 续轮的那句话先存成一条主人身份、来源为 goal 的消息（界面显示成系统发的，历史里她看得到），再交内核跑；
//   - 停止：停止按钮和 /goal clear、pause 都会取消正在跑的自动轮和裁判；轮次被停下也会把目标暂停。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/askowner"
	"sumika-native/internal/goal"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
	"sumika-native/internal/turnbudget"
)

// 裁判一次最多等多久。
const goalJudgeTimeout = 3 * time.Minute

// autoRuns 记着每个会话里宿主自动发起的活动（目标的裁判和续轮、定期任务的一轮）的取消函数：
// 停止按钮据此取消它们，主人在自动轮进行时发普通消息得到"正在生成"。
type autoRuns struct {
	mu       sync.Mutex
	m        map[string]context.CancelCauseFunc
	onChange func(conv string) // 登记或放掉时通知界面刷新状态
}

func (r *autoRuns) keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.m))
	for k := range r.m {
		out = append(out, k)
	}
	return out
}

func (r *autoRuns) changed(conv string) {
	if r.onChange != nil {
		r.onChange(conv)
	}
}

// set 登记新的活动，取消同一会话里上一个（它已经结束，只是放掉资源）。
func (r *autoRuns) set(conv string, c context.CancelCauseFunc) {
	r.mu.Lock()
	if r.m == nil {
		r.m = map[string]context.CancelCauseFunc{}
	}
	prev := r.m[conv]
	r.m[conv] = c
	r.mu.Unlock()
	if prev != nil {
		prev(goal.ErrSuperseded) // 被新的活动顶替：裁判据此知道自己不是被停止的，不要把目标暂停
	}
	r.changed(conv)
}

// clear 放掉这个会话的登记（活动结束了）。
func (r *autoRuns) clear(conv string) {
	r.mu.Lock()
	c := r.m[conv]
	delete(r.m, conv)
	r.mu.Unlock()
	if c != nil {
		c(nil)
		r.changed(conv)
	}
}

func (r *autoRuns) has(conv string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[conv] != nil
}

// stopAll 取消全部（关机用）。
func (r *autoRuns) stopAll() {
	r.mu.Lock()
	cs := r.m
	r.m = nil
	r.mu.Unlock()
	for _, c := range cs {
		c(nil)
	}
}

type goalCtxKey struct{}

type goalCtx struct{ character, conv string }

// goalJudge 用后台模型当 /goal 的裁判。
type goalJudge struct{ app *App }

func (j goalJudge) Complete(ctx context.Context, prompt string) (string, error) {
	gc, _ := ctx.Value(goalCtxKey{}).(goalCtx)
	a := j.app
	model := a.backgroundModel(gc.character)
	if model == "" {
		return "", goal.Unavailable(errors.New("没有配置后台模型（defaults.backgroundModel），裁判没法判断"))
	}
	r, err := memoryGenerator{app: a, character: gc.character, kind: kernel.KindGoalJudge, conversation: gc.conv}.Call(ctx, model, prompt, "goal:"+gc.conv)
	if err != nil {
		var se *llm.StatusError
		switch {
		case errors.Is(err, turnbudget.ErrExhausted):
			return "", goal.Unavailable(errors.New("额度用完了"))
		case errors.As(err, &se) && (se.Code == 401 || se.Code == 402 || se.Code == 403 || se.Code == 404):
			return "", goal.Unavailable(err)
		}
		return "", err
	}
	return r.Text, nil
}

// goalPlugin 订阅"回合定了"。
type goalPlugin struct{ app *App }

func (goalPlugin) ID() string { return "goal" }
func (p goalPlugin) Setup(r *turn.Registry) (func(), error) {
	return r.OnSettled(func(_ context.Context, s turn.Settled) { p.app.goalSettled(s) }), nil
}

func (a *App) openGoals() error {
	m, err := goal.NewManager(goal.NewFileStore(a.paths.GoalsDir()), goalJudge{app: a}, goal.Config{Now: a.now})
	if err != nil {
		a.logf("目标文件有读不了的：%v", err)
	}
	a.goals = m
	a.auto.onChange = a.statusChanged
	a.slashes.add(goal.Command, a.slashGoal)
	return a.hub.Use(goalPlugin{app: a})
}

func (a *App) goalActive(conv string) bool {
	if a.goals == nil {
		return false
	}
	g, ok := a.goals.Get(conv)
	return ok && g.Status == goal.StatusActive
}

// goalSettled 在内核钩子的队列里被调：只判断要不要处理，处理放到后台任务里。
func (a *App) goalSettled(s turn.Settled) {
	// 只认本地私聊主人的回合：别的场合即使会话号碰巧有目标也不处理
	if a.goals == nil || a.closing.Load() || !ownerLocalPrivate(s.Identity.Scope) || !a.goalActive(s.Conversation) {
		return
	}
	switch s.Outcome {
	case turn.OutcomeStopped:
		a.pauseGoal(s.Conversation, "主人停止")
		return
	case turn.OutcomeError:
		reason := "这一轮出错了"
		if s.Err != nil {
			reason += "：" + clipText(s.Err.Error(), 120)
		}
		a.pauseGoal(s.Conversation, reason)
		a.attend(AttentionGoalPaused, s.Conversation, "目标暂停了，要你看一眼", reason)
		return
	}
	a.autoWG.Add(1)
	go func() {
		defer a.autoWG.Done()
		a.goalWorker(s)
	}()
}

func (a *App) goalWorker(s turn.Settled) {
	conv, char := s.Conversation, s.CharacterID
	ctx, cancel := context.WithCancelCause(a.autoCtx)
	a.auto.set(conv, cancel)
	// 她问完主人就结束了这一轮：没等到回答、自己也定不了，别再自动续（问完接着干的不算）
	asked := len(s.Tools) > 0 && s.Tools[len(s.Tools)-1].Name == askowner.ToolName
	res := goal.TurnResult{Answer: s.Answer, Tokens: s.Tokens, NeedsUser: a.pendingApproval(conv) || asked}
	for _, t := range s.Tools {
		res.Tools = append(res.Tools, goal.ToolSummary{Name: t.Name, OK: t.OK, Result: t.Result})
	}
	jctx, jcancel := context.WithTimeout(context.WithValue(ctx, goalCtxKey{}, goalCtx{character: char, conv: conv}), goalJudgeTimeout)
	d := a.goals.AfterTurn(jctx, conv, res)
	jcancel()
	if d.Kind == goal.Idle && errors.Is(context.Cause(ctx), goal.ErrSuperseded) {
		return // 判到一半，主人插话排的新一轮已经结束并接手，登记和状态都归新的那个
	}
	a.statusChanged(conv)
	switch d.Kind {
	case goal.Continue:
		if err := a.goalLaunch(conv, char, d.Prompt); err != nil {
			a.pauseGoal(conv, "自动续轮没发出去："+err.Error())
			a.attend(AttentionGoalPaused, conv, "目标暂停了，要你看一眼", "自动续轮没发出去："+err.Error())
			a.auto.clear(conv)
		}
		return
	case goal.Achieved:
		a.noteLogf(a.systemNotice(conv, noticeGoal, "目标达成："+d.Reason))
		a.attend(AttentionGoalDone, conv, "目标达成", clipText(d.Goal.Text, 40)+"："+d.Reason)
	case goal.Paused:
		a.noteLogf(a.systemNotice(conv, noticeGoal, "目标已暂停："+d.Reason+"。/goal resume 继续，/goal clear 结束"))
		a.attend(AttentionGoalPaused, conv, "目标暂停了，要你看一眼", d.Reason)
	}
	a.auto.clear(conv)
}

func (a *App) noteLogf(err error) {
	if err != nil {
		a.logf("系统行没写进去：%v", err)
	}
}

// goalLaunch 自动发一轮：这句话先存成"主人身份、来源 goal"的消息，再交内核。会话里已经有一轮在跑就不发
// （那一轮结束时会再走一遍轮末处理）。
func (a *App) goalLaunch(conv, char, prompt string) error {
	if a.hub.Running(char, conv) {
		return nil
	}
	st, err := a.set.ForConversation(conv)
	if err != nil {
		return err
	}
	seq, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: "goal", Kind: "message", Text: prompt, Segments: selftestSegs(conv, nil), CreatedAt: a.now().UnixMilli()}, []string{char})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancelCause(a.autoCtx)
	a.auto.set(conv, cancel)
	out := a.hub.SendLocalStored(ctx, char, conv, prompt, turn.StoredSend{Seqs: []int64{seq}, Auto: true}, nil)
	a.autoWG.Add(1)
	go func() {
		defer a.autoWG.Done()
		// 普通出错走"回合定了"；这里只接住没跑起来的（受理失败），免得目标挂着、会话一直显示忙
		if o := <-out; o.Err != nil && !errors.Is(o.Err, context.Canceled) {
			a.pauseGoal(conv, "自动续轮没跑起来："+clipText(o.Err.Error(), 120))
			a.attend(AttentionGoalPaused, conv, "目标暂停了，要你看一眼", "自动续轮没跑起来："+clipText(o.Err.Error(), 120))
			a.auto.clear(conv)
		}
	}()
	return nil
}

// pauseGoal 暂停进行中的目标并记一行；已经不是进行中就什么都不做。
func (a *App) pauseGoal(conv, reason string) {
	if a.closing.Load() || !a.goalActive(conv) {
		return
	}
	if err := a.goals.Pause(conv, reason); err == nil {
		a.noteLogf(a.systemNotice(conv, noticeGoal, "目标已暂停："+reason+"。/goal resume 继续，/goal clear 结束"))
	}
}

// autoStop 是停止按钮和 /goal pause、clear 用的：先暂停目标，再取消正在跑的自动轮和裁判。
func (a *App) autoStop(conv, reason string) {
	if a.goals != nil {
		a.pauseGoal(conv, reason)
	}
	a.auto.clear(conv)
}

func (a *App) pendingApproval(conv string) bool {
	if a.approvals == nil {
		return false
	}
	for _, s := range a.approvals.Snapshot() {
		if a.cardConversation(s.Request) == conv && s.Decision == nil {
			return true
		}
	}
	return false
}

func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

const goalHelp = "用法：/goal <目标> 设目标；/goal 看状态；/goal edit <新目标> 改目标；/goal pause、resume 暂停和继续；/goal clear 结束"

func (a *App) slashGoal(_ context.Context, c slashCall) (slashResult, error) {
	cmd, arg, ok := goal.ParseCommand(c.Text)
	if !ok {
		return slashResult{}, errors.New("认不出这条 /goal 命令")
	}
	conv, char := c.Conv, c.Character
	reply := func(s string) (slashResult, error) { return slashResult{Reply: s, Source: noticeGoal}, nil }
	setNew := func(g goal.Goal) (slashResult, error) {
		return slashResult{Reply: fmt.Sprintf("目标已设定：%s。最多 %d 轮，到数先收尾再暂停；/goal 看状态，/goal clear 结束", clipText(g.Text, 60), g.Budget.MaxRounds), Send: g.Text, Source: noticeGoal}, nil
	}
	busy := func() error {
		if a.hub.Running(char, conv) || a.auto.has(conv) {
			return errors.New("这个会话正在生成，先点停止或等这一轮说完，再设目标")
		}
		return nil
	}
	switch cmd {
	case goal.CmdSet:
		if err := busy(); err != nil {
			return slashResult{}, err
		}
		g, err := a.goals.Set(conv, arg)
		if err != nil {
			return slashResult{}, err
		}
		return setNew(g)
	case goal.CmdEdit:
		if strings.TrimSpace(arg) == "" {
			return slashResult{}, errors.New("要写新目标，例如 /goal edit 把报告写完并检查错别字")
		}
		if _, exists := a.goals.Get(conv); !exists {
			if err := busy(); err != nil {
				return slashResult{}, err
			}
		}
		g, created, err := a.goals.Edit(conv, arg)
		if err != nil {
			return slashResult{}, err
		}
		if created {
			return setNew(g)
		}
		return reply("目标改为：" + clipText(g.Text, 80))
	case goal.CmdStatus:
		g, ok := a.goals.Get(conv)
		if !ok {
			return reply("这个会话没有目标。" + goalHelp)
		}
		line := goal.StatusLine(g)
		if g.Last != nil && g.Last.Reason != "" {
			line += "。上次判断：" + g.Last.Reason
		}
		return reply(line)
	case goal.CmdClear:
		cleared, err := a.goals.Clear(conv)
		a.auto.clear(conv) // 先清目标再取消正在跑的轮，免得那一轮被停下时又被记成"暂停"
		if err != nil {
			return slashResult{}, err
		}
		if !cleared {
			return reply("这个会话没有目标")
		}
		return reply("目标已清除")
	case goal.CmdPause:
		cur, ok := a.goals.Get(conv)
		if !ok {
			return reply("这个会话没有目标")
		}
		if cur.Status != goal.StatusActive {
			return reply(goal.StatusLine(cur))
		}
		a.autoStop(conv, "主人暂停")
		return reply("") // pauseGoal 已经记了一行
	case goal.CmdResume:
		g, err := a.goals.Resume(conv)
		if errors.Is(err, goal.ErrNoGoal) {
			return reply("这个会话没有目标")
		}
		if err != nil {
			return slashResult{}, err
		}
		if err := a.goalLaunch(conv, char, "继续做这个目标。"); err != nil {
			return slashResult{}, err
		}
		return reply("目标继续：" + clipText(g.Text, 60))
	}
	return slashResult{}, errors.New("不认识的 /goal 子命令")
}

// stopAuto 关机时取消全部自动活动并等后台任务退出。
func (a *App) stopAuto() {
	if a.autoCancel != nil {
		a.autoCancel()
	}
	a.auto.stopAll()
	a.autoWG.Wait()
}
