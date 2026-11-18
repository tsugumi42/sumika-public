package dialogs_test

import (
	"bytes"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestTTLCropFollowsPreparedAvatarPresentation(t *testing.T) {
	square := ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><rect width="24" height="24"/></svg>`))
	circle := ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><circle cx="12" cy="12" r="3"/></svg>`))
	item := dialogs.Conversation{ID: "ttl", Name: "TTL", AvatarIcon: square, TTL: "1d"}
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		services = c.Services()
		dialogs.Row(c, item, dialogs.RowOptions{})
	}, 360, 100)
	snapshot := func() []byte {
		r := found(tt, "TTL badge")
		img := tt.Image()
		result := make([]byte, 0, int(r.W*r.H)*4)
		cx, cy := int(r.X+r.W/2), int(r.Y+r.H/2)
		// Exclude transparent corners and antialiased edges: those reveal the
		// changing avatar below even when the TTL crop itself is stale.
		radius := int(r.W/2-theme.Light().Metrics().Conversation().OnlineStroke()) - 1
		for y := cy - radius; y <= cy+radius; y++ {
			for x := cx - radius; x <= cx+radius; x++ {
				dx, dy := x-cx, y-cy
				if dx*dx+dy*dy <= radius*radius {
					start := y*img.Stride + x*4
					result = append(result, img.Pix[start:start+4]...)
				}
			}
		}
		return result
	}
	before := snapshot()
	item.AvatarIcon = circle
	services.Invalidate()
	tt.Frame()
	check(t, "ttl crop follows prepared icon", !bytes.Equal(before, snapshot()))
	before = snapshot()
	item.AvatarIcon = nil
	item.Kind = dialogs.Archive
	services.Invalidate()
	tt.Frame()
	check(t, "ttl crop follows fallback palette", !bytes.Equal(before, snapshot()))
}

func TestSubscriptionBadgePaintsForeground(t *testing.T) {
	th := theme.Light()
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		dialogs.Row(c, dialogs.Conversation{ID: "subscribed", Name: "Subscriber", Subscribed: true}, dialogs.RowOptions{})
	}, 360, 100)
	r := found(tt, "Subscription badge")
	check(t, "subscription paints foreground", pixel(tt, int(r.X+r.W/2), int(r.Y+r.H/2)) == th.Colors().Credits().Background1())
}
