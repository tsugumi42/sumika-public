package selfcheck_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

// S11：没接 → 无法判定；没有被隔离的 → ok；有 → bad，例子里有服务名、原因、时间，备注写怎么解除；读失败 → 无法判定（不记成 ok）
func TestMCPQuarantinedCheck(t *testing.T) {
	c := findCheck(t, "mcp.quarantined", selfcheck.DefaultThresholds())
	eval := func(f func(context.Context) ([]selfcheck.MCPIsolation, error)) selfcheck.Result {
		env := envAt(t0, selfcheck.NewCounters())
		env.MCP = f
		return c.Eval(context.Background(), env)
	}
	if r := eval(nil); r.State != selfcheck.Unknown {
		t.Fatalf("没接 MCP 状态：%+v", r)
	}
	if r := eval(func(context.Context) ([]selfcheck.MCPIsolation, error) { return nil, nil }); r.State != selfcheck.Ok {
		t.Fatalf("没有被隔离的：%+v", r)
	}
	if r := eval(func(context.Context) ([]selfcheck.MCPIsolation, error) { return nil, errors.New("database is locked") }); r.State != selfcheck.Unknown {
		t.Fatalf("读失败不能记成 ok：%+v", r)
	}
	since := t0.Add(-3 * time.Hour)
	r := eval(func(context.Context) ([]selfcheck.MCPIsolation, error) {
		return []selfcheck.MCPIsolation{
			{Server: "files", Exhausted: true},
			{Server: "docs", Calls: 2, Since: since, Cause: "timeout"},
		}, nil
	})
	if r.State != selfcheck.Bad || r.Value != 2 || r.Example["servers"] != "docs,files" || r.Example["first"] != "docs" || r.Example["cause"] != "timeout" || r.Example["since"] != since.Format(time.RFC3339) {
		t.Fatalf("被隔离时要记 bad，例子里有服务名、原因、时间：%+v", r)
	}
	if r.Next == "" || !strings.Contains(r.Next, "sumika-ctl mcp") {
		t.Fatalf("下一步里要写怎么解除：%+v", r)
	}
}
