package chat

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func verify(t *testing.T, name string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", name)
	}
}
func near(a, b float32) bool                       { return math.Abs(float64(a-b)) <= 1 }
func box(tt *ui.Tester, label string) ui.Rect      { r, _ := tt.Find(label); return r }
func pixel(tt *ui.Tester, x, y float32) color.RGBA { return tt.Image().RGBAAt(int(x), int(y)) }
func rgba(v ui.Color) color.RGBA                   { return color.RGBA{R: v.R, G: v.G, B: v.B, A: v.A} }
func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}

func TestHeaderGeometryAndInput(t *testing.T) {
	source := theme.Light()
	m := source.Metrics().Header()
	calls, disabled := 0, false
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		Header(c, HeaderInfo{Title: "Study group", Subtitle: "online", Status: Online}, HeaderOptions{
			Actions: []Action{{ID: "search", Label: "Search", Kind: SearchAction, Disabled: disabled, OnActivate: func() { calls++ }}, {ID: "menu", Label: "More", Kind: MenuAction}},
		}).Label("header")
	}, 500, 200)
	// info.style:919,929,950,923: fixed height and independent action widths.
	verify(t, "header height", near(box(tt, "header").H, m.Height()))
	verify(t, "header search size", near(box(tt, "Search").W, m.ActionWidth()) && near(box(tt, "Search").H, m.Height()))
	verify(t, "header menu width", near(box(tt, "More").W, m.MenuWidth()))
	verify(t, "title position", near(box(tt, "Study group").X, m.TitleLeft()) && near(box(tt, "Study group").Y, m.TitleTop()))
	verify(t, "status bottom", near(box(tt, "online").Y+box(tt, "online").H, m.Height()-m.StatusBottom()))
	click(t, tt, "Search")
	verify(t, "header click once", calls == 1)
	tt.Key(0, ui.KeySpace)
	verify(t, "header keyboard activation", calls == 2)
	disabled = true
	tt.Frame()
	click(t, tt, "Search")
	verify(t, "disabled action suppressed", calls == 2)
	tt.SetSize(220, 200)
	verify(t, "actions stay within narrow header", box(tt, "More").X+box(tt, "More").W <= 220)
	tt.SetPreferences(ui.Preferences{TextScale: 1.5})
	verify(t, "large header text does not overlap status", box(tt, "Study group").Y+box(tt, "Study group").H <= box(tt, "online").Y+1 && box(tt, "header").H > m.Height())
}

func TestBubbleGeometryMetadataAndSlots(t *testing.T) {
	source := theme.Light()
	m := source.Metrics().Message()
	value := "Hello"
	group := Group{}
	selected := false
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		Bubble(c, Message{Text: value, SenderName: "Aoi", Info: Info{Label: "18:42", Delivery: Read}}, BubbleOptions{Side: Outgoing, Group: group, Selected: selected}, func() { ui.Box(c).Size(120, 20).Label("inserted content") }).Label("bubble")
	}, 600, 400)
	r := box(tt, "bubble")
	name := box(tt, "Aoi")
	inserted := box(tt, "inserted content")
	stamp := box(tt, "18:42")
	// chat.style:25,100; message.cpp:5433-5434: info right=11−2, bottom=8−5.
	verify(t, "bubble left padding", near(name.X-r.X, m.PaddingLeft()))
	verify(t, "bubble top padding", near(name.Y-r.Y, m.PaddingTop()))
	verify(t, "content insertion order", inserted.Y >= name.Y+name.H-1)
	verify(t, "metadata right inset", near(r.X+r.W-stamp.X-stamp.W, m.PaddingRight()-m.TimestampDeltaX()))
	verify(t, "metadata bottom inset", near(r.Y+r.H-stamp.Y-stamp.H, m.PaddingBottom()-m.TimestampDeltaY()))
	verify(t, "read receipt visible", box(tt, "Read").W > 0)
	value = strings.Repeat("long message ", 60)
	tt.Frame()
	verify(t, "maximum bubble width", near(box(tt, "bubble").W, m.MaxWidth()))
	group.Previous = true
	tt.Frame()
	verify(t, "group continuation hides name", !tt.HasText("Aoi"))
	selected = true
	tt.Frame()
	r = box(tt, "bubble")
	verify(t, "outgoing selection background", pixel(tt, r.X+r.W/2, r.Y+2) == rgba(source.Colors().Message().Out().BackgroundSelected()))
	tt.SetSize(240, 500)
	verify(t, "bubble shrinks to viewport", box(tt, "bubble").W <= 240)
}

