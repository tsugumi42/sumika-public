package main

// 模型页的"模型上下文和输出"一节（ADR-334）：每个模型一行，写窗口、单次输出和各自的来源，点一行可以覆盖或恢复。
// 来源角标：服务商报的不加字（默认情况），手填写"手填"，models.dev 补的写"资料库"，谁都没给写"未知"。
import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/tsugumi42/MewTypeUI/settings"
	"sumika-native/internal/api"
	"sumika-native/internal/config"

	"github.com/egoist/mygo/ui"
)

// limitCount 把令牌数写短：128000 → 128K，1050000 → 1.05M
func limitCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%gM", float64(n/10_000)/100)
	case n >= 1000:
		// 一位小数：65536 → 65.5K，128000 → 128K
		return fmt.Sprintf("%gK", math.Round(float64(n)/100)/10)
	}
	return fmt.Sprint(n)
}

// limitTag 是来源角标：服务商报的不加字
func limitTag(source string) string {
	switch source {
	case "user":
		return "（手填）"
	case "modelsdev":
		return "（资料库）"
	case "default":
		return "（未知）"
	}
	return ""
}

// modelLimitValue 是一行右边的字：上下文和单次输出，各带来源角标
func modelLimitValue(m api.ModelOption) string {
	return fmt.Sprintf("上下文 %s%s · 输出 %s%s", limitCount(m.ContextWindow), limitTag(m.WindowSource), limitCount(m.MaxOutput), limitTag(m.OutputSource))
}

// modelLimitDisagree 在服务商和资料库说的不一样时写出两边的数（照服务商的，这里只是让人知道）；一样或只有一边说时是空
func modelLimitDisagree(m api.ModelOption) string {
	var parts []string
	if m.ProviderWindow > 0 && m.DevWindow > 0 && m.ProviderWindow != m.DevWindow {
		parts = append(parts, fmt.Sprintf("上下文：服务商 %s，资料库 %s", limitCount(m.ProviderWindow), limitCount(m.DevWindow)))
	}
	if m.ProviderOutput > 0 && m.DevOutput > 0 && m.ProviderOutput != m.DevOutput {
		parts = append(parts, fmt.Sprintf("输出：服务商 %s，资料库 %s", limitCount(m.ProviderOutput), limitCount(m.DevOutput)))
	}
	if m.DevMixed && m.WindowSource == "modelsdev" {
		parts = append(parts, "资料库里各家说法不一致，取了最小的")
	}
	return strings.Join(parts, "；")
}

// modelsDevStatus 是资料库那一行右边的字
func modelsDevStatus(s api.ModelsDevState, now time.Time) string {
	switch {
	case !s.Enabled:
		return "已关闭"
	case s.FetchedAt.IsZero() && s.Error != "":
		return "没取到，也没有缓存"
	case s.FetchedAt.IsZero():
		return "还没有缓存"
	}
	age := now.Sub(s.FetchedAt)
	when := "刚刚更新"
	switch {
	case age >= 48*time.Hour:
		when = fmt.Sprintf("%d 天前更新", int(age.Hours()/24))
	case age >= time.Hour:
		when = fmt.Sprintf("%d 小时前更新", int(age.Hours()))
	case age >= time.Minute:
		when = fmt.Sprintf("%d 分钟前更新", int(age.Minutes()))
	}
	if s.Error != "" {
		return "没联网，用的是缓存 · " + when
	}
	return when
}

func (a *app) modelLimitsSection(c *ui.Context) {
	s := &a.models
	a.settingsHeading(c, "模型的上下文和输出")
	status := modelsDevStatus(s.view.ModelsDev, time.Now())
	// 点这一行 = 刷新模型列表，同时立刻联网刷新一次资料库
	a.settingsRow(c.Key("models-dev"), "模型资料库", settings.ButtonOptions{Value: status, MutedValue: true, Disabled: s.busy || s.writer == nil || !s.view.ModelsDev.Enabled, OnActivate: func() { a.modelJob(modelTarget{}, nil, true) }})
	if s.view.ModelsDev.Error != "" {
		a.modelNote(c, "资料库没取到："+s.view.ModelsDev.Error, true)
	}
	for _, m := range s.view.Models {
		m := m
		if k := a.settingsKit(); k != nil { // 新界面：每个模型一行可展开，展开后直接加减
			a.modelLimitInline(c, k, m)
			continue
		}
		target := modelTarget{model: m.ID}
		// 行名用模型 ID：覆盖按它记，也和角色行上显示的名字分得开
		name := m.ID
		a.modelRow(c, name, modelLimitValue(m), target, !s.view.ModelsEditable, func() {
			s.target, s.title, s.selected = target, name, ""
			s.choices = modelLimitMenu(m)
			s.chooser.Show()
		})
		if d := modelLimitDisagree(m); d != "" {
			a.modelNote(c, d, false)
		}
	}
}

// modelLimitMenu 是点一个模型后的菜单：改窗口、改输出，有手填的才有恢复
func modelLimitMenu(m api.ModelOption) []settings.ChoiceItem {
	items := []settings.ChoiceItem{
		{ID: "ctx", Title: "改上下文窗口", Subtitle: "现在 " + limitCount(m.ContextWindow) + limitTag(m.WindowSource)},
		{ID: "out", Title: "改单次输出上限", Subtitle: "现在 " + limitCount(m.MaxOutput) + limitTag(m.OutputSource)},
	}
	if m.UserWindow > 0 {
		items = append(items, settings.ChoiceItem{ID: "ctx0", Title: "恢复上下文窗口", Subtitle: "去掉手填的 " + limitCount(m.UserWindow)})
	}
	if m.UserOutput > 0 {
		items = append(items, settings.ChoiceItem{ID: "out0", Title: "恢复单次输出上限", Subtitle: "去掉手填的 " + limitCount(m.UserOutput)})
	}
	return items
}

// modelLimitPick 处理菜单的选择：改的打开数字框，恢复的直接写 0（去掉覆盖）
func (a *app) modelLimitPick(id string) {
	s := &a.models
	ctxField, outField := config.ModelContextOverride, config.ModelOutputOverride
	var cur api.ModelOption
	for _, m := range s.view.Models {
		if m.ID == s.target.model {
			cur = m
		}
	}
	switch id {
	case "ctx", "out":
		field, value := ctxField, cur.ContextWindow
		if id == "out" {
			field, value = outField, cur.MaxOutput
		}
		if user := map[string]int{"ctx": cur.UserWindow, "out": cur.UserOutput}[id]; user > 0 {
			value = user
		}
		s.target.field = field
		s.title += "：" + map[string]string{"ctx": "上下文窗口", "out": "单次输出上限"}[id]
		s.number.Text, s.numberError = fmt.Sprint(value), ""
		s.numberBox.Open = true
	case "ctx0", "out0":
		field := ctxField
		if id == "out0" {
			field = outField
		}
		zero := 0
		target := modelTarget{field: field, model: s.target.model}
		a.modelJob(target, &api.ModelSettingsChange{Field: field, Model: s.target.model, Int: &zero}, false)
	}
}
