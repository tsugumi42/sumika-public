package content

import (
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
	"github.com/tsugumi42/MewTypeUI/theme"
)

var stickerSelectionShader = sync.OnceValue(func() *shader.Shader {
	return shader.MustCompile(`package main
var Overlay vec4
func Fragment(dst vec4,pos vec2)vec4 {
 size:=image0Size()
 scale:=min(rectSize().x/size.x,rectSize().y/size.y)
 offset:=(rectSize()-size*scale)/2
 sample:=image0Sample((pos-offset)/scale)
 return vec4(sample.rgb*(1-Overlay.a)+Overlay.rgb*Overlay.a*sample.a,sample.a)
}
`)
})

type stickerSelectionKey struct{}
type stickerSelection struct {
	values *shader.Values
	color  ui.Color
}

// Tint the bitmap's occupied pixels. A rectangle overlay would color transparent
// sticker margins and create a bubble where Telegram leaves the background clear.
func selectedSticker(c *ui.Context, bitmap *ui.Bitmap) ui.Element {
	program := stickerSelectionShader()
	e := ui.Box(c).Absolute().Fill()
	state := ui.Local(e, stickerSelectionKey{}, func() stickerSelection { return stickerSelection{values: program.NewValues()} })
	color := theme.T(c).Colors().Message().StickerOverlay()
	if state.color != color {
		state.values.Color(program.Uniform("Overlay"), color)
		state.color = color
	}
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		program.Draw(p, r, shader.Options{Values: state.values, Images: [4]*ui.Bitmap{bitmap}})
	})
	return e
}
