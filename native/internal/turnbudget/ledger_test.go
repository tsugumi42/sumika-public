package turnbudget

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func fixture(t *testing.T) (*Ledger, *store.Store, kernel.Invocation, kernel.Limits) {
	t.Helper()
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	in := kernel.Invocation{RootID: "root", RunID: "run", Kind: "foreground", Scope: kernel.Scope{CharacterID: "fiction", AuthorityID: "host"}}
	limits := kernel.Limits{MaxRequests: 1, MaxTools: 2, MaxTokens: 100, MaxOutputTokens: 20, Deadline: time.Now().Add(time.Hour)}
	ledger := &Ledger{Store: st}
	if err := ledger.OpenRoot(context.Background(), in.RootID, in.Scope, limits); err != nil {
		t.Fatal(err)
	}
	return ledger, st, in, limits
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRootBudgetAtomicReservation(t *testing.T) {
	l, st, in, limits := fixture(t)
	ctx := context.Background()
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, kind := range []string{"foreground", "child", "advisor"} {
		wg.Add(1)
		go func(i int, kind string) {
			defer wg.Done()
			<-start
			x := in
			x.Kind = kind
			x.RunID = kind
			err := l.Reserve(ctx, kernel.Reservation{ID: fmt.Sprint(i), Invocation: x, Requests: 1, Tokens: 40, OutputTokens: 10})
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrExhausted) {
				t.Errorf("reserve: %v", err)
			}
		}(i, kind)
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("last request admitted %d competitors", wins.Load())
	}
	// 使用第二个 ledger 实例模拟重启；未结算预留不会从存储中消失。
	restarted := &Ledger{Store: st}
	snap, err := restarted.Snapshot(ctx, in.RootID)
	must(t, err)
	if snap.Requests != 1 || snap.ReservedTokens != 40 || snap.ReservedOutputTokens != 10 {
		t.Fatalf("restart refunded unknown: %+v", snap)
	}
	must(t, restarted.OpenRoot(ctx, in.RootID, in.Scope, limits))
	limits.MaxRequests = 2
	if !errors.Is(restarted.OpenRoot(ctx, in.RootID, in.Scope, limits), ErrConflict) {
		t.Fatal("root reopened with larger allowance")
	}
	// 准入尝试包含被拒绝的工具，重复同号不能多扣；同号改参数不能借用。
	r := kernel.Reservation{ID: "tool", Invocation: in, Tools: 1}
	must(t, l.Reserve(ctx, r))
	must(t, l.Reserve(ctx, r))
	r.Tools = 2
	if !errors.Is(l.Reserve(ctx, r), ErrConflict) {
		t.Fatal("different reservation accepted")
	}
	p := paths.Paths{Root: t.TempDir()}
	disk, err := store.OpenFresh(p)
	must(t, err)
	durable := &Ledger{Store: disk}
	must(t, durable.OpenRoot(ctx, in.RootID, in.Scope, limits))
	unknown := kernel.Reservation{ID: "unknown", Invocation: in, Requests: 1, Tokens: 40, OutputTokens: 10}
	must(t, durable.Reserve(ctx, unknown))
	must(t, durable.Dispatch(ctx, unknown.ID))
	disk.Close()
	disk, err = store.OpenFresh(p)
	must(t, err)
	defer disk.Close()
	durable = &Ledger{Store: disk}
	recovered, err := durable.Snapshot(ctx, in.RootID)
	must(t, err)
	if recovered.Requests != 1 || recovered.ReservedTokens != 40 || !recovered.Incomplete {
		t.Fatalf("database reopen refunded unknown: %+v", recovered)
	}
}

