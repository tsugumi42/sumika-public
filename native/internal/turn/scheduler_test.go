package turn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/tools"
	"sumika-native/internal/turnbudget"
)

func batchCall(id, name, path string) Call {
	return Call{ID: id, Name: name, Args: jsjson.Value{Kind: jsjson.Object, Obj: map[string]jsjson.Value{"path": {Kind: jsjson.String, Str: path}, "purpose": {Kind: jsjson.String, Str: "fixture"}, "content": {Kind: jsjson.String, Str: "fiction"}}}}
}
func batchRun(ctx context.Context, h *harness, cfg Config, calls ...Call) ([]Msg, error) {
	decls := []tools.Declaration{{Name: "read"}, {Name: "write"}, {Name: "ls"}, {Name: "external_write"}}
	return runTools(ctx, cfg, Workspace{CWD: h.wd}, "fixture-context", h.st, h.hub.plugins, Msg{Calls: calls}, "toolUse", h.c.CharacterID, "console:"+h.c.CharacterID, audience.Console("console:"+h.c.CharacterID), decls, nil, "fixture-run", &repeatGuard{}, nil)
}

// barrierLimit 是等屏障、等回合收尾的兜底时限，只防真卡住：卡住的怎么等都到不了，改坏了照样红，只是红得慢。
// 合并队列低优先级跑、CPU 80% 以上时，起 pwsh、写库停几秒很常见，所以放到一分钟。
const barrierLimit = time.Minute

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case x := <-ch:
		return x
	case <-time.After(barrierLimit):
		t.Fatal("fixture barrier not reached")
		var zero T
		return zero
	}
}

func TestBoundedMixedToolBatch(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{mode: "yolo"})
	cfg := h.hub.Cfg
	cfg.Budget = nil
	cfg.Scheduler = NewToolScheduler(2, 2)
	started := make(chan string, 8)
	finished := make(chan string, 8)
	release := map[string]chan struct{}{"a": make(chan struct{}), "b": make(chan struct{}), "c": make(chan struct{}), "w": make(chan struct{}), "d": make(chan struct{})}
	cfg.ExecTool = func(_ context.Context, _, _ string, args jsjson.Value) (tools.Result, error) {
		p := args.Obj["path"].Str
		started <- p
		<-release[p]
		return tools.Result{Text: p}, nil
	}
	cfg.OnToolFinalized = func(_ context.Context, in kernel.Invocation, r tools.Result) error {
		finished <- in.ProviderCallID
		return nil
	}
	type result struct {
		msgs []Msg
		err  error
	}
	done := make(chan result, 1)
	go func() {
		msgs, err := batchRun(context.Background(), h, cfg, batchCall("a", "read", "a"), batchCall("b", "read", "b"), batchCall("c", "read", "c"), batchCall("w", "write", "w"), batchCall("d", "read", "d"))
		done <- result{msgs, err}
	}()
	first, second := receive(t, started), receive(t, started)
	if first == second || first == "w" || second == "w" {
		t.Fatalf("first reads: %s %s", first, second)
	}
	close(release["b"])
	if receive(t, finished) != "b" {
		t.Fatal("later completion was hidden behind a")
	}
	if receive(t, started) != "c" {
		t.Fatal("pool did not replenish while a still running")
	}
	close(release["c"])
	receive(t, finished)
	select {
	case p := <-started:
		t.Fatalf("exclusive crossed a: %s", p)
	case <-time.After(100 * time.Millisecond):
		// c 的完成回调先于协调器继续；立即 default 会漏掉随后错误派发的 write。
	}
	close(release["a"])
	receive(t, finished)
	if receive(t, started) != "w" {
		t.Fatal("write barrier missing")
	}
	close(release["w"])
	receive(t, finished)
	if receive(t, started) != "d" {
		t.Fatal("read after write missing")
	}
	close(release["d"])
	got := receive(t, done)
	if got.err != nil {
		t.Fatal(got.err)
	}
	for i, id := range []string{"a", "b", "c", "w", "d"} {
		if got.msgs[i].CallID != id || got.msgs[i].Text != id {
			t.Fatalf("ordinal %d: %+v", i, got.msgs)
		}
	}
	t.Log("per_run_peak=2 global_peak=2 completion_before_a=b rolling_refill=c ordered=a,b,c,w,d")
}

