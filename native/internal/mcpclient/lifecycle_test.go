package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestMCPBindingSchemaAndCancellation(t *testing.T) {
	journal := mcpFixtureStore(t)
	_, a := mcpAccess(t, "mcp:docs")
	var calls atomic.Int32
	remote := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	waiting := make(chan struct{})
	// 远端处理协程是被取消通知唤醒的；stop 只在测试收尾时兜底，防止回归时挂到总超时，
	// 是否由取消唤醒由 cancelled 断言。
	cancelled := make(chan struct{})
	stop := make(chan struct{})
	add := func(schema string) {
		remote.AddTool(&mcp.Tool{Name: "search", Description: "fixture search", InputSchema: json.RawMessage(schema)}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			var args map[string]any
			if err := json.Unmarshal(r.Params.Arguments, &args); err != nil {
				return nil, err
			}
			if args["query"] == "wait" {
				close(waiting)
				select {
				case <-ctx.Done():
					close(cancelled)
				case <-stop:
				}
				return nil, ctx.Err()
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture"}}}, nil
		})
	}
	add(`{"type":"object"}`)
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remote }, nil))
	defer httpServer.Close()
	client := New(journal)
	defer client.Close()
	defer close(stop)
	cfg := ServerConfig{ID: "docs", Type: "http", URL: httpServer.URL, Headers: map[string]string{"X-Fixture-Credential": "first"}, Sumika: ServerPolicy{Enabled: true, Include: []string{"search"}}}
	if err := client.Connect(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	list, err := client.Search(t.Context(), a, SearchRequest{Query: "fixture"})
	if err != nil || len(list.Items) != 1 {
		t.Fatal(list, err)
	}
	entry := list.Items[0]
	old, err := client.Resolve(t.Context(), a, entry.PublicID, entry.SchemaDigest, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = old.Approve(t.Context(), func(context.Context, *ResolvedCall) error { return nil }); err != nil {
		t.Fatal(err)
	}
	add(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	cfg.Headers = map[string]string{"X-Fixture-Credential": "second"}
	if err = client.Connect(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if old.Valid() {
		t.Fatal("凭据/schema 更新后旧冻结目标仍有效")
	}
	if _, err = old.Execute(t.Context()); err == nil {
		t.Fatal("旧目标被派发")
	}
	if _, err = client.Resolve(t.Context(), a, entry.PublicID, entry.SchemaDigest, json.RawMessage(`{"query":"fixture"}`)); err == nil {
		t.Fatal("旧 schema 被新连接采用")
	}
	if calls.Load() != 0 {
		t.Fatal("失效目标已触发服务", calls.Load())
	}
	list, err = client.Search(t.Context(), a, SearchRequest{Query: "fixture"})
	if err != nil || len(list.Items) != 1 {
		t.Fatal(list, err)
	}
	entry = list.Items[0]
	current, err := client.Resolve(t.Context(), a, entry.PublicID, entry.SchemaDigest, json.RawMessage(`{"query":"wait"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = current.Approve(t.Context(), func(context.Context, *ResolvedCall) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := current.Execute(ctx)
		finished <- err
	}()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("真实 SDK 调用未到达服务端", ctx.Err())
	}
	// 固定重载已发布新类型、旧 HTTP 才取消的交错，不能把旧远端任务记成 stdio 已退出。
	current.server.mu.Lock()
	current.server.config.Type = "stdio"
	current.server.mu.Unlock()
	cancel()
	if err = <-finished; err == nil {
		t.Fatal("取消被当作成功")
	}
	if calls.Load() != 1 {
		t.Fatal("真实 SDK 取消未抵达调用", calls.Load())
	}
	// 取消通知要在连接被关掉之前送到远端，远端那次调用才会退出。
	select {
	case <-cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("取消通知没有在关连接前送到远端")
	}
	blocked, err := journal.MCPBlocked(t.Context(), "docs")
	if err != nil || !blocked {
		t.Fatal("远端终止未知未持久隔离", blocked, err)
	}
	if err = client.Connect(t.Context(), cfg); err == nil {
		t.Fatal("重连清掉未知隔离")
	}
	t.Logf("sdk_http_calls=%d old_target_calls=0 unknown_blocked=%v", calls.Load(), blocked)
}

func TestMCPDisableDuringHandshake(t *testing.T) {
	journal := mcpFixtureStore(t)
	_, access := mcpAccess(t, "mcp:docs")
	var calls atomic.Int32
	remote := fixtureServer(&calls)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remote }, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	var blocked, released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && blocked.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	client := New(journal)
	defer client.Close()
	defer unblock()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- client.Connect(ctx, ServerConfig{ID: "docs", Type: "http", URL: httpServer.URL, Sumika: ServerPolicy{Enabled: true, Include: []string{"search"}}})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := client.Disable("docs"); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-finished; err == nil {
		t.Fatal("禁用之后旧握手重新发布了来源")
	}
	listing, err := client.Search(t.Context(), access, SearchRequest{Query: "搜索"})
	if err != nil || len(listing.Items) != 0 || calls.Load() != 0 {
		t.Fatalf("已撤销来源仍可发现或执行: %+v calls=%d err=%v", listing, calls.Load(), err)
	}
}

// 远端迟迟不回响应头时，传输层不能占着服务锁：同服务的校验和目录搜索照常返回。
func TestMCPSlowCallDoesNotHoldServerLock(t *testing.T) {
	journal := mcpFixtureStore(t)
	_, a := mcpAccess(t, "mcp:docs")
	remote := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	waiting := make(chan struct{})
	stop := make(chan struct{})
	remote.AddTool(&mcp.Tool{Name: "search", Description: "fixture search", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(waiting)
		select {
		case <-ctx.Done():
		case <-stop:
		}
		return nil, ctx.Err()
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remote }, nil))
	defer httpServer.Close()
	client := New(journal)
	defer client.Close()
	defer close(stop)
	cfg := ServerConfig{ID: "docs", Type: "http", URL: httpServer.URL, Sumika: ServerPolicy{Enabled: true, Include: []string{"search"}}}
	if err := client.Connect(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	list, err := client.Search(t.Context(), a, SearchRequest{Query: "fixture"})
	if err != nil || len(list.Items) != 1 {
		t.Fatal(list, err)
	}
	entry := list.Items[0]
	call, err := client.Resolve(t.Context(), a, entry.PublicID, entry.SchemaDigest, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = call.Approve(t.Context(), func(context.Context, *ResolvedCall) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := call.Execute(ctx)
		finished <- err
	}()
	<-waiting
	probed := make(chan error, 1)
	go func() {
		if !call.Valid() {
			probed <- errors.New("进行中的调用被判为失效")
			return
		}
		_, err := client.Resolve(t.Context(), a, entry.PublicID, entry.SchemaDigest, json.RawMessage(`{}`))
		probed <- err
	}()
	select {
	case err = <-probed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("远端调用等响应头时，同服务的校验和解析被服务锁堵住")
	}
	cancel()
	if err = <-finished; err == nil {
		t.Fatal("取消被当作成功")
	}
}
