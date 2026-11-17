package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
)

// 同一个文件改多处：全部成功才写；第 2 处失败时文件一个字节都不变，报错指出是 edits[1]
func TestEditManyAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	const original = "one\r\ntwo\r\nthree\r\nfour\r\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(edits string) Result {
		args, err := jsjson.Parse(`{"purpose":"测试","path":"a.txt","edits":` + edits + `}`)
		if err != nil {
			t.Fatal(err)
		}
		r, err := Execute(context.Background(), dir, "edit", args)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	bad := run(`[{"oldText":"one","newText":"1"},{"oldText":"missing","newText":"x"},{"oldText":"four","newText":"4"}]`)
	got, _ := os.ReadFile(path)
	if !bad.IsError || !strings.Contains(bad.Text, "edits[1]") || string(got) != original {
		t.Fatalf("第 2 处失败应整体不写：%+v，文件 %q", bad, got)
	}
	ok := run(`[{"oldText":"one","newText":"1"},{"oldText":"three","newText":"3"},{"oldText":"four","newText":"4"}]`)
	got, _ = os.ReadFile(path)
	if ok.IsError || !strings.Contains(ok.Text, "3 block(s)") || string(got) != "1\r\ntwo\r\n3\r\n4\r\n" {
		t.Fatalf("三处都成功：%+v，文件 %q", ok, got)
	}
}
