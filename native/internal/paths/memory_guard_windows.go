package paths

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Windows 上 EvalSymlinks 不认联接（junction），所以重解析点、真实路径、硬链接数都问系统：
// 照 mediastore/media_read_windows.go 的写法（取目录句柄，再取最终路径）

func sameName(a, b string) bool { return strings.EqualFold(a, b) }

// isLink：这一级是不是重解析点（符号链接、联接、云盘占位都算）
func isLink(path string) (bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attrs, err := windows.GetFileAttributes(name)
	if err != nil {
		return false, err
	}
	return attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

// trimVerbatim 去掉 GetFinalPathNameByHandle 给的 \\?\ 前缀（共享盘的 \\?\UNC\ 还原成 \\服务器\共享）。
// 前缀是两个反斜杠加问号，写成一个会匹配不上、整个分支成了死代码（复核查出）
func trimVerbatim(p string) string {
	if strings.HasPrefix(p, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(p, `\\?\UNC\`)
	}
	return strings.TrimPrefix(p, `\\?\`)
}

// realPath 跟着链接走到真实位置（目录或文件都行）
func realPath(path string) (string, error) {
	h, err := openForInfo(path)
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
		return filepath.Clean(trimVerbatim(windows.UTF16ToString(buf[:n]))), nil
	}
}

func hardLinks(path string) (uint32, error) {
	h, err := openForInfo(path)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return 0, err
	}
	return info.NumberOfLinks, nil
}

func openForInfo(path string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}
