package app

// 内置插件的装配（规格-插件两层 第二节第 2、3 条）：登记进插件表，开关看配置（plugins.<id>.enabled，没写用装配处给的默认），
// 然后当场调整。一个插件启用失败只记日志，别的照常，不再因为它中止启动。

import (
	"fmt"
	"sort"

	"sumika-native/internal/config"
	"sumika-native/internal/turn"
)

// mountPlugin 登记并按开关启用一个内置插件；Setup 出错、panic 不返回错误（插件表已经记成"启用失败"、写了日志）
func (a *App) mountPlugin(p turn.Plugin, def bool) error {
	if err := a.hub.Register(p); err != nil {
		return fmt.Errorf("登记插件 %s：%w", p.ID(), err)
	}
	on, set := a.cfg.PluginEnabled(p.ID())
	if !set {
		on = def
	}
	a.hub.SetEnabled(p.ID(), on)
	return nil
}

// pluginActive 这个插件现在在跑（起来了）
func (a *App) pluginActive(id string) bool {
	if a.hub == nil {
		return false
	}
	for _, x := range a.hub.RegisteredPlugins() {
		if x == id {
			return true
		}
	}
	return false
}

// unknownPluginIDs 找出配置里写了 plugins.<id>、但插件表里没有这个 id 的（多半是拼错，比如 linkreader）。
// 这样的节开关不生效，启动也不报，所以启动时记一行。第二步的用户插件（plugins/<id>/）还没接，接上后要把它们算进已知 id。
func unknownPluginIDs(items map[string]config.PluginSwitch, statuses []turn.PluginStatus) []string {
	known := map[string]bool{}
	for _, s := range statuses {
		known[s.ID] = true
	}
	var out []string
	for id := range items {
		if !known[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// warnUnknownPlugins 启动末尾调一次：把拼错的插件 id 记进日志
func (a *App) warnUnknownPlugins() {
	if a.hub == nil {
		return
	}
	for _, id := range unknownPluginIDs(a.cfg.Plugins.Items, a.hub.PluginStatuses()) {
		a.logf("配置里的 plugins.%s 没有对应的内置插件，这一节不生效（拼写对吗？现有插件的 id：link-reader、life-tools、userhooks）", id)
	}
}
