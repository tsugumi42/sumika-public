package watchdog

import (
	"errors"

	"golang.org/x/sys/windows"
)

// AcquireLock 拿一把当前登录会话里的命名互斥量（Local\<name>）。已经有别人拿着返回 ok=false。release 释放它
func AcquireLock(name string) (release func(), ok bool, err error) {
	p, err := windows.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateMutex(nil, false, p)
	if err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			windows.CloseHandle(h)
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { windows.CloseHandle(h) }, true, nil
}
