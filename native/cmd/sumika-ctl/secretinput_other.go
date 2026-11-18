//go:build !windows

package main

import (
	"bufio"
	"errors"
	"io"
	"os"
)

// readHiddenLine 只支持从管道读：Sumika 的桌面程序和 DPAPI 只在 Windows 上，
// 这里不做各系统的终端回显开关，标准输入是终端时直接报错，免得主密码显示在屏幕上。
func readHiddenLine(_ io.Writer, in *bufio.Reader) (string, error) {
	if f, ok := secretStdin.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return "", errors.New("非 Windows 上只支持从管道读：echo 值 | sumika-ctl secrets set <名字>")
		}
	}
	return readLine(in)
}
