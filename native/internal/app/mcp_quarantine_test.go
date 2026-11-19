package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sumika-native/internal/audience"
	"sumika-native/internal/mcpclient"
	"sumika-native/internal/mcpplugin"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
)

// mcpFixtureConfig 起一个编造的远端 MCP 服务，写进临时数据目录的 mcp.json，返回服务名
func mcpFixtureConfig(t *testing.T, e *testEnv) string {
	t.Helper()
	remote := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	remote.AddTool(&mcp.Tool{Name: "search", Description: "搜索编造的文档", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remote }, nil))
	t.Cleanup(srv.Close)
	cfg := `{"sumika":{"enabled":true},"mcpServers":{"docs":{"type":"http","url":"` + srv.URL + `","sumika":{"enabled":true,"include":["search"]}}}}`
	if err := os.MkdirAll(filepath.Join(e.data, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.data, "config", "mcp.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return "docs"
}

// 隔离的完整路线：这次调用没拿到结果 → 重启也不解除、提醒里写清怎么解除 → 主人带原因解除 → 服务补连上、提醒撤掉、留痕保留。
func TestMCPQuarantineSurvivesRestartAndReleaseReopens(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := mcpFixtureConfig(t, e)
	a := e.open()
	if err := a.mcpStore().MCPDispatch(t.Context(), "old-call", id, "binding", store.MCPTransportHTTP); err != nil {
		t.Fatal(err)
	}
	if err := a.mcpStore().MCPEnd(t.Context(), "old-call", false, store.MCPCauseTimeout); err != nil {
		t.Fatal(err)
	}
	a.cleanup()

	a = e.open()
	defer a.cleanup()
	al, ok := openAlert(a, "mcp:"+id)
	if !ok || al.Title != mcpAlertTitle(id) || !strings.Contains(al.Body, "sumika-ctl mcp release "+id) || !strings.Contains(al.Body, "--accept-unknown-risk") {
		t.Fatalf("启动时被隔离要有提醒，并写清怎么解除：%+v", al)
	}
	st, err := a.MCPStatus(t.Context())
	if err != nil || len(st.Servers) != 1 || !st.Servers[0].Isolated || st.Servers[0].Connected || st.Servers[0].Calls[0].Cause != store.MCPCauseTimeout {
		t.Fatalf("状态不对：%+v %v", st, err)
	}
	iso, err := a.mcpSelfcheck(a.mcpStore().DB)(t.Context())
	if err != nil || len(iso) != 1 || iso[0].Server != id || iso[0].Cause != store.MCPCauseTimeout || iso[0].Since.IsZero() {
		t.Fatalf("自检要看到被隔离的服务、原因和时间：%+v %v", iso, err)
	}
	if _, err = a.MCPReconnect(t.Context(), id); err == nil || !strings.Contains(err.Error(), "被隔离") {
		t.Fatalf("重连不能解除隔离：%v", err)
	}
	if st, _ = a.MCPStatus(t.Context()); !st.Servers[0].Isolated || st.Servers[0].Connected {
		t.Fatalf("被拒的重连改了状态：%+v", st)
	}
	if _, err = a.MCPRelease(t.Context(), id, "   "); err == nil {
		t.Fatal("没写原因也解除了")
	}
	if _, ok := openAlert(a, "mcp:"+id); !ok {
		t.Fatal("被拒的解除撤了提醒")
	}
	out, err := a.MCPRelease(t.Context(), id, "远端文档服务那次是我手动停的")
	if err != nil || out.Released != 1 || !out.Reopened {
		t.Fatalf("%+v %v", out, err)
	}
	if _, ok := openAlert(a, "mcp:"+id); ok {
		t.Fatal("解除并连上以后提醒还在")
	}
	if iso, err = a.mcpSelfcheck(a.mcpStore().DB)(t.Context()); err != nil || len(iso) != 0 {
		t.Fatalf("解除后自检应恢复正常：%+v %v", iso, err)
	}
	st, _ = a.MCPStatus(t.Context())
	if st.Servers[0].Isolated || !st.Servers[0].Connected {
		t.Fatalf("解除后应已连上：%+v", st)
	}
	var state, raw string
	if err := a.mcpStore().DB.QueryRowContext(t.Context(), `SELECT state,reason FROM kernel_mcp_calls WHERE id='old-call'`).Scan(&state, &raw); err != nil {
		t.Fatal(err)
	}
	if state != store.MCPStateRiskAccepted || !strings.Contains(raw, "远端文档服务那次是我手动停的") || !strings.Contains(raw, "命令行 sumika-ctl") || !strings.Contains(raw, `"cause":"timeout"`) {
		t.Fatalf("解除没留痕或抹掉了旧记录：%s %s", state, raw)
	}
}

// 解除入口不在模型能调的工具里：MCP 的元工具是固定的四个，所有登记的工具名、策略表里都没有解除、接受风险一类的名字。
func TestMCPReleaseIsNotAModelTool(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	mcpFixtureConfig(t, e)
	a := e.open()
	defer a.cleanup()
	meta := map[string]bool{}
	for _, d := range mcpplugin.Declarations {
		meta[d.Name] = true
	}
	if len(meta) != 4 || !meta["mcp_search"] || !meta["mcp_call"] || !meta["mcp_result_read"] || !meta["mcp_cancel"] {
		t.Fatalf("MCP 元工具清单变了，要有意识地确认新工具不能解除隔离：%v", meta)
	}
	names := map[string]bool{}
	for name := range policy.Specs {
		names[name] = true
	}
	// 每个场合都看一遍：本地私聊、本地群聊、QQ 私聊、QQ 群。MCP 的调用入口只在本地私聊（主人）开放，这次没有改变它
	for _, c := range []struct {
		aud     audience.Audience
		hasCall bool
	}{
		{audience.Audience{Channel: "console", Conversation: "internal:probe", Scene: "private", PrivateOK: true, OwnerAsked: true}, true},
		{audience.Audience{Channel: "console", Conversation: "local:group:x", Scene: "group", PrivateOK: true}, false},
		{audience.Audience{Channel: "qq", Conversation: "qq:private:10001", Scene: "private", PrivateOK: true, OwnerAsked: true}, false},
		{audience.Audience{Channel: "qq", Conversation: "qq:group:10001", Scene: "group"}, false},
	} {
		aud := c.aud
		got := a.hub.ToolNames(aud)
		hasCall := false
		for _, n := range got {
			names[n] = true
			hasCall = hasCall || n == "mcp_call"
		}
		if hasCall != c.hasCall {
			t.Errorf("场合 %s/%s：mcp_call 可用应为 %v，得到 %v", aud.Channel, aud.Conversation, c.hasCall, hasCall)
		}
	}
	for n := range names {
		l := strings.ToLower(n)
		for _, word := range []string{"release", "quarantine", "unquarantine", "accept", "risk", "unblock", "isolat"} {
			if strings.Contains(l, word) {
				t.Errorf("工具 %q 看起来能解除隔离：这个入口只能在主人的本地管理入口", n)
			}
		}
	}
}

// TestMCPAppStdioHelper 是被当成本机 MCP 服务起的那个子进程（SUMIKA_APP_MCP_HELPER=1 才工作）。
func TestMCPAppStdioHelper(t *testing.T) {
	if os.Getenv("SUMIKA_APP_MCP_HELPER") != "1" {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "search", Description: "搜索编造的文档", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

// 启动收尾：上一个进程被杀时留下的“已派发”，本机服务收尾成已终止、不再挡（并如实记原因），远端服务改成结果未知、继续挡着；
// 这次启动里本机服务连得上、远端服务连不上并有提醒。
func TestMCPStartupRecoversLocalButKeepsRemoteIsolated(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	remote := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	remote.AddTool(&mcp.Tool{Name: "search", Description: "搜索编造的文档", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remote }, nil))
	t.Cleanup(srv.Close)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	cfg := map[string]any{"sumika": map[string]any{"enabled": true}, "mcpServers": map[string]any{
		"far":  map[string]any{"type": "http", "url": srv.URL, "sumika": map[string]any{"enabled": true, "include": []string{"search"}}},
		"near": map[string]any{"command": exe, "args": []string{"-test.run=^TestMCPAppStdioHelper$"}, "cwd": work, "env": map[string]string{"SUMIKA_APP_MCP_HELPER": "1", "TEMP": work, "TMP": work}, "sumika": map[string]any{"enabled": true, "include": []string{"search"}}},
	}}
	raw, _ := json.Marshal(cfg)
	if err := os.MkdirAll(filepath.Join(e.data, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.data, "config", "mcp.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	a := e.open()
	// 上一个进程（不是这个测试进程）派发后被杀：造两行
	for _, c := range []struct{ id, server, transport string }{{"dead-near", "near", store.MCPTransportStdio}, {"dead-far", "far", store.MCPTransportHTTP}} {
		note := store.MCPNote{Transport: c.transport, Process: "killed-process", DispatchedAt: 1}
		b, _ := json.Marshal(note)
		if _, err := a.mcpStore().DB.ExecContext(t.Context(), `INSERT INTO kernel_mcp_calls(id,server_id,state,binding,reason) VALUES(?,?,'dispatched','b',?)`, c.id, c.server, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	a.cleanup()

	a = e.open()
	defer a.cleanup()
	state := func(id string) (string, store.MCPNote) {
		var st, raw string
		if err := a.mcpStore().DB.QueryRowContext(t.Context(), `SELECT state,reason FROM kernel_mcp_calls WHERE id=?`, id).Scan(&st, &raw); err != nil {
			t.Fatal(err)
		}
		var n store.MCPNote
		_ = json.Unmarshal([]byte(raw), &n)
		return st, n
	}
	wantNear := store.MCPStateTerminated
	if !mcpclient.ChildrenDieWithHost {
		wantNear = store.MCPStateUnknown // 这个平台不保证子进程随宿主退出：保持隔离
	}
	if st, n := state("dead-near"); st != wantNear || n.Cause != store.MCPCauseHostExit || n.EndedAt == 0 {
		t.Fatalf("本机服务遗留的调用：%s %+v，应为 %s 且原因是程序退出", st, n, wantNear)
	}
	if st, n := state("dead-far"); st != store.MCPStateUnknown || n.Cause != store.MCPCauseHostExit {
		t.Fatalf("远端服务遗留的调用必须保持隔离：%s %+v", st, n)
	}
	status, err := a.MCPStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]bool{}
	connected := map[string]bool{}
	for _, s := range status.Servers {
		byID[s.ID], connected[s.ID] = s.Isolated, s.Connected
	}
	if mcpclient.ChildrenDieWithHost && (byID["near"] || !connected["near"]) {
		t.Fatalf("本机服务收尾后应不被挡且已连上：%+v", status)
	}
	if !byID["far"] || connected["far"] {
		t.Fatalf("远端服务应仍被隔离、没连上：%+v", status)
	}
	if _, ok := openAlert(a, "mcp:far"); !ok {
		t.Fatal("远端服务被隔离要有提醒")
	}
	if _, ok := openAlert(a, "mcp:near"); ok && mcpclient.ChildrenDieWithHost {
		t.Fatal("本机服务不该有提醒")
	}
}

// 结局没写进库、行停在“已派发”：在途窗口过后，自检那一轮补上提醒；旧版本留下的记录（没记传输种类）要在状态和提醒里说明。
func TestMCPStaleDispatchedGetsAlertAndLegacyRowIsExplained(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := mcpFixtureConfig(t, e)
	a := e.open()
	defer a.cleanup()
	db := a.mcpStore().DB
	stale := store.MCPNote{Transport: store.MCPTransportHTTP, Process: store.ProcessID(), DispatchedAt: a.now().Add(-20 * time.Minute).UnixMilli()}
	b, _ := json.Marshal(stale)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO kernel_mcp_calls(id,server_id,state,binding,reason) VALUES('stale',?,'dispatched','b',?)`, id, string(b)); err != nil {
		t.Fatal(err)
	}
	fresh := store.MCPNote{Transport: store.MCPTransportHTTP, Process: store.ProcessID(), DispatchedAt: a.now().Add(-1 * time.Minute).UnixMilli()}
	b, _ = json.Marshal(fresh)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO kernel_mcp_calls(id,server_id,state,binding,reason) VALUES('fresh','other','dispatched','b',?)`, string(b)); err != nil {
		t.Fatal(err)
	}
	if _, ok := openAlert(a, "mcp:"+id); ok {
		t.Fatal("还没自检就有提醒了")
	}
	iso, err := a.mcpSelfcheck(db)(t.Context())
	if err != nil || len(iso) != 1 || iso[0].Server != id {
		t.Fatalf("超过在途窗口的算隔离，窗口内的不算：%+v %v", iso, err)
	}
	if _, ok := openAlert(a, "mcp:"+id); !ok {
		t.Fatal("自检那一轮要把提醒补上")
	}
	if _, ok := openAlert(a, "mcp:other"); ok {
		t.Fatal("在途的调用不该有提醒")
	}
	// 旧版本留下的行：没有备注
	if _, err := db.ExecContext(t.Context(), `INSERT INTO kernel_mcp_calls(id,server_id,state,binding) VALUES('old-version',?,'unknown','b')`, id); err != nil {
		t.Fatal(err)
	}
	a.refreshMCPAlert(id)
	al, _ := openAlert(a, "mcp:"+id)
	if !strings.Contains(al.Body, "旧版本留下的记录") {
		t.Fatalf("提醒里要说明旧版本留下的记录：%s", al.Body)
	}
	st, err := a.MCPStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	legacy := 0
	for _, c := range st.Servers[0].Calls {
		if c.Legacy {
			legacy++
		}
	}
	if legacy != 1 {
		t.Fatalf("状态里要标出没记传输种类的旧记录：%+v", st.Servers[0].Calls)
	}
}
