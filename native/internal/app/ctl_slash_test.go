package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/llm/llmtest"
)

func slashReq(cmd, sub, text string) SlashCommandRequest {
	r := SlashCommandRequest{Command: cmd, Sub: sub}
	switch {
	case cmd == "goal" && sub == "show", cmd == "loop" && sub == "list":
		r.Text = "/" + cmd
	case sub == "set" || (cmd == "loop" && sub == "add"):
		r.Text = "/" + cmd + " " + text
	default:
		r.Text = "/" + cmd + " " + sub
		if text != "" {
			r.Text += " " + text
		}
	}
	return r
}

// goal set 走和输入框同一套逻辑：会话是自检话题，目标原文当主人这一条发给她，自动续轮到达成；
// 回复存成系统行，行带自检标记，真实的主聊天里什么都没多。
func TestCtlGoalSetRunsInSelftestTopic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{llmtest.Text("第一轮进展"), llmtest.Text(judgeNo), llmtest.Text("第二轮做完"), llmtest.Text(judgeYes)}})
	ctx := context.Background()
	res, err := d.Reader.SlashCommand(ctx, slashReq("goal", "set", "建 hello.txt 并读回"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Started || !strings.HasPrefix(res.Reply, "目标已设定") || res.Goal == nil || res.Goal.Status != "active" || res.Goal.Text != "建 hello.txt 并读回" {
		t.Fatalf("设目标的结果：%+v", res)
	}
	if !audience.IsSelftest(res.Conversation) || res.Character != testChar {
		t.Fatalf("会话应是自检话题：%+v", res)
	}
	waitFor(t, "目标达成", func() bool { return hasRow(slashRows(t, a, res.Conversation), "目标达成：文件已读回") })
	rows := slashRows(t, a, res.Conversation)
	for _, w := range []string{"message/owner/owner/建 hello.txt 并读回", "notice/system/system:goal/目标已设定", "message/character/character/第一轮进展", "message/owner/goal/目标还没达成"} {
		if !hasRow(rows, w) {
			t.Fatalf("会话里缺 %q：\n%s", w, strings.Join(rows, "\n"))
		}
	}
	if main := slashRows(t, a, "console:"+testChar); len(main) != 0 {
		t.Fatalf("真实主聊天不该多出行：%q", main)
	}
	// 达成后目标已清：show 回的是没有目标
	res, err = d.Reader.SlashCommand(ctx, slashReq("goal", "show", ""))
	if err != nil || res.Goal != nil {
		t.Fatalf("达成后 show：%+v %v", res, err)
	}
}

// goal 的完整周期和 loop 的完整周期，各子命令的结果和系统行都对；写法撞了子命令词时回 invalid，不会误执行。
func TestCtlGoalAndLoopLifecycle(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{
		Replies: []llmtest.Reply{llmtest.Text("第一轮进展"), llmtest.Text(judgeNo), llmtest.Text("接着做前半", "接着做后半")},
		Hold: func(nth int) int {
			if nth == 3 { // 续轮停在半句上：目标保持进行中，方便测 edit、pause、resume
				return 1
			}
			return -1
		},
	})
	ctx := context.Background()
	res, err := d.Reader.SlashCommand(ctx, slashReq("goal", "set", "做事"))
	if err != nil {
		t.Fatal(err)
	}
	conv := res.Conversation
	waitFor(t, "续轮开始", func() bool { return e.llmCalls() == 3 && a.hub.ActiveRunID(testChar, conv) != "" })
	// edit：改目标原文
	if res, err = d.Reader.SlashCommand(ctx, slashReq("goal", "edit", "做更多事")); err != nil || res.Goal == nil || res.Goal.Text != "做更多事" || !strings.Contains(res.Reply, "目标改为") {
		t.Fatalf("edit：%+v %v", res, err)
	}
	// pause：目标暂停、自动轮被取消
	if res, err = d.Reader.SlashCommand(ctx, slashReq("goal", "pause", "")); err != nil || res.Goal == nil || res.Goal.Status != "paused" {
		t.Fatalf("pause：%+v %v", res, err)
	}
	waitFor(t, "自动轮收尾", func() bool { return !a.hub.Running(testChar, conv) && !a.auto.has(conv) })
	// 撞词：目标文字恰好是 pause，拼出来会变成暂停，要被拒，目标不变
	if _, err = d.Reader.SlashCommand(ctx, slashReq("goal", "set", "pause")); !errors.Is(err, ErrSlashInvalid) {
		t.Fatalf("撞词应 invalid：%v", err)
	}
	if g, ok := a.goals.Get(conv); !ok || g.Text != "做更多事" {
		t.Fatalf("被拒的命令不该改目标：%+v", g)
	}
	// clear
	if res, err = d.Reader.SlashCommand(ctx, slashReq("goal", "clear", "")); err != nil || res.Goal != nil || res.Reply != "目标已清除" {
		t.Fatalf("clear：%+v %v", res, err)
	}
	// 没有目标时 resume 是命令自己的错，按 failed（普通错误，不是 invalid）
	if _, err = d.Reader.SlashCommand(ctx, slashReq("goal", "pause", "")); err != nil {
		t.Fatal(err) // 没有目标：回复"这个会话没有目标"，不是错
	}

	// loop：add → list → edit → pause → resume → remove，编号和状态对得上
	res, err = d.Reader.SlashCommand(ctx, slashReq("loop", "add", "30m 报一下现在几点"))
	if err != nil || len(res.Loops) != 1 || res.Loops[0].ID != 1 || res.Loops[0].IntervalSec != 1800 || res.Loops[0].Status != "running" || !strings.HasPrefix(res.Reply, "已建立 #1") {
		t.Fatalf("loop add：%+v %v", res, err)
	}
	if res, err = d.Reader.SlashCommand(ctx, slashReq("loop", "edit", "1 2h")); err != nil || res.Loops[0].IntervalSec != 7200 {
		t.Fatalf("loop edit：%+v %v", res, err)
	}
	pause := slashReq("loop", "pause", "1")
	pause.ID = 1
	if res, err = d.Reader.SlashCommand(ctx, pause); err != nil || res.Loops[0].Status != "paused" {
		t.Fatalf("loop pause：%+v %v", res, err)
	}
	resume := slashReq("loop", "resume", "1")
	resume.ID = 1
	if res, err = d.Reader.SlashCommand(ctx, resume); err != nil || res.Loops[0].Status != "running" {
		t.Fatalf("loop resume：%+v %v", res, err)
	}
	remove := SlashCommandRequest{Command: "loop", Sub: "remove", Text: "/loop stop 1", ID: 1}
	if res, err = d.Reader.SlashCommand(ctx, remove); err != nil || res.Loops[0].Status != "stopped" {
		t.Fatalf("loop remove：%+v %v", res, err)
	}
	// 任务文字以子命令词开头会被当成别的命令：拒
	if _, err = d.Reader.SlashCommand(ctx, slashReq("loop", "add", "edit 1 2h")); !errors.Is(err, ErrSlashInvalid) {
		t.Fatalf("loop add 撞词应 invalid：%v", err)
	}
	if _, err = d.Reader.SlashCommand(ctx, slashReq("loop", "add", "1x 写错的间隔")); !errors.Is(err, ErrSlashInvalid) {
		t.Fatalf("坏间隔应 invalid：%v", err)
	}
	// 命令自己的错（编号不存在）不是 invalid：原样给出
	bad := SlashCommandRequest{Command: "loop", Sub: "remove", Text: "/loop stop 99", ID: 99}
	if _, err = d.Reader.SlashCommand(ctx, bad); err == nil || errors.Is(err, ErrSlashInvalid) {
		t.Fatalf("不存在的编号应是命令自己的错：%v", err)
	}
	// 系统行都写进了自检话题
	rows := slashRows(t, a, conv)
	for _, w := range []string{"目标改为", "目标已清除", "已建立 #1", "已修改", "已暂停", "已恢复", "已停止"} {
		if !hasRow(rows, w) {
			t.Fatalf("会话里缺系统行 %q：\n%s", w, strings.Join(rows, "\n"))
		}
	}
}

