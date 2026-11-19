package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/control"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/theme"
	"image"
	"slices"
	"time"

	"sumika-native/internal/segview"
)

// Telegram 相册布局最多十项；长组切成连续相册，沿用原始段身份。
const albumLimit = 10

// 20 张 40/100ms 混合贴纸须满足 96MiB/s 上传预算，显示尺寸仍由库决定。
const stickerFrameMax = 256

func imgMaxW(c *ui.Context) float32 { return theme.T(c).Metrics().Media().MaxSize() }
func isSticker(s seg) bool          { return s.Sticker || s.Kind == "emoji" && s.Media != nil }

// 现行 lightbox 的加载占位；不将它传给新 Viewer 伪装 Ready。
var emptyBitmap = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 1, 1)))

func (a *app) mediaWidth(c *ui.Context, naked bool) float32 {
	if !naked {
		return a.envFor(c).bodyW
	}
	width := a.list.Bounds(c).W
	if layer := a.forward.rendering; layer != nil {
		width = layer.list.Bounds(c).W
	}
	if width == 0 {
		return imgMaxW(c)
	}
	m := theme.T(c).Metrics().Message()
	return max(1, min(imgMaxW(c), width-m.MarginLeft()-m.MarginRight()))
}

type imageGroupState struct {
	message  *msg
	segments []seg
}

func (a *app) imageGroup(c *ui.Context, m *msg, views []segview.Segment, naked bool) {
	root := ui.Column(c).MinWidth(0)
	root.Children(func() {
		state := ui.Local(root, "segments", func() imageGroupState { return imageGroupState{} })
		if state.message != m {
			state.message = m
			state.segments = slices.Grow(state.segments[:0], len(views))[:len(views)]
			for i, view := range views {
				state.segments[i] = m.segs[view.Position]
			}
		}
		imgs := state.segments
		for i := 0; i < len(imgs); {
			c.Key(imgs[i].position)
			if isSticker(imgs[i]) {
				a.stickerView(c, m, imgs[i], naked)
				i++
				continue
			}
			j := i
			for j < len(imgs) && !isSticker(imgs[j]) {
				j++
			}
			a.pictures(c, m, imgs[i:j], naked)
			i = j
		}
	})
}
func mediaDimensions(s seg, fallback float32) (float32, float32) {
	if s.Width > 0 && s.Height > 0 {
		return float32(s.Width), float32(s.Height)
	}
	return fallback, fallback * 3 / 4
}
func imagePresentation(e *imgEntry) (content.MediaStatus, string) {
	switch e.state {
	case imgOK:
		return content.MediaReady, ""
	case imgFailed:
		return content.MediaFailed, "图片没取到，点一下再试"
	case imgBad:
		return content.MediaFailed, "图片打不开"
	}
	return content.MediaLoading, ""
}
func mediaUIID(m *msg, s seg) string { return fmt.Sprintf("%s:image:%d", m.id, s.position) }
func imageBottom(c *ui.Context, m *msg, naked bool) func() {
	if !naked {
		return nil
	}
	side := chat.Incoming
	if m.mine {
		side = chat.Outgoing
	}
	return func() {
		chat.ImageInfo(c, chat.Info{Label: time.UnixMilli(m.at).Format("15:04")}, chat.BodyOptions{Side: side})
	}
}

type pictureCells struct {
	items []content.Media
	shown []*imgEntry
	keys  []imgKey
}

