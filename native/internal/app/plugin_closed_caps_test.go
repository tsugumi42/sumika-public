package app

// 关着的插件也要出现在客户端自知的能力列表里，状态是“已关闭”（规格-插件两层 第二节：她能看到装了哪些）。
// 放回旧代码（只列 RegisteredPlugins，关着的 life-tools 整行不出现）就红：找不到 life-tools 这一行。

import (
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/lifetools"
)

func TestClosedPluginListedAsClosed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.extraConfig = "plugins:\n  life-tools:\n    enabled: false\n"
	e.writeConfig(testToken)
	a := e.open()
	t.Cleanup(func() { a.Close() })
	scope := kernel.Scope{CharacterID: testChar, Conversation: "console:" + testChar, ScopeID: "owner", Platform: "local", Channel: "console", AccountID: "desktop", Scene: "private", ActorKind: "owner", ActorID: "owner"}
	aud := audience.Audience{Channel: "console", Scene: "private", Conversation: scope.Conversation, PrivateOK: true, OwnerAsked: true}
	var got *struct{ state, reason string }
	for _, c := range a.clientCapabilities(scope, aud, a.currentLinks()) {
		if c.ID == lifetools.ID {
			got = &struct{ state, reason string }{c.State, c.Reason}
		}
	}
	if got == nil {
		t.Fatal("life-tools 关着，能力列表里也应该有这一行")
	}
	if got.state != "已关闭" {
		t.Fatalf("life-tools 关着，状态应该是已关闭，实际 %q（%s）", got.state, got.reason)
	}
}
