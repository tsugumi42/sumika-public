package main

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/chatui"
)

// procScript 是本地私聊 A 里晴枝的一条轨道；steps 后面统一接一句回复，用来看 await 的分支有没有接上。
func procScript(steps ...step) script {
	steps = append(steps, step{Op: "trigger", Trigger: "M06", ID: "reply", Args: args{Text: "好了。", Chunk: "grapheme", IntervalMS: 10}},
		step{Op: "await", Ref: "reply", Then: map[string][]step{"complete": {}}})
	return script{Version: 1, ID: "proc", Seed: 1, Policy: "current", Initial: "four-venues",
		Tracks: []track{{Name: "t", Target: target{"localPrivate", "local", "A", "r1", "$input.turnId"}, Steps: steps}}}
}

// runProc 校验脚本（走和面板同一条 parseScript）、启动，返回引擎和 A 会话。
func runProc(t *testing.T, s script) (*engine, *conversation) {
	t.Helper()
	e := newEngine(1)
	parsed, err := parseScript(scriptJSON(s), e)
	if err != nil {
		t.Fatal(err)
	}
	e.start(parsed, "run.start", "")
	return e, e.conv("A")
}

func procSteps(cv *conversation) []*chatui.Step {
	for _, m := range cv.Messages {
		if len(m.Steps) > 0 {
			return m.Steps
		}
	}
	return nil
}

func last(cv *conversation) *message { return cv.Messages[len(cv.Messages)-1] }

func then(ok string, others ...string) map[string][]step {
	m := map[string][]step{ok: {}}
	for _, o := range others {
		m[o] = []step{}
	}
	return m
}

// 推进虚拟时间并像窗口每帧那样更新秒数、倒计时。
func run(e *engine, d time.Duration) { e.advance(e.Now + d); e.tickSteps() }

// A01：思考进行中 → 想了 N 秒；过程消息排在回复前面。
func TestEngineThink(t *testing.T) {
	e, cv := runProc(t, procScript(step{Op: "trigger", Trigger: "A01", ID: "think", Args: args{DurationMS: 3000, Text: "先看看她说了什么"}}, step{Op: "await", Ref: "think", Then: then("complete")}))
	run(e, time.Second)
	s := procSteps(cv)
	if len(s) != 1 || s[0].Kind != chatui.StepThink || s[0].State != chatui.StepRunning || s[0].Thought == "" {
		t.Fatalf("思考进行中不对：%+v", s)
	}
	run(e, 3*time.Second)
	if s[0].State != chatui.StepDone || s[0].Seconds != 3 || last(cv).Text != "好了。" || len(cv.Messages[len(cv.Messages)-2].Steps) != 1 {
		t.Fatalf("思考完成后不对：%+v，最后一条 %q", s[0], last(cv).Text)
	}
}

// A03：输出按行到达；退出码不是 0 → 失败，走 failed 分支，回复照样出来。
func TestEngineCommandFails(t *testing.T) {
	e, cv := runProc(t, procScript(step{Op: "trigger", Trigger: "A03", ID: "cmd", Args: args{Command: "go test ./...", Output: "a\nb\nc", Exit: 1, DurationMS: 4000}}, step{Op: "await", Ref: "cmd", Then: then("succeeded", "failed")}))
	run(e, 2100*time.Millisecond)
	s := procSteps(cv)[0]
	if s.State != chatui.StepRunning || s.Output != "a\nb\n" || s.Seconds != 2 {
		t.Fatalf("运行中：状态 %v 输出 %q 秒 %d", s.State, s.Output, s.Seconds)
	}
	run(e, 3*time.Second)
	if s.State != chatui.StepFailed || !s.HasExit || s.ExitCode != 1 || s.Seconds != 4 || last(cv).State != "complete" {
		t.Fatalf("失败后：%+v；回复 %q %s", s, last(cv).Text, last(cv).State)
	}
}

