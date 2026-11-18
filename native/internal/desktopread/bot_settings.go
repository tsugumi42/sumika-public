package desktopread

import (
	"sort"
	"strconv"
	"strings"

	"sumika-native/internal/config"
)

// PluginSettings 显示缺省和每项显式覆盖，不把号码和尚未通过的发送条件说成已经能发。
func PluginSettings(c *config.Config) []string {
	out := []string{"群：复读关、戳回去关", "可接的 QQ 私聊：戳回去开"}
	keys := make([]string, 0, len(c.Plugins.Sessions))
	for key := range c.Plugins.Sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, plugin := range []string{"repeater", "poke"} {
			if on, set := c.PluginSwitch(plugin, key); set {
				name := "复读"
				if plugin == "poke" {
					name = "戳回去"
				}
				state := "关"
				if on {
					state = "开"
				}
				out = append(out, maskedConversation(key)+" "+name+"："+state)
			}
		}
	}
	return out
}

func maskedConversation(key string) string {
	parts := strings.Split(key, ":")
	for i := 2; i < len(parts); i++ {
		parts[i] = config.MaskID(parts[i])
	}
	return strings.Join(parts, ":")
}

// DenySettings 只给出分类数量，不暴露名单里的原始身份。
func DenySettings(c *config.Config) []string {
	var ai, bot, blocked int
	for _, reason := range c.Reply.Deny {
		switch reason {
		case "ai":
			ai++
		case "bot":
			bot++
		case "blocked":
			blocked++
		}
	}
	return []string{"AI 号 " + strconv.Itoa(ai), "纯机器人 " + strconv.Itoa(bot), "拉黑 " + strconv.Itoa(blocked)}
}
