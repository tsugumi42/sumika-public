package catalog

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// motionDemo：选中和切换。五种“选中的那块滑过去”的控件各摆一个，每样都能点；
// 顶栏的“动画速度”慢放、“重放”（R）把状态拨回初始，方便反复看。
func motionDemo(c *ui.Context, e Env) {
	k := e.Kit
	seg := Local(e, "seg", func() int { return 0 })
	chip := Local(e, "chip", func() int { return 0 })
	multi := Local(e, "multi", func() []bool { return []bool{true, false, true, false} })
	tab := Local(e, "tab", func() int { return 0 })
	cat := Local(e, "cat", func() string { return "memory" })
	page := Local(e, "page", func() string { return "chat" })

	e.Section(c, "seg", "分段：选中块滑过去，位移回弹、宽度恢复", func() {
		k.Segment(c, e.Key("seg"), seg, []string{"每天", "每周", "每月", "每年"})
	})
	e.Section(c, "chips", "筛选片：单选的底色滑过去；多选的每片自己淡入淡出", func() {
		e.Cells(c, "chips-row", func() {
			e.Cell(c, "chips-one", "单选", func() { k.Chips(c, e.Key("chip"), []string{"全部", "晴枝", "雾栞", "砚", "小满"}, chip) })
			e.Cell(c, "chips-multi", "多选", func() { k.MultiChips(c, e.Key("multi"), []string{"事实", "偏好", "承诺", "界限"}, *multi) })
		})
	})
	tabs := []winshell.IconTab{
		{Label: "聊天", Icon: winshell.IconChat},
		{Label: "记忆", Icon: winshell.IconMemory},
		{Label: "运行", Icon: winshell.IconRuntime},
		{Label: "设置", Icon: winshell.IconSettings},
	}
	e.Section(c, "tabs", "图标页签：选中的展开成图标加文字，旁边的圆钮让位；←→ 切换", func() {
		k.IconTabs(c, e.Key("tabs"), tabs, tab)
		e.Line(c, "tabs-out", fmt.Sprintf("现在在“%s”。", tabs[*tab].Label))
	})
	cats := []winshell.Category{
		{ID: "memory", Label: "记忆和学习", Icon: chatui.Icon("brain", false)},
		{ID: "plugins", Label: "插件", Icon: chatui.Icon("layers", false)},
		{ID: "look", Label: "外观", Icon: chatui.Icon("eye", false)},
	}
	nav := []winshell.NavItem{
		{ID: "home", Label: "首页", Icon: winshell.IconHome},
		{ID: "chat", Label: "消息", Icon: winshell.IconChat},
		{ID: "memory", Label: "档案", Icon: winshell.IconMemory},
		{ID: "settings", Label: "偏好", Icon: winshell.IconSettings},
	}
	e.Section(c, "side", "分类栏和导航栏：选中的块上下滑", func() {
		e.Cells(c, "side-row", func() {
			e.Cell(c, "cats", "设置页左栏的分类行", func() {
				ui.Column(c.Key(e.Key("cats-w"))).Width(winshell.SettingsListWidth).Children(func() {
					k.CategoryList(c, e.Key("cats"), cats, *cat, func(id string) { *cat = id })
				})
			})
			e.Cell(c, "rail", "导航栏的图标", func() {
				k.NavRail(c, e.Key("rail"), nav, *page, func(id string) { *page = id })
			})
		})
	})
}
