// Package compose provides the chat input area. The host owns drafts, media and actions.
package compose

import (
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/locales"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type SubmitKey uint8

const (
	EnterSends SubmitKey = iota
	ControlEnterSends
)

type FieldOptions struct {
	Placeholder        string
	Label              string
	Disabled, ReadOnly bool
	// AvailableHeight is the height left below the chat header and other bars.
	// Zero uses the theme's expanded maximum. ReserveHeight accounts for a reply/attachment area.
	AvailableHeight, ReserveHeight float32
	SubmitKey                      SubmitKey
	AllowEmptySubmit               bool
	OnSubmit                       func(string)
	OnChange                       func(string)
	OnPaste                        func(string) bool
	Handle                         *ui.Handle
}

// Field grows with wrapped text, then scrolls. Submitting never clears the host's draft.
func Field(c *ui.Context, draft *string, o FieldOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Compose()
	maxHeight := m.ExpandedMaxHeight()
	if o.AvailableHeight > 0 {
		// history_widget.cpp:9234-9253 reserves one reply-height of visible history.
		maxHeight = max(m.FieldMinHeight(), min(maxHeight, o.AvailableHeight-o.ReserveHeight-2*m.SendPadding()-m.ReplyHeight()))
	}
	size := t.Typography().Compose().Size() * c.Preferences().TextScale
	lineHeight := fontLineHeight(c, t, t.Typography().Compose())
	maxHeight = max(maxHeight, lineHeight)
	in := ui.TextAreaBase(c, draft).FillWidth().MinWidth(0).
		Lines(1, max(1, int(math.Ceil(float64(maxHeight/lineHeight))))).
		MinHeight(m.FieldMinHeight()).MaxHeight(maxHeight).Shrink(0).
		Padding(m.TextMargin()).Radius(m.FieldRadius()).
		Font(t.Typography().Font()).FontSize(size).FontWeight(t.Typography().Compose().Weight()).
		TextColor(t.Colors().Compose().Text()).CaretWidth(t.Metrics().Control().BorderWidth()).
		Disabled(o.Disabled).ReadOnly(o.ReadOnly).
		Label(label(o.Label, locales.Text(c.Locale(), "message")))
	if o.Handle != nil {
		in.Bind(o.Handle)
	}
	fg := t.Colors().Compose().Placeholder()
	if in.Focused() {
		fg = t.Colors().Compose().PlaceholderFocused()
	}
	motion := t.Motion().ComposePlaceholder()
	fg = in.AnimateColor("placeholder", fg, ui.Animation{Duration: motion.Duration(), Ease: motion.Easing().Curve()})
	if *draft == "" && !in.Composing() {
		span := ui.Span{Text: label(o.Placeholder, locales.Text(c.Locale(), "writeMessage")), Font: t.Typography().Font(), Size: size, Weight: t.Typography().Compose().Weight(), Color: fg}
		in.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.RichText(r.X+m.PlaceholderInset(), r.Y+m.TextMargin(), max(float32(0), r.W-2*m.PlaceholderInset()), span)
		})
	}
	in.OnTextKey(func(k ui.TextKey) bool {
		send := k.Key == ui.KeyEnter && (o.SubmitKey == EnterSends && k.Mods == 0 || o.SubmitKey == ControlEnterSends && (k.Mods == ui.Ctrl || k.Mods == ui.Super))
		if !send || o.ReadOnly || o.Disabled {
			return false
		}
		if text := k.Text(); (strings.TrimSpace(text) != "" || o.AllowEmptySubmit) && o.OnSubmit != nil {
			*draft = text
			o.OnSubmit(text)
			// TextKey owns edits until the next build. Apply the host's accepted replacement there too.
			if *draft != text {
				k.SetText(*draft)
			}
		}
		return true
	}).OnPaste(o.OnPaste).OnChange(func() {
		if o.OnChange != nil {
			o.OnChange(*draft)
		}
	})
	return in
}

