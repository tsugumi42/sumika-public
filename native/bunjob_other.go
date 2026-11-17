//go:build !windows

package main

// 临时：Electrobun 接管进程后去掉（见 bunchild.go）。
// 非 Windows 没有作业对象这一套：sumika.exe 正常退出时由监护的 stop 杀子进程；崩溃时子进程不保证跟着走（本机是 Windows，这里只求能编过）。

import (
	"os"
	"os/exec"
)

func assignToKillJob(*os.Process) error { return nil }

func hideChildWindow(*exec.Cmd) {}
