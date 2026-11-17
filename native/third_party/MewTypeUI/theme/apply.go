package theme

import "github.com/egoist/mygo/ui"

type slotKey struct{}
type slot struct {
	source *Theme
	scale  float32
	mapped ui.Theme
}

// Apply belongs at the start of each view build. The source remains at 100% text scale.
func Apply(c *ui.Context, t *Theme) {
	s := ui.Local(c.Root(), slotKey{}, func() slot { return slot{} })
	scale := c.Preferences().TextScale
	if s.source != t || s.scale != scale {
		s.source = t
		s.scale = scale
		colors, metrics := t.Colors(), t.Metrics()
		control, window := colors.Control(), colors.Window()
		s.mapped = ui.Theme{
			Dark: t.Dark(), Background: window.Background(),
			Surface: control.SurfaceBackground(), SurfaceHover: control.SurfaceBackgroundHover(), SurfacePressed: control.SurfaceBackgroundPressed(),
			Border: control.Border(), Text: window.Text(), TextMuted: window.MutedText(),
			Accent: control.PrimaryBackground(), AccentHover: control.PrimaryBackgroundHover(), AccentPressed: control.PrimaryBackgroundPressed(), AccentText: control.PrimaryForeground(),
			Danger: colors.Semantic().Danger(), Warning: colors.Semantic().Warning(), Success: colors.Semantic().Success(),
			Selection: colors.Selection().Background(), Focus: control.Focus(),
			Inverse: colors.Toast().Background(), InverseText: colors.Toast().Foreground(),
			Scrollbar: colors.Scrollbar().ThumbBackground(), ScrollbarWidth: metrics.Scrollbar().Width(),
			Radius: metrics.Control().RadiusLarge(), Spacing: metrics.Control().Spacing(), FontSize: t.Typography().Body().Size() * scale, Font: t.Typography().Font(),
		}
	}
	c.SetTheme(&s.mapped)
}

// T returns the applied theme, or the shared preset matching mygo's current mode.
func T(c *ui.Context) *Theme {
	s := ui.Local(c.Root(), slotKey{}, func() slot { return slot{} })
	if s.source != nil {
		return s.source
	}
	if c.Theme().Dark {
		return Dark()
	}
	return Light()
}
