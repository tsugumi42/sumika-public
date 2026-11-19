package gallery

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func init() {
	Register(Page{ID: "content/album-state", Area: "content", Title: "相册逐项状态", View: albumStatePage})
}

func albumStatePage(c *ui.Context) {
	type model struct {
		items []content.Media
		event string
		sizes map[string]ui.Point
	}
	s := ui.Local(c.Root(), "album-state", func() model {
		return model{items: []content.Media{
			{ID: "ready", Label: "已就绪照片", Bitmap: contentBitmap(), Width: 400, Height: 400},
			{ID: "remote", Label: "远程照片", Width: 400, Height: 400, Status: content.MediaRemote, StatusText: "点击下载"},
			{ID: "loading", Label: "正在下载", Bitmap: contentBitmap(), Width: 400, Height: 400, Status: content.MediaLoading, Progress: .35, StatusText: "35% · 点击取消"},
			{ID: "failed", Label: "下载失败", Width: 400, Height: 400, Status: content.MediaFailed, StatusText: "网络失败，点击重试"},
		}, sizes: map[string]ui.Point{}}
	})
	change := func(id string, status content.MediaStatus, label string) {
		for i := range s.items {
			if s.items[i].ID == id {
				s.items[i].Status = status
				s.items[i].StatusText = label
				s.event = id + "：" + label
			}
		}
	}
	contentPage(c, func(_ *contentDemo) {
		ui.Text(c, "每格独立下载、取消、重试；尺寸通知使用实际布局 DIP。")
		content.Album(c, s.items, content.AlbumOptions{
			OnOpen:     func(id string) { s.event = "打开 " + id },
			OnDownload: func(id string) { change(id, content.MediaLoading, "正在下载，点击取消") },
			OnCancel:   func(id string) { change(id, content.MediaRemote, "已取消，点击下载") },
			OnRetry:    func(id string) { change(id, content.MediaLoading, "正在重试，点击取消") },
			OnTileSize: func(id string, w, h float32) { s.sizes[id] = ui.Point{X: w, Y: h} },
		})
		ui.Row(c).Gap(theme.T(c).Metrics().Control().Spacing()).Children(func() {
			ui.Button(c, "完成下载").OnClick(func() {
				for i := range s.items {
					if s.items[i].Status == content.MediaLoading {
						s.items[i].Status = content.MediaReady
						s.items[i].StatusText = ""
						s.items[i].Bitmap = contentBitmap()
					}
				}
			})
			ui.Button(c, "提高进度").OnClick(func() {
				for i := range s.items {
					if s.items[i].Status == content.MediaLoading {
						s.items[i].Progress = min(1, s.items[i].Progress+.2)
					}
				}
			})
		})
		for _, item := range s.items {
			size := s.sizes[item.ID]
			ui.Text(c, fmt.Sprintf("%s：%.1f × %.1f DIP", item.ID, size.X, size.Y))
		}
		ui.Text(c, s.event)
	})
}
