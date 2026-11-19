package memsource

// 记忆包判断范围用的 memory.ChannelOf 和 store 选库用的 store.PlatformOfConversation 各拆一次会话串，
// 拆法不能分叉（规格-聊天记录按平台分库 第六节）：同一个会话串两边取出的渠道要一样，取不出也两边都取不出。
// 以后有人改了一边忘了另一边，这个测试会红

import (
	"testing"

	"sumika-native/internal/memory"
	"sumika-native/internal/store"
)

func TestChannelOfMatchesPlatformOfConversation(t *testing.T) {
	for _, conv := range []string{"console:tester", "qq:group:400", "qq:private:100:200"} {
		want, err := store.PlatformOfConversation(conv)
		if err != nil {
			t.Fatalf("%s：store 取不出平台：%v", conv, err)
		}
		got, err := store.PlatformOf(memory.ChannelOf(conv))
		if err != nil || got != want {
			t.Fatalf("%s：记忆拆出的渠道给 %q %v，store 给 %q", conv, got, err, want)
		}
	}
	// 没登记的渠道，两边都取不出；不许一边有兜底
	for _, conv := range []string{"telegram:group:1", "nocolon", ":group:1", ""} {
		_, err := store.PlatformOfConversation(conv)
		_, merr := store.PlatformOf(memory.ChannelOf(conv))
		if err == nil || merr == nil {
			t.Fatalf("%s：store 报错 %v，记忆那边 %v", conv, err, merr)
		}
	}
	// 两边唯一会分叉的输入：不带冒号、本身又是登记过的渠道名（"qq"、"console"）。这不是合法的会话串；
	// store 选库时拒绝（更紧），记忆的 ChannelOf 把整串当渠道（只管范围判断，不拿来选库），所以不会选错库。
	// 记下这一处，以后谁想让记忆也拿 ChannelOf 选库，这里会提醒他两边不一样
	for _, conv := range []string{"qq", "console"} {
		if _, err := store.PlatformOfConversation(conv); err == nil {
			t.Fatalf("%q 不是合法的会话串，store 选库该拒绝", conv)
		}
		if memory.ChannelOf(conv) != conv {
			t.Fatalf("记忆的 ChannelOf(%q) = %q：这一处的已知差别变了，回头看上面的说明", conv, memory.ChannelOf(conv))
		}
	}
}
