package llm

// 线上字节对拍（规格-模型对话编解码 设计第 1 条）：故障切换后剥掉思考的那份请求体。
// P2-6 第 2b 步起在中立消息上删 Thinking 块再编码，不再把 MessagesJSON 解析成 map 按字母重排。
// 重录：先在 testdata/wire/intended.md 登记一行，再 WIRE_UPDATE=1 跑。

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/llm/llmtest/wire"
	"sumika-native/internal/llm/openaichat"
	"sumika-native/internal/tools"
)

func TestWireStripModelReasoning(t *testing.T) {
	t.Parallel()
	args, err := jsjson.Parse(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := jsjson.Parse(`{"type":"reasoning.text","text":"结构化思考","signature":"sig-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	req := chat.Request{
		Model: "test/vision",
		Messages: []chat.Message{
			{Role: chat.RoleSystem, Blocks: []chat.Block{chat.Text("sys <tag> & more")}},
			{Role: chat.RoleUser, Blocks: []chat.Block{chat.Text("问")}},
			{Role: chat.RoleAssistant, Blocks: []chat.Block{
				chat.Text("答"),
				chat.Thinking{Text: "原思考", Replay: openaichat.ReplayFrom("reasoning_content", []jsjson.Value{detail})},
				chat.ToolCall{ID: "call-1", Name: "weather", Arguments: args},
			}},
			{Role: chat.RoleTool, Blocks: []chat.Block{chat.ToolResult{CallID: "call-1", Text: "结果 <b>"}}},
		},
		Tools:           []tools.Declaration{{Name: "weather", Description: "查天气", Parameters: `{"type":"object","properties":{}}`}},
		CacheKey:        "wire:cache",
		Thinking:        "low",
		MaxOutputTokens: 800,
		LongCache:       true,
		NoStore:         true,
		Style:           chat.StyleForeground,
	}
	stripped := chat.StripThinking(req)
	body, _, _, err := openaichat.EncodeRequest(stripped)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"reasoning_content"`, `"reasoning"`, `"reasoning_text"`, `"reasoning_details"`, `"signature"`} {
		if strings.Contains(string(body), field) {
			t.Fatalf("剥思考以后不该还有 %s：%s", field, body)
		}
	}
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Reply: llmtest.Text("好的")}, &bodies, &mu))
	defer srv.Close()
	client := New(Magpie(srv.URL + "/v1"))
	if _, err := client.StreamChat(context.Background(), stripped, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("应该发一次请求，发了 %d 次", len(bodies))
	}
	wire.Check(t, "testdata/wire", "strip-reasoning", []byte(bodies[0]))
}
