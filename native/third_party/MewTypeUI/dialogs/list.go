package dialogs

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// Entry is either a conversation or a section heading. HeadingID is stable and
// nonempty for headings; a conversation uses Conversation.ID.
type Entry struct {
	Conversation       Conversation
	HeadingID, Heading string
}

// Model indexes an immutable host snapshot once, outside the render path.
// Replace the model when entries change; do not mutate Entries after NewModel.
type Model struct {
	entries []Entry
	indices map[string]int
}

func NewModel(entries []Entry) *Model {
	m := &Model{entries: entries, indices: make(map[string]int, len(entries))}
	for i := range entries {
		item := &entries[i]
		id := item.Conversation.ID
		if item.HeadingID != "" {
			id = item.HeadingID
		}
		m.indices[id] = i
	}
	return m
}
func (m *Model) Len() int { return len(m.entries) }

// ListState belongs to one list in one window. SelectedID is host-controlled;
// the zero value selects nothing. Keyboard focus and scroll position survive
// filtering, reordering and theme changes independently of row construction.
type ListState struct {
	SelectedID string
	View       ui.ListState
	cursor     int
	model      *Model
	selected   string
	choice     ui.Selection[string]
}

type ListOptions struct {
	Compact     bool
	Label       string
	OnSelect    func(string)
	OnActivate  func(string)
	Menu        func(*ui.Menu, string)
	OnReorder   func(ids []string, before string)
	OnStory     func(string)
	OnCommunity func(string)
}

func List(c *ui.Context, model *Model, state *ListState, o ListOptions) ui.Element {
	if state.model != model || state.selected != state.SelectedID {
		state.cursor = -1
		if i, ok := model.indices[state.SelectedID]; ok && model.entries[i].HeadingID == "" {
			state.cursor = i
		}
		// Key membership distinguishes a host replacement at the old row index.
		setChoice(&state.choice, state.SelectedID)
		state.model, state.selected = model, state.SelectedID
	}
	state.View.Selected = &state.cursor
	state.View.Selection = &state.choice
	state.View.MenuKeepsSelection = true
	// Keep the viewport fixed when the host sorts active conversations to the top.
	state.View.KeepOffset = true
	state.View.Key = func(i int) any {
		item := &model.entries[i]
		if item.HeadingID != "" {
			return item.HeadingID
		}
		return item.Conversation.ID
	}
	state.View.Label = func(i int) string {
		item := &model.entries[i]
		if item.HeadingID != "" {
			return item.Heading
		}
		return item.Conversation.Name
	}
	state.View.Header = func(i int) bool { return model.entries[i].HeadingID != "" }
	state.View.Reorder = nil
	if o.OnReorder != nil {
		state.View.Reorder = func(rows []int, to int) {
			ids := make([]string, 0, len(rows))
			for _, i := range rows {
				if model.entries[i].HeadingID == "" {
					ids = append(ids, model.entries[i].Conversation.ID)
				}
			}
			before := ""
			if to < len(model.entries) {
				before = model.entries[to].Conversation.ID
			}
			o.OnReorder(ids, before)
		}
	}
	commit := func() {
		id := ""
		if state.cursor >= 0 && state.cursor < model.Len() {
			id = model.entries[state.cursor].Conversation.ID
		}
		setChoice(&state.choice, id)
		if state.SelectedID != id {
			state.SelectedID, state.selected = id, id
			if o.OnSelect != nil {
				o.OnSelect(id)
			}
		}
	}
	root := ui.List(c, &state.View, model.Len()).Gap(0).Padding(0).Label(o.Label).Background(theme.T(c).Materials().Conversation().Background())
	root.Rows(func(row ui.ListRow) {
		item := &model.entries[row.Index]
		if item.HeadingID != "" {
			GroupHeading(c, item.Heading)
			return
		}
		buildRow(c, item.Conversation, RowOptions{Selected: row.Selected(), Compact: o.Compact, Menu: o.Menu, OnStory: o.OnStory, OnCommunity: o.OnCommunity}, &row)
		// The list row takes the click and chooses; a double click or Enter reaches OnSubmit below.
	})
	root.OnChange(commit).OnSubmit(func() {
		commit()
		if state.SelectedID != "" && o.OnActivate != nil {
			o.OnActivate(state.SelectedID)
		}
	})
	return root
}

// ScrollTo selects no item. Call before building List to reveal a host ID.
func (s *ListState) ScrollTo(model *Model, id string, align ui.Align) {
	if row, ok := model.indices[id]; ok {
		s.View.ScrollTo(row, align)
	}
}

func GroupHeading(c *ui.Context, label string) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Search()
	return ui.Box(c).FillWidth().Height(m.GroupHeight()).Shrink(0).Background(t.Colors().Search().BarBackground()).Children(func() {
		text(c, label, t.Typography().Body(), t.Colors().Search().BarForeground()).Absolute().Left(m.GroupLeft()).Right(m.GroupLeft()).Top(m.GroupTop()).SingleLine()
	})
}
