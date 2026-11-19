package content

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type FileData struct {
	Name, Size, StatusText, Link string
	Thumbnail                    *ui.Bitmap
	Status                       MediaStatus
	Progress                     float32
}
type FileOptions struct {
	Appearance
	OnOpen, OnDownload, OnCancel, OnRetry, OnLink func()
}

func fileIntent(status MediaStatus, o FileOptions) func() {
	switch status {
	case MediaRemote:
		return o.OnDownload
	case MediaLoading:
		return o.OnCancel
	case MediaFailed:
		return o.OnRetry
	default:
		return o.OnOpen
	}
}
func fileColors(t *theme.Theme, a Appearance) (bg, hover, icon ui.Color) {
	p := t.Colors().Message()
	bg, hover, icon = p.FileInBackground(), p.FileInBackgroundHover(), p.FileInIconForeground()
	if a.Outgoing {
		bg, hover, icon = p.FileOutBackground(), p.FileOutBackground(), p.FileOutIconForeground()
	}
	if a.Selected {
		bg, hover, icon = p.FileInBackgroundSelected(), p.FileInBackgroundSelected(), p.FileInIconForegroundSelected()
		if a.Outgoing {
			bg, hover, icon = p.FileOutBackgroundSelected(), p.FileOutBackgroundSelected(), p.FileOutIconForegroundSelected()
		}
	}
	return
}

// File is a document row; size and localized transfer/error text come from the host.
func File(c *ui.Context, data FileData, o FileOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().File()
	p := colors(t, o.Appearance)
	left, top, right, bottom := m.PaddingLeft(), m.PaddingTop(), m.PaddingRight(), m.PaddingBottom()
	slot, gap, nameTop, statusTop := m.IconSize(), m.IconGap(), m.NameTop(), m.StatusTop()
	if data.Thumbnail != nil {
		left, top, right, bottom = m.ThumbPaddingLeft(), m.ThumbPaddingTop(), m.ThumbPaddingRight(), m.ThumbPaddingBottom()
		slot, gap, nameTop, statusTop = m.ThumbSize(), m.ThumbGap(), m.ThumbNameTop(), m.ThumbStatusTop()
	}
	root := action(c, data.Name, o.Disabled, fileIntent(data.Status, o)).Row().Justify(ui.Start).AlignItems(ui.Start).FillWidth().MinWidth(0).MinHeight(top+slot+bottom).Padding(top, right, bottom, left).Gap(gap)
	bg, over, fg := fileColors(t, o.Appearance)
	target := float32(0)
	if root.Hovered() {
		target = 1
	}
	amount := target
	if t.Motion().FileHover().Duration() > 0 {
		amount = root.AnimateTo("hover", target, transition(t.Motion().FileHover()))
	}
	icon := fileIcon
	switch data.Status {
	case MediaRemote, MediaFailed:
		icon = downloadIcon
	case MediaLoading:
		icon = cancelIcon
	}
	root.Children(func() {
		if data.Thumbnail != nil {
			ui.Box(c).Size(slot, slot).Radius(m.ThumbRadius()).Clip().Children(func() {
				ui.Image(c, data.Thumbnail).Absolute().Fill().Fit(ui.Cover)
				if data.Status != MediaReady {
					draw.CircleIcon(c, icon, t.Metrics().Media().ActionIconSize(), t.Colors().Control().VideoPlayIconBackground(), t.Colors().Control().VideoPlayIconForeground()).
						Size(m.IconSize(), m.IconSize()).Attach(ui.AnchorCenter, ui.AnchorCenter).Children(func() {
						if data.Status == MediaLoading {
							progressArc(c, data.Progress)
						}
					})
				}
			})
		} else {
			draw.CircleIcon(c, icon, t.Metrics().Media().ActionIconSize(), mix(bg, over, amount), fg).Size(slot, slot).Children(func() {
				if data.Status == MediaLoading {
					progressArc(c, data.Progress)
				}
			})
		}
		ui.Box(c).Grow(1).MinWidth(0).Height(slot).Children(func() {
			text(c, data.Name, t.Typography().ContentTitle(), p.text).Absolute().Top(nameTop - top).Left(0).Right(0).SingleLine()
			status := data.Size
			if data.StatusText != "" {
				status = data.StatusText
			}
			text(c, status, t.Typography().MessageTime(), p.muted).Absolute().Top(statusTop - top).Left(0).Right(0).SingleLine()
			if data.Thumbnail != nil && data.Link != "" {
				link := text(c, data.Link, t.Typography().Message(), p.link).Absolute().Top(m.ThumbLinkTop() - top).Left(0).Right(0).SingleLine()
				if o.OnLink != nil {
					link.Focusable().Cursor(ui.CursorPointer).OnClick(o.OnLink)
				}
			}
		})
	})
	return root
}

