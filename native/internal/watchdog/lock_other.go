//go:build !windows

package watchdog

// AcquireLock 只有 Windows 上做真的互斥；别的系统不守护，直接给
func AcquireLock(name string) (release func(), ok bool, err error) { return func() {}, true, nil }
