package jsjson

// 性质测试：JSON.parse / JSON.stringify 的往返（照 JS 的行为，非有限数 stringify 成 null）。

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// normalize 把 JS 里 stringify 成 null 的数换成 Null，往返比较用；别的值原样递归。
func normalize(v Value) Value {
	switch v.Kind {
	case Number:
		if math.IsNaN(v.Num) || math.IsInf(v.Num, 0) {
			return Value{Kind: Null}
		}
	case Array:
		arr := make([]Value, len(v.Arr))
		for i, e := range v.Arr {
			arr[i] = normalize(e)
		}
		v.Arr = arr
	case Object:
		obj := make(map[string]Value, len(v.Obj))
		for k, e := range v.Obj {
			obj[k] = normalize(e)
		}
		v.Obj = obj
	}
	return v
}

// FuzzParseStringifyRoundTrip：Quote 出来的串能解析回去（合法 UTF-8 时值相同）；Parse 成功的值，
// Stringify 再 Parse 得到相同的值（非有限数按 JS 变成 null），Stringify 的结果再走一遍不变。
func FuzzParseStringifyRoundTrip(f *testing.F) {
	seeds := []string{
		`{"a":1,"b":[true,null,"x"],"0":"idx","12":[]}`,
		`1e400`,
		`-1e400`,
		`{"中文":"日本語","emoji":"😀👍🏽","ctrl":"\u0001\u001f"}`,
		`{"a":1,"a":2,"b":{"0":"x","1":"y"}}`,
		`[` + strings.Repeat(`"long",`, 200) + `0]`,
		`{"a":`,
		"\x00\x01",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		q, err := Parse(Quote(s))
		if err != nil {
			t.Fatalf("Quote(%q) 解析不回来：%v（%q）", s, err, Quote(s))
		}
		if utf8.ValidString(s) && (q.Kind != String || q.Str != s) {
			t.Fatalf("Quote 往返变了：%q → %q → %+v", s, Quote(s), q)
		}

		v, err := Parse(s)
		if err != nil {
			return
		}
		w1 := Stringify(v)
		v2, err := Parse(w1)
		if err != nil {
			t.Fatalf("Stringify 的结果解析不回来：输入 %q，值 %+v，串 %q：%v", s, v, w1, err)
		}
		w2 := Stringify(v2)
		if w2 != w1 {
			t.Fatalf("Stringify 第二遍变了：输入 %q\n一遍 %q\n两遍 %q", s, w1, w2)
		}
		if !reflect.DeepEqual(normalize(v), normalize(v2)) {
			t.Fatalf("Parse→Stringify→Parse 值变了：输入 %q\n一遍 %+v\n两遍 %+v", s, normalize(v), normalize(v2))
		}
	})
}
