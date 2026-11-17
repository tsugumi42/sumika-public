package config

import (
	"strings"
	"testing"
)

func TestBotConfiguration(t *testing.T) {
	t.Setenv("SUMIKA_CONFIG", write(t, `characters:
  invented: {aliases: [小澪, DS]}
plugins:
  sessions:
    "qq:group:91001": {repeater: true, poke: false}
    "qq:private:91002:91003": {poke: false}
reply:
  deny: {"qq:91004": ai, "qq:91005": bot, "qq:91006": blocked}
`))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if on, set := c.PluginSwitch("repeater", "qq:group:91001"); !on || !set {
		t.Fatal("群覆盖未读取")
	}
	if on, set := c.PluginSwitch("poke", "qq:private:91002:91003"); on || !set {
		t.Fatal("显式关丢失")
	}
	if _, set := c.PluginSwitch("poke", "qq:group:91007"); set {
		t.Fatal("未设被当成覆盖")
	}
	if len(c.Characters["invented"].Aliases) != 2 || c.Reply.Deny["qq:91004"] != "ai" || c.Reply.Deny["qq:91005"] != "bot" || c.Reply.Deny["qq:91006"] != "blocked" {
		t.Fatal("别名或三类名单未读取")
	}
	for _, tc := range []struct{ text, key string }{
		{`plugins: {sessions: {"qq:group:91001": {poke: wat}}}`, "YAML"},
		{`plugins: {sessions: {"qq:group:0": {poke: true}}}`, "plugins.sessions"},
		{`plugins: {sessions: {"qq:group:91001": {unknown: true}}}`, "plugins.sessions"},
		{`reply: {deny: {"qq:0": ai}}`, "reply.deny"},
		{`reply: {deny: {"qq:": bot}}`, "reply.deny"},
		{`reply: {deny: {"qq:91004": unknown}}`, "reply.deny"},
		{`reply: {deny: {"qq:91004": null}}`, "reply.deny"},
		{`characters: {invented: {aliases: [" "]}}`, "aliases"},
	} {
		t.Setenv("SUMIKA_CONFIG", write(t, tc.text))
		if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s: %v", tc.text, err)
		}
	}
}
