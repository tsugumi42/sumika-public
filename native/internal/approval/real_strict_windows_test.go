//go:build windows

package approval

import (
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/testutil/junction"
)

func TestRealStrictJunctionAndInvalidTarget(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	inside := filepath.Join(root, "inside")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{inside, outside} {
		name := "in"
		if target == outside {
			name = "out"
		}
		link := filepath.Join(root, name)
		if err := junction.Create(link, target); err != nil {
			t.Fatalf("建联接：%v", err)
		}
		want, err := RealStrict(target)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := RealStrict(link); err != nil || got != want {
			t.Fatalf("联接真实目标不对：%q %v，要 %q", got, err, want)
		}
		if got, err := RealForWrite(filepath.Join(link, "new.txt")); err != nil || got != filepath.Join(want, "new.txt") {
			t.Fatalf("新文件未跟随联接：%q %v", got, err)
		}
	}
	// NUL 不能交给 Windows 路径 API，也不能退到可解析的父目录冒充成功。
	invalid := filepath.Join(root, "invalid\x00.txt")
	for _, resolve := range []func(string) (string, error){RealStrict, RealForWrite} {
		if got, err := resolve(invalid); got != "" || err == nil {
			t.Fatalf("非法目标必须保留错误：%q %v", got, err)
		}
	}
}