func TestRootBudgetChildBinding(t *testing.T) {
	l, st, parent, _ := fixture(t)
	ctx := context.Background()
	parent.Generation = 1
	child := parent
	child.Kind, child.ParentRunID, child.RunID = kernel.KindChild, parent.RunID, "child-run"
	child.Scope.Conversation, child.Scope.AuthorityID, child.Scope.AuthorityRevision = "internal:child-run", "delegated", 1
	reservation := kernel.Reservation{ID: "child-request", Invocation: child, Requests: 1, Tokens: 40, OutputTokens: 10}
	if err := l.Reserve(ctx, reservation); !errors.Is(err, ErrConflict) {
		t.Fatalf("未绑定子授权取得根余额：%v", err)
	}
	for _, change := range []func(*kernel.Invocation){
		func(in *kernel.Invocation) { in.RootID = "other-root" },
		func(in *kernel.Invocation) { in.ParentRunID = "other-parent" },
		func(in *kernel.Invocation) { in.Generation++ },
		func(in *kernel.Invocation) { in.Scope.ActorID = "forged-owner" },
		func(in *kernel.Invocation) { in.Scope.AccountID = "other-account" },
		func(in *kernel.Invocation) { in.Scope.Scene = "group" },
		func(in *kernel.Invocation) { in.Scope.Platform = "qq" },
		func(in *kernel.Invocation) { in.Scope.AuthorityID = "" },
		func(in *kernel.Invocation) { in.Scope.AuthorityRevision = 0 },
	} {
		bad := child
		change(&bad)
		if err := l.BindChild(ctx, parent, bad); !errors.Is(err, ErrConflict) {
			t.Fatalf("跨域或缺身份的委托被绑定：%+v %v", bad, err)
		}
	}
	must(t, l.BindChild(ctx, parent, child))
	must(t, l.BindChild(ctx, parent, child))
	// 重开仍只承认这一条精确委托，不依赖进程内白名单。
	l = &Ledger{Store: st}
	for _, change := range []func(*kernel.Invocation){
		func(in *kernel.Invocation) { in.RunID = "other-run" },
		func(in *kernel.Invocation) { in.ParentRunID = "other-parent" },
		func(in *kernel.Invocation) { in.Generation++ },
		func(in *kernel.Invocation) { in.Kind = kernel.KindForeground },
		func(in *kernel.Invocation) { in.Scope.ActorID = "forged-owner" },
		func(in *kernel.Invocation) { in.Scope.AccountID = "other-account" },
		func(in *kernel.Invocation) { in.Scope.Scene = "group" },
		func(in *kernel.Invocation) { in.Scope.AuthorityRevision++ },
	} {
		bad := reservation
		change(&bad.Invocation)
		if err := l.Reserve(ctx, bad); !errors.Is(err, ErrConflict) {
			t.Fatalf("已绑定子运行被冒用：%+v %v", bad.Invocation, err)
		}
	}
	must(t, l.Reserve(ctx, reservation))
	if err := l.Reserve(ctx, kernel.Reservation{ID: "parent-request", Invocation: parent, Requests: 1, Tokens: 40, OutputTokens: 10}); !errors.Is(err, ErrExhausted) {
		t.Fatalf("父子没有共用最后一次请求：%v", err)
	}
	snapshot, err := l.Snapshot(ctx, parent.RootID)
	must(t, err)
	if snapshot.Requests != 1 || snapshot.ReservedTokens != 40 {
		t.Fatalf("委托重复扣费或另建额度：%+v", snapshot)
	}
}

