//go:build windows

package secrets

import (
	"bytes"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 和 daemon/util/dpapi.ts 一样：当前用户、无附加熵、不弹系统界面。
func unprotect(data []byte) ([]byte, error) {
	input := windows.DataBlob{Size: uint32(len(data)), Data: unsafe.SliceData(data)}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return bytes.Clone(unsafe.Slice(out.Data, int(out.Size))), nil
}

// protect 是 unprotect 的反向：同一 Windows 用户、无附加熵、不弹系统界面。
func protect(data []byte) ([]byte, error) {
	input := windows.DataBlob{Size: uint32(len(data)), Data: unsafe.SliceData(data)}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return bytes.Clone(unsafe.Slice(out.Data, int(out.Size))), nil
}
