package compose

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/locales"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type ReferenceKind uint8

const (
	ReplyReference ReferenceKind = iota
	EditReference
)

type Reference struct {
	ID, Name, Summary string
	Kind              ReferenceKind
	Preview           ui.ImageSource
	// Loading and Missing use host-provided summaries, including localization.
	Loading, Missing bool
}

type ReferenceOptions struct {
	Disabled             bool
	CancelLabel          string
	OnCancel, OnActivate func()
}

// ReferenceBar displays a reply/edit target with a separate cancel hit area.
func ReferenceBar(c *ui.Context, reference Reference, o ReferenceOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Compose()
	height := referenceHeight(c, t)
	root := ui.Row(c).FillWidth().MinWidth(0).Height(height).Gap(0).Background(t.Colors().Compose().ReplyBackground())
	root.Children(func() {
		c.Key("target")
		ui.ButtonBase(c).Grow(1).MinWidth(0).Height(height).Disabled(o.Disabled).Label(reference.Name).OnClick(o.OnActivate).Children(func() {
			src, glyphLabel := replyIcon, locales.Text(c.Locale(), "replyTarget")
			if reference.Kind == EditReference {
				src, glyphLabel = editIcon, locales.Text(c.Locale(), "editTarget")
			}
			gm := t.Metrics().Glyph()
			iw, ih := gm.Reply().Width(), gm.Reply().Height()
			if reference.Kind == EditReference {
				iw, ih = gm.Edit().Width(), gm.Edit().Height()
			}
			glyph := draw.Fit(c, src, m.ReferenceIconSize(), m.ReferenceIconSize(), iw, ih, gm.Stroke())
			glyph.Element(c).Absolute().Left(m.ReferenceIconLeft()).Top(m.ReferenceIconTop()).
				Size(m.ReferenceIconSize(), m.ReferenceIconSize()).TextColor(t.Colors().Compose().ReplyIconForeground()).Label(glyphLabel).IgnoreInput(true)
			left := m.ReplyTextLeft()
			if reference.Preview != nil {
				ui.Image(c, reference.Preview).Size(m.ReplyPreviewSize(), m.ReplyPreviewSize()).Absolute().Left(left).Top((height - m.ReplyPreviewSize()) / 2).Radius(t.Metrics().Control().RadiusSmall()).Fit(ui.Cover).IgnoreInput(true)
				left += m.ReplyPreviewSize() + t.Metrics().Quote().ReplyBarGap()
			}
			ui.Column(c).Absolute().Left(0).Right(0).Top(0).MinWidth(0).Gap(0).Padding(m.ReplyTextTop(), m.ReplyTextRight(), m.ReplyTextBottom(), left).IgnoreInput(true).Children(func() {
				text(c, reference.Name, t.Typography().ComposeTitle(), t.Colors().Window().ActiveTextForeground()).SingleLine()
				color := t.Colors().Compose().Text()
				if reference.Loading || reference.Missing {
					color = t.Colors().Compose().AreaForegroundService()
				}
				text(c, reference.Summary, t.Typography().Compose(), color).SingleLine()
			})
		})
		c.Key("cancel")
		cancel := ui.ButtonBase(c).Size(m.ReplyHeight(), height).Shrink(0).Center().Label(label(o.CancelLabel, locales.Text(c.Locale(), "cancelReference"))).Disabled(o.Disabled).OnClick(o.OnCancel)
		fg := t.Colors().Compose().ReplyCancelForeground()
		if cancel.Hovered() {
			fg = t.Colors().Compose().ReplyCancelForegroundHover()
		}
		cancel.TextColor(fg).Children(func() { draw.Element(c, closeIcon, m.IconSize(), m.IconSize()) })
	})
	return root
}

func referenceHeight(c *ui.Context, t *theme.Theme) float32 {
	m := t.Metrics().Compose()
	return max(m.ReplyHeight(), m.ReplyTextTop()+m.ReplyTextBottom()+fontLineHeight(c, t, t.Typography().ComposeTitle())+fontLineHeight(c, t, t.Typography().Compose()))
}
