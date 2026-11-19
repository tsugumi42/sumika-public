//go:build !windows

package approval

import "path/filepath"

func realPath(p string) (string, error) { return filepath.EvalSymlinks(p) }
