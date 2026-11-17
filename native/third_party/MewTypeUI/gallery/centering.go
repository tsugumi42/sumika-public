package gallery

import (
	"fmt"
	"image"
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/compose"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func init() {
	Register(Page{ID: "layout/centering", Area: "layout", Title: "居中和抗锯齿", View: centeringPage})
}

type centeringSample struct {
	name string
	view func(*ui.Context)
	crop ui.Rect
}

// Snapshots run actual components; the native viewer uses its shared radial painter.
// Its complete window renderer is exercised in overlay/centering_test.go.
func centeringSamples(t *theme.Theme) []centeringSample {
	m, f, d, n := t.Metrics().Media(), t.Metrics().File(), t.Metrics().Conversation(), t.Metrics().Navigation()
	photo := func(status content.MediaStatus, progress float32) func(*ui.Context) {
		return func(c *ui.Context) {
			content.Photo(c, content.Media{ID: "photo", Label: "Media", Width: m.MinPhotoSize(), Height: m.MinPhotoSize(), Status: status, Progress: progress}, content.MediaOptions{})
		}
	}
	mediaCrop := ui.Rect{W: m.MinPhotoSize(), H: m.MinPhotoSize()}
	fileCrop := ui.Rect{W: f.PaddingLeft() + f.IconSize() + f.PaddingRight(), H: f.PaddingTop() + f.IconSize() + f.PaddingBottom()}
	voice := content.VoiceState{}
	selected := dialogs.RailState{}
	return []centeringSample{
		{"媒体 · 下载", photo(content.MediaRemote, 0), mediaCrop},
		{"媒体 · 50%", photo(content.MediaLoading, .5), mediaCrop},
		{"媒体 · 整圆", photo(content.MediaLoading, 1), mediaCrop},
		{"文件 · 就绪", func(c *ui.Context) { content.File(c, content.FileData{Name: "File"}, content.FileOptions{}) }, fileCrop},
		{"文件 · 加载", func(c *ui.Context) {
			content.File(c, content.FileData{Name: "File", Status: content.MediaLoading, Progress: 1}, content.FileOptions{})
		}, fileCrop},
		{"语音 · 无操作回调", func(c *ui.Context) {
			content.Voice(c, content.VoiceData{Duration: "Voice"}, &voice, content.VoiceOptions{})
		}, fileCrop},
		{"头像 · 在线角标", func(c *ui.Context) {
			dialogs.Row(c, dialogs.Conversation{ID: "peer", Initials: "AA", Online: true}, dialogs.RowOptions{})
		}, ui.Rect{W: d.PaddingLeft() + d.AvatarSize() + d.PaddingRight(), H: d.RowHeight()}},
		{"未读 · 8", func(c *ui.Context) { dialogs.Row(c, dialogs.Conversation{ID: "peer", Unread: 8}, dialogs.RowOptions{}) }, ui.Rect{X: 280 - 2*d.PaddingRight() - 2*d.BadgeHeight(), Y: d.PreviewTop() - d.BadgeHeight()/2, W: 2*d.BadgeHeight() + 2*d.PaddingRight(), H: 2 * d.BadgeHeight()}},
		{"文件夹 · 8", func(c *ui.Context) {
			dialogs.Rail(c, []dialogs.Folder{{ID: "all", Name: "Folder", Badge: "8"}}, &selected, dialogs.RailOptions{})
		}, ui.Rect{W: t.Metrics().Sidebar().Width(), H: t.Metrics().Sidebar().ItemMinHeight()}},
		{"发送", func(c *ui.Context) { compose.ActionButton(c, compose.Send, compose.ActionOptions{}) }, ui.Rect{W: t.Metrics().Compose().SendWidth(), H: t.Metrics().Compose().SendHeight()}},
		{"取消 · 对称图标", func(c *ui.Context) { compose.ActionButton(c, compose.Cancel, compose.ActionOptions{}) }, ui.Rect{W: t.Metrics().Compose().SendWidth(), H: t.Metrics().Compose().SendHeight()}},
		{"跳到最新 · 8", func(c *ui.Context) {
			chat.LatestButton(c, chat.LatestOptions{Visible: true, Unread: 8, Label: "Latest"})
		}, ui.Rect{X: 280 - n.ToLatestRight() - n.ToLatestWidth(), Y: 120 - n.ToLatestBottom() - n.ToLatestHeight(), W: n.ToLatestWidth(), H: n.ToLatestHeight()}},
		{"看大图 · 加载", func(c *ui.Context) {
			// Preview the native viewer's shared painter; Viewer itself owns a window.
			m := theme.T(c).Metrics().Viewer()
			path := ui.Local(c.Root(), "centering-viewer-arc", func() ui.Path {
				var path ui.Path
				path.Circle(0, 0, m.LoadingSize()/2-m.LoadingStroke()).Trim(0, 1)
				return path
			})
			ui.Overlay(c, func() {
				ui.Box(c).Absolute().Attach(ui.AnchorCenter, ui.AnchorCenter).Size(m.LoadingSize(), m.LoadingSize()).Draw(func(p *ui.Painter, r ui.Rect) {
					draw.Radial(p, r, path, m.LoadingStroke(), -90, t.Colors().Control().RadialBackground(), t.Colors().Control().RadialForeground(), false)
				})
			})
		}, ui.Rect{X: 160 - t.Metrics().Viewer().LoadingSize(), Y: 80 - t.Metrics().Viewer().LoadingSize(), W: 2 * t.Metrics().Viewer().LoadingSize(), H: 2 * t.Metrics().Viewer().LoadingSize()}},
	}
}

