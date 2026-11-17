package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/winshell"
)

// H：设置页在新外壳里：导航 | 分类 | 内容，不再整页跳旧界面；各个分类都在左栏，点哪个内容区标题就是哪个。
func TestShellSettingsLayoutAndCategories(t *testing.T) {
	a := shellApp()
	a.rail = 4
	tt := shellTester(a, 2048, 1030)
	nav, cats, main := boxOf(t, tt, "navigation"), boxOf(t, tt, "sidebar"), boxOf(t, tt, "settings")
	if !closeTo(nav.Box.W, 64) || !closeTo(cats.Box.W, 275) || main.Box.X < cats.Box.X+cats.Box.W-1 {
		t.Fatalf("设置页分栏：导航 %+v 分类 %+v 内容 %+v，应为 64 / 275 / 其余", nav.Box, cats.Box, main.Box)
	}
	for _, n := range settingsTabs {
		if !tt.HasText(n) {
			t.Fatalf("左栏缺分类 %s：%v", n, tt.Texts())
		}
	}
	if b := boxOf(t, tt, "search-row"); !closeTo(b.Box.H, 48) || !closeTo(b.Box.Y, 16) {
		t.Fatalf("search-row：y=%v 高=%v，应与别的栏顶行同高", b.Box.Y, b.Box.H)
	}
	// 内容顶行和聊天标题条同一条线（y 16、高 48），窗口按钮胶囊摆得下
	if b := boxOf(t, tt, "settings-header"); !closeTo(b.Box.H, 64) || !closeTo(b.Box.Y, 0) {
		t.Fatalf("settings-header：y=%v 高=%v，应为 0 和 64", b.Box.Y, b.Box.H)
	}
	click(t, tt, "通知")
	if a.realTab != tabNotify {
		t.Fatalf("点“通知”后 realTab=%d", a.realTab)
	}
	tt.Advance(3 * time.Second) // 读配置的占位按 loadstate 延迟出现：刚点开什么都不写，慢了才写
	tt.Frame()
	if !tt.HasText("正在读配置…") { // 旧组件库画的分类内容装进了外壳（没有读到配置时的样子）
		t.Fatalf("外观以外的分类内容没出来：%v", tt.Texts())
	}
	click(t, tt, "外观")
	for _, label := range []string{"外观模式", "字号", "字体", "圆角", "疏密", "底色", "聊天背景图案", "面板材质", "等宽字连字", "预览"} {
		if !tt.HasText(label) {
			t.Fatalf("外观页缺“%s”：%v", label, tt.Texts())
		}
	}
	if !hasKey(tt, "look-preview") {
		t.Fatal("外观页没有预览气泡")
	}
}

