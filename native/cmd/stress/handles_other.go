//go:build !windows

package main

import "fmt"

func threadCount() (int, error) { return 0, fmt.Errorf("线程采样只支持 Windows") }
func snapshotHandles() (handleSnapshot, error) {
	return handleSnapshot{}, fmt.Errorf("句柄分类只支持 Windows")
}
