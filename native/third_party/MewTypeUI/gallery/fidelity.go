package gallery

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// Comparison is host-owned calibration. Offsets are page-local DIP; Scale is
// device pixels per reference DIP, independent of the current monitor's DPI.
type Comparison struct {
	Mode                     string
	X, Y, Zoom, Scale, Alpha float64
	effect                   ui.Effect
	values                   *shader.Values
}

// Tool-only composition: docs/reference/exceptions.md#本地叠图验收工具fidelity.
// Sampling uses page-local DIP; it does not replace component paint contracts.
var comparisonShader = shader.MustCompile(`package main
var Offset vec2
var Ratio float
var Alpha float
var Difference float
func LayerEffect(pos vec2) vec4 {
 a := layerAt(pos)
 q := (pos-Offset)*Ratio
 size := image0Size()
 if q.x < 0 || q.y < 0 || q.x >= size.x || q.y >= size.y { return a }
 b := image0Sample(q)
 if Difference > 0 { return vec4(abs(a.rgb-b.rgb), 1) }
 return mix(a,b,Alpha*b.a)
}`)
var comparisonOffset = comparisonShader.Uniform("Offset")
var comparisonRatio = comparisonShader.Uniform("Ratio")
var comparisonAlpha = comparisonShader.Uniform("Alpha")
var comparisonDifference = comparisonShader.Uniform("Difference")

func NewComparison() Comparison {
	return Comparison{Mode: "alpha", Zoom: 1, Scale: 1.25, Alpha: .5}
}

func (s *Comparison) prepare(reference *ui.Bitmap) ui.Effect {
	if s.values == nil {
		s.values = comparisonShader.NewValues()
	}
	s.values.Vec2(comparisonOffset, float32(s.X), float32(s.Y)).Float(comparisonRatio, float32(s.Scale/s.Zoom)).Float(comparisonAlpha, float32(s.Alpha))
	diff := float32(0)
	if s.Mode == "difference" {
		diff = 1
	}
	s.values.Float(comparisonDifference, diff)
	// Effect stores the bitmap and Values pointer; recreate only when loading a
	// reference. Values are copied by the renderer on each paint.
	if s.effect == nil {
		s.effect = comparisonShader.Effect(shader.Options{Values: s.values, Images: [4]*ui.Bitmap{reference}})
	}
	return s.effect
}

func (a *App) SetReference(bitmap *ui.Bitmap) {
	a.Reference = bitmap
	a.Comparison.effect = nil
	a.Compare = false
	a.Overlay = true
}

func (a *App) comparisonControls(c *ui.Context) {
	m := theme.T(c).Metrics()
	ui.Column(c.Key("comparison-controls")).FillWidth().MinWidth(0).Gap(m.Control().Spacing()).Children(func() {
		ui.Row(c).FillWidth().MinWidth(0).Wrap().Gap(m.Header().ActionGap()).Children(func() {
			for _, mode := range []struct{ id, label string }{{"alpha", "半透明"}, {"difference", "差异"}} {
				ui.Button(c.Key(mode.id), mode.label).OnClick(func() { a.Comparison.Mode = mode.id; a.Overlay = true; a.Compare = false })
			}
			ui.Checkbox(c, &a.Overlay, "叠图")
			ui.Checkbox(c, &a.Compare, "并排对照")
			ui.Button(c, "复位对齐").OnClick(func() { a.Comparison = NewComparison() })
			ui.Textf(c, "x %.2f / y %.2f DIP · %.3f× · %.3f px/DIP", a.Comparison.X, a.Comparison.Y, a.Comparison.Zoom, a.Comparison.Scale)
		})
		w, h := c.Size()
		ui.Row(c).FillWidth().MinWidth(0).Gap(m.Header().ActionGap()).Children(func() {
			ui.Text(c, "X")
			ui.Slider(c.Key("offset-x"), &a.Comparison.X, -float64(w), float64(w)).Step(1 / a.Comparison.Scale).Grow(1).Basis(0).MinWidth(0).Label("参考图水平偏移")
			ui.Text(c, "Y")
			ui.Slider(c.Key("offset-y"), &a.Comparison.Y, -float64(h), float64(h)).Step(1 / a.Comparison.Scale).Grow(1).Basis(0).MinWidth(0).Label("参考图垂直偏移")
			ui.Text(c, "缩放")
			ui.Slider(c.Key("reference-zoom"), &a.Comparison.Zoom, .25, 4).Step(.001).Grow(1).Basis(0).MinWidth(0).Label("参考图缩放")
			ui.Text(c, "比例")
			ui.Slider(c.Key("reference-scale"), &a.Comparison.Scale, 1, 2).Step(.01).Grow(1).Basis(0).MinWidth(0).Label("参考图物理像素比例")
			ui.Text(c, "透明度")
			ui.Slider(c.Key("reference-alpha"), &a.Comparison.Alpha, 0, 1).Step(.01).Grow(1).Basis(0).MinWidth(0).Label("参考图透明度")
		})
	})
}
