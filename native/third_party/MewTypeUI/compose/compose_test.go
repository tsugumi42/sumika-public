package compose

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func expect(t *testing.T, id string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", id)
	}
}
func near(a, b float32) bool { return math.Abs(float64(a-b)) <= 1 }
func bounds(t *testing.T, tt *ui.Tester, label string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("missing %q: %q", label, tt.Texts())
	}
	return r
}
func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if e := tt.Click(label); e != nil {
		t.Fatal(e)
	}
}
func view(fn func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		ui.Column(c).Fill().Gap(0).Children(func() { fn(c) })
	}
}
func imageCopy(tt *ui.Tester) []byte { return tt.Image().Pix }

func TestEditorInputAndSubmission(t *testing.T) {
	draft := ""
	submitted := []string{}
	attach, emoji, voice := 0, 0, 0
	tt := ui.NewTester(view(func(c *ui.Context) {
		Editor(c, &draft, EditorOptions{OnSubmit: func(s string) { submitted = append(submitted, s) }, OnAttach: func() { attach++ }, OnEmoji: func() { emoji++ }, OnVoice: func() { voice++ }}).Label("Editor")
	}), 500, 400)
	m := theme.Light().Metrics().Compose()
	// chat_helpers.style:1313,1329-1341,1431; history_widget.cpp:7640-7667.
	field := bounds(t, tt, "Message")
	root := bounds(t, tt, "Editor")
	left := bounds(t, tt, "Attach")
	right := bounds(t, tt, "Voice message")
	expect(t, "editor-min-height", near(root.H, m.FieldMinHeight()+2*m.SendPadding()))
	expect(t, "field-min-height", near(field.H, m.FieldMinHeight()))
	expect(t, "field-left", near(field.X, m.SendRight()+m.AttachWidth()))
	expect(t, "field-top-padding", near(field.Y-root.Y, m.SendPadding()))
	expect(t, "attach-size", near(left.W, m.AttachWidth()) && near(left.H, m.AttachHeight()))
	expect(t, "send-size", near(right.W, m.SendWidth()) && near(right.H, m.SendHeight()))
	expect(t, "button-bottom", near(right.Y+right.H, root.Y+root.H))
	click(t, tt, "Voice message")
	click(t, tt, "Attach")
	click(t, tt, "Emoji")
	expect(t, "toolbar-actions", attach == 1 && emoji == 1 && voice == 1)
	click(t, tt, "Message")
	tt.Type("hello")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("world")
	expect(t, "shift-enter-newline", draft == "hello\nworld" && len(submitted) == 0)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	expect(t, "enter-submits-once", len(submitted) == 1 && submitted[0] == "hello\nworld")
	expect(t, "submit-preserves-draft", draft == "hello\nworld")
	click(t, tt, "Send")
	tt.Frame()
	expect(t, "click-submits-once", len(submitted) == 2 && submitted[1] == draft)
	draft = " \n\t"
	tt.Frame()
	click(t, tt, "Message")
	tt.Key(0, ui.KeyEnter)
	expect(t, "blank-does-not-submit", len(submitted) == 2)
}

func TestFieldCompositionReadonlyAndLifetime(t *testing.T) {
	draft := ""
	submits := 0
	readOnly, disabled, shown := false, false, true
	var handle ui.Handle
	tt := ui.NewTester(view(func(c *ui.Context) {
		if shown {
			c.Key("draft")
			Field(c, &draft, FieldOptions{Handle: &handle, ReadOnly: readOnly, Disabled: disabled, OnSubmit: func(string) { submits++ }})
		}
	}), 400, 300)
	click(t, tt, "Message")
	tt.Compose("にほん", 3)
	tt.Key(0, ui.KeyEnter)
	expect(t, "ime-enter-does-not-send", submits == 0 && draft == "")
	tt.Type("日本")
	tt.Key(0, ui.KeyEnter)
	expect(t, "ime-commit-can-send", submits == 1 && draft == "日本")
	readOnly = true
	tt.Frame()
	click(t, tt, "Message")
	tt.Type("x")
	tt.Key(0, ui.KeyEnter)
	tt.Command("selectAll")
	if err := tt.RightClick("Message"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy"); err != nil {
		t.Fatal(err)
	}
	expect(t, "readonly-copy-no-edit", draft == "日本" && tt.Clipboard() == "日本" && submits == 1)
	readOnly = false
	disabled = true
	tt.Frame()
	tt.Type("x")
	tt.Key(0, ui.KeyEnter)
	expect(t, "disabled-no-edit", draft == "日本" && submits == 1)
	disabled = false
	shown = false
	tt.Frame()
	tt.Frame()
	shown = true
	tt.Frame()
	click(t, tt, "Message")
	tt.Command("selectAll")
	tt.Type("restored")
	expect(t, "remount-binds-host-draft", draft == "restored")
}

