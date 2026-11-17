package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLines(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "lines=0\n"},
		{"a\n", "lines=1\n"},
		{"a\nb", "lines=2\n"},
		{"a\n\nb\n", "lines=3\n"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		if err := runLines(nil, strings.NewReader(c.in), &out); err != nil {
			t.Fatalf("%q：%v", c.in, err)
		}
		if out.String() != c.want {
			t.Errorf("%q：得到 %q，想要 %q", c.in, out.String(), c.want)
		}
	}
}

func TestLinesBadFlag(t *testing.T) {
	var out bytes.Buffer
	if err := runLines([]string{"-z"}, strings.NewReader(""), &out); err == nil {
		t.Error("不认识的参数应该返回错误")
	}
}
