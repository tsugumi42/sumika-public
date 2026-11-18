package main

// 托盘图标和窗口图标（ADR-230）：图是 docs/assets/logo 里的 SVG 渲染出来的 PNG，go:embed 随 exe 带着。
// 托盘有七张：mygo 把 PNG 按原尺寸做成图标再交给系统缩放（mygo internal/windows/tray.go 的 iconFromPNG），
// 尺寸和系统要的小图标一样大才清楚，所以按当前缩放比例下系统小图标的边长挑一张，不是缩一张大的。

import (
	"embed"
	"fmt"

	"github.com/egoist/mygo"
)

//go:embed assets/tray/tray-*.png
var trayFS embed.FS

//go:embed assets/icon/icon-256.png
var windowIconPNG []byte

// traySizes 是嵌进来的托盘图的边长，从小到大；文件是 assets/tray/tray-<边长>.png。
// 加减尺寸时和文件一起改（icons_test.go 两头对）
var traySizes = []int{16, 20, 24, 32, 40, 48, 64}

// pickTraySize 挑托盘图：有一样大的就用它，没有就用比它大的里最小的（缩小比放大清楚），都比它小就用最大的。
// want 小于等于 0（取不到尺寸）按最小的算
func pickTraySize(want int) int {
	for _, n := range traySizes {
		if n >= want {
			return n
		}
	}
	return traySizes[len(traySizes)-1]
}

// trayIcon 是托盘图标的 PNG：按当前缩放比例下系统小图标的边长挑
func trayIcon() []byte {
	b, err := trayFS.ReadFile(fmt.Sprintf("assets/tray/tray-%d.png", pickTraySize(traySmallIconSize())))
	if err != nil {
		return nil // 编译时就嵌好了，traySizes 对不上文件由测试拦；万一没有，托盘建出来没有图标，不影响别的
	}
	return b
}

// setWindowIcon 给所有窗口（已有的和以后开的）设窗口和任务栏的图标，界面线程调。
// exe 本身带着图标资源（rsrc_windows_*.syso，里面每个尺寸一张）时什么也不做：窗口类自己从资源里按系统要的尺寸挑，
// 标题栏、任务栏、Alt+Tab 各用各的那一张；SetIcon 只收一张 PNG、小图标大图标都是它，系统缩小起来边缘有锯齿。
// 2026-11-02 在 125% 缩放的机器上画到 16～48 像素对过：资源里取的那张六个尺寸都更接近同尺寸的原图（差距不大，见 ADR-230）。
// 没带资源的构建（别的架构、改了链接方式）才用 256 那张顶上
func setWindowIcon() error {
	if exeHasIconResource() {
		return nil
	}
	return mygo.App.Dock.SetIcon(windowIconPNG)
}
