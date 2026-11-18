//go:build windows

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// readConsoleUnits 读标准输入挂接的控制台的一行：先关掉回显（值、主密码、恢复码不显示在屏幕上），
// 读完恢复原来的模式。控制台按 UTF-16 宽字符给输入，不是本地代码页（中文 Windows 上默认 GBK）的字节。
// 标准输入是管道、文件时第二项返回 false，调用方改走 readLine 按字节读。测试里换成注入的输入。
var readConsoleUnits = func(stderr io.Writer) ([]uint16, bool, error) {
	f, ok := secretStdin.(*os.File)
	if !ok {
		return nil, false, nil
	}
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return nil, false, nil
	}
	if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return nil, false, fmt.Errorf("关掉输入回显失败：%w", err)
	}
	units, err := readConsoleWide(h)
	// 回显关着，回车也不会被显示：换一行，后面的提示和结果从新行开始。
	if stderr != nil {
		fmt.Fprintln(stderr)
	}
	if cerr := windows.SetConsoleMode(h, mode); cerr != nil && err == nil {
		err = fmt.Errorf("恢复输入回显失败：%w", cerr)
	}
	return units, true, err
}

// readConsoleWide 一段一段读，拼到换行为止：比缓冲区长的行不会只剩前半截。
func readConsoleWide(h windows.Handle) ([]uint16, error) {
	var units []uint16
	buf := make([]uint16, 256)
	for {
		var n uint32
		if err := windows.ReadConsole(h, &buf[0], uint32(len(buf)), &n, nil); err != nil {
			return nil, fmt.Errorf("读控制台出错：%w", err)
		}
		units = append(units, buf[:n]...)
		if n == 0 || units[len(units)-1] == '\n' {
			return units, nil
		}
	}
}

// readHiddenLine 读一行。标准输入是控制台时按宽字符读、转成 UTF-8；是管道、文件时按字节读。
func readHiddenLine(stderr io.Writer, in *bufio.Reader) (string, error) {
	units, isConsole, err := readConsoleUnits(stderr)
	if err != nil {
		return "", err
	}
	if !isConsole {
		return readLine(in)
	}
	if len(units) == 0 {
		return "", errNoInput
	}
	line, ok := utf16ToString(units)
	if !ok {
		return "", errors.New("控制台读到的文本里有落单的代理项，不是完整的 Unicode：重新输入一遍")
	}
	return line, nil
}

// utf16ToString 把控制台读到的宽字符转成字符串；落单的代理项返回 false：
// 不悄悄换成替换字符存下去（和管道读到的非法 UTF-8 一样按非法输入拒掉）。
func utf16ToString(units []uint16) (string, bool) {
	for i := 0; i < len(units); i++ {
		switch u := units[i]; {
		case u >= 0xD800 && u <= 0xDBFF:
			if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] > 0xDFFF {
				return "", false
			}
			i++
		case u >= 0xDC00 && u <= 0xDFFF:
			return "", false
		}
	}
	return string(utf16.Decode(units)), true
}
