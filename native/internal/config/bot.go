package config

import (
	"fmt"
	"strings"

	"sumika-native/internal/channel/types"
)

// PluginSwitch 只读这份启动配置；未设不能当显式关闭，否则会抹掉私聊戳回去的默认。
func (c *Config) PluginSwitch(plugin, conversation string) (on, set bool) {
	on, set = c.Plugins.Sessions[conversation][plugin]
	return
}

func (c *Config) checkBot() []string {
	var bad []string
	for key, switches := range c.Plugins.Sessions {
		conv, err := types.ParseConversation(key)
		if err != nil || conv.ID == "0" || conv.Self == "0" {
			bad = append(bad, "plugins.sessions：会话 ID 必须是 平台:group:群号 或 平台:private:她的号:对方号")
			continue
		}
		if conv.Channel == "qq" && (!positiveDigits(conv.ID) || (conv.Scene == types.ScenePrivate && !positiveDigits(conv.Self))) {
			bad = append(bad, "plugins.sessions：QQ 会话的号码必须是数字串")
		}
		for plugin := range switches {
			if plugin != "repeater" && plugin != "poke" {
				bad = append(bad, "plugins.sessions：插件名只支持 repeater / poke")
			}
		}
	}
	for person, reason := range c.Reply.Deny {
		ch, id, ok := strings.Cut(person, ":")
		if !ok || ch == "" || id == "" || id == "0" || strings.ContainsAny(id, ": \t\r\n") || (ch == "qq" && !positiveDigits(id)) {
			bad = append(bad, "reply.deny：身份必须是 平台:号，QQ 号必须是非零数字串")
		}
		if reason != "ai" && reason != "bot" && reason != "blocked" {
			bad = append(bad, "reply.deny：原因只支持 ai / bot / blocked")
		}
	}
	for character, cfg := range c.Characters {
		for _, alias := range cfg.Aliases {
			if strings.TrimSpace(alias) != alias || alias == "" {
				bad = append(bad, fmt.Sprintf("characters.%s.aliases：别名不能空或带首尾空白", character))
			}
		}
	}
	return bad
}

func positiveDigits(id string) bool {
	return digitsOf(id) && strings.Trim(id, "0") != ""
}
