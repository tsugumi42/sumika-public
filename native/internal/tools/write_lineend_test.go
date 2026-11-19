package tools

import (
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/approval"
	"sumika-native/internal/jsdiff"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/lineend"
)

// useLineEnding 设 tools.lineEnding，测试结束还原
func useLineEnding(t *testing.T, mode string) {
	t.Helper()
	old := lineend.Default()
	lineend.SetDefault(mode)
	t.Cleanup(func() { lineend.SetDefault(old) })
}

func writeArgs(t *testing.T, path, content string) jsjson.Value {
	t.Helper()
	b := jsjson.Stringify(jsjson.Value{Kind: jsjson.Object, Keys: []string{"path", "content"}, Obj: map[string]jsjson.Value{
		"path": {Kind: jsjson.String, Str: path}, "content": {Kind: jsjson.String, Str: content}}})
	v, err := jsjson.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWriteKeepsExistingLineEnding(t *testing.T) {
	useLineEnding(t, "lf")
	wd := t.TempDir()
	p := filepath.Join(wd, "a.txt")
	os.WriteFile(p, []byte("旧\r\n的\r\n"), 0o644)
	if r := execIn(t, scopeCtx("c1"), wd, "write", map[string]any{"path": "a.txt", "content": "新\n正文\n"}); r.IsError {
		t.Fatal(r.Text)
	}
	if got, _ := os.ReadFile(p); string(got) != "新\r\n正文\r\n" {
		t.Fatalf("已有 CRLF 要沿用：%q", got)
	}
}

// 改了 tools.lineEnding，没有别的线索时写出来的换行跟着变
func TestWriteFollowsLineEndingSetting(t *testing.T) {
	for mode, want := range map[string]string{"lf": "甲\n乙\n", "crlf": "甲\r\n乙\r\n"} {
		useLineEnding(t, mode)
		wd := t.TempDir()
		if r := execIn(t, scopeCtx("c1"), wd, "write", map[string]any{"path": "n.txt", "content": "甲\r\n乙\n"}); r.IsError {
			t.Fatal(r.Text)
		}
		if got, _ := os.ReadFile(filepath.Join(wd, "n.txt")); string(got) != want {
			t.Errorf("%s：%q", mode, got)
		}
	}
}

// 审批卡上的预览必须就是写进文件的字节：新建看全文，覆盖看和原文件的差分
func TestWritePreviewIsWhatGetsWritten(t *testing.T) {
	gate := approval.NewGateWith(t.TempDir(), nil)
	for _, mode := range []string{"auto", "lf", "crlf"} {
		for name, old := range map[string]*string{"新建": nil, "覆盖CRLF": ptr("旧\r\n文\r\n"), "覆盖LF": ptr("旧\n文\n"), "覆盖一行": ptr("旧"), "覆盖带BOM": ptr("\uFEFF旧\r\n")} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				useLineEnding(t, mode)
				wd := t.TempDir()
				p := filepath.Join(wd, "f.txt")
				if old != nil {
					os.WriteFile(p, []byte(*old), 0o644)
				}
				args := writeArgs(t, "f.txt", "新\r\n正文\n末行")
				req := gate.Describe(wd, "suzu", "c1", "write", args)
				if r := execIn(t, scopeCtx("c1"), wd, "write", map[string]any{"path": "f.txt", "content": "新\r\n正文\n末行"}); r.IsError {
					t.Fatal(r.Text)
				}
				written, _ := os.ReadFile(p)
				want := string(written)
				if old != nil {
					want, _, _ = jsdiff.GenerateDiffString(*old, string(written), 4)
				}
				if req.Preview == nil || req.Preview.Text != want {
					t.Fatalf("预览和写入的不一样\n预览：%+v\n写入：%q", req.Preview, written)
				}
			})
		}
	}
}

func ptr(s string) *string { return &s }
