//go:build !windows

package main

// traySmallIconSize 非 Windows 平台没有"当前缩放比例下的小图标尺寸"可问（桌面程序目前只在 Windows 上发）：
// 固定给 32，mygo 的文档说托盘图 Retina 屏上要 32×32 像素，Linux 面板会自己缩小
func traySmallIconSize() int { return 32 }

// exeHasIconResource 非 Windows 没有 .syso 这回事：窗口图标一律用 SetIcon 设
func exeHasIconResource() bool { return false }
