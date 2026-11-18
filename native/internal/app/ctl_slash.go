package app

// sumika-ctl 的 goal、loop 子命令（规格-本机控制接口第十节）的后台一侧。
//
// 命令行把子命令拼成和主窗口输入框里一样的斜杠命令全文，这里用同一张命令表（slashes）和同一批处理函数执行
// （/goal 设目标、暂停、继续，/loop 新建、修改、停止），不另写一套；会话固定是角色的自检话题
// （和 chat 同一个，话题里存的行带 selftest 标记），所以碰不到真实的聊天记录、QQ 或群聊。
// 命令的回复和输入框里一样存成一条系统行，主人在界面上看得到是谁改了目标。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/goal"
	"sumika-native/internal/loop"
)

// ErrSlashInvalid 是命令行拼出来的命令不合格（写法不对、拼出来变成了别的子命令……）；包在错误里，接线层回 invalid
var ErrSlashInvalid = errors.New("命令不合格")

func slashInvalid(format string, a ...any) error {
	return fmt.Errorf("%w：%s", ErrSlashInvalid, fmt.Sprintf(format, a...))
}

// SlashCommandRequest 是 sumika-ctl 的一条 goal 或 loop 命令。Text 是拼好的斜杠命令全文，Sub 是命令行里的子命令名
type SlashCommandRequest struct {
	Command   string // goal / loop
	Sub       string
	Character string
	Text      string
	ID        int
	Loop      *ctlproto.LoopPatch // loop add|edit 带了日程、聊天方式或通知档：走定期任务页同一个入口，Text 只是任务文字
}

// GoalView、LoopView、SlashCommandResult 是命令之后这个会话的现状
type GoalView struct {
	Text, Status, PauseReason, LastJudge, Line string
	Rounds, MaxRounds                          int
	TokensUsed                                 int64
}

type LoopView struct {
	ID          int
	Status      string
	Prompt      string
	IntervalSec int64
	Runs        int
	NextRun     time.Time
	Line        string
	Kind        string
	At          string
	Days        []int // 周一是 1、周日是 7
	Notify      string
	NewChat     bool
}

type SlashCommandResult struct {
	Character, Topic, Conversation string
	Reply                          string
	Started                        bool
	Goal                           *GoalView
	Loops                          []LoopView
}

// goalSubCmd、loopSubKind 是命令行子命令和解析结果的对应；拼出来的全文被解析成别的，说明目标或任务文字撞了子命令词
var goalSubCmd = map[string]string{"show": goal.CmdStatus, "set": goal.CmdSet, "edit": goal.CmdEdit, "pause": goal.CmdPause, "resume": goal.CmdResume, "clear": goal.CmdClear}
var loopSubKind = map[string]loop.CmdKind{"list": loop.CmdList, "add": loop.CmdAdd, "edit": loop.CmdEdit, "pause": loop.CmdPause, "resume": loop.CmdResume, "remove": loop.CmdStop}

// checkSlash 核对拼好的全文解析出来正是命令行要的那件事（目标文字恰好是 pause、任务文字以 stop 3 开头这类会变成别的子命令）
func checkSlash(req SlashCommandRequest) error {
	switch req.Command {
	case "goal":
		cmd, _, ok := goal.ParseCommand(req.Text)
		if !ok || cmd != goalSubCmd[req.Sub] {
			return slashInvalid("目标文字和子命令词撞了（%q），换个说法写", req.Sub)
		}
	case "loop":
		c, ok, err := loop.ParseCommand(req.Text)
		if err != nil {
			return fmt.Errorf("%w：%v", ErrSlashInvalid, err)
		}
		if !ok || c.Kind != loopSubKind[req.Sub] {
			return slashInvalid("任务文字以 stop、pause、resume、edit 这类子命令词开头，会被当成别的命令；换个说法写")
		}
		if req.ID != 0 && c.ID != req.ID {
			return slashInvalid("任务编号对不上")
		}
	case "trust":
		if req.Text != "/trust" || req.Sub != "" {
			return slashInvalid("trust 不带参数")
		}
	default:
		return slashInvalid("不认识的命令")
	}
	return nil
}

