package mediastore

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// 数据根与 paths.Open 一样允许联接；从目录句柄取真实根，不依赖 EvalSymlinks 对联接的识别。
func mediaRootPath(data string) (string, error) {
	path, err := filepath.Abs(data)
	if err != nil {
		return "", err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	return mediaFinalPath(h)
}

func mediaFinalPath(h windows.Handle) (string, error) {
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
		path := windows.UTF16ToString(buf[:n])
		if strings.HasPrefix(path, `\\?\UNC\`) {
			path = `\\` + strings.TrimPrefix(path, `\\?\UNC\`)
		} else {
			path = strings.TrimPrefix(path, `\\?\`)
		}
		return filepath.Clean(path), nil
	}
}

func checkMediaHandle(f *os.File, expected string) error {
	h := windows.Handle(f.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrMediaLink
	}
	actual, err := mediaFinalPath(h)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, filepath.Clean(expected)) {
		return ErrMediaLink
	}
	return nil
}
