package main

import (
	"io/fs"
	"path/filepath"
	"syscall"
)

// openFiles 列出 dir 下还被打开着的文件：逐个用不共享的方式打开，打不开（共享冲突）就是还有句柄没关。
// run 返回后还开着文件，删临时目录时 Windows 会报“目录不是空的”（CI，2026-11-19），这里先查出是谁。
func openFiles(dir string) []string {
	var held []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name, err := syscall.UTF16PtrFromString(p)
		if err != nil {
			return nil
		}
		h, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			rel, _ := filepath.Rel(dir, p)
			held = append(held, rel+"（"+err.Error()+"）")
			return nil
		}
		syscall.CloseHandle(h)
		return nil
	})
	return held
}
