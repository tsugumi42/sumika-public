package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCountOn(t *testing.T, input string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := runCount(args, strings.NewReader(input), &out); err != nil {
		t.Fatalf("count %v：%v", args, err)
	}
	return out.String()
}

func TestCountAll(t *testing.T) {
	got := runCountOn(t, "hello world\nbye\n")
	if want := "lines=2 words=3 bytes=16\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestCountEmpty(t *testing.T) {
	got := runCountOn(t, "")
	if want := "lines=0 words=0 bytes=0\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestCountSelected(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-l"}, "lines=2\n"},
		{[]string{"-w"}, "words=3\n"},
		{[]string{"-c"}, "bytes=16\n"},
		{[]string{"-w", "-l"}, "lines=2 words=3\n"},
		{[]string{"-c", "-w"}, "words=3 bytes=16\n"},
		{[]string{"-c", "-l", "-w"}, "lines=2 words=3 bytes=16\n"},
	}
	for _, c := range cases {
		if got := runCountOn(t, "hello world\nbye\n", c.args...); got != c.want {
			t.Errorf("%v：得到 %q，想要 %q", c.args, got, c.want)
		}
	}
}

func TestCountBytesNotRunes(t *testing.T) {
	// “你好”是 6 个字节，加换行 7 个
	if got := runCountOn(t, "你好\n", "-c"); got != "bytes=7\n" {
		t.Errorf("得到 %q", got)
	}
}

func TestCountNoTrailingNewline(t *testing.T) {
	// 行数的算法和 lines 一致：最后一行没有换行也算一行
	if got := runCountOn(t, "a b\nc", "-l", "-w"); got != "lines=2 words=3\n" {
		t.Errorf("得到 %q", got)
	}
}

func TestCountFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(p, []byte("one two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 给了文件就不读标准输入
	if got := runCountOn(t, "ignored input here", "-w", p); got != "words=2\n" {
		t.Errorf("得到 %q", got)
	}
}

func TestCountErrors(t *testing.T) {
	var out bytes.Buffer
	if err := runCount([]string{"-x"}, strings.NewReader(""), &out); err == nil {
		t.Error("不认识的参数应该返回错误")
	}
	if err := runCount([]string{filepath.Join(t.TempDir(), "nope.txt")}, strings.NewReader(""), &out); err == nil {
		t.Error("文件不存在应该返回错误")
	}
}
