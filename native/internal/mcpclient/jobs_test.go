package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sumika-native/internal/kernel"
	"sumika-native/internal/runstate"
)

func TestBackgroundJobCancel(t *testing.T) {
	journal := mcpFixtureStore(t)
	_, a := mcpAccess(t, "mcp:docs")
	remote := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	waiting := make(chan struct{}, 1)
	serverSawCancel := make(chan struct{}, 1)
	remote.AddTool(&mcp.Tool{Name: "search", Description: "fixture", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		if err := json.Unmarshal(r.Params.Arguments, &args); err != nil {
			return nil, err
		}
		if args["query"] == "wait" {
			waiting <- struct{}{}
			<-ctx.Done() // 客户端断开这一次请求，服务端的 context 才会取消
			serverSawCancel <- struct{}{}
			return nil, ctx.Err()
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture"}}}, nil
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remote }, nil))
	defer httpServer.Close()
	client := New(journal)
	defer client.Close()
	if err := client.Connect(t.Context(), ServerConfig{ID: "docs", Type: "http", URL: httpServer.URL, Sumika: ServerPolicy{Enabled: true, Include: []string{"search"}, CallTimeoutMs: 60000}}); err != nil {
		t.Fatal(err)
	}
	list, err := client.Search(t.Context(), a, SearchRequest{Query: "fixture"})
	if err != nil || len(list.Items) != 1 {
		t.Fatal(list, err)
	}
	entry := list.Items[0]
	lifetime := runstate.New(nil)
	inv := kernel.Invocation{Scope: a.Scope(), Kind: kernel.KindForeground, RunID: "run", RootID: "run", ToolID: "tool", Generation: 1}
	start := func(query string) *Job {
		t.Helper()
		call, err := client.Resolve(t.Context(), a, entry.PublicID, entry.SchemaDigest, json.RawMessage(`{"query":"`+query+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = call.Approve(t.Context(), func(context.Context, *ResolvedCall) error { return nil }); err != nil {
			t.Fatal(err)
		}
		job, err := client.Start(call, inv, t.Context(), lifetime, nil)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}

	// 取消已完成的调用：不报错，原样报告最终状态
	done := start("fast")
	select {
	case <-done.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("快调用没有完成")
	}
	if state, err := client.CancelJob(a.Scope(), done.ID, time.Second); err != nil || state != "done" {
		t.Fatal("取消已完成的调用", state, err)
	}

	// 卡住的调用：别人和不存在的取消不了；自己取消后请求真的断开
	stuck := start("wait")
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("调用没有到达服务端")
	}
	if stuck.State() != "running" {
		t.Fatal("卡住的调用应在运行", stuck.State())
	}
	other := a.Scope()
	other.Conversation = "other-conversation"
	if _, err := client.CancelJob(other, stuck.ID, time.Second); !errors.Is(err, ErrNoJob) {
		t.Fatal("别的会话取消了我的调用", err)
	}
	if _, err := client.CancelJob(a.Scope(), "no-such-call", time.Second); !errors.Is(err, ErrNoJob) {
		t.Fatal("不存在的调用应报没有", err)
	}
	if stuck.State() != "running" {
		t.Fatal("被拒的取消不该影响调用")
	}
	state, err := client.CancelJob(a.Scope(), stuck.ID, 5*time.Second)
	if err != nil || state != "cancelled" {
		t.Fatal("取消卡住的调用", state, err)
	}
	select {
	case <-serverSawCancel:
	case <-time.After(5 * time.Second):
		t.Fatal("请求没有真的断开：服务端 context 没取消")
	}
	if _, err := stuck.Outcome(); err == nil {
		t.Fatal("取消被当成成功")
	}
	// 再取消一次：已结束，报告 cancelled
	if state, err = client.CancelJob(a.Scope(), stuck.ID, time.Second); err != nil || state != "cancelled" {
		t.Fatal(state, err)
	}
}
