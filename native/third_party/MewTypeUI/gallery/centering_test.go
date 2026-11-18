package gallery

import (
	"fmt"
	"image"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/compose"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// White over black measures fractional coverage, including antialiased edges.
// All unrelated roles are transparent; the prepared thumbnail is black too.
func centeringMask(t *testing.T, keys ...string) *theme.Theme {
	t.Helper()
	d := theme.Light().Definition()
	for key := range d.Colors {
		d.Colors[key] = "#00000000"
	}
	d.Colors["window.background"] = "#000000"
	for _, key := range keys {
		d.Colors[key] = "#ffffff"
	}
	th, err := theme.Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func inkCenter(t *testing.T, img *image.RGBA) (x, y float64, bounds image.Rectangle) {
	t.Helper()
	var weight float64
	bounds.Min = img.Bounds().Max
	for py := range img.Bounds().Dy() {
		for px := range img.Bounds().Dx() {
			w := float64(img.RGBAAt(px, py).R) / 255
			if w == 0 {
				continue
			}
			weight += w
			x += (float64(px) + .5) * w
			y += (float64(py) + .5) * w
			bounds.Min.X, bounds.Min.Y = min(bounds.Min.X, px), min(bounds.Min.Y, py)
			bounds.Max.X, bounds.Max.Y = max(bounds.Max.X, px+1), max(bounds.Max.Y, py+1)
		}
	}
	if weight == 0 {
		t.Fatal("no foreground pixels")
	}
	return x / weight, y / weight, bounds
}

func TestCircularIconPixelCenters(t *testing.T) {
	// chat.style:384,427 (44 / 3); Lucide viewBox 24, exceptions.md.
	// Threshold is deliberately below the old +/-0.5 px paint-rounding split.
	for _, scale := range []float32{1, 1.25, 1.5} {
		for _, offset := range []float32{0, .3} {
			for _, kind := range []string{"media", "file", "thumbnail", "voice"} {
				t.Run(fmt.Sprintf("%s/%.2f/%.1f", kind, scale, offset), func(t *testing.T) {
					view := func(c *ui.Context) {
						switch kind {
						case "media":
							content.Photo(c, content.Media{ID: "p", Width: 100, Height: 100, Status: content.MediaLoading, Progress: 1}, content.MediaOptions{})
						case "file", "thumbnail":
							var thumbnail *ui.Bitmap
							if kind == "thumbnail" {
								thumbnail = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 4, 4)))
							}
							content.File(c, content.FileData{Name: "file", Thumbnail: thumbnail, Status: content.MediaLoading, Progress: 1}, content.FileOptions{})
						case "voice":
							content.Voice(c, content.VoiceData{Status: content.MediaLoading, Progress: 1}, &content.VoiceState{}, content.VoiceOptions{})
						}
					}
					outer, inner := "message.fileInBackground", "message.fileInIconForeground"
					if kind == "media" || kind == "thumbnail" {
						outer, inner = "control.videoPlayIconBackground", "control.videoPlayIconForeground"
					}
					ox, oy, _ := inkCenter(t, centeringTester(centeringMask(t, outer), view, scale, offset).Image())
					ix, iy, _ := inkCenter(t, centeringTester(centeringMask(t, inner), view, scale, offset).Image())
					centerNear(t, "icon x", ix, ox, .05)
					centerNear(t, "icon y", iy, oy, .05)
				})
			}
		}
	}
}