// H：搜索设置按分类名过滤，没结果写空态。
func TestShellSettingsSearch(t *testing.T) {
	a := shellApp()
	a.rail = 4
	tt := shellTester(a, 2048, 1030)
	click(t, tt, "搜索设置")
	tt.Type("通")
	tt.Frame()
	if !hasKey(tt, categoryKey(tabNotify)) || hasKey(tt, categoryKey(tabLook)) {
		t.Fatalf("按名字过滤：%v", tt.Texts())
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("没有这个")
	tt.Frame()
	if !tt.HasText("没有找到设置，换个词试试") {
		t.Fatalf("空态：%v", tt.Texts())
	}
}

// H：窄窗口两层：先看到分类，点进去看内容，“返回分类”回来。
func TestShellSettingsNarrowLayers(t *testing.T) {
	a := shellApp()
	a.rail = 4
	tt := shellTester(a, 640, 900)
	if !hasKey(tt, "list-layer") || !tt.HasText("通知") {
		t.Fatalf("窄窗口应先是分类层：%v", tt.Texts())
	}
	click(t, tt, "通知")
	if hasKey(tt, "list-layer") || !tt.HasText("返回分类") {
		t.Fatalf("点分类后应进内容层：%v", tt.Texts())
	}
	click(t, tt, "返回分类")
	if !hasKey(tt, "list-layer") {
		t.Fatal("返回分类没回分类层")
	}
}

// H：外观页的控件真改东西：外观模式写进桌面主题，圆角滑块改外观并在 500 毫秒后写进桌面设置，背景图案开关，面板材质三档，字体下拉。
func TestShellAppearanceControlsWork(t *testing.T) {
	st := desksettings.New(hostFixture(t).DataDir)
	a := shellApp()
	a.rail = 4
	a.desktop = st
	a.postFn = func(f func()) { f() }
	a.initLook()
	tt := shellTester(a, 2048, 1030)

	click(t, tt, "夜间")
	if a.look.settings.Mode != "night" {
		t.Fatalf("外观模式没改：%q", a.look.settings.Mode)
	}
	click(t, tt, "跟随系统")

	// 圆角滑块：点在轨道靠右处，值应变大（滑块的轨道尺寸圆钮都是自己画的，按 Key 找它的框）
	before := a.workbench.shell.look.Radius
	slider := boxOf(t, tt, "look-radius-slider")
	tt.ClickAt(slider.Box.X+slider.Box.W*0.9, slider.Box.Y+slider.Box.H/2)
	tt.Frame()
	if after := a.workbench.shell.look.Radius; after <= before || after > chatui.RadiusMax {
		t.Fatalf("圆角没跟着滑块变：%v → %v", before, after)
	}
	tt.Advance(time.Second)
	tt.Frame()
	deadline := time.Now().Add(time.Minute) // 只防卡死：写设置要落盘，低优先级跑在忙机器上慢得很
	for {
		if got, err := st.Look(); err == nil && got.Radius == a.workbench.shell.look.Radius && got.Radius != 12 {
			break
		}
		if time.Now().After(deadline) {
			got, err := st.Look()
			t.Fatalf("外观没写进桌面设置：%+v %v（界面里 %v）", got, err, a.workbench.shell.look.Radius)
		}
		time.Sleep(10 * time.Millisecond)
		tt.Advance(time.Second)
		tt.Frame()
	}

	// 聊天背景图案开关
	was := a.workbench.shell.look.Wallpaper
	sw := boxOf(t, tt, "look-wallpaper")
	tt.ClickAt(sw.Box.X+sw.Box.W/2, sw.Box.Y+sw.Box.H/2)
	tt.Frame()
	if a.workbench.shell.look.Wallpaper == was {
		t.Fatal("背景图案开关没动")
	}

	// 面板材质三档
	click(t, tt, "磨砂")
	if a.workbench.shell.material != "blur" {
		t.Fatalf("选磨砂后材质 %q", a.workbench.shell.material)
	}
	click(t, tt, "实色")
	if a.workbench.shell.material != "solid" {
		t.Fatalf("选实色后材质 %q", a.workbench.shell.material)
	}
	click(t, tt, "液态玻璃")
	if a.workbench.shell.material != "liquid" {
		t.Fatalf("选液态玻璃后材质 %q", a.workbench.shell.material)
	}
	// 写设置按顺序：连着改几次，文件里留下的是最后一次（液态玻璃是默认值，分不出写没写，再改回实色）；等写完再收场，临时目录才删得掉
	click(t, tt, "实色")
	a.deskSaves.wait()
	if got, err := st.PanelMaterial(); err != nil || got != "solid" {
		t.Fatalf("连改几次，留下的应该是最后一次：%q %v", got, err)
	}
}

// H：字体下拉：点开列出选项，选“系统字体”改了桌面主题里的字体。
func TestShellAppearanceFontDropdown(t *testing.T) {
	a := shellApp()
	a.rail = 4
	a.initLook()
	tt := shellTester(a, 2048, 1030)
	click(t, tt, "文渊圆体（默认）") // 下拉的触发钮里显示的是当前字体
	if !tt.HasText("系统字体") {
		t.Fatalf("下拉没列出选项：%v", tt.Texts())
	}
	click(t, tt, "系统字体")
	tt.Frame()
	if got := a.look.current.Typography().Font(); got != desksettings.SystemFont {
		t.Fatalf("字体没改：%q", got)
	}
}

// 坏的外观设置不会进界面：越界值整份回默认；winshell 的字号档永远在 13～20。
func TestShellLookFromBadSettingsStaysSane(t *testing.T) {
	t.Parallel()
	s := lookFromDesk(desksettings.DefaultLook())
	if s.Radius != chatui.RadiusDefault || s.Density != chatui.DensityDefault || !s.Wallpaper {
		t.Fatalf("默认外观和 chatui 默认不一致：%+v", s)
	}
	k := winshell.NewKit(chatui.Settings{Text: 99, Radius: -5, Density: 0}, "")
	if k.S.Text != chatui.TextMax || k.S.Radius != chatui.RadiusMin || k.S.Density != chatui.DensityDefault {
		t.Fatalf("越界的值没夹住：%+v", k.S)
	}
}

// 悬停、选中的块不许比所在的栏宽：嵌套块四边等距，内层缩进并填满可用宽度（LESSONS）。
// 设置分类行：离栏边一个内距（8）、宽 = 栏宽 − 两个内距；导航项离栏边 12、宽 48；会话行按 ADR-284 铺满整栏（直角）。
func TestShellHoverBlocksStayInsideTheirColumns(t *testing.T) {
	a := shellApp()
	a.rail = 4
	tt := shellTester(a, 2048, 1030)
	cats := boxOf(t, tt, "sidebar")
	for i := range settingsTabs {
		row := boxOf(t, tt, categoryKey(i))
		if !closeTo(row.Box.X, cats.Box.X+8) || !closeTo(row.Box.W, cats.Box.W-16) {
			t.Fatalf("分类行 %d：x=%v 宽=%v，栏 x=%v 宽=%v；应离栏边 8、宽 = 栏宽 − 16", i, row.Box.X, row.Box.W, cats.Box.X, cats.Box.W)
		}
	}
	nav := boxOf(t, tt, "navigation")
	for _, id := range []string{"local", "qq", "memory", "runtime", "settings"} {
		item := boxOf(t, tt, "nav-"+id)
		if !closeTo(item.Box.X, nav.Box.X+10) || !closeTo(item.Box.W, nav.Box.W-20) {
			t.Fatalf("导航项 %s：x=%v 宽=%v，应在栏里居中、离栏边 10", id, item.Box.X, item.Box.W)
		}
	}
	a.rail = 0
	tt.Frame()
	tt.Frame()
	list, row := boxOf(t, tt, "sidebar"), boxOf(t, tt, "conversation-local:r1")
	if !closeTo(row.Box.X, list.Box.X+8) || !closeTo(row.Box.W, list.Box.W-16) {
		t.Fatalf("会话行 %+v 与列表 %+v：应两边各留 8", row.Box, list.Box)
	}
}

// 设置页分组之间用细线，不用整行宽的粗灰条（主人 2026-11-08）。
func TestSettingsBreakIsAThinLine(t *testing.T) {
	a := &app{}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(10).Children(func() {
			ui.Text(c, "上面一行")
			a.settingsBreak(c, "")
			a.settingsBreak(c, "下面有说明")
			ui.Text(c, "最后一行")
		})
	}, 400, 300)
	tt.Frame()
	lines := 0
	for _, b := range uiBoxes(tt) {
		if b.Key == "break-line" {
			lines++
			if b.Box.H > 1.01 {
				t.Fatalf("分隔线高 %v，应是 1 的细线", b.Box.H)
			}
		}
	}
	if lines != 2 || !tt.HasText("下面有说明") {
		t.Fatalf("分隔线 %d 条，说明 %v", lines, tt.Texts())
	}
}

