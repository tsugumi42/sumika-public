package selfcheck

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MCPIsolation 是一个被隔离、或断了没重连上的 MCP 服务（app 从调用留痕和 MCP 客户端的状态拼出来，自检包不 import mcpclient 和 store）。
type MCPIsolation struct {
	Server    string
	Calls     int       // 挡着它的旧调用数；0 = 只是本机服务自动重连用完了次数
	Since     time.Time // 最早一次旧调用没有结果的时间；零 = 不知道（旧版本留下的行没记）
	Cause     string    // 最早一次的原因，固定的词：timeout、cancelled、error、host_exit
	Exhausted bool      // 本机服务自动重连用完了次数
}

// mcpQuarantined 是 S11 mcp.quarantined：有 MCP 服务被隔离（一次已发出的调用没有结果，为防重复执行已暂停），
// 或本机服务断了、自动重连用完次数。任何一个都是“这个服务现在用不了、自己不会好”，要主人处理。
// 判定：没接 MCP 状态 → unknown；一个都没有 → ok；否则 bad，例子里放服务名、最早一次的时间和原因（固定词）。
// 解除办法写在备注里：sumika-ctl mcp status 看原因，mcp release（被隔离）或 mcp reconnect（重连用完）处理。
type mcpQuarantined struct{}

func (mcpQuarantined) ID() string   { return "mcp.quarantined" }
func (mcpQuarantined) Name() string { return "有 MCP 服务被隔离或断了没重连上" }

func (mcpQuarantined) Eval(ctx context.Context, e *Env) Result {
	r := Result{Threshold: 1}
	if e.MCP == nil {
		r.State, r.Note = Unknown, "没有接 MCP 状态"
		return r
	}
	list, err := e.MCP(ctx)
	if err != nil {
		r.State, r.Note = Unknown, "读取失败："+errClassOf(err)
		return r
	}
	r.Value = int64(len(list))
	if len(list) == 0 {
		r.State = Ok
		return r
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Server < list[j].Server })
	var names []string
	for _, m := range list {
		names = append(names, m.Server)
	}
	first := list[0]
	for _, m := range list {
		if !m.Since.IsZero() && (first.Since.IsZero() || m.Since.Before(first.Since)) {
			first = m
		}
	}
	r.State = Bad
	r.Example = map[string]string{"servers": strings.Join(names, ","), "first": first.Server, "calls": strconv.Itoa(first.Calls)}
	if first.Cause != "" {
		r.Example["cause"] = first.Cause
	}
	if !first.Since.IsZero() {
		r.Example["since"] = first.Since.Format(time.RFC3339)
	}
	r.Note = "有 MCP 服务被隔离，或本机服务断了、自动重连用完次数"
	r.Next = "用 sumika-ctl mcp status 看原因；被隔离的用 mcp release <服务> --reason <原因> --accept-unknown-risk 解除，重连用完的用 mcp reconnect <服务>"
	return r
}
