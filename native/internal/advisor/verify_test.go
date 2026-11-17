package advisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
)

func TestCompletionRequiresEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*fixture)
	}{
		{"valid", "done", func(*fixture) {}},
		{"missing-result", "partial", func(f *fixture) {
			if err := os.Remove(filepath.Join(f.files.dir, f.req.Evidence[0].Ref.ID)); err != nil {
				t.Fatal(err)
			}
		}},
		{"truncated", "partial", func(f *fixture) { f.req.Evidence[0].Ref.Complete = false }},
		{"forged-hash", "partial", func(f *fixture) { f.req.Evidence[0].Ref.SHA256 = strings.Repeat("0", 64) }},
		{"preview-truncated", "partial", func(f *fixture) {
			ref, err := f.files.Put(context.Background(), f.req.Invocation, "text/plain", strings.NewReader(strings.Repeat("x", 5000)), true)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Evidence = []kernel.Evidence{{Ref: ref, Start: 0, End: ref.Bytes, Claim: f.req.Criteria[0]}}
		}},
		{"corrupt-bytes", "partial", func(f *fixture) {
			id := f.req.Evidence[0].Ref.ID
			b, err := os.ReadFile(filepath.Join(f.files.dir, id))
			if err != nil {
				t.Fatal(err)
			}
			b[0] ^= 1
			if err := os.WriteFile(filepath.Join(f.files.dir, id), b, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"byte-range", "partial", func(f *fixture) { f.req.Evidence[0].Start = 1 }},
		{"range-overrun", "partial", func(f *fixture) { f.req.Evidence[0].End++ }},
		{"cross-owner", "partial", func(f *fixture) { f.req.Evidence[0].Ref.Owner.Conversation = "other" }},
		{"unknown-side-effect", "blocked", func(f *fixture) { f.req.Work.Unknown = []string{"写入进程尚未退出，结局 unknown"} }},
		{"undefined-criteria", "partial", func(f *fixture) { f.req.Criteria = nil }},
		{"mere-claim", "partial", func(f *fixture) {
			f.req.Evidence = nil
			f.req.Work.Evidence = nil
			f.req.Work.Completed = []string{"我完成了"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.change(f)
			v, _ := f.s.Check(context.Background(), f.req)
			if v.Status != tc.want {
				t.Fatalf("got %+v want %s", v, tc.want)
			}
			if tc.name == "undefined-criteria" && (len(v.Findings) != 1 || v.Findings[0].Text != "未定义验收") {
				t.Fatalf("undefined criteria hidden: %+v", v)
			}
			if tc.want != "done" && tc.name != "preview-truncated" && f.calls != 0 {
				t.Fatalf("invalid evidence dispatched %d", f.calls)
			}
		})
	}
}

func TestVerificationFailuresArePartial(t *testing.T) {
	for _, failure := range []string{"timeout", "length", "bad-json", "tool", "budget", "missing-item", "forged-index", "too-many-blockers", "unknown-status", "missing-support", "duplicate-item"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.response = func(ctx context.Context, _ string) (string, string, []llm.ToolCall) {
				switch failure {
				case "timeout":
					<-ctx.Done()
					return "", "stop", nil
				case "length":
					return goodResponse("done"), "length", nil
				case "bad-json":
					return "done!", "stop", nil
				case "tool":
					return goodResponse("done"), "tool_calls", []llm.ToolCall{{ID: "untrusted", Name: "shell", Args: `{"command":"bad"}`}}
				case "missing-item":
					return `{"status":"done","criteria":[],"findings":[]}`, "stop", nil
				case "forged-index":
					return strings.ReplaceAll(goodResponse("done"), "[0]", "[999]"), "stop", nil
				case "too-many-blockers":
					return `{"status":"done","findings":[{"severity":"blocker","text":"a"},{"severity":"blocker","text":"b"},{"severity":"blocker","text":"c"},{"severity":"blocker","text":"d"},{"severity":"blocker","text":"e"}]}`, "stop", nil
				case "unknown-status":
					return goodResponse("passed"), "stop", nil
				case "missing-support":
					return strings.ReplaceAll(goodResponse("done"), "[0]", "[]"), "stop", nil
				case "duplicate-item":
					return `{"status":"done","criteria":[{"criterion":"校验数量为二","status":"supported","evidence":[0]},{"criterion":"校验数量为二","status":"supported","evidence":[0]}],"findings":[]}`, "stop", nil
				default:
					return goodResponse("done"), "stop", nil
				}
			}
			if failure == "timeout" {
				f.s.cfg.Verify.TimeoutMs = 20
			}
			if failure == "budget" {
				f.caller.fail = errors.New("root budget exhausted")
			}
			v, err := f.s.Check(context.Background(), f.req)
			if err == nil || v.Status != "partial" {
				t.Fatalf("failure passed: %+v %v", v, err)
			}
			if _, err := os.Stat(filepath.Join(f.files.dir, f.req.Evidence[0].Ref.ID)); err != nil {
				t.Fatal("existing artifact lost", err)
			}
			record, ok, e := f.st.ReviewByID(context.Background(), reviewID(f.req, "verify"))
			if e != nil || !ok || record.State != "failed" {
				t.Fatalf("advanced failure cursor %+v %v", record, e)
			}
		})
	}
}

