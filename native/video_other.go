//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

func systemOpenVideoFile(path string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, path).Run()
}
