package dialogs

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// RailState, like ListState, is per window. A locked folder remains invokable
// and reports OnLocked while selection stays on the current folder.
type RailState struct {
	SelectedID string
	View       ui.ListState
	cursor     int
	selected   string
	choice     ui.Selection[string]
}

func Rail(c *ui.Context, folders []Folder, state *RailState, o RailOptions) ui.Element {
	if state.selected != state.SelectedID {
		setChoice(&state.choice, state.SelectedID)
		state.selected = state.SelectedID
	}
	state.cursor = -1
	for i := range folders {
		if folders[i].ID == state.SelectedID {
			state.cursor = i
			break
		}
	}
	state.View.Selected = &state.cursor
	state.View.Selection = &state.choice
	state.View.MenuKeepsSelection = true
	state.View.Key = func(i int) any { return folders[i].ID }
	state.View.Label = func(i int) string { return folders[i].Name }
	state.View.Reorder = nil
	if o.OnReorder != nil {
		state.View.Reorder = func(rows []int, to int) {
			ids := make([]string, len(rows))
			for i, row := range rows {
				ids[i] = folders[row].ID
			}
			before := ""
			if to < len(folders) {
				before = folders[to].ID
			}
			o.OnReorder(ids, before)
		}
	}
	t := theme.T(c)
	choose := func(index int) {
		if index < 0 {
			return
		}
		f := &folders[index]
		if f.Locked {
			setChoice(&state.choice, state.SelectedID)
			if o.OnLocked != nil {
				o.OnLocked(f.ID)
			}
			return
		}
		state.SelectedID, state.selected = f.ID, f.ID
		setChoice(&state.choice, f.ID)
		if o.OnSelect != nil {
			o.OnSelect(f.ID)
		}
	}
	root := ui.List(c, &state.View, len(folders)).Width(t.Metrics().Sidebar().Width()).Padding(0).Gap(0).Background(t.Materials().Sidebar().Background())
	root.Rows(func(row ui.ListRow) {
		folderRow(c, folders[row.Index], row.Selected() && !folders[row.Index].Locked, o, &row)
	})
	root.OnChange(func() { choose(state.cursor) })
	return root
}

// folderRow draws one folder; the mygo list row lr takes the click, the name and the press state.
func folderRow(c *ui.Context, item Folder, selected bool, o RailOptions, lr *ui.ListRow) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Sidebar()
	p := t.Colors().Sidebar()
	bg, fg, ic, bgBadge := t.Materials().Sidebar().Background(), p.TextForeground(), p.IconForeground(), p.BadgeBackground()
	if item.Muted {
		bgBadge = p.BadgeBackgroundMuted()
	}
	if selected {
		bg, fg, ic, bgBadge = p.BackgroundActive(), p.TextForegroundActive(), p.IconForegroundActive(), p.BadgeBackgroundActive()
		if item.Muted {
			bgBadge = p.BadgeBackgroundMutedActive()
		}
	}
	badgeFg := p.BadgeForeground()
	if item.Locked {
		opacity := m.LockedOpacity()
		fg, ic, bgBadge, badgeFg = fg.Alpha(opacity), ic.Alpha(opacity), bgBadge.Alpha(opacity), badgeFg.Alpha(opacity)
	}
	_, line := measure(c, "Mg", t.Typography().Sidebar())
	_, labelHeight := c.MeasureText(m.Width()-2*m.TextGap(), ui.Span{Text: item.Name, Font: t.Typography().Font(), Size: t.Typography().Sidebar().Size() * c.Preferences().TextScale, Weight: t.Typography().Sidebar().Weight()})
	labelHeight = min(labelHeight, 3*line)
	height, top := m.ItemMinHeight()+max(float32(0), labelHeight-line), m.TextTop()
	if o.Mode == IconsOnly {
		height = m.IconsOnlyHeight()
	}
	if o.Mode == TextOnly {
		height, top = m.TextOnlyHeight()+max(float32(0), labelHeight-line), m.TextOnlyTop()
	}
	root := ui.Box(c).FillWidth().Height(height).Shrink(0).Background(bg).Clip()
	ripple(c, root, p.BackgroundPressed(), lr)
	if o.Menu != nil {
		root.ContextMenu(func(menu *ui.Menu) { o.Menu(menu, item.ID) })
	}
	root.Children(func() {
		if o.Mode != TextOnly {
			src := item.Icon
			if src == nil {
				src = folderIcon
			}
			iconTop := m.IconTop()
			if o.Mode == IconsOnly {
				iconTop = (height - m.IconSize()) / 2
			}
			// paint/icon-glyph.md#图形限框: host Lucide artwork keeps the 36 DIP slot.
			var glyph ui.Element
			if item.Icon == nil {
				glyph = icon(c, src, m.IconSize(), ic)
			} else {
				gm := t.Metrics().Glyph()
				glyph = draw.HostGlyph(c, src, m.IconSize(), m.IconSize(), gm.Rail().Width(), gm.Rail().Height()).TextColor(ic)
			}
			glyph.Absolute().Left((m.Width() - m.IconSize()) / 2).Top(iconTop)
		}
		if o.Mode != IconsOnly {
			if item.Locked {
				ui.Row(c).Absolute().Top(top).Left(m.TextGap()).Right(m.TextGap()).AlignItems(ui.Center).Justify(ui.Center).Gap(t.Metrics().Conversation().TypeSkip()).Children(func() {
					icon(c, lockIcon, t.Metrics().Conversation().IconSize(), ic)
					text(c, item.Name, t.Typography().Sidebar(), fg).MaxLines(3).TextAlign(ui.Center).MinWidth(0).Shrink(1)
				})
			} else {
				text(c, item.Name, t.Typography().Sidebar(), fg).Absolute().Top(top).Left(m.TextGap()).Right(m.TextGap()).MaxLines(3).TextAlign(ui.Center)
			}
		}
		if item.Badge != "" {
			w, h := measure(c, item.Badge, t.Typography().SidebarBadge())
			h = max(h, m.BadgeHeight())
			w = max(h, w+2*m.BadgePadding())
			offset, badgeTop := m.BadgeOffsetX(), m.BadgeTop()
			if o.Mode == TextOnly {
				offset, badgeTop = m.TextOnlyBadgeOffsetX(), m.TextOnlyBadgeTop()
			}
			left := min(m.Width()/2+offset, m.Width()-w-t.Metrics().Scrollbar().TrackWidth())
			ui.Box(c).Absolute().Left(left-m.BadgeStroke()).Top(badgeTop-m.BadgeStroke()).Size(w+2*m.BadgeStroke(), h+2*m.BadgeStroke()).Radius(h/2 + m.BadgeStroke()).Background(bg).Center().Children(func() {
				badge(c, item.Badge, h, m.BadgePadding(), bgBadge, badgeFg, t.Typography().SidebarBadge())
			})
		}
		if item.Locked && o.Mode == IconsOnly {
			icon(c, lockIcon, t.Metrics().Conversation().IconSize(), ic).Absolute().Right(m.TextGap()).Bottom(m.IconTop())
		}
	})
	return root
}

func CompactWidth(c *ui.Context) float32 {
	m := theme.T(c).Metrics().Conversation()
	return m.PaddingLeft() + m.AvatarSize() + m.PaddingRight()
}
