package main

import (
	"io/fs"
	"path/filepath"
	"syscall"
)

// openFiles 列出 dir 下还被打开着的文件：逐个用不共享的方式打开，打不开（共享冲突）就是还有句柄没关。
// CI 上删临时目录报过“目录不是空的”（2026-11-19）；runner 的 Defender 实时防护是关的、盘也整盘排除，不是它。
// 原因还没查清：run 返回时还开着文件就是我们自己没关，这里先拦下并列出是谁。
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