func TestRadialPixelCentersAcrossRotation(t *testing.T) {
	for _, scale := range []float32{1, 1.25, 1.5} {
		for _, kind := range []string{"media", "file"} {
			t.Run(fmt.Sprintf("%s/%.2f", kind, scale), func(t *testing.T) {
				makeFrame := func(key string, phase int) *image.RGBA {
					d := centeringMask(t, key).Definition()
					for _, motion := range []string{"mediaTransferRotate", "viewerLoadingRotation"} {
						d.Motion[motion] = theme.MotionDefinition{Duration: new(float64(8)), Easing: new(theme.Linear)}
					}
					d.Motion["viewerLoading"] = theme.MotionDefinition{Duration: new(float64(0)), Easing: new(theme.Linear)}
					th, err := theme.Compile(d)
					if err != nil {
						t.Fatal(err)
					}
					tt := centeringTester(th, func(c *ui.Context) {
						switch kind {
						case "media":
							content.Photo(c, content.Media{ID: "p", Width: 100, Height: 100, Status: content.MediaLoading, Progress: 1}, content.MediaOptions{})
						case "file":
							content.File(c, content.FileData{Name: "file", Status: content.MediaLoading, Progress: 1}, content.FileOptions{})
						}
					}, scale, .3)
					tt.SetPreferences(ui.Preferences{TextScale: 1})
					tt.Advance(time.Duration(phase) * 2 * time.Millisecond)
					tt.Frame()
					return tt.Image()
				}
				outer := "control.videoPlayIconBackground"
				if kind == "file" {
					outer = "message.fileInBackground"
				}
				for phase := range 4 {
					ox, oy, _ := inkCenter(t, makeFrame(outer, phase))
					ix, iy, _ := inkCenter(t, makeFrame("control.radialForeground", phase))
					centerNear(t, "radial x", ix, ox, .3)
					centerNear(t, "radial y", iy, oy, .3)
				}
			})
		}
	}
}

func TestVoiceIconDoesNotMoveWithCallbackAvailability(t *testing.T) {
	th := centeringMask(t, "message.fileInIconForeground")
	for _, scale := range []float32{1, 1.25, 1.5} {
		for _, status := range []content.MediaStatus{content.MediaReady, content.MediaRemote, content.MediaLoading, content.MediaFailed} {
			var frames [2]*image.RGBA
			for i := range frames {
				opts := content.VoiceOptions{}
				if i == 1 {
					opts.OnToggle, opts.OnDownload, opts.OnCancel, opts.OnRetry = func() {}, func() {}, func() {}, func() {}
				}
				frames[i] = centeringTester(th, func(c *ui.Context) {
					content.Voice(c, content.VoiceData{Status: status, Progress: 1}, &content.VoiceState{}, opts)
				}, scale, .3).Image()
			}
			sameInk(t, fmt.Sprintf("voice callbacks/%.2f/%v", scale, status), frames[0], frames[1])
		}
	}
}

