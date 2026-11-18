package main

// goal、loop 子命令的端到端：命令行 → 带测试名的管道 → internal/ctlserver 的校验 → 假后端，再走回来变成退出码和输出。

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

type slashStub struct {
	stubBackend
	smu  sync.Mutex
	reqs []ctlserver.SlashRequest
	err  error
}

func (b *slashStub) Slash(_ context.Context, req ctlserver.SlashRequest) (ctlproto.SlashResult, error) {
	b.smu.Lock()
	b.reqs = append(b.reqs, req)
	b.smu.Unlock()
	if b.err != nil {
		return ctlproto.SlashResult{}, b.err
	}
	res := ctlproto.SlashResult{Character: "example", Topic: "selftest-example", Conversation: "console:example:topic:selftest-example", Reply: "已记下 " + req.Text,
		Goal: &ctlproto.GoalState{Text: "建 hello.txt", Status: "active", Rounds: 2, MaxRounds: 30, Line: "目标：建 hello.txt · 第 3 轮 · 进行中", LastJudge: "还差文件"}}
	if req.Command == "loop" {
		res.Goal = nil
		res.Loops = []ctlproto.LoopTask{{ID: 1, Status: "running", Prompt: "报时", IntervalSec: 1800, Line: "#1 每 30 分钟 · 报时 · 已跑 0 次"}}
	}
	return res, nil
}

func TestGoalLoopAgainstRealServer(t *testing.T) {
	name := testPipeName("slash")
	ln, err := ctlserver.ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	b := &slashStub{stubBackend: stubBackend{quit: make(chan struct{})}}
	srv := ctlserver.New(b, ctlserver.Options{})
	go srv.Serve(ln)
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, _ bool) (net.Conn, error) { return ctlproto.Dial(ctx, name) }
	do := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb, dial)
		return code, out.String(), errb.String()
	}
	last := func() ctlserver.SlashRequest {
		b.smu.Lock()
		defer b.smu.Unlock()
		return b.reqs[len(b.reqs)-1]
	}

	// goal set：--json 是一个对象，data 里有回复和目标现状；后端收到拼好的斜杠命令全文
	code, out, _ := do("--json", "goal", "set", "建", "hello.txt", "--character", "example")
	var rep ctlproto.Reply
	if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || !rep.OK {
		t.Fatalf("goal set --json：%d %q", code, out)
	}
	var res ctlproto.SlashResult
	if err := json.Unmarshal(rep.Data, &res); err != nil || res.Goal == nil || res.Goal.Status != "active" || res.Loops == nil || res.Conversation == "" {
		t.Fatalf("goal set 的 data：%+v %v", res, err)
	}
	if got := last(); got.Text != "/goal 建 hello.txt" || got.Sub != "set" || got.Character != "example" {
		t.Fatalf("后端收到：%+v", got)
	}
	// 不写子命令 = show；给人看的输出里有目标、状态、上次判断
	if code, out, _ := do("goal"); code != 0 || !strings.Contains(out, "目标：建 hello.txt") || !strings.Contains(out, "进行中") || !strings.Contains(out, "上次判断：还差文件") {
		t.Fatalf("goal：%d %q", code, out)
	}
	if got := last(); got.Sub != "show" || got.Text != "/goal" {
		t.Fatalf("goal 不带子命令应是 show：%+v", got)
	}
	for _, sub := range []string{"pause", "resume", "clear"} {
		if code, _, _ := do("goal", sub); code != 0 {
			t.Fatalf("goal %s：%d", sub, code)
		}
		if got := last(); got.Text != "/goal "+sub {
			t.Fatalf("goal %s 后端收到：%+v", sub, got)
		}
	}
	// 目标原文以 - 开头要用 -- 隔开
	if code, _, _ := do("goal", "set", "--", "-5 度也要做完"); code != 0 || last().Text != "/goal -5 度也要做完" {
		t.Fatalf("-- 之后的原文：%d %+v", code, last())
	}

	// loop：list 的输出是任务那一行；remove 变成 stop；编号写 #3 也行
	if code, out, _ := do("loop", "list"); code != 0 || !strings.Contains(out, "#1 每 30 分钟") {
		t.Fatalf("loop list：%d %q", code, out)
	}
	if code, _, _ := do("loop", "add", "30m", "报时"); code != 0 || last().Text != "/loop 30m 报时" {
		t.Fatalf("loop add：%d %+v", code, last())
	}
	if code, _, _ := do("loop", "remove", "#3"); code != 0 || last().Text != "/loop stop 3" || last().ID != 3 {
		t.Fatalf("loop remove：%d %+v", code, last())
	}
	if code, _, _ := do("loop", "edit", "3", "2h"); code != 0 || last().Text != "/loop edit 3 2h" {
		t.Fatalf("loop edit：%d %+v", code, last())
	}

	// 退出码：写错了 3（--json 也给一个对象）；后端报 invalid 3；后端报别的错 1；写法过关但被拒也不碰后端
	calls := len(b.reqs)
	for _, args := range [][]string{{"goal", "nuke"}, {"goal", "set"}, {"goal", "set", "/goal clear"}, {"loop", "remove"}, {"loop", "remove", "x"}, {"loop", "add"}, {"goal", "show", "多余"}} {
		if code, _, errs := do(args...); code != 3 || !strings.Contains(errs, "invalid") {
			t.Fatalf("%v 应被拒（退出码 3）：%d %q", args, code, errs)
		}
	}
	code, out, _ = do("--json", "loop", "remove")
	if code != 3 || json.Unmarshal([]byte(out), &rep) != nil || rep.OK || rep.Code != ctlproto.CodeInvalid {
		t.Fatalf("--json 被拒：%d %q", code, out)
	}
	if len(b.reqs) != calls {
		t.Fatalf("被拒的请求不该到后端：%d → %d", calls, len(b.reqs))
	}
	b.err = ctlproto.Errorf(ctlproto.CodeInvalid, "命令不合格：任务文字以子命令词开头")
	if code, _, errs := do("loop", "add", "stop 3"); code != 3 || !strings.Contains(errs, "子命令词") {
		t.Fatalf("后端的 invalid：%d %q", code, errs)
	}
	b.err = ctlproto.Errorf(ctlproto.CodeFailed, "这个会话没有目标")
	if code, _, errs := do("goal", "resume"); code != 1 || !strings.Contains(errs, "没有目标") {
		t.Fatalf("后端的 failed：%d %q", code, errs)
	}
	b.err = ctlproto.Errorf(ctlproto.CodeReadOnly, "只看历史模式不能设目标")
	if code, _, errs := do("--json", "goal", "set", "x"); code != 1 || errs != "" {
		t.Fatalf("readonly --json：%d %q", code, errs)
	}
}
