package app

// 配置里写错的插件 id（规格-插件两层 第二节第 3 条）：plugins.linkreader 这种拼错的节不生效，要能被找出来。
// 放回旧代码（没有 unknownPluginIDs）就编译不过。用户插件（plugins/<id>/）还没接，这里只看内置插件。

import (
	"reflect"
	"testing"

	"sumika-native/internal/config"
	"sumika-native/internal/turn"
)

func TestUnknownPluginIDsListsTypos(t *testing.T) {
	t.Parallel()
	items := map[string]config.PluginSwitch{"link-reader": {}, "linkreader": {}, "life-tools": {}, "link_reader": {}}
	statuses := []turn.PluginStatus{{ID: "link-reader"}, {ID: "life-tools"}, {ID: "userhooks"}}
	got := unknownPluginIDs(items, statuses)
	want := []string{"link_reader", "linkreader"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("拼错的 id 应该列出来，期望 %v，实际 %v", want, got)
	}
	if got := unknownPluginIDs(map[string]config.PluginSwitch{"life-tools": {}}, statuses); len(got) != 0 {
		t.Fatalf("写对的不该列出来：%v", got)
	}
}