func TestAdvisorMergesPendingAndBoundsRequests(t *testing.T) {
	f := newFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	f.response = func(context.Context, string) (string, string, []llm.ToolCall) {
		calls++
		if calls == 1 {
			close(entered)
			<-release
		}
		return `{"findings":[]}`, "stop", nil
	}
	if err := f.s.Observe(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
	<-entered
	// 同根正在运行时只保留最后一份未开始检查点。
	for n := range 10 {
		r := f.req
		r.CheckpointID = string(rune('a' + n))
		if err := f.s.Observe(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	f.drain()
	if f.calls != 2 {
		t.Fatalf("pending not merged: %d", f.calls)
	}
	r := f.req
	r.CheckpointID = "third"
	if err := f.s.Observe(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	f.drain()
	r.CheckpointID = "fourth"
	if err := f.s.Observe(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	f.drain()
	if f.calls != 3 {
		t.Fatalf("root request cap bypassed: %d", f.calls)
	}
}

func TestReviewPersistenceAndRestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v, err := f.s.Check(ctx, f.req)
	if err != nil || v.Status != "done" {
		t.Fatal(v, err)
	}
	next, err := New(f.s.cfg, f.s.deps)
	if err != nil {
		t.Fatal(err)
	}
	v, err = next.Check(ctx, f.req)
	if err != nil || v.Status != "done" || f.calls != 1 {
		t.Fatalf("restart lost cursor %+v %v requests=%d", v, err, f.calls)
	}
	// 已派发但无结局的检查点在新服务里保留 partial，不自动重放。
	r := f.req
	r.CheckpointID = "interrupted"
	id := reviewID(r, "verify")
	started, err := f.st.BeginReview(ctx, reviewRecord(r, id), 2)
	if err != nil || !started {
		t.Fatal(err)
	}
	v, err = next.Check(ctx, r)
	if err == nil || v.Status != "partial" || f.calls != 1 {
		t.Fatalf("replayed interrupted review %+v %v", v, err)
	}
	// 模型已返回、保存失败时不推进 cursor，也不伪报核验通过。
	failed := newFixture(t)
	_, err = failed.st.Trace.Exec(`CREATE TRIGGER deny_review_finish BEFORE UPDATE OF verdict_json ON kernel_reviews BEGIN SELECT RAISE(ABORT,'fixture save failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	v, err = failed.s.Check(ctx, failed.req)
	if err == nil || v.Status != "partial" {
		t.Fatalf("save failure passed: %+v %v", v, err)
	}
	record, ok, err := failed.st.ReviewByID(ctx, reviewID(failed.req, "verify"))
	if err != nil || !ok || record.State != "running" {
		t.Fatalf("save failure advanced cursor: %+v %v", record, err)
	}
}

func TestReviewInputBudgetIsGrouped(t *testing.T) {
	f := newFixture(t)
	f.s.cfg.Advisor.MaxInputTokens = 400
	f.req.Work.Constraints = []string{strings.Repeat("越界资料", 500)}
	v, err := f.s.Check(context.Background(), f.req)
	if err == nil || v.Status != "partial" || f.calls != 0 {
		t.Fatalf("input budget bypass %+v %v", v, err)
	}
}

func TestVerificationDeadlineIncludesQueue(t *testing.T) {
	f := newFixture(t)
	f.s.cfg.Verify.TimeoutMs = 20
	for range cap(f.s.slots) {
		f.s.slots <- struct{}{}
	}
	start := time.Now()
	v, err := f.s.Check(context.Background(), f.req)
	for range cap(f.s.slots) {
		<-f.s.slots
	}
	if err == nil || v.Status != "partial" || f.calls != 0 {
		t.Fatalf("queue timeout dispatched %+v %v", v, err)
	}
	t.Logf("queue timeout elapsed=%s requests=%d", time.Since(start), f.calls)
}

func TestContinuationDisabledRemainsPartial(t *testing.T) {
	f := newFixture(t)
	f.s.cfg.Verify.MaxContinuations = 0
	f.response = func(context.Context, string) (string, string, []llm.ToolCall) {
		return goodResponse("continue"), "stop", nil
	}
	v, err := f.s.Check(context.Background(), f.req)
	if err != nil || v.Status != "partial" {
		t.Fatalf("disabled continuation accepted: %+v %v", v, err)
	}
}
