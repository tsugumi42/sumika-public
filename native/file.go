package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/content"
	"sumika-native/internal/segview"
)

func (a *app) fileBubble(c *ui.Context, m *msg, s seg, index int) {
	d := a.watchDownload(s)
	if d != nil && d.resolved != nil {
		position := s.position
		s = *d.resolved
		s.position = position
	}
	name := (segview.Segment{Kind: s.Kind, Name: s.Name, Summary: s.Summary}).DisplayName()
	switch s.FileKind {
	case "image":
		ui.Text(c, name).SingleLine()
		a.pictures(c, m, []seg{s}, false)
		return
	case "video":
		ui.Text(c, name).SingleLine()
		a.videoBubble(c, m, s, index)
		return
	}
	status, progress, note := downloadPresentation(d, false)
	if d == nil {
		note = "当前后台不能下载"
	}
	if d != nil && status == content.MediaReady {
		note = "下载完成，此类文件暂不能打开"
	}
	size := ""
	if s.Size > 0 {
		size = humanSize(s.Size)
	}
	options := content.FileOptions{Appearance: content.Appearance{Outgoing: m.mine, Disabled: d != nil && d.locked()}}
	if d != nil {
		options.OnDownload = func() { a.changeDownload(d, false) }
		options.OnRetry = options.OnDownload
		options.OnCancel = func() { a.changeDownload(d, true) }
	}
	content.File(c, content.FileData{Name: name, Size: size, Status: status, Progress: progress, StatusText: note}, options).Width(a.mediaWidth(c, false)).Label(downloadLabel(status, "文件") + "：" + name)
}
