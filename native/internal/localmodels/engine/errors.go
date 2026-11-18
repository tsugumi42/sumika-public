package engine

import "fmt"

// RequestError 是请求本身的问题（HTTP 400 类）：条数、长度、维度、空输入、查询太长……Detail 是给调用方看的原因，
// 只写数字和模型 id，不带输入文字（日志和响应里都不留原文）。
//
// 引擎返回的错误分三类，上层（GM5）按类映射状态码：
//   - *RequestError：400，把 Detail 原样回给调用方；
//   - context.Canceled / context.DeadlineExceeded（errors.Is 能认出）：调用方走了 / 超时，只停在行边界；
//   - 其余：500，模型或分词器出了问题（形状不对、NaN、零向量、ORT 报错、分词器内部错误）。
type RequestError struct{ Detail string }

func (e *RequestError) Error() string { return e.Detail }

func requestErrorf(format string, a ...any) error {
	return &RequestError{Detail: fmt.Sprintf(format, a...)}
}
