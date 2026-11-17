package app

import (
	"context"
	"encoding/json"
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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sumika-native/internal/api"
	"sumika-native/internal/approval"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/subagent"
)

type ecosystemRoundTripper func(*http.Request) (*http.Response, error)

func (f ecosystemRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 正式 App 装配；只替换外部模型、MCP 和网页响应，不替换身份、账本、结果文件和内部执行循环。
func TestEcosystemToolRoundTrip(t *testing.T) {
	e := newEnv(t)
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "proof.txt"), []byte("made-child-proof-tail"), 0600); err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "fixture_search", Description: "fixture documents", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "made-mcp-proof"}}}, nil
	})
	mcpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer mcpServer.Close()
	rawConfig := fmt.Sprintf(`{"mcpServers":{"docs":{"type":"http","url":%q,"sumika":{"enabled":true,"include":["fixture_search"]}}},"sumika":{"enabled":true}}`, mcpServer.URL)
	if err := os.WriteFile(filepath.Join(e.data, "config", "mcp.json"), []byte(rawConfig), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUMIKA_KERNEL_TEST_KEY", "invented-key")
	e.configure(fmt.Sprintf("tools:\n  workdir: %q\nskills:\n  enabled: true\nweb:\n  enabled: true\n  keyRef: '${env:SUMIKA_KERNEL_TEST_KEY}'\n  dailySearch: 20\n  dailyFetch: 20\n  monthlyMicroCredits: %d\nsubagents:\n  enabled: true\n  model: fixture-child-model\n", filepath.ToSlash(workdir), 10*1_000_000))
	args, _ := json.Marshal(map[string]any{"tool": "mcp__docs__fixture_search", "schemaDigest": ecosystem.Digest(json.RawMessage(`{"type":"object"}`)), "arguments": map[string]any{}})
	parentReplies := []llmtest.Reply{
		llmtest.Call("skill-call", "skill", `{"name":"builtin:sumika"}`),
		llmtest.Call("mcp-search", "mcp_search", `{"query":"fixture"}`),
		llmtest.Call("mcp-call", "mcp_call", string(args)),
		llmtest.Call("web-call", "web_search", `{"queries":["编造资料"]}`),
		llmtest.Call("task-call", "task", `{"agent":"reader","description":"编造的只读任务","prompt":"读取 proof.txt 并给结论"}`),
		llmtest.Call("parent-read", "read", `{"path":"proof.txt"}`),
		llmtest.Text("收到独立任务结论。"),
	}
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`), json.RawMessage(`{"id":"fixture-child-model","context_length":32768}`)}
	parentWaiting, releaseParent := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseParent) })
	defer release()
	var routeMu sync.Mutex
	parentCalls, childCalls := 0, 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := llmtest.Text("撤权以后普通聊天。")
		if r.Method == http.MethodPost {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
			var request struct{ Model string }
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Error(err)
				return
			}
			routeMu.Lock()
			child := request.Model == "fixture-child-model"
			n := parentCalls
			if child {
				n = childCalls
				childCalls++
			} else {
				parentCalls++
			}
			routeMu.Unlock()
			if child {
				switch n {
				case 0:
					reply = llmtest.Call("child-write-denied", "write", `{"path":"child-forbidden.txt","content":"must-not-write"}`)
				case 1:
					reply = llmtest.Call("child-exec-denied", "powershell", `{"command":"Write-Output must-not-execute"}`)
				case 2:
					reply = llmtest.Call("child-memory-denied", "remember", `{"text":"must-not-become-memory"}`)
				case 3:
					reply = llmtest.Call("child-read", "read", `{"path":"proof.txt"}`)
				default:
					reply = llmtest.Text("独立结论 made-child-proof-tail")
				}
			} else if n < len(parentReplies) {
				if n == 5 {
					close(parentWaiting)
					select {
					case <-releaseParent:
					case <-r.Context().Done():
						return
					}
				}
				reply = parentReplies[n]
			}
		}
		llmtest.Handler(llmtest.Script{Models: models, Reply: reply}, &e.bodies, &e.mu)(w, r)
	}))
	defer gateway.Close()
	e.llm = gateway
	originalTransport := http.DefaultTransport
	http.DefaultTransport = ecosystemRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.tavily.com" {
			return originalTransport.RoundTrip(r)
		}
		if r.Header.Get("Authorization") != "Bearer invented-key" {
			t.Error("网页凭据未按配置解析")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"results":[{"url":"https://8.8.8.8/fixture","title":"编造资料","content":"made-web-proof"}],"usage":{"credits":1}}`))}, nil
	})
	defer func() { http.DefaultTransport = originalTransport }()
	a := e.open()
	http.DefaultTransport = originalTransport
	d := a.Desktop()
	defer func() {
		release()
		d.Close()
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	}()
	var approvalsMu sync.Mutex
	mcpApprovals := 0
	a.approvalGate.Ask = func(_ context.Context, request approval.Request, _ *approval.Pending) approval.Decision {
		if request.Tool == "mcp_call" {
			approvalsMu.Lock()
			mcpApprovals++
			approvalsMu.Unlock()
		}
		return approval.Decision{Kind: "approved"}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	key := localKey("console:" + testChar)
	started := time.Now()
	receipt, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "先读说明，再查来源并派任务。", Mode: "start", ClientKey: "kernel-roundtrip"})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := d.Reader.localInputScope(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	watch, err := a.hub.WatchRun(scope, receipt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	select {
	case <-parentWaiting:
	case <-ctx.Done():
		t.Fatal("父回合没有到达任务后的边界", ctx.Err())
	}
	access, err := a.authority.Resolve(scope)
	if err != nil {
		t.Fatal(err)
	}
	inv := watch.Snapshot().Identity
	var pending []subagent.Result
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for len(pending) == 0 {
		pending, err = a.ecosystem.Tasks.Take(ctx, access, inv)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 0 {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal("子任务未返回原域邮箱", ctx.Err())
			}
		}
	}
	if len(pending) != 1 || pending[0].Status != "completed" || pending[0].Ref.Owner != scope || !strings.HasPrefix(pending[0].Ref.ToolID, "task-result:") {
		t.Fatalf("子任务结果未正式托管 %+v", pending)
	}
	page, err := a.hub.Cfg.Results.Read(ctx, scope, pending[0].Ref.ID, 0, 15000)
	if err != nil || page.Text != "独立结论 made-child-proof-tail" {
		t.Fatalf("子任务原文不可续读 %+v %v", page, err)
	}
	release()
	for !watch.Snapshot().Drained {
		select {
		case <-watch.Wake():
		case <-ctx.Done():
			t.Fatal("父回合未排空", ctx.Err())
		}
	}
	final := watch.Snapshot()
	if final.Outcome != "done" {
		t.Fatalf("父回合结局 %+v", final)
	}
	if notes, err := a.ecosystem.Tasks.Take(ctx, access, inv); err != nil || len(notes) != 0 {
		t.Fatalf("已持久接纳仍重复投递 %+v %v", notes, err)
	}
	materials, err := a.set.TraceStore().KernelMaterials(ctx, inv)
	if err != nil || len(materials) != 1 || materials[0].Kind != "child" {
		t.Fatalf("父资料没有持久接纳 %+v %v", materials, err)
	}
	e.mu.Lock()
	bodies := append([]string(nil), e.bodies...)
	e.mu.Unlock()
	var parentLast, childLast string
	for _, body := range bodies {
		if strings.Contains(body, `"model":"fixture-child-model"`) {
			childLast = body
		} else {
			parentLast = body
		}
	}
	for _, marker := range []string{"made-mcp-proof", "made-web-proof", "独立结论 made-child-proof-tail"} {
		if !strings.Contains(parentLast, marker) {
			t.Fatalf("父请求缺正式结果 %s", marker)
		}
	}
	if strings.Contains(childLast, "先读说明") || !strings.Contains(childLast, "made-child-proof-tail") {
		t.Fatal("子会话未隔离父历史或未继承工作区")
	}
	var childRequest struct {
		Tools []struct{ Function struct{ Name string } }
	}
	if err := json.Unmarshal([]byte(childLast), &childRequest); err != nil {
		t.Fatal(err)
	}
	for _, tool := range childRequest.Tools {
		switch tool.Function.Name {
		case "write", "edit", "powershell", "memory_search", "recall_raw", "remember":
			t.Fatalf("只读委托曝光越权工具 %s", tool.Function.Name)
		}
	}
	var rejected int
	if err := a.set.Trace().QueryRow(`SELECT count(*) FROM steps WHERE provider_call_id IN ('child-write-denied','child-exec-denied','child-memory-denied') AND is_error=1`).Scan(&rejected); err != nil || rejected != 3 {
		t.Fatalf("真实 child 循环未配对拒绝越权调用：%d %v", rejected, err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "child-forbidden.txt")); !os.IsNotExist(err) {
		t.Fatalf("只读 child 实际写入文件：%v", err)
	}
	approvalsMu.Lock()
	count := mcpApprovals
	approvalsMu.Unlock()
	if count != 1 {
		t.Fatalf("冻结 MCP 目标审批次数 %d", count)
	}
	snapshot, err := a.hub.Cfg.Budget.Snapshot(ctx, inv.RootID)
	if err != nil || snapshot.Requests != int64(len(bodies)) {
		t.Fatalf("子模型未归同一根账本 %+v requests=%d %v", snapshot, len(bodies), err)
	}
	a.authority.Revoke(scope.AuthorityID)
	if _, err := a.hub.Cfg.Results.Read(ctx, scope, pending[0].Ref.ID, 0, 15000); err == nil {
		t.Fatal("撤权后仍能续读来源结果")
	}
	if err := d.Reader.SendLocal(ctx, key, "撤权以后继续普通聊天", nil); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	revokedBody := e.bodies[len(e.bodies)-1]
	e.mu.Unlock()
	var revoked struct {
		Tools []struct{ Function struct{ Name string } }
	}
	if err := json.Unmarshal([]byte(revokedBody), &revoked); err != nil {
		t.Fatal(err)
	}
	for _, tool := range revoked.Tools {
		name := tool.Function.Name
		if name == "skill" || name == "task" || strings.HasPrefix(name, "mcp_") || strings.HasPrefix(name, "web_") || strings.HasPrefix(name, "task_") || name == "result_read" {
			t.Fatal("撤权后仍曝光来源能力", name)
		}
	}
	t.Logf("实际 App/Hub：parent=%d child=%d root_requests=%d result_bytes=%d mcp_approvals=%d elapsed_ms=%d", parentCalls, childCalls, snapshot.Requests, pending[0].Ref.Bytes, count, time.Since(started).Milliseconds())
}

