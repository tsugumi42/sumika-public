//go:build !windows

package paths

import (
	"os"
	"path/filepath"
)

func sameName(a, b string) bool { return a == b }

// isLink：这一级是不是符号链接
func isLink(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return fi.Mode()&os.ModeSymlink != 0, nil
}

func realPath(path string) (string, error) { return filepath.EvalSymlinks(path) }

// hardLinks：非 Windows 不查硬链接数（这个程序只在 Windows 上发出去），返回 1
func hardLinks(string) (uint32, error) { return 1, nil }
