//go:build !windows

package checkpoint

import (
	"errors"
	"os"
)

func durableRename(root *os.Root, from, to string) error {
	if err := root.Rename(from, to); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func publishRestoreTemp(root *os.Root, from, to string, mode os.FileMode) (err error) {
	file, err := root.OpenFile(from, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if err := durableRename(root, from, to); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	return file.Sync()
}
