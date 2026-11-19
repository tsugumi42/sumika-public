package turn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/runstate"
	"sumika-native/internal/tools"
)

// Real Hub, HTTP model transport, SQLite and tool output persistence; barriers
// hold the dispatched tool while stop is acknowledged and new input is rejected.
func TestStopStagesGracePersistsResult(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"owner", "seal", "caller", "plugin"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan context.Context, 1), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var executions atomic.Int32
			runTool := func(ctx context.Context) (tools.Result, error) {
				executions.Add(1)
				entered <- ctx
				select {
				case <-release:
				case <-ctx.Done():
					return tools.Result{}, ctx.Err()
				}
				return tools.Result{Text: "fixture committed result"}, nil
			}
			name, args := "write", writeCall
			if mode == "plugin" {
				name, args = "recall_raw", `{"keywords":"fixture"}`
			}
			h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + callReply(name, args) + `,` + textReply + `]`, mutate: func(c *Config) {
				c.Lifecycle.StopGrace = time.Second
				c.ExecTool = func(ctx context.Context, wd, name string, a jsjson.Value) (tools.Result, error) {
					r, err := runTool(ctx)
					if err == nil {
						err = os.WriteFile(filepath.Join(wd, "grace-result.txt"), []byte(r.Text), 0600)
					}
					return r, err
				}
			}})
			if mode == "plugin" {
				h.use(toolPlugin{"grace-plugin", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(ctx context.Context, _ ToolCall) (tools.Result, error) { return runTool(ctx) }}}})
			}
			caller, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "停止测试", "grace", kernel.InputStart)
			s.Input.requestCtx = caller
			rec, err := h.hub.Submit(caller, s)
			if err != nil {
				t.Fatal(err)
			}
			toolCtx := receive(t, entered)
			switch mode {
			case "seal", "plugin":
				h.hub.Seal()
				if _, err := h.hub.Submit(context.Background(), lifecycleSubmission(t, h, s.Input.Conversation, "封口后输入", "sealed", kernel.InputQueue)); !errors.Is(err, runstate.ErrSealed) {
					t.Fatalf("sealed admission: %v", err)
				}
			case "caller":
				cancel()
			default:
				stopped, err := h.hub.StopRun(context.Background(), kernel.StopRequest{Scope: s.Input.Identity.Scope, RunID: rec.RunID, ThroughSequence: rec.Sequence})
				if err != nil || stopped.Drained {
					t.Fatalf("stop receipt: %+v %v", stopped, err)
				}
				sub, err := h.hub.WatchRun(s.Input.Identity.Scope, rec.RunID)
				if err != nil {
					t.Fatal(err)
				}
				snap := sub.Snapshot()
				sub.Close()
				if snap.State != kernel.RunStopping || snap.Outcome != kernel.OutcomeStopped || snap.Drained {
					t.Fatalf("visible state: %+v", snap)
				}
			}
			if toolCtx.Err() != nil {
				t.Fatalf("tool cancelled before grace: %v", toolCtx.Err())
			}
			unblock()
			terminal := lifecycleWait(t, h.hub, s.Input.Identity.Scope, rec.RunID)
			if terminal.Outcome != kernel.OutcomeStopped {
				t.Fatalf("terminal: %+v", terminal)
			}
			step := h.stepState("c1")
			if step.status != "done" || step.preview != "fixture committed result" {
				t.Fatalf("persisted result: %+v", step)
			}
			if mode != "plugin" {
				b, err := os.ReadFile(filepath.Join(h.wd, "grace-result.txt"))
				if err != nil || string(b) != "fixture committed result" {
					t.Fatalf("file: %q %v", b, err)
				}
			}
			h.mu.Lock()
			requests := len(h.bodies)
			h.mu.Unlock()
			if requests != 1 || executions.Load() != 1 {
				t.Fatalf("new work after stop: model=%d tool=%d", requests, executions.Load())
			}
			t.Logf("mode=%s stopped_before_exit=true result_persisted=true model_requests=%d", mode, requests)
		})
	}
}

