package modelcall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/runstate"
	"sumika-native/internal/store"
	"sumika-native/internal/turnbudget"
)

func TestHTTPDispatchAccountingAndReplay(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ledger := &turnbudget.Ledger{Store: st}
	in := kernel.Invocation{RootID: "r", RunID: "run", StepID: "s", AttemptID: "a", Kind: "foreground"}
	limits := kernel.Limits{MaxRequests: 3, MaxTools: 2, MaxTokens: 10000, MaxOutputTokens: 20, Deadline: time.Now().Add(time.Minute)}
	if err := ledger.OpenRoot(ctx, "r", in.Scope, limits); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	var mu sync.Mutex
	reply := llmtest.Text("fixture")
	reply.Usage = json.RawMessage(`{"prompt_tokens":12,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":5},"completion_tokens_details":{"reasoning_tokens":3}}`)
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Reply: reply}, &bodies, &mu))
	defer srv.Close()
	caller := New(llm.New(llm.Magpie(srv.URL+"/v1")), ledger, st, 1, 10)
	req := testChatRequest("fiction", 30)
	result, err := caller.StreamChat(ctx, in, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "fixture" || !result.UsageSeen {
		t.Fatal(result)
	}
	if !strings.Contains(bodies[0], `"max_completion_tokens":10`) {
		t.Fatal(bodies)
	}
	snap, err := ledger.Snapshot(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Requests != 1 || snap.Tokens != 16 || snap.OutputTokens != 4 || snap.ReservedTokens != 0 || snap.CostMicros != nil {
		t.Fatal(snap)
	}
	if _, err = caller.StreamChat(ctx, in, req, nil); !errors.Is(err, turnbudget.ErrConflict) {
		t.Fatalf("attempt replay: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatal("same attempt dispatched twice")
	}
	var root, purpose string
	var seen, incomplete int
	if err = st.Trace.QueryRow(`SELECT root_id,purpose,usage_seen,incomplete FROM model_runs`).Scan(&root, &purpose, &seen, &incomplete); err != nil {
		t.Fatal(err)
	}
	if root != "r" || purpose != "foreground" || seen != 1 || incomplete != 0 {
		t.Fatalf("model accounting %s %s %d %d", root, purpose, seen, incomplete)
	}
}

type transportFunc func(context.Context, chat.Request, func(llm.Event)) (llm.Result, error)

func (f transportFunc) StreamChat(ctx context.Context, r chat.Request, e func(llm.Event)) (llm.Result, error) {
	return f(ctx, r, e)
}

func testChatRequest(model string, max int64) chat.Request {
	return chat.Request{Model: model, MaxOutputTokens: max, Style: chat.StyleBackground,
		Messages: []chat.Message{{Role: chat.RoleUser, Blocks: []chat.Block{chat.Text("x")}}}}
}
func TestModelCapacityAndOverdrawCancellation(t *testing.T) {
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ledger := &turnbudget.Ledger{Store: st}
	in := kernel.Invocation{RootID: "r", RunID: "run", Kind: "foreground", StepID: "s"}
	limits := kernel.Limits{MaxRequests: 4, MaxTools: 2, MaxTokens: 1000, MaxOutputTokens: 100, Deadline: time.Now().Add(time.Minute)}
	if err := ledger.OpenRoot(ctx, "r", in.Scope, limits); err != nil {
		t.Fatal(err)
	}
	entered := make(chan string, 2)
	release := make(chan struct{})
	var active, peak atomic.Int32
	tr := transportFunc(func(ctx context.Context, r chat.Request, _ func(llm.Event)) (llm.Result, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- r.Model
		if r.Model == "overdraw" {
			<-release
			return llm.Result{UsageSeen: true, Usage: llm.Usage{Input: 1100}}, nil
		}
		<-ctx.Done()
		return llm.Result{}, ctx.Err()
	})
	caller := New(tr, ledger, st, 2, 10)
	done := make(chan error, 2)
	for _, name := range []string{"waiting", "overdraw"} {
		x := in
		x.AttemptID = name
		go func(x kernel.Invocation) {
			_, e := caller.StreamChat(ctx, x, testChatRequest(x.AttemptID, 0), nil)
			done <- e
		}(x)
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("model capacity barrier")
		}
	}
	close(release)
	var exhausted bool
	for range 2 {
		select {
		case err := <-done:
			exhausted = exhausted || errors.Is(err, turnbudget.ErrExhausted)
		case <-time.After(5 * time.Second):
			t.Fatal("overdraw failed to cancel sibling")
		}
	}
	if !exhausted || peak.Load() != 2 || len(caller.Slots) != 0 {
		t.Fatalf("root exhausted=%v peak=%d slots=%d", exhausted, peak.Load(), len(caller.Slots))
	}
	// 取消 transport 的未知用量不能写成已知的零思考；已报告用量的零仍须保留。
	for _, attempt := range []string{"waiting", "overdraw"} {
		var reasoning sql.NullInt64
		if err := st.Trace.QueryRow(`SELECT reasoning_tokens FROM model_runs WHERE attempt_id=?`, attempt).Scan(&reasoning); err != nil {
			t.Fatal(err)
		}
		if reasoning.Valid != (attempt == "overdraw") || reasoning.Int64 != 0 {
			t.Fatalf("%s 的思考用量已知状态错误：%+v", attempt, reasoning)
		}
	}
}

// 封入口可能发生在预留之后；准入拒绝仍须退回预留，不能用失败返回的 nil 上下文收尾。
func TestSealedLifetimeReleasesModelReservation(t *testing.T) {
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ledger := &turnbudget.Ledger{Store: st}
	in := kernel.Invocation{RootID: "sealed-root", RunID: "sealed-run", StepID: "sealed-step", AttemptID: "sealed-attempt", Kind: kernel.KindForeground}
	limits := kernel.Limits{MaxRequests: 2, MaxTools: 1, MaxTokens: 1000, MaxOutputTokens: 100, Deadline: time.Now().Add(time.Minute)}
	if err := ledger.OpenRoot(context.Background(), in.RootID, in.Scope, limits); err != nil {
		t.Fatal(err)
	}
	lifetime := runstate.New(nil)
	lifetime.Seal()
	var dispatched bool
	caller := New(transportFunc(func(context.Context, chat.Request, func(llm.Event)) (llm.Result, error) {
		dispatched = true
		return llm.Result{}, nil
	}), ledger, st, 1, 10)
	_, err = caller.StreamChat(WithLifetime(context.Background(), lifetime), in, testChatRequest("fixture", 0), nil)
	if !errors.Is(err, runstate.ErrSealed) || dispatched {
		t.Fatalf("封入口后不得派发模型：err=%v dispatched=%v", err, dispatched)
	}
	snap, err := ledger.Snapshot(context.Background(), in.RootID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Requests != 0 || snap.ReservedTokens != 0 || snap.ReservedOutputTokens != 0 || snap.Tokens != 0 || len(caller.Slots) != 0 {
		t.Fatalf("未派发预留未退回：%+v", snap)
	}
	if err := lifetime.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// 失败的模型请求要把原因类别写进 model_runs.error_class（规格-运行页 第七节）：用真实的错误类型走真实的 Stream 和库。
// 旧代码不写 error_class 时这里会红。
func TestStreamWritesErrorClass(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"网关 504", &llm.StatusError{Code: 504, Body: "held"}, llm.ClassTimeout},
		{"流里出错", &llm.StreamError{Message: "boom", Data: []byte(`{"error":{"type":"api_error"}}`)}, llm.ClassStreamError},
		{"限流", &llm.StatusError{Code: 429, Body: "slow"}, llm.ClassRateLimited},
		{"上下文到时限", context.DeadlineExceeded, llm.ClassTimeout},
	}
	for i, c := range cases {
		st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		ledger := &turnbudget.Ledger{Store: st}
		in := kernel.Invocation{RootID: "r", RunID: "run", StepID: "s", AttemptID: "a", Kind: "foreground"}
		limits := kernel.Limits{MaxRequests: 3, MaxTools: 2, MaxTokens: 10000, MaxOutputTokens: 100, Deadline: time.Now().Add(time.Minute)}
		if err := ledger.OpenRoot(ctx, "r", in.Scope, limits); err != nil {
			t.Fatal(err)
		}
		fail := c.err
		tr := transportFunc(func(context.Context, chat.Request, func(llm.Event)) (llm.Result, error) { return llm.Result{}, fail })
		caller := New(tr, ledger, st, 1, 10)
		_, _ = caller.StreamChat(ctx, in, testChatRequest("m", 0), nil)
		var class sql.NullString
		var msg sql.NullString
		if err := st.Trace.QueryRow(`SELECT error_class, error FROM model_runs WHERE attempt_id = 'a'`).Scan(&class, &msg); err != nil {
			t.Fatalf("%d %s：%v", i, c.name, err)
		}
		if !msg.Valid || class.String != c.want {
			t.Errorf("%s：error_class 是 %q，应是 %q", c.name, class.String, c.want)
		}
		st.Close()
	}
}

// 成功的请求 error_class 必须是 NULL；预算用完（请求成功但结算超支）归 budget_exhausted
func TestStreamErrorClassNullOnSuccessAndBudget(t *testing.T) {
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ledger := &turnbudget.Ledger{Store: st}
	limits := kernel.Limits{MaxRequests: 5, MaxTools: 2, MaxTokens: 1000, MaxOutputTokens: 100, Deadline: time.Now().Add(time.Minute)}
	in := kernel.Invocation{RootID: "r", RunID: "run", StepID: "s", Kind: "foreground"}
	if err := ledger.OpenRoot(ctx, "r", in.Scope, limits); err != nil {
		t.Fatal(err)
	}
	tr := transportFunc(func(_ context.Context, r chat.Request, _ func(llm.Event)) (llm.Result, error) {
		if r.Model == "overdraw" {
			return llm.Result{UsageSeen: true, Usage: llm.Usage{Input: 1100}}, nil
		}
		return llm.Result{Text: "ok", UsageSeen: true, Usage: llm.Usage{Input: 5, Output: 1}}, nil
	})
	caller := New(tr, ledger, st, 1, 10)
	good := in
	good.AttemptID = "good"
	if _, err := caller.StreamChat(ctx, good, testChatRequest("m", 0), nil); err != nil {
		t.Fatal(err)
	}
	over := in
	over.AttemptID = "over"
	_, _ = caller.StreamChat(ctx, over, testChatRequest("overdraw", 0), nil)
	get := func(id string) (c, e sql.NullString) {
		if err := st.Trace.QueryRow(`SELECT error_class, error FROM model_runs WHERE attempt_id = ?`, id).Scan(&c, &e); err != nil {
			t.Fatal(err)
		}
		return
	}
	if c, e := get("good"); c.Valid || e.Valid {
		t.Errorf("成功的请求带着失败标记：%+v %+v", c, e)
	}
	if c, e := get("over"); !e.Valid || c.String != llm.ClassBudgetExhausted {
		t.Errorf("超支的请求 error_class 是 %q，应是 budget_exhausted", c.String)
	}
}

// 运行页的"模型请求槽位"行读 Load：一个在跑、一个在等槽时，在跑 1、排队 1、容量 1，拿到槽或放弃后排队清零。
func TestLoadReportsRunningAndWaiting(t *testing.T) {
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ledger := &turnbudget.Ledger{Store: st}
	in := kernel.Invocation{RootID: "r", RunID: "run", Kind: "foreground", StepID: "s"}
	limits := kernel.Limits{MaxRequests: 4, MaxTools: 2, MaxTokens: 100000, MaxOutputTokens: 1000, Deadline: time.Now().Add(time.Minute)}
	if err := ledger.OpenRoot(ctx, "r", in.Scope, limits); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	tr := transportFunc(func(ctx context.Context, r chat.Request, _ func(llm.Event)) (llm.Result, error) {
		entered <- struct{}{}
		<-release
		return llm.Result{UsageSeen: true, Usage: llm.Usage{Input: 1}}, nil
	})
	caller := New(tr, ledger, st, 1, 10)
	if run, capacity, waiting, oldest := caller.Load(); run != 0 || capacity != 1 || waiting != 0 || oldest != 0 {
		t.Fatalf("空闲时的读数不对：%d %d %d %v", run, capacity, waiting, oldest)
	}
	done := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		x := in
		x.AttemptID = name
		go func(x kernel.Invocation) {
			_, e := caller.StreamChat(ctx, x, testChatRequest("m", 0), nil)
			done <- e
		}(x)
	}
	<-entered // 第一个拿到槽、进了 transport；第二个只能在等槽
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, capacity, waiting, oldest := caller.Load()
		if run == 1 && capacity == 1 && waiting == 1 && oldest > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("没等到排队读数：%d %d %d %v", run, capacity, waiting, oldest)
		}
		runtime.Gosched()
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if run, _, waiting, oldest := caller.Load(); run != 0 || waiting != 0 || oldest != 0 {
		t.Fatalf("做完以后应该清零：%d %d %v", run, waiting, oldest)
	}
}

