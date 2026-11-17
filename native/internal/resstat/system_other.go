//go:build !windows

package resstat

import (
	"errors"
	"runtime"
)

// 非 Windows 的空实现：能编译，进程表只剩主程序自己，数值都留空（nil），不编

type otherSystem struct{}

func newSystem() system { return otherSystem{} }

func (otherSystem) list() ([]procEntry, error) {
	return nil, errors.New("这个系统没有实现进程列表")
}
func (otherSystem) info(int) (procInfo, error) {
	return procInfo{}, errors.New("这个系统没有实现进程读数")
}
func (otherSystem) creation(int) int64 { return 0 }

type noGPU struct{}

func newGPU() GPUSource { return noGPU{} }

func (noGPU) Read() (GPUReading, error) {
	return GPUReading{}, errors.New("这个系统（" + runtime.GOOS + "）没有显存计数器")
}
func (noGPU) Close() {}
