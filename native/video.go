package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/theme"
	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

// 验收替换系统打开动作，不启动真实播放器。
var openVideoFile = systemOpenVideoFile

type videoOpenState struct {
	busy   bool
	reason string
}

func videoDuration(ms int64) string {
	seconds := (ms + 999) / 1000
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
func videoNote(s seg) string {
	state := s.FetchState
	if state == "" {
		state = s.MediaState
	}
	switch state {
	case store.MediaFailed, store.MediaExpired:
		return "视频取不到，尚未下载"
	case store.MediaBlocked:
		return "视频地址不在允许的主机里，尚未下载"
	case store.MediaNotVideo:
		return "收到的文件不是支持的视频"
	case store.MediaPending:
		return "视频正在下载"
	case store.MediaTooBig:
		if s.Size > 0 {
			return "视频较大（" + humanSize(s.Size) + "），尚未下载"
		}
		return "视频较大，尚未下载"
	}
	return "视频还没下载"
}

func (a *app) videoBubble(c *ui.Context, m *msg, s seg, index int) {
	d := a.watchDownload(s)
	if d != nil && d.resolved != nil {
		s = *d.resolved
	}
	if d == nil && s.Media == nil {
		ui.Text(c, videoNote(s)).Wrap().Opacity(.7)
		return
	}
	c.Key(index)
	host := ui.Column(c).MinWidth(0).Gap(theme.T(c).Metrics().Preview().MediaGap()).MaxWidth(a.mediaWidth(c, false))
	host.Children(func() {
		state := ui.Local(host, "open", func() videoOpenState { return videoOpenState{} })
		width := a.mediaWidth(c, false)
		// 固有占位保留元数据宽高比，不使用首帧尚未量出的列表宽度。
		w := theme.T(c).Metrics().Media().MaxSize()
		h := w * 9 / 16
		if s.Width > 0 && s.Height > 0 {
			h = w * float32(s.Height) / float32(s.Width)
		}
		status, progress, note := downloadPresentation(d, s.Media != nil)
		label := downloadLabel(status, "视频")
		if status == content.MediaReady {
			label = "播放视频"
		}
		media := content.Media{ID: fmt.Sprintf("%s:video:%d", m.id, index), Label: label, Width: w, Height: h, Status: status}
		if s.Cover != nil {
			cover := segFromDisplay(api.DisplaySegment{Kind: "image", Media: s.Cover, Width: s.Width, Height: s.Height})
			st := a.is()
			e := st.getRef(keyOf(cover, width, false, st.scale), *s.Cover)
			if e.state == imgOK {
				media.Bitmap = e.bmp
			}
		}
		media.Progress, media.StatusText = progress, note
		duration := "视频"
		if s.DurationMS > 0 {
			duration = videoDuration(s.DurationMS)
		}
		options := content.MediaOptions{Appearance: content.Appearance{Outgoing: m.mine, Disabled: state.busy || d != nil && d.locked()}, Width: width, InBubble: true}
		if s.Media != nil {
			ref := *s.Media
			options.OnOpen = func() { a.playVideo(ref, state) }
		}
		if d != nil {
			options.OnDownload = func() { a.changeDownload(d, false) }
			options.OnRetry = options.OnDownload
			options.OnCancel = func() { a.changeDownload(d, true) }
		}
		// 播放标记照换库前的样子一直显示（MewTypeUI ShowPlay 改成显式开关以后）
		root := content.Video(c, media, duration, content.VideoOptions{MediaOptions: options, ShowPlay: true})
		a.probeOf("video:"+m.id+":"+s.mediaHash(), root)
		if state.busy {
			footnote(c, "正在打开视频…", m.mine)
		}
		if state.reason != "" {
			footnote(c, state.reason, m.mine)
		}
	})
}
func (a *app) playVideo(ref api.MediaRef, state *videoOpenState) {
	reader, ok := a.reader.(api.VideoReader)
	if !ok {
		state.reason = "当前后台不能打开视频"
		return
	}
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	state.busy, state.reason = true, ""
	open := openVideoFile
	go func() {
		path, err := reader.PrepareVideo(ctx, ref)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = open(path)
		}
		a.post(func() {
			if ctx.Err() != nil {
				return
			}
			state.busy = false
			if err != nil {
				state.reason = "视频打不开，点一下再试"
			}
		})
	}()
}
