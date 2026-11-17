package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDispatchAll(t *testing.T) {
	cases := []struct {
		args []string
		in   string
		want string
	}{
		{[]string{"lines"}, "a\nb\n", "lines=2\n"},
		{[]string{"count", "-w"}, "a b c\n", "words=3\n"},
		{[]string{"top", "-n", "1", "-stop", "config/stopwords.txt"}, "x y x the the the", "x 2\n"},
		{[]string{"uniq", "-c"}, "k\nk\n", "   2 k\n"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		if err := dispatch(c.args, strings.NewReader(c.in), &out); err != nil {
			t.Fatalf("%v：%v", c.args, err)
		}
		if out.String() != c.want {
			t.Errorf("%v：得到 %q，想要 %q", c.args, out.String(), c.want)
		}
	}
}

func TestDispatchErrors(t *testing.T) {
	var out bytes.Buffer
	if err := dispatch(nil, strings.NewReader(""), &out); err == nil {
		t.Error("没有子命令应该返回错误")
	}
	if err := dispatch([]string{"nope"}, strings.NewReader(""), &out); err == nil {
		t.Error("不认识的子命令应该返回错误")
	}
}
