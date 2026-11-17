package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/goal"
	"sumika-native/internal/loop"
)

var _ api.LoopControl = (*DesktopReader)(nil)

// goalInfo 把目标的详情交给界面：输入框上方那一行点开看全文、轮数、预算和裁判的最近一次理由。
func goalInfo(g goal.Goal) *api.GoalInfo {
	gi := &api.GoalInfo{
		Text: g.Text, State: string(g.Status), PauseReason: g.PauseReason,
		Rounds: g.Rounds, MaxRounds: g.Budget.MaxRounds, TokensUsed: g.TokensUsed, MaxTokens: g.Budget.MaxTokens,
		StartedAt: g.StartedAt.UnixMilli(),
	}
	if j := g.Last; j != nil {
		gi.LastReason, gi.LastAchieved, gi.LastFailed, gi.LastAt = j.Reason, j.Achieved, j.Failed, j.At.UnixMilli()
	}
	return gi
}

// loopInfo 把一个定期任务交给界面（定期任务页）；零值时间写 0，不写成 1970 年。
func loopInfo(t loop.Task) api.LoopInfo {
	ms := func(x interface {
		IsZero() bool
		UnixMilli() int64
	}) int64 {
		if x.IsZero() {
			return 0
		}
		return x.UnixMilli()
	}
	li := api.LoopInfo{
		ID: t.ID, Prompt: t.Prompt, Status: string(t.Status), IntervalSec: int64(t.Interval.Seconds()),
		NextRun: ms(t.NextRun), NextReason: t.NextReason, Runs: t.Runs, LastRun: ms(t.LastRun),
		LastResult: string(t.LastResult), LastNote: t.LastNote, Expires: ms(t.Expires),
		Kind: string(t.Schedule().Kind()), At: t.At, Days: append([]int(nil), t.Days...), Notify: t.NotifyLevel(), NewChat: t.NewChat,
	}
	for _, e := range t.History {
		li.History = append(li.History, api.LoopRun{At: e.At.UnixMilli(), Result: string(e.Result), Note: e.Note, Missed: e.Missed})
	}
	return li
}

// loopSchedule 把表单的日程写法转成调度器的 Schedule；种类和它用的字段对不上、认不出的种类都报错
func loopSchedule(sp api.LoopSpec) (loop.Schedule, error) {
	switch sp.Kind {
	case string(loop.KindInterval):
		if sp.IntervalSec <= 0 {
			return loop.Schedule{}, errors.New("固定间隔要写多久一次")
		}
		return loop.Schedule{Interval: time.Duration(sp.IntervalSec) * time.Second}, nil
	case string(loop.KindDaily):
		if sp.At == "" {
			return loop.Schedule{}, errors.New("每天要写几点")
		}
		return loop.Schedule{At: sp.At}, nil
	case string(loop.KindWeekly):
		if len(sp.Days) == 0 {
			return loop.Schedule{}, errors.New("每周要至少选一天")
		}
		if sp.At == "" {
			return loop.Schedule{}, errors.New("每周要写几点")
		}
		return loop.Schedule{At: sp.At, Days: sp.Days}, nil
	case string(loop.KindSelf):
		return loop.Schedule{}, nil
	}
	return loop.Schedule{}, fmt.Errorf("不认识的频率「%s」", sp.Kind)
}

func sameSchedule(a, b loop.Schedule) bool {
	a, b = a.Normalized(), b.Normalized()
	return a.Interval == b.Interval && a.At == b.At && slices.Equal(a.Days, b.Days)
}

// AddLoop 是定期任务页的“新建”：和 /loop 同一个 Scheduler.Add，会话里留一行系统行。
func (r *DesktopReader) AddLoop(ctx context.Context, k api.ConversationKey, sp api.LoopSpec) (int, error) {
	if err := r.bunOwned(); err != nil {
		return 0, err
	}
	sc, err := loopSchedule(sp)
	if err != nil {
		return 0, err
	}
	a, done, err := r.loopEntry(ctx, k)
	if err != nil {
		return 0, err
	}
	defer done()
	t, err := a.loops.Add(k.Conversation, sp.Prompt, loop.AddOptions{Interval: sc.Interval, At: sc.At, Days: sc.Days, Notify: sp.Notify, NewChat: sp.NewChat})
	if err != nil {
		return 0, err
	}
	a.noteLogf(a.systemNotice(k.Conversation, noticeLoop, "已建立 "+t.Line(a.now())))
	a.statusChanged(k.Conversation)
	return t.ID, nil
}

