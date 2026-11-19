//go:build !windows

package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func linkInfo(fi os.FileInfo) bool { return fi.Mode()&os.ModeSymlink != 0 }
func checkHandle(f *os.File, expected string) (string, error) {
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(expected)
	if err != nil {
		return "", err
	}
	if path != filepath.Clean(expected) {
		return "", ErrLink
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ErrUnavailable
	}
	if fi.Mode().IsRegular() && st.Nlink != 1 {
		return "", ErrLink
	}
	current, err := os.Stat(expected)
	if err != nil {
		return "", err
	}
	if !os.SameFile(fi, current) {
		return "", ErrLink
	}
	return fmt.Sprintf("%x:%x", st.Dev, st.Ino), nil
}

func directoryCaseSensitive(*os.File) (bool, error) { return true, nil }

func resolveRecoveryPaths(_ *os.Root, paths []string) ([]string, error) { return paths, nil }

func openMaterialFile(parent *os.File, name string) (*os.File, error) {
	return os.Open(filepath.Join(parent.Name(), name))
}

// deleteMaterialHandle：Unix 没有按句柄删除的原语。dir 是 removeRestoreMaterial 已经核验过身份的材料目录，
// f 是核验过的材料文件：取 f 的对象身份，在 dir 里比对名字后再删（removeSameFile），不是无条件按名删，也不再按路径字符串重新打开目录
func deleteMaterialHandle(dir *os.Root, name string, f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	return removeSameFile(dir, name, fi)
}
