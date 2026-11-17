//go:build !windows

package main

import (
	"fmt"
	"time"
)

func processStats() (time.Duration, uint64, error) {
	return 0, 0, fmt.Errorf("进程 CPU 和内存峰值目前只支持 Windows")
}
