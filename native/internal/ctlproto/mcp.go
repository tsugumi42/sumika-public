package ctlproto

import (
	"strings"
	"unicode"
)

// CmdMCP 是 MCP 服务隔离在命令行里的写法：
//
//	mcp status                                              各个 MCP 服务现在的状态；被隔离的写出原因和时间
//	mcp release <服务> --reason "<原因>" --accept-unknown-risk   解除某个服务的隔离
//	mcp reconnect <服务>                                    本机服务自动重连用完次数后，修好原因再让它连一次（不能解除隔离）
//
// 隔离的意思：这个服务有一次调用发出去了却没拿到结果（超时、被停止、断线、程序当时被关了），对方可能还在执行，
// 为防止重复执行有副作用的操作，不再给它派新调用。解除就是主人看过说明后明确接受“那次调用结果未知”的风险：
// 必须写原因，必须带 --accept-unknown-risk；旧调用的记录保留（记下谁、什么时候、原因），不会改成成功。
// 这个入口只给主人，不是模型能调的工具。
const CmdMCP = "mcp"

// 子命令
const (
	MCPStatusCmd  = "status"
	MCPReleaseCmd = "release"
	// MCPReconnectCmd：自动重连用完次数、或没连上的本机服务，主人修好原因后让它按配置再连一次；被隔离的服务照样被拒
	MCPReconnectCmd = "reconnect"
)

// MCPMaxReason 是解除原因的字数上限
const MCPMaxReason = 500

// CleanReason 把解除原因里的控制字符、格式字符（文字方向覆盖 U+202E、零宽字符等）、行分隔和段分隔符（U+2028、U+2029）换成空格，
// 去掉首尾空白。服务端校验和库里留痕共用这一个，原因要写进库和日志，不让它伪造一行或把显示和内容对不上。
func CleanReason(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, s))
}

// MCPArgs 是 mcp release 的参数
type MCPArgs struct {
	Reason     string `json:"reason,omitempty"`
	AcceptRisk bool   `json:"acceptRisk,omitempty"` // 主人明确接受“旧调用结果未知”的风险
}

// MCPCall 是 mcp status 里挡着服务的一次旧调用。时间是毫秒时间戳，0 = 不知道（旧版本留下的行没记）
type MCPCall struct {
	ID        string `json:"id"`    // 只给前 8 位，够对号
	State     string `json:"state"` // dispatched | unknown
	Cause     string `json:"cause,omitempty"`
	Transport string `json:"transport,omitempty"`
	// Legacy：这一行没记传输种类（旧版本留下的，或记录坏了）：重启后不会自动收尾，要主人手动解除一次
	Legacy       bool  `json:"legacy,omitempty"`
	DispatchedAt int64 `json:"dispatchedAt,omitempty"`
	EndedAt      int64 `json:"endedAt,omitempty"`
}

// MCPServer 是 mcp status 的一行。Isolated 为真时 Calls 非空
type MCPServer struct {
	ID        string    `json:"id"`
	Transport string    `json:"transport,omitempty"` // stdio | http；没连接过也没挡着时不知道
	Connected bool      `json:"connected"`
	Isolated  bool      `json:"isolated"`
	Calls     []MCPCall `json:"calls,omitempty"`
	// Reconnect 是本机服务自动重连的状态：空 = 没事；retrying = 正在重连；exhausted = 次数用完，要主人解除后才再试
	Reconnect string `json:"reconnect,omitempty"`
	Note      string `json:"note,omitempty"` // 给人看的一句话，比如重连失败的原因
}

// MCPStatus 是 mcp status 的 data
type MCPStatus struct {
	Servers []MCPServer `json:"servers"`
}

// MCPReconnected 是 mcp reconnect 的 data
type MCPReconnected struct {
	Server    string `json:"server"`
	Connected bool   `json:"connected"`
}

// MCPReleased 是 mcp release 的 data
type MCPReleased struct {
	Server   string `json:"server"`
	Released int    `json:"released"` // 解除了几次旧调用的隔离
	Reopened bool   `json:"reopened"` // 已经用现在的配置重新连上
	Note     string `json:"note,omitempty"`
}
