package gallery

import (
	"embed"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/compose"
	"github.com/tsugumi42/MewTypeUI/locales"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func init() {
	Register(Page{ID: "compose/field", Area: "compose", Title: "Field", View: composeFieldPage})
	Register(Page{ID: "compose/editor", Area: "compose", Title: "Editor", View: composeEditorPage})
	Register(Page{ID: "compose/actions", Area: "compose", Title: "Action buttons", View: composeActionPage})
	Register(Page{ID: "compose/reference", Area: "compose", Title: "Reply and edit bars", View: composeReferencePage})
	Register(Page{ID: "compose/picker", Area: "compose", Title: "Emoji, stickers and GIFs", View: composePickerPage})
	Register(Page{ID: "compose/attachments", Area: "compose", Title: "Attachment previews", View: composeAttachmentPage})
	Register(Page{ID: "compose/ime", Area: "compose", Title: "IME preedit and caret", View: composeIMEPage})
}

type composeDemo struct {
	drafts      [5]string
	event       string
	open        bool
	picker      compose.PickerState
	attachments []compose.Attachment
}

func composeDemoState(c *ui.Context) *composeDemo {
	return ui.Local(c.Root(), "compose-demo", func() composeDemo {
		return composeDemo{drafts: [5]string{"", "A wrapped draft. " + strings.Repeat("More text to wrap. ", 8), strings.Repeat("A long draft\n", 40), "Read only: select and copy this draft.", "Disabled draft"}, attachments: []compose.Attachment{{ID: "image", Name: "Observatory.png", Status: "Ready", Preview: composeArt}, {ID: "second-image", Name: "Second view.png", Preview: composeArt}, {ID: "file", Name: "Notes.txt", Status: "Preparing…", Preparing: true}, {ID: "failed", Name: "Archive.zip", Status: "Could not prepare", Failed: true}}}
	})
}
func composeHeading(c *ui.Context, name string) {
	t := theme.T(c)
	ui.Text(c, name).FontSize(t.Typography().ComposeTitle().Size() * c.Preferences().TextScale).FontWeight(t.Typography().ComposeTitle().Weight()).TextColor(t.Colors().Window().Text())
}
func composeFieldPage(c *ui.Context) {
	s := composeDemoState(c)
	m := theme.T(c).Metrics().Attachment()
	ui.Scroll(c).Fill().Gap(m.Gap()).Children(func() {
		for i, title := range []string{"Empty / placeholder", "Wrapped draft", "Maximum height / scrolling", "Read only", "Disabled"} {
			c.Key(title)
			ui.Column(c).FillWidth().Gap(0).Children(func() {
				composeHeading(c, title)
				compose.Field(c, &s.drafts[i], compose.FieldOptions{ReadOnly: i == 3, Disabled: i == 4})
			})
		}
	})
}
func composeEditorPage(c *ui.Context) {
	s := composeDemoState(c)
	t := theme.T(c)
	ui.Column(c).Fill().Gap(t.Metrics().Attachment().Gap()).Children(func() {
		composeHeading(c, "Type, Shift+Enter, Enter; host accepts and clears")
		ui.Text(c, s.event)
		ui.Spacer(c)
		if len(s.attachments) > 0 {
			c.Key("attachments")
			compose.Attachments(c, s.attachments, compose.AttachmentOptions{OnRemove: func(id string) { s.attachments = removeComposeAttachment(s.attachments, id) }})
		}
		c.Key("editor")
		anchor := compose.Editor(c, &s.drafts[0], compose.EditorOptions{HasAttachments: len(s.attachments) > 0, EmojiOpen: s.open, OnSubmit: func(value string) { s.event = "Submitted: " + value; s.drafts[0] = "" }, OnAttach: func() { s.event = "Host attachment chooser requested" }, OnEmoji: func() { s.open = !s.open }, OnVoice: func() { s.event = "Host voice recorder requested" }})
		c.Key("picker")
		compose.DockedPicker(c, anchor, &s.picker, composeCatalog(c), compose.PickerDockOptions{Picker: compose.PickerOptions{Open: &s.open, OnSelect: func(tab compose.PickerTab, item compose.PickerItem) {
			s.event = "Selected " + item.ID
			if tab == compose.EmojiTab {
				s.drafts[0] += item.Emoji
			}
		}}})
	})
}
func composeActionPage(c *ui.Context) {
	s := composeDemoState(c)
	ui.Column(c).Fill().Gap(theme.T(c).Metrics().Attachment().Gap()).Children(func() {
		ui.Text(c, s.event)
		for _, disabled := range []bool{false, true} {
			c.Key(disabled)
			ui.Row(c).Wrap().Gap(0).Children(func() {
				for _, a := range []compose.Action{compose.Attach, compose.Emoji, compose.Send, compose.Voice, compose.Save, compose.Cancel} {
					c.Key(a)
					compose.ActionButton(c, a, compose.ActionOptions{Disabled: disabled, OnClick: func() { s.event = fmt.Sprintf("Action %d", a) }})
				}
			})
		}
	})
}
func composeReferencePage(c *ui.Context) {
	s := composeDemoState(c)
	ui.Column(c).Fill().Gap(theme.T(c).Metrics().Attachment().Gap()).Children(func() {
		ui.Text(c, s.event)
		for _, r := range []compose.Reference{{ID: "reply", Name: "Mio", Summary: "Meet at the observatory."}, {ID: "edit", Kind: compose.EditReference, Name: "Editing message", Summary: "A message being edited.", Preview: composeArt}, {ID: "long", Name: strings.Repeat("A very long name ", 20), Summary: strings.Repeat("A long summary ", 30)}, {ID: "loading", Name: "Reply", Summary: "Loading message…", Loading: true}, {ID: "missing", Name: "Reply", Summary: "Message no longer available", Missing: true}} {
			c.Key(r.ID)
			compose.ReferenceBar(c, r, compose.ReferenceOptions{OnCancel: func() { s.event = "Cancel " + r.ID }, OnActivate: func() { s.event = "Open " + r.ID }})
		}
	})
}
func composePickerPage(c *ui.Context) {
	s := composeDemoState(c)
	ui.Scroll(c).Fill().Gap(theme.T(c).Metrics().Attachment().Gap()).Children(func() {
		ui.Text(c, s.event)
		c.Key("catalog")
		compose.Picker(c, &s.picker, composeCatalog(c), compose.PickerOptions{OnSelect: func(_ compose.PickerTab, item compose.PickerItem) { s.event = "Picked " + item.Emoji }})
		for _, status := range []compose.PickerPageState{compose.PageReady, compose.PageLoading, compose.PageFailed, compose.PageRestricted} {
			c.Key(status)
			state := ui.Local(c.Root(), status, func() compose.PickerState { return compose.PickerState{} })
			compose.Picker(c, state, compose.PickerData{{State: status}, {State: status}, {State: status}}, compose.PickerOptions{})
		}
	})
}
func composeAttachmentPage(c *ui.Context) {
	s := composeDemoState(c)
	ui.Scroll(c).Fill().Gap(theme.T(c).Metrics().Attachment().Gap()).Children(func() {
		composeHeading(c, "Drag to reorder; open, edit and remove use stable IDs")
		ui.Text(c, s.event)
		compose.Attachments(c, s.attachments, compose.AttachmentOptions{OnOpen: func(id string) { s.event = "Open " + id }, OnEdit: func(id string) { s.event = "Edit " + id }, OnRemove: func(id string) { s.attachments = removeComposeAttachment(s.attachments, id) }, OnMove: func(source, target string) {
			var item compose.Attachment
			for _, a := range s.attachments {
				if a.ID == source {
					item = a
				}
			}
			s.attachments = removeComposeAttachment(s.attachments, source)
			for i, a := range s.attachments {
				if a.ID == target {
					s.attachments = append(s.attachments, compose.Attachment{})
					copy(s.attachments[i+1:], s.attachments[i:])
					s.attachments[i] = item
					break
				}
			}
		}})
	})
}
func removeComposeAttachment(items []compose.Attachment, id string) []compose.Attachment {
	for i, item := range items {
		if item.ID == id {
			return append(items[:i], items[i+1:]...)
		}
	}
	return items
}

// Original geometric host artwork; no third-party media or client assets.
var composeArt = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="96" height="64" viewBox="0 0 96 64"><rect width="96" height="64" rx="8" fill="#466b8a"/><circle cx="68" cy="19" r="9" fill="#f1d878"/><path d="M0 64 28 20 53 52 68 35 96 64" fill="#95bd9b"/></svg>`))

// Twemoji v17.0.3, CC-BY-4.0; attribution in THIRD_PARTY_NOTICES.md.
//
//go:embed emoji/*.svg
var emojiArtwork embed.FS

func demoEmoji(name string) *ui.SVG {
	data, err := emojiArtwork.ReadFile("emoji/" + name + ".svg")
	if err != nil {
		panic(err)
	}
	return ui.MustParseSVG(data)
}

var emojiSmile, emojiWave, emojiHerb, emojiStar = demoEmoji("1f600"), demoEmoji("1f44b"), demoEmoji("1f33f"), demoEmoji("2b50")

func composeCatalog(c *ui.Context) compose.PickerData {
	type cached struct {
		locale string
		data   compose.PickerData
	}
	v := ui.Local(c.Root(), "compose-catalog", func() cached { return cached{} })
	if v.data[0].Categories == nil || v.locale != c.Locale() {
		v.locale = c.Locale()
		tr := func(key string) string { return locales.Text(v.locale, key) }
		v.data = compose.PickerData{
			{Categories: []compose.Category{
				{ID: "recent", Label: tr("recent"), Icon: emojiSmile, Items: []compose.PickerItem{
					{ID: "smile", Label: "😀", Emoji: "😀", Keywords: "smile happy face 微笑", Image: emojiSmile},
					{ID: "wave", Label: "👋", Emoji: "👋", Keywords: "wave hello 挥手", Image: emojiWave},
				}},
				{ID: "nature", Label: tr("nature"), Icon: emojiHerb, Items: []compose.PickerItem{
					{ID: "star", Label: "⭐", Emoji: "⭐", Keywords: "star night 星星", Image: emojiStar},
					{ID: "herb", Label: "🌿", Emoji: "🌿", Keywords: "herb nature 自然", Image: emojiHerb},
					{ID: "system", Label: "🌸", Emoji: "🌸", Keywords: "flower system 花"},
				}},
			}},
			{Categories: []compose.Category{{ID: "favorites", Label: tr("favorites"), Icon: emojiStar, Items: []compose.PickerItem{{ID: "sticker-one", Label: "Observatory sticker", Image: composeArt}, {ID: "sticker-two", Label: "Locked sticker", Image: composeArt, Disabled: true}}}}},
			{Categories: []compose.Category{{ID: "saved", Label: tr("savedGIFs"), Icon: emojiStar, Items: []compose.PickerItem{{ID: "gif-one", Label: "Night sky", Image: composeArt, AspectRatio: 1.5}, {ID: "gif-two", Label: "Portrait sky", Image: composeArt, AspectRatio: 0.6}}}}},
		}
	}
	return v.data
}

func composeIMEPage(c *ui.Context) {
	s := composeDemoState(c)
	ui.Column(c).Fill().Gap(theme.T(c).Metrics().Attachment().Gap()).Children(func() {
		composeHeading(c, "中文输入：预编辑下划线、换行及滚动后的候选窗")
		ui.Text(c, s.event)
		compose.Field(c, &s.drafts[0], compose.FieldOptions{Label: "IME draft", OnSubmit: func(value string) { s.event = "Submitted: " + value }})
		ui.Button(c, "Fill multiline draft").OnClick(func() { s.drafts[0] = strings.Repeat("中文输入 candidate caret\n", 30) })
		ui.Button(c, "Clear IME draft").OnClick(func() { s.drafts[0] = "" })
	})
}
