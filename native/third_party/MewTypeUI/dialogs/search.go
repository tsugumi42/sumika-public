package dialogs

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type searchKey struct{}
type searchState struct{ input ui.Handle }

// Search edits the host's query. Filtering and submitting a search belong to
// the host. The clear button keeps keyboard focus in the same text input.
func Search(c *ui.Context, query *string, o SearchOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Search()
	palette := t.Colors().Control()
	root := ui.Box(c).FillWidth().Height(m.Height()).MinWidth(m.ClearSize()).Disabled(o.Disabled)
	state := ui.Local(root, searchKey{}, func() searchState { return searchState{} })
	root.Children(func() {
		c.Key("input")
		field := ui.TextInputBase(c, query).Bind(&state.input).Fill().Label(o.Label).Placeholder(o.Placeholder).Font(t.Typography().Font()).FontSize(t.Typography().Body().Size()*c.Preferences().TextScale).Padding(m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft()).Radius(m.Radius())
		background, border, width := palette.FilterInputInactiveBackground(), palette.FilterInputInactiveBackground(), m.Border()
		placeholder := t.Colors().Compose().Placeholder()
		if state.input.Focused(c) {
			background, border, width = palette.FilterInputActiveBackground(), t.Colors().Window().BackgroundPressed(), m.BorderActive()
			placeholder = t.Colors().Compose().PlaceholderFocused()
		}
		field.Background(background).Border(width, border).TextColor(t.Colors().Window().Text()).PlaceholderColor(placeholder)
		field.OnChange(func() {
			if o.OnChange != nil {
				o.OnChange(*query)
			}
		}).OnSubmit(func() {
			if o.OnSubmit != nil {
				o.OnSubmit(*query)
			}
		})
		if *query != "" {
			c.Key("clear")
			clear := ui.ButtonBase(c).Absolute().Top(0).Right(0).Size(m.ClearSize(), m.ClearSize()).Label(o.ClearLabel)
			color := t.Colors().Conversation().MenuIconForeground()
			if clear.Hovered() {
				color = t.Colors().Conversation().MenuIconForegroundHover()
			}
			clear.Children(func() { icon(c, clearIcon, m.ClearSize()-2*m.ClearInset(), color) })
			clear.OnClick(func() {
				*query = ""
				state.input.Focus(c)
				if o.OnChange != nil {
					o.OnChange("")
				}
			})
		}
	})
	return root
}
