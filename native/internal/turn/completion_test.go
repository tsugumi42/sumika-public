package turn_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/turn"
	"sumika-native/internal/turn/turntest"
)

type reviewWork struct {
	mu    sync.Mutex
	state kernel.WorkState
	loads []kernel.Invocation
}

func (w *reviewWork) Load(_ context.Context, i kernel.Invocation) (kernel.WorkState, []turn.Msg, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loads = append(w.loads, i)
	return w.state, nil, nil
}
func (w *reviewWork) Record(context.Context, kernel.Invocation, turn.Msg) error { return nil }
func (w *reviewWork) Save(_ context.Context, _ kernel.Invocation, _ uint64, state kernel.WorkState) error {
	w.mu.Lock()
	w.state = state
	w.mu.Unlock()
	return nil
}

type completionFn func(context.Context, kernel.ReviewRequest) (kernel.Verdict, error)

func (f completionFn) Check(ctx context.Context, r kernel.ReviewRequest) (kernel.Verdict, error) {
	return f(ctx, r)
}

type observation struct {
	mu       sync.Mutex
	requests []kernel.ReviewRequest
}

func (o *observation) Observe(_ context.Context, r kernel.ReviewRequest) error {
	o.mu.Lock()
	o.requests = append(o.requests, r)
	o.mu.Unlock()
	return nil
}
func (o *observation) Take(context.Context, kernel.Invocation) ([]kernel.Finding, error) {
	return nil, nil
}
func workRequest() kernel.ReviewRequest {
	owner := kernel.Scope{CharacterID: turntest.CharacterID, Conversation: "console:" + turntest.CharacterID, ScopeID: "work", Platform: "local", Channel: "console", AccountID: "desktop", Scene: "private", ActorKind: "owner", ActorID: "owner", AuthorityID: "explicit-test", AuthorityRevision: 1}
	hash := sha256.Sum256([]byte("result=2"))
	ref := kernel.ResultRef{ID: "e1", SHA256: hex.EncodeToString(hash[:]), Owner: owner, Bytes: 8, Complete: true}
	e := kernel.Evidence{Ref: ref, Start: 0, End: 8, Claim: "数量=2"}
	return kernel.ReviewRequest{Invocation: kernel.Invocation{Scope: owner, RootID: "work-root", RunID: "initial", Generation: 1, Kind: "foreground"}, CheckpointID: "checkpoint", Trigger: "candidate", Criteria: []string{"数量=2"}, Evidence: []kernel.Evidence{e}, Work: kernel.WorkState{RootID: "work-root", Owner: owner, Revision: 1, Goal: "校验数量", Evidence: []kernel.Evidence{e}}}
}
func workInput(r kernel.ReviewRequest) turn.Input {
	return turn.Input{CharacterID: r.Invocation.Scope.CharacterID, Conversation: r.Invocation.Scope.Conversation, Scope: r.Invocation.Scope.ScopeID, Audience: audience.Console(r.Invocation.Scope.Conversation), Text: "校验数量"}
}

func TestBoundedCompletionContinuation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		changed bool
		second  string
		want    string
		checks  int
	}{{"new-evidence", true, "done", "done", 2}, {"no-progress", false, "done", "partial", 1}, {"still-incomplete", true, "continue", "partial", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := workRequest()
			w := &reviewWork{state: r.Work}
			observer := &observation{}
			var checked []kernel.ReviewRequest
			k := turntest.New(t, turntest.Options{Mutate: func(cfg *turn.Config) {
				cfg.Work = w
				cfg.Advisor = observer
				cfg.Completion = completionFn(func(ctx context.Context, req kernel.ReviewRequest) (kernel.Verdict, error) {
					checked = append(checked, req)
					status := "continue"
					if len(checked) > 1 {
						status = tc.second
					}
					if len(checked) == 1 && tc.changed {
						w.mu.Lock()
						w.state.Evidence = append([]kernel.Evidence(nil), w.state.Evidence...)
						w.state.Revision++
						w.state.Evidence[0].Ref.SHA256 = "new-evidence"
						w.mu.Unlock()
					}
					return kernel.Verdict{CheckpointID: req.CheckpointID, Status: status}, nil
				})
			}})
			out := k.Hub.RespondWork(context.Background(), workInput(r), r, reviewCurrent)
			if out.Verdict.Status != tc.want || len(checked) != tc.checks || len(out.Runs) != 2 {
				t.Fatalf("verdict=%+v checked=%d runs=%+v err=%v", out.Verdict, len(checked), out.Runs, out.Outcome.Err)
			}
			if out.Runs[0].RunID == out.Runs[1].RunID || out.Runs[1].ParentRunID != out.Runs[0].RunID {
				t.Fatalf("continuation identity broken: %+v", out.Runs)
			}
			for _, i := range out.Runs {
				if i.RootID != r.Invocation.RootID || i.Scope != r.Invocation.Scope || i.Generation != r.Invocation.Generation {
					t.Fatalf("authority/root changed %+v", i)
				}
			}
			if len(observer.requests) != 2 {
				t.Fatalf("advisor self-loop: %d", len(observer.requests))
			}
			if tc.want == "done" {
				if out.Outcome.Err != nil || len(out.Outcome.Result.Said) != 1 {
					t.Fatal(out.Outcome)
				}
			} else {
				if !errors.Is(out.Outcome.Err, turn.ErrUnverified) || len(out.Outcome.Result.Said) != 0 {
					t.Fatalf("unverified candidate escaped: %+v", out.Outcome)
				}
			}
		})
	}
}

