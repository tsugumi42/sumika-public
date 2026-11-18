package advisor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

type fileResults struct {
	dir  string
	refs map[string]kernel.ResultRef
}

func (f *fileResults) Put(ctx context.Context, i kernel.Invocation, media string, r io.Reader, complete bool) (kernel.ResultRef, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return kernel.ResultRef{}, err
	}
	h := sha256.Sum256(b)
	id := hex.EncodeToString(h[:])
	ref := kernel.ResultRef{ID: id, SHA256: id, MediaType: media, Owner: i.Scope, RunID: i.RunID, Bytes: int64(len(b)), Complete: complete}
	if err := os.WriteFile(filepath.Join(f.dir, id), b, 0600); err != nil {
		return ref, err
	}
	f.refs[id] = ref
	return ref, nil
}
func (f *fileResults) Read(ctx context.Context, s kernel.Scope, id string, offset, limit int64) (kernel.ResultPage, error) {
	if err := ctx.Err(); err != nil {
		return kernel.ResultPage{}, err
	}
	ref, ok := f.refs[id]
	if !ok || ref.Owner != s {
		return kernel.ResultPage{}, os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(f.dir, id))
	if err != nil {
		return kernel.ResultPage{}, err
	}
	if offset < 0 || offset > int64(len(b)) || limit < 0 {
		return kernel.ResultPage{}, errors.New("range")
	}
	end := min(int64(len(b)), offset+limit)
	return kernel.ResultPage{Ref: ref, Text: string(b[offset:end]), Start: offset, End: end, Next: end, EOF: end == int64(len(b))}, nil
}

type testLifetime struct {
	mu     sync.Mutex
	active int
	sealed bool
	idle   chan struct{}
}
type testActivity struct{ life *testLifetime }

