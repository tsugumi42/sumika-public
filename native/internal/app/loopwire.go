package app

// /loop 接进本地私聊（规格-目标和循环 第三节、第四节第 4 步）。
//
//   - 调度器随后台起停：Open 里建、Run 里起（用 autoCtx，关机最先取消）；
//   - 到点在原会话里发起一轮：输入先存成"主人身份、来源 loop"的消息，她的回答先按"待分类"存（界面和历史都看不见），
//     这一轮结束后调度器分类，Deliver 把待分类的行改成可见（有内容、出错）或永久隐藏（回了 [无事]）；
//   - schedule_next 工具只在"这个会话正在跑定期任务的一轮"、而且是本地私聊主人时才对她可见。

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sumika-native/internal/applog"

	"sumika-native/internal/audience"
	"sumika-native/internal/history"
	"sumika-native/internal/loop"
	"sumika-native/internal/slash"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// loopRound 记着一轮的待分类行在哪（Run 里记下，Deliver 里用）。
type loopRound struct {
	conv, char       string
	promptSeq, hiSeq int64
}

var loopSlashCommand = slash.Command{
	Name:    "loop",
	Summary: "定期任务：/loop 30m <任务> 新建，/loop edit <编号> 修改，pause、resume、stop 管理，/loop 看列表",
	Subs: []slash.Sub{
		{Name: "stop", Summary: "停掉"}, {Name: "pause", Summary: "暂停"}, {Name: "resume", Summary: "恢复"}, {Name: "edit", Summary: "修改间隔或任务文字"},
	},
}

// loopTool 是 schedule_next 的登记：声明在 loop 包里，谁能看见由宿主定。
type loopTool struct{ app *App }

func (loopTool) ID() string { return "loop-schedule-next" }
func (p loopTool) Setup(r *turn.Registry) (func(), error) {
	return r.OnTool(turn.Tool{Decl: loop.ScheduleNextDecl, For: p.visible, Exec: p.exec})
}

// visible：本地私聊、会话正在跑定期任务的一轮。
func (p loopTool) visible(aud audience.Audience) bool {
	return aud.Channel == "console" && aud.Scene != "group" && aud.PrivateOK && localPrivateConv(aud.Conversation) &&
		p.app.loops != nil && p.app.loops.InFlight(aud.Conversation)
}

func (p loopTool) exec(_ context.Context, c turn.ToolCall) (tools.Result, error) {
	fail := func(s string) (tools.Result, error) { return tools.Result{Text: s, IsError: true}, nil }
	if !ownerLocalPrivate(c.Identity.Scope) || !p.visible(c.Audience) || c.Conversation != c.Identity.Scope.Conversation {
		return fail("当前身份不能用这个工具")
	}
	text, err := p.app.loops.ExecScheduleNext(c.Conversation, c.Args)
	if err != nil {
		return fail(err.Error())
	}
	return tools.Result{Text: text}, nil
}

func (a *App) openLoops() error {
	// 上次没收尾的待分类行（崩溃、强退）改回可见，不让它们永远藏着
	for _, name := range a.set.Platforms() {
		if st, ok := a.set.Platform(name); ok && name == "local" {
			a.noteLogf(a.revealPendingLoopRows(st))
		}
	}
	s, err := loop.New(loop.Options{
		Store:       loop.FileStore{Path: a.paths.LoopFile()},
		Run:         a.loopRun,
		SessionBusy: func(conv string) bool { return a.auto.has(conv) || a.hub.Running(characterOfConv(conv), conv) },
		GoalActive:  a.goalActive,
		Deliver:     a.loopDeliver,
		Now:         a.now,
		Logf:        a.logfFor(applog.Loop),
	})
	if err != nil {
		return err
	}
	a.loops = s
	a.slashes.add(loopSlashCommand, a.slashLoop)
	return a.hub.Use(loopTool{app: a})
}

// runLoops 在 Run 里起调度器；返回时已经交给 autoWG。
func (a *App) runLoops() {
	if a.loops == nil {
		return
	}
	a.autoWG.Add(1)
	go func() {
		defer a.autoWG.Done()
		a.loops.Run(a.autoCtx)
	}()
}

func (a *App) revealPendingLoopRows(st *store.Store) error {
	if _, err := st.RetagOrigin("", 0, 0, history.OriginLoopPending, "loop", "owner"); err != nil {
		return err
	}
	_, err := st.RetagOrigin("", 0, 0, history.OriginLoopPending, "character", "character")
	return err
}

func (a *App) loopRun(ctx context.Context, req loop.Request) (string, error) {
	conv := req.Task.Session
	char := characterOfConv(conv)
	if char == "" || !localPrivateConv(conv) {
		return "", errors.New("定期任务只能在本地私聊里跑")
	}
	if req.Task.NewChat {
		var err error
		if conv, err = a.loopNewChat(req.Task); err != nil {
			return "", err
		}
		a.loops.Attach(req.Task.Session, conv)
	}
	st, err := a.set.ForConversation(conv)
	if err != nil {
		return "", err
	}
	seq, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: history.OriginLoopPending, Kind: "message", Text: req.Input, Segments: selftestSegs(conv, nil), CreatedAt: a.now().UnixMilli()}, []string{char})
	if err != nil {
		return "", err
	}
	rctx, cancel := context.WithCancelCause(ctx)
	a.auto.set(conv, cancel)
	defer a.auto.clear(conv)
	out := <-a.hub.SendLocalStored(rctx, char, conv, req.Input, turn.StoredSend{Seqs: []int64{seq}, ReplyOrigin: history.OriginLoopPending, Auto: true}, nil)
	hi, _ := history.MaxSeq(st.Events, conv)
	a.loopRounds.Store(req.Task.ID, loopRound{conv: conv, char: char, promptSeq: seq, hiSeq: hi})
	switch {
	case out.Err != nil:
		return "", out.Err
	case out.Result == nil:
		return "", errors.New("这一轮没有结果")
	case out.Result.Stopped:
		return "", errors.New("这一轮被停止了")
	}
	return out.Result.Reply, nil
}

