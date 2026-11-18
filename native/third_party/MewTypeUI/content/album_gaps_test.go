package content

import (
	"bytes"
	"image"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func check(t *testing.T, label string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", label)
	}
}

func TestAlbumIndividualStateActionsAndSizes(t *testing.T) {
	items := []Media{
		{ID: "ready", Label: "ready", Width: 200, Height: 200},
		{ID: "remote", Label: "remote", Width: 200, Height: 200, Status: MediaRemote, StatusText: "下载"},
		{ID: "loading", Label: "loading", Width: 200, Height: 200, Status: MediaLoading, Progress: .35, StatusText: "35%"},
		{ID: "failed", Label: "failed", Width: 200, Height: 200, Status: MediaFailed, StatusText: "网络失败，点击重试"},
	}
	event := ""
	disabled := false
	width := float32(320)
	sizes := map[string]ui.Point{}
	counts := map[string]int{}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		Album(c, items, AlbumOptions{Width: width, Appearance: Appearance{Disabled: disabled},
			OnOpen: func(id string) { event = "open:" + id }, OnDownload: func(id string) { event = "download:" + id },
			OnCancel: func(id string) { event = "cancel:" + id }, OnRetry: func(id string) { event = "retry:" + id },
			OnTileSize: func(id string, w, h float32) { sizes[id] = ui.Point{X: w, Y: h}; counts[id]++ },
		})
	}, 400, 400)
	tt.Frame()
	check(t, "per tile feedback is visible", tt.HasText("下载") && tt.HasText("35%") && tt.HasText("网络失败，点击重试"))
	for _, tc := range []struct{ id, event string }{{"ready", "open:ready"}, {"remote", "download:remote"}, {"loading", "cancel:loading"}, {"failed", "retry:failed"}} {
		if err := tt.Click(tc.id); err != nil {
			t.Fatal(err)
		}
		check(t, "status action "+tc.id, event == tc.event)
	}
	r, _ := tt.Find("remote")
	// chat.style:510: the original 4 DIP gap scales with the album width.
	ready, _ := tt.Find("ready")
	check(t, "album retains scaled source gap", closeTo(r.X-ready.X-ready.W, theme.Light().Metrics().Media().AlbumGap()*width/theme.Light().Metrics().Media().MaxSize()))
	check(t, "host receives actual tile dimensions", closeTo(sizes["remote"].X, r.W) && closeTo(sizes["remote"].Y, r.H) && counts["remote"] == 1)
	tt.Frame()
	tt.Frame()
	check(t, "unchanged size is not notified again", counts["remote"] == 1)
	items[0], items[1] = items[1], items[0]
	tt.Frame()
	tt.Frame()
	if err := tt.Click("remote"); err != nil {
		t.Fatal(err)
	}
	reordered, _ := tt.Find("remote")
	// Four-photo topology gives the first slot a larger box (grouped_layout.cpp:223–269).
	check(t, "reorder preserves ID and reports changed slot size", event == "download:remote" && counts["remote"] == 2 && closeTo(sizes["remote"].X, reordered.W) && closeTo(sizes["remote"].Y, reordered.H))
	width = 240
	tt.Frame()
	tt.Frame()
	r, _ = tt.Find("remote")
	check(t, "resize reports new actual dimensions", counts["remote"] == 3 && closeTo(sizes["remote"].X, r.W) && r.W < reordered.W)
	disabled = true
	event = ""
	tt.Frame()
	if err := tt.Click("loading"); err != nil {
		t.Fatal(err)
	}
	check(t, "disabled tile rejects cancellation", event == "")
	disabled = false
	items[2].Status = MediaReady
	items[2].StatusText = ""
	tt.Frame()
	if err := tt.Click("loading"); err != nil {
		t.Fatal(err)
	}
	check(t, "ready transition opens same ID", event == "open:loading" && !tt.HasText("35%"))
}

func TestAlbumProgressIsPerItemAndStopsOnReady(t *testing.T) {
	items := []Media{{ID: "a", Label: "a", Width: 200, Height: 200, Status: MediaLoading, Progress: .1}, {ID: "b", Label: "b", Width: 200, Height: 200, Status: MediaLoading, Progress: .8}}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c, theme.Light()); Album(c, items, AlbumOptions{Width: 300}) }, 400, 300)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	crop := func(label string) []byte {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatal(label)
		}
		im := tt.Image()
		result := image.NewRGBA(image.Rect(0, 0, int(r.W), int(r.H)))
		for y := range result.Rect.Dy() {
			for x := range result.Rect.Dx() {
				result.Set(x, y, im.At(int(r.X)+x, int(r.Y)+y))
			}
		}
		return result.Pix
	}
	beforeA, beforeB := crop("a"), crop("b")
	items[0].Progress = .7
	tt.Frame()
	check(t, "changing one progress changes its own ring", !bytes.Equal(beforeA, crop("a")))
	check(t, "changing one progress leaves sibling untouched", bytes.Equal(beforeB, crop("b")))
	items[0].Status, items[1].Status = MediaReady, MediaReady
	tt.Frame()
	check(t, "ready album stops progress frames", !tt.FramePending())
}
