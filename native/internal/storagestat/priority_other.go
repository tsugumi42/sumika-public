//go:build !windows

package storagestat

func lowerPriority() (restore func()) { return func() {} }
