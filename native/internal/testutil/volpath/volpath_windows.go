package volpath

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// GUIDPath 把本机盘上的绝对路径 p 写成 `\\?\Volume{GUID}\…` 的样子（卷标识写法）。这种写法落在本机的同一个位置上，
// 但不是 `D:\…`，规整出来和受保护目录比不上；测试用它造“绕开受保护目录的另一种写法”
func GUIDPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	vol := filepath.VolumeName(abs)
	if len(vol) != 2 || vol[1] != ':' {
		return "", fmt.Errorf("不是本机盘符路径：%s", p)
	}
	root, err := windows.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, 64)
	if err := windows.GetVolumeNameForVolumeMountPoint(root, &buf[0], uint32(len(buf))); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf) + strings.TrimLeft(abs[len(vol):], `\`), nil
}
