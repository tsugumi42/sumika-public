package content

import (
	"bytes"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"strings"
	"testing"
)

// photo.cpp:425-432, media_streaming_utility.cpp:389-410; gif.cpp:538-548.
func TestCaptionCropRules(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		width, originalHeight, wantHeight float32
		gif                               bool
	}{
		{"portrait", 350, 600, 525, false},
		{"too much crop", 350, 1600, 430, false},
		{"three quarters allowed", 383, 600, 574, false},
		{"three quarters rejected", 384, 600, 430, false},
		{"GIF bounded expansion", 320, 1600, 426, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) {
				surface(c, func() {
					m := Media{ID: tc.name, Label: tc.name, Width: 400, Height: tc.originalHeight, Status: MediaRemote}
					o := MediaOptions{Width: tc.width, InBubble: true, Caption: strings.Repeat("m", 80)}
					if tc.gif {
						GIF(c, m, o)
					} else {
						Photo(c, m, o)
					}
				})
			}, 500, 900)
			r := box(t, tt, tc.name)
			expect(t, "caption width is measured within media", closeTo(r.W, tc.width))
			expect(t, "source crop expansion and rejection", closeTo(r.H, tc.wantHeight))
		})
	}
}

// radial_animation.cpp:40-59: from the previous shown value, duration/(duration+elapsed).
func TestTransferNonlinearCatchup(t *testing.T) {
	source, err := theme.Compile(theme.Definition{Version: theme.Version, Base: theme.LightBase, Motion: map[string]theme.MotionDefinition{"mediaTransferRotate": {Duration: new(float64(0))}}})
	if err != nil {
		t.Fatal(err)
	}
	progress := float32(0)
	size := source.Metrics().Media().ActionSize()
	line := source.Metrics().Media().ProgressWidth()
	radius := (size - 2*line) / 2
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		ui.Box(c).Size(size, size).Background(ui.RGB(0, 0, 0)).Children(func() { progressArc(c, progress) })
	}, 100, 100)
	tt.Image()
	progress = 1
	tt.Frame()
	initial := tt.Image().RGBAAt(int(size/2+radius), int(size/2))
	expect(t, "host progress jump does not jump visible ring", initial.R < 100)
	tt.Advance(source.Motion().MediaTransferCatchup().Duration())
	tt.Frame()
	image := tt.Image()
	expect(t, "radial duration catches half target", image.RGBAAt(int(size/2+radius), int(size/2)).R > 200 && image.RGBAAt(int(size/2), int(size/2+radius)).R > 200)
	expect(t, "nonlinear catchup has not reached three quarters", image.RGBAAt(int(size/2-radius), int(size/2)).R < 100)
	progress = .25
	tt.Frame()
	tt.Advance(source.Motion().MediaTransferCatchup().Duration())
	tt.Frame()
	expect(t, "updated target starts from displayed progress", tt.Image().RGBAAt(int(size/2), int(size/2+radius)).R < 100)
	tt.SetPreferences(ui.Preferences{TextScale: 1, ReduceMotion: true})
	progress = 1
	tt.Frame()
	expect(t, "reduced motion reaches host progress immediately", tt.Image().RGBAAt(int(size/2-radius), int(size/2)).R > 200 && !tt.FramePending())
}

// spoiler_mess.cpp:32-52,124-193,432-480: periodic particles, not a changing dot grid.
func TestSpoilerParticleFramesAndPrivacy(t *testing.T) {
	state := MarkdownState{}
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			Markdown(c, "||A prepared private phrase with 中文||", &state, MarkdownOptions{RevealLabel: "Reveal"})
		})
	}, 360, 100)
	first := tt.Image()
	before := append([]byte(nil), first.Pix...)
	minimum := uint8(255)
	for i := 0; i < len(first.Pix); i += 4 {
		minimum = min(minimum, first.Pix[i])
	}
	expect(t, "concealed whitespace paints no fallback glyph", minimum >= theme.Light().Colors().Message().In().TimestampForeground().R-1)
	expect(t, "hidden particle text stays out of accessible text", !tt.HasText("A prepared private phrase"))
	tt.Advance(theme.Light().Motion().SpoilerFrame().Duration())
	tt.Frame()
	expect(t, "source frame duration advances particle trajectories", !bytes.Equal(before, tt.Image().Pix))
	tt.SetPreferences(ui.Preferences{TextScale: 1, ReduceMotion: true})
	frozen := append([]byte(nil), tt.Image().Pix...)
	tt.Advance(theme.Light().Motion().SpoilerFrame().Duration() * 3)
	tt.Frame()
	expect(t, "reduced motion freezes particle frame", bytes.Equal(frozen, tt.Image().Pix) && !tt.FramePending())
	click(t, tt, "Reveal")
	expect(t, "click reveals prepared message", state.SpoilersRevealed && tt.HasText("A prepared private phrase"))
}

// spoiler_mess.cpp:55-71: image mask uses 3000 faster particles and 300/0/300ms.
func TestImageSpoilerParticleReveal(t *testing.T) {
	hidden := true
	bitmap := fixture(200, 200)
	tt := ui.NewTester(func(c *ui.Context) {
		surface(c, func() {
			Photo(c, Media{ID: "prepared", Label: "Image spoiler", Bitmap: bitmap}, MediaOptions{Spoiler: &hidden})
		})
	}, 300, 300)
	before := append([]byte(nil), tt.Image().Pix...)
	tt.Advance(theme.Light().Motion().SpoilerFrame().Duration())
	tt.Frame()
	expect(t, "image descriptor advances independent particle frames", !bytes.Equal(before, tt.Image().Pix))
	click(t, tt, "Image spoiler")
	expect(t, "revealed media releases particle animation", !hidden && !tt.FramePending() && pixel(tt, 100, 100).R == 240)
}
