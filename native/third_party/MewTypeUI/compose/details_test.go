package compose

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestConfirmationMediaAspectRatioAndActions(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Attachment()
	portrait := ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="200" height="600"><rect width="200" height="600" fill="#368aab"/></svg>`))
	items := []Attachment{{ID: "portrait", Name: "Portrait", Preview: portrait}, {ID: "landscape", Name: "Landscape", Preview: testArt}, {ID: "file", Name: "A file", Status: "12 KB"}}
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		services = c.Services()
		Attachments(c, items, AttachmentOptions{Ungrouped: true, OnEdit: func(string) {}}).Label("confirmation")
	}, 500, 700)
	p := bounds(t, tt, "Open attachment portrait")
	// attach_abstract_single_media_preview.cpp:138-170: max 245 DIP, original aspect retained.
	expect(t, "portrait-max-height", near(p.H, m.MaxMediaHeight()))
	expect(t, "portrait-preserves-ratio", near(p.W, p.H/3))
	landscape := bounds(t, tt, "Open attachment landscape")
	expect(t, "small-media-keeps-natural-size", near(landscape.W, 64) && near(landscape.H, 64))
	edit, remove := bounds(t, tt, "Edit attachment portrait"), bounds(t, tt, "Remove attachment portrait")
	expect(t, "media-controls-horizontal", near(edit.Y, remove.Y) && near(remove.X-edit.X, m.ControlWidth()/2) && near(edit.H, m.ControlHeight()))
	items[0].AsFile = true
	services.Invalidate()
	tt.Frame()
	expect(t, "send-as-file-uses-thumbnail", near(bounds(t, tt, "Open attachment portrait").W, m.ThumbSize()) && tt.HasText("Portrait"))
}

func TestDockedPickerPlacementDismissalAndOverride(t *testing.T) {
	state := PickerState{}
	open := false
	closed := 0
	var services ui.Services
	custom := false
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		services = c.Services()
		ui.Column(c).Fill().Children(func() {
			ui.Button(c, "Outside")
			ui.Spacer(c)
			anchor := ui.Button(c, "Toggle picker").Label("Picker anchor").FillWidth().OnClick(func() { open = !open })
			o := PickerDockOptions{KeepOpen: true, Picker: PickerOptions{Open: &open}, OnClose: func() { closed++ }}
			if custom {
				o.Place = func(panel, anchor ui.Element) {
					panel.Absolute().Left(theme.Light().Metrics().Conversation().PaddingLeft()).Top(theme.Light().Metrics().Header().Height())
				}
			}
			DockedPicker(c, anchor, &state, catalog(), o)
		})
	}, 600, 700)
	click(t, tt, "Toggle picker")
	tt.Advance(theme.Light().Motion().Picker().Duration() + time.Millisecond)
	tt.Frame()
	panel, anchor := bounds(t, tt, "Media picker"), bounds(t, tt, "Picker anchor")
	expect(t, "picker-docks-above-editor", near(panel.Y+panel.H, anchor.Y) && near(panel.X+panel.W, anchor.X+anchor.W))
	click(t, tt, "Search media")
	tt.Key(0, ui.KeyEscape)
	expect(t, "docked-escape-closes", !open && closed == 1)
	tt.Advance(theme.Light().Motion().Picker().Duration() + time.Millisecond)
	tt.Frame()
	_, visible := tt.Find("Search media")
	expect(t, "docked-exit-finishes", !visible && !tt.FramePending())
	click(t, tt, "Toggle picker")
	tt.Advance(theme.Light().Motion().Picker().Duration() + time.Millisecond)
	tt.Frame()
	click(t, tt, "Outside")
	expect(t, "outside-press-closes-picker", !open && closed == 2)
	tt.Advance(theme.Light().Motion().Picker().Duration() + time.Millisecond)
	tt.Frame()
	custom = true
	open = true
	services.Invalidate()
	tt.Frame()
	tt.Advance(theme.Light().Motion().Picker().Duration() + time.Millisecond)
	tt.Frame()
	expect(t, "host-placement-overrides-default", near(bounds(t, tt, "Media picker").X, theme.Light().Metrics().Conversation().PaddingLeft()))
}

func TestFieldPreeditUnderlineCommitAndCaretFollow(t *testing.T) {
	th := theme.Light()
	draft := ""
	submitted := 0
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		services = c.Services()
		Field(c, &draft, FieldOptions{OnSubmit: func(string) { submitted++ }})
	}, 160, 300)
	click(t, tt, "Message")
	origin, _ := tt.TextCaret()
	tt.Compose("中文", 2)
	preedit, active := tt.TextCaret()
	before := append([]byte(nil), tt.Image().Pix...)
	expect(t, "ime-preedit-remains-uncommitted", active && draft == "" && preedit.X > origin.X)
	tt.Key(0, ui.KeyEnter)
	expect(t, "ime-confirm-does-not-submit", submitted == 0 && draft == "")
	tt.Type("中文")
	committed, _ := tt.TextCaret()
	expect(t, "ime-commit-retains-caret", draft == "中文" && near(committed.X, preedit.X) && near(committed.Y, preedit.Y))
	// mygo textarea.go:412-415 places the preedit underline at line bottom-2.
	img := tt.Image()
	different := false
	for y := int(preedit.Y+preedit.H) - 2; y < int(preedit.Y+preedit.H); y++ {
		for x := int(origin.X); x < int(preedit.X); x++ {
			i := y*img.Stride + x*4
			if !bytes.Equal(before[i:i+4], img.Pix[i:i+4]) {
				different = true
			}
		}
	}
	expect(t, "preedit-underline-clears-on-commit", different)
	draft = strings.Repeat("中文输入 ", 30)
	services.Invalidate()
	tt.Frame()
	tt.Key(ui.Ctrl, ui.KeyEnd)
	tt.Compose("候选", 2)
	caret, active := tt.TextCaret()
	field := bounds(t, tt, "Message")
	expect(t, "ime-caret-stays-in-scrolled-field", active && caret.Y >= field.Y && caret.Y+caret.H <= field.Y+field.H+1 && caret.X >= field.X && caret.X <= field.X+field.W)
	tt.Compose("", 0)
	tt.Type("候选")
	tt.Key(0, ui.KeyEnter)
	expect(t, "ime-commit-submits-once", submitted == 1 && strings.HasSuffix(draft, "候选"))
}

func TestConfirmationAlbumGroupsAndStableActions(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Attachment()
	items := []Attachment{{ID: "a", Name: "Image A", Preview: testArt}, {ID: "b", Name: "Image B", Preview: testArt}}
	opened, move := "", ""
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		services = c.Services()
		Attachments(c, items, AttachmentOptions{OnOpen: func(id string) { opened = id }, OnEdit: func(string) {}, OnMove: func(a, b string) { move = a + ":" + b }})
	}, 500, 1000)
	a, b := bounds(t, tt, "Open attachment a"), bounds(t, tt, "Open attachment b")
	// attach_album_preview.cpp:160-164 uses 308 DIP limit, 50 minimum and 2 DIP gap.
	expect(t, "album-two-geometry", near(a.W, (m.Width()-m.AlbumGap())/2) && near(b.X-a.X-a.W, m.AlbumGap()) && near(a.H, a.W))
	click(t, tt, "Open attachment b")
	expect(t, "album-open-stable-id", opened == "b")
	tt.Press(a.X+a.W/2, a.Y+a.H/2)
	tt.Move(b.X+b.W/2, b.Y+b.H/2)
	tt.Release(b.X+b.W/2, b.Y+b.H/2)
	expect(t, "album-drag-stable-id", move == "a:b")
	items[0], items[1] = items[1], items[0]
	services.Invalidate()
	tt.Frame()
	click(t, tt, "Open attachment a")
	expect(t, "album-reorder-preserves-action", opened == "a")
	for n := 3; n <= 11; n++ {
		items = append(items, Attachment{ID: fmt.Sprint(n), Name: fmt.Sprint(n), Preview: testArt})
		services.Invalidate()
		tt.Frame()
		for _, item := range items {
			r := bounds(t, tt, "Open attachment "+item.ID)
			expect(t, "album-items-stay-in-preview", r.W > 0 && r.H > 0 && r.X >= 0 && r.X+r.W <= m.Width()+1)
		}
	}
	last := bounds(t, tt, "Open attachment 11")
	tenth := bounds(t, tt, "Open attachment 10")
	expect(t, "album-splits-after-ten", last.Y >= tenth.Y+tenth.H+m.Gap()-1)
}

func TestDockedPickerPointerLeaveDelay(t *testing.T) {
	th := theme.Light()
	open := true
	state := PickerState{}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		ui.Column(c).Fill().Children(func() {
			ui.Spacer(c)
			anchor := ui.Button(c, "Anchor").FillWidth()
			DockedPicker(c, anchor, &state, catalog(), PickerDockOptions{Picker: PickerOptions{Open: &open}})
		})
	}, 600, 700)
	tt.Advance(th.Motion().Picker().Duration() + time.Millisecond)
	tt.Frame()
	p := bounds(t, tt, "Media picker")
	tt.Move(p.X+p.W/2, p.Y+p.H/2)
	tt.Move(0, 0)
	tt.Advance(th.Motion().PickerLeave().Duration() - time.Millisecond)
	tt.Frame()
	expect(t, "picker-leave-waits-source-delay", open)
	tt.Advance(time.Millisecond)
	tt.Frame()
	expect(t, "picker-leave-closes-at-source-delay", !open)
}
