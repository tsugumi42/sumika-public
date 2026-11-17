package app

import "testing"

// 回归：`\bEOF\b` 曾被存成退格字符（0x08），EOF 类失败一直归不进“连接断开”。
func TestModelFailureKindEOF(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Post \"http://127.0.0.1:3425/v1/chat\": EOF": "连接断开",
		"unexpected EOF":                     "连接断开",
		"read tcp: connection reset by peer": "连接断开",
		"GEOFFREY said hi":                   "原因未分类",
	}
	for in, want := range cases {
		if got := modelFailureKind(in); got != want {
			t.Errorf("modelFailureKind(%q) = %q，要 %q", in, got, want)
		}
	}
}