// loop add|edit 带日程选项：走和定期任务页同一个入口，会话是自检话题；每天的任务第一轮不马上开始；
// edit 只改写了的项；新聊天的任务在自检话题的新话题里跑，行带自检标记。
func TestCtlLoopOptionsAddAndEdit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := autoDesktop(t, e, llmtest.Script{Reply: llmtest.Text("早安")})
	ctx := context.Background()
	req := SlashCommandRequest{Command: "loop", Sub: "add", Text: "早上问个好", Loop: &ctlproto.LoopPatch{Kind: "weekly", At: "09:00", Days: []int{1, 3}}}
	res, err := d.Reader.SlashCommand(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Started || len(res.Loops) != 1 || res.Loops[0].Kind != "weekly" || res.Loops[0].At != "09:00" || !slices.Equal(res.Loops[0].Days, []int{1, 3}) || !strings.HasPrefix(res.Reply, "已建立 #1 每周一三 09:00") {
		t.Fatalf("新建每周任务：%+v", res)
	}
	if !audience.IsSelftest(res.Conversation) {
		t.Fatalf("会话应是自检话题：%s", res.Conversation)
	}
	before, _ := a.loops.Get(1)
	yes := true
	res, err = d.Reader.SlashCommand(ctx, SlashCommandRequest{Command: "loop", Sub: "edit", ID: 1, Loop: &ctlproto.LoopPatch{NewChat: &yes, Notify: "errors"}})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := a.loops.Get(1)
	if after.Prompt != "早上问个好" || after.At != "09:00" || !after.NewChat || after.Notify != "errors" || !after.NextRun.Equal(before.NextRun) {
		t.Fatalf("只改了聊天方式和通知，其余不动：%+v", after)
	}
	// 改成每 1 分钟，马上能跑：新聊天的行带自检标记，原自检话题里没有这一轮
	if _, err = d.Reader.SlashCommand(ctx, SlashCommandRequest{Command: "loop", Sub: "edit", ID: 1, Loop: &ctlproto.LoopPatch{Kind: "interval", IntervalSec: 60}}); err != nil {
		t.Fatal(err)
	}
	e.setNow(e.now.Add(2 * time.Minute)) // 没跑过的任务改间隔，从现在起算一个间隔
	a.loops.Tick(ctx)
	a.loops.Wait()
	st, _ := a.set.ForConversation(res.Conversation)
	var n int
	st.Events.QueryRow(`SELECT COUNT(*) FROM local_topics WHERE id LIKE 'selftest-%loop1-%' AND title LIKE '自检 定期任务：%'`).Scan(&n)
	if n != 1 {
		t.Fatalf("新聊天应在自检话题体系里开一个新话题，得到 %d 个", n)
	}
	// 不存在的编号、非法值：报错，任务不变
	if _, err = d.Reader.SlashCommand(ctx, SlashCommandRequest{Command: "loop", Sub: "edit", ID: 9, Loop: &ctlproto.LoopPatch{Notify: "none"}}); err == nil {
		t.Fatal("不存在的编号应报错")
	}
	if _, err = d.Reader.SlashCommand(ctx, SlashCommandRequest{Command: "loop", Sub: "add", Text: "x", Loop: &ctlproto.LoopPatch{Kind: "daily", At: "99:99"}}); err == nil {
		t.Fatal("非法时间应报错")
	}
	if n := len(a.loops.List("")); n != 1 {
		t.Fatalf("非法输入不该留下任务：%d", n)
	}
}
