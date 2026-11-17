package main

// 新界面的设置页接线：winshell 画分类栏和内容区的外壳；“外观”一页用 winshell 的控件按原型重做（不用旧组件库），
// 其余分类的内容暂时还是旧组件库画的（legacySettingsBody），按各页自己的计划逐页换。
// 这个文件和 workbench_*.go 一样不许直接 import 任何组件库、不许调旧主题桥（archcheck/newui_test.go）。

import (
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/winshell"
)

// wbSettingIcons 和 settingsTabs 一一对应：用同一组 tabXxx 当下标，加页、撤页时两边不会错位。
// 都取 chatui 图标表里现成的，没有新画。
var wbSettingIcons = []string{
	tabLook: "palette", tabModels: "ai", tabMemory: "brain", tabTools: "tool",
	tabAccounts: "qq", tabNotify: "bell", tabData: "folder", tabPlugins: "layers",
}

const wbLookSaveDelay = 500 * time.Millisecond

func (a *app) wbSettingsSpec(c *ui.Context, kit *winshell.Kit) *winshell.SettingsSpec {
	w := &a.workbench.shell
	st := &winshell.SettingsSpec{
		Group: "通用", Selected: strconv.Itoa(a.realTab), Search: &w.settingsSearch,
		OnSelect: func(id string) {
			a.loopsUI.page = id == loopsCategoryID
			a.remindUI.page = id == remindCategoryID
			if i, err := strconv.Atoi(id); err == nil && i >= 0 && i < len(settingsTabs) {
				a.realTab = i
			}
		},
	}
	if a.loopsUI.page {
		st.Selected = loopsCategoryID
	}
	if a.remindUI.page {
		st.Selected = remindCategoryID
	}
	for i, name := range settingsTabs {
		st.Categories = append(st.Categories, winshell.Category{ID: strconv.Itoa(i), Label: name, Icon: chatui.Icon(wbSettingIcons[i], false)})
		if i == tabNotify { // “提醒”“定期任务”排在“通知”后面：只有新界面有这两页，所以不进 settingsTabs（旧设置页按下标对内容）
			st.Categories = append(st.Categories, winshell.Category{ID: remindCategoryID, Label: "提醒", Icon: chatui.Icon("bell", false)})
			st.Categories = append(st.Categories, winshell.Category{ID: loopsCategoryID, Label: "定期任务", Icon: chatui.Icon("time", false)})
		}
	}
	st.Title = settingsTabs[a.realTab]
	if a.loopsUI.page {
		st.Title = "定期任务"
	}
	if a.remindUI.page {
		st.Title = "提醒"
	}
	st.Content = func() {
		if a.remindUI.page {
			a.wbReminders(c, kit)
			return
		}
		if a.loopsUI.page {
			a.wbLoops(c, kit)
			return
		}
		if a.realTab == tabLook {
			a.wbAppearance(c, kit)
			return
		}
		if a.realTab == tabTools {
			drawn := a.wbApprovalRules(c, kit)
			a.wbWebOrder(c, kit)
			if drawn {
				kit.SectionHeading(c, "审批设置")
			}
		}
		a.legacySettingsBody(c)
	}
	return st
}

var wbModes = []string{"system", "light", "night"}

func modeIndex(mode string) int {
	for i, m := range wbModes {
		if m == mode {
			return i
		}
	}
	return 0
}

// lookFromDesk、lookToDesk 在桌面设置里存的外观和 chatui 的设置之间转换（字号走桌面主题，深浅每帧带进来）。
func lookFromDesk(l desksettings.Look) chatui.Settings {
	s := chatui.DefaultSettings()
	s.Radius, s.Density, s.Neutral, s.Wallpaper, s.Ligatures = l.Radius, l.Density, chatui.Neutral(l.Neutral), l.Wallpaper, l.Ligatures
	return s
}

func (a *app) lookToDesk(s chatui.Settings) desksettings.Look {
	l := desksettings.Look{Radius: s.Radius, Density: s.Density, Neutral: int(s.Neutral), Wallpaper: s.Wallpaper, Ligatures: s.Ligatures, GroupWidth: 100, PrivateWidth: 0}
	if p := a.workbench.shell.widthPct; p[0] != 0 {
		l.PrivateWidth = p[0]
	}
	if p := a.workbench.shell.widthPct; p[1] != 0 {
		l.GroupWidth = p[1]
	}
	return l
}

// wbLookChanged 记下外观改了：之后 500 毫秒没再动就写进桌面设置（拖滑块不会一帧写一次）。
func (a *app) wbLookChanged(c *ui.Context) {
	w := &a.workbench.shell
	w.lookDirty, w.lookAt = true, c.Now()
	w.lookTrack.rearm()
	c.After(wbLookSaveDelay)
}

func (a *app) flushWbLook(c *ui.Context) {
	w := &a.workbench.shell
	if !w.lookDirty || a.desktop == nil {
		return
	}
	if rest := wbLookSaveDelay - c.Now().Sub(w.lookAt); rest > 0 {
		c.After(rest)
		return
	}
	l, st := a.lookToDesk(w.look), a.desktop
	if w.lookTrack.skip(l) {
		return // 这一份刚写失败过：留着“没存上”，等用户再改
	}
	w.lookDirty = false
	seq := w.lookTrack.begin()
	a.deskSaves.add(func() {
		err := st.SetLook(l)
		a.post(func() { // 回界面线程对账：序号不是最新的旧结果不改账
			if w.lookTrack.finish(seq, l, err) {
				w.lookDirty = true
				a.status = "外观没写下：" + err.Error()
			}
		})
	})
}

