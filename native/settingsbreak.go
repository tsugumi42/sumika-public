package main

import "github.com/egoist/mygo/ui"

// settingsBreak 是设置页分组之间的分隔：一条细线加上方留白，有说明文字时说明写在线下面。
// 原来用旧组件库的“分隔带”（整行宽的粗灰条），太突兀（主人 2026-11-08 真窗口）；设计语言的分隔线用 line，不用色带。
// 设置页各分类按原型令牌重做时整页统一用这一种。
func (a *app) settingsBreak(c *ui.Context, description string) {
	if k := a.settingsKit(); k != nil {
		k.Break(c, description)
		return
	}
	t := c.Theme()
	ui.Column(c).FillWidth().Shrink(0).Padding(t.Space(3), 0, 0).Gap(t.Space(2)).Children(func() {
		ui.Box(c.Key("break-line")).FillWidth().Height(1).Background(t.Border)
		if description != "" {
			ui.Text(c, description).TextColor(t.TextMuted).Wrap()
		}
	})
}
