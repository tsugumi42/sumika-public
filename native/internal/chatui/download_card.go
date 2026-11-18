package chatui

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// 视频和文件消息（照 Telegram）：
//   - 视频：封面铺满气泡（规则同图片）；中间一个半透明圆按钮——没下载是下载图标、下载中是带进度的圆环（可点取消）、
//     下好了是播放图标、失败是重试；左上角半透明小牌子写“时长 · 大小”（下载中写“已下载 / 总大小”）；
//     时间牌子在右下（同图片）。没有“视频”标题行，也没有通栏下载按钮。
//   - 文件：左边圆形图标（下载中变进度圆环），右边文件名和人能读的大小。
//
// 后台接口不变：状态和动作仍来自 MediaResolver.Download，点击仍走 OnDownload / OnCancelDownload / OnOpenMedia。

// humanBytes 是人能读的大小：1024 进制，最多一位小数。
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
}

func humanDuration(ms int64) string {
	s := ms / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// mediaAction 一个下载类卡片现在能做什么：按钮的名字、图标和点击动作。
func (h Host) mediaAction(result MediaResult, s segview.Segment, kind string) (label, icon string, action func(string, int)) {
	label, icon, action = "下载", "download", h.OnDownload
	switch {
	case result.State == MediaDownloading:
		label, icon, action = "取消下载", "close", h.OnCancelDownload
	case result.State == MediaDownloaded || result.State == MediaReady:
		label, icon, action = "打开", "file", h.OnOpenMedia
		if kind == "视频" {
			icon = "play"
		}
	case result.State == MediaFailed:
		label, icon = "重试下载", "download"
	}
	usable := s.CanDownload || result.State == MediaDownloaded || result.State == MediaReady || result.State == MediaDownloading
	if !usable {
		action = nil
	}
	if kind == "视频" {
		switch label {
		case "打开":
			label = "播放视频"
		default:
			label += kind
		}
	}
	return
}

// progressRing 在圆形按钮上画下载进度：从 12 点钟方向顺时针画 ratio 圈；ratio < 0 是不知道总量，画固定的四分之一弧。
// 弧自己用折线算（每 4 度一段），起点和方向不依赖路径裁剪的约定。
func progressRing(p *ui.Painter, r ui.Rect, ratio float32, width float32, c ui.Color) {
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	rad := min(r.W, r.H)/2 - width/2 - 2
	sweep := float64(max(0.02, min(1, ratio))) * 2 * math.Pi
	if ratio < 0 {
		sweep = math.Pi / 2
	}
	n := max(2, int(sweep/(4*math.Pi/180)))
	pts := make([][2]float32, 0, n+1)
	for i := 0; i <= n; i++ {
		a := -math.Pi/2 + sweep*float64(i)/float64(n)
		pts = append(pts, [2]float32{cx + rad*float32(math.Cos(a)), cy + rad*float32(math.Sin(a))})
	}
	p.StrokePolyline(pts, width, c)
}

// circleButton 是视频、文件卡片上的圆形主按钮。
func (h Host) circleButton(c *ui.Context, key string, size float32, label, icon string, ratio float32, ring bool, action func(), bg, fg ui.Color) {
	b := ui.ButtonBase(c.Key(key)).Label(label).Size(size, size).Radius(size / 2).Background(bg).Center().Shrink(0)
	if action != nil {
		b.OnClick(action)
	}
	if ring {
		b.Draw(func(p *ui.Painter, r ui.Rect) { progressRing(p, r, ratio, 2.5, fg) })
	}
	b.Children(func() {
		ui.Icon(c.Key("glyph"), Icon(icon, true)).Size(size*0.42, size*0.42).TextColor(fg)
	})
}

// videoCard 画视频封面卡：w×ht 是卡片尺寸，radius 是自己的圆角（铺满气泡时 0，由气泡裁出圆角）。
func (h Host) videoCard(c *ui.Context, id string, s segview.Segment, pos int, w, ht, radius float32) {
	t := h.T
	result := MediaResult{Progress: -1}
	if h.Media != nil {
		result = h.Media.Download(id, pos)
	}
	cover := MediaResult{}
	if h.Media != nil && s.HasCover {
		cover = h.Media.Image(id, pos, true, int(w))
	}
	label, icon, action := h.mediaAction(result, s, "视频")
	white := ui.RGB(255, 255, 255)
	shade := ui.RGBA(0, 0, 0, 0.5)
	box := ui.Box(c.Key("video")).Label("视频").Size(w, ht).Shrink(0).Background(t.C.Surface).Radius(radius)
	if radius > 0 { // 铺满气泡时圆角由气泡裁；再套一层直角裁剪，封面的下面两个角就成了直角（主人 2026-11-09）
		box.Clip()
	}
	box.Children(func() {
		if cover.State == MediaReady && cover.Image != nil {
			ui.Image(c.Key("cover"), cover.Image).Size(w, ht).Fit(ui.Cover)
		} else {
			ui.Box(c.Key("cover-empty")).Size(w, ht).Center().Children(func() {
				ui.Text(c, "[视频]").Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Text3)
			})
		}
		// 中间的圆按钮
		ui.Box(c.Key("center")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Children(func() {
			var act func()
			if action != nil {
				act = func() { action(id, pos) }
			}
			downloading := result.State == MediaDownloading
			h.circleButton(c, "action", 56, label, icon, result.Progress, downloading, act, shade, white)
		})
		// 左上角：时长、大小；下载中写进度
		var parts []string
		switch {
		case result.State == MediaDownloading && result.Progress >= 0 && s.Size > 0:
			parts = append(parts, fmt.Sprintf("%s / %s", humanBytes(int64(float64(s.Size)*float64(result.Progress))), humanBytes(s.Size)))
		case result.State == MediaFailed:
			parts = append(parts, "下载失败")
		default:
			if s.DurationMS > 0 {
				parts = append(parts, humanDuration(s.DurationMS))
			}
			if s.Size > 0 {
				parts = append(parts, humanBytes(s.Size))
			}
		}
		if len(parts) > 0 {
			chip := ui.Row(c.Key("meta")).Label("视频信息").Absolute().Left(t.S2).Top(t.S2).Padding(2, 7).Background(shade).Radius(999).AlignItems(ui.Center)
			if result.State == MediaFailed && result.Description != "" {
				chip.Tooltip(result.Description)
			}
			chip.Children(func() {
				ui.Text(c, strings.Join(parts, " · ")).Font(h.Font).FontSize(t.FS[0]).FontFeatures(Tabular).TextColor(white).SingleLine()
			})
		}
	})
}

// fileBar 是文件条：左边圆形图标（下载中是进度圆环），右边文件名和大小。
func (h Host) fileBar(c *ui.Context, id string, s segview.Segment, pos int) {
	t := h.T
	result := MediaResult{Progress: -1}
	if h.Media != nil {
		result = h.Media.Download(id, pos)
	}
	label, icon, action := h.mediaAction(result, s, "文件")
	if result.State == MediaDownloaded || result.State == MediaReady {
		icon = "file"
	}
	name := s.DisplayName()
	if name == "" {
		name = "文件"
	}
	var meta []string
	if s.Size > 0 {
		meta = append(meta, humanBytes(s.Size))
	}
	switch {
	case result.State == MediaDownloading && result.Progress >= 0:
		meta = append(meta, fmt.Sprintf("%.0f%%", result.Progress*100))
	case result.State == MediaFailed && result.Description != "":
		meta = append(meta, result.Description)
	case result.State == MediaMissing && result.Description != "":
		meta = append(meta, result.Description)
	}
	ui.Row(c.Key("attachment")).Label("文件").FillWidth().MinWidth(0).Gap(t.S2).AlignItems(ui.Center).Padding(t.S2).Background(t.C.Hover).Radius(t.RInnerMin).Children(func() {
		var act func()
		if action != nil {
			act = func() { action(id, pos) }
		}
		ink := t.C.Text
		h.circleButton(c, "action", 44, label, icon, result.Progress, result.State == MediaDownloading, act, t.C.Press, ink)
		ui.Column(c.Key("info")).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c.Key("name"), name).Font(h.Font).FontSize(t.FS[2]).FontWeight(t.Weight[2]).TextColor(t.C.Text).FillWidth().SingleLine().Selectable()
			if len(meta) > 0 {
				ui.Text(c.Key("meta"), strings.Join(meta, " · ")).Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Text3).FillWidth().SingleLine()
			}
		})
	})
}