func (a *app) pictures(c *ui.Context, m *msg, imgs []seg, naked bool) {
	st := a.is()
	width := a.mediaWidth(c, naked)
	host := ui.Column(c).MinWidth(0).Gap(theme.T(c).Metrics().Preview().MediaGap())
	a.probeOf("pic:"+m.id+":"+imgs[0].mediaHash(), host)
	host.Children(func() {
		open := ui.Local(host, "open", func() bool { return false })
		at := ui.Local(host, "at", func() int { return 0 })
		view := ui.Local(host, "view", func() viewState { return viewState{} })
		cells := ui.Local(host, "cells", func() pictureCells { return pictureCells{} })
		cells.items = slices.Grow(cells.items[:0], len(imgs))[:len(imgs)]
		cells.shown = slices.Grow(cells.shown[:0], len(imgs))[:len(imgs)]
		cells.keys = slices.Grow(cells.keys[:0], len(imgs))[:len(imgs)]
		items, shown, keys := cells.items, cells.shown, cells.keys
		for i, s := range imgs {
			w, h := mediaDimensions(s, width)
			metrics := theme.T(c).Metrics().Media()
			limit := metrics.MaxSize()
			if s.Animated {
				limit = metrics.MaxGifSize()
			}
			// 读取宽度跟随组件的缩放及正文最小宽，避免竖图和 GIF 多解一档。
			css := min(width, max(metrics.MinPhotoSize(), w*min(1, limit/max(w, h))))
			if !naked {
				css = min(width, max(css, metrics.BubbleMinWidth()))
			}
			if len(imgs) > 1 {
				css = coverCSS(width, s)
			}
			keys[i] = keyOf(s, css, false, st.scale)
			e := st.getRef(keys[i], refOf(s))
			shown[i] = e
			a.play(c, e)
			label := s.Summary
			if label == "" {
				label = "图片"
				if len(imgs) > 1 {
					label = fmt.Sprintf("图片 %d", i+1)
				}
			}
			status, text := imagePresentation(e)
			items[i] = content.Media{ID: mediaUIID(m, s), Label: label, Bitmap: e.bmp, Width: w, Height: h, Status: status, StatusText: text}
		}
		activate := func(i int) {
			if shown[i].state == imgOK {
				*open, *at = true, i
				host.SetChanged()
				playFeedback(feedbackOpen)
			} else if shown[i].state == imgFailed {
				st.retry(keys[i])
			}
		}
		for start := 0; start < len(items); start += albumLimit {
			end := min(start+albumLimit, len(items))
			c.Key(start)
			bottom := imageBottom(c, m, naked && end == len(items))
			if end-start == 1 {
				i := start
				options := content.MediaOptions{Width: width, Appearance: content.Appearance{Outgoing: m.mine}, InBubble: !naked, OnOpen: func() { activate(i) }, BottomInfo: bottom, LastOutgoing: m.mine}
				if shown[i].state == imgFailed {
					options.OnRetry = func() { st.retry(keys[i]) }
				}
				if imgs[i].Animated {
					content.GIF(c, items[i], options)
				} else {
					content.Photo(c, items[i], options)
				}
			} else {
				processing := false
				for _, item := range items[start:end] {
					processing = processing || item.Status != content.MediaReady
				}
				content.Album(c, items[start:end], content.AlbumOptions{Width: width, Appearance: content.Appearance{Outgoing: m.mine}, BottomInfo: bottom, LastOutgoing: m.mine, Processing: processing, OnOpen: func(id string) {
					for i := start; i < end; i++ {
						if items[i].ID == id {
							activate(i)
							return
						}
					}
				}})
				// 网络失败保留逐项重试入口，坏格式只说明且不重复读取。
				for i := start; i < end; i++ {
					if status, text := imagePresentation(shown[i]); status == content.MediaFailed {
						label := fmt.Sprintf("图片 %d：%s", i+1, text)
						if shown[i].state == imgFailed {
							control.LinkButton(c.Key(items[i].ID), label, control.LinkButtonOptions{OnActivate: func() { st.retry(keys[i]) }})
						} else {
							ui.Text(c, label).Wrap()
						}
					}
				}
			}
		}
		if *open {
			if a.forward.rendering != nil {
				a.forward.viewerUp = true
			}
			if imgs[*at].Animated && shown[*at].state == imgOK {
				mode := playLive
				if c.Preferences().ReduceMotion {
					mode = playStill
				}
				st.advance(c, shown[*at], mode)
			}
			label := imgs[0].Summary
			if label == "" {
				label = "图片"
			}
			if lightbox(c, label, viewImages(imgs, a.largeOf(imgs, shown, true, *at)), at, open, view).Changed() {
				host.SetChanged()
				playFeedback(feedbackClose)
			}
		} else {
			*view = viewState{}
		}
	})
}
func (a *app) stickerView(c *ui.Context, m *msg, s seg, naked bool) {
	st := a.is()
	limit := theme.T(c).Metrics().Media().MaxStickerSize()
	w, h := mediaDimensions(s, limit)
	if s.Width <= 0 || s.Height <= 0 {
		w, h = limit, limit
	}
	k := keyOf(s, min(w, limit*w/max(w, h)), true, st.scale)
	if s.Animated {
		k.tier = min(k.tier, stickerFrameMax)
	}
	e := st.getRef(k, refOf(s))
	a.play(c, e)
	status, text := imagePresentation(e)
	options := content.MediaOptions{Width: a.mediaWidth(c, naked), Appearance: content.Appearance{Outgoing: m.mine}, BottomInfo: imageBottom(c, m, naked), LastOutgoing: m.mine}
	if e.state == imgFailed {
		options.OnRetry = func() { st.retry(k) }
	}
	root := content.Sticker(c, content.Media{ID: mediaUIID(m, s), Label: "表情包", Bitmap: e.bmp, Width: w, Height: h, Status: status, StatusText: text}, options)
	if s.Summary != "" {
		ui.Overlay(c, func() { overlay.Tooltip(c, root, s.Summary, overlay.TooltipOptions{}) })
	}
}

// 现行查看器只取当前及前后原图；动图共用聊天缓存和播放产物。
func (a *app) largeOf(imgs []seg, shown []*imgEntry, open bool, at int) []*ui.Bitmap {
	st := a.is()
	out := make([]*ui.Bitmap, len(imgs))
	for i, s := range imgs {
		out[i] = emptyBitmap
		if i < len(shown) && shown[i] != nil && shown[i].state == imgOK {
			out[i] = shown[i].bmp
		}
		if s.Animated {
			if open && abs(i-at) <= 1 && i < len(shown) && shown[i] != nil && shown[i].state == imgOK {
				out[i] = shown[i].stillBitmap()
				if i == at {
					out[i] = shown[i].bmp
				}
			}
			continue
		}
		k := imgKey{mediaID(s), lightboxTier, false}
		var e *imgEntry
		if open && abs(i-at) <= 1 {
			e = st.getRef(k, refOf(s))
		} else {
			e = st.peek(k)
		}
		if e != nil && e.state == imgOK {
			out[i] = e.bmp
		}
	}
	return out
}
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// 播放只跟随当前列表的可见行，减少动态效果只显示首帧。
func (a *app) play(c *ui.Context, e *imgEntry) {
	if e.anim == nil {
		return
	}
	mode := playLive
	switch {
	case c.Preferences().ReduceMotion:
		mode = playStill
	case !a.rowVisible():
		mode = playHold
	}
	a.is().advance(c, e, mode)
}
func (a *app) rowVisible() bool {
	if layer := a.forward.rendering; layer != nil {
		first, last := layer.list.Visible()
		return a.forward.rowAt >= first && a.forward.rowAt <= last
	}
	st := a.is()
	if a.visFrame != st.frame {
		a.visFrame = st.frame
		a.visFirst, a.visLast = a.list.Visible()
	}
	return a.rowAt >= a.visFirst && a.rowAt <= a.visLast
}
