package turn

// 共用场景（testdata/shared/turn，格式见该目录 README.md）：本地私聊一个会话里 start / steer / queue / stop / withdraw 的语义。
// 用真实 Hub、临时库；模型是假网关，每次请求按脚本卡住，等场景放行（release）才回，所以谁先谁后由场景决定、不靠睡眠。
// SUMIKA_SHARED_UPDATE=1 把实际结果写回各文件的 expect。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/sharedtest"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

type tsStepOut struct {
	Error    string   `json:"error,omitempty"`    // busy | conflict | not_pending | 其他错误原文
	State    string   `json:"state,omitempty"`    // submit：回执里这句话的状态
	Run      string   `json:"run,omitempty"`      // submit：这句话挂在哪一轮（用开出这一轮的那句话的 label 称呼）
	SeqRank  int      `json:"seq_rank,omitempty"` // submit：回执序号在本场景所有序号里的名次（从 1 起）
	Outcome  string   `json:"outcome,omitempty"`  // wait_run：这一轮的结局
	Requeued []string `json:"requeued,omitempty"` // wait_run：收尾时才到、被改排成新一轮的插话
}
type tsStep struct {
	Do          string    `json:"do"` // submit | wait_request | release | stop | wait_run | withdraw
	Label       string    `json:"label,omitempty"`
	Mode        string    `json:"mode,omitempty"`
	Text        string    `json:"text,omitempty"`
	ClientKey   string    `json:"client_key,omitempty"`   // 默认等于 label
	ExpectedRun string    `json:"expected_run,omitempty"` // steer：目标那一轮（开出它的 label）；ghost 是不存在的一轮
	Conv        string    `json:"conv,omitempty"`         // 空是主会话；neighbor 是同一角色的另一个会话
	N           int       `json:"n,omitempty"`            // wait_request、release：第几次模型请求（按到达先后，从 1 起）
	Run         string    `json:"run,omitempty"`          // stop、wait_run：哪一轮（开出它的 label）
	Through     string    `json:"through,omitempty"`      // stop：取消到哪句话的序号为止（label）
	Expect      tsStepOut `json:"expect"`
}
type tsInputOut struct {
	Label string `json:"label"`
	State string `json:"state"`
	Run   string `json:"run"`
}
type tsRunOut struct {
	Label   string `json:"label"`
	State   string `json:"state"`
	Outcome string `json:"outcome"`
}
type tsRequestOut struct {
	N        int      `json:"n"`
	Contains []string `json:"contains"` // 请求正文里出现了哪些 label 的原话（含历史里的）
}
type tsSteerOut struct {
	Phase   string `json:"phase"`
	Input   string `json:"input"`
	Requeue bool   `json:"requeue"`
}
type tsFinal struct {
	Inputs   []tsInputOut   `json:"inputs"`
	Runs     []tsRunOut     `json:"runs"`
	Requests []tsRequestOut `json:"requests"`
	Steers   []tsSteerOut   `json:"steers"`
}
type tsScenario struct {
	ID          string   `json:"id"`
	About       string   `json:"about"`
	SourceTests []string `json:"source_tests"`
	Replies     []string `json:"replies"` // 每次模型请求回什么：text | tool（先调一个不用审批的工具，下一次请求才收尾）
	Steps       []tsStep `json:"steps"`
	Final       tsFinal  `json:"final"`
}

func TestSharedTurnInputs(t *testing.T) {
	t.Parallel()
	for _, path := range sharedtest.Files(t, "turn") {
		var sc tsScenario
		sharedtest.Load(t, path, &sc)
		t.Run(sc.ID, func(t *testing.T) {
			steps, final := runTurnScenario(t, sc)
			if sharedtest.Update() {
				for i := range sc.Steps {
					sc.Steps[i].Expect = steps[i]
				}
				sc.Final = final
				sharedtest.Save(t, path, sc)
				return
			}
			for i, s := range sc.Steps {
				if ok, want, got := sharedtest.Same(s.Expect, steps[i]); !ok {
					t.Errorf("第 %d 步（%s %s）不同\n期望：%s\n实际：%s", i+1, s.Do, s.Label, want, got)
				}
			}
			if ok, want, got := sharedtest.Same(sc.Final, final); !ok {
				t.Errorf("最终状态不同\n期望：%s\n实际：%s", want, got)
			}
		})
	}
}

