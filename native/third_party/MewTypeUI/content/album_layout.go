package content

import (
	"github.com/egoist/mygo/ui"
	"math"
)

type albumCacheKey struct{}
type albumCache struct {
	ratios                             []float64
	tiles                              []ui.Rect
	width, height, limit, gap, minimum float32
}

func (a *albumCache) update(items []Media, width, minimum, gap float32) {
	changed := len(a.ratios) != len(items) || a.limit != width || a.minimum != minimum || a.gap != gap
	if len(a.ratios) != len(items) {
		a.ratios = make([]float64, len(items))
		a.tiles = make([]ui.Rect, len(items))
	}
	for i, item := range items {
		w, h := item.dimensions()
		ratio := float64(w / h)
		if a.ratios[i] != ratio {
			changed = true
			a.ratios[i] = ratio
		}
	}
	if !changed {
		return
	}
	a.limit, a.minimum, a.gap = width, minimum, gap
	a.arrange()
	a.width, a.height = 0, 0
	for _, r := range a.tiles {
		a.width = max(a.width, r.X+r.W)
		a.height = max(a.height, r.Y+r.H)
	}
}

type albumSizeKey struct{}
type albumSize struct{ measured, delivered ui.Point }

// paint/album-state.md#只读尺寸通知: paint observes the final full box;
// the following build delivers a guarded notification, never a paint callback.
func albumTileSize(c *ui.Context, tile ui.Element, id string, notify func(string, float32, float32)) {
	s := ui.Local(tile, albumSizeKey{}, func() albumSize { return albumSize{} })
	if s.measured.X > 0 && s.measured.Y > 0 && s.measured != s.delivered {
		s.delivered = s.measured
		notify(id, s.measured.X, s.measured.Y)
	}
	services := c.Services()
	tile.Draw(func(_ *ui.Painter, r ui.Rect) {
		size := ui.Point{X: r.W, Y: r.H}
		if size != s.measured {
			s.measured = size
			services.Invalidate()
		}
	})
}
func rounded(v float64) float64 { return math.Round(v) }

// Topologies and ratio thresholds follow grouped_layout.cpp:115-419. These
// dimensionless constraints describe crops; all DIP inputs come from the theme.
func (a *albumCache) arrange() {
	n := len(a.ratios)
	r := a.ratios
	w, g, m := float64(a.limit), float64(a.gap), float64(a.minimum)
	put := func(i int, x, y, width, height float64) {
		a.tiles[i] = ui.Rect{X: float32(x), Y: float32(y), W: float32(width), H: float32(height)}
	}
	avg := 1.0
	wide := false
	for _, v := range r {
		avg += v
		wide = wide || v > 2
	}
	avg /= float64(n)
	if n == 1 {
		put(0, 0, 0, w, math.Floor(w/r[0]))
		return
	}
	if n >= 5 || wide {
		a.justified(avg)
		return
	}
	switch n {
	case 2:
		bothWide := r[0] > 1.2 && r[1] > 1.2
		bothSquare := r[0] >= 0.8 && r[0] <= 1.2 && r[1] >= 0.8 && r[1] <= 1.2
		if bothWide && avg > 1.4 && r[1]-r[0] < 0.2 {
			h := rounded(min(w/r[0], w/r[1], (w-g)/2))
			put(0, 0, 0, w, h)
			put(1, 0, h+g, w, h)
			return
		}
		x := math.Floor((w - g) / 2)
		if !bothWide && !bothSquare {
			x = w - g - min(rounded(max(0.4*(w-g), (w-g)/r[0]/(1/r[0]+1/r[1]))), w-g-rounded(m*1.5))
		}
		h := min(w, rounded(min(x/r[0], (w-g-x)/r[1])))
		put(0, 0, 0, x, h)
		put(1, x+g, 0, w-x-g, h)
	case 3:
		if r[0] < 0.8 {
			lower := rounded(min((w-g)/2, r[1]*(w-g)/(r[2]+r[1])))
			upper := w - g - lower
			right := max(m, rounded(min((w-g)/2, lower*r[2], upper*r[1])))
			left := min(rounded(w*r[0]), w-g-right)
			put(0, 0, 0, left, w)
			put(1, left+g, 0, right, upper)
			put(2, left+g, upper+g, right, lower)
		} else {
			top := rounded(min(w/r[0], (w-g)*0.66))
			left := math.Floor((w - g) / 2)
			bottom := min(w-top-g, rounded(min(left/r[1], left/r[2])))
			put(0, 0, 0, w, top)
			put(1, 0, top+g, left, bottom)
			put(2, left+g, top+g, w-g-left, bottom)
		}
	case 4:
		if r[0] > 1.2 {
			top := rounded(min(w/r[0], (w-g)*0.66))
			row := rounded((w - 2*g) / (r[1] + r[2] + r[3]))
			left := max(m, rounded(min((w-2*g)*0.4, row*r[1])))
			right := rounded(max(m, (w-2*g)*0.33, row*r[3]))
			middle := w - left - right - 2*g
			bottom := min(w-top-g, row)
			put(0, 0, 0, w, top)
			put(1, 0, top+g, left, bottom)
			put(2, left+g, top+g, middle, bottom)
			put(3, left+middle+2*g, top+g, right, bottom)
		} else {
			left := rounded(min(w*r[0], (w-g)*0.6))
			column := rounded((w - 2*g) / (1/r[1] + 1/r[2] + 1/r[3]))
			right := max(m, min(w-left-g, column))
			top := rounded(column / r[1])
			middle := rounded(column / r[2])
			bottom := w - top - middle - 2*g
			put(0, 0, 0, left, w)
			put(1, left+g, 0, right, top)
			put(2, left+g, top+g, right, middle)
			put(3, left+g, top+middle+2*g, right, bottom)
		}
	}
}

