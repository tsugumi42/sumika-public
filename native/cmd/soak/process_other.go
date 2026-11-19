//go:build !windows

package main

import "errors"

func processStats() (uint64, uint64, uint64, error) {
	return 0, 0, 0, errors.New("soak 的进程常驻、私有内存和句柄采样目前只支持 Windows")
}