// 排队的请求被取消（等槽期间 ctx 到期）也要从排队里删掉，不然"最久等待"会一直涨。
func TestLoadWaitingRemovedOnCancel(t *testing.T) {
	c := New(nil, nil, nil, 1, 10)
	c.Slots <- struct{}{}
	leave := c.enterWait()
	if _, _, w, _ := c.Load(); w != 1 {
		t.Fatalf("排队数：%d", w)
	}
	leave()
	leave() // 重复调不出错
	if _, _, w, o := c.Load(); w != 0 || o != 0 {
		t.Fatalf("离开后该清零：%d %v", w, o)
	}
}

// 单次输出上限跟着根走：根里写了 RequestOutputTokens 就用它（本地干活要写大文件），没写用调用端自己的默认；
// 两种情况都不能超过根剩下的输出额度
func TestRequestOutputCapFollowsRootLimits(t *testing.T) {
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ledger := &turnbudget.Ledger{Store: st}
	var got []int64
	tr := transportFunc(func(_ context.Context, r chat.Request, _ func(llm.Event)) (llm.Result, error) {
		got = append(got, r.MaxOutputTokens)
		return llm.Result{UsageSeen: true, Usage: llm.Usage{Input: 5, Output: 1}}, nil
	})
	caller := New(tr, ledger, st, 1, 10)
	cases := []struct {
		root      string
		perRoot   int64
		maxOutput int64
		want      int64
	}{
		{"own", 77, 1000, 77},
		{"fallback", 0, 1000, 10},
		{"remaining", 500, 40, 40},
	}
	for _, c := range cases {
		in := kernel.Invocation{RootID: c.root, RunID: c.root + "-run", Kind: "foreground", StepID: "s", AttemptID: c.root + "-a"}
		limits := kernel.Limits{MaxRequests: 4, MaxTools: 2, MaxTokens: 100000, MaxOutputTokens: c.maxOutput, Deadline: time.Now().Add(time.Minute), RequestOutputTokens: c.perRoot}
		if err := ledger.OpenRoot(ctx, c.root, in.Scope, limits); err != nil {
			t.Fatal(err)
		}
		if _, err := caller.StreamChat(ctx, in, testChatRequest("m", 0), nil); err != nil {
			t.Fatalf("%s：%v", c.root, err)
		}
		if g := got[len(got)-1]; g != c.want {
			t.Errorf("%s：max_completion_tokens = %d，想要 %d", c.root, g, c.want)
		}
	}
}
