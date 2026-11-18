package compose

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// Confirmation albums use a different limit/minimum/gap from message albums
// (308/50/2 versus 430/100/4). Keep their cache in this component; no message
// rendering changes are needed. Topologies follow grouped_layout.cpp:115-562.
type attachmentLayout struct {
	ratios                      [10]float64
	tiles                       [10]ui.Rect
	count                       int
	width, height, minimum, gap float32
}

func (a *attachmentLayout) update(items []Attachment, width, minimum, gap float32) {
	changed := a.count != len(items) || a.width != width || a.minimum != minimum || a.gap != gap
	for i, item := range items {
		w, h := imageSize(item.Preview)
		ratio := float64(w / h)
		if ratio != a.ratios[i] {
			changed = true
			a.ratios[i] = ratio
		}
	}
	if !changed {
		return
	}
	a.count, a.width, a.minimum, a.gap = len(items), width, minimum, gap
	a.arrange()
	a.height = 0
	for _, tile := range a.tiles[:a.count] {
		a.height = max(a.height, tile.Y+tile.H)
	}
}

func (a *attachmentLayout) put(i int, x, y, w, h float64) {
	a.tiles[i] = ui.Rect{X: float32(x), Y: float32(y), W: float32(w), H: float32(h)}
}
func (a *attachmentLayout) arrange() {
	n, r := a.count, &a.ratios
	w, g, m := float64(a.width), float64(a.gap), float64(a.minimum)
	average, wide := 1.0, false
	for _, v := range r[:n] {
		average += v
		wide = wide || v > 2
	}
	average /= float64(n)
	if n >= 5 || wide {
		a.rows(average)
		return
	}
	round := math.Round
	switch n {
	case 2:
		bothWide := r[0] > 1.2 && r[1] > 1.2
		bothSquare := r[0] >= .8 && r[0] <= 1.2 && r[1] >= .8 && r[1] <= 1.2
		if bothWide && average > 1.4 && r[1]-r[0] < .2 {
			h := round(min(w/r[0], w/r[1], (w-g)/2))
			a.put(0, 0, 0, w, h)
			a.put(1, 0, h+g, w, h)
			return
		}
		x := math.Floor((w - g) / 2)
		if !bothWide && !bothSquare {
			x = w - g - min(round(max(.4*(w-g), (w-g)/r[0]/(1/r[0]+1/r[1]))), w-g-round(m*1.5))
		}
		h := min(w, round(min(x/r[0], (w-g-x)/r[1])))
		a.put(0, 0, 0, x, h)
		a.put(1, x+g, 0, w-x-g, h)
	case 3:
		if r[0] < .8 {
			lower := round(min((w-g)/2, r[1]*(w-g)/(r[2]+r[1])))
			upper := w - g - lower
			right := max(m, round(min((w-g)/2, lower*r[2], upper*r[1])))
			left := min(round(w*r[0]), w-g-right)
			a.put(0, 0, 0, left, w)
			a.put(1, left+g, 0, right, upper)
			a.put(2, left+g, upper+g, right, lower)
		} else {
			top := round(min(w/r[0], (w-g)*.66))
			left := math.Floor((w - g) / 2)
			bottom := min(w-top-g, round(min(left/r[1], left/r[2])))
			a.put(0, 0, 0, w, top)
			a.put(1, 0, top+g, left, bottom)
			a.put(2, left+g, top+g, w-g-left, bottom)
		}
	case 4:
		if r[0] > 1.2 {
			top := round(min(w/r[0], (w-g)*.66))
			row := round((w - 2*g) / (r[1] + r[2] + r[3]))
			left := max(m, round(min((w-2*g)*.4, row*r[1])))
			right := round(max(m, (w-2*g)*.33, row*r[3]))
			middle := w - left - right - 2*g
			bottom := min(w-top-g, row)
			a.put(0, 0, 0, w, top)
			a.put(1, 0, top+g, left, bottom)
			a.put(2, left+g, top+g, middle, bottom)
			a.put(3, left+middle+2*g, top+g, right, bottom)
		} else {
			left := round(min(w*r[0], (w-g)*.6))
			column := round((w - 2*g) / (1/r[1] + 1/r[2] + 1/r[3]))
			right := max(m, min(w-left-g, column))
			top := round(column / r[1])
			middle := round(column / r[2])
			bottom := w - top - middle - 2*g
			a.put(0, 0, 0, left, w)
			a.put(1, left+g, 0, right, top)
			a.put(2, left+g, top+g, right, middle)
			a.put(3, left+g, top+middle+2*g, right, bottom)
		}
	}
}

func (a *attachmentLayout) rows(average float64) {
	n, w, g := a.count, float64(a.width), float64(a.gap)
	var ratios [10]float64
	for i, r := range a.ratios[:n] {
		lo, hi := .6667, 1.0
		if average > 1.1 {
			lo, hi = 1, 2.75
		}
		ratios[i] = max(lo, min(hi, r))
	}
	var counts, best [4]int
	var heights, bestHeights [4]float64
	score, bestRows := math.Inf(1), 0
	var search func(int, int, int)
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
		if rows == 3 && row == 1 && average < .85 {
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
	index, y := 0, 0.0
	for row := range bestRows {
		x, h := 0.0, math.Round(bestHeights[row])
		for col := range best[row] {
			width := math.Round(ratios[index] * bestHeights[row])
			if col == best[row]-1 {
				width = w - x
			}
			a.put(index, x, y, width, h)
			x += width + g
			index++
		}
		y += h + g
	}
}
