package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 只接同目录叶子；源与目标均绑定句柄，最后核验后的目录换路不能改变发布位置。
func durableRename(root *os.Root, from, to string) error {
	return renameWithMode(root, from, to, nil)
}

func publishRestoreTemp(root *os.Root, from, to string, mode os.FileMode) error {
	if mode&0200 != 0 {
		return durableRename(root, from, to)
	}
	return renameWithMode(root, from, to, &mode)
}

func renameWithMode(root *os.Root, from, to string, mode *os.FileMode) (err error) {
	if err := checkParents(root, from); err != nil {
		return err
	}
	if err := checkParents(root, to); err != nil {
		return err
	}
	if filepath.Base(from) != from || filepath.Base(to) != to {
		return ErrUnavailable
	}
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	if _, err := checkHandle(parent, root.Name()); err != nil {
		return err
	}
	source, err := openRenameFile(parent, from)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	if _, err := checkHandle(source, filepath.Join(root.Name(), from)); err != nil {
		return err
	}
	if err := commitRename(parent, source, to); err != nil {
		return err
	}
	if mode != nil {
		if err := source.Chmod(*mode); err != nil {
			return err
		}
		return source.Sync()
	}
	return nil
}

func openRenameFile(parent *os.File, name string) (*os.File, error) {
	object, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: object}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE, &attrs, &windows.IO_STATUS_BLOCK{}, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_WRITE_THROUGH, 0, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), filepath.Join(parent.Name(), name)), nil
}

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [256]uint16
}

func commitRename(parent, source *os.File, to string) error {
	target, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	info := fileRenameInformation{ReplaceIfExists: 1, RootDirectory: windows.Handle(parent.Fd()), FileNameLength: uint32((len(target) - 1) * 2)}
	if len(target) > len(info.FileName) {
		return ErrUnavailable
	}
	copy(info.FileName[:], target)
	if err := windows.NtSetInformationFile(windows.Handle(source.Fd()), &windows.IO_STATUS_BLOCK{}, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), windows.FileRenameInformation); err != nil {
		return err
	}
	// 改名后刷新同一文件的全部缓存信息；屏障失败必须保留 pending/partial，不能发布 ready。
	return source.Sync()
}

func openMaterialFile(parent *os.File, name string) (*os.File, error) {
	file, err := openRenameFile(parent, name)
	// NTStatus 不参与 os.ErrNotExist 分类；在平台边界转为系统对应的 Win32 错误。
	if status, ok := err.(windows.NTStatus); ok {
		return file, status.Errno()
	}
	return file, err
}

// 删除绑定已核验的句柄；名字在核验后被换入也不会删到新文件。
func deleteMaterialHandle(file *os.File) error {
	remove := byte(1)
	return windows.SetFileInformationByHandle(windows.Handle(file.Fd()), windows.FileDispositionInfo, &remove, 1)
}
