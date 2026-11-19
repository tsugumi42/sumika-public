//go:build !windows

package main

// openFiles 只在 Windows 上查：别的系统删开着的文件不影响删目录。
func openFiles(string) []string { return nil }
