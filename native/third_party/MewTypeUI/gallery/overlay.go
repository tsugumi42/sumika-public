package gallery

import (
	"image"
	"image/color"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// Lucide check geometry, ISC/MIT; shared notice in THIRD_PARTY_NOTICES.md.
var galleryCheck = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>`))

func init() {
	Register(Page{ID: "overlay/menus", Area: "overlay", Title: "Menus", View: overlayMenus})
	Register(Page{ID: "overlay/tooltip", Area: "overlay", Title: "Tooltip", View: overlayTooltip})
	Register(Page{ID: "overlay/box", Area: "overlay", Title: "Box", View: overlayBoxes})
	Register(Page{ID: "overlay/toast", Area: "overlay", Title: "Toast", View: overlayToasts})
	Register(Page{ID: "overlay/viewer", Area: "overlay", Title: "Media viewer", View: overlayViewer})
	Register(Page{ID: "overlay/native", Area: "overlay", Title: "Native window edges", View: overlayNative})
}
func overlayPage(c *ui.Context, title string, content func()) {
	t := theme.T(c)
	ui.Column(c).Fill().Padding(t.Metrics().Dialog().PaddingLeft()).Gap(t.Metrics().Dialog().PaddingTop()).Children(func() {
		ui.Text(c, title).FontSize(t.Typography().DialogTitle().Size() * c.Preferences().TextScale).FontWeight(t.Typography().DialogTitle().Weight())
		content()
	})
}
func overlayMenus(c *ui.Context) {
	type model struct {
		plain, icons, context overlay.MenuState
		chosen                string
	}
	s := ui.Local(c.Root(), "overlay-menus", func() model { return model{} })
	items := []overlay.MenuItem{
		{ID: "reply", Label: "Reply", Shortcut: "Ctrl+R"},
		{ID: "copy", Label: "Copy", Shortcut: "Ctrl+C", Icon: galleryCheck},
		{ID: "pin", Label: "Pinned", Checkable: true, Checked: true},
		{ID: "unavailable", Label: "Unavailable", Disabled: true},
		{ID: "separator", Separator: true},
		{ID: "send", Label: "Send to…", Children: []overlay.MenuItem{{ID: "saved", Label: "Saved messages"}, {ID: "folders", Label: "Folders", Children: []overlay.MenuItem{{ID: "work", Label: "Work"}, {ID: "friends", Label: "Friends"}}}}},
		{ID: "delete", Label: "Delete", Danger: true},
	}
	selectID := func(id string) { s.chosen = id }
	overlayPage(c, "Menus", func() {
		ui.Text(c, "Hover, press, keyboard navigation, disabled, checked, shortcuts, danger, nested submenus and edge flipping.")
		ui.Row(c).Gap(theme.T(c).Metrics().Dialog().PaddingTop()).Children(func() {
			b := ui.Button(c, "Plain popup")
			overlay.PopupMenu(c, b, items, &s.plain, overlay.MenuOptions{Label: "Plain menu", OnSelect: selectID})
			b = ui.Button(c, "Icon spacing / wide separator")
			overlay.PopupMenu(c, b, items, &s.icons, overlay.MenuOptions{Label: "Icon menu", Icons: true, ExpandedSeparators: true, OnSelect: selectID})
		})
		target := ui.Button(c, "Right-click here or press Shift+F10").FillWidth().Height(theme.T(c).Metrics().Dialog().TitleHeight())
		overlay.ContextMenu(c, target, items, &s.context, overlay.MenuOptions{Label: "Context menu", Icons: true, OnSelect: selectID})
		ui.Text(c, "Selected ID: "+s.chosen)
	})
}
func overlayTooltip(c *ui.Context) {
	overlayPage(c, "Tooltip", func() {
		ui.Text(c, "悬停一秒或按 Tab 获取键盘焦点。Esc 关闭并保留焦点；离开后可再次显示。")
		for _, item := range []struct{ id, label, tip string }{{"short", "Timestamp", "Tuesday, 3 November 2026 at 10:24"}, {"long", "Long tooltip", "This prepared text wraps inside the source maximum width. It remains readable at larger system text sizes and flips away from the window edge."}, {"disabled", "Disabled target", "Disabled targets do not show a tooltip."}} {
			ui.Row(c).Key(item.id).Children(func() {
				b := ui.Button(c, item.label).Disabled(item.id == "disabled")
				overlay.Tooltip(c, b, item.tip, overlay.TooltipOptions{})
			})
		}
	})
}
func overlayBoxes(c *ui.Context) {
	type model struct {
		ordinary, danger, wide, locked overlay.BoxState
		last                           string
	}
	s := ui.Local(c.Root(), "overlay-boxes", func() model { return model{} })
	overlayPage(c, "Box", func() {
		for _, example := range []struct {
			label string
			state *overlay.BoxState
		}{{"Open standard box", &s.ordinary}, {"Open dangerous action", &s.danger}, {"Open long scrolling box", &s.wide}, {"Open locked box", &s.locked}} {
			ui.Button(c, example.label).OnClick(func() { example.state.Open = true })
		}
		ui.Text(c, "Last action: "+s.last)
	})
	action := func(id string) { s.last = id }
	overlay.Box(c, &s.ordinary, "Edit information", overlay.BoxOptions{CloseButton: true, AdditionalTitle: "Example", Actions: []overlay.BoxAction{{ID: "save", Label: "Save", Default: true}, {ID: "cancel", Label: "Cancel"}, {ID: "disabled", Label: "Disabled", Disabled: true}}, OnAction: action}, func() { ui.Text(c, "The host supplies content and accepts actions. Tab stays inside this dialog.") })
	overlay.Box(c, &s.danger, "Delete message?", overlay.BoxOptions{Actions: []overlay.BoxAction{{ID: "delete", Label: "Delete", Danger: true, Default: true}, {ID: "cancel", Label: "Cancel"}}, OnAction: action}, func() { ui.Text(c, "This gallery uses invented data. Choose Delete or Cancel.") })
	overlay.Box(c, &s.wide, "Long body", overlay.BoxOptions{Wide: true, CloseButton: true, Actions: []overlay.BoxAction{{ID: "done", Label: "Done", Default: true}}}, func() {
		for range 24 {
			ui.Text(c, "A scrolling body leaves the title and action row visible.")
		}
	})
	overlay.Box(c, &s.locked, "Explicit choice required", overlay.BoxOptions{Locked: true, Actions: []overlay.BoxAction{{ID: "done", Label: "Done"}}}, func() { ui.Text(c, "Escape and backdrop presses leave this dialog open.") })
}
func overlayToasts(c *ui.Context) {
	type model struct {
		bottom, center, multiline, action overlay.ToastState
		undone                            bool
	}
	s := ui.Local(c.Root(), "overlay-toasts", func() model { return model{} })
	overlayPage(c, "Toast", func() {
		for _, example := range []struct {
			label string
			state *overlay.ToastState
		}{{"Bottom slide", &s.bottom}, {"Center fade", &s.center}, {"Multiline", &s.multiline}, {"Action / persistent", &s.action}} {
			ui.Button(c, example.label).OnClick(example.state.Show)
		}
		if s.undone {
			ui.Text(c, "Undo action accepted")
		}
	})
	overlay.Toast(c, &s.bottom, "Link copied", overlay.ToastOptions{})
	overlay.Toast(c, &s.center, "Saved", overlay.ToastOptions{Center: true})
	overlay.Toast(c, &s.multiline, "This is a longer notification that wraps at the multiline toast width and slides from the bottom.", overlay.ToastOptions{Multiline: true})
	overlay.Toast(c, &s.action, "Message removed", overlay.ToastOptions{Infinite: true, Action: "Undo", OnAction: func() { s.undone = true }})
}
func overlayViewer(c *ui.Context) {
	type model struct {
		state    overlay.ViewerState
		items    []overlay.ViewerItem
		selected string
	}
	s := ui.Local(c.Root(), "overlay-viewer", func() model {
		a := image.NewRGBA(image.Rect(0, 0, 960, 540))
		b := image.NewRGBA(image.Rect(0, 0, 300, 900))
		for y := range 540 {
			for x := range 960 {
				a.SetRGBA(x, y, color.RGBA{R: uint8(x * 255 / 960), G: uint8(y * 255 / 540), B: 128, A: 255})
			}
		}
		for y := range 900 {
			for x := range 300 {
				b.SetRGBA(x, y, color.RGBA{R: 80, G: uint8(y * 255 / 900), B: uint8(x * 255 / 300), A: 255})
			}
		}
		return model{items: []overlay.ViewerItem{{ID: "landscape", Title: "Evening study", Information: "Mira · 3 November 2026 · 1 of 4", Caption: "Generated fixture, no external assets", Image: ui.NewBitmap(a)}, {ID: "portrait", Title: "Color study", Information: "Mira · 2 of 4", Image: ui.NewBitmap(b)}, {ID: "loading", Title: "Loading photo", Information: "3 of 4", Phase: overlay.ViewerLoading, Progress: .4}, {ID: "failed", Title: "Unavailable photo", Information: "4 of 4", Phase: overlay.ViewerFailed, Error: "The host could not prepare this image."}}}
	})
	overlayPage(c, "Media viewer", func() {
		ui.Text(c, "Double-click the image, Ctrl+0 or middle-click toggles fit / actual size. Click dismissal starts after 500 ms; Esc closes immediately.")
		for _, item := range s.items {
			ui.Button(c, item.Title).Key(item.ID).OnClick(func() { s.state.Show(item.ID) })
		}
		ui.Button(c, "Thumbnail with intrinsic 1920 × 1080").OnClick(func() {
			s.items[0].IntrinsicWidth, s.items[0].IntrinsicHeight = 1920, 1080
			s.state.Show(s.items[0].ID)
		})
		ui.Text(c, "Selected ID: "+s.selected)
	})
	overlay.Viewer(c, &s.state, s.items, overlay.ViewerOptions{OnChange: func(id string) { s.selected = id }, OnRetry: func(id string) { s.items[3].Phase = overlay.ViewerLoading; s.items[3].Progress = 0 }})
}

func overlayNative(c *ui.Context) {
	type model struct {
		edge, long       overlay.MenuState
		selected         string
		items, longItems []overlay.MenuItem
	}
	s := ui.Local(c.Root(), "native-edges", func() model {
		long := make([]overlay.MenuItem, 40)
		for i := range long {
			id := strconv.Itoa(i)
			long[i] = overlay.MenuItem{ID: id, Label: "Action " + id}
		}
		return model{longItems: long, items: []overlay.MenuItem{{ID: "copy", Label: "Copy"}, {ID: "more", Label: "More", Children: []overlay.MenuItem{{ID: "nested", Label: "Nested action"}}}, {ID: "disabled", Label: "Disabled", Disabled: true}}}
	})
	t := theme.T(c)
	selectID := func(id string) { s.selected = id }
	ui.Column(c).Fill().Padding(t.Metrics().Dialog().PaddingLeft()).Gap(t.Metrics().Dialog().PaddingTop()).Children(func() {
		ui.Text(c, "Native window edges")
		ui.Text(c, "Move the host near a screen edge. Menus and tooltips extend beyond the host and remain inside the display work area.")
		ui.Text(c, "Selected ID: "+s.selected)
		b := ui.Button(c, "Long native menu")
		overlay.PopupMenu(c, b, s.longItems, &s.long, overlay.MenuOptions{Label: "Long menu", OnSelect: selectID})
		b = ui.Button(c, "Edge popup").Absolute().Right(0).Bottom(0)
		overlay.PopupMenu(c, b, s.items, &s.edge, overlay.MenuOptions{Label: "Edge menu", OnSelect: selectID})
		overlay.Tooltip(c, b, "Native tooltip beyond host bounds", overlay.TooltipOptions{})
	})
}
