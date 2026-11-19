package app

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/modelcall"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
	"sumika-native/internal/turnbudget"
)

func TestAuxiliaryCallsShareLedger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Reply: llmtest.Text("fixture confirmation")}, &bodies, &mu))
	defer srv.Close()
	ledger := &turnbudget.Ledger{Store: st}
	caller := modelcall.New(llm.New(llm.Magpie(srv.URL+"/v1")), ledger, st, 2, 256)
	g := memoryGenerator{runtime: &ModelRuntime{Models: []llm.Model{{ID: "fiction-model", MaxOutputTokens: 64}}}, caller: caller, budget: ledger, character: "fiction"}
	owner := kernel.Scope{CharacterID: "fiction", Conversation: "qq:private:fixture", AuthorityID: "fixture-host"}
	root := kernel.Invocation{RootID: "foreground-root", RunID: "foreground-run", Kind: "guard", Scope: owner}
	if err := ledger.OpenRoot(ctx, root.RootID, owner, config.DefaultBudget().Limits(time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Call(kernel.WithInvocation(ctx, root), "fiction-model", "fixture guard", "guard:fiction"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodies[0], `"max_completion_tokens":64`) {
		t.Fatalf("auxiliary request exceeded model ceiling: %s", bodies[0])
	}
	if _, err := g.Call(ctx, "fiction-model", "fixture background", "memory:fiction"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Call(ctx, "fiction-model", "missing owner", "guard:fiction"); err == nil {
		t.Fatal("unowned guard silently created memory root")
	}
	snap, err := ledger.Snapshot(ctx, root.RootID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Requests != 1 || snap.Tokens != 110 {
		t.Fatalf("guard charged wrong root: %+v", snap)
	}
	var independent int
	if err := st.Trace.QueryRow(`SELECT count(*) FROM model_runs WHERE purpose='memory' AND root_id!=?`, root.RootID).Scan(&independent); err != nil || independent != 1 {
		t.Fatalf("independent memory rows=%d %v", independent, err)
	}
	if len(bodies) != 2 {
		t.Fatalf("transport count %d", len(bodies))
	}
	// 同角色另一会话不能被背景任务或守卫偷偷扣账。
	other := root
	other.RootID = "other-root"
	other.Scope.Conversation = "qq:private:other"
	if err := ledger.OpenRoot(ctx, other.RootID, other.Scope, config.DefaultBudget().Limits(time.Now())); err != nil {
		t.Fatal(err)
	}
	untouched, err := ledger.Snapshot(ctx, other.RootID)
	if err != nil || untouched.Requests != 0 {
		t.Fatalf("neighbor root %+v %v", untouched, err)
	}
	if _, err := caller.StreamChat(ctx, kernel.Invocation{}, chat.Request{}, nil); err == nil || errors.Is(err, turnbudget.ErrExhausted) {
		t.Fatalf("missing identity should reject at boundary: %v", err)
	}
	t.Log("transport=2 guard_root_requests=1 memory_roots=1 neighbor_requests=0 unowned_guard_dispatch=0")
}

// 直接调用守卫的夹具充当受理宿主，显式提供所属根，不依赖最近聊天。
func kernelGuardContext(t *testing.T, a *App) context.Context {
	t.Helper()
	id := "guard-fixture:" + store.NewID(time.Now())
	in := kernel.Invocation{RootID: id, RunID: id, Kind: kernel.KindGuard, Scope: kernel.Scope{CharacterID: testChar, Conversation: "qq:group:fixture"}}
	if err := turn.AdmitBudgetRoot(context.Background(), a.hub.Cfg.Budget, in, config.DefaultBudget().Limits(time.Now())); err != nil {
		t.Fatal(err)
	}
	return kernel.WithInvocation(context.Background(), in)
}

// 后台请求（记忆整理等）的单次输出不能把请求顶出窗口：窗口 10 万、输入约 9 万令牌时，发送值要压到余量以内。
func TestMemoryGeneratorClampsOutputToWindow(t *testing.T) {
	t.Parallel()
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Reply: llmtest.Text("fixture")}, &bodies, &mu))
	defer srv.Close()
	ledger := &turnbudget.Ledger{Store: st}
	caller := modelcall.New(llm.New(llm.Magpie(srv.URL+"/v1")), ledger, st, 2, 0)
	g := memoryGenerator{runtime: &ModelRuntime{Models: []llm.Model{{ID: "fiction-model", MaxOutputTokens: 64000, ContextLength: 100000}}}, caller: caller, budget: ledger, character: "fiction"}
	if _, err := g.Call(context.Background(), "fiction-model", strings.Repeat("a", 180000), "memory:fiction"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodies[0], `"max_completion_tokens":5904`) {
		t.Fatalf("输出没按窗口余量压：%.300s", bodies[len(bodies)-1][:min(300, len(bodies[0]))])
	}
}