// A04 + A05：很慢的命令被取消，过确认延迟才停；取消以后到点的结果只进轨迹，不改成功。
func TestEngineCancelLateResult(t *testing.T) {
	e, cv := runProc(t, procScript(
		step{Op: "trigger", Trigger: "A04", ID: "slow", Args: args{Command: "make all", DurationMS: 8000}},
		step{Op: "wait", MS: 2000},
		step{Op: "trigger", Trigger: "A05", ID: "cancel", Ref: "slow", Args: args{DurationMS: 300}},
		step{Op: "await", Ref: "slow", Then: then("succeeded", "canceled")}))
	run(e, 2100*time.Millisecond)
	s := procSteps(cv)[0]
	if s.State != chatui.StepRunning {
		t.Fatal("确认延迟还没过就停了")
	}
	run(e, 300*time.Millisecond)
	if s.State != chatui.StepStopped || e.conv("A") == nil {
		t.Fatalf("取消后：%v", s.State)
	}
	run(e, 10*time.Second)
	if s.State != chatui.StepStopped || s.HasExit {
		t.Fatalf("迟到的结果改了状态：%+v", s)
	}
	late := false
	for _, tr := range e.Trace {
		late = late || tr.To == "late-result"
	}
	if !late || last(cv).Text != "好了。" {
		t.Errorf("迟到结果没进轨迹（%v）或回复没接上（%q）", late, last(cv).Text)
	}
}

// P01：等批准时 turn 不算完；批准先进提交中（再点不认），之后才执行；拒绝走 denied 分支。
func TestEngineApproval(t *testing.T) {
	e, cv := runProc(t, procScript(step{Op: "trigger", Trigger: "P01", ID: "rm", Args: args{Tool: "fake-command", Command: "rm -rf build", Text: "清掉旧的构建", TTLMS: 60000, DurationMS: 1000, Output: "removed"}}, step{Op: "await", Ref: "rm", Then: then("succeeded", "denied", "expired")}))
	run(e, 10*time.Second)
	s := procSteps(cv)[0]
	if s.Approval.State != chatui.ApprovalPending || !e.active("A") || s.Approval.Remaining != 50*time.Second || s.Output != "" {
		t.Fatalf("等批准：%+v，turn 在跑 %v", s.Approval, e.active("A"))
	}
	if !e.approve(s.ID, chatui.AllowOnce) || e.approve(s.ID, chatui.AllowOnce) || s.Approval.State != chatui.ApprovalSubmitting {
		t.Fatal("提交中又认了一次批准，或没进提交中")
	}
	run(e, 200*time.Millisecond)
	if s.Approval.State != chatui.ApprovalAllowed || s.State != chatui.StepRunning {
		t.Fatalf("批准后没开始跑：%+v", s)
	}
	run(e, 2*time.Second)
	if s.State != chatui.StepDone || s.Output != "removed\n" || last(cv).Text != "好了。" {
		t.Fatalf("执行完：%+v；回复 %q", s, last(cv).Text)
	}

	e, cv = runProc(t, procScript(step{Op: "trigger", Trigger: "P01", ID: "edit", Args: args{Tool: "fake-edit", Path: "notes/a.md", Diff: "@@\n-旧\n+新\n+又一行\n", TTLMS: 60000}}, step{Op: "await", Ref: "edit", Then: then("succeeded", "denied", "expired")}))
	run(e, time.Second)
	s = procSteps(cv)[0]
	if s.Kind != chatui.StepEdit || s.Added != 2 || s.Removed != 1 {
		t.Fatalf("改文件卡：%+v", s)
	}
	e.deny(s.ID)
	run(e, 200*time.Millisecond)
	if s.Approval.State != chatui.ApprovalDenied || s.State != chatui.StepDenied || last(cv).Text != "好了。" {
		t.Fatalf("拒绝后：%+v；回复 %q", s, last(cv).Text)
	}
}

