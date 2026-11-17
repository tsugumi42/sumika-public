package approval

import (
	"golang.org/x/sys/windows"
)

// GetFinalPathNameByHandleW 的 flags：VOLUME_NAME_DOS（盘符写法）| FILE_NAME_NORMALIZED，两个都是 0（x/sys 没有定义这两个名字）
const volumeNameDOS = 0

// realPath 像 Node 的 fs.realpathSync.native（libuv 的 uv_fs_realpath）：打开它、问系统最终的路径（GetFinalPathNameByHandleW），
// 联接、符号链接都解析，大小写按磁盘上的；去掉 \\?\ 前缀。Go 的 filepath.EvalSymlinks 从 1.23 起不跟联接走，不能用
func realPath(p string) (string, error) {
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	return openedHandlePath(h)
}
