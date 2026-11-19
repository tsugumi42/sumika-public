package main

// sumika-ctl 的 goal、loop 子命令（规格-本机控制接口第十节）接进桌面程序：走后台和输入框斜杠命令同一批处理函数
// （DesktopReader.SlashCommand），会话由后台按角色拼成自检话题，控制接口收不到任何会话 id。

import (
	"context"
	"errors"
	"time"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

var _ ctlserver.SlashBackend = (*ctlBackend)(nil)

func (b *ctlBackend) Slash(ctx context.Context, req ctlserver.SlashRequest) (ctlproto.SlashResult, error) {
	r, err := b.checkpointReader() // 取可写桌面的读写入口：没有可写后台（只看历史）回 readonly，演示模式回 demo
	if err != nil {
		return ctlproto.SlashResult{}, err
	}
	return slashReply(r.SlashCommand(ctx, backend.SlashCommandRequest{Command: req.Command, Sub: req.Sub, Character: req.Character, Text: req.Text, ID: req.ID, Loop: req.Loop}))
}

// slashReply 把后台的命令结果翻成协议的结果
func slashReply(res backend.SlashCommandResult, err error) (ctlproto.SlashResult, error) {
	switch {
	case errors.Is(err, api.ErrLocalChatUnsupported):
		return ctlproto.SlashResult{}, ctlproto.Errorf(ctlproto.CodeReadOnly, "只看历史模式不能设目标或定期任务，请先开始接收")
	case errors.Is(err, backend.ErrSelftestCharacter):
		return ctlproto.SlashResult{}, ctlproto.Errorf(ctlproto.CodeInvalid, "没有这个角色")
	case errors.Is(err, backend.ErrSlashInvalid):
		return ctlproto.SlashResult{}, ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
	case err != nil:
		return ctlproto.SlashResult{}, err // 命令自己报的错（没有这个目标、会话正在生成……）按 failed，原文给出
	}
	out := ctlproto.SlashResult{Character: res.Character, Topic: res.Topic, Conversation: res.Conversation, Reply: res.Reply, Started: res.Started}
	if g := res.Goal; g != nil {
		out.Goal = &ctlproto.GoalState{Text: g.Text, Status: g.Status, Rounds: g.Rounds, MaxRounds: g.MaxRounds, TokensUsed: g.TokensUsed, PauseReason: g.PauseReason, LastJudge: g.LastJudge, Line: g.Line}
	}
	out.Loops = make([]ctlproto.LoopTask, len(res.Loops))
	for i, t := range res.Loops {
		out.Loops[i] = ctlproto.LoopTask{ID: t.ID, Status: t.Status, Prompt: t.Prompt, IntervalSec: t.IntervalSec, Runs: t.Runs, Line: t.Line, Kind: t.Kind, At: t.At, Days: t.Days, Notify: t.Notify, NewChat: t.NewChat}
		if !t.NextRun.IsZero() {
			out.Loops[i].NextRun = t.NextRun.Format(time.RFC3339)
		}
	}
	return out, nil
}
