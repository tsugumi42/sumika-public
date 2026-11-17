package chat

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func identityPixels(r ui.Rect, scale float32) image.Rectangle {
	return image.Rect(int(math.Round(float64(r.X*scale))), int(math.Round(float64(r.Y*scale))), int(math.Round(float64((r.X+r.W)*scale))), int(math.Round(float64((r.Y+r.H)*scale))))
}

func identityInk(img *image.RGBA, r image.Rectangle, ink color.RGBA) image.Rectangle {
	var bounds image.Rectangle
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.RGBAAt(x, y) == ink {
				bounds = bounds.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return bounds
}

// White canvas corners are outside the circle, not initials foreground.
func identityAvatarInk(img *image.RGBA, r image.Rectangle, ink color.RGBA) image.Rectangle {
	var bounds image.Rectangle
	cx, cy := float64(r.Min.X+r.Max.X)/2, float64(r.Min.Y+r.Max.Y)/2
	radius := float64(min(r.Dx(), r.Dy()))/2 - 1
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			dx, dy := float64(x)+.5-cx, float64(y)+.5-cy
			if dx*dx+dy*dy < radius*radius && img.RGBAAt(x, y) == ink {
				bounds = bounds.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return bounds
}

func identityCrop(img *image.RGBA, r image.Rectangle) []byte {
	out := make([]byte, 0, r.Dx()*r.Dy()*4)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		i := img.PixOffset(r.Min.X, y)
		out = append(out, img.Pix[i:i+r.Dx()*4]...)
	}
	return out
}

// Compare the whole rendered message outside its name, including inline objects
// and date ink; RichText's accessible text also contains its reservation object.
func identityWithout(img *image.RGBA, excluded image.Rectangle) []byte {
	out := append([]byte(nil), img.Pix...)
	excluded = excluded.Intersect(img.Bounds())
	for y := excluded.Min.Y; y < excluded.Max.Y; y++ {
		start := img.PixOffset(excluded.Min.X, y)
		clear(out[start : start+excluded.Dx()*4])
	}
	return out
}

// A regular Text centered by flex layout is independent of the custom painter.
// Glyph ink is asymmetric inside its line box and need not share its center.
func identityReference(c *ui.Context, source *theme.Theme, background ui.Color) ui.Element {
	m, style := source.Metrics().Message(), source.Typography().MessageAvatar()
	return ui.Row(c).Size(m.AvatarSize(), m.AvatarSize()).AlignItems(ui.Center).
		Background(background).Radius(m.AvatarSize() / 2).Label("initials reference").Children(func() {
		ui.Spacer(c)
		ui.Text(c, "H").Font(source.Typography().Font()).FontSize(style.Size()).FontWeight(style.Weight()).TextColor(source.Colors().Avatar().UserpicForeground())
		ui.Spacer(c)
	})
}

func identityMatchesReference(tt *ui.Tester, avatar ui.Rect, scale float32, foreground ui.Color) bool {
	actualRect := identityPixels(avatar, scale)
	referenceRect := identityPixels(box(tt, "initials reference"), scale)
	actual := identityAvatarInk(tt.Image(), actualRect, rgba(foreground)).Sub(actualRect.Min)
	reference := identityAvatarInk(tt.Image(), referenceRect, rgba(foreground)).Sub(referenceRect.Min)
	return !actual.Empty() && !reference.Empty() &&
		near(float32(actual.Min.X-reference.Min.X)/scale, 0) && near(float32(actual.Min.Y-reference.Min.Y)/scale, 0) &&
		near(float32(actual.Max.X-reference.Max.X)/scale, 0) && near(float32(actual.Max.Y-reference.Max.Y)/scale, 0)
}

// paint/chat-identity.md#圆与首字: 33 DIP circle, centered semibold initials,
// stable geometry through missing/loading/failed -> ready -> missing transitions.
func TestIdentityAvatarResourcesAndGeometry(t *testing.T) {
	for _, source := range []*theme.Theme{theme.Light(), theme.Dark()} {
		for _, scale := range []float32{1, 1.25, 1.5} {
			t.Run(fmt.Sprintf("dark=%t/scale=%.2f", source.Dark(), scale), func(t *testing.T) {
				m := source.Metrics().Message()
				hostColor := source.Colors().Avatar().Peer1UserpicBackground()
				message := Message{Text: "Prepared message", SenderName: "Sender", AvatarInitials: "H", AvatarLabel: "Complete sender identity", AvatarColor: hostColor}
				photo := image.NewRGBA(image.Rect(0, 0, 8, 8))
				photoColor := color.RGBA{13, 27, 99, 255}
				draw.Draw(photo, photo.Bounds(), image.NewUniform(photoColor), image.Point{}, draw.Src)
				tt := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c, source)
					ui.Column(c).Fill().Background(source.Colors().Window().Background()).Children(func() {
						ui.Row(c).FillWidth().AlignItems(ui.End).Gap(m.AvatarLeft()).Children(func() {
							Row(c.Key("message"), message, RowOptions{ReserveAvatar: true}, nil).Width(400).Label("row")
							identityReference(c.Key("reference"), source, message.AvatarColor).Margin(0, 0, m.MarginBottom())
						})
					})
				}, 500, 200)
				tt.SetScale(scale)
				row := box(tt, "row")
				avatar := ui.Rect{X: m.AvatarLeft(), Y: row.Y + row.H - m.MarginBottom() - m.AvatarSize(), W: m.AvatarSize(), H: m.AvatarSize()}
				pr := identityPixels(avatar, scale)
				img := tt.Image()
				verify(t, "initials match independently centered text", identityMatchesReference(tt, avatar, scale, source.Colors().Avatar().UserpicForeground()))
				fill := identityInk(img, pr, rgba(hostColor))
				verify(t, "sourced avatar diameter and left", near(float32(fill.Dx())/scale, m.AvatarSize()) && near(float32(fill.Dy())/scale, m.AvatarSize()) && near(float32(fill.Min.X)/scale, m.AvatarLeft()))
				column, found := tt.Find(message.AvatarLabel)
				verify(t, "full accessible name has avatar column bounds", found && near(column.W, m.AvatarSkip()+m.MarginLeft()-m.AvatarLeft()) && near(column.H, row.H-m.MarginTop()-m.MarginBottom()))
				fallback := identityCrop(img, pr)
				for _, state := range []string{"loading", "failed"} {
					message.AvatarLabel = state + " full sender name"
					tt.Frame()
					verify(t, "loading and failure preserve initials pixels and row size", bytes.Equal(fallback, identityCrop(tt.Image(), pr)) && box(tt, "row") == row)
				}
				message.Avatar = ui.NewBitmap(photo)
				tt.Frame()
				verify(t, "ready bitmap replaces initials without changing row", tt.Image().RGBAAt((pr.Min.X+pr.Max.X)/2, (pr.Min.Y+pr.Max.Y)/2) == photoColor && box(tt, "row") == row)
				message.Avatar = nil
				tt.Frame()
				verify(t, "lost bitmap restores exact initials", bytes.Equal(fallback, identityCrop(tt.Image(), pr)))
				message.AvatarColor = source.Colors().Avatar().Peer5UserpicBackground()
				tt.Frame()
				verify(t, "host recolor preserves initials geometry", tt.Image().RGBAAt(pr.Min.X+int(4*scale), (pr.Min.Y+pr.Max.Y)/2) == rgba(message.AvatarColor) && identityMatchesReference(tt, avatar, scale, source.Colors().Avatar().UserpicForeground()))
			})
		}
	}
}

