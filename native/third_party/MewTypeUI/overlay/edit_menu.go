package overlay

import "github.com/egoist/mygo/ui"

// EditContextMenu gives a Selectable body one native menu with host items and
// standard Copy / Select All. Build after binding scope in the current pass.
// Standard items execute through mygo, including the body's CopyData callback.
// The menu preserves the focused paragraph; focusing its container would hide
// the selection. See paint/edit-menu.md#选区、焦点和画法.
func EditContextMenu(c *ui.Context, scope ui.Handle, build func(*ui.Menu)) ui.Element {
	body := c.Resolve(scope)
	if body.Valid() {
		body.ContextMenu(func(m *ui.Menu) {
			if build != nil {
				build(m)
				m.Separator()
			}
			m.EditItems()
		})
	}
	return body
}
