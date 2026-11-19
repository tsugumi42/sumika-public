package main

// 聊天框上方的“模型来源连不上”一行（规格-模型配置 6.1 第 7 条）：
// 目录取不到时写原因之外的一句话，带“去设置”；错误原文太长，只留在模型页。
// 状态来自宿主状态里的 CatalogError / CatalogSource（host.go 的 watchCatalog 从后台当前目录带过来），不另开协议。

import (
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"
)

// catalogWhere 把配置里的模型网关地址缩成提示里的写法：http://127.0.0.1:3425/v1 → 127.0.0.1:3425
// （规格-模型配置 6.1 的示例就是主机和端口）。解析不出来就原样写，总比空着强。
func catalogWhere(base string) string {
	base = strings.TrimSpace(base)
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}

// catalogLine 是那一行的文字：地址取现在配置的模型网关地址，不带错误原文。
func (s hostState) catalogLine() string {
	where := catalogWhere(s.CatalogSource)
	if where == "" {
		where = "模型服务"
	}
	return "模型来源连不上（" + where + "）：打开 Magpie，或在设置 · 模型里换地址"
}

// workbenchCatalogNote 画“模型来源连不上”一行和“去设置”（点了打开设置的模型页）。
// 只在能发消息、又没有“只能看 / 已停止”这类更高优先级的说明时调用（workbench_composer.go）。
func (a *app) workbenchCatalogNote(c *ui.Context) {
	text := a.host.catalogLine()
	open := func() { a.rail, a.realTab = 4, tabModels }
	if kit := a.workbench.shell.kit; kit != nil {
		kit.NoteBarAction(c, "composer-note", text, "去设置", open)
		return
	}
	th := c.Theme()
	ui.Row(c.Key("composer-note")).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c.Key("composer-note-text"), text).Font(th.Font).TextColor(th.TextMuted).Wrap().Grow(1).MinWidth(0)
		a.wbButton(c, "composer-note-settings", "去设置", false, open)
	})
}
