package chat

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
	"time"
)

func TestBubbleContextMenuAndPointerFeedback(t *testing.T) {
	source := theme.Light()
	selected := false
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		Header(c, HeaderInfo{Title: "Study group"}, HeaderOptions{Actions: []Action{{ID: "search", Label: "Search", Kind: SearchAction}}})
		Bubble(c, Message{Text: "Context target"}, BubbleOptions{Selected: selected}, nil).Label("target").ContextMenu(func(m *ui.Menu) {
			if m.Item("Select").Chosen() {
				selected = true
			}
		})
	}, 400, 250)
	r := box(tt, "Search")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	// Edge pixels mix with the background; inspect the complete icon's solid ink.
	ink := rgba(source.Colors().Menu().IconForegroundHover())
	hasHoverInk := false
	image := tt.Image()
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			hasHoverInk = hasHoverInk || image.RGBAAt(x, y) == ink
		}
	}
	verify(t, "header hover changes actual icon ink", hasHoverInk)
	if err := tt.RightClick("target"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Select"); err != nil {
		t.Fatal(err)
	}
	verify(t, "right click applies host selection", selected)
	tt.Press(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(source.Motion().RippleShow().Duration() / 2)
	verify(t, "pressed ripple requests paint frames", tt.FramePending())
	tt.Release(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(source.Motion().RippleHide().Duration() + time.Millisecond)
	tt.Frame()
	verify(t, "released ripple stops paint frames", !tt.FramePending())
}

func TestAlternateRevealAndLatestThreshold(t *testing.T) {
	source := theme.Light()
	var state RevealState
	var start time.Time
	quint, exact, beyond, unloaded := float32(0), false, false, false
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		start = c.Now()
		Reveal(c, &state, RevealOptions{Height: 100, ListPath: true}, func() { ui.Text(c, "Alternate reveal") }).Label("alternate")
		exact = LatestVisible(c, source.Metrics().Navigation().ToLatestShownAfter(), true)
		beyond = LatestVisible(c, source.Metrics().Navigation().ToLatestShownAfter()+1, true)
		unloaded = LatestVisible(c, 0, false)
	}, 400, 250)
	state.Start(start)
	tt.Frame()
	tt.Advance(source.Motion().MessageRevealList().Duration() / 2)
	tt.Frame()
	quint = box(tt, "alternate").H
	verify(t, "alternate reveal follows quintic curve", near(quint, 96.875))
	verify(t, "latest threshold preserves boundary and unloaded state", !exact && beyond && unloaded)
}
