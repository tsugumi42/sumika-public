package compose

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/locales"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type Attachment struct {
	ID, Name, Status string
	Preview          ui.ImageSource
	// AsFile keeps an image in the source's thumbnail/file presentation.
	AsFile            bool
	Failed, Preparing bool
}

type AttachmentOptions struct {
	Disabled                          bool
	OpenLabel, RemoveLabel, EditLabel string
	OnOpen, OnRemove, OnEdit          func(id string)
	// OnMove requests moving the source before the target; the host updates its slice.
	OnMove func(sourceID, targetID string)
	// Width limits the confirmation content; zero uses sendMediaPreviewSize.
	Width float32
	// Ungrouped keeps each media item separate, matching the confirmation toggle.
	Ungrouped bool
}

type attachmentDrag string
type attachmentMove struct {
	source, target string
	pending        bool
}

// Attachments uses the send-confirmation preview layout: media keep their
// aspect ratio, files use 44/64 DIP leading previews, and items stack vertically.
// Scrolling belongs to the confirmation's parent. Items and actions retain IDs.
func Attachments(c *ui.Context, items []Attachment, o AttachmentOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Attachment()
	width := m.Width()
	if o.Width > 0 {
		width = min(width, o.Width)
	}
	root := ui.Column(c).Width(width).MaxWidthPercent(100).Gap(m.Gap()).Disabled(o.Disabled)
	pending := ui.Local(root, "move", func() attachmentMove { return attachmentMove{} })
	pendingWidth := ui.Local(root, "bounds", func() ui.Handle { return ui.Handle{} })
	root.Bind(pendingWidth)
	if measured := pendingWidth.Bounds(c).W; measured > 0 {
		width = min(width, measured)
	}
	root.OnChange(func() {
		if pending.pending {
			move := *pending
			pending.pending = false
			o.OnMove(move.source, move.target)
		}
	})
	root.Children(func() {
		for start := 0; start < len(items); {
			end := start + 1
			if !o.Ungrouped && items[start].Preview != nil && !items[start].AsFile {
				for end < len(items) && end-start < 10 && items[end].Preview != nil && !items[end].AsFile {
					end++
				}
			}
			if end-start > 1 {
				c.Key(items[start].ID)
				attachmentAlbum(c, items[start:end], o, width, root, pending)
			} else {
				item := items[start]
				c.Key(item.ID)
				card := ui.Box(c).FillWidth().Shrink(0).Label(item.Name)
				attachmentDrop(card, root, item.ID, pending, o)
				card.Children(func() {
					if item.Preview != nil && !item.AsFile {
						attachmentMedia(c, item, o, width, card)
					} else {
						attachmentFile(c, item, o, card)
					}
				})
			}
			start = end
		}
	})
	return root
}

func imageSize(source ui.ImageSource) (float32, float32) {
	switch image := source.(type) {
	case *ui.Bitmap:
		w, h := image.Size()
		return float32(w), float32(h)
	case *ui.SVG:
		return image.Size()
	}
	panic("compose: attachment preview must be a prepared Bitmap or SVG")
}

func attachmentMedia(c *ui.Context, item Attachment, o AttachmentOptions, width float32, card ui.Element) {
	t := theme.T(c)
	m := t.Metrics().Attachment()
	w, h := imageSize(item.Preview)
	previewWidth := min(width, max(m.MinMediaWidth(), w))
	previewHeight := h * previewWidth / w
	maxHeight := min(m.MaxMediaHeight(), previewWidth*m.MediaHeightRatio())
	if previewHeight > maxHeight {
		previewWidth = max(m.MinMediaWidth(), previewWidth*maxHeight/previewHeight)
		previewHeight = maxHeight
	}
	height := max(previewHeight, m.ControlHeight()+2*m.ControlTop())
	card.Height(height).Radius(t.Metrics().Message().RadiusSmall()).Background(t.Colors().Window().BackgroundHover())
	ui.ButtonBase(c).Size(previewWidth, previewHeight).Attach(ui.AnchorCenter, ui.AnchorCenter).Label(label(o.OpenLabel, locales.Text(c.Locale(), "openAttachment")) + " " + item.ID).OnClick(func() {
		if o.OnOpen != nil {
			o.OnOpen(item.ID)
		}
	}).Children(func() { ui.Image(c, item.Preview).Fill().Fit(ui.Contain) })
	attachmentControls(c, item, o, true)
	if item.Status != "" && (item.Preparing || item.Failed) {
		fg := t.Colors().Premium().ButtonForeground()
		if item.Failed {
			fg = t.Colors().Semantic().Danger()
		}
		text(c, item.Status, t.Typography().Compose(), fg).Absolute().Bottom(m.ControlTop()).Left(m.ControlRight()).Right(m.ControlRight()).SingleLine().Background(t.Colors().Control().RadialBackground())
	}
}

