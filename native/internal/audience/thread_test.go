package audience

import "testing"

func TestIsThreadShapes(t *testing.T) {
	ok := ThreadConversation("例甲", "abc123")
	if !IsThread(ok) || ThreadID(ok) != "abc123" {
		t.Fatalf("正常的线要认出：%q %q", ok, ThreadID(ok))
	}
	for _, bad := range []string{
		"", "console:例甲", "console:例甲:topic:selftest", "console:例甲:topic:thread-", "console:例甲:topic:thread",
		"console::topic:thread-a", "console:例甲:topic:thread-a:b", "qq:例甲:topic:thread-a", "console:group:g1:topic:thread-a",
		"console:例甲:topic: thread-a", "console:例甲:topic:thread- ", "console:例甲:note:thread-a", "console:例甲:topic:Thread-a",
	} {
		if IsThread(bad) {
			t.Errorf("不该认成线：%q", bad)
		}
		if ThreadID(bad) != "" {
			t.Errorf("不是线不该有线号：%q", bad)
		}
	}
	a := Thread(ok)
	if a.PrivateOK || a.OwnerAsked || a.Channel != "console" || a.Scene != "private" || a.Outsiders() {
		t.Fatalf("线的听众：%+v", a)
	}
}

func TestWhisperConversationShapes(t *testing.T) {
	for _, src := range []string{"qq:group:123", "qq:private:1:2", "console:group:g1", "console:a:topic:t1", ""} {
		c := WhisperConversation("例甲", src)
		if !IsWhisper(c) || WhisperCharacter(c) != "例甲" || !NoMemory(c) {
			t.Errorf("来源 %q 拼出的串要认成悄悄话且记忆不收：%q", src, c)
		}
		if IsSelftest(c) || IsThread(c) {
			t.Errorf("悄悄话会话不是自检也不是线：%q", c)
		}
		if c != WhisperConversation("例甲", src) {
			t.Errorf("同来源同角色要得到同一个会话：%q", src)
		}
	}
	if WhisperConversation("例甲", "qq:group:1") == WhisperConversation("例甲", "qq:group:2") || WhisperConversation("例甲", "x") == WhisperConversation("例乙", "x") {
		t.Fatal("不同来源、不同角色要各自一个会话")
	}
	for _, bad := range []string{
		"", "console:例甲", "console:例甲:whisper", "console:例甲:whisper:", "console:例甲:whisper:abc", "console:例甲:whisper:0123456789abcdeg",
		"console:例甲:whisper:0123456789ABCDEF", "console::whisper:0123456789abcdef", "console:例甲:whisper:0123456789abcdef:x", "qq:例甲:whisper:0123456789abcdef",
		"console:例甲:topic:0123456789abcdef", "console: 例甲:whisper:0123456789abcdef", "console:例甲:whisper: 123456789abcdef",
	} {
		if IsWhisper(bad) || WhisperCharacter(bad) != "" {
			t.Errorf("不该认成悄悄话：%q", bad)
		}
	}
	if NoMemory("console:例甲") || NoMemory("console:例甲:topic:t1") {
		t.Fatal("普通会话记忆照收")
	}
}