func TestUsageIncompleteAndOverdrawn(t *testing.T) {
	for _, tc := range []struct {
		name             string
		charge           kernel.Charge
		tokens, reserved int64
		incomplete       bool
	}{
		{"known_zero", kernel.Charge{Dispatched: true, UsageSeen: true}, 0, 0, true},
		{"missing_usage", kernel.Charge{Dispatched: true}, 0, 40, true},
		{"partial_failure", kernel.Charge{Dispatched: true, UsageSeen: true, Input: 3, CacheRead: 5, CacheWrite: 7, Output: 11, Reasoning: 9}, 26, 0, true},
		{"overdrawn", kernel.Charge{Dispatched: true, UsageSeen: true, Input: 101, Output: 4}, 105, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, _, in, _ := fixture(t)
			ctx := context.Background()
			r := kernel.Reservation{ID: "a", Invocation: in, Requests: 1, Tokens: 40, OutputTokens: 10}
			must(t, l.Reserve(ctx, r))
			must(t, l.Dispatch(ctx, r.ID))
			must(t, l.Settle(ctx, r.ID, tc.charge))
			must(t, l.Settle(ctx, r.ID, tc.charge))
			snap, err := l.Snapshot(ctx, in.RootID)
			must(t, err)
			if snap.Tokens != tc.tokens || snap.ReservedTokens != tc.reserved || snap.Incomplete != tc.incomplete || snap.CostMicros != nil {
				t.Fatalf("usage accounting: %+v", snap)
			}
			if tc.name == "overdrawn" {
				if !errors.Is(l.Reserve(ctx, kernel.Reservation{ID: "t", Invocation: in, Tools: 1}), ErrExhausted) {
					t.Fatal("overdraw allowed tool")
				}
			}
			if !errors.Is(l.Settle(ctx, r.ID, kernel.Charge{}), ErrConflict) {
				t.Fatal("dispatched charge refunded")
			}
		})
	}
	t.Run("unknown_price", func(t *testing.T) {
		l, _, in, limits := fixture(t)
		in.RootID = "priced"
		cap := int64(100)
		limits.MaxCostMicros = &cap
		must(t, l.OpenRoot(context.Background(), in.RootID, in.Scope, limits))
		if !errors.Is(l.Reserve(context.Background(), kernel.Reservation{ID: "paid", Invocation: in, Requests: 1, Tokens: 10}), ErrExhausted) {
			t.Fatal("unknown price treated as free")
		}
	})
}

func TestBudgetExhaustionBoundaries(t *testing.T) {
	for _, dimension := range []string{"requests", "tools", "tokens", "output", "deadline", "stopped", "cost_zero"} {
		t.Run(dimension, func(t *testing.T) {
			l, _, in, limits := fixture(t)
			ctx := context.Background()
			in.RootID = dimension
			r := kernel.Reservation{ID: dimension, Invocation: in, Requests: 1, Tokens: 20, OutputTokens: 10}
			switch dimension {
			case "requests":
				r.Requests = 2
			case "tools":
				r.Requests = 0
				r.Tools = 3
			case "tokens":
				r.Tokens = limits.MaxTokens + 1
			case "output":
				r.OutputTokens = 21
				r.Tokens = 30
			case "deadline":
				limits.Deadline = time.Now().Add(-time.Second)
			case "cost_zero":
				zero := int64(0)
				limits.MaxCostMicros = &zero
				r.CostMicros = &zero
			}
			must(t, l.OpenRoot(ctx, in.RootID, in.Scope, limits))
			if dimension == "stopped" {
				must(t, l.Stop(ctx, in.RootID))
			}
			if !errors.Is(l.Reserve(ctx, r), ErrExhausted) {
				t.Fatal("boundary admitted")
			}
			snap, err := l.Snapshot(ctx, in.RootID)
			must(t, err)
			if snap.Requests != 0 || snap.Tools != 0 || snap.ReservedTokens != 0 {
				t.Fatalf("rejected reservation changed account %+v", snap)
			}
		})
	}
	t.Run("dispatch_expired", func(t *testing.T) {
		l, _, in, limits := fixture(t)
		now := time.Now()
		l.Now = func() time.Time { return now }
		ctx := context.Background()
		r := kernel.Reservation{ID: "late", Invocation: in, Requests: 1, Tokens: 20, OutputTokens: 10}
		must(t, l.Reserve(ctx, r))
		now = limits.Deadline
		if !errors.Is(l.Dispatch(ctx, r.ID), ErrExhausted) {
			t.Fatal("expired reservation dispatched")
		}
		must(t, l.Settle(ctx, r.ID, kernel.Charge{}))
		snap, err := l.Snapshot(ctx, in.RootID)
		must(t, err)
		if snap.Requests != 0 || snap.ReservedTokens != 0 {
			t.Fatal(snap)
		}
	})
}
