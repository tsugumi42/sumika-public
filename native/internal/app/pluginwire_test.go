package app

// 三个样板插件的装配（规格-插件两层 第八节第一步第三条）：链接解析、生活工具、用户钩子都有清单；能在插件表里关、再开；
// 关着时对应的工具从清单里消失，开回来又出现。生活工具停了再起，假日数据换一个新的（旧的已经关了）。
// 放回旧代码（三个都无条件 Use，没有开关）就红：配置写了 enabled: false 时生活工具照样在跑。

import (
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/lifetools"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/userhooks"
)

func TestSamplePluginsManifestToggleAndTools(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.extraConfig = "plugins:\n  life-tools:\n    enabled: false\n"
	e.writeConfig(testToken)
	a := e.open()
	t.Cleanup(func() { a.Close() })
	aud := audience.Audience{Channel: "console", Scene: "private", PrivateOK: true, OwnerAsked: true, Conversation: "console:private:owner"}

	state := func(id string) string {
		for _, s := range a.hub.PluginStatuses() {
			if s.ID == id {
				return s.State
			}
		}
		return "missing"
	}
	has := func(name string) bool {
		for _, n := range a.hub.ToolNames(aud) {
			if n == name {
				return true
			}
		}
		return false
	}

	for _, s := range a.hub.PluginStatuses() {
		switch s.ID {
		case lifetools.ID, linkplugin.ID, userhooks.ID:
			if s.Name == "" || s.Sub == "" {
				t.Errorf("%s 的清单应该有名字和说明：%+v", s.ID, s)
			}
		}
	}

	// 配置写了 life-tools 关：工具不在，状态 off
	if got := state(lifetools.ID); got != "off" {
		t.Fatalf("life-tools 配置关着，应该 off，实际 %s", got)
	}
	if has(lifetools.ToolCalendar) {
		t.Fatal("life-tools 关着，calendar 不该在工具清单里")
	}

	// 打开：工具回来
	a.hub.SetEnabled(lifetools.ID, true)
	if got := state(lifetools.ID); got != "active" {
		t.Fatalf("打开以后应该 active，实际 %s", got)
	}
	for _, name := range []string{lifetools.ToolCalendar, lifetools.ToolWeather, lifetools.ToolExchangeRate} {
		if !has(name) {
			t.Errorf("打开以后 %s 应该在工具清单里", name)
		}
	}
	first := a.life.Holidays()

	// 再关、再开：工具消失又回来；假日数据换成新的（停掉的已经关了，不能拿来用）
	a.hub.SetEnabled(lifetools.ID, false)
	if has(lifetools.ToolCalendar) {
		t.Fatal("关掉以后 calendar 应该消失")
	}
	a.hub.SetEnabled(lifetools.ID, true)
	if !has(lifetools.ToolCalendar) {
		t.Fatal("再打开 calendar 应该回来")
	}
	if a.life.Holidays() == first {
		t.Fatal("停过又起，假日数据应该换成新的")
	}

	// 链接解析默认开：详情工具在；关掉就没了
	if got := state(linkplugin.ID); got != "active" {
		t.Fatalf("链接解析默认开，实际 %s", got)
	}
	if !has("read_link_detail") {
		t.Fatal("链接解析开着，详情工具应该在")
	}
	a.hub.SetEnabled(linkplugin.ID, false)
	if has("read_link_detail") {
		t.Fatal("链接解析关掉以后，详情工具应该消失")
	}
	a.hub.SetEnabled(linkplugin.ID, true)

	// 用户钩子：hooks.enabled 默认开，插件开着
	if got := state(userhooks.ID); got != "active" {
		t.Fatalf("用户钩子默认开，实际 %s", got)
	}
}