// loopNewChat 给“每次新聊天”的任务开这一轮的话题，返回它的会话号。任务建在自检话题里（sumika-ctl）时，
// 新话题也是自检话题，里面的行带自检标记，不进记忆。标题是任务开头加时间，会话列表里一眼认得出。
func (a *App) loopNewChat(t loop.Task) (string, error) {
	char := characterOfConv(t.Session)
	st, err := a.set.For("console")
	if err != nil {
		return "", err
	}
	now := a.now()
	stamp := strconv.FormatInt(now.UnixMilli(), 36)
	title := "定期任务：" + clipText(strings.Join(strings.Fields(t.Prompt), " "), 16) + " " + now.Format("01-02 15:04")
	ctx := context.Background()
	for n := 0; n < 50; n++ {
		id := "loop-" + strconv.Itoa(t.ID) + "-" + stamp
		if audience.IsSelftest(t.Session) {
			id = audience.SelftestTopicID(char, "loop"+strconv.Itoa(t.ID)+"-"+stamp)
		}
		if n > 0 {
			id += "-" + strconv.Itoa(n)
		}
		var created bool
		if audience.IsSelftest(t.Session) {
			created, err = st.EnsureSelftestTopic(ctx, char, id, store.SelftestTopicTitle+" "+title, now.UnixMilli())
		} else {
			created, err = st.CreateLocalTopic(ctx, char, id, title, "loop", now.UnixMilli())
		}
		if err != nil {
			return "", err
		}
		if created {
			if audience.IsSelftest(t.Session) {
				return audience.SelftestConversation(char, id), nil
			}
			return "console:" + char + ":topic:" + id, nil
		}
	}
	return "", errors.New("开不出新的话题")
}

func (a *App) loopDeliver(rep loop.Report) {
	v, ok := a.loopRounds.LoadAndDelete(rep.Task.ID)
	if !ok {
		return
	}
	r := v.(loopRound)
	st, err := a.set.ForConversation(r.conv)
	if err != nil {
		a.logf("定期任务 #%d 的结果没处理：%v", rep.Task.ID, err)
		return
	}
	lo := r.promptSeq - 1
	switch rep.Kind {
	case loop.ResultQuiet:
		_, err = st.RetagOrigin(r.conv, lo, r.hiSeq, history.OriginLoopPending, history.OriginLoopQuiet, "")
	default:
		if _, err = st.RetagOrigin(r.conv, lo, r.hiSeq, history.OriginLoopPending, "loop", "owner"); err == nil {
			_, err = st.RetagOrigin(r.conv, lo, r.hiSeq, history.OriginLoopPending, "character", "character")
		}
	}
	a.noteLogf(err)
	a.statusChanged(r.conv)
	level := rep.Task.NotifyLevel()
	switch {
	case rep.Kind == loop.ResultQuiet: // [无事]：不显示、不通知
	case level == loop.NotifyNone, level == loop.NotifyErrors && rep.Kind != loop.ResultError: // 通知档不要这一类
	case rep.Kind == loop.ResultError:
		body := "这一轮出错了"
		if rep.Err != nil {
			body += "：" + rep.Err.Error()
		}
		a.attend(AttentionLoop, r.conv, "定期任务 #"+fmt.Sprint(rep.Task.ID)+" 出错了", body)
	default:
		a.attend(AttentionLoop, r.conv, "定期任务 #"+fmt.Sprint(rep.Task.ID)+" 有新消息", rep.Text)
	}
	if rep.Kind == loop.ResultError {
		msg := "定期任务 #" + fmt.Sprint(rep.Task.ID) + " 这一轮出错了"
		if rep.Err != nil {
			msg += "：" + clipText(rep.Err.Error(), 120)
		}
		a.noteLogf(a.systemNotice(r.conv, noticeLoop, msg))
	}
	if rep.Task.Status == loop.StatusStopped {
		a.noteLogf(a.systemNotice(rep.Task.Session, noticeLoop, "定期任务 #"+fmt.Sprint(rep.Task.ID)+" 已停止："+rep.Task.StopReason))
	}
}

func (a *App) slashLoop(_ context.Context, c slashCall) (slashResult, error) {
	cmd, ok, err := loop.ParseCommand(c.Text)
	if !ok {
		return slashResult{}, errors.New("认不出这条 /loop 命令")
	}
	if err != nil {
		return slashResult{}, err
	}
	text, err := a.loops.Apply(c.Conv, cmd)
	if err != nil {
		return slashResult{}, err
	}
	if cmd.Kind == loop.CmdStop || cmd.Kind == loop.CmdPause {
		a.auto.clear(c.Conv) // 立刻停住正在跑的那一轮
	}
	return slashResult{Reply: strings.TrimSpace(text), Source: noticeLoop}, nil
}
