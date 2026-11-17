package control

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"math"
)

type InputOptions struct {
	Placeholder                                     string
	Multiline, ReadOnly, Disabled, Error, AutoFocus bool
	// Multiline Enter inserts a newline unless SubmitOnControlEnter is enabled.
	SubmitOnControlEnter bool
	Handle               *ui.Handle
	OnChange, OnSubmit   func(string)
	OnCancel             func()
}
type fieldLocal struct {
	input                                  ui.Handle
	focus, floating, errorLine, labelFocus timeline
	origin                                 float32
	pointer, focused, expanding            bool
}

// InputField delegates shaping, editing, scrolling and IME to the native primitive.
func InputField(c *ui.Context, value *string, label string, o InputOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Input()
	style := t.Typography().DialogBody()
	_, line := measure(c, "M", style, 0)
	root := ui.Column(c).FillWidth().MinWidth(m.WidthMin()).MinHeight(m.HeightMin()).Shrink(0).Padding(m.TextTop(), m.TextRight(), m.TextBottom(), m.TextLeft()).Background(t.Colors().Window().Background()).Disabled(o.Disabled)
	s := ui.Local(root, "input", func() fieldLocal { return fieldLocal{} })
	handle := &s.input
	if o.Handle != nil {
		handle = o.Handle
	}
	root.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
			s.origin = ev.X
			s.pointer = true
		}
		return false
	})
	root.Children(func() {
		var in ui.Element
		if o.Multiline {
			in = ui.TextAreaBase(c.Key("editor"), value).Lines(1, max(1, int(math.Ceil(float64((m.HeightMax()-m.TextTop()-m.TextBottom())/line))))).MaxHeight(max(line, m.HeightMax()-m.TextTop()-m.TextBottom())).Scrollbars(false)
		} else {
			in = ui.TextInputBase(c.Key("editor"), value).Height(line)
		}
		// The frame propagates disabled input state; repeating it would fade glyphs twice.
		in.FillWidth().MinWidth(0).Shrink(0).Padding(0).Bind(handle).Label(label).Font(t.Typography().Font()).FontSize(style.Size() * c.Preferences().TextScale).FontWeight(style.Weight()).TextColor(t.Colors().Window().Text()).ReadOnly(o.ReadOnly)
		if o.AutoFocus {
			in.AutoFocus()
		}
		// paint/input-field.md#单行: mygo normalizes CR/LF in its native edit path,
		// preserving the selection and undo history rather than replacing host text.
		in.OnChange(func() {
			if o.OnChange != nil {
				o.OnChange(*value)
			}
		})
		composing := in.Composing()
		in.OnTextKey(func(k ui.TextKey) bool {
			// paint/input-field.md#状态和所有权: composition owns navigation keys.
			if composing {
				return true
			}
			if o.Disabled {
				return false
			}
			if k.Key == ui.KeyEscape {
				if o.OnCancel != nil {
					o.OnCancel()
				}
				return false
			}
			submit := k.Key == ui.KeyEnter && (!o.Multiline || o.SubmitOnControlEnter && (k.Mods == ui.Ctrl || k.Mods == ui.Super))
			if submit && !o.ReadOnly {
				if o.OnSubmit != nil {
					o.OnSubmit(k.Text())
				}
				return true
			}
			return false
		})
		focused := handle.Focused(c)
		if focused && !s.focused {
			if !s.pointer {
				s.origin = root.LayoutBounds().W / 2
			}
			s.expanding = s.focus.value == 0
		}
		if !focused {
			s.expanding = false
		}
		s.pointer = false
		s.focused = focused
		a := s.focus.update(c, unit(focused || o.Error), t.Motion().InputField(), o.Disabled)
		f := s.floating.update(c, unit(focused || *value != "" || in.Composing()), t.Motion().InputField(), o.Disabled)
		er := s.errorLine.update(c, unit(o.Error), t.Motion().InputField(), o.Disabled)
		fc := s.labelFocus.update(c, unit(focused), t.Motion().InputField(), o.Disabled)
		// paint/input-field.md#形状数值和文字框; numeric-input.md#字段矩形与绘制.
		ink := t.Colors().Window().MutedText().Mix(t.Colors().Window().ActiveTextForeground(), fc).Mix(t.Colors().Control().DangerForeground(), er)
		ui.Text(c, o.Placeholder).Absolute().Left(m.TextLeft()).Right(m.TextRight()).Top(m.TextTop() - m.PlaceholderRise()*f).Font(t.Typography().Font()).FontWeight(t.Typography().InputPlaceholder().Weight()).FontSize(t.Typography().InputPlaceholder().Size() * c.Preferences().TextScale * (1 + (m.PlaceholderScale()-1)*f)).TextColor(ink).SingleLine().IgnoreInput(true)
		root.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X, Y: r.Y + r.H - m.Border(), W: r.W, H: m.Border()}, t.Colors().Control().Border(), 0)
			left, right, alpha := float32(0), r.W, a
			if focused && s.expanding && !o.Error {
				origin := min(r.W, max(0, s.origin))
				left = round(origin * (1 - a))
				right = origin + round((r.W-origin)*a)
				alpha = 1
			}
			p.Fill(ui.Rect{X: r.X + left, Y: r.Y + r.H - m.Border() - m.BorderActive()/2, W: right - left, H: m.BorderActive()}, t.Colors().Control().Focus().Mix(t.Colors().Control().ActiveLineForegroundError(), er).Alpha(alpha), 0)
		})
	})
	return root.OnClick(func() { handle.Focus(c) })
}
