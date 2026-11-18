package selfcheck_test

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

// S10 内核停止受理：没有故障 ok；有会话或内核的故障 bad，例子里带哪个会话、什么时候、什么原因类别；故障解除后回到 ok；没接 unknown。
func TestKernelFaultedCheck(t *testing.T) {
	c := findCheck(t, "kernel.faulted", selfcheck.DefaultThresholds())
	if c.Name() != "内核停止受理" {
		t.Fatalf("名字：%s", c.Name())
	}
	env := envAt(t0, selfcheck.NewCounters())
	if r := c.Eval(context.Background(), env); r.State != selfcheck.Unknown {
		t.Fatalf("没接内核故障状态该是 unknown，不能记成正常：%+v", r)
	}
	var faults []selfcheck.KernelFault
	env.Faults = func() []selfcheck.KernelFault { return faults }
	if r := c.Eval(context.Background(), env); r.State != selfcheck.Ok {
		t.Fatalf("没有故障该是 ok：%+v", r)
	}

	at := t0.Add(-10 * time.Minute)
	faults = []selfcheck.KernelFault{
		{CharacterID: "c1", Conversation: "console:c1", Kind: "run_state", At: at, Detail: "database is locked (SQLITE_BUSY)", Tries: 2},
	}
	r := c.Eval(context.Background(), env)
	if r.State != selfcheck.Bad || r.Value != 1 {
		t.Fatalf("有会话故障该是 bad：%+v", r)
	}
	for k, want := range map[string]string{"scope": "conversation", "conversation": "console:c1", "kind": "run_state", "errorClass": selfcheck.StoreBusy, "since": at.Format(time.RFC3339), "tries": "2"} {
		if r.Example[k] != want {
			t.Errorf("例子 %s = %q，要 %q", k, r.Example[k], want)
		}
	}
	if r.Note == "" || r.Next == "" {
		t.Errorf("异常要有原因和下一步：%+v", r)
	}

	// 内核故障排在会话故障前面
	faults = append(faults, selfcheck.KernelFault{Kernel: true, Kind: "startup_recover", At: t0, Detail: "disk full"})
	r = c.Eval(context.Background(), env)
	if r.State != selfcheck.Bad || r.Value != 2 || r.Example["scope"] != "kernel" || r.Example["errorClass"] != selfcheck.StoreFull {
		t.Fatalf("内核故障该排在最前：%+v", r)
	}
	if _, ok := r.Example["conversation"]; ok {
		t.Fatalf("内核故障不带会话：%+v", r.Example)
	}

	// 解除以后回到正常
	faults = nil
	if r := c.Eval(context.Background(), env); r.State != selfcheck.Ok {
		t.Fatalf("故障解除后该回到 ok：%+v", r)
	}

	// 没人认领的活动退出记录：不挡受理，所以仍是 ok，但 Note 和例子里给条数；有故障时一并带在例子里
	stray := 2
	env.StrayEnds = func() int { return stray }
	if r := c.Eval(context.Background(), env); r.State != selfcheck.Ok || r.Example["strayEnds"] != "2" || r.Note == "" || r.Next == "" {
		t.Fatalf("没人认领的退出记录该在 ok 的 Note 里提一句，并给下一步：%+v", r)
	}
	faults = []selfcheck.KernelFault{{CharacterID: "c1", Conversation: "console:c1", Kind: "run_state", At: at}}
	if r := c.Eval(context.Background(), env); r.State != selfcheck.Bad || r.Example["strayEnds"] != "2" {
		t.Fatalf("有故障时例子里也带条数：%+v", r)
	}
	faults, stray = nil, 0
	if r := c.Eval(context.Background(), env); r.State != selfcheck.Ok || r.Note != "" || r.Next != "" {
		t.Fatalf("都清掉以后回到干净的 ok：%+v", r)
	}
}
