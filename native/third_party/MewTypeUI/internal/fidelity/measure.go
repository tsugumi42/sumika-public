// Package fidelity measures local reference regions without extracting text.
package fidelity

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

// Region is a manually calibrated ROI. Surface uses a seed and a flood fill;
// Ink measures pixels that differ from the dominant background within the ROI.
// Neither reads, recognizes or records textual content.
type Region struct {
	Name, Component, Spec, Classification string
	Method                                string
	Reference                             image.Rectangle
	Seed                                  image.Point
	OwnSeed                               image.Point // physical offset from the observed component's top left
	Label                                 string
	Axes                                  string
}

type Sample struct {
	Bounds  image.Rectangle
	Color   color.RGBA // Seed fill for Surface; dominant contrasting foreground for Ink.
	Pixels  int
	Clipped bool
}

// Surface finds the four-connected flat fill at seed. Tolerance applies per RGB
// channel, in encoded sRGB bytes; no gamma/DeltaE claim is made.
func Surface(img *image.RGBA, scope image.Rectangle, seed image.Point, tolerance int) (Sample, error) {
	if !scope.In(img.Bounds()) || scope.Empty() || !seed.In(scope) {
		return Sample{}, fmt.Errorf("surface ROI or seed outside image: %v / %v", scope, seed)
	}
	target := img.RGBAAt(seed.X, seed.Y)
	visited := make([]bool, scope.Dx()*scope.Dy())
	queue := make([]image.Point, 1, scope.Dx()*scope.Dy()/2)
	queue[0] = seed
	visited[(seed.Y-scope.Min.Y)*scope.Dx()+seed.X-scope.Min.X] = true
	out := Sample{Color: target, Bounds: image.Rect(seed.X, seed.Y, seed.X+1, seed.Y+1)}
	for at := 0; at < len(queue); at++ {
		p := queue[at]
		out.Pixels++
		out.Bounds = out.Bounds.Union(image.Rect(p.X, p.Y, p.X+1, p.Y+1))
		for _, d := range [...]image.Point{{X: 1}, {X: -1}, {Y: 1}, {Y: -1}} {
			q := p.Add(d)
			if !q.In(scope) {
				continue
			}
			index := (q.Y-scope.Min.Y)*scope.Dx() + q.X - scope.Min.X
			if visited[index] {
				continue
			}
			visited[index] = true
			c := img.RGBAAt(q.X, q.Y)
			if abs(int(c.R)-int(target.R)) <= tolerance && abs(int(c.G)-int(target.G)) <= tolerance && abs(int(c.B)-int(target.B)) <= tolerance {
				queue = append(queue, q)
			}
		}
	}
	out.Clipped = out.Bounds.Min.X == scope.Min.X || out.Bounds.Min.Y == scope.Min.Y || out.Bounds.Max.X == scope.Max.X || out.Bounds.Max.Y == scope.Max.Y
	return out, nil
}

func Dominant(img *image.RGBA, r image.Rectangle) (color.RGBA, error) {
	if !r.In(img.Bounds()) || r.Empty() {
		return color.RGBA{}, fmt.Errorf("color ROI outside image: %v", r)
	}
	counts := make(map[color.RGBA]int)
	var best color.RGBA
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			counts[c]++
			if counts[c] > n {
				best, n = c, counts[c]
			}
		}
	}
	return best, nil
}

func Ink(img *image.RGBA, r image.Rectangle, tolerance int) (Sample, error) {
	bg, err := Dominant(img, r)
	if err != nil {
		return Sample{}, err
	}
	out := Sample{}
	counts := make(map[color.RGBA]int)
	best := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if max(abs(int(c.R)-int(bg.R)), abs(int(c.G)-int(bg.G)), abs(int(c.B)-int(bg.B))) <= tolerance {
				continue
			}
			counts[c]++
			if counts[c] > best {
				out.Color, best = c, counts[c]
			}
			if out.Pixels == 0 {
				out.Bounds = image.Rect(x, y, x+1, y+1)
			} else {
				out.Bounds = out.Bounds.Union(image.Rect(x, y, x+1, y+1))
			}
			out.Pixels++
		}
	}
	if out.Pixels == 0 {
		return Sample{}, fmt.Errorf("ink ROI contains no contrasting pixels: %v", r)
	}
	return out, nil
}

func Physical(x, y, w, h float32, scale float64) image.Rectangle {
	return image.Rect(int(math.Round(float64(x)*scale)), int(math.Round(float64(y)*scale)), int(math.Round(float64(x+w)*scale)), int(math.Round(float64(y+h)*scale)))
}

func ColorDifference(a, b color.RGBA) int {
	return max(abs(int(a.R)-int(b.R)), abs(int(a.G)-int(b.G)), abs(int(a.B)-int(b.B)))
}
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
