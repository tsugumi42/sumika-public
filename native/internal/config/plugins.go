package config

// 插件配置（规格-插件两层 第二节第 3 条）：plugins.<id>.enabled 是开关，plugins.<id>.<字段> 是插件清单里 Settings 的值。
// plugins.sessions 是 Go 内置拦截插件的会话开关（旧的，保持原样）；sessions 不能当插件 id。
// 没写 enabled 的，由装配处决定默认（清单默认开，userhooks 沿用 hooks.enabled）。

import (
	"fmt"

	"github.com/goccy/go-yaml"
)

// PluginsConfig 是 plugins 一节
type PluginsConfig struct {
	Sessions map[string]map[string]bool // 完整会话 ID → 插件名 → 开关
	Items    map[string]PluginSwitch    // 插件 id → 开关和设置（除 sessions 以外的键）
}

// PluginSwitch 是一个插件的一节：Enabled 没写是 nil，Settings 是除 enabled 以外的字段
type PluginSwitch struct {
	Enabled  *bool
	Settings map[string]any
}

// UnmarshalYAML 拆开 sessions 和插件节
func (p *PluginsConfig) UnmarshalYAML(b []byte) error {
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return err
	}
	var named struct {
		Sessions map[string]map[string]bool `yaml:"sessions"`
	}
	if err := yaml.Unmarshal(b, &named); err != nil {
		return err
	}
	p.Sessions = named.Sessions
	p.Items = map[string]PluginSwitch{}
	for id, v := range raw {
		if id == "sessions" {
			continue
		}
		sec, ok := v.(map[string]any)
		if !ok && v != nil {
			return fmt.Errorf("plugins.%s 应该是一节（键值），实际是 %T", id, v)
		}
		ps := PluginSwitch{Settings: map[string]any{}}
		for k, x := range sec {
			if k == "enabled" {
				on, ok := x.(bool)
				if !ok {
					return fmt.Errorf("plugins.%s.enabled 只能是 true 或 false", id)
				}
				ps.Enabled = &on
				continue
			}
			ps.Settings[k] = x
		}
		p.Items[id] = ps
	}
	return nil
}

// PluginEnabled 是配置里写的开关：set 为假表示没写，由调用方决定默认
func (c *Config) PluginEnabled(id string) (on, set bool) {
	ps, ok := c.Plugins.Items[id]
	if !ok || ps.Enabled == nil {
		return false, false
	}
	return *ps.Enabled, true
}