func runTurnScenario(t *testing.T, sc tsScenario) ([]tsStepOut, tsFinal) {
	var replies []string
	for _, r := range sc.Replies {
		switch r {
		case "text":
			replies = append(replies, textReply)
		case "tool":
			replies = append(replies, callReply("recall_raw", `{"keywords":"编的"}`))
		default:
			t.Fatalf("replies 只认 text、tool：%q", r)
		}
	}
	var mu sync.Mutex
	var arrived []string
	released := map[int]bool{}
	h := newHarnessOpts(t, harnessOpts{replies: "[" + strings.Join(replies, ",") + "]", onBody: func(ctx context.Context, body string) {
		mu.Lock()
		arrived = append(arrived, body)
		n := len(arrived)
		mu.Unlock()
		for {
			mu.Lock()
			ok := released[n]
			mu.Unlock()
			if ok || ctx.Err() != nil {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}})
	h.use(toolPlugin{"mem", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(context.Context, ToolCall) (tools.Result, error) {
		return tools.Result{Text: "编的工具结果"}, nil
	}}}})
	until := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("等不到：%s", what)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	texts := map[string]string{}        // label -> 原话
	scopes := map[string]kernel.Scope{} // label -> scope
	recs := map[string]kernel.Receipt{}
	runOf := map[string]string{}   // label -> 当前挂的 RunID
	owner := map[string]string{}   // RunID -> 开出这一轮的 label
	byInput := map[string]string{} // InputID -> label
	var labels []string
	var seqs []uint64
	convOf := func(name string) string {
		switch name {
		case "":
			return consoleConv(h.c.CharacterID)
		case "neighbor":
			return consoleConv(h.c.CharacterID) + ":topic:neighbor"
		}
		t.Fatalf("conv 只认空、neighbor：%q", name)
		return ""
	}
	claim := func(label, run string) {
		runOf[label] = run
		if _, ok := owner[run]; !ok {
			owner[run] = label
		}
	}
	runLabel := func(label string) string { return owner[runOf[label]] }
	snapshot := func(label string) RunSnapshot {
		s, err := h.hub.WatchRun(scopes[label], runOf[label])
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		return s.Snapshot()
	}
	norm := func(err error) string {
		switch {
		case err == nil:
			return ""
		case errors.Is(err, ErrInputBusy):
			return "busy"
		case errors.Is(err, store.ErrInputConflict):
			return "conflict"
		case errors.Is(err, ErrInputNotPending):
			return "not_pending"
		}
		return err.Error()
	}
	var outs []tsStepOut
	for i, st := range sc.Steps {
		var out tsStepOut
		switch st.Do {
		case "submit":
			key := st.ClientKey
			if key == "" {
				key = st.Label
			}
			sub := lifecycleSubmission(t, h, convOf(st.Conv), st.Text, key, kernel.InputMode(st.Mode))
			if st.ExpectedRun == "ghost" {
				sub.ExpectedRunID = "ghost-run"
			} else if st.ExpectedRun != "" {
				sub.ExpectedRunID = runOf[st.ExpectedRun]
			}
			rec, err := h.hub.Submit(context.Background(), sub)
			out.Error = norm(err)
			if err == nil {
				if _, ok := texts[st.Label]; !ok {
					labels = append(labels, st.Label)
				}
				texts[st.Label], scopes[st.Label], recs[st.Label] = st.Text, sub.Input.Identity.Scope, rec
				byInput[rec.InputID] = st.Label
				claim(st.Label, rec.RunID)
				seqs = append(seqs, rec.Sequence)
				out.State, out.Run = string(rec.State), runLabel(st.Label)
			}
		case "wait_request":
			until(fmt.Sprintf("第 %d 次请求到达", st.N), func() bool { mu.Lock(); defer mu.Unlock(); return len(arrived) >= st.N })
		case "release":
			mu.Lock()
			released[st.N] = true
			mu.Unlock()
		case "stop":
			var through uint64
			if st.Through != "" {
				through = recs[st.Through].Sequence
			}
			_, err := h.hub.StopRun(context.Background(), kernel.StopRequest{Scope: scopes[st.Run], RunID: runOf[st.Run], ThroughSequence: through})
			out.Error = norm(err)
		case "wait_run":
			until("这一轮排空 "+st.Run, func() bool { return snapshot(st.Run).Drained })
			snap := snapshot(st.Run)
			out.Outcome = string(snap.Outcome)
			if snap.Requeued != nil {
				for _, in := range snap.Requeued.Inputs {
					if l, ok := byInput[in.InputID]; ok {
						claim(l, in.RunID)
						out.Requeued = append(out.Requeued, l)
					}
				}
			}
		case "withdraw":
			out.Error = norm(h.hub.WithdrawQueued(context.Background(), scopes[st.Label], recs[st.Label].InputID))
		default:
			t.Fatalf("第 %d 步 do 不认：%q", i+1, st.Do)
		}
		outs = append(outs, out)
	}
	// 序号换成名次：先收集每个成功 submit 的序号，按大小排名
	sorted := append([]uint64(nil), seqs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := map[uint64]int{}
	for _, s := range sorted {
		if _, ok := rank[s]; !ok {
			rank[s] = len(rank) + 1
		}
	}
	si := 0
	for i, st := range sc.Steps {
		if st.Do == "submit" && outs[i].Error == "" {
			outs[i].SeqRank = rank[seqs[si]]
			si++
		}
	}
	// 收尾：放行还卡着的请求，等所有轮排空，再取最终状态
	mu.Lock()
	for n := 1; n <= len(arrived); n++ {
		released[n] = true
	}
	mu.Unlock()
	for _, l := range labels {
		l := l
		until("最终排空 "+l, func() bool { return snapshot(l).Drained })
	}
	var final tsFinal
	final.Inputs, final.Runs, final.Requests, final.Steers = []tsInputOut{}, []tsRunOut{}, []tsRequestOut{}, []tsSteerOut{}
	doneRuns := map[string]bool{}
	for _, l := range labels {
		final.Inputs = append(final.Inputs, tsInputOut{l, string(inputState(t, h, recs[l].InputID)), runLabel(l)})
		if r := runLabel(l); r == l && !doneRuns[l] {
			doneRuns[l] = true
			s := snapshot(l)
			final.Runs = append(final.Runs, tsRunOut{l, string(s.State), string(s.Outcome)})
		}
	}
	mu.Lock()
	for i, body := range arrived {
		r := tsRequestOut{N: i + 1, Contains: []string{}}
		for _, l := range labels {
			if strings.Contains(body, texts[l]) {
				r.Contains = append(r.Contains, l)
			}
		}
		final.Requests = append(final.Requests, r)
	}
	mu.Unlock()
	h.mu.Lock()
	for _, e := range h.steers {
		final.Steers = append(final.Steers, tsSteerOut{e.Phase, byInput[e.ID], e.Requeue})
	}
	h.mu.Unlock()
	return outs, final
}
