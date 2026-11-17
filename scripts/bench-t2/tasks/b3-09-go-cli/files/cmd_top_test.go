package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runTopOn(input string, args ...string) (string, error) {
	var out bytes.Buffer
	err := runTop(args, strings.NewReader(input), &out)
	return out.String(), err
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTopBasic(t *testing.T) {
	stop := writeTemp(t, "stop.txt", "")
	got, err := runTopOn("b a b c b a", "-stop", stop)
	if err != nil {
		t.Fatal(err)
	}
	if want := "b 3\na 2\nc 1\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestTopTieBreakAndLimit(t *testing.T) {
	stop := writeTemp(t, "stop.txt", "")
	got, err := runTopOn("pear apple fig apple pear kiwi", "-n", "3", "-stop", stop)
	if err != nil {
		t.Fatal(err)
	}
	// apple、pear 各 2 次，字典序 apple 在前；其余各 1 次，fig 排在 kiwi 前
	if want := "apple 2\npear 2\nfig 1\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestTopDefaultN(t *testing.T) {
	stop := writeTemp(t, "stop.txt", "")
	got, _ := runTopOn("a b c d e", "-stop", stop)
	if want := "a 1\nb 1\nc 1\n"; got != want {
		t.Errorf("默认 n 是 3：得到 %q，想要 %q", got, want)
	}
}

func TestTopFewerWords(t *testing.T) {
	stop := writeTemp(t, "stop.txt", "")
	got, _ := runTopOn("x y", "-n", "10", "-stop", stop)
	if want := "x 1\ny 1\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestTopCaseAndPunctuation(t *testing.T) {
	stop := writeTemp(t, "stop.txt", "")
	got, _ := runTopOn("Go, go! GO? golang; go-lang 2go", "-n", "2", "-stop", stop)
	// 切出来：go go go golang go lang 2go，所以 go 4 次；其余各 1 次，字典序 2go 在 golang 前
	if want := "go 4\n2go 1\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestTopCustomStopwords(t *testing.T) {
	stop := writeTemp(t, "stop.txt", "  Foo \n\nBAR\n")
	got, _ := runTopOn("foo bar baz foo qux", "-n", "5", "-stop", stop)
	if want := "baz 1\nqux 1\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestTopDefaultStopwordsFile(t *testing.T) {
	// 测试在包目录里跑，当前目录下有 config/stopwords.txt（含 the、and）
	got, err := runTopOn("the cat and the dog and the bird", "-n", "2")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bird 1\ncat 1\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestTopNoStopwordsFileIsFine(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	got, err := runTopOn("the the a", "-n", "1")
	if err != nil {
		t.Fatalf("默认的停用词文件不存在不算错误：%v", err)
	}
	if want := "the 2\n"; got != want {
		t.Errorf("得到 %q，想要 %q", got, want)
	}
}

func TestTopEmpty(t *testing.T) {
	stop := writeTemp(t, "stop.txt", "")
	got, err := runTopOn(" ,.! \n", "-stop", stop)
	if err != nil || got != "" {
		t.Errorf("没有词应该什么都不输出：%q %v", got, err)
	}
}

func TestTopErrors(t *testing.T) {
	if _, err := runTopOn("a", "-n", "0"); err == nil || !strings.Contains(err.Error(), "-n") {
		t.Errorf("-n 0 应该返回带 -n 的错误：%v", err)
	}
	if _, err := runTopOn("a", "-n", "-2"); err == nil {
		t.Error("-n 负数应该返回错误")
	}
	if _, err := runTopOn("a", "-stop", filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Error("明确给了 -stop 而文件读不了，应该返回错误")
	}
	if _, err := runTopOn("a", "-zzz"); err == nil {
		t.Error("不认识的参数应该返回错误")
	}
}