type EditorOptions struct {
	Field                              FieldOptions
	Editing, HasAttachments, EmojiOpen bool
	Disabled, ReadOnly                 bool
	// Action overrides automatic voice/send/save selection when nonzero.
	Action                               Action
	OnSubmit                             func(string)
	OnAttach, OnEmoji, OnVoice, OnAction func()
	Reference                            *Reference
	ReferenceOptions                     ReferenceOptions
	// Generation owns the submit slot while a host run is active. StopDisabled
	// is independent of editing permissions so read-only drafts remain stoppable.
	Generation   *Generation
	StopDisabled bool
	OnStop       func(string)
}

// Editor combines the field, attachment, emoji and send/voice controls.
// Attachments and Picker are independent components so hosts can dock them in their layout.
func Editor(c *ui.Context, draft *string, o EditorOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Compose()
	disabled := o.Disabled || o.Field.Disabled
	readOnly := o.ReadOnly || o.Field.ReadOnly
	editing := o.Editing || o.Reference != nil && o.Reference.Kind == EditReference
	generating := o.Generation != nil && o.Generation.active()
	root := ui.Column(c).FillWidth().Gap(0).Background(t.Materials().Compose().Background()).Radius(m.FieldRadius())
	root.Children(func() {
		if o.Reference != nil {
			c.Key("reference")
			ReferenceBar(c, *o.Reference, o.ReferenceOptions)
		}
		ui.Row(c).FillWidth().Gap(0).AlignItems(ui.End).Padding(0, m.SendRight()).Children(func() {
			c.Key("attach")
			ActionButton(c, Attach, ActionOptions{Disabled: disabled || readOnly, OnClick: o.OnAttach})
			ui.Column(c).Grow(1).MinWidth(0).Padding(m.SendPadding(), 0).Children(func() {
				fo := o.Field
				fo.Disabled = fo.Disabled || o.Disabled
				fo.ReadOnly = fo.ReadOnly || o.ReadOnly
				fo.AllowEmptySubmit = o.HasAttachments || editing
				if o.Reference != nil {
					fo.ReserveHeight += referenceHeight(c, t)
				}
				fo.OnSubmit = o.OnSubmit
				if generating {
					fo.OnSubmit = nil
				}
				c.Key("field")
				if Field(c, draft, fo).Changed() {
					root.SetChanged()
				}
			})
			c.Key("emoji")
			ActionButton(c, Emoji, ActionOptions{Active: o.EmojiOpen, Disabled: disabled || readOnly, OnClick: o.OnEmoji})
			action := o.Action
			if action == Automatic {
				action = Voice
				if editing {
					action = Save
				} else if strings.TrimSpace(*draft) != "" || o.HasAttachments {
					action = Send
				}
			}
			buttonDisabled := disabled || readOnly
			buttonLabel := ""
			if generating {
				action = Stop
				buttonDisabled = o.StopDisabled || o.Generation.Phase == Stopping
				if o.Generation.Phase == Stopping {
					buttonLabel = locales.Text(c.Locale(), "stopping")
				}
			}
			c.Key("submit")
			ActionButton(c, action, ActionOptions{Label: buttonLabel, Disabled: buttonDisabled, OnClick: func() {
				switch action {
				case Stop:
					if o.OnStop != nil {
						o.OnStop(o.Generation.RunID)
					}
				case Send, Save:
					if (strings.TrimSpace(*draft) != "" || o.HasAttachments || editing) && o.OnSubmit != nil {
						o.OnSubmit(*draft)
					}
				case Voice:
					if o.OnVoice != nil {
						o.OnVoice()
					}
				default:
					if o.OnAction != nil {
						o.OnAction()
					}
				}
			}})
		})
	})
	return root
}

func label(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func text(c *ui.Context, value string, style theme.TextStyle, color ui.Color) ui.Element {
	return ui.Text(c, value).Font(theme.T(c).Typography().Font()).FontSize(style.Size() * c.Preferences().TextScale).FontWeight(style.Weight()).TextColor(color)
}

func fontLineHeight(c *ui.Context, t *theme.Theme, style theme.TextStyle) float32 {
	_, height := c.MeasureText(0, ui.Span{Text: "M", Font: t.Typography().Font(), Size: style.Size() * c.Preferences().TextScale, Weight: style.Weight()})
	return height
}
