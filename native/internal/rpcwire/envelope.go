// Package rpcwire 是界面和后台之间 JSON-RPC 2.0 协议的线上类型（规格-Bun后台第1批 第四节）。
//
// wire_gen.go 由 bun scripts/gen-rpc.ts 从 protocol/sumika-rpc.json 生成，不手改；这个文件手写信封、Result、List 和错误码。
// 和 ctlproto 一样不引项目里的别的包：客户端可以只链接它（internal/archcheck 守着）。
package rpcwire

import "encoding/json"

// 结果状态，和 api.State 同名同值
const (
	StateLoading     = "loading"
	StateReady       = "ready"
	StateFailed      = "failed"
	StateUnsupported = "unsupported"
)

// Result 区分成功但没数据、失败、不支持；Stale 表示刷新失败但带着旧值
type Result[T any] struct {
	State string       `json:"state"`
	Value *T           `json:"value,omitempty"`
	Error *ResultError `json:"error,omitempty"`
	Stale bool         `json:"stale,omitempty"`
}

type ResultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type List[T any] struct {
	Items      []T    `json:"items,omitempty"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more,omitempty"`
}

// 错误码、帧上限在 wire_gen.go（protocol/sumika-rpc.json 的 errors、limits 生成）

// Version 是 JSON-RPC 的版本字符串
const Version = "2.0"

// Message 是线上的一条消息：请求（有 id 和 method）、通知（只有 method）、回复（有 id，result 或 error）
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error 是 JSON-RPC 的错误对象
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// RunFile 是 <数据目录>/run/rpc.json 的内容：后台开着的地址和令牌（规格第三节）。令牌不写日志
type RunFile struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
	Impl  string `json:"impl"`
	// Nonce：拉起这个后台的宿主在环境变量 SUMIKA_HOST_NONCE 里给的一次性随机串，后台原样写回来，宿主据此认出“这是我刚拉起的那个”
	// （pid 不可靠：bun 经 scoop 垫片启动时，宿主拿到的是垫片的 pid，写进文件的是真 bun 的）。手动起的后台没有，省略
	Nonce string `json:"nonce,omitempty"`
}
