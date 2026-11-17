package linkreader

import (
	"encoding/json"
	"errors"
	"reflect"
)

// lv 是“宽松”的可选字段：类型对不上、是 null 或缺失时 V 为 nil，不让整份响应解码失败，
// 也不会像 *string、*int 那样在类型不符时留下一个被误分配的零值。
// 结构体字段里个别子字段类型不对时，保留能解出来的其余部分。
// 只有 JSON 语法错误才会由外层的 json.Unmarshal 报出。
type lv[T any] struct{ V *T }

func (l *lv[T]) UnmarshalJSON(b []byte) error {
	l.V = nil
	if string(b) == "null" {
		return nil
	}
	var v T
	err := json.Unmarshal(b, &v)
	if err == nil {
		l.V = &v
		return nil
	}
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) && reflect.TypeFor[T]().Kind() == reflect.Struct && len(b) > 0 && b[0] == '{' {
		l.V = &v // 结构体只坏了个别子字段：其余照用，坏的那个子字段为空
	}
	return nil
}
