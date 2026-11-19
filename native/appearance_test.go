package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/desksettings"
	"sumika-native/internal/lookpreset"
)

func TestAppearanceEditorAndFiles(t *testing.T) {
	a := &app{rail: 4}
	var mapped ui.Theme
	tt := ui.NewTester(func(c *ui.Context) { a.view(c); mapped = *c.Theme() }, 1300, 1400)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	swatch := func(label string) ui.Color {
		t.Helper()
		r, ok := tt.Find(label + "预览")
		if !ok {
			t.Fatalf("找不到色样 %s", label)
		}
		p := tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+r.H/2))
		return ui.RGB(p.R, p.G, p.B)
	}
	edit := func(label, text string) {
		t.Helper()
		if err := tt.Click(label + "色值"); err != nil {
			t.Fatal(err)
		}
		tt.Key(ui.Cmd, ui.KeyA)
		tt.Type(text)
		tt.Advance(time.Second)
	}
	if mapped.Background != lookpreset.Light().Colors().Window().Background() {
		t.Fatal("默认没有应用原型预设的亮色")
	}
	originalBubble := swatch("发出的气泡")
	edit("强调色", "#9234b6")
	if swatch("强调色") != ui.Hex("#9234b6") || mapped.Accent != ui.Hex("#9234b6") {
		t.Fatalf("强调色没有进入元素与 ui.Theme：%v %v", swatch("强调色"), mapped.Accent)
	}
	focus, ok := tt.Find("强调色色值")
	if !ok {
		t.Fatal("找不到强调色输入框")
	}
	// 边框画在框上沿的整像素上，默认字号改过以后（现在是 15）框的纵坐标不一定是整数：上下各看一行
	found := false
	var border color.RGBA
	for dy := -1; dy <= 1; dy++ {
		border = tt.Image().RGBAAt(int(focus.X+focus.W/2), int(focus.Y)+dy)
		found = found || (border.R == 0x92 && border.G == 0x34 && border.B == 0xb6)
	}
	if !found {
		t.Fatalf("实际输入框焦点边框没有使用新强调色：%v", border)
	}
	if swatch("发出的气泡") != originalBubble {
		t.Fatal("强调色不应该改变气泡颜色")
	}
	edit("发出的气泡", "#bbdd88")
	if swatch("发出的气泡") != ui.Hex("#bbdd88") {
		t.Fatal("气泡色没有立即更新")
	}
	edit("强调色", "#12")
	if mapped.Accent != ui.Hex("#9234b6") || a.look.invalid == "" || !tt.HasText(a.look.invalid) || !tt.Focused("强调色色值") {
		t.Fatal("无效输入应该保留有效主题、显示错误，并保留输入焦点")
	}
	edit("强调色", "#9234b6")
	before := mapped.FontSize
	if err := tt.Click("14"); err != nil { // 默认是 15（2026-11-11，ADR-350），选 14 看字号立即变
		t.Fatal(err)
	}
	tt.Frame()
	if before != desksettings.DefaultTextSize || mapped.FontSize != 14 {
		t.Fatalf("字号没有立即生效：%v → %v", before, mapped.FontSize)
	}
	if err := tt.Click("Increase 大圆角"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if want := lookpreset.Light().Metrics().Message().RadiusLarge() + 1; mapped.Radius != want {
		t.Fatalf("圆角没有应用到控件：%v，应为 %v", mapped.Radius, want)
	}

	// 系统切换与手动选择共用根视图；回到亮色时保留刚才编辑的颜色。
	tt.SetDark(true)
	if !mapped.Dark || mapped.Background != lookpreset.Night().Colors().Window().Background() {
		t.Fatal("系统转深色没有切夜间预设")
	}
	if err := tt.Click("亮色"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if mapped.Dark || mapped.Accent != ui.Hex("#9234b6") {
		t.Fatal("手选亮色必须优先于系统并保留修改")
	}
	if err := tt.Click("跟随系统"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !mapped.Dark {
		t.Fatal("恢复跟随系统没有回到夜间")
	}
	tt.SetDark(false)
	if mapped.Dark || mapped.Accent != ui.Hex("#9234b6") {
		t.Fatal("系统转亮色丢了自定义")
	}

	file := filepath.Join(t.TempDir(), "主题 # &.json")
	if err := writeTheme(file, a.look.current.Definition()); err != nil {
		t.Fatal(err)
	}
	imported, err := readTheme(file)
	if err != nil {
		t.Fatal(err)
	}
	other := &app{}
	other.initLook()
	if err := other.importLook(imported); err != nil {
		t.Fatal(err)
	}
	expected, _ := theme.Compile(imported)
	if _, _, err := other.look.settings.Compile(); err != nil {
		t.Fatal(err)
	}
	actual, _ := theme.Compile(other.look.settings.Light)
	if !reflect.DeepEqual(actual.Definition(), expected.Definition()) {
		t.Fatal("导入导出丢了主题参数")
	}
	if err := os.WriteFile(file, []byte(`{"version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readTheme(file); err == nil {
		t.Fatal("坏导入必须报错")
	}

	// 真正的消息壳也读独立颜色，避免只改了设置页色样。
	bubble := ui.NewTester(func(c *ui.Context) {
		a.applyLook(c)
		ui.Column(c).Fill().Background(windowBackground(c)).Padding(30).Children(func() {
			chat.Bubble(c, chat.Message{Text: "主题"}, chat.BubbleOptions{Side: chat.Outgoing}, nil).Label("出站主题气泡").Size(200, 80)
		})
	}, 400, 200)
	bubble.Frame()
	r, _ := bubble.Find("出站主题气泡")
	p := bubble.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+r.H/2))
	if p.R != 0xbb || p.G != 0xdd || p.B != 0x88 {
		t.Fatalf("真实消息壳颜色没变：%v", p)
	}
	a.changeLook(func(d *theme.Definition) {
		v := d.Materials["window"]
		v.Kind = new(theme.Mica)
		v.Alpha = new(float32(0.4))
		d.Materials["window"] = v
	})
	bubble.SetVibrancy(true)
	p = bubble.Image().RGBAAt(3, 3)
	if p.A != 102 {
		t.Fatalf("有材质时窗口背景应为 40%%：%v", p)
	}
	bubble.SetVibrancy(false)
	if p := bubble.Image().RGBAAt(3, 3); p.A != 255 {
		t.Fatalf("没有材质时必须实色：%v", p)
	}
	a.resetLook()
	tt.Frame()
	if a.look.settings.Mode != "system" || mapped.Accent != lookpreset.Light().Colors().Control().PrimaryBackground() {
		t.Fatal("恢复默认没有清除自定义")
	}
}

func TestAppearanceHostLoadAndSave(t *testing.T) {
	opt := hostFixture(t)
	st := desksettings.New(opt.DataDir)
	if err := st.SetAppearance(desksettings.DefaultAppearance()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.File(), []byte(`{"appearance":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &app{postFn: func(fn func()) { fn() }}
	sh := newShell(a, opt, false)
	sh.loadAppearance(st)
	tt := ui.NewTester(a.view, 1300, 1030)
	a.rail = 4
	tt.Frame()
	if a.look.settings.Mode != "system" || !tt.HasText("读主题失败，已用默认") {
		t.Fatal("坏文件没有默认主题与用户提示")
	}
	newer := desksettings.DefaultAppearance()
	newer.Mode = "night"
	sh.writeAppearance(st, newer, 2)
	sh.writeAppearance(st, desksettings.DefaultAppearance(), 1)
	got, err := st.Appearance()
	if err != nil || got.Mode != "night" {
		t.Fatalf("旧的后台写覆盖了新主题：%+v %v", got, err)
	}
}

// 只在显式指定目录时写图；视口与像素比照主人的屏幕，裁出设置组件区域。
func TestThemeShot(t *testing.T) {
	dir := os.Getenv("SUMIKA_THEME_SHOT")
	if dir == "" {
		t.Skip("没设 SUMIKA_THEME_SHOT，不截图")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		a := &app{rail: 4}
		tt := ui.NewTester(a.view, 2048, 1030)
		tt.SetTitleBar(win11Bar)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.SetDark(dark)
		tt.SetScale(1.25)
		tt.Advance(time.Second)
		img := tt.Image()
		// 左侧导航与编辑器一起保留，右侧空白不截。
		crop := img.SubImage(image.Rect(80*5/4, 32*5/4, 1120*5/4, 1030*5/4)).(*image.RGBA)
		name := "light.png"
		if dark {
			name = "night.png"
		}
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, crop)
		cerr := f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if cerr != nil {
			t.Fatal(cerr)
		}
		t.Logf("%s: %dx%d", name, crop.Bounds().Dx(), crop.Bounds().Dy())
	}
}
