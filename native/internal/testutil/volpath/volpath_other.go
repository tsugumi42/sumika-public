//go:build !windows

package volpath

import "errors"

// GUIDPath：卷标识写法是 Windows 的，别的系统没有
func GUIDPath(string) (string, error) { return "", errors.New("卷标识路径只有 Windows 有") }