// EditLoop 是“保存修改”：整张表单的值一次交进来。日程没变就不动下次时间，只改说明、通知、聊天方式；
// 暂停的仍是暂停（Scheduler.Edit 不碰状态）。
func (r *DesktopReader) EditLoop(ctx context.Context, k api.ConversationKey, id int, sp api.LoopSpec) error {
	if err := r.bunOwned(); err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("要指定定期任务的编号")
	}
	if strings.TrimSpace(sp.Prompt) == "" {
		return errors.New("任务内容是空的") // 表单整张提交，空说明不是“不改”
	}
	sc, err := loopSchedule(sp)
	if err != nil {
		return err
	}
	a, done, err := r.loopEntry(ctx, k)
	if err != nil {
		return err
	}
	defer done()
	old, ok := a.loops.Get(id)
	if !ok || old.Session != k.Conversation {
		return fmt.Errorf("这个会话里没有定期任务 #%d", id)
	}
	opt := loop.EditOptions{Prompt: sp.Prompt, Notify: &sp.Notify, NewChat: &sp.NewChat}
	if !sameSchedule(old.Schedule(), sc) {
		opt.Schedule = &sc
	}
	t, err := a.loops.Edit(k.Conversation, id, opt)
	if err != nil {
		return err
	}
	a.noteLogf(a.systemNotice(k.Conversation, noticeLoop, "已修改 "+t.Line(a.now())))
	a.statusChanged(k.Conversation)
	return nil
}

// loopEntry 是新建、修改共用的入口检查：后台开着、会话是本地私聊且角色存在、调度器已装配。
func (r *DesktopReader) loopEntry(ctx context.Context, k api.ConversationKey) (*App, func(), error) {
	_, done, err := r.desk.enterChat()
	if err != nil {
		return nil, nil, err
	}
	if _, err := r.localCharacter(ctx, k); err != nil {
		done()
		return nil, nil, err
	}
	a := r.desk.app
	if a.loops == nil {
		done()
		return nil, nil, errors.New("定期任务还没装配好")
	}
	return a, done, nil
}

func (r *DesktopReader) PauseLoop(ctx context.Context, k api.ConversationKey, id int) error {
	if err := r.bunOwned(); err != nil {
		return err
	}
	return r.loopCommand(ctx, k, id, loop.CmdPause)
}

func (r *DesktopReader) ResumeLoop(ctx context.Context, k api.ConversationKey, id int) error {
	if err := r.bunOwned(); err != nil {
		return err
	}
	return r.loopCommand(ctx, k, id, loop.CmdResume)
}

// RemoveLoop 是“删除”：调度器没有真删，就是停止（和 /loop stop 同一条），停了的任务不再出现在列表里。
func (r *DesktopReader) RemoveLoop(ctx context.Context, k api.ConversationKey, id int) error {
	if err := r.bunOwned(); err != nil {
		return err
	}
	return r.loopCommand(ctx, k, id, loop.CmdStop)
}

// loopCommand 走和输入框 /loop 同一套 Apply：同样的校验，同样写一行系统行（会话里看得到谁停了哪个任务），
// 暂停、停止同时取消正在跑的那一轮。只认本地私聊里的会话，必须指定编号（0 在命令里表示“全部”，这里不允许）。
func (r *DesktopReader) loopCommand(ctx context.Context, k api.ConversationKey, id int, kind loop.CmdKind) error {
	if id <= 0 {
		return errors.New("要指定定期任务的编号")
	}
	_, done, err := r.desk.enterChat()
	if err != nil {
		return err
	}
	defer done()
	if _, err := r.localCharacter(ctx, k); err != nil {
		return err
	}
	a := r.desk.app
	if a.loops == nil {
		return errors.New("定期任务还没装配好")
	}
	text, err := a.loops.Apply(k.Conversation, loop.Command{Kind: kind, ID: id})
	if err != nil {
		return err
	}
	if kind != loop.CmdResume {
		a.auto.clear(k.Conversation) // 立刻停住正在跑的那一轮
	}
	a.noteLogf(a.systemNotice(k.Conversation, noticeLoop, text))
	a.statusChanged(k.Conversation)
	return nil
}
