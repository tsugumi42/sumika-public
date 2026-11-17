package ctlserver

import (
	"context"
	"regexp"

	"sumika-native/internal/ctlproto"
)

// MCPBackend 是 mcp 命令的可选扩展：MCP 客户端在正在运行的程序里，没接的宿主（演示、只看历史）不实现它。
// MCPRelease 拿到的 server 一定是合法的服务 id，reason 一定是清过的、非空的、不超长的原因，
// 并且主人已经明确带了 --accept-unknown-risk（本层校验过）；谁解除由后端写“命令行 sumika-ctl”。
type MCPBackend interface {
	MCPStatus(ctx context.Context) (ctlproto.MCPStatus, error)
	MCPRelease(ctx context.Context, server, reason string) (ctlproto.MCPReleased, error)
	MCPReconnect(ctx context.Context, server string) (ctlproto.MCPReconnected, error)
}

const mcpUsage = `用法：mcp status | mcp release <服务> --reason "<原因>" --accept-unknown-risk | mcp reconnect <服务>`

// mcpServerID 和 mcp.json 里的服务名一样：字母数字和 . _ -，不超过 64 个字
var mcpServerID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// mcp 校验 mcp 请求：子命令只认 status、release；release 要服务 id、原因和明确接受风险的开关，
// 缺哪样都 invalid、不调后端。演示数据模式一律不做。
func (s *Server) mcp(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) == 0 {
		return invalid("%s", mcpUsage)
	}
	switch req.Args[0] {
	case ctlproto.MCPStatusCmd:
		if len(req.Args) != 1 || req.MCP != nil {
			return invalid("%s", mcpUsage)
		}
		if r, ok := s.demo("mcp status"); ok {
			return r, nil
		}
		b, ok := s.b.(MCPBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "MCP 接口不可用"), nil
		}
		out, err := b.MCPStatus(s.ctx)
		if err != nil {
			return s.fromErr(err), nil
		}
		for i := range out.Servers {
			v := &out.Servers[i]
			v.ID = ctlproto.Clean(v.ID)
			v.Note = ctlproto.Clean(v.Note)
			for j := range v.Calls {
				v.Calls[j].ID = ctlproto.Clean(v.Calls[j].ID)
			}
		}
		return success(out), nil
	case ctlproto.MCPReconnectCmd:
		if len(req.Args) != 2 || !mcpServerID.MatchString(req.Args[1]) || req.MCP != nil {
			return invalid("%s", mcpUsage)
		}
		if r, ok := s.demo("mcp reconnect"); ok {
			return r, nil
		}
		b, ok := s.b.(MCPBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "MCP 接口不可用"), nil
		}
		out, err := b.MCPReconnect(s.ctx, req.Args[1])
		if err != nil {
			return s.fromErr(err), nil
		}
		out.Server = ctlproto.Clean(out.Server)
		return success(out), nil
	case ctlproto.MCPReleaseCmd:
		if len(req.Args) != 2 || !mcpServerID.MatchString(req.Args[1]) {
			return invalid("%s", mcpUsage)
		}
		reason := ""
		if req.MCP != nil {
			reason = ctlproto.CleanReason(req.MCP.Reason)
		}
		if reason == "" {
			return invalid("解除隔离必须写原因：--reason \"……\"")
		}
		if len([]rune(reason)) > ctlproto.MCPMaxReason {
			return invalid("--reason 最多 %d 个字", ctlproto.MCPMaxReason)
		}
		if req.MCP == nil || !req.MCP.AcceptRisk {
			return invalid("解除隔离等于接受“那次调用结果未知”的风险（对方可能已经执行、也可能还在执行），要明确写 --accept-unknown-risk；先用 mcp status 看清是哪几次调用")
		}
		if r, ok := s.demo("mcp release"); ok {
			return r, nil
		}
		b, ok := s.b.(MCPBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "MCP 接口不可用"), nil
		}
		out, err := b.MCPRelease(s.ctx, req.Args[1], reason)
		if err != nil {
			return s.fromErr(err), nil
		}
		out.Server = ctlproto.Clean(out.Server)
		out.Note = ctlproto.Clean(out.Note)
		return success(out), nil
	}
	return invalid("%s", mcpUsage)
}
