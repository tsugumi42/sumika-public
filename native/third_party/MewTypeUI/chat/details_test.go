package chat

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/compose"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
)

// history_inner_widget.cpp:1084-1106: group end and bottom-most visible row.
func TestGroupEndAndViewportAvatar(t *testing.T) {
	source := theme.Light()
	m := source.Metrics().Message()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		ui.Column(c).Fill().Children(func() {
			Row(c, Message{Text: "first"}, RowOptions{BubbleOptions: BubbleOptions{Group: Group{Next: true}}, ReserveAvatar: true}, nil).Label("first row")
			Row(c, Message{Text: "last"}, RowOptions{BubbleOptions: BubbleOptions{Group: Group{Previous: true}}, ReserveAvatar: true}, nil).Label("last row")
		})
	}, 500, 300)
	image := tt.Image()
	first, last := box(tt, "first row"), box(tt, "last row")
	x := int(m.AvatarLeft() + m.AvatarSize()/2)
	want := rgba(source.Colors().Avatar().Peer4UserpicBackground())
	verify(t, "group first has no avatar", image.RGBAAt(x, int(first.Y+first.H-m.MarginBottom()-m.AvatarSize()/2)) != want)
	verify(t, "group last aligns avatar to bubble bottom", image.RGBAAt(x, int(last.Y+last.H-m.MarginBottom()-m.AvatarSize()/2)) == want)
	top := float32(0)
	clipped := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		ui.Box(c).Size(500, 180).Clip().Children(func() {
			Row(c, Message{}, RowOptions{BubbleOptions: BubbleOptions{Group: Group{Next: true}}, ReserveAvatar: true, AvatarGroupTop: &top}, func() { ui.Box(c).Size(100, 300) })
		})
	}, 500, 300)
	avatarY := 180 - m.HistoryBottom() - m.MarginBottom() - m.AvatarSize()/2
	verify(t, "unfinished group follows viewport bottom", clipped.Image().RGBAAt(x, int(avatarY)) == want)
	top = 150
	cached := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		ui.Box(c).Size(460, 180).Absolute().Left(20).Top(top).Cache().Clip().Children(func() {
			Row(c, Message{}, RowOptions{BubbleOptions: BubbleOptions{Group: Group{Next: true}}, ReserveAvatar: true, AvatarGroupTop: &top}, func() { ui.Box(c).Size(100, 300) })
		})
	}, 500, 400)
	verify(t, "cached group anchor remains in window coordinates", cached.Image().RGBAAt(x+20, int(top+avatarY)) == want)
}

// message_sending_text_flight.cpp:39-44,159-160,735-741,803-830.
func TestAcceptedDraftSnapshotFlight(t *testing.T) {
	source := theme.Light()
	var capture DraftCapture
	var flight SendingState
	var field ui.Handle
	draft := ""
	sent := false
	fieldColor := ui.RGB(220, 50, 30)
	destination := ui.RGB(20, 80, 220)
	targetX := float32(240)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		ui.Box(c).Fill().Background(ui.RGB(255, 255, 255)).Children(func() {
			if sent {
				SendingTarget(c, &flight, func() { ui.Box(c).Size(100, 40).Background(destination) }).Absolute().Left(targetX).Top(30).Size(100, 40)
			}
			CaptureDraft(c, &capture, func() {
				compose.Field(c, &draft, compose.FieldOptions{Label: "Draft", Handle: &field, OnSubmit: func(string) {
					flight.Start(capture.Take(c, &field))
					sent = true
					draft = ""
					fieldColor = ui.RGB(30, 220, 50)
				}}).Size(160, 40).Background(fieldColor)
			}).Absolute().Left(20).Top(300).Width(160)
		})
		SendingFlight(c, &flight)
	}, 400, 400)
	click(t, tt, "Draft")
	tt.Type("Snapshot ink")
	tt.Image()
	tt.Key(0, ui.KeyEnter)
	verify(t, "accepted send clears draft and retains field focus", sent && draft == "" && tt.Focused("Draft"))
	image := tt.Image()
	verify(t, "source snapshot survives clearing editor", image.RGBAAt(22, 302).R > 180 && image.RGBAAt(22, 302).G < 100)
	tt.Advance(source.Motion().SendingFlight().Duration() / 2)
	tt.Frame()
	y := 300 + (30-300)*sendingRise(.5)
	image = tt.Image()
	verify(t, "flight reaches sourced rise and horizontal position", image.RGBAAt(int(targetX+50), int(y+20)).B > 180)
	targetX = 200
	tt.Frame()
	image = tt.Image()
	verify(t, "flight tracks live destination geometry", image.RGBAAt(int(targetX+50), int(y+20)).B > 180)
	tt.Advance(source.Motion().SendingFlight().Duration() / 2)
	tt.Frame()
	verify(t, "flight completes at destination without pending frames", !flight.active && tt.Image().RGBAAt(int(targetX+50), 50).B > 180)
}

func TestSendingInsideCachedPane(t *testing.T) {
	var capture DraftCapture
	var flight SendingState
	var field ui.Handle
	draft := ""
	sent := false
	color := ui.RGB(220, 50, 30)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		ui.Box(c).Size(340, 360).Absolute().Left(40).Top(20).Cache().Children(func() {
			if sent {
				SendingTarget(c, &flight, func() { ui.Box(c).Size(100, 40).Background(ui.RGB(20, 80, 220)) }).Absolute().Left(200).Top(30).Size(100, 40)
			}
			CaptureDraft(c, &capture, func() {
				compose.Field(c, &draft, compose.FieldOptions{Label: "Cached draft", Handle: &field, OnSubmit: func(string) {
					flight.Start(capture.Take(c, &field))
					sent = true
					draft = ""
					color = ui.RGB(30, 220, 50)
				}}).Size(160, 40).Background(color)
			}).Absolute().Left(20).Top(260).Width(160)
		})
		SendingFlight(c, &flight)
	}, 400, 400)
	click(t, tt, "Cached draft")
	tt.Type("Nested snapshot")
	tt.Image()
	tt.Key(0, ui.KeyEnter)
	verify(t, "cached parent preserves source window coordinates", tt.Image().RGBAAt(62, 282).R > 180)
	tt.Advance(2 * theme.Light().Motion().SendingFlight().Duration())
	verify(t, "paint-only deadline releases flight state", !flight.active)
	tt.Frame()
	verify(t, "cached parent preserves destination window coordinates", tt.Image().RGBAAt(290, 70).B > 180)
	verify(t, "completed flight releases both snapshot references", flight.source.image == nil && flight.target.image == nil)
}
