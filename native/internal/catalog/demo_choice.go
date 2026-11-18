package catalog

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// choiceDemo：分段、筛选片、开关、勾选框。
func choiceDemo(c *ui.Context, e Env) {
	k := e.Kit
	g := k.G
	// 状态排每个控件各自一份值；键带条目前缀，Kit 里按键记状态不会撞
	segA, segB := Local(e, "seg-a", func() int { return 0 }), Local(e, "seg-b", func() int { return 1 })
	chipA := Local(e, "chip-a", func() int { return 0 })
	swOn, swOff := Local(e, "sw-on", func() bool { return true }), Local(e, "sw-off", func() bool { return false })
	ckOn, ckOff := Local(e, "ck-on", func() bool { return true }), Local(e, "ck-off", func() bool { return false })
	days := []string{"每天", "每周", "每月"}

	e.Section(c, "states", "状态排", func() {
		e.Cells(c, "row", func() {
			e.Cell(c, "seg-a", "分段·选第一格", func() { k.Segment(c, e.Key("seg-a"), segA, days) })
			e.Cell(c, "seg-b", "分段·选第二格", func() { k.Segment(c, e.Key("seg-b"), segB, days) })
			e.Cell(c, "chip-a", "筛选片·选中和没选", func() { k.Chips(c, e.Key("chip-a"), []string{"全部", "晴枝", "雾栞"}, chipA) })
		})
		e.Cells(c, "row2", func() {
			e.Cell(c, "sw-off", "开关·关", func() { k.Switch(c, e.Key("sw-off"), "睡前提醒", swOff) })
			e.Cell(c, "sw-on", "开关·开", func() { k.Switch(c, e.Key("sw-on"), "睡前提醒", swOn) })
			e.Cell(c, "sw-dis", "开关·禁用", func() { k.SwitchValue(c, e.Key("sw-dis"), "睡前提醒", true, true, func(bool) {}) })
			e.Cell(c, "ck-off", "勾选·没勾", func() {
				ui.Column(c.Key(e.Key("ck-off-w"))).Width(g.ControlWide).Children(func() { k.Check(c, e.Key("ck-off"), "同步到另一台电脑", ckOff, false) })
			})
			e.Cell(c, "ck-on", "勾选·勾上", func() {
				ui.Column(c.Key(e.Key("ck-on-w"))).Width(g.ControlWide).Children(func() { k.Check(c, e.Key("ck-on"), "同步到另一台电脑", ckOn, false) })
			})
			e.Cell(c, "ck-dis", "勾选·禁用", func() {
				ui.Column(c.Key(e.Key("ck-dis-w"))).Width(g.ControlWide).Children(func() {
					v := true
					k.Check(c, e.Key("ck-dis"), "同步到另一台电脑", &v, true)
				})
			})
		})
	})

	freq := Local(e, "freq", func() int { return 1 })
	who := Local(e, "who", func() int { return 0 })
	night := Local(e, "night", func() bool { return false })
	sync := Local(e, "sync", func() bool { return false })
	names := []string{"全部", "晴枝", "雾栞", "砚", "小满"}
	counts := []int{42, 17, 11, 9, 5}
	when := []string{"每天早上 9 点", "每周一早上 9 点", "每月 1 号早上 9 点"}
	e.Section(c, "play", "试一试", func() {
		e.Play(c, "freq", func() {
			k.Segment(c, e.Key("freq"), freq, days)
			e.Line(c, "freq-out", "会在"+when[*freq]+"提醒你整理纸灯笔记。")
		})
		e.Play(c, "who", func() {
			k.Chips(c, e.Key("who"), names, who)
			e.Line(c, "who-out", fmt.Sprintf("显示 %s 的 %d 条对话。", names[*who], counts[*who]))
		})
		e.Play(c, "toggles", func() {
			ui.Column(c.Key(e.Key("toggles-w"))).Width(g.ControlWide * 2).Gap(g.SpaceTight).Children(func() {
				k.Row(c, e.Key("night"), "睡前提醒喝水", "夜里 23 点以后只在聊天里轻轻提一句", func() { k.Switch(c, e.Key("night-sw"), "睡前提醒喝水", night) })
				k.Check(c, e.Key("sync-ck"), "同步到另一台电脑", sync, false)
			})
			e.Line(c, "toggles-out", fmt.Sprintf("睡前提醒：%s；同步：%s。", map[bool]string{true: "开", false: "关"}[*night], map[bool]string{true: "已勾选", false: "没勾选"}[*sync]))
		})
	})
}
