package content

import (
	"bytes"
	"image"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestVideoPlayMarkerIndependentOfBadgeAndTransfer(t *testing.T) {
	media := Media{ID: "video", Label: "cover", Bitmap: fixture(200, 100)}
	shown := false
	badge := "视频"
	event := ""
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		Video(c, media, badge, VideoOptions{ShowPlay: shown, MediaOptions: MediaOptions{OnOpen: func() { event = "open" }, OnDownload: func() { event = "download" }, OnCancel: func() { event = "cancel" }, OnRetry: func() { event = "retry" }}})
	}, 300, 200)
	center := func() []byte {
		r := box(t, tt, "cover")
		size := theme.Light().Metrics().Media().ActionSize()
		crop := image.Rect(int(r.X+(r.W-size)/2), int(r.Y+(r.H-size)/2), int(r.X+(r.W+size)/2), int(r.Y+(r.H+size)/2))
		src := tt.Image()
		var pixels []byte
		for y := crop.Min.Y; y < crop.Max.Y; y++ {
			start := src.PixOffset(crop.Min.X, y)
			pixels = append(pixels, src.Pix[start:start+crop.Dx()*4]...)
		}
		return pixels
	}
	hidden := center()
	check(t, "video badge remains when marker is hidden", tt.HasText("视频"))
	shown = true
	tt.Frame()
	play := center()
	check(t, "explicit play switch changes center", !bytes.Equal(hidden, play))
	badge = ""
	tt.Frame()
	check(t, "empty badge retains explicit playback marker", bytes.Equal(play, center()) && !tt.HasText("视频") && !tt.HasText("0:00"))
	click(t, tt, "cover")
	check(t, "unknown duration video still delivers open", event == "open")
	shown = false
	tt.Frame()
	check(t, "explicit false hides marker with empty badge", bytes.Equal(hidden, center()))
	for _, tc := range []struct {
		status MediaStatus
		event  string
	}{{MediaRemote, "download"}, {MediaLoading, "cancel"}, {MediaFailed, "retry"}} {
		media.Status = tc.status
		media.StatusText = tc.event
		tt.Frame()
		click(t, tt, "cover")
		check(t, "transfer control takes precedence "+tc.event, event == tc.event && !bytes.Equal(hidden, center()) && tt.HasText(tc.event))
	}
}

func TestVideoEmptyBadgePreservesMotionMediaCrop(t *testing.T) {
	badge := ""
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		Video(c, Media{ID: "portrait", Label: "portrait", Width: 400, Height: 1600}, badge, VideoOptions{ShowPlay: true, MediaOptions: MediaOptions{InBubble: true, Caption: "带说明的视频"}})
	}, 500, 800)
	empty := box(t, tt, "portrait")
	badge = "视频"
	tt.Frame()
	with := box(t, tt, "portrait")
	check(t, "video crop geometry ignores badge text", closeTo(empty.W, with.W) && closeTo(empty.H, with.H))
}