// 业务错误是模型可读的工具结果；仍在执行的同批调用和后续补位不能被连带取消。
func TestToolBusinessErrorPreservesBatch(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{mode: "yolo"})
	cfg := h.hub.Cfg
	cfg.Budget = nil
	cfg.Scheduler = NewToolScheduler(2, 2)
	peerStarted, nextStarted := make(chan struct{}), make(chan struct{})
	cfg.ExecTool = func(ctx context.Context, _, _ string, args jsjson.Value) (tools.Result, error) {
		switch args.Obj["path"].Str {
		case "bad":
			<-peerStarted
			return tools.Result{Text: "fixture read failed", IsError: true}, nil
		case "peer":
			close(peerStarted)
			select {
			case <-ctx.Done():
				return tools.Result{}, ctx.Err()
			case <-nextStarted:
				return tools.Result{Text: "peer survived"}, nil
			}
		default:
			close(nextStarted)
			return tools.Result{Text: "next completed"}, nil
		}
	}
	type result struct {
		msgs []Msg
		err  error
	}
	done := make(chan result, 1)
	go func() {
		msgs, err := batchRun(context.Background(), h, cfg, batchCall("bad", "read", "bad"), batchCall("peer", "read", "peer"), batchCall("next", "read", "next"))
		done <- result{msgs, err}
	}()
	got := receive(t, done)
	if got.err != nil || len(got.msgs) != 3 || !got.msgs[0].IsError || got.msgs[0].Text != "fixture read failed" || got.msgs[1].Text != "peer survived" || got.msgs[2].Text != "next completed" {
		t.Fatalf("业务错误应保留同行和补位结果：%+v err=%v", got.msgs, got.err)
	}
}

func TestTimedOutToolKeepsSlot(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{mode: "yolo"})
	cfg := h.hub.Cfg
	cfg.Budget = nil
	cfg.Scheduler = NewToolScheduler(1, 1)
	entered, release, visible := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	var writes atomic.Int32
	cfg.ToolTimeout = func(string, time.Duration) time.Duration { return 20 * time.Millisecond }
	cfg.ExecTool = func(_ context.Context, _, name string, _ jsjson.Value) (tools.Result, error) {
		if name == "write" {
			writes.Add(1)
			return tools.Result{Text: "wrote"}, nil
		}
		close(entered)
		<-release
		return tools.Result{Text: "late"}, nil
	}
	cfg.OnToolFinalized = func(_ context.Context, _ kernel.Invocation, r tools.Result) error {
		if strings.HasPrefix(r.Text, "超时了") {
			visible <- struct{}{}
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := batchRun(context.Background(), h, cfg, batchCall("slow", "read", "a"), batchCall("write", "write", "b"))
		done <- err
	}()
	receive(t, entered)
	receive(t, visible)
	if len(cfg.Scheduler.global) != 1 || writes.Load() != 0 {
		t.Fatal("timeout released real slot")
	}
	select {
	case <-done:
		t.Fatal("batch reported drained before real exit")
	default:
	}
	close(release)
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Scheduler.global) != 0 || writes.Load() != 0 {
		t.Fatalf("stopped batch dispatched write: %d", writes.Load())
	}
	t.Log("timeout_visible_before_exit=true slot_held=1 writes_after_timeout=0 drained_after_release=true")
}

func TestApprovalCancelAndBindingChange(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"cancel", "binding"} {
		t.Run(change, func(t *testing.T) {
			h := newHarnessOpts(t, harnessOpts{mode: "manual"})
			cfg := h.hub.Cfg
			cfg.Budget = nil
			var writes atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg.ExecTool = func(_ context.Context, _, name string, _ jsjson.Value) (tools.Result, error) {
				if name == "write" {
					writes.Add(1)
				}
				return tools.Result{Text: "read evidence"}, nil
			}
			readerDone := make(chan struct{}, 1)
			cfg.OnToolFinalized = func(_ context.Context, in kernel.Invocation, _ tools.Result) error {
				if in.ProviderCallID == "read" {
					readerDone <- struct{}{}
				}
				return nil
			}
			var valid atomic.Bool
			valid.Store(true)
			secondTool := "write"
			if change == "binding" {
				secondTool = "external_write"
				cfg.Resolver = resolverFunc(func(ctx context.Context, c ToolCall) (PreparedCall, error) {
					return PreparedCall{Name: c.Name, Args: c.Args, Valid: valid.Load, Execute: func(ctx context.Context) (tools.Result, error) { return cfg.ExecTool(ctx, h.wd, "write", c.Args) }}, nil
				})
			}
			h.approvals.OnRequest = append(h.approvals.OnRequest, func(req approval.Request) {
				receive(t, readerDone)
				if change == "cancel" {
					cancel()
				} else {
					valid.Store(false)
					h.approvals.Decide(req.ID, "once", "")
				}
			})
			msgs, err := batchRun(ctx, h, cfg, batchCall("read", "read", "a"), batchCall("write", secondTool, "b"))
			if err != nil {
				t.Fatal(err)
			}
			if writes.Load() != 0 || msgs[0].Text != "read evidence" || msgs[1].Text == "" {
				t.Fatalf("approval race writes=%d msgs=%+v", writes.Load(), msgs)
			}
		})
	}
	for _, scene := range []struct {
		channel, scene, conv string
		allow                bool
	}{{"console", "private", "console:fiction", true}, {"console", "group", "console:group:fiction", false}, {"qq", "private", "qq:private:fiction", false}, {"qq", "group", "qq:group:fiction", false}} {
		t.Run(fmt.Sprint(scene.channel, "_", scene.scene), func(t *testing.T) {
			h := newHarnessOpts(t, harnessOpts{mode: "yolo"})
			cfg := h.hub.Cfg
			var ran atomic.Int32
			cfg.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				ran.Add(1)
				return tools.Result{}, nil
			}
			full := audience.Audience{Channel: scene.channel, Scene: scene.scene, Conversation: scene.conv, PrivateOK: true}
			_, imm, err := prepareCall(context.Background(), cfg, Workspace{CWD: h.wd}, "ctx", h.hub.plugins, batchCall("fake-owner", "write", "b"), "", h.c.CharacterID, scene.conv, "run", full, []tools.Declaration{{Name: "write"}}, nil, kernel.Invocation{ToolID: "host-id"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if scene.allow && imm != nil || !scene.allow && imm == nil || ran.Load() != 0 {
				t.Fatalf("four scene gate imm=%+v ran=%d", imm, ran.Load())
			}
		})
	}
}

