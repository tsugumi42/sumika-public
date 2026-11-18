package app

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/settingsreg"
)

// 同一个设置项“生不生效”，设置页的投影、Agent 设置工具的白名单、自知投影的暴露名单、注册表四处说同一句话（质检 X-01、X-10）。
// 注册表是唯一的出处：另外三处只许引用它，不许自己另说一份。
func TestSettingsTruthAgreesAcrossSurfaces(t *testing.T) {
	// 设置页投影里的键
	c := config.Defaults()
	c.Characters = map[string]config.CharacterModels{"anne": {}}
	res, err := desktopread.SettingsReader{Load: func(context.Context) (config.Config, error) { return c, nil }}.ReadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	page := map[string]api.Result[api.Setting]{}
	for _, it := range res.Value.Items.Items {
		page[it.Value.Key] = it
	}
	// 设置页上“程序当下的状态”不是配置键，和注册表里同名的规划项含义不同（见 desktopread 的同名测试）
	runtimeStatus := map[string]bool{"occupation": true, "platform.selectedAccount": true, "platform.connection": true, "notifications.alerts": true, "notifications.autostart": true, "shortcuts.registered": true}
	// 自知投影暴露的键：这是一张暴露名单，名字、说明、生效方式都来自注册表
	exposed := map[string]bool{}
	for _, e := range settingsreg.Entries() {
		key := strings.Replace(e.Key, "*", "anne", 1)
		if clientSettingAllowed(key, "anne", "console:anne") {
			exposed[key] = true
		}
	}
	// 设置工具白名单
	tool := map[string]bool{}
	for _, s := range settingToolSpecs {
		tool[s.Key] = true
	}
	if len(exposed) < 20 || len(tool) < 10 {
		t.Fatalf("名单被读小了：自知 %d 个、设置工具 %d 个", len(exposed), len(tool))
	}
	all := map[string]bool{}
	for k := range page {
		all[k] = true
	}
	for k := range exposed {
		all[k] = true
	}
	for k := range tool {
		all[k] = true
	}
	multi := 0
	for key := range all {
		inPage := page[key].Value.Key != "" && page[key].Value.Source != "runtime"
		places := 0
		for _, in := range []bool{inPage, exposed[key], tool[key]} {
			if in {
				places++
			}
		}
		entry, ok := settingsreg.Lookup(key)
		if runtimeStatus[key] {
			continue
		}
		if places == 0 {
			continue // 只出现在设置页的“程序当下的状态”里（appearance.*、data.*），不是配置键
		}
		if !ok {
			t.Errorf("%s 出现在名单里，注册表没登记", key)
			continue
		}
		if places >= 2 {
			multi++
		}
		if inPage && page[key].Value.Supported == entry.Inert() {
			t.Errorf("%s 设置页说已启用=%v，注册表说 %s", key, page[key].Value.Supported, entry.Effect)
		}
		if (exposed[key] || tool[key]) && entry.Inert() {
			t.Errorf("%s 注册表说尚未生效，却还在对 Agent 暴露或可由设置工具改", key)
		}
		if spec, ok := settingToolSpec(key); ok {
			restartInSpec := strings.Contains(spec.Effect, "重启")
			restartInRegistry := entry.Effect == settingsreg.EffectRestartReceive || entry.Effect == settingsreg.EffectRestartApp
			if restartInSpec != restartInRegistry {
				t.Errorf("%s 设置工具说生效：%q，注册表说 %s", key, spec.Effect, entry.Effect)
			}
			if entry.ReadOnly {
				t.Errorf("%s 在设置工具白名单里，注册表却说只读", key)
			}
		}
	}
	if multi < 10 {
		t.Fatalf("同时出现在两处以上的键只有 %d 个，交叉核对没覆盖到", multi)
	}
}