func TestFieldHeightAndTheme(t *testing.T) {
	draft := ""
	available := float32(0)
	base := theme.Light()
	m := base.Metrics().Compose()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, base)
		ui.Column(c).Fill().Children(func() { Field(c, &draft, FieldOptions{AvailableHeight: available}) })
	}, 240, 600)
	draft = strings.Repeat("a wrapped line with plenty of words ", 80)
	tt.Frame()
	// chat_helpers.style:1327; history_widget.cpp:9234-9253 overrides the inherited 72.
	expect(t, "expanded-cap", near(bounds(t, tt, "Message").H, m.ExpandedMaxHeight()))
	available = 180
	tt.Frame()
	expect(t, "available-height-reserve", near(bounds(t, tt, "Message").H, available-2*m.SendPadding()-m.ReplyHeight()))
	draft = "one two three four five six seven eight nine ten"
	available = 0
	tt.Frame()
	wide := bounds(t, tt, "Message").H
	tt.SetSize(120, 600)
	expect(t, "wrap-grows-height", bounds(t, tt, "Message").H > wide)
	draft = "one\ntwo\nthree"
	tt.SetSize(400, 600)
	tt.Frame()
	normal := bounds(t, tt, "Message").H
	tt.SetPreferences(ui.Preferences{TextScale: 2})
	expect(t, "text-scale-once", near(bounds(t, tt, "Message").H, 2*normal))
	tt.Frame()
	expect(t, "text-scale-stable", near(bounds(t, tt, "Message").H, 2*normal))
}

func TestFieldSubmitModeAndPaste(t *testing.T) {
	draft := ""
	submits := 0
	paste := ""
	tt := ui.NewTester(view(func(c *ui.Context) {
		Field(c, &draft, FieldOptions{SubmitKey: ControlEnterSends, OnSubmit: func(string) { submits++ }, OnPaste: func(s string) bool { paste = s; return true }})
	}), 400, 300)
	click(t, tt, "Message")
	tt.Type("draft")
	tt.Key(0, ui.KeyEnter)
	expect(t, "alternate-enter-newline", draft == "draft\n" && submits == 0)
	tt.Key(ui.Ctrl, ui.KeyEnter)
	expect(t, "control-enter-submit", submits == 1)
	tt.SetClipboard("media caption")
	tt.Command("paste")
	expect(t, "host-takes-paste", paste == "media caption" && draft == "draft\n")
}

func TestEditorModesAndButtonAnimation(t *testing.T) {
	draft := ""
	editing, attachments, disabled := false, false, false
	sent := 0
	tt := ui.NewTester(view(func(c *ui.Context) {
		Editor(c, &draft, EditorOptions{Editing: editing, HasAttachments: attachments, Disabled: disabled, OnSubmit: func(string) { sent++ }})
	}), 500, 300)
	draft = "typed"
	tt.Frame()
	start := imageCopy(tt)
	motion := theme.Light().Motion().ComposeButton()
	tt.Advance(motion.Duration() / 2)
	middle := imageCopy(tt)
	tt.Advance(motion.Duration()/2 + time.Millisecond)
	end := imageCopy(tt)
	// send_button.cpp:139-143: universalDuration (120), default linear.
	expect(t, "button-transition-midpoint", !bytes.Equal(start, middle) && !bytes.Equal(middle, end))
	click(t, tt, "Attach")
	tt.Move(490, 290)
	tt.Advance(theme.Light().Motion().RippleHide().Duration() + time.Millisecond)
	expect(t, "button-transition-stops", !tt.FramePending())
	editing = true
	tt.Frame()
	click(t, tt, "Save edit")
	expect(t, "editing-saves", sent == 1)
	editing = false
	draft = ""
	attachments = true
	tt.Frame()
	click(t, tt, "Send")
	expect(t, "attachments-enable-send", sent == 2)
	click(t, tt, "Message")
	tt.Key(0, ui.KeyEnter)
	expect(t, "attachment-enter-submit", sent == 3)
	disabled = true
	tt.Frame()
	click(t, tt, "Send")
	expect(t, "disabled-blocks-send", sent == 3)
}

