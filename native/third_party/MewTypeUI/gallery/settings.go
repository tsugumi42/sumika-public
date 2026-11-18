package gallery

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"github.com/tsugumi42/MewTypeUI/settings"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// Lucide lock geometry, ISC/MIT; host-prepared alternative to restricted artwork.
var settingsLock = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>`))

func init() {
	Register(Page{ID: "settings/sections", Area: "settings", Title: "分段", View: settingsSections})
	Register(Page{ID: "settings/buttons", Area: "settings", Title: "设置行按钮", View: settingsButtons})
	Register(Page{ID: "settings/toggles", Area: "settings", Title: "开关行", View: settingsToggles})
	Register(Page{ID: "settings/radio", Area: "settings", Title: "单选", View: settingsRadio})
	Register(Page{ID: "settings/check", Area: "settings", Title: "多选", View: settingsCheck})
	Register(Page{ID: "settings/choose", Area: "settings", Title: "选择框", View: settingsChoose})
	Register(Page{ID: "settings/numbers", Area: "settings", Title: "数值输入", View: settingsNumbers})
	Register(Page{ID: "settings/slider", Area: "settings", Title: "数值滑块", View: settingsSlider})
	Register(Page{ID: "settings/models", Area: "settings", Title: "模型设置示例", View: settingsModels})
}
func settingsPage(c *ui.Context, content func()) {
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().MaxWidth(theme.T(c).Metrics().Layout().ListMaxWidth()).Children(content)
	})
}
func settingsSections(c *ui.Context) {
	visited := ui.Local(c.Root(), "settings-description-link", func() bool { return false })
	settingsPage(c, func() {
		settings.Skip(c)
		settings.SectionTitle(c, "模型与能力")
		settings.Button(c, "默认模型", settings.ButtonOptions{Value: "星海 / Aurora", Arrow: true})
		settings.Skip(c)
		settings.Divider(c, "", settings.DividerOptions{})
		settings.SectionTitle(c, "上下文")
		settings.Button(c, "上下文上限", settings.ButtonOptions{Value: "128K"})
		settings.Divider(c, "说明在灰色分隔带内自然换行。这段编造的数据用于检查长文字、灰底覆盖及两端阴影。改变窗口宽度可检查末行。", settings.DividerOptions{})
		settings.DividerContent(c, settings.DividerOptions{HideBottom: true}, func() {
			ui.RichText(c).Children(func() {
				ui.Text(c, "可点击说明：")
				ui.Text(c, "查看示例").Role(ui.RoleLink).TextColor(theme.T(c).Colors().Window().ActiveTextForeground()).OnClick(func() { *visited = !*visited })
			})
		})
		if *visited {
			settings.Divider(c, "说明链接已激活", settings.DividerOptions{})
		}
		settings.Skip(c)
		settings.Skip(c)
		settings.SectionTitle(c, "连续两段空白")
		settings.Divider(c, "只有底阴影", settings.DividerOptions{HideTop: true})
		settings.Divider(c, "关闭全部阴影", settings.DividerOptions{HideTop: true, HideBottom: true})
	})
}
func settingsButtons(c *ui.Context) {
	last := ui.Local(c.Root(), "settings-buttons-result", func() string { return "未点击" })
	settingsPage(c, func() {
		settings.SectionTitle(c, "按下、悬停、键盘和禁用")
		for i, o := range []settings.ButtonOptions{{}, {Icon: galleryCheck}, {Value: "Aurora 7"}, {Value: "当前值", MutedValue: true, Arrow: true}, {Tone: settings.Light}, {Tone: settings.Attention}, {Disabled: true}, {Icon: galleryCheck, Disabled: true}, {Value: "非常长的当前模型名称，用于先截右值", Arrow: true}} {
			label := fmt.Sprintf("设置行 %d", i+1)
			o.OnActivate = func() { *last = label }
			settings.Button(c.Key(i), label, o)
		}
		settings.Button(c, "这是一个很长的标题，超过可用宽时右值隐藏，标题单行省略", settings.ButtonOptions{Value: "隐藏右值", Arrow: true})
		settings.Divider(c, *last, settings.DividerOptions{})
	})
}
func settingsToggles(c *ui.Context) {
	values := ui.Local(c.Root(), "settings-toggle-values", func() [6]bool { return [6]bool{false, true, false, true, false, true} })
	settingsPage(c, func() {
		settings.SectionTitle(c, "开关：初始、禁用、锁定")
		for i, label := range []string{"关闭", "开启", "禁用关闭", "禁用开启", "锁定关闭", "锁定开启"} {
			o := settings.ToggleOptions{Disabled: i == 2 || i == 3, Locked: i >= 4, LockIcon: settingsLock, OnChange: func(v bool) {
				if i < 4 {
					values[i] = v
				}
			}}
			if i == 1 {
				o.Icon = galleryCheck
			}
			settings.ToggleRow(c.Key(i), label, values[i], o)
		}
		settings.Divider(c, "锁定由宿主决定如何处理点击。本例保留值；禁用采用 mygo 的 0.5 透明度差异。", settings.DividerOptions{})
	})
}
func settingsRadio(c *ui.Context) {
	selected := ui.Local(c.Root(), "settings-radio-value", func() string { return "balanced" })
	settingsPage(c, func() {
		settings.SectionTitle(c, "思考档位")
		for _, v := range []struct{ id, label string }{{"fast", "快速"}, {"balanced", "均衡"}, {"deep", "深入"}, {"disabled", "禁用"}} {
			settings.Radio(c.Key(v.id), v.label, *selected == v.id, settings.ChoiceOptions{Disabled: v.id == "disabled", OnChange: func(bool) { *selected = v.id }})
		}
	})
}
func settingsCheck(c *ui.Context) {
	values := ui.Local(c.Root(), "settings-check-values", func() [5]bool { return [5]bool{false, true, false, true, false} })
	settingsPage(c, func() {
		settings.SectionTitle(c, "多选能力")
		for i, label := range []string{"能看图", "支持工具", "禁用未选", "禁用已选", "未选择错误状态：长说明会随窗口宽度自然换行，保持完整的命中区域。"} {
			settings.Check(c.Key(i), label, values[i], settings.ChoiceOptions{Disabled: i == 2 || i == 3, Error: i == 4, Multiline: true, OnChange: func(v bool) { values[i] = v }})
		}
	})
}
func demoModels(count int) []settings.ChoiceItem {
	items := make([]settings.ChoiceItem, count)
	providers := []string{"星海", "青岚", "月港", "白榆", "流光"}
	for i := range items {
		g := i / (count / len(providers))
		name := providers[g]
		items[i] = settings.ChoiceItem{ID: fmt.Sprintf("provider-%d/model-%d", g, i), Title: fmt.Sprintf("%s Aurora %02d", name, i+1), Subtitle: "能看图 · 128K 上下文", GroupID: fmt.Sprintf("provider-%d", g), GroupTitle: name}
	}
	return items
}
func chooseOptions() settings.ChooseOptions {
	return settings.ChooseOptions{SearchLabel: "搜索模型", SearchPlaceholder: "搜索模型或能力", ClearLabel: "清空搜索", EmptyLabel: "没有匹配的模型", CloseLabel: "取消"}
}
func settingsChoose(c *ui.Context) {
	type model struct {
		state    settings.ChooseState
		items    []settings.ChoiceItem
		selected string
		wide     bool
	}
	s := ui.Local(c.Root(), "settings-choose-demo", func() model { return model{items: demoModels(200)} })
	settingsPage(c, func() {
		settings.SectionTitle(c, "200 项 / 五个服务商")
		settings.Button(c, "打开选择框", settings.ButtonOptions{Value: s.selected, Arrow: true, OnActivate: s.state.Show})
		settings.ToggleRow(c, "宽文本变体", s.wide, settings.ToggleOptions{OnChange: func(v bool) { s.wide = v }})
		settings.Divider(c, "输入筛选、清空、上下键、翻页、Tab、Enter、Escape 和遮罩关闭。选中值由宿主接受后关闭。", settings.DividerOptions{})
	})
	o := chooseOptions()
	o.WideText = s.wide
	o.OnSelect = func(id string) { s.selected = id; s.state.Open = false }
	settings.ChooseBox(c, &s.state, s.items, s.selected, "选择模型", o)
}
func settingsNumbers(c *ui.Context) {
	type model struct {
		port, zero, error, disabled settings.NumberState
		last                        string
	}
	s := ui.Local(c.Root(), "settings-number-demo", func() model {
		return model{port: settings.NumberState{Text: "3425"}, zero: settings.NumberState{Text: "0"}, disabled: settings.NumberState{Text: "65535"}}
	})
	settingsPage(c, func() {
		settings.SectionTitle(c, "数字字段")
		ui.Column(c).FillWidth().Padding(0, theme.T(c).Metrics().Input().PaddingRight(), 0, theme.T(c).Metrics().Input().PaddingLeft()).Children(func() {
			settings.NumberInput(c.Key("port"), &s.port, "端口", settings.NumberOptions{Placeholder: "端口", Limit: 65535, OnSubmit: func(v string) { s.last = "提交端口 " + v }}).Width(theme.T(c).Metrics().Input().NumberPortWidth())
			settings.NumberInput(c.Key("zero"), &s.zero, "上下文", settings.NumberOptions{Placeholder: "上下文（0 为自动）", AllowZero: true, OnSubmit: func(v string) { s.last = "提交上下文 " + v }})
			settings.NumberInput(c.Key("error"), &s.error, "必填端口", settings.NumberOptions{Placeholder: "必填端口", Limit: 65535, Error: true})
			settings.NumberInput(c.Key("disabled"), &s.disabled, "禁用端口", settings.NumberOptions{Placeholder: "禁用端口", Limit: 65535, Disabled: true})
		})
		settings.Divider(c, s.last+" · 精确输入没有加减按钮；输入 12a3 会纠正为 123，超过上限恢复前值。", settings.DividerOptions{})
	})
}
func settingsSlider(c *ui.Context) {
	v := ui.Local(c.Root(), "settings-slider-value", func() int { return 2 })
	settingsPage(c, func() {
		settings.SectionTitle(c, "宿主提供离散档位")
		settings.LimitValue(c, "下载大小", fmt.Sprintf("最多 %d MiB", []int{1, 10, 50, 100, 500}[*v]))
		settings.LimitSlider(c, "下载大小滑块", *v, 5, settings.SliderOptions{OnChange: func(n int) { *v = n }})
		settings.LimitValue(c, "禁用示例", "最多 100 MiB")
		settings.LimitSlider(c, "禁用滑块", 3, 5, settings.SliderOptions{Disabled: true})
	})
}
func settingsModels(c *ui.Context) {
	type model struct {
		items    []settings.ChoiceItem
		models   [5]string
		thinking int
		active   int
		chooser  settings.ChooseState
		number   settings.NumberState
		box      overlay.BoxState
		editing  int
		numbers  [3]int
		error    bool
	}
	s := ui.Local(c.Root(), "model-settings-example", func() model {
		items := demoModels(40)
		return model{items: items, models: [5]string{items[0].ID, items[8].ID, items[16].ID, items[24].ID, items[32].ID}, numbers: [3]int{131072, 8192, 2048}}
	})
	labels := []string{"澪的模型", "葵的模型", "凛的模型", "后台模型", "识图模型"}
	numberLabels := []string{"上下文上限", "最大输出", "保留上下文"}
	settingsPage(c, func() {
		settings.Skip(c)
		settings.SectionTitle(c, "角色模型")
		for i := range 3 {
			settings.Button(c.Key(labels[i]), labels[i], settings.ButtonOptions{Value: modelTitle(s.items, s.models[i]), MutedValue: true, Arrow: true, OnActivate: func() { s.active = i; s.chooser.Show() }})
		}
		settings.Button(c, "思考档位", settings.ButtonOptions{Value: []string{"快速", "均衡", "深入"}[s.thinking], MutedValue: true, Arrow: true, OnActivate: func() { s.thinking = (s.thinking + 1) % 3 }})
		settings.Skip(c)
		settings.Divider(c, "", settings.DividerOptions{})
		settings.SectionTitle(c, "辅助任务")
		for i := 3; i < 5; i++ {
			settings.Button(c.Key(labels[i]), labels[i], settings.ButtonOptions{Value: modelTitle(s.items, s.models[i]), MutedValue: true, Arrow: true, OnActivate: func() { s.active = i; s.chooser.Show() }})
		}
		settings.Divider(c, "所有模型与能力都是编造的数据。选择只更新宿主状态，不发起网络请求。", settings.DividerOptions{})
		settings.SectionTitle(c, "上下文")
		for i, label := range numberLabels {
			settings.Button(c.Key(label), label, settings.ButtonOptions{Value: strconv.Itoa(s.numbers[i]) + " tokens", MutedValue: true, Arrow: true, OnActivate: func() { s.editing = i; s.number.Text = strconv.Itoa(s.numbers[i]); s.error = false; s.box.Open = true }})
		}
		settings.Divider(c, "上下文数可精确输入；0 表示由宿主自动决定。保存前由宿主检查范围。", settings.DividerOptions{})
	})
	o := chooseOptions()
	o.OnSelect = func(id string) { s.models[s.active] = id; s.chooser.Open = false }
	settings.ChooseBox(c.Key("model-choice"), &s.chooser, s.items, s.models[s.active], "选择模型", o)
	save := func(text string) {
		value, err := strconv.Atoi(text)
		if err != nil || value < 0 || value > 1048576 {
			s.error = true
			return
		}
		s.numbers[s.editing] = value
		s.box.Open = false
	}
	m := theme.T(c).Metrics()
	overlay.Box(c, &s.box, numberLabels[s.editing], overlay.BoxOptions{FullBody: true, BodyHeight: m.Input().HeightMin() + m.Dialog().LineHeight(), Actions: []overlay.BoxAction{{ID: "save", Label: "保存", Default: true, KeepOpen: true}, {ID: "cancel", Label: "取消"}}, OnAction: func(id string) {
		if id == "save" {
			save(s.number.Text)
		}
	}}, func() {
		ui.Column(c).FillWidth().Padding(0, m.Input().PaddingRight(), 0, m.Input().PaddingLeft()).Children(func() {
			settings.NumberInput(c, &s.number, "编辑"+numberLabels[s.editing], settings.NumberOptions{Placeholder: "tokens（0 为自动）", AllowZero: true, Error: s.error, AutoFocus: true, OnSubmit: save})
			ui.Text(c, "范围 0–1048576 tokens")
		})
	})
}
func modelTitle(items []settings.ChoiceItem, id string) string {
	for _, item := range items {
		if item.ID == id {
			return item.Title
		}
	}
	return ""
}