func (l *testLifetime) Begin(ctx context.Context, _ kernel.Invocation) (context.Context, kernel.Activity, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sealed {
		return nil, nil, errors.New("sealed")
	}
	if l.active == 0 {
		l.idle = make(chan struct{})
	}
	l.active++
	return ctx, &testActivity{l}, nil
}
func (a *testActivity) End(error) {
	a.life.mu.Lock()
	defer a.life.mu.Unlock()
	a.life.active--
	if a.life.active == 0 {
		close(a.life.idle)
	}
}
func (l *testLifetime) Seal() { l.mu.Lock(); l.sealed = true; l.mu.Unlock() }
func (l *testLifetime) Drain(ctx context.Context) error {
	l.mu.Lock()
	idle, n := l.idle, l.active
	l.mu.Unlock()
	if n == 0 {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type testCaller struct {
	client *llm.Client
	mu     sync.Mutex
	inv    []kernel.Invocation
	fail   error
}

func (c *testCaller) StreamChat(ctx context.Context, i kernel.Invocation, r chat.Request, on func(llm.Event)) (llm.Result, error) {
	c.mu.Lock()
	c.inv = append(c.inv, i)
	err := c.fail
	c.mu.Unlock()
	if err != nil {
		return llm.Result{}, err
	}
	return c.client.StreamChat(ctx, r, on)
}

type fixture struct {
	t          *testing.T
	s          *Service
	req        kernel.ReviewRequest
	files      *fileResults
	life       *testLifetime
	st         *store.Store
	caller     *testCaller
	mu         sync.Mutex
	current    kernel.ReviewRequest
	allow      bool
	bodies     []string
	accepted   map[string]bool
	acceptFail bool
	calls      int
	response   func(context.Context, string) (string, string, []llm.ToolCall)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, allow: true, accepted: map[string]bool{}}
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.st, err = store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.st.Close() })
	if _, err := f.st.Trace.Exec(`CREATE TABLE test_review_inbox(id TEXT PRIMARY KEY, root TEXT)`); err != nil {
		t.Fatal(err)
	}
	f.files = &fileResults{dir: t.TempDir(), refs: map[string]kernel.ResultRef{}}
	f.life = &testLifetime{}
	owner := kernel.Scope{CharacterID: "r", Conversation: "console:r", ScopeID: "work", Platform: "local", Channel: "console", AccountID: "desktop", Scene: "private", ActorKind: "owner", ActorID: "owner", AuthorityID: "grant-test", AuthorityRevision: 1}
	f.req = kernel.ReviewRequest{Invocation: kernel.Invocation{Scope: owner, RootID: "root", RunID: "run", Generation: 1, Kind: "foreground"}, CheckpointID: "checkpoint", Trigger: "candidate", Criteria: []string{"校验数量为二"}}
	ref, err := f.files.Put(context.Background(), f.req.Invocation, "text/plain", strings.NewReader("校验结果：数量=2；检查成功。"), true)
	if err != nil {
		t.Fatal(err)
	}
	e := kernel.Evidence{Ref: ref, Start: 0, End: ref.Bytes, Claim: f.req.Criteria[0]}
	f.req.Work = kernel.WorkState{RootID: "root", Owner: owner, Revision: 1, Goal: "交付校验结果", Completed: []string{"校验数量为二"}, Evidence: []kernel.Evidence{e}}
	f.req.Evidence = []kernel.Evidence{e}
	f.current = f.req
	f.response = func(context.Context, string) (string, string, []llm.ToolCall) {
		return goodResponse("done"), "stop", nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		f.calls++
		f.mu.Unlock()
		text, stop, calls := f.response(r.Context(), string(b))
		if r.Context().Err() != nil {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		delta := map[string]any{"content": text}
		if len(calls) > 0 {
			var out []any
			for n, c := range calls {
				out = append(out, map[string]any{"index": n, "id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": c.Args}})
			}
			delta["tool_calls"] = out
		}
		chunk := map[string]any{"id": "fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}}
		raw, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		raw, _ = json.Marshal(map[string]any{"id": "fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": stop}}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 40}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
	}))
	t.Cleanup(srv.Close)
	f.caller = &testCaller{client: llm.New(llm.Magpie(srv.URL + "/v1"))}
	cfg := config.DefaultReviews()
	cfg.Advisor.Enabled, cfg.Verify.Enabled = true, true
	cfg.Advisor.Model, cfg.Verify.Model = "fixture/review", "fixture/review"
	f.s, err = New(cfg, Dependencies{Caller: f.caller, Results: f.files, ReadPageBytes: 32768, Lifetime: f.life, Store: f.st, Current: f.isCurrent, Accept: f.accept, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.life.Drain(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}
func goodResponse(status string) string {
	return `{"status":"` + status + `","criteria":[{"criterion":"校验数量为二","status":"supported","evidence":[0]}],"findings":[]}`
}
func (f *fixture) isCurrent(ctx context.Context, r kernel.ReviewRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.allow || r.Invocation.RootID != f.current.Invocation.RootID || r.Invocation.Scope != f.current.Invocation.Scope || r.Invocation.Generation != f.current.Invocation.Generation || r.Work.Revision != f.current.Work.Revision {
		return errors.New("非当前授权/活动/工作")
	}
	return nil
}
func (f *fixture) accept(ctx context.Context, i kernel.Invocation, id string, findings []kernel.Finding) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	accepted, err := f.st.AcceptReview(ctx, id, func(tx *sql.Tx) error {
		if !f.allow || i.Scope != f.current.Invocation.Scope || i.Generation != f.current.Invocation.Generation {
			return errors.New("revoked during acceptance")
		}
		for _, finding := range findings {
			if _, err := tx.ExecContext(ctx, `INSERT INTO test_review_inbox(id,root) VALUES(?,?) ON CONFLICT DO NOTHING`, finding.ID, i.RootID); err != nil {
				return err
			}
		}
		if f.acceptFail {
			return errors.New("input transaction failed")
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if accepted {
		for _, finding := range findings {
			f.accepted[finding.ID] = true
		}
	}
	return accepted, nil
}
func (f *fixture) drain() {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.life.Drain(ctx); err != nil {
		f.t.Fatal(err)
	}
}

func TestReviewSnapshotAuthority(t *testing.T) {
	for _, scene := range []struct{ name, platform, channel, scene, conv string }{{"local-private", "local", "console", "private", "console:r"}, {"local-group", "local", "console", "group", "console:group:g"}, {"qq-private", "qq", "qq", "private", "qq:private:100:200"}, {"qq-group", "qq", "qq", "group", "qq:group:300"}} {
		t.Run(scene.name, func(t *testing.T) {
			f := newFixture(t)
			r := f.req
			r.Invocation.Scope.Platform, r.Invocation.Scope.Channel, r.Invocation.Scope.Scene, r.Invocation.Scope.Conversation = scene.platform, scene.channel, scene.scene, scene.conv
			r.Work.Owner = r.Invocation.Scope
			r.Evidence = append([]kernel.Evidence(nil), r.Evidence...)
			r.Evidence[0].Ref.Owner = r.Invocation.Scope
			r.Work.Evidence = r.Evidence
			f.files.refs[r.Evidence[0].Ref.ID] = r.Evidence[0].Ref
			f.current = r
			v, err := f.s.Check(context.Background(), r)
			if err != nil || v.Status != "done" {
				t.Fatalf("%+v %v", v, err)
			}
			invocation := f.caller.inv[0]
			if invocation.RunID == r.Invocation.RunID || invocation.ParentRunID != r.Invocation.RunID || invocation.Kind != "verify" || invocation.RootID != r.Invocation.RootID || invocation.Scope != r.Invocation.Scope {
				t.Fatalf("review identity reused parent: %+v", invocation)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(f.bodies[0]), &body); err != nil {
				t.Fatal(err)
			}
			if tools, ok := body["tools"]; ok && tools != nil {
				t.Fatalf("review has tools: %v", tools)
			}
			for _, forbidden := range []string{"thinking", "private-config", "cross-scope-secret", "AuthorityID", "ActorID"} {
				if strings.Contains(f.bodies[0], forbidden) {
					t.Fatalf("leaked %s", forbidden)
				}
			}
			// 同一个直接入口逐维度测试，不给任何场合开特例。
			for _, change := range []func(*kernel.Scope){func(s *kernel.Scope) { s.CharacterID = "other" }, func(s *kernel.Scope) { s.Conversation += "x" }, func(s *kernel.Scope) { s.AccountID = "other" }, func(s *kernel.Scope) { s.AuthorityID = "forged" }, func(s *kernel.Scope) { s.ActorID = "0" }, func(s *kernel.Scope) { s.ActorID = "" }, func(s *kernel.Scope) { s.ActorID = s.AccountID }, func(s *kernel.Scope) { s.AuthorityID = "" }, func(s *kernel.Scope) { s.AuthorityRevision++ }, func(s *kernel.Scope) { s.ScopeID = "other" }, func(s *kernel.Scope) { s.Scene = "" }} {
				bad := r
				change(&bad.Invocation.Scope)
				v, err := f.s.Check(context.Background(), bad)
				if err == nil || v.Status == "done" {
					t.Fatalf("unauthorized review: %+v %v", v, err)
				}
			}
			if f.calls != 1 {
				t.Fatalf("unauthorized HTTP dispatches: %d", f.calls)
			}
		})
	}
}

func TestReviewDedupAndCursor(t *testing.T) {
	f := newFixture(t)
	f.response = func(context.Context, string) (string, string, []llm.ToolCall) {
		return `{"findings":[{"severity":"concern","text":"补 核对！","evidence":[0]},{"severity":"blocker","text":"补 核对","evidence":[0]}]}`, "stop", nil
	}
	ctx := context.Background()
	if err := f.s.Observe(ctx, f.req); err != nil {
		t.Fatal(err)
	}
	f.drain()
	f.acceptFail = true
	if _, err := f.s.Take(ctx, f.req.Invocation); err == nil {
		t.Fatal("failed transaction cleared mailbox")
	}
	var inserted int
	if err := f.st.Trace.QueryRow(`SELECT count(*) FROM test_review_inbox`).Scan(&inserted); err != nil || inserted != 0 {
		t.Fatalf("failed acceptance left inbox rows=%d err=%v", inserted, err)
	}
	f.acceptFail = false
	type delivery struct {
		findings []kernel.Finding
		err      error
	}
	deliveries := make(chan delivery, 2)
	for range 2 {
		go func() { findings, err := f.s.Take(ctx, f.req.Invocation); deliveries <- delivery{findings, err} }()
	}
	var got []kernel.Finding
	for range 2 {
		d := <-deliveries
		if d.err != nil {
			t.Fatal(d.err)
		}
		got = append(got, d.findings...)
	}
	if len(got) != 1 || got[0].Severity != "blocker" {
		t.Fatalf("concurrent duplicate or lost escalation: %+v", got)
	}
	if got, err := f.s.Take(ctx, f.req.Invocation); err != nil || len(got) != 0 {
		t.Fatalf("duplicate delivered: %v %v", got, err)
	}
	if err := f.s.Observe(ctx, f.req); err != nil {
		t.Fatal(err)
	}
	f.drain()
	if f.calls != 1 {
		t.Fatalf("duplicate requests=%d", f.calls)
	}
	successor := f.req
	successor.Invocation.RunID = "successor"
	if err := f.s.Observe(ctx, successor); err != nil {
		t.Fatal(err)
	}
	f.drain()
	if f.calls != 1 {
		t.Fatalf("same checkpoint repeated after run change: %d", f.calls)
	}
	// 格式失败不成为 cursor，相同 checkpoint 可以在剩余额度内重试。
	r := f.req
	r.CheckpointID = "bad-format"
	f.response = func(context.Context, string) (string, string, []llm.ToolCall) { return "not json", "stop", nil }
	if err := f.s.Observe(ctx, r); err != nil {
		t.Fatal(err)
	}
	f.drain()
	record, ok, err := f.st.ReviewByID(ctx, reviewID(r, "advisor"))
	if err != nil || !ok || record.State != "failed" {
		t.Fatalf("bad cursor: %+v %v", record, err)
	}
	f.response = func(context.Context, string) (string, string, []llm.ToolCall) { return `{"findings":[]}`, "stop", nil }
	if err := f.s.Observe(ctx, r); err != nil {
		t.Fatal(err)
	}
	f.drain()
	record, _, _ = f.st.ReviewByID(ctx, reviewID(r, "advisor"))
	if record.State != "complete" {
		t.Fatalf("retry failed: %+v", record)
	}
	// 新工作/权限代次不能拿旧结论；邮箱保持可查，不重放。
	f.mu.Lock()
	f.current.Work.Revision++
	f.current.Invocation.Scope.AuthorityRevision++
	f.mu.Unlock()
	got, err = f.s.Take(ctx, f.req.Invocation)
	if err != nil || len(got) != 0 {
		t.Fatalf("stale accepted %v %v", got, err)
	}
}

func TestStoppedReviewNeverRestarts(t *testing.T) {
	for _, cause := range []string{"cancel", "budget", "restart", "authority", "revision"} {
		t.Run(cause, func(t *testing.T) {
			f := newFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			f.response = func(ctx context.Context, _ string) (string, string, []llm.ToolCall) {
				close(entered)
				<-release
				return `{"findings":[{"severity":"blocker","text":"补证据","evidence":[0]}]}`, "stop", nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := f.s.Observe(ctx, f.req); err != nil {
				t.Fatal(err)
			}
			<-entered
			f.mu.Lock()
			switch cause {
			case "cancel":
				cancel()
			case "authority":
				f.current.Invocation.Scope.AuthorityRevision++
			case "revision":
				f.current.Work.Revision++
			default:
				f.allow = false
			}
			f.mu.Unlock()
			close(release)
			f.drain()
			findings, err := f.s.Take(context.Background(), f.req.Invocation)
			if err != nil || len(findings) != 0 {
				t.Fatalf("stopped adopted %v %v", findings, err)
			}
			records, err := f.st.ReviewFindings(context.Background(), f.req.Invocation)
			if err != nil {
				t.Fatal(err)
			}
			if cause != "cancel" && len(records) != 1 {
				t.Fatalf("late result not retained: %d", len(records))
			}
			if f.calls != 1 || len(f.accepted) != 0 {
				t.Fatalf("restarted: calls=%d accepted=%d", f.calls, len(f.accepted))
			}
		})
	}
}

func reviewRecord(r kernel.ReviewRequest, id string) store.ReviewRecord {
	return store.ReviewRecord{ID: id, Digest: digest(r), Kind: "verify", Request: r}
}

type seqCaller struct {
	mu       sync.Mutex
	models   []string
	attempts []string
	errs     []error
	text     string
}

func (c *seqCaller) StreamChat(_ context.Context, inv kernel.Invocation, r chat.Request, _ func(llm.Event)) (llm.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = append(c.models, r.Model)
	c.attempts = append(c.attempts, inv.AttemptID)
	if len(c.errs) > 0 {
		err := c.errs[0]
		c.errs = c.errs[1:]
		if err != nil {
			return llm.Result{}, err
		}
	}
	return llm.Result{Text: c.text, Stop: "stop"}, nil
}

func TestReviewFailoverSwitchesOnceAndKeepsBadRequest(t *testing.T) {
	f := newFixture(t)
	caller := &seqCaller{errs: []error{io.ErrUnexpectedEOF, nil}, text: goodResponse("done")}
	selector := &llm.Failover{}
	f.s.deps.Caller = caller
	f.s.deps.Failover = selector
	f.s.deps.Models = func(string) ([]string, []llm.Model) { return []string{"standby"}, nil }
	v, err := f.s.Check(context.Background(), f.req)
	if err != nil || v.Status != "done" || len(caller.models) != 2 || caller.models[0] != "fixture/review" || caller.models[1] != "standby" || caller.attempts[0] == "" || caller.attempts[0] == caller.attempts[1] {
		t.Fatalf("审阅该换一次模型：%+v %v models=%v attempts=%v", v, err, caller.models, caller.attempts)
	}
	again := f.req
	again.CheckpointID = "again"
	if _, err := f.s.review(context.Background(), again, true, reviewInvocation(again, "verify")); err != nil || len(caller.models) != 3 || caller.models[2] != "standby" {
		t.Fatalf("冷却期不该再打原模型：%v %v", err, caller.models)
	}
	f.s.deps.Failover = &llm.Failover{}
	before := len(caller.models)
	caller.errs = []error{&llm.StatusError{Code: 400, Body: "bad"}}
	bad := f.req
	bad.CheckpointID = "bad"
	bad.Invocation.RootID = "other-root"
	bad.Work.RootID = "other-root"
	f.current = bad
	if _, err := f.s.review(context.Background(), bad, true, reviewInvocation(bad, "verify")); err == nil || strings.Contains(err.Error(), "故障切换") || len(caller.models) != before+1 {
		t.Fatalf("请求本身的错不该换：%v models=%v", err, caller.models)
	}
}
