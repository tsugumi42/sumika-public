package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/config"
)

func TestBotSettingsSurface(t *testing.T) {
	r := realSettingsOf(t, func(c *config.Config) {
		c.Plugins.Sessions = map[string]map[string]bool{"qq:group:91001": {"repeater": true, "poke": false}}
		c.Reply.Deny = map[string]string{"qq:91004": "blocked"}
	})
	a := settingsApp(r, tabAccounts)
	tt := testerOf(2048, 1030, func(c *ui.Context) { a.realSettingsView(c) })
	for _, want := range []string{"复读与戳回去", "不接话名单", "群：复读关、戳回去关", "复读：开", "戳回去：关", "拉黑 1"} {
		found := false
		for _, text := range tt.Texts() {
			found = found || strings.Contains(text, want)
		}
		if !found {
			t.Errorf("设置页缺 %q：%v", want, tt.Texts())
		}
	}
	if strings.Contains(strings.Join(tt.Texts(), "|"), "91001") {
		t.Fatal("界面泄露完整群号")
	}
	name, _ := tt.Find("复读与戳回去")
	if name.X < 240 || name.X+name.W > 2048 {
		t.Fatalf("状态行越界：%+v", name)
	}
	t.Logf("设置行 x=%.1f width=%.1f，窗口 2048×1030", name.X, name.W)
}
