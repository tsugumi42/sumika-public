package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/assets/fonts"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/paths"
)

// tinyFont 是 testdata 里文渊圆体改名后的子集（内部名字 SumikaTestRounded，可变字重，只有 ASCII 和几个字），测试不读 46 MB 的真文件。
const tinyFont = "testdata/fonts/WenYuanTiny.ttf"

func TestDefaultFontIsBundled(t *testing.T) {
	t.Parallel()
	d := desksettings.DefaultAppearance()
	light, night, err := d.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if light.Typography().Font() != bundledFamily || night.Typography().Font() != bundledFamily || bundledFamily != "WY Rounded" {
		t.Fatalf("默认字体不是文渊圆体：%q %q", light.Typography().Font(), night.Typography().Font())
	}
}

func TestLoadBundledFont(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(tinyFont)
	if err != nil {
		t.Fatal(err)
	}
	exe := t.TempDir()
	// 文件缺失：回退、不崩溃，错误里写明找哪里
	var calls int
	spy := func([]byte, string) error { calls++; return nil }
	got := loadBundledFont(exe, bundledFamily, spy)
	if !got.Loaded || got.Err == nil || calls != 0 {
		t.Fatalf("缺文件应回退：%+v 调用 %d", got, calls)
	}
	// 文件在：注册成默认家族名
	if err := os.MkdirAll(filepath.Join(exe, "fonts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exe, "fonts", fonts.WenYuanSubset), data, 0o644); err != nil {
		t.Fatal(err)
	}
	var family string
	var size int
	got = loadBundledFont(exe, bundledFamily, func(b []byte, f string) error { family, size = f, len(b); return nil })
	if got.Err != nil || family != bundledFamily || size != len(data) {
		t.Fatalf("文件在却没注册：%+v %q %d", got, family, size)
	}
	// 注册失败：回退并带错误
	got = loadBundledFont(exe, bundledFamily, func([]byte, string) error { return errors.New("坏字体") })
	if got.Err == nil {
		t.Fatal("注册失败要报错")
	}
}

func inkOf(tt *ui.Tester, label string) int {
	r, ok := tt.Find(label)
	if !ok {
		return -1
	}
	img, n := tt.Image(), 0
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			n += int(255 - img.RGBAAt(x, y).R)
		}
	}
	return n
}

// 可变字重真的生效（不是合成加粗）；子集里没有的字回退到系统字体，不显示成方块。
func TestVariableWeightAndFallback(t *testing.T) {
	data, err := os.ReadFile(tinyFont)
	if err != nil {
		t.Fatal(err)
	}
	const fam = "WY Test" // 不占用默认家族名，免得影响同进程里别的测试
	if err := ui.RegisterFont(data, fam); err != nil {
		t.Fatal(err)
	}
	weights := []int{300, 400, 500, 700}
	var widths [4]float32
	tt := ui.NewTester(func(c *ui.Context) {
		for i, w := range weights {
			widths[i], _ = c.MeasureText(1e6, ui.Span{Text: "你好 Hello", Font: fam, Size: 24, Weight: w})
		}
		ui.Column(c).Children(func() {
			for _, f := range []string{fam, ""} {
				for _, tx := range []string{"龘", "你好"} {
					ui.Text(c.Key(f+tx), tx).Font(f).FontSize(48).TextColor(ui.Hex("#000000")).Label(f + "|" + tx)
				}
			}
		})
	}, 600, 400)
	tt.Frame()
	tt.Frame()
	for i := 1; i < len(weights); i++ {
		if widths[i] <= widths[i-1] {
			t.Fatalf("字重 %d 的宽度 %v 没有比 %d 的 %v 宽，可变字重没起作用", weights[i], widths[i], weights[i-1], widths[i-1])
		}
	}
	rare, sys := inkOf(tt, fam+"|龘"), inkOf(tt, "|龘")
	if rare <= 0 || rare != sys {
		t.Fatalf("字体里没有的字应回退到系统字体显示：%d vs %d", rare, sys)
	}
	if own, sys := inkOf(tt, fam+"|你好"), inkOf(tt, "|你好"); own <= 0 || own == sys {
		t.Fatalf("字体里有的字应该用这款字体画：%d vs %d", own, sys)
	}
}