func TestCompletionGateKeepsCandidateFromChannel(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"partial", "blocked", "failure", "done"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			r := workRequest()
			r.Invocation.Scope.Platform, r.Invocation.Scope.Channel, r.Invocation.Scope.Scene, r.Invocation.Scope.Conversation = "qq", "qq", "group", "qq:group:300"
			r.Work.Owner = r.Invocation.Scope
			k := turntest.New(t, turntest.Options{Mutate: func(cfg *turn.Config) {
				cfg.Work = &reviewWork{state: r.Work}
				cfg.Completion = completionFn(func(context.Context, kernel.ReviewRequest) (kernel.Verdict, error) {
					if status == "failure" {
						return kernel.Verdict{Status: "partial"}, errors.New("review failed")
					}
					return kernel.Verdict{Status: status}, nil
				})
			}})
			in := workInput(r)
			in.Audience = audience.Audience{Channel: "qq", Conversation: r.Invocation.Scope.Conversation, Scene: "group"}
			out := k.Hub.RespondWork(context.Background(), in, r, reviewCurrent)
			if status == "done" {
				if out.Outcome.Err != nil || len(out.Outcome.Result.Said) != 1 {
					t.Fatal(out)
				}
			} else {
				if out.Outcome.Err == nil || len(out.Outcome.Result.Said) != 0 {
					t.Fatalf("unverified channel text: %+v", out)
				}
			}
		})
	}
}

func TestStoppedWorkDoesNotContinue(t *testing.T) {
	t.Parallel()
	for _, cause := range []string{"cancel", "budget", "revoked"} {
		t.Run(cause, func(t *testing.T) {
			r := workRequest()
			w := &reviewWork{state: r.Work}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			allowed := true
			calls := 0
			current := func(ctx context.Context, _ kernel.ReviewRequest) error {
				if !allowed {
					return errors.New(cause)
				}
				return ctx.Err()
			}
			k := turntest.New(t, turntest.Options{Mutate: func(cfg *turn.Config) {
				cfg.Work = w
				cfg.Completion = completionFn(func(context.Context, kernel.ReviewRequest) (kernel.Verdict, error) {
					calls++
					if cause == "cancel" {
						cancel()
					} else {
						allowed = false
					}
					return kernel.Verdict{Status: "continue"}, nil
				})
			}})
			out := k.Hub.RespondWork(ctx, workInput(r), r, current)
			if out.Outcome.Err == nil || len(out.Runs) != 1 || k.Requests() != 1 || calls != 1 || len(out.Outcome.Result.Said) != 0 {
				t.Fatalf("stopped work continued: %+v requests=%d checks=%d", out, k.Requests(), calls)
			}
		})
	}
}

func TestPlainChatDoesNotReview(t *testing.T) {
	t.Parallel()
	r := workRequest()
	calls := 0
	k := turntest.New(t, turntest.Options{Mutate: func(cfg *turn.Config) {
		cfg.Work = &reviewWork{state: r.Work}
		cfg.Advisor = &observation{}
		cfg.Completion = completionFn(func(context.Context, kernel.ReviewRequest) (kernel.Verdict, error) {
			calls++
			return kernel.Verdict{Status: "blocked"}, nil
		})
	}})
	result := k.Send("在吗")
	if calls != 0 || len(result.Said) != 1 || k.Requests() != 1 {
		t.Fatalf("plain chat reviewed: checks=%d result=%+v requests=%d", calls, result, k.Requests())
	}
}

func reviewCurrent(ctx context.Context, _ kernel.ReviewRequest) error { return ctx.Err() }
