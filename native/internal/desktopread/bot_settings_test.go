package desktopread

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
)

func TestBotSettingsShowDefaultsAndOverrides(t *testing.T) {
	t.Parallel()
	c := config.Defaults()
	c.Plugins.Sessions = map[string]map[string]bool{"qq:group:91001": {"repeater": true, "poke": false}}
	c.Characters["invented"] = config.CharacterModels{Aliases: []string{"小澪", "DS", "91004"}}
	c.Reply.Deny = map[string]string{"qq:91004": "ai", "qq:91005": "bot", "qq:91006": "blocked"}
	r, err := (SettingsReader{Load: func(context.Context) (config.Config, error) { return c, nil }}).ReadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]api.Setting{}
	for _, it := range r.Value.Items.Items {
		if it.State == api.Ready {
			items[it.Value.Key] = it.Value
		}
	}
	got := items["plugins.sessions"].Display(items["plugins.sessions"].Value)
	for _, want := range []string{"群：复读关、戳回去关", "可接的 QQ 私聊：戳回去开", "复读：开", "戳回去：关"} {
		if !strings.Contains(got, want) {
			t.Errorf("插件状态缺 %s：%s", want, got)
		}
	}
	if got := items["characters.invented.aliases"].Display(items["characters.invented.aliases"].Value); got != "小澪、DS、***04" {
		t.Fatal(got)
	}
	if got := items["reply.deny"].Display(items["reply.deny"].Value); got != "AI 号 1、纯机器人 1、拉黑 1" {
		t.Fatal(got)
	}
	for _, id := range []string{"91001", "91004", "91005", "91006"} {
		if strings.Contains(fmt.Sprint(r), id) {
			t.Fatal("号码未脱敏")
		}
	}
}
