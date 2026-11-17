package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"unsafe"
)

func linkInfo(fi os.FileInfo) bool {
	attrs, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return fi.Mode()&os.ModeSymlink != 0 || ok && attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
func checkHandle(f *os.File, expected string) (string, error) {
	identity, _, err := checkedFilePath(f, expected)
	return identity, err
}

func checkedFilePath(f *os.File, expected string) (string, string, error) {
	h := windows.Handle(f.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return "", "", err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 && info.NumberOfLinks != 1 {
		return "", "", ErrLink
	}
	buf := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", "", err
		}
		if n >= uint32(len(buf)) {
			buf = make([]uint16, n+1)
			continue
		}
		actual := windows.UTF16ToString(buf[:n])
		if strings.HasPrefix(actual, `\\?\UNC\`) {
			actual = `\\` + strings.TrimPrefix(actual, `\\?\UNC\`)
		} else {
			actual = strings.TrimPrefix(actual, `\\?\`)
		}
		equal, err := equalWindowsName(filepath.Clean(actual), filepath.Clean(expected))
		if err != nil {
			return "", "", err
		}
		if !equal {
			return "", "", ErrLink
		}
		return fmt.Sprintf("%x:%x:%x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), actual, nil
	}
}

// 恢复预览必须保存实际目录项拼写；用旧名字打开成功不代表大小写回退尚未执行。
func resolveRecoveryPaths(root *os.Root, paths []string) ([]string, error) {
	for i, path := range paths {
		name, err := relative(root.Name(), path)
		if err != nil {
			return nil, err
		}
		file, err := root.Open(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		_, actual, err := checkedFilePath(file, path)
		if err = errors.Join(err, file.Close()); err != nil {
			return nil, err
		}
		paths[i] = actual
	}
	return paths, nil
}

func directoryCaseSensitive(f *os.File) (bool, error) {
	var flags uint32
	err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileCaseSensitiveInfo, (*byte)(unsafe.Pointer(&flags)), uint32(unsafe.Sizeof(flags)))
	return flags&windows.FILE_CS_FLAG_CASE_SENSITIVE_DIR != 0, err
}