// paint/chat-identity.md#组尾与视口: initials use the same moving rectangle as
// photos, including local painter origins inside cached panes.
func TestIdentityInitialsFollowVisibleGroup(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprint(cached), func(t *testing.T) {
			source := theme.Light()
			m := source.Metrics().Message()
			top := float32(40)
			hostColor := source.Colors().Avatar().Peer1UserpicBackground()
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, source)
				pane := ui.Box(c).Absolute().Left(20).Top(top).Size(400, 180).Clip()
				if cached {
					pane.Cache()
				}
				pane.Children(func() {
					Row(c, Message{AvatarInitials: "H", AvatarLabel: "Visible group sender", AvatarColor: hostColor}, RowOptions{ReserveAvatar: true, AvatarGroupTop: &top, BubbleOptions: BubbleOptions{Group: Group{Next: true}}}, func() { ui.Box(c).Size(100, 300) })
					identityReference(c.Key("reference"), source, hostColor).Absolute().Left(m.AvatarLeft() + 240).Top(180 - m.HistoryBottom() - m.MarginBottom() - m.AvatarSize())
				})
			}, 500, 300)
			tt.SetScale(1.25)
			bottom := top + 180 - m.HistoryBottom() - m.MarginBottom()
			r := ui.Rect{X: 20 + m.AvatarLeft(), Y: bottom - m.AvatarSize(), W: m.AvatarSize(), H: m.AvatarSize()}
			verify(t, "clipped continuation initials follow viewport", identityMatchesReference(tt, r, 1.25, source.Colors().Avatar().UserpicForeground()))
			fill := identityInk(tt.Image(), identityPixels(ui.Rect{X: 20, Y: top, W: 80, H: 180}, 1.25), rgba(hostColor))
			verify(t, "viewport avatar bottom and diameter", near(float32(fill.Max.Y)/1.25, bottom) && near(float32(fill.Dy())/1.25, m.AvatarSize()))
		})
	}
}