type resolverFunc func(context.Context, ToolCall) (PreparedCall, error)

func (f resolverFunc) Prepare(ctx context.Context, c ToolCall) (PreparedCall, error) {
	return f(ctx, c)
}

func TestBudgetExhaustionSettlesPartial(t *testing.T) {
	t.Parallel()
	for _, dimension := range []string{"requests", "tools", "tokens", "output", "deadline"} {
		t.Run(dimension, func(t *testing.T) {
			t.Parallel()
			var expired atomic.Bool
			calls := `{"toolCalls":[{"id":"first","name":"write","args":["{\"path\":\"a\",\"content\":\"fixture\",\"purpose\":\"fixture\"}"]}],` + usage + `}`
			if dimension == "tools" {
				calls = `{"toolCalls":[{"id":"first","name":"write","args":["{\"path\":\"a\",\"content\":\"fixture\",\"purpose\":\"fixture\"}"]},{"id":"second","name":"write","args":["{\"path\":\"b\",\"content\":\"fixture\",\"purpose\":\"fixture\"}"]}],` + usage + `}`
			}
			h := newHarnessOpts(t, harnessOpts{mode: "yolo", replies: `[` + calls + `,` + textReply + `]`, mutate: func(c *Config) {
				b := config.DefaultBudget()
				switch dimension {
				case "requests":
					b.MaxRequests, b.Local.MaxRequests = 1, 1
				case "tools":
					b.MaxTools, b.Local.MaxTools = 1, 1
				case "tokens":
					b.MaxTokens, b.Local.MaxTokens = 1, 1
				case "output":
					b.MaxOutputTokens, b.Local.MaxOutputTokens = 10, 10
				case "deadline":
					b.MaxDurationMs, b.Local.MaxDurationMs = 60000, 60000 // 本地默认不设时限，这里设 1 分钟，再把时钟拨快一小时
				}
				c.BudgetConfig = b
				c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
					if dimension == "deadline" {
						expired.Store(true)
					}
					return tools.Result{Text: "preserved evidence"}, nil
				}
			}})
			if dimension == "deadline" {
				h.hub.Cfg.Budget.(*turnbudget.Ledger).Now = func() time.Time {
					now := time.Now()
					if expired.Load() {
						return now.Add(time.Hour)
					}
					return now
				}
			}
			outcome := receive(t, h.hub.Send(h.c.CharacterID, "fixture goal"))
			if outcome.Result == nil || !outcome.Result.BudgetExhausted || !errors.Is(outcome.Err, turnbudget.ErrExhausted) {
				t.Fatalf("outcome: %+v", outcome)
			}
			wantRequests := 1
			if dimension == "tokens" {
				wantRequests = 0
			}
			if len(h.bodies) != wantRequests {
				t.Fatalf("explanation request leaked: %d, want %d", len(h.bodies), wantRequests)
			}
			var count int
			if err := h.st.Trace.QueryRow(`SELECT count(*) FROM steps WHERE kind='tool' AND preview='preserved evidence'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != wantRequests {
				t.Fatalf("preserved effects=%d want=%d", count, wantRequests)
			}
		})
	}
}

func TestCompactSharesRootLedger(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`, mutate: smallKeep})
	h.seedRounds(6)
	result := h.send("fixture goal")
	root := result.Built.Invocation.RootID
	before, err := h.hub.Cfg.Budget.Snapshot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	live := h.hub.liveOf(h.c.CharacterID)
	compact := h.hub.compactNow(kernel.WithInvocation(context.Background(), result.Built.Invocation), live.compactSource(live.cfg), "fixture", 0, nil)
	if !compact.OK {
		t.Fatalf("compact %+v", compact)
	}
	after, err := h.hub.Cfg.Budget.Snapshot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := h.st.Trace.QueryRow(`SELECT root_id FROM model_runs WHERE purpose='compact'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != root || after.Requests != before.Requests+1 || after.Tokens != before.Tokens+110 {
		t.Fatalf("compact ledger before=%+v after=%+v root=%s", before, after, got)
	}
}

func TestToolGlobalCapacity(t *testing.T) {
	t.Parallel()
	scheduler := NewToolScheduler(4, 8)
	release := make(chan struct{})
	started := make(chan struct{}, 20)
	done := make(chan error, 3)
	var active, peak atomic.Int32
	for range 3 {
		h := newHarnessOpts(t, harnessOpts{mode: "yolo"})
		cfg := h.hub.Cfg
		cfg.Budget = nil
		cfg.Scheduler = scheduler
		cfg.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
			n := active.Add(1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return tools.Result{Text: "fixture"}, nil
		}
		go func() {
			_, err := batchRun(context.Background(), h, cfg, batchCall("a", "read", "a"), batchCall("b", "read", "b"), batchCall("c", "read", "c"), batchCall("d", "read", "d"), batchCall("e", "read", "e"))
			done <- err
		}()
	}
	for range 8 {
		receive(t, started)
	}
	select {
	case <-started:
		close(release)
		t.Fatal("全局 8 个实际执行未退时派发了第 9 个工具")
	case <-time.After(100 * time.Millisecond):
		// 保持前 8 个屏障，给其余协调器争槽；立即释放会掩盖全局超发。
	}
	if peak.Load() != 8 || len(scheduler.global) != 8 {
		t.Fatalf("global peak=%d slots=%d", peak.Load(), len(scheduler.global))
	}
	close(release)
	for range 3 {
		if err := receive(t, done); err != nil {
			t.Fatal(err)
		}
	}
	if peak.Load() != 8 || len(scheduler.global) != 0 {
		t.Fatalf("global exceeded/leaked: %d/%d", peak.Load(), len(scheduler.global))
	}
	t.Log("global_peak=8 per_run_limit=4 admitted_tools=15 leaked_slots=0")
}

func TestInternalModelRevocation(t *testing.T) {
	t.Parallel()
	denied := errors.New("made-delegation-revoked")
	authorized := true
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`, mutate: func(c *Config) {
		c.InternalAuthorize = func(context.Context, kernel.Invocation, string) error {
			if !authorized {
				return denied
			}
			return nil
		}
	}})
	cfg := h.hub.Cfg
	in := kernel.Invocation{Scope: kernel.Scope{CharacterID: h.c.CharacterID, Conversation: "internal:revocation-fixture"}, RootID: "revocation-root", RunID: "child-run", ParentRunID: "parent-run", StepID: "step-before", AttemptID: "attempt-before", Kind: kernel.KindChild}
	limits := cfg.BudgetConfig.Limits(time.Now())
	if err := AdmitBudgetRoot(t.Context(), cfg.Budget, in, limits); err != nil {
		t.Fatal(err)
	}
	child := &internalLimits{limits: limits}
	req := chat.Request{Model: cfg.Models[h.c.CharacterID], Style: chat.StyleBackground,
		Messages: []chat.Message{{Role: chat.RoleUser, Blocks: []chat.Block{chat.Text("编造的独立任务")}}}}
	if _, err := child.stream(t.Context(), cfg, in, req, func(llm.Event) {}); err != nil {
		t.Fatal(err)
	}
	authorized = false
	in.StepID, in.AttemptID = "step-after", "attempt-after"
	if _, err := child.stream(t.Context(), cfg, in, req, func(llm.Event) {}); !errors.Is(err, denied) {
		t.Fatalf("撤权后子模型未拒绝：%v", err)
	}
	snapshot, err := cfg.Budget.Snapshot(t.Context(), in.RootID)
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	requests := len(h.bodies)
	h.mu.Unlock()
	if requests != 1 || snapshot.Requests != 1 {
		t.Fatalf("撤权后仍出网或新增预算占用 requests=%d budget=%+v", requests, snapshot)
	}
}