func TestBottomInfoWrapsOnlyWhenNecessary(t *testing.T) {
	source := theme.Light()
	value := "Hello"
	width := float32(200)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		ui.Column(c).Width(width).Children(func() { Body(c, value, Info{Label: "18:42", Delivery: Sent}, BodyOptions{}).Label("body") })
	}, 450, 300)
	short := box(tt, "body").H
	verify(t, "short body shares timestamp line", box(tt, "18:42").Y < short)
	value = "A final line that occupies nearly the full available width"
	tt.Frame()
	wrapped := box(tt, "body").H
	verify(t, "wrapped body grows without info collision", wrapped > short && box(tt, "18:42").Y >= short)
	value = "مرحبا بالعالم"
	tt.Frame()
	verify(t, "rtl info has separate line", box(tt, "body").H > short)
	value = "Hello"
	width = 200
	tt.SetPreferences(ui.Preferences{TextScale: 1.5})
	tt.Frame()
	verify(t, "text scale expands body once", box(tt, "body").H > short && box(tt, "body").H < short*2)
}

func TestGroupedCornersAndTailPixels(t *testing.T) {
	source := theme.Light()
	m := source.Metrics().Message()
	for _, side := range []Side{Incoming, Outgoing} {
		for _, group := range []Group{{}, {Next: true}, {Previous: true, Next: true}, {Previous: true}} {
			t.Run(strings.Join([]string{string(rune('a' + side)), string(rune('a' + boolInt(group.Previous)*2 + boolInt(group.Next)))}, "/"), func(t *testing.T) {
				tt := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c, source)
					ui.Column(c).Padding(20).Background(source.Colors().Window().BackgroundHover()).Children(func() {
						Bubble(c, Message{}, BubbleOptions{Side: side, Group: group}, func() { ui.Box(c).Size(150, 50) }).Label("shell")
					})
				}, 300, 160)
				r := box(tt, "shell")
				x := r.X + 2
				if side == Outgoing {
					x = r.X + r.W - 3
				}
				bg := rgba(source.Colors().Message().In().Background())
				if side == Outgoing {
					bg = rgba(source.Colors().Message().Out().Background())
				}
				// message.cpp:6638-6649: attached top/bottom only on sending side.
				small := pixel(tt, x, r.Y+m.RadiusSmall()/2)
				verify(t, "attached top corner geometry", (small == bg) == group.Previous)
				tailLeft := r.X - m.TailWidth()
				if side == Outgoing {
					tailLeft = r.X + r.W
				}
				filled := false
				for y := int(r.Y + r.H - m.TailHeight() + m.ShadowHeight()); y < int(r.Y+r.H); y++ {
					for x := int(tailLeft); x < int(tailLeft+m.TailWidth()); x++ {
						filled = filled || pixel(tt, float32(x), float32(y)) == bg
					}
				}
				verify(t, "tail only at group end", filled == !group.Next)
			})
		}
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestRowGroupingAndIdentity(t *testing.T) {
	source := theme.Light()
	m := source.Metrics().Message()
	group := Group{}
	ids := []string{"one", "two"}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		for _, id := range ids {
			c.Key(id)
			Row(c, Message{Text: id, SenderName: "Aoi"}, RowOptions{BubbleOptions: BubbleOptions{Group: group}, ReserveAvatar: true}, func() {
				slot := ui.Column(c)
				n := ui.Local(slot, "clicks", func() int { return 0 })
				slot.Children(func() {
					ui.Button(c, id+" action").OnClick(func() { *n++ })
					ui.Text(c, fmt.Sprintf("%s clicks: %d", id, *n))
				})
			}).Label(id + " row")
		}
	}, 500, 350)
	first := box(tt, "one row")
	// message.cpp:6562-6565: bubble starts at msgMargin.left + msgPhotoSkip.
	verify(t, "sender body starts after avatar column", near(box(tt, "Aoi").X, m.MarginLeft()+m.AvatarSkip()+m.PaddingLeft()))
	click(t, tt, "two action")
	group.Previous = true
	tt.Frame()
	verify(t, "attached rows remove top gap", box(tt, "one row").H < first.H)
	verify(t, "regrouping preserves slot interaction state", tt.HasText("two clicks: 1"))
	ids = []string{"older", "one", "two"}
	tt.Frame()
	verify(t, "history prepend preserves local interaction state", tt.HasText("two clicks: 1") && tt.HasText("one clicks: 0"))
}

