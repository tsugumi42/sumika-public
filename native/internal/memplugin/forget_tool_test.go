package memplugin

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/memory"
	"sumika-native/internal/store/memsource"
)

// memory_forget 走正式内核：只有主人的私聊工具表里有它，群里和自检会话没有；私聊里调了就忘掉，说的是"没有真删"。

func TestKernelForgetEndToEnd(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	source, err := memsource.New(k.st, character)
	if err != nil {
		t.Fatal(err)
	}
	m, err := memory.Open(context.Background(), migratedFile(t, nil), nil, nil, memory.Options{Writable: true, Character: character, Events: source, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	k.enableChars(map[string]*memory.Memory{character: m}, func() []string { return []string{character} })

	k.mu.Lock()
	k.calls = []testToolCall{{"remember", `{"kind":"fact","text":"主人喜欢蓝色纸鹤","source_quote":"蓝色纸鹤","temporal":"stable"}`}, {}}
	k.mu.Unlock()
	k.send("记住我喜欢蓝色纸鹤")
	if got := k.toolResult(1); got != "记住了。" {
		t.Fatalf("先记一条：%q", got)
	}
	items := m.Items()
	if len(items) != 1 {
		t.Fatalf("该有一条：%d", len(items))
	}
	code := items[0].Ref[len(items[0].Ref)-6:]

	start := k.requests()
	k.mu.Lock()
	k.calls = []testToolCall{{"memory_forget", `{"id":"#` + code + `","reason":"主人说不喜欢了","source_quote":"忘掉蓝色纸鹤"}`}, {}}
	k.mu.Unlock()
	k.send("请忘掉蓝色纸鹤那条")
	if d := k.toolDescriptions(start)["memory_forget"]; d == "" || strings.HasPrefix(d, readOnlyForgetNote) {
		t.Fatalf("私聊的工具表里该有 memory_forget（写入已开，不带只读说明）：%q", d)
	}
	if got := k.toolResult(start + 1); !strings.Contains(got, "已经忘掉") || !strings.Contains(got, "没有真删") {
		t.Fatalf("结果：%q", got)
	}
	if n := len(m.Items()); n != 0 {
		t.Fatalf("忘掉以后不该再被想起：%d 条", n)
	}
}

func TestKernelForgetNotOfferedInGroupsOrReadOnlyReports(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	m := openMemory(t, fixedNow, nil, nil) // 只读
	k.enableChars(map[string]*memory.Memory{character: m}, func() []string { return []string{character} })
	k.mu.Lock()
	k.calls = []testToolCall{{"memory_forget", `{"id":"#ABCDEF","reason":"测试","source_quote":"忘掉"}`}, {}}
	k.mu.Unlock()
	k.send("请忘掉那条")
	if d := k.toolDescriptions(0)["memory_forget"]; !strings.HasPrefix(d, readOnlyForgetNote) {
		t.Fatalf("只读时描述要说明忘不掉：%q", d)
	}
	if got := k.toolResult(1); !strings.Contains(got, "只读") {
		t.Fatalf("只读时的结果：%q", got)
	}
	for _, a := range []audience.Audience{
		{Channel: "qq", Conversation: groupA, Scene: "group", OwnerAsked: true},
		{Channel: "qq", Conversation: groupA, Scene: "group"},
	} {
		k.say(groupA, a, memory.ScopeKey(a), "owner", "owner", "请忘掉那条")
		if _, has := k.toolDescriptions(k.requests() - 1)["memory_forget"]; has {
			t.Fatalf("群里不该有 memory_forget：%+v", a)
		}
	}
}

func TestSelftestConversationCannotForget(t *testing.T) {
	t.Parallel()
	conv := audience.SelftestConversation(character, audience.SelftestTopicID(character, ""))
	k := newKernel(t)
	m := writableMemory(t)
	k.enableChars(map[string]*memory.Memory{character: m}, func() []string { return []string{character} })
	k.mu.Lock()
	k.calls = []testToolCall{{"memory_forget", `{"id":"#ABCDEF","reason":"测试","source_quote":"忘掉"}`}, {}}
	k.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if o := <-k.hub.SendLocal(ctx, character, conv, "请忘掉那条", nil); o.Err != nil {
		t.Fatal(o.Err)
	}
	if _, has := k.toolDescriptions(0)["memory_forget"]; has {
		t.Fatal("自检会话的工具表里不该有 memory_forget")
	}
}
