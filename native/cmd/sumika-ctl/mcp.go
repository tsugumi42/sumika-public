package main

import (
	"fmt"
	"io"
	"time"

	"sumika-native/internal/ctlproto"
)

var mcpCauseName = map[string]string{
	"timeout":   "调用时限到了还没回结果",
	"cancelled": "调用被停止",
	"error":     "调用出错或断线",
	"host_exit": "程序退出时这次调用还没有结果",
}

var mcpReconnectName = map[string]string{
	"retrying":  "本机服务断了，正在自动重连",
	"exhausted": "自动重连的次数用完了",
}

func mcpWhen(ms int64) string {
	if ms == 0 {
		return "时间没记"
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

// renderMCP 打印 mcp status、mcp release。服务名、原因都是读出来的，只当数据看。--json 不走这里
func renderMCP(w, stderr io.Writer, req ctlproto.Request, r ctlproto.Reply) {
	if len(req.Args) == 0 {
		return
	}
	switch req.Args[0] {
	case ctlproto.MCPStatusCmd:
		var v ctlproto.MCPStatus
		if !decode(stderr, r, &v) {
			return
		}
		if len(v.Servers) == 0 {
			fmt.Fprintln(w, "（没有配置启用的 MCP 服务，也没有隔离记录）")
			return
		}
		for _, s := range v.Servers {
			state := "正常"
			switch {
			case s.Isolated:
				state = "被隔离"
			case !s.Connected:
				state = "没连上"
			}
			fmt.Fprintf(w, "%s｜%s｜%s\n", ctlproto.Clean(s.ID), nameOr(map[string]string{"stdio": "本机程序", "stdio-remote": "本机程序（转给别处）", "http": "远端 HTTP"}, s.Transport), state)
			if s.Reconnect != "" {
				fmt.Fprintf(w, "  %s\n", nameOr(mcpReconnectName, s.Reconnect))
			}
			if s.Note != "" {
				fmt.Fprintf(w, "  %s\n", ctlproto.Clean(s.Note))
			}
			for _, c := range s.Calls {
				fmt.Fprintf(w, "  旧调用 %s：%s｜发出 %s", ctlproto.Clean(c.ID), nameOr(mcpCauseName, c.Cause), mcpWhen(c.DispatchedAt))
				if c.Legacy {
					fmt.Fprint(w, "｜旧版本留下的记录，没记传输种类，要手动解除一次")
				}
				if c.EndedAt != 0 {
					fmt.Fprintf(w, "｜没有结果的时间 %s", mcpWhen(c.EndedAt))
				}
				fmt.Fprintln(w)
			}
			if s.Reconnect == "exhausted" && !s.Isolated {
				fmt.Fprintf(w, "  修好原因后：sumika-ctl mcp reconnect %s\n", ctlproto.Clean(s.ID))
			}
			if s.Isolated {
				fmt.Fprintf(w, "  对方可能已经执行、也可能还在执行，所以暂停了这个服务。确认后：sumika-ctl mcp release %s --reason \"原因\" --accept-unknown-risk\n", ctlproto.Clean(s.ID))
			}
		}
	case ctlproto.MCPReconnectCmd:
		var v ctlproto.MCPReconnected
		if !decode(stderr, r, &v) {
			return
		}
		fmt.Fprintf(w, "%s 已重新连上，可以再调用了\n", ctlproto.Clean(v.Server))
	case ctlproto.MCPReleaseCmd:
		var v ctlproto.MCPReleased
		if !decode(stderr, r, &v) {
			return
		}
		fmt.Fprintf(w, "已解除 %s 的隔离（%d 次旧调用按“结果未知”接受了风险，记录保留）\n", ctlproto.Clean(v.Server), v.Released)
		switch {
		case v.Note != "":
			fmt.Fprintln(w, "  "+ctlproto.Clean(v.Note))
		case v.Reopened:
			fmt.Fprintln(w, "  服务已连上，可以再调用了")
		}
	}
}
