package app

// “记住”默认开、端到端（规格-记忆库从零建库 验收 2、ADR-377 第 1 条）：
// 全新数据目录加一张卡、配置里不写 memory.write，本地私聊里让模型调用 remember；
// 关掉程序、用同一个数据目录重开，memory_search 还能找到这一条。
// 走的是真实工具路径（模型回工具调用 → memplugin → 写库），不直接调 Memory.Remember。
// 号、角色、话都是编的；数据目录只用 t.TempDir()；不连真协议端。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"sumika-native/internal/llm/llmtest"
)

// bodyAt 取假网关收到的第 i 次请求体（从 0 数）
func bodyAt(t *testing.T, e *testEnv, i int) string {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if i >= len(e.bodies) {
		t.Fatalf("假网关只收到 %d 次请求，要第 %d 次", len(e.bodies), i+1)
	}
	return e.bodies[i]
}

// lastToolMessage 取请求里最后一条 tool 消息的内容（工具结果）
func lastToolMessage(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("请求体不是 JSON：%v：%s", err, body)
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "tool" {
			continue
		}
		var text string
		if err := json.Unmarshal(req.Messages[i].Content, &text); err != nil {
			return string(req.Messages[i].Content)
		}
		return text
	}
	t.Fatalf("请求里没有 tool 消息：%s", body)
	return ""
}

func TestRememberDefaultOnAndRecallAfterRestart(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{
		Models: models,
		Replies: []llmtest.Reply{
			llmtest.Call("m1", "remember", `{"kind":"preference","text":"主人喜欢喝乌龙茶","source_quote":"我喜欢喝乌龙茶"}`),
			llmtest.Text("记好了。"),
			llmtest.Call("m2", "memory_search", `{"query":"乌龙茶"}`),
			llmtest.Text("你喜欢乌龙茶。"),
		},
	}, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv

	// 第一次启动：不写 memory.write，角色该自动建库、默认开 remember
	a := e.open()
	d := a.Desktop()
	t.Cleanup(func() { d.Close(); a.Close() })
	if m := a.memories[testChar]; m == nil || !m.Writable() || m.SummarizeEnabled() {
		t.Fatalf("默认配置该建库并只开 remember：%+v", m)
	}
	ctx := context.Background()
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(ctx, key, "我喜欢喝乌龙茶，记一下", nil); err != nil {
		t.Fatal(err)
	}
	first := lastToolMessage(t, bodyAt(t, e, 1))
	if !strings.Contains(first, "记住了") {
		t.Fatalf("remember 该成功，工具结果：%q", first)
	}
	if strings.Contains(first, "没有记忆库") || strings.Contains(first, "只读") {
		t.Fatalf("remember 不该报没有库或只读：%q", first)
	}

	// 关掉程序，用同一个数据目录重开
	d.Close()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = e.open()
	d = a.Desktop()
	t.Cleanup(func() { d.Close(); a.Close() })
	if err := d.Reader.SendLocal(ctx, key, "我喜欢喝什么？", nil); err != nil {
		t.Fatal(err)
	}
	second := lastToolMessage(t, bodyAt(t, e, 3))
	if !strings.Contains(second, "主人喜欢喝乌龙茶") {
		t.Fatalf("重启后 memory_search 该找到乌龙茶，工具结果：%q", second)
	}
}