func TestFontChoicesAndNote(t *testing.T) {
	t.Parallel()
	have := func(names ...string) func(string) bool {
		return func(n string) bool {
			for _, h := range names {
				if h == n {
					return true
				}
			}
			return false
		}
	}
	got := fontChoices(bundledFamily, have("Microsoft YaHei", "Noto Serif SC", "Webdings"))
	var labels []string
	for _, c := range got {
		labels = append(labels, c.Label)
	}
	want := []string{"文渊圆体（默认）", "系统字体", "微软雅黑", "思源宋体"}
	if len(labels) != len(want) {
		t.Fatalf("选项 %v，想要 %v（没装的和非中文字体不列）", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("选项 %v，想要 %v", labels, want)
		}
	}
	if extra := fontChoices("My Custom", have()); extra[len(extra)-1].Family != "My Custom" {
		t.Fatal("当前字体不在名单里也要列出来")
	}
	missing := bundledFont{Loaded: true, Err: errors.New("x")}
	if fontNote(bundledFamily, missing, have()) == "" || fontNote(bundledFamily, bundledFont{Loaded: true}, have()) != "" || fontNote(desksettings.SystemFont, missing, have()) != "" {
		t.Fatal("文渊圆体缺失的提示只在用它的时候出现")
	}
	if fontNote("Nope", bundledFont{}, have()) == "" || fontNote("Nope", bundledFont{}, have("Nope")) != "" {
		t.Fatal("别的字体没装要提示")
	}
}

// 选字体和字号：立即生效，亮色夜间一起改，存盘后读回；坏值回退。
func TestFontSettingPersists(t *testing.T) {
	a := &app{rail: 4}
	var mapped ui.Theme
	tt := ui.NewTester(func(c *ui.Context) { a.view(c); mapped = *c.Theme() }, 1300, 1400)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if mapped.Font != bundledFamily {
		t.Fatalf("默认字体 %q", mapped.Font)
	}
	heightBefore := func() float32 { r, _ := tt.Find("深浅模式"); return r.H }()
	a.setFontFamily("Microsoft YaHei")
	a.setTextSize(18)
	a.look.current = nil
	tt.Frame()
	if mapped.Font != "Microsoft YaHei" || mapped.FontSize != 18 {
		t.Fatalf("没有立即生效：%q %v", mapped.Font, mapped.FontSize)
	}
	if r, _ := tt.Find("深浅模式"); r.H <= heightBefore {
		t.Fatalf("文字实际高度没变大：%v → %v", heightBefore, r.H)
	}
	if a.look.night.Typography().Font() != "Microsoft YaHei" || a.look.night.Typography().Body().Size() != 18 {
		t.Fatal("夜间主题没跟着改")
	}
	p, err := paths.Init(filepath.Join(t.TempDir(), "数据"))
	if err != nil {
		t.Fatal(err)
	}
	dir := p.Root
	st := desksettings.New(dir)
	if err := st.SetAppearance(a.look.settings); err != nil {
		t.Fatal(err)
	}
	back, err := desksettings.New(dir).Appearance()
	if err != nil {
		t.Fatal(err)
	}
	lt, nt, err := back.Compile()
	if err != nil || lt.Typography().Font() != "Microsoft YaHei" || nt.Typography().Body().Size() != 18 {
		t.Fatalf("重启后读回不一致：%v", err)
	}

	// 坏值：字号离谱、字体名带控制字符，改回默认，同时报 ErrTextReset
	bad := a.look.settings
	d := a.look.light.Definition()
	d.Typography["body"] = theme.TextDefinition{Size: new(float32(500)), Weight: new(400)}
	d.Font = new("坏\x00名")
	bad.Light = d
	if err := st.SetAppearance(bad); err != nil {
		t.Fatal(err)
	}
	fixed, err := desksettings.New(dir).Appearance()
	if !errors.Is(err, desksettings.ErrTextReset) {
		t.Fatalf("坏值应报 ErrTextReset：%v", err)
	}
	lt, _, cerr := fixed.Compile()
	if cerr != nil || lt.Typography().Body().Size() != theme.Light().Typography().Body().Size() || lt.Typography().Font() != bundledFamily {
		t.Fatalf("坏值没改回默认：%v %q", lt.Typography().Body().Size(), lt.Typography().Font())
	}
}
