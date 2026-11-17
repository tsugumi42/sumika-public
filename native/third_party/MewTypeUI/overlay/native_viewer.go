package overlay

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type nativeViewer struct {
	host    *nativeHost
	window  *mygo.Window
	state   *ViewerState
	items   []ViewerItem
	options ViewerOptions
	closing bool
}

// Viewer opens prepared media in an independent window covering the owner's
// monitor, including its taskbar area. Keep it outside virtual thumbnail rows,
// inside Native. Deleting the selected ID closes the window.
func Viewer(c *ui.Context, state *ViewerState, items []ViewerItem, o ViewerOptions) ui.Element {
	root := ui.Box(c).Size(0, 0)
	if !state.Open {
		slot := ui.Local(c.Root(), nativeKey{}, func() nativeSlot { return nativeSlot{} })
		if slot.host != nil && slot.host.viewer != nil && slot.host.viewer.state == state {
			slot.host.viewer.close()
		}
		return root
	}
	h := native(c)
	h.viewerSeen = true
	if h.viewer != nil && h.viewer.state == state {
		h.viewer.items, h.viewer.options = items, o
		return root
	}
	if h.viewer != nil {
		h.viewer.close()
	}
	if h.menu != nil {
		h.menu.close(false)
	}
	if h.tip != nil {
		h.tip.close()
	}
	v := &nativeViewer{host: h, state: state, items: items, options: o}
	h.viewer = v
	direction := c.Direction()
	h.window.Update(func() {
		if h.viewer != v || v.closing {
			return
		}
		at := h.window.Bounds()
		v.window = mygo.NewWindow(mygo.WindowOptions{
			Title: "MewTypeUI media viewer", Parent: h.window, X: at.X, Y: at.Y, Width: at.Width, Height: at.Height,
			Hidden: true, Frameless: true, Transparent: true, SkipTaskbar: true, DisableResize: true,
			DisableMinimize: true, DisableMaximize: true, DisableShadow: true,
			Content: ui.View(func(c *ui.Context) {
				theme.Apply(c, h.theme)
				c.Root().Direction(direction).Background(ui.Transparent)
				o := v.options
				o.OnClose = v.close
				viewerContent(c, state, v.items, o)
			}),
		})
		placeViewer(v.window, h.window)
		v.window.OnReadyToShow(func() {
			if !v.closing {
				v.window.Show()
			}
		})
		v.window.OnClose(func(e *mygo.CloseEvent) { e.PreventDefault(); v.close() })
		v.window.OnBlur(v.close)
		off := mygo.Screen.OnDisplaysChanged(func() { placeViewer(v.window, h.window) })
		v.window.OnClosed(func() { off(); v.close() })
	})
	return root
}
func (v *nativeViewer) close() {
	if v.closing {
		return
	}
	v.closing = true
	v.state.Close()
	if v.host.viewer == v {
		v.host.viewer = nil
	}
	if v.options.OnClose != nil {
		v.options.OnClose()
	}
	v.host.window.Invalidate()
	v.host.window.Update(func() {
		if v.window != nil {
			v.window.Destroy()
		}
	})
}
