//go:build !windows

package approval

import (
	"fmt"
	"os"
)

func OpenedPath(file *os.File) (string, error) {
	actual, err := RealStrict(file.Name())
	if err != nil {
		return "", err
	}
	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	current, err := os.Stat(actual)
	if err != nil {
		return "", err
	}
	if !os.SameFile(opened, current) {
		return "", fmt.Errorf("文件身份已变")
	}
	return actual, nil
}
