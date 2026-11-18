//go:build !windows

package main

// closeKillJobForTest 只在 Windows 有作业对象；别的系统上调用它的测试开头就跳过，这里只为让根包测试能编译（Linux CI 上 go vet 会报 undefined）。
func closeKillJobForTest() {}
