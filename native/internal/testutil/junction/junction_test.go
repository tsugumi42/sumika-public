package junction_test

import (
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/testutil/junction"
)

func TestCreate(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "编造目标 空格 #%&😀")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	const content = "编造的目标文件"
	if err := os.WriteFile(filepath.Join(target, "内容.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "编造联接")
	if err := junction.Create(link, target); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&(os.ModeIrregular|os.ModeSymlink) == 0 {
		t.Fatalf("创建的不是重解析点或符号链接：%s", info.Mode())
	}
	got, err := os.Readlink(link)
	if err != nil || got != target {
		t.Fatalf("联接目标：%q %v，要 %q", got, err, target)
	}
	b, err := os.ReadFile(filepath.Join(link, "内容.txt"))
	if err != nil || string(b) != content {
		t.Fatalf("通过联接读取：%q %v", b, err)
	}
}
