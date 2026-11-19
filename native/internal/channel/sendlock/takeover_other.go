//go:build !windows

package sendlock

import (
	"context"
	"errors"
	"syscall"
)

func bunStopped(context.Context) (bool, error) {
	return false, errors.New("当前系统尚不能核对 Bun 进程")
}
func connectionRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
