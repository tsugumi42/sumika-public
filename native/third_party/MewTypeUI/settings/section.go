package settings

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// SectionTitle has its own padding; the page itself must not add a second inset.
// paint/settings-section.md#小标题.
func SectionTitle(c *ui.Context, title string) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Settings()
	return ui.Column(c).FillWidth().Shrink(0).Padding(m.SectionTitlePaddingTop(), m.SectionTitlePaddingRight(), m.SectionTitlePaddingBottom(), m.SectionTitlePaddingLeft()).Children(func() {
		text(c, title, t.Typography().SettingsSectionTitle(), t.Colors().Window().ActiveTextForeground()).FillWidth()
	})
}

// Skip is an independent layout item, including when two skips are adjacent.
// paint/settings-section.md#skip与页面内边距.
func Skip(c *ui.Context) ui.Element {
	return ui.Box(c).FillWidth().Height(theme.T(c).Metrics().Settings().SectionSkip()).Shrink(0)
}

type DividerOptions struct{ HideTop, HideBottom bool }

// Divider paints a full-width band, optionally containing plain description text.
// paint/settings-section.md#分隔带; #带说明文字的分隔带.
func Divider(c *ui.Context, description string, o DividerOptions) ui.Element {
	if description == "" {
		return DividerContent(c, o, nil)
	}
	return DividerContent(c, o, func() { ui.Text(c, description) })
}

// DividerContent supports inline rich text and host-owned clickable links.
func DividerContent(c *ui.Context, o DividerOptions, content func()) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Settings()
	root := ui.Column(c).FillWidth().Shrink(0).Background(t.Colors().Dialog().DividerBackground())
	if content == nil {
		root.Height(m.DividerHeight())
	} else {
		root.Padding(m.DividerTextPaddingTop(), m.DividerTextPaddingRight(), m.DividerTextPaddingBottom(), m.DividerTextPaddingLeft()).Font(t.Typography().Font()).FontSize(t.Typography().Body().Size() * c.Preferences().TextScale).FontWeight(t.Typography().Body().Weight()).TextColor(t.Colors().Window().MutedText()).Children(content)
	}
	// SET-DIVIDER-SHADOW is a procedural substitute for restricted bitmaps.
	root.DrawOver(func(p *ui.Painter, r ui.Rect) {
		ink := t.Colors().Control().ShadowForeground()
		h := m.DividerEdgeHeight()
		if !o.HideTop {
			p.FillGradient(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: h}, ui.LinearGradient{From: ink, To: ink.Alpha(0), Angle: 180}, 0)
		}
		if !o.HideBottom {
			p.FillGradient(ui.Rect{X: r.X, Y: r.Y + r.H - h, W: r.W, H: h}, ui.LinearGradient{From: ink.Alpha(0), To: ink, Angle: 180}, 0)
		}
	})
	return root
}
