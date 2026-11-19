package pool

import "fmt"

// Error 是池交给调用方的错误，Status 是建议的 HTTP 状态码（和 server.py 的 PoolError 一样）：
// 404 不认识的模型或种类不对，500 加载失败，503 腾不出位置（都在用、超内存上限、还在加载、池在关闭）。
// Detail 只写模型 id、数字和相对路径，不带输入文字、绝对路径、令牌。
// 调用方走了或超时不是 *Error，原样返回 ctx 的错误（errors.Is 认得出），GM5 另映射成 499 / 504。
type Error struct {
	Status int
	Detail string
	Err    error // 底层原因（加载函数返回的错误），只给日志和 errors.Is 用，Detail 里已经有它的文字
}

func (e *Error) Error() string { return e.Detail }
func (e *Error) Unwrap() error { return e.Err }

func newError(status int, format string, a ...any) *Error {
	return &Error{Status: status, Detail: fmt.Sprintf(format, a...)}
}
