package llm

import "net/http"

// StreamError：流里来了 error 事件（回复头已经发出去，没有状态码）。Data 是这个事件的原始数据，Header 是回复头。
// 线上格式的解析在 openaichat；这里保留给重试和错误分类用
type StreamError struct {
	Message string
	Data    []byte
	Header  http.Header
}

func (e *StreamError) Error() string { return e.Message }
