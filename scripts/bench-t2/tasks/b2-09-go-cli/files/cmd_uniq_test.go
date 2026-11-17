package main

import (
	"bytes"
	"strings"
	"testing"
)

func runUniqOn(input string, args ...string) (string, error) {
	var out bytes.Buffer
	err := runUniq(args, strings.NewReader(input), &out)
	return out.String(), err
}

func TestUniqBasic(t *testing.T) {
	got, err := runUniqOn("a\na\nB\nb\na\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := "a\nB\nb\na\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestUniqIgnoreCase(t *testing.T) {
	got, _ := runUniqOn("a\nA\nb\nB\nB\n", "-i")
	if want := "a\nb\n"; got != want {
		t.Errorf("-i 保留这一组的第一行：得到 %q，想要 %q", got, want)
	}
}

func TestUniqCount(t *testing.T) {
	got, _ := runUniqOn("a\na\nB\nb\na\n", "-i", "-c")
	if want := "   2 a\n   2 B\n   1 a\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
	got, _ = runUniqOn("x\nx\nx\ny\n", "-c")
	if want := "   3 x\n   1 y\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestUniqNoTrailingNewline(t *testing.T) {
	got, _ := runUniqOn("a\na\nb")
	if want := "a\nb\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestUniqBlankLines(t *testing.T) {
	got, _ := runUniqOn("a\n\n\nb\n\n", "-c")
	if want := "   1 a\n   2 \n   1 b\n   1 \n"; got != want {
		t.Errorf("空行也是行：得到 %q，想要 %q", got, want)
	}
}

func TestUniqEmpty(t *testing.T) {
	got, err := runUniqOn("")
	if err != nil || got != "" {
		t.Errorf("空输入什么都不输出：%q %v", got, err)
	}
}

func TestUniqNotAdjacent(t *testing.T) {
	got, _ := runUniqOn("a\nb\na\nb\n")
	if want := "a\nb\na\nb\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestUniqBadFlag(t *testing.T) {
	if _, err := runUniqOn("a\n", "-q"); err == nil {
		t.Error("不认识的参数应该返回错误")
	}
}