// wbAppearance 是“外观”一页：项目照原型设置页（深色外观、字号、圆角、疏密、底色冷暖、聊天背景图案、等宽字连字），
// 外加正式程序已有的字体和面板材质（原型的“玻璃”开关换成三档材质），下面一块实时预览。
// 原型的“进行中展开过程”开关正式程序里没有对应功能，不画。
func (a *app) wbAppearance(c *ui.Context, kit *winshell.Kit) {
	w := &a.workbench.shell
	g := kit.G
	changed := false
	control := func(key string, f func()) {
		ui.Column(c.Key(key)).Width(g.ControlWide).Shrink(1).MinWidth(0).Children(f)
	}
	// 外观模式：跟随系统、亮色、夜间（原型只有一个深色开关，正式程序的三选一更全，保留）
	mode := modeIndex(a.look.settings.Mode)
	kit.Row(c, "look-mode-row", "外观模式", "", func() {
		control("look-mode-box", func() {
			if kit.Segment(c, "look-mode", &mode, []string{"跟随系统", "亮色", "夜间"}) {
				a.look.settings.Mode = wbModes[mode]
				a.persistLook()
			}
		})
	})
	// 字号是连续数值，用滑块（12～20，一档一个刻度，键盘左右键调）：拖动时界面实时跟着变，松手才写设置文件
	size := a.legacyTextSize()
	sizeChanged, dragging := kit.SliderRowTicks(c, "look-text", "字号", &size, chatui.TextMin, chatui.TextMax, 1, true)
	if sizeChanged {
		a.setTextSizeLive(size)
	}
	if !dragging && a.look.textDirty {
		a.look.textDirty = false
		a.persistLook()
	}
	// 字体：本机装了的常见中文字体加文渊圆体、系统字体
	a.wbFontRow(c, kit)
	changed = kit.SliderRow(c, "look-radius", "圆角", &w.look.Radius, chatui.RadiusMin, chatui.RadiusMax, 1) || changed
	changed = kit.SliderRow(c, "look-density", "疏密", &w.look.Density, chatui.DensityMin, chatui.DensityMax, 0.5) || changed
	// 聊天区宽度（消息列和输入框同宽）：百分比滑块 40～100，步长 5；群聊默认拉满，私聊默认 55（宽屏约等于原型的 752）
	for i, row := range []struct{ key, label string }{{"look-width-private", "私聊区宽度（%）"}, {"look-width-group", "群聊区宽度（%）"}} {
		v := w.widthPct[i]
		if v == 0 { // 没设就是拉满
			v = 100
		}
		if kit.SliderRow(c, row.key, row.label, &v, 40, 100, 5) {
			w.widthPct[i] = v
			changed = true
		}
	}
	n := int(w.look.Neutral)
	kit.Row(c, "look-neutral-row", "底色", "", func() {
		control("look-neutral-box", func() {
			if kit.Segment(c, "look-neutral", &n, []string{"偏冷", "中性", "偏暖"}) {
				w.look.Neutral = chatui.Neutral(n)
				changed = true
			}
		})
	})
	kit.Row(c, "look-wallpaper-row", "聊天背景图案", "", func() {
		before := w.look.Wallpaper
		kit.Switch(c, "look-wallpaper", "聊天背景图案", &w.look.Wallpaper)
		changed = changed || before != w.look.Wallpaper
	})
	materialIdx := panelMaterialIndex(w.material)
	kit.Row(c, "look-material-row", "面板材质", "标题栏、输入框浮在消息上的样子；系统开了降低透明度就用实色", func() {
		control("look-material-box", func() {
			if kit.Segment(c, "look-material", &materialIdx, panelMaterialLabels) {
				a.setPanelMaterial(panelMaterialNames[materialIdx])
			}
		})
	})
	kit.Row(c, "look-ligatures-row", "等宽字连字", "", func() {
		before := w.look.Ligatures
		kit.Switch(c, "look-ligatures", "等宽字连字", &w.look.Ligatures)
		changed = changed || before != w.look.Ligatures
	})
	ui.Text(c, "预览").FontSize(g.LabelSize).FontWeight(g.StrongWeight).FixedLineHeight(g.LabelLine)
	kit.Preview(c)
	if changed {
		a.wbLookChanged(c)
	}
	a.flushWbLook(c)
}

// wbFontRow 是字体那一行：下拉，选项来自 fontChoices（文渊圆体、系统字体、本机装了的中文字体），当前字体不在里面也列出来。
func (a *app) wbFontRow(c *ui.Context, kit *winshell.Kit) {
	w := &a.workbench.shell
	current := ""
	if t := a.look.current; t != nil {
		current = t.Typography().Font()
	}
	choices := fontChoices(current, fontInstalled)
	labels := make([]string, len(choices))
	idx := 0
	for i, ch := range choices {
		labels[i] = ch.Label
		if strings.EqualFold(ch.Family, current) {
			idx = i
		}
	}
	note := fontNote(current, a.look.bundled, fontInstalled)
	kit.Row(c, "look-font-row", "字体", note, func() {
		ui.Column(c.Key("look-font-box")).Width(kit.G.ControlWide).Shrink(1).MinWidth(0).Children(func() {
			kit.Dropdown(c, "look-font", "字体", &w.fontOpen, idx, labels, func(i int) { a.setFontFamily(choices[i].Family) }, func(i int) string {
				if choices[i].Family == desksettings.SystemFont {
					return ""
				}
				return choices[i].Family
			})
		})
	})
}
