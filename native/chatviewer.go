package main

import (
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// viewLoadingDelay 是原图还没好时，等多久才在角落出现加载指示。
// 取经验值 300 毫秒：人对"立刻响应"的感知上限在 0.1 秒量级、1 秒内还觉得连贯（Nielsen 的响应时间三档），
// 缓存命中、局域网取图通常几十毫秒就好，这段时间里出现任何新元素都只是一闪；超过 300 毫秒才说明确实要等。
// Material 对加载指示的"延迟显示"建议也在这个量级。
const viewLoadingDelay = 300 * time.Millisecond

// chatLightbox 是新视图的看大图。旧界面仍用行内的 open 状态；这里只服务 chatui 的点击。
//
// 看大图的寿命只跟"窗口级"的东西走：会话（gen）、数据源（srcGen）、转发面板（layer、forwardGen），
// 不跟消息行走。消息行会因为新消息进来、版本号变、虚拟列表回收而被重建，行对象的指针也会换；
// 原来用 chatMedia.valid 去核对"这一行还是不是同一个对象"，动图下载、播放期间一刷新查看器就被关掉。
// 图用稳定的 msgID 和每段的 position 认，位图从共享的图片缓存（images）里取。
type chatLightbox struct {
	msgID      string
	segments   []seg
	at         int
	open       bool
	state      viewState
	images     *imageStore
	gen        uint64
	srcGen     uint64
	layer      *forwardLayer
	forwardGen uint64
	waitSince  time.Time // 原图第一次没好的时刻（窗口帧的时钟）；零值是现在已经好了
}

// alive 看大图还该不该开着：只在换了会话、换了数据源、或者它所在的转发面板被关掉 / 被盖住时才不该。
func (v *chatLightbox) alive(a *app) bool {
	if a == nil || v.images == nil || v.gen != a.gen || v.srcGen != a.srcGen {
		return false
	}
	if v.layer != nil {
		f := a.forward
		return f.open && v.forwardGen == f.gen && len(f.stack) > 0 && f.stack[len(f.stack)-1] == v.layer
	}
	return true
}

func (a *app) chatImageWindow(c *ui.Context) {
	v := &a.chatLightbox
	if !v.open {
		return
	}
	if !v.alive(a) {
		v.open = false
		return
	}
	st := v.images
	shown := make([]*imgEntry, len(v.segments))
	for i, s := range v.segments {
		k := keyOf(s, 480, isSticker(s), st.scale)
		if s.Animated && isSticker(s) {
			k.tier = min(k.tier, stickerFrameMax)
		}
		if abs(i-v.at) <= 1 {
			shown[i] = st.getRef(k, refOf(s))
		} else if e := st.peek(k); e != nil {
			shown[i] = e
		}
	}
	if v.at < 0 || v.at >= len(v.segments) {
		v.open = false
		return
	}
	// 渐进加载：先用已经有的最好版本（列表里的缩略图放到同样大小），原图好了原地换，尺寸不变；
	// 不弹"正在准备"的浮层。超过 viewLoadingDelay 还没好，才在角落出一个小指示。
	ready := shown[v.at] != nil && shown[v.at].state == imgOK
	failed := shown[v.at] != nil && (shown[v.at].state == imgFailed || shown[v.at].state == imgBad)
	if ready {
		v.waitSince = time.Time{}
	} else if v.waitSince.IsZero() {
		v.waitSince = c.Now()
	}
	if ready && v.segments[v.at].Animated {
		a.play(c, shown[v.at])
	}
	bmps := a.largeOf(v.segments, shown, true, v.at)
	if !ready {
		if b := standIn(st, v.segments[v.at]); b != nil {
			bmps[v.at] = b
		}
	}
	imgs := make([]viewImage, len(bmps))
	for i, bmp := range bmps {
		imgs[i] = viewImage{bmp: bmp, w: v.segments[i].Width, h: v.segments[i].Height}
	}
	label := v.segments[v.at].Summary
	if label == "" {
		label = "图片"
	}
	lightbox(c, label, imgs, &v.at, &v.open, &v.state)
	if v.open && !ready {
		a.viewerWait(c, v, failed)
	}
	if v.layer != nil {
		a.forward.viewerUp = v.open
	}
}

// standIn 原图没好时先放的位图：列表里这张图已经取到的最大一档（动图取第一帧）；一档都没有就是 nil。
func standIn(st *imageStore, s seg) *ui.Bitmap {
	var best *ui.Bitmap
	var bestPx int
	for _, w := range []float32{480, 320, 256, 240, 128} {
		k := keyOf(s, w, isSticker(s), st.scale)
		e := st.peek(k)
		if e == nil || e.state != imgOK || e.bmp == nil {
			continue
		}
		b := e.stillBitmap()
		if bw, _ := b.Size(); bw > bestPx {
			best, bestPx = b, bw
		}
	}
	return best
}

// viewerWait 画在大图右下角的小指示：不盖住图、不拦指针（失败图标例外，要能悬停看原因）。
// 原图没好但没到 viewLoadingDelay：什么都不画，只排一次到点重画。
func (a *app) viewerWait(c *ui.Context, v *chatLightbox, failed bool) {
	elapsed := c.Now().Sub(v.waitSince)
	if !failed && elapsed < viewLoadingDelay {
		c.After(viewLoadingDelay - elapsed)
		return
	}
	const size = 28
	ui.Overlay(c, func() {
		if failed {
			box := ui.Box(c.Key("view-fail")).Label("原图加载失败").Absolute().End(16).Bottom(16).Size(size, size).Center().Radius(size / 2).
				Background(ui.RGBA(0, 0, 0, 0.5)).Tooltip("原图没有取到，关掉再点开会重试")
			box.Children(func() {
				ui.Icon(c, chatui.Icon("alert", true)).Size(18, 18).TextColor(ui.RGB(255, 255, 255))
			})
			return
		}
		ui.Box(c.Key("view-wait")).Label("正在加载原图").Absolute().End(16).Bottom(16).Size(size, size).Center().Radius(size / 2).
			Background(ui.RGBA(0, 0, 0, 0.35)).PassThrough().Children(func() {
			ui.Spinner(c).Size(18, 18)
		})
	})
}
