package content

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
	"math"
)

type MediaStatus uint8

const (
	MediaReady MediaStatus = iota
	MediaRemote
	MediaLoading
	MediaFailed
)

// Media keeps intrinsic geometry available while the host loads a bitmap.
// Width/Height may be zero when Bitmap supplies both dimensions.
type Media struct {
	ID, Label     string
	Bitmap        *ui.Bitmap
	Width, Height float32
	Status        MediaStatus
	// Progress is normalized [0,1]; StatusText is prepared localized feedback.
	Progress   float32
	StatusText string
}

func (m Media) dimensions() (float32, float32) {
	if m.Width > 0 && m.Height > 0 {
		return m.Width, m.Height
	}
	w, h := m.Bitmap.Size()
	return float32(w), float32(h)
}

type MediaOptions struct {
	Appearance
	Width                                 float32
	Corners                               *Corners
	Spoiler                               *bool // true hides the image; revealing is a host-visible state change
	OnOpen, OnDownload, OnCancel, OnRetry func()
	// Caption is measured inside the component to negotiate media width; the
	// host renders that prepared caption in its regular message content slot.
	Caption  string
	InBubble bool
	// BottomInfo paints image information only when this media owns the tail.
	BottomInfo func()
	// Preview and LastOutgoing are host presentation states. SavedMessages
	// disables the last-outgoing visibility rule.
	Preview, LastOutgoing, SavedMessages bool
}

