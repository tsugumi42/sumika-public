package main

import (
	"github.com/ELY-Open-Source/CrychicUI/button"
	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/ELY-Open-Source/CrychicUI/input"
	"github.com/ELY-Open-Source/CrychicUI/kit"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/desksettings"
	"sumika-native/internal/lookpreset"
)

var appearanceModes = []string{"跟随系统", "亮色", "夜间"}
var appearanceMaterials = []string{"none", "mica", "acrylic"}

// 面板材质三档：名字存进桌面设置，标签给设置页。
var (
	panelMaterialNames  = []string{desksettings.MaterialLiquid, desksettings.MaterialBlur, desksettings.MaterialSolid}
	panelMaterialLabels = []string{"液态玻璃", "磨砂", "实色"}
)

func panelMaterialIndex(name string) int {
	for i, n := range panelMaterialNames {
		if n == name {
			return i
		}
	}
	return 0 // 没设或坏值：液态玻璃
}

// pageLook 沿用设置页的 Section/row；输入直接绑到持久状态，避免下一次构建丢掉编辑。
func (a *app) pageLook(c *ui.Context) {
	a.initLook()
	s := &a.look
	if s.current == nil {
		s.current = s.light
		if s.settings.Mode == "night" || s.settings.Mode == "system" && c.Theme().Dark {
			s.current = s.night
		}
		s.syncEditor()
	}
	e := &s.editor
	// 提示单独占一个固定容器；错误出现时不能把下面输入框的身份挤走，导致焦点丢失。
	ui.Column(c).Children(func() {
		if s.notice != "" {
			ui.Text(c, s.notice).TextColor(c.Theme().Warning).Wrap()
		}
		if s.invalid != "" {
			ui.Text(c, s.invalid).TextColor(c.Theme().Danger).Wrap()
		}
	})
	// 文案带“（试做中）”：笔记里 9 个区域全部通过验收之前不去掉（规格-新界面主线“原型外壳迁移”）
	label := "新界面（试做中）：关"
	if a.workbenchEnabled() {
		label = "新界面（试做中）：开"
	}
	ui.Button(c.Key("main-view"), label).OnClick(func() {
		next := "workbench"
		if a.workbenchEnabled() {
			next = "legacy"
		}
		a.switchMainView(c, next)
		if next == "workbench" && a.rail > 1 {
			a.rail = 0
		}
	})
	group(c, "主题", "编辑当前显示的版本，亮色和夜间分别保存", func() {
		row(c, "配色预设", "换整套配色和圆角，字体不动", func() {
			choice.Dropdown(c, "配色预设", lookpreset.Names, &e.preset).OnChange(func() { a.applyPreset(e.preset) })
		})
		row(c, "深浅模式", "", func() {
			choice.Segments(c, "主题模式", appearanceModes, &e.mode).OnChange(func() {
				s.settings.Mode = [...]string{"system", "light", "night"}[e.mode]
				s.current, s.invalid = nil, ""
				a.persistLook()
			})
		})
		lookColor(c, "强调色", &e.accent, s.current.Colors().Control().PrimaryBackground(), func() {
			a.changeLook(func(d *theme.Definition) {
				d.Colors["control.primaryBackground"] = e.accent
				d.Colors["control.primaryBackgroundHover"] = e.accent
				d.Colors["control.primaryBackgroundPressed"] = e.accent
				d.Colors["control.focus"] = e.accent
				d.Colors["selection.background"] = e.accent
			})
		})
		lookColor(c, "收到的气泡", &e.incoming, s.current.Colors().Message().In().Background(), func() {
			a.changeLook(func(d *theme.Definition) {
				d.Colors["message.in.background"] = e.incoming
				v := d.Materials["messageIn"]
				v.Background = new("@message.in.background")
				d.Materials["messageIn"] = v
			})
		})
		lookColor(c, "发出的气泡", &e.outgoing, s.current.Colors().Message().Out().Background(), func() {
			a.changeLook(func(d *theme.Definition) {
				d.Colors["message.out.background"] = e.outgoing
				v := d.Materials["messageOut"]
				v.Background = new("@message.out.background")
				d.Materials["messageOut"] = v
			})
		})
	})
	group(c, "文字", "立即作用于整个界面，亮色和夜间一起改", func() {
		a.fontRow(c)
		a.textSizeRow(c)
	})
	group(c, "尺寸", "圆角立即作用于当前界面", func() {
		row(c, "大圆角", "", func() {
			input.NumberField(c, "大圆角", &e.large, input.NumberFieldOptions{Min: 0, Max: 40, Step: 1}).OnChange(func() {
				a.changeLook(func(d *theme.Definition) {
					d.Metrics["control.radiusLarge"] = float32(e.large)
					d.Metrics["message.radiusLarge"] = float32(e.large)
				})
			})
		})
		row(c, "小圆角", "", func() {
			input.NumberField(c, "小圆角", &e.small, input.NumberFieldOptions{Min: 0, Max: 24, Step: 1}).OnChange(func() {
				a.changeLook(func(d *theme.Definition) {
					d.Metrics["control.radiusSmall"] = float32(e.small)
					d.Metrics["message.radiusSmall"] = float32(e.small)
					d.Metrics["quote.radius"] = float32(e.small)
				})
			})
		})
	})
	group(c, "材质", "半透明只改变表面，文字保持清晰", func() {
		row(c, "窗口材质", "", func() {
			choice.Dropdown(c, "窗口材质", appearanceMaterials, &e.material).OnChange(func() {
				a.changeLook(func(d *theme.Definition) {
					v := d.Materials["window"]
					v.Kind = new(theme.WindowMaterial(e.material))
					d.Materials["window"] = v
				})
			})
		})
		row(c, "面板材质", "新界面里标题栏、输入框浮在消息上的样子；系统开了降低透明度就用实色", func() {
			if cur := a.workbench.shell.material; e.panelSeen != cur || cur == "" && e.panelSeen == "" {
				e.panel, e.panelSeen = panelMaterialIndex(cur), cur
			}
			choice.Segments(c, "面板材质", panelMaterialLabels, &e.panel).OnChange(func() {
				e.panelSeen = panelMaterialNames[e.panel]
				a.setPanelMaterial(e.panelSeen)
			})
		})
		ui.Box(c).Children(func() {
			if !c.Vibrancy() && s.current.Materials().Window().Kind() != theme.None {
				kit.Muted(c, "此窗口未提供系统材质，使用实色背景")
			}
		})
		row(c, "表面不透明度", "100% 为实色", func() {
			input.NumberField(c, "表面不透明度", &e.alpha, input.NumberFieldOptions{Min: 0, Max: 100, Step: 5}).OnChange(func() {
				a.changeLook(func(d *theme.Definition) {
					for key, value := range d.Materials {
						value.Alpha = new(float32(e.alpha / 100))
						d.Materials[key] = value
					}
				})
			})
		})
	})
	group(c, "主题文件", "JSON 文件包含完整的颜色、尺寸与材质参数", func() {
		ui.Row(c).Gap(c.Theme().Space(2)).Wrap().Children(func() {
			button.Outline(c, "导入主题").Disabled(s.busy || s.choose == nil).OnClick(func() { s.choose(true, theme.Definition{}) })
			button.Outline(c, "导出主题").Disabled(s.busy || s.choose == nil).OnClick(func() { s.choose(false, s.current.Definition()) })
			button.Outline(c, "恢复默认").OnClick(a.resetLook)
		})
		if a.demoMode {
			kit.Muted(c, "演示模式的外观仅保留在当前窗口")
		} else if s.save == nil {
			kit.Muted(c, "桌面设置尚不可写，当前修改仅用于预览")
		}
	})
}

func lookColor(c *ui.Context, label string, value *string, color ui.Color, changed func()) {
	row(c, label, "", func() {
		ui.Box(c).Label(label+"预览").Size(24, 24).Radius(theme.T(c).Metrics().Control().RadiusSmall()).Background(color).Border(1, c.Theme().Border)
		ui.TextInput(c, value).Label(label + "色值").Width(156).OnChange(changed)
	})
}
