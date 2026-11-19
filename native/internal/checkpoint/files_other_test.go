//go:build !windows

package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Unix 的 deleteMaterialHandle 拿到的是 removeRestoreMaterial 已经核验过的 *os.Root、名字和已打开的材料文件：
// 同一个文件就删；名字在核验后被换成别的文件，保留并报冲突。这条路径不再按 f.Name() 拆路径重开目录。
func TestDeleteMaterialHandleUsesVerifiedRoot(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require(t, err)
	t.Cleanup(func() { root.Close() })
	path := filepath.Join(dir, "m.tmp")

	require(t, os.WriteFile(path, []byte("material"), 0600))
	f, err := os.Open(path)
	require(t, err)
	require(t, deleteMaterialHandle(root, "m.tmp", f))
	require(t, f.Close())
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("材料应当被删掉：%v", err)
	}

	require(t, os.WriteFile(path, []byte("material"), 0600))
	f, err = os.Open(path)
	require(t, err)
	t.Cleanup(func() { f.Close() })
	require(t, os.Rename(path, filepath.Join(dir, "retained")))
	require(t, os.WriteFile(path, []byte("user-file"), 0600))
	if err := deleteMaterialHandle(root, "m.tmp", f); !errors.Is(err, ErrConflict) {
		t.Fatalf("名字换成别的文件应当报冲突：%v", err)
	}
	checkFile(t, path, []byte("user-file"))
}
