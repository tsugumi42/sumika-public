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

func deleteMaterialHandle(*os.File) error {
	// 没有按句柄删除的原语时保留材料，不能退回有换入竞态的按名删除。
	return ErrUnavailable
}
