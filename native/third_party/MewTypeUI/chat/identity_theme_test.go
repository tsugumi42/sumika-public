package chat

import (
	"fmt"
	"image/color"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// The independent Text widget is the consumer's typography reference. This
// catches stale shaping, wrong roles and double TextScale in the custom painter.
func TestIdentityAvatarThemeAndTextScale(t *testing.T) {
	for _, textScale := range []float32{1, 1.5} {
		t.Run(fmt.Sprint(textScale), func(t *testing.T) {
			source := theme.Light()
			// paint/chat-identity.md#圆与首字: independent 13 DIP/600 reference.
			referenceSize, referenceWeight := float32(13), 600
			message := Message{Text: "Body", AvatarInitials: "H", AvatarLabel: "Sender initials"}
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, source)
				ui.Column(c).Fill().Children(func() {
					Row(c, message, RowOptions{ReserveAvatar: true}, nil).FillWidth().Label("row")
					ui.Text(c, "H").Font(source.Typography().Font()).FontSize(referenceSize * textScale).FontWeight(referenceWeight).TextColor(source.Colors().Avatar().UserpicForeground()).Background(source.Colors().Avatar().Peer4UserpicBackground()).Label("typography reference")
				})
			}, 400, 200)
			tt.SetScale(1.25)
			tt.SetPreferences(ui.Preferences{TextScale: textScale})
			for i, variant := range []struct {
				size   float32
				weight int
			}{{13, 600}, {19, 700}} {
				referenceSize, referenceWeight = variant.size, variant.weight
				if i > 0 {
					definition := source.Definition()
					definition.Typography["messageAvatar"] = theme.TextDefinition{Size: new(variant.size), Weight: new(variant.weight)}
					var err error
					source, err = theme.Compile(definition)
					if err != nil {
						t.Fatal(err)
					}
				}
				tt.Frame()
				m := source.Metrics().Message()
				row := box(tt, "row")
				avatar := ui.Rect{X: m.AvatarLeft(), Y: row.Y + row.H - m.MarginBottom() - m.AvatarSize(), W: m.AvatarSize(), H: m.AvatarSize()}
				img := tt.Image()
				ink := identityAvatarInk(img, identityPixels(avatar, 1.25), rgba(source.Colors().Avatar().UserpicForeground()))
				reference := identityInk(img, identityPixels(box(tt, "typography reference"), 1.25), rgba(source.Colors().Avatar().UserpicForeground()))
				verify(t, "avatar uses independent theme font and one text scale", near(float32(ink.Dx())/1.25, float32(reference.Dx())/1.25) && near(float32(ink.Dy())/1.25, float32(reference.Dy())/1.25) && !reference.Empty())
				// empty_userpic.cpp:306-310: endpoints progress vertically across D.
				for _, fraction := range []float32{.25, .75} {
					x, y := int((avatar.X+4)*1.25), int((avatar.Y+avatar.H*fraction)*1.25)
					want := source.Colors().Avatar().Peer4UserpicBackground().Mix(source.Colors().Avatar().Peer4UserpicBackground2(), fraction)
					got := img.RGBAAt(x, y)
					verify(t, "default initials background follows vertical palette gradient", identityColorNear(got, rgba(want)))
				}
			}
		})
	}
}

func identityColorNear(a, b color.RGBA) bool {
	for _, pair := range [][2]uint8{{a.R, b.R}, {a.G, b.G}, {a.B, b.B}, {a.A, b.A}} {
		if difference := int(pair[0]) - int(pair[1]); difference < -2 || difference > 2 {
			return false
		}
	}
	return true
}