type VoiceData struct {
	Duration, StatusText string
	Samples              []float32
	Playing              bool
	Status               MediaStatus
	Progress             float32
}

// VoiceState is the host's seek position, normalized to [0,1].
type VoiceState struct{ Position float64 }
type VoiceOptions struct {
	Appearance
	OnToggle, OnDownload, OnCancel, OnRetry func()
	OnSeek                                  func(position float64)
}

// Voice shows a minimum-height waveform when samples have not arrived yet.
// Decoding and playback clocks belong to the host.
func Voice(c *ui.Context, data VoiceData, state *VoiceState, o VoiceOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().File()
	v := t.Metrics().Voice()
	p := colors(t, o.Appearance)
	root := ui.Row(c).FillWidth().MinWidth(0).Padding(m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft()).Gap(m.IconGap()).AlignItems(ui.Start)
	bg, _, fg := fileColors(t, o.Appearance)
	fn := o.OnToggle
	switch data.Status {
	case MediaRemote:
		fn = o.OnDownload
	case MediaLoading:
		fn = o.OnCancel
	case MediaFailed:
		fn = o.OnRetry
	}
	root.Children(func() {
		button := action(c, data.Duration, o.Disabled, fn).Size(m.IconSize(), m.IconSize())
		button.Children(func() {
			icon := playIcon
			if data.Playing {
				icon = pauseIcon
			}
			if data.Status == MediaRemote || data.Status == MediaFailed {
				icon = downloadIcon
			}
			if data.Status == MediaLoading {
				icon = cancelIcon
			}
			draw.CircleIcon(c, icon, t.Metrics().Media().ActionIconSize(), bg, fg).Absolute().Fill().PassThrough()
			if data.Status == MediaLoading {
				progressArc(c, data.Progress)
			}
		})
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			var wave ui.Element
			if o.OnSeek != nil {
				wave = ui.SliderBase(c, &state.Position, 0, 1).Label("Seek voice").Disabled(o.Disabled || data.Status != MediaReady).OnChange(func() { o.OnSeek(state.Position) })
			} else {
				wave = ui.Box(c).Label("Voice waveform")
			}
			wave.FillWidth().Height(m.StatusTop() - m.PaddingTop()).Draw(func(painter *ui.Painter, r ui.Rect) {
				active, inactive := t.Colors().Message().WaveformInActive(), t.Colors().Message().WaveformInInactive()
				if o.Outgoing {
					active, inactive = t.Colors().Message().WaveformOutActive(), t.Colors().Message().WaveformOutInactive()
				}
				if o.Selected {
					active, inactive = t.Colors().Message().WaveformInActiveSelected(), t.Colors().Message().WaveformInInactiveSelected()
					if o.Outgoing {
						active, inactive = t.Colors().Message().WaveformOutActiveSelected(), t.Colors().Message().WaveformOutInactiveSelected()
					}
				}
				count := int(r.W / (v.BarWidth() + v.BarGap()))
				n := len(data.Samples)
				for i := range count {
					peak := float32(0)
					if n > 0 {
						from, to := i*n/count, max(i*n/count+1, (i+1)*n/count)
						for _, sample := range data.Samples[from:to] {
							peak = max(peak, sample)
						}
					}
					h := v.MinHeight() + (v.MaxHeight()-v.MinHeight())*peak
					color := inactive
					if float64(i) < float64(count)*state.Position {
						color = active
					}
					painter.Fill(ui.Rect{X: r.X + float32(i)*(v.BarWidth()+v.BarGap()), Y: r.Y + (r.H-h)/2, W: v.BarWidth(), H: h}, color, v.BarWidth()/2)
				}
			})
			status := data.Duration
			if data.StatusText != "" {
				status = data.StatusText
			}
			text(c, status, t.Typography().MessageTime(), p.muted)
		})
	})
	return root
}
