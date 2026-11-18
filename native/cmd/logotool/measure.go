package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// Metrics 是 metrics.json 的内容。颜色误差的单位是 0..255 的灰阶。
type Metrics struct {
	Ref  string `json:"ref"`
	Cand string `json:"cand"`
	W    int    `json:"width"`
	H    int    `json:"height"`

	IoUBody float64 `json:"iou_body"`
	IoUEye  float64 `json:"iou_eye"`
	IoUBeak float64 `json:"iou_beak"`
	IoUBG   float64 `json:"iou_bg"`

	// 边界距离：四个类的边界像素，参考到候选、候选到参考两个方向合在一起。
	// 每条交界线两侧各属一类，所以每条线都恰好算两次，权重一致。
	BoundaryMean float64 `json:"boundary_mean_px"`
	BoundaryMax  float64 `json:"boundary_max_px"`
	BoundaryP99  float64 `json:"boundary_p99_px"`

	// 颜色误差：每个像素三通道绝对差的平均，整图取平均和 99 分位。
	MAE float64 `json:"mae"`
	P99 float64 `json:"p99"`

	// 每类的颜色平均误差（按参考图的分类圈像素）。
	MAEBG   float64 `json:"mae_bg"`
	MAEBody float64 `json:"mae_body"`
	MAEEye  float64 `json:"mae_eye"`
	MAEBeak float64 `json:"mae_beak"`

	BoundaryByClass map[string]ClassBoundary `json:"boundary_by_class"`
	PixelsRef       map[string]int           `json:"pixels_ref"`
	PixelsCand      map[string]int           `json:"pixels_cand"`

	CentersLab  map[string][3]float64 `json:"class_centers_lab"`
	KmeansIters int                   `json:"kmeans_iters"`

	// 分类之后清掉的斑点：小于 speck_min_area_px 的 4 连通块并入周围最多的类。
	SpeckMinAreaPx  int `json:"speck_min_area_px"`
	SpeckPixelsRef  int `json:"speck_pixels_changed_ref"`
	SpeckPixelsCand int `json:"speck_pixels_changed_cand"`

	Notes []string `json:"notes"`
}

type ClassBoundary struct {
	DistStats
	RefToCandMean float64 `json:"ref_to_cand_mean"`
	CandToRefMean float64 `json:"cand_to_ref_mean"`
	NRef          int     `json:"n_ref"`
	NCand         int     `json:"n_cand"`
}

