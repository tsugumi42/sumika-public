package gallery

import (
	"fmt"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
	"time"
)

func TestEveryEntry(t *testing.T) {
	for _, page := range Pages() {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dark=%t", page.ID, dark), func(t *testing.T) {
				th := theme.Light()
				if dark {
					th = theme.Dark()
				}
				var window mygo.Window
				tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c, th); overlay.Native(c, &window, func() { page.View(c) }) }, 800, 600)
				tt.SetDark(dark)
				tt.SetPreferences(ui.Preferences{TextScale: 1, ReduceMotion: true})
				tt.Advance(time.Second)
				tt.Frame()
				tt.Image()
			})
		}
	}
}
