package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/store"
)

// quarantineFixture 起一个远端 HTTP 服务：work 工具的 mode 为 hang 时一直挂着（直到被取消），否则马上返回。
type quarantineFixture struct {
	client  *Client
	journal store.EcosystemStore
	access  ecosystem.Access
	calls   atomic.Int32
	waiting chan struct{} // 每次 hang 开始时收一个
	changes chan string
	cfg     ServerConfig
}

func newQuarantineFixture(t *testing.T, callTimeoutMs int) *quarantineFixture {
	t.Helper()
	return newQuarantineFixtureWith(t, callTimeoutMs, func(j store.EcosystemStore) Journal { return j })
}

// failingJournal 让指定的写库动作失败：模拟“调用返回了、写库却失败了”这类形状
type failingJournal struct {
	store.EcosystemStore
	failEnd, failTerminated bool
}

func (j failingJournal) MCPEnd(ctx context.Context, id string, known bool, cause string) error {
	if j.failEnd {
		return errors.New("模拟写库失败")
	}
	return j.EcosystemStore.MCPEnd(ctx, id, known, cause)
}

func (j failingJournal) MCPTerminated(ctx context.Context, id string) error {
	if j.failTerminated {
		return errors.New("模拟写库失败")
	}
	return j.EcosystemStore.MCPTerminated(ctx, id)
}

func newQuarantineFixtureWith(t *testing.T, callTimeoutMs int, wrap func(store.EcosystemStore) Journal) *quarantineFixture {
	t.Helper()
	f := &quarantineFixture{journal: mcpFixtureStore(t), waiting: make(chan struct{}, 8), changes: make(chan string, 32)}
	_, f.access = mcpAccess(t, "mcp:docs")
	remote := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	stop := make(chan struct{})
	remote.AddTool(&mcp.Tool{Name: "work", Description: "fixture work", InputSchema: json.RawMessage(`{"type":"object","properties":{"mode":{"type":"string"}},"additionalProperties":false}`)}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.calls.Add(1)
		var args struct{ Mode string }
		_ = json.Unmarshal(r.Params.Arguments, &args)
		if args.Mode == "hang" {
			f.waiting <- struct{}{}
			select {
			case <-ctx.Done():
			case <-stop:
			}
			return nil, ctx.Err()
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
	})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remote }, nil))
	t.Cleanup(srv.Close)
	// 清理按后进先出：stop 要先于 srv.Close 关掉，否则挂着的处理函数等不到 stop，srv.Close 会一直等到调用时限（断言失败时曾卡 233 秒）
	t.Cleanup(func() { close(stop) })
	f.client = New(wrap(f.journal))
	t.Cleanup(func() { f.client.Close() })
	f.client.SetOnChange(func(id string) { f.changes <- id })
	f.cfg = ServerConfig{ID: "docs", Type: "http", URL: srv.URL, Sumika: ServerPolicy{Enabled: true, Include: []string{"work"}, CallTimeoutMs: callTimeoutMs}}
	if err := f.client.Connect(t.Context(), f.cfg); err != nil {
		t.Fatal(err)
	}
	return f
}