// Enumerate row partitions in fixed storage. Score candidates by total height
// and penalties for tiny rows or decreasing counts; allocate no candidate trees.
func (a *albumCache) justified(average float64) {
	n := len(a.ratios)
	w, g := float64(a.limit), float64(a.gap)
	var ratios [10]float64
	for i, r := range a.ratios {
		lo, hi := 0.6667, 1.0
		if average > 1.1 {
			lo, hi = 1, 2.75
		}
		ratios[i] = max(lo, min(hi, r))
	}
	var counts, best [4]int
	var heights, bestHeights [4]float64
	score := math.Inf(1)
	bestRows := 0
	var search func(row, rows, offset int)
	search = func(row, rows, offset int) {
		if row == rows {
			if offset != n {
				return
			}
			total := g * float64(rows-1)
			tiny, decreasing := false, false
			for j := range rows {
				total += heights[j]
				tiny = tiny || heights[j] < float64(a.minimum)
				if j > 0 {
					decreasing = decreasing || counts[j-1] > counts[j]
				}
			}
			cost := math.Abs(total - math.Floor(w*4/3))
			if tiny {
				cost *= 1.5
			}
			if decreasing {
				cost *= 1.5
			}
			if cost < score {
				score, bestRows = cost, rows
				best, bestHeights = counts, heights
			}
			return
		}
		maxCount := 3
		if rows == 3 && row == 1 && average < 0.85 {
			maxCount = 4
		}
		sum := 0.0
		for count := 1; count <= maxCount && offset+count <= n; count++ {
			sum += ratios[offset+count-1]
			counts[row] = count
			heights[row] = (w - g*float64(count-1)) / sum
			search(row+1, rows, offset+count)
		}
	}
	for rows := 2; rows <= 4; rows++ {
		search(0, rows, 0)
	}
	index, y := 0, float64(0)
	for row := range bestRows {
		x, h := 0.0, rounded(bestHeights[row])
		for col := range best[row] {
			width := rounded(ratios[index] * bestHeights[row])
			if col == best[row]-1 {
				width = w - x
			}
			a.tiles[index] = ui.Rect{X: float32(x), Y: float32(y), W: float32(width), H: float32(h)}
			x += width + g
			index++
		}
		y += h + g
	}
}