// 字号是连续数值：滑块 12～20、一档一个刻度、键盘左右键可调；拖动时界面实时跟着变，松手才写设置（主人 2026-11-08）。
func TestShellTextSizeSlider(t *testing.T) {
	a := shellApp()
	a.rail = 4
	a.initLook()
	saves := 0
	a.look.save = func(desksettings.Appearance, uint64) { saves++ }
	tt := shellTester(a, 2048, 1030)
	if got := a.legacyTextSize(); got != 16 {
		t.Fatalf("默认字号 %v，应为 16", got)
	}
	slider := boxOf(t, tt, "look-text-slider")
	y := slider.Box.Y + slider.Box.H/2
	// 按下并拖到最右：界面实时变大，但按着的时候不写盘
	tt.Press(slider.Box.X+slider.Box.W*0.5, y)
	tt.Move(slider.Box.X+slider.Box.W-2, y)
	tt.Frame()
	tt.Frame()
	if got := a.legacyTextSize(); got != chatui.TextMax {
		t.Fatalf("拖到最右字号 %v，应为 %v", got, chatui.TextMax)
	}
	if saves != 0 {
		t.Fatalf("拖动中写盘了 %d 次，应松手才写", saves)
	}
	if got := a.workbench.shell.kit.S.Text; got != chatui.TextMax {
		t.Fatalf("界面令牌的字号 %v 没跟着实时变", got)
	}
	tt.Release(slider.Box.X+slider.Box.W-2, y)
	tt.Frame()
	tt.Frame()
	if saves != 1 {
		t.Fatalf("松手后写盘 %d 次，应为 1", saves)
	}
	// 拖到最左是 12
	slider = boxOf(t, tt, "look-text-slider")
	y = slider.Box.Y + slider.Box.H/2
	tt.Press(slider.Box.X+slider.Box.W*0.5, y)
	tt.Move(slider.Box.X+2, y)
	tt.Release(slider.Box.X+2, y)
	tt.Frame()
	tt.Frame()
	if got := a.legacyTextSize(); got != chatui.TextMin || chatui.TextMin != 12 {
		t.Fatalf("拖到最左字号 %v，应为 12", got)
	}
	// 键盘：焦点在滑块上，右键加 1、左键减 1
	saves = 0
	slider = boxOf(t, tt, "look-text-slider")
	tt.ClickAt(slider.Box.X+slider.Box.W*0.5, slider.Box.Y+slider.Box.H/2)
	tt.Frame()
	tt.Frame()
	mid := a.legacyTextSize()
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	tt.Frame()
	if got := a.legacyTextSize(); got != mid+1 {
		t.Fatalf("右键后字号 %v，应为 %v", got, mid+1)
	}
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	tt.Frame()
	if got := a.legacyTextSize(); got != mid {
		t.Fatalf("左键后字号 %v，应为 %v", got, mid)
	}
}

// 拖字号滑块的时候滑块自己不能抖：它的横向位置和大小每一帧都不变（行高随字号变，纵向挪不算）。
func TestShellTextSliderDoesNotShiftWhileDragging(t *testing.T) {
	a := shellApp()
	a.rail = 4
	a.initLook()
	tt := shellTester(a, 2048, 1030)
	slider := boxOf(t, tt, "look-text-slider")
	y := slider.Box.Y + slider.Box.H/2
	tt.Press(slider.Box.X+slider.Box.W*0.4, y)
	assertNoHorizontalShift(t, tt, []string{"look-text-slider"}, func() {
		tt.Move(slider.Box.X+slider.Box.W*0.8, y)
		tt.Frame()
	}, 12)
	tt.Release(slider.Box.X+slider.Box.W*0.8, y)
}
