package design_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/chatui"
	"sumika-native/internal/design"
	"sumika-native/internal/design/colorx"
	"sumika-native/internal/design/tokenspec"
)

// V5、7.8：令牌 JSON 改了没重新生成、或 $value 被手改，这里红。
func TestGeneratedIsFresh(t *testing.T) {
	spec, err := tokenspec.Load("tokens")
	if err != nil {
		t.Fatal(err)
	}
	m, err := spec.Build()
	if err != nil {
		t.Fatal(err)
	}
	want, err := m.Go()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("tokens_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Fatal("tokens_gen.go 不是最新的：在 internal/design 下跑 go generate")
	}
}

// 规格第四节的默认值（默认设置：字号 16、圆角 12、疏密 8）。
func TestDefaultsMatchSpec(t *testing.T) {
	tk := design.Derive(design.DefaultSettings())
	d := tk.Dim
	want := map[string][2]float32{
		"space": {d.SpaceS2, 8}, "s0.5": {d.SpaceS0Half, 2}, "s1": {d.SpaceS1, 4}, "s1.5": {d.SpaceS1Half, 6}, "s2.5": {d.SpaceS2Half, 10},
		"s3": {d.SpaceS3, 12}, "s4": {d.SpaceS4, 16}, "s5": {d.SpaceS5, 24}, "s6": {d.SpaceS6, 32},
		"inset": {d.SpaceInset, 6}, "inset-tight": {d.SpaceInsetTight, 3}, "inset-ctl": {d.SpaceInsetCtl, 4},
		"正文": {d.FontSize4, 16}, "最小字": {d.FontSize0, 11}, "页面标题": {d.FontSize6, 22}, "正文行高": {d.FontLineBody, 26},
		"r-ctl": {d.RadiusCtl, 12}, "r-1": {d.Radius1, 18}, "r-2": {d.Radius2, 26}, "r-win": {d.RadiusWin, 34},
		"r-join": {d.RadiusJoin, 6}, "inner-min": {d.RadiusInnerMin, 4},
		"控件 sm/md/lg": {d.SizeCtlSm + d.SizeCtlMd + d.SizeCtlLg, 32 + 40 + 48},
		"导航":          {d.LayoutNav, 64}, "聊天最窄": {d.LayoutChatMin, 380},
		"分段选中块": {d.SegmentIndicatorRadius, 8}, "发送键": {d.SendRadius, 16}, "面板": {d.PanelRadius, 28},
		"窄断点": {tk.Breakpoint.Narrow, 64 + 260 + 1 + 380}, "宽断点": {tk.Breakpoint.Wide, 64 + 260 + 1 + 380 + 1 + 292},
	}
	for k, v := range want {
		if v[0] != v[1] {
			t.Errorf("%s = %v，规格是 %v", k, v[0], v[1])
		}
	}
	if tk.Motion.MotionFast != 140*time.Millisecond || tk.Motion.MotionPanel != 230*time.Millisecond {
		t.Errorf("动效时长 %v / %v", tk.Motion.MotionFast, tk.Motion.MotionPanel)
	}
	if design.Weight(tk.Num.FontWeightBold) != 700 || design.Weight(design.Derive(design.Settings{Dark: true}).Num.FontWeightBold) != 660 {
		t.Error("字重：亮色 700、深色 660")
	}
}

func sliderGrid() []design.Settings {
	var out []design.Settings
	for _, tx := range []float32{design.TextMin, 13, 15, design.TextDefault, 17, design.TextMax} {
		for _, r := range []float32{design.RadiusMin, 7, design.RadiusDefault, design.RadiusMax} {
			for _, de := range []float32{design.DensityMin, 7, design.DensityDefault, design.DensityMax} {
				s := design.DefaultSettings()
				s.Text, s.Radius, s.Density = tx, r, de
				out = append(out, s)
			}
		}
	}
	return out
}

func dimValues(d design.Dims) map[string]float32 {
	out := map[string]float32{}
	v, ty := reflect.ValueOf(d), reflect.TypeOf(d)
	for i := range v.NumField() {
		out[ty.Field(i).Name] = float32(v.Field(i).Float())
	}
	return out
}

