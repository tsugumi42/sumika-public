package fidelity

import (
	"image"
	"math"
)

// Composite maps reference physical pixels into a scene of the same output
// pixel grid. Outside the reference footprint, the original scene is retained.
// Pixel centers and bilinear sampling match gallery's LayerEffect mapping.
func Composite(ours, reference *image.RGBA, scale, x, y, zoom, alpha float64, difference bool) *image.RGBA {
	out := image.NewRGBA(ours.Bounds())
	for py := range out.Bounds().Dy() {
		for px := range out.Bounds().Dx() {
			a := ours.RGBAAt(px, py)
			qx := (float64(px) + .5 - x*scale) / zoom
			qy := (float64(py) + .5 - y*scale) / zoom
			if qx < 0 || qy < 0 || qx >= float64(reference.Bounds().Dx()) || qy >= float64(reference.Bounds().Dy()) {
				out.SetRGBA(px, py, a)
				continue
			}
			sx, sy := qx-.5, qy-.5
			ix, iy := int(math.Floor(sx)), int(math.Floor(sy))
			fx, fy := sx-math.Floor(sx), sy-math.Floor(sy)
			var b [4]float64
			for j := range 2 {
				for i := range 2 {
					c := reference.RGBAAt(min(max(ix+i, 0), reference.Bounds().Dx()-1), min(max(iy+j, 0), reference.Bounds().Dy()-1))
					weight := []float64{1 - fx, fx}[i] * []float64{1 - fy, fy}[j]
					b[0] += float64(c.R) * weight
					b[1] += float64(c.G) * weight
					b[2] += float64(c.B) * weight
					b[3] += float64(c.A) * weight
				}
			}
			offset := out.PixOffset(px, py)
			v := [3]uint8{a.R, a.G, a.B}
			for k := range 3 {
				value := float64(v[k])*(1-alpha*b[3]/255) + b[k]*alpha*b[3]/255
				if difference {
					value = math.Abs(float64(v[k]) - b[k])
				}
				out.Pix[offset+k] = uint8(math.Round(value))
			}
			out.Pix[offset+3] = 255
		}
	}
	return out
}
