package main

import (
	"math"
	"math/rand"
	"testing"
)

// 距离变换对着暴力算法核对：随机点集、空集、单点、边角点。
func TestEDTMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	cases := []struct {
		name string
		w, h int
		pts  int
	}{
		{"稀疏", 97, 61, 5}, {"稠密", 40, 33, 300}, {"单点", 50, 70, 1}, {"细长", 300, 3, 4}, {"一格", 1, 1, 1},
	}
	for _, c := range cases {
		src := make([]bool, c.w*c.h)
		for i := 0; i < c.pts; i++ {
			src[rng.Intn(len(src))] = true
		}
		got := edtSquared(src, c.w, c.h)
		for y := 0; y < c.h; y++ {
			for x := 0; x < c.w; x++ {
				best := math.MaxFloat64
				for sy := 0; sy < c.h; sy++ {
					for sx := 0; sx < c.w; sx++ {
						if src[sy*c.w+sx] {
							d := float64((x-sx)*(x-sx) + (y-sy)*(y-sy))
							best = math.Min(best, d)
						}
					}
				}
				if got[y*c.w+x] != best {
					t.Fatalf("%s (%d,%d)：变换 %v，暴力 %v", c.name, x, y, got[y*c.w+x], best)
				}
			}
		}
	}
	// 没有源点：全是"够不着"
	for _, v := range edtSquared(make([]bool, 20*20), 20, 20) {
		if v < edtInf/2 {
			t.Fatalf("空集应该全是够不着，得到 %v", v)
		}
	}
}

// 两个同心圆（半径差 2）的边界平均距离应该就是 2 左右。
func TestConcentricCirclesBoundaryDistance(t *testing.T) {
	const n = 400
	mk := func(r float64) []uint8 {
		l := make([]uint8, n*n)
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				if math.Hypot(float64(x)-199.5, float64(y)-199.5) < r {
					l[y*n+x] = clsBody
				}
			}
		}
		return l
	}
	a, b := mk(150), mk(152)
	d := summarize(distancesTo(boundaryMask(a, n, n, clsBody), boundaryMask(b, n, n, clsBody), n, n))
	if d.Mean < 1.7 || d.Mean > 2.3 || d.Max > 3 {
		t.Fatalf("半径差 2 的两个圆，边界距离平均 %.3f 最大 %.3f，应该在 2 附近", d.Mean, d.Max)
	}
}

func TestSpeckRemoval(t *testing.T) {
	const w, h = 40, 40
	l := make([]uint8, w*h)
	for i := range l {
		l[i] = clsBody
	}
	l[5*w+5] = clsEye // 1 像素斑点
	for y := 20; y < 30; y++ {
		for x := 10; x < 30; x++ {
			l[y*w+x] = clsEye // 200 像素的"眼睛"
		}
	}
	if n := removeSpecks(l, w, h, 10); n != 1 {
		t.Fatalf("应该只改 1 个像素，改了 %d", n)
	}
	if l[5*w+5] != clsBody || l[25*w+20] != clsEye {
		t.Fatalf("斑点没并回 body，或者把大块也改了")
	}
}

func TestPercentile(t *testing.T) {
	s := []float64{1, 2, 3, 4, 5}
	if v := percentileSorted(s, 0.5); v != 3 {
		t.Fatalf("中位数 %v", v)
	}
	if v := percentileSorted(s, 1); v != 5 {
		t.Fatalf("100 分位 %v", v)
	}
	if v := percentileSorted(s, 0.99); math.Abs(v-4.96) > 1e-9 {
		t.Fatalf("99 分位 %v", v)
	}
}
