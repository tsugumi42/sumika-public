//go:build !windows

package app

import "syscall"

// errConnRefused 在 Windows 以外就是 ECONNREFUSED，见 probe_windows.go。
const errConnRefused = syscall.ECONNREFUSED
