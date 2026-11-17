//go:build !windows

package tools

import "errors"

// 别的系统上剪贴板和默认程序打开都没有实现；Sumika 只在 Windows 上跑，这里只求编译得过、给出明确的报错

var errDesktopOnlyWindows = errors.New("剪贴板和打开文件只在 Windows 上有")

func readClipboardText() (string, bool, error) { return "", false, errDesktopOnlyWindows }
func writeClipboardText(string) error          { return errDesktopOnlyWindows }
func openDefault(string) error                 { return errDesktopOnlyWindows }
