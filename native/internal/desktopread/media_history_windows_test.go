package desktopread

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"sumika-native/internal/api"
)

// 桌面持有数据组锁时，历史导入日志不参与逐图读取；日志临时不可读也不能拖住已下载图片。
func TestReadMediaDoesNotReopenImportHistory(t *testing.T) {
	t.Parallel()
	_, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	hash := putMedia(t, root, "qq", pngBytes)
	id := addSegs(t, qq, "qq", "qq:group:90002", image(hash), "", []string{"a"})
	dir := filepath.Join(root, "go", "import", "fixture")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	b := []byte("损坏的旧启用日志")
	file := filepath.Join(dir, "activation.json")
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	s := New(set, []api.CharacterView{{ID: "a", Name: "编造甲"}}, nil).WithData(root)
	defer s.Close()
	name, err := windows.UTF16PtrFromString(file)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	// 先证明确实锁住了日志；否则旧实现也能偶然通过。
	if _, err := os.ReadFile(file); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("历史日志未被独占：%v", err)
	}
	got, err := s.ReadMedia(context.Background(), mediaRef("qq:group:90002", id, hash), 768)
	if err != nil || !bytes.Equal(got.Value.Bytes, pngBytes) {
		t.Fatalf("已下载图片不应重新打开历史导入日志：%v", err)
	}
}
