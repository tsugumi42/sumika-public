package checkpoint

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var upcaseUnicodeString = windows.NewLazySystemDLL("ntdll.dll").NewProc("RtlUpcaseUnicodeString")
var compareStringOrdinal = windows.NewLazySystemDLL("kernel32.dll").NewProc("CompareStringOrdinal")

// 使用 Windows 的大写表生成键；通用 Unicode 大小写转换会合并 K/K、i/İ、ß/ẞ。
func insensitiveNameKey(name string) (string, error) {
	source, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, source.MaximumLength/2)
	target := windows.NTUnicodeString{MaximumLength: source.MaximumLength, Buffer: &buffer[0]}
	status, _, _ := upcaseUnicodeString.Call(uintptr(unsafe.Pointer(&target)), uintptr(unsafe.Pointer(source)), 0)
	if status != 0 {
		return "", windows.NTStatus(status).Errno()
	}
	return target.String(), nil
}

func equalWindowsName(left, right string) (bool, error) {
	if left == right {
		return true, nil
	}
	a, err := windows.UTF16FromString(left)
	if err != nil {
		return false, err
	}
	b, err := windows.UTF16FromString(right)
	if err != nil {
		return false, err
	}
	result, _, err := compareStringOrdinal.Call(uintptr(unsafe.Pointer(&a[0])), uintptr(len(a)-1), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)-1), 1)
	if result == 0 {
		return false, err
	}
	return result == 2, nil
}
