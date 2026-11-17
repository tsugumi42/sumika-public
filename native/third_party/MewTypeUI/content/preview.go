package content

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// PreviewData uses SmallImage for an article thumbnail at the right; otherwise
// Image is the full-width attachment after the text.
type PreviewData struct {
	Site, Title, Description, URL, Status string
	Image                                 *ui.Bitmap
	SmallImage                            bool
}
type PreviewOptions struct {
	Appearance
	Width  float32 // available content width; zero uses the normal bubble content width
	OnOpen func(url string)
}

func LinkPreview(c *ui.Context, data PreviewData, o PreviewOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Preview()
	q := t.Metrics().Quote()
	p := colors(t, o.Appearance)
	width := o.Width
	if width == 0 {
		message := t.Metrics().Message()
		width = message.MaxWidth() - message.PaddingLeft() - message.PaddingRight()
	}
	inner := width - m.PaddingLeft() - m.PaddingRight()
	font := t.Typography().Font()
	size := t.Typography().Message().Size() * c.Preferences().TextScale
	_, line := c.MeasureText(0, ui.Span{Text: "M", Font: font, Size: size})
	description := data.Description
	if data.Status != "" {
		description = data.Status
	}
	titleLines, descriptionLines := 0, 0
	measure := func(available float32) float32 {
		used := 0
		if data.Site != "" {
			used = 1
		}
		titleLines = 0
		if data.Title != "" {
			_, height := c.MeasureText(available, ui.Span{Text: data.Title, Font: font, Size: size, Weight: t.Typography().ContentTitle().Weight()})
			titleLines = int(min(m.TitleLines(), float32(int((height+line-1)/line))))
		}
		descriptionLines = 0
		if description != "" {
			_, height := c.MeasureText(available, ui.Span{Text: description, Font: font, Size: size})
			descriptionLines = min(int(m.MaxLines())-used-titleLines, int((height+line-1)/line))
		}
		return float32(used+titleLines+descriptionLines) * line
	}
	thumbW, thumbH := float32(0), float32(0)
	if data.Image != nil && data.SmallImage {
		w, h := data.Image.Size()
		thumbH = line * m.MaxLines()
		for {
			thumbW = max(line, min(thumbH, thumbH*float32(w)/float32(h)))
			textHeight := measure(inner - m.ImageGap() - thumbW)
			if textHeight >= thumbH || thumbH <= line {
				break
			}
			thumbH -= line
		}
	} else {
		measure(inner)
	}
	var fn func()
	if o.OnOpen != nil {
		fn = func() { o.OnOpen(data.URL) }
	}
	root := action(c, data.URL, o.Disabled, fn).Column().Justify(ui.Start).AlignItems(ui.Stretch).Width(width).MaxWidthPercent(100).MinWidth(0).
		Padding(m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft()).Radius(q.Radius()).Clip().Background(p.bar.Alpha(q.BackgroundOpacity()))
	quoteBar(root, q.OutlineWidth(), q.Radius(), p.bar.Alpha(q.OutlineOpacity()))
	root.Children(func() {
		ui.Row(c).MinWidth(0).AlignItems(ui.Start).Gap(m.ImageGap()).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				if data.Site != "" {
					text(c, data.Site, t.Typography().ContentTitle(), p.service).SingleLine()
				}
				if data.Title != "" {
					text(c, data.Title, t.Typography().ContentTitle(), p.text).MaxLines(titleLines)
				}
				if description != "" {
					text(c, description, t.Typography().Message(), p.text).MaxLines(descriptionLines)
				}
			})
			if data.Image != nil && data.SmallImage {
				ui.Image(c, data.Image).Size(thumbW, thumbH).Fit(ui.Cover).Radius(q.Radius()).Shrink(0)
			}
		})
		if data.Image != nil && !data.SmallImage {
			w, h := data.Image.Size()
			ui.Image(c, data.Image).FillWidth().AspectRatio(float32(w)/float32(h)).MaxHeight(t.Metrics().Media().MaxSize()).Fit(ui.Cover).Radius(q.Radius()).Margin(m.MediaGap(), 0, 0)
		}
	})
	if fn != nil {
		ripple(c, root, p.bar, q.Radius(), q.BackgroundOpacity())
	}
	return root
}
