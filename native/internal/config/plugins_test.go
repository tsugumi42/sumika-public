package config

// 插件开关的配置（规格-插件两层 第二节第 3 条）：plugins.<id>.enabled 能读、能看出没写；sessions 不被当成插件。

import "testing"

func TestPluginSwitchParsed(t *testing.T) {
	t.Parallel()
	c, err := Parse([]byte("plugins:\n  sessions: {}\n  linkplugin:\n    enabled: false\n  userhooks:\n    enabled: true\n  other:\n    note: 备注\n"), "t.yml")
	if err != nil {
		t.Fatal(err)
	}
	if on, set := c.PluginEnabled("linkplugin"); on || !set {
		t.Fatalf("linkplugin 写了 false，应该是 (false, true)，实际 (%v, %v)", on, set)
	}
	if on, set := c.PluginEnabled("userhooks"); !on || !set {
		t.Fatalf("userhooks 写了 true，实际 (%v, %v)", on, set)
	}
	if _, set := c.PluginEnabled("other"); set {
		t.Fatal("other 没写 enabled，应该是没写")
	}
	if c.Plugins.Items["other"].Settings["note"] != "备注" {
		t.Fatalf("设置字段应该保留：%#v", c.Plugins.Items["other"].Settings)
	}
	if _, ok := c.Plugins.Items["sessions"]; ok {
		t.Fatal("sessions 不能当插件")
	}
}

func TestPluginSwitchRejectsBadType(t *testing.T) {
	t.Parallel()
	if _, err := Parse([]byte("plugins:\n  linkplugin:\n    enabled: 有时候\n"), "t.yml"); err == nil {
		t.Fatal("enabled 不是布尔应该报错")
	}
}

func TestPluginSessionsStillParsed(t *testing.T) {
	t.Parallel()
	c, err := Parse([]byte("plugins:\n  sessions:\n    \"qq:group:12345\": {repeater: true}\n"), "t.yml")
	if err != nil {
		t.Fatal(err)
	}
	if on, set := c.PluginSwitch("repeater", "qq:group:12345"); !on || !set {
		t.Fatalf("会话开关应该照常读：(%v, %v)", on, set)
	}
}