func TestStopStagesEscalationKeepsRealExit(t *testing.T) {
	t.Parallel()
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	finalized := make(chan tools.Result, 1)
	h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`, mutate: func(c *Config) {
		c.Lifecycle = LifecycleConfig{StopGrace: time.Millisecond, StopGraceSet: true, DrainTimeout: 50 * time.Millisecond}
		c.ExecTool = func(ctx context.Context, _, _ string, _ jsjson.Value) (tools.Result, error) {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release
			return tools.Result{Text: "late success"}, nil
		}
		c.OnToolFinalized = func(_ context.Context, _ kernel.Invocation, r tools.Result) error { finalized <- r; return nil }
	}})
	s := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "超时测试", "force", kernel.InputStart)
	rec, err := h.hub.Submit(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	receive(t, entered)
	h.hub.Seal()
	receive(t, cancelled)
	r := receive(t, finalized)
	if !r.IsError || !strings.Contains(r.Text, "可能已经执行了一部分") {
		t.Fatalf("unknown effects: %+v", r)
	}
	sub, err := h.hub.WatchRun(s.Input.Identity.Scope, rec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if sub.Snapshot().Drained {
		t.Fatal("uncooperative tool falsely drained")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = h.hub.Drain(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded drain: %v", err)
	}
	if len(h.hub.Cfg.Scheduler.global) != 1 {
		t.Fatal("slot released before real exit")
	}
	unblock()
	lifecycleWait(t, h.hub, s.Input.Identity.Scope, rec.RunID)
	if err := h.hub.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	step := h.stepState("c1")
	if step.status != "error" || !strings.Contains(step.preview, "可能已经执行了一部分") || strings.Contains(step.preview, "late success") {
		t.Fatalf("late result overwrote unknown: %+v", step)
	}
	t.Log("stage3_cancelled=true unknown_persisted=true drain_bounded=true late_success_discarded=true")
}

func TestStopStagesRepeatedStopKeepsDeadline(t *testing.T) {
	t.Parallel()
	ctx, s := newStagedStop(context.Background(), time.Hour, t.Logf, nil)
	defer s.finish()
	s.stop()
	original := s.started
	s.stop()
	if !original.Equal(s.started) || ctx.Err() == nil {
		t.Fatal("repeated stop reset deadline")
	}
	s.immediate()
	receive(t, s.hard.Done())
}

func TestStopStageLogsDurations(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	_, grace := newStagedStop(context.Background(), time.Hour, logf, nil)
	grace.stop()
	grace.stop()
	grace.finish()
	mu.Lock()
	got := append([]string(nil), lines...)
	lines = nil
	mu.Unlock()
	if len(got) != 2 || !strings.Contains(got[0], "stage=1") || !strings.Contains(got[0], "elapsed_ms=0") || !strings.Contains(got[0], "result=sealed") || !strings.Contains(got[1], "stage=2") || !strings.Contains(got[1], "result=drained") {
		t.Fatalf("grace logs: %#v", got)
	}
	_, forced := newStagedStop(context.Background(), time.Hour, logf, nil)
	forced.stop()
	forced.immediate()
	forced.finish()
	mu.Lock()
	got = append([]string(nil), lines...)
	mu.Unlock()
	joined := strings.Join(got, "\n")
	for _, want := range []string{"stage=1", "result=sealed", "stage=2", "result=expired", "stage=3", "result=cancelled", "result=drained"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
	if strings.Contains(joined, "conversation=") || strings.Contains(joined, "停止测试") {
		t.Fatalf("log carried payload: %s", joined)
	}
}

func TestStopStagesDrainDoesNotExtendGrace(t *testing.T) {
	t.Parallel()
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`, mutate: func(c *Config) {
		c.Lifecycle = LifecycleConfig{StopGrace: time.Hour, StopGraceSet: true, DrainTimeout: time.Hour + time.Second}
		c.ExecTool = func(ctx context.Context, _, _ string, _ jsjson.Value) (tools.Result, error) {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release
			return tools.Result{Text: "late success"}, nil
		}
	}})
	s := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "截止测试", "budget", kernel.InputStart)
	if _, err := h.hub.Submit(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	receive(t, entered)
	h.hub.Seal()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	err := h.hub.Drain(ctx)
	cancel()
	if elapsed := time.Since(start); elapsed > barrierLimit || !errors.Is(err, context.DeadlineExceeded) { // 宽限一小时，拖过宽限的改法照样红
		t.Fatalf("drain extended grace: %v after %v", err, elapsed)
	}
	receive(t, cancelled)
	unblock()
	if err := h.hub.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// 生成已经结束、只剩工具收尾的回合，结局已定，不改写成“被停下”。
func TestMarkStoppingKeepsDecidedOutcome(t *testing.T) {
	t.Parallel()
	r := &inputRun{}
	r.snapshot.State, r.snapshot.Outcome = kernel.RunDraining, kernel.OutcomeDone
	markStoppingLocked(r)
	if r.snapshot.State != kernel.RunDraining || r.snapshot.Outcome != kernel.OutcomeDone {
		t.Fatalf("draining 被改写：%+v", r.snapshot)
	}
	r.snapshot.State, r.snapshot.Outcome = kernel.RunRunning, ""
	markStoppingLocked(r)
	if r.snapshot.State != kernel.RunStopping || r.snapshot.Outcome != kernel.OutcomeStopped {
		t.Fatalf("生成中的回合该标停止：%+v", r.snapshot)
	}
}

// 点了停止，正在跑的命令马上取消，不等宽限（TestShellStopMatch 用真实 pwsh 在每日整套里再验一遍）。
func TestStopCancelsCommandAtOnce(t *testing.T) {
	t.Parallel()
	entered := make(chan context.Context, 1)
	h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + callReply("powershell", `{"command":"编造的长命令"}`) + `,` + textReply + `]`, mutate: func(c *Config) {
		// 宽限被 DrainTimeout 封顶：只设 StopGrace 实际只有几秒，不马上取消的改法等到宽限也能过，这里两个都放长
		c.Lifecycle = LifecycleConfig{StopGrace: time.Hour, StopGraceSet: true, DrainTimeout: time.Hour + time.Second}
		c.ExecTool = func(ctx context.Context, _, _ string, _ jsjson.Value) (tools.Result, error) {
			entered <- ctx
			<-ctx.Done()
			return tools.Result{}, ctx.Err()
		}
	}})
	s := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "停止命令", "command", kernel.InputStart)
	rec, err := h.hub.Submit(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	toolCtx := enteredOrDrained(t, h.hub, s.Input.Identity.Scope, rec.RunID, entered)
	if _, err := h.hub.StopRun(context.Background(), kernel.StopRequest{Scope: s.Input.Identity.Scope, RunID: rec.RunID, ThroughSequence: rec.Sequence}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-toolCtx.Done():
	case <-time.After(barrierLimit):
		t.Fatal("宽限一小时，命令却没有马上被取消")
	}
	lifecycleWait(t, h.hub, s.Input.Identity.Scope, rec.RunID)
}

// enteredOrDrained 等假工具被调到；这一轮没走到工具就结束了，立刻报出这一轮的快照，不干等满 barrierLimit（issue #36：
// 某个沙箱里命令没进假工具，等了 60 秒才报“fixture barrier not reached”，看不出卡在哪）
func enteredOrDrained[T any](t *testing.T, h *Hub, scope kernel.Scope, id string, entered <-chan T) T {
	t.Helper()
	w, err := h.WatchRun(scope, id)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	deadline := time.NewTimer(barrierLimit)
	defer deadline.Stop()
	for {
		select {
		case x := <-entered:
			return x
		default:
		}
		if v := w.Snapshot(); v.Drained {
			select {
			case x := <-entered:
				return x
			default:
			}
			t.Fatalf("工具还没调到，这一轮就结束了（多半是前置条件不满足，比如本机找不到命令行）：%+v", v)
		}
		select {
		case x := <-entered:
			return x
		case <-w.Wake():
		case <-deadline.C:
			t.Fatalf("等了 %v 工具还没被调到，这一轮也没结束：%+v", barrierLimit, w.Snapshot())
		}
	}
}
