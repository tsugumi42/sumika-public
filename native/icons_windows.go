package main

import "golang.org/x/sys/windows"

// smCxSmIcon 是 GetSystemMetrics 里系统小图标的宽（SM_CXSMICON），托盘图标就按这个尺寸画
const smCxSmIcon = 49

// iconMetrics 是取系统小图标边长的两级办法，各自取不到返回 0；拆出来是为了单独测每一级退路
type iconMetrics struct {
	forDPI func() int // GetSystemMetricsForDpi(SM_CXSMICON, GetDpiForSystem())，Windows 10 1607 起有
	plain  func() int // GetSystemMetrics(SM_CXSMICON)，不跟缩放比例
}

// smallIconSize 先问按系统 DPI 算的，取不到退到不跟缩放的，再取不到用 16
func smallIconSize(m iconMetrics) int {
	if n := m.forDPI(); n > 0 {
		return n
	}
	if n := m.plain(); n > 0 {
		return n
	}
	return 16
}

// systemIconMetrics 是真的去问 user32。用系统 DPI：托盘在主显示器的任务栏上，mygo 启动时已设成每显示器 DPI 感知
var systemIconMetrics = iconMetrics{
	forDPI: func() int {
		user32 := windows.NewLazySystemDLL("user32.dll")
		getDpi, forDpi := user32.NewProc("GetDpiForSystem"), user32.NewProc("GetSystemMetricsForDpi")
		if getDpi.Find() != nil || forDpi.Find() != nil {
			return 0
		}
		dpi, _, _ := getDpi.Call()
		if dpi == 0 {
			return 0
		}
		n, _, _ := forDpi.Call(smCxSmIcon, dpi)
		return int(n)
	},
	plain: func() int {
		metrics := windows.NewLazySystemDLL("user32.dll").NewProc("GetSystemMetrics")
		if metrics.Find() != nil {
			return 0
		}
		n, _, _ := metrics.Call(smCxSmIcon)
		return int(n)
	},
}

// traySmallIconSize 当前缩放比例下系统小图标的边长（像素）：100% 是 16，125% 是 20，150% 是 24，200% 是 32
func traySmallIconSize() int { return smallIconSize(systemIconMetrics) }

// exeHasIconResource exe 里有没有图标资源 1：mygo 的窗口类用 LoadIconW(实例, 1) 取窗口图标，这里问的是同一件事
func exeHasIconResource() bool {
	getModule := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	loadIcon := windows.NewLazySystemDLL("user32.dll").NewProc("LoadIconW")
	if getModule.Find() != nil || loadIcon.Find() != nil {
		return false
	}
	inst, _, _ := getModule.Call(0) // 0：当前进程的 exe
	if inst == 0 {
		return false
	}
	h, _, _ := loadIcon.Call(inst, 1) // MAKEINTRESOURCE(1)；资源里的图标是共享的，不用销毁
	return h != 0
}