// V3：三个滑块的各种组合下，所有尺寸都是整数；内层圆角不小于下限、比外层小。
func TestDimsAreIntegers(t *testing.T) {
	for _, s := range sliderGrid() {
		tk := design.Derive(s)
		for name, v := range dimValues(tk.Dim) {
			if v != float32(math.Round(float64(v))) || v < 0 {
				t.Errorf("字号 %v 圆角 %v 疏密 %v：%s = %v 不是非负整数", s.Text, s.Radius, s.Density, name, v)
			}
		}
		d := tk.Dim
		if d.SegmentIndicatorRadius < min(d.RadiusInnerMin, d.RadiusCtl) || d.SegmentIndicatorRadius > d.RadiusCtl {
			t.Errorf("分段选中块圆角 %v 越界（下限 %v、外层 %v）", d.SegmentIndicatorRadius, d.RadiusInnerMin, d.RadiusCtl)
		}
		if !(d.RadiusCtl < d.Radius1 && d.Radius1 < d.Radius2 && d.Radius2 < d.RadiusWin) {
			t.Errorf("圆角没有一层比一层大：%v %v %v %v", d.RadiusCtl, d.Radius1, d.Radius2, d.RadiusWin)
		}
	}
}

func allModes() []design.Settings { return modes() }

// telegramNameHues 照 chatui（Telegram colors.palette:292-313），群友名字色：明度 0.62、彩度 0.14。
var telegramNameHues = []float64{29, 52, 300, 140, 205, 245, 355}

// checkChars 是对比度要一起查的角色色：8 个默认角色色 + 群友七色。
func checkChars() []colorx.OKLCH {
	var out []colorx.OKLCH
	for _, c := range chatui.CharColors {
		out = append(out, colorx.OKLCH{L: c.L, C: c.C, H: c.H, A: c.A})
	}
	for _, h := range telegramNameHues {
		out = append(out, colorx.New(0.62, 0.14, h))
	}
	return out
}

// V4：12 种模式下，必须达标的对都达标；只记录的那些打到日志里（结果表由 cmd/design-mapping 写进 data/dev/design-tokens/）。
func TestContrast(t *testing.T) {
	soft := 0
	for _, s := range allModes() {
		for _, c := range design.Derive(s).ContrastChecks(checkChars()...) {
			if c.Pass() {
				continue
			}
			if c.Required {
				t.Errorf("dark=%v/%s/wallpaper=%v %s 对 %s：%.2f < %.1f", s.Dark, s.Neutral, s.Wallpaper, c.Name, c.Bg, c.Ratio, c.Min)
			} else {
				soft++
			}
		}
	}
	t.Logf("不强制的对比度不达标 %d 处（明细见对照表工具写的对比度表）", soft)
}

func hexOf(c ui.Color) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// 规格 3.3：三个计算的标准答案，以后别的语言的实现跑同一份。DESIGN_UPDATE_VECTORS=1 时重写。
type vectors struct {
	Note     string            `json:"note"`
	Colors   []colorVector     `json:"oklch_to_srgb"`
	Contrast []contrastVector  `json:"contrast"`
	Reach    []reachVector     `json:"reach"`
	Strong   []strongVector    `json:"strong"`
	Scale    []scaleVector     `json:"scale"`
	Extra    map[string]string `json:"extra,omitempty"`
}
type colorVector struct {
	In  [4]float64 `json:"oklch"`
	Hex string     `json:"hex"`
	A   uint8      `json:"a"`
}
type contrastVector struct {
	Fg    string  `json:"fg"`
	Bg    string  `json:"bg"`
	Ratio float64 `json:"ratio"`
}
type reachVector struct {
	Base    [3]float64 `json:"base"`
	Dir     float64    `json:"dir"`
	Min     float64    `json:"min"`
	Against []string   `json:"against"`
	Hex     string     `json:"hex"`
}
type strongVector struct {
	Char  [3]float64 `json:"char"`
	Bg    string     `json:"bg"`
	Muted string     `json:"muted"`
}
type scaleVector struct {
	Text, Radius, Density float32
	Dims                  map[string]float32 `json:"dims"`
}