func centeringTester(t *theme.Theme, view func(*ui.Context), scale, offset float32) *ui.Tester {
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, t)
		ui.Box(c).Absolute().Left(13+offset).Top(17+offset).Size(280, 120).Children(func() { view(c) })
	}, 320, 160)
	tt.SetDark(t.Dark())
	tt.SetPreferences(ui.Preferences{TextScale: 1, ReduceMotion: true})
	tt.SetScale(scale)
	tt.Frame()
	return tt
}

type centeringSnapshot struct {
	name   string
	images [3]*ui.Bitmap
}
type centeringCache struct {
	theme *theme.Theme
	rows  []centeringSnapshot
}

func centeringPage(c *ui.Context) {
	t := theme.T(c)
	root := ui.Scroll(c).Fill()
	cache := ui.Local(root, "centering-rasters", func() centeringCache { return centeringCache{} })
	if cache.theme != t {
		cache.theme, cache.rows = t, nil
		for _, sample := range centeringSamples(t) {
			row := centeringSnapshot{name: sample.name}
			for i, scale := range [...]float32{1, 1.25, 1.5} {
				img := centeringTester(t, sample.view, scale, .3).Image()
				r := sample.crop
				if sample.name != "看大图 · 加载" {
					r.X, r.Y = r.X+13.3, r.Y+17.3
				}
				crop := image.Rect(int(math.Floor(float64(r.X*scale))), int(math.Floor(float64(r.Y*scale))), int(math.Ceil(float64((r.X+r.W)*scale))), int(math.Ceil(float64((r.Y+r.H)*scale))))
				row.images[i] = ui.NewBitmap(img.SubImage(crop.Intersect(img.Bounds()))).Pixelated()
			}
			cache.rows = append(cache.rows, row)
		}
	}
	root.Children(func() {
		ui.Column(c).FillWidth().Padding(t.Metrics().Message().PaddingTop(), t.Metrics().Message().PaddingLeft()).Gap(t.Metrics().Header().ActionGap()).Children(func() {
			ui.Text(c, "居中和抗锯齿").FontSize(t.Typography().HeaderTitle().Size()).Bold()
			ui.Text(c, "三列分别在 100%、125%、150% 下成像。每个源像素放大为 2 DIP；动画固定，便于比较边缘。顶部为当前窗口的实时组件。")
			ui.Text(c, "当前窗口 · 实时加载")
			ui.Row(c).Gap(t.Metrics().Header().ActionGap()).Children(func() {
				content.Photo(c, content.Media{ID: "live", Label: "Live loading", Width: t.Metrics().Media().MinPhotoSize(), Height: t.Metrics().Media().MinPhotoSize(), Status: content.MediaLoading, Progress: .7}, content.MediaOptions{OnCancel: func() {}})
				compose.ActionButton(c, compose.Send, compose.ActionOptions{})
			})
			for _, row := range cache.rows {
				ui.Text(c, row.name)
				ui.Row(c).Gap(t.Metrics().Header().ActionGap()).Children(func() {
					for i, bitmap := range row.images {
						ui.Column(c).Children(func() {
							ui.Text(c, fmt.Sprintf("%.2f×", [...]float32{1, 1.25, 1.5}[i]))
							w, h := bitmap.Size()
							ui.Image(c, bitmap).Size(2*float32(w), 2*float32(h))
						})
					}
				})
			}
		})
	})
}
