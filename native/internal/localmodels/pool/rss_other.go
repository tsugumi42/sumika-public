//go:build !windows

package pool

import "errors"

// ProcessRSS 这一版只在 Windows 上实现（macOS 这一版不测，规格 1）。读不到时池不对账，只按估计记账。
func ProcessRSS() (uint64, error) {
	return 0, errors.New("这个系统上还没实现读进程常驻内存")
}
