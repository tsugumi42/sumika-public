//go:build !windows

package main

import "fmt"

func processStats() (uint64, uint64, uint64, error) {
	return 0, 0, 0, fmt.Errorf("WorkingSet、PrivateUsage 和句柄采样只支持 Windows")
}