func TestReferenceBarGeometryAndActions(t *testing.T) {
	cancel, open := 0, 0
	tt := ui.NewTester(view(func(c *ui.Context) {
		ReferenceBar(c, Reference{ID: "reply", Name: "Alice", Summary: "A summary", Preview: testArt}, ReferenceOptions{OnCancel: func() { cancel++ }, OnActivate: func() { open++ }}).Label("Reference")
	}), 500, 200)
	m := theme.Light().Metrics().Compose()
	root := bounds(t, tt, "Reference")
	glyph := bounds(t, tt, "Reply target")
	close := bounds(t, tt, "Cancel reference")
	// chat_helpers.style:1144-1162; history_widget.cpp:11546-11635 uses an action glyph.
	expect(t, "reference-height", near(root.H, m.ReplyHeight()))
	expect(t, "reference-glyph-size", near(glyph.W, m.ReferenceIconSize()) && near(glyph.H, m.ReferenceIconSize()))
	expect(t, "reference-glyph-position", near(glyph.X, m.ReferenceIconLeft()) && near(glyph.Y, m.ReferenceIconTop()))
	expect(t, "cancel-size", near(close.W, m.ReplyHeight()) && near(close.H, m.ReplyHeight()))
	click(t, tt, "Alice")
	click(t, tt, "Cancel reference")
	tt.Frame()
	expect(t, "independent-reference-actions", open == 1 && cancel == 1)
	tt.SetPreferences(ui.Preferences{TextScale: 2})
	scaled, summary := bounds(t, tt, "Reference"), bounds(t, tt, "A summary")
	expect(t, "scaled-reference-fits-summary", scaled.H > root.H && summary.Y+summary.H <= scaled.Y+scaled.H)
}

