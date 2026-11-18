package ctlserver

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

type mcpFake struct {
	fakeBackend
	released []string
}

func (f *mcpFake) MCPStatus(context.Context) (ctlproto.MCPStatus, error) {
	f.rec("mcp-status")
	return ctlproto.MCPStatus{Servers: []ctlproto.MCPServer{{ID: "docs\n坏", Isolated: true, Calls: []ctlproto.MCPCall{{ID: "ab\x1b12", State: "unknown", Cause: "timeout"}}}}}, nil
}

func (f *mcpFake) MCPRelease(_ context.Context, server, reason string) (ctlproto.MCPReleased, error) {
	f.rec("mcp-release")
	f.released = append(f.released, server+"|"+reason)
	return ctlproto.MCPReleased{Server: server, Released: 1}, nil
}

func (f *mcpFake) MCPReconnect(_ context.Context, server string) (ctlproto.MCPReconnected, error) {
	f.rec("mcp-reconnect")
	return ctlproto.MCPReconnected{Server: server, Connected: true}, nil
}

func TestMCPStatusIsReadOnlyAndCleansText(t *testing.T) {
	f := &mcpFake{}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, ctlproto.Request{Cmd: "mcp", Args: []string{"status"}})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	v := dataOf[ctlproto.MCPStatus](t, r)
	if strings.ContainsAny(v.Servers[0].ID+v.Servers[0].Calls[0].ID, "\n\x1b") {
		t.Fatalf("控制字符没清掉：%+v", v)
	}
	if len(logs) != 0 {
		t.Fatalf("status 只读，不该写改状态的日志：%v", logs)
	}
}

// 解除隔离的形状 × 入口：缺原因、缺“接受风险”、服务名不对、多余参数、别处带了这些字段，一律 invalid、一个后端函数都不调。
func TestMCPReleaseShapes(t *testing.T) {
	ok := &ctlproto.MCPArgs{Reason: "核对过，旧调用已结束", AcceptRisk: true}
	for name, req := range map[string]ctlproto.Request{
		"没有原因":           {Cmd: "mcp", Args: []string{"release", "docs"}, MCP: &ctlproto.MCPArgs{AcceptRisk: true}},
		"原因全是空白和控制字符":    {Cmd: "mcp", Args: []string{"release", "docs"}, MCP: &ctlproto.MCPArgs{Reason: " \n\t ", AcceptRisk: true}},
		"原因太长":           {Cmd: "mcp", Args: []string{"release", "docs"}, MCP: &ctlproto.MCPArgs{Reason: strings.Repeat("字", ctlproto.MCPMaxReason+1), AcceptRisk: true}},
		"没有明确接受风险":       {Cmd: "mcp", Args: []string{"release", "docs"}, MCP: &ctlproto.MCPArgs{Reason: "有原因"}},
		"根本没有 mcp 字段":    {Cmd: "mcp", Args: []string{"release", "docs"}},
		"没写服务":           {Cmd: "mcp", Args: []string{"release"}, MCP: ok},
		"服务名带路径":         {Cmd: "mcp", Args: []string{"release", "../docs"}, MCP: ok},
		"服务名带换行":         {Cmd: "mcp", Args: []string{"release", "docs\nx"}, MCP: ok},
		"服务名为空":          {Cmd: "mcp", Args: []string{"release", ""}, MCP: ok},
		"多余参数":           {Cmd: "mcp", Args: []string{"release", "docs", "more"}, MCP: ok},
		"不认识的子命令":        {Cmd: "mcp", Args: []string{"unquarantine", "docs"}, MCP: ok},
		"没有子命令":          {Cmd: "mcp"},
		"status 带参数":     {Cmd: "mcp", Args: []string{"status", "docs"}},
		"status 带原因":     {Cmd: "mcp", Args: []string{"status"}, MCP: ok},
		"reconnect 带原因":  {Cmd: "mcp", Args: []string{"reconnect", "docs"}, MCP: ok},
		"reconnect 没写服务": {Cmd: "mcp", Args: []string{"reconnect"}},
		"别的命令带原因":        {Cmd: "backup", Args: []string{"list"}, MCP: ok},
	} {
		f := &mcpFake{}
		n, _ := start(t, f, Options{})
		if r := ask(t, n, req); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Errorf("%s：该 invalid，得到 %+v", name, r)
		}
		if got := f.got(); len(got) != 0 {
			t.Errorf("%s：被拒的请求调到了后端：%v", name, got)
		}
	}
}

func TestMCPReleaseForwardsCleanedReasonAndLogsNoReason(t *testing.T) {
	f := &mcpFake{}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, ctlproto.Request{Cmd: "mcp", Args: []string{"release", "docs"}, MCP: &ctlproto.MCPArgs{Reason: "  旧调用\n已结束 ", AcceptRisk: true}})
	if !r.OK || len(f.released) != 1 || f.released[0] != "docs|旧调用 已结束" {
		t.Fatalf("%+v %v", r, f.released)
	}
	if len(logs) != 1 || strings.Contains(logs[0], "旧调用") {
		t.Fatalf("解除要记一行日志，但不带原文：%v", logs)
	}
}

func TestMCPDemoRefusesRelease(t *testing.T) {
	f := &mcpFake{}
	name, _ := start(t, f, Options{Demo: true})
	r := ask(t, name, ctlproto.Request{Cmd: "mcp", Args: []string{"release", "docs"}, MCP: &ctlproto.MCPArgs{Reason: "x", AcceptRisk: true}})
	if r.OK || r.Code != ctlproto.CodeDemo || len(f.got()) != 0 {
		t.Fatalf("%+v %v", r, f.got())
	}
}

// 后端不实现 MCPBackend（比如别的宿主）时回“不可用”，不是崩溃
func TestMCPWithoutBackendExtension(t *testing.T) {
	f := &fakeBackend{}
	name, _ := start(t, f, Options{})
	r := ask(t, name, ctlproto.Request{Cmd: "mcp", Args: []string{"status"}})
	if r.OK || r.Code != ctlproto.CodeFailed {
		t.Fatalf("%+v", r)
	}
}
