//go:build !windows

package policy

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
