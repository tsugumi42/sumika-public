package store

import "testing"

// JSString 要和 JS 的 JSON.stringify 一样：< > & 不转义，U+2028 / U+2029 写原字；原文里本来就是反斜杠 u2028 这六个字的不动
func TestJSString(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   any
		want string
	}{
		{map[string]string{"t": "<b>&</b>"}, `{"t":"<b>&</b>"}`},
		{"a\u2028b\u2029c", "\"a\u2028b\u2029c\""},
		{`原文是 \u2028`, `"原文是 \\u2028"`},
		{[]any{}, `[]`},
		{"a\bb\fc\u0001", `"a\bb\fc\u0001"`},
	} {
		got, err := JSString(c.in)
		if err != nil || got != c.want {
			t.Errorf("JSString(%q) = %q，应该是 %q（%v）", c.in, got, c.want, err)
		}
	}
	// 第二条的结果里是原字，不是转义
	if got, _ := JSString("\u2028"); got != "\""+string(rune(0x2028))+"\"" {
		t.Errorf("U+2028 应该写原字：%q", got)
	}
}
