package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/store"
)

// TestMCPStdioFlakyHelper 是被当成本机服务起的子进程（SUMIKA_K4_MCP_HELPER=flaky 才工作）：
//   - SUMIKA_HELPER_LOG：每次启动、每次调用往这个文件追加一行（不碰 stdout，那是协议通道）；
//   - SUMIKA_HELPER_ONCE：这个文件已存在就立刻退出（只让第一次启动成功，之后的重连都失败）；
//   - work 的 mode：crash 直接退出进程，hang 挂着直到被取消，exitafter 先回结果、过一会儿自己退出（模拟空闲时进程自己没了），其余马上返回。
func TestMCPStdioFlakyHelper(t *testing.T) {
	if os.Getenv("SUMIKA_K4_MCP_HELPER") != "flaky" {
		return
	}
	logLine := func(line string) {
		if path := os.Getenv("SUMIKA_HELPER_LOG"); path != "" {
			if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				f.WriteString(line + "\n")
				f.Close()
			}
		}
	}
	logLine("start")
	if once := os.Getenv("SUMIKA_HELPER_ONCE"); once != "" {
		if _, err := os.Stat(once); err == nil {
			os.Exit(4)
		}
		os.WriteFile(once, []byte("x"), 0o600)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "work", Description: "fixture work", InputSchema: json.RawMessage(`{"type":"object","properties":{"mode":{"type":"string"}},"additionalProperties":false}`)}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct{ Mode string }
		_ = json.Unmarshal(r.Params.Arguments, &args)
		logLine("call:" + args.Mode)
		switch args.Mode {
		case "crash":
			os.Exit(3)
		case "hang":
			<-ctx.Done()
			return nil, ctx.Err()
		case "exitafter":
			time.AfterFunc(30*time.Millisecond, func() { os.Exit(5) })
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

type stdioFixture struct {
	client  *Client
	journal store.EcosystemStore
	access  ecosystem.Access
	cfg     ServerConfig
	logPath string
	once    string
	changes chan string
}

// newStdioFixture 连上一个本机假服务；重连间隔缩到 1 毫秒，次数仍是 5
func newStdioFixture(t *testing.T, callTimeoutMs int, onlyFirstStart bool) *stdioFixture {
	t.Helper()
	return newStdioFixtureWith(t, callTimeoutMs, onlyFirstStart, func(j store.EcosystemStore) Journal { return j })
}

func newStdioFixtureWith(t *testing.T, callTimeoutMs int, onlyFirstStart bool, wrap func(store.EcosystemStore) Journal) *stdioFixture {
	t.Helper()
	oldDelays, oldStable := recoverDelays, stableAfter
	recoverDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	stableAfter = time.Hour
	t.Cleanup(func() { recoverDelays, stableAfter = oldDelays, oldStable })
	f := &stdioFixture{journal: mcpFixtureStore(t), changes: make(chan string, 256)}
	_, f.access = mcpAccess(t, "mcp:near")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	f.logPath = work + string(os.PathSeparator) + "helper.log"
	env := map[string]string{"SUMIKA_K4_MCP_HELPER": "flaky", "SUMIKA_HELPER_LOG": f.logPath, "TEMP": work, "TMP": work}
	if onlyFirstStart {
		f.once = work + string(os.PathSeparator) + "once"
		env["SUMIKA_HELPER_ONCE"] = f.once
	}
	f.cfg = ServerConfig{ID: "near", Command: exe, Args: []string{"-test.run=^TestMCPStdioFlakyHelper$"}, CWD: work, Env: env, Sumika: ServerPolicy{Enabled: true, Include: []string{"work"}, CallTimeoutMs: callTimeoutMs}}
	f.client = New(wrap(f.journal))
	t.Cleanup(func() {
		// 带时限地关：万一哪条路持锁卡死，不能把整个测试包拖住（断言已经报了）
		done := make(chan struct{})
		go func() { f.client.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("Client.Close 20 秒没返回（疑似死锁）")
		}
	})
	f.client.SetOnChange(func(id string) { f.changes <- id })
	if err := f.client.Connect(t.Context(), f.cfg); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *stdioFixture) resolved(t *testing.T, mode string) *ResolvedCall {
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

func (f *stdioFixture) health() Health {
	for _, h := range f.client.Health() {
		if h.ID == "near" {
			return h
		}
	}
	return Health{}
}

// waitFor 等宿主收到状态变化通知，每次检查条件；通知是事件，不是固定等待。
func (f *stdioFixture) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(60 * time.Second)
	for !cond() {
		select {
		case <-f.changes:
		case <-deadline:
			t.Fatalf("等不到：%s（状态 %+v）", what, f.health())
		}
	}
}

func (f *stdioFixture) helperLog(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(f.logPath)
	return strings.Fields(string(b))
}

func (f *stdioFixture) state(t *testing.T) (blocked bool) {
	t.Helper()
	blocked, err := f.journal.MCPBlocked(t.Context(), "near")
	if err != nil {
		t.Fatal(err)
	}
	return blocked
}

// 子进程在调用中途崩溃：这次调用失败并记成已终止（不挡）；服务自动重连；下一次调用成功；崩溃那次调用不会被重放。
func TestStdioCrashMidCallReconnectsWithoutReplay(t *testing.T) {
	f := newStdioFixture(t, 60000, false)
	old := f.resolved(t, "crash")
	if _, err := old.Execute(t.Context()); err == nil {
		t.Fatal("崩溃的调用被当成成功")
	}
	if f.state(t) {
		t.Fatal("本机进程树已确认退出，不该被隔离")
	}
	f.waitFor(t, "自动重连成功", func() bool { return f.health().Connected })
	if old.Valid() {
		t.Fatal("重连后旧的冻结调用仍有效")
	}
	if _, err := old.Execute(t.Context()); err == nil {
		t.Fatal("重连后旧调用被重放")
	}
	if _, err := f.resolved(t, "ok").Execute(t.Context()); err != nil {
		t.Fatalf("重连后调用失败：%v", err)
	}
	if got := strings.Join(f.helperLog(t), ","); got != "start,call:crash,start,call:ok" {
		t.Fatalf("崩溃那次调用只该到一次、重连起了一个新进程：%s", got)
	}
}

// 调用超时：本机服务同样记已终止、重连，之后能再调。
func TestStdioTimeoutReconnects(t *testing.T) {
	f := newStdioFixture(t, 300, false)
	if _, err := f.resolved(t, "hang").Execute(t.Context()); err == nil {
		t.Fatal("超时被当成成功")
	}
	f.waitFor(t, "自动重连成功", func() bool { return f.health().Connected })
	if _, err := f.resolved(t, "ok").Execute(t.Context()); err != nil {
		t.Fatalf("超时后重连的服务调不了：%v", err)
	}
}

// 没有调用时进程退出、管道断：下一次调用前已经重连好。
func TestStdioIdleExitReconnects(t *testing.T) {
	f := newStdioFixture(t, 60000, false)
	f.client.mu.Lock()
	session := f.client.servers["near"].session
	f.client.mu.Unlock()
	if err := session.Close(); err != nil {
		t.Logf("关连接：%v", err)
	}
	f.waitFor(t, "断开后自动重连", func() bool {
		n := 0
		for _, l := range f.helperLog(t) {
			if l == "start" {
				n++
			}
		}
		return n == 2 && f.health().Connected
	})
	if _, err := f.resolved(t, "ok").Execute(t.Context()); err != nil {
		t.Fatalf("断开重连后调用失败：%v", err)
	}
}

// 重连次数有上限：用完停在 exhausted，说明原因，不再起进程；主人显式连接（服务修好后）清零、恢复。
func TestStdioReconnectBudgetThenOwnerReconnect(t *testing.T) {
	f := newStdioFixture(t, 60000, true)
	if _, err := f.resolved(t, "crash").Execute(t.Context()); err == nil {
		t.Fatal("崩溃的调用被当成成功")
	}
	f.waitFor(t, "重连次数用完", func() bool { return f.health().Reconnect == "exhausted" })
	h := f.health()
	if h.Connected || !strings.Contains(h.Note, "5") {
		t.Fatalf("用完次数后应说明原因：%+v", h)
	}
	starts := 0
	for _, l := range f.helperLog(t) {
		if l == "start" {
			starts++
		}
	}
	if starts != 1+5 {
		t.Fatalf("应是 1 次正常启动加 5 次重连：%v", f.helperLog(t))
	}
	// 服务修好了（这里是删掉“只让第一次成功”的标记），主人显式重连
	if err := os.Remove(f.once); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Connect(t.Context(), f.cfg); err != nil {
		t.Fatal(err)
	}
	if h = f.health(); !h.Connected || h.Reconnect != "" {
		t.Fatalf("显式重连后状态没清：%+v", h)
	}
}

// 被隔离的服务不自动重连：库里有结果未知的旧调用时，连接断了也不起新进程。
func TestStdioQuarantinedServerIsNotAutoReconnected(t *testing.T) {
	f := newStdioFixture(t, 60000, false)
	if err := f.journal.MCPDispatch(t.Context(), "stale", "near", "b", store.MCPTransportStdio); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.MCPEnd(t.Context(), "stale", false, store.MCPCauseError); err != nil {
		t.Fatal(err)
	}
	f.client.mu.Lock()
	session := f.client.servers["near"].session
	f.client.mu.Unlock()
	session.Close()
	f.waitFor(t, "断开被发现", func() bool { return !f.health().Connected })
	f.client.recoverWG.Wait() // 重连循环（如果有）跑完
	f.waitFor(t, "重连循环结束", func() bool { return f.health().Reconnect == "" })
	if n := len(f.helperLog(t)); n != 1 {
		t.Fatalf("被隔离的服务又被拉起了：%v", f.helperLog(t))
	}
	if err := f.client.Connect(t.Context(), f.cfg); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("显式连接也不能绕过隔离：%v", err)
	}
	// 主人带原因解除、再连接：恢复
	if _, err := f.client.Release(t.Context(), "near", "主人（测试）", "那次调用我确认过没有副作用"); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Connect(t.Context(), f.cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := f.resolved(t, "ok").Execute(t.Context()); err != nil {
		t.Fatalf("解除并重连后调用失败：%v", err)
	}
}

// 主人显式停用时，自动重连不会把它又拉起来。
func TestStdioDisableCancelsReconnect(t *testing.T) {
	f := newStdioFixture(t, 60000, false)
	f.client.mu.Lock()
	session := f.client.servers["near"].session
	f.client.mu.Unlock()
	if err := f.client.Disable("near"); err != nil {
		t.Fatal(err)
	}
	session.Close()
	f.client.recoverWG.Wait() // 监视协程和可能的重连循环都已退出
	if h := f.health(); h.Connected || h.Reconnect != "" {
		t.Fatalf("停用后不该有重连：%+v", h)
	}
	if n := len(f.helperLog(t)); n != 1 {
		t.Fatalf("停用后又起了进程：%v", f.helperLog(t))
	}
}

// 本机服务调用失败后，“已终止”没能写进库：这一行仍是结果未知，服务继续被挡，也不自动重连（刚因真实故障隔离的服务不能悄悄恢复）。
func TestStdioTerminatedWriteFailureStaysIsolatedAndNotReconnected(t *testing.T) {
	f := newStdioFixtureWith(t, 60000, false, func(j store.EcosystemStore) Journal { return failingJournal{EcosystemStore: j, failTerminated: true} })
	if _, err := f.resolved(t, "crash").Execute(t.Context()); err == nil {
		t.Fatal("崩溃的调用被当成成功")
	}
	f.client.recoverWG.Wait() // 如果错误地起了重连循环，等它跑完
	if !f.state(t) {
		t.Fatal("已终止没写进库，服务却不挡了")
	}
	if h := f.health(); h.Connected || h.Reconnect != "" {
		t.Fatalf("不该重连：%+v", h)
	}
	if got := strings.Join(f.helperLog(t), ","); got != "start,call:crash" {
		t.Fatalf("不该又起进程：%s", got)
	}
	if err := f.client.Connect(t.Context(), f.cfg); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("显式连接也不能绕过：%v", err)
	}
}

