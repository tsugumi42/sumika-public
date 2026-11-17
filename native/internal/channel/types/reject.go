package types

import "strings"

// RejectedErr 是适配器在“协议端明确拒绝这一条”时回执 Err 的开头（可以再接冒号和细分，如“协议端拒绝：不支持这类消息”）。
// 拒绝是协议端对这份内容的判断，原样重发只会再被拒；出口不重试，链接展开按它换形式。
// 本地原因（没连上、没写出去）和结果不明不以它开头。
const RejectedErr = "协议端拒绝"

// IsRejection 说一条失败回执的 Err 是不是协议端的明确拒绝。
func IsRejection(err string) bool { return strings.HasPrefix(err, RejectedErr) }

// 结果不明里“发出去了但没拿到回执”的几种原因文字（适配器和出口共用，别处按它们认）。
// 这几种消息可能已经到了、也可能没到：链接展开会向协议端查一次历史核实（ADR-333），其余场合照旧只当结果不明。
const (
	UnconfirmedErr    = "协议端没有确认送达"   // SnowLuma：群消息 result=0 却没有 sequence（delivery was not confirmed）
	NoReplyErr        = "写出以后没有回包"    // 写出去了，等不到回包
	ReceiptTimeoutErr = "发送超时，没有等到回执" // 出口等回执超时
)

// ConnLostErr 是“写出去以后连接断了”的结果不明（SnowLuma 适配器写的）：可能已经到了，链接展开和没拿到回执一样等回声
const ConnLostErr = "写出以后连接断了"

// NoIDErr 是“回包说成功却没带消息号”的结果不明（SnowLuma 适配器写的）：没有号不能当已确认，同样等回声
const NoIDErr = "回包没有消息号"

// AwaitsEcho 说一条结果不明值不值得等回声（规格-链接发送改版 A1 的 c）：没拿到回执的三种、写出后断线、回包缺消息号
func AwaitsEcho(err string) bool { return IsNoReceipt(err) || err == ConnLostErr || err == NoIDErr }

// IsNoReceipt 说一条结果不明的原因是不是“发出去了但没拿到回执”（值得去查历史核实）。
func IsNoReceipt(err string) bool {
	return err == UnconfirmedErr || err == NoReplyErr || err == ReceiptTimeoutErr
}
