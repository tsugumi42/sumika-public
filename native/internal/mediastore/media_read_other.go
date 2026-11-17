//go:build !windows

package mediastore

import (
	"os"
	"path/filepath"
)

func mediaRootPath(data string) (string, error) {
	path, err := filepath.Abs(data)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

// 非 Windows 暂只做打开前的逐级 Lstat；此分支不保证拦住检查与打开之间的路径替换。
func checkMediaHandle(_ *os.File, _ string) error { return nil }
