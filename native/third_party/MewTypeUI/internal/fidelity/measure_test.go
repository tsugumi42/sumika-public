package fidelity

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
)

func TestCompositeAlignmentAndDifference(t *testing.T) {
	ours := image.NewRGBA(image.Rect(0, 0, 5, 1))
	draw.Draw(ours, ours.Bounds(), image.NewUniform(color.RGBA{20, 40, 60, 255}), image.Point{}, draw.Src)
	reference := image.NewRGBA(image.Rect(0, 0, 2, 1))
	reference.SetRGBA(0, 0, color.RGBA{100, 120, 140, 255})
	reference.SetRGBA(1, 0, color.RGBA{180, 200, 220, 255})
	cases := []struct {
		name           string
		x, zoom, alpha float64
		diff           bool
		pixel          int
		want           color.RGBA
	}{
		{"blend", 0, 1, .5, false, 0, color.RGBA{60, 80, 100, 255}},
		{"difference", 0, 1, .5, true, 1, color.RGBA{160, 160, 160, 255}},
		{"physical-offset", .8, 1, 1, false, 0, color.RGBA{20, 40, 60, 255}},
		{"translated-pixel", .8, 1, 1, false, 1, color.RGBA{100, 120, 140, 255}},
		{"negative-offset", -.8, 1, 1, false, 0, color.RGBA{180, 200, 220, 255}},
		{"scaled-sampling", 0, 2, 1, false, 1, color.RGBA{120, 140, 160, 255}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Composite(ours, reference, 1.25, c.x, 0, c.zoom, c.alpha, c.diff).RGBAAt(c.pixel, 0)
			if got != c.want {
				t.Fatalf("pixel: got %v, want %v", got, c.want)
			}
		})
	}
}

func TestSurfaceKeepsDisconnectedColorsSeparate(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 12, 8))
	draw.Draw(img, image.Rect(1, 1, 5, 6), image.NewUniform(color.RGBA{70, 120, 180, 255}), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(7, 1, 11, 6), image.NewUniform(color.RGBA{70, 120, 180, 255}), image.Point{}, draw.Src)
	img.SetRGBA(2, 2, color.RGBA{255, 255, 255, 255})
	sample, err := Surface(img, img.Bounds(), image.Pt(1, 1), 0)
	if err != nil {
		t.Fatal(err)
	}
	if sample.Bounds != image.Rect(1, 1, 5, 6) || sample.Pixels != 19 || sample.Clipped {
		t.Fatalf("disconnected surface: %+v", sample)
	}
}

func TestInkReportsForegroundColor(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 30, 10))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{255, 255, 255, 255}), image.Point{}, draw.Src)
	ink := color.RGBA{120, 130, 140, 255}
	draw.Draw(img, image.Rect(3, 3, 10, 5), image.NewUniform(ink), image.Point{}, draw.Src)
	img.SetRGBA(25, 3, color.RGBA{245, 245, 245, 255})
	img.SetRGBA(11, 3, color.RGBA{180, 180, 180, 255})
	sample, err := Ink(img, img.Bounds(), 24)
	if err != nil {
		t.Fatal(err)
	}
	if sample.Color != ink {
		t.Fatalf("dominant ink: got %v, want %v", sample.Color, ink)
	}
}