func identityForeground(img *image.RGBA, r image.Rectangle, background color.RGBA) color.RGBA {
	counts := map[color.RGBA]int{}
	var result color.RGBA
	best := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c == background {
				continue
			}
			counts[c]++
			if counts[c] > best {
				best, result = counts[c], c
			}
		}
	}
	return result
}

// paint/chat-identity.md#发送人名字: both public entry points preserve per-message
// override precedence and keep body/time roles, private rows and groups intact.
func TestIdentitySenderNameColor(t *testing.T) {
	for _, source := range []*theme.Theme{theme.Light(), theme.Dark()} {
		for _, side := range []Side{Incoming, Outgoing} {
			for _, selected := range []bool{false, true} {
				for _, row := range []bool{false, true} {
					t.Run(fmt.Sprintf("dark=%t/side=%d/selected=%t/row=%t", source.Dark(), side, selected, row), func(t *testing.T) {
						message := Message{Text: "Body H", SenderName: "Sender H", Info: Info{Label: "12:34"}}
						options := BubbleOptions{Side: side, Selected: selected}
						tt := ui.NewTester(func(c *ui.Context) {
							theme.Apply(c, source)
							if row {
								Row(c, message, RowOptions{BubbleOptions: options}, nil).FillWidth().Label("shell")
							} else {
								Bubble(c, message, options, nil).Label("shell")
							}
						}, 400, 200)
						tt.SetScale(1.25)
						bg, _, _, name, _ := messageColors(source, side, selected)
						foreground := func(label string) color.RGBA {
							return identityForeground(tt.Image(), identityPixels(box(tt, label), 1.25), rgba(bg))
						}
						verify(t, "unspecified sender color uses direction and selection", foreground(message.SenderName) == rgba(name))
						height := box(tt, "shell").H
						namePixels := identityPixels(box(tt, message.SenderName), 1.25).Inset(-1)
						otherPixels := identityWithout(tt.Image(), namePixels)
						message.SenderNameColor = source.Colors().Avatar().Peer1NameForeground()
						tt.Frame()
						verify(t, "per-message name color wins in both shells", foreground(message.SenderName) == rgba(message.SenderNameColor) && box(tt, "shell").H == height)
						verify(t, "name override leaves body and date colors", bytes.Equal(otherPixels, identityWithout(tt.Image(), namePixels)))
						message.SenderNameColor = ui.Color{}
						tt.Frame()
						verify(t, "clearing name override restores theme", foreground(message.SenderName) == rgba(name))
						options.Group.Previous = true
						message.SenderNameColor = source.Colors().Avatar().Peer1NameForeground()
						tt.Frame()
						verify(t, "continuation suppresses colored sender", !tt.HasText(message.SenderName))
						groupHeight := box(tt, "shell").H
						options.Group.Previous = false
						message.SenderName = ""
						tt.Frame()
						margin := float32(0)
						if row {
							margin = source.Metrics().Message().MarginTop() - source.Metrics().Message().AttachedTop()
						}
						verify(t, "private message color creates no name line", near(box(tt, "shell").H, groupHeight+margin))
					})
				}
			}
		}
	}
}

func TestIdentityColorsArePerMessage(t *testing.T) {
	source := theme.Dark()
	colors := make([]ui.Color, 3)
	names := []string{"First H", "Second H", "Theme H"}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		ui.Column(c).Fill().Children(func() {
			for i, name := range names {
				Row(c.Key(i), Message{Text: "Body", SenderName: name, SenderNameColor: colors[i]}, RowOptions{}, nil).FillWidth()
			}
		})
	}, 400, 300)
	tt.SetScale(1.25)
	for i, next := range []ui.Color{source.Colors().Avatar().Peer1NameForeground(), source.Colors().Avatar().Peer5NameForeground()} {
		namePixels := identityPixels(box(tt, names[i]), 1.25).Inset(-1)
		beforeName := identityCrop(tt.Image(), namePixels)
		beforeOthers := identityWithout(tt.Image(), namePixels)
		colors[i] = next
		tt.Frame()
		verify(t, "adjacent sender colors do not leak", !bytes.Equal(beforeName, identityCrop(tt.Image(), namePixels)) && bytes.Equal(beforeOthers, identityWithout(tt.Image(), namePixels)))
	}
}