// 重连成功后很快又断（没稳定运行满窗口）：预算不清零，五次重连用完以后停在“次数用完”，不会无限起进程。
func TestStdioFlappingServerExhaustsBudgetAcrossReconnects(t *testing.T) {
	f := newStdioFixture(t, 60000, false)
	for i := 1; i <= len(recoverDelays); i++ {
		if _, err := f.resolved(t, "crash").Execute(t.Context()); err == nil {
			t.Fatal("崩溃的调用被当成成功")
		}
		f.waitFor(t, "自动重连成功", func() bool { return f.health().Connected })
	}
	if _, err := f.resolved(t, "crash").Execute(t.Context()); err == nil {
		t.Fatal("崩溃的调用被当成成功")
	}
	f.waitFor(t, "第六次崩溃后次数用完", func() bool { return f.health().Reconnect == "exhausted" })
	starts := 0
	for _, l := range f.helperLog(t) {
		if l == "start" {
			starts++
		}
	}
	if starts != 1+len(recoverDelays) {
		t.Fatalf("应是 1 次正常启动加 %d 次重连：%v", len(recoverDelays), f.helperLog(t))
	}
	if f.state(t) {
		t.Fatal("本机进程树都已确认退出，不该被隔离；只是重连次数用完")
	}
}

// 调用成功返回、过一会儿子进程自己退出（真的退出，不是测试里关连接）：监视协程发现，自动重连，下一次调用成功。
func TestStdioProcessExitsByItselfReconnects(t *testing.T) {
	f := newStdioFixture(t, 60000, false)
	if _, err := f.resolved(t, "exitafter").Execute(t.Context()); err != nil {
		t.Fatalf("先回结果的调用失败：%v", err)
	}
	f.waitFor(t, "进程自己退出后重连", func() bool {
		n := 0
		for _, l := range f.helperLog(t) {
			if l == "start" {
				n++
			}
		}
		return n == 2 && f.health().Connected
	})
	if _, err := f.resolved(t, "ok").Execute(t.Context()); err != nil {
		t.Fatalf("重连后调用失败：%v", err)
	}
	if f.state(t) {
		t.Fatal("进程自己退出不该留下隔离")
	}
}
