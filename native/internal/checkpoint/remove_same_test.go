package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// removeSameFile 是没有按句柄删除原语的系统（Unix）删恢复材料的唯一出口：只删“被核验过的那一个”文件。
// 逻辑本身与系统无关，Windows 上也跑，免得只有 Linux 那一格才见得到它红
func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	require(t, err)
	t.Cleanup(func() { root.Close() })
	return root
}

func TestRemoveSameFile(t *testing.T) {
	stat := func(t *testing.T, path string) os.FileInfo {
		t.Helper()
		fi, err := os.Lstat(path)
		require(t, err)
		// Windows 的 FileInfo 惰性读文件标识，先触发一次，免得文件被换掉以后才读到新文件的标识
		os.SameFile(fi, fi)
		return fi
	}

	t.Run("是同一个文件就删", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "m.tmp")
		require(t, os.WriteFile(path, []byte("material"), 0600))
		require(t, removeSameFile(openRoot(t, dir), "m.tmp", stat(t, path)))
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("材料应当被删掉：%v", err)
		}
	})

	t.Run("名字被换成别的文件就不删", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "m.tmp")
		require(t, os.WriteFile(path, []byte("material"), 0600))
		want := stat(t, path)
		// 改名挪开而不是删掉：原文件的 inode 还占着，新文件不会复用它的号（生产里 removeRestoreMaterial 一直开着原文件，同样复用不了）
		require(t, os.Rename(path, path+".moved"))
		require(t, os.WriteFile(path, []byte("imposter"), 0600))
		err := removeSameFile(openRoot(t, dir), "m.tmp", want)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("名字换成别的文件应当报冲突：%v", err)
		}
		checkFile(t, path, []byte("imposter"))
	})

	t.Run("名字已经不在算做完", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "m.tmp")
		require(t, os.WriteFile(path, []byte("material"), 0600))
		want := stat(t, path)
		require(t, os.Remove(path))
		require(t, removeSameFile(openRoot(t, dir), "m.tmp", want))
		require(t, removeSameFile(openRoot(t, dir), "没有这个名字.tmp", want))
	})

	t.Run("名字被换成指向外面的链接不跟随", func(t *testing.T) {
		dir, outside := t.TempDir(), t.TempDir()
		secret := filepath.Join(outside, "secret")
		require(t, os.WriteFile(secret, []byte("untouched"), 0600))
		path := filepath.Join(dir, "m.tmp")
		require(t, os.WriteFile(path, []byte("material"), 0600))
		want := stat(t, path)
		require(t, os.Rename(path, path+".moved"))
		if err := os.Symlink(secret, path); err != nil {
			t.Skipf("这个环境建不了符号链接（Windows 需要权限）：%v", err)
		}
		if err := removeSameFile(openRoot(t, dir), "m.tmp", want); !errors.Is(err, ErrConflict) {
			t.Fatalf("链接不是被核验的文件，应当报冲突：%v", err)
		}
		checkFile(t, secret, []byte("untouched"))
	})
}