func TestOtherCircularContentGeometry(t *testing.T) {
	cross := ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 6 6 18M6 6 18 18"/></svg>`))
	for _, scale := range []float32{1, 1.25, 1.5} {
		for _, offset := range []float32{0, .3} {
			avatar := func(c *ui.Context) {
				dialogs.Row(c, dialogs.Conversation{ID: "p", AvatarIcon: cross}, dialogs.RowOptions{})
			}
			ox, oy, _ := inkCenter(t, centeringTester(centeringMask(t, "avatar.peer4UserpicBackground", "avatar.peer4UserpicBackground2"), avatar, scale, offset).Image())
			ix, iy, _ := inkCenter(t, centeringTester(centeringMask(t, "avatar.userpicForeground"), avatar, scale, offset).Image())
			centerNear(t, "avatar x", ix, ox, .05)
			centerNear(t, "avatar y", iy, oy, .05)

			online := centeringTester(centeringMask(t, "conversation.onlineBadgeForeground"), func(c *ui.Context) {
				dialogs.Row(c, dialogs.Conversation{ID: "p", Online: true}, dialogs.RowOptions{})
			}, scale, offset)
			px, py, _ := inkCenter(t, online.Image())
			d := theme.Light().Metrics().Conversation()
			cx := 13 + offset + d.PaddingLeft() + d.AvatarSize() - d.OnlineSize()/2
			cy := 17 + offset + d.PaddingTop() + d.AvatarSize() - d.OnlineSkipY() - d.OnlineSize()/2
			radius := d.OnlineSize()/2 + d.OnlineStroke()
			snappedCenter := func(center float32) float64 {
				return (math.Round(float64((center-radius)*scale)) + math.Round(float64((center+radius)*scale))) / 2
			}
			// dialogs.style:185-188: 10 DIP online dot in a 2 DIP border.
			centerNear(t, "online x", px, snappedCenter(cx), .05)
			centerNear(t, "online y", py, snappedCenter(cy), .05)

			for _, kind := range []string{"dialogs", "latest", "sidebar"} {
				state := dialogs.RailState{}
				view := func(c *ui.Context) {
					switch kind {
					case "latest":
						chat.LatestButton(c, chat.LatestOptions{Visible: true, Unread: 8})
					case "sidebar":
						dialogs.Rail(c, []dialogs.Folder{{ID: "all", Name: "Folder", Badge: "8"}}, &state, dialogs.RailOptions{}).Height(120)
					default:
						dialogs.Row(c, dialogs.Conversation{ID: "p", Unread: 8}, dialogs.RowOptions{})
					}
				}
				key := "conversation.unreadBackground"
				if kind == "sidebar" {
					key = "sidebar.badgeBackground"
				}
				bg := centeringTester(centeringMask(t, key), view, scale, offset)
				x, y, _ := inkCenter(t, bg.Image())
				textBox, ok := bg.Find("8")
				if !ok {
					t.Fatal("missing badge text")
				}
				// Qt centers the font line box, not the digit's ink centroid:
				// unread_badge_paint.cpp:181-188. Keep that distinction explicit.
				centerNear(t, "badge line x", float64((textBox.X+textBox.W/2)*scale), x, .75)
				centerNear(t, "badge line y", float64((textBox.Y+textBox.H/2)*scale), y, .75)
			}

			tt := centeringTester(centeringMask(t, "compose.iconForeground"), func(c *ui.Context) { compose.ActionButton(c, compose.Cancel, compose.ActionOptions{Label: "cancel"}) }, scale, offset)
			x, y, _ := inkCenter(t, tt.Image())
			r, _ := tt.Find("cancel")
			centerNear(t, "compose x", x, float64((r.X+r.W/2)*scale), .05)
			centerNear(t, "compose y", y, float64((r.Y+r.H/2)*scale), .05)

			send := centeringTester(centeringMask(t, "compose.sendIconForeground"), func(c *ui.Context) {
				compose.ActionButton(c, compose.Send, compose.ActionOptions{Label: "send"})
			}, scale, offset)
			_, _, ink := inkCenter(t, send.Image())
			button, _ := send.Find("send")
			// The plane is asymmetric; center its canvas, not its ink mass.
			centerNear(t, "send bounds x", float64(ink.Min.X+ink.Max.X)/2, float64((button.X+button.W/2)*scale), .55)
			centerNear(t, "send bounds y", float64(ink.Min.Y+ink.Max.Y)/2, float64((button.Y+button.H/2)*scale), .55)
		}
	}
}

func TestHalfRadialRetainsProgressGeometry(t *testing.T) {
	for _, scale := range []float32{1, 1.25, 1.5} {
		view := func(c *ui.Context) {
			content.Photo(c, content.Media{ID: "p", Width: 100, Height: 100, Status: content.MediaLoading, Progress: .5}, content.MediaOptions{})
		}
		ox, oy, _ := inkCenter(t, centeringTester(centeringMask(t, "control.videoPlayIconBackground"), view, scale, .3).Image())
		ix, iy, _ := inkCenter(t, centeringTester(centeringMask(t, "control.radialForeground"), view, scale, .3).Image())
		m := theme.Light().Metrics().Media()
		// A half-circle's mass is 2r/pi from its center; round caps and
		// the one-degree minimum slightly change this, within one DIP.
		distance := 2 * float64((m.ActionSize()/2-m.ProgressWidth())*scale) / math.Pi
		centerNear(t, "half arc x", ix-ox, distance, float64(scale))
		centerNear(t, "half arc y", iy, oy, float64(scale))
	}
}
