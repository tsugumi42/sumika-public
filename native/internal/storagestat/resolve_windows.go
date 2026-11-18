//go:build windows

package storagestat

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// realPath 取路径的真实位置。Go 的 EvalSymlinks 不解联接（junction），所以和 mediastore 一样，从目录句柄取最终路径
func realPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	name, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 260)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err
		}
		if n >= uint32(len(buf)) {
			buf = make([]uint16, n+1)
			continue
		}
		p := windows.UTF16ToString(buf[:n])
		if strings.HasPrefix(p, `\?\UNC\`) {
			p = `\` + strings.TrimPrefix(p, `\?\UNC\`)
		} else {
			p = strings.TrimPrefix(p, `\?\`)
		}
		return filepath.Clean(p), nil
	}
}
