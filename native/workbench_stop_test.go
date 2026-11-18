package main

import (
	"context"
	"runtime"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

type delayedStopInputs struct {
	*fakeInputs
	updates chan api.Snapshot[api.LocalRunSnapshot]
}

func (f *delayedStopInputs) WatchLocalRun(context.Context, api.ConversationKey, string) (api.Subscription[api.LocalRunSnapshot], error) {
	return settledRun{f.updates}, nil
}

func (f *delayedStopInputs) StopLocalRun(context.Context, api.ConversationKey, string, uint64) (api.StopReceipt, error) {
	return api.StopReceipt{RunID: "r1"}, nil
}

func TestWorkbenchStopShowsStoppedUntilToolDrains(t *testing.T) {
	f := &delayedStopInputs{fakeInputs: &fakeInputs{}, updates: make(chan api.Snapshot[api.LocalRunSnapshot], 4)}
	r := newWbRig(t, f.fakeInputs)
	r.a.reader = f
	st := r.a.localRun(r.key.Conversation)
	st.busy, st.runID, st.through = true, "r1", 1
	st.startRun()
	go r.a.watchComposerRun(r.a.viewCtx, f, r.key, r.a.srcGen, st, "r1")
	apply := func(v api.LocalRunSnapshot) {
		t.Helper()
		f.updates <- api.Snapshot[api.LocalRunSnapshot]{Result: api.Result[api.LocalRunSnapshot]{State: api.Ready, Value: v}}
		deadline := time.After(5 * time.Second)
		for r.q.pending() == 0 {
			select {
			case <-deadline:
				t.Fatal("progress was not delivered to the UI")
			default:
				runtime.Gosched()
			}
		}
		r.settle()
	}
	tool := api.LocalProcessStep{ID: "tool1", Kind: "tool", Tool: "read", State: "running", Started: 1}
	apply(api.LocalRunSnapshot{RunID: "r1", State: "running", Text: "先前正文", Process: []api.LocalProcessStep{tool}})
	if err := r.tt.Click("停止生成"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for r.q.pending() == 0 {
		select {
		case <-deadline:
			t.Fatal("stop confirmation was not delivered")
		default:
			runtime.Gosched()
		}
	}
	r.settle()
	if !st.stopped || !st.busy || !r.tt.HasText("已停止") {
		t.Fatal("stop confirmation must be visible while the tool is still running")
	}
	r.p.Main.Text = "下一条"
	r.a.submitComposer(f, r.key, r.p)
	if len(f.submissions()) != 0 {
		t.Fatal("another input was submitted before drain")
	}
	tool.State = "done"
	tool.Output = "工具完成"
	apply(api.LocalRunSnapshot{RunID: "r1", State: "stopping", Outcome: "stopped", Text: "迟到正文", Process: []api.LocalProcessStep{tool}})
	if !st.busy || !st.stopped || st.draft.Text != "" || r.tt.HasText("迟到正文") {
		t.Fatal("late text reopened a stopped run")
	}
	if len(st.procs) == 0 || len(st.procs[0].steps) == 0 || st.procs[0].steps[0].State != chatui.StepDone {
		t.Fatal("the completed tool result was not reflected")
	}
	apply(api.LocalRunSnapshot{RunID: "r1", State: "settled", Outcome: "stopped", Drained: true, Process: []api.LocalProcessStep{tool}})
	deadline = time.After(5 * time.Second)
	for st.busy {
		r.settle()
		select {
		case <-deadline:
			t.Fatal("drained run remained busy")
		default:
			runtime.Gosched()
		}
	}
	if !r.tt.HasText("已停止") {
		t.Fatal("stopped state disappeared after drain")
	}
}