// P04：倒计时到 0 就过期，走 expired 分支；过期以后的批准不认，工具不启动。
func TestEngineApprovalExpires(t *testing.T) {
	e, cv := runProc(t, procScript(step{Op: "trigger", Trigger: "P04", ID: "rm", Args: args{Tool: "fake-command", Command: "rm x", TTLMS: 3000, DurationMS: 100, Output: "removed"}}, step{Op: "await", Ref: "rm", Then: then("succeeded", "denied", "expired")}))
	run(e, 3100*time.Millisecond)
	s := procSteps(cv)[0]
	if s.Approval.State != chatui.ApprovalExpired || s.Approval.Remaining != 0 {
		t.Fatalf("没过期：%+v", s.Approval)
	}
	if e.approve(s.ID, chatui.AllowOnce) {
		t.Fatal("过期以后的批准被认了")
	}
	run(e, 5*time.Second)
	if s.Output != "" || s.State == chatui.StepDone || last(cv).Text != "好了。" {
		t.Fatalf("过期以后工具跑了：%+v；回复 %q", s, last(cv).Text)
	}
}

// A08：停下时还在跑的步骤标成停了，没结算的审批撤回；之后的批准不认。
func TestEngineStopRevokes(t *testing.T) {
	e, cv := runProc(t, procScript(
		step{Op: "trigger", Trigger: "A01", ID: "think", Args: args{DurationMS: 60000}},
		step{Op: "trigger", Trigger: "P01", ID: "rm", Args: args{Tool: "fake-command", Command: "rm x", TTLMS: 60000}},
		step{Op: "await", Ref: "rm", Then: then("succeeded", "denied", "expired")}))
	run(e, time.Second)
	e.stop("A")
	st := procSteps(cv)
	if st[0].State != chatui.StepStopped || st[1].Approval.State != chatui.ApprovalRevoked || e.active("A") {
		t.Fatalf("停下后：思考 %v，审批 %v，turn 在跑 %v", st[0].State, st[1].Approval.State, e.active("A"))
	}
	if e.approve(st[1].ID, chatui.AllowOnce) {
		t.Fatal("撤回以后的批准被认了")
	}
}

// 新触发器的参数校验：缺东西、不在本地、A05 指错、await 用了不认识的终态，都不接收。
func TestEngineScriptValidation(t *testing.T) {
	ok := procScript(step{Op: "trigger", Trigger: "P01", ID: "x", Args: args{Tool: "fake-command", Command: "ls", TTLMS: 1000}}, step{Op: "await", Ref: "x", Then: then("succeeded", "denied", "expired")})
	if _, err := parseScript(scriptJSON(ok), newEngine(1)); err != nil {
		t.Fatalf("正常脚本被拒：%v", err)
	}
	bad := map[string]script{
		"A03 没命令":       procScript(step{Op: "trigger", Trigger: "A03", ID: "x", Args: args{DurationMS: 10}}),
		"A04 太快":        procScript(step{Op: "trigger", Trigger: "A04", ID: "x", Args: args{Command: "ls", DurationMS: 100}}),
		"A05 指向读":       procScript(step{Op: "trigger", Trigger: "A02", ID: "r", Args: args{Tool: "fake-read"}}, step{Op: "trigger", Trigger: "A05", ID: "c", Ref: "r"}),
		"P01 没有效期":      procScript(step{Op: "trigger", Trigger: "P01", ID: "x", Args: args{Tool: "fake-command", Command: "ls"}}),
		"P01 改文件没 diff": procScript(step{Op: "trigger", Trigger: "P01", ID: "x", Args: args{Tool: "fake-edit", Path: "a", TTLMS: 1}}),
		"await 不认识的终态":  procScript(step{Op: "trigger", Trigger: "A03", ID: "x", Args: args{Command: "ls"}}, step{Op: "await", Ref: "x", Then: then("succeeded", "exploded")}),
	}
	qq := procScript(step{Op: "trigger", Trigger: "A03", ID: "x", Args: args{Command: "ls"}})
	qq.Tracks[0].Target = target{"qqPrivate", "q1", "C", "r1", "$input.turnId"}
	bad["QQ 里跑命令"] = qq
	for name, s := range bad {
		if _, err := parseScript(scriptJSON(s), newEngine(1)); err == nil {
			t.Errorf("%s：被接收了", name)
		} else if strings.Contains(err.Error(), "第一步") {
			t.Errorf("%s：报错还写着第一步：%v", name, err)
		}
	}
}
