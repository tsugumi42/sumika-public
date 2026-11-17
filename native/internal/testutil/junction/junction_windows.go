package junction

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Create 在 link 创建指向 target 的目录联接；link 必须不存在，父目录由调用者准备。
// 参考 Go 的 os/os_windows_test.go 的 createMountPoint 和 createDirLink。
// 不用 go-windows-junction：它还要引入 go-winio；这里只需已有 x/sys 的一个系统调用。
func Create(link, target string) error {
	target, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	name, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		return err
	}
	linkName, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	// 8 字节公共头 + 8 字节 mount-point 头；两段路径都以 NUL 结束，长度不含 NUL。
	printName := name[4:]
	data := make([]byte, 16+2*(len(name)+len(printName)))
	binary.LittleEndian.PutUint32(data[0:4], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(data[4:6], uint16(len(data)-8))
	binary.LittleEndian.PutUint16(data[10:12], uint16(2*(len(name)-1)))
	binary.LittleEndian.PutUint16(data[12:14], uint16(2*len(name)))
	binary.LittleEndian.PutUint16(data[14:16], uint16(2*(len(printName)-1)))
	offset := 16
	for _, path := range [][]uint16{name, printName} {
		for _, c := range path {
			binary.LittleEndian.PutUint16(data[offset:offset+2], c)
			offset += 2
		}
	}
	if err := os.Mkdir(link, 0o755); err != nil {
		return err
	}
	h, err := windows.CreateFile(linkName, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var returned uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &data[0], uint32(len(data)), nil, 0, &returned, nil)
}
