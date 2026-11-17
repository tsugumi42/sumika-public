//go:build !windows

package main

import (
	"errors"
	"time"
)

// 资源验收只实现 Windows；不能用零值假装量过其他平台。
func issue17ProcessStats() (time.Duration, uint64, uint64, error) {
	return 0, 0, 0, errors.New("issue17 资源量测目前只支持 Windows")
}