func parseHex(s string) ui.Color {
	var r, g, b uint8
	fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b)
	return ui.Color{R: r, G: g, B: b, A: 255}
}

func buildVectors() vectors {
	v := vectors{Note: "规格-设计令牌 3.3 的标准答案：颜色按 0～255 整数比，对比度保留 4 位小数，尺寸是整数逻辑像素。由 internal/design 的测试生成。"}
	for _, l := range []float64{0, 0.19, 0.25, 0.47, 0.52, 0.75, 0.94, 0.993, 1} {
		for _, ch := range [][2]float64{{0, 0}, {0.008, 250}, {0.1, 195}, {0.15, 30}, {0.3, 140}} {
			o := colorx.New(l, ch[0], ch[1])
			c := o.Color()
			v.Colors = append(v.Colors, colorVector{[4]float64{l, ch[0], ch[1], 1}, hexOf(c), c.A})
		}
	}
	samples := []string{"#000000", "#ffffff", "#edf1f5", "#1d232a", "#ba493a", "#007a7b", "#767676"}
	for _, a := range samples {
		for _, b := range samples {
			r := math.Round(colorx.Contrast(parseHex(a), parseHex(b))*10000) / 10000
			v.Contrast = append(v.Contrast, contrastVector{a, b, r})
		}
	}
	for _, rc := range []struct {
		base [3]float64
		dir  float64
		min  float64
		bg   []string
	}{
		{[3]float64{0.57, 0.15, 30}, -1, 4.5, []string{"#edf1f5", "#fcfdfe"}},
		{[3]float64{0.72, 0.14, 30}, 1, 4.5, []string{"#1b1f24", "#23282e"}},
		{[3]float64{0.58, 0.12, 155}, -1, 3, []string{"#edf1f5", "#fcfdfe", "#e6eaee"}},
		{[3]float64{0.9, 0.05, 250}, -1, 7, []string{"#ffffff"}},
		{[3]float64{0.2, 0.05, 250}, 1, 7, []string{"#000000"}},
	} {
		var bgs []ui.Color
		for _, h := range rc.bg {
			bgs = append(bgs, parseHex(h))
		}
		o, _ := colorx.Reach(colorx.New(rc.base[0], rc.base[1], rc.base[2]), rc.dir, rc.min, bgs...)
		v.Reach = append(v.Reach, reachVector{rc.base, rc.dir, rc.min, rc.bg, hexOf(o.Color())})
	}
	for _, ch := range chatui.CharColors {
		bg, _, muted := colorx.Strong(colorx.New(ch.L, ch.C, ch.H), 4.5, 0.9)
		v.Strong = append(v.Strong, strongVector{[3]float64{ch.L, ch.C, ch.H}, hexOf(bg), hexOf(muted)})
	}
	for _, tx := range []float32{design.TextMin, design.TextDefault, design.TextMax} {
		for _, r := range []float32{design.RadiusMin, design.RadiusDefault, design.RadiusMax} {
			for _, de := range []float32{design.DensityMin, design.DensityDefault, design.DensityMax} {
				s := design.DefaultSettings()
				s.Text, s.Radius, s.Density = tx, r, de
				v.Scale = append(v.Scale, scaleVector{s.Text, s.Radius, s.Density, design.Derive(s).DimsByPath()})
			}
		}
	}
	return v
}

func TestVectors(t *testing.T) {
	got := buildVectors()
	data, err := json.MarshalIndent(got, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	const path = "testdata/vectors.json"
	if os.Getenv("DESIGN_UPDATE_VECTORS") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v（第一次跑设 DESIGN_UPDATE_VECTORS=1 生成）", err)
	}
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), data) {
		t.Fatal("三个计算的结果和 testdata/vectors.json 不一致：算法改了要先想清楚，有意改动再设 DESIGN_UPDATE_VECTORS=1 重写并在提交说明写原因")
	}
	if n := len(got.Colors) + len(got.Contrast) + len(got.Reach) + len(got.Strong); n < 20 || len(got.Scale) < 20 {
		t.Fatalf("标准答案太少：颜色类 %d 组、缩放 %d 组，各要至少 20 组", n, len(got.Scale))
	}
}
