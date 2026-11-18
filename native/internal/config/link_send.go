package config

import "fmt"

// LinkSend 是链接展开的发送流程设置（linkReader.expand.send，规格-链接发送改版 A 节）。
type LinkSend struct {
	// EchoWaitMS：发出去没拿到确认（协议端没确认送达、没回包、超时、写出后断线）时等回声的毫秒数；到点没回声就当这一条没送到
	EchoWaitMS int `yaml:"echoWaitMS"`
	// HistoryCheck：没等到回声以后再翻一次历史核实（默认关）
	HistoryCheck bool `yaml:"historyCheck"`
	// ForwardVideo（实验）：容器是合并转发时视频放进转发；协议端不支持自动退回成视频单独一条
	ForwardVideo bool `yaml:"forwardVideo"`
	// ForwardQuoteReply（实验）：合并转发里引用推文用 reply 段指向被引的那一节点；不支持自动退回旧排法
	ForwardQuoteReply bool `yaml:"forwardQuoteReply"`
}

// 等回声的范围和默认值（毫秒）
const (
	DefaultEchoWaitMS = 2000
	MinEchoWaitMS     = 500
	MaxEchoWaitMS     = 10000
)

// DefaultLinkSend 是发送流程的默认设置
func DefaultLinkSend() LinkSend {
	return LinkSend{EchoWaitMS: DefaultEchoWaitMS, ForwardVideo: true, ForwardQuoteReply: true}
}

// linkSendKinds 是 send 子块各键的类型（配置解码时查写成了别的类型）
var linkSendKinds = [][2]string{
	{"expand.send.echoWaitMS", "int"}, {"expand.send.historyCheck", "bool"},
	{"expand.send.forwardVideo", "bool"}, {"expand.send.forwardQuoteReply", "bool"},
}

// Check 校验发送流程设置；错误文字直接给用户看，带键名。
func (s LinkSend) Check() []string {
	if s.EchoWaitMS < MinEchoWaitMS || s.EchoWaitMS > MaxEchoWaitMS {
		return []string{fmt.Sprintf("linkReader.expand.send.echoWaitMS：必须在%d～%d毫秒", MinEchoWaitMS, MaxEchoWaitMS)}
	}
	return nil
}
