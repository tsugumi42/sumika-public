package catalog

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/winshell"
)

// inputDemo：输入框、搜索框、下拉、步进器、滑块。
func inputDemo(c *ui.Context, e Env) {
	k := e.Kit
	g := k.G
	wide := func(name string, body func()) { // 状态排里每个控件放在同宽的一格里，左边缘对齐
		ui.Column(c.Key(e.Key(name))).Width(g.ControlWide).Children(body)
	}
	nameA, nameB := Local(e, "name-a", func() string { return "" }), Local(e, "name-b", func() string { return "晴枝的纸灯笔记" })
	findA, findB := Local(e, "find-a", func() string { return "" }), Local(e, "find-b", func() string { return "纸灯" })
	nameF := Local(e, "name-f", func() string { return "正在打字" })
	pickA := Local(e, "pick-a", func() int { return 0 })
	openA := Local(e, "open-a", func() bool { return false })
	models := []string{"深度求索·快", "深度求索·深思", "本地小模型"}
	stepA := Local(e, "step-a", func() float64 { return 3 })
	slideA, slideB := Local(e, "slide-a", func() float64 { return 20 }), Local(e, "slide-b", func() float64 { return 80 })

	e.Section(c, "states", "状态排", func() {
		e.Cells(c, "row", func() {
			e.Cell(c, "field-a", "输入框·空", func() { k.TextField(c, e.Key("field-a"), nameA, "笔记标题").Width(g.ControlWide) })
			e.Cell(c, "field-b", "输入框·有字", func() { k.TextField(c, e.Key("field-b"), nameB, "笔记标题").Width(g.ControlWide) })
			e.Cell(c, "field-focus", "输入框·聚焦", func() { k.TextField(c, e.Key("field-focus"), nameF, "笔记标题").Width(g.ControlWide).AutoFocus() })
			e.Cell(c, "find-a", "搜索框·空", func() { wide("find-a-w", func() { k.SearchField(c, e.Key("find-a"), findA, "搜索笔记", nil) }) })
			e.Cell(c, "find-b", "搜索框·有字", func() { wide("find-b-w", func() { k.SearchField(c, e.Key("find-b"), findB, "搜索笔记", nil) }) })
			e.Cell(c, "drop", "下拉·收着", func() {
				wide("drop-w", func() {
					k.Dropdown(c, e.Key("drop-a"), "回复用的模型·示例", openA, *pickA, models, func(i int) { *pickA = i }, nil)
				})
			})
		})
		e.Cells(c, "row2", func() {
			e.Cell(c, "step-a", "步进器", func() {
				k.Stepper(c, e.Key("step-a"), "每天提醒几次·示例", *stepA, winshell.StepperSpec{Min: 1, Max: 12}, func(v float64) { *stepA = v })
			})
			e.Cell(c, "step-min", "步进器·到下限", func() {
				k.Stepper(c, e.Key("step-min"), "每天提醒几次·示例", 1, winshell.StepperSpec{Min: 1, Max: 12}, func(float64) {})
			})
			e.Cell(c, "step-off", "步进器·禁用", func() {
				k.Stepper(c, e.Key("step-off"), "每天提醒几次·示例", 3, winshell.StepperSpec{Min: 1, Max: 12, Disabled: true}, func(float64) {})
			})
			e.Cell(c, "slide-a", "滑块·20", func() { k.Slider(c, e.Key("slide-a"), slideA, 0, 100, 5, "音量", false) })
			e.Cell(c, "slide-b", "滑块·80", func() { k.Slider(c, e.Key("slide-b"), slideB, 0, 100, 5, "音量", false) })
		})
	})

	// 展开的下拉摆在自己的一节里，下面留出面板长到位的高度，浮层不会盖住别的例子；点别处会收起，换条目再回来会重新展开
	openB := Local(e, "open-b", func() bool { return true })
	pickB := Local(e, "pick-b", func() int { return 1 })
	e.Section(c, "dropdown-open", "下拉·展开", func() {
		ui.Column(c.Key(e.Key("drop-open-w"))).Width(g.ControlWide).Height(k.DropdownHeight(len(models))).Children(func() {
			k.Dropdown(c, e.Key("drop-b"), "回复用的模型·展开", openB, *pickB, models, func(i int) { *pickB = i }, nil)
		})
	})
	// “选项很多”的下拉为了一屏放得下没摆在这里，长列表的封顶和滚动由 winshell 的测试管

	title := Local(e, "title", func() string { return "" })
	query := Local(e, "query", func() string { return "" })
	notes := []string{"纸灯.md", "窗边.md", "购物清单.md", "旅行计划.md", "雨天歌单.md", "周报草稿.md"}
	var hit []string
	for _, n := range notes {
		if strings.Contains(n, strings.TrimSpace(*query)) {
			hit = append(hit, n)
		}
	}
	modelOpen := Local(e, "model-open", func() bool { return false })
	model := Local(e, "model", func() int { return 1 })
	times := Local(e, "times", func() float64 { return 3 })
	volume := Local(e, "volume", func() float32 { return 40 })
	e.Section(c, "play", "试一试", func() {
		e.Cells(c, "play-row", func() {
			e.Play(c, "title", func() {
				k.TextField(c, e.Key("title"), title, "给这条笔记起个名字").Width(g.ControlWide)
				e.Line(c, "title-out", map[bool]string{true: "还没起名字。", false: "要保存为「" + *title + "」。"}[strings.TrimSpace(*title) == ""])
			})
			e.Play(c, "find", func() {
				wide("find-w", func() { k.SearchField(c, e.Key("find"), query, "搜索笔记", nil) })
				e.Line(c, "find-out", fmt.Sprintf("找到 %d 条：%s", len(hit), strings.Join(hit, "、")))
			})
			e.Play(c, "model", func() {
				wide("model-w", func() {
					k.Dropdown(c, e.Key("model"), "回复用的模型", modelOpen, *model, models, func(i int) { *model = i }, nil)
				})
				e.Line(c, "model-out", "之后的回复用「"+models[*model]+"」。")
			})
		})
		e.Cells(c, "play-row2", func() {
			e.Play(c, "times", func() {
				k.Stepper(c, e.Key("times"), "每天提醒几次", *times, winshell.StepperSpec{Min: 1, Max: 12}, func(v float64) { *times = v })
				e.Line(c, "times-out", fmt.Sprintf("每天提醒 %g 次。", *times))
			})
			e.Play(c, "volume", func() {
				k.SliderRow(c, e.Key("volume"), "提示音音量", volume, 0, 100, 5)
				e.Line(c, "volume-out", map[bool]string{true: "提示音已静音。", false: fmt.Sprintf("提示音音量 %g%%。", *volume)}[*volume == 0])
			})
		})
	})
}
