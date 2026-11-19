package app

import (
	"context"
	"errors"
	"fmt"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/turn"
)

// 回合内核的故障（sumika-ctl kernel faults / kernel retry，质检 X-07）。重试是主人和运维的操作，
// 只经本机管道和主窗口进来，不注册成模型能调的工具。

// ErrNoKernel 是回合内核还没建好
var ErrNoKernel = errors.New("回合内核还没准备好")

func ctlFault(f turn.Fault) ctlproto.KernelFault {
	scope := "conversation"
	if f.Kernel() {
		scope = "kernel"
	}
	return ctlproto.KernelFault{Scope: scope, Character: f.CharacterID, Conversation: f.Conversation, Kind: string(f.Kind), Reason: f.Reason,
		Detail: f.Detail, At: msOf(f.At), Tries: f.Tries}
}

func ctlFaults(fs []turn.Fault) []ctlproto.KernelFault {
	out := make([]ctlproto.KernelFault, 0, len(fs))
	for _, f := range fs {
		out = append(out, ctlFault(f))
	}
	return out
}

// KernelFaults 列出现在没解除的故障
func (a *App) KernelFaults(context.Context) (ctlproto.KernelFaults, error) {
	if a == nil || a.hub == nil {
		return ctlproto.KernelFaults{}, ErrNoKernel
	}
	return ctlproto.KernelFaults{Faults: ctlFaults(a.hub.Faults()), StrayEnds: a.hub.StrayEndFailures()}, nil
}

// KernelRetry 重试故障：角色和会话都空 = 全部。没恢复返回 *ctlproto.Error（failed），Data 里是还没恢复的那些；不说成成功。
func (a *App) KernelRetry(ctx context.Context, character, conversation string) (ctlproto.KernelRetry, error) {
	if a == nil || a.hub == nil {
		return ctlproto.KernelRetry{}, ErrNoKernel
	}
	if character == "" && conversation == "" {
		rep, err := a.hub.RetryAllFaults(ctx)
		out := ctlproto.KernelRetry{Cleared: ctlFaults(rep.Cleared), Remaining: ctlFaults(rep.Remaining), StrayEnds: rep.StrayEnds}
		if err != nil {
			return out, &ctlproto.Error{Code: ctlproto.CodeFailed, Msg: fmt.Sprintf("还有 %d 处故障没有恢复：%v", len(rep.Remaining), err), Data: out}
		}
		return out, nil
	}
	before, had := a.hub.FaultExact(character, conversation)
	err := a.hub.RetryFault(ctx, character, conversation)
	out := ctlproto.KernelRetry{Cleared: []ctlproto.KernelFault{}, Remaining: []ctlproto.KernelFault{}}
	switch {
	case err == nil:
		if had {
			out.Cleared = append(out.Cleared, ctlFault(before))
		}
		return out, nil
	case errors.Is(err, turn.ErrNoFault), errors.Is(err, turn.ErrRetryBusy):
		return out, &ctlproto.Error{Code: ctlproto.CodeFailed, Msg: err.Error()}
	}
	if cur, ok := a.hub.FaultExact(character, conversation); ok {
		out.Remaining = append(out.Remaining, ctlFault(cur))
	}
	return out, &ctlproto.Error{Code: ctlproto.CodeFailed, Msg: err.Error(), Data: out}
}
