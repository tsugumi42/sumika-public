package overlay

import (
	"math"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type nativeKey struct{}
type nativeSlot struct{ host *nativeHost }

type nativeHost struct {
	window                        *mygo.Window
	theme                         *theme.Theme
	menu                          *nativeMenu
	tip                           *nativeTooltip
	viewer                        *nativeViewer
	menuSeen, tipSeen, viewerSeen bool
}

// Native binds a native window to its component tree. Call it once per window,
// after theme.Apply, on every build. Missing owners close their native surfaces.
// All state and callbacks stay on mygo's UI thread; resources remain host-owned.
func Native(c *ui.Context, window *mygo.Window, content func()) ui.Element {
	slot := ui.Local(c.Root(), nativeKey{}, func() nativeSlot { return nativeSlot{} })
	if slot.host == nil {
		h := &nativeHost{window: window}
		slot.host = h
		window.OnMinimize(h.close)
		window.OnHide(h.close)
		window.OnClosed(h.close)
		window.OnBlur(func() {
			// A tooltip never takes focus; menu/viewer focus moves to an owned window.
			if h.tip != nil {
				h.tip.close()
			}
		})
	}
	h := slot.host
	h.theme = theme.T(c)
	h.menuSeen, h.tipSeen, h.viewerSeen = false, false, false
	root := ui.Box(c).Fill().Children(content)
	if h.menu != nil {
		if !h.menuSeen {
			h.menu.close(false)
		} else {
			h.menu.invalidate()
		}
	}
	if h.tip != nil {
		if !h.tipSeen {
			h.tip.hide()
		} else if h.tip.window != nil {
			h.tip.window.Invalidate()
		}
	}
	if h.viewer != nil {
		if !h.viewerSeen {
			h.viewer.close()
		} else if h.viewer.window != nil {
			h.viewer.window.Invalidate()
		}
	}
	return root
}

func native(c *ui.Context) *nativeHost {
	slot := ui.Local(c.Root(), nativeKey{}, func() nativeSlot { return nativeSlot{} })
	if slot.host == nil {
		panic("overlay: wrap the window view in overlay.Native")
	}
	return slot.host
}

func (h *nativeHost) close() {
	if h.menu != nil {
		h.menu.close(false)
	}
	if h.tip != nil {
		h.tip.close()
	}
	if h.viewer != nil {
		h.viewer.close()
	}
}

func rectangle(r ui.Rect) mygo.Rectangle {
	return mygo.Rectangle{X: int(math.Round(float64(r.X))), Y: int(math.Round(float64(r.Y))), Width: int(math.Ceil(float64(r.W))), Height: int(math.Ceil(float64(r.H)))}
}
func extent(v float32) int { return max(1, int(math.Ceil(float64(v)))) }
