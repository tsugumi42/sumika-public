package compose

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type PickerDockOptions struct {
	Picker PickerOptions
	// Place overrides bottom-right anchoring above the editor. Both elements
	// belong to this build; retaining them outside this callback is invalid.
	Place func(panel, anchor ui.Element)
	// KeepOpen disables pointer-leave dismissal, for host click-only policies.
	KeepOpen bool
	OnClose  func()
}

type pickerDockState struct {
	leftAt time.Time
	inside bool
}

// DockedPicker anchors a catalog above an editor/action without consuming layout
// height. Open must be supplied. Escape, outside press and pointer leave close it;
// the independent Picker remains available for custom host layouts.
func DockedPicker(c *ui.Context, anchor ui.Element, state *PickerState, data PickerData, o PickerDockOptions) ui.Element {
	t := theme.T(c)
	root := ui.Box(c).Size(0, 0)
	local := ui.Local(root, "dock", func() pickerDockState { return pickerDockState{} })
	// This transition only retains the overlay while Picker runs its own reveal.
	target := float32(0)
	if *o.Picker.Open {
		target = 1
	}
	visible := root.AnimateTo("presence", target, ui.Animation{Duration: t.Motion().Picker().Duration(), Ease: ui.Linear})
	if !*o.Picker.Open && visible == 0 {
		return root
	}
	close := func() {
		if *o.Picker.Open {
			*o.Picker.Open = false
			local.leftAt = time.Time{}
			if o.Picker.OnClose != nil {
				o.Picker.OnClose()
			}
			if o.OnClose != nil {
				o.OnClose()
			}
		}
	}
	ui.Overlay(c, func() {
		panel := ui.Box(c).Role(ui.RolePopup).Label("Media picker").IgnoreInput(!*o.Picker.Open)
		if o.Place != nil {
			o.Place(panel, anchor)
		} else {
			// AttachTo adds mygo's 4 DIP popup margin. The selector's source
			// instead aligns to the anchor and clamps at the window edge.
			w, h := c.Size()
			width := min(t.Metrics().Picker().Width(), w)
			if o.Picker.AvailableWidth > 0 {
				width = min(width, o.Picker.AvailableWidth)
			}
			at := anchor.Bounds()
			panel.Absolute().Right(max(float32(0), w-max(width, at.X+at.W))).Bottom(h - at.Y)
		}
		options := o.Picker
		options.OnClose = func() {
			local.leftAt = time.Time{}
			if o.Picker.OnClose != nil {
				o.Picker.OnClose()
			}
			if o.OnClose != nil {
				o.OnClose()
			}
		}
		if options.AvailableHeight == 0 {
			options.AvailableHeight = max(float32(0), anchor.Bounds().Y)
		}
		panel.Children(func() { Picker(c, state, data, options) })
		if panel.OverlayShortcut(0, ui.KeyEscape) || panel.PressedOutside() {
			close()
		}
		_, _, over := panel.PointerPosition()
		_, _, overAnchor := anchor.PointerPosition()
		inside := over || overAnchor
		if inside {
			local.leftAt = time.Time{}
		} else if local.inside {
			local.leftAt = c.Now()
		}
		local.inside = inside
		if !o.KeepOpen && !local.leftAt.IsZero() && *o.Picker.Open {
			left := t.Motion().PickerLeave().Duration() - c.Now().Sub(local.leftAt)
			if left <= 0 {
				close()
			} else {
				c.After(left)
			}
		}
	})
	return root
}
