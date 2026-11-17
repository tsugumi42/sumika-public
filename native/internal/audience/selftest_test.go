package audience

import "testing"

func TestIsSelftest(t *testing.T) {
	for conv, want := range map[string]bool{
		"console:suzu:topic:selftest":                          true,
		"console:suzu:topic:selftest-suzu":                     true,
		"console:suzu:topic:selftest-suzu-a":                   true,
		SelftestConversation("甲", SelftestTopicID("甲", "")):    true,
		SelftestConversation("甲", SelftestTopicID("甲", "x-1")): true,
		"console:suzu":                         false, // 主窗口日常
		"console:suzu:topic:t1":                false,
		"console:suzu:topic:selftestx":         false, // 前缀后面必须是连字符
		"console:suzu:topic:Selftest":          false, // 大小写不同不算
		"console:suzu:topic:":                  false,
		"console::topic:selftest":              false,
		"console:suzu:topic:selftest:x":        false, // 多一段
		"console:suzu:group:selftest":          false, // 不是话题
		"console:group:selftest":               false,
		"qq:suzu:topic:selftest":               false, // 渠道不是 console
		"qq:group:10001":                       false,
		"console:suzu:topic: selftest":         false,
		"console:suzu:topic:selftest\n":        false,
		"console:suzu:topic:selftest-suzu\x00": true, // 形状上仍是前缀；会话串由服务端按角色拼，不收外来的
		"":                                     false,
		"selftest":                             false,
	} {
		if got := IsSelftest(conv); got != want {
			t.Errorf("%q：得到 %v，应是 %v", conv, got, want)
		}
	}
}
