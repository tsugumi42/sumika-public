package approval

import (
	"golang.org/x/sys/windows"
	"os"
	"strings"
)

// OpenedPath 让审批针对随后读取的同一个已打开文件，不再按名字重新打开。
func OpenedPath(file *os.File) (string, error) { return openedHandlePath(windows.Handle(file.Fd())) }
func openedHandlePath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), volumeNameDOS)
		if err != nil {
			return "", err
		}
		if int(n) < len(buffer) {
			name := windows.UTF16ToString(buffer[:n])
			if strings.HasPrefix(name, `\\?\UNC\`) {
				return `\\` + name[len(`\\?\UNC\`):], nil
			}
			return strings.TrimPrefix(name, `\\?\`), nil
		}
		buffer = make([]uint16, n+1)
	}
}
