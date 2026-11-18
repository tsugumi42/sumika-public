//go:build !windows

package backup

import (
	"fmt"
	"os"
	"syscall"
)

func fileID(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("取不到 %s 的文件身份", path)
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), nil
}
