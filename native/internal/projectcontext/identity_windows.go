//go:build windows

package projectcontext

import (
	"os"

	"golang.org/x/sys/windows"
)

type fileIdentity struct {
	volume uint32
	index  uint64
}

func identify(path string) (os.FileInfo, fileIdentity, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	defer windows.CloseHandle(h)
	var id windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &id); err != nil {
		return nil, fileIdentity{}, err
	}
	info, err := os.Stat(path)
	return info, fileIdentity{id.VolumeSerialNumber, uint64(id.FileIndexHigh)<<32 | uint64(id.FileIndexLow)}, err
}

func sameIdentity(a, b fileIdentity) bool { return a == b }
