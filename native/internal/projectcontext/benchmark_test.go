package projectcontext

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func benchmarkFixture(t testing.TB) (Options, *int) {
	t.Helper()
	root := t.TempDir()
	cwd := root
	for range 16 {
		cwd = filepath.Join(cwd, "dir")
	}
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for i := range 64 {
		name := fmt.Sprintf("import%d.md", i)
		fmt.Fprintf(&body, "@%s\n", name)
		put(t, root, name, "导入的说明")
	}
	body.WriteString(strings.Repeat("x", (32<<10)-body.Len()))
	put(t, root, "AGENTS.md", body.String())
	bytes := new(int)
	return Options{Root: root, CWD: cwd, Open: func(p string) (io.ReadCloser, error) {
		rd, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		return countedReader{rd, bytes}, nil
	}}, bytes
}

func TestHotPathReadsZero(t *testing.T) {
	o, bytes := benchmarkFixture(t)
	r := resolver(t, o)
	cold := r.Discover(background)
	if *bytes != (32<<10)+64*len("导入的说明") {
		t.Fatalf("冷路径未读取全部 64 个导入：bytes=%d", *bytes)
	}
	*bytes = 0
	hot := r.Discover(background)
	if *bytes != 0 || hot.Text != cold.Text {
		t.Fatalf("热路径重读或版本不稳定：bytes=%d", *bytes)
	}
	t.Logf("深度 16，32 KiB 说明 + 64 import：热路径正文 %d 字节", *bytes)
}

func BenchmarkProjectContext(b *testing.B) {
	o, bytes := benchmarkFixture(b)
	b.Run("Cold", func(b *testing.B) {
		b.ReportAllocs()
		*bytes = 0
		for b.Loop() {
			r := resolver(b, o)
			r.Discover(background)
		}
		b.ReportMetric(float64(*bytes)/float64(b.N), "body-B/op")
	})
	b.Run("Hot", func(b *testing.B) {
		r := resolver(b, o)
		r.Discover(background)
		*bytes = 0
		b.ReportAllocs()
		for b.Loop() {
			r.Discover(background)
		}
		if *bytes != 0 {
			b.Fatalf("热路径读取 %d 字节正文", *bytes)
		}
		b.ReportMetric(float64(*bytes), "body-B/op")
	})
}