func mediaIntent(m Media, o MediaOptions) func() {
	if o.Spoiler != nil && *o.Spoiler {
		return func() { *o.Spoiler = false }
	}
	switch m.Status {
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

// Photo fits within maxMediaSize and preserves the host bubble's corners.
func Photo(c *ui.Context, m Media, o MediaOptions) ui.Element {
	return picture(c, m, o, picturePhoto, "", false)
}

// Sticker has no bubble or padding. Animated frames are updated by the host.
func Sticker(c *ui.Context, m Media, o MediaOptions) ui.Element {
	return picture(c, m, o, pictureSticker, "", false)
}
func GIF(c *ui.Context, m Media, o MediaOptions) ui.Element {
	return picture(c, m, o, pictureGIF, "GIF", false)
}

// VideoOptions controls the playback marker independently of badge text.
type VideoOptions struct {
	MediaOptions
	ShowPlay bool
}

func Video(c *ui.Context, m Media, duration string, o VideoOptions) ui.Element {
	return picture(c, m, o.MediaOptions, pictureVideo, duration, o.ShowPlay)
}

type pictureKind uint8

const (
	picturePhoto pictureKind = iota
	pictureSticker
	pictureGIF
	pictureVideo
)

func picture(c *ui.Context, m Media, o MediaOptions, kind pictureKind, badge string, showPlay bool) ui.Element {
	t := theme.T(c)
	metrics := t.Metrics().Media()
	sticker, hoverBadge := kind == pictureSticker, kind == pictureGIF
	motionMedia := kind == pictureGIF || kind == pictureVideo
	limit := metrics.MaxSize()
	if sticker {
		limit = metrics.MaxStickerSize()
	} else if kind == pictureGIF {
		limit = metrics.MaxGifSize()
	}
	w, h := m.dimensions()
	originalWidth, originalHeight := w, h
	scale := min(1, limit/max(w, h))
	w, h = w*scale, h*scale
	if !sticker {
		w = max(w, metrics.MinPhotoSize())
		h = max(h, metrics.MinPhotoSize())
	}
	if o.Width > 0 {
		scale = min(1, o.Width/w)
		w, h = w*scale, h*scale
	}
	if !sticker && (o.InBubble || o.Caption != "") {
		maximum := limit
		if o.Width > 0 {
			maximum = min(maximum, o.Width)
		}
		captionWidth, _ := c.MeasureText(0, ui.Span{Text: o.Caption, Font: t.Typography().Font(), Size: t.Typography().Message().Size() * c.Preferences().TextScale, Weight: t.Typography().Message().Weight()})
		w = min(maximum, max(w, metrics.BubbleMinWidth(), captionWidth))
		uncropped := float32(math.Floor(float64(w * originalHeight / originalWidth)))
		// paint/video.md#画法与状态: crop behavior follows media kind, not text.
		if motionMedia {
			h = max(h, min(uncropped, float32(math.Floor(float64(h*4/3)))))
		} else if photoCanGrow(w, h, originalWidth, originalHeight) {
			h = max(h, uncropped)
		}
	}
	root := action(c, m.Label, o.Disabled, mediaIntent(m, o)).Description(m.StatusText).Width(w).MaxWidthPercent(100).AspectRatio(w / h).Padding(0)
	corners := radii(t, o.Corners)
	if sticker {
		corners = [4]float32{}
	}
	root.Radius(corners[:]...).Clip()
	if !sticker {
		root.Background(t.Colors().Media().ImageBackground())
	}
	root.Children(func() {
		if m.Bitmap != nil && (o.Spoiler == nil || !*o.Spoiler) {
			if sticker && o.Selected {
				selectedSticker(c, m.Bitmap)
			} else {
				image := ui.Image(c, m.Bitmap).Absolute().Fill().Fit(ui.Cover)
				if sticker {
					image.Fit(ui.Contain)
				}
			}
		}
		if o.Spoiler != nil && *o.Spoiler {
			atlas := spoilerTexture(t, true)
			reduced := c.Preferences().ReduceMotion
			ui.Box(c).Absolute().Fill().Background(t.Colors().Message().TimestampImgBackgroundHover()).Draw(func(p *ui.Painter, r ui.Rect) {
				atlas.draw(p, r, t.Colors().Message().TimestampImgForeground(), reduced)
			}).Children(func() {
				text(c, m.StatusText, t.Typography().Message(), t.Colors().Message().TimestampImgForeground()).Attach(ui.AnchorCenter, ui.AnchorCenter)
			})
		} else {
			// paint/video.md#画法与状态: transfer controls take precedence over play.
			if m.Status != MediaReady || showPlay {
				mediaControl(c, m)
			}
			if badge != "" && (!hoverBadge || root.Hovered() || m.Status == MediaLoading) {
				mediaBadge(c, badge, o.Selected)
			}
		}
		if o.Selected && !sticker {
			ui.Box(c).Absolute().Fill().Background(t.Colors().Message().SelectOverlay()).PassThrough()
		}
		// paint/message-bottom.md#图片与状态矩阵: ordinary photos reveal
		// information on hover, while pending/failed and previews keep it visible.
		if o.BottomInfo != nil && (root.Hovered() || m.Status == MediaLoading || m.Status == MediaFailed || o.Preview || (o.LastOutgoing && !o.SavedMessages)) {
			o.BottomInfo()
		}
	})
	return root
}
func mediaControl(c *ui.Context, m Media) {
	t := theme.T(c)
	metrics := t.Metrics().Media()
	size := metrics.ActionSize()
	icon := playIcon
	switch m.Status {
	case MediaRemote:
		icon = downloadIcon
	case MediaLoading:
		icon = cancelIcon
	case MediaFailed:
		icon = downloadIcon
	}
	// paint/album-state.md#状态文字底板: center the whole group so a
	// narrow tile fits both the control and its bounded, readable caption.
	ui.Column(c).Attach(ui.AnchorCenter, ui.AnchorCenter).MaxWidthPercent(100).Padding(0, metrics.BadgeInset()).AlignItems(ui.Center).Gap(metrics.BadgeInset()).PassThrough().Children(func() {
		draw.CircleIcon(c, icon, metrics.ActionIconSize(), t.Colors().Control().VideoPlayIconBackground(), t.Colors().Control().VideoPlayIconForeground()).
			Size(size, size).PassThrough().Children(func() {
			if m.Status == MediaLoading {
				progressArc(c, m.Progress)
			}
		})
		if m.StatusText != "" {
			_, height := c.MeasureText(0, ui.Span{Text: m.StatusText, Size: t.Typography().MessageTime().Size() * c.Preferences().TextScale, Font: t.Typography().Font()})
			ui.Box(c).MaxWidthPercent(100).Padding(metrics.BadgePaddingY(), metrics.BadgePaddingX()).Radius((height + 2*metrics.BadgePaddingY()) / 2).Background(t.Colors().Media().ImageBackground()).PassThrough().Children(func() {
				text(c, m.StatusText, t.Typography().MessageTime(), t.Colors().Message().TimestampImgForeground()).MaxWidthPercent(100).SingleLine()
			})
		}
	})
}
func mediaBadge(c *ui.Context, label string, selected bool) {
	t := theme.T(c)
	m := t.Metrics().Media()
	bg := t.Colors().Message().TimestampImgBackground()
	if selected {
		bg = t.Colors().Message().TimestampImgBackgroundSelected()
	}
	_, height := c.MeasureText(0, ui.Span{Text: label, Size: t.Typography().MessageTime().Size() * c.Preferences().TextScale, Font: t.Typography().Font()})
	ui.Box(c).Absolute().Top(m.BadgeInset()).Left(m.BadgeInset()).Padding(m.BadgePaddingY(), m.BadgePaddingX()).Radius((height + 2*m.BadgePaddingY()) / 2).Background(bg).PassThrough().Children(func() { text(c, label, t.Typography().MessageTime(), t.Colors().Message().TimestampImgForeground()) })
}

// AlbumOptions applies corners only at the outside perimeter. IDs identify tiles.
type AlbumOptions struct {
	Appearance
	Width                                 float32
	Corners                               *Corners
	OnOpen, OnDownload, OnCancel, OnRetry func(id string)
	// OnTileSize reports full laid-out tile dimensions in DIP, once per change.
	// Record these sizes; fetching/decoding remains host-owned.
	OnTileSize                                       func(id string, width, height float32)
	BottomInfo                                       func()
	Preview, LastOutgoing, SavedMessages, Processing bool
}

func Album(c *ui.Context, items []Media, o AlbumOptions) ui.Element {
	t := theme.T(c)
	width := o.Width
	if width == 0 {
		width = t.Metrics().Media().MaxSize()
	}
	width = min(width, t.Metrics().Media().MaxSize())
	root := ui.Box(c).Width(width).MaxWidthPercent(100)
	cache := ui.Local(root, albumCacheKey{}, func() albumCache { return albumCache{} })
	cache.update(items, t.Metrics().Media().MaxSize(), t.Metrics().Media().MinPhotoSize(), t.Metrics().Media().AlbumGap())
	root.AspectRatio(cache.width / cache.height)
	corners := radii(t, o.Corners)
	root.Children(func() {
		for i, item := range items {
			rect := cache.tiles[i]
			r := [4]float32{}
			left, top := rect.X == 0, rect.Y == 0
			right, bottom := math.Abs(float64(rect.X+rect.W-cache.width)) < 1, math.Abs(float64(rect.Y+rect.H-cache.height)) < 1
			if left && top {
				r[0] = corners[0]
			}
			if top && right {
				r[1] = corners[1]
			}
			if bottom && right {
				r[2] = corners[2]
			}
			if bottom && left {
				r[3] = corners[3]
			}
			c.Key(item.ID)
			// paint/album-state.md#格子与控制: state belongs to this stable ID.
			offer := o.OnOpen
			switch item.Status {
			case MediaRemote:
				offer = o.OnDownload
			case MediaLoading:
				offer = o.OnCancel
			case MediaFailed:
				offer = o.OnRetry
			}
			var fn func()
			if offer != nil {
				fn = func() { offer(item.ID) }
			}
			tile := action(c, item.Label, o.Disabled, fn).Description(item.StatusText).Absolute().LeftPercent(rect.X / cache.width * 100).TopPercent(rect.Y / cache.height * 100).WidthPercent(rect.W / cache.width * 100).HeightPercent(rect.H / cache.height * 100).Radius(r[:]...).Clip().Background(t.Colors().Media().ImageBackground())
			if o.OnTileSize != nil {
				albumTileSize(c, tile, item.ID, o.OnTileSize)
			}
			tile.Children(func() {
				if item.Bitmap != nil {
					ui.Image(c, item.Bitmap).Absolute().Fill().Fit(ui.Cover)
				}
				if item.Status != MediaReady {
					mediaControl(c, item)
				}
				if o.Selected {
					ui.Box(c).Absolute().Fill().Background(t.Colors().Message().SelectOverlay()).PassThrough()
				}
			})
		}
		// paint/message-bottom.md#图片与状态矩阵: one badge for the whole album.
		processing := o.Processing
		for _, item := range items {
			processing = processing || item.Status == MediaLoading || item.Status == MediaFailed
		}
		if o.BottomInfo != nil && (root.Hovered() || o.Preview || processing || (o.LastOutgoing && !o.SavedMessages)) {
			o.BottomInfo()
		}
	})
	return root.Width(min(width, cache.width))
}

// Photo's expansion decision permits at least 3/4 of the original to remain
// visible, with the upstream one-pixel tolerance. This is an aspect-ratio rule,
// not an arbitrary maximum height: media_streaming_utility.cpp:389-410.
func photoCanGrow(w, h, originalWidth, originalHeight float32) bool {
	smallest := min(w, h, originalWidth, originalHeight)
	numerator, denominator := 3*smallest-4, 4*smallest
	if 2*numerator < denominator {
		return false
	}
	scale := max(w/originalWidth, h/originalHeight)
	expandedWidth := float32(math.Round(float64(originalWidth * scale)))
	expandedHeight := float32(math.Round(float64(originalHeight * scale)))
	return expandedWidth <= w && expandedHeight*numerator <= h*denominator
}