// measureImages 做全部计算，不写文件。
func measureImages(ref, cand *RGB) (*Metrics, []uint8, []uint8, []float64, error) {
	if ref.W != cand.W || ref.H != cand.H {
		return nil, nil, nil, nil, fmt.Errorf("两张图尺寸不同：参考 %d×%d，候选 %d×%d", ref.W, ref.H, cand.W, cand.H)
	}
	w, h := ref.W, ref.H
	labRef := labImage(ref)
	labCand := labImage(cand)
	centers, iters := kmeans(labRef)
	lr := classify(labRef, &centers)
	lc := classify(labCand, &centers)
	minArea := specksMinArea(w, h)
	specksRef := removeSpecks(lr, w, h, minArea)
	specksCand := removeSpecks(lc, w, h, minArea)

	m := &Metrics{W: w, H: h, KmeansIters: iters,
		SpeckMinAreaPx: minArea, SpeckPixelsRef: specksRef, SpeckPixelsCand: specksCand,
		BoundaryByClass: map[string]ClassBoundary{},
		PixelsRef:       map[string]int{}, PixelsCand: map[string]int{},
		CentersLab: map[string][3]float64{}, Notes: []string{},
	}
	for c := 0; c < numCls; c++ {
		m.CentersLab[clsNames[c]] = centers[c]
	}

	// IoU
	var inter, uni, nr, nc [numCls]int
	for i := range lr {
		a, b := lr[i], lc[i]
		nr[a]++
		nc[b]++
		if a == b {
			inter[a]++
			uni[a]++
		} else {
			uni[a]++
			uni[b]++
		}
	}
	iou := func(c int) float64 {
		if uni[c] == 0 {
			return 1 // 两边都没有这一类：算一致
		}
		return float64(inter[c]) / float64(uni[c])
	}
	m.IoUBG, m.IoUBody, m.IoUEye, m.IoUBeak = iou(clsBG), iou(clsBody), iou(clsEye), iou(clsBeak)
	for c := 0; c < numCls; c++ {
		m.PixelsRef[clsNames[c]] = nr[c]
		m.PixelsCand[clsNames[c]] = nc[c]
		if nr[c] > 0 && nc[c] == 0 {
			m.Notes = append(m.Notes, fmt.Sprintf("候选图里没有 %s 类的像素；该类边界距离按整图对角线长度计", clsNames[c]))
		}
	}

	// 边界距离
	var pooled []float64
	for c := 0; c < numCls; c++ {
		br := boundaryMask(lr, w, h, uint8(c))
		bc := boundaryMask(lc, w, h, uint8(c))
		d1 := distancesTo(br, bc, w, h) // 参考的边界 → 候选的边界
		d2 := distancesTo(bc, br, w, h) // 候选的边界 → 参考的边界
		all := append(append([]float64(nil), d1...), d2...)
		cb := ClassBoundary{DistStats: summarize(all), NRef: len(d1), NCand: len(d2)}
		if len(d1) > 0 {
			cb.RefToCandMean = summarize(d1).Mean
		}
		if len(d2) > 0 {
			cb.CandToRefMean = summarize(d2).Mean
		}
		m.BoundaryByClass[clsNames[c]] = cb
		pooled = append(pooled, all...)
	}
	ps := summarize(pooled)
	m.BoundaryMean, m.BoundaryMax, m.BoundaryP99 = ps.Mean, ps.Max, ps.P99

	// 颜色误差（整数和 /3，保持精确）
	errs := make([]float64, w*h)
	var clsSum [numCls]float64
	total := 0.0
	for i := 0; i < w*h; i++ {
		s := absDiff(ref.Pix[i*3], cand.Pix[i*3]) + absDiff(ref.Pix[i*3+1], cand.Pix[i*3+1]) + absDiff(ref.Pix[i*3+2], cand.Pix[i*3+2])
		e := float64(s) / 3
		errs[i] = e
		total += e
		clsSum[lr[i]] += e
	}
	m.MAE = total / float64(w*h)
	sorted := append([]float64(nil), errs...)
	sort.Float64s(sorted)
	m.P99 = percentileSorted(sorted, 0.99)
	mc := func(c int) float64 {
		if nr[c] == 0 {
			return 0
		}
		return clsSum[c] / float64(nr[c])
	}
	m.MAEBG, m.MAEBody, m.MAEEye, m.MAEBeak = mc(clsBG), mc(clsBody), mc(clsEye), mc(clsBeak)
	return m, lr, lc, errs, nil
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// ---------- 出图 ----------

// 热图色标：5 个停靠点之间线性插值（黑 → 紫 → 红 → 橙 → 浅黄）。
var heatStops = [5][3]float64{{0, 0, 4}, {87, 16, 110}, {188, 55, 84}, {249, 142, 9}, {252, 255, 164}}

func heatColor(v float64) (uint8, uint8, uint8) { // v: 0..255
	t := math.Min(math.Max(v/255, 0), 1) * 4
	i := int(t)
	if i >= 4 {
		i = 3
	}
	f := t - float64(i)
	var c [3]float64
	for k := 0; k < 3; k++ {
		c[k] = heatStops[i][k] + (heatStops[i+1][k]-heatStops[i][k])*f
	}
	return clamp8(c[0]), clamp8(c[1]), clamp8(c[2])
}

// 类的伪彩色，只在 classes_*.png 里用。
var clsColors = [numCls][3]uint8{{250, 245, 235}, {60, 80, 160}, {20, 20, 20}, {240, 150, 40}}

func cmdMeasure(args []string) error {
	fs := flag.NewFlagSet("measure", flag.ContinueOnError)
	refPath := fs.String("ref", "", "参考图 PNG")
	candPath := fs.String("cand", "", "候选图 PNG（尺寸要和参考图一样）")
	outDir := fs.String("out", "", "输出目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *refPath == "" || *candPath == "" || *outDir == "" {
		return fmt.Errorf("measure 需要 -ref、-cand、-out")
	}
	ref, err := loadPNG(*refPath)
	if err != nil {
		return err
	}
	cand, err := loadPNG(*candPath)
	if err != nil {
		return err
	}
	m, lr, lc, errs, err := measureImages(ref, cand)
	if err != nil {
		return err
	}
	m.Ref, m.Cand = absPath(*refPath), absPath(*candPath)
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	w, h := ref.W, ref.H

	// 热图：每像素误差放大 4 倍
	heat := newRGB(w, h)
	for i, e := range errs {
		r, g, b := heatColor(e * 4)
		heat.Pix[i*3], heat.Pix[i*3+1], heat.Pix[i*3+2] = r, g, b
	}
	if err := savePNG(filepath.Join(*outDir, "diff.png"), heat); err != nil {
		return err
	}

	// 叠加图：参考图上画参考的边界（红）和候选的边界（青，画在后面，重合处显示青）
	ov := &RGB{W: w, H: h, Pix: append([]uint8(nil), ref.Pix...)}
	paint := func(labels []uint8, r, g, b uint8) {
		for c := 0; c < numCls; c++ {
			for i, on := range boundaryMask(labels, w, h, uint8(c)) {
				if on {
					ov.Pix[i*3], ov.Pix[i*3+1], ov.Pix[i*3+2] = r, g, b
				}
			}
		}
	}
	paint(lr, 255, 0, 0)
	paint(lc, 0, 255, 255)
	if err := savePNG(filepath.Join(*outDir, "overlay.png"), ov); err != nil {
		return err
	}

	// 并排：参考 | 候选 | 热图
	if err := savePNG(filepath.Join(*outDir, "side.png"), hconcat([]*RGB{ref, cand, heat}, 8)); err != nil {
		return err
	}

	// 两张分类图，看类中心估得对不对
	for _, p := range []struct {
		name   string
		labels []uint8
	}{{"classes_ref.png", lr}, {"classes_cand.png", lc}} {
		cm := newRGB(w, h)
		for i, c := range p.labels {
			cm.Pix[i*3], cm.Pix[i*3+1], cm.Pix[i*3+2] = clsColors[c][0], clsColors[c][1], clsColors[c][2]
		}
		if err := savePNG(filepath.Join(*outDir, p.name), cm); err != nil {
			return err
		}
	}

	js, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "metrics.json"), js, 0o644); err != nil {
		return err
	}
	fmt.Printf("iou body=%.4f eye=%.4f beak=%.4f | 边界 平均=%.3fpx p99=%.3fpx 最大=%.3fpx | 颜色 mae=%.3f p99=%.3f | 输出 %s\n",
		m.IoUBody, m.IoUEye, m.IoUBeak, m.BoundaryMean, m.BoundaryP99, m.BoundaryMax, m.MAE, m.P99, *outDir)
	return nil
}

func absPath(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