var testArt = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><rect width="64" height="64" fill="#d7a646"/></svg>`))

func catalog() PickerData {
	return PickerData{
		{Categories: []Category{{ID: "faces", Label: "Faces", Icon: testArt, Items: []PickerItem{{ID: "smile", Label: "Smile", Keywords: "happy", Image: testArt}, {ID: "frown", Label: "Frown", Keywords: "sad", Image: testArt}}}, {ID: "nature", Label: "Nature", Icon: testArt, Items: []PickerItem{{ID: "star", Label: "Star", Image: testArt}}}}},
		{Categories: []Category{{ID: "pack", Label: "Pack", Items: []PickerItem{{ID: "sticker", Label: "Sticker", Image: testArt}, {ID: "locked", Label: "Locked sticker", Image: testArt, Disabled: true}}}}},
		{Categories: []Category{{ID: "saved", Label: "Saved", Items: []PickerItem{{ID: "gif", Label: "GIF", Image: testArt, AspectRatio: 1.5}}}}},
	}
}

func TestPickerSearchTabsAndState(t *testing.T) {
	state := PickerState{}
	picks := []string{}
	search := ""
	data := catalog()
	tt := ui.NewTester(view(func(c *ui.Context) {
		Picker(c, &state, data, PickerOptions{OnSelect: func(_ PickerTab, item PickerItem) { picks = append(picks, item.ID) }, OnSearch: func(_ PickerTab, q string) { search = q }}).Label("Picker")
	}), 500, 500)
	m := theme.Light().Metrics().Picker()
	r := bounds(t, tt, "Picker")
	cell := bounds(t, tt, "Smile")
	// chat_helpers.style:514-523,799-809,580; widgets.style:1193.
	expect(t, "picker-size", near(r.W, m.Width()) && near(r.H, m.MinHeight()))
	expect(t, "emoji-cell-size", near(cell.W, m.CellWidth()) && near(cell.H, m.CellHeight()))
	expect(t, "picker-tab-height", near(bounds(t, tt, "Emoji").H, m.TabHeight()))
	expect(t, "picker-search-height", near(bounds(t, tt, "Search media").H, m.SearchHeight()))
	click(t, tt, "Smile")
	expect(t, "emoji-selection", len(picks) == 1 && picks[0] == "smile")
	click(t, tt, "Search media")
	tt.Type("HAPPY")
	expect(t, "query-notice", state.Query[EmojiTab] == "HAPPY" && search == "HAPPY")
	_, found := tt.Find("Frown")
	_, smile := tt.Find("Smile")
	expect(t, "keyword-filter", !found && smile)
	click(t, tt, "Stickers")
	tt.Advance(theme.Light().Motion().PickerSlide().Duration() + time.Millisecond)
	expect(t, "tab-query-isolation", state.Tab == StickerTab && state.Query[StickerTab] == "")
	expect(t, "sticker-cell-size", near(bounds(t, tt, "Sticker").W, m.StickerSize()))
	// stickers_list_widget.cpp:2277-2280 bounds the image by the cell minus two small radii.
	sticker := bounds(t, tt, "Sticker")
	goldWidth := 0
	for x := int(sticker.X); x < int(sticker.X+sticker.W); x++ {
		pixel := tt.Image().RGBAAt(x, int(sticker.Y+sticker.H/2))
		if pixel.R == 0xd7 && pixel.G == 0xa6 && pixel.B == 0x46 {
			goldWidth++
		}
	}
	expect(t, "sticker-image-inset", near(float32(goldWidth)+2*theme.Light().Metrics().Control().RadiusSmall(), sticker.W))
	click(t, tt, "Locked sticker")
	expect(t, "locked-item-no-pick", len(picks) == 1)
	click(t, tt, "Sticker")
	click(t, tt, "GIFs")
	tt.Advance(theme.Light().Motion().PickerSlide().Duration() + time.Millisecond)
	expect(t, "gif-aspect-geometry", near(bounds(t, tt, "GIF").W, m.GifHeight()*1.5) && near(bounds(t, tt, "GIF").H, m.GifHeight()))
	click(t, tt, "Emoji")
	tt.Advance(theme.Light().Motion().PickerSlide().Duration() + time.Millisecond)
	expect(t, "tab-retains-query", state.Query[EmojiTab] == "HAPPY")
	click(t, tt, "Clear search")
	tt.ClickAt(r.X+m.CategoryGap()+2*m.CategoryWidth()+m.CategoryWidth()/2, r.Y+r.H-m.FooterHeight()/2)
	expect(t, "category-selects-id", state.Category[EmojiTab] == "nature")
	click(t, tt, "All")
	click(t, tt, "Search media")
	tt.Type("no match")
	expect(t, "empty-results", tt.HasText("No results"))
	state.Query[EmojiTab] = ""
	data[EmojiTab].Status = "Loading catalog"
	tt.Frame()
	expect(t, "host-status-replaces-grid", tt.HasText("Loading catalog") && !tt.HasText("Smile"))
}

func TestPickerMotionRadiusAndNarrowLayout(t *testing.T) {
	state := PickerState{}
	data := catalog()
	base, err := theme.Compile(theme.Definition{Version: theme.Version, Base: theme.LightBase, Colors: map[string]string{"emoji.panBackground": "@control.primaryBackground"}})
	if err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, base)
		ui.Column(c).Fill().Children(func() { Picker(c, &state, data, PickerOptions{AvailableHeight: 600}).Label("Picker") })
	}, 500, 600)
	m := base.Metrics().Picker()
	r := bounds(t, tt, "Picker")
	expect(t, "picker-height-ratio", near(r.H, 600*m.HeightRatio()))
	// emojiPanRadius=8, chat_helpers.style:523. Rounded corner exposes window background.
	corner := tt.Image().RGBAAt(int(r.X), int(r.Y))
	center := tt.Image().RGBAAt(int(r.X+m.Radius()), int(r.Y))
	expect(t, "picker-rounded-corner", corner != center)
	state.Tab = StickerTab
	tt.Frame()
	tt.Advance(base.Motion().PickerSlide().Duration() / 2)
	mid := bounds(t, tt, "Sticker")
	tt.Advance(base.Motion().PickerSlide().Duration()/2 + time.Millisecond)
	tt.Frame()
	last := bounds(t, tt, "Sticker")
	expect(t, "picker-slide-path", mid.X > last.X && mid.X < r.X+r.W)
	expect(t, "picker-motion-stops", !tt.FramePending())
	tt.SetSize(220, 600)
	expect(t, "picker-narrow-width", near(bounds(t, tt, "Picker").W, 220))
	state.Tab = EmojiTab
	tt.Frame()
	tt.Advance(base.Motion().PickerSlide().Duration() / 2)
	tt.Frame()
	narrow := bounds(t, tt, "Picker")
	// The first tile is clipped at the viewport edge during entry; sample the second.
	arriving := bounds(t, tt, "Frown").X
	tt.Advance(base.Motion().PickerSlide().Duration()/2 + time.Millisecond)
	tt.Frame()
	expect(t, "narrow-picker-slide-path", near(bounds(t, tt, "Frown").X-arriving, narrow.W*(1-float32(math.Sqrt(0.75)))))
}

func TestAttachmentsIDsReorderAndRemove(t *testing.T) {
	items := []Attachment{{ID: "one", Name: "Same name", Status: "Ready", Preview: testArt, AsFile: true}, {ID: "two", Name: "Same name", Status: "Preparing", Preparing: true}}
	opened, edited, removed, move := "", "", "", ""
	tt := ui.NewTester(view(func(c *ui.Context) {
		Attachments(c, items, AttachmentOptions{OnOpen: func(id string) { opened = id }, OnEdit: func(id string) { edited = id }, OnRemove: func(id string) { removed = id }, OnMove: func(a, b string) { move = a + ":" + b }}).Label("Attachments")
	}), 750, 300)
	m := theme.Light().Metrics().Attachment()
	a := bounds(t, tt, "Open attachment one")
	b := bounds(t, tt, "Open attachment two")
	// chat.style:406-418; send_files_box.cpp:1643 stacks file previews with 10 DIP gaps.
	expect(t, "attachment-height", near(bounds(t, tt, "Attachments").H, m.ThumbSize()+m.Gap()+m.FileSize()))
	expect(t, "attachment-preview-size", near(a.W, m.ThumbSize()) && near(a.H, m.ThumbSize()))
	expect(t, "attachment-gap", near(b.Y-(a.Y+a.H), m.Gap()))
	click(t, tt, "Open attachment two")
	click(t, tt, "Edit attachment two")
	click(t, tt, "Remove attachment one")
	expect(t, "attachment-stable-actions", opened == "two" && edited == "" && removed == "one")
	// Drag a non-button part of the card to the other card.
	tt.Press(a.X+m.ThumbSize()+20, a.Y+m.ThumbSize()-10)
	tt.Move(b.X+m.FileSize()+20, b.Y+m.FileSize()-10)
	tt.Release(b.X+m.FileSize()+20, b.Y+m.FileSize()-10)
	expect(t, "attachment-drag-ids", move == "one:two")
	items[0], items[1] = items[1], items[0]
	tt.Frame()
	click(t, tt, "Open attachment one")
	expect(t, "reordered-action-still-id", opened == "one")
}

func TestPickerRevealAndReducedMotion(t *testing.T) {
	state := PickerState{}
	open := false
	tt := ui.NewTester(view(func(c *ui.Context) { Picker(c, &state, catalog(), PickerOptions{Open: &open}).Label("Picker") }), 500, 500)
	open = true
	tt.Frame()
	start := imageCopy(tt)
	tt.Advance(theme.Light().Motion().Picker().Duration() / 2)
	middle := imageCopy(tt)
	tt.Advance(theme.Light().Motion().Picker().Duration()/2 + time.Millisecond)
	tt.Frame()
	end := imageCopy(tt)
	expect(t, "panel-reveal-progress", !bytes.Equal(start, middle) && !bytes.Equal(middle, end))
	click(t, tt, "Search media")
	tt.Key(0, ui.KeyEscape)
	expect(t, "panel-escape-closes", !open)
	tt.Advance(theme.Light().Motion().Picker().Duration() + time.Millisecond)
	tt.Frame()
	_, visible := tt.Find("Search media")
	expect(t, "panel-close-finishes", !visible && !tt.FramePending())
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	open = true
	tt.Frame()
	state.Tab = StickerTab
	tt.Frame()
	expect(t, "reduced-motion-immediate", !tt.FramePending() && bounds(t, tt, "Sticker").X < theme.Light().Metrics().Picker().Width())
}

func TestFieldTypographyAndPlaceholder(t *testing.T) {
	draft := ""
	base, err := theme.Compile(theme.Definition{Version: theme.Version, Base: theme.LightBase, Colors: map[string]string{"compose.placeholder": "#00ff00", "compose.placeholderFocused": "#00ff00"}})
	if err != nil {
		t.Fatal(err)
	}
	var measured float32
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, base)
		ui.Column(c).Fill().Gap(0).Children(func() {
			_, measured = c.MeasureText(0, ui.Span{Text: "M", Size: base.Typography().Compose().Size(), Weight: base.Typography().Compose().Weight(), Font: base.Typography().Font()})
			Field(c, &draft, FieldOptions{Placeholder: "Sample"})
		})
	}, 400, 300)
	calibration := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, base)
		ui.Column(c).Fill().Gap(0).Children(func() {
			text(c, "Sample", base.Typography().Compose(), base.Colors().Compose().Placeholder()).Absolute().Left(base.Metrics().Compose().PlaceholderInset()).Top(base.Metrics().Compose().TextMargin()).SingleLine()
		})
	}, 400, 300)
	// chat_helpers.style:1318,1321 and basic.style:54: exact glyph paint at the 2 DIP inset.
	expect(t, "placeholder-inset-and-type", bytes.Equal(tt.Image().Pix, calibration.Image().Pix))
	click(t, tt, "Message")
	tt.Type("M")
	caret, active := tt.TextCaret()
	expect(t, "field-type-height", active && near(caret.H, measured))
	hasPlaceholder := func() bool {
		pixels := tt.Image().Pix
		for i := 0; i < len(pixels); i += 4 {
			p := pixels[i : i+4]
			if p[0] == 0 && p[1] == 255 && p[2] == 0 {
				return true
			}
		}
		return false
	}
	expect(t, "placeholder-clears-on-edit", !hasPlaceholder())
	tt.Command("selectAll")
	tt.Command("delete")
	tt.Compose("日本", 2)
	expect(t, "composition-hides-placeholder", !hasPlaceholder() && draft == "")
}

func TestHostAcceptsOrReplacesDraft(t *testing.T) {
	draft := ""
	submitted := []string{}
	tt := ui.NewTester(view(func(c *ui.Context) {
		Editor(c, &draft, EditorOptions{OnSubmit: func(s string) { submitted = append(submitted, s); draft = "" }})
	}), 500, 300)
	click(t, tt, "Message")
	tt.Type("accepted")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	expect(t, "host-clear-reaches-native-editor", draft == "" && len(submitted) == 1 && submitted[0] == "accepted")
	tt.Type("second")
	tt.Key(0, ui.KeyEnter)
	expect(t, "accepted-text-not-replayed", draft == "" && len(submitted) == 2 && submitted[1] == "second")
}

func TestActionHoverRippleAndKeyboard(t *testing.T) {
	disabled := false
	clicks := 0
	var services ui.Services
	tt := ui.NewTester(view(func(c *ui.Context) {
		services = c.Services()
		ActionButton(c, Attach, ActionOptions{Disabled: disabled, OnClick: func() { clicks++ }})
	}), 300, 200)
	r := bounds(t, tt, "Attach")
	base := theme.Light()
	m := base.Metrics().Compose()
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	painted := func(want ui.Color) bool {
		image := tt.Image()
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			for x := int(r.X); x < int(r.X+r.W); x++ {
				p := image.RGBAAt(x, y)
				if p.R == want.R && p.G == want.G && p.B == want.B && p.A == want.A {
					return true
				}
			}
		}
		return false
	}
	// colors.palette:461-462: icon hover; widgets.style:705-706: ripple lifetime.
	expect(t, "action-hover-palette", painted(base.Colors().Compose().IconForegroundHover()))
	x, y := r.X+r.W/2, r.Y+(r.H-m.RippleSize())/2+m.RippleSize()/8
	tt.Press(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(base.Motion().RippleShow().Duration())
	tt.Frame()
	p := tt.Image().RGBAAt(int(x), int(y))
	want := base.Colors().Compose().ButtonBackgroundPressed()
	expect(t, "ripple-fills-circle", p.R == want.R && p.G == want.G && p.B == want.B)
	tt.Release(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(base.Motion().RippleHide().Duration())
	tt.Frame()
	p = tt.Image().RGBAAt(int(x), int(y))
	want = base.Colors().Window().Background()
	expect(t, "ripple-release-clears", p.R == want.R && p.G == want.G && p.B == want.B)
	tt.Key(0, ui.KeySpace)
	expect(t, "action-keyboard-activation", clicks == 2)
	disabled = true
	// A host change must invalidate even while a ripple requests paint-only frames.
	services.Invalidate()
	tt.Frame()
	click(t, tt, "Attach")
	tt.Key(0, ui.KeySpace)
	expect(t, "action-disabled-blocks-input", clicks == 2)
}
