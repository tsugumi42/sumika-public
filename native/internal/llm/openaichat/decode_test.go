package openaichat

import (
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/chat"
)

// sseBody 把几段 data 拼成一条流；末尾照真实服务商加 [DONE]
func sseBody(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: ")
		b.WriteString(c)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func decodeOne(t *testing.T, chunks ...string) Output {
	t.Helper()
	out, err := Decode(strings.NewReader(sseBody(chunks...)), nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// 纯文字：几块正文接起来，最后一块给 finish_reason
func TestDecodeText(t *testing.T) {
	out := decodeOne(t,
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"你"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"好～"},"finish_reason":"stop"}]}`)
	if out.Result.StopReason != chat.StopReasonStop {
		t.Fatalf("结束原因 = %q", out.Result.StopReason)
	}
	if len(out.Result.Blocks) != 1 {
		t.Fatalf("块 = %#v", out.Result.Blocks)
	}
	if text, ok := out.Result.Blocks[0].(chat.Text); !ok || string(text) != "你好～" {
		t.Fatalf("正文 = %#v", out.Result.Blocks[0])
	}
	if len(out.ToolCalls) != 0 {
		t.Fatalf("不该有工具调用：%+v", out.ToolCalls)
	}
}

// 工具调用分成几块：id、名字只补一次，参数接起来；Result 里是解析好的参数，ToolCalls 留着原文
func TestDecodeToolCallIncrements(t *testing.T) {
	var events []Event
	out, err := Decode(strings.NewReader(sseBody(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"lookup","arguments":"{\"code\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"arguments":" \"ZEBRA\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)), func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.StopReason != chat.StopReasonToolUse {
		t.Fatalf("结束原因 = %q", out.Result.StopReason)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0] != (ToolCall{ID: "call-1", Name: "lookup", Args: `{"code": "ZEBRA"}`}) {
		t.Fatalf("拼好的调用 = %+v", out.ToolCalls)
	}
	var call chat.ToolCall
	for _, block := range out.Result.Blocks {
		if c, ok := block.(chat.ToolCall); ok {
			call = c
		}
	}
	if call.ID != "call-1" || call.Name != "lookup" || jsjson.Stringify(call.Arguments) != `{"code":"ZEBRA"}` {
		t.Fatalf("中立调用 = %+v", call)
	}
	if len(events) != 2 || !events[0].Progress || events[1].ToolCalls[0].Arguments != ` "ZEBRA"}` {
		t.Fatalf("增量事件 = %+v", events)
	}
}

// reasoning_content：思考文字接起来，回放记住是哪个线上字段
func TestDecodeReasoningContent(t *testing.T) {
	out := decodeOne(t,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"想一"}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"想二","content":"答"},"finish_reason":"stop"}]}`)
	if out.Result.StopReason != chat.StopReasonStop {
		t.Fatalf("结束原因 = %q", out.Result.StopReason)
	}
	var thinking chat.Thinking
	for _, block := range out.Result.Blocks {
		if v, ok := block.(chat.Thinking); ok {
			thinking = v
		}
	}
	if thinking.Text != "想一想二" {
		t.Fatalf("思考 = %q", thinking.Text)
	}
	field, details := ReplayParts(thinking.Replay)
	if field != "reasoning_content" || len(details) != 0 {
		t.Fatalf("回放 = %q / %+v", field, details)
	}
}

// reasoning_details：连着的文字合成一条、最后一块只带签名也并进去
func TestDecodeReasoningDetails(t *testing.T) {
	out := decodeOne(t,
		`{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"想","format":"anthropic-claude-v1","index":0}]}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"好了","format":"anthropic-claude-v1","index":0}]}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","signature":"签名","format":"anthropic-claude-v1","index":0}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	_, details := ReplayParts(out.Result.Replay)
	if len(details) != 1 || jsjson.Stringify(details[0]) != `{"type":"reasoning.text","text":"想好了","format":"anthropic-claude-v1","index":0,"signature":"签名"}` {
		t.Fatalf("合并后的结构化思考 = %+v", details)
	}
	var thinking chat.Thinking
	for _, block := range out.Result.Blocks {
		if v, ok := block.(chat.Thinking); ok {
			thinking = v
		}
	}
	if thinking.Text != "" || thinking.Replay.Codec != Codec {
		t.Fatalf("只有结构化思考也要有一块思考：%+v", thinking)
	}
}

// 用量在最后一块来，缓存命中照 prompt_tokens_details / prompt_cache_hit_tokens 依次取
func TestDecodeUsage(t *testing.T) {
	out := decodeOne(t,
		`{"choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_cache_hit_tokens":64,"completion_tokens_details":{"reasoning_tokens":3}}}`)
	want := chat.Usage{Input: 36, Output: 5, CacheRead: 64, Reasoning: 3}
	if out.Result.Usage != want {
		t.Fatalf("用量 = %+v，应为 %+v", out.Result.Usage, want)
	}
}

// length 是截断，不报错
func TestDecodeLength(t *testing.T) {
	out := decodeOne(t,
		`{"choices":[{"index":0,"delta":{"content":"半句"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`)
	if out.Result.StopReason != chat.StopReasonLength {
		t.Fatalf("结束原因 = %q", out.Result.StopReason)
	}
}

// 没给 finish_reason：报 StopError，已经收到的字留着
func TestDecodeMissingFinish(t *testing.T) {
	out, err := Decode(strings.NewReader(sseBody(`{"choices":[{"index":0,"delta":{"content":"半句"}}]}`)), nil)
	var stop *StopError
	if !errors.As(err, &stop) || stop.Reason != "" {
		t.Fatalf("应该报没给结束原因：%v", err)
	}
	if len(out.Result.Blocks) != 1 || string(out.Result.Blocks[0].(chat.Text)) != "半句" {
		t.Fatalf("收到的字留着：%#v", out.Result.Blocks)
	}
}