func TestServiceAndUnreadGeometry(t *testing.T) {
	source := theme.Light()
	m := source.Metrics().Service()
	var line float32
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		_, line = c.MeasureText(0, span(c, "Today", source.Typography().Service(), source.Colors().Service().Text()))
		DateMarker(c, "Today", DateOptions{}).Label("date")
		UnreadMarker(c, "Unread").Label("unread")
	}, 500, 240)
	// chat.style:95-96,353-355: natural line + 3/4 padding + 10/2 margins.
	verify(t, "date height", near(box(tt, "date").H, line+m.PaddingTop()+m.PaddingBottom()+m.MarginTop()+m.MarginBottom()))
	verify(t, "date centered", near(box(tt, "Today").X+box(tt, "Today").W/2, 250))
	verify(t, "unread total height", near(box(tt, "unread").H, m.UnreadHeight()+m.UnreadMargin()))
	verify(t, "unread baseline area", near(box(tt, "Unread").Y+box(tt, "Unread").H/2, box(tt, "unread").Y+m.UnreadMargin()+m.UnreadHeight()/2))
}

func TestLatestGeometryVisibilityAndActions(t *testing.T) {
	source := theme.Light()
	m := source.Metrics().Navigation()
	visible, unread, calls := true, 0, 0
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		ui.Column(c).Fill().Clip().Children(func() {
			LatestButton(c, LatestOptions{Visible: visible, Unread: unread, Label: "Latest", OnActivate: func() { calls++ }})
		})
	}, 400, 300)
	r := box(tt, "Latest")
	// chat_helpers.style:1195,1208-1218,1224.
	verify(t, "latest outer size", near(r.W, m.ToLatestWidth()) && near(r.H, m.ToLatestHeight()))
	verify(t, "latest viewport placement", near(400-r.X-r.W, m.ToLatestRight()) && near(300-r.Y-r.H, m.ToLatestBottom()))
	click(t, tt, "Latest")
	verify(t, "zero unread button activates", calls == 1)
	unread = 12345
	tt.Frame()
	verify(t, "unread badge appears", tt.HasText("12345"))
	// The click's independent 200 ms ripple must finish before measuring the 150 ms slide.
	tt.Advance(source.Motion().RippleHide().Duration())
	tt.Frame()
	visible = false
	tt.Frame()
	tt.Advance(source.Motion().ToLatest().Duration())
	tt.Frame()
	_, shown := tt.Find("Latest")
	verify(t, "latest hides at slide endpoint", !shown)
	verify(t, "latest animation settles", !tt.FramePending())
}

func TestFloatingDateTimeoutAndDayBoundary(t *testing.T) {
	source := theme.Light()
	var state DateState
	offset, bottom := float32(0), float32(0)
	var floating ui.Rect
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		ui.Column(c).Fill().Clip().Children(func() {
			FloatingDate(c, "Today", &state, FloatingDateOptions{ScrollOffset: offset, DayBottom: bottom}).Label("floating").Draw(func(_ *ui.Painter, r ui.Rect) { floating = r })
		})
	}, 400, 300)
	offset = 100
	tt.Frame()
	tt.Advance(source.Motion().DateFade().Duration())
	r := box(tt, "Today")
	on := pixel(tt, r.X+r.W/2, r.Y+1)
	verify(t, "scroll reveals floating date", on != rgba(source.Colors().Window().Background()))
	bottom = 15
	tt.Frame()
	verify(t, "day end pushes floating date upward", floating.Y < 0)
	bottom = 0
	tt.Frame()
	tt.Advance(source.Motion().DateHold().Duration() + source.Motion().DateFade().Duration())
	verify(t, "floating date stops after timeout", !tt.FramePending())
	r = box(tt, "Today")
	verify(t, "floating date fades away", pixel(tt, r.X+r.W/2, r.Y+1) == rgba(source.Colors().Window().Background()))
}

func TestMessageRevealClockAndRemount(t *testing.T) {
	source := theme.Light()
	var state RevealState
	show := true
	var start time.Time
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		start = c.Now()
		if show {
			Reveal(c, &state, RevealOptions{Height: 100}, func() { ui.Text(c, "New message") }).Label("reveal")
		}
	}, 300, 250)
	state.Start(start)
	tt.Frame()
	verify(t, "reveal starts collapsed", near(box(tt, "reveal").H, 0))
	tt.Advance(source.Motion().MessageReveal().Duration() / 2)
	tt.Frame() // Advance runs refreshes due before 75 ms; sample this exact clock time.
	verify(t, "reveal follows circular curve", near(box(tt, "reveal").H, float32(math.Sqrt(.75))*100))
	show = false
	tt.Frame()
	tt.Advance(source.Motion().MessageReveal().Duration())
	show = true
	tt.Frame()
	verify(t, "offscreen remount finishes without replay", near(box(tt, "reveal").H, 100))
	verify(t, "completed reveal stops frames", !tt.FramePending())
	state.Start(start)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	tt.Frame()
	verify(t, "reduced motion immediately opens row", near(box(tt, "reveal").H, 100) && !tt.FramePending())
}
