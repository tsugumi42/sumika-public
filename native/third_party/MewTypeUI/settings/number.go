package settings

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// NumberState retains the editable empty state and the last accepted edit/caret.
// Hosts may replace Text when showing a dialog; numeric policy is explicit.
type NumberState struct {
	Text                                   string
	accepted                               string
	start, end, acceptedStart, acceptedEnd int
	selection                              bool
	input                                  ui.Handle
	focus, floating, errorLine, labelFocus timeline
	lineOrigin                             float32
	focused                                bool
	pointerOrigin, expanding               bool
}
type NumberOptions struct {
	Placeholder                           string
	Limit                                 int32
	AllowZero, Error, Disabled, AutoFocus bool
	OnChange, OnSubmit                    func(string)
	OnCancel                              func()
}

// NumberInput uses NumberInput's positive signed-int correction by default.
// AllowZero is the explicit SET-NUMBER-ROW extension; empty remains editable.
func NumberInput(c *ui.Context, s *NumberState, label string, o NumberOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Input()
	root := ui.Box(c).FillWidth().Height(max(m.HeightMin(), m.TextTop()+fontHeight(c, t.Typography().DialogBody())+m.TextBottom())).Shrink(0).Background(t.Colors().Window().Background()).Disabled(o.Disabled)
	root.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
			s.lineOrigin = ev.X
			s.pointerOrigin = true
		}
		return false
	})
	root.Children(func() {
		field := ui.TextInputBase(c.Key("number"), &s.Text).Bind(&s.input).Label(label).Absolute().Left(m.TextLeft()).Right(m.TextRight()).Top(m.TextTop()).Height(fontHeight(c, t.Typography().DialogBody())).Font(t.Typography().Font()).FontSize(t.Typography().DialogBody().Size() * c.Preferences().TextScale).FontWeight(t.Typography().DialogBody().Weight()).TextColor(t.Colors().Window().Text())
		if o.AutoFocus {
			field.AutoFocus()
		}
		if s.selection {
			field.SetTextSelection(s.start, s.end)
			s.selection = false
		}
		start, end := field.TextSelection()
		field.OnChange(func() {
			next, ns, ne, valid := numberEdit(s.Text, start, end, o.Limit, o.AllowZero)
			if !valid {
				next = s.accepted
				ns = s.acceptedStart
				ne = s.acceptedEnd
			}
			s.Text = next
			s.start, s.end = ns, ne
			s.selection = true
			s.accepted = next
			s.acceptedStart, s.acceptedEnd = ns, ne
			if o.OnChange != nil {
				o.OnChange(next)
			}
		})
		field.OnTextKey(func(k ui.TextKey) bool {
			if k.Key == ui.KeyEnter {
				if o.OnSubmit != nil {
					o.OnSubmit(k.Text())
				}
				return true
			}
			if k.Key == ui.KeyEscape && o.OnCancel != nil {
				o.OnCancel()
				return true
			}
			return false
		})
		if !field.Changed() && !s.selection {
			s.accepted = s.Text
			s.acceptedStart, s.acceptedEnd = start, end
		}
		focused := s.input.Focused(c)
		if focused && !s.focused {
			if !s.pointerOrigin {
				s.lineOrigin = root.LayoutBounds().W / 2
			}
			s.expanding = s.focus.value == 0
		}
		if !focused {
			s.expanding = false
		}
		s.pointerOrigin = false
		s.focused = focused
		a := s.focus.update(c, unit(focused || o.Error), t.Motion().InputField(), o.Disabled)
		f := s.floating.update(c, unit(focused || s.Text != ""), t.Motion().InputField(), o.Disabled)
		er := s.errorLine.update(c, unit(o.Error), t.Motion().InputField(), o.Disabled)
		focusColor := s.labelFocus.update(c, unit(focused), t.Motion().InputField(), o.Disabled)
		// paint/numeric-input.md#字段矩形与绘制: underline and floating label
		// have independent linear timelines. Preserve the native editor/IME.
		ink := t.Colors().Window().MutedText().Mix(t.Colors().Window().ActiveTextForeground(), focusColor).Mix(t.Colors().Control().DangerForeground(), er)
		labelTop := m.TextTop() - m.PlaceholderRise()*f
		labelSize := t.Typography().InputPlaceholder().Size() * (1 + (m.PlaceholderScale()-1)*f)
		ui.Text(c, o.Placeholder).Absolute().Left(m.TextLeft()).Right(m.TextRight()).Top(labelTop).Font(t.Typography().Font()).FontWeight(t.Typography().InputPlaceholder().Weight()).FontSize(labelSize * c.Preferences().TextScale).TextColor(ink).SingleLine().IgnoreInput(true)
		root.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X, Y: r.Y + r.H - m.Border(), W: r.W, H: m.Border()}, t.Colors().Control().Border(), 0)
			origin := min(r.W, max(0, s.lineOrigin))
			left, right := float32(0), r.W
			alpha := a
			if focused && s.expanding && !o.Error {
				left = round(origin * (1 - a))
				right = origin + round((r.W-origin)*a)
				alpha = 1
			}
			lineInk := t.Colors().Control().Focus().Mix(t.Colors().Control().ActiveLineForegroundError(), er).Alpha(alpha)
			p.Fill(ui.Rect{X: r.X + left, Y: r.Y + r.H - m.Border() - m.BorderActive()/2, W: right - left, H: m.BorderActive()}, lineInk, 0)
		})
	})
	return root.OnClick(func() { s.input.Focus(c) })
}
func numberEdit(value string, start, end int, limit int32, allowZero bool) (string, int, int, bool) {
	removedStart, removedEnd := 0, 0
	next := value
	if strings.IndexFunc(value, func(r rune) bool { return !unicode.IsDigit(r) }) >= 0 {
		var b strings.Builder
		pos := 0
		for _, r := range value {
			if unicode.IsDigit(r) {
				b.WriteRune(r)
			} else {
				if pos < start {
					removedStart++
				}
				if pos < end {
					removedEnd++
				}
			}
			pos++
		}
		next = b.String()
	}
	n, err := strconv.ParseInt(next, 10, 32)
	if next == "" || err != nil || n == 0 && !allowZero {
		return "", 0, 0, true
	}
	if limit > 0 && n > int64(limit) {
		return "", 0, 0, false
	}
	return next, start - removedStart, end - removedEnd, true
}
