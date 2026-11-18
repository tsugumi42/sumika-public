package openaichat

import (
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/chat"
)

func TestReplayRoundTrip(t *testing.T) {
	details := []jsjson.Value{
		{Kind: jsjson.Object, Keys: []string{"type", "text", "signature"}, Obj: map[string]jsjson.Value{
			"type":      {Kind: jsjson.String, Str: "reasoning.text"},
			"text":      {Kind: jsjson.String, Str: "想 <&> 😀"},
			"signature": {Kind: jsjson.String, Str: "sig-1"},
		}},
	}
	replay := ReplayFrom("reasoning_content", details)
	if replay.Codec != Codec {
		t.Fatalf("Codec = %q，应为 %q", replay.Codec, Codec)
	}
	field, got := ReplayParts(replay)
	if field != "reasoning_content" {
		t.Fatalf("field = %q", field)
	}
	if len(got) != 1 || jsjson.Stringify(got[0]) != jsjson.Stringify(details[0]) {
		t.Fatalf("details = %s，应为 %s", jsjson.Stringify(jsjson.Value{Kind: jsjson.Array, Arr: got}), jsjson.Stringify(jsjson.Value{Kind: jsjson.Array, Arr: details}))
	}
	if field, details := ReplayParts(chat.Replay{Codec: "other", Data: replay.Data}); field != "" || len(details) != 0 {
		t.Fatal("别的编解码器的回放不该被解析")
	}
}

func TestReplayPartsEmpty(t *testing.T) {
	field, details := ReplayParts(ReplayFrom("", nil))
	if field != "" || len(details) != 0 {
		t.Fatalf("空回放解析出 %q / %d 条", field, len(details))
	}
}

func TestStripThinkingPreservesOtherBlocks(t *testing.T) {
	call := chat.ToolCall{ID: "call-1", Name: "lookup", Arguments: jsjson.Value{Kind: jsjson.Object, Obj: map[string]jsjson.Value{}}}
	req := chat.Request{Messages: []chat.Message{
		{Role: chat.RoleUser, Blocks: []chat.Block{chat.Text("hello"), chat.Image{DataURL: "data:image/png;base64,abc"}}},
		{Role: chat.RoleAssistant, Blocks: []chat.Block{
			chat.Thinking{Text: "secret", Replay: ReplayFrom("reasoning_content", []jsjson.Value{{Kind: jsjson.Object, Obj: map[string]jsjson.Value{"signature": {Kind: jsjson.String, Str: "secret"}}}})},
			call,
		}},
		{Role: chat.RoleTool, Blocks: []chat.Block{chat.ToolResult{CallID: "call-1", Text: "result", IsError: true, Images: []chat.Image{{DataURL: "data:image/png;base64,def"}}}}},
	}}
	got := chat.StripThinking(req)
	first := got.Messages[0].Blocks
	if len(first) != 2 || first[0] != chat.Text("hello") || first[1] != (chat.Image{DataURL: "data:image/png;base64,abc"}) {
		t.Fatalf("user 块被改：%#v", first)
	}
	second := got.Messages[1].Blocks
	if len(second) != 1 {
		t.Fatalf("工具调用那步剩 %d 块：%#v", len(second), second)
	}
	gotCall, ok := second[0].(chat.ToolCall)
	if !ok || gotCall.ID != call.ID || gotCall.Name != call.Name || jsjson.Stringify(gotCall.Arguments) != jsjson.Stringify(call.Arguments) {
		t.Fatalf("工具调用被改：%#v", second)
	}
	if len(got.Messages[2].Blocks) != 1 {
		t.Fatalf("工具结果消息剩 %d 块", len(got.Messages[2].Blocks))
	}
	third, ok := got.Messages[2].Blocks[0].(chat.ToolResult)
	want := chat.ToolResult{CallID: "call-1", Text: "result", IsError: true, Images: []chat.Image{{DataURL: "data:image/png;base64,def"}}}
	if !ok || third.CallID != want.CallID || third.Text != want.Text || third.IsError != want.IsError || len(third.Images) != 1 || third.Images[0] != want.Images[0] {
		t.Fatalf("工具结果被改：%#v", got.Messages[2].Blocks[0])
	}
}

func TestStripThinking(t *testing.T) {
	req := chat.Request{Messages: []chat.Message{
		{Role: chat.RoleAssistant, Blocks: []chat.Block{
			chat.Text("正文"),
			chat.Thinking{Text: "思考", Replay: ReplayFrom("reasoning_content", nil)},
			chat.ToolCall{ID: "call-1", Name: "read"},
		}},
		{Role: chat.RoleUser, Blocks: []chat.Block{chat.Text("问")}},
	}}
	got := chat.StripThinking(req)
	if len(got.Messages[0].Blocks) != 2 {
		t.Fatalf("剥完还剩 %d 块，应为 2", len(got.Messages[0].Blocks))
	}
	for _, block := range got.Messages[0].Blocks {
		if _, thinking := block.(chat.Thinking); thinking {
			t.Fatal("Thinking 块没剥掉")
		}
	}
	if len(req.Messages[0].Blocks) != 3 {
		t.Fatal("StripThinking 改了原请求")
	}
}
