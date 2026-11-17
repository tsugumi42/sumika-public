package selfcheck

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// KernelFault 是回合内核记下的一条没解除的故障（internal/turn 的 Fault 的一份拷贝：自检包不能 import turn，
// 和 BackupState 一样由 app 注入一个函数）。
type KernelFault struct {
	Kernel       bool // 整个内核停止受理；否则只是 CharacterID、Conversation 这个会话
	CharacterID  string
	Conversation string
	Kind         string // 稳定的来源键，比如 run_state、activity_end
	At           time.Time
	Detail       string // 最近一次的原始错误；只取类别写进文件，原文可能带路径或内容
	Tries        int    // 已经重试了几次
}

// kernelFaulted 是 S10 kernel.faulted“内核停止受理”：回合内核有会话（或整个内核）因为状态写不进记录库而停止受理新输入。
// 有任何一条就是异常；故障要 sumika-ctl kernel retry 重试才解除（界面里的重试按钮还没做），重启 Sumika 也会清掉，解除后下一轮评估回到正常。
// 没接内核（Faults 为 nil）记 unknown，不记成正常。
type kernelFaulted struct{}

func (kernelFaulted) ID() string   { return "kernel.faulted" }
func (kernelFaulted) Name() string { return "内核停止受理" }

func (kernelFaulted) Eval(_ context.Context, e *Env) Result {
	if e.Faults == nil {
		return Result{State: Unknown, Note: "没有接内核故障状态"}
	}
	faults := e.Faults()
	stray := 0
	if e.StrayEnds != nil {
		stray = e.StrayEnds()
	}
	if len(faults) == 0 {
		if stray > 0 {
			// 没人认领的活动退出记录没写进库：不挡受理（所以不是异常），但要看得见；sumika-ctl kernel retry 会一起重写
			return Result{State: Ok, Example: map[string]string{"strayEnds": strconv.Itoa(stray)},
				Note: strconv.Itoa(stray) + " 条工具或后台任务的退出记录没写进库，不影响受理",
				Next: "运行 sumika-ctl kernel retry 可以重写"}
		}
		return Result{State: Ok}
	}
	// 例子取最该先看的一条：整个内核的优先，其次最早出的
	first := faults[0]
	for _, f := range faults[1:] {
		if first.Kernel != f.Kernel {
			if f.Kernel {
				first = f
			}
			continue
		}
		if f.At.Before(first.At) {
			first = f
		}
	}
	r := Result{State: Bad, Value: int64(len(faults)), Threshold: 1, Example: map[string]string{
		"kind":       first.Kind,
		"since":      first.At.Format(time.RFC3339),
		"errorClass": StoreErrorClass(errors.New(first.Detail)),
		"tries":      strconv.Itoa(first.Tries),
	}}
	if stray > 0 {
		r.Example["strayEnds"] = strconv.Itoa(stray)
	}
	r.Next = "运行 sumika-ctl kernel retry 重试，或重启 Sumika（启动时会核对）"
	if first.Kernel {
		r.Example["scope"] = "kernel"
		r.Note = "整个内核停止受理新输入"
	} else {
		r.Example["scope"] = "conversation"
		r.Example["conversation"] = first.Conversation
		r.Note = "有会话停止受理新输入，别的会话照常"
	}
	return r
}
