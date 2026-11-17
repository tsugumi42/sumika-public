package control

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/settings"
	"slices"
)

type DropdownMode uint8

const (
	Popup DropdownMode = iota
	SearchBox
)

type DropdownState struct {
	menu     overlay.MenuState
	chooser  settings.ChooseState
	opener   ui.Handle
	items    []settings.ChoiceItem
	entries  []overlay.MenuItem
	selected string
	restore  bool
}

type dropdownSlot struct {
	owner uint64
	mode  DropdownMode
}
type DropdownOptions struct {
	Placeholder           string
	Disabled, SettingsRow bool
	Mode                  DropdownMode
	Choose                settings.ChooseOptions
	OnSelect              func(string)
}

// Dropdown opens either the existing native menu or an explicitly requested
// searchable choice box. It never changes the host's selected ID itself.
func Dropdown(c *ui.Context, s *DropdownState, items []settings.ChoiceItem, selected, label string, o DropdownOptions) ui.Element {
	caption := o.Placeholder
	for _, v := range items {
		if v.ID == selected {
			caption = v.Title
			break
		}
	}
	disabled := o.Disabled || len(items) == 0
	// paint/dropdown.md#入口: an unavailable entry also withdraws an open choice.
	if disabled {
		s.chooser.Open = false
		s.menu.Close()
		s.restore = false
	}
	var b ui.Element
	// paint/dropdown.md#入口: existing settings row and Light button geometry.
	if o.SettingsRow {
		b = settings.Button(c, label, settings.ButtonOptions{Value: caption, Arrow: true, Disabled: disabled})
	} else {
		b = RoundButton(c, caption, RoundButtonOptions{Tone: Light, Disabled: disabled, Icon: downIcon, TrailingIcon: true}).Label(label).Value(caption)
	}
	b.Bind(&s.opener)
	if s.restore {
		s.opener.Focus(c)
		s.restore = false
	}
	if o.Mode == SearchBox {
		b.OnClick(s.chooser.Show).Expanded(s.chooser.Open)
		options := o.Choose
		options.OnSelect = func(id string) {
			s.chooser.Open = false
			s.restore = true
			if o.OnSelect != nil {
				o.OnSelect(id)
			}
		}
		options.OnClose = func() {
			s.restore = true
			if o.Choose.OnClose != nil {
				o.Choose.OnClose()
			}
		}
		settings.ChooseBox(c.Key(dropdownSlot{b.ID(), SearchBox}), &s.chooser, items, selected, label, options)
	} else {
		if !slices.Equal(s.items, items) || s.selected != selected {
			s.items = append(s.items[:0], items...)
			s.selected = selected
			s.entries = s.entries[:0]
			for _, v := range items {
				s.entries = append(s.entries, overlay.MenuItem{ID: v.ID, Label: v.Title, Disabled: v.Disabled, Radio: true, Checked: v.ID == selected})
			}
		}
		// paint/dropdown.md#交互与提交: native menu owns highlighter and close focus.
		overlay.PopupMenu(c.Key(dropdownSlot{b.ID(), Popup}), b, s.entries, &s.menu, overlay.MenuOptions{Label: label, OnSelect: o.OnSelect})
	}
	return b
}
