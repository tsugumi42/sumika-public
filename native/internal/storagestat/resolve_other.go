//go:build !windows

package storagestat

import "path/filepath"

func realPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
