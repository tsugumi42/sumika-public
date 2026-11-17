package main

// 新界面的“模型的上下文和输出”：每个模型一行，点开直接加减窗口和单次输出，改完即存；
// 平时折叠（模型多，一页几十行不能全摊开）。写入走原来的路径（modelNumberApply → modelJob），配置键和默认值不变。

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/winshell"
)

// limitSourceText 是一个数从哪来，写在加减行的说明里。
func limitSourceText(source string) string {
	switch source {
	case "user":
		return "手填的"
	case "modelsdev":
		return "资料库补的"
	case "default":
		return "没有数据，先按默认值"
	}
	return "服务商报的"
}

// modelLimitInline 画一个模型：一行摘要，点开是上下文窗口和单次输出两个加减行。
func (a *app) modelLimitInline(c *ui.Context, k *winshell.Kit, m api.ModelOption) {
	s := &a.models
	if s.limitOpen == nil {
		s.limitOpen = map[string]bool{}
	}
	g := k.G
	open := s.limitOpen[m.ID]
	// 行名用模型 ID：覆盖按它记，也和角色行上显示的名字分得开
	k.DisclosureRow(c, "model-limit-"+m.ID, m.ID, modelLimitValue(m), open, false, func() { s.limitOpen[m.ID] = !open }, nil)
	if open {
		inset := g.IconSmall + g.SpaceSmall
		// 缩进用一块定宽的空位而不是左内距：四边内距要相等，左边多一块会过不了几何检查
		ui.Row(c.Key("model-limit-indent-" + m.ID)).FillWidth().Children(func() {
			ui.Box(c.Key("model-limit-gap-" + m.ID)).Width(inset).Shrink(0)
			ui.Column(c.Key("model-limit-body-" + m.ID)).Grow(1).MinWidth(0).Gap(g.SpaceSmall).Children(func() {
				a.modelLimitField(c, k, m, "上下文窗口", config.ModelContextOverride, m.ContextWindow, m.UserWindow, m.WindowSource, 10000)
				a.modelLimitField(c, k, m, "单次输出上限", config.ModelOutputOverride, m.MaxOutput, m.UserOutput, m.OutputSource, 1000)
			})
		})
	}
	if !s.view.ModelsEditable {
		a.modelNote(c, s.view.ModelsDisabledReason, false)
	}
	if d := modelLimitDisagree(m); d != "" {
		a.modelNote(c, d, false)
	}
}

// modelLimitField 是一个覆盖项的加减行。框里是现在生效的数（手填的或服务商、资料库给的），第一次加减就把它写成手填；
// 有手填才出“恢复”，恢复就是写 0 去掉覆盖。
func (a *app) modelLimitField(c *ui.Context, k *winshell.Kit, m api.ModelOption, name string, field config.ModelField, effective, user int, source string, step float64) {
	s := &a.models
	g := k.G
	target := modelTarget{field: field, model: m.ID}
	key := fmt.Sprint("model-limit-", m.ID, "-", field)
	busy := s.busy || !s.view.ModelsEditable
	k.Row(c, key+"-row", name, "现在的数："+limitSourceText(source), func() {
		ui.Row(c.Key(key + "-controls")).Gap(g.SpaceSmall).Shrink(0).AlignItems(ui.Center).Children(func() {
			if user > 0 {
				k.Button(c, key+"-reset", "恢复", false, busy, func() {
					zero := 0
					a.modelJob(target, &api.ModelSettingsChange{Field: field, Model: m.ID, Int: &zero}, false)
				}).Tooltip("去掉手填的 " + limitCount(user) + "，回到服务商或资料库的数")
			}
			spec := winshell.StepperSpec{Min: 1000, Step: step, AllowZero: true, ZeroLabel: "不覆盖", Disabled: busy}
			k.Stepper(c, key, m.ID+" "+name, float64(effective), spec, func(v float64) {
				if msg := a.modelNumberApply(target, strconv.FormatFloat(v, 'f', -1, 64)); msg != "" {
					if s.notes == nil {
						s.notes = map[modelTarget]string{}
					}
					s.notes[target] = msg
				}
			})
		})
	})
	a.modelNote(c, s.notes[target], true)
	a.modelSavingNote(c, target)
}
