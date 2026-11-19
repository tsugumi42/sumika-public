package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/winshell"
)

// wbGroupDialog 是新界面的“新建群聊”对话框（入口在会话列表搜索框右边的“+”菜单）。
// 状态和建群动作沿用 a.gc、a.createGroup；外框、勾选、按钮用 winshell 的组件，不用旧组件库。
func (a *app) wbGroupDialog(c *ui.Context, kit *winshell.Kit) {
	g := &a.gc
	was := g.open
	kit.Dialog(c, &g.open, "新建群聊", "关闭建群", func() {
		gm, p := kit.G, kit.P
		kit.TextField(c, "group-name", &g.name, "群聊名称").Placeholder("群聊名称").Disabled(g.busy).FillWidth()
		ui.Text(c, "选择 2 到 10 个角色，主人会自动加入。").FontSize(gm.MetaSize).FixedLineHeight(gm.MetaLine).TextColor(p.Muted).Wrap()
		ui.Scroll(c.Key("group-choices")).MaxHeight(gm.HitSmall * 8).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Gap(gm.SpaceTight).Children(func() {
				for i := range g.choices {
					item := &g.choices[i]
					kit.Check(c, "group-choice-"+item.character.ID, item.character.Name+" · "+item.character.ID, &item.selected, g.busy)
				}
			})
		})
		if len(g.choices) < 2 {
			ui.Text(c, "需要至少两个资料可读的角色才能建群。").FontSize(gm.MetaSize).FixedLineHeight(gm.MetaLine).TextColor(p.Muted).Wrap()
		}
		if g.err != "" {
			ui.Text(c, g.err).FontSize(gm.MetaSize).FixedLineHeight(gm.MetaLine).TextColor(p.Danger).Wrap().FillWidth()
		}
		count := 0
		for _, item := range g.choices {
			if item.selected {
				count++
			}
		}
		label := "创建群聊"
		if g.busy {
			label = "正在创建群聊…"
		}
		ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
			kit.Button(c, "group-create", label, true, g.busy || strings.TrimSpace(g.name) == "" || count < 2 || count > 10, a.createGroup)
		})
	})
	if was && !g.open {
		if g.cancel != nil {
			g.cancel()
			g.cancel = nil
		}
		g.generation++
		g.busy = false
	}
}
