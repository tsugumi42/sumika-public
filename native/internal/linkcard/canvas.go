package linkcard

import (
	"image"
	"sync"

	"github.com/egoist/mygo/ui"
)

// ui.Render 每次都新建一个 Tester，Tester 自带一套文字系统，会把注册过的字体（文渊圆体、Maple，几十 MB）
// 通过 DirectWrite 全部再装一遍，而且 mygo 没有释放接口：每画一张卡漏一套，本机量过每张约 86 MB，
// 画 60 张涨到 5.4 GB，正式运行时一样（上游 egoist/mygo#1902，修复 #1903 还没合）。
// 所以卡片统一画在这一块长期存在的离屏画布上：整个进程只建一个 Tester，每张卡改尺寸、改倍数后重画，
// 用完把像素拷出来。画布不能并发，用一把锁排队；Renderer 的 worker 本来就是一张一张画。
// 画布里只有 mygo 自己的文字系统和字体表，卡片内容、主题、尺寸都由每次传进来的 view 决定，不留到下一张。
//
// 临时：等 mygo 合了 #1903（Tester 文字系统不可达时自动关引擎），升级后可以改回每次 ui.Render；到时整个文件删掉。
type canvas struct {
	mu    sync.Mutex
	tt    *ui.Tester
	view  func(c *ui.Context) // 这一张的画法；Tester 的 view 固定指向它
	w, h  int
	scale float32
}

var offscreen canvas

// renderOffscreen 同 ui.Render：在 w×h（逻辑像素）、每个逻辑像素 scale 个设备像素的窗口里画一帧 view，
// 返回的图是拷出来的，画布接着画下一张也不会改它。
func renderOffscreen(view func(c *ui.Context), w, h int, scale float32) *image.RGBA {
	return offscreen.render(view, w, h, scale)
}

func (cv *canvas) render(view func(c *ui.Context), w, h int, scale float32) *image.RGBA {
	cv.mu.Lock()
	defer cv.mu.Unlock()
	done := false
	defer func() {
		if !done { // view 里 panic（比如重复的 key）：画布状态不可信，下一张换新的
			cv.tt = nil
		}
	}()
	cv.view = view
	switch {
	case cv.tt == nil:
		cv.tt = ui.NewTester(func(c *ui.Context) { cv.view(c) }, w, h)
		cv.w, cv.h, cv.scale = w, h, 1
		cv.setScale(scale)
	default:
		resized := false
		if w != cv.w || h != cv.h {
			cv.tt.SetSize(w, h)
			cv.w, cv.h = w, h
			resized = true
		}
		if scale != cv.scale {
			cv.setScale(scale)
		} else if !resized {
			cv.tt.Frame()
		}
	}
	src := cv.tt.Image()
	out := image.NewRGBA(src.Rect)
	copy(out.Pix, src.Pix)
	done = true
	return out
}

func (cv *canvas) setScale(scale float32) {
	cv.tt.SetScale(scale)
	cv.scale = scale
}
