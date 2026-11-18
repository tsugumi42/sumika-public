package ctlserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

type kernelFake struct {
	fakeBackend
	retries   []string
	remaining []ctlproto.KernelFault
	failErr   error
}

func (f *kernelFake) KernelFaults(context.Context) (ctlproto.KernelFaults, error) {
	f.rec("kernel-faults")
	return ctlproto.KernelFaults{Faults: []ctlproto.KernelFault{{Scope: "conversation", Character: "c1", Conversation: "console:c1\x1b[31m", Kind: "run_state", Reason: "状态\n没写进", Detail: "made\x07", At: 1000, Tries: 1}}}, nil
}

func (f *kernelFake) KernelRetry(_ context.Context, character, conversation string) (ctlproto.KernelRetry, error) {
	f.rec("kernel-retry")
	f.retries = append(f.retries, character+"|"+conversation)
	if f.failErr != nil {
		return ctlproto.KernelRetry{Remaining: f.remaining}, f.failErr
	}
	return ctlproto.KernelRetry{Cleared: []ctlproto.KernelFault{{Scope: "conversation", Character: "c1", Conversation: "console:c1", Kind: "run_state", Reason: "好了"}}}, nil
}

func TestKernelFaultsIsReadOnlyAndCleansText(t *testing.T) {
	f := &kernelFake{}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, ctlproto.Request{Cmd: "kernel", Args: []string{"faults"}})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	v := dataOf[ctlproto.KernelFaults](t, r)
	if len(v.Faults) != 1 || strings.ContainsAny(v.Faults[0].Conversation+v.Faults[0].Reason+v.Faults[0].Detail, "\n\x07\x1b") {
		t.Fatalf("控制字符没清掉：%+v", v)
	}
	if len(logs) != 0 {
		t.Fatalf("faults 只读，不该写改状态的日志：%v", logs)
	}
}

func TestKernelRetryForwardsTargetAndLogs(t *testing.T) {
	f := &kernelFake{}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	if r := ask(t, name, ctlproto.Request{Cmd: "kernel", Args: []string{"retry"}}); !r.OK {
		t.Fatalf("不写目标 = 全部：%+v", r)
	}
	if r := ask(t, name, ctlproto.Request{Cmd: "kernel", Args: []string{"retry", "c1", "console:c1"}}); !r.OK {
		t.Fatalf("%+v", r)
	}
	if len(f.retries) != 2 || f.retries[0] != "|" || f.retries[1] != "c1|console:c1" {
		t.Fatalf("目标没有原样交给后端：%v", f.retries)
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "控制接口") {
		t.Fatalf("retry 会改库，该记日志：%v", logs)
	}
}

// 重试没恢复：失败（不是 OK），回复里带着还没恢复的那些；退出码靠 OK=false 判，不会被说成成功
func TestKernelRetryStillFaultedIsFailureWithRemaining(t *testing.T) {
	f := &kernelFake{failErr: errors.New("没有恢复：这个对话暂时没法继续\n（made）"), remaining: []ctlproto.KernelFault{{Scope: "conversation", Character: "c1", Conversation: "console:c1", Reason: "没写进"}}}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "kernel", Args: []string{"retry", "c1", "console:c1"}})
	if r.OK || r.Code != ctlproto.CodeFailed || strings.Contains(r.Error, "\n") || !strings.Contains(r.Error, "没有恢复") {
		t.Fatalf("没恢复该是 failed 并说没恢复：%+v", r)
	}
	v := dataOf[ctlproto.KernelRetry](t, r)
	if len(v.Remaining) != 1 || len(v.Cleared) != 0 {
		t.Fatalf("失败回复里该带着还没恢复的：%+v", v)
	}
}

func TestKernelRejectsBadShapes(t *testing.T) {
	f := &kernelFake{}
	name, _ := start(t, f, Options{})
	bad := []ctlproto.Request{
		{Cmd: "kernel"},
		{Cmd: "kernel", Args: []string{"restart"}},
		{Cmd: "kernel", Args: []string{"faults", "extra"}},
		{Cmd: "kernel", Args: []string{"retry", "c1"}},
		{Cmd: "kernel", Args: []string{"retry", "c1", "console:c1", "extra"}},
		{Cmd: "kernel", Args: []string{"retry", "", "console:c1"}},
		{Cmd: "kernel", Args: []string{"retry", "c1", ""}},
		{Cmd: "kernel", Args: []string{"retry"}, Wait: "5s"},
	}
	for i, req := range bad {
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Errorf("第 %d 条该被拒（invalid）：%+v", i, r)
		}
	}
	if len(f.got()) != 0 {
		t.Fatalf("校验不过的不许调后端：%v", f.got())
	}
}

func TestKernelDemoModeDoesNothing(t *testing.T) {
	f := &kernelFake{}
	name, _ := start(t, f, Options{Demo: true})
	for _, args := range [][]string{{"faults"}, {"retry"}} {
		if r := ask(t, name, ctlproto.Request{Cmd: "kernel", Args: args}); r.OK || r.Code != ctlproto.CodeDemo {
			t.Errorf("演示数据模式该拒：%v %+v", args, r)
		}
	}
	if len(f.got()) != 0 {
		t.Fatalf("演示模式不许调后端：%v", f.got())
	}
}

func TestKernelReadOnlyTable(t *testing.T) {
	if !readOnly(ctlproto.Request{Cmd: "kernel", Args: []string{"faults"}}) {
		t.Fatal("kernel faults 是只读")
	}
	if readOnly(ctlproto.Request{Cmd: "kernel", Args: []string{"retry"}}) {
		t.Fatal("kernel retry 会改库，不是只读")
	}
	if got := describe(ctlproto.Request{Cmd: "kernel", Args: []string{"retry", "c1", "console:c1"}}); got != "kernel retry" {
		t.Fatalf("日志里只写固定的词，不带会话号：%q", got)
	}
}
