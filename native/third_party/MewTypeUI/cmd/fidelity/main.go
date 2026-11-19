// Command fidelity renders the fourteen gallery fixtures and measures annotated
// local reference regions. It never performs OCR or uploads either image.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/gallery"
	"github.com/tsugumi42/MewTypeUI/internal/fidelity"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type observation struct {
	ID, Region, Component, Spec, Classification, Axes string
	Reference, Ours                                   [4]float64
	ReferenceColor, OursColor                         string
	Delta                                             [4]float64
	RGB                                               int
	Missing                                           bool
	Score                                             float64
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	capture := flag.String("capture-menu", "", "capture this process's real native menu, normalize to 125%, then exit")
	references := flag.String("references", "", "directory containing only the named reference inputs; no directory scan is performed")
	out := flag.String("out", "", "explicit local diagnostic output directory, outside the repository")
	only := flag.String("only", "", "one time ID; empty renders all fourteen")
	menuPath := flag.String("menu-render", "", "local PNG captured from overlay's real menu renderer, at 125%")
	before := flag.String("before", "", "previous observations.json for before/after tables")
	report := flag.String("report", "", "optional Markdown report destination")
	flag.Parse()
	if *capture != "" {
		return captureMenu(*capture)
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	absOut, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(cwd, absOut); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("diagnostic images must be outside the worktree")
	}
	if err = os.MkdirAll(absOut, 0700); err != nil {
		return err
	}
	var menu *ui.Bitmap
	if *menuPath != "" {
		data, err := os.ReadFile(*menuPath)
		if err != nil {
			return err
		}
		menu, err = ui.DecodeBitmap(data)
		if err != nil {
			return err
		}
	}
	start := time.Now()
	var rows []observation
	var timings []map[string]any
	for _, sample := range gallery.FidelityCases() {
		if *only != "" && *only != sample.ID {
			continue
		}
		if sample.Kind == "menu" && menu == nil {
			return fmt.Errorf("14-44-28 needs -menu-render: capture the existing native menu renderer; no stand-in is generated")
		}
		view := gallery.NewFidelityScene(sample, menu)
		begin := time.Now()
		tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c, theme.Light()); view(c) }, int(math.Ceil(float64(sample.Width)/1.25)), int(math.Ceil(float64(sample.Height)/1.25)))
		tt.SetPreferences(ui.Preferences{TextScale: 1, ReduceMotion: true})
		tt.SetScale(1.25)
		tt.Frame()
		imageOurs := tt.Image().SubImage(image.Rect(0, 0, sample.Width, sample.Height)).(*image.RGBA)
		path := filepath.Join(absOut, sample.ID+"-ours.png")
		if err = savePNG(path, imageOurs); err != nil {
			return err
		}
		timing := map[string]any{"id": sample.ID, "render_setup_ms": float64(time.Since(begin)) / float64(time.Millisecond)}
		if *references != "" {
			refPath := filepath.Join(*references, "2026-11-03_"+sample.ID+".png")
			ref, err := loadPNG(refPath)
			if err != nil {
				return err
			}
			if ref.Bounds().Dx() != sample.Width || ref.Bounds().Dy() != sample.Height {
				return fmt.Errorf("%s: reference dimensions changed: %v", sample.ID, ref.Bounds())
			}
			for _, region := range fidelity.Regions(sample.ID) {
				row := observation{ID: sample.ID, Region: region.Name, Component: region.Component, Spec: region.Spec, Classification: region.Classification, Axes: region.Axes}
				rr := region.Reference
				rc, err := fidelity.Dominant(ref, rr)
				if err != nil {
					return fmt.Errorf("%s %s: %w", sample.ID, region.Name, err)
				}
				own, found := tt.Find(region.Label)
				if region.Label == "" {
					found = false
				}
				row.Missing = !found
				row.Reference = rectValues(rr)
				if found {
					row.Ours = [4]float64{float64(own.X), float64(own.Y), float64(own.W), float64(own.H)}
					pr := fidelity.Physical(own.X, own.Y, own.W, own.H, 1.25).Intersect(imageOurs.Bounds())
					if pr.Empty() {
						return fmt.Errorf("%s %s: fixture region is not visible: %v", sample.ID, region.Name, own)
					}
					oc, err := fidelity.Dominant(imageOurs, pr)
					if err != nil {
						return err
					}
					row.OursColor = hex(oc)
					row.RGB = fidelity.ColorDifference(rc, oc)
					if region.Method == "ink" {
						a, err := fidelity.Ink(ref, rr, 24)
						if err != nil {
							return err
						}
						b, err := fidelity.Ink(imageOurs, pr, 24)
						if err != nil {
							return err
						}
						row.Reference = rectValues(a.Bounds)
						row.Ours = rectValues(b.Bounds)
						rc, oc = a.Color, b.Color
						row.OursColor, row.RGB = hex(oc), fidelity.ColorDifference(rc, oc)
					}
					if region.Method == "surface" {
						a, err := fidelity.Surface(ref, rr, region.Seed, 2)
						if err != nil {
							return err
						}
						seed := pr.Min.Add(region.OwnSeed)
						if region.OwnSeed.X < 0 {
							seed.X = pr.Max.X + region.OwnSeed.X
						}
						b, err := fidelity.Surface(imageOurs, pr, seed, 2)
						if err != nil {
							return err
						}
						if a.Clipped || b.Clipped {
							return fmt.Errorf("%s %s: surface crosses measurement ROI", sample.ID, region.Name)
						}
						row.Reference, row.Ours = rectValues(a.Bounds), rectValues(b.Bounds)
						rc, oc = a.Color, b.Color
						row.OursColor, row.RGB = hex(oc), fidelity.ColorDifference(rc, oc)
					}
				} else {
					row.Classification = "规格未覆盖（组件入口缺失）"
				}
				row.ReferenceColor = hex(rc)
				for i, axis := range []string{"x", "y", "w", "h"} {
					row.Delta[i] = row.Ours[i] - row.Reference[i]
					if strings.Contains(row.Axes, axis) {
						row.Score = max(row.Score, math.Abs(row.Delta[i]))
					}
				}
				rows = append(rows, row)
			}
			for _, difference := range []bool{false, true} {
				suffix := "alpha"
				if difference {
					suffix = "difference"
				}
				if err = savePNG(filepath.Join(absOut, sample.ID+"-"+suffix+".png"), fidelity.Composite(imageOurs, ref, 1.25, 0, 0, 1, .5, difference)); err != nil {
					return err
				}
			}
		}
		timing["total_ms"] = float64(time.Since(begin)) / float64(time.Millisecond)
		timings = append(timings, timing)
		fmt.Printf("%s: rendered %dx%d, %d regions\n", sample.ID, imageOurs.Bounds().Dx(), imageOurs.Bounds().Dy(), len(fidelity.Regions(sample.ID)))
	}
	if len(timings) == 0 {
		return fmt.Errorf("unknown -only ID %q", *only)
	}
	if err = saveJSON(filepath.Join(absOut, "observations.json"), rows); err != nil {
		return err
	}
	if err = saveJSON(filepath.Join(absOut, "timings.json"), map[string]any{"cases": timings, "wall_ms": float64(time.Since(start)) / float64(time.Millisecond)}); err != nil {
		return err
	}
	if err = saveCSV(filepath.Join(absOut, "measurements.csv"), rows); err != nil {
		return err
	}
	var previous []observation
	if *before != "" {
		data, err := os.ReadFile(*before)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &previous); err != nil {
			return err
		}
	}
	if *report != "" {
		if err = os.WriteFile(*report, []byte(markdown(rows, previous)), 0600); err != nil {
			return err
		}
	}
	return nil
}
func rectValues(r image.Rectangle) [4]float64 {
	return [4]float64{float64(r.Min.X) / 1.25, float64(r.Min.Y) / 1.25, float64(r.Dx()) / 1.25, float64(r.Dy()) / 1.25}
}
func hex(c color.RGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }
func loadPNG(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst, nil
}
func savePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = png.Encode(f, img)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}
func saveCSV(path string, rows []observation) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if err = w.Write([]string{"截图", "区域", "参考x", "参考y", "参考w", "参考h", "本库x", "本库y", "本库w", "本库h", "dx", "dy", "dw", "dh", "参考RGB", "本库RGB", "RGB最大通道差", "计分轴", "组件", "规格", "归因"}); err != nil {
		f.Close()
		return err
	}
	for _, r := range rows {
		record := []string{r.ID, r.Region}
		for _, v := range [][4]float64{r.Reference, r.Ours, r.Delta} {
			for _, n := range v {
				record = append(record, fmt.Sprintf("%.3f", n))
			}
		}
		rgb := fmt.Sprint(r.RGB)
		if r.Missing {
			rgb = ""
		}
		record = append(record, r.ReferenceColor, r.OursColor, rgb, r.Axes, r.Component, r.Spec, r.Classification)
		if err = w.Write(record); err != nil {
			f.Close()
			return err
		}
	}
	w.Flush()
	err = w.Error()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func markdown(rows, previous []observation) string {
	var b strings.Builder
	b.WriteString("# 2026-11-03 第一轮叠图量差\n\n")
	b.WriteString("所有坐标、尺寸为 DIP；物理截图除以 1.25。矩形为 (x,y,w,h)，右下边界不包含。参考矩形由人工逐像素标定；本库矩形来自实际组件 Tester 布局，纯色取区域众数，墨迹颜色取排除背景及低对比像素后的众数。文字、名字、头像和媒体内容不参与像素相似度评分。列宽、截图裁剪和系统标题带为宿主校准输入，不计组件误差。第一轮不修改组件。\n\n")
	b.WriteString("排序：只在“计分轴”中取最大绝对 DIP 差，由大到小；同分按 RGB 最大通道差。颜色以编码 sRGB 的 0–255 通道绝对差报告，不冒称 DeltaE。截图客户端版本、主题及字体配置不能由图片证明；观察差异不等于固定官方版本的实现缺陷。\n\n")
	b.WriteString("## 前20处\n\n|截图|区域|参考 (x,y,w,h)|本库 (x,y,w,h)|计分轴|最大尺寸/位置差|参考RGB|本库RGB|RGB差|组件|规格|归因|\n|---|---|---|---|---|---:|---|---|---:|---|---|---|\n")
	ranked := append([]observation(nil), rows...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return ranked[i].RGB > ranked[j].RGB
		}
		return ranked[i].Score > ranked[j].Score
	})
	for _, r := range ranked[:min(20, len(ranked))] {
		writeRow(&b, r)
	}
	b.WriteString("\n## 全部区域\n\n|截图|区域|参考 (x,y,w,h)|本库 (x,y,w,h)|计分轴|最大尺寸/位置差|参考RGB|本库RGB|RGB差|组件|规格|归因|\n|---|---|---|---|---|---:|---|---|---:|---|---|---|\n")
	for _, r := range rows {
		writeRow(&b, r)
	}
	b.WriteString("\n## 改前与改后\n\n")
	if len(previous) == 0 {
		b.WriteString("本轮仅建立基线，没有组件改动；上表即改前=改后，差值变化为0。下一轮以 -before 指向本轮 observations.json 生成逐区域变化。\n")
	} else {
		b.WriteString("|截图|区域|改前最大DIP差|改后最大DIP差|变化|\n|---|---|---:|---:|---:|\n")
		old := map[string]observation{}
		for _, r := range previous {
			old[r.ID+"/"+r.Region] = r
		}
		for _, r := range rows {
			if p, ok := old[r.ID+"/"+r.Region]; ok {
				fmt.Fprintf(&b, "|%s|%s|%.3f|%.3f|%+.3f|\n", r.ID, r.Region, p.Score, r.Score, r.Score-p.Score)
			}
		}
	}
	return b.String()
}
func writeRow(b *strings.Builder, r observation) {
	rgb := fmt.Sprint(r.RGB)
	if r.Missing {
		rgb = ""
	}
	fmt.Fprintf(b, "|%s|%s|%.2f, %.2f, %.2f, %.2f|%.2f, %.2f, %.2f, %.2f|%s|%.3f|%s|%s|%s|%s|[%s](%s)|%s|\n", r.ID, r.Region, r.Reference[0], r.Reference[1], r.Reference[2], r.Reference[3], r.Ours[0], r.Ours[1], r.Ours[2], r.Ours[3], r.Axes, r.Score, r.ReferenceColor, r.OursColor, rgb, r.Component, r.Spec, r.Spec, r.Classification)
}