// SlashCommand 执行一条 goal、loop 命令，交出命令之后的目标和定期任务现状。
// 设目标、继续目标、新建定期任务会让她排上一轮（设目标时目标原文当主人这一条消息发给她）；一轮在后台跑，这里不等。
func (r *DesktopReader) SlashCommand(ctx context.Context, req SlashCommandRequest) (SlashCommandResult, error) {
	if err := r.bunOwned(); err != nil {
		return SlashCommandResult{}, err
	}
	a := r.desk.app
	if a.opt.Mode == Archive {
		return SlashCommandResult{}, api.ErrLocalChatUnsupported
	}
	if a.slashes == nil || a.goals == nil || a.loops == nil {
		return SlashCommandResult{}, errors.New("目标和定期任务还没装配好")
	}
	if req.Loop == nil { // 带选项的不拼斜杠命令，任务文字原样进定期任务页同一个入口，不会撞子命令词
		if err := checkSlash(req); err != nil {
			return SlashCommandResult{}, err
		}
	} else if req.Command != "loop" || (req.Sub != "add" && req.Sub != "edit") {
		return SlashCommandResult{}, slashInvalid("日程选项只用于 loop add 和 loop edit")
	}
	char, topic, conv, err := r.selftestConversation(ctx, req.Character, false)
	if err != nil {
		return SlashCommandResult{}, err
	}
	out := SlashCommandResult{Character: char, Topic: topic, Conversation: conv}
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: conv}
	if req.Loop != nil {
		return r.slashLoopSpec(ctx, req, out, key)
	}
	inv, perr := a.slashes.reg.Parse(req.Text)
	if perr != nil {
		return out, slashInvalid("%v", perr)
	}
	res, herr := a.callSlash(ctx, conv, char, req.Text, inv)
	if herr != nil {
		return out, herr
	}
	switch {
	case res.Send != "":
		// 设目标：把目标原文当主人这一条发给她，回复排在这条消息后面（和输入框里一样）
		out.Started = true
		cancel := func() {}
		if res.Reply != "" {
			cancel = a.pendingNotes.add(conv, res.Send, res.source(), res.Reply)
		}
		a.autoWG.Add(1)
		go func() {
			defer a.autoWG.Done()
			defer cancel()
			if _, err := r.sendLocal(a.autoCtx, key, res.Send, nil); err != nil && !errors.Is(err, context.Canceled) {
				a.logf("控制接口：目标第一轮没发出去：%v", err)
			}
		}()
	case res.Reply != "":
		a.noteLogf(a.systemNotice(conv, res.source(), res.Reply))
	}
	out.Reply = res.Reply
	if req.Command == "goal" && req.Sub == "resume" || req.Command == "loop" && req.Sub == "add" {
		out.Started = true // 继续目标会马上续一轮；新建的定期任务第一轮马上开始
	}
	out.Goal, out.Loops = a.slashView(conv)
	return out, nil
}

func (a *App) slashView(conv string) (*GoalView, []LoopView) {
	var gv *GoalView
	if g, ok := a.goals.Get(conv); ok {
		gv = &GoalView{Text: g.Text, Status: string(g.Status), PauseReason: g.PauseReason, Line: goal.StatusLine(g), Rounds: g.Rounds, MaxRounds: g.Budget.MaxRounds, TokensUsed: g.TokensUsed}
		if g.Last != nil {
			gv.LastJudge = g.Last.Reason
		}
	}
	now := a.now()
	var lv []LoopView
	for _, t := range a.loops.List(conv) {
		v := LoopView{ID: t.ID, Status: string(t.Status), Prompt: t.Prompt, IntervalSec: int64(t.Interval / time.Second), Runs: t.Runs, Line: t.Line(now),
			Kind: string(t.Schedule().Kind()), At: t.At, Notify: t.NotifyLevel(), NewChat: t.NewChat}
		for _, d := range t.Days {
			v.Days = append(v.Days, (d+6)%7+1) // 周日 0 → 7，周一 1 → 1
		}
		if t.Status == loop.StatusRunning {
			v.NextRun = t.NextRun
		}
		lv = append(lv, v)
	}
	return gv, lv
}

// slashLoopSpec 执行带选项的 loop add|edit：和定期任务页同一个 AddLoop、EditLoop。edit 没写的项沿用现在的值。
func (r *DesktopReader) slashLoopSpec(ctx context.Context, req SlashCommandRequest, out SlashCommandResult, key api.ConversationKey) (SlashCommandResult, error) {
	a := r.desk.app
	p := req.Loop
	spec := api.LoopSpec{Prompt: req.Text, Kind: "self", Notify: p.Notify}
	if p.NewChat != nil {
		spec.NewChat = *p.NewChat
	}
	apply := func(sp *api.LoopSpec) {
		if p.Kind != "" {
			sp.Kind, sp.IntervalSec, sp.At, sp.Days = p.Kind, p.IntervalSec, p.At, p.Days
		}
	}
	switch req.Sub {
	case "add":
		apply(&spec)
		id, err := r.AddLoop(ctx, key, spec)
		if err != nil {
			return out, err
		}
		out.Started = spec.Kind == "interval" || spec.Kind == "self" // 钟点日程的第一轮要等到钟点
		out.Reply = "已建立 " + loopLineByID(a, key.Conversation, id)
	case "edit":
		old, ok := a.loops.Get(req.ID)
		if !ok || old.Session != key.Conversation {
			return out, fmt.Errorf("这个会话里没有定期任务 #%d", req.ID)
		}
		spec = api.LoopSpec{Prompt: old.Prompt, Kind: string(old.Schedule().Kind()), IntervalSec: int64(old.Interval / time.Second), At: old.At, Days: old.Days, Notify: old.Notify, NewChat: old.NewChat}
		if req.Text != "" {
			spec.Prompt = req.Text
		}
		if p.Notify != "" {
			spec.Notify = p.Notify
		}
		if p.NewChat != nil {
			spec.NewChat = *p.NewChat
		}
		apply(&spec)
		if err := r.EditLoop(ctx, key, req.ID, spec); err != nil {
			return out, err
		}
		out.Reply = "已修改 " + loopLineByID(a, key.Conversation, req.ID)
	default:
		return out, slashInvalid("loop %s 不带日程选项", req.Sub)
	}
	out.Goal, out.Loops = a.slashView(key.Conversation)
	return out, nil
}

func loopLineByID(a *App, conv string, id int) string {
	if t, ok := a.loops.Get(id); ok {
		return t.Line(a.now())
	}
	return fmt.Sprintf("#%d", id)
}
