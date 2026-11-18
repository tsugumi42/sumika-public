package workcontext

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/artifact"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func TestWorkCandidateCAS(t *testing.T) {
	for _, change := range []string{"tail", "model", "rules", "tools", "workspace", "authority", "prefix", "revision"} {
		t.Run(change, func(t *testing.T) {
			p, err := paths.Init(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			db, err := store.OpenFresh(p)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			limits := config.DefaultWork()
			limits.KeepRecentTokens = 1
			s := New(db, artifact.New(db, p.WorkResultsDir(), limits), limits)
			inv := kernel.Invocation{RootID: "made-root", RunID: "made-run", Kind: "foreground", Scope: kernel.Scope{CharacterID: "made-a", Conversation: "console:made-a", ScopeID: "made-scope", Platform: "local", Channel: "console", AccountID: "desktop", Scene: "private", ActorKind: "owner", ActorID: "made-owner", AuthorityID: "made-grant", AuthorityRevision: 1}}
			ctx := context.Background()
			if err := s.Open(ctx, inv, "initial-model/rules/tools/workspace"); err != nil {
				t.Fatal(err)
			}
			for i := range 4 {
				if err := s.Record(ctx, inv, Entry{Delta: Delta{Goal: "原始目标", Completed: []string{fmt.Sprint("证据-", i)}}, Tokens: 100, Boundary: true}); err != nil {
					t.Fatal(err)
				}
			}
			c, err := s.Candidate(ctx, inv)
			if err != nil {
				t.Fatal(err)
			}
			if c.State.ThroughOrdinal != 3 || c.State.Goal != "原始目标" {
				t.Fatalf("未保留完整最新组：%+v", c)
			}
			// 摘要正在生成时到达的新尾部，不论候选是否采用都必须留下。
			if err := s.Record(ctx, inv, Entry{Delta: Delta{Pending: []string{"new-tail"}}, Tokens: 100, Boundary: true}); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "model", "rules", "tools", "workspace":
				if err := db.SetWorkStamp(ctx, inv, change); err != nil {
					t.Fatal(err)
				}
			case "authority":
				inv.Scope.AuthorityRevision++
			case "prefix":
				if _, err := db.Trace.Exec(`UPDATE work_entries SET digest='changed' WHERE root_id=? AND ordinal=1`, inv.RootID); err != nil {
					t.Fatal(err)
				}
			case "revision":
				if err := s.Install(ctx, inv, c); err != nil {
					t.Fatal(err)
				}
			}
			err = s.Install(ctx, inv, c)
			if change == "tail" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, store.ErrWorkConflict) && !(change == "authority" && strings.Contains(fmt.Sprint(err), "归属")) {
				t.Fatalf("旧候选未拒绝：%v", err)
			}
			inv.Scope.AuthorityRevision = 1
			state, tail, err := s.Load(ctx, inv)
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Pending) != 1 || state.Pending[0] != "new-tail" || len(tail) == 0 {
				t.Fatalf("新尾部丢失：%+v %+v", state, tail)
			}
		})
	}
	t.Run("unfinished-group", func(t *testing.T) {
		p, err := paths.Init(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		db, err := store.OpenFresh(p)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		limits := config.DefaultWork()
		limits.KeepRecentTokens = 1
		s := New(db, artifact.New(db, p.WorkResultsDir(), limits), limits)
		inv := kernel.Invocation{RootID: "made-root", RunID: "made-run", Scope: kernel.Scope{CharacterID: "made-a", Conversation: "console:made-a", ScopeID: "made-scope", Platform: "local", Channel: "console", AccountID: "made-desktop", Scene: "private", ActorKind: "owner", ActorID: "made-owner", AuthorityID: "made-grant", AuthorityRevision: 1}}
		ctx := context.Background()
		if err := s.Open(ctx, inv, "made-stamp"); err != nil {
			t.Fatal(err)
		}
		for _, e := range []Entry{{Tokens: 100, Boundary: true}, {Tokens: 100, Group: "made-step", Calls: 2}, {Tokens: 100, Group: "made-step", Result: true, Boundary: true}, {Tokens: 100, Boundary: true}} {
			if err := s.Record(ctx, inv, e); err != nil {
				t.Fatal(err)
			}
		}
		c, err := s.Candidate(ctx, inv)
		if err != nil {
			t.Fatal(err)
		}
		if c.State.ThroughOrdinal != 1 {
			t.Fatalf("缺一个结果时拆掉了调用组：%d", c.State.ThroughOrdinal)
		}
		for _, e := range []Entry{{Tokens: 100, Group: "made-step", Result: true, Boundary: true}, {Tokens: 100, Boundary: true}} {
			if err := s.Record(ctx, inv, e); err != nil {
				t.Fatal(err)
			}
		}
		c, err = s.Candidate(ctx, inv)
		if err != nil {
			t.Fatal(err)
		}
		if c.State.ThroughOrdinal != 5 {
			t.Fatalf("完整组未成为切点：%d", c.State.ThroughOrdinal)
		}
	})
}