func TestKernelChildDrainsAfterParentGeneration(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	const childModel = "fixture-draining-child"
	childEntered, childCancelled, releaseChild := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseChild) })
	defer release()
	var routeMu sync.Mutex
	parentCalls := 0
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`), json.RawMessage(`{"id":"` + childModel + `","context_length":32768}`)}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := llmtest.Text("父生成已结束。")
		if r.Method == http.MethodPost {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
			var request struct{ Model string }
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Error(err)
				return
			}
			if request.Model == childModel {
				close(childEntered)
				select {
				case <-releaseChild:
					reply = llmtest.Text("made-late-child-complete")
				case <-r.Context().Done():
					close(childCancelled)
					return
				}
			} else {
				routeMu.Lock()
				n := parentCalls
				parentCalls++
				routeMu.Unlock()
				if n == 0 {
					reply = llmtest.Call("task-call", "task", `{"agent":"reader","description":"编造慢任务","prompt":"给出编造结论"}`)
				}
			}
		}
		llmtest.Handler(llmtest.Script{Models: models, Reply: reply}, &e.bodies, &e.mu)(w, r)
	}))
	defer gateway.Close()
	e.llm = gateway
	e.configure("subagents:\n  enabled: true\n  model: " + childModel + "\ntools:\n  workdir: '" + filepath.ToSlash(t.TempDir()) + "'\n")
	a := e.open()
	a.approvalGate.Ask = func(_ context.Context, request approval.Request, _ *approval.Pending) approval.Decision {
		if request.Tool == "task" {
			return approval.Decision{Kind: "approved"}
		}
		return approval.Decision{Kind: "denied"}
	}
	d := a.Desktop()
	defer func() {
		release()
		d.Close()
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	key := localKey("console:" + testChar)
	receipt, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "派一个编造的慢任务。", Mode: "start", ClientKey: "late-child"})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := d.Reader.localInputScope(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	watch, err := a.hub.WatchRun(scope, receipt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	select {
	case <-childEntered:
	case <-ctx.Done():
		e.mu.Lock()
		for _, body := range e.bodies {
			var request struct {
				Messages []struct {
					Role    string
					Content json.RawMessage
				}
			}
			if err := json.Unmarshal([]byte(body), &request); err != nil {
				t.Logf("解码诊断请求：%v", err)
				continue
			}
			for _, message := range request.Messages {
				if message.Role == "tool" {
					t.Logf("任务创建回执：%s", message.Content)
				}
			}
		}
		e.mu.Unlock()
		t.Fatal("子任务未进入模型", ctx.Err())
	}
	for watch.Snapshot().State != "draining" {
		if watch.Snapshot().Drained {
			t.Fatal("子任务仍活着却已报告排空")
		}
		select {
		case <-watch.Wake():
		case <-ctx.Done():
			t.Fatal("父生成未转为 draining", ctx.Err())
		}
	}
	select {
	case <-childCancelled:
		t.Fatal("父生成结束提前取消子任务")
	default:
	}
	release()
	for !watch.Snapshot().Drained {
		select {
		case <-watch.Wake():
		case <-ctx.Done():
			t.Fatal("子任务结束后没有排空", ctx.Err())
		}
	}
	access, err := a.authority.Resolve(scope)
	if err != nil {
		t.Fatal(err)
	}
	results, err := a.ecosystem.Tasks.Take(ctx, access, watch.Snapshot().Identity)
	if err != nil || len(results) != 1 || results[0].Status != "completed" || results[0].Text != "made-late-child-complete" {
		t.Fatalf("子任务实际完成未归原域 %+v %v", results, err)
	}
	t.Log("父生成 done→draining 时子模型仍在执行；子模型实际完成并写原域结果后才 Drained")
}
