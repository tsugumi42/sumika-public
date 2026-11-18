//go:build !windows

package toolchain

import "os/exec"

func hideWindow(*exec.Cmd) {}
