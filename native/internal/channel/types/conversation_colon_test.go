package types

import (
	"strings"
	"testing"
)

// String 用冒号拼、ParseConversation 用冒号拆：渠道、会话号、她的号任何一段带冒号，往返就会变样。
// 每段各测 带冒号 / 空 / 正常；Valid 通过的一定要 String → Parse 原样回来
func TestConversationValidRejectsColonAndRoundTrips(t *testing.T) {
	cases := []struct {
		name  string
		conv  Conversation
		wants string // 空表示合法
	}{
		{"群正常", Conversation{Channel: "qq", Scene: SceneGroup, ID: "g1"}, ""},
		{"私聊正常", Conversation{Channel: "qq", Scene: ScenePrivate, Self: "bot-a", ID: "u1"}, ""},
		{"群渠道带冒号", Conversation{Channel: "q:q", Scene: SceneGroup, ID: "g1"}, "渠道"},
		{"群会话号带冒号", Conversation{Channel: "qq", Scene: SceneGroup, ID: "g:1"}, "会话号"},
		{"私聊渠道带冒号", Conversation{Channel: "q:q", Scene: ScenePrivate, Self: "bot-a", ID: "u1"}, "渠道"},
		{"私聊会话号带冒号", Conversation{Channel: "qq", Scene: ScenePrivate, Self: "bot-a", ID: "u:1"}, "会话号"},
		{"私聊她的号带冒号", Conversation{Channel: "qq", Scene: ScenePrivate, Self: "bot:a", ID: "u1"}, "她的号"},
		{"只剩冒号的会话号", Conversation{Channel: "qq", Scene: SceneGroup, ID: ":"}, "会话号"},
		{"渠道空", Conversation{Scene: SceneGroup, ID: "g1"}, "渠道"},
		{"会话号空", Conversation{Channel: "qq", Scene: SceneGroup}, "会话号"},
		{"私聊她的号空", Conversation{Channel: "qq", Scene: ScenePrivate, ID: "u1"}, "她的号"},
	}
	for _, c := range cases {
		err := c.conv.Valid()
		if c.wants != "" {
			if err == nil || !strings.Contains(err.Error(), c.wants) {
				t.Errorf("%s: Valid = %v，应报含 %q 的错", c.name, err, c.wants)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: Valid 报错 %v", c.name, err)
			continue
		}
		back, err := ParseConversation(c.conv.String())
		if err != nil || back != c.conv {
			t.Errorf("%s: 往返得到 %+v（%v），应原样回来 %+v", c.name, back, err, c.conv)
		}
	}
}