func attachmentFile(c *ui.Context, item Attachment, o AttachmentOptions, card ui.Element) {
	t := theme.T(c)
	m := t.Metrics().Attachment()
	size, gap, nameTop, statusTop := m.FileSize(), m.FileGap(), m.FileNameTop(), m.FileStatusTop()
	if item.Preview != nil {
		size, gap, nameTop, statusTop = m.ThumbSize(), m.ThumbGap(), m.NameTop(), m.StatusTop()
	}
	height := max(size, statusTop+fontLineHeight(c, t, t.Typography().Compose()))
	card.Height(height)
	ui.ButtonBase(c).Absolute().Left(0).Top(0).Size(size, size).Radius(t.Metrics().Control().RadiusSmall()).Center().Label(label(o.OpenLabel, locales.Text(c.Locale(), "openAttachment")) + " " + item.ID).OnClick(func() {
		if o.OnOpen != nil {
			o.OnOpen(item.ID)
		}
	}).Children(func() {
		if item.Preview != nil {
			ui.Image(c, item.Preview).Fill().Fit(ui.Cover)
		} else {
			ui.Box(c).Fill().Radius(size / 2).Background(t.Colors().Message().FileInBackground()).Center().Children(func() {
				draw.Element(c, fileIcon, t.Metrics().Compose().IconSize(), t.Metrics().Compose().IconSize()).TextColor(t.Colors().Message().FileInIconForeground())
			})
		}
	})
	buttons := float32(1)
	if o.OnEdit != nil {
		buttons++
	}
	right := buttons*m.ActionSize() + m.ControlRight()
	text(c, item.Name, t.Typography().ComposeTitle(), t.Colors().Message().FileNameInForeground()).Absolute().Left(size + gap).Right(right).Top(nameTop).SingleLine()
	fg := t.Colors().Media().InForeground()
	if item.Failed {
		fg = t.Colors().Semantic().Danger()
	}
	text(c, item.Status, t.Typography().Compose(), fg).Absolute().Left(size + gap).Right(right).Top(statusTop).SingleLine()
	attachmentControls(c, item, o, false)
}

func attachmentControls(c *ui.Context, item Attachment, o AttachmentOptions, media bool) {
	t := theme.T(c)
	m := t.Metrics().Attachment()
	w, h, top, right := m.ActionSize(), m.ActionSize(), m.FileControlTop(), m.FileControlRight()
	fg, bg := t.Colors().Menu().IconForeground(), ui.Color{}
	if media {
		w, h, top, right = m.ControlWidth()/2, m.ControlHeight(), m.ControlTop(), m.ControlRight()
		fg, bg = t.Colors().Control().RadialForeground(), t.Colors().Control().RadialBackground()
	}
	ui.Row(c).Absolute().Right(right).Top(top).Height(h).Gap(0).Radius(h / 2).Background(bg).Children(func() {
		if o.OnEdit != nil {
			c.Key("edit")
			ui.ButtonBase(c).Size(w, h).Center().Label(label(o.EditLabel, locales.Text(c.Locale(), "editAttachment")) + " " + item.ID).Disabled(item.Preparing).OnClick(func() { o.OnEdit(item.ID) }).Children(func() {
				draw.Element(c, editIcon, min(t.Metrics().Compose().IconSize(), w), min(t.Metrics().Compose().IconSize(), h)).TextColor(fg)
			})
		}
		c.Key("remove")
		ui.ButtonBase(c).Size(w, h).Center().Label(label(o.RemoveLabel, locales.Text(c.Locale(), "removeAttachment")) + " " + item.ID).OnClick(func() {
			if o.OnRemove != nil {
				o.OnRemove(item.ID)
			}
		}).Children(func() {
			draw.Element(c, closeIcon, min(t.Metrics().Compose().IconSize(), w), min(t.Metrics().Compose().IconSize(), h)).TextColor(fg)
		})
	})
}

func attachmentDrop(card, root ui.Element, id string, pending *attachmentMove, o AttachmentOptions) {
	if o.OnMove == nil {
		return
	}
	card.Drag(attachmentDrag(id))
	if source, ok := ui.Drop[attachmentDrag](card); ok && string(source) != id {
		*pending = attachmentMove{source: string(source), target: id, pending: true}
		root.SetChanged()
	}
}

func attachmentAlbum(c *ui.Context, items []Attachment, o AttachmentOptions, width float32, root ui.Element, pending *attachmentMove) {
	t := theme.T(c)
	m := t.Metrics().Attachment()
	group := ui.Box(c).FillWidth().Shrink(0).Label(locales.Text(c.Locale(), "attachmentAlbum"))
	cache := ui.Local(group, "layout", func() attachmentLayout { return attachmentLayout{} })
	cache.update(items, width, m.AlbumMinimum(), m.AlbumGap())
	group.Height(cache.height)
	group.Children(func() {
		for i, item := range items {
			r := cache.tiles[i]
			c.Key(item.ID)
			tile := ui.Box(c).Absolute().Left(r.X).Top(r.Y).Size(r.W, r.H).Radius(t.Metrics().Message().RadiusSmall()).Clip().Label(item.Name)
			attachmentDrop(tile, root, item.ID, pending, o)
			tile.Children(func() {
				ui.ButtonBase(c).Fill().Label(label(o.OpenLabel, locales.Text(c.Locale(), "openAttachment")) + " " + item.ID).OnClick(func() {
					if o.OnOpen != nil {
						o.OnOpen(item.ID)
					}
				}).Children(func() { ui.Image(c, item.Preview).Fill().Fit(ui.Cover) })
				attachmentControls(c, item, o, true)
				if item.Preparing || item.Failed {
					text(c, item.Status, t.Typography().Compose(), t.Colors().Premium().ButtonForeground()).Absolute().Bottom(m.ControlTop()).Left(m.ControlRight()).Right(m.ControlRight()).SingleLine().Background(t.Colors().Control().RadialBackground())
				}
			})
		}
	})
}