// resolved 搜到 work 并冻结一次带这个 mode 的调用，已审批
func (f *quarantineFixture) resolved(t *testing.T, mode string) *ResolvedCall {
	t.Helper()
	list, err := f.client.Search(t.Context(), f.access, SearchRequest{Query: "fixture"})
	if err != nil || len(list.Items) != 1 {
		t.Fatal(list, err)
	}
	e := list.Items[0]
	p, err := f.client.Resolve(t.Context(), f.access, e.PublicID, e.SchemaDigest, json.RawMessage(`{"mode":"`+mode+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Approve(t.Context(), func(context.Context, *ResolvedCall) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *quarantineFixture) noteOf(t *testing.T, server string) store.MCPCall {
	t.Helper()
	calls, err := f.journal.MCPQuarantined(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if c.Server == server {
			return c
		}
	}
	t.Fatalf("库里没有 %s 的隔离记录", server)
	return store.MCPCall{}
}

func (f *quarantineFixture) waitChange(t *testing.T) {
	t.Helper()
	select {
	case <-f.changes:
	case <-time.After(10 * time.Second):
		t.Fatal("状态变化没有通知宿主")
	}
}

// 超时 → 隔离 → 拒收新调用 → 不带原因解除失败 → 带原因解除后又能调；旧记录保留成 risk_accepted，不改成成功。
func TestMCPTimeoutQuarantineThenReleaseWithReason(t *testing.T) {
	f := newQuarantineFixture(t, 200)
	hang := f.resolved(t, "hang")
	if _, err := hang.Execute(t.Context()); err == nil {
		t.Fatal("挂住的调用被当成功")
	}
	select {
	case <-f.waiting:
	case <-time.After(10 * time.Second):
		t.Fatal("调用没到远端")
	}
	f.waitChange(t)
	c := f.noteOf(t, "docs")
	if c.State != store.MCPStateUnknown || c.Note.Cause != store.MCPCauseTimeout || c.Note.Transport != "http" {
		t.Fatalf("超时没按“结果未知、原因超时”留痕：%+v", c)
	}
	before := f.calls.Load()
	if _, err := f.resolved(t, "ok").Execute(t.Context()); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("隔离期间新调用没被挡：%v", err)
	}
	if f.calls.Load() != before {
		t.Fatal("被挡的调用仍到了远端")
	}
	if err := f.client.Connect(t.Context(), f.cfg); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("重连清掉了隔离：%v", err)
	}
	for _, bad := range []string{"", "  ", "\n"} {
		if _, err := f.client.Release(t.Context(), "docs", "主人", bad); err == nil {
			t.Fatalf("原因 %q 也能解除", bad)
		}
	}
	if _, err := f.resolved(t, "ok").Execute(t.Context()); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("没带原因的解除尝试后又能调了：%v", err)
	}
	n, err := f.client.Release(t.Context(), "docs", "主人（测试）", "远端文档服务重启过，旧调用不可能还在跑")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	f.waitChange(t)
	if _, err := f.resolved(t, "ok").Execute(t.Context()); err != nil {
		t.Fatalf("解除后仍调不了：%v", err)
	}
	var state, raw string
	if err := f.journal.DB.QueryRowContext(t.Context(), `SELECT state,reason FROM kernel_mcp_calls WHERE id=?`, c.ID).Scan(&state, &raw); err != nil {
		t.Fatal(err)
	}
	var note store.MCPNote
	if err := json.Unmarshal([]byte(raw), &note); err != nil || state != store.MCPStateRiskAccepted || note.ReleasedBy != "主人（测试）" || note.ReleasedAt == 0 || note.Cause != store.MCPCauseTimeout {
		t.Fatalf("解除没留痕或改写了旧记录：%s %s", state, raw)
	}
}

// 调用还在进行时不能解除：那一行不是遗留，放行会让第二次调用和它并行。
func TestMCPReleaseRefusedWhileCallInFlight(t *testing.T) {
	f := newQuarantineFixture(t, 60000)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	call := f.resolved(t, "hang")
	go func() { _, err := call.Execute(ctx); done <- err }()
	select {
	case <-f.waiting:
	case <-time.After(10 * time.Second):
		t.Fatal("调用没到远端")
	}
	if n, err := f.client.Release(t.Context(), "docs", "主人", "手滑"); !errors.Is(err, ErrCallInFlight) || n != 0 {
		t.Fatalf("进行中的调用被解除了：n=%d err=%v", n, err)
	}
	if blocked, _ := f.journal.MCPBlocked(t.Context(), "docs"); !blocked {
		t.Fatal("进行中的那一行被改了")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("取消被当成功")
	}
	if c := f.noteOf(t, "docs"); c.Note.Cause != store.MCPCauseCancelled {
		t.Fatalf("被停止的原因没记对：%+v", c)
	}
}

// 调用成功返回了、结局却没写进库：调用按失败处理（不当成功），那一行停在 dispatched，服务继续被挡，直到主人解除。
func TestMCPEndWriteFailureKeepsServerBlocked(t *testing.T) {
	f := newQuarantineFixtureWith(t, 60000, func(j store.EcosystemStore) Journal { return failingJournal{EcosystemStore: j, failEnd: true} })
	if _, err := f.resolved(t, "ok").Execute(t.Context()); err == nil {
		t.Fatal("结局写库失败还当成功返回")
	}
	if blocked, _ := f.journal.MCPBlocked(t.Context(), "docs"); !blocked {
		t.Fatal("结局没写进库，服务却不挡了")
	}
	if _, err := f.resolved(t, "ok").Execute(t.Context()); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("下一次调用没被挡：%v", err)
	}
}
