package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/jsjson"
)

// grep 带上下文时从文件读行：文件结尾半截的 UTF-8 要像 JS 的 readFile('utf-8') 一样变成一个 U+FFFD，不能丢掉
// （以前收尾用的是另一个新的解码器，半截的字节被悄悄丢了）
func TestGrepContextKeepsBrokenTail(t *testing.T) {
	vendor, _ := filepath.Abs(filepath.Join("..", "..", "..", "vendor", "bin"))
	setToolPath(t, vendor+string(os.PathListSeparator)+os.Getenv("PATH"))
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, "f.txt"), []byte{'m', 'a', 't', 'c', 'h', '\n', 0xE4, 0xB8}, 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := jsjson.Parse(`{"pattern":"match","context":1}`)
	r, err := Execute(context.Background(), wd, "grep", args)
	if err != nil {
		t.Fatal(err)
	}
	if want := "f.txt:1: match\nf.txt-2- �"; r.Text != want {
		t.Fatalf("得到 %q，应该是 %q", r.Text, want)
	}
}
